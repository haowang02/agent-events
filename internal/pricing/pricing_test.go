package pricing

import (
	"math"
	"testing"
	"time"

	"github.com/haowang02/agent-events/internal/event"
)

func ptr(value float64) *float64 { return &value }

func TestPriceCost(t *testing.T) {
	price := Price{Rates: Rates{
		Input: 2, Output: 10, CacheRead: ptr(0.2),
		LongContext: &LongContext{Threshold: 1000, Input: 4, Output: 15},
	}}
	e := event.Event{Agent: "claude", InputTokens: 100, CacheReadTokens: 200, CacheWriteTokens: 300, CacheWrite1hTokens: 100, OutputTokens: 50}
	cost := price.Cost(&e)
	// Cache writes default to the input rate; 1h writes to twice the input rate.
	want := (100*2 + 200*0.2 + 200*2 + 100*4 + 50*10) / 1e6
	if cost.LongContext || math.Abs(cost.Total-want) > 1e-12 {
		t.Errorf("standard cost = %+v, want total %v", cost, want)
	}
	e.CacheReadTokens = 2000
	cost = price.Cost(&e)
	// The long-context tier has no cache read rate, so it falls back to the tier's input rate.
	want = (100*4 + 2000*4 + 200*4 + 100*8 + 50*15) / 1e6
	if !cost.LongContext || math.Abs(cost.Total-want) > 1e-12 {
		t.Errorf("long-context cost = %+v, want total %v", cost, want)
	}
	priority := event.Event{Agent: "codex", Tier: "priority", InputTokens: 100, CacheReadTokens: 100, OutputTokens: 10}
	cost = price.Cost(&priority)
	want = 2 * (100*2 + 100*0.2 + 10*10) / 1e6
	if cost.Multiplier != 2 || cost.InputRate != 4 || math.Abs(cost.Total-want) > 1e-12 {
		t.Errorf("priority cost = %+v, want total %v", cost, want)
	}
}

func TestResolvePrice(t *testing.T) {
	c, err := parseCatalog([]byte(`{
		"models": {"anthropic/claude-x": {}, "deepseek/deepseek-v9-flash-1017": {}, "google/gemini-4-argon": {}},
		"providers": {
			"anthropic": {"models": {"claude-x": {"canonical_model_id": "anthropic/claude-x", "cost": {"input": 3, "output": 15}}}},
			"deepseek": {"models": {"deepseek-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0.15, "output": 0.6}}}},
			"google": {"models": {"gemini-4-argon": {"cost": {"input": 2, "output": 10}}}},
			"openai": {"models": {"gpt-5.4": {"cost": {"input": 2.5, "output": 15}}, "gpt-5.6-luna": {"cost": {"input": 0.2, "output": 1.2}}}},
			"reseller": {"models": {
				"claude-x": {"canonical_model_id": "anthropic/claude-x", "cost": {"input": 1, "output": 5}},
				"free-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0, "output": 0}},
				"open-y": {"cost": {"input": 1, "output": 2}}
			}},
			"other": {"models": {"open-y": {"id": "vendor/open-y", "cost": {"input": 1, "output": 3}}}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p := &Pricer{custom: map[string]Rates{}, resolved: map[string]*Price{}}
	p.setCatalog(c, time.Now())
	now := time.Now().UnixMilli()
	for _, test := range []struct {
		model string
		at    int64
		want  string
	}{
		{"Proxy/Claude-X", now, "anthropic/claude-x"},              // The creator's listing beats a reseller's.
		{"DeepSeek-V9-Flash-1017", now, "deepseek/deepseek-flash"}, // The creator lists the canonical model under another ID.
		{"free-flash", now, "reseller/free-flash"},                 // A reseller's own model keeps its price.
		{"gemini-4-argon-trial", now, "google/gemini-4-argon"},
		{"codex-auto-review", day("2026-07-29"), "openai/gpt-5.4"},
		{"codex-auto-review", day("2026-07-30"), "openai/gpt-5.6-luna"},
		{"open-y", now, ""}, // Resellers disagree.
	} {
		price := p.Resolve(test.model, test.at)
		got := ""
		if price != nil {
			got = price.Provider + "/" + price.ModelID
		}
		if got != test.want {
			t.Errorf("Resolve(%q, %d) = %q, want %q", test.model, test.at, got, test.want)
		}
	}

	p.SetCustom(map[string]Rates{"gemini-4-argon": {Input: 9, Output: 9}})
	if price := p.Resolve("gemini-4-argon-trial", now); price == nil || price.Source != "custom" || price.As != "gemini-4-argon" || price.Input != 9 {
		t.Errorf("alias does not follow the custom price of its target: %+v", price)
	}
}
