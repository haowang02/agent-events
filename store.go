package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	_ "modernc.org/sqlite"
)

const schema = `
CREATE TABLE IF NOT EXISTS events (
	agent TEXT NOT NULL,
	id TEXT NOT NULL,
	time INTEGER NOT NULL,
	model TEXT NOT NULL DEFAULT '',
	provider TEXT NOT NULL DEFAULT '',
	session TEXT NOT NULL DEFAULT '',
	project TEXT NOT NULL DEFAULT '',
	client TEXT NOT NULL DEFAULT '',
	kind TEXT NOT NULL DEFAULT '',
	effort TEXT NOT NULL DEFAULT '',
	tier TEXT NOT NULL DEFAULT '',
	input_tokens INTEGER NOT NULL DEFAULT 0,
	cache_read_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_tokens INTEGER NOT NULL DEFAULT 0,
	cache_write_1h_tokens INTEGER NOT NULL DEFAULT 0,
	output_tokens INTEGER NOT NULL DEFAULT 0,
	reasoning_tokens INTEGER NOT NULL DEFAULT 0,
	latency_ms INTEGER NOT NULL DEFAULT 0,
	ttft_ms INTEGER NOT NULL DEFAULT 0,
	failed INTEGER NOT NULL DEFAULT 0,
	error TEXT NOT NULL DEFAULT '',
	stop TEXT NOT NULL DEFAULT '',
	version TEXT NOT NULL DEFAULT '',
	source TEXT NOT NULL DEFAULT '',
	PRIMARY KEY (agent, id)
);
CREATE TABLE IF NOT EXISTS sources (
	path TEXT PRIMARY KEY,
	agent TEXT NOT NULL,
	size INTEGER NOT NULL,
	mtime INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS custom_prices (
	model TEXT PRIMARY KEY,
	rates TEXT NOT NULL
);`

var eventColumns = []string{
	"agent", "id", "time", "model", "provider", "session", "project", "client", "kind", "effort", "tier",
	"input_tokens", "cache_read_tokens", "cache_write_tokens", "cache_write_1h_tokens", "output_tokens", "reasoning_tokens",
	"latency_ms", "ttft_ms", "failed", "error", "stop", "version", "source",
}

func eventFields(e *Event) []any {
	return []any{
		e.Agent, e.ID, e.Time, e.Model, e.Provider, e.Session, e.Project, e.Client, e.Kind, e.Effort, e.Tier,
		e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, e.CacheWrite1hTokens, e.OutputTokens, e.ReasoningTokens,
		e.LatencyMS, e.TTFTMS, e.Failed, e.Error, e.Stop, e.Version, e.Source,
	}
}

func eventTargets(e *Event) []any {
	return []any{
		&e.Agent, &e.ID, &e.Time, &e.Model, &e.Provider, &e.Session, &e.Project, &e.Client, &e.Kind, &e.Effort, &e.Tier,
		&e.InputTokens, &e.CacheReadTokens, &e.CacheWriteTokens, &e.CacheWrite1hTokens, &e.OutputTokens, &e.ReasoningTokens,
		&e.LatencyMS, &e.TTFTMS, &e.Failed, &e.Error, &e.Stop, &e.Version, &e.Source,
	}
}

// The same request can appear in several files, e.g. when a session is
// resumed into a new transcript; the copy with timing information wins.
// Identical rows are left alone, so the affected count reports real changes.
var upsertEventSQL = func() string {
	var updates, existing, incoming []string
	for _, column := range eventColumns[2:] {
		updates = append(updates, column+" = excluded."+column)
		existing = append(existing, "events."+column)
		incoming = append(incoming, "excluded."+column)
	}
	return fmt.Sprintf(
		"INSERT INTO events (%s) VALUES (?%s) ON CONFLICT (agent, id) DO UPDATE SET %s "+
			"WHERE (events.latency_ms = 0 OR excluded.latency_ms > 0) AND (%s) IS NOT (%s)",
		strings.Join(eventColumns, ", "), strings.Repeat(", ?", len(eventColumns)-1), strings.Join(updates, ", "),
		strings.Join(existing, ", "), strings.Join(incoming, ", "),
	)
}()

type Store struct{ db *sql.DB }

func openStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize %s: %w", path, err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

// Reset discards collected events so that every source is parsed again.
// Custom prices are kept.
func (s *Store) Reset() error {
	_, err := s.db.Exec("DELETE FROM events; DELETE FROM sources;")
	return err
}

func (s *Store) Sources() (map[string]Source, error) {
	rows, err := s.db.Query("SELECT path, agent, size, mtime FROM sources")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	sources := make(map[string]Source)
	for rows.Next() {
		var source Source
		if err := rows.Scan(&source.Path, &source.Agent, &source.Size, &source.MTime); err != nil {
			return nil, err
		}
		sources[source.Path] = source
	}
	return sources, rows.Err()
}

// Save stores parsed events and marks their sources as scanned in one
// transaction. It returns how many events were added or changed.
func (s *Store) Save(events []Event, sources []Source) (int64, error) {
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
	var saved int64
	for i := range events {
		result, err := upsertEvent.Exec(eventFields(&events[i])...)
		if err != nil {
			return 0, err
		}
		affected, _ := result.RowsAffected()
		saved += affected
	}
	upsertSource, err := tx.Prepare("INSERT OR REPLACE INTO sources (path, agent, size, mtime) VALUES (?, ?, ?, ?)")
	if err != nil {
		return 0, err
	}
	defer upsertSource.Close()
	for _, source := range sources {
		if _, err := upsertSource.Exec(source.Path, source.Agent, source.Size, source.MTime); err != nil {
			return 0, err
		}
	}
	return saved, tx.Commit()
}

func (s *Store) Events() ([]Event, error) {
	rows, err := s.db.Query("SELECT " + strings.Join(eventColumns, ", ") + " FROM events ORDER BY time DESC, agent, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var event Event
		if err := rows.Scan(eventTargets(&event)...); err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	return events, rows.Err()
}

func (s *Store) CustomPrices() (map[string]Rates, error) {
	rows, err := s.db.Query("SELECT model, rates FROM custom_prices")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	prices := make(map[string]Rates)
	for rows.Next() {
		var model string
		var raw []byte
		if err := rows.Scan(&model, &raw); err != nil {
			return nil, err
		}
		var rates Rates
		if err := json.Unmarshal(raw, &rates); err != nil {
			return nil, fmt.Errorf("custom price of %s: %w", model, err)
		}
		prices[model] = rates
	}
	return prices, rows.Err()
}

func (s *Store) SetCustomPrice(model string, rates Rates) error {
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
