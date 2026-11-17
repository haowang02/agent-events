package collect

import (
	"cmp"
	"encoding/json"
	"path/filepath"
	"slices"
	"strconv"

	"github.com/haowang02/agent-events/internal/event"
)

type copilotSnapshot struct {
	event.Event
	usage    copilotUsage
	requests int64
}

type copilotState struct {
	records   []copilotCall
	snapshots []copilotSnapshot
	retired   []event.Event
}

// Recent clients write one usage_record per call. Shutdowns following those
// records (or carrying their accounting watermarks) repeat the same usage.
func readCopilotState(path string) (copilotState, error) {
	session := filepath.Base(filepath.Dir(path))
	var state copilotState
	covered := false
	lineNumber := 0
	err := readLines(path, func(raw []byte) {
		lineNumber++
		var line struct {
			Type      string    `json:"type"`
			ID        string    `json:"id"`
			Timestamp timestamp `json:"timestamp"`
			Data      struct {
				ModelMetrics map[string]struct {
					Usage    *copilotUsage `json:"usage"`
					Requests struct {
						Count int64 `json:"count"`
					} `json:"requests"`
				} `json:"modelMetrics"`
				Watermarks map[string]int64 `json:"usageAccountingWatermarks"`
				Usage      *struct {
					copilotUsage
					Model      string `json:"model"`
					API        string `json:"apiCallId"`
					Service    string `json:"serviceRequestId"`
					Accounting struct {
						Source   string `json:"sourceSessionId"`
						Sequence int64  `json:"sequence"`
					} `json:"accounting"`
				} `json:"usage"`
			} `json:"data"`
		}
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		if line.Type == "session.usage_record" && line.Data.Usage != nil {
			u := line.Data.Usage
			if u.Model == "" {
				return
			}
			covered = true
			id := "usage-record:" + session + ":" + cmp.Or(line.ID, strconv.Itoa(lineNumber))
			if a := u.Accounting; a.Source != "" && a.Sequence > 0 {
				id = "usage-record:" + a.Source + ":" + strconv.FormatInt(a.Sequence, 10)
			}
			e := event.Event{Agent: "copilot", ID: id, Time: int64(line.Timestamp), Model: copilotModel(u.Model), Session: session, Source: path, OutputTokens: u.OutputTokens, ReasoningTokens: u.ReasoningTokens}
			e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(u.InputTokens, u.CacheReadTokens, u.CacheWriteTokens)
			if e.TotalTokens() > 0 {
				state.records = append(state.records, copilotCall{Event: e, response: u.API, service: u.Service, accounting: u.Accounting.Source})
			}
			return
		}
		for _, sequence := range line.Data.Watermarks {
			covered = covered || sequence > 0
		}
		if line.Type != "session.shutdown" {
			return
		}
		for name, metrics := range line.Data.ModelMetrics {
			if metrics.Usage == nil {
				continue
			}
			model := copilotModel(name)
			e := event.Event{Agent: "copilot", ID: session + ":" + cmp.Or(line.ID, strconv.FormatInt(int64(line.Timestamp), 10)) + ":" + model, Time: int64(line.Timestamp), Model: model, Session: session, Scope: event.ScopeSession, Source: path}
			if covered {
				state.retired = append(state.retired, e)
				continue
			}
			state.snapshots = append(state.snapshots, copilotSnapshot{e, *metrics.Usage, metrics.Requests.Count})
		}
	})
	return state, err
}

