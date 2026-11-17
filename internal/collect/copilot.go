package collect

import (
	"cmp"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

// copilot collects GitHub Copilot CLI from its OpenTelemetry exports, which
// record each model call, and its session-state shutdown snapshots, which only
// total usage per model. Both describe the same sessions, so the home is
// parsed as one source and covered calls explicitly retire their older rows.
func copilot() Collector {
	root := cmp.Or(strings.TrimSpace(os.Getenv("COPILOT_HOME")), homeDir(".copilot"))
	exporter := strings.TrimSpace(os.Getenv("COPILOT_OTEL_FILE_EXPORTER_PATH"))
	paths := []string{filepath.Join(root, "otel"), filepath.Join(root, "session-state")}
	if exporter != "" {
		paths = append(paths, exporter)
	}
	accept := func(path string) bool {
		if path == exporter {
			return true
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return false
		}
		parts := strings.Split(filepath.ToSlash(relative), "/")
		return parts[0] == "otel" && strings.HasSuffix(path, ".jsonl") ||
			len(parts) == 3 && parts[0] == "session-state" && parts[2] == "events.jsonl"
	}
	return Collector{
		Agent: "copilot", Version: 3,
		List: func() ([]Source, error) {
			source := Source{Agent: "copilot", Path: root}
			err := walk(paths, accept, func(_ string, info fs.FileInfo) {
				source.Size += info.Size()
				source.MTime = max(source.MTime, info.ModTime().UnixNano())
			})
			if err != nil || source.MTime == 0 {
				return nil, err
			}
			return []Source{source}, nil
		},
		Parse: func(string) (Result, error) {
			var calls []copilotCall
			var states []copilotState
			var readErr error
			err := walk(paths, accept, func(path string, _ fs.FileInfo) {
				if readErr != nil {
					return
				}
				if filepath.Base(path) == "events.jsonl" && path != exporter {
					state, err := readCopilotState(path)
					readErr = err
					states = append(states, state)
				} else {
					entries, err := readCopilotCalls(path)
					readErr = err
					calls = append(calls, entries...)
				}
			})
			if err != nil {
				return Result{}, err
			}
			if readErr != nil {
				return Result{}, readErr
			}
			return reconcileCopilot(calls, states), nil
		},
	}
}

type copilotRecord struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	TraceID     string `json:"traceId"`
	SpanID      string `json:"spanId"`
	SpanContext *struct {
		TraceID string `json:"traceId"`
		SpanID  string `json:"spanId"`
	} `json:"spanContext"`
	StartTime         json.RawMessage `json:"startTime"`
	EndTime           json.RawMessage `json:"endTime"`
	HRTime            json.RawMessage `json:"hrTime"`
	Timestamp         json.RawMessage `json:"timestamp"`
	ObservedTimestamp json.RawMessage `json:"observedTimestamp"`
	TimeUnixNano      json.RawMessage `json:"timeUnixNano"`
	Body              any             `json:"body"`
	Attributes        map[string]any  `json:"attributes"`
}

// Kinds of OpenTelemetry records that carry usage, most precise first. A
// trace counts only its most precise kind, since the others repeat it.
const (
	copilotChatSpan = iota
	copilotInferenceLog
	copilotTurnLog
	copilotAgentSpan
)

func (r *copilotRecord) kind() (int, bool) {
	span := r.Type == "span" || r.Type == "" && r.Name != "" && (r.TraceID != "" || r.SpanID != "" || len(r.StartTime) > 0 || len(r.EndTime) > 0)
	operation, eventName, body := text(r.Attributes["gen_ai.operation.name"]), text(r.Attributes["event.name"]), text(r.Body)
	switch {
	case span && (operation == "chat" || strings.HasPrefix(r.Name, "chat ")):
		return copilotChatSpan, true
	case span && (operation == "invoke_agent" || strings.HasPrefix(r.Name, "invoke_agent ")):
		return copilotAgentSpan, true
	case !span && (eventName == "gen_ai.client.inference.operation.details" || strings.HasPrefix(body, "GenAI inference:")):
		return copilotInferenceLog, true
	case !span && (eventName == "copilot_chat.agent.turn" || strings.HasPrefix(body, "copilot_chat.agent.turn")):
		return copilotTurnLog, true
	}
	return 0, false
}

var (
	copilotSessionAttributes = []string{"gen_ai.conversation.id", "copilot_chat.session_id", "copilot_chat.chat_session_id", "session.id"}
	copilotCallAttributes    = []string{"github.copilot.interaction_id", "gen_ai.response.id"}
)

func copilotModel(model string) string {
	model = strings.TrimSpace(model)
	return strings.TrimSuffix(strings.TrimSuffix(model, "-1m-internal"), "-1m")
}

