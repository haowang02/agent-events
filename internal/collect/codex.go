package collect

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func codex() Collector {
	homes := roots("CODEX_HOME", homeDir(".codex"))
	return Collector{
		Agent: "codex",
		List: func() ([]Source, error) {
			var dirs []string
			for _, home := range homes {
				sessions, archived := filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")
				if !isDir(sessions) && !isDir(archived) {
					sessions = home
				}
				dirs = append(dirs, sessions, archived)
			}
			return files("codex", dirs, isRollout)
		},
		Parse: parseCodex,
	}
}

func isDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// isRollout matches plain and Zstandard-compressed rollouts; Codex compresses
// older ones.
func isRollout(path string) bool {
	name := filepath.Base(path)
	return strings.HasPrefix(name, "rollout-") && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.zst"))
}

type codexUsage struct {
	InputTokens              int64 `json:"input_tokens"` // Includes cached input.
	CachedInputTokens        int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens    int64 `json:"cache_write_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"` // Includes reasoning.
	ReasoningOutputTokens    int64 `json:"reasoning_output_tokens"`
}

func (u codexUsage) apply(e *event.Event) {
	e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(
		u.InputTokens, u.CachedInputTokens, max(u.CacheWriteInputTokens, u.CacheCreationInputTokens))
	e.OutputTokens = u.OutputTokens
	e.ReasoningTokens = u.ReasoningOutputTokens
}

func (u codexUsage) minus(previous *codexUsage) codexUsage {
	if previous == nil {
		return u
	}
	return codexUsage{
		InputTokens:              max(0, u.InputTokens-previous.InputTokens),
		CachedInputTokens:        max(0, u.CachedInputTokens-previous.CachedInputTokens),
		CacheWriteInputTokens:    max(0, u.CacheWriteInputTokens-previous.CacheWriteInputTokens),
		CacheCreationInputTokens: max(0, u.CacheCreationInputTokens-previous.CacheCreationInputTokens),
		OutputTokens:             max(0, u.OutputTokens-previous.OutputTokens),
		ReasoningOutputTokens:    max(0, u.ReasoningOutputTokens-previous.ReasoningOutputTokens),
	}
}

func (u codexUsage) String() string {
	return fmt.Sprintf("%d.%d.%d.%d.%d.%d", u.InputTokens, u.CachedInputTokens, u.CacheWriteInputTokens,
		u.CacheCreationInputTokens, u.OutputTokens, u.ReasoningOutputTokens)
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
		Model           string  `json:"model"`
		ModelProviderID string  `json:"model_provider_id"`
		ServiceTier     *string `json:"service_tier"` // Absent leaves the tier unchanged.
	} `json:"thread_settings"`
	Info *struct {
		TotalTokenUsage *codexUsage `json:"total_token_usage"`
		LastTokenUsage  *codexUsage `json:"last_token_usage"`
	} `json:"info"`
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

// parseCodex reads a Codex rollout. Current versions write a
// token_usage_record for each model response; older ones only report usage in
// token_count events, which count when no record precedes them. A request
// starts at the latest boundary: the user message that started it, or the
// token_count written once the previous response and its tool calls finished.
func parseCodex(path string) ([]event.Event, error) {
	var (
		events                    []event.Event
		session, project, version string
		client, provider, kind    string
		model, effort, tier       string
		turn                      string
		boundary, lastResponse    int64
		recorded                  bool
		previousTotal             *codexUsage
		turnStarts                = map[string]int64{}
		turnFirst                 = map[string]int{}
	)
	newEvent := func(id string, at int64) event.Event {
		return event.Event{
			Agent: "codex", ID: id, Time: at, Model: model, Provider: provider, Session: session, Project: project,
			Client: client, Kind: kind, Effort: effort, Tier: tier, Version: version, Source: path,
		}
	}
	addRequest := func(id string, at int64, usage codexUsage, turnID string) {
		e := newEvent(id, at)
		usage.apply(&e)
		if start := max(boundary, lastResponse); start > 0 && start <= at {
			e.Time = start
			e.LatencyMS = at - start
		}
		if _, ok := turnFirst[turnID]; !ok {
			turnFirst[turnID] = len(events)
		}
		events = append(events, e)
		lastResponse = at
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
		at := parseTime(line.Timestamp)
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
			project = cmp.Or(payload.CWD, project)
		case "token_usage_record":
			addRequest(cmp.Or(payload.ResponseID, session+":"+strconv.FormatInt(at, 10)), at, payload.Usage, cmp.Or(payload.TurnID, turn))
			recorded = true
		case "event_msg":
			switch payload.Type {
			case "task_started":
				boundary, turn = at, payload.TurnID
				turnStarts[turn] = at
			case "item_completed":
				boundary = max(boundary, at, payload.CompletedAtMS)
			case "token_count":
				if info := payload.Info; info != nil {
					total, usage := info.TotalTokenUsage, info.LastTokenUsage
					if usage == nil && total != nil {
						delta := total.minus(previousTotal)
						usage = &delta
					}
					advanced := total == nil || previousTotal == nil || *total != *previousTotal
					if !recorded && advanced && usage != nil && *usage != (codexUsage{}) {
						addRequest(codexUsageID(session, at, total, usage), at, *usage, turn)
					}
					previousTotal = cmp.Or(total, previousTotal)
				}
				recorded = false
				boundary = at
			case "thread_settings_applied":
				if settings := payload.ThreadSettings; settings != nil {
					model = cmp.Or(settings.Model, model)
					provider = cmp.Or(settings.ModelProviderID, provider)
					if settings.ServiceTier != nil {
						tier = codexTier(*settings.ServiceTier)
					}
				}
			case "task_complete":
				// Codex measures time to first token from the start of the turn;
				// rebase it onto the start of the turn's first request.
				index, ok := turnFirst[payload.TurnID]
				if started := turnStarts[payload.TurnID]; ok && started > 0 && payload.TimeToFirstTokenMS > 0 {
					e := &events[index]
					if ttft := started + payload.TimeToFirstTokenMS - e.Time; ttft > 0 && ttft <= e.LatencyMS {
						e.TTFTMS = ttft
					}
				}
			case "error":
				e := newEvent(session+":error:"+strconv.FormatInt(at, 10), at)
				e.Failed, e.Error = true, payload.Message
				events = append(events, e)
			}
		}
	})
	return events, err
}

// codexUsageID keys a token_count request. Forks and subagents replay their
// parent's token counts under new timestamps; keying by the cumulative usage
// makes a replayed copy the same event as the original.
func codexUsageID(session string, at int64, total, last *codexUsage) string {
	if total == nil {
		return session + ":" + strconv.FormatInt(at, 10)
	}
	return "usage:" + total.String() + ":" + last.String()
}

func codexTier(serviceTier string) string {
	switch serviceTier {
	case "default", "standard":
		return ""
	case "fast":
		return "priority"
	}
	return serviceTier
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
