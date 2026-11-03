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
	in.Profile.SystemPrompt = "[profile:case_executor] 版本 {{.VersionTitle}}（{{.VersionStatus}}）"

	system := minimalSystemContent(in)
	if !strings.Contains(system, "版本 订单系统 Q4（running）") {
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

// TestStableSystemContentExcludesTaskContext pins the Task 80 split: the part
// of the system content that feeds the cache-stable prefix is the rendered
// profile prompt only. The per-work-item task block and the P3 execution block
// must never leak into it, or every work item gets a unique provider prefix.
func TestStableSystemContentExcludesTaskContext(t *testing.T) {
	in := internalPromptInput()
	in.Task.Constraints = []string{"work_item_id=1001 不能替用户确认需求"}
	in.Task.Scope = []string{"work_item_id=1001", "requirement_id=501"}
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "登录流程", Priority: "P1",
		Steps: []ExecutionStep{{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"}},
	}

	stable := stableSystemContent(in)
	if !strings.Contains(stable, "[profile:clarification_host] 版本 订单系统 Q4") {
		t.Fatalf("stable system content lost the rendered profile prompt: %s", stable)
	}
	for _, leaked := range []string{
		"# 任务", "# 执行任务", "work_item_id=1001", "requirement_id=501",
		"不能替用户确认需求", "登录流程", "打开登录页",
	} {
		if strings.Contains(stable, leaked) {
			t.Fatalf("per-work-item %q leaked into the stable system content:\n%s", leaked, stable)
		}
	}
}

// TestTaskContextBlockKeepsTaskAndExecution pins that the extracted task
// context still carries goal, constraints, acceptance, scope and the P3
// execution snapshot, and that the legacy context path (renderSections/minimal
// fallback) keeps rendering both blocks in the system section.
func TestTaskContextBlockKeepsTaskAndExecution(t *testing.T) {
	in := internalPromptInput()
	in.Execution = &ExecutionTask{
		CaseRunID: 3001, CaseVersionID: 41, CaseTitle: "登录流程", Priority: "P1",
		Steps: []ExecutionStep{{Seq: 1, Action: "打开登录页", Expected: "出现登录表单"}},
	}

	block := taskContextBlock(in)
	for _, want := range []string{
		"# 任务", "类型: chat.turn", "目标: 回答用户问题",
		"约束:", "不能替用户确认需求", "# 执行任务", "登录流程", "打开登录页",
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("task context block misses %q:\n%s", want, block)
		}
	}

	legacy := systemContent(in)
	if !strings.Contains(legacy, "[profile:clarification_host] 版本 订单系统 Q4") ||
		!strings.Contains(legacy, block) {
		t.Fatalf("legacy system content lost the prompt or the task context:\n%s", legacy)
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
