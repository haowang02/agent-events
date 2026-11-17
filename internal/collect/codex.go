package collect

import (
	"bufio"
	"bytes"
	"cmp"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"

	"github.com/haowang02/agent-events/internal/event"
)

func codex() Collector {
	homes := roots("CODEX_HOME", homeDir(".codex"))
	var mu sync.RWMutex
	paths := map[string]string{}
	cached := map[string]*codexCached{}
	return Collector{
		Agent: "codex", Version: 4,
		List: func() ([]Source, error) {
			var dirs []string
			for _, home := range homes {
				sessions, archived := filepath.Join(home, "sessions"), filepath.Join(home, "archived_sessions")
				if !isDir(sessions) && !isDir(archived) {
					sessions = home
				}
				dirs = append(dirs, sessions, archived)
			}
			sources, err := files("codex", dirs, isRollout)
			if err != nil {
				return nil, err
			}
			mu.Lock()
			defer mu.Unlock()
			byPath := make(map[string]*codexCached, len(sources))
			bySession := map[string]string{}
			for _, source := range sources {
				entry := cached[source.Path]
				if entry == nil || entry.source != source {
					entry = &codexCached{source: source}
					entry.metadata, _ = codexMetadata(source.Path)
				}
				byPath[source.Path] = entry
				if entry.metadata.ID != "" {
					bySession[entry.metadata.ID] = source.Path
				}
			}
			paths, cached = bySession, byPath
			return sources, nil
		},
		Parse: func(path string) (Result, error) {
			mu.RLock()
			defer mu.RUnlock()
			transcript, _, err := readCodexFamily(path, paths, cached, map[string]bool{})
			if err != nil {
				return Result{}, err
			}
			return transcript.result(), nil
		},
	}
}

// isRollout matches plain and Zstandard-compressed rollouts; Codex compresses
// older ones.
func isRollout(path string) bool {
	name := filepath.Base(path)
	return strings.HasPrefix(name, "rollout-") && (strings.HasSuffix(name, ".jsonl") || strings.HasSuffix(name, ".jsonl.zst"))
}

type codexModelFields struct {
	Model     string          `json:"model"`
	ModelName string          `json:"model_name"`
	Metadata  json.RawMessage `json:"metadata"`
}

func (f codexModelFields) resolvedModel() string {
	var metadata struct {
		Model     string `json:"model"`
		ModelName string `json:"model_name"`
	}
	_ = json.Unmarshal(f.Metadata, &metadata)
	return cmp.Or(f.Model, f.ModelName, metadata.Model, metadata.ModelName)
}

type codexPayload struct {
	Type string `json:"type"`
	// session_meta
	ID            string          `json:"id"`
	ForkedFromID  string          `json:"forked_from_id"`
	CWD           string          `json:"cwd"`
	CLIVersion    string          `json:"cli_version"`
	Originator    string          `json:"originator"`
	ModelProvider string          `json:"model_provider"`
	Source        json.RawMessage `json:"source"`
	// turn_context
	codexModelFields
	CompactionResponseID string `json:"compaction_response_id"`
	Effort               string `json:"effort"`
	CollaborationMode    *struct {
		Settings struct {
			ReasoningEffort string `json:"reasoning_effort"`
		} `json:"settings"`
	} `json:"collaboration_mode"`
	// event_msg
	TurnID             string `json:"turn_id"`
	TimeToFirstTokenMS int64  `json:"time_to_first_token_ms"`
	CompletedAtMS      int64  `json:"completed_at_ms"`
	Message            string `json:"message"`
	ThreadSettings     *struct {
		Model           string  `json:"model"`
		ModelProviderID string  `json:"model_provider_id"`
		ServiceTier     *string `json:"service_tier"` // Absent leaves the tier unchanged.
	} `json:"thread_settings"`
	Info *struct {
		codexModelFields
		TotalTokenUsage *codexUsage `json:"total_token_usage"`
		LastTokenUsage  *codexUsage `json:"last_token_usage"`
	} `json:"info"`
	// token_usage_record
	ResponseID string     `json:"response_id"`
	Usage      codexUsage `json:"usage"`
}

// Parent links identify the history a fork or subagent copied.
func (p *codexPayload) parentSession() string {
	if p.ForkedFromID != "" {
		return p.ForkedFromID
	}
	return p.spawnParent()
}

func (p *codexPayload) spawnParent() string {
	var source struct {
		Subagent struct {
			ThreadSpawn struct {
				Parent string `json:"parent_thread_id"`
			} `json:"thread_spawn"`
		} `json:"subagent"`
	}
	_ = json.Unmarshal(p.Source, &source)
	return source.Subagent.ThreadSpawn.Parent
}

