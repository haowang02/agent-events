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
		Agent:   "pi",
		Version: 2,
		List:    func() ([]Source, error) { return files("pi", dirs, accept) },
		Parse:   func(path string) (Result, error) { return parsePiFile("pi", path) },
	}
}

// openClaw collects OpenClaw, built on Pi, including the homes of its earlier
// names and the per-agent SQLite transcript stores of newer versions.
func openClaw() Collector {
	dirs := roots("OPENCLAW_DIR", homeDir(".openclaw"), homeDir(".clawdbot"), homeDir(".moltbot"), homeDir(".moldbot"))
	return Collector{
		Agent:   "openclaw",
		Version: 3,
		List: func() ([]Source, error) {
			sources, err := filesPruned("openclaw", dirs, func(path string) bool {
				_, suffix, ok := strings.Cut(filepath.Base(path), ".jsonl")
				return ok && (suffix == "" || strings.HasPrefix(suffix, ".deleted.") || strings.HasPrefix(suffix, ".reset."))
			}, func(path string) bool {
				name := filepath.Base(path)
				return name == ".git" || name == "node_modules" || name == "subagent-artifacts" || strings.HasPrefix(name, "workspace")
			})
			if err != nil {
				return nil, err
			}
			stores, err := globDatabases("openclaw", under(dirs, "agents", "*", "agent", "openclaw-agent.sqlite")...)
			return append(sources, stores...), err
		},
		Parse: func(path string) (Result, error) {
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
	ParentSession json.RawMessage `json:"parentSession"`
	ParentID      json.RawMessage `json:"parentId"`
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
			Total      int64 `json:"totalTokens"`
		} `json:"usage"`
	} `json:"message"`
}

// piTranscript follows one session's transcript: its header, the current
// model and thinking level, and the usage of assistant messages.
type piTranscript struct {
	agent, source, session, project string
	model, provider, effort         string
	at                              int64
	lineNumber                      int
	header, lineage                 bool
	forkTime                        int64
	parent                          string
	links                           []piEntryLink
	entryIDs                        []string
	events                          []event.Event
}

func (t *piTranscript) add(raw []byte) {
	t.lineNumber++
	var line piLine
	if json.Unmarshal(raw, &line) != nil {
		return
	}
	if line.Type != "session" {
		t.links = append(t.links, piEntryLink{line.ID, line.ParentID})
	}
	switch {
	case line.Type == "session":
		t.session = cmp.Or(line.ID, t.session)
		t.project = cmp.Or(line.CWD, t.project)
		t.at = cmp.Or(int64(line.Timestamp), t.at)
		t.header = t.header || line.ID != ""
		t.forkTime = int64(line.Timestamp)
		t.lineage = t.lineNumber == 1 && line.ID != "" && t.forkTime > 0
		if len(line.ParentSession) > 0 {
			if json.Unmarshal(line.ParentSession, &t.parent) != nil || strings.TrimSpace(t.parent) == "" {
				t.lineage = false
			}
		}
	case line.Type == "model_change" || line.Type == "custom" && line.CustomType == "model-snapshot":
		var data struct {
			ModelID  string `json:"modelId"`
			Model    string `json:"model"`
			Provider string `json:"provider"`
		}
		_ = json.Unmarshal(line.Data, &data)
		t.model = cmp.Or(data.ModelID, data.Model, line.ModelID, line.Model, t.model)
		t.provider = cmp.Or(data.Provider, line.Provider, t.provider)
	case line.Type == "thinking_level_change":
		t.effort = line.ThinkingLevel
	case line.Type == "message" && line.Message != nil && line.Message.Role == "assistant" && line.Message.Usage != nil:
		message, usage := line.Message, line.Message.Usage
		e := event.Event{
			Agent: t.agent, Time: cmp.Or(int64(message.Timestamp), int64(line.Timestamp), t.at), Model: cmp.Or(message.ModelID, message.Model, t.model),
			Provider: cmp.Or(message.Provider, t.provider), Session: t.session, Project: t.project, Effort: t.effort,
			InputTokens: usage.Input, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite, OutputTokens: usage.Output,
			Stop: message.StopReason, Source: t.source,
		}
		if message.StopReason == "error" {
			e.Failed = true
			e.Error = message.ErrorMessage
		}
		fillTotal(&e, usage.Total)
		if e.TotalTokens() == 0 && !e.Failed {
			return
		}
		namespace := t.session
		if !t.header && t.agent == "pi" {
			namespace = t.source
		}
		identity := fmt.Sprintf("line:%d", t.lineNumber)
		if line.ID != "" {
			identity = "event:" + line.ID
		}
		e.ID = namespace + ":" + identity
		t.events = append(t.events, e)
		t.entryIDs = append(t.entryIDs, line.ID)
	}
}

func parsePiFile(agent, path string) (Result, error) {
	t, err := readPiFamily(agent, path, map[string]bool{}, map[string]*piTranscript{})
	if err != nil {
		return Result{}, err
	}
	return Result{Events: t.events}, nil
}

// parseOpenClawStore reads the transcript_events of an openclaw-agent.sqlite
// store; each row holds one transcript line.
func parseOpenClawStore(path string) (Result, error) {
	db, err := openDatabase(path)
	if err != nil {
		return Result{}, err
	}
	defer db.Close()
	names, err := columns(db, "transcript_events")
	if err != nil {
		return Result{}, err
	}
	rows, err := db.Query("SELECT session_id, event_json, " + columnOr(names, "created_at", "NULL") + " FROM transcript_events ORDER BY seq")
	if err != nil {
		return Result{}, err
	}
	transcripts := map[string]*piTranscript{}
	err = scanRows(rows, func() error {
		var session, line string
		var created any
		if err := rows.Scan(&session, &line, &created); err != nil {
			return err
		}
		t := transcripts[session]
		if t == nil {
			t = &piTranscript{agent: "openclaw", source: path, session: session, at: fileTime(path)}
			transcripts[session] = t
		}
		t.at = cmp.Or(sqlTime(created), t.at)
		t.add([]byte(line))
		return nil
	})
	var events []event.Event
	for _, t := range transcripts {
		events = append(events, t.events...)
	}
	return Result{Events: events}, err
}
