package server

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/haowang02/agent-events/internal/event"
	"github.com/haowang02/agent-events/internal/pricing"
)

func testServer(t *testing.T, events []event.Event) *Server {
	t.Helper()
	pricer := pricing.NewPricer(filepath.Join(t.TempDir(), "missing.json"), nil)
	return &Server{pricer: pricer, events: events}
}

func TestEventsSnapshot(t *testing.T) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	base := time.Date(2026, 11, 8, 12, 0, 0, 0, zone).UnixMilli()
	// Newest first, as the store returns them; seq 4 started earlier but was saved last.
	srv := testServer(t, []event.Event{
		{Seq: 3, Agent: "codex", ID: "c", Time: base + 3000, Model: "m", OutputTokens: 1},
		{Seq: 2, Agent: "codex", ID: "b", Time: base + 2000, Model: "m", Failed: true},
		{Seq: 4, Agent: "codex", ID: "d", Time: base + 1500, Model: "m"},
		{Seq: 1, Agent: "codex", ID: "a", Time: base + 1000, Model: "n"},
	})
	query := EventsQuery{Filter: Filter{From: base, To: base + time.Hour.Milliseconds()}, Sort: "time", Descending: true, Limit: 2, Location: zone}

	latest := srv.Events(query)
	if latest.Anchor != 4 || latest.Total != 4 || latest.Newer != 0 || latest.Entries[0].ID != "c" {
		t.Fatalf("latest: anchor %d total %d newer %d first %s", latest.Anchor, latest.Total, latest.Newer, latest.Entries[0].ID)
	}

	query.Anchor, query.Offset = 2, 0
	pinned := srv.Events(query)
	if pinned.Total != 2 || pinned.Newer != 2 || pinned.Entries[0].ID != "b" || pinned.StatusCounts["failed"] != 1 {
		t.Fatalf("pinned: total %d newer %d first %s", pinned.Total, pinned.Newer, pinned.Entries[0].ID)
	}
	query.Model = "m"
	if filtered := srv.Events(query); filtered.Total != 1 || filtered.Newer != 2 || len(filtered.Models) != 2 {
		t.Fatalf("filtered: total %d newer %d models %v", filtered.Total, filtered.Newer, filtered.Models)
	}

	if count := srv.Count(Filter{From: base, To: base + 2500, Model: "n", Query: "x"}); count != 3 {
		t.Fatalf("count ignores table filters and covers the range: got %d", count)
	}

	counted := 0
	for i := range latest.Timeline.OK {
		counted += latest.Timeline.OK[i] + latest.Timeline.Failed[i]
	}
	if counted != latest.Total {
		t.Fatalf("timeline counts %d of %d events", counted, latest.Total)
	}
}

func TestTimeline(t *testing.T) {
	zone, _ := time.LoadLocation("Asia/Shanghai")
	from := time.Date(2026, 11, 8, 19, 34, 10, 0, zone).UnixMilli()
	day := newTimeline(from, from+24*time.Hour.Milliseconds(), nil, zone)
	if day.Step != 15*time.Minute.Milliseconds() || day.Start != time.Date(2026, 11, 8, 19, 30, 0, 0, zone).UnixMilli() || len(day.OK) != 97 {
		t.Fatalf("one day: start %d step %d buckets %d", day.Start, day.Step, len(day.OK))
	}

	oldest := from - 3*365*24*time.Hour.Milliseconds()
	all := newTimeline(0, from, []EventView{{Event: &event.Event{Time: from}}, {Event: &event.Event{Time: oldest, Failed: true}}}, zone)
	start := time.UnixMilli(all.Start).In(zone)
	if all.Step%(24*time.Hour.Milliseconds()) != 0 || len(all.OK) > timelineBuckets+1 || start.Hour() != 0 || start.Minute() != 0 {
		t.Fatalf("all time: start %v step %d buckets %d", start, all.Step, len(all.OK))
	}
	if all.Failed[0] != 1 || all.OK[len(all.OK)-1] != 1 {
		t.Fatalf("all time: first and last buckets must hold the oldest and newest events")
	}

	if future := newTimeline(from, from-1000, nil, zone); len(future.OK) != 1 {
		t.Fatalf("an empty span still needs a bucket, got %d", len(future.OK))
	}
}

func TestAnalysisTrend(t *testing.T) {
	zone, _ := time.LoadLocation("UTC")
	base := time.Date(2026, 11, 1, 0, 0, 0, 0, zone).UnixMilli()
	hour := time.Hour.Milliseconds()
	var events []event.Event
	for i, model := range []string{"a", "a", "b", "c", "d", "e", "f", "g", "h"} {
		events = append([]event.Event{{
			Agent: "codex", ID: model + string(rune('0'+i)), Time: base + int64(i)*hour, Model: model,
			InputTokens: int64(100 - i), LatencyMS: 1000, OutputTokens: 10,
		}}, events...)
	}
	srv := testServer(t, events)
	filter := Filter{From: base, To: base + 7*24*hour}

	daily := srv.Analysis(AnalysisQuery{Filter: filter, Location: zone, TrendBy: "models"})
	if daily.Bucket != "day" || len(daily.Trend) != 7 {
		t.Fatalf("seven days are daily by default: %s with %d buckets", daily.Bucket, len(daily.Trend))
	}
	series := daily.TrendSeries
	if len(series) != trendSeriesLimit+1 || series[0].Key != "a" || series[0].Requests[0] != 2 || series[len(series)-1].Others != 2 {
		t.Fatalf("series: %+v", series)
	}
	if speed := daily.Distribution["models"][0].Speed; speed.Samples != 2 || speed.TPS != 10 || speed.Latency != 1000 {
		t.Fatalf("speed: %+v", speed)
	}

	hourly := srv.Analysis(AnalysisQuery{Filter: filter, Location: zone, Bucket: "hour", TrendBy: "agents"})
	if hourly.Bucket != "hour" || len(hourly.Trend) != 7*24 || len(hourly.TrendSeries) != 1 || hourly.TrendSeries[0].Requests[2] != 1 {
		t.Fatalf("hourly: %s with %d buckets and %d series", hourly.Bucket, len(hourly.Trend), len(hourly.TrendSeries))
	}
	filter.From = base - 60*24*hour
	if wide := srv.Analysis(AnalysisQuery{Filter: filter, Location: zone, Bucket: "hour"}); wide.Bucket != "day" {
		t.Fatalf("over a month stays daily, got %s", wide.Bucket)
	}

	rows := srv.Breakdown(Filter{}, "agents", "codex", "models")
	if len(rows) != 8 || rows[0].Requests+rows[1].Requests < 2 {
		t.Fatalf("breakdown: %d rows", len(rows))
	}
}
