// Package store keeps collected events, the signatures of parsed sources and
// custom prices in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/event"
	"github.com/haowang02/agent-events/internal/pricing"
)

var eventColumns = []string{
	"agent", "id", "time", "model", "provider", "session", "project", "client", "kind", "scope", "effort", "tier",
	"input_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write_1h_tokens", "output_tokens", "reasoning_tokens",
	"latency_ms", "ttft_ms", "failed", "error", "stop", "version", "source",
}

func eventFields(e *event.Event) []any {
	return []any{
		e.Agent, e.ID, e.Time, e.Model, e.Provider, e.Session, e.Project, e.Client, e.Kind, e.Scope, e.Effort, e.Tier,
		e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.CacheWrite1hTokens, e.OutputTokens, e.ReasoningTokens,
		e.LatencyMS, e.TTFTMS, e.Failed, e.Error, e.Stop, e.Version, e.Source,
	}
}

func eventTargets(e *event.Event) []any {
	return []any{
		&e.Agent, &e.ID, &e.Time, &e.Model, &e.Provider, &e.Session, &e.Project, &e.Client, &e.Kind, &e.Scope, &e.Effort, &e.Tier,
		&e.InputTokens, &e.CacheReadTokens, &e.CacheWriteTokens, &e.CacheWrite1hTokens, &e.OutputTokens, &e.ReasoningTokens,
		&e.LatencyMS, &e.TTFTMS, &e.Failed, &e.Error, &e.Stop, &e.Version, &e.Source,
	}
}

// The same request can be recorded in several sources: a resumed session's
// new transcript, a fork that replays its parent, or a compressed copy. The
// copy with timing information wins, then the earliest, which is the
// original. A source parsed again always updates its own copy. Identical rows
// are left alone, so the affected count reports real changes.
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
			"(excluded.latency_ms > 0) = (events.latency_ms > 0) AND excluded.time <= events.time) AND (%s) IS NOT (%s)",
		strings.Join(eventColumns, ", "), strings.Repeat(", ?", len(eventColumns)-1), strings.Join(updates, ", "),
		strings.Join(existing, ", "), strings.Join(incoming, ", "),
	)
}()

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Sources() (map[string]collect.Source, error) {
	rows, err := s.db.Query("SELECT path, agent, size, mtime, parser FROM sources")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make(map[string]collect.Source)
	for rows.Next() {
		var source collect.Source
		if err := rows.Scan(&source.Path, &source.Agent, &source.Size, &source.MTime, &source.Parser); err != nil {
			return nil, err
		}
		sources[source.Path] = source
	}
	return sources, rows.Err()
}

// Parsed is what parsing a source yielded. Replace is set when the source is
// read by a new parser: events it no longer yields are removed, but only in
// the sessions it still holds, since agents delete old sessions from shared
// databases. Otherwise events are only added and updated, so requests an
// agent removes from its logs stay.
type Parsed struct {
	Source  collect.Source
	Events  []event.Event
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
	for _, p := range parsed {
		if p.Replace {
			removed, err := removeStale(tx, p)
			if err != nil {
				return 0, err
			}
			saved += removed
		}
		for i := range p.Events {
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
	return saved, tx.Commit()
}

// removeStale deletes the events of a source that it no longer yields, in the
// sessions it yields events of.
func removeStale(tx *sql.Tx, p Parsed) (int64, error) {
	type key struct{ agent, id string }
	yielded := make(map[key]bool, len(p.Events))
	sessions := map[string]bool{}
	for _, e := range p.Events {
		yielded[key{e.Agent, e.ID}] = true
		sessions[e.Session] = true
	}
	rows, err := tx.Query("SELECT agent, id, session FROM events WHERE source = ?", p.Source.Path)
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

// CustomPrices returns custom rates by model key.
func (s *Store) CustomPrices() (map[string]pricing.Rates, error) {
	rows, err := s.db.Query("SELECT model, rates FROM custom_prices")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := make(map[string]pricing.Rates)
	for rows.Next() {
		var model string
		var raw []byte
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		var rates pricing.Rates
		if err := json.Unmarshal(raw, &rates); err != nil {
			return nil, fmt.Errorf("custom price of %s: %w", model, err)
		}
		prices[model] = rates
	}
	return prices, rows.Err()
}

func (s *Store) SetCustomPrice(model string, rates pricing.Rates) error {
	raw, err := json.Marshal(rates)
	if err != nil {
		return err
	}
	_, err = s.db.Exec("INSERT OR REPLACE INTO custom_prices (model, rates) VALUES (?, ?)", model, raw)
	return err
}

func (s *Store) DeleteCustomPrice(model string) error {
	_, err := s.db.Exec("DELETE FROM custom_prices WHERE model = ?", model)
	return err
}
