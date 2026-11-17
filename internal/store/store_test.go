package store

import (
	"database/sql"
	"os"
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
		Parsed{Source: source, Result: collect.Result{Events: []event.Event{ev("x", "s1", source.Path, 10, 1), ev("y", "s1", source.Path, 20, 1), ev("old", "s2", source.Path, 5, 1)}}},
		Parsed{Source: collect.Source{Agent: "opencode", Path: "/gone.json"}, Result: collect.Result{Events: []event.Event{ev("gone", "s3", "/gone.json", 1, 1)}}},
	)
	// A source that changed is merged, so nothing it stops yielding is removed.
	save(0, Parsed{Source: source, Result: collect.Result{Events: []event.Event{ev("x", "s1", source.Path, 10, 1)}}})

	// A new parser corrects x, even to a later time, and no longer yields y.
	// Session s2 was deleted from the database and /gone.json from disk.
	save(2, Parsed{Source: source, Result: collect.Result{Events: []event.Event{ev("x", "s1", source.Path, 15, 2)}}, Replace: true})
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
	save(0, Parsed{Source: collect.Source{Agent: "opencode", Path: "/fork.db"}, Result: collect.Result{Events: []event.Event{ev("x", "s1", "/fork.db", 30, 9)}}, Replace: true})

	// A present session can have no billable requests after reconciliation.
	save(1, Parsed{Source: source, Result: collect.Result{Sessions: []string{"s1"}}, Replace: true})
	if events := eventsByID(t, s); len(events) != 2 || events["old"].ID != "old" || events["gone"].ID != "gone" {
		t.Fatalf("empty-session replacement affected archived sessions: %+v", events)
	}
}

func TestCopilotLiveReconciliationKeepsArchive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("COPILOT_HOME", root)
	t.Setenv("COPILOT_OTEL_FILE_EXPORTER_PATH", "")
	log := filepath.Join(root, "otel", "calls.jsonl")
	state := filepath.Join(root, "session-state", "s", "events.jsonl")
	for _, path := range []string{log, state} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
	}
	call := `{"type":"span","name":"chat m","traceId":"t","spanId":"c","startTime":"2026-11-15T01:00:00Z","attributes":{"gen_ai.conversation.id":"s","gen_ai.response.model":"m","gen_ai.response.id":"api","github.copilot.service_request_id":"service","gen_ai.usage.input_tokens":100,"gen_ai.usage.output_tokens":20}}` + "\n"
	if err := os.WriteFile(log, []byte(call), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(state, []byte("{}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var c collect.Collector
	for _, candidate := range collect.All() {
		if candidate.Agent == "copilot" {
			c = candidate
		}
	}
	s := openTest(t, filepath.Join(t.TempDir(), "events.db"))
	archive := event.Event{Agent: "copilot", ID: "archive", Session: "s", Model: "m", Source: filepath.Join(root, "gone.jsonl"), InputTokens: 7}
	if _, err := s.Save([]Parsed{{Source: collect.Source{Path: archive.Source}, Result: collect.Result{Events: []event.Event{archive}}}}); err != nil {
		t.Fatal(err)
	}
	scan := func() {
		t.Helper()
		sources, err := c.List()
		if err != nil || len(sources) != 1 {
			t.Fatalf("sources: %+v, %v", sources, err)
		}
		result, err := c.Parse(sources[0].Path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = s.Save([]Parsed{{Source: sources[0], Result: result}}); err != nil {
			t.Fatal(err)
		}
	}
	check := func() {
		t.Helper()
		events, err := s.Events()
		if err != nil {
			t.Fatal(err)
		}
		var input, output int64
		for _, e := range events {
			input += e.InputTokens
			output += e.OutputTokens
		}
		if input != 107 || output != 20 || len(events) != 2 {
			t.Fatalf("live reconciliation double-counted or deleted archive: %+v", events)
		}
	}
	scan()
	check()
	shutdown := `{"type":"session.shutdown","id":"end","timestamp":"2026-11-15T01:01:00Z","data":{"modelMetrics":{"m":{"requests":{"count":1},"usage":{"inputTokens":100,"outputTokens":20}}}}}` + "\n"
	if err := os.WriteFile(state, []byte(shutdown), 0600); err != nil {
		t.Fatal(err)
	}
	scan()
	check()
	record := `{"type":"session.usage_record","id":"record","timestamp":"2026-11-15T01:00:00Z","data":{"usage":{"model":"m","inputTokens":100,"outputTokens":20,"apiCallId":"api","serviceRequestId":"service"}}}` + "\n"
	if err := os.WriteFile(state, []byte(record+shutdown), 0600); err != nil {
		t.Fatal(err)
	}
	scan()
	check()
}

func TestSupersededSources(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "events.db"))
	old := event.Event{Agent: "antigravity", ID: "old", Source: "/gone.db", Session: "s", InputTokens: 7}
	if _, err := s.Save([]Parsed{{Source: collect.Source{Path: old.Source}, Result: collect.Result{Events: []event.Event{old}}}}); err != nil {
		t.Fatal(err)
	}
	new := event.Event{Agent: "antigravity", ID: "new", Source: "/live.db", Session: "s", InputTokens: 10}
	if _, err := s.Save([]Parsed{{Source: collect.Source{Path: new.Source}, Result: collect.Result{Events: []event.Event{new}}}}); err != nil {
		t.Fatal(err)
	}
	if events := eventsByID(t, s); len(events) != 2 {
		t.Fatalf("replacement removed archived source: %+v", events)
	}
}

func TestEqualCopyPreferenceIsStable(t *testing.T) {
	s := openTest(t, filepath.Join(t.TempDir(), "events.db"))
	for _, source := range []string{"/z.jsonl", "/a.jsonl", "/z.jsonl"} {
		e := event.Event{Agent: "pi", ID: "same", Session: source, Time: 100, InputTokens: 10, Source: source}
		if _, err := s.Save([]Parsed{{Source: collect.Source{Path: source}, Result: collect.Result{Events: []event.Event{e}}}}); err != nil {
			t.Fatal(err)
		}
	}
	if e := eventsByID(t, s)["same"]; e.Source != "/a.jsonl" {
		t.Fatalf("equal copies oscillated: %+v", e)
	}
}
