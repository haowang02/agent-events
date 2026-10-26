package collect

import (
	"cmp"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

// pi collects the Pi coding agent. Debug transcripts that pi-subagents writes
// under subagent-artifacts repeat calls recorded in the main sessions.
func pi() Collector {
	dirs := roots("PI_AGENT_DIR", homeDir(".pi", "agent", "sessions"))
	accept := func(path string) bool {
		return strings.HasSuffix(path, ".jsonl") && !strings.Contains(filepath.ToSlash(path), "/subagent-artifacts/")
	}
	return Collector{
		Agent: "pi",
		List:  func() ([]Source, error) { return files("pi", dirs, accept) },
		Parse: func(path string) ([]event.Event, error) { return parsePiFile("pi", path) },
	}
}

// openClaw collects OpenClaw, built on Pi, including the homes of its earlier
// names and the per-agent SQLite transcript stores of newer versions.
func openClaw() Collector {
	dirs := roots("OPENCLAW_DIR", homeDir(".openclaw"), homeDir(".clawdbot"), homeDir(".moltbot"), homeDir(".moldbot"))
	return Collector{
		Agent: "openclaw",
		List: func() ([]Source, error) {
			sources, err := files("openclaw", dirs, withSuffix(".jsonl"))
			if err != nil {
				return nil, err
			}
			stores, err := globDatabases("openclaw", under(dirs, "agents", "*", "agent", "openclaw-agent.sqlite")...)
			return append(sources, stores...), err
		},
		Parse: func(path string) ([]event.Event, error) {
			if strings.HasSuffix(path, ".sqlite") {
				return parseOpenClawStore(path)
			}
			return parsePiFile("openclaw", path)
		},
	}
}

type piLine struct {
	Type          string          `json:"type"`
	ID            string          `json:"id"`
	Timestamp     timestamp       `json:"timestamp"`
	CWD           string          `json:"cwd"`
	CustomType    string          `json:"customType"`
	ModelID       string          `json:"modelId"`
	Model         string          `json:"model"`
	Provider      string          `json:"provider"`
	ThinkingLevel string          `json:"thinkingLevel"`
	Data          json.RawMessage `json:"data"`
	Message       *struct {
		Role         string    `json:"role"`
		Model        string    `json:"model"`
		ModelID      string    `json:"modelId"`
		Provider     string    `json:"provider"`
		Timestamp    timestamp `json:"timestamp"`
		StopReason   string    `json:"stopReason"`
		ErrorMessage string    `json:"errorMessage"`
		Usage        *struct {
			Input      int64 `json:"input"` // Excludes cached input.
			Output     int64 `json:"output"`
			CacheRead  int64 `json:"cacheRead"`
			CacheWrite int64 `json:"cacheWrite"`
		} `json:"usage"`
	} `json:"message"`
}

// piTranscript follows one session's transcript: its header, the current
// model and thinking level, and the usage of assistant messages.
type piTranscript struct {
	agent, source, session, project string
	model, provider, effort         string
	events                          []event.Event
}

func (t *piTranscript) add(raw []byte) {
	var line piLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	switch {
	case line.Type == "session":
		t.session = cmp.Or(line.ID, t.session)
		t.project = cmp.Or(line.CWD, t.project)
	case line.Type == "model_change" || line.Type == "custom" && line.CustomType == "model-snapshot":
		var data struct {
			ModelID  string `json:"modelId"`
			Model    string `json:"model"`
			Provider string `json:"provider"`
		}
		if json.Unmarshal(line.Data, &data) != nil {
			data.ModelID, data.Model, data.Provider = line.ModelID, line.Model, line.Provider
		}
		t.model = cmp.Or(data.ModelID, data.Model, t.model)
		t.provider = cmp.Or(data.Provider, t.provider)
	case line.Type == "thinking_level_change":
		t.effort = line.ThinkingLevel
	case line.Type == "message" && line.Message != nil && line.Message.Role == "assistant" && line.Message.Usage != nil:
		message, usage := line.Message, line.Message.Usage
		e := event.Event{
			Agent: t.agent, Time: int64(cmp.Or(message.Timestamp, line.Timestamp)), Model: cmp.Or(message.ModelID, message.Model, t.model),
			Provider: cmp.Or(message.Provider, t.provider), Session: t.session, Project: t.project, Effort: t.effort,
			InputTokens: usage.Input, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite, OutputTokens: usage.Output,
			Stop: message.StopReason, Source: t.source,
		}
		if message.StopReason == "error" {
			e.Failed = true
			e.Error = message.ErrorMessage
		}
		if e.TotalTokens() == 0 && !e.Failed {
			return
		}
		// Forked sessions copy their parent's messages, so the usage itself
		// identifies a request across files.
		e.ID = fmt.Sprintf("%d:%s:%d:%d:%d:%d", e.Time, e.Model, e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.OutputTokens)
		t.events = append(t.events, e)
	}
}

func parsePiFile(agent, path string) ([]event.Event, error) {
	t := &piTranscript{agent: agent, source: path, session: strings.TrimSuffix(filepath.Base(path), ".jsonl")}
	err := readLines(path, t.add)
	return t.events, err
}

// parseOpenClawStore reads the transcript_events of an openclaw-agent.sqlite
// store; each row holds one transcript line.
func parseOpenClawStore(path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query("SELECT session_id, event_json FROM transcript_events ORDER BY seq")
	if err != nil {
		return nil, err
	}
	transcripts := map[string]*piTranscript{}
	err = scanRows(rows, func() error {
		var session, line string
		if err := rows.Scan(&session, &line); err != nil {
			return err
		}
		t := transcripts[session]
		if t == nil {
			t = &piTranscript{agent: "openclaw", source: path, session: session}
			transcripts[session] = t
		}
		t.add([]byte(line))
		return nil
	})
	var events []event.Event
	for _, t := range transcripts {
		events = append(events, t.events...)
	}
	return events, err
}
