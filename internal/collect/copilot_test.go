package collect

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/haowang02/agent-events/internal/event"
)

func TestCopilotReasoningIdentity(t *testing.T) {
	var rows []string
	for i, reasoning := range []int{1, 2, 1} {
		var record map[string]any
		if err := json.Unmarshal([]byte(copilotSpan("trace", fmt.Sprint(i), "turn", "", 10)), &record); err != nil {
			t.Fatal(err)
		}
		record["attributes"].(map[string]any)["gen_ai.usage.reasoning.output_tokens"] = reasoning
		raw, err := json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, string(raw))
	}
	events := parseCopilotOTel(t, writeFile(t, "otel.jsonl", rows...))
	if len(events) != 2 || events[0].ReasoningTokens+events[1].ReasoningTokens != 3 {
		t.Fatalf("different reasoning usage must survive while repeated exports merge: %+v", events)
	}
}

func copilotSpan(trace, span, response, service string, input int) string {
	raw, _ := json.Marshal(map[string]any{"type": "span", "name": "chat m", "traceId": trace, "spanId": span, "startTime": []int64{1794704400, 0}, "endTime": []int64{1794704401, 0}, "attributes": map[string]any{"gen_ai.operation.name": "chat", "gen_ai.conversation.id": "s", "gen_ai.response.model": "m", "gen_ai.response.id": response, "github.copilot.service_request_id": service, "gen_ai.usage.input_tokens": input, "gen_ai.usage.output_tokens": 20}})
	return string(raw)
}

func TestCopilotSharedResponseID(t *testing.T) {
	path := writeFile(t, "otel.jsonl", copilotSpan("trace", "span1", "turn", "", 100), copilotSpan("trace", "span2", "turn", "", 200), copilotSpan("trace", "span3", "turn", "", 100))
	events := parseCopilotOTel(t, path)
	if len(events) != 2 || events[0].InputTokens+events[1].InputTokens != 300 {
		t.Fatalf("distinct calls under one response, with repeated export: %+v", events)
	}
	path = writeFile(t, "otel.jsonl", copilotSpan("trace", "span1", "turn", "svc1", 100), copilotSpan("trace", "span2", "turn", "svc2", 100))
	if events := parseCopilotOTel(t, path); len(events) != 2 {
		t.Fatalf("service IDs distinguish equal usage: %+v", events)
	}
}

func TestCopilotUsageRecordsAndSnapshots(t *testing.T) {
	snapshot := `{"type":"session.shutdown","id":"shutdown","timestamp":"2026-11-15T01:02:00Z","data":{"modelMetrics":{"m":{"requests":{"count":1},"usage":{"inputTokens":100,"outputTokens":20}}}}}`
	record := `{"type":"session.usage_record","id":"call","timestamp":"2026-11-15T01:01:00Z","data":{"usage":{"model":"m","apiCallId":"turn","serviceRequestId":"svc1","accounting":{"sourceSessionId":"s","sequence":1},"inputTokens":100,"cacheReadTokens":30,"cacheWriteTokens":5,"outputTokens":20,"reasoningTokens":7}}}`
	path := writeFile(t, "session-state/s/events.jsonl", record, snapshot)
	state, err := readCopilotState(path)
	if err != nil {
		t.Fatal(err)
	}
	calls := parseCopilotCalls(t, writeFile(t, "otel.jsonl", copilotSpan("trace", "span1", "turn", "svc1", 100), copilotSpan("trace", "span2", "turn", "svc2", 200)))
	events := reconcileCopilot(calls, []copilotState{state, state}).Events
	if len(events) != 2 {
		t.Fatalf("one record replaces only its own OTel call and its shutdown: %+v", events)
	}
	var found bool
	for _, e := range events {
		if e.ID == "usage-record:s:1" {
			found = true
			if e.InputTokens != 65 || e.CacheReadTokens != 30 || e.CacheWriteTokens != 5 || e.OutputTokens != 20 || e.ReasoningTokens != 7 || e.Scope != "" {
				t.Fatalf("per-request buckets: %+v", e)
			}
		}
	}
	if !found {
		t.Fatal("usage record not preserved")
	}
}

func TestCopilotSnapshotIntervals(t *testing.T) {
	path := writeFile(t, "session-state/s/events.jsonl",
		`{"type":"session.shutdown","id":"first","timestamp":"2026-11-15T01:00:00Z","data":{"modelMetrics":{"m":{"requests":{"count":1},"usage":{"inputTokens":100,"outputTokens":20}}}}}`,
		`{"type":"session.shutdown","id":"second","timestamp":"2026-11-16T01:00:00Z","data":{"modelMetrics":{"m":{"requests":{"count":2},"usage":{"inputTokens":150,"outputTokens":30}}}}}`,
	)
	state, err := readCopilotState(path)
	if err != nil {
		t.Fatal(err)
	}
	call := copilotCall{Event: event.Event{Agent: "copilot", ID: "later", Session: "s", Model: "m", Time: parseTime("2026-11-16T02:00:00Z"), InputTokens: 10, OutputTokens: 2}}
	events := reconcileCopilot([]copilotCall{call}, []copilotState{state}).Events
	var input, output int64
	for _, e := range events {
		input += e.InputTokens
		output += e.OutputTokens
	}
	if len(events) != 3 || input != 160 || output != 32 {
		t.Fatalf("snapshots plus calls after the last snapshot: %+v", events)
	}
	// More OTel calls than the recorded request count reveal lost shutdown usage.
	calls := []copilotCall{{Event: event.Event{ID: "one", Session: "s", Model: "m", Time: parseTime("2026-11-15T00:00:00Z"), InputTokens: 1}}, {Event: event.Event{ID: "two", Session: "s", Model: "m", Time: parseTime("2026-11-15T00:01:00Z"), InputTokens: 1}}, {Event: event.Event{ID: "three", Session: "s", Model: "m", Time: parseTime("2026-11-16T00:00:00Z"), InputTokens: 1}}}
	events = reconcileCopilot(calls, []copilotState{state}).Events
	if len(events) != 3 || events[0].InputTokens+events[1].InputTokens+events[2].InputTokens != 3 {
		t.Fatalf("OTel must retain calls from a process without a shutdown: %+v", events)
	}
}

func parseCopilotCalls(t *testing.T, path string) []copilotCall {
	t.Helper()
	calls, err := readCopilotCalls(path)
	if err != nil {
		t.Fatal(err)
	}
	return calls
}

func parseCopilotOTel(t *testing.T, path string) []event.Event {
	t.Helper()
	return reconcileCopilot(parseCopilotCalls(t, path), nil).Events
}
