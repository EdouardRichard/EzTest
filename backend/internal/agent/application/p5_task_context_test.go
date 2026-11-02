package agentapp_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// p5VisualExecutionTask is the execution snapshot with one configured visual
// assertion. The IDs come from the visual context (visual_assertion.id) and the
// design fixture (case_assertion.id).
func p5VisualExecutionTask() agentapp.ExecutionTask {
	return agentapp.ExecutionTask{
		CaseRunID:     3001,
		WaveID:        2001,
		IntentID:      1001,
		CaseVersionID: 41,
		CaseTitle:     "视觉登录流程",
		Description:   "打开首页并与基线截图比对",
		Steps: []agentapp.ExecutionStep{{
			Seq: 1, Action: "打开首页", Expected: "首页可见", TargetVersionSUTID: 7,
		}},
		Assertions: []agentapp.ExecutionAssertion{{
			StepSeq: 1, AssertionSeq: 1, Kind: "visual",
			Expected: "首页与已批准基线一致", TargetVersionSUTID: 7,
			CaseAssertionID: 9001, VisualAssertionID: 6001,
		}, {
			StepSeq: 1, AssertionSeq: 2, Kind: "ui",
			Expected: "登录按钮可见", TargetVersionSUTID: 7,
		}},
	}
}

func TestExecutionPromptListsVisualAssertionIDs(t *testing.T) {
	executor, ok := profileByKey(agentapp.P3Profiles(), agentapp.ProfileKeyCaseExecutor)
	if !ok {
		t.Fatal("case_executor profile missing")
	}
	builder := agentapp.NewContextBuilder(&fakeSettings{}, nil)
	task := p5VisualExecutionTask()
	packet, _, err := builder.Build(context.Background(), agentapp.ContextInput{
		Profile: executor,
		Task: agentapp.TaskContext{
			Type: agentapp.WorkTypeExecuteCase,
			Goal: "按步骤执行用例",
		},
		Execution: &task,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var content strings.Builder
	for _, section := range packet.Sections {
		content.WriteString(section.Content)
		content.WriteString("\n")
	}
	text := content.String()

	for _, want := range []string{
		"visual_assertion_id=6001",
		"case_assertion_id=9001",
		"execution.visual_assert",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("execution prompt is missing %q:\n%s", want, text)
		}
	}
	if count := strings.Count(text, "visual_assertion_id="); count != 1 {
		t.Fatalf("visual_assertion_id appears %d times, want once:\n%s", count, text)
	}
	// The non-visual assertion must not be presented as a visual assertion.
	if strings.Contains(text, "assertion 2 [kind=ui") && strings.Contains(text, "visual_assertion_id=0") {
		t.Fatalf("non-visual assertion leaked a visual ID:\n%s", text)
	}
}

func TestTaskPackResultListsVisualAssertionIDs(t *testing.T) {
	provider := &fakeTaskProvider{task: p5VisualExecutionTask()}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextTaskPack, `{}`),
		{Content: "已读取任务"},
	}
	if _, err := mustRun(t, fixture, p3CaseItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var toolContent string
	for i := range fixture.repo.messages {
		message := fixture.repo.messages[i]
		if message.Role != agent.MessageRoleTool || len(message.Parts) == 0 {
			continue
		}
		if strings.Contains(message.Parts[0].Text, `"execution"`) {
			toolContent = message.Parts[0].Text
		}
	}
	if toolContent == "" {
		t.Fatal("context.task_pack tool result was not recorded")
	}
	var decoded struct {
		Execution struct {
			Assertions []struct {
				StepSeq         int    `json:"step_seq"`
				Kind            string `json:"kind"`
				CaseAssertionID string `json:"case_assertion_id"`
				VisualAssertion string `json:"visual_assertion_id"`
			} `json:"assertions"`
		} `json:"execution"`
	}
	if err := json.Unmarshal([]byte(toolContent), &decoded); err != nil {
		t.Fatalf("decode task_pack result: %v\n%s", err, toolContent)
	}
	assertions := decoded.Execution.Assertions
	if len(assertions) != 2 {
		t.Fatalf("task_pack assertions = %#v", assertions)
	}
	if assertions[0].Kind != "visual" || assertions[0].VisualAssertion != "6001" || assertions[0].CaseAssertionID != "9001" {
		t.Fatalf("visual assertion task pack = %#v", assertions[0])
	}
	if assertions[1].VisualAssertion != "" || assertions[1].CaseAssertionID != "" {
		t.Fatalf("non-visual assertion task pack = %#v", assertions[1])
	}
}

func TestRunnerRegistryExposesVisualAssertTool(t *testing.T) {
	provider := &fakeTaskProvider{task: p5VisualExecutionTask()}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "执行完成"}}
	if _, err := mustRun(t, fixture, p3CaseItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fixture.chat.requestCount() == 0 {
		t.Fatal("no LLM request recorded")
	}
	names := make([]string, 0)
	for _, definition := range fixture.chat.request(0).Tools {
		names = append(names, definition.Name)
		if definition.Name == tools.ToolExecutionVisualAssert {
			return
		}
	}
	t.Fatalf("case_executor request is missing %q: %v", tools.ToolExecutionVisualAssert, names)
}
