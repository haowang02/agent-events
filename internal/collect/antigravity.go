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
	antigravityGenTTFT          = 11 // request: time to first response, google.protobuf.Duration
	antigravityGenStreaming     = 12 // request: time from first response to completion, google.protobuf.Duration
	antigravityGenAttempts      = 17 // request: generation attempts
	antigravityGenModel         = 19 // request: model name
	antigravityGenLabels        = 20 // request: repeated {1: key, 2: value}
	antigravityGenDisplayName   = 21 // request: display name, e.g. "Gemini 4 Hydrogen (Medium)"
	antigravityAttemptError     = 3  // attempt: error message
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
	antigravityStepTypeError    = 17 // steps.step_type of an error message
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
	kind    int64
	created int64
	usage   []byte
	model   int64
	error   string
}

type antigravityModel struct {
	name   string
	effort string
}

func antigravityEffort(displayName, model string) string {
	if strings.HasSuffix(displayName, ")") {
		if open := strings.LastIndexByte(displayName, '('); open >= 0 {
			if effort := normalizeAntigravityEffort(displayName[open+1 : len(displayName)-1]); effort != "" {
				return effort
			}
		}
	}
	if last := strings.LastIndexByte(model, '-'); last >= 0 {
		return normalizeAntigravityEffort(model[last+1:])
	}
	return ""
}

func normalizeAntigravityEffort(raw string) string {
	effort := strings.ToLower(strings.TrimSpace(raw))
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return effort
	default:
		return ""
	}
}

// antigravitySteps indexes response and error steps, and matches responses by message ID.
func antigravitySteps(db *sql.DB) (map[int64]antigravityStep, map[string]int64, error) {
	rows, err := db.Query("SELECT idx, step_type, metadata, error_details FROM steps WHERE step_type IN (?, ?)", antigravityStepTypeResponse, antigravityStepTypeError)
	if err != nil {
		return nil, nil, err
	}
	steps := map[int64]antigravityStep{}
	byMessage := map[string]int64{}
	err = scanRows(rows, func() error {
		var index int64
		var metadata, details []byte
		var kind int64
		if err := rows.Scan(&index, &kind, &metadata, &details); err != nil {
			return err
		}
		step := antigravityStep{
			kind:    kind,
			created: pbMillis(pbBytes(metadata, antigravityStepCreated)),
			usage:   pbBytes(metadata, antigravityStepUsage),
			model:   pbVarint(metadata, antigravityStepModel),
			error:   string(pbBytes(details, antigravityStepErrorMessage)),
		}
		steps[index] = step
		if id := string(pbBytes(step.usage, antigravityUsageMessageID)); kind == antigravityStepTypeResponse && id != "" {
			byMessage[id] = index
		}
		return nil
	})
	return steps, byMessage, err
}

// parseAntigravity reads gen_metadata and dates requests by their response or error step.
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
	newEvent := func(id string, m antigravityModel, usage []byte) event.Event {
		return event.Event{
			Agent: "antigravity", ID: id, Model: m.name, Effort: m.effort, Session: conversation, Project: project, Client: client, Source: path,
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
	models := map[int64]antigravityModel{}
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
		displayName := string(pbBytes(request, antigravityGenDisplayName))
		m := antigravityModel{name: model, effort: antigravityEffort(displayName, model)}
		modelID := pbVarint(usage, antigravityUsageModel)
		if modelID != 0 {
			m.name = cmp.Or(m.name, models[modelID].name)
			models[modelID] = m
		}
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
		failure := string(pbBytes(pbBytes(request, antigravityGenAttempts), antigravityAttemptError))
		if step.kind == antigravityStepTypeError && failure == "" {
			return nil
		}
		responded[stepIndex] = true
		e := newEvent(cmp.Or(labels["request_id"], conversation+":"+strconv.FormatInt(index, 10)), m, usage)
		e.Time = step.created
		first, streaming := pbBytes(request, antigravityGenTTFT), pbBytes(request, antigravityGenStreaming)
		e.TTFTMS = pbMillis(first)
		if first != nil && streaming != nil {
			e.LatencyMS = e.TTFTMS + pbMillis(streaming)
		}
		if step.kind == antigravityStepTypeError {
			e.Failed, e.Error = true, failure
		}
		events = append(events, e)
		return nil
	})
	if err != nil {
		return nil, err
	}
	for index, step := range steps {
		if step.kind != antigravityStepTypeResponse || responded[index] || step.error == "" {
			continue
		}
		e := newEvent(conversation+":step:"+strconv.FormatInt(index, 10), models[step.model], step.usage)
		e.Time = step.created
		e.Failed = true
		e.Error = step.error
		events = append(events, e)
	}
	return events, nil
}
