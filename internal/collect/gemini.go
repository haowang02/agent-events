package collect

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func gemini() Collector {
	dirs := roots("GEMINI_DATA_DIR", homeDir(".gemini", "tmp"))
	inChats := func(path string) bool {
		return filepath.Base(filepath.Dir(path)) == "chats" && withSuffix(".json", ".jsonl")(path)
	}
	return Collector{
		Agent: "gemini",
		List:  func() ([]Source, error) { return files("gemini", dirs, inChats) },
		Parse: func(path string) ([]event.Event, error) {
			if strings.HasSuffix(path, ".jsonl") {
				return parseGeminiLog(path)
			}
			return parseGeminiChat(path)
		},
	}
}

// qwen collects Qwen Code, a fork of Gemini CLI that records Gemini-style
// usage metadata.
func qwen() Collector {
	dirs := under(roots("QWEN_DATA_DIR", homeDir(".qwen")), "projects")
	inChats := func(path string) bool {
		return filepath.Base(filepath.Dir(path)) == "chats" && strings.HasSuffix(path, ".jsonl")
	}
	return Collector{
		Agent: "qwen",
		List:  func() ([]Source, error) { return files("qwen", dirs, inChats) },
		Parse: parseQwen,
	}
}

// geminiTokens fills the token buckets from Gemini usage. Its prompt count
// includes cached tokens when the total adds up without them; otherwise
// cached tokens are counted on their own.
func geminiTokens(e *event.Event, prompt, output, cached, thoughts, tool, total int64) {
	if inclusive := prompt + output + thoughts + tool; cached > 0 && total == inclusive {
		prompt -= min(cached, prompt)
	}
	e.InputTokens = prompt + tool
	e.CacheReadTokens = cached
	e.OutputTokens = output + thoughts
	e.ReasoningTokens = thoughts
}

type geminiMessage struct {
	ID        string         `json:"id"`
	Type      string         `json:"type"`
	Timestamp timestamp      `json:"timestamp"`
	Model     string         `json:"model"`
	Tokens    map[string]any `json:"tokens"`
	SessionID string         `json:"sessionId"`
}

// event converts a gemini message with token counts.
func (m *geminiMessage) event(id, session, model, path string) (event.Event, bool) {
	if m.Type != "gemini" || m.Tokens == nil {
		return event.Event{}, false
	}
	e := event.Event{
		Agent: "gemini", ID: cmp.Or(m.ID, id), Time: int64(m.Timestamp), Model: cmp.Or(m.Model, model),
		Session: session, Source: path,
	}
	t := m.Tokens
	geminiTokens(&e, firstNumber(t, "input", "prompt"), firstNumber(t, "output", "candidates"), firstNumber(t, "cached"),
		firstNumber(t, "thoughts", "reasoning"), firstNumber(t, "tool"), firstNumber(t, "total"))
	return e, e.TotalTokens() > 0
}

func geminiSession(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), ".json")
}

// parseGeminiChat reads a JSON conversation. Chats that only kept per-model
// stats yield one session total per model.
func parseGeminiChat(path string) ([]event.Event, error) {
	var chat struct {
		SessionID string          `json:"sessionId"`
		StartTime timestamp       `json:"startTime"`
		Messages  []geminiMessage `json:"messages"`
		Stats     *struct {
			Models map[string]struct {
				Tokens map[string]any `json:"tokens"`
			} `json:"models"`
		} `json:"stats"`
	}
	if err := readJSON(path, &chat); err != nil {
		return nil, nil
	}
	session := cmp.Or(chat.SessionID, geminiSession(path))
	var events []event.Event
	for i := range chat.Messages {
		if e, ok := chat.Messages[i].event(session+":"+strconv.Itoa(i), session, "", path); ok {
			events = append(events, e)
		}
	}
	if len(events) > 0 || chat.Stats == nil {
		return events, nil
	}
	for model, stats := range chat.Stats.Models {
		t := stats.Tokens
		e := event.Event{
			Agent: "gemini", ID: session + ":" + model, Time: int64(chat.StartTime), Model: model,
			Session: session, Scope: event.ScopeSession, Source: path,
		}
		prompt, output := firstNumber(t, "prompt", "input"), firstNumber(t, "candidates", "output")
		thoughts, tool := firstNumber(t, "thoughts"), firstNumber(t, "tool")
		// Stats count cached tokens inside the prompt.
		geminiTokens(&e, prompt, output, firstNumber(t, "cached"), thoughts, tool, prompt+output+thoughts+tool)
		events = append(events, e)
	}
	return events, nil
}

// parseGeminiLog reads a JSONL chat, where a rewritten message repeats its ID.
func parseGeminiLog(path string) ([]event.Event, error) {
	session, model := geminiSession(path), ""
	var events []event.Event
	index := map[string]int{}
	err := readLines(path, func(raw []byte) {
		var message geminiMessage
		if json.Unmarshal(raw, &message) != nil {
			return
		}
		session = cmp.Or(message.SessionID, session)
		model = cmp.Or(message.Model, model)
		e, ok := message.event(session+":"+strconv.Itoa(len(events)), session, model, path)
		if !ok {
			return
		}
		if i, seen := index[e.ID]; seen {
			events[i] = e
			return
		}
		index[e.ID] = len(events)
		events = append(events, e)
	})
	return events, err
}

type qwenRecord struct {
	UUID          string    `json:"uuid"`
	Type          string    `json:"type"`
	SessionID     string    `json:"sessionId"`
	Timestamp     timestamp `json:"timestamp"`
	CWD           string    `json:"cwd"`
	Version       string    `json:"version"`
	Model         string    `json:"model"`
	UsageMetadata *struct {
		PromptTokenCount        int64 `json:"promptTokenCount"`
		CandidatesTokenCount    int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount      int64 `json:"thoughtsTokenCount"`
		CachedContentTokenCount int64 `json:"cachedContentTokenCount"`
		TotalTokenCount         int64 `json:"totalTokenCount"`
	} `json:"usageMetadata"`
}

// parseQwen reads a Qwen Code chat under projects/<project>/chats.
func parseQwen(path string) ([]event.Event, error) {
	project := filepath.Base(filepath.Dir(filepath.Dir(path)))
	session := project + "-" + strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var events []event.Event
	err := readLines(path, func(raw []byte) {
		var record qwenRecord
		if json.Unmarshal(raw, &record) != nil || record.Type != "assistant" || record.UsageMetadata == nil {
			return
		}
		usage := record.UsageMetadata
		e := event.Event{
			Agent: "qwen", ID: cmp.Or(record.UUID, session+":"+strconv.Itoa(len(events))), Time: int64(record.Timestamp),
			Model: record.Model, Session: cmp.Or(record.SessionID, session), Project: cmp.Or(record.CWD, project),
			Version: record.Version, Source: path,
		}
		geminiTokens(&e, usage.PromptTokenCount, usage.CandidatesTokenCount, usage.CachedContentTokenCount,
			usage.ThoughtsTokenCount, 0, usage.TotalTokenCount)
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
	})
	return events, err
}
