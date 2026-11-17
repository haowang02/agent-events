package collect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func TestCodexMissingParentRewrittenBaseline(t *testing.T) {
	var rows []string
	rows = append(rows, `{"timestamp":"2026-11-15T01:00:00Z","type":"session_meta","payload":{"id":"child","forked_from_id":"missing","source":{"subagent":{"thread_spawn":{"parent_thread_id":"missing"}}}}}`)
	usage := func(input, output int) map[string]int {
		return map[string]int{"input_tokens": input, "output_tokens": output}
	}
	count := func(last, total map[string]int) {
		raw, _ := json.Marshal(map[string]any{"timestamp": "2026-11-15T01:00:00Z", "type": "event_msg", "payload": map[string]any{"type": "token_count", "info": map[string]any{"last_token_usage": last, "total_token_usage": total}}})
		rows = append(rows, string(raw))
	}
	count(usage(990000, 10000), usage(990000, 10000))
	rows = append(rows, `{"timestamp":"2026-11-15T01:00:00Z","type":"token_usage_record","payload":{"response_id":"own","usage":{"input_tokens":10000,"output_tokens":100}}}`)
	count(usage(10000, 100), usage(1000000, 10100))
	path := writeFile(t, "rollout-child.jsonl", rows...)
	transcript, _, err := readCodexFamily(path, nil, nil, map[string]bool{})
	events := transcript.result().Events
	if err != nil || len(events) != 1 || events[0].ID != "own" || events[0].InputTokens != 10000 {
		t.Fatalf("rewritten baseline charged or child's own call dropped: %+v, %v", events, err)
	}
}

func TestCodexCumulativeResetKeepsRepeatedCounts(t *testing.T) {
	var rows []string
	rows = append(rows, `{"type":"session_meta","payload":{"id":"s"}}`)
	for i, total := range []int{100, 200, 100} {
		rows = append(rows, fmt.Sprintf(`{"timestamp":"2026-11-15T01:00:0%dZ","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":10},"last_token_usage":{"input_tokens":100,"output_tokens":10}}}}`, i+1, total))
	}
	events, err := eventsOnly(parseCodex(writeFile(t, "rollout-reset.jsonl", rows...)))
	ids := map[string]bool{}
	for _, e := range events {
		ids[e.ID] = true
	}
	if err != nil || len(events) != 3 || len(ids) != 3 {
		t.Fatalf("counter reset merged independent calls: %+v, %v", events, err)
	}
}

func TestCodexCompactionDoesNotEndUserForkReplay(t *testing.T) {
	compaction := []string{
		`{"timestamp":"2026-11-15T01:00:01Z","type":"token_usage_record","payload":{"response_id":"summary","usage":{"input_tokens":9,"output_tokens":1}}}`,
		`{"type":"compacted","payload":{"compaction_response_id":"summary"}}`,
	}
	count := `{"timestamp":"2026-11-15T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50,"output_tokens":5},"total_token_usage":{"input_tokens":50,"output_tokens":5}}}}`
	parent := writeFile(t, "rollout-parent.jsonl", append([]string{`{"type":"session_meta","payload":{"id":"parent"}}`}, append(compaction, count)...)...)
	child := writeFile(t, "rollout-child.jsonl", append([]string{`{"timestamp":"2026-11-15T01:00:10Z","type":"session_meta","payload":{"id":"child","forked_from_id":"parent"}}`}, append(compaction, count)...)...)
	transcript, _, err := readCodexFamily(child, map[string]string{"parent": parent}, nil, map[string]bool{})
	events := transcript.result().Events
	if err != nil || len(events) != 2 || events[1].Session != "parent" {
		t.Fatalf("compaction interrupted copied history: %+v, %v", events, err)
	}
}

