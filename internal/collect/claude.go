package collect

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func claude() Collector {
	dirs := under(roots("CLAUDE_CONFIG_DIR", homeDir(".config", "claude"), homeDir(".claude")), "projects")
	return Collector{
		Agent: "claude",
		List:  func() ([]Source, error) { return files("claude", dirs, withSuffix(".jsonl")) },
		Parse: parseClaude,
	}
}

type claudeLine struct {
	Type              string         `json:"type"`
	Timestamp         string         `json:"timestamp"`
	SessionID         string         `json:"sessionId"`
	CWD               string         `json:"cwd"`
	Version           string         `json:"version"`
	Entrypoint        string         `json:"entrypoint"`
	IsSidechain       bool           `json:"isSidechain"`
	RequestID         string         `json:"requestId"`
	RequestRef        string         `json:"requestRef"`
	RequestedModel    string         `json:"requestedModel"`
	Effort            string         `json:"effort"`
	IsAPIErrorMessage bool           `json:"isApiErrorMessage"`
	Error             string         `json:"error"`
	APIErrorStatus    int            `json:"apiErrorStatus"`
	Message           *claudeMessage `json:"message"`
	// Older versions log subagent responses inside the parent transcript as
	// progress lines that wrap the subagent's assistant line.
	Data *struct {
		Message *claudeLine `json:"message"`
	} `json:"data"`
	// api-request lines
	ID          string `json:"id"`
	QuerySource string `json:"querySource"`
	Params      *struct {
		Model string `json:"model"`
	} `json:"params"`
}

type claudeMessage struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      *struct {
		claudeTokens
		OutputTokensDetails *struct {
			ThinkingTokens int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
		ServiceTier string `json:"service_tier"`
		Speed       string `json:"speed"`
		// Advisor calls made while generating the response, billed under
		// their own model on top of the top-level usage.
		Iterations []struct {
			Type  string `json:"type"`
			Model string `json:"model"`
			claudeTokens
		} `json:"iterations"`
	} `json:"usage"`
}

