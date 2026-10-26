package main

import (
	"encoding/json"
	"fmt"
)

type openCodeMessage struct {
	Time struct {
		Created   int64 `json:"created"`
		Completed int64 `json:"completed"`
	} `json:"time"`
	Agent string `json:"agent"`
	Model struct {
		ID         string `json:"id"`
		ProviderID string `json:"providerID"`
		Variant    string `json:"variant"`
	} `json:"model"`
	Finish string `json:"finish"`
	Tokens *struct {
		Input     int64 `json:"input"`  // Excludes cached input.
		Output    int64 `json:"output"` // Excludes reasoning.
		Reasoning int64 `json:"reasoning"`
		Cache     struct {
			Read  int64 `json:"read"`
			Write int64 `json:"write"`
		} `json:"cache"`
	} `json:"tokens"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"error"`
}

// parseOpenCodeDatabase reads assistant messages; each one is a model request.
func parseOpenCodeDatabase(path string) ([]Event, error) {
	db, err := openReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`
		SELECT m.id, m.session_id, m.data, COALESCE(s.directory, ''), COALESCE(s.parent_id, ''), COALESCE(s.version, '')
		FROM session_message m LEFT JOIN session_v2 s ON s.id = m.session_id
		WHERE m.type = 'assistant'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var events []Event
	for rows.Next() {
		var id, session, data, directory, parent, version string
		if err := rows.Scan(&id, &session, &data, &directory, &parent, &version); err != nil {
			return nil, err
		}
		var message openCodeMessage
		if json.Unmarshal([]byte(data), &message) != nil || (message.Tokens == nil && message.Error == nil) {
			continue
		}
		event := Event{
			Agent: "opencode", ID: id, Time: message.Time.Created, Model: message.Model.ID, Provider: message.Model.ProviderID,
			Session: session, Project: directory, Client: message.Agent, Kind: "main", Effort: message.Model.Variant,
			Stop: message.Finish, Version: version, Source: path,
		}
		if parent != "" {
			event.Kind = "subagent"
		}
		if message.Time.Completed >= message.Time.Created {
			event.LatencyMS = message.Time.Completed - message.Time.Created
		}
		if tokens := message.Tokens; tokens != nil {
			event.InputTokens = tokens.Input
			event.CacheReadTokens = tokens.Cache.Read
			event.CacheWriteTokens = tokens.Cache.Write
			event.OutputTokens = tokens.Output + tokens.Reasoning
			event.ReasoningTokens = tokens.Reasoning
		}
		if failure := message.Error; failure != nil {
			event.Failed = true
			event.Error = failure.Message
			if failure.Status > 0 {
				event.Error = fmt.Sprintf("HTTP %d %s\n%s", failure.Status, failure.Type, failure.Message)
			}
		}
		events = append(events, event)
	}
	return events, rows.Err()
}
