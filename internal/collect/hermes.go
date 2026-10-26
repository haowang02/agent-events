package collect

import (
	"fmt"

	"github.com/haowang02/agent-events/internal/event"
)

func hermes() Collector {
	paths := under(roots("HERMES_HOME", homeDir(".hermes")), "state.db")
	return Collector{
		Agent: "hermes",
		List:  func() ([]Source, error) { return databases("hermes", paths...) },
		Parse: parseHermes,
	}
}

// parseHermes reads the sessions table of the Hermes state database, which
// only keeps each session's total usage.
func parseHermes(path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	names, err := columns(db, "sessions")
	if err != nil || !names["model"] {
		return nil, err
	}
	rows, err := db.Query(fmt.Sprintf("SELECT id, TRIM(model), %s, %s, %s, %s, %s, %s, %s FROM sessions WHERE TRIM(model) != ''",
		columnOr(names, "billing_provider", "''"), columnOr(names, "started_at", "NULL"), columnOr(names, "input_tokens", "0"),
		columnOr(names, "output_tokens", "0"), columnOr(names, "cache_read_tokens", "0"), columnOr(names, "cache_write_tokens", "0"),
		columnOr(names, "reasoning_tokens", "0")))
	if err != nil {
		return nil, err
	}
	var events []event.Event
	err = scanRows(rows, func() error {
		var id, model, provider string
		var started any
		var input, output, cacheRead, cacheWrite, reasoning int64
		if err := rows.Scan(&id, &model, &provider, &started, &input, &output, &cacheRead, &cacheWrite, &reasoning); err != nil {
			return err
		}
		e := event.Event{
			Agent: "hermes", ID: id, Time: sqlTime(started), Model: model, Provider: provider, Session: id,
			Scope: event.ScopeSession, Source: path, InputTokens: input, CacheReadTokens: cacheRead,
			CacheWriteTokens: cacheWrite, OutputTokens: output + reasoning, ReasoningTokens: reasoning,
		}
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
		return nil
	})
	return events, err
}
