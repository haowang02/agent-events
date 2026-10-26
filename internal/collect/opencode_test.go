package collect

import (
	"database/sql"
	"path/filepath"
	"testing"
)

// A database migrated from V1 keeps V1 messages next to V2 ones; a V2 fork
// copies its parent's history under new message IDs.
func TestParseOpenCodeDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "opencode.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range []string{
		`CREATE TABLE session (id TEXT PRIMARY KEY, parent_id TEXT, directory TEXT, version TEXT)`,
		`CREATE TABLE message (id TEXT PRIMARY KEY, session_id TEXT, time_created INTEGER, data TEXT)`,
		`CREATE TABLE session_v2 (id TEXT PRIMARY KEY, parent_id TEXT, fork_session_id TEXT, fork_boundary TEXT, directory TEXT, version TEXT)`,
		`CREATE TABLE session_message (id TEXT PRIMARY KEY, session_id TEXT, type TEXT, seq INTEGER, time_created INTEGER, data TEXT)`,
		`INSERT INTO session VALUES ('old', 'root', '/v1', '1.2.0')`,
		`INSERT INTO message VALUES ('u1', 'old', 1767312001000, '{"role":"user"}')`,
		`INSERT INTO message VALUES ('a1', 'old', 1767312001000, '{"role":"assistant","time":{"created":1767312001000,"completed":1767312004000},"modelID":"m1","providerID":"p1","agent":"build","tokens":{"input":10,"output":5,"reasoning":2,"cache":{"read":30,"write":4}},"error":{"name":"APIError","data":{"message":"overloaded","statusCode":529}}}')`,
		`INSERT INTO session_v2 VALUES ('parent', NULL, NULL, NULL, '/v2', '2.0.0')`,
		`INSERT INTO session_v2 VALUES ('fork', NULL, 'parent', '{"type":"through","messageID":"p2"}', '/v2', '2.0.0')`,
		`INSERT INTO session_message VALUES ('p1', 'parent', 'assistant', 1, 1767312002000, '{"model":{"id":"m2","providerID":"p2","variant":"high"},"time":{"created":1767312002000,"streamed":1767312002500,"completed":1767312009000},"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO session_message VALUES ('p2', 'parent', 'assistant', 2, 1767312003000, '{"model":{"id":"m2","providerID":"p2"},"time":{"created":1767312003000},"tokens":{"input":2,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO session_message VALUES ('f1', 'fork', 'assistant', 1, 1767312004000, '{"model":{"id":"m2","providerID":"p2"},"time":{"created":1767312002000},"tokens":{"input":1,"output":1,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO session_message VALUES ('f2', 'fork', 'assistant', 2, 1767312004000, '{"model":{"id":"m2","providerID":"p2"},"time":{"created":1767312003000},"tokens":{"input":2,"output":2,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO session_message VALUES ('f3', 'fork', 'assistant', 3, 1767312005000, '{"model":{"id":"m2","providerID":"p2"},"time":{"created":1767312005000},"tokens":{"input":3,"output":3,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
		`INSERT INTO session_message VALUES ('c1', 'fork', 'compaction', 4, 1767312006000, '{"status":"completed","model":{"id":"m3","providerID":"p2"},"time":{"created":1767312006000},"tokens":{"input":40,"output":4,"reasoning":0,"cache":{"read":0,"write":0}}}')`,
	} {
		if _, err := db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	events, err := parseOpenCodeDatabase("opencode", path)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]int{}
	for i, e := range events {
		byID[e.ID] = i
	}
	for _, id := range []string{"a1", "p1", "p2", "f3", "c1"} {
		if _, ok := byID[id]; !ok {
			t.Fatalf("missing %s in %+v", id, events)
		}
	}
	if len(events) != 5 {
		t.Fatalf("got %d events, want 5 without the fork's copies: %+v", len(events), events)
	}
	v1 := events[byID["a1"]]
	if v1.Kind != "subagent" || v1.Project != "/v1" || v1.Version != "1.2.0" || v1.Client != "build" || v1.LatencyMS != 3000 ||
		v1.OutputTokens != 7 || v1.CacheReadTokens != 30 || !v1.Failed || v1.Error != "HTTP 529 APIError\noverloaded" {
		t.Errorf("V1 message: %+v", v1)
	}
	v2 := events[byID["p1"]]
	if v2.Kind != "main" || v2.Model != "m2" || v2.Provider != "p2" || v2.Effort != "high" || v2.LatencyMS != 500 {
		t.Errorf("V2 message timed until streamed: %+v", v2)
	}
	if compaction := events[byID["c1"]]; compaction.Kind != "compaction" || compaction.Model != "m3" || compaction.InputTokens != 40 {
		t.Errorf("compaction: %+v", compaction)
	}
}
