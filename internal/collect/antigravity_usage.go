package collect

import (
	"cmp"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protowire"

	"github.com/haowang02/agent-events/internal/event"
)

type antigravityRequest struct {
	event.Event
	keys      []string
	visible   int64
	fallbacks []string
}
type antigravityRequests struct {
	base         event.Event
	fallbackTime int64
	models       antigravityModels
	requests     []antigravityRequest
	identities   map[string]int
}

func (r *antigravityRequests) add(e event.Event, usage []byte, fallbackID string, alternatives ...string) {
	e.InputTokens = pbVarint(usage, 2)
	e.CacheReadTokens = pbVarint(usage, 5)
	e.CacheWriteTokens = pbVarint(usage, 4)
	e.ReasoningTokens = pbVarint(usage, 9)
	visible := max(pbVarint(usage, 10), pbVarint(usage, 3)-e.ReasoningTokens)
	e.OutputTokens = max(pbVarint(usage, 3), visible+e.ReasoningTokens)
	if e.TotalTokens() == 0 && !e.Failed {
		return
	}
	e.Agent, e.Session, e.Project, e.Client, e.Source = r.base.Agent, r.base.Session, r.base.Project, r.base.Client, r.base.Source
	e.Time = cmp.Or(e.Time, r.fallbackTime)
	keys := antigravityUsageKeys(usage)
	if len(keys) == 0 {
		keys = []string{fallbackID}
	}
	e.ID = keys[0]
	incoming := antigravityRequest{Event: e, keys: keys, visible: visible, fallbacks: append([]string{fallbackID}, alternatives...)}
	index := len(r.requests)
	var matched []int
	for _, key := range keys {
		if i, ok := r.identities[key]; ok {
			index = min(index, i)
			if !slices.Contains(matched, i) {
				matched = append(matched, i)
			}
		}
	}

	if index == len(r.requests) {
		r.requests = append(r.requests, incoming)
	} else {
		r.requests[index].merge(incoming)
	}
	// A copy can bridge message and response identities that were seen apart.
	for _, old := range matched {
		if old != index && r.requests[old].ID != "" {
			r.requests[index].merge(r.requests[old])
			r.requests[old].ID = ""
		}
	}
	for _, key := range r.requests[index].keys {
		r.identities[key] = index
	}
	// Only a copy without provider identities uses the step fallback.
	// Distinct identified attempts may share the same response step.
	r.identities[fallbackID] = index
}
func (target *antigravityRequest) merge(other antigravityRequest) {
	target.fallbacks = append(target.fallbacks, other.fallbacks...)
	target.InputTokens = max(target.InputTokens, other.InputTokens)
	target.CacheReadTokens = max(target.CacheReadTokens, other.CacheReadTokens)
	target.CacheWriteTokens = max(target.CacheWriteTokens, other.CacheWriteTokens)
	target.ReasoningTokens = max(target.ReasoningTokens, other.ReasoningTokens)
	target.visible = max(target.visible, other.visible)
	target.OutputTokens = max(target.OutputTokens, other.OutputTokens, target.visible+target.ReasoningTokens)
	if !antigravityNamedModel(target.Model) && antigravityNamedModel(other.Model) {
		target.Model, target.Effort = other.Model, other.Effort
	}
	if target.LatencyMS == 0 {
		target.LatencyMS = other.LatencyMS
	}
	if target.TTFTMS == 0 {
		target.TTFTMS = other.TTFTMS
	}
	for _, key := range other.keys {
		if !slices.Contains(target.keys, key) {
			target.keys = append(target.keys, key)
		}
	}
}

func (r *antigravityRequests) retries(base event.Event, primary, metadata []byte, field protowire.Number, id string) {
	mirrored := false
	ordinal := 0
	for _, attempt := range pbRepeated(metadata, field) {
		usage := pbBytes(attempt, 2)
		if usage == nil {
			continue
		}
		if !mirrored && antigravityUsageMirror(usage, primary) {
			mirrored = true
			continue
		}
		e := base
		e.LatencyMS, e.TTFTMS = 0, 0 // Aggregate generation timing is not a retry's duration.
		e.Error = string(pbBytes(attempt, 3))
		e.Failed = e.Error != ""
		context := antigravityModel{}
		if id := pbVarint(usage, 1); id == 0 || id == pbVarint(primary, 1) {
			context = antigravityModel{name: e.Model, effort: e.Effort}
		}
		m := r.models.resolve(pbVarint(usage, 1), context)
		e.Model, e.Effort = m.name, m.effort
		ordinal++
		r.add(e, usage, fmt.Sprintf("%s:attempt:%d", id, ordinal))
	}
}
func (r *antigravityRequests) result() Result {
	result := Result{Sessions: []string{r.base.Session}}
	owners := map[string]int{}
	active := map[string]bool{}
	for _, request := range r.requests {
		if request.ID == "" {
			continue
		}
		active[request.ID] = true
		unique := map[string]bool{}
		for _, id := range request.fallbacks {
			if id != "" && !unique[id] {
				owners[id]++
				unique[id] = true
			}
		}
	}
	for _, request := range r.requests {
		if request.ID != "" {
			for _, id := range append(slices.Clone(request.keys), request.fallbacks...) {
				if id != "" && !active[id] && (owners[id] == 0 || owners[id] == 1) && !slices.Contains(result.Superseded, RecordRef{Agent: "antigravity", ID: id, Source: r.base.Source}) {
					result.Superseded = append(result.Superseded, RecordRef{Agent: "antigravity", ID: id, Source: r.base.Source})
				}
			}
			result.Events = append(result.Events, request.Event)
		}
	}
	return result
}

func antigravityUsageKeys(usage []byte) []string {
	var keys []string
	for _, field := range []struct {
		number protowire.Number
		prefix string
	}{{7, "message:"}, {11, "response:"}, {12, "provider:"}} {
		if id := string(pbBytes(usage, field.number)); id != "" {
			keys = append(keys, field.prefix+id)
		}
	}
	return keys
}

func antigravityUsageMirror(usage, primary []byte) bool {
	if usage == nil || primary == nil {
		return false
	}
	for _, field := range []protowire.Number{1, 2, 3, 4, 5, 9, 10} {
		if pbVarint(usage, field) != pbVarint(primary, field) {
			return false
		}
	}
	keys := antigravityUsageKeys(primary)
	for _, key := range antigravityUsageKeys(usage) {
		if !slices.Contains(keys, key) {
			return false
		}
	}
	return true
}
