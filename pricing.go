package main

import (
	"context"
	"errors"
	"log"
	"math"
	"os"
	"strings"
	"sync"
	"time"
)

const catalogMaxAge = 24 * time.Hour

// Rates are USD per million tokens. Missing cache rates fall back to the
// input rate. Anthropic bills 1h-TTL cache writes at twice the input rate.
type Rates struct {
	Input       float64      `json:"input"`
	Output      float64      `json:"output"`
	CacheRead   *float64     `json:"cache_read,omitempty"`
	CacheWrite  *float64     `json:"cache_write,omitempty"`
	LongContext *LongContext `json:"long_context,omitempty"`
}

// LongContext rates apply when a request's prompt exceeds Threshold tokens.
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

type Cost struct {
	Total            float64 `json:"total"`
	Input            float64 `json:"input"`
	CacheRead        float64 `json:"cache_read"`
	CacheWrite       float64 `json:"cache_write"`
	Output           float64 `json:"output"`
	LongContext      bool    `json:"long_context,omitempty"`
	InputRate        float64 `json:"input_rate"`
	OutputRate       float64 `json:"output_rate"`
	CacheReadRate    float64 `json:"cache_read_rate"`
	CacheWriteRate   float64 `json:"cache_write_rate"`
	CacheWrite1hRate float64 `json:"cache_write_1h_rate,omitempty"`
}

func perMillion(tokens int64, rate float64) float64 { return float64(tokens) * rate / 1e6 }

func (p *Price) Cost(e *Event) Cost {
	input, output := p.Input, p.Output
	cacheRead, cacheWrite := p.CacheRead, p.CacheWrite
	long := false
	if tier := p.LongContext; tier != nil && e.PromptTokens() > tier.Threshold {
		long = true
		input, output = tier.Input, tier.Output
		cacheRead, cacheWrite = tier.CacheRead, tier.CacheWrite
	}
	rate := func(value *float64, fallback float64) float64 {
		if value != nil {
			return *value
		}
		return fallback
	}
	cost := Cost{
		LongContext:    long,
		InputRate:      input,
		OutputRate:     output,
		CacheReadRate:  rate(cacheRead, input),
		CacheWriteRate: rate(cacheWrite, input),
	}
	cost.Input = perMillion(e.InputTokens, input)
	cost.CacheRead = perMillion(e.CacheReadTokens, cost.CacheReadRate)
	cost.CacheWrite = perMillion(e.CacheWriteTokens-e.CacheWrite1hTokens, cost.CacheWriteRate)
	if e.CacheWrite1hTokens > 0 {
		cost.CacheWrite1hRate = 2 * input
		cost.CacheWrite += perMillion(e.CacheWrite1hTokens, cost.CacheWrite1hRate)
	}
	cost.Output = perMillion(e.OutputTokens, output)
	cost.Total = cost.Input + cost.CacheRead + cost.CacheWrite + cost.Output
	return cost
}

// priceAliases name the model whose price applies to models that models.dev
// has no creator price for.
var priceAliases = map[string]string{
	"codex-auto-review":    "gpt-5.6-luna",
	"deepseek-v4.1-flash":  "deepseek-v4.1-flash-1017",
	"gemini-4-argon-trial": "gemini-4-argon",
}

func modelKey(model string) string { return strings.ToLower(strings.TrimSpace(model)) }

type CatalogStatus struct {
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Models    int       `json:"models"`
	Error     string    `json:"error,omitempty"`
}

// Pricer resolves model prices: a custom price, else the price of the model
// it is an alias of, else the models.dev reference price.
type Pricer struct {
	store       *Store
	catalogPath string

	mu       sync.RWMutex
	catalog  *catalog
	status   CatalogStatus
	custom   map[string]Rates
	resolved map[string]*Price

	refreshMu sync.Mutex
}

func newPricer(store *Store, catalogPath string) (*Pricer, error) {
	custom, err := store.CustomPrices()
	if err != nil {
		return nil, err
	}
	p := &Pricer{store: store, catalogPath: catalogPath, custom: custom, resolved: map[string]*Price{}}
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return p, nil
	}
	if c, err := parseCatalog(raw); err != nil {
		log.Printf("cached models.dev catalog: %v", err)
	} else if info, err := os.Stat(catalogPath); err == nil {
		p.setCatalog(c, info.ModTime())
	}
	return p, nil
}

func (p *Pricer) setCatalog(c *catalog, fetched time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.catalog = c
	p.status = CatalogStatus{FetchedAt: fetched, Models: len(c.listings)}
	p.resolved = map[string]*Price{}
}

func (p *Pricer) Status() CatalogStatus {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.status
}

func (p *Pricer) Stale() bool {
	status := p.Status()
	return status.Models == 0 || time.Since(status.FetchedAt) > catalogMaxAge
}

// Resolve returns the price of a model, or nil when it is unpriced.
func (p *Pricer) Resolve(model string) *Price {
	key := modelKey(model)
	p.mu.RLock()
	price, ok := p.resolved[key]
	p.mu.RUnlock()
	if ok {
		return price
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	price = p.resolve(key)
	p.resolved[key] = price
	return price
}

func (p *Pricer) resolve(key string) *Price {
	if rates, ok := p.custom[key]; ok {
		return &Price{Rates: rates, Source: "custom"}
	}
	if target, ok := priceAliases[key]; ok {
		price := p.resolve(target)
		if price != nil {
			aliased := *price
			aliased.As = target
			price = &aliased
		}
		return price
	}
	if p.catalog == nil {
		return nil
	}
	if listing, ok := p.catalog.match(key); ok {
		return &Price{Rates: *listing.Rates, Source: "models.dev", Provider: listing.Provider, ModelID: listing.ModelID}
	}
	return nil
}

// SetCustom stores a custom price for a model, or removes it when rates is nil.
func (p *Pricer) SetCustom(model string, rates *Rates) error {
	key := modelKey(model)
	if key == "" {
		return errors.New("model is required")
	}
	var err error
	if rates == nil {
		err = p.store.DeleteCustomPrice(key)
	} else if err = rates.Validate(); err == nil {
		err = p.store.SetCustomPrice(key, *rates)
	}
	if err != nil {
		return err
	}
	custom, err := p.store.CustomPrices()
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.custom = custom
	p.resolved = map[string]*Price{}
	return nil
}

func (p *Pricer) Search(query string, limit int) []ReferencePrice {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.catalog == nil {
		return []ReferencePrice{}
	}
	return p.catalog.search(query, limit)
}

// Refresh downloads the models.dev catalog and caches it on disk.
func (p *Pricer) Refresh(ctx context.Context) error {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	raw, err := downloadCatalog(ctx)
	var c *catalog
	if err == nil {
		c, err = parseCatalog(raw)
	}
	if err == nil {
		temporary := p.catalogPath + ".tmp"
		if err = os.WriteFile(temporary, raw, 0o644); err == nil {
			err = os.Rename(temporary, p.catalogPath)
		}
	}
	if err != nil {
		p.mu.Lock()
		p.status.Error = err.Error()
		p.mu.Unlock()
		return err
	}
	p.setCatalog(c, time.Now())
	return nil
}
