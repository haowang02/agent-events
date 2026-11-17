package collect

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
)

func antigravityNamedModel(name string) bool {
	return name != "" && !strings.HasPrefix(strings.ToLower(name), "model_placeholder_") && !strings.HasPrefix(name, "antigravity-model-")
}

func antigravityDisplayModel(display string) string {
	if i := strings.IndexByte(display, '('); i >= 0 {
		display = display[:i]
	}
	model := strings.Join(strings.Fields(strings.ToLower(display)), "-")
	if strings.HasPrefix(model, "claude-") {
		model = strings.ReplaceAll(model, ".", "-")
	}
	return model
}

// These usage enum aliases are also recognized by ccusage. Usage identifies
// the model that ran, which can differ from the requested model. Unknown enums
// still use names observed in the database rather than a guessed mapping.
func antigravityModelName(id int64) string {
	switch id {
	case 246:
		return "gemini-2.5-pro"
	case 312:
		return "gemini-2.5-flash"
	case 313, 329:
		return "gemini-2.5-flash-thinking"
	case 330:
		return "gemini-2.5-flash-lite"
	case 281, 282:
		return "claude-sonnet-4"
	case 290, 291:
		return "claude-opus-4"
	case 333, 334:
		return "claude-sonnet-4-5"
	case 340, 341:
		return "claude-haiku-4-5"
	case 342:
		return "gpt-oss-120b-medium"
	case 1026:
		return "claude-opus-4-6"
	case 1035:
		return "claude-sonnet-4-6"
	case 1036, 1037, 1016:
		return "gemini-3.1-pro"
	case 1018, 1084, 1047:
		return "gemini-3-flash-preview"
	case 1132, 1133:
		return "gemini-3.5-flash-high"
	case 1187:
		return "gemini-3.5-flash-extra-low"
	case 1020:
		return "gemini-3.5-flash-medium"
	case 1050:
		return "gemini-3.1-flash-lite"
	case 1071, 1072, 1073:
		return "gemini-3.6-flash-" + []string{"high", "medium", "low"}[id-1071]
	case 1298, 1299, 1300:
		return "gemini-3.7-flash-" + []string{"high", "medium", "low"}[id-1298]
	case 1318, 1319, 1320:
		return "gemini-3.8-flash-" + []string{"high", "medium", "low"}[id-1318]
	case 1402:
		return "claude-opus-5-5"
	case 1417, 1418, 1419:
		return "gemini-4-argon"
	}
	return ""
}

func antigravityModelAlias(name string) string {
	if raw, ok := strings.CutPrefix(strings.ToLower(name), "model_placeholder_m"); ok {
		if id, err := strconv.ParseInt(raw, 10, 64); err == nil {
			if model := antigravityModelName(id + 1000); model != "" {
				return model
			}
			return fmt.Sprintf("antigravity-model-%d", id+1000)
		}
	}
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "gemini-pro-default", "gemini-pro-agent":
		return "gemini-3.1-pro"
	case "gemini-3-flash-agent", "gemini-3-flash-agent-a", "gemini-3-flash-agent-b", "gemini-3-flash-a", "gemini-3-flash-b":
		return "gemini-3.5-flash-high"
	case "gemini-3-flash-c", "gemini-3-flash":
		return "gemini-3-flash-preview"
	case "model_openai_gpt_oss_120b_medium":
		return "gpt-oss-120b-medium"
	}
	if lower := strings.ToLower(name); strings.HasPrefix(lower, "gemini ") || strings.HasPrefix(lower, "claude ") {
		return antigravityDisplayModel(name)
	}
	return name
}

type antigravityModel struct {
	name   string
	effort string
}

func antigravityEffort(displayName, model string) string {
	if strings.HasSuffix(displayName, ")") {
		if open := strings.LastIndexByte(displayName, '('); open >= 0 {
			if effort := normalizeAntigravityEffort(displayName[open+1 : len(displayName)-1]); effort != "" {
				return effort
			}
		}
	}
	if last := strings.LastIndexByte(model, '-'); last >= 0 {
		return normalizeAntigravityEffort(model[last+1:])
	}
	return ""
}

func normalizeAntigravityEffort(raw string) string {
	effort := strings.ToLower(strings.TrimSpace(raw))
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh", "max":
		return effort
	default:
		return ""
	}
}

type antigravityModels map[int64]antigravityModel

func (models antigravityModels) remember(id int64, name, display string) antigravityModel {
	m := antigravityModel{name: antigravityModelAlias(name), effort: antigravityEffort(cmp.Or(display, name), name)}
	if !antigravityNamedModel(m.name) {
		m.name = cmp.Or(antigravityModelName(id), antigravityDisplayModel(display), m.name)
	}
	if m.effort == "" {
		m.effort = antigravityEffort("", m.name)
	}
	if id != 0 && antigravityNamedModel(m.name) {
		models[id] = m
	}
	return m
}
func (models antigravityModels) resolve(id int64, m antigravityModel) antigravityModel {
	if canonical := antigravityModelName(id); canonical != "" {
		effort := antigravityEffort("", canonical)
		base := m.name
		if suffix := antigravityEffort("", base); suffix != "" {
			base = strings.TrimSuffix(base, "-"+suffix)
		}
		if effort == "" && (m.name == canonical || base == canonical) {
			effort = m.effort
		}
		return antigravityModel{name: canonical, effort: effort}
	}
	if antigravityNamedModel(m.name) {
		return m
	}
	if known, ok := models[id]; id != 0 && ok {
		return known
	}
	if id != 0 {
		m.name = cmp.Or(antigravityModelName(id), fmt.Sprintf("antigravity-model-%d", id))
		m.effort = antigravityEffort("", m.name)
	}
	return m
}
