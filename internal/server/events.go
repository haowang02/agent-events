package server

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/haowang02/agent-events/internal/event"
	"github.com/haowang02/agent-events/internal/pricing"
)

type Filter struct {
	Agent   string
	From    int64 // Unix ms, inclusive; 0 is unbounded.
	To      int64 // Unix ms, exclusive; 0 is unbounded.
	Model   string
	Project string
	Status  string // "", "ok" or "failed"
	Query   string
}

func parseFilter(query url.Values) Filter {
	from, _ := strconv.ParseInt(query.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(query.Get("to"), 10, 64)
	return Filter{
		Agent:   query.Get("agent"),
		From:    from,
		To:      to,
		Model:   query.Get("model"),
		Project: query.Get("project"),
		Status:  query.Get("status"),
		Query:   strings.ToLower(strings.TrimSpace(query.Get("q"))),
	}
}

// scope matches the agent, time range and text search: the selection that
// filter options are counted over.
func (f Filter) scope(e *event.Event) bool {
	if f.Agent != "" && e.Agent != f.Agent {
		return false
	}
	if (f.From > 0 && e.Time < f.From) || (f.To > 0 && e.Time >= f.To) {
		return false
	}
	if f.Query != "" {
		haystack := strings.ToLower(e.Model + "\x00" + e.Session + "\x00" + e.Project + "\x00" + e.ID + "\x00" + e.Error + "\x00" + e.Provider)
		if !strings.Contains(haystack, f.Query) {
			return false
		}
	}
	return true
}

func (f Filter) fields(e *event.Event) bool {
	return (f.Model == "" || e.Model == f.Model) && (f.Project == "" || e.Project == f.Project)
}

func (f Filter) status(e *event.Event) bool {
	switch f.Status {
	case "ok":
		return !e.Failed
	case "failed":
		return e.Failed
	}
	return true
}

func (f Filter) match(e *event.Event) bool { return f.scope(e) && f.fields(e) && f.status(e) }

type Option struct {
	Value string `json:"value"`
	Count int    `json:"count"`
}

func countOptions(counts map[string]int) []Option {
	options := make([]Option, 0, len(counts))
	for value, count := range counts {
		options = append(options, Option{value, count})
	}
	slices.SortFunc(options, func(a, b Option) int { return cmp.Or(cmp.Compare(b.Count, a.Count), cmp.Compare(a.Value, b.Value)) })
	return options
}

type EventView struct {
	*event.Event
	TotalTokens int64          `json:"total_tokens"`
	TPS         *float64       `json:"tps"`
	Price       *pricing.Price `json:"price"`
	Cost        *pricing.Cost  `json:"cost"`
}

func (s *Server) view(e *event.Event) EventView {
	price := s.pricer.Resolve(e.Model, e.Time)
	view := EventView{Event: e, TotalTokens: e.TotalTokens(), Price: price}
	if tps, ok := e.OutputTPS(); ok {
		view.TPS = &tps
	}
	if price != nil {
		cost := price.Cost(e)
		view.Cost = &cost
	}
	return view
}

type EventsResult struct {
	Total        int            `json:"total"`
	Entries      []EventView    `json:"entries"`
	Models       []Option       `json:"models"`
	Projects     []Option       `json:"projects"`
	StatusCounts map[string]int `json:"status_counts"`
	Anchor       int64          `json:"anchor"` // Snapshot bound: the largest Seq included.
	Newer        int            `json:"newer"`  // Matching events saved after the snapshot.
	Timeline     *Timeline      `json:"timeline"`
}

var eventSorts = map[string]func(a, b *EventView) int{
	"time":        func(a, b *EventView) int { return cmp.Compare(a.Time, b.Time) },
	"agent":       func(a, b *EventView) int { return cmp.Compare(a.Agent, b.Agent) },
	"model":       func(a, b *EventView) int { return cmp.Compare(a.Model, b.Model) },
	"project":     func(a, b *EventView) int { return cmp.Compare(a.Project, b.Project) },
	"session":     func(a, b *EventView) int { return cmp.Compare(a.Session, b.Session) },
	"status":      func(a, b *EventView) int { return compareBool(a.Failed, b.Failed) },
	"speed":       func(a, b *EventView) int { return cmp.Compare(optional(a.TPS), optional(b.TPS)) },
	"input":       func(a, b *EventView) int { return cmp.Compare(a.PromptTokens(), b.PromptTokens()) },
	"cache_read":  func(a, b *EventView) int { return cmp.Compare(a.CacheReadTokens, b.CacheReadTokens) },
	"cache_write": func(a, b *EventView) int { return cmp.Compare(a.CacheWriteTokens, b.CacheWriteTokens) },
	"output":      func(a, b *EventView) int { return cmp.Compare(a.OutputTokens, b.OutputTokens) },
	"total":       func(a, b *EventView) int { return cmp.Compare(a.TotalTokens, b.TotalTokens) },
	"cost": func(a, b *EventView) int {
		cost := func(view *EventView) float64 {
			if view.Cost == nil {
				return -1
			}
			return view.Cost.Total
		}
		return cmp.Compare(cost(a), cost(b))
	},
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case a:
		return 1
	}
	return -1
}

// optional orders unknown values first in ascending order.
func optional(value *float64) float64 {
	if value == nil {
		return -1
	}
	return *value
}

