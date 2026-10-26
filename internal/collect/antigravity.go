package collect

import (
	"cmp"
	"database/sql"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/haowang02/agent-events/internal/event"
)

// antigravity collects every Antigravity edition. A configured root may hold
// a conversations directory or be one.
func antigravity() Collector {
	var patterns []string
	if os.Getenv("ANTIGRAVITY_DATA_DIR") != "" {
		for _, root := range roots("ANTIGRAVITY_DATA_DIR") {
			patterns = append(patterns, filepath.Join(root, "conversations", "*.db"), filepath.Join(root, "*.db"))
		}
	} else {
		for _, root := range []string{
			homeDir(".gemini", "antigravity"), homeDir(".gemini", "antigravity-cli"), homeDir(".gemini", "antigravity-ide"),
			homeDir(".gemini", "antigravity-backup"), homeDir(".gemini", "antigravity-acp"), homeDir(".config", "antigravity"),
		} {
			patterns = append(patterns, filepath.Join(root, "conversations", "*.db"))
		}
	}
	return Collector{
		Agent: "antigravity",
		List:  func() ([]Source, error) { return globDatabases("antigravity", patterns...) },
		Parse: parseAntigravity,
	}
}

// Antigravity keeps one SQLite database per conversation with protobuf blobs
// and no published schema. Field numbers below were read from its data and
// match Codeium's ModelUsageStats layout.
const (
	antigravityGenRequest       = 1  // gen_metadata: the request
	antigravityGenUsage         = 4  // request: ModelUsageStats
	antigravityGenLatency       = 11 // request: google.protobuf.Duration
	antigravityGenModel         = 19 // request: model name
	antigravityGenLabels        = 20 // request: repeated {1: key, 2: value}
	antigravityUsageModel       = 1  // usage: model enum
	antigravityUsageInput       = 2  // usage: uncached input
	antigravityUsageOutput      = 3  // usage: thinking + response output
	antigravityUsageCacheWrite  = 4
	antigravityUsageCacheRead   = 5
	antigravityUsageMessageID   = 7
	antigravityUsageThinking    = 9
	antigravityStepCreated      = 1  // step metadata: google.protobuf.Timestamp
	antigravityStepUsage        = 9  // step metadata: ModelUsageStats
	antigravityStepModel        = 11 // step metadata: model enum
	antigravityStepErrorMessage = 2  // error_details: message
	antigravityStepTypeResponse = 15 // steps.step_type of a model response
	antigravityWorkspace        = 1  // trajectory metadata: {1: workspace URI}
)

func pbEach(message []byte, fn func(number protowire.Number, kind protowire.Type, value []byte)) {
	for len(message) > 0 {
		number, kind, n := protowire.ConsumeTag(message)
		if n < 0 {
			return
		}
		message = message[n:]
		n = protowire.ConsumeFieldValue(number, kind, message)
		if n < 0 {
			return
		}
		fn(number, kind, message[:n])
		message = message[n:]
	}
}

func pbRepeated(message []byte, number protowire.Number) [][]byte {
	var values [][]byte
	pbEach(message, func(field protowire.Number, kind protowire.Type, value []byte) {
		if field == number && kind == protowire.BytesType {
			bytes, _ := protowire.ConsumeBytes(value)
			values = append(values, bytes)
		}
	})
	return values
}

func pbBytes(message []byte, number protowire.Number) []byte {
	values := pbRepeated(message, number)
	if len(values) == 0 {
		return nil
	}
	return values[len(values)-1]
}

func pbVarint(message []byte, number protowire.Number) int64 {
	var result uint64
	pbEach(message, func(field protowire.Number, kind protowire.Type, value []byte) {
		if field == number && kind == protowire.VarintType {
			result, _ = protowire.ConsumeVarint(value)
		}
	})
	return int64(result)
}

// pbMillis converts a Timestamp or Duration message to milliseconds.
func pbMillis(message []byte) int64 {
	return pbVarint(message, 1)*1000 + pbVarint(message, 2)/1_000_000
}

type antigravityStep struct {
	created int64
	usage   []byte
	model   int64
	error   string
}

