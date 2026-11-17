package collect

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

type piEntryLink struct {
	id     string
	parent json.RawMessage
}

// Read the declared ancestry before assigning copied calls their parent's
// identity. Equal usage alone never establishes that two requests are copies.
func readPiFamily(agent, path string, visiting map[string]bool, cache map[string]*piTranscript) (*piTranscript, error) {
	key, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if canonical, err := filepath.EvalSymlinks(key); err == nil {
		key = canonical
	}
	if visiting[key] {
		return nil, nil
	}
	if cached := cache[key]; cached != nil {
		return cached, nil
	}
	visiting[key] = true
	defer delete(visiting, key)
	session, _, _ := strings.Cut(filepath.Base(path), ".jsonl")
	t := &piTranscript{agent: agent, source: path, session: session, at: fileTime(path)}
	if err := readLines(path, t.add); err != nil {
		return nil, err
	}
	if t.parent != "" && t.lineage {
		parentPath := t.parent
		if !filepath.IsAbs(parentPath) {
			// Session headers can name either a cwd-relative path or a sibling.
			if !isRegularFile(parentPath) {
				parentPath = filepath.Join(filepath.Dir(path), parentPath)
			}
		}
		parent, err := readPiFamily(agent, parentPath, visiting, cache)
		if err != nil || parent == nil || !parent.lineage {
			t.lineage = false
		} else {
			active, valid := parent.activeUsage()
			_, childValid := t.activeUsage()
			if valid && childValid {
				for i, p := range active {
					if i >= len(t.events) || parent.events[p].Time > t.forkTime || !piSameUsage(t.events[i], parent.events[p]) {
						break
					}
					t.events[i].ID = parent.events[p].ID
				}
			}
		}
	}
	cache[key] = t
	return t, nil
}

func isRegularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

func piSameUsage(a, b event.Event) bool {
	return a.Time == b.Time && a.Model == b.Model && a.Provider == b.Provider &&
		a.InputTokens == b.InputTokens && a.CacheReadTokens == b.CacheReadTokens &&
		a.CacheWriteTokens == b.CacheWriteTokens && a.OutputTokens == b.OutputTokens &&
		a.Failed == b.Failed
}

// Only the active parent branch is copied by a fork. Other branches remain
// billable requests in their original file and cannot justify dropping calls.
func (t *piTranscript) activeUsage() ([]int, bool) {
	linked := false
	for _, link := range t.links {
		if link.id != "" || len(link.parent) > 0 && string(link.parent) != "null" {
			linked = true
			break
		}
	}
	if !linked {
		indices := make([]int, len(t.events))
		for i := range indices {
			indices[i] = i
		}
		return indices, true
	}
	parents := map[string]string{}
	for _, link := range t.links {
		if link.id == "" {
			return nil, false
		}
		if _, duplicate := parents[link.id]; duplicate {
			return nil, false
		}
		var parent string
		if len(link.parent) > 0 && string(link.parent) != "null" {
			if json.Unmarshal(link.parent, &parent) != nil || parent == "" {
				return nil, false
			}
		}
		parents[link.id] = parent
	}
	// Validate all components, including inactive siblings; a partial or cyclic
	// graph cannot establish which history was copied.
	checked := map[string]bool{}
	for id := range parents {
		chain := map[string]bool{}
		for id != "" && !checked[id] {
			if chain[id] {
				return nil, false
			}
			chain[id] = true
			parent, exists := parents[id]
			if !exists {
				return nil, false
			}
			id = parent
		}
		for id := range chain {
			checked[id] = true
		}
	}
	usage := map[string]int{}
	for i, id := range t.entryIDs {
		if id != "" {
			usage[id] = i
		}
	}
	var reverse []int
	for id := t.links[len(t.links)-1].id; id != ""; id = parents[id] {
		if i, ok := usage[id]; ok {
			reverse = append(reverse, i)
		}
	}
	indices := make([]int, len(reverse))
	for i := range reverse {
		indices[i] = reverse[len(reverse)-1-i]
	}
	return indices, true
}
