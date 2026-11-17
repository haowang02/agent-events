package server

import (
	"fmt"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/haowang02/agent-events/internal/collect"
	"github.com/haowang02/agent-events/internal/store"
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

// scan parses new and changed sources, and sources read by an older parser.
// It reports whether any event was added, changed or removed, which a source
// changing does not imply.
func (s *Server) scan() (changed bool, err error) {
	known, err := s.store.Sources()
	if err != nil {
		return false, err
	}
	type job struct {
		collector collect.Collector
		source    collect.Source
		replace   bool
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
			source.Parser = collector.Version
			previous, ok := known[source.Path]
			if ok && previous == source {
				continue
			}
			jobs = append(jobs, job{collector, source, ok && previous.Parser != source.Parser})
		}
	}
	// Large sources first keeps the workers busy until the end.
	sort.Slice(jobs, func(i, j int) bool { return jobs[i].source.Size > jobs[j].source.Size })

	type result struct {
		store.Parsed
		err error
	}
	queue := make(chan job)
	results := make(chan result)
	var workers sync.WaitGroup
	for range min(runtime.NumCPU(), 8, len(jobs)) {
		workers.Go(func() {
			for item := range queue {
				parsed, err := item.collector.Parse(item.source.Path)
				results <- result{store.Parsed{Source: item.source, Result: parsed, Replace: item.replace}, err}
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

	var batch []store.Parsed
	var batchEvents int
	var saveErr error
	flush := func() {
		if len(batch) > 0 && saveErr == nil {
			var saved int64
			saved, saveErr = s.store.Save(batch)
			changed = changed || saved > 0
		}
		batch, batchEvents = nil, 0
	}
	for item := range results {
		if item.err != nil {
			// Its signature is not stored, so the source is retried on the next scan.
			problems = append(problems, item.Source.Path+": "+item.err.Error())
			continue
		}
		batch = append(batch, item.Parsed)
		batchEvents += len(item.Events)
		if batchEvents >= 20000 || len(batch) >= 200 {
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
