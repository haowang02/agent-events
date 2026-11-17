package collect

import (
	"cmp"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/haowang02/agent-events/internal/event"
)

// antigravity collects every Antigravity edition. A configured root may hold
// a conversations directory or be one.
func antigravity() Collector {
	custom := strings.TrimSpace(os.Getenv("ANTIGRAVITY_DATA_DIR")) != ""
	configured := roots("ANTIGRAVITY_DATA_DIR",
		homeDir(".gemini", "antigravity"), homeDir(".gemini", "antigravity-cli"), homeDir(".gemini", "antigravity-ide"),
		homeDir(".gemini", "antigravity-backup"), homeDir(".gemini", "antigravity-acp"), homeDir(".config", "antigravity"),
	)
	return Collector{
		Agent:   "antigravity",
		Version: 4,
		List: func() ([]Source, error) {
			var dirs []string
			for _, root := range configured {
				if nested := filepath.Join(root, "conversations"); isDir(nested) {
					dirs = append(dirs, nested)
				} else if custom {
					dirs = append(dirs, root)
				}
			}
			sources, err := files("antigravity", dirs, withSuffix(".db"))
			if err != nil {
				return nil, err
			}
			var paths []string
			for _, source := range sources {
				paths = append(paths, source.Path)
			}
			return databases("antigravity", paths...)
		},
		Parse: parseAntigravity,
	}
}

const (
	antigravityGenRequest       = 1  // gen_metadata: the request
	antigravityGenUsage         = 4  // request: ModelUsageStats
	antigravityGenTTFT          = 11 // request: time to first response, google.protobuf.Duration
	antigravityGenStreaming     = 12 // request: time from first response to completion, google.protobuf.Duration
	antigravityGenAttempts      = 17 // request: generation attempts
	antigravityGenModel         = 19 // request: model name
	antigravityGenLabels        = 20 // request: repeated {1: key, 2: value}
	antigravityGenDisplayName   = 21 // request: display name, e.g. "Gemini 4 Hydrogen (Medium)"
	antigravityAttemptError     = 3  // attempt: error message
	antigravityUsageModel       = 1  // usage: model enum
	antigravityUsageInput       = 2  // usage: uncached input
	antigravityUsageOutput      = 3  // usage: thinking + response output
	antigravityUsageMessageID   = 7
	antigravityStepCreated      = 1  // step metadata: google.protobuf.Timestamp
	antigravityStepUsage        = 9  // step metadata: ModelUsageStats
	antigravityStepModel        = 11 // step metadata: model enum
	antigravityStepErrorMessage = 2  // error_details: message
	antigravityStepTypeResponse = 15 // steps.step_type of a model response
	antigravityStepTypeError    = 17 // steps.step_type of an error message
	antigravityWorkspace        = 1  // trajectory metadata: {1: workspace URI}
)

type antigravityConversation struct {
	path, conversation, client, project string
	fallbackTime                        int64
	steps                               []antigravityStep
	generations                         []antigravityGeneration
	models                              antigravityModels
}

func parseAntigravity(path string) (Result, error) {
	c, err := readAntigravity(path)
	if err != nil || c == nil {
		return Result{}, err
	}
	return c.requests(), nil
}

func (c *antigravityConversation) requests() Result {
	requests := antigravityRequests{base: event.Event{Agent: "antigravity", Session: c.conversation, Project: c.project, Client: c.client, Source: c.path}, fallbackTime: c.fallbackTime, models: c.models, identities: map[string]int{}}
	byIndex, byMessage := map[int64]antigravityStep{}, map[string]antigravityStep{}
	for _, step := range c.steps {
		byIndex[step.index] = step
		for _, key := range antigravityUsageKeys(step.usage) {
			byMessage[key] = step
		}
	}
	responded := map[int64]bool{}
	current := antigravityModel{}
	var currentID int64
	for _, g := range c.generations {
		usage := pbBytes(g.request, 4)
		labels := map[string]string{}
		for _, label := range pbRepeated(g.request, 20) {
			labels[string(pbBytes(label, 1))] = string(pbBytes(label, 2))
		}
		id := cmp.Or(pbVarint(usage, 1), pbVarint(g.request, 3))
		if name := string(pbBytes(g.request, 19)); name != "" || len(pbBytes(g.request, 21)) > 0 {
			currentID = cmp.Or(pbVarint(g.request, 3), id)
			current = c.models.remember(currentID, name, string(pbBytes(g.request, 21)))
		}
		context := current
		if id != 0 && currentID != id {
			context = antigravityModel{}
		}
		m := c.models.resolve(id, context)
		var step antigravityStep
		found := false
		for _, key := range antigravityUsageKeys(usage) {
			if matched, ok := byMessage[key]; ok {
				step, found = matched, true
				break
			}
		}
		if !found {
			for _, index := range antigravityStepIndices(g.data) {
				if matched, ok := byIndex[index]; ok && (matched.kind == 15 || matched.kind == 17 || matched.usage != nil) {
					step, found = matched, true
					break
				}
			}
		}
		if !found {
			if last, err := strconv.ParseInt(labels["last_step_index"], 10, 64); err == nil {
				step, found = byIndex[last+1]
			}
		}
		e := event.Event{Model: m.name, Effort: m.effort, Time: pbMillis(pbBytes(pbBytes(g.request, 9), 4))}
		fallbackID := cmp.Or(labels["request_id"], fmt.Sprintf("%s:gen:%d", c.conversation, g.index))
		if found {
			e.Time = cmp.Or(e.Time, step.created)
			e.Failed = step.kind == 17 || step.error != ""
			e.Error = step.error
			responded[step.index] = true
			fallbackID = fmt.Sprintf("%s:step:%d", c.conversation, step.index)
		}
		if e.Failed {
			e.Error = cmp.Or(e.Error, string(pbBytes(pbBytes(g.request, 17), 3)))
		}
		e.TTFTMS = pbMillis(pbBytes(g.request, 11))
		if first, streaming := pbBytes(g.request, 11), pbBytes(g.request, 12); first != nil && streaming != nil {
			e.LatencyMS = pbMillis(first) + pbMillis(streaming)
		}
		requests.add(e, usage, fallbackID, fmt.Sprintf("%s:gen:%d", c.conversation, g.index), labels["request_id"])
		requests.retries(e, usage, g.request, 17, fallbackID)
	}
	for _, step := range c.steps {
		if step.usage == nil && step.kind != antigravityStepTypeResponse && step.kind != antigravityStepTypeError && len(pbRepeated(step.metadata, 28)) == 0 {
			continue
		}
		m := c.models.resolve(cmp.Or(pbVarint(step.usage, 1), step.model), antigravityModel{})
		e := event.Event{Model: m.name, Effort: m.effort, Time: step.created, Failed: step.kind == 17 || step.error != "", Error: step.error}
		// A zero-token error step already represented by a generation adds no call.
		if step.usage != nil || !responded[step.index] && (step.kind == antigravityStepTypeResponse || step.kind == antigravityStepTypeError) {
			requests.add(e, step.usage, fmt.Sprintf("%s:step:%d", c.conversation, step.index))
		}
		requests.retries(e, step.usage, step.metadata, 28, fmt.Sprintf("%s:step:%d", c.conversation, step.index))
	}

	return requests.result()
}
