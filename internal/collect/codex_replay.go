package collect

import (
	"slices"
	"sync"

	"github.com/haowang02/agent-events/internal/event"
)

type codexReplayUsage struct {
	index     int
	at        int64
	record    bool
	totalOnly bool
	total     *codexUsage
}

type codexTranscript struct {
	events     []event.Event
	usage      []codexReplayUsage
	metadata   codexPayload
	start      int64
	countTimes []int64
	sessions   []string
}

// Only raw parsing is cached. Resolving ancestry uses fresh copies, so callers
// can resolve overlapping fork trees concurrently without sharing mutations.
type codexCached struct {
	source   Source
	metadata codexPayload
	once     sync.Once
	data     *codexTranscript
	err      error
}

func (t *codexTranscript) result() Result {
	if t == nil {
		return Result{}
	}
	return Result{Events: slices.DeleteFunc(slices.Clone(t.events), func(e event.Event) bool { return e.ID == "" }), Sessions: t.sessions}
}

func codexSameUsage(a, b event.Event) bool {
	return a.InputTokens == b.InputTokens && a.CacheReadTokens == b.CacheReadTokens &&
		a.CacheWriteTokens == b.CacheWriteTokens && a.OutputTokens == b.OutputTokens &&
		a.ReasoningTokens == b.ReasoningTokens
}

func readCodexFamily(path string, paths map[string]string, cache map[string]*codexCached, visiting map[string]bool) (*codexTranscript, bool, error) {
	if visiting[path] {
		return nil, false, nil
	}
	visiting[path] = true
	defer delete(visiting, path)
	entry := cache[path]
	if entry == nil {
		entry = &codexCached{}
	}
	entry.once.Do(func() { entry.data, entry.err = parseCodexTranscript(path) })
	if entry.err != nil {
		return nil, false, entry.err
	}
	t := *entry.data
	t.events = slices.Clone(t.events)
	parentID := t.metadata.parentSession()
	if parentID == "" {
		return &t, true, nil
	}
	parentPath := paths[parentID]
	parent := &codexTranscript{}
	valid := false
	if parentPath != "" {
		var err error
		parent, valid, err = readCodexFamily(parentPath, paths, cache, visiting)
		if err != nil || parent == nil || !valid {
			return &t, false, nil
		}
	}
	var prefix []codexReplayUsage
	var summed event.Event
	var inherited *codexUsage
	for _, u := range parent.usage {
		e := parent.events[u.index]
		if t.start > 0 && u.at > t.start {
			continue
		}
		if u.total != nil {
			inherited = u.total
		}
		if e.Kind == "compaction" || e.ID == "" {
			continue
		}
		prefix = append(prefix, u)
		summed.InputTokens += e.InputTokens
		summed.CacheReadTokens += e.CacheReadTokens
		summed.CacheWriteTokens += e.CacheWriteTokens
		summed.OutputTokens += e.OutputTokens
		summed.ReasoningTokens += e.ReasoningTokens
	}
	if inherited != nil {
		inherited.apply(&summed)
	}
	// Spawned threads drop their parent's response records; the first such
	// record therefore ends replay. User forks can copy response records too.
	spawned := t.metadata.spawnParent() != ""
	burst := len(t.countTimes) >= 2 && t.start > 0 && t.countTimes[0] >= t.start-1000 &&
		t.countTimes[0] <= t.start+1000 && t.countTimes[1] >= t.countTimes[0] && t.countTimes[1]-t.countTimes[0] <= 1000
	skipping := false
	matched := 0
	previous := t.start
	for _, u := range t.usage {
		if spawned && u.record {
			break
		}
		if t.events[u.index].Kind == "compaction" {
			continue
		}
		// Copies retain their old timestamp or are rewritten as a dense burst
		// at the fork instant. A later equal-sized real request is independent.
		if t.start > 0 && u.at > t.start && (previous == 0 || u.at-previous > 1000) {
			break
		}
		e := &t.events[u.index]
		var cumulative event.Event
		if u.total != nil {
			u.total.apply(&cumulative)
		}
		if skipping {
			e.ID = ""
			previous = u.at
			continue
		}
		if matched == 0 && t.start > 0 && (u.totalOnly || u.total != nil && codexSameUsage(*e, cumulative)) &&
			(len(prefix) > 1 || len(prefix) == 0 && inherited != nil) && codexSameUsage(*e, summed) {
			// A cumulative-only inherited snapshot establishes a baseline, not
			// another request for the parent's entire history.
			e.ID = ""
			break
		}
		if matched >= len(prefix) || !codexSameUsage(*e, parent.events[prefix[matched].index]) {
			if matched == 0 && burst {
				skipping = true
				e.ID = ""
				previous = u.at
				continue
			}
			break
		}
		original := parent.events[prefix[matched].index]
		original.Source = path
		*e = original
		matched++
		previous = u.at
	}
	return &t, valid, nil
}
