package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/event"
)

func openTest(t *testing.T, path string) *Store {
	t.Helper()
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func eventsByID(t *testing.T, s *Store) map[string]event.Event {
	t.Helper()
	events, err := s.Events()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]event.Event{}
	for _, e := range events {
		byID[e.ID] = e
	}
	return byID
}

func TestMigrateKeepsEvents(t *testing.T) {
	path := filepath.Join(t.TempDir(), "events.db")
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(migrations[0] + `
		INSERT INTO events (agent, id, time, source) VALUES ('claude', 'a', 1, '/gone.jsonl');
		INSERT INTO sources (path, agent, size, mtime) VALUES ('/gone.jsonl', 'claude', 10, 20);`)
	db.Close()
	if err != nil {
		t.Fatal(err)
	}

	s := openTest(t, path)
	if _, ok := eventsByID(t, s)["a"]; !ok {
		t.Fatal("event lost")
	}
	sources, err := s.Sources()
	if err != nil || sources["/gone.jsonl"] != (collect.Source{Agent: "claude", Path: "/gone.jsonl", Size: 10, MTime: 20}) {
		t.Fatalf("sources %v, %v", sources, err)
	}
}

func TestSaveReplace(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "events.db"))
	source := collect.Source{Agent: "opencode", Path: "/opencode.db"}
	ev := func(id, session, path string, time, output int64) event.Event {
		return event.Event{Agent: "opencode", ID: id, Session: session, Time: time, OutputTokens: output, LatencyMS: 100, Source: path}
	}
	save := func(want int64, parsed ...Parsed) {
		t.Helper()
		if saved, err := s.Save(parsed); err != nil || saved != want {
			t.Fatalf("saved %d, want %d, %v", saved, want, err)
		}
	}
	save(4,
		Parsed{Source: source, Events: []event.Event{ev("x", "s1", source.Path, 10, 1), ev("y", "s1", source.Path, 20, 1), ev("old", "s2", source.Path, 5, 1)}},
		Parsed{Source: collect.Source{Agent: "opencode", Path: "/gone.json"}, Events: []event.Event{ev("gone", "s3", "/gone.json", 1, 1)}},
	)
	// A source that changed is merged, so nothing it stops yielding is removed.
	save(0, Parsed{Source: source, Events: []event.Event{ev("x", "s1", source.Path, 10, 1)}})

	// A new parser corrects x, even to a later time, and no longer yields y.
	// Session s2 was deleted from the database and /gone.json from disk.
	save(2, Parsed{Source: source, Events: []event.Event{ev("x", "s1", source.Path, 15, 2)}, Replace: true})
	events := eventsByID(t, s)
	if x := events["x"]; x.Time != 15 || x.OutputTokens != 2 {
		t.Fatalf("x not replaced: %+v", x)
	}
	for id, kept := range map[string]bool{"y": false, "old": true, "gone": true} {
		if _, ok := events[id]; ok != kept {
			t.Fatalf("%s kept %v, want %v", id, ok, kept)
		}
	}

	// A later copy from another source does not replace the original.
	save(0, Parsed{Source: collect.Source{Agent: "opencode", Path: "/fork.db"}, Events: []event.Event{ev("x", "s1", "/fork.db", 30, 9)}, Replace: true})
}
