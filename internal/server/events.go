package server

import (
	"cmp"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

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

func (s *Server) Events(filter Filter, sortKey string, descending bool, offset, limit int) EventsResult {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := EventsResult{StatusCounts: map[string]int{}}
	models, projects := map[string]int{}, map[string]int{}
	views := []EventView{}
	for i := range s.events {
		e := &s.events[i]
		if !filter.scope(e) {
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
	// Events are kept newest first, which is the default order.
	if compare := eventSorts[sortKey]; compare != nil && !(sortKey == "time" && descending) {
		slices.SortStableFunc(views, func(x, y EventView) int {
			if descending {
				return compare(&y, &x)
			}
			return compare(&x, &y)
		})
	}
	result.Total = len(views)
	start := min(max(offset, 0), len(views))
	result.Entries = views[start:min(start+limit, len(views))]
	return result
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sortKey := query.Get("sort")
	if _, ok := eventSorts[sortKey]; !ok {
		sortKey = "time"
	}
	result := s.Events(
		parseFilter(query), sortKey, query.Get("order") != "asc",
		queryInt(r, "offset", 0, 0, 1<<30), queryInt(r, "limit", 50, 1, 500),
	)
	writeJSON(w, http.StatusOK, result)
}
