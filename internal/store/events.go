package store

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/event"
)

var eventColumns = []string{
	"agent", "id", "time", "model", "provider", "session", "project", "client", "kind", "scope", "effort", "tier",
	"input_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write_1h_tokens", "output_tokens", "reasoning_tokens",
	"latency_ms", "ttft_ms", "failed", "error", "stop", "version", "source",
	"native_session",
}

func eventFields(e *event.Event) []any {
	return []any{
		e.Agent, e.ID, e.Time, e.Model, e.Provider, e.Session, e.Project, e.Client, e.Kind, e.Scope, e.Effort, e.Tier,
		e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.CacheWrite1hTokens, e.OutputTokens, e.ReasoningTokens,
		e.LatencyMS, e.TTFTMS, e.Failed, e.Error, e.Stop, e.Version, e.Source,
		e.NativeSession,
	}
}

func eventTargets(e *event.Event) []any {
	return []any{
		&e.Agent, &e.ID, &e.Time, &e.Model, &e.Provider, &e.Session, &e.Project, &e.Client, &e.Kind, &e.Scope, &e.Effort, &e.Tier,
		&e.InputTokens, &e.CacheReadTokens, &e.CacheWriteTokens, &e.CacheWrite1hTokens, &e.OutputTokens, &e.ReasoningTokens,
		&e.LatencyMS, &e.TTFTMS, &e.Failed, &e.Error, &e.Stop, &e.Version, &e.Source,
		&e.NativeSession,
	}
}

// Prefer timed requests, then the earliest copy and a stable source order.
// A source always updates its own rows; identical rows do not count as changes.
var upsertEventSQL = func() string {
	var updates, existing, incoming []string
	for _, column := range eventColumns[2:] {
		updates = append(updates, column+" = excluded."+column)
		existing = append(existing, "events."+column)
		incoming = append(incoming, "excluded."+column)
	}
	return fmt.Sprintf(
		"INSERT INTO events (%s) VALUES (?%s) ON CONFLICT (agent, id) DO UPDATE SET %s WHERE "+
			"(excluded.source = events.source OR (excluded.latency_ms > 0) > (events.latency_ms > 0) OR "+
			"(excluded.latency_ms > 0) = (events.latency_ms > 0) AND (excluded.time < events.time OR "+
			"excluded.time = events.time AND excluded.source < events.source)) AND (%s) IS NOT (%s)",
		strings.Join(eventColumns, ", "), strings.Repeat(", ?", len(eventColumns)-1), strings.Join(updates, ", "),
		strings.Join(existing, ", "), strings.Join(incoming, ", "),
	)
}()

// Parser upgrades replace only sessions still present in a source.
// Ordinary scans retain requests removed from the agent's logs.
type Parsed struct {
	Source collect.Source
	collect.Result
	Replace bool
}

// Save stores the events of parsed sources and marks the sources as parsed in
// one transaction. It returns how many events were added, changed or removed.
func (s *Store) Save(parsed []Parsed) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	upsertEvent, err := tx.Prepare(upsertEventSQL)
	if err != nil {
		return 0, err
	}
	defer upsertEvent.Close()
	upsertSource, err := tx.Prepare("INSERT OR REPLACE INTO sources (path, agent, size, mtime, parser) VALUES (?, ?, ?, ?, ?)")
	if err != nil {
		return 0, err
	}
	defer upsertSource.Close()
	var saved int64
	type identity struct{ agent, id string }
	yielded := map[identity]bool{}
	superseded := map[collect.RecordRef]bool{}
	for _, p := range parsed {
		if p.Replace {
			removed, err := removeStale(tx, p)
			if err != nil {
				return 0, err
			}
			saved += removed
		}
		for _, ref := range p.Superseded {
			superseded[ref] = true
		}
		for i := range p.Events {
			e := &p.Events[i]
			yielded[identity{e.Agent, e.ID}] = true

			result, err := upsertEvent.Exec(eventFields(&p.Events[i])...)
			if err != nil {
				return 0, err
			}
			affected, _ := result.RowsAffected()
			saved += affected
		}
		source := p.Source
		if _, err := upsertSource.Exec(source.Path, source.Agent, source.Size, source.MTime, source.Parser); err != nil {
			return 0, err
		}
	}
	for ref := range superseded {
		if yielded[identity{ref.Agent, ref.ID}] {
			continue
		}
		result, err := tx.Exec("DELETE FROM events WHERE agent = ? AND id = ? AND source = ?", ref.Agent, ref.ID, ref.Source)
		if err != nil {
			return 0, err
		}
		n, _ := result.RowsAffected()
		saved += n
	}
	return saved, tx.Commit()
}

// removeStale deletes the events of a source that it no longer yields, in the
// sessions it yields events of.
func removeStale(tx *sql.Tx, p Parsed) (int64, error) {
	type key struct{ agent, id string }
	yielded := make(map[key]bool, len(p.Events))
	sessions := map[string]bool{}
	for _, session := range p.Sessions {
		sessions[session] = true
	}
	paths := map[string]bool{p.Source.Path: true}
	for _, e := range p.Events {
		yielded[key{e.Agent, e.ID}] = true
		sessions[e.Session] = true
		paths[e.Source] = true
	}
	var args []any
	for path := range paths {
		if path != "" {
			args = append(args, path)
		}
	}
	if len(args) == 0 {
		return 0, nil
	}
	rows, err := tx.Query("SELECT agent, id, session FROM events WHERE source IN (?"+strings.Repeat(",?", len(args)-1)+")", args...)
	if err != nil {
		return 0, err
	}
	defer rows.Close()
	var stale []key
	for rows.Next() {
		var k key
		var session string
		if err := rows.Scan(&k.agent, &k.id, &session); err != nil {
			return 0, err
		}
		if sessions[session] && !yielded[k] {
			stale = append(stale, k)
		}
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, k := range stale {
		if _, err := tx.Exec("DELETE FROM events WHERE agent = ? AND id = ?", k.agent, k.id); err != nil {
			return 0, err
		}
	}
	return int64(len(stale)), nil
}

// Events returns every event, newest first. Seq is the row ID, which an
// upsert keeps, so it orders events by when they were first saved.
func (s *Store) Events() ([]event.Event, error) {
	rows, err := s.db.Query("SELECT rowid, " + strings.Join(eventColumns, ", ") + " FROM events ORDER BY time DESC, agent, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []event.Event
	for rows.Next() {
		var e event.Event
		if err := rows.Scan(append([]any{&e.Seq}, eventTargets(&e)...)...); err != nil {
			return nil, err
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
