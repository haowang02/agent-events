package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/haowang02/agent-events/internal/pricing"
)

type ModelPrice struct {
	Model    string         `json:"model"`
	Agents   []string       `json:"agents"`
	Requests int            `json:"requests"`
	Tokens   int64          `json:"total_tokens"`
	Price    *pricing.Price `json:"price"`
}

// ModelPrices lists every model seen in events with the price it resolves to
// now.
func (s *Server) ModelPrices() []ModelPrice {
	s.mu.RLock()
	byModel := map[string]*ModelPrice{}
	for i := range s.events {
		e := &s.events[i]
		if e.Model == "" {
			continue
		}
		row := byModel[e.Model]
		if row == nil {
			row = &ModelPrice{Model: e.Model}
			byModel[e.Model] = row
		}
		row.Requests++
		row.Tokens += e.TotalTokens()
		if !slices.Contains(row.Agents, e.Agent) {
			row.Agents = append(row.Agents, e.Agent)
		}
	}
	s.mu.RUnlock()
	now := time.Now().UnixMilli()
	rows := make([]ModelPrice, 0, len(byModel))
	for _, row := range byModel {
		row.Price = s.pricer.Resolve(row.Model, now)
		rows = append(rows, *row)
	}
	slices.SortFunc(rows, func(x, y ModelPrice) int {
		return cmp.Or(cmp.Compare(y.Requests, x.Requests), cmp.Compare(x.Model, y.Model))
	})
	return rows
}

// SetCustomPrice stores a custom price for a model, or removes it when rates
// is nil, and reprices every event.
func (s *Server) SetCustomPrice(model string, rates *pricing.Rates) error {
	key := pricing.ModelKey(model)
	if key == "" {
		return errors.New("model is required")
	}
	var err error
	if rates == nil {
		err = s.store.DeleteCustomPrice(key)
	} else if err = rates.Validate(); err == nil {
		err = s.store.SetCustomPrice(key, *rates)
	}
	if err != nil {
		return err
	}
	custom, err := s.store.CustomPrices()
	if err != nil {
		return err
	}
	s.pricer.SetCustom(custom)
	s.publish(func(*State) {})
	return nil
}

func (s *Server) RefreshPrices(ctx context.Context) error {
	if err := s.pricer.Refresh(ctx); err != nil {
		return err
	}
	s.publish(func(*State) {})
	return nil
}

func (s *Server) handlePrices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": s.ModelPrices(), "catalog": s.pricer.Status()})
}

func (s *Server) handleSetPrice(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model string        `json:"model"`
		Rates pricing.Rates `json:"rates"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := s.SetCustomPrice(request.Model, &request.Rates); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDeletePrice(w http.ResponseWriter, r *http.Request) {
	if err := s.SetCustomPrice(r.URL.Query().Get("model"), nil); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleRefreshPrices(w http.ResponseWriter, r *http.Request) {
	if err := s.RefreshPrices(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSearchPrices(w http.ResponseWriter, r *http.Request) {
	results := s.pricer.Search(r.URL.Query().Get("q"), queryInt(r, "limit", 20, 1, 100))
	writeJSON(w, http.StatusOK, map[string]any{"results": results})
}