func codexMetadata(path string) (codexPayload, error) {
	var result struct {
		Payload codexPayload `json:"payload"`
	}
	file, err := os.Open(path)
	if err != nil {
		return result.Payload, err
	}
	defer file.Close()
	var reader io.Reader = file
	if strings.HasSuffix(path, ".zst") {
		decoder, err := zstd.NewReader(file)
		if err != nil {
			return result.Payload, err
		}
		defer decoder.Close()
		reader = decoder
	}
	line, err := bufio.NewReader(io.LimitReader(reader, 1<<20)).ReadBytes('\n')
	if err != nil && err != io.EOF {
		return result.Payload, err
	}
	err = json.Unmarshal(line, &result)
	return result.Payload, err
}

// Structured sources describe thread roles; originator identifies the client.
func (p *codexPayload) sessionKind() string {
	var object map[string]json.RawMessage
	if json.Unmarshal(p.Source, &object) == nil {
		for _, key := range []string{"subagent", "exec", "cli", "vscode"} {
			if _, ok := object[key]; ok {
				return key
			}
		}
	}
	switch p.Originator {
	case "codex-tui":
		return "cli"
	case "codex_exec":
		return "exec"
	}
	var source string
	json.Unmarshal(p.Source, &source)
	return source
}

var (
	codexTypePrefix  = []byte(`"type":"`)
	codexUserMessage = []byte(`"type":"UserMessage"`)
)

// codexLineTypes returns the first two "type" values of a rollout line: the
// line type and, for event_msg lines, the payload type. Rollouts write the
// line type before the payload, so large lines are skipped without decoding.
func codexLineTypes(line []byte) (string, string) {
	var types [2]string
	rest := line
	for i := range types {
		start := bytes.Index(rest, codexTypePrefix)
		if start < 0 {
			break
		}
		rest = rest[start+len(codexTypePrefix):]
		end := bytes.IndexByte(rest, '"')
		if end < 0 {
			break
		}
		types[i] = string(rest[:end])
		rest = rest[end:]
	}
	known := slices.Contains([]string{"session_meta", "turn_context", "event_msg", "response_item", "token_usage_record", "compacted"}, types[0])
	if !known || types[0] == "event_msg" && types[1] == "" || bytes.Contains(line, []byte(`"type" `)) || bytes.Contains(line, []byte(`"type": `)) {
		var entry struct {
			Type    string `json:"type"`
			Payload struct {
				Type string `json:"type"`
			} `json:"payload"`
		}
		if json.Unmarshal(line, &entry) == nil {
			return entry.Type, entry.Payload.Type
		}
	}
	return types[0], types[1]
}

func codexWanted(line []byte) bool {
	lineType, payloadType := codexLineTypes(line)
	switch lineType {
	case "session_meta", "turn_context", "token_usage_record", "compacted":
		return true
	case "event_msg":
		switch payloadType {
		case "task_started", "task_complete", "token_count", "thread_settings_applied", "error":
			return true
		case "item_completed":
			return bytes.Contains(line, codexUserMessage) || bytes.Contains(line, []byte(`"UserMessage"`))
		}
	}
	return false
}

// parseCodex reads a Codex rollout. Current versions write a
// token_usage_record for each model response; older ones only report usage in
// token_count events, which count when no record precedes them. A request
// starts at the latest boundary: the user message that started it, or the
// token_count written once the previous response and its tool calls finished.
func parseCodex(path string) (Result, error) {
	t, err := parseCodexTranscript(path)
	return t.result(), err
}

