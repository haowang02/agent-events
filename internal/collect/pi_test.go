package collect

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/haowang02/agent-events/internal/event"
)

func TestPiIndependentRequests(t *testing.T) {
	for _, agent := range []string{"pi", "openclaw"} {
		t.Run(agent, func(t *testing.T) {
			call := func(id string) string {
				return fmt.Sprintf(`{"type":"message","id":%q,"timestamp":"2026-11-15T01:00:00Z","message":{"role":"assistant","model":"m","usage":{"input":10,"output":2}}}`, id)
			}
			a, err := eventsOnly(parsePiFile(agent, writeFile(t, "a.jsonl", `{"type":"session","id":"a","timestamp":"2026-11-15T00:00:00Z"}`, call("one"), call("two"))))
			if err != nil {
				t.Fatal(err)
			}
			b, err := eventsOnly(parsePiFile(agent, writeFile(t, "b.jsonl", `{"type":"session","id":"b","timestamp":"2026-11-15T00:00:00Z"}`, call("one"))))
			if err != nil {
				t.Fatal(err)
			}
			if len(a) != 2 || len(b) != 1 || a[0].ID == a[1].ID || a[0].ID == b[0].ID {
				t.Fatalf("equal usage/time cannot identify unrelated requests: a=%+v b=%+v", a, b)
			}
		})
	}
}

func TestPiNullModelSource(t *testing.T) {
	path := writeFile(t, "s.jsonl", `{"type":"model_change","data":null,"modelId":"new","provider":"provider"}`, `{"type":"message","timestamp":"2026-11-15T01:00:00Z","message":{"role":"assistant","usage":{"input":10,"output":2}}}`)
	events, err := eventsOnly(parsePiFile("openclaw", path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Model != "new" || events[0].Provider != "provider" {
		t.Fatalf("null data must not discard root model metadata: %+v", events)
	}
}

func TestOpenClawArchivedDiscovery(t *testing.T) {
	root := t.TempDir()
	t.Setenv("OPENCLAW_DIR", root)
	for _, name := range []string{"a.jsonl", "b.jsonl.deleted.123", "c.jsonl.reset.123", "ignore.jsonl.bak"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := openClaw().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 3 {
		t.Fatalf("archives discovery: %+v", sources)
	}
}

func piHeader(id, parent string) string {
	extra := ""
	if parent != "" {
		extra = fmt.Sprintf(`,"parentSession":%q`, parent)
	}
	return fmt.Sprintf(`{"type":"session","id":%q,"timestamp":"2026-11-15T23:00:00Z"%s}`, id, extra)
}

func piCall(id, parent string, minute int) string {
	encoded := "null"
	if parent != "" {
		encoded = fmt.Sprintf("%q", parent)
	}
	return fmt.Sprintf(`{"type":"message","id":%q,"parentId":%s,"timestamp":"2026-11-15T01:%02d:00Z","message":{"role":"assistant","model":"m","usage":{"input":10,"output":2}}}`, id, encoded, minute)
}

func piWrite(t *testing.T, root, name string, rows ...string) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, []byte(strings.Join(rows, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPiReplayActiveBranchAndNestedFork(t *testing.T) {
	root := t.TempDir()
	parent := piWrite(t, root, "parent.jsonl", piHeader("parent", ""), piCall("a", "", 1), piCall("abandoned", "a", 2), piCall("chosen", "a", 3))
	child := piWrite(t, root, "child.jsonl", piHeader("child", parent), piCall("copy-a", "", 1), piCall("copy-chosen", "copy-a", 3), piCall("own", "copy-chosen", 4))
	grand := piWrite(t, root, "grand.jsonl", piHeader("grand", "child.jsonl"), piCall("g-a", "", 1), piCall("g-chosen", "g-a", 3), piCall("g-own", "g-chosen", 4), piCall("g-new", "g-own", 5))
	a, err := eventsOnly(parsePiFile("pi", parent))
	if err != nil {
		t.Fatal(err)
	}
	b, err := eventsOnly(parsePiFile("pi", child))
	if err != nil {
		t.Fatal(err)
	}
	c, err := eventsOnly(parsePiFile("pi", grand))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 3 || len(b) != 3 || len(c) != 4 {
		t.Fatalf("all billable calls remain available: %d/%d/%d", len(a), len(b), len(c))
	}
	if b[0].ID != a[0].ID || b[1].ID != a[2].ID || c[0].ID != a[0].ID || c[1].ID != a[2].ID || c[2].ID != b[2].ID {
		t.Fatalf("fork copies must inherit the active ancestors' identity: %+v %+v %+v", a, b, c)
	}
	ids := map[string]bool{}
	for _, events := range [][]event.Event{a, b, c} {
		for _, e := range events {
			ids[e.ID] = true
		}
	}
	if len(ids) != 5 {
		t.Fatalf("5 unique calls including the abandoned branch, got %d", len(ids))
	}
}

func TestPiReplayInvalidAncestryKeepsCalls(t *testing.T) {
	for _, kind := range []string{"missing-parent", "invalid-links", "cycle"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			childPath := filepath.Join(root, "child.jsonl")
			header := piHeader("parent", "")
			if kind == "cycle" {
				header = piHeader("parent", childPath)
			}
			parent := piWrite(t, root, "parent.jsonl", header, piCall("original", "", 1))
			if kind == "missing-parent" {
				parent = filepath.Join(root, "missing.jsonl")
			}
			copied := piCall("copy", "", 1)
			if kind == "invalid-links" {
				copied = piCall("copy", "missing-node", 1)
			}
			child := piWrite(t, root, "child.jsonl", piHeader("child", parent), copied)
			events, err := eventsOnly(parsePiFile("pi", child))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || !strings.HasPrefix(events[0].ID, "child:") {
				t.Fatalf("unproven replay must keep its own identity: %+v", events)
			}
		})
	}
}

func TestOpenClawEventIdentityAcrossSQLiteAndJSON(t *testing.T) {
	raw := piCall("stable-event", "", 1)
	events, err := eventsOnly(parsePiFile("openclaw", writeFile(t, "s.jsonl", raw)))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "store.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE transcript_events(seq INTEGER,session_id TEXT,event_json TEXT); INSERT INTO transcript_events VALUES(1,'s',?)`, raw); err != nil {
		t.Fatal(err)
	}
	migrated, err := eventsOnly(parseOpenClawStore(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || len(migrated) != 1 || events[0].ID != migrated[0].ID {
		t.Fatalf("migration copies must share event identity: %+v %+v", events, migrated)
	}
}
