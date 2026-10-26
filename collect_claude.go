package main

import (
	"bytes"
	"cmp"
	"encoding/json"
	"fmt"
	"strings"
)

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
	// api-request lines
	ID          string                  `json:"id"`
	QuerySource string                  `json:"querySource"`
	Params      *struct{ Model string } `json:"params"`
}

type claudeMessage struct {
	ID         string          `json:"id"`
	Model      string          `json:"model"`
	StopReason string          `json:"stop_reason"`
	Content    json.RawMessage `json:"content"`
	Usage      *struct {
		InputTokens              int64 `json:"input_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		OutputTokensDetails      *struct {
			ThinkingTokens int64 `json:"thinking_tokens"`
		} `json:"output_tokens_details"`
		ServiceTier   string `json:"service_tier"`
		Speed         string `json:"speed"`
		CacheCreation *struct {
			Ephemeral1h int64 `json:"ephemeral_1h_input_tokens"`
		} `json:"cache_creation"`
	} `json:"usage"`
}

type claudeAPIRequest struct {
	at     int64
	source string
	model  string
}

const claudeSyntheticModel = "<synthetic>"

var (
	claudeAssistantType  = []byte(`"type":"assistant"`)
	claudeAPIRequestType = []byte(`"type":"api-request"`)
)

// parseClaudeFile reads a Claude Code transcript. Each content block of a
// response is its own line sharing message.id, requestId and usage, so lines
// are merged per response. Responses that reference an api-request entry get
// a latency from that request to their last content block.
func parseClaudeFile(path string) ([]Event, error) {
	requests := map[string]claudeAPIRequest{}
	responses := map[string]*Event{}
	refs := map[string]string{}
	ends := map[string]int64{}
	var order []string
	err := readLines(path, func(raw []byte) {
		if !bytes.Contains(raw, claudeAssistantType) && !bytes.Contains(raw, claudeAPIRequestType) {
			return
		}
		var line claudeLine
		if json.Unmarshal(raw, &line) != nil {
			return
		}
		at := parseTimestamp(line.Timestamp)
		if line.Type == "api-request" {
			request := claudeAPIRequest{at: at, source: line.QuerySource}
			if line.Params != nil {
				request.model = line.Params.Model
			}
			requests[line.ID] = request
			return
		}
		message := line.Message
		if line.Type != "assistant" || message == nil || message.Usage == nil {
			return
		}
		if message.Model == claudeSyntheticModel && !line.IsAPIErrorMessage {
			return // A local placeholder, not a model request.
		}
		key := message.ID + ":" + line.RequestID
		event := responses[key]
		if event == nil {
			event = &Event{
				Agent: "claude", ID: key, Time: at, Model: message.Model, Session: line.SessionID, Project: line.CWD,
				Client: line.Entrypoint, Effort: line.Effort, Version: line.Version, Source: path,
			}
			if line.IsSidechain {
				event.Kind = "sidechain"
			}
			responses[key] = event
			order = append(order, key)
		}
		if line.RequestRef != "" {
			refs[key] = line.RequestRef
		}
		ends[key] = max(ends[key], at)
		// Early blocks may carry partial output counts; keep the largest.
		if usage := message.Usage; usage.OutputTokens >= event.OutputTokens {
			event.InputTokens = usage.InputTokens
			event.CacheReadTokens = usage.CacheReadInputTokens
			event.CacheWriteTokens = usage.CacheCreationInputTokens
			event.CacheWrite1hTokens = 0
			if usage.CacheCreation != nil {
				event.CacheWrite1hTokens = min(usage.CacheCreation.Ephemeral1h, usage.CacheCreationInputTokens)
			}
			event.OutputTokens = usage.OutputTokens
			event.ReasoningTokens = 0
			if usage.OutputTokensDetails != nil {
				event.ReasoningTokens = usage.OutputTokensDetails.ThinkingTokens
			}
			event.Tier = claudeTier(usage.ServiceTier, usage.Speed)
		}
		if message.StopReason != "" {
			event.Stop = message.StopReason
		}
		if line.IsAPIErrorMessage {
			event.Failed = true
			event.Error = claudeErrorText(line)
		}
		if event.Model == claudeSyntheticModel {
			event.Model = line.RequestedModel
		}
	})
	if err != nil {
		return nil, err
	}
	events := make([]Event, 0, len(order))
	for _, key := range order {
		event := responses[key]
		if request, ok := requests[refs[key]]; ok && request.at > 0 && request.at <= ends[key] {
			event.Time = request.at
			event.LatencyMS = ends[key] - request.at
			event.Kind = cmp.Or(request.source, event.Kind)
			event.Model = cmp.Or(event.Model, request.model)
		}
		events = append(events, *event)
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
