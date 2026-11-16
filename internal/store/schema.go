package store

import (
	"database/sql"
	"fmt"
)

// migrations bring a database to the current schema one step at a time, and
// PRAGMA user_version counts the steps applied. Released steps never change,
// so collected events survive every upgrade.
var migrations = []string{
	`CREATE TABLE IF NOT EXISTS events (
		agent TEXT NOT NULL,
		id TEXT NOT NULL,
		time INTEGER NOT NULL,
		model TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL DEFAULT '',
		session TEXT NOT NULL DEFAULT '',
		project TEXT NOT NULL DEFAULT '',
		client TEXT NOT NULL DEFAULT '',
		kind TEXT NOT NULL DEFAULT '',
		scope TEXT NOT NULL DEFAULT '',
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
	);`,
	`ALTER TABLE sources ADD COLUMN parser INTEGER NOT NULL DEFAULT 0;
	CREATE INDEX events_source ON events (source);`,
}

func migrate(db *sql.DB) error {
	var version int
	if err := db.QueryRow("PRAGMA user_version").Scan(&version); err != nil {
		return err
	}
	if version > len(migrations) {
		return fmt.Errorf("schema version %d is newer than this build supports", version)
	}
	for ; version < len(migrations); version++ {
		tx, err := db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(fmt.Sprintf("%s\nPRAGMA user_version = %d;", migrations[version], version+1)); err != nil {
			return fmt.Errorf("migrate to schema version %d: %w", version+1, err)
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
