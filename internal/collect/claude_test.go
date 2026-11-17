package collect

import (
	"fmt"
	"strings"
	"testing"
)

func TestClaudeRequestlessGatewayIdentity(t *testing.T) {
	row := func(session, at string, output int) string {
		return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"sessionId":%q,"message":{"id":"ocgo","model":"m","usage":{"input_tokens":100,"output_tokens":%d}}}`, at, session, output)
	}
	a, err := eventsOnly(parseClaude(writeFile(t, "a.jsonl", row("a", "2026-11-15T01:00:00Z", 25), row("a", "2026-11-15T01:00:01Z", 250))))
	if err != nil || len(a) != 2 || a[0].OutputTokens+a[1].OutputTokens != 275 {
		t.Fatalf("requestless gateway responses merged: %+v, %v", a, err)
	}
	b, err := eventsOnly(parseClaude(writeFile(t, "b.jsonl", row("b", "2026-11-15T01:00:00Z", 25))))
	if err != nil || len(b) != 1 || a[0].ID == b[0].ID {
		t.Fatalf("gateway sessions collided: %+v, %v", b, err)
	}
}

func TestParseClaude(t *testing.T) {
	path := writeFile(t, "session.jsonl",
		`{"type":"api-request","id":"r1","timestamp":"2026-10-26T05:00:00.000Z","querySource":"repl_main_thread","params":{"model":"claude-x"}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:01.000Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","effort":"high","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":30}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:03.500Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","message":{"id":"m1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":50,"output_tokens_details":{"thinking_tokens":20},"speed":"fast","cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":30},"iterations":[{"type":"message","model":null,"input_tokens":5},{"type":"advisor_message","model":"claude-advisor","input_tokens":7,"output_tokens":3}]}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:04.000Z","message":{"id":"m2","model":"<synthetic>","usage":{"output_tokens":0}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:05.000Z","requestId":"q3","requestedModel":"claude-x","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"id":"m3","model":"<synthetic>","content":[{"type":"text","text":"limit reached"}],"usage":{"output_tokens":0}}}`,
		`{"type":"progress","sessionId":"s1","cwd":"/p","data":{"message":{"type":"assistant","timestamp":"2026-10-26T05:00:06.000Z","requestId":"q4","message":{"id":"msg_sub","model":"claude-y","usage":{"input_tokens":1,"output_tokens":2}}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:07.000Z","sessionId":"s2","isSidechain":true,"requestId":"q5","message":{"id":"msg_sub","model":"claude-y","usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:08.000Z","sessionId":"s1","requestId":"q6","message":{"id":"msg_fallback","model":"claude-x","usage":{"input_tokens":2,"output_tokens":144,"output_tokens_details":{"thinking_tokens":80}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:09.000Z","sessionId":"s1","requestId":"q6","message":{"id":"msg_fallback","model":"claude-x","usage":{"input_tokens":2,"output_tokens":44,"iterations":[{"type":"message","model":"claude-x","input_tokens":2,"cache_read_input_tokens":100,"output_tokens":144},{"type":"fallback_message","model":"claude-y","input_tokens":2,"output_tokens":44}]}}}`,
		`{"type":"assistant", truncated line`,
	)
	events, err := eventsOnly(parseClaude(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 6 {
		t.Fatalf("got %d events, want 6: %+v", len(events), events)
	}
	ok, advisor, failed, subagent, fallback, original := events[0], events[1], events[2], events[3], events[4], events[5]
	if ok.ID != "m1:q1" || ok.Time != parseTime("2026-10-26T05:00:00.000Z") || ok.LatencyMS != 3500 {
		t.Errorf("request timing: id=%s time=%d latency=%d", ok.ID, ok.Time, ok.LatencyMS)
	}
	if ok.OutputTokens != 50 || ok.ReasoningTokens != 20 || ok.CacheWriteTokens != 40 || ok.CacheWrite1hTokens != 30 ||
		ok.Tier != "fast" || ok.Kind != "repl_main_thread" || ok.Stop != "tool_use" {
		t.Errorf("merged usage: %+v", ok)
	}
	if advisor.Model != "claude-advisor" || advisor.Kind != "advisor" || advisor.InputTokens != 7 || advisor.Time != ok.Time {
		t.Errorf("advisor iteration: %+v", advisor)
	}
	if !failed.Failed || failed.Model != "claude-x" || !strings.Contains(failed.Error, "HTTP 429") || !strings.Contains(failed.Error, "limit reached") {
		t.Errorf("failed request: %+v", failed)
	}
	// A replay of an Anthropic message under another request ID is the same request.
	if subagent.ID != "msg_sub" || subagent.Kind != "sidechain" || subagent.Session != "s1" || subagent.Project != "/p" {
		t.Errorf("progress line: %+v", subagent)
	}
	if fallback.Model != "claude-y" || fallback.OutputTokens != 44 || fallback.ReasoningTokens != 0 || original.Model != "claude-x" ||
		original.Kind != "fallback" || original.OutputTokens != 144 || original.CacheReadTokens != 100 || original.LatencyMS != 0 {
		t.Errorf("fallback usage: final=%+v original=%+v", fallback, original)
	}
}

func TestClaudeGatewayStreamingAndMissingIDs(t *testing.T) {
	path := writeFile(t, "session/subagents/workflows/wf/agent.jsonl",
		`{ "type" : "assistant", "timestamp":"2026-11-15T01:00:00Z","requestRef":"r1","message":{"id":"gateway","model":"deepseek-flash","usage":{"input_tokens":10,"cache_read_input_tokens":100,"output_tokens":20}}}`,
		`{ "type" : "assistant", "timestamp":"2026-11-15T01:00:01Z","requestRef":"r1","message":{"id":"gateway","model":"deepseek-flash","usage":{"input_tokens":10,"cache_read_input_tokens":100,"output_tokens":20}}}`,
		`{"type":"assistant","timestamp":"2026-11-15T01:00:01Z","requestRef":"other","message":{"id":"gateway","model":"m","usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{ "type" : "assistant", "timestamp":"2026-11-15T01:00:02Z","requestRef":"r2","message":{"model":"m","usage":{"input_tokens":2,"output_tokens":3}}}`,
		`{ "type" : "assistant", "timestamp":"2026-11-15T01:00:03Z","requestRef":"r3","message":{"model":"m","usage":{"input_tokens":4,"output_tokens":5}}}`,
	)
	events, err := eventsOnly(parseClaude(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 || events[0].CacheReadTokens != 100 || events[0].OutputTokens != 20 || events[0].Session != "session" || events[0].Kind != "sidechain" || events[0].ID == events[1].ID || events[2].ID == events[3].ID {
		t.Fatalf("gateway streaming/workflow requests: %+v", events)
	}
}
