package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"strconv"
	"time"
)

func (a *App) routes(web fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /", http.FileServerFS(web))
	mux.HandleFunc("GET /api/stream", a.handleStream)
	mux.HandleFunc("GET /api/events", a.handleEvents)
	mux.HandleFunc("GET /api/analysis", a.handleAnalysis)
	mux.HandleFunc("GET /api/prices", a.handlePrices)
	mux.HandleFunc("PUT /api/prices", a.handleSetPrice)
	mux.HandleFunc("DELETE /api/prices", a.handleDeletePrice)
	mux.HandleFunc("POST /api/prices/refresh", a.handleRefreshPrices)
	mux.HandleFunc("GET /api/prices/search", a.handleSearchPrices)
	return mux
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

// handleStream sends the current State as server-sent events, once on
// connect and again whenever it is published.
func (a *App) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported"))
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	updates, unsubscribe := a.subscribe()
	defer unsubscribe()
	for {
		data, err := json.Marshal(a.State())
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

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	sortKey := query.Get("sort")
	if _, ok := eventSorts[sortKey]; !ok {
		sortKey = "time"
	}
	result := a.Events(
		parseFilter(query), sortKey, query.Get("order") != "asc",
		queryInt(r, "offset", 0, 0, 1<<30), queryInt(r, "limit", 50, 1, 500),
	)
	writeJSON(w, http.StatusOK, result)
}

func (a *App) handleAnalysis(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	location, err := time.LoadLocation(query.Get("tz"))
	if err != nil {
		location = time.Local
	}
	writeJSON(w, http.StatusOK, a.Analysis(parseFilter(query), location, query.Get("speed_model")))
}

func (a *App) handlePrices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"models": a.ModelPrices(), "catalog": a.pricer.Status()})
}

func (a *App) handleSetPrice(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Model string `json:"model"`
		Rates Rates  `json:"rates"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&request); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := a.SetCustomPrice(request.Model, &request.Rates); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDeletePrice(w http.ResponseWriter, r *http.Request) {
	if err := a.SetCustomPrice(r.URL.Query().Get("model"), nil); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleRefreshPrices(w http.ResponseWriter, r *http.Request) {
	if err := a.RefreshPrices(r.Context()); err != nil {
		writeError(w, http.StatusBadGateway, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleSearchPrices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"results": a.pricer.Search(r.URL.Query().Get("q"), queryInt(r, "limit", 20, 1, 100))})
}
