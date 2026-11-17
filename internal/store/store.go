// Package store keeps collected events, the signatures of parsed sources and
// custom prices in SQLite.
package store

import (
	"database/sql"
	"encoding/json"
	"fmt"

	_ "modernc.org/sqlite"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/pricing"
)

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
