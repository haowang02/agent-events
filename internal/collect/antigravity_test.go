package collect

import (
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/haowang02/agent-events/internal/pricing"
)

func pbStringField(num protowire.Number, s string) []byte {
	var b []byte
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendString(b, s)
}

func pbBytesField(num protowire.Number, v []byte) []byte {
	var b []byte
	b = protowire.AppendTag(b, num, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}

func pbVarintField(num protowire.Number, v uint64) []byte {
	var b []byte
	b = protowire.AppendTag(b, num, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}

func TestParseAntigravity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "antigravity-cli", "conversations", "conv1.db")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	_, err = db.Exec(`
		CREATE TABLE steps (idx integer PRIMARY KEY, step_type integer, metadata blob, error_details blob);
		CREATE TABLE gen_metadata (idx integer PRIMARY KEY, data blob);
		CREATE TABLE trajectory_metadata_blob (id text PRIMARY KEY, data blob);
	`)
	if err != nil {
		t.Fatal(err)
	}

	first := append(pbVarintField(1, 2), pbVarintField(2, 193000000)...)
	streaming := append(pbVarintField(1, 37), pbVarintField(2, 627000000)...)
	cases := []struct {
		idx         int64
		msgID       string
		model       string
		displayName string
		wantEffort  string
		first       []byte
		streaming   []byte
		wantTTFT    int64
		wantLatency int64
	}{
		{0, "m0", "gemini-4-hydrogen", "Gemini 4 Hydrogen (Medium)", "medium", first, streaming, 2193, 39820},
		{1, "m1", "claude-opus-5-5-high", "", "high", first, nil, 2193, 0},
		{2, "m2", "gemini-2.5-pro", "Gemini 2.5 Pro (Preview)", "", nil, streaming, 0, 0},
		{3, "m3", "gemini-4-hydrogen", "", "", first, []byte{}, 2193, 2193},
	}

	for _, tc := range cases {
		var usage []byte
		usage = append(usage, pbVarintField(antigravityUsageModel, uint64(tc.idx+1))...)
		usage = append(usage, pbVarintField(antigravityUsageInput, 10)...)
		usage = append(usage, pbVarintField(antigravityUsageOutput, 20)...)
		usage = append(usage, pbStringField(antigravityUsageMessageID, tc.msgID)...)

		var ts []byte
		ts = append(ts, pbVarintField(1, 1794736900+uint64(tc.idx))...)
		var stepMeta []byte
		stepMeta = append(stepMeta, pbBytesField(antigravityStepCreated, ts)...)
		stepMeta = append(stepMeta, pbBytesField(antigravityStepUsage, usage)...)
		if _, err := db.Exec(`INSERT INTO steps (idx, step_type, metadata) VALUES (?, ?, ?)`, tc.idx*2+1, antigravityStepTypeResponse, stepMeta); err != nil {
			t.Fatal(err)
		}

		var req []byte
		req = append(req, pbBytesField(antigravityGenUsage, usage)...)
		req = append(req, pbStringField(antigravityGenModel, tc.model)...)
		if tc.displayName != "" {
			req = append(req, pbStringField(antigravityGenDisplayName, tc.displayName)...)
		}
		if tc.first != nil {
			req = append(req, pbBytesField(antigravityGenTTFT, tc.first)...)
		}
		if tc.streaming != nil {
			req = append(req, pbBytesField(antigravityGenStreaming, tc.streaming)...)
		}
		gen := pbBytesField(antigravityGenRequest, req)
		if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data) VALUES (?, ?)`, tc.idx, gen); err != nil {
			t.Fatal(err)
		}
	}
	failedUsage := pbVarintField(antigravityUsageModel, 1)
	failedMeta := pbBytesField(antigravityStepCreated, pbVarintField(1, 1794736910))
	if _, err := db.Exec(`INSERT INTO steps (idx, step_type, metadata) VALUES (?, ?, ?)`, 10, antigravityStepTypeError, failedMeta); err != nil {
		t.Fatal(err)
	}
	failedRequest := pbBytesField(antigravityGenUsage, failedUsage)
	failedRequest = append(failedRequest, pbBytesField(antigravityGenAttempts, pbStringField(antigravityAttemptError, "API error"))...)
	label := append(pbStringField(1, "last_step_index"), pbStringField(2, "9")...)
	failedRequest = append(failedRequest, pbBytesField(antigravityGenLabels, label)...)
	if _, err := db.Exec(`INSERT INTO gen_metadata (idx, data) VALUES (?, ?)`, len(cases), pbBytesField(antigravityGenRequest, failedRequest)); err != nil {
		t.Fatal(err)
	}

	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != len(cases)+1 {
		t.Fatalf("got %d events, want %d", len(events), len(cases)+1)
	}
	for i, tc := range cases {
		if events[i].Effort != tc.wantEffort {
			t.Errorf("event %d (%s): got Effort=%q, want %q", i, tc.model, events[i].Effort, tc.wantEffort)
		}
		if events[i].TTFTMS != tc.wantTTFT || events[i].LatencyMS != tc.wantLatency {
			t.Errorf("event %d: got TTFT=%d latency=%d, want %d and %d", i, events[i].TTFTMS, events[i].LatencyMS, tc.wantTTFT, tc.wantLatency)
		}
	}
	if failed := events[len(cases)]; !failed.Failed || failed.Error != "API error" || failed.Time != 1794736910000 || failed.Model != "gemini-4-hydrogen" {
		t.Errorf("failed request: %+v", failed)
	}
}

func antigravityFixture(t *testing.T) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "conversations", "test.db")
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	_, err = db.Exec(`CREATE TABLE steps(idx INTEGER PRIMARY KEY,step_type INTEGER,metadata BLOB,error_details BLOB); CREATE TABLE gen_metadata(idx INTEGER PRIMARY KEY,data BLOB); CREATE TABLE trajectory_metadata_blob(data BLOB);`)
	if err != nil {
		t.Fatal(err)
	}
	return path, db
}

func agFields(parts ...[]byte) []byte {
	var b []byte
	for _, p := range parts {
		b = append(b, p...)
	}
	return b
}

func agUsage(model, input, output, read uint64, id string) []byte {
	return agFields(pbVarintField(1, model), pbVarintField(2, input), pbVarintField(3, output), pbVarintField(5, read), pbStringField(7, id))
}

func agInsert(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func agStep(t *testing.T, db *sql.DB, index, kind int, usage []byte, extra ...[]byte) {
	t.Helper()
	metadata := agFields(pbBytesField(1, pbVarintField(1, 1794745867)), pbBytesField(9, usage))
	metadata = agFields(append([][]byte{metadata}, extra...)...)
	agInsert(t, db, "INSERT INTO steps VALUES(?,?,?,NULL)", index, kind, metadata)
}

func agGen(t *testing.T, db *sql.DB, index int, usage []byte, extra ...[]byte) {
	t.Helper()
	request := agFields(append([][]byte{pbBytesField(4, usage)}, extra...)...)
	agInsert(t, db, "INSERT INTO gen_metadata VALUES(?,?)", index, pbBytesField(1, request))
}

func TestAntigravityCheckpointCost(t *testing.T) {
	path, db := antigravityFixture(t)
	agGen(t, db, 0, agUsage(1600, 0, 0, 0, ""), pbStringField(19, "gemini-4-hydrogen"))
	usage := agFields(agUsage(1600, 5833, 4058, 254330, "checkpoint"), pbVarintField(9, 309))
	agStep(t, db, 215, 23, usage, pbBytesField(28, pbBytesField(2, usage)))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("checkpoint and mirrored attempt: %+v", events)
	}
	e := events[0]
	rate := 0.075
	p := pricing.Price{Rates: pricing.Rates{Input: 1.5, Output: 7.5, CacheRead: &rate}}
	if e.Model != "gemini-4-hydrogen" || e.Failed || e.ReasoningTokens != 309 || math.Abs(p.Cost(&e).Total-0.05825925) > 1e-12 {
		t.Fatalf("checkpoint usage: %+v", e)
	}
}

func TestAntigravityRetriesAndMirrors(t *testing.T) {
	for _, keyed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unkeyed", true: "keyed"}[keyed], func(t *testing.T) {
			path, db := antigravityFixture(t)
			id := ""
			if keyed {
				id = "primary"
			}
			usage := agUsage(1318, 100, 20, 10, id)
			retry := agUsage(246, 50, 10, 5, "")
			if keyed {
				retry = agUsage(246, 50, 10, 5, "retry")
			}
			attempts := agFields(pbBytesField(17, pbBytesField(2, usage)), pbBytesField(17, agFields(pbBytesField(2, retry), pbStringField(3, "rate limited"))))
			request := agFields(pbBytesField(4, usage), pbStringField(19, "gemini-3.8-flash-high"), attempts)
			root := agFields(pbBytesField(1, request), pbBytesField(2, []byte{3}))
			agInsert(t, db, "INSERT INTO gen_metadata VALUES(0,?)", root)
			agStep(t, db, 3, 15, usage, pbBytesField(28, pbBytesField(2, usage)), pbBytesField(28, agFields(pbBytesField(2, retry), pbStringField(3, "rate limited"))))
			events, err := eventsOnly(parseAntigravity(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 2 {
				t.Fatalf("expected primary + retry once: %+v", events)
			}
			if events[0].InputTokens != 100 || events[1].InputTokens != 50 || events[1].Model != "gemini-2.5-pro" || !events[1].Failed || events[1].LatencyMS != 0 {
				t.Fatalf("retry model and usage: %+v", events)
			}
		})
	}
}

func TestAntigravityIdenticalUnkeyedAttempts(t *testing.T) {
	path, db := antigravityFixture(t)
	usage := agUsage(246, 10, 2, 0, "")
	agGen(t, db, 0, usage, pbStringField(19, "gemini-2.5-pro"), pbBytesField(17, pbBytesField(2, usage)), pbBytesField(17, pbBytesField(2, usage)))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 {
		t.Fatalf("one mirrored attempt, one distinct identical attempt: %+v", events)
	}
}

func TestAntigravityGenerationWithoutSteps(t *testing.T) {
	path, db := antigravityFixture(t)
	agInsert(t, db, "DROP TABLE steps")
	at := pbBytesField(9, pbBytesField(4, pbVarintField(1, 1794745800)))
	for i, effort := range []string{"low", "high"} {
		agGen(t, db, i, agFields(agUsage(1600, 10, 0, 0, effort), pbVarintField(9, 5), pbVarintField(10, 7)), pbStringField(19, "gemini-4-hydrogen"), pbStringField(21, "Gemini 4 Hydrogen ("+effort+")"), at)
	}
	agGen(t, db, 2, agUsage(0, 10, 2, 0, "continuation"), at)
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 || events[0].Time != 1794745800000 || events[0].OutputTokens != 12 || events[0].Effort != "low" || events[1].Effort != "high" || events[2].Model != "gemini-4-hydrogen" {
		t.Fatalf("generation fallback: %+v", events)
	}
}

func TestAntigravityBridgeIdentities(t *testing.T) {
	path, db := antigravityFixture(t)
	agStep(t, db, 0, 23, agUsage(246, 10, 2, 0, "message"))
	agStep(t, db, 1, 23, agFields(agUsage(246, 20, 2, 0, ""), pbStringField(11, "response")))
	agStep(t, db, 2, 23, agFields(agUsage(246, 10, 2, 0, "message"), pbStringField(11, "response")))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].InputTokens != 20 {
		t.Fatalf("bridged copies: %+v", events)
	}
}

func TestAntigravityEmptyAndMalformed(t *testing.T) {
	empty := filepath.Join(t.TempDir(), "empty.db")
	if err := os.WriteFile(empty, nil, 0644); err != nil {
		t.Fatal(err)
	}
	if events, err := eventsOnly(parseAntigravity(empty)); err != nil || len(events) != 0 {
		t.Fatalf("empty database: %+v %v", events, err)
	}
	path, db := antigravityFixture(t)
	agGen(t, db, 0, []byte{0x12, 0x05, 0x01}, pbStringField(19, "gemini-2.5-pro"))
	if _, err := eventsOnly(parseAntigravity(path)); err == nil {
		t.Fatal("malformed usage must fail the source rather than save partial counts")
	}
}

func TestAntigravityStepModelAndStartTime(t *testing.T) {
	path, db := antigravityFixture(t)
	usage := agUsage(1600, 10, 2, 0, "message")
	info := pbBytesField(24, pbStringField(12, "gemini-4-hydrogen"))
	agStep(t, db, 1, 23, usage, info, pbBytesField(8, pbVarintField(1, 1794745900)))
	// A tool error without model usage must not become a model request.
	agInsert(t, db, "INSERT INTO steps VALUES(2,132,?,?)", pbBytesField(1, pbVarintField(1, 1794745800)), pbStringField(2, "tool failed"))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Model != "gemini-4-hydrogen" || events[0].Time != 1794745867000 {
		t.Fatalf("step model and request start: %+v", events)
	}
}

func TestAntigravityDistinctRequestsSharingStep(t *testing.T) {
	path, db := antigravityFixture(t)
	label := pbBytesField(20, agFields(pbStringField(1, "last_step_index"), pbStringField(2, "0")))
	agGen(t, db, 0, agUsage(246, 10, 2, 0, "first"), label)
	agGen(t, db, 1, agUsage(246, 20, 3, 0, "second"), label)
	agStep(t, db, 1, 15, agUsage(246, 20, 3, 0, ""))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	var input int64
	for _, e := range events {
		input += e.InputTokens
	}
	if len(events) != 2 || input != 30 {
		t.Fatalf("distinct attempts sharing a step must not merge: %+v", events)
	}
}

func TestAntigravityModelAliases(t *testing.T) {
	for _, tc := range []struct{ name, model, effort string }{
		{"MODEL_PLACEHOLDER_M318", "gemini-3.8-flash-high", "high"},
		{"gemini-pro-agent", "gemini-3.1-pro", ""},
		{"Gemini 4 Hydrogen (Medium)", "gemini-4-hydrogen", "medium"},
		{"Claude Sonnet 4.5", "claude-sonnet-4-5", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, db := antigravityFixture(t)
			agGen(t, db, 0, agUsage(0, 10, 2, 0, "model"), pbStringField(19, tc.name))
			events, err := eventsOnly(parseAntigravity(path))
			if err != nil {
				t.Fatal(err)
			}
			if len(events) != 1 || events[0].Model != tc.model || events[0].Effort != tc.effort {
				t.Fatalf("alias model: %+v", events)
			}
		})
	}
}

func TestAntigravityRetiresFallbackWhenStepAppears(t *testing.T) {
	path, db := antigravityFixture(t)
	u := agUsage(246, 10, 2, 0, "")
	request := pbBytesField(1, pbBytesField(4, u))
	agInsert(t, db, "INSERT INTO gen_metadata VALUES(0,?)", agFields(request, pbBytesField(2, []byte{3})))
	first, err := eventsOnly(parseAntigravity(path))
	if err != nil || len(first) != 1 {
		t.Fatalf("first: %+v, %v", first, err)
	}
	agStep(t, db, 3, 15, u)
	second, err := parseAntigravity(path)
	if err != nil || len(second.Events) != 1 || !slices.Contains(second.Superseded, RecordRef{Agent: "antigravity", ID: first[0].ID, Source: path}) {
		t.Fatalf("new step did not retire generation fallback: %+v, %v", second, err)
	}
}

func TestAntigravityDiscoversConversationsCreatedAfterStartup(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ANTIGRAVITY_DATA_DIR", "")
	c := antigravity()
	path := filepath.Join(homeDir(".gemini", "antigravity"), "conversations", "new.db")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	sources, err := c.List()
	if err != nil || len(sources) != 1 || sources[0].Path != path {
		t.Fatalf("new Antigravity install was not discovered: %+v, %v", sources, err)
	}
}

func TestAntigravityUsageModel(t *testing.T) {
	path, db := antigravityFixture(t)
	agGen(t, db, 0, agUsage(246, 100, 20, 0, "actual"), pbVarintField(3, 1402), pbStringField(19, "claude-opus-5-5"))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Model != "gemini-2.5-pro" {
		t.Fatalf("usage.model must override a different requested model: %+v", events)
	}
}

func TestAntigravityRoot(t *testing.T) {
	root := t.TempDir()
	t.Setenv("ANTIGRAVITY_DATA_DIR", root)
	if err := os.MkdirAll(filepath.Join(root, "conversations", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"conversation_summaries.db", "conversations/one.db", "conversations/nested/two.db"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0644); err != nil {
			t.Fatal(err)
		}
	}
	sources, err := antigravity().List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sources) != 2 {
		t.Fatalf("only conversation databases, recursively: %+v", sources)
	}
	for _, source := range sources {
		if filepath.Base(source.Path) == "conversation_summaries.db" {
			t.Fatal("summary index must not be treated as a conversation")
		}
	}
}

func TestAntigravityUsageAlias(t *testing.T) {
	path, db := antigravityFixture(t)
	agGen(t, db, 0, agUsage(1187, 60, 10, 0, "alias"), pbStringField(19, "gemini-3-flash-agent"))
	events, err := eventsOnly(parseAntigravity(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Model != "gemini-3.5-flash-extra-low" {
		t.Fatalf("known usage enum takes precedence over a requested alias: %+v", events)
	}
}
