package collect

import (
	"cmp"
	"encoding/json"
	"fmt"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

func grok() Collector {
	dirs := under(roots("GROK_HOME", homeDir(".grok")), "sessions")
	return Collector{
		Agent: "grok",
		List:  func() ([]Source, error) { return files("grok", dirs, named("updates.jsonl")) },
		Parse: parseGrok,
	}
}

type grokUsage struct {
	InputTokens         int64 `json:"inputTokens"` // Includes cache reads and writes.
	OutputTokens        int64 `json:"outputTokens"`
	CachedReadTokens    int64 `json:"cachedReadTokens"`
	CacheCreationTokens int64 `json:"cacheCreationTokens"`
	ReasoningTokens     int64 `json:"reasoningTokens"` // Included in output.
}

// grokModel drops the CLI's display prefix and -build suffix, which name the
// model the build CLI bills.
func grokModel(model string) string {
	return strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(model), "[grok] "), "-build")
}

// parseGrok reads sessions/<encoded cwd>/<session>/updates.jsonl. Only
// completed turns record usage, totalled per model over the turn's requests.
func parseGrok(path string) ([]event.Event, error) {
	session := filepath.Base(filepath.Dir(path))
	project := filepath.Base(filepath.Dir(filepath.Dir(path)))
	if decoded, err := url.PathUnescape(project); err == nil {
		project = decoded
	}
	var summary struct {
		Info struct {
			ID  string `json:"id"`
			CWD string `json:"cwd"`
		} `json:"info"`
		GitRootDir     string `json:"git_root_dir"`
		CurrentModelID string `json:"current_model_id"`
	}
	_ = readJSON(filepath.Join(filepath.Dir(path), "summary.json"), &summary)
	session = cmp.Or(summary.Info.ID, session)
	project = cmp.Or(summary.Info.CWD, summary.GitRootDir, project)

	var events []event.Event
	err := readLines(path, func(raw []byte) {
		var line struct {
			Timestamp float64 `json:"timestamp"` // Unix seconds.
			Params    struct {
				SessionID string `json:"sessionId"`
				Update    struct {
					SessionUpdate string `json:"sessionUpdate"`
					Usage         *struct {
						grokUsage
						ModelUsage map[string]grokUsage `json:"modelUsage"`
					} `json:"usage"`
				} `json:"update"`
				Meta struct {
					EventID          string `json:"eventId"`
					AgentTimestampMs int64  `json:"agentTimestampMs"`
				} `json:"_meta"`
			} `json:"params"`
		}
		if json.Unmarshal(raw, &line) != nil || line.Params.Update.SessionUpdate != "turn_completed" || line.Params.Update.Usage == nil {
			return
		}
		usage := line.Params.Update.Usage
		models := usage.ModelUsage
		if len(models) == 0 {
			models = map[string]grokUsage{summary.CurrentModelID: usage.grokUsage}
		}
		at := cmp.Or(line.Params.Meta.AgentTimestampMs, unixMillis(line.Timestamp))
		turnSession := cmp.Or(line.Params.SessionID, session)
		for name, usage := range models {
			e := event.Event{
				Agent: "grok", Time: at, Model: grokModel(name), Session: turnSession, Project: project, Scope: event.ScopeTurn,
				OutputTokens: usage.OutputTokens, ReasoningTokens: usage.ReasoningTokens, Source: path,
			}
			e.InputTokens, e.CacheReadTokens, e.CacheWriteTokens = inclusiveInput(usage.InputTokens, usage.CachedReadTokens, usage.CacheCreationTokens)
			if e.TotalTokens() == 0 {
				continue
			}
			e.ID = line.Params.Meta.EventID + "|" + name
			if line.Params.Meta.EventID == "" {
				e.ID = fmt.Sprintf("%s|%d|%s|%d|%d", turnSession, at, name, e.InputTokens, e.OutputTokens)
			}
			events = append(events, e)
		}
	})
	return events, err
}
