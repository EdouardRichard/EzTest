package agentapp_test

import (
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func promptRenderInput() agentapp.ContextInput {
	in := sampleInput()
	in.Profile.SystemPrompt = "[profile:clarification_host]\n版本 {{.VersionTitle}}（{{.VersionStatus}}），profile {{.ProfileKey}}/{{.ProfileName}}，version {{.VersionID}}。"
	return in
}

func TestPromptVariablesCatalog(t *testing.T) {
	common := agentapp.PromptVariables(agentapp.ProfileKeyClarificationHost)
	names := variableNames(common)
	for _, want := range []string{
		"Now", "ProfileKey", "ProfileName", "VersionID", "VersionTitle",
		"VersionStatus", "TaskType", "TaskGoal",
	} {
		if !names[want] {
			t.Fatalf("common variable %q missing: %v", want, names)
		}
	}
	for _, variable := range common {
		if variable.Source != "runtime" {
			t.Fatalf("variable %q source = %q", variable.Name, variable.Source)
		}
		if variable.Description == "" || variable.Example == "" {
			t.Fatalf("variable %q has no description/example", variable.Name)
		}
	}
	for _, execution := range []string{
		agentapp.ProfileKeyCaseExecutor,
		agentapp.ProfileKeyFailureAnalyst,
		agentapp.ProfileKeyCoverageAnalyst,
	} {
		executionNames := variableNames(agentapp.PromptVariables(execution))
		for _, want := range []string{
			"CaseRunID", "WaveID", "IntentID", "CaseVersionID", "CaseTitle", "Priority",
		} {
			if !executionNames[want] {
				t.Fatalf("execution profile %q variable %q missing", execution, want)
			}
		}
	}
	for _, nonExecution := range []string{
		agentapp.ProfileKeyClarificationHost,
		agentapp.ProfileKeyDesignHost,
		agentapp.ProfileKeyReportWriter,
	} {
		if variableNames(agentapp.PromptVariables(nonExecution))["CaseRunID"] {
			t.Fatalf("non-execution profile %q exposes CaseRunID", nonExecution)
		}
	}
}

func TestRenderPromptSubstitutesStableContextValues(t *testing.T) {
	in := promptRenderInput()
	in.VersionPack.VersionID = 10
	in.VersionPack.Title = "订单系统 Q4"
	in.VersionPack.Status = "running"

	rendered, err := agentapp.RenderPrompt(agentapp.ProfileKeyClarificationHost, in.Profile.SystemPrompt, in)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	for _, want := range []string{
		"版本 订单系统 Q4（running）",
		"profile clarification_host/澄清 Host",
		"version 10",
		"[profile:clarification_host]",
	} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered misses %q: %q", want, rendered)
		}
	}
}

func TestRenderPromptRejectsUnknownVariableAndSyntax(t *testing.T) {
	in := promptRenderInput()
	for _, template := range []string{
		"[profile:clarification_host] {{.Bogus}}",
		"[profile:clarification_host] {{.Now",
	} {
		_, err := agentapp.RenderPrompt(in.Profile.Key, template, in)
		if err == nil {
			t.Fatalf("template %q accepted", template)
		}
		apiErr := platform.AsError(err)
		if apiErr.Status != 400 || apiErr.Details["field"] != "system_prompt" {
			t.Fatalf("err = %#v", apiErr)
		}
	}
}

