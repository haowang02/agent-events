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
			"anthropic": {"models": {
				"claude-x": {"cost": {"input": 3, "output": 15}},
				"sonnet-medium": {"canonical_model_id": "anthropic/claude-x-medium", "cost": {"input": 5, "output": 25}},
				"claude-x-low": {"cost": {"input": 3, "output": 15, "input_audio": 9}}
			}},
			"deepseek": {"models": {"deepseek-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0.15, "output": 0.6}}}},
			"google": {"models": {
				"gemini-4-argon": {"cost": {"input": 2, "output": 10}},
				"gemini-4-argon-trial": {"cost": {"input": 0, "output": 0}}
			}},
			"openai": {"models": {"gpt-5.4": {"cost": {"input": 2.5, "output": 15}}, "gpt-5.6-luna": {"cost": {"input": 0.2, "output": 1.2}}}},
			"reseller": {"models": {
				"claude-x": {"canonical_model_id": "anthropic/claude-x", "cost": {"input": 1, "output": 5}},
				"free-flash": {"canonical_model_id": "deepseek/deepseek-v9-flash-1017", "cost": {"input": 0, "output": 0}},
				"open-y": {"cost": {"input": 1, "output": 2}},
				"claude-x-high": {"cost": {"input": 1, "output": 2}}
			}},
			"other": {"models": {
				"open-y": {"id": "vendor/open-y", "cost": {"input": 1, "output": 3}},
				"claude-x-high": {"cost": {"input": 1, "output": 3}}
			}}
		}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	p := &Pricer{}
	p.setCatalog(c, time.Now())
	now := time.Now().UnixMilli()
	for _, test := range []struct {
		model string
		at    int64
		want  string
		as    string
	}{
		{"Proxy/Claude-X", now, "anthropic/claude-x", ""},
		{"DeepSeek-V9-Flash-1017", now, "deepseek/deepseek-flash", ""},
		{"free-flash", now, "reseller/free-flash", ""},
		{"gemini-4-argon-trial", now, "google/gemini-4-argon", "gemini-4-argon"},
		{"codex-auto-review", day("2026-07-29"), "openai/gpt-5.4", "gpt-5.4"},
		{"codex-auto-review", day("2026-07-30"), "openai/gpt-5.6-luna", "gpt-5.6-luna"},
		{"open-y", now, "", ""},
		{" Proxy/Gemini-4-Argon-A ", now, "google/gemini-4-argon", "gemini-4-argon"},
		{"gemini-4-argon-z", now, "google/gemini-4-argon", "gemini-4-argon"},
		{"gemini-4-argon-low", now, "google/gemini-4-argon", "gemini-4-argon"},
		{"gemini-4-argon-medium", now, "google/gemini-4-argon", "gemini-4-argon"},
		{"free-flash-high", now, "reseller/free-flash", "free-flash"},
		{"claude-x-medium", now, "anthropic/sonnet-medium", ""},
		{"claude-x-low", now, "", ""},
		{"claude-x-high", now, "", ""},
		{"open-y-high", now, "", ""},
		{"missing-high", now, "", ""},
		{"gemini-4-argon-nn", now, "", ""},
		{"gemini-4-argon-1", now, "", ""},
		{"claude-x-n", now, "", ""},
		{"gemini-4-argon-high-low", now, "", ""},
	} {
		t.Run(test.model+"/"+time.UnixMilli(test.at).Format(time.DateOnly), func(t *testing.T) {
			price := p.Resolve(test.model, test.at)
			if test.want == "" {
				if price != nil {
					t.Fatalf("Resolve(%q) = %+v, want unpriced", test.model, price)
				}
				return
			}
			if price == nil || price.Source != "models.dev" || price.Provider+"/"+price.ModelID != test.want || price.As != test.as {
				t.Fatalf("Resolve(%q) = %+v, want %s as %q", test.model, price, test.want, test.as)
			}
		})
	}

	p.SetCustom(map[string]Rates{"gemini-4-argon": {Input: 9, Output: 9}, "claude-x": {Input: 9, Output: 9}})
	for _, model := range []string{"gemini-4-argon-trial", "gemini-4-argon-n"} {
		if price := p.Resolve(model, now); price == nil || price.Source != "custom" || price.As != "gemini-4-argon" || price.Input != 9 {
			t.Errorf("Resolve(%q) did not follow the target's custom price: %+v", model, price)
		}
	}
	if price := p.Resolve("claude-x-medium", now); price == nil || price.Source != "models.dev" || price.As != "" || price.Input != 5 {
		t.Errorf("exact listing lost to base custom price: %+v", price)
	}
	for _, model := range []string{"claude-x-low", "claude-x-high"} {
		if price := p.Resolve(model, now); price != nil {
			t.Errorf("unpriced listing %q fell back to base custom price: %+v", model, price)
		}
	}
}

func TestPriceUpdates(t *testing.T) {
	p := &Pricer{resolved: map[string]*Price{}}
	now := time.Now().UnixMilli()
	model, base := "claude-opus-5-5-medium", "claude-opus-5-5"
	if price := p.Resolve(model, now); price != nil {
		t.Fatalf("missing catalog and custom rates: %+v", price)
	}
	p.SetCustom(map[string]Rates{base: {Input: 9, Output: 10}})
	if price := p.Resolve(model, now); price == nil || price.Source != "custom" || price.As != base || price.Input != 9 {
		t.Fatalf("base custom rate: %+v", price)
	}
	p.SetCustom(map[string]Rates{base: {Input: 9, Output: 10}, model: {}})
	if price := p.Resolve(model, now); price == nil || price.Source != "custom" || price.As != "" || price.Input != 0 {
		t.Fatalf("original custom rate must override fallback, including zero: %+v", price)
	}
	p.SetCustom(nil)
	if price := p.Resolve(model, now); price != nil {
		t.Fatalf("removed custom rates stayed cached: %+v", price)
	}
	for _, catalogJSON := range []string{
		`{"providers":{"anthropic":{"models":{"claude-opus-5-5":{"cost":{"input":5,"output":25}}}}}}`,
		`{"providers":{"anthropic":{"models":{"claude-opus-5-5-medium":{"cost":{"input":7,"output":35}}}}}}`,
	} {
		c, err := parseCatalog([]byte(catalogJSON))
		if err != nil {
			t.Fatal(err)
		}
		p.setCatalog(c, time.Now())
		price := p.Resolve(model, now)
		if price == nil || price.ModelID != c.listings[0].ModelID || price.Input != c.listings[0].Rates.Input {
			t.Fatalf("catalog refresh did not replace cached price: %+v", price)
		}
		wantAs := ""
		if price.ModelID == base {
			wantAs = base
		}
		if price.As != wantAs {
			t.Fatalf("catalog refresh: As = %q, want %q", price.As, wantAs)
		}
	}
}
