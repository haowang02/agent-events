package server

import (
	"cmp"
	"errors"
	"math"
	"net/http"
	"slices"
	"time"
)

type Usage struct {
	Requests     int     `json:"requests"`
	Failed       int     `json:"failed"`
	Input        int64   `json:"input_tokens"`
	CacheRead    int64   `json:"cache_read_tokens"`
	CacheWrite   int64   `json:"cache_write_tokens"`
	Output       int64   `json:"output_tokens"`
	Reasoning    int64   `json:"reasoning_tokens"`
	Total        int64   `json:"total_tokens"`
	Cost         float64 `json:"cost"`
	CostInput    float64 `json:"cost_input"`
	CostRead     float64 `json:"cost_cache_read"`
	CostWrite    float64 `json:"cost_cache_write"`
	CostOutput   float64 `json:"cost_output"`
	Unpriced     int     `json:"unpriced"` // Requests with tokens but no price.
	latencySum   int64
	latencyCount int
}

func (u *Usage) add(view *EventView) {
	u.Requests++
	if view.Failed {
		u.Failed++
	}
	u.Input += view.InputTokens
	u.CacheRead += view.CacheReadTokens
	u.CacheWrite += view.CacheWriteTokens
	u.Output += view.OutputTokens
	u.Reasoning += view.ReasoningTokens
	u.Total += view.TotalTokens
	if view.Cost != nil {
		u.Cost += view.Cost.Total
		u.CostInput += view.Cost.Input
		u.CostRead += view.Cost.CacheRead
		u.CostWrite += view.Cost.CacheWrite
		u.CostOutput += view.Cost.Output
	} else if view.TotalTokens > 0 {
		u.Unpriced++
	}
	if view.LatencyMS > 0 && !view.Failed {
		u.latencySum += view.LatencyMS
		u.latencyCount++
	}
}

func (u Usage) cacheRate() float64 {
	if prompt := u.Input + u.CacheRead + u.CacheWrite; prompt > 0 {
		return float64(u.CacheRead) * 100 / float64(prompt)
	}
	return 0
}

type Summary struct {
	Usage
	SuccessRate float64 `json:"success_rate"`
	CacheRate   float64 `json:"cache_rate"`
	AvgLatency  float64 `json:"avg_latency_ms"`
}

type TrendPoint struct {
	Time int64 `json:"time"`
	Usage
	CacheRate float64 `json:"cache_rate"`
}

type DistributionRow struct {
	Key string `json:"key"`
	Usage
	Speed GroupSpeed `json:"speed"`
}

// GroupSpeed holds medians over a group's timed successful requests.
type GroupSpeed struct {
	Samples    int     `json:"samples"`
	TPS        float64 `json:"tps"`
	TTFT       float64 `json:"ttft_ms"`
	Latency    float64 `json:"latency_ms"`
	LatencyP95 float64 `json:"latency_p95_ms"`
}

type speedSamples struct{ tps, ttft, latency []float64 }

func (s *speedSamples) add(view *EventView) {
	if view.Failed || view.Scope != "" || view.LatencyMS <= 0 {
		return
	}
	s.latency = append(s.latency, float64(view.LatencyMS))
	if view.TTFTMS > 0 {
		s.ttft = append(s.ttft, float64(view.TTFTMS))
	}
	if view.TPS != nil {
		s.tps = append(s.tps, *view.TPS)
	}
}

func (s *speedSamples) speed() GroupSpeed {
	for _, values := range [][]float64{s.tps, s.ttft, s.latency} {
		slices.Sort(values)
	}
	return GroupSpeed{
		Samples: len(s.latency), TPS: percentile(s.tps, 0.5), TTFT: percentile(s.ttft, 0.5),
		Latency: percentile(s.latency, 0.5), LatencyP95: percentile(s.latency, 0.95),
	}
}

// TrendSeries is one entity's share of the trend. The entities past the
// first trendSeriesLimit share one series whose Others counts them.
type TrendSeries struct {
	Key      string    `json:"key"`
	Others   int       `json:"others,omitempty"`
	Requests []int     `json:"requests"`
	Failed   []int     `json:"failed"`
	Tokens   []int64   `json:"total_tokens"`
	Cost     []float64 `json:"cost"`
}

// trendSeriesLimit matches the colored entries of the distribution; agents
// keep a series each.
const trendSeriesLimit = 6

// SpeedStats are percentiles of output tokens per second.
type SpeedStats struct {
	Time    int64   `json:"time,omitempty"`
	Samples int     `json:"samples"`
	P10     float64 `json:"p10"`
	P25     float64 `json:"p25"`
	P50     float64 `json:"p50"`
	P75     float64 `json:"p75"`
	P90     float64 `json:"p90"`
}

type Analysis struct {
	Bucket       string                       `json:"bucket"` // "hour" or "day"
	Summary      Summary                      `json:"summary"`
	Trend        []TrendPoint                 `json:"trend"`
	Distribution map[string][]DistributionRow `json:"distribution"`
	TrendBy      string                       `json:"trend_by,omitempty"`
	TrendSeries  []TrendSeries                `json:"trend_series,omitempty"`
	SpeedModels  []Option                     `json:"speed_models"`
	SpeedModel   string                       `json:"speed_model"`
	Speed        SpeedStats                   `json:"speed"`
	SpeedTrend   []SpeedStats                 `json:"speed_trend"`
}

