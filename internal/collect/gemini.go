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
	inChats := withSuffix(".json", ".jsonl")
	return Collector{
		Agent: "gemini", Version: 2,
		List: func() ([]Source, error) { return files("gemini", dirs, inChats) },
		Parse: func(path string) (Result, error) {
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
		if !strings.HasSuffix(path, ".jsonl") {
			return false
		}
		for _, root := range dirs {
			relative, err := filepath.Rel(root, path)
			if err != nil {
				continue
			}
			parts := strings.Split(filepath.ToSlash(relative), "/")
			if len(parts) == 3 && parts[1] == "chats" || len(parts) == 4 && parts[1] == "chats" && parts[2] == "archive" {
				return true
			}
		}
		return false
	}
	return Collector{
		Agent: "qwen", Version: 2,
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
	fillTotal(e, total)
	e.ReasoningTokens = max(thoughts, e.OutputTokens-output)
}

type geminiMessage struct {
	ID           string          `json:"id"`
	Type         string          `json:"type"`
	Timestamp    timestamp       `json:"timestamp"`
	Model        string          `json:"model"`
	Tokens       map[string]any  `json:"tokens"`
	SessionID    string          `json:"sessionId"`
	SessionSnake string          `json:"session_id"`
	Created      timestamp       `json:"created_at"`
	StartTime    timestamp       `json:"startTime"`
	LastUpdated  timestamp       `json:"lastUpdated"`
	Messages     []geminiMessage `json:"messages"`
	Stats        map[string]any  `json:"stats"`
	Result       struct {
		Stats map[string]any `json:"stats"`
	} `json:"result"`
}

func (m *geminiMessage) stats() map[string]any {
	if m.Stats != nil {
		return m.Stats
	}
	return m.Result.Stats
}

// event converts a gemini message with token counts.
func (m *geminiMessage) event(id, session, model, path string, fallbackTime int64) (event.Event, bool) {
	if m.Type != "gemini" || m.Tokens == nil {
		return event.Event{}, false
	}
	e := event.Event{
		Agent: "gemini", ID: cmp.Or(m.ID, id), Time: cmp.Or(int64(m.Timestamp), int64(m.Created), fallbackTime), Model: cmp.Or(m.Model, model),
		Session: session, Source: path,
	}
	if m.ID == "" {
		// A session can have anonymous records in several files. The source
		// position is stable across appends and does not depend on file mtime.
		e.ID = path + ":" + id
	}
	t := m.Tokens
	geminiTokens(&e, firstNumber(t, "input", "prompt", "input_tokens", "prompt_tokens"), firstNumber(t, "output", "candidates", "output_tokens", "candidates_tokens"), firstNumber(t, "cached", "cached_tokens"),
		firstNumber(t, "thoughts", "reasoning", "thoughts_tokens", "reasoning_tokens"), firstNumber(t, "tool", "tool_tokens"), firstNumber(t, "total", "total_tokens"))
	return e, e.TotalTokens() > 0
}

func geminiSession(path string) string {
	return strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".jsonl"), ".json")
}

// parseGeminiChat reads a JSON conversation. Chats that only kept per-model
// stats yield one session total per model.
func parseGeminiChat(path string) (Result, error) {
	var chat geminiMessage
	if err := readJSON(path, &chat); err != nil {
		return Result{}, nil
	}
	session := cmp.Or(chat.SessionID, chat.SessionSnake, geminiSession(path))
	fallbackTime := cmp.Or(int64(chat.StartTime), int64(chat.LastUpdated), fileTime(path))
	var events []event.Event
	for i := range chat.Messages {
		if e, ok := chat.Messages[i].event(session+":"+strconv.Itoa(i), session, chat.Model, path, fallbackTime); ok {
			events = append(events, e)
		}
	}
	if len(events) > 0 {
		return Result{Events: events}, nil
	}
	if chat.Type == "gemini" {
		if e, ok := chat.event(session+":0", session, "", path, fallbackTime); ok {
			return Result{Events: []event.Event{e}}, nil
		}
	}
	return Result{Events: geminiStats(chat.stats(), session, chat.Model, path, cmp.Or(int64(chat.Timestamp), fallbackTime))}, nil
}

