package agentapp

import (
	"strings"
	"text/template"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// RenderPrompt renders a saved profile template with values generated from the
// run's ContextInput. It fails (never half-renders) for unknown variables,
// syntax errors or non-variable template actions; the runtime caller then
// falls back to the built-in default body.
func RenderPrompt(profileKey, tmpl string, in ContextInput) (string, error) {
	if err := agent.ValidatePromptTemplate(tmpl, PromptVariables(profileKey)); err != nil {
		return "", err
	}
	return renderPromptTemplate(profileKey, tmpl, promptValues(profileKey, in))
}

// RenderPromptSample renders an admin preview with catalog example values and
// the optional sample overrides. It validates the template against the same
// variable catalog as a save; nothing is persisted.
func RenderPromptSample(profileKey, tmpl string, sample map[string]string) (string, error) {
	if err := agent.ValidatePromptTemplate(tmpl, PromptVariables(profileKey)); err != nil {
		return "", err
	}
	values := PromptSampleValues(profileKey)
	for name, value := range sample {
		values[name] = value
	}
	return renderPromptTemplate(profileKey, tmpl, values)
}

func renderPromptTemplate(profileKey, tmpl string, values map[string]string) (string, error) {
	parsed, err := template.New("system_prompt").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return "", platform.Invalid("system_prompt", "invalid template syntax: "+err.Error())
	}
	var rendered strings.Builder
	if err := parsed.Execute(&rendered, values); err != nil {
		return "", platform.Invalid("system_prompt", "invalid template: "+err.Error())
	}
	return rendered.String(), nil
}

// renderedSystemPrompt renders the current profile body for the system section.
// A rendering failure falls back to the built-in seed default; if that also
// fails the raw default body is used. The run never fails because of a bad
// prompt template (spec §8.7).
func renderedSystemPrompt(in ContextInput) string {
	original := strings.TrimSpace(in.Profile.SystemPrompt)
	if original == "" {
		return ""
	}
	if rendered, err := RenderPrompt(in.Profile.Key, original, in); err == nil {
		if trimmed := strings.TrimSpace(rendered); trimmed != "" {
			return trimmed
		}
	}

	defaultPrompt, ok := DefaultPrompt(in.Profile.Key)
	if !ok {
		// The profile key is not a seeded profile (only possible in tests or a
		// future profile); keep the configured body instead of dropping it.
		return original
	}
	if rendered, err := RenderPrompt(in.Profile.Key, defaultPrompt, in); err == nil {
		if trimmed := strings.TrimSpace(rendered); trimmed != "" {
			return trimmed
		}
	}
	return strings.TrimSpace(defaultPrompt)
}

// promptValues maps the run context onto the frozen variable catalog. Fields
// unavailable for a run render as empty strings, never as an error.
func promptValues(profileKey string, in ContextInput) map[string]string {
	key := in.Profile.Key
	if key == "" {
		key = profileKey
	}
	values := map[string]string{
		"Now":           time.Now().UTC().Format(time.RFC3339),
		"ProfileKey":    key,
		"ProfileName":   in.Profile.Name,
		"VersionID":     idOrEmpty(in.VersionPack.VersionID),
		"VersionTitle":  in.VersionPack.Title,
		"VersionStatus": in.VersionPack.Status,
		"TaskType":      in.Task.Type,
		"TaskGoal":      in.Task.Goal,
		"CaseRunID":     "",
		"WaveID":        "",
		"IntentID":      "",
		"CaseVersionID": "",
		"CaseTitle":     "",
		"Priority":      "",
	}
	if in.Execution != nil {
		values["CaseRunID"] = idOrEmpty(in.Execution.CaseRunID)
		values["WaveID"] = idOrEmpty(in.Execution.WaveID)
		values["IntentID"] = idOrEmpty(in.Execution.IntentID)
		values["CaseVersionID"] = idOrEmpty(in.Execution.CaseVersionID)
		values["CaseTitle"] = in.Execution.CaseTitle
		values["Priority"] = in.Execution.Priority
	}
	return values
}

func idOrEmpty(value int64) string {
	if value <= 0 {
		return ""
	}
	return idText(value)
}