// TestSystemPromptRejectsRunVariables pins the Task 81 policy: the profile
// system prompt feeds the cache-stable provider prefix, so per-run and
// per-work-item variables are rejected with a typed, actionable error at the
// admin save/validation and render paths instead of silently invalidating the
// prefix on every run.
func TestSystemPromptRejectsRunVariables(t *testing.T) {
	in := promptRenderInput()
	cases := []struct {
		name       string
		profileKey string
		template   string
		variable   string
	}{
		{name: "now", profileKey: agentapp.ProfileKeyClarificationHost, template: "[profile:clarification_host] 现在 {{.Now}}", variable: "Now"},
		{name: "task_type", profileKey: agentapp.ProfileKeyClarificationHost, template: "[profile:clarification_host] {{.TaskType}}", variable: "TaskType"},
		{name: "task_goal", profileKey: agentapp.ProfileKeyClarificationHost, template: "[profile:clarification_host] 目标 {{.TaskGoal}}", variable: "TaskGoal"},
		{name: "case_run_id", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.CaseRunID}}", variable: "CaseRunID"},
		{name: "wave_id", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.WaveID}}", variable: "WaveID"},
		{name: "intent_id", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.IntentID}}", variable: "IntentID"},
		{name: "case_version_id", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.CaseVersionID}}", variable: "CaseVersionID"},
		{name: "case_title", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.CaseTitle}}", variable: "CaseTitle"},
		{name: "priority", profileKey: agentapp.ProfileKeyCaseExecutor, template: "[profile:case_executor] {{.Priority}}", variable: "Priority"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := agentapp.RenderPrompt(tc.profileKey, tc.template, in)
			requireUnstableSystemPromptError(t, err, tc.variable)

			// The admin save validator is the same guard: an unsafe revision can
			// never be persisted.
			err = agent.ValidatePrompt(tc.profileKey, tc.template, agentapp.PromptVariables(tc.profileKey))
			requireUnstableSystemPromptError(t, err, tc.variable)
		})
	}

	t.Run("control_flow", func(t *testing.T) {
		template := "[profile:clarification_host]{{if .TaskGoal}}目标 {{.TaskGoal}}{{end}}"
		_, err := agentapp.RenderPrompt(agentapp.ProfileKeyClarificationHost, template, in)
		requireUnstableSystemPromptError(t, err, "TaskGoal")
	})

	t.Run("dedupes_repeated_references", func(t *testing.T) {
		template := "[profile:case_executor] {{.CaseTitle}} / {{.CaseTitle}} / {{.Priority}}"
		_, err := agentapp.RenderPrompt(agentapp.ProfileKeyCaseExecutor, template, in)
		if err == nil {
			t.Fatal("execution variables accepted in the system prompt")
		}
		apiErr := platform.AsError(err)
		variables, ok := apiErr.Details["variables"].([]string)
		if !ok || len(variables) != 2 || variables[0] != "CaseTitle" || variables[1] != "Priority" {
			t.Fatalf("details variables = %#v", apiErr.Details["variables"])
		}
	})

	t.Run("sample_preview", func(t *testing.T) {
		_, err := agentapp.RenderPromptSample(agentapp.ProfileKeyClarificationHost,
			"[profile:clarification_host] {{.Now}}", nil)
		requireUnstableSystemPromptError(t, err, "Now")
	})
}

func TestPromptSampleValuesCoverCatalog(t *testing.T) {
	for _, key := range []string{
		agentapp.ProfileKeyClarificationHost,
		agentapp.ProfileKeyCaseExecutor,
	} {
		values := agentapp.PromptSampleValues(key)
		for _, variable := range agentapp.PromptVariables(key) {
			if _, ok := values[variable.Name]; !ok {
				t.Fatalf("profile %q sample misses %q", key, variable.Name)
			}
		}
	}
	if got := agentapp.PromptSampleValues(agentapp.ProfileKeyClarificationHost)["VersionTitle"]; got == "" {
		t.Fatal("VersionTitle sample is empty")
	}
}

// TestPromptVariableCatalogStability pins the Task 81 classification: every
// catalog variable declares its stability class, and the per-run class is
// exactly the work-item/run data that must never enter the cache-stable prefix.
func TestPromptVariableCatalogStability(t *testing.T) {
	want := map[string]string{
		"Now":           agent.PromptVariableStabilityRun,
		"ProfileKey":    agent.PromptVariableStabilityStable,
		"ProfileName":   agent.PromptVariableStabilityStable,
		"VersionID":     agent.PromptVariableStabilityStable,
		"VersionTitle":  agent.PromptVariableStabilityStable,
		"VersionStatus": agent.PromptVariableStabilityStable,
		"TaskType":      agent.PromptVariableStabilityRun,
		"TaskGoal":      agent.PromptVariableStabilityRun,
		"CaseRunID":     agent.PromptVariableStabilityRun,
		"WaveID":        agent.PromptVariableStabilityRun,
		"IntentID":      agent.PromptVariableStabilityRun,
		"CaseVersionID": agent.PromptVariableStabilityRun,
		"CaseTitle":     agent.PromptVariableStabilityRun,
		"Priority":      agent.PromptVariableStabilityRun,
	}
	seen := make(map[string]bool, len(want))
	for _, profile := range agentapp.Profiles() {
		for _, variable := range agentapp.PromptVariables(profile.Key) {
			seen[variable.Name] = true
			stability, ok := want[variable.Name]
			if !ok {
				t.Fatalf("variable %q of profile %q is not classified by the test", variable.Name, profile.Key)
			}
			if variable.Stability != stability {
				t.Fatalf("variable %q stability = %q, want %q", variable.Name, variable.Stability, stability)
			}
		}
	}
	for name := range want {
		if !seen[name] {
			t.Fatalf("classified variable %q is missing from every profile catalog", name)
		}
	}
}

