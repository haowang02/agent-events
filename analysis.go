package main

import (
	"cmp"
	"math"
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
}

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
	SpeedModels  []Option                     `json:"speed_models"`
	SpeedModel   string                       `json:"speed_model"`
	Speed        SpeedStats                   `json:"speed"`
	SpeedTrend   []SpeedStats                 `json:"speed_trend"`
}

// timeBuckets split a time range into local hours, for ranges up to three
// days, or local days.
type timeBuckets struct {
	hourly   bool
	location *time.Location
	starts   []int64
	index    map[int64]int
}

func newTimeBuckets(from, to int64, location *time.Location) *timeBuckets {
	b := &timeBuckets{hourly: to-from <= 3*24*time.Hour.Milliseconds(), location: location, index: map[int64]int{}}
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

// Analysis summarizes the matching events. Speed covers one model, the
// requested one or else the one with most timed requests, since different
// models' speeds are not comparable.
func (a *App) Analysis(filter Filter, location *time.Location, speedModel string) Analysis {
	a.mu.RLock()
	var views []EventView
	for i := range a.events {
		if event := &a.events[i]; filter.match(event) {
			views = append(views, a.view(event))
		}
	}
	a.mu.RUnlock()

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
	buckets := newTimeBuckets(from, to, location)
	result := Analysis{Bucket: "day", Trend: make([]TrendPoint, len(buckets.starts)), Distribution: map[string][]DistributionRow{}}
	if buckets.hourly {
		result.Bucket = "hour"
	}
	for i, start := range buckets.starts {
		result.Trend[i].Time = start
	}
	groups := map[string]map[string]*Usage{}
	for name := range distributionKeys {
		groups[name] = map[string]*Usage{}
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
			}
			usage.add(view)
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
			rows = append(rows, DistributionRow{Key: key, Usage: *usage})
		}
		slices.SortFunc(rows, func(x, y DistributionRow) int {
			return cmp.Or(cmp.Compare(y.Total, x.Total), cmp.Compare(x.Key, y.Key))
		})
		result.Distribution[name] = rows
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
