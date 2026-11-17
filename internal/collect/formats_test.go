package collect

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"
)

func TestGrokFallbackIdentity(t *testing.T) {
	row := func(input, read int) string {
		return fmt.Sprintf(`{"timestamp":1794704400,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"grok-4.5":{"inputTokens":%d,"cachedReadTokens":%d,"outputTokens":20}}}}}}`, input, read)
	}
	events, err := eventsOnly(parseGrok(writeFile(t, "updates.jsonl", row(100, 10), row(110, 20))))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].ID == events[1].ID {
		t.Fatalf("different cache usage identifies different fallback turns: %+v", events)
	}
}

func TestAgentJSONFormats(t *testing.T) {
	cases := []struct {
		name, path, raw                       string
		parse                                 func(string) (Result, error)
		input, read, write, output, reasoning int64
	}{
		{"amp ledger", "T-test.json", `{"id":"T-test","messages":[null,3,{"role":"assistant","messageId":0,"usage":{"cacheReadInputTokens":30,"cacheCreationInputTokens":5}}],"usageLedger":{"events":[null,false,{"id":1,"timestamp":"2026-11-15T01:00:00Z","model":"m","toMessageId":0,"tokens":{"input":10,"output":20,"total":70}}]}}`, parseAmp, 10, 30, 5, 25, 0},
		{"amp message", "T-test.json", `{"id":"T-test","messages":[null,"bad",{"role":"assistant","messageId":1,"timestamp":"2026-11-15T01:00:00Z","model":"m","usage":{"inputTokens":10,"outputTokens":20,"cacheReadInputTokens":30,"cacheCreationInputTokens":5}}]}`, parseAmp, 10, 30, 5, 20, 0},
		{"codebuff", "manicode/projects/p/chats/2026-11-15T01-00-00Z/chat-messages.json", `[{"role":"assistant","metadata":{"model":"m","usage":{"input_tokens":10,"output_tokens":20},"codebuff":{"usage":{"cache_read_input_tokens":30,"cache_creation_input_tokens":5}}}}]`, parseCodebuff, 10, 30, 5, 20, 0},
		{"droid", "test.settings.json", `{"model":"custom: Claude Opus 4.5 [local]","providerLockTimestamp":"2026-11-15T01:00:00Z","tokenUsage":{"inputTokens":10,"outputTokens":20,"cacheReadTokens":30,"cacheCreationTokens":5,"thinkingTokens":7}}`, parseDroid, 10, 30, 5, 27, 7},
		{"qwen inclusive cache", "projects/p/chats/s.jsonl", `{"type":"assistant","uuid":"q","timestamp":"2026-11-15T01:00:00Z","model":"m","usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":5,"cachedContentTokenCount":80,"totalTokenCount":115}}`, parseQwen, 20, 80, 0, 15, 5},
		{"qwen total only", "projects/p/chats/s.jsonl", `{"type":"assistant","usageMetadata":{"totalTokenCount":90}}`, parseQwen, 0, 0, 0, 90, 90},
		{"gemini direct", "direct.json", `{"type":"gemini","session_id":"s","created_at":"2026-11-15T01:00:00Z","model":"m","tokens":{"input_tokens":100,"output_tokens":10,"cached_tokens":80,"reasoning_tokens":5,"total_tokens":115}}`, parseGeminiChat, 20, 80, 0, 15, 5},
		{"gemini flat result", "result.json", `{"model":"m","result":{"stats":{"input":100,"output":10,"cached":80,"thoughts":5,"total":115}}}`, parseGeminiChat, 20, 80, 0, 15, 5},
		{"gemini model stats", "stats.json", `{"stats":{"models":{"m":{"tokens":{"prompt":100,"candidates":10,"cached":80,"thoughts":5}}}}}`, parseGeminiChat, 20, 80, 0, 15, 5},
		{"gemini rewritten message", "s.jsonl", "{\"type\":\"gemini\",\"id\":\"m1\",\"model\":\"m\",\"tokens\":{\"total\":5}}\n{\"type\":\"gemini\",\"id\":\"m1\",\"model\":\"m\",\"tokens\":{\"total\":90}}", parseGeminiLog, 0, 0, 0, 90, 90},
		{"kimi CLI total", "sessions/g/s/wire.jsonl", `{"timestamp":1794704400,"message":{"type":"StatusUpdate","payload":{"message_id":"m","token_usage":{"input_other":10,"output":20,"input_cache_creation":5,"input_cache_read":30,"total":70}}}}`, parseKimi, 10, 30, 5, 25, 0},
		{"kimi Code turn", "sessions/g/s/agents/a/wire.jsonl", `{"type":"usage.record","time":1794704400000,"usageScope":"turn","usage":{"inputOther":10,"output":20,"inputCacheRead":30,"inputCacheCreation":5}}`, parseKimi, 10, 30, 5, 20, 0},
		{"pi total fallback", "pi.jsonl", `{"type":"message","timestamp":"2026-11-15T01:00:00Z","message":{"role":"assistant","model":"m","usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":5,"totalTokens":70}}}`, func(p string) (Result, error) { return parsePiFile("pi", p) }, 10, 30, 5, 25, 0},
		{"openclaw", "s.jsonl.reset.123", `{"type":"message","timestamp":"2026-11-15T01:00:00Z","message":{"role":"assistant","model":"m","usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":5}}}`, func(p string) (Result, error) { return parsePiFile("openclaw", p) }, 10, 30, 5, 20, 0},
		{"grok model turn", "grok/s/updates.jsonl", `{"timestamp":1794704400,"params":{"sessionId":"s","update":{"sessionUpdate":"turn_completed","usage":{"modelUsage":{"[grok] grok-4.5-build":{"inputTokens":100,"outputTokens":20,"cachedReadTokens":30,"cacheCreationTokens":5,"reasoningTokens":7}}}},"_meta":{"eventId":"turn"}}}`, parseGrok, 65, 30, 5, 20, 7},
		{"opencode total fallback", "storage/message/s/m.json", `{"id":"m","sessionID":"s","role":"assistant","modelID":"m","time":{"created":1794704400000},"tokens":{"input":10,"output":20,"reasoning":7,"cache":{"read":30,"write":5},"total":80}}`, func(p string) (Result, error) { return parseOpenCodeMessageFile("opencode", p) }, 10, 30, 5, 35, 7},
		{"kilo total fallback", "storage/message/s/m.json", `{"id":"m","sessionID":"s","role":"assistant","modelID":"m","time":{"created":1794704400000},"tokens":{"input":10,"output":20,"reasoning":7,"cache":{"read":30,"write":5},"total":80}}`, func(p string) (Result, error) { return parseOpenCodeMessageFile("kilo", p) }, 10, 30, 5, 35, 7},
		{"amp unlinked cache", "T-test.json", `{"id":"T-test","messages":[{"role":"assistant","usage":{"cacheReadInputTokens":30}}],"usageLedger":{"events":[{"id":1,"timestamp":"2026-11-15T01:00:00Z","model":"m","tokens":{"input":10,"output":2}}]}}`, parseAmp, 10, 0, 0, 2, 0},
		{"codebuff cached prompt", "manicode/projects/p/chats/c/chat-messages.json", `[{"role":"assistant","metadata":{"model":"m","usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":80},"total_tokens":120}}}]`, parseCodebuff, 20, 80, 0, 20, 0},
		{"codebuff partial total", "manicode/projects/p/chats/c/chat-messages.json", `[{"role":"assistant","metadata":{"model":"m","usage":{"total_tokens":100},"codebuff":{"usage":{"inputTokens":20,"outputTokens":30,"cacheReadInputTokens":10}}}}]`, parseCodebuff, 20, 10, 0, 70, 0},
		{"codebuff explicit zero", "manicode/projects/p/chats/c/chat-messages.json", `[{"role":"assistant","metadata":{"model":"m","usage":{"input_tokens":0,"cache_read_input_tokens":100,"output_tokens":20},"codebuff":{"usage":{"inputTokens":9,"outputTokens":30,"cacheReadInputTokens":0}}}}]`, parseCodebuff, 0, 100, 0, 20, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeFile(t, tc.path, tc.raw)
			events, err := eventsOnly(tc.parse(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("got %d events: %+v", len(events), events)
			}
			e := events[0]
			if e.InputTokens != tc.input || e.CacheReadTokens != tc.read || e.CacheWriteTokens != tc.write || e.OutputTokens != tc.output || e.ReasoningTokens != tc.reasoning || e.Time <= 0 {
				t.Fatalf("unexpected token buckets/time: %+v", e)
			}
		})
	}
}

