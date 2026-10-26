package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"strconv"
)

type codexUsage struct {
	InputTokens           int64 `json:"input_tokens"` // Includes cached input.
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"` // Includes reasoning.
	ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
}

type codexPayload struct {
	Type string `json:"type"`
	// session_meta
	ID            string          `json:"id"`
	CWD           string          `json:"cwd"`
	CLIVersion    string          `json:"cli_version"`
	Originator    string          `json:"originator"`
	ModelProvider string          `json:"model_provider"`
	Source        json.RawMessage `json:"source"`
	// turn_context
	Model             string `json:"model"`
	Effort            string `json:"effort"`
	CollaborationMode *struct {
		Settings struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"settings"`
	} `json:"collaboration_mode"`
	// event_msg
	TurnID             string `json:"turn_id"`
	TimeToFirstTokenMS int64  `json:"time_to_first_token_ms"`
	CompletedAtMS      int64  `json:"completed_at_ms"`
	Message            string `json:"message"`
	ThreadSettings     *struct {
		Model           string `json:"model"`
		ModelProviderID string `json:"model_provider_id"`
		ServiceTier     string `json:"service_tier"`
	} `json:"thread_settings"`
	// token_usage_record
	ResponseID string     `json:"response_id"`
	Usage      codexUsage `json:"usage"`
}

var (
	codexTypePrefix  = []byte(`"type":"`)
	codexUserMessage = []byte(`"type":"UserMessage"`)
)

// codexLineTypes returns the first two "type" values of a rollout line: the
// line type and, for event_msg lines, the payload type. Rollouts write the
// line type before the payload, so large lines are skipped without decoding.
func codexLineTypes(line []byte) (string, string) {
	var types [2]string
	rest := line
	for i := range types {
		start := bytes.Index(rest, codexTypePrefix)
		if start < 0 {
			break
		}
		rest = rest[start+len(codexTypePrefix):]
		end := bytes.IndexByte(rest, '"')
		if end < 0 {
			break
		}
		types[i] = string(rest[:end])
		rest = rest[end:]
	}
	return types[0], types[1]
}

func codexWanted(line []byte) bool {
	lineType, payloadType := codexLineTypes(line)
	switch lineType {
	case "session_meta", "turn_context", "token_usage_record":
		return true
	case "event_msg":
		switch payloadType {
		case "task_started", "task_complete", "token_count", "thread_settings_applied", "error":
			return true
		case "item_completed":
			return bytes.Contains(line, codexUserMessage)
		}
	}
	return false
}

// parseCodexFile reads a Codex rollout, where token_usage_record has one entry
// per model response. A response's request starts at the latest boundary: the
// user message that started it, or the token_count written once the previous
// response and its tool calls finished.
func parseCodexFile(path string) ([]Event, error) {
	var (
		events                    []Event
		session, project, version string
		client, provider, kind    string
		model, effort, tier       string
		boundary, lastResponse    int64
		turnStarts                = map[string]int64{}
		turnFirst                 = map[string]int{}
	)
	newEvent := func(id string, at int64) Event {
		return Event{
			Agent: "codex", ID: id, Time: at, Model: model, Provider: provider, Session: session, Project: project,
			Client: client, Kind: kind, Effort: effort, Tier: tier, Version: version, Source: path,
		}
	}
	err := readLines(path, func(raw []byte) {
		if !codexWanted(raw) {
			return
		}
		var line struct {
			Timestamp string       `json:"timestamp"`
			Type      string       `json:"type"`
			Payload   codexPayload `json:"payload"`
		}
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		at := parseTimestamp(line.Timestamp)
		payload := &line.Payload
		switch line.Type {
		case "session_meta":
			session, project, version = payload.ID, payload.CWD, payload.CLIVersion
			client, provider = payload.Originator, payload.ModelProvider
			kind = codexSourceKind(payload.Source)
		case "turn_context":
			model, effort = payload.Model, payload.Effort
			if effort == "" && payload.CollaborationMode != nil {
				effort = payload.CollaborationMode.Settings.ReasoningEffort
			}
			if payload.CWD != "" {
				project = payload.CWD
			}
		case "token_usage_record":
			event := newEvent(cmp.Or(payload.ResponseID, session+":"+strconv.FormatInt(at, 10)), at)
			usage := payload.Usage
			event.CacheReadTokens = usage.CachedInputTokens
			event.CacheWriteTokens = usage.CacheWriteInputTokens
			event.InputTokens = max(0, usage.InputTokens-usage.CachedInputTokens-usage.CacheWriteInputTokens)
			event.OutputTokens = usage.OutputTokens
			event.ReasoningTokens = usage.ReasoningOutputTokens
			if start := max(boundary, lastResponse); start > 0 && start <= at {
				event.Time = start
				event.LatencyMS = at - start
			}
			if _, ok := turnFirst[payload.TurnID]; !ok {
				turnFirst[payload.TurnID] = len(events)
			}
			events = append(events, event)
			lastResponse = at
		case "event_msg":
			switch payload.Type {
			case "task_started":
				boundary = at
				turnStarts[payload.TurnID] = at
			case "item_completed":
				boundary = max(boundary, at, payload.CompletedAtMS)
			case "token_count":
				boundary = at
			case "thread_settings_applied":
				if settings := payload.ThreadSettings; settings != nil {
					model = cmp.Or(settings.Model, model)
					provider = cmp.Or(settings.ModelProviderID, provider)
					if settings.ServiceTier != "default" {
						tier = settings.ServiceTier
					}
				}
			case "task_complete":
				// Codex measures time to first token from the start of the turn;
				// rebase it onto the start of the turn's first request.
				index, ok := turnFirst[payload.TurnID]
				if started := turnStarts[payload.TurnID]; ok && started > 0 && payload.TimeToFirstTokenMS > 0 {
					event := &events[index]
					if ttft := started + payload.TimeToFirstTokenMS - event.Time; ttft > 0 && ttft <= event.LatencyMS {
						event.TTFTMS = ttft
					}
				}
			case "error":
				event := newEvent(session+":error:"+strconv.FormatInt(at, 10), at)
				event.Failed = true
				event.Error = payload.Message
				events = append(events, event)
			}
		}
	})
	return events, err
}

// codexSourceKind names where a session came from: "cli", "exec", "vscode",
// or "subagent" for a thread spawned by another one.
func codexSourceKind(raw json.RawMessage) string {
	var name string
	if json.Unmarshal(raw, &name) == nil {
		return name
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) == nil {
		for key := range object {
			return key
		}
	}
	return ""
}
