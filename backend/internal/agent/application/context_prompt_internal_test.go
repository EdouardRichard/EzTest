package agentapp

import (
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func internalPromptInput() ContextInput {
	return ContextInput{
		Profile: agent.AgentProfile{
			Key:          ProfileKeyClarificationHost,
			Name:         "澄清 Host",
			SystemPrompt: "[profile:clarification_host] 版本 {{.VersionTitle}}",
		},
		Task: TaskContext{
			Type:        WorkTypeChatTurn,
			Goal:        "回答用户问题",
			Constraints: []string{"不能替用户确认需求"},
		},
		VersionPack: VersionPack{VersionID: 10, Title: "订单系统 Q4", Status: "running"},
	}
}

func TestSystemContentRendersPromptBeforeTaskBlock(t *testing.T) {
	in := internalPromptInput()
	system := systemContent(in)

	if !strings.Contains(system, "[profile:clarification_host] 版本 订单系统 Q4") {
		t.Fatalf("rendered prompt missing: %s", system)
	}
	promptIndex := strings.Index(system, "[profile:clarification_host]")
	taskIndex := strings.Index(system, "# 任务")
	if promptIndex < 0 || taskIndex < 0 || promptIndex > taskIndex {
		t.Fatalf("system order = %d/%d: %s", promptIndex, taskIndex, system)
	}
}

func TestMinimalSystemContentRendersPromptAfterTaskBlock(t *testing.T) {
	in := internalPromptInput()
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseTitle: "登录流程", Priority: "P1",
	}
	in.Profile.Key = ProfileKeyCaseExecutor
	in.Profile.SystemPrompt = "[profile:case_executor] 用例 {{.CaseTitle}}/{{.Priority}}"

	system := minimalSystemContent(in)
	if !strings.Contains(system, "用例 登录流程/P1") {
		t.Fatalf("minimal prompt not rendered: %s", system)
	}
	taskIndex := strings.Index(system, "# 任务")
	executionIndex := strings.Index(system, "# 执行任务")
	promptIndex := strings.Index(system, "[profile:case_executor]")
	if taskIndex < 0 || executionIndex < 0 || promptIndex < 0 {
		t.Fatalf("minimal system blocks missing: %s", system)
	}
	if !(taskIndex < executionIndex && executionIndex < promptIndex) {
		t.Fatalf("minimal block order = %d/%d/%d: %s", taskIndex, executionIndex, promptIndex, system)
	}
}

func TestSystemContentFallsBackToSeedOnRenderFailure(t *testing.T) {
	in := internalPromptInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] {{.Bogus}}"

	system := systemContent(in)
	if strings.Contains(system, "{{.Bogus}}") {
		t.Fatalf("broken template rendered: %s", system)
	}
	defaultPrompt, ok := DefaultPrompt(ProfileKeyClarificationHost)
	if !ok {
		t.Fatal("default prompt missing")
	}
	firstLine := strings.SplitN(defaultPrompt, "\n", 2)[0]
	if !strings.Contains(system, firstLine) {
		t.Fatalf("default prompt fallback missing: %s", system)
	}
	if !strings.Contains(system, "[profile:clarification_host]") {
		t.Fatalf("marker lost in fallback: %s", system)
	}
}

func TestSystemContentKeepsConfiguredBodyForUnknownProfile(t *testing.T) {
	in := internalPromptInput()
	in.Profile.Key = "future_profile"
	in.Profile.SystemPrompt = "[profile:future_profile] {{.Bogus}}"

	system := systemContent(in)
	if !strings.Contains(system, "[profile:future_profile] {{.Bogus}}") {
		t.Fatalf("unknown profile body not retained: %s", system)
	}
}

func TestSystemContentKeepsPlainPromptUnchanged(t *testing.T) {
	in := internalPromptInput()
	in.Profile.SystemPrompt = "[profile:clarification_host] 你是澄清 Host。"
	system := systemContent(in)
	if !strings.HasPrefix(system, "[profile:clarification_host] 你是澄清 Host。") {
		t.Fatalf("plain prompt changed: %s", system)
	}
}