func TestAgentSQLiteFormats(t *testing.T) {
	cases := []struct {
		name, schema, insert                      string
		parse                                     func(string) (Result, error)
		input, read, write, output, reasoning, at int64
	}{
		{"hermes", `CREATE TABLE sessions(id TEXT,model TEXT,started_at REAL,input_tokens INTEGER,output_tokens INTEGER,cache_read_tokens INTEGER,cache_write_tokens INTEGER,reasoning_tokens INTEGER)`, `INSERT INTO sessions VALUES(NULL,'m',1794704400.5,999,999,0,0,0),('s','m',1794704400.5,10,20,30,5,7)`, parseHermes, 10, 30, 5, 27, 7, 1794704400500},
		{"goose", `CREATE TABLE sessions(id TEXT,model_config_json TEXT,created_at TEXT,input_tokens INTEGER,output_tokens INTEGER,total_tokens INTEGER,accumulated_input_tokens INTEGER,accumulated_output_tokens INTEGER,accumulated_total_tokens INTEGER)`, `INSERT INTO sessions VALUES(NULL,'{"model_name":"m"}','2026-11-15',1,2,3,999,999,1998),('s','{"model_name":"m"}','2026-11-15 01:00:00',1,2,3,10,20,37)`, parseGoose, 10, 0, 0, 27, 7, 1794704400000},
		{"zcode", `CREATE TABLE model_usage(id TEXT,session_id TEXT,started_at INTEGER,model_id TEXT,status TEXT,input_tokens INTEGER,output_tokens INTEGER,cache_read_input_tokens INTEGER,cache_creation_input_tokens INTEGER,computed_total_tokens INTEGER)`, `INSERT INTO model_usage VALUES(NULL,'s',1794704400000,'m','completed',999,999,0,0,0),('a','s',1794704400000,'m','completed',100,20,30,5,127),('b','s',1794704400000,'m','running',999,999,0,0,0)`, parseZCode, 65, 30, 5, 27, 0, 1794704400000},
		{"openclaw store", `CREATE TABLE transcript_events(seq INTEGER,session_id TEXT,event_json TEXT,created_at INTEGER)`, `INSERT INTO transcript_events VALUES(1,'s','{"type":"message","message":{"role":"assistant","model":"m","usage":{"input":10,"output":20,"cacheRead":30,"cacheWrite":5,"totalTokens":70}}}',1794704400000)`, parseOpenClawStore, 10, 30, 5, 25, 0, 1794704400000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "test.db")
			db, err := sql.Open("sqlite", path)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			if _, err = db.Exec(tc.schema + ";" + tc.insert); err != nil {
				t.Fatal(err)
			}
			events, err := eventsOnly(tc.parse(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 {
				t.Fatalf("unexpected events: %+v", events)
			}
			e := events[0]
			if e.InputTokens != tc.input || e.CacheReadTokens != tc.read || e.CacheWriteTokens != tc.write || e.OutputTokens != tc.output || e.ReasoningTokens != tc.reasoning || e.Time != tc.at {
				t.Fatalf("unexpected buckets: %+v", e)
			}
		})
	}
}

func TestKimiAgentIdentity(t *testing.T) {
	raw := `{"type":"usage.record","time":1794704400000,"usageScope":"turn","usage":{"inputOther":10,"output":20}}`
	a, err := eventsOnly(parseKimi(writeFile(t, "sessions/g/s/agents/a/wire.jsonl", raw)))
	if err != nil {
		t.Fatal(err)
	}
	b, err := eventsOnly(parseKimi(writeFile(t, "sessions/g/s/agents/b/wire.jsonl", raw)))
	if err != nil {
		t.Fatal(err)
	}
	if len(a) != 1 || len(b) != 1 || a[0].ID == b[0].ID || a[0].Session != b[0].Session {
		t.Fatalf("distinct subagent calls: %+v %+v", a, b)
	}
}