// EventsQuery selects a page of events. Anchor fixes the snapshot to events
// saved up to that Seq; 0 takes a new snapshot.
type EventsQuery struct {
	Filter
	Sort       string
	Descending bool
	Offset     int
	Limit      int
	Anchor     int64
	Location   *time.Location
}

// Events returns a page of the snapshot. Events saved after it only count
// toward Newer, so paging and live updates never shift the rows.
func (s *Server) Events(query EventsQuery) EventsResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	filter := query.Filter
	result := EventsResult{StatusCounts: map[string]int{}, Anchor: query.Anchor}
	if result.Anchor <= 0 {
		for i := range s.events {
			result.Anchor = max(result.Anchor, s.events[i].Seq)
		}
	}
	models, projects := map[string]int{}, map[string]int{}
	views := []EventView{}
	for i := range s.events {
		e := &s.events[i]
		if !filter.scope(e) {
			continue
		}
		if e.Seq > result.Anchor {
			if filter.fields(e) && filter.status(e) {
				result.Newer++
			}
			continue
		}
		models[e.Model]++
		projects[e.Project]++
		if !filter.fields(e) {
			continue
		}
		result.StatusCounts["all"]++
		if e.Failed {
			result.StatusCounts["failed"]++
		} else {
			result.StatusCounts["ok"]++
		}
		if filter.status(e) {
			views = append(views, s.view(e))
		}
	}
	result.Models, result.Projects = countOptions(models), countOptions(projects)
	// Events are kept newest first; the timeline needs that order.
	result.Timeline = newTimeline(filter.From, filter.To, views, query.Location)
	if compare := eventSorts[query.Sort]; compare != nil && !(query.Sort == "time" && query.Descending) {
		slices.SortStableFunc(views, func(x, y EventView) int {
			if query.Descending {
				return compare(&y, &x)
			}
			return compare(&x, &y)
		})
	}
	result.Total = len(views)
	start := min(max(query.Offset, 0), len(views))
	result.Entries = views[start:min(start+query.Limit, len(views))]
	return result
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sortKey := query.Get("sort")
	if _, ok := eventSorts[sortKey]; !ok {
		sortKey = "time"
	}
	anchor, _ := strconv.ParseInt(query.Get("anchor"), 10, 64)
	writeJSON(w, http.StatusOK, s.Events(EventsQuery{
		Filter: parseFilter(query), Sort: sortKey, Descending: query.Get("order") != "asc",
		Offset: queryInt(r, "offset", 0, 0, 1<<30), Limit: queryInt(r, "limit", 50, 1, 500),
		Anchor: anchor, Location: queryLocation(query),
	}))
}

// Count returns how many events match the filter's agent and time range.
func (s *Server) Count(filter Filter) int {
	scope := Filter{Agent: filter.Agent, From: filter.From, To: filter.To}
	s.mu.RLock()
	defer s.mu.RUnlock()
	count := 0
	for i := range s.events {
		if scope.scope(&s.events[i]) {
			count++
		}
	}
	return count
}

func (s *Server) handleCount(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]int{"events": s.Count(parseFilter(r.URL.Query()))})
}

// Timeline counts events in equal spans aligned to local time, at most
// timelineBuckets of them. Times are Unix milliseconds.
type Timeline struct {
	Start  int64 `json:"start"`
	Step   int64 `json:"step"`
	OK     []int `json:"ok"`
	Failed []int `json:"failed"`
}

const timelineBuckets = 120

var timelineSteps = func() []int64 {
	minutes := []int64{1, 2, 5, 10, 15, 30, 60, 120, 180, 360, 720, 1440, 2 * 1440, 7 * 1440}
	steps := make([]int64, len(minutes))
	for i, m := range minutes {
		steps[i] = m * time.Minute.Milliseconds()
	}
	return steps
}()

// newTimeline spans the filter's range; without a start it begins at the
// oldest event, and without an end it ends now. views are newest first.
func newTimeline(from, to int64, views []EventView, location *time.Location) *Timeline {
	if to == 0 {
		to = time.Now().UnixMilli()
	}
	if from == 0 {
		from = to
		if len(views) > 0 {
			from = min(views[len(views)-1].Time, to)
		}
	}
	day := 24 * time.Hour.Milliseconds()
	span := max(to-from, 1)
	step := (span + timelineBuckets*day - 1) / (timelineBuckets * day) * day
	for _, candidate := range timelineSteps {
		if (span+candidate-1)/candidate <= timelineBuckets {
			step = candidate
			break
		}
	}
	at := time.UnixMilli(from).In(location)
	start := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, location).UnixMilli()
	if step < day {
		start += (from - start) / step * step
	}
	n := max((to-start+step-1)/step, 1)
	timeline := &Timeline{Start: start, Step: step, OK: make([]int, n), Failed: make([]int, n)}
	for i := range views {
		index := min(max((views[i].Time-start)/step, 0), n-1)
		if views[i].Failed {
			timeline.Failed[index]++
		} else {
			timeline.OK[index]++
		}
	}
	return timeline
}

func queryLocation(query url.Values) *time.Location {
	if location, err := time.LoadLocation(query.Get("tz")); err == nil && query.Get("tz") != "" {
		return location
	}
	return time.Local
}
