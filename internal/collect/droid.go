package collect

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func droid() Collector {
	dirs := roots("DROID_SESSIONS_DIR", homeDir(".factory", "sessions"))
	return Collector{
		Agent: "droid",
		List:  func() ([]Source, error) { return files("droid", dirs, withSuffix(".settings.json")) },
		Parse: parseDroid,
	}
}

// parseDroid reads a Factory Droid session's <id>.settings.json, which only
// keeps the session's total usage. Sessions that name no model may mention it
// in their <id>.jsonl transcript.
func parseDroid(path string) ([]event.Event, error) {
	var settings map[string]any
	if err := readJSON(path, &settings); err != nil {
		return nil, nil
	}
	usage := object(settings["tokenUsage"])
	if usage == nil {
		return nil, nil
	}
	session := strings.TrimSuffix(filepath.Base(path), ".settings.json")
	thinking := number(usage["thinkingTokens"])
	e := event.Event{
		Agent: "droid", ID: session, Session: session, Scope: event.ScopeSession, Source: path,
		Model: droidModel(text(settings["model"])), Provider: text(settings["providerLock"]), Project: text(settings["cwd"]),
		InputTokens: number(usage["inputTokens"]), CacheReadTokens: number(usage["cacheReadTokens"]),
		CacheWriteTokens: number(usage["cacheCreationTokens"]), OutputTokens: number(usage["outputTokens"]) + thinking,
		ReasoningTokens: thinking, Time: parseTime(text(settings["providerLockTimestamp"])),
	}
	fillTotal(&e, number(usage["totalTokens"]))
	if e.TotalTokens() == 0 {
		return nil, nil
	}
	if e.Model == "" {
		e.Model = droidTranscriptModel(strings.TrimSuffix(path, ".settings.json") + ".jsonl")
	}
	if e.Time == 0 {
		if info, err := os.Stat(path); err == nil {
			e.Time = info.ModTime().UnixMilli()
		}
	}
	return []event.Event{e}, nil
}

// droidModel turns a model name into an ID: it drops the custom: prefix and
// bracketed notes of custom models, and spells Claude versions with dashes
// like Anthropic's IDs.
func droidModel(model string) string {
	model = strings.TrimPrefix(strings.TrimSpace(model), "custom:")
	var kept strings.Builder
	depth := 0
	for _, r := range model {
		switch {
		case r == '[':
			depth++
		case r == ']':
			depth = max(0, depth-1)
		case depth == 0:
			kept.WriteRune(r)
		}
	}
	id := strings.Join(strings.Fields(strings.ToLower(strings.Trim(kept.String(), " -"))), "-")
	if strings.HasPrefix(id, "claude-") {
		id = strings.ReplaceAll(id, ".", "-")
	}
	return id
}

// droidTranscriptModel finds "Model: <name>" in the start of a transcript.
func droidTranscriptModel(path string) string {
	model, lines := "", 0
	_ = readLines(path, func(line []byte) {
		if lines++; model != "" || lines > 500 {
			return
		}
		if _, tail, ok := bytes.Cut(line, []byte("Model:")); ok {
			name, _, _ := strings.Cut(string(tail), `"`)
			name, _, _ = strings.Cut(name, `\`)
			model = droidModel(name)
		}
	})
	return model
}
