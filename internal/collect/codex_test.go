package collect

import (
	"os"
	"testing"

	"github.com/klauspost/compress/zstd"
)

// compress replaces a file with its Zstandard-compressed .zst copy.
func compress(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder, _ := zstd.NewWriter(nil)
	if err := os.WriteFile(path+".zst", encoder.EncodeAll(raw, nil), 0o644); err != nil {
		t.Fatal(err)
	}
	return path + ".zst"
}

func TestParseCodexRecords(t *testing.T) {
	path := compress(t, writeFile(t, "rollout-1.jsonl",
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"session_meta","payload":{"id":"thread","cwd":"/p","cli_version":"1.0","originator":"codex-tui","model_provider":"openai","source":"cli"}}`,
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"priority"}}}`,
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"event_msg","payload":{"type":"task_started","turn_id":"t1"}}`,
		`{"timestamp":"2026-10-26T05:00:01.000Z","type":"turn_context","payload":{"turn_id":"t1","model":"gpt-x","effort":"high"}}`,
		`{"timestamp":"2026-10-26T05:00:02.000Z","type":"event_msg","payload":{"type":"item_completed","item":{"type":"UserMessage"},"completed_at_ms":0}}`,
		`{"timestamp":"2026-10-26T05:00:02.500Z","type":"response_item","payload":{"type":"message","content":"{\"type\":\"token_usage_record\"}"}}`,
		`{"timestamp":"2026-10-26T05:00:10.000Z","type":"token_usage_record","payload":{"turn_id":"t1","response_id":"resp1","usage":{"input_tokens":100,"cached_input_tokens":60,"output_tokens":20,"reasoning_output_tokens":5}}}`,
		`{"timestamp":"2026-10-26T05:00:12.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20},"last_token_usage":{"input_tokens":100,"output_tokens":20}}}}`,
		`{"timestamp":"2026-10-26T05:00:12.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"model":"gpt-x"}}}`,
		`{"timestamp":"2026-10-26T05:00:20.000Z","type":"token_usage_record","payload":{"turn_id":"t1","response_id":"resp2","usage":{"input_tokens":130,"cached_input_tokens":100,"output_tokens":4}}}`,
		`{"timestamp":"2026-10-26T05:00:20.100Z","type":"event_msg","payload":{"type":"task_complete","turn_id":"t1","time_to_first_token_ms":5000}}`,
		`{"timestamp":"2026-10-26T05:01:00.000Z","type":"event_msg","payload":{"type":"thread_settings_applied","thread_settings":{"service_tier":"default"}}}`,
		`{"timestamp":"2026-10-26T05:01:05.000Z","type":"token_usage_record","payload":{"turn_id":"t2","response_id":"resp3","usage":{"input_tokens":10,"output_tokens":1}}}`,
	))
	events, err := parseCodex(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	start := parseTime("2026-10-26T05:00:00.000Z")
	first, second, third := events[0], events[1], events[2]
	if first.Time != start+2000 || first.LatencyMS != 8000 || first.TTFTMS != 3000 {
		t.Errorf("first request: time=%d latency=%d ttft=%d", first.Time-start, first.LatencyMS, first.TTFTMS)
	}
	if first.InputTokens != 40 || first.CacheReadTokens != 60 || first.OutputTokens != 20 || first.Model != "gpt-x" || first.Effort != "high" {
		t.Errorf("first usage: %+v", first)
	}
	if second.Time != start+12000 || second.LatencyMS != 8000 || second.TTFTMS != 0 {
		t.Errorf("second request: time=%d latency=%d ttft=%d", second.Time-start, second.LatencyMS, second.TTFTMS)
	}
	// Settings without a service tier keep it; "default" clears it.
	if first.Tier != "priority" || second.Tier != "priority" || third.Tier != "" {
		t.Errorf("tiers: %q %q %q", first.Tier, second.Tier, third.Tier)
	}
}

// Older rollouts only report usage in token_count events. A fork replays its
// parent's events under the fork's timestamp.
func TestParseCodexTokenCounts(t *testing.T) {
	parent := writeFile(t, "rollout-parent.jsonl",
		`{"timestamp":"2026-05-01T00:00:00.000Z","type":"session_meta","payload":{"id":"parent"}}`,
		`{"timestamp":"2026-05-01T00:00:00.000Z","type":"turn_context","payload":{"model":"gpt-x"}}`,
		`{"timestamp":"2026-05-01T00:00:01.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-01T00:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-01T00:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"cached_input_tokens":120,"output_tokens":15}}}}`,
		`{"timestamp":"2026-05-01T00:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
	)
	child := writeFile(t, "rollout-child.jsonl",
		`{"timestamp":"2026-05-02T00:00:00.000Z","type":"session_meta","payload":{"id":"child"}}`,
		`{"timestamp":"2026-05-02T00:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-02T00:00:09.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"cached_input_tokens":130,"output_tokens":20},"last_token_usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":5}}}}`,
	)
	parentEvents, err := parseCodex(parent)
	if err != nil {
		t.Fatal(err)
	}
	childEvents, err := parseCodex(child)
	if err != nil {
		t.Fatal(err)
	}
	if len(parentEvents) != 2 || len(childEvents) != 2 {
		t.Fatalf("got %d parent and %d child events, want 2 and 2", len(parentEvents), len(childEvents))
	}
	if delta := parentEvents[1]; delta.InputTokens != 50 || delta.CacheReadTokens != 100 || delta.OutputTokens != 5 {
		t.Errorf("usage from cumulative totals: %+v", delta)
	}
	if childEvents[0].ID != parentEvents[0].ID || childEvents[1].ID == parentEvents[1].ID {
		t.Errorf("replayed IDs: parent %s %s, child %s %s", parentEvents[0].ID, parentEvents[1].ID, childEvents[0].ID, childEvents[1].ID)
	}
}