// otelTime reads an OpenTelemetry time: a [seconds, nanoseconds] pair, a Unix
// time in seconds through nanoseconds, or an ISO 8601 string.
func otelTime(raw json.RawMessage) int64 {
	var value any
	if len(raw) == 0 || json.Unmarshal(raw, &value) != nil {
		return 0
	}
	scalar := func(value float64) int64 {
		for value >= 1e14 {
			value /= 1000
		}
		return unixMillis(value)
	}
	switch value := value.(type) {
	case []any:
		if len(value) == 2 {
			return number(value[0])*1000 + number(value[1])/1_000_000
		}
	case string:
		if parsed, err := strconv.ParseFloat(value, 64); err == nil {
			return scalar(parsed)
		}
		return parseTime(value)
	case float64:
		return scalar(value)
	}
	return 0
}

type copilotCall struct {
	event.Event
	kind                     int
	trace, response, service string
	accounting               string
}

func copilotCallKey(call copilotCall) string {
	if call.kind == copilotChatSpan && call.service == "" && call.response != "" {
		return fmt.Sprintf("%s:%s:%d:%d:%d:%d:%d", call.response, call.Model, call.InputTokens, call.CacheReadTokens, call.CacheWriteTokens, call.OutputTokens, call.ReasoningTokens)
	}
	return call.ID
}

// readCopilotCalls reads the raw accounting records in one export.
func readCopilotCalls(path string) ([]copilotCall, error) {
	var records []copilotRecord
	err := readLines(path, func(raw []byte) {
		var record copilotRecord
		if json.Unmarshal(raw, &record) == nil && record.Attributes != nil {
			records = append(records, record)
		}
	})
	if err != nil {
		return nil, err
	}
	traceModels, traceSessions := map[string]string{}, map[string]string{}
	for i := range records {
		r := &records[i]
		if r.SpanContext != nil {
			r.TraceID, r.SpanID = cmp.Or(r.TraceID, r.SpanContext.TraceID), cmp.Or(r.SpanID, r.SpanContext.SpanID)
		}
		if r.TraceID == "" {
			continue
		}
		model := cmp.Or(text(r.Attributes["gen_ai.response.model"]), text(r.Attributes["gen_ai.request.model"]))
		traceModels[r.TraceID] = cmp.Or(traceModels[r.TraceID], model)
		for _, name := range copilotSessionAttributes {
			traceSessions[r.TraceID] = cmp.Or(traceSessions[r.TraceID], text(r.Attributes[name]))
		}
	}

	var calls []copilotCall
	for i := range records {
		r := &records[i]
		kind, ok := r.kind()
		if !ok {
			continue
		}
		a := r.Attributes
		input, output := firstNumber(a, "gen_ai.usage.input_tokens"), firstNumber(a, "gen_ai.usage.output_tokens")
		cacheRead := firstNumber(a, "gen_ai.usage.cache_read.input_tokens")
		cacheWrite := max(firstNumber(a, "gen_ai.usage.cache_write.input_tokens"), firstNumber(a, "gen_ai.usage.cache_creation.input_tokens"))
		reasoning := max(firstNumber(a, "gen_ai.usage.reasoning.output_tokens"), firstNumber(a, "gen_ai.usage.reasoning_tokens"))
		total := max(firstNumber(a, "gen_ai.usage.total_tokens"), firstNumber(a, "gen_ai.usage.total.token_count"))
		call := copilotCall{kind: kind, trace: r.TraceID, response: text(a["gen_ai.response.id"]), service: text(a["github.copilot.service_request_id"])}
		e := &call.Event
		e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(input, cacheRead, cacheWrite)
		e.OutputTokens, e.ReasoningTokens = output, reasoning
		if reasoning > 0 && total == input+output+reasoning {
			e.OutputTokens += reasoning
		}
		fillTotal(e, total)
		if e.TotalTokens() == 0 {
			continue
		}
		e.Agent, e.Source = "copilot", path
		e.Model = copilotModel(cmp.Or(text(a["gen_ai.response.model"]), text(a["gen_ai.request.model"]), traceModels[r.TraceID]))
		// A session attribute of the call, else of its trace, else an
		// identifier of the call itself.
		for _, name := range copilotSessionAttributes {
			e.Session = cmp.Or(e.Session, text(a[name]))
		}
		e.Session = cmp.Or(e.Session, traceSessions[r.TraceID])
		for _, name := range copilotCallAttributes {
			e.Session = cmp.Or(e.Session, text(a[name]))
		}
		e.Session = cmp.Or(e.Session, r.TraceID)
		start, end := otelTime(r.StartTime), otelTime(r.EndTime)
		e.Time = cmp.Or(start, end, otelTime(r.HRTime), otelTime(r.Timestamp), otelTime(r.ObservedTimestamp), otelTime(r.TimeUnixNano), fileTime(path))
		if start > 0 && end >= start {
			e.LatencyMS = end - start
		}
		if kind >= copilotTurnLog {
			e.Scope = event.ScopeTurn
		}
		e.ID = cmp.Or(call.service, r.TraceID+":"+r.SpanID)
		if call.service == "" && (r.TraceID == "" || r.SpanID == "") {
			e.ID = cmp.Or(call.response, path+":"+strconv.Itoa(i))
		}
		calls = append(calls, call)
	}

	return calls, nil
}

type copilotUsage struct {
	InputTokens      int64 `json:"inputTokens"` // Includes cache reads and writes.
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
	ReasoningTokens  int64 `json:"reasoningTokens"` // Included in output.
}
