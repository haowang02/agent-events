package collect

import (
	"fmt"

	"github.com/haowang02/agent-events/internal/event"
)

func zcode() Collector {
	paths := under(roots("ZCODE_HOME", homeDir(".zcode")), "cli", "db", "db.sqlite")
	return Collector{
		Agent: "zcode",
		List:  func() ([]Source, error) { return databases("zcode", paths...) },
		Parse: parseZCode,
	}
}

// parseZCode reads the completed requests of the ZCode model_usage ledger.
func parseZCode(path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	usage, err := columns(db, "model_usage")
	if err != nil || !usage["status"] || !usage["model_id"] {
		return nil, err
	}
	session, err := columns(db, "session")
	if err != nil {
		return nil, err
	}
	join := ""
	if session["id"] {
		join = "LEFT JOIN session s ON s.id = m.session_id"
	} else {
		session = nil
	}
	rows, err := db.Query(fmt.Sprintf(
		"SELECT m.id, m.session_id, m.started_at, m.model_id, %s, %s, %s, %s, %s, %s, %s, %s FROM model_usage m %s WHERE m.status = 'completed'",
		columnOr(usage, "m.provider_id", "''"), columnOr(usage, "m.input_tokens", "0"), columnOr(usage, "m.output_tokens", "0"),
		columnOr(usage, "m.cache_read_input_tokens", "0"), columnOr(usage, "m.cache_creation_input_tokens", "0"),
		columnOr(usage, "m.computed_total_tokens", "0"), columnOr(session, "s.directory", "''"), columnOr(session, "s.version", "''"), join))
	if err != nil {
		return nil, err
	}
	var events []event.Event
	err = scanRows(rows, func() error {
		var id, sessionID, model, provider, directory, version string
		var started any
		var input, output, cacheRead, cacheWrite, total int64
		if err := rows.Scan(&id, &sessionID, &started, &model, &provider, &input, &output, &cacheRead, &cacheWrite, &total,
			&directory, &version); err != nil {
			return err
		}
		e := event.Event{
			Agent: "zcode", ID: id, Time: sqlTime(started), Model: model, Provider: provider, Session: sessionID,
			Project: directory, Version: version, OutputTokens: output, Source: path,
		}
		e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(input, cacheRead, cacheWrite)
		fillTotal(&e, total)
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
		return nil
	})
	return events, err
}
