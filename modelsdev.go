package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"
)

const (
	modelsDevURL      = "https://models.dev/catalog.json"
	modelsDevMaxBytes = 128 << 20
	modelsDevTimeout  = 3 * time.Minute
)

// ReferencePrice is one provider's listing on models.dev. Canonical is the
// models.dev model it serves; Rates is nil when its price cannot be expressed
// as per-token rates.
type ReferencePrice struct {
	Provider  string `json:"provider"`
	ModelID   string `json:"model_id"`
	Canonical string `json:"canonical,omitempty"`
	Rates     *Rates `json:"rates"`
}

// firstParty reports whether the listing comes from the model's creator.
func (r ReferencePrice) firstParty() bool {
	creator, _, ok := strings.Cut(r.Canonical, "/")
	return ok && strings.EqualFold(creator, r.Provider)
}

type catalog struct {
	listings    []ReferencePrice
	byModel     map[string][]ReferencePrice // By referenceKey of ModelID.
	byCanonical map[string][]ReferencePrice // By referenceKey of Canonical; first-party listings only.
}

type modelsDevRates struct {
	Input       *float64 `json:"input"`
	Output      *float64 `json:"output"`
	Reasoning   *float64 `json:"reasoning"`
	CacheRead   *float64 `json:"cache_read"`
	CacheWrite  *float64 `json:"cache_write"`
	InputAudio  *float64 `json:"input_audio"`
	OutputAudio *float64 `json:"output_audio"`
}

type modelsDevCost struct {
	modelsDevRates
	Tiers []struct {
		modelsDevRates
		Tier struct {
			Type string `json:"type"`
			Size int64  `json:"size"`
		} `json:"tier"`
	} `json:"tiers"`
}

// textOnly reports whether reasoning and audio cost the same as text.
func (r modelsDevRates) textOnly() bool {
	same := func(special, standard *float64) bool {
		return special == nil || (standard != nil && *special == *standard)
	}
	return r.Input != nil && r.Output != nil && same(r.Reasoning, r.Output) && same(r.InputAudio, r.Input) && same(r.OutputAudio, r.Output)
}

// catalogRates keeps prices that text token rates with at most one context
// tier represent exactly.
func catalogRates(cost *modelsDevCost) *Rates {
	if cost == nil || !cost.textOnly() || len(cost.Tiers) > 1 {
		return nil
	}
	rates := &Rates{Input: *cost.Input, Output: *cost.Output, CacheRead: cost.CacheRead, CacheWrite: cost.CacheWrite}
	if len(cost.Tiers) == 1 {
		tier := cost.Tiers[0]
		if tier.Tier.Type != "context" || tier.Tier.Size <= 0 || !tier.textOnly() {
			return nil
		}
		rates.LongContext = &LongContext{
			Threshold: tier.Tier.Size, Input: *tier.Input, Output: *tier.Output, CacheRead: tier.CacheRead, CacheWrite: tier.CacheWrite,
		}
	}
	if rates.Validate() != nil {
		return nil
	}
	return rates
}

func parseCatalog(raw []byte) (*catalog, error) {
	var source struct {
		Models    map[string]json.RawMessage `json:"models"`
		Providers map[string]struct {
			Models map[string]struct {
				ID        string         `json:"id"`
				Canonical string         `json:"canonical_model_id"`
				Cost      *modelsDevCost `json:"cost"`
			} `json:"models"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		return nil, err
	}
	c := &catalog{byModel: map[string][]ReferencePrice{}, byCanonical: map[string][]ReferencePrice{}}
	priced := 0
	for provider, listings := range source.Providers {
		for key, model := range listings.Models {
			listing := ReferencePrice{Provider: provider, ModelID: cmp.Or(model.ID, key), Canonical: model.Canonical, Rates: catalogRates(model.Cost)}
			if _, ok := source.Models[provider+"/"+listing.ModelID]; ok && listing.Canonical == "" {
				listing.Canonical = provider + "/" + listing.ModelID
			}
			if listing.Rates != nil {
				priced++
			}
			c.listings = append(c.listings, listing)
		}
	}
	if priced == 0 {
		return nil, errors.New("models.dev returned no usable prices")
	}
	slices.SortFunc(c.listings, func(a, b ReferencePrice) int {
		return cmp.Or(cmp.Compare(a.Provider, b.Provider), cmp.Compare(a.ModelID, b.ModelID))
	})
	for _, listing := range c.listings {
		key := referenceKey(listing.ModelID)
		c.byModel[key] = append(c.byModel[key], listing)
		if listing.firstParty() {
			key = referenceKey(listing.Canonical)
			c.byCanonical[key] = append(c.byCanonical[key], listing)
		}
	}
	return c, nil
}

// referenceKey drops a namespace such as "deepseek/" and case from a model ID.
func referenceKey(id string) string {
	key := strings.ToLower(strings.TrimSpace(id))
	return key[strings.LastIndex(key, "/")+1:]
}

// match prices a model the way its creator does: the creator's listing of
// that model ID, else the creator's listing of the canonical model with that
// name. A model without a creator listing takes the price that every listing
// agrees on. Conflicting candidates leave the model unpriced.
func (c *catalog) match(model string) (ReferencePrice, bool) {
	key := referenceKey(model)
	listings := c.byModel[key]
	own := slices.DeleteFunc(slices.Clone(listings), func(listing ReferencePrice) bool { return !listing.firstParty() })
	for _, candidates := range [][]ReferencePrice{own, c.byCanonical[key], listings} {
		if len(candidates) == 0 {
			continue
		}
		for _, candidate := range candidates {
			if candidate.Rates == nil || !candidate.Rates.equal(candidates[0].Rates) {
				return ReferencePrice{}, false
			}
		}
		return candidates[0], true
	}
	return ReferencePrice{}, false
}

// search lists priced listings whose provider/model ID contains every word of
// the query, exact model IDs and creators' listings first.
func (c *catalog) search(query string, limit int) []ReferencePrice {
	words := strings.Fields(strings.ToLower(query))
	exact := strings.Join(words, " ")
	matches := []ReferencePrice{}
	for _, listing := range c.listings {
		name := strings.ToLower(listing.Provider + "/" + listing.ModelID)
		if listing.Rates != nil && !slices.ContainsFunc(words, func(word string) bool { return !strings.Contains(name, word) }) {
			matches = append(matches, listing)
		}
	}
	rank := func(listing ReferencePrice) int {
		score := 0
		if referenceKey(listing.ModelID) == exact {
			score -= 2
		}
		if listing.firstParty() {
			score--
		}
		return score
	}
	slices.SortStableFunc(matches, func(a, b ReferencePrice) int { return rank(a) - rank(b) })
	return matches[:min(len(matches), limit)]
}

func downloadCatalog(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, modelsDevTimeout)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, modelsDevURL, nil)
	if err != nil {
		return nil, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("download models.dev: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("download models.dev: HTTP %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, modelsDevMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("download models.dev: %w", err)
	}
	if len(raw) > modelsDevMaxBytes {
		return nil, errors.New("models.dev catalog exceeds the size limit")
	}
	return raw, nil
}
