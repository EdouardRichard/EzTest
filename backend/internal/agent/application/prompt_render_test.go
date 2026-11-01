package agentapp_test

import (
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func promptRenderInput() agentapp.ContextInput {
	in := sampleInput()
	in.Profile.SystemPrompt = "[profile:clarification_host]\n版本 {{.VersionTitle}}（{{.VersionStatus}}），任务 {{.TaskType}}: {{.TaskGoal}}。"
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

func TestRenderPromptSubstitutesContextValues(t *testing.T) {
	in := promptRenderInput()
	in.VersionPack.VersionID = 10
	in.VersionPack.Title = "订单系统 Q4"
	in.VersionPack.Status = "running"
	in.Task.Type = "chat.turn"
	in.Task.Goal = "回答登录问题"

	rendered, err := agentapp.RenderPrompt(agentapp.ProfileKeyClarificationHost, in.Profile.SystemPrompt, in)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	if !strings.Contains(rendered, "版本 订单系统 Q4（running）") {
		t.Fatalf("rendered = %q", rendered)
	}
	if !strings.Contains(rendered, "任务 chat.turn: 回答登录问题") {
		t.Fatalf("rendered = %q", rendered)
	}
	if !strings.Contains(rendered, "[profile:clarification_host]") {
		t.Fatalf("marker lost: %q", rendered)
	}
}

func TestRenderPromptExecutionVariables(t *testing.T) {
	in := promptRenderInput()
	in.Profile.Key = agentapp.ProfileKeyCaseExecutor
	in.Profile.SystemPrompt = "[profile:case_executor] 用例 {{.CaseTitle}} priority={{.Priority}} case_run={{.CaseRunID}} wave={{.WaveID}} intent={{.IntentID}} case_version={{.CaseVersionID}}"
	in.Execution = &agentapp.ExecutionTask{
		CaseRunID: 3001, WaveID: 12, IntentID: 44, CaseVersionID: 55,
		CaseTitle: "登录流程", Priority: "P1",
	}
	rendered, err := agentapp.RenderPrompt(in.Profile.Key, in.Profile.SystemPrompt, in)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	for _, want := range []string{"登录流程", "priority=P1", "case_run=3001", "wave=12", "intent=44", "case_version=55"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("rendered misses %q: %q", want, rendered)
		}
	}

	// A profile without an execution task renders execution variables as empty
	// strings instead of failing the run.
	in.Execution = nil
	rendered, err = agentapp.RenderPrompt(in.Profile.Key, in.Profile.SystemPrompt, in)
	if err != nil {
		t.Fatalf("RenderPrompt without execution: %v", err)
	}
	if !strings.Contains(rendered, "priority= case_run= wave= intent= case_version=") {
		t.Fatalf("empty execution values = %q", rendered)
	}
}

func TestRenderPromptNowIsRFC3339UTC(t *testing.T) {
	in := promptRenderInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] now={{.Now}}"
	rendered, err := agentapp.RenderPrompt(in.Profile.Key, in.Profile.SystemPrompt, in)
	if err != nil {
		t.Fatalf("RenderPrompt: %v", err)
	}
	value := strings.TrimPrefix(rendered, "[profile:clarification_host] now=")
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("Now = %q: %v", value, err)
	}
	if parsed.Location() != time.UTC {
		t.Fatalf("Now location = %v", parsed.Location())
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

func TestRenderPromptSampleOverrides(t *testing.T) {
	tmpl := "[profile:clarification_host] {{.VersionTitle}} / {{.TaskGoal}}"
	rendered, err := agentapp.RenderPromptSample(agentapp.ProfileKeyClarificationHost, tmpl, map[string]string{
		"VersionTitle": "示例版本",
	})
	if err != nil {
		t.Fatalf("RenderPromptSample: %v", err)
	}
	if rendered != "[profile:clarification_host] 示例版本 / 回答用户关于登录流程的问题" {
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
