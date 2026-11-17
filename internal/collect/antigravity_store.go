package collect

import (
	"cmp"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"google.golang.org/protobuf/encoding/protowire"
)

type antigravityStep struct {
	index    int64
	kind     int64
	created  int64
	usage    []byte
	model    int64
	error    string
	metadata []byte
}

type antigravityGeneration struct {
	index         int64
	data, request []byte
}

// antigravitySteps includes every step kind: checkpoints carry billable usage
// without a corresponding gen_metadata row. Older stores may have no steps.
func antigravitySteps(db *sql.DB) ([]antigravityStep, error) {
	names, err := columns(db, "steps")
	if err != nil || len(names) == 0 {
		return nil, err
	}
	rows, err := db.Query("SELECT idx, step_type, metadata, " + columnOr(names, "error_details", "NULL") + " FROM steps ORDER BY idx")
	if err != nil {
		return nil, err
	}
	var steps []antigravityStep
	err = scanRows(rows, func() error {
		var step antigravityStep
		var details []byte
		if err := rows.Scan(&step.index, &step.kind, &step.metadata, &details); err != nil {
			return err
		}
		if err := antigravityValidate(step.metadata, 9, 28); err != nil {
			return fmt.Errorf("step %d: %w", step.index, err)
		}
		step.created = cmp.Or(pbMillis(pbBytes(step.metadata, 1)), pbMillis(pbBytes(step.metadata, 8)))
		step.usage = pbBytes(step.metadata, antigravityStepUsage)
		step.model = cmp.Or(pbVarint(pbBytes(step.metadata, 24), 1), pbVarint(step.metadata, antigravityStepModel), pbVarint(step.usage, 1))
		step.error = string(pbBytes(details, antigravityStepErrorMessage))
		steps = append(steps, step)
		return nil
	})
	return steps, err
}

func readAntigravity(path string) (*antigravityConversation, error) {
	db, err := openDatabase(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	// All tables must describe the same snapshot while Antigravity writes them.
	if _, err := db.Exec("BEGIN"); err != nil {
		return nil, err
	}
	defer db.Exec("ROLLBACK")
	var pages int
	if err := db.QueryRow("PRAGMA page_count").Scan(&pages); err != nil {
		return nil, err
	}
	if pages == 0 {
		return nil, nil
	}
	steps, err := antigravitySteps(db)
	if err != nil {
		return nil, err
	}
	var generations []antigravityGeneration
	rows, err := db.Query("SELECT idx, data FROM gen_metadata ORDER BY idx")
	if err != nil {
		return nil, err
	}
	err = scanRows(rows, func() error {
		var g antigravityGeneration
		if err := rows.Scan(&g.index, &g.data); err != nil {
			return err
		}
		g.request = pbBytes(g.data, antigravityGenRequest)
		if g.request == nil {
			return fmt.Errorf("generation %d: missing request", g.index)
		}
		if err := antigravityValidate(g.request, 4, 17); err != nil {
			return fmt.Errorf("generation %d: %w", g.index, err)
		}
		generations = append(generations, g)
		return nil
	})
	if err != nil {
		return nil, err
	}
	conversation := strings.TrimSuffix(filepath.Base(path), ".db")
	client := filepath.Base(filepath.Dir(filepath.Dir(path)))
	var trajectory []byte
	names, err := columns(db, "trajectory_metadata_blob")
	if err != nil {
		return nil, err
	}
	if len(names) > 0 {
		if err := db.QueryRow("SELECT data FROM trajectory_metadata_blob LIMIT 1").Scan(&trajectory); err != nil && err != sql.ErrNoRows {
			return nil, err
		}
	}
	project := strings.TrimPrefix(string(pbBytes(pbBytes(trajectory, antigravityWorkspace), 1)), "file://")
	fallbackTime := pbMillis(pbBytes(trajectory, 2))
	if fallbackTime == 0 {
		if info, err := os.Stat(path); err == nil {
			fallbackTime = info.ModTime().UnixMilli()
		}
	}
	models := antigravityModels{}
	for _, g := range generations {
		id := cmp.Or(pbVarint(g.request, 3), pbVarint(pbBytes(g.request, 4), 1))
		models.remember(id, string(pbBytes(g.request, 19)), string(pbBytes(g.request, 21)))
	}
	for _, step := range steps {
		info := pbBytes(step.metadata, 24)
		name := cmp.Or(string(pbBytes(info, 12)), string(pbBytes(info, 8)))
		if name != "" {
			models.remember(step.model, name, "")
		}
	}
	return &antigravityConversation{path: path, conversation: conversation, client: client, project: project, fallbackTime: fallbackTime, steps: steps, generations: generations, models: models}, nil
}

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

// Reject truncated metadata rather than saving partial usage as a successful
// parse. Unrecognized fields are still skipped according to their wire type.
func antigravityValidate(metadata []byte, usage, retries protowire.Number) error {
	validate := func(data []byte) error {
		for len(data) > 0 {
			number, kind, n := protowire.ConsumeTag(data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
			n = protowire.ConsumeFieldValue(number, kind, data)
			if n < 0 {
				return protowire.ParseError(n)
			}
			data = data[n:]
		}
		return nil
	}
	if err := validate(metadata); err != nil {
		return err
	}
	for _, data := range pbRepeated(metadata, usage) {
		if err := validate(data); err != nil {
			return err
		}
	}
	for _, attempt := range pbRepeated(metadata, retries) {
		if err := validate(attempt); err != nil {
			return err
		}
		if err := validate(pbBytes(attempt, 2)); err != nil {
			return err
		}
	}
	return nil
}

// step_indices is repeated uint32 and may be packed or unpacked.
func antigravityStepIndices(data []byte) []int64 {
	var indices []int64
	pbEach(data, func(number protowire.Number, kind protowire.Type, value []byte) {
		if number != 2 {
			return
		}
		if kind == protowire.VarintType {
			n, _ := protowire.ConsumeVarint(value)
			indices = append(indices, int64(n))
		}
		if kind == protowire.BytesType {
			packed, _ := protowire.ConsumeBytes(value)
			for len(packed) > 0 {
				n, size := protowire.ConsumeVarint(packed)
				if size < 0 {
					break
				}
				indices = append(indices, int64(n))
				packed = packed[size:]
			}
		}
	})
	return indices
}
