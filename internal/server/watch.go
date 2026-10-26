package server

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/event"
)

const watchInterval = 3 * time.Second

// Watch rescans the agents' logs forever. A scan only parses sources whose
// size or modification time changed, so an idle pass costs a directory walk.
func (s *Server) Watch() {
	lastError := ""
	for first := true; ; first = false {
		changed, err := s.scan()
		message := ""
		if err != nil {
			message = err.Error()
		}
		if message != "" && message != lastError {
			log.Printf("scan: %s", message)
		}
		if first || changed || message != lastError {
			s.publish(func(state *State) {
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

// scan parses new and changed sources. It reports whether any event was
// added or changed, which a source changing does not imply.
func (s *Server) scan() (changed bool, err error) {
	known, err := s.store.Sources()
	if err != nil {
		return false, err
	}
	type job struct {
		collector collect.Collector
		source    collect.Source
	}
	var jobs []job
	var problems []string
	for _, collector := range s.collectors {
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
	// Large sources first keeps the workers busy until the end.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].source.Size > jobs[j].source.Size })

	type result struct {
		source collect.Source
		events []event.Event
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

	var events []event.Event
	var sources []collect.Source
	var saveErr error
	flush := func() {
		if len(sources) > 0 && saveErr == nil {
			var saved int64
			saved, saveErr = s.store.Save(events, sources)
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
		if err := s.loadEvents(); err != nil {
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
