package collect

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGeminiIndependentResults(t *testing.T) {
	path := writeFile(t, "results.jsonl",
		`{"type":"result","sessionId":"a","timestamp":"2026-11-15T01:00:00Z","model":"m","stats":{"input":10,"output":2}}`,
		`{"type":"result","sessionId":"b","timestamp":"2026-11-15T02:00:00Z","model":"m","result":{"stats":{"input":20,"output":3}}}`,
	)
	events, err := eventsOnly(parseGeminiLog(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].InputTokens+events[1].InputTokens != 30 {
		t.Fatalf("result records cannot overwrite earlier runs: %+v", events)
	}
}

func TestQwenArchive(t *testing.T) {
	root := t.TempDir()
	t.Setenv("QWEN_DATA_DIR", root)
	dir := filepath.Join(root, "projects", "p", "chats", "archive")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "s.jsonl")
	if err := os.WriteFile(path, []byte(`{"type":"assistant","timestamp":"2026-11-15T01:00:00Z","model":"m","usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":2}}`), 0644); err != nil {
		t.Fatal(err)
	}

	for _, relative := range []string{"projects/p/other/chats", "projects/p/chats/deep"} {
		invalid := filepath.Join(root, relative)
		if err := os.MkdirAll(invalid, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(invalid, "ignore.jsonl"), []byte(`{}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := qwen().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 1 {
		t.Fatalf("archived chats must remain discoverable: %+v", sources)
	}
	events, err := eventsOnly(parseQwen(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Project != "p" || events[0].Session != "p-s" {
		t.Fatalf("archive location must preserve project and session identity: %+v", events)
	}
}

func TestGeminiAnonymousSourceIdentity(t *testing.T) {
	raw := `{"type":"gemini","sessionId":"s","model":"m","timestamp":"2026-11-15T01:00:00Z","tokens":{"input":10,"output":2}}`
	a, err := eventsOnly(parseGeminiChat(writeFile(t, "first.json", raw)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := eventsOnly(parseGeminiChat(writeFile(t, "second.json", raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 || a[0].ID == b[0].ID {
		t.Fatalf("anonymous records need their source identity: %+v %+v", a, b)
	}
}