// Stats documents have either per-model tokens or a flat token summary.
func geminiStats(stats map[string]any, session, model, path string, at int64) []event.Event {
	if stats == nil {
		return nil
	}
	models := object(stats["models"])
	if len(models) == 0 {
		models = map[string]any{model: map[string]any{"tokens": stats}}
	}
	var events []event.Event
	for name, raw := range models {
		t := object(object(raw)["tokens"])
		prompt := firstNumber(t, "prompt", "input", "input_tokens", "prompt_tokens")
		output := firstNumber(t, "candidates", "output", "output_tokens", "candidates_tokens")
		cached := firstNumber(t, "cached", "cached_tokens")
		thoughts := firstNumber(t, "thoughts", "reasoning", "thoughts_tokens", "reasoning_tokens")
		tool := firstNumber(t, "tool", "tool_tokens")
		e := event.Event{Agent: "gemini", ID: session + ":" + name, Time: at, Model: name, Session: session, Scope: event.ScopeSession, Source: path}
		e.InputTokens, e.CacheReadTokens = max(0, prompt-cached)+tool, cached
		e.OutputTokens, e.ReasoningTokens = output+thoughts, thoughts
		fillTotal(&e, firstNumber(t, "total", "total_tokens"))
		e.ReasoningTokens = max(thoughts, e.OutputTokens-output)
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
	}
	return events
}

// parseGeminiLog reads a JSONL chat, where a rewritten message repeats its ID.
func parseGeminiLog(path string) (Result, error) {
	session, model := geminiSession(path), ""
	fallbackTime := fileTime(path)
	var summaries []event.Event
	var events []event.Event
	covered := map[string]bool{}
	lineNumber := 0
	index := map[string]int{}
	err := readLines(path, func(raw []byte) {
		lineNumber++
		var message geminiMessage
		if json.Unmarshal(raw, &message) != nil {
			return
		}
		session = cmp.Or(message.SessionID, message.SessionSnake, session)
		model = cmp.Or(message.Model, model)
		e, ok := message.event(session+":"+strconv.Itoa(len(events)), session, model, path, fallbackTime)
		if !ok {
			if stats := message.stats(); stats != nil {
				for _, summary := range geminiStats(stats, session, model, path, cmp.Or(int64(message.Timestamp), fallbackTime)) {
					summary.ID = path + ":stats:" + cmp.Or(message.ID, strconv.Itoa(lineNumber)) + ":" + summary.Model
					summaries = append(summaries, summary)
				}
			}
			return
		}
		covered[e.Session+"\x00"+e.Model] = true
		if i, seen := index[e.ID]; seen {
			events[i] = e
			return
		}
		index[e.ID] = len(events)
		events = append(events, e)
	})
	for _, summary := range summaries {
		if !covered[summary.Session+"\x00"+summary.Model] {
			events = append(events, summary)
		}
	}
	return Result{Events: events}, err
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
func parseQwen(path string) (Result, error) {
	chats := filepath.Dir(path)
	if filepath.Base(chats) == "archive" {
		chats = filepath.Dir(chats)
	}
	project := filepath.Base(filepath.Dir(chats))
	fallbackTime := fileTime(path)
	session := project + "-" + strings.TrimSuffix(filepath.Base(path), ".jsonl")
	var events []event.Event
	err := readLines(path, func(raw []byte) {
		var record qwenRecord
		if json.Unmarshal(raw, &record) != nil || record.Type != "assistant" || record.UsageMetadata == nil {
			return
		}
		usage := record.UsageMetadata
		e := event.Event{
			Agent: "qwen", ID: cmp.Or(record.UUID, session+":"+strconv.Itoa(len(events))), Time: cmp.Or(int64(record.Timestamp), fallbackTime),
			Model: record.Model, Session: cmp.Or(record.SessionID, session), Project: cmp.Or(record.CWD, project),
			Version: record.Version, Source: path,
		}
		geminiTokens(&e, usage.PromptTokenCount, usage.CandidatesTokenCount, usage.CachedContentTokenCount,
			usage.ThoughtsTokenCount, 0, usage.TotalTokenCount)
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
	})
	return Result{Events: events}, err
}