// antigravitySteps returns model response steps by index, and their indexes
// by the message ID of their usage.
func antigravitySteps(db *sql.DB) (map[int64]antigravityStep, map[string]int64, error) {
	rows, err := db.Query("SELECT idx, metadata, error_details FROM steps WHERE step_type = ?", antigravityStepTypeResponse)
	if err != nil {
		return nil, nil, err
	}
	steps := map[int64]antigravityStep{}
	byMessage := map[string]int64{}
	err = scanRows(rows, func() error {
		var index int64
		var metadata, details []byte
		if err := rows.Scan(&index, &metadata, &details); err != nil {
			return err
		}
		step := antigravityStep{
			created: pbMillis(pbBytes(metadata, antigravityStepCreated)),
			usage:   pbBytes(metadata, antigravityStepUsage),
			model:   pbVarint(metadata, antigravityStepModel),
			error:   string(pbBytes(details, antigravityStepErrorMessage)),
		}
		steps[index] = step
		if id := string(pbBytes(step.usage, antigravityUsageMessageID)); id != "" {
			byMessage[id] = index
		}
		return nil
	})
	return steps, byMessage, err
}

// parseAntigravity reads gen_metadata, one row per model request, and dates
// each request by its response step. Failed responses have no row there.
func parseAntigravity(path string) ([]event.Event, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	steps, stepsByMessage, err := antigravitySteps(db)
	if err != nil {
		return nil, err
	}
	conversation := strings.TrimSuffix(filepath.Base(path), ".db")
	client := filepath.Base(filepath.Dir(filepath.Dir(path)))
	var trajectory []byte
	_ = db.QueryRow("SELECT data FROM trajectory_metadata_blob LIMIT 1").Scan(&trajectory)
	project := strings.TrimPrefix(string(pbBytes(pbBytes(trajectory, antigravityWorkspace), 1)), "file://")
	newEvent := func(id, model string, usage []byte) event.Event {
		return event.Event{
			Agent: "antigravity", ID: id, Model: model, Session: conversation, Project: project, Client: client, Source: path,
			InputTokens:      pbVarint(usage, antigravityUsageInput),
			CacheReadTokens:  pbVarint(usage, antigravityUsageCacheRead),
			CacheWriteTokens: pbVarint(usage, antigravityUsageCacheWrite),
			OutputTokens:     pbVarint(usage, antigravityUsageOutput),
			ReasoningTokens:  pbVarint(usage, antigravityUsageThinking),
		}
	}

	rows, err := db.Query("SELECT idx, data FROM gen_metadata ORDER BY idx")
	if err != nil {
		return nil, err
	}
	var events []event.Event
	modelNames := map[int64]string{}
	responded := map[int64]bool{}
	err = scanRows(rows, func() error {
		var index int64
		var data []byte
		if err := rows.Scan(&index, &data); err != nil {
			return err
		}
		request := pbBytes(data, antigravityGenRequest)
		usage := pbBytes(request, antigravityGenUsage)
		if usage == nil {
			return nil
		}
		labels := map[string]string{}
		for _, label := range pbRepeated(request, antigravityGenLabels) {
			labels[string(pbBytes(label, 1))] = string(pbBytes(label, 2))
		}
		model := string(pbBytes(request, antigravityGenModel))
		modelNames[pbVarint(usage, antigravityUsageModel)] = model
		stepIndex, ok := stepsByMessage[string(pbBytes(usage, antigravityUsageMessageID))]
		if !ok {
			// The response step follows the last step the request was built from.
			last, err := strconv.ParseInt(labels["last_step_index"], 10, 64)
			stepIndex, ok = last+1, err == nil
		}
		step, found := steps[stepIndex]
		if !ok || !found {
			return nil
		}
		responded[stepIndex] = true
		e := newEvent(cmp.Or(labels["request_id"], conversation+":"+strconv.FormatInt(index, 10)), model, usage)
		e.Time = step.created
		e.LatencyMS = pbMillis(pbBytes(request, antigravityGenLatency))
		events = append(events, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index, step := range steps {
		if responded[index] || step.error == "" {
			continue
		}
		e := newEvent(conversation+":step:"+strconv.FormatInt(index, 10), modelNames[step.model], step.usage)
		e.Time = step.created
		e.Failed = true
		e.Error = step.error
		events = append(events, e)
	}
	return events, nil
}
