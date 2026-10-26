package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"runtime"
	"sort"
	"sync"
	"time"
)

const watchInterval = 3 * time.Second

type App struct {
	store      *Store
	pricer     *Pricer
	collectors []*Collector
	dataDir    string

	mu     sync.RWMutex
	events []Event // Newest first.

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

func newApp(store *Store, pricer *Pricer, dataDir string) (*App, error) {
	app := &App{store: store, pricer: pricer, collectors: collectors(), dataDir: dataDir, subscribers: map[chan struct{}]struct{}{}}
	return app, app.loadEvents()
}

func (a *App) loadEvents() error {
	events, err := a.store.Events()
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.events = events
	a.mu.Unlock()
	return nil
}

func (a *App) State() State {
	counts := map[string]int{}
	a.mu.RLock()
	for i := range a.events {
		counts[a.events[i].Agent]++
	}
	a.mu.RUnlock()
	home, _ := os.UserHomeDir()
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	state := a.state
	state.Counts, state.DataDir, state.Home = counts, a.dataDir, home
	return state
}

// publish applies an update and tells every open page to reload.
func (a *App) publish(update func(*State)) {
	a.stateMu.Lock()
	defer a.stateMu.Unlock()
	update(&a.state)
	a.state.Version++
	for subscriber := range a.subscribers {
		select {
		case subscriber <- struct{}{}:
		default:
		}
	}
}

func (a *App) subscribe() (<-chan struct{}, func()) {
	updates := make(chan struct{}, 1)
	a.stateMu.Lock()
	a.subscribers[updates] = struct{}{}
	a.stateMu.Unlock()
	return updates, func() {
		a.stateMu.Lock()
		delete(a.subscribers, updates)
		a.stateMu.Unlock()
	}
}

// Watch rescans the agents' logs forever. A scan only parses sources whose
// size or modification time changed, so an idle pass costs a directory walk.
func (a *App) Watch() {
	lastError := ""
	for first := true; ; first = false {
		changed, err := a.scan()
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != lastError {
			log.Printf("scan: %s", message)
		}
		if first || changed || message != lastError {
			a.publish(func(state *State) {
				state.Ready = true
				state.Error = message
				if changed {
					state.UpdatedAt = time.Now()
				}
			})
		}
		lastError = message
		time.Sleep(watchInterval)
	}
}

// scan parses new and changed sources. It reports whether any request was
// added or changed, which a source changing does not imply.
func (a *App) scan() (changed bool, err error) {
	known, err := a.store.Sources()
	if err != nil {
		return false, err
	}
	type job struct {
		collector *Collector
		source    Source
	}
	var jobs []job
	var problems []string
	for _, collector := range a.collectors {
		sources, err := collector.List()
		if err != nil {
			problems = append(problems, collector.Agent+": "+err.Error())
			continue
		}
		for _, source := range sources {
			if previous, ok := known[source.Path]; !ok || previous != source {
				jobs = append(jobs, job{collector, source})
			}
		}
	}
	// Large files first keeps the workers busy until the end.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].source.Size > jobs[j].source.Size })

	type result struct {
		source Source
		events []Event
		err    error
	}
	queue := make(chan job)
	results := make(chan result)
	var workers sync.WaitGroup
	for range min(runtime.NumCPU(), 8, len(jobs)) {
		workers.Go(func() {
			for item := range queue {
				events, err := item.collector.Parse(item.source.Path)
				results <- result{item.source, events, err}
			}
		})
	}
	go func() {
		for _, item := range jobs {
			queue <- item
		}
		close(queue)
		workers.Wait()
		close(results)
	}()

	var events []Event
	var sources []Source
	var saveErr error
	flush := func() {
		if len(sources) > 0 && saveErr == nil {
			var saved int64
			saved, saveErr = a.store.Save(events, sources)
			changed = changed || saved > 0
		}
		events, sources = nil, nil
	}
	for item := range results {
		if item.err != nil {
			// Its signature is not stored, so the source is retried on the next scan.
			problems = append(problems, item.source.Path+": "+item.err.Error())
			continue
		}
		events = append(events, item.events...)
		sources = append(sources, item.source)
		if len(events) >= 20000 || len(sources) >= 200 {
			flush()
		}
	}
	flush()
	if changed {
		if err := a.loadEvents(); err != nil {
			return changed, err
		}
	}
	if saveErr != nil {
		return changed, saveErr
	}
	if len(problems) > 0 {
		return changed, fmt.Errorf("%d sources could not be read; first: %s", len(problems), problems[0])
	}
	return changed, nil
}

func (a *App) RefreshPrices(ctx context.Context) error {
	if err := a.pricer.Refresh(ctx); err != nil {
		return err
	}
	a.publish(func(*State) {})
	return nil
}

func (a *App) SetCustomPrice(model string, rates *Rates) error {
	if err := a.pricer.SetCustom(model, rates); err != nil {
		return err
	}
	a.publish(func(*State) {})
	return nil
}
