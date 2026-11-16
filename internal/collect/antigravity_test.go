package collect

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"google.golang.org/protobuf/encoding/protowire"
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

	events, err := parseAntigravity(path)
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
