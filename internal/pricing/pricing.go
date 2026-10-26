// Package pricing prices events with custom rates or models.dev reference
// prices.
package pricing

import (
	"errors"
	"math"

	"github.com/haowang02/agent-events/internal/event"
)

// Rates are USD per million tokens. Missing cache rates fall back to the
// input rate.
type Rates struct {
	Input       float64      `json:"input"`
	Output      float64      `json:"output"`
	CacheRead   *float64     `json:"cache_read,omitempty"`
	CacheWrite  *float64     `json:"cache_write,omitempty"`
	LongContext *LongContext `json:"long_context,omitempty"`
}

// LongContext rates apply to the whole request once its prompt exceeds
// Threshold tokens.
type LongContext struct {
	Threshold  int64    `json:"threshold"`
	Input      float64  `json:"input"`
	Output     float64  `json:"output"`
	CacheRead  *float64 `json:"cache_read,omitempty"`
	CacheWrite *float64 `json:"cache_write,omitempty"`
}

func (r Rates) Validate() error {
	values := []*float64{&r.Input, &r.Output, r.CacheRead, r.CacheWrite}
	if tier := r.LongContext; tier != nil {
		if tier.Threshold <= 0 {
			return errors.New("the long-context threshold must be positive")
		}
		values = append(values, &tier.Input, &tier.Output, tier.CacheRead, tier.CacheWrite)
	}
	for _, value := range values {
		if value != nil && (math.IsNaN(*value) || *value < 0 || *value > 1e6) {
			return errors.New("prices must be between 0 and 1,000,000")
		}
	}
	return nil
}

func sameRate(a, b *float64) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func (r *Rates) equal(other *Rates) bool {
	if r.Input != other.Input || r.Output != other.Output || !sameRate(r.CacheRead, other.CacheRead) || !sameRate(r.CacheWrite, other.CacheWrite) {
		return false
	}
	a, b := r.LongContext, other.LongContext
	if a == nil || b == nil {
		return a == b
	}
	return a.Threshold == b.Threshold && a.Input == b.Input && a.Output == b.Output &&
		sameRate(a.CacheRead, b.CacheRead) && sameRate(a.CacheWrite, b.CacheWrite)
}

// Price is the rates applied to a model and where they came from.
type Price struct {
	Rates
	Source   string `json:"source"` // "custom" or "models.dev"
	Provider string `json:"provider,omitempty"`
	ModelID  string `json:"model_id,omitempty"`
	As       string `json:"as,omitempty"` // The model whose price an aliased model takes.
}

// Cost is the cost of an event and the effective rates behind it.
type Cost struct {
	Total            float64 `json:"total"`
	Input            float64 `json:"input"`
	CacheRead        float64 `json:"cache_read"`
	CacheWrite       float64 `json:"cache_write"`
	Output           float64 `json:"output"`
	LongContext      bool    `json:"long_context,omitempty"`
	Multiplier       float64 `json:"multiplier,omitempty"` // Set when the service tier scales the rates.
	InputRate        float64 `json:"input_rate"`
	OutputRate       float64 `json:"output_rate"`
	CacheReadRate    float64 `json:"cache_read_rate"`
	CacheWriteRate   float64 `json:"cache_write_rate"`
	CacheWrite1hRate float64 `json:"cache_write_1h_rate,omitempty"`
}

// tierMultiplier scales the rates of a service tier. OpenAI bills priority
// processing, Codex's fast mode, at twice the standard rates.
func tierMultiplier(e *event.Event) float64 {
	if e.Agent == "codex" && e.Tier == "priority" {
		return 2
	}
	return 1
}

func perMillion(tokens int64, rate float64) float64 { return float64(tokens) * rate / 1e6 }

func (p *Price) Cost(e *event.Event) Cost {
	input, output := p.Input, p.Output
	cacheRead, cacheWrite := p.CacheRead, p.CacheWrite
	long := false
	if tier := p.LongContext; tier != nil && e.PromptTokens() > tier.Threshold {
		long = true
		input, output = tier.Input, tier.Output
		cacheRead, cacheWrite = tier.CacheRead, tier.CacheWrite
	}
	multiplier := tierMultiplier(e)
	rate := func(value *float64) float64 {
		if value != nil {
			return *value * multiplier
		}
		return input * multiplier
	}
	cost := Cost{
		LongContext:    long,
		InputRate:      input * multiplier,
		OutputRate:     output * multiplier,
		CacheReadRate:  rate(cacheRead),
		CacheWriteRate: rate(cacheWrite),
	}
	if multiplier != 1 {
		cost.Multiplier = multiplier
	}
	cost.Input = perMillion(e.InputTokens, cost.InputRate)
	cost.CacheRead = perMillion(e.CacheReadTokens, cost.CacheReadRate)
	cost.CacheWrite = perMillion(e.CacheWriteTokens-e.CacheWrite1hTokens, cost.CacheWriteRate)
	if e.CacheWrite1hTokens > 0 {
		// Anthropic bills 1h-TTL cache writes at twice the input rate.
		cost.CacheWrite1hRate = 2 * cost.InputRate
		cost.CacheWrite += perMillion(e.CacheWrite1hTokens, cost.CacheWrite1hRate)
	}
	cost.Output = perMillion(e.OutputTokens, cost.OutputRate)
	cost.Total = cost.Input + cost.CacheRead + cost.CacheWrite + cost.Output
	return cost
}
