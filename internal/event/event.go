// Package event defines the model request record that collectors produce and
// the store, pricing and server consume.
package event

// Scopes of events that total several requests, for agents that record no
// per-request usage.
const (
	ScopeTurn    = "turn"
	ScopeSession = "session"
)

// Event is one model request made by a coding agent, or the total of a turn or
// session when Scope is set. Token counts are split into non-overlapping
// buckets so that every agent is priced the same way.
type Event struct {
	Agent    string `json:"agent"`
	ID       string `json:"id"`
	Time     int64  `json:"time"` // Unix milliseconds; request start when known.
	Model    string `json:"model"`
	Provider string `json:"provider,omitempty"`
	Session  string `json:"session,omitempty"`
	Project  string `json:"project,omitempty"`
	Client   string `json:"client,omitempty"` // Entrypoint, originator, or agent profile.
	Kind     string `json:"kind,omitempty"`   // Main thread, subagent, or other request source.
	Scope    string `json:"scope,omitempty"`
	Effort   string `json:"effort,omitempty"`
	Tier     string `json:"tier,omitempty"` // Service tier or speed mode.

	InputTokens        int64 `json:"input_tokens"` // Uncached input.
	CacheReadTokens    int64 `json:"cache_read_tokens"`
	CacheWriteTokens   int64 `json:"cache_write_tokens"`    // All cache writes, including 1h writes.
	CacheWrite1hTokens int64 `json:"cache_write_1h_tokens"` // Anthropic 1h-TTL writes, priced separately.
	OutputTokens       int64 `json:"output_tokens"`         // Includes reasoning.
	ReasoningTokens    int64 `json:"reasoning_tokens"`

	LatencyMS int64 `json:"latency_ms"` // 0 when unknown.
	TTFTMS    int64 `json:"ttft_ms"`    // 0 when unknown.

	Failed  bool   `json:"failed"`
	Error   string `json:"error,omitempty"`
	Stop    string `json:"stop,omitempty"`
	Version string `json:"version,omitempty"`
	Source  string `json:"source,omitempty"` // File or database the event was read from.
}

func (e *Event) PromptTokens() int64 {
	return e.InputTokens + e.CacheReadTokens + e.CacheWriteTokens
}

func (e *Event) TotalTokens() int64 {
	return e.PromptTokens() + e.OutputTokens
}

// OutputTPS is output tokens per second over the whole latency of a request.
func (e *Event) OutputTPS() (float64, bool) {
	if e.Failed || e.Scope != "" || e.LatencyMS <= 0 || e.OutputTokens <= 0 {
		return 0, false
	}
	return float64(e.OutputTokens) * 1000 / float64(e.LatencyMS), true
}