func reconcileCopilot(calls []copilotCall, states []copilotState) Result {
	var candidates []event.Event
	for _, call := range calls {
		candidates = append(candidates, call.Event)
	}
	for _, state := range states {
		for _, record := range state.records {
			candidates = append(candidates, record.Event)
		}
		for _, snapshot := range state.snapshots {
			candidates = append(candidates, snapshot.Event)
		}
		candidates = append(candidates, state.retired...)
	}
	bestByTrace, bestByResponse := map[string]int{}, map[string]int{}
	for _, call := range calls {
		if best, ok := bestByTrace[call.trace]; !ok || call.kind < best {
			bestByTrace[call.trace] = call.kind
		}
		if best, ok := bestByResponse[call.response]; !ok || call.kind < best {
			bestByResponse[call.response] = call.kind
		}
	}
	var unique []copilotCall
	callIndices := map[string]int{}
	for _, call := range calls {
		if call.trace != "" && call.kind > bestByTrace[call.trace] || call.response != "" && call.kind > bestByResponse[call.response] {
			continue
		}
		key := copilotCallKey(call)
		if i, ok := callIndices[key]; ok {
			if call.Time < unique[i].Time {
				unique[i] = call
			}
			continue
		}
		callIndices[key] = len(unique)
		unique = append(unique, call)
	}
	calls = unique
	var records []copilotCall
	recordIndices := map[string]int{}
	groups := map[string][]copilotSnapshot{}
	seen := map[string]bool{}
	for _, state := range states {
		for _, record := range state.records {
			if i, ok := recordIndices[record.ID]; ok {
				if record.accounting != "" && record.Session == record.accounting {
					records[i] = record
				}
				continue
			}
			recordIndices[record.ID] = len(records)
			records = append(records, record)
		}
		for _, snapshot := range state.snapshots {
			if !seen[snapshot.ID] {
				seen[snapshot.ID] = true
				key := snapshot.Session + "\x00" + snapshot.Model
				groups[key] = append(groups[key], snapshot)
			}
		}
	}
	// A usage record replaces one matching OTel call, preferring service IDs;
	// response IDs can be shared by several calls in a turn.
	matched := map[int]bool{}
	slices.SortFunc(records, func(a, b copilotCall) int { return cmp.Compare(a.Time, b.Time) })
	for _, service := range []bool{true, false} {
		for r := range records {
			if matched[-r-1] {
				continue
			}
			best := -1
			var distance int64
			for i, call := range calls {
				if matched[i] || call.Model != records[r].Model {
					continue
				}
				if service {
					if records[r].service == "" || records[r].service != call.service {
						continue
					}
				} else {
					if records[r].response == "" || records[r].response != call.response {
						continue
					}
				}
				delta := call.Time - records[r].Time
				if delta < 0 {
					delta = -delta
				}
				if best < 0 || delta < distance {
					best, distance = i, delta
				}
			}
			if best >= 0 {
				matched[best], matched[-r-1] = true, true
			}
		}
	}
	after := map[string]int64{}
	var events []event.Event
	for key, snapshots := range groups {
		slices.SortFunc(snapshots, func(a, b copilotSnapshot) int { return cmp.Compare(a.Time, b.Time) })
		var times []int64
		for _, call := range calls {
			if call.Session+"\x00"+call.Model == key {
				times = append(times, call.Time)
			}
		}
		slices.Sort(times)
		start, counted, otelCount, reportedCount, previousCount := -1, 0, 0, int64(0), int64(0)
		comparable := true
		for i, snapshot := range snapshots {
			if snapshot.requests == 0 {
				comparable = false
			}
			through, _ := slices.BinarySearch(times, snapshot.Time+1)
			if start < 0 && through > counted {
				start = i
			}
			if start >= 0 {
				otelCount += through - counted
				reportedCount += max(0, snapshot.requests-previousCount)
			}
			counted, previousCount = through, snapshot.requests
		}
		if !comparable || int64(otelCount) <= reportedCount {
			start = len(snapshots)
		}
		if start > 0 {
			after[key] = snapshots[start-1].Time
		}
		var previous copilotUsage
		for i, snapshot := range snapshots {
			usage := snapshot.usage
			before := previous
			previous = usage
			if i >= start {
				continue
			}
			if usage.InputTokens >= before.InputTokens && usage.OutputTokens >= before.OutputTokens && usage.CacheReadTokens >= before.CacheReadTokens && usage.CacheWriteTokens >= before.CacheWriteTokens {
				usage = copilotUsage{InputTokens: usage.InputTokens - before.InputTokens, OutputTokens: usage.OutputTokens - before.OutputTokens, CacheReadTokens: usage.CacheReadTokens - before.CacheReadTokens, CacheWriteTokens: usage.CacheWriteTokens - before.CacheWriteTokens, ReasoningTokens: max(0, usage.ReasoningTokens-before.ReasoningTokens)}
			}
			e := snapshot.Event
			e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(usage.InputTokens, usage.CacheReadTokens, usage.CacheWriteTokens)
			e.OutputTokens, e.ReasoningTokens = usage.OutputTokens, usage.ReasoningTokens
			if e.TotalTokens() > 0 {
				events = append(events, e)
			}
		}
	}
	for i, call := range calls {
		if !matched[i] && call.Time > after[call.Session+"\x00"+call.Model] {
			events = append(events, call.Event)
		}
	}
	for _, record := range records {
		events = append(events, record.Event)
	}
	result := Result{Events: events}
	active := map[string]bool{}
	for _, e := range events {
		active[e.ID] = true
	}
	for _, e := range candidates {
		if !active[e.ID] {
			result.Superseded = append(result.Superseded, RecordRef{Agent: e.Agent, ID: e.ID, Source: e.Source})
		}
	}
	return result
}
