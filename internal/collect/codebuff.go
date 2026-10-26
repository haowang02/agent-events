package collect

import (
	"cmp"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

// codebuff collects Codebuff from each release channel's config directory.
func codebuff() Collector {
	var dirs []string
	for _, root := range roots("CODEBUFF_DATA_DIR", homeDir(".config", "manicode"), homeDir(".config", "manicode-dev"), homeDir(".config", "manicode-staging")) {
		if filepath.Base(root) != "projects" {
			root = filepath.Join(root, "projects")
		}
		dirs = append(dirs, root)
	}
	return Collector{
		Agent: "codebuff",
		List:  func() ([]Source, error) { return files("codebuff", dirs, named("chat-messages.json")) },
		Parse: parseCodebuff,
	}
}

// codebuffUsage reads a usage object, whichever provider spelled it.
func codebuffUsage(usage map[string]any) event.Event {
	e := event.Event{
		InputTokens:  firstNumber(usage, "inputTokens", "input_tokens", "promptTokens", "prompt_tokens"),
		OutputTokens: firstNumber(usage, "outputTokens", "output_tokens", "completionTokens", "completion_tokens"),
		CacheReadTokens: max(firstNumber(usage, "cacheReadInputTokens", "cache_read_input_tokens"),
			number(object(usage["promptTokensDetails"])["cachedTokens"]), number(object(usage["prompt_tokens_details"])["cached_tokens"])),
		CacheWriteTokens: firstNumber(usage, "cacheCreationInputTokens", "cache_creation_input_tokens", "cacheCreationTokens",
			"cache_creation_tokens", "cachedTokensCreated", "cached_tokens_created"),
		Model: text(usage["model"]),
	}
	fillTotal(&e, firstNumber(usage, "totalTokens", "total_tokens", "total"))
	return e
}

// codebuffFallback fills the token buckets and model e lacks from fallback.
func codebuffFallback(e *event.Event, fallback event.Event) {
	e.InputTokens = cmp.Or(e.InputTokens, fallback.InputTokens)
	e.OutputTokens = cmp.Or(e.OutputTokens, fallback.OutputTokens)
	e.CacheReadTokens = cmp.Or(e.CacheReadTokens, fallback.CacheReadTokens)
	e.CacheWriteTokens = cmp.Or(e.CacheWriteTokens, fallback.CacheWriteTokens)
	e.Model = cmp.Or(e.Model, fallback.Model)
}

// parseCodebuff reads projects/<project>/chats/<chat>/chat-messages.json.
// Usage sits in the message metadata, or in the run state's message history
// of the main agent.
func parseCodebuff(path string) ([]event.Event, error) {
	var messages []map[string]any
	if err := readJSON(path, &messages); err != nil {
		return nil, nil
	}
	chatDir := filepath.Dir(path)
	chat := filepath.Base(chatDir)
	project := filepath.Base(filepath.Dir(filepath.Dir(chatDir)))
	channel := filepath.Base(filepath.Dir(filepath.Dir(filepath.Dir(filepath.Dir(chatDir)))))
	session := channel + "/" + project + "/" + chat
	// Chat directories are named by their start time, with dashes for colons.
	fallbackTime := int64(0)
	if day, clock, ok := strings.Cut(chat, "T"); ok {
		fallbackTime = parseTime(day + "T" + strings.Replace(clock, "-", ":", 2))
	}
	if fallbackTime == 0 {
		if info, err := os.Stat(path); err == nil {
			fallbackTime = info.ModTime().UnixMilli()
		}
	}
	var events []event.Event
	for i, message := range messages {
		if role := cmp.Or(text(message["variant"]), text(message["role"])); role != "ai" && role != "agent" && role != "assistant" {
			continue
		}
		metadata := object(message["metadata"])
		e := codebuffUsage(object(metadata["usage"]))
		e.Model = cmp.Or(text(metadata["model"]), e.Model)
		codebuffFallback(&e, codebuffUsage(object(object(metadata["codebuff"])["usage"])))
		history, _ := object(object(object(metadata["runState"])["sessionState"])["mainAgentState"])["messageHistory"].([]any)
		for j := len(history) - 1; j >= 0; j-- {
			entry := object(history[j])
			options := object(entry["providerOptions"])
			if text(entry["role"]) != "assistant" || options == nil {
				continue
			}
			fallback := codebuffUsage(object(options["usage"]))
			codebuff := object(options["codebuff"])
			codebuffFallback(&fallback, codebuffUsage(object(codebuff["usage"])))
			fallback.Model = cmp.Or(text(codebuff["model"]), fallback.Model)
			codebuffFallback(&e, fallback)
		}
		if e.TotalTokens() == 0 {
			continue
		}
		e.Agent, e.Session, e.Project, e.Source = "codebuff", session, project, path
		e.ID = session + ":" + cmp.Or(text(message["id"]), strconv.Itoa(i))
		e.Time = cmp.Or(timeValue(message["timestamp"]), timeValue(message["createdAt"]), timeValue(metadata["timestamp"]), fallbackTime)
		events = append(events, e)
	}
	return events, nil
}
