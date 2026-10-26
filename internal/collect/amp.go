package collect

import (
	"cmp"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func amp() Collector {
	dirs := under(roots("AMP_DATA_DIR", xdgDataDir("amp")), "threads")
	isThread := func(path string) bool {
		name := filepath.Base(path)
		return strings.HasPrefix(name, "T-") && strings.HasSuffix(name, ".json")
	}
	return Collector{
		Agent: "amp",
		List:  func() ([]Source, error) { return files("amp", dirs, isThread) },
		Parse: parseAmp,
	}
}

// parseAmp reads an Amp thread. Older threads keep a usage ledger whose
// events take their cache counts from the message they produced; current
// ones record usage on each assistant message.
func parseAmp(path string) ([]event.Event, error) {
	var thread struct {
		ID       string           `json:"id"`
		Messages []map[string]any `json:"messages"`
		Ledger   *struct {
			Events []map[string]any `json:"events"`
		} `json:"usageLedger"`
	}
	if err := readJSON(path, &thread); err != nil || thread.ID == "" {
		return nil, nil
	}
	newEvent := func(id string, at int64, model string) event.Event {
		return event.Event{Agent: "amp", ID: thread.ID + ":" + id, Time: at, Model: model, Session: thread.ID, Source: path}
	}
	messageID := func(value any) string {
		if id := number(value); id > 0 {
			return strconv.FormatInt(id, 10)
		}
		return text(value)
	}
	var events []event.Event
	if thread.Ledger != nil && thread.Ledger.Events != nil {
		type cache struct{ read, write int64 }
		caches := map[string]cache{}
		for _, message := range thread.Messages {
			if usage := object(message["usage"]); text(message["role"]) == "assistant" && usage != nil {
				caches[messageID(message["messageId"])] = cache{number(usage["cacheReadInputTokens"]), number(usage["cacheCreationInputTokens"])}
			}
		}
		for i, ledger := range thread.Ledger.Events {
			tokens := object(ledger["tokens"])
			model := text(ledger["model"])
			if tokens == nil || model == "" {
				continue
			}
			e := newEvent(cmp.Or(messageID(ledger["id"]), fmt.Sprint("ledger-", i)), parseTime(text(ledger["timestamp"])), model)
			cached := caches[messageID(ledger["toMessageId"])]
			e.InputTokens, e.OutputTokens = number(tokens["input"]), number(tokens["output"])
			e.CacheReadTokens, e.CacheWriteTokens = cached.read, cached.write
			fillTotal(&e, number(tokens["total"]))
			if e.TotalTokens() > 0 && e.Time > 0 {
				events = append(events, e)
			}
		}
		return events, nil
	}
	for i, message := range thread.Messages {
		usage := object(message["usage"])
		if text(message["role"]) != "assistant" || usage == nil {
			continue
		}
		at := parseTime(cmp.Or(text(usage["timestamp"]), text(message["timestamp"])))
		model := cmp.Or(text(usage["model"]), text(message["model"]))
		if at == 0 || model == "" {
			continue
		}
		e := newEvent(cmp.Or(messageID(message["messageId"]), fmt.Sprint("message-", i)), at, model)
		e.InputTokens, e.OutputTokens = number(usage["inputTokens"]), number(usage["outputTokens"])
		e.CacheReadTokens, e.CacheWriteTokens = number(usage["cacheReadInputTokens"]), number(usage["cacheCreationInputTokens"])
		fillTotal(&e, number(usage["totalTokens"]))
		if e.TotalTokens() > 0 {
			events = append(events, e)
		}
	}
	return events, nil
}