type claudeTokens struct {
	InputTokens              int64 `json:"input_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreation            *struct {
		Ephemeral5m int64 `json:"ephemeral_5m_input_tokens"`
		Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
	} `json:"cache_creation"`
}

func (t claudeTokens) apply(e *event.Event) {
	e.InputTokens = t.InputTokens
	e.CacheReadTokens = t.CacheReadInputTokens
	e.CacheWriteTokens = t.CacheCreationInputTokens
	e.CacheWrite1hTokens = 0
	if breakdown := t.CacheCreation; breakdown != nil {
		e.CacheWriteTokens = max(e.CacheWriteTokens, breakdown.Ephemeral5m+breakdown.Ephemeral1h)
		e.CacheWrite1hTokens = breakdown.Ephemeral1h
	}
	e.OutputTokens = t.OutputTokens
}

type claudeAPIRequest struct {
	at     int64
	source string
	model  string
}

type claudeResponse struct {
	event    event.Event
	ref      string
	end      int64
	advisors []event.Event
}

const claudeSyntheticModel = "<synthetic>"

var (
	claudeAssistantType  = []byte(`"type":"assistant"`)
	claudeAPIRequestType = []byte(`"type":"api-request"`)
)

// parseClaude reads a Claude Code transcript. Each content block of a response
// is its own line sharing the message ID, request ID and usage, so lines are
// merged per response. Responses that reference an api-request line are timed
// from that request to their last content block.
func parseClaude(path string) ([]event.Event, error) {
	requests := map[string]claudeAPIRequest{}
	responses := map[string]*claudeResponse{}
	var order []string
	err := readLines(path, func(raw []byte) {
		if !bytes.Contains(raw, claudeAssistantType) && !bytes.Contains(raw, claudeAPIRequestType) {
			return
		}
		var line claudeLine
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		if line.Type == "api-request" {
			request := claudeAPIRequest{at: parseTime(line.Timestamp), source: line.QuerySource}
			if line.Params != nil {
				request.model = line.Params.Model
			}
			requests[line.ID] = request
			return
		}
		if line.Type == "progress" && line.Data != nil && line.Data.Message != nil {
			inner := line.Data.Message
			inner.SessionID = cmp.Or(inner.SessionID, line.SessionID)
			inner.CWD = cmp.Or(inner.CWD, line.CWD)
			inner.Version = cmp.Or(inner.Version, line.Version)
			inner.Entrypoint = cmp.Or(inner.Entrypoint, line.Entrypoint)
			inner.IsSidechain = true
			line = *inner
		}
		message := line.Message
		if line.Type != "assistant" || message == nil || message.Usage == nil {
			return
		}
		if message.Model == claudeSyntheticModel && !line.IsAPIErrorMessage {
			return // A local placeholder, not a model request.
		}
		// Anthropic message IDs are unique per response, so a response copied
		// into another transcript, or replayed by a /btw side question under a
		// new request ID, keeps one key. Gateways may reuse their own IDs.
		key := message.ID
		if !strings.HasPrefix(key, "msg_") {
			key += ":" + line.RequestID
		}
		at := parseTime(line.Timestamp)
		response := responses[key]
		if response == nil {
			response = &claudeResponse{event: event.Event{
				Agent: "claude", ID: key, Time: at, Model: message.Model, Session: line.SessionID, Project: line.CWD,
				Client: line.Entrypoint, Effort: line.Effort, Version: line.Version, Source: path,
			}}
			if line.IsSidechain {
				response.event.Kind = "sidechain"
			}
			responses[key] = response
			order = append(order, key)
		}
		e := &response.event
		response.ref = cmp.Or(line.RequestRef, response.ref)
		response.end = max(response.end, at)
		// Early blocks may carry partial output counts; keep the largest.
		if usage := message.Usage; usage.OutputTokens >= e.OutputTokens {
			usage.apply(e)
			e.ReasoningTokens = 0
			if usage.OutputTokensDetails != nil {
				e.ReasoningTokens = usage.OutputTokensDetails.ThinkingTokens
			}
			e.Tier = claudeTier(usage.ServiceTier, usage.Speed)
			response.advisors = response.advisors[:0]
			for i, iteration := range usage.Iterations {
				if iteration.Type != "advisor_message" || iteration.Model == "" {
					continue
				}
				advisor := event.Event{
					Agent: "claude", ID: key + ":advisor:" + strconv.Itoa(i), Model: iteration.Model, Session: e.Session,
					Project: e.Project, Client: e.Client, Kind: "advisor", Version: e.Version, Source: path,
				}
				iteration.apply(&advisor)
				response.advisors = append(response.advisors, advisor)
			}
		}
		e.Stop = cmp.Or(message.StopReason, e.Stop)
		if line.IsAPIErrorMessage {
			e.Failed = true
			e.Error = claudeErrorText(line)
		}
		if e.Model == claudeSyntheticModel {
			e.Model = line.RequestedModel
		}
	})
	if err != nil {
		return nil, err
	}
	var events []event.Event
	for _, key := range order {
		response := responses[key]
		e := &response.event
		if request, ok := requests[response.ref]; ok && request.at > 0 && request.at <= response.end {
			e.Time = request.at
			e.LatencyMS = response.end - request.at
			e.Kind = cmp.Or(request.source, e.Kind)
			e.Model = cmp.Or(e.Model, request.model)
		}
		events = append(events, *e)
		for _, advisor := range response.advisors {
			advisor.Time = e.Time
			events = append(events, advisor)
		}
	}
	return events, nil
}

func claudeTier(serviceTier, speed string) string {
	if speed != "" && speed != "standard" {
		return speed
	}
	if serviceTier != "standard" {
		return serviceTier
	}
	return ""
}

func claudeErrorText(line claudeLine) string {
	var texts []string
	if line.APIErrorStatus > 0 {
		texts = append(texts, fmt.Sprintf("HTTP %d %s", line.APIErrorStatus, line.Error))
	} else if line.Error != "" {
		texts = append(texts, line.Error)
	}
	var blocks []struct{ Text string }
	_ = json.Unmarshal(line.Message.Content, &blocks)
	for _, block := range blocks {
		if block.Text != "" {
			texts = append(texts, block.Text)
		}
	}
	return strings.Join(texts, "\n")
}