// timeBuckets split a time range into local hours or days. Hours are the
// default up to three days and available up to a month.
type timeBuckets struct {
	hourly   bool
	location *time.Location
	starts   []int64
	index    map[int64]int
}

func newTimeBuckets(from, to int64, location *time.Location, bucket string) *timeBuckets {
	day := 24 * time.Hour.Milliseconds()
	hourly := to-from <= 3*day
	switch bucket {
	case "hour":
		hourly = to-from <= 31*day
	case "day":
		hourly = false
	}
	b := &timeBuckets{hourly: hourly, location: location, index: map[int64]int{}}
	for at := b.start(from); at < to && len(b.starts) < 2000; at = b.next(at) {
		b.index[at] = len(b.starts)
		b.starts = append(b.starts, at)
	}
	return b
}

func (b *timeBuckets) start(ms int64) int64 {
	at := time.UnixMilli(ms).In(b.location)
	hour := 0
	if b.hourly {
		hour = at.Hour()
	}
	return time.Date(at.Year(), at.Month(), at.Day(), hour, 0, 0, 0, b.location).UnixMilli()
}

func (b *timeBuckets) next(start int64) int64 {
	at := time.UnixMilli(start).In(b.location)
	if b.hourly {
		return at.Add(time.Hour).UnixMilli()
	}
	return time.Date(at.Year(), at.Month(), at.Day()+1, 0, 0, 0, 0, b.location).UnixMilli()
}

func (b *timeBuckets) find(ms int64) (int, bool) {
	index, ok := b.index[b.start(ms)]
	return index, ok
}

func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	position := p * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := min(lower+1, len(sorted)-1)
	return sorted[lower] + (sorted[upper]-sorted[lower])*(position-float64(lower))
}

func speedStats(at int64, values []float64) SpeedStats {
	slices.Sort(values)
	return SpeedStats{
		Time: at, Samples: len(values),
		P10: percentile(values, 0.1), P25: percentile(values, 0.25), P50: percentile(values, 0.5),
		P75: percentile(values, 0.75), P90: percentile(values, 0.9),
	}
}

var distributionKeys = map[string]func(*EventView) string{
	"models":    func(v *EventView) string { return v.Model },
	"agents":    func(v *EventView) string { return v.Agent },
	"projects":  func(v *EventView) string { return v.Project },
	"providers": func(v *EventView) string { return v.Provider },
}

// AnalysisQuery selects what Analysis returns besides the summary.
type AnalysisQuery struct {
	Filter
	Location   *time.Location
	Bucket     string // "hour", "day" or "" to choose by range.
	TrendBy    string // A distribution dimension to split the trend by, or "".
	SpeedModel string
}

// Analysis summarizes the matching events. Speed covers one model, the
// requested one or else the one with most timed requests, since different
// models' speeds are not comparable.
func (s *Server) Analysis(query AnalysisQuery) Analysis {
	filter, location, speedModel := query.Filter, query.Location, query.SpeedModel
	s.mu.RLock()
	var views []EventView
	for i := range s.events {
		if e := &s.events[i]; filter.match(e) {
			views = append(views, s.view(e))
		}
	}
	s.mu.RUnlock()

	from, to := filter.From, filter.To
	if to == 0 {
		to = time.Now().UnixMilli()
	}
	if from == 0 {
		from = to - 24*time.Hour.Milliseconds()
		if len(views) > 0 {
			from = views[len(views)-1].Time
		}
	}
	buckets := newTimeBuckets(from, to, location, query.Bucket)
	result := Analysis{Bucket: "day", Trend: make([]TrendPoint, len(buckets.starts)), Distribution: map[string][]DistributionRow{}}
	if buckets.hourly {
		result.Bucket = "hour"
	}
	for i, start := range buckets.starts {
		result.Trend[i].Time = start
	}
	groups := map[string]map[string]*Usage{}
	samples := map[string]map[string]*speedSamples{}
	for name := range distributionKeys {
		groups[name] = map[string]*Usage{}
		samples[name] = map[string]*speedSamples{}
	}
	speedCounts := map[string]int{}
	for i := range views {
		view := &views[i]
		result.Summary.add(view)
		if index, ok := buckets.find(view.Time); ok {
			result.Trend[index].add(view)
		}
		for name, key := range distributionKeys {
			usage := groups[name][key(view)]
			if usage == nil {
				usage = &Usage{}
				groups[name][key(view)] = usage
				samples[name][key(view)] = &speedSamples{}
			}
			usage.add(view)
			samples[name][key(view)].add(view)
		}
		if view.TPS != nil {
			speedCounts[view.Model]++
		}
	}

	summary := &result.Summary
	if summary.Requests > 0 {
		summary.SuccessRate = float64(summary.Requests-summary.Failed) * 100 / float64(summary.Requests)
	}
	if summary.latencyCount > 0 {
		summary.AvgLatency = float64(summary.latencySum) / float64(summary.latencyCount)
	}
	summary.CacheRate = summary.cacheRate()
	for i := range result.Trend {
		result.Trend[i].CacheRate = result.Trend[i].cacheRate()
	}
	for name, usages := range groups {
		rows := make([]DistributionRow, 0, len(usages))
		for key, usage := range usages {
			rows = append(rows, DistributionRow{Key: key, Usage: *usage, Speed: samples[name][key].speed()})
		}
		slices.SortFunc(rows, func(x, y DistributionRow) int {
			return cmp.Or(cmp.Compare(y.Total, x.Total), cmp.Compare(x.Key, y.Key))
		})
		result.Distribution[name] = rows
	}
	if key := distributionKeys[query.TrendBy]; key != nil {
		result.TrendBy = query.TrendBy
		result.TrendSeries = trendSeries(views, buckets, key, result.Distribution[query.TrendBy], query.TrendBy == "agents")
	}

	result.SpeedModels = countOptions(speedCounts)
	if speedCounts[speedModel] == 0 && len(result.SpeedModels) > 0 {
		speedModel = result.SpeedModels[0].Value
	}
	result.SpeedModel = speedModel
	var all []float64
	byBucket := make([][]float64, len(buckets.starts))
	for i := range views {
		view := &views[i]
		if view.Model != speedModel || view.TPS == nil {
			continue
		}
		all = append(all, *view.TPS)
		if index, ok := buckets.find(view.Time); ok {
			byBucket[index] = append(byBucket[index], *view.TPS)
		}
	}
	result.Speed = speedStats(0, all)
	result.SpeedTrend = make([]SpeedStats, len(buckets.starts))
	for i, values := range byBucket {
		result.SpeedTrend[i] = speedStats(buckets.starts[i], values)
	}
	return result
}

