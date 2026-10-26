package collect

import (
	"bufio"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	_ "modernc.org/sqlite"

	"github.com/haowang02/agent-events/internal/event"
)

// readLines calls fn for each line of a file, decompressing .zst files.
// Lines longer than the buffer are passed whole.
func readLines(path string, fn func(line []byte)) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	var source io.Reader = file
	if strings.HasSuffix(path, ".zst") {
		decoder, err := zstd.NewReader(file)
		if err != nil {
			return err
		}
		defer decoder.Close()
		source = decoder
	}
	reader := bufio.NewReaderSize(source, 1<<20)
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			long := append([]byte(nil), line...)
			for errors.Is(err, bufio.ErrBufferFull) {
				line, err = reader.ReadSlice('\n')
				long = append(long, line...)
			}
			line = long
		}
		if len(line) > 0 {
			fn(line)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func readJSON(path string, value any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, value)
}

func openDatabase(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	return db, nil
}

// scanRows calls scan for each row and closes the rows.
func scanRows(rows *sql.Rows, scan func() error) error {
	defer rows.Close()
	for rows.Next() {
		if err := scan(); err != nil {
			return err
		}
	}
	return rows.Err()
}

// columns returns the column names of a table, or none when it does not exist.
func columns(db *sql.DB, table string) (map[string]bool, error) {
	rows, err := db.Query("SELECT name FROM pragma_table_info(?)", table)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	err = scanRows(rows, func() error {
		var name string
		err := rows.Scan(&name)
		names[name] = true
		return err
	})
	return names, err
}

// columnOr selects a column, possibly qualified by a table alias, or
// fallback when this version of the schema lacks it or the value is NULL.
func columnOr(names map[string]bool, column, fallback string) string {
	if !names[column[strings.LastIndex(column, ".")+1:]] {
		return fallback
	}
	return "COALESCE(" + column + ", " + fallback + ")"
}

// sqlTime reads a SQLite time column: Unix seconds or milliseconds, text, or
// a time the driver parsed from a declared timestamp column.
func sqlTime(value any) int64 {
	switch value := value.(type) {
	case time.Time:
		return value.UnixMilli()
	case int64:
		return unixMillis(float64(value))
	case float64:
		return unixMillis(value)
	case string:
		return parseTime(value)
	case []byte:
		return parseTime(string(value))
	}
	return 0
}

var timeLayouts = []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999", "2006-01-02 15:04:05.999999999", "2006-01-02"}

// parseTime reads a timestamp as Unix milliseconds. Times without a zone are
// UTC; numbers are Unix seconds or milliseconds.
func parseTime(value string) int64 {
	value = strings.TrimSpace(value)
	if number, err := strconv.ParseFloat(value, 64); err == nil {
		return unixMillis(number)
	}
	for _, layout := range timeLayouts {
		if at, err := time.Parse(layout, value); err == nil {
			return at.UnixMilli()
		}
	}
	return 0
}

// unixMillis reads a Unix time in seconds or milliseconds as milliseconds.
func unixMillis(value float64) int64 {
	if math.IsNaN(value) || value <= 0 {
		return 0
	}
	if value < 1e11 {
		value *= 1000
	}
	return int64(value)
}

// timestamp decodes an ISO 8601 string or a Unix seconds or milliseconds
// number as Unix milliseconds.
type timestamp int64

func (t *timestamp) UnmarshalJSON(raw []byte) error {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		*t = timestamp(timeValue(value))
	}
	return nil
}

// timeValue reads a decoded ISO 8601 string or Unix time number.
func timeValue(value any) int64 {
	switch value := value.(type) {
	case string:
		return parseTime(value)
	case float64:
		return unixMillis(value)
	}
	return 0
}

// number reads a JSON number or numeric string as a non-negative count.
func number(value any) int64 {
	switch value := value.(type) {
	case float64:
		if value > 0 && !math.IsInf(value, 0) {
			return int64(value)
		}
	case string:
		if parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err == nil && parsed > 0 {
			return int64(parsed)
		}
	}
	return 0
}

// firstNumber reads the first of keys that holds a number.
func firstNumber(object map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := object[key]; ok && value != nil {
			return number(value)
		}
	}
	return 0
}

func text(value any) string {
	if value, ok := value.(string); ok {
		return strings.TrimSpace(value)
	}
	return ""
}

func object(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// inclusiveInput splits an input count that includes cache reads and writes
// into non-overlapping buckets, clamping caches that exceed it.
func inclusiveInput(input, cacheRead, cacheWrite int64) (uncached, read, write int64) {
	read = min(max(cacheRead, 0), input)
	write = min(max(cacheWrite, 0), input-read)
	return input - read - write, read, write
}

// fillTotal counts the tokens a reported total has beyond the buckets as
// output, for records that only report a total.
func fillTotal(e *event.Event, total int64) {
	if missing := total - e.TotalTokens(); missing > 0 {
		e.OutputTokens += missing
	}
}