// TestStaticSeedPromptsAreSystemStable pins that the built-in seeded prompts
// keep working under the Task 81 guard: they are static, valid system prompts
// and therefore never invalidate the prefix across runs.
func TestStaticSeedPromptsAreSystemStable(t *testing.T) {
	profiles := agentapp.Profiles()
	if len(profiles) == 0 {
		t.Fatal("no seed profiles")
	}
	in := sampleInput()
	for _, profile := range profiles {
		t.Run(profile.Key, func(t *testing.T) {
			if strings.TrimSpace(profile.SystemPrompt) == "" {
				t.Fatal("seeded system prompt is empty")
			}
			if err := agent.ValidatePrompt(profile.Key, profile.SystemPrompt, agentapp.PromptVariables(profile.Key)); err != nil {
				t.Fatalf("seeded system prompt rejected: %v", err)
			}
			defaultPrompt, ok := agentapp.DefaultPrompt(profile.Key)
			if !ok || defaultPrompt != profile.SystemPrompt {
				t.Fatalf("DefaultPrompt(%q) differs from the seed", profile.Key)
			}
			in.Profile.Key = profile.Key
			in.Profile.Name = profile.Name
			if _, err := agentapp.RenderPrompt(profile.Key, profile.SystemPrompt, in); err != nil {
				t.Fatalf("seeded system prompt does not render: %v", err)
			}
		})
	}
}

func TestRenderPromptSampleOverrides(t *testing.T) {
	tmpl := "[profile:clarification_host] {{.VersionTitle}} / {{.VersionStatus}}"
	rendered, err := agentapp.RenderPromptSample(agentapp.ProfileKeyClarificationHost, tmpl, map[string]string{
		"VersionTitle": "示例版本",
	})
	if err != nil {
		t.Fatalf("RenderPromptSample: %v", err)
	}
	if rendered != "[profile:clarification_host] 示例版本 / running" {
		t.Fatalf("rendered = %q", rendered)
	}
}

func TestRenderPromptSampleRejectsUnknown(t *testing.T) {
	_, err := agentapp.RenderPromptSample(agentapp.ProfileKeyClarificationHost,
		"[profile:clarification_host] {{.Bogus}}", nil)
	if err == nil {
		t.Fatal("unknown variable accepted")
	}
}

func variableNames(catalog []agent.PromptVariable) map[string]bool {
	names := make(map[string]bool, len(catalog))
	for _, variable := range catalog {
		names[variable.Name] = true
	}
	return names
}

// requireUnstableSystemPromptError asserts the typed, actionable rejection of a
// per-run variable in the cache-stable system prompt.
func requireUnstableSystemPromptError(t *testing.T, err error, variable string) {
	t.Helper()
	if err == nil {
		t.Fatalf("per-run variable .%s accepted in the system prompt", variable)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != 400 || apiErr.Code != "invalid_argument" || apiErr.Details["field"] != "system_prompt" {
		t.Fatalf("err = %#v", apiErr)
	}
	if apiErr.Details["reason"] != "unstable_variable" {
		t.Fatalf("reason = %#v", apiErr.Details["reason"])
	}
	variables, ok := apiErr.Details["variables"].([]string)
	if !ok || len(variables) != 1 || variables[0] != variable {
		t.Fatalf("details variables = %#v", apiErr.Details["variables"])
	}
	if !strings.Contains(apiErr.Message, "."+variable) || !strings.Contains(apiErr.Message, "user") {
		t.Fatalf("message not actionable: %q", apiErr.Message)
	}
}
