package collect

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func goose() Collector {
	paths := []string{
		homeDir(".local", "share", "goose", "sessions", "sessions.db"),
		homeDir("Library", "Application Support", "goose", "sessions", "sessions.db"),
		homeDir(".local", "share", "Block", "goose", "sessions", "sessions.db"),
	}
	if root := strings.TrimSpace(os.Getenv("GOOSE_PATH_ROOT")); root != "" {
		paths = []string{filepath.Join(root, "data", "sessions", "sessions.db")}
	}
	return Collector{
		Agent: "goose",
		List:  func() ([]Source, error) { return databases("goose", paths...) },
		Parse: parseGoose,
	}
}

// parseGoose reads the sessions table of Goose, which only keeps each
// session's total usage. Accumulated counts include compacted history.
func parseGoose(path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	names, err := columns(db, "sessions")
	if err != nil || !names["model_config_json"] {
		return nil, err
	}
	tokens := func(name string) string {
		return fmt.Sprintf("COALESCE(NULLIF(%s, 0), %s)", columnOr(names, "accumulated_"+name, "0"), columnOr(names, name, "0"))
	}
	rows, err := db.Query(fmt.Sprintf("SELECT id, model_config_json, %s, %s, %s, %s, %s, %s FROM sessions WHERE model_config_json IS NOT NULL",
		columnOr(names, "provider_name", "''"), columnOr(names, "created_at", "NULL"), columnOr(names, "working_dir", "''"),
		tokens("input_tokens"), tokens("output_tokens"), tokens("total_tokens")))
	if err != nil {
		return nil, err
	}
	var events []event.Event
	err = scanRows(rows, func() error {
		var id, config, provider, directory string
		var created any
		var input, output, total int64
		if err := rows.Scan(&id, &config, &provider, &created, &directory, &input, &output, &total); err != nil {
			return err
		}
		var model struct {
			Name string `json:"model_name"`
		}
		if json.Unmarshal([]byte(config), &model) != nil || model.Name == "" {
			return nil
		}
		reasoning := max(0, total-input-output)
		e := event.Event{
			Agent: "goose", ID: id, Time: sqlTime(created), Model: model.Name, Provider: provider, Session: id,
			Project: directory, Scope: event.ScopeSession, Source: path,
			InputTokens: input, OutputTokens: output + reasoning, ReasoningTokens: reasoning,
		}
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
		return nil
	})
	return events, err
}
