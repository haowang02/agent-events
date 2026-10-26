// Package server keeps the collected events up to date and serves them, with
// the web UI, over HTTP.
package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/event"
	"github.com/haowang02/agent-events/internal/pricing"
	"github.com/haowang02/agent-events/internal/store"
)

type Server struct {
	store      *store.Store
	pricer     *pricing.Pricer
	collectors []collect.Collector
	web        fs.FS
	dataDir    string

	mu     sync.RWMutex
	events []event.Event // Newest first.

	stateMu     sync.Mutex
	state       State
	subscribers map[chan struct{}]struct{}
}

// State is pushed to the page; a new Version means the data it shows changed.
type State struct {
	Ready     bool           `json:"ready"` // The first scan has finished.
	Version   int            `json:"version"`
	UpdatedAt time.Time      `json:"updated_at,omitzero"`
	Error     string         `json:"error,omitempty"`
	Counts    map[string]int `json:"counts"`
	DataDir   string         `json:"data_dir"`
	Home      string         `json:"home"`
}

// New serves the events in the store, priced by pricer, with the web UI.
func New(store *store.Store, pricer *pricing.Pricer, web fs.FS, dataDir string) (*Server, error) {
	s := &Server{
		store: store, pricer: pricer, collectors: collect.All(), web: web, dataDir: dataDir,
		subscribers: map[chan struct{}]struct{}{},
	}
	return s, s.loadEvents()
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(s.web))
	mux.HandleFunc("GET /api/stream", s.handleStream)
	mux.HandleFunc("GET /api/events", s.handleEvents)
	mux.HandleFunc("GET /api/analysis", s.handleAnalysis)
	mux.HandleFunc("GET /api/prices", s.handlePrices)
	mux.HandleFunc("PUT /api/prices", s.handleSetPrice)
	mux.HandleFunc("DELETE /api/prices", s.handleDeletePrice)
	mux.HandleFunc("POST /api/prices/refresh", s.handleRefreshPrices)
	mux.HandleFunc("GET /api/prices/search", s.handleSearchPrices)
	return mux
}

func (s *Server) loadEvents() error {
	events, err := s.store.Events()
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.events = events
	s.mu.Unlock()
	return nil
}

func (s *Server) State() State {
	counts := map[string]int{}
	s.mu.RLock()
	for i := range s.events {
		counts[s.events[i].Agent]++
	}
	s.mu.RUnlock()
	home, _ := os.UserHomeDir()
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	state := s.state
	state.Counts, state.DataDir, state.Home = counts, s.dataDir, home
	return state
}

// publish applies an update and tells every open page to reload.
func (s *Server) publish(update func(*State)) {
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	update(&s.state)
	s.state.Version++
	for subscriber := range s.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}

func (s *Server) subscribe() (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	s.stateMu.Lock()
	s.subscribers[updates] = struct{}{}
	s.stateMu.Unlock()
	return updates, func() {
		s.stateMu.Lock()
		delete(s.subscribers, updates)
		s.stateMu.Unlock()
	}
}

// handleStream sends the current State as server-sent events, once on
// connect and again whenever it is published.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	updates, unsubscribe := s.subscribe()
	defer unsubscribe()
	for {
		data, err := json.Marshal(s.State())
		if err != nil {
			return
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
			return
		}
		flusher.Flush()
		select {
		case <-updates:
		case <-r.Context().Done():
			return
		}
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(value); err != nil {
		log.Printf("write response: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func queryInt(r *http.Request, name string, fallback, minimum, maximum int) int {
	value, err := strconv.Atoi(r.URL.Query().Get(name))
	if err != nil {
		return fallback
	}
	return min(max(value, minimum), maximum)
}
