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
		Agent:   "amp",
		Version: 2,
		List:    func() ([]Source, error) { return files("amp", dirs, isThread) },
		Parse:   parseAmp,
	}
}

// parseAmp reads an Amp thread. Older threads keep a usage ledger whose
// events take their cache counts from the message they produced; current
// ones record usage on each assistant message.
func parseAmp(path string) (Result, error) {
	var thread struct {
		ID       string `json:"id"`
		Messages []any  `json:"messages"`
		Ledger   *struct {
			Events []any `json:"events"`
		} `json:"usageLedger"`
	}
	if err := readJSON(path, &thread); err != nil {
		return Result{}, err
	}
	if thread.ID == "" {
		return Result{}, nil
	}
	newEvent := func(id string, at int64, model string) event.Event {
		return event.Event{Agent: "amp", ID: thread.ID + ":" + id, Time: at, Model: model, Session: thread.ID, Source: path}
	}
	messageID := func(value any) string {
		if id, ok := value.(float64); ok && float64(int64(id)) == id {
			return strconv.FormatInt(int64(id), 10)
		}
		return text(value)
	}
	var events []event.Event
	if thread.Ledger != nil && thread.Ledger.Events != nil {
		type cache struct{ read, write int64 }
		caches := map[string]cache{}
		for _, value := range thread.Messages {
			message := object(value)
			if usage := object(message["usage"]); text(message["role"]) == "assistant" && usage != nil {
				if id := messageID(message["messageId"]); id != "" {
					caches[id] = cache{number(usage["cacheReadInputTokens"]), number(usage["cacheCreationInputTokens"])}
				}
			}
		}
		for i, value := range thread.Ledger.Events {
			ledger := object(value)
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
		return Result{Events: events}, nil
	}
	for i, value := range thread.Messages {
		message := object(value)
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
	return Result{Events: events}, nil
}
