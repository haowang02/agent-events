package collect

import (
	"encoding/json"
	"fmt"

	"github.com/haowang02/agent-events/internal/event"
)

type codexUsage struct {
	InputTokens              int64 `json:"input_tokens"` // Includes cached input.
	CachedInputTokens        int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens    int64 `json:"cache_write_input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"` // Includes reasoning.
	ReasoningOutputTokens    int64 `json:"reasoning_output_tokens"`
	specified                uint8
}

func (u *codexUsage) UnmarshalJSON(raw []byte) error {
	type plain codexUsage
	var value plain
	if err := json.Unmarshal(raw, &value); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	*u = codexUsage(value)
	for i, names := range [][]string{{"cached_input_tokens"}, {"cache_write_input_tokens", "cache_creation_input_tokens"}, {"reasoning_output_tokens"}} {
		for _, name := range names {
			if value, ok := fields[name]; ok && string(value) != "null" {
				u.specified |= 1 << i
			}
		}
	}
	return nil
}

func (u codexUsage) counts() [5]int64 {
	var e event.Event
	u.apply(&e)
	return [5]int64{u.InputTokens, e.CacheReadTokens, e.CacheWriteTokens, u.OutputTokens, u.ReasoningOutputTokens}
}

// Some older mirrors omit cache/reasoning fields entirely. An explicit zero
// is still known usage and must distinguish a different call after compaction.
func (u codexUsage) mirrors(record codexUsage) bool {
	a, b := u.counts(), record.counts()
	return a[0] == b[0] && a[3] == b[3] &&
		(u.specified&1 == 0 || a[1] == b[1]) &&
		(u.specified&2 == 0 || a[2] == b[2]) &&
		(u.specified&4 == 0 || a[4] == b[4])
}

func (u codexUsage) apply(e *event.Event) {
	e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(
		u.InputTokens, u.CachedInputTokens, max(u.CacheWriteInputTokens, u.CacheCreationInputTokens))
	e.OutputTokens = u.OutputTokens
	e.ReasoningTokens = u.ReasoningOutputTokens
}

func (u codexUsage) minus(previous *codexUsage) codexUsage {
	if previous == nil {
		return u
	}
	return codexUsage{
		InputTokens:              max(0, u.InputTokens-previous.InputTokens),
		CachedInputTokens:        max(0, u.CachedInputTokens-previous.CachedInputTokens),
		CacheWriteInputTokens:    max(0, u.CacheWriteInputTokens-previous.CacheWriteInputTokens),
		CacheCreationInputTokens: max(0, u.CacheCreationInputTokens-previous.CacheCreationInputTokens),
		OutputTokens:             max(0, u.OutputTokens-previous.OutputTokens),
		ReasoningOutputTokens:    max(0, u.ReasoningOutputTokens-previous.ReasoningOutputTokens),
		specified:                u.specified,
	}
}

func (u codexUsage) String() string {
	return fmt.Sprintf("%d.%d.%d.%d.%d.%d", u.InputTokens, u.CachedInputTokens, u.CacheWriteInputTokens,
		u.CacheCreationInputTokens, u.OutputTokens, u.ReasoningOutputTokens)
}
