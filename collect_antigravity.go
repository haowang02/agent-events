package main

import (
	"cmp"
	"database/sql"
	"path/filepath"
	"strconv"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

// Antigravity CLI (agy) keeps one SQLite database per conversation with
// protobuf blobs and no published schema. Field numbers below were read from
// its data and match Codeium's ModelUsageStats layout.
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

// readAntigravitySteps returns model response steps by index, and their indexes by
// the message ID of their usage.
func readAntigravitySteps(db *sql.DB) (map[int64]antigravityStep, map[string]int64, error) {
	rows, err := db.Query("SELECT idx, metadata, error_details FROM steps WHERE step_type = ?", antigravityStepTypeResponse)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	steps := map[int64]antigravityStep{}
	byMessage := map[string]int64{}
	for rows.Next() {
		var index int64
		var metadata, details []byte
		if err := rows.Scan(&index, &metadata, &details); err != nil {
			return nil, nil, err
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
	}
	return steps, byMessage, rows.Err()
}

// parseAntigravityDatabase reads gen_metadata, one row per model request, and dates
// each request by its response step. Failed responses have no row there.
func parseAntigravityDatabase(path string) ([]Event, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	steps, stepsByMessage, err := readAntigravitySteps(db)
	if err != nil {
		return nil, err
	}
	conversation := strings.TrimSuffix(filepath.Base(path), ".db")
	var trajectory []byte
	_ = db.QueryRow("SELECT data FROM trajectory_metadata_blob LIMIT 1").Scan(&trajectory)
	project := strings.TrimPrefix(string(pbBytes(pbBytes(trajectory, antigravityWorkspace), 1)), "file://")
	newEvent := func(id, model string, usage []byte) Event {
		return Event{
			Agent: "antigravity", ID: id, Model: model, Session: conversation, Project: project, Client: "agy", Source: path,
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
	defer rows.Close()
	var events []Event
	modelNames := map[int64]string{}
	responded := map[int64]bool{}
	for rows.Next() {
		var index int64
		var data []byte
		if err := rows.Scan(&index, &data); err != nil {
			return nil, err
		}
		request := pbBytes(data, antigravityGenRequest)
		usage := pbBytes(request, antigravityGenUsage)
		if usage == nil {
			continue
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
			continue
		}
		responded[stepIndex] = true
		event := newEvent(cmp.Or(labels["request_id"], conversation+":"+strconv.FormatInt(index, 10)), model, usage)
		event.Time = step.created
		event.LatencyMS = pbMillis(pbBytes(request, antigravityGenLatency))
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index, step := range steps {
		if responded[index] || step.error == "" {
			continue
		}
		event := newEvent(conversation+":step:"+strconv.FormatInt(index, 10), modelNames[step.model], step.usage)
		event.Time = step.created
		event.Failed = true
		event.Error = step.error
		events = append(events, event)
	}
	return events, nil
}
