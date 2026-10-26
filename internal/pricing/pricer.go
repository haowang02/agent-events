package pricing

import (
	"context"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

const catalogMaxAge = 24 * time.Hour

// alias names the model whose price applies from a time on.
type alias struct {
	since  int64 // Unix milliseconds.
	target string
}

func day(date string) int64 {
	at, _ := time.Parse(time.DateOnly, date)
	return at.UnixMilli()
}

// aliases price models that models.dev has no creator price for as the model
// they bill as, newest first. Codex routes codex-auto-review server-side; its
// timeline follows OpenAI's announcements.
var aliases = map[string][]alias{
	"codex-auto-review": {
		{day("2026-07-30"), "gpt-5.6-luna"}, {day("2026-03-05"), "gpt-5.4"}, {day("2026-02-05"), "gpt-5.3-codex"},
		{day("2025-12-11"), "gpt-5.2-codex"}, {day("2025-11-13"), "gpt-5.1-codex"}, {day("2025-09-15"), "gpt-5-codex"}, {0, "gpt-5"},
	},
	"deepseek-v4.1-flash":  {{0, "deepseek-v4.1-flash-1017"}},
	"gemini-4-argon-trial": {{0, "gemini-4-argon"}},
}

func aliasTarget(key string, at int64) string {
	for _, alias := range aliases[key] {
		if at >= alias.since {
			return alias.target
		}
	}
	return ""
}

func ModelKey(model string) string { return strings.ToLower(strings.TrimSpace(model)) }

type CatalogStatus struct {
	FetchedAt time.Time `json:"fetched_at,omitzero"`
	Models    int       `json:"models"`
	Error     string    `json:"error,omitempty"`
}

// Pricer resolves model prices: a custom price, else the price of the model
// it is an alias of, else the models.dev reference price.
type Pricer struct {
	catalogPath string

	mu       sync.RWMutex
	catalog  *catalog
	status   CatalogStatus
	custom   map[string]Rates
	resolved map[string]*Price

	refreshMu sync.Mutex
}

// NewPricer prices with custom rates by model key and the models.dev catalog
// cached at catalogPath, if any.
func NewPricer(catalogPath string, custom map[string]Rates) *Pricer {
	p := &Pricer{catalogPath: catalogPath, custom: custom, resolved: map[string]*Price{}}
	raw, err := os.ReadFile(catalogPath)
	if err != nil {
		return p
	}
	if c, err := parseCatalog(raw); err != nil {
		log.Printf("cached models.dev catalog: %v", err)
	} else if info, err := os.Stat(catalogPath); err == nil {
		p.setCatalog(c, info.ModTime())
	}
	return p
}

func (p *Pricer) setCatalog(c *catalog, fetched time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.catalog = c
	p.status = CatalogStatus{FetchedAt: fetched, Models: len(c.listings)}
	p.resolved = map[string]*Price{}
}

// SetCustom replaces the custom rates by model key.
func (p *Pricer) SetCustom(custom map[string]Rates) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.custom = custom
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

// Resolve returns the price of a model for a request made at a time in Unix
// milliseconds, or nil when it is unpriced.
func (p *Pricer) Resolve(model string, at int64) *Price {
	key := ModelKey(model)
	p.mu.RLock()
	target := ""
	if _, ok := p.custom[key]; !ok {
		target = aliasTarget(key, at)
	}
	cacheKey := key + "\x00" + target
	price, ok := p.resolved[cacheKey]
	p.mu.RUnlock()
	if ok {
		return price
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	price = p.resolve(key, target)
	p.resolved[cacheKey] = price
	return price
}

func (p *Pricer) resolve(key, target string) *Price {
	if rates, ok := p.custom[key]; ok {
		return &Price{Rates: rates, Source: "custom"}
	}
	if target != "" {
		price := p.resolve(target, "")
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
