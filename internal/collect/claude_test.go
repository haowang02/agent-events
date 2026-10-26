package collect

import (
	"strings"
	"testing"
)

func TestParseClaude(t *testing.T) {
	path := writeFile(t, "session.jsonl",
		`{"type":"api-request","id":"r1","timestamp":"2026-10-26T05:00:00.000Z","querySource":"repl_main_thread","params":{"model":"claude-x"}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:01.000Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","effort":"high","message":{"id":"m1","model":"claude-x","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":10,"cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":30}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:03.500Z","sessionId":"s1","cwd":"/p","requestId":"q1","requestRef":"r1","message":{"id":"m1","model":"claude-x","stop_reason":"tool_use","usage":{"input_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":40,"output_tokens":50,"output_tokens_details":{"thinking_tokens":20},"speed":"fast","cache_creation":{"ephemeral_5m_input_tokens":10,"ephemeral_1h_input_tokens":30},"iterations":[{"type":"message","model":null,"input_tokens":5},{"type":"advisor_message","model":"claude-advisor","input_tokens":7,"output_tokens":3}]}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:04.000Z","message":{"id":"m2","model":"<synthetic>","usage":{"output_tokens":0}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:05.000Z","requestId":"q3","requestedModel":"claude-x","isApiErrorMessage":true,"error":"rate_limit","apiErrorStatus":429,"message":{"id":"m3","model":"<synthetic>","content":[{"type":"text","text":"limit reached"}],"usage":{"output_tokens":0}}}`,
		`{"type":"progress","sessionId":"s1","cwd":"/p","data":{"message":{"type":"assistant","timestamp":"2026-10-26T05:00:06.000Z","requestId":"q4","message":{"id":"msg_sub","model":"claude-y","usage":{"input_tokens":1,"output_tokens":2}}}}}`,
		`{"type":"assistant","timestamp":"2026-10-26T05:00:07.000Z","sessionId":"s2","isSidechain":true,"requestId":"q5","message":{"id":"msg_sub","model":"claude-y","usage":{"input_tokens":1,"output_tokens":2}}}`,
		`{"type":"assistant", truncated line`,
	)
	events, err := parseClaude(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 4 {
		t.Fatalf("got %d events, want 4: %+v", len(events), events)
	}
	ok, advisor, failed, subagent := events[0], events[1], events[2], events[3]
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
}