func TestCodexSiblingForksKeepIndependentUsage(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	usage := func(at string, total, last int) string {
		return fmt.Sprintf(`{"timestamp":%q,"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":%d,"output_tokens":10},"last_token_usage":{"input_tokens":%d,"output_tokens":5}}}}`, at, total, last)
	}
	for _, session := range []string{"parent", "left", "right", "unrelated"} {
		parent := ""
		if session == "left" || session == "right" {
			parent = `,"forked_from_id":"parent"`
		}
		start := "2026-11-15T01:00:00Z"
		if parent != "" {
			start = "2026-11-15T01:00:10Z"
		}
		raw := fmt.Sprintf(`{"timestamp":%q,"type":"session_meta","payload":{"id":%q%s}}`+"\n", start, session, parent)
		raw += usage("2026-11-15T01:00:01Z", 100, 100) + "\n"
		if parent != "" {
			raw += usage("2026-11-15T01:00:20Z", 150, 50) + "\n"
		}
		if err := os.WriteFile(filepath.Join(root, "rollout-"+session+".jsonl"), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
	}
	c := codex()
	sources, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	unique := map[string]bool{}
	for _, source := range sources {
		events, err := eventsOnly(c.Parse(source.Path))
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range events {
			unique[e.ID] = true
		}
	}
	if len(unique) != 4 {
		t.Fatalf("unrelated roots and two new fork calls must have four identities; got %d: %v", len(unique), unique)
	}
}

func TestCodexInheritedCumulativeBaseline(t *testing.T) {
	parent := writeFile(t, "rollout-parent.jsonl",
		`{"timestamp":"2026-11-15T01:00:00Z","type":"session_meta","payload":{"id":"parent"}}`,
		`{"timestamp":"2026-11-15T01:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":10}}}}`,
		`{"timestamp":"2026-11-15T01:00:02Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"output_tokens":30}}}}`,
	)
	child := writeFile(t, "rollout-child.jsonl",
		`{"timestamp":"2026-11-15T01:00:10Z","type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`,
		`{"timestamp":"2026-11-15T01:00:10Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"output_tokens":30}}}}`,
		`{"timestamp":"2026-11-15T01:00:20Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":350,"output_tokens":35}}}}`,
	)
	transcript, _, err := readCodexFamily(child, map[string]string{"parent": parent}, nil, map[string]bool{})
	events := transcript.result().Events
	if err != nil || len(events) != 1 || events[0].InputTokens != 50 || events[0].OutputTokens != 5 {
		t.Fatalf("inherited total was charged again: %+v, %v", events, err)
	}
}

func TestCodexLateEqualUsageIsNotReplay(t *testing.T) {
	parent := writeFile(t, "rollout-parent.jsonl",
		`{"type":"session_meta","payload":{"id":"parent"}}`,
		`{"timestamp":"2026-11-15T01:00:01Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50,"output_tokens":5}}}}`,
	)
	for _, line := range []string{
		`{"timestamp":"2026-11-15T01:00:20Z","type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":50,"output_tokens":5}}}}`,
		`{"timestamp":"2026-11-15T01:00:10Z","type":"token_usage_record","payload":{"response_id":"new-response","usage":{"input_tokens":50,"output_tokens":5}}}`,
	} {
		child := writeFile(t, "rollout-child.jsonl",
			`{"timestamp":"2026-11-15T01:00:10Z","type":"session_meta","payload":{"id":"child","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent"}}}}}`,
			line,
		)
		transcript, _, err := readCodexFamily(child, map[string]string{"parent": parent}, nil, map[string]bool{})
		events := transcript.result().Events
		if err != nil || len(events) != 1 || events[0].Session != "child" {
			t.Fatalf("child's own request was mistaken for replay: %+v, %v", events, err)
		}
	}
}

func TestCodexCompactionCoincidentalCounts(t *testing.T) {
	path := writeFile(t, "rollout-s.jsonl",
		`{"type":"session_meta","payload":{"id":"s"}}`,
		`{"type":"turn_context","payload":{"model":"normal"}}`,
		`{"timestamp":"2026-11-15T01:00:00Z","type":"token_usage_record","payload":{"response_id":"summary","model":"small","usage":{"input_tokens":100,"cached_input_tokens":80,"output_tokens":20}}}`,
		`{"type":"compacted","payload":{"compaction_response_id":"summary"}}`,
		`{"timestamp":"2026-11-15T01:01:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":20},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":20}}}}`,
	)
	events, err := eventsOnly(parseCodex(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Model != "small" || events[1].Model != "normal" || events[1].CacheReadTokens != 20 {
		t.Fatalf("cache counts distinguish the next normal request from compaction: %+v", events)
	}
}

func TestCodexModelMetadata(t *testing.T) {
	path := writeFile(t, "rollout-s.jsonl",
		`{"type":"session_meta","payload":{"id":"s"}}`,
		`{"type":"turn_context","payload":{"metadata":{"model":"initial"}}}`,
		`{"timestamp":"2026-11-15T01:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"metadata":{"model":"actual"},"total_token_usage":{"input_tokens":100,"output_tokens":10},"last_token_usage":{"input_tokens":100,"output_tokens":10}}}}`,
		`{"timestamp":"2026-11-15T01:01:00Z","type":"event_msg","payload":{"type":"token_count","metadata":{"model":"payload"},"info":{"total_token_usage":{"input_tokens":200,"output_tokens":20},"last_token_usage":{"input_tokens":100,"output_tokens":10}}}}`,
		`{"timestamp":"2026-11-15T01:02:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"output_tokens":25},"last_token_usage":{"input_tokens":50,"output_tokens":5}}}}`,
	)
	events, err := eventsOnly(parseCodex(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Model != "actual" || events[1].Model != "payload" || events[2].Model != "payload" {
		t.Fatalf("model metadata and subsequent unnamed calls: %+v", events)
	}
}

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
		`{"timestamp":"2026-10-26T05:00:00.000Z","type":"session_meta","payload":{"id":"thread","cwd":"/p","cli_version":"1.0","originator":"codex-tui","model_provider":"openai","source":"vscode"}}`,
		`{"type":"session_meta","payload":{"id":"copied-parent","cwd":"/parent"}}`,
		`{"type":"compacted","payload":{}}`,
		`{"type":"token_usage_record","payload":{"usage":{"input_tokens":10,"output_tokens":2}}}`,
		`{"type":"token_usage_record","payload":{"response_id":"zero","usage":{}}}`,

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
	events, err := eventsOnly(parseCodex(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3: %+v", len(events), events)
	}
	start := parseTime("2026-10-26T05:00:00.000Z")
	first, second, third := events[0], events[1], events[2]
	if first.Kind != "cli" || first.Client != "codex-tui" || first.Session != "thread" || first.Project != "/p" {
		t.Errorf("session metadata: kind=%q client=%q", first.Kind, first.Client)
	}
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
		`{"timestamp":"2026-05-01T00:00:00.000Z","type":"session_meta","payload":{"id":"parent","source":"cli"}}`,
		`{"timestamp":"2026-05-01T00:00:00.000Z","type":"turn_context","payload":{"model":"gpt-x"}}`,
		`{"timestamp":"2026-05-01T00:00:01.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-01T00:00:02.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-01T00:00:03.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":250,"cached_input_tokens":120,"output_tokens":15}}}}`,
		`{"timestamp":"2026-05-01T00:00:04.000Z","type":"event_msg","payload":{"type":"token_count","info":null}}`,
	)
	child := writeFile(t, "rollout-child.jsonl",
		`{"timestamp":"2026-05-02T00:00:00.000Z","type":"session_meta","payload":{"id":"child","originator":"codex-tui","source":{"subagent":{"thread_spawn":{"parent_thread_id":"parent","depth":1}}}}}`,
		`{"timestamp":"2026-05-02T00:00:00.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10},"last_token_usage":{"input_tokens":100,"cached_input_tokens":20,"output_tokens":10}}}}`,
		`{"timestamp":"2026-05-02T00:00:09.000Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":300,"cached_input_tokens":130,"output_tokens":20},"last_token_usage":{"input_tokens":50,"cached_input_tokens":10,"output_tokens":5}}}}`,
	)
	parentTranscript, _, err := readCodexFamily(parent, map[string]string{"parent": parent}, nil, map[string]bool{})
	parentEvents := parentTranscript.result().Events
	if err != nil {
		t.Fatal(err)
	}
	childTranscript, _, err := readCodexFamily(child, map[string]string{"parent": parent}, nil, map[string]bool{})
	childEvents := childTranscript.result().Events
	if err != nil {
		t.Fatal(err)
	}
	if len(parentEvents) != 2 || len(childEvents) != 2 {
		t.Fatalf("got %d parent and %d child events, want 2 and 2", len(parentEvents), len(childEvents))
	}
	if parentEvents[0].Kind != "cli" || childEvents[0].Session != "parent" || childEvents[1].Kind != "subagent" {
		t.Errorf("session kinds: parent=%q replay session=%q child=%q", parentEvents[0].Kind, childEvents[0].Session, childEvents[1].Kind)
	}
	if delta := parentEvents[1]; delta.InputTokens != 50 || delta.CacheReadTokens != 100 || delta.OutputTokens != 5 {
		t.Errorf("usage from cumulative totals: %+v", delta)
	}
	if childEvents[0].ID != parentEvents[0].ID || childEvents[1].ID == parentEvents[1].ID {
		t.Errorf("replayed IDs: parent %s %s, child %s %s", parentEvents[0].ID, parentEvents[1].ID, childEvents[0].ID, childEvents[1].ID)
	}
}

func TestCodexNestedForkIdentity(t *testing.T) {
	root := t.TempDir()
	t.Setenv("CODEX_HOME", root)
	lines := map[string]string{
		"a": `{"type":"session_meta","payload":{"id":"a"}}`,
		"b": `{"type":"session_meta","payload":{"id":"b","forked_from_id":"a"}}`,
		"c": `{"type":"session_meta","payload":{"id":"c","forked_from_id":"b"}}`,
	}
	for name, header := range lines {
		path := root + "/rollout-" + name + ".jsonl"
		raw := header + "\n" + `{"timestamp":"2026-11-15T01:00:00Z","type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":100,"output_tokens":20}}}}` + "\n"
		if err := os.WriteFile(path, []byte(raw), 0644); err != nil {
			t.Fatal(err)
		}
	}
	collector := codex()
	sources, err := collector.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("sources: %+v", sources)
	}
	var id string
	for _, source := range sources {
		events, err := eventsOnly(collector.Parse(source.Path))
		if err != nil {
			t.Fatal(err)
		}
		if len(events) != 1 {
			t.Fatalf("events: %+v", events)
		}
		if id != "" && id != events[0].ID {
			t.Fatalf("nested replay identities: %q and %q", id, events[0].ID)
		}
		id = events[0].ID
	}
}