func parseCodexTranscript(path string) (*codexTranscript, error) {
	transcript := &codexTranscript{}
	var (
		events                    []event.Event
		session, project, version string
		client, provider, kind    string
		model, effort, tier       string
		turn                      string
		boundary, lastResponse    int64
		recorded                  *codexUsage
		records                   = map[string]int{}
		compactions               = map[string]bool{}
		previousTotal             *codexUsage
		metadataSeen              bool
		turnStarts                = map[string]int64{}
		turnFirst                 = map[string]int{}
	)
	session = strings.TrimSuffix(strings.TrimSuffix(filepath.Base(path), ".zst"), ".jsonl")
	identities := map[string]int{}
	newEvent := func(id string, at int64) event.Event {
		return event.Event{
			Agent: "codex", ID: id, Time: at, Model: model, Provider: provider, Session: session, Project: project,
			Client: client, Kind: kind, Effort: effort, Tier: tier, Version: version, Source: path,
		}
	}
	addRequest := func(id string, at int64, usage codexUsage, turnID string) {
		e := newEvent(id, at)
		usage.apply(&e)
		if start := max(boundary, lastResponse); start > 0 && start <= at {
			e.Time = start
			e.LatencyMS = at - start
		}
		if _, ok := turnFirst[turnID]; !ok {
			turnFirst[turnID] = len(events)
		}
		events = append(events, e)
		lastResponse = at
	}
	err := readLines(path, func(raw []byte) {
		if !codexWanted(raw) {
			return
		}
		var line struct {
			Timestamp timestamp    `json:"timestamp"`
			Type      string       `json:"type"`
			Payload   codexPayload `json:"payload"`
		}
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		at := int64(line.Timestamp)
		payload := &line.Payload
		switch line.Type {
		case "session_meta":
			if payload.ID != "" && !slices.Contains(transcript.sessions, payload.ID) {
				transcript.sessions = append(transcript.sessions, payload.ID)
			}
			// A fork can contain the parent's copied session_meta after its own
			// header. Only the first header describes the file being collected.
			if metadataSeen {
				return
			}
			metadataSeen = true
			session, project, version = cmp.Or(payload.ID, session), payload.CWD, payload.CLIVersion
			client, provider = payload.Originator, payload.ModelProvider
			kind = payload.sessionKind()
			transcript.metadata, transcript.start = *payload, at
		case "turn_context":
			model, effort = cmp.Or(payload.resolvedModel(), model), payload.Effort
			if effort == "" && payload.CollaborationMode != nil {
				effort = payload.CollaborationMode.Settings.ReasoningEffort
			}
			project = cmp.Or(payload.CWD, project)
		case "compacted":
			if payload.CompactionResponseID == "" {
				return
			}
			compactions[payload.CompactionResponseID] = true
			if i, ok := records[payload.CompactionResponseID]; ok {
				events[i].Kind = "compaction"
				events[i].LatencyMS, events[i].TTFTMS = 0, 0
			}
		case "token_usage_record":
			if payload.ResponseID == "" || payload.Usage.counts() == ([5]int64{}) {
				return
			}
			if _, exists := records[payload.ResponseID]; exists && payload.ResponseID != "" {
				return
			}
			addRequest(cmp.Or(payload.ResponseID, session+":"+strconv.FormatInt(at, 10)), at, payload.Usage, cmp.Or(payload.TurnID, turn))
			e := &events[len(events)-1]
			e.Model = cmp.Or(payload.resolvedModel(), e.Model)
			if compactions[payload.ResponseID] {
				e.Kind = "compaction"
				e.LatencyMS, e.TTFTMS = 0, 0
			}
			records[payload.ResponseID] = len(events) - 1
			transcript.usage = append(transcript.usage, codexReplayUsage{index: len(events) - 1, at: at, record: true})
			copy := payload.Usage
			recorded = &copy
		case "event_msg":
			switch payload.Type {
			case "task_started":
				boundary, turn = at, payload.TurnID
				turnStarts[turn] = at
			case "item_completed":
				boundary = max(boundary, at, payload.CompletedAtMS)
			case "token_count":
				if info := payload.Info; info != nil {
					total, usage := info.TotalTokenUsage, info.LastTokenUsage
					if usage == nil && total != nil {
						delta := total.minus(previousTotal)
						usage = &delta
					}
					advanced := total == nil || previousTotal == nil || total.counts() != previousTotal.counts()
					if usage != nil && usage.counts() != ([5]int64{}) {
						transcript.countTimes = append(transcript.countTimes, at)
					}
					duplicate := recorded != nil && usage != nil && usage.mirrors(*recorded)
					if duplicate && total != nil && len(transcript.usage) > 0 {
						transcript.usage[len(transcript.usage)-1].total = total
					}
					if !duplicate && advanced && usage != nil && usage.counts() != ([5]int64{}) {
						model = cmp.Or(payload.resolvedModel(), info.resolvedModel(), model)
						key := codexUsageID(session, at, total, usage)
						identities[key]++
						addRequest(key+":"+strconv.Itoa(identities[key]), at, *usage, turn)
						transcript.usage = append(transcript.usage, codexReplayUsage{index: len(events) - 1, at: at, totalOnly: info.LastTokenUsage == nil, total: total})
					}
					previousTotal = cmp.Or(total, previousTotal)
				}
				recorded = nil
				boundary = at
			case "thread_settings_applied":
				if settings := payload.ThreadSettings; settings != nil {
					model = cmp.Or(settings.Model, model)
					provider = cmp.Or(settings.ModelProviderID, provider)
					if settings.ServiceTier != nil {
						tier = codexTier(*settings.ServiceTier)
					}
				}
			case "task_complete":
				// Codex measures time to first token from the start of the turn;
				// rebase it onto the start of the turn's first request.
				index, ok := turnFirst[payload.TurnID]
				if started := turnStarts[payload.TurnID]; ok && started > 0 && payload.TimeToFirstTokenMS > 0 {
					e := &events[index]
					if ttft := started + payload.TimeToFirstTokenMS - e.Time; ttft > 0 && ttft <= e.LatencyMS {
						e.TTFTMS = ttft
					}
				}
			case "error":
				e := newEvent(session+":error:"+strconv.FormatInt(at, 10), at)
				e.Failed, e.Error = true, payload.Message
				events = append(events, e)
			}
		}
	})
	transcript.events = events
	return transcript, err
}

// Cumulative counts identify repeats within one session. Verified replayed
// prefixes inherit parent identities separately; new sibling calls stay unique.
func codexUsageID(session string, at int64, total, last *codexUsage) string {
	if total == nil {
		return session + ":" + strconv.FormatInt(at, 10)
	}
	return "usage:" + session + ":" + total.String() + ":" + last.String()
}

func codexTier(serviceTier string) string {
	switch serviceTier {
	case "default", "standard":
		return ""
	case "fast":
		return "priority"
	}
	return serviceTier
}
