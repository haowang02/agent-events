package main

import (
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeLines(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestParseClaudeFileMergesBlocksAndTimesRequests(t *testing.T) {
	path := writeLines(t,
		`{"type":"api-request","id":"r1","timestamp":"2026-10-26T05:00:00.000Z","querySource":"repl_main_thread","params":{"model":"claude-x"}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:01.000Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","effort":"high","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":10,"cache_creation":{"ephemeral_1h_input_tokens":30}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:03.500Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","message":{"id":"m1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":50,"output_tokens_details":{"thinking_tokens":20},"speed":"fast","cache_creation":{"ephemeral_1h_input_tokens":30}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:04.000Z","message":{"id":"m2","model":"<synthetic>","usage":{"output_tokens":0}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:05.000Z","requestId":"q3","requestedModel":"claude-x","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"id":"m3","model":"<synthetic>","content":[{"type":"text","text":"limit reached"}],"usage":{"output_tokens":0}}}`,
		`{"type":"assistant", truncated line`,
	)
	events, err := parseClaudeFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	ok := events[0]
	if ok.ID != "m1:q1" || ok.Time != parseTimestamp("2026-10-26T05:00:00.000Z") || ok.LatencyMS != 3500 {
		t.Errorf("request timing: id=%s time=%d latency=%d", ok.ID, ok.Time, ok.LatencyMS)
	}
	if ok.OutputTokens != 50 || ok.ReasoningTokens != 20 || ok.CacheWrite1hTokens != 30 || ok.Tier != "fast" || ok.Kind != "repl_main_thread" || ok.Stop != "tool_use" {
		t.Errorf("merged usage: %+v", ok)
	}
	failed := events[1]
	if !failed.Failed || failed.Model != "claude-x" || !strings.Contains(failed.Error, "HTTP 429") || !strings.Contains(failed.Error, "limit reached") {
		t.Errorf("failed request: %+v", failed)
	}
}

func TestParseCodexFileTimesRequestsFromBoundaries(t *testing.T) {
	path := writeLines(t,
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"session_meta","payload":{"id":"thread","cwd":"/p","cli_version":"1.0","originator":"codex-tui","model_provider":"openai","source":"cli"}}`,
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-10-26T05:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-x","effort":"high"}}`,
		`{"timestamp":"2026-10-26T05:00:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage"},"completed_at_ms":0}}`,
		`{"timestamp":"2026-10-26T05:00:02.500Z","type":"response_item","payload":{"type":"message","content":"{\"type\":\"token_usage_record\"}"}}`,
		`{"timestamp":"2026-10-26T05:00:10.000Z","type":"token_usage_record","payload":{"turn_id":"t1","response_id":"resp1","usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":20,"reasoning_output_tokens":5}}}`,
		`{"timestamp":"2026-10-26T05:00:12.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"total_tokens":120},"last_token_usage":{"total_tokens":120}}}}`,
		`{"timestamp":"2026-10-26T05:00:20.000Z","type":"token_usage_record","payload":{"turn_id":"t1","response_id":"resp2","usage":{"input_tokens":130,"cached_input_tokens":100,"output_tokens":4}}}`,
		`{"timestamp":"2026-10-26T05:00:20.100Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","time_to_first_token_ms":5000}}`,
	)
	events, err := parseCodexFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2: %+v", len(events), events)
	}
	start := parseTimestamp("2026-10-26T05:00:00.000Z")
	first, second := events[0], events[1]
	if first.Time != start+2000 || first.LatencyMS != 8000 || first.TTFTMS != 3000 {
		t.Errorf("first request: time=%d latency=%d ttft=%d", first.Time-start, first.LatencyMS, first.TTFTMS)
	}
	if first.InputTokens != 40 || first.CacheReadTokens != 60 || first.OutputTokens != 20 || first.Model != "gpt-x" || first.Effort != "high" {
		t.Errorf("first usage: %+v", first)
	}
	if second.Time != start+12000 || second.LatencyMS != 8000 || second.TTFTMS != 0 {
		t.Errorf("second request: time=%d latency=%d ttft=%d", second.Time-start, second.LatencyMS, second.TTFTMS)
	}
}

func ptr(value float64) *float64 { return &value }

func TestPriceCost(t *testing.T) {
	price := Price{Rates: Rates{
		Input: 2, Output: 10, CacheRead: ptr(0.2),
		LongContext: &LongContext{Threshold: 1000, Input: 4, Output: 15},
	}}
	event := Event{InputTokens: 100, CacheReadTokens: 200, CacheWriteTokens: 300, CacheWrite1hTokens: 100, OutputTokens: 50}
	cost := price.Cost(&event)
	// Cache writes default to the input rate; 1h writes to twice the input rate.
	want := (100*2 + 200*0.2 + 200*2 + 100*4 + 50*10) / 1e6
	if cost.LongContext || math.Abs(cost.Total-want) > 1e-12 {
		t.Errorf("standard cost = %+v, want total %v", cost, want)
	}
	event.CacheReadTokens = 2000
	cost = price.Cost(&event)
	// The long-context tier has no cache read rate, so it falls back to the tier's input rate.
	want = (100*4 + 2000*4 + 200*4 + 100*8 + 50*15) / 1e6
	if !cost.LongContext || math.Abs(cost.Total-want) > 1e-12 {
		t.Errorf("long-context cost = %+v, want total %v", cost, want)
	}
}

func TestResolvePrice(t *testing.T) {
	c, err := parseCatalog([]byte(`{
		"models": {"anthropic/claude-x": {}, "deepseek/deepseek-v9-flash-1017": {}, "google/gemini-4-argon": {}},
		"providers": {
			"anthropic": {"models": {"claude-x": {"canonical_model_id": "anthropic/claude-x", "cost": {"input": 3, "output": 15}}}},
			"deepseek": {"models": {"deepseek-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0.15, "output": 0.6}}}},
			"google": {"models": {"gemini-4-argon": {"cost": {"input": 2, "output": 10}}}},
			"reseller": {"models": {
				"claude-x": {"canonical_model_id": "anthropic/claude-x", "cost": {"input": 1, "output": 5}},
				"free-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0, "output": 0}},
				"open-y": {"cost": {"input": 1, "output": 2}}
			}},
			"other": {"models": {"open-y": {"id": "vendor/open-y", "cost": {"input": 1, "output": 3}}}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p := &Pricer{custom: map[string]Rates{}, resolved: map[string]*Price{}}
	p.setCatalog(c, time.Now())
	for model, want := range map[string]string{
		"Proxy/Claude-X":         "anthropic/claude-x",      // The creator's listing beats a reseller's.
		"DeepSeek-V9-Flash-1017": "deepseek/deepseek-flash", // The creator lists the canonical model under another ID.
		"free-flash":             "reseller/free-flash",     // A reseller's own model keeps its price.
		"gemini-4-argon-trial":   "google/gemini-4-argon",
		"open-y":                 "", // Resellers disagree.
	} {
		price := p.Resolve(model)
		got := ""
		if price != nil {
			got = price.Provider + "/" + price.ModelID
		}
		if got != want {
			t.Errorf("Resolve(%q) = %q, want %q", model, got, want)
		}
	}

	p.custom["gemini-4-argon"] = Rates{Input: 9, Output: 9}
	p.resolved = map[string]*Price{}
	if price := p.Resolve("gemini-4-argon-trial"); price == nil || price.Source != "custom" || price.As != "gemini-4-argon" || price.Input != 9 {
		t.Errorf("alias does not follow the custom price of its target: %+v", price)
	}
}
