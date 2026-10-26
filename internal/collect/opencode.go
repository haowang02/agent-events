package collect

import (
	"cmp"
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func openCode() Collector {
	return openCodeFamily("opencode", roots("OPENCODE_DATA_DIR", xdgDataDir("opencode")), "opencode.db", "opencode-*.db")
}

// kilo collects Kilo Code, a fork of OpenCode that keeps its storage format.
func kilo() Collector {
	return openCodeFamily("kilo", roots("KILO_DATA_DIR", xdgDataDir("kilo")), "kilo.db")
}

// openCodeFamily reads the SQLite databases of OpenCode and its forks, and
// the per-message JSON files that versions before SQLite wrote.
func openCodeFamily(agent string, dirs []string, databaseNames ...string) Collector {
	return Collector{
		Agent: agent,
		List: func() ([]Source, error) {
			var patterns []string
			for _, dir := range dirs {
				for _, name := range databaseNames {
					patterns = append(patterns, filepath.Join(dir, name))
				}
			}
			sources, err := globDatabases(agent, patterns...)
			if err != nil {
				return nil, err
			}
			messages, err := files(agent, under(dirs, "storage", "message"), withSuffix(".json"))
			return append(sources, messages...), err
		},
		Parse: func(path string) ([]event.Event, error) {
			if strings.HasSuffix(path, ".json") {
				return parseOpenCodeMessageFile(agent, path)
			}
			return parseOpenCodeDatabase(agent, path)
		},
	}
}

// openCodeMessage covers both message shapes: V1 assistant messages with flat
// modelID and providerID, and V2 assistant and compaction messages with a
// model reference.
type openCodeMessage struct {
	ID         string `json:"id"`
	SessionID  string `json:"sessionID"`
	Type       string `json:"-"` // The V2 session_message type.
	Role       string `json:"role"`
	Agent      string `json:"agent"`
	Mode       string `json:"mode"`
	ModelID    string `json:"modelID"`
	ProviderID string `json:"providerID"`
	Variant    string `json:"variant"`
	Model      *struct {
		ID         string `json:"id"`
		ModelID    string `json:"modelID"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	} `json:"model"`
	Path struct {
		CWD string `json:"cwd"`
	} `json:"path"`
	Time struct {
		Created   timestamp `json:"created"`
		Streamed  timestamp `json:"streamed"` // When the response ended, before tools ran.
		Completed timestamp `json:"completed"`
	} `json:"time"`
	Finish string `json:"finish"`
	Tokens *struct {
		Input     int64 `json:"input"`  // Excludes cached input.
		Output    int64 `json:"output"` // Excludes reasoning.
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Error *struct {
		Type    string `json:"type"` // V2
		Message string `json:"message"`
		Status  int    `json:"status"`
		Name    string `json:"name"` // V1
		Data    struct {
			Message    string `json:"message"`
			StatusCode int    `json:"statusCode"`
		} `json:"data"`
	} `json:"error"`
}

type openCodeSession struct {
	directory, parent, version string
}

// event converts a message to an event, or reports false when it carries no
// usage or error.
func (m *openCodeMessage) event(agent, id string, session openCodeSession) (event.Event, bool) {
	if m.Tokens == nil && m.Error == nil {
		return event.Event{}, false
	}
	e := event.Event{
		Agent: agent, ID: id, Time: int64(m.Time.Created), Model: m.ModelID, Provider: m.ProviderID,
		Session: m.SessionID, Project: cmp.Or(session.directory, m.Path.CWD), Client: cmp.Or(m.Agent, m.Mode),
		Kind: "main", Effort: m.Variant, Stop: m.Finish, Version: session.version,
	}
	if model := m.Model; model != nil {
		e.Model = cmp.Or(model.ID, model.ModelID, e.Model)
		e.Provider = cmp.Or(model.ProviderID, e.Provider)
		e.Effort = cmp.Or(model.Variant, e.Effort)
	}
	if m.Type == "compaction" {
		e.Kind = m.Type
	} else if session.parent != "" {
		e.Kind = "subagent"
	}
	if end := int64(cmp.Or(m.Time.Streamed, m.Time.Completed)); end >= e.Time && e.Time > 0 {
		e.LatencyMS = end - e.Time
	}
	if tokens := m.Tokens; tokens != nil {
		e.InputTokens = tokens.Input
		e.CacheReadTokens = tokens.Cache.Read
		e.CacheWriteTokens = tokens.Cache.Write
		e.OutputTokens = tokens.Output + tokens.Reasoning
		e.ReasoningTokens = tokens.Reasoning
	}
	if failure := m.Error; failure != nil {
		e.Failed = true
		message := cmp.Or(failure.Message, failure.Data.Message, failure.Type, failure.Name)
		e.Error = message
		if status := cmp.Or(failure.Status, failure.Data.StatusCode); status > 0 {
			e.Error = fmt.Sprintf("HTTP %d %s\n%s", status, cmp.Or(failure.Type, failure.Name), message)
		}
	}
	return e, true
}

// parseOpenCodeDatabase reads V1 messages from the message table and V2
// messages from session_message; a database may hold both.
func parseOpenCodeDatabase(agent, path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	sessions, err := openCodeSessions(db)
	if err != nil {
		return nil, err
	}
	var events []event.Event
	add := func(id, sessionID string, message *openCodeMessage, created int64) {
		message.SessionID = sessionID
		message.Time.Created = cmp.Or(message.Time.Created, timestamp(created))
		if e, ok := message.event(agent, id, sessions[sessionID]); ok {
			e.Source = path
			events = append(events, e)
		}
	}

	v1, err := columns(db, "message")
	if err != nil {
		return nil, err
	}
	if v1["id"] && v1["session_id"] && v1["data"] {
		rows, err := db.Query("SELECT id, session_id, data, " + columnOr(v1, "time_created", "0") + " FROM message")
		if err != nil {
			return nil, err
		}
		err = scanRows(rows, func() error {
			var id, session, data string
			var created int64
			if err := rows.Scan(&id, &session, &data, &created); err != nil {
				return err
			}
			var message openCodeMessage
			if json.Unmarshal([]byte(data), &message) == nil && message.Role == "assistant" {
				add(id, session, &message, created)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	v2, err := columns(db, "session_message")
	if err != nil {
		return nil, err
	}
	if v2["id"] && v2["session_id"] && v2["type"] && v2["data"] {
		copies, err := openCodeForkCopies(db, v2["seq"])
		if err != nil {
			return nil, err
		}
		rows, err := db.Query(fmt.Sprintf(
			"SELECT id, session_id, type, data, %s, %s FROM session_message WHERE type IN ('assistant', 'compaction')",
			columnOr(v2, "seq", "-1"), columnOr(v2, "time_created", "0")))
		if err != nil {
			return nil, err
		}
		err = scanRows(rows, func() error {
			var id, session, kind, data string
			var seq, created int64
			if err := rows.Scan(&id, &session, &kind, &data, &seq, &created); err != nil {
				return err
			}
			if cutoff, ok := copies[session]; ok && seq >= 0 && seq <= cutoff {
				return nil
			}
			message := openCodeMessage{Type: kind}
			if json.Unmarshal([]byte(data), &message) == nil {
				add(id, session, &message, created)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return events, nil
}

// openCodeSessions reads session metadata from the V1 session table and the
// V2 session_v2 table, whichever exist.
func openCodeSessions(db *sql.DB) (map[string]openCodeSession, error) {
	sessions := map[string]openCodeSession{}
	for _, table := range []string{"session", "session_v2"} {
		names, err := columns(db, table)
		if err != nil {
			return nil, err
		}
		if !names["id"] {
			continue
		}
		rows, err := db.Query(fmt.Sprintf("SELECT id, %s, %s, %s FROM %s",
			columnOr(names, "directory", "''"), columnOr(names, "parent_id", "''"), columnOr(names, "version", "''"), table))
		if err != nil {
			return nil, err
		}
		err = scanRows(rows, func() error {
			var id string
			var session openCodeSession
			if err := rows.Scan(&id, &session.directory, &session.parent, &session.version); err != nil {
				return err
			}
			sessions[id] = session
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return sessions, nil
}

// openCodeForkCopies maps each forked V2 session to the last seq of the
// parent history it copied under new message IDs: through the boundary
// message, or up to the one before it.
func openCodeForkCopies(db *sql.DB, hasSeq bool) (map[string]int64, error) {
	copies := map[string]int64{}
	names, err := columns(db, "session_v2")
	if err != nil || !hasSeq || !names["fork_session_id"] || !names["fork_boundary"] {
		return copies, err
	}
	type fork struct{ id, parent, boundary string }
	var forks []fork
	rows, err := db.Query("SELECT id, fork_session_id, fork_boundary FROM session_v2 WHERE fork_session_id IS NOT NULL AND fork_boundary IS NOT NULL")
	if err != nil {
		return nil, err
	}
	err = scanRows(rows, func() error {
		var f fork
		if err := rows.Scan(&f.id, &f.parent, &f.boundary); err != nil {
			return err
		}
		forks = append(forks, f)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for _, f := range forks {
		var boundary struct {
			Type      string `json:"type"`
			MessageID string `json:"messageID"`
		}
		if json.Unmarshal([]byte(f.boundary), &boundary) != nil {
			continue
		}
		var seq sql.NullInt64
		if db.QueryRow("SELECT seq FROM session_message WHERE session_id = ? AND id = ?", f.parent, boundary.MessageID).Scan(&seq) != nil {
			continue
		}
		if boundary.Type == "before" {
			if db.QueryRow("SELECT MAX(seq) FROM session_message WHERE session_id = ? AND seq < ?", f.parent, seq.Int64).Scan(&seq) != nil {
				continue
			}
		}
		if seq.Valid {
			copies[f.id] = seq.Int64
		}
	}
	return copies, nil
}

// parseOpenCodeMessageFile reads storage/message/<session>/<message>.json and
// the session's storage/session/<project>/<session>.json.
func parseOpenCodeMessageFile(agent, path string) ([]event.Event, error) {
	var message openCodeMessage
	if err := readJSON(path, &message); err != nil || message.Role != "assistant" {
		return nil, nil
	}
	var session openCodeSession
	storage := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if matches, _ := filepath.Glob(filepath.Join(storage, "session", "*", message.SessionID+".json")); len(matches) > 0 {
		var info struct {
			Directory string `json:"directory"`
			ParentID  string `json:"parentID"`
			Version   string `json:"version"`
		}
		if readJSON(matches[0], &info) == nil {
			session = openCodeSession{directory: info.Directory, parent: info.ParentID, version: info.Version}
		}
	}
	id := cmp.Or(message.ID, strings.TrimSuffix(filepath.Base(path), ".json"))
	e, ok := message.event(agent, id, session)
	if !ok {
		return nil, nil
	}
	e.Source = path
	return []event.Event{e}, nil
}
