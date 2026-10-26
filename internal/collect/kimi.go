package collect

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

const kimiDefaultModel = "kimi-for-coding"

// kimi collects Kimi CLI and Kimi Code. A session's wire.jsonl sits at
// sessions/<group>/<session>, and a subagent's below its agents/<name>.
func kimi() Collector {
	dirs := under(roots("KIMI_DATA_DIR", homeDir(".kimi"), homeDir(".kimi-code")), "sessions")
	accept := func(path string) bool {
		if filepath.Base(path) != "wire.jsonl" {
			return false
		}
		for _, dir := range dirs {
			if relative, err := filepath.Rel(dir, path); err == nil && !strings.HasPrefix(relative, "..") {
				parts := strings.Split(filepath.ToSlash(relative), "/")
				return len(parts) == 3 || len(parts) == 5 && parts[2] == "agents"
			}
		}
		return false
	}
	return Collector{
		Agent: "kimi",
		List:  func() ([]Source, error) { return files("kimi", dirs, accept) },
		Parse: parseKimi,
	}
}

// parseKimi reads a wire.jsonl. Kimi CLI reports each step's usage in
// StatusUpdate messages and leaves the model to config.json; Kimi Code writes
// usage.record lines that total a turn.
func parseKimi(path string) ([]event.Event, error) {
	sessionDir, kind := filepath.Dir(path), ""
	if filepath.Base(filepath.Dir(sessionDir)) == "agents" {
		sessionDir, kind = filepath.Dir(filepath.Dir(sessionDir)), "subagent"
	}
	session := filepath.Base(sessionDir)
	var config struct {
		Model string `json:"model"`
	}
	_ = readJSON(filepath.Join(filepath.Dir(filepath.Dir(filepath.Dir(sessionDir))), "config.json"), &config)
	defaultModel := cmp.Or(config.Model, kimiDefaultModel)

	var events []event.Event
	err := readLines(path, func(raw []byte) {
		var line struct {
			Type       string  `json:"type"`
			Timestamp  float64 `json:"timestamp"` // Unix seconds.
			Time       int64   `json:"time"`      // Unix milliseconds.
			Model      string  `json:"model"`
			UsageScope string  `json:"usageScope"`
			Usage      *struct {
				InputOther         int64 `json:"inputOther"`
				Output             int64 `json:"output"`
				InputCacheCreation int64 `json:"inputCacheCreation"`
				InputCacheRead     int64 `json:"inputCacheRead"`
			} `json:"usage"`
			Message *struct {
				Type    string `json:"type"`
				Payload struct {
					MessageID  string `json:"message_id"`
					TokenUsage *struct {
						InputOther         int64 `json:"input_other"`
						Output             int64 `json:"output"`
						InputCacheCreation int64 `json:"input_cache_creation"`
						InputCacheRead     int64 `json:"input_cache_read"`
						Total              int64 `json:"total"`
					} `json:"token_usage"`
				} `json:"payload"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		e := event.Event{Agent: "kimi", Session: session, Kind: kind, Source: path}
		switch {
		case line.Type == "usage.record" && line.UsageScope == "turn" && line.Usage != nil:
			usage := line.Usage
			e.Time, e.Scope = line.Time, event.ScopeTurn
			e.Model = strings.TrimPrefix(cmp.Or(line.Model, kimiDefaultModel), "kimi-code/")
			e.InputTokens, e.OutputTokens = usage.InputOther, usage.Output
			e.CacheReadTokens, e.CacheWriteTokens = usage.InputCacheRead, usage.InputCacheCreation
		case line.Message != nil && line.Message.Type == "StatusUpdate" && line.Message.Payload.TokenUsage != nil:
			usage := line.Message.Payload.TokenUsage
			e.ID = line.Message.Payload.MessageID
			e.Time, e.Model = unixMillis(line.Timestamp), defaultModel
			e.InputTokens, e.OutputTokens = usage.InputOther, usage.Output
			e.CacheReadTokens, e.CacheWriteTokens = usage.InputCacheRead, usage.InputCacheCreation
			fillTotal(&e, usage.Total)
		default:
			return
		}
		if e.TotalTokens() == 0 {
			return
		}
		e.ID = session + ":" + cmp.Or(e.ID, strconv.FormatInt(e.Time, 10)+":"+strconv.Itoa(len(events)))
		events = append(events, e)
	})
	return events, err
}