// trendSeries splits the trend by entity, in the order of rows.
func trendSeries(views []EventView, buckets *timeBuckets, key func(*EventView) string, rows []DistributionRow, all bool) []TrendSeries {
	n := len(buckets.starts)
	newSeries := func(key string) TrendSeries {
		return TrendSeries{Key: key, Requests: make([]int, n), Failed: make([]int, n), Tokens: make([]int64, n), Cost: make([]float64, n)}
	}
	limit := len(rows)
	if !all && limit > trendSeriesLimit+1 {
		limit = trendSeriesLimit
	}
	series := make([]TrendSeries, 0, limit+1)
	index := map[string]int{}
	for _, row := range rows[:limit] {
		index[row.Key] = len(series)
		series = append(series, newSeries(row.Key))
	}
	if limit < len(rows) {
		other := newSeries("")
		other.Others = len(rows) - limit
		series = append(series, other)
	}
	for i := range views {
		view := &views[i]
		bucket, ok := buckets.find(view.Time)
		if !ok {
			continue
		}
		target, ok := index[key(view)]
		if !ok {
			target = len(series) - 1
		}
		item := &series[target]
		item.Requests[bucket]++
		if view.Failed {
			item.Failed[bucket]++
		}
		item.Tokens[bucket] += view.TotalTokens
		if view.Cost != nil {
			item.Cost[bucket] += view.Cost.Total
		}
	}
	return series
}

// Breakdown splits the matching events of one entity of a dimension by
// another dimension, largest cost first.
func (s *Server) Breakdown(filter Filter, dimension, key, by string) []DistributionRow {
	of, split := distributionKeys[dimension], distributionKeys[by]
	if of == nil || split == nil {
		return nil
	}
	usages, samples := map[string]*Usage{}, map[string]*speedSamples{}
	s.mu.RLock()
	for i := range s.events {
		e := &s.events[i]
		if !filter.match(e) {
			continue
		}
		view := s.view(e)
		if of(&view) != key {
			continue
		}
		group := split(&view)
		if usages[group] == nil {
			usages[group], samples[group] = &Usage{}, &speedSamples{}
		}
		usages[group].add(&view)
		samples[group].add(&view)
	}
	s.mu.RUnlock()
	rows := make([]DistributionRow, 0, len(usages))
	for group, usage := range usages {
		rows = append(rows, DistributionRow{Key: group, Usage: *usage, Speed: samples[group].speed()})
	}
	slices.SortFunc(rows, func(x, y DistributionRow) int {
		return cmp.Or(cmp.Compare(y.Cost, x.Cost), cmp.Compare(y.Total, x.Total), cmp.Compare(x.Key, y.Key))
	})
	return rows
}

func (s *Server) handleBreakdown(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	rows := s.Breakdown(parseFilter(query), query.Get("dimension"), query.Get("key"), query.Get("by"))
	if rows == nil {
		writeError(w, http.StatusBadRequest, errors.New("unknown dimension"))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

func (s *Server) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	writeJSON(w, http.StatusOK, s.Analysis(AnalysisQuery{
		Filter: parseFilter(query), Location: queryLocation(query), Bucket: query.Get("bucket"),
		TrendBy: query.Get("trend_by"), SpeedModel: query.Get("speed_model"),
	}))
}
