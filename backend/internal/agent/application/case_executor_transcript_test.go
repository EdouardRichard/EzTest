package agentapp_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// fourStepExecutionTask is the S5 retry-7 shape (Task 98): a four-step case with
// one assertion per step and no prior result, so the executor has to record
// everything itself.
func fourStepExecutionTask() agentapp.ExecutionTask {
	task := executionTaskFixture()
	task.PriorSteps = nil
	task.PriorAssertions = nil
	task.Steps = nil
	task.Assertions = nil
	for seq := 1; seq <= 4; seq++ {
		task.Steps = append(task.Steps, agentapp.ExecutionStep{
			Seq: seq, Action: fmt.Sprintf("执行步骤 %d", seq), Expected: "出现预期结果",
			AccountRole: "admin", TargetVersionSUTID: 7,
		})
		task.Assertions = append(task.Assertions, agentapp.ExecutionAssertion{
			StepSeq: seq, AssertionSeq: 1, Kind: "ui",
			Expected: fmt.Sprintf("步骤 %d 的断言满足", seq), TargetVersionSUTID: 7,
		})
	}
	return task
}

// newCaseExecutorTranscriptFixture wires a case-executor run whose P3 execution
// port is the recording fake, so the test can inspect which tool calls really
// reach the execution context.
func newCaseExecutorTranscriptFixture(t *testing.T, task agentapp.ExecutionTask) (*runnerFixture, *fakeExecutionPort) {
	t.Helper()
	provider := &fakeTaskProvider{task: task}
	fixture := newRunnerFixtureWithTaskProvider(t, provider)
	execution := &fakeExecutionPort{stepResult: map[string]any{"ok": true}}
	fixture.runner = agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      fixture.repo,
		Chat:         fixture.chat,
		Settings:     fixture.settings,
		Clock:        fixture.clock,
		IDs:          &seqIDGenerator{},
		Events:       fixture.events,
		Audit:        fixture.audit,
		Versions:     fixture.versions,
		TeamRoom:     fixture.team,
		Content:      fixture.content,
		TaskProvider: provider,
		ToolDeps: tools.Deps{
			Requirement: fixture.requirement,
			Attachment:  &fakeAttachmentPort{},
			Resource:    &fakeResourcePort{},
			Design:      fixture.design,
			Execution:   execution,
		},
		InstanceID: "instance-1",
	})
	return fixture, execution
}

func browserSnapshotCall(callID string) llmapp.ChatResponse {
	return llmapp.ChatResponse{
		ToolCalls:    []llmapp.ToolCall{{ID: callID, Name: tools.ToolBrowserSnapshot, Arguments: `{}`}},
		FinishReason: "tool_calls",
	}
}

func toolMessageContents(request llmapp.ChatRequest) []string {
	contents := make([]string, 0, len(request.Messages))
	for _, message := range request.Messages {
		if message.Role == "tool" {
			contents = append(contents, message.Content)
		}
	}
	return contents
}

func toolNames(definitions []llmapp.ToolDefinition) []string {
	names := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		names = append(names, definition.Name)
	}
	return names
}

// TestCaseExecutorPrunesBrowserSnapshots covers the per-turn cost root cause of
// the S5 retry-7 run (Task 98). Every turn re-sends the whole transcript, and
// each browser.snapshot result is a 12KB ARIA tree: replaying every older
// snapshot made the cumulative usage grow far faster than the steps. The case
// executor must keep only the newest browser reads verbatim and replace older
// ones with a bounded, self-describing stub; the immutable transcript keeps the
// full results for audit.
func TestCaseExecutorPrunesBrowserSnapshots(t *testing.T) {
	fixture, execution := newCaseExecutorTranscriptFixture(t, fourStepExecutionTask())
	// ~12KB per snapshot, the executor protocol cap.
	big := strings.Repeat("aria-node 登录按钮 ", 700)
	for seq := 1; seq <= 4; seq++ {
		execution.browserResults = append(execution.browserResults, map[string]any{
			"ok":     true,
			"result": map[string]any{"snapshot": fmt.Sprintf("SNAPSHOT-%d %s", seq, big)},
		})
	}
	fixture.chat.responses = []llmapp.ChatResponse{
		browserSnapshotCall("call_1"),
		browserSnapshotCall("call_2"),
		browserSnapshotCall("call_3"),
		browserSnapshotCall("call_4"),
		{Content: "已记录页面状态"},
	}

	if _, err := fixture.runner.Run(context.Background(), p3CaseItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if execution.browserCalls == nil || len(execution.browserCalls) != 4 {
		t.Fatalf("browser calls = %v, want four snapshots", execution.browserCalls)
	}

	// The fifth request is the last turn: it re-sends the whole transcript.
	last := fixture.chat.request(4)
	pruned := 0
	kept := 0
	for _, content := range toolMessageContents(last) {
		switch {
		case strings.Contains(content, `"pruned":true`):
			pruned++
			if !strings.Contains(content, `"tool":"browser.snapshot"`) {
				t.Fatalf("pruned stub does not name the browser read tool: %q", content)
			}
			if len(content) > 512 {
				t.Fatalf("pruned stub is not bounded (%d bytes): %q", len(content), content)
			}
		case strings.Contains(content, "SNAPSHOT-"):
			kept++
			if strings.Contains(content, "SNAPSHOT-1 ") || strings.Contains(content, "SNAPSHOT-2 ") {
				t.Fatalf("a stale snapshot is still replayed verbatim: %.120q", content)
			}
		}
	}
	if pruned != 2 || kept != 2 {
		t.Fatalf("transcript tool results: pruned=%d kept=%d, want the two newest snapshots kept and the two older ones pruned",
			pruned, kept)
	}
	// The prompt no longer carries the two stale 12KB ARIA trees.
	prompt := requestPrompt(last)
	if strings.Contains(prompt, "SNAPSHOT-1 ") || strings.Contains(prompt, "SNAPSHOT-2 ") {
		t.Fatal("the last request still replays the pruned snapshots")
	}
	if !strings.Contains(prompt, "SNAPSHOT-3 ") || !strings.Contains(prompt, "SNAPSHOT-4 ") {
		t.Fatal("the last request lost the live snapshots")
	}

	// Pruning is prompt-only: the persisted transcript keeps every real result.
	persisted := ""
	for _, message := range fixture.repo.messages {
		if message.Role != agent.MessageRoleTool || len(message.Parts) == 0 {
			continue
		}
		persisted += message.Parts[0].Text
	}
	for seq := 1; seq <= 4; seq++ {
		if !strings.Contains(persisted, fmt.Sprintf("SNAPSHOT-%d ", seq)) {
			t.Fatalf("the persisted tool transcript lost SNAPSHOT-%d", seq)
		}
	}
}

// TestCaseBudgetExitKeepsExecutedAssertions covers the S5 retry-7 outcome: the
// token budget turns the next turn into a settlement turn with tools disabled,
// so the assertions of already executed steps could never be written
// (assertion_total=0). The case executor keeps a recording-only settlement
// surface: it may record the real results of steps it already executed, but it
// may not run a new browser/network action nor finalize the case. The
// deterministic Finalize after the run aggregates the recorded results and
// writes the budget cause/handoff (Task 92), so the verdict stays honest and
// nothing is fabricated: the model still supplies every verdict, the server
// overwrites expected with the P2 assertion truth, and unexecuted steps stay
// unrecorded.
func TestCaseBudgetExitKeepsExecutedAssertions(t *testing.T) {
	fixture, execution := newCaseExecutorTranscriptFixture(t, fourStepExecutionTask())
	item := p3CaseItem()
	item.Budget = json.RawMessage(`{"max_tokens":10,"run_seconds":120,"max_turns_fuse":8}`)
	fixture.chat.responses = []llmapp.ChatResponse{
		{
			ToolCalls: []llmapp.ToolCall{{
				ID: "call_1", Name: tools.ToolExecutionStepRecord,
				Arguments: `{"seq":1,"status":"passed","observed":"登录弹窗可见"}`,
			}},
			Usage:        map[string]int{"total_tokens": 10},
			FinishReason: "tool_calls",
		},
		{
			ToolCalls: []llmapp.ToolCall{
				{
					ID: "call_2", Name: tools.ToolExecutionAssertionRecord,
					Arguments: `{"step_seq":1,"assertion_seq":1,"verdict":"passed","actual":"登录弹窗可见"}`,
				},
				// A new action must never run during the settlement turn.
				{ID: "call_3", Name: tools.ToolBrowserSnapshot, Arguments: `{}`},
			},
			Usage:        map[string]int{"total_tokens": 10},
			FinishReason: "tool_calls",
			Content:      "已补记并收尾",
		},
	}

	result, err := fixture.runner.Run(context.Background(), item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v, want a settled done run", result)
	}
	if result.SettlementCause != agentapp.BudgetCauseTokens {
		t.Fatalf("settlement cause = %q, want %q", result.SettlementCause, agentapp.BudgetCauseTokens)
	}
	if fixture.chat.requestCount() != 2 {
		t.Fatalf("chat calls = %d, want tool turn + recording settlement turn", fixture.chat.requestCount())
	}

	// The settlement turn exposes only the recording tools.
	settlement := fixture.chat.request(1)
	names := toolNames(settlement.Tools)
	wantTools := []string{
		tools.ToolExecutionStepRecord,
		tools.ToolExecutionAssertionRecord,
		tools.ToolExecutionEvidence,
	}
	if len(names) != len(wantTools) {
		t.Fatalf("settlement tools = %v, want %v", names, wantTools)
	}
	for _, want := range wantTools {
		found := false
		for _, name := range names {
			if name == want {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("settlement tools = %v, want %v", names, wantTools)
		}
	}
	for _, name := range names {
		if strings.HasPrefix(name, "browser.") || name == tools.ToolHTTPRequest || name == tools.ToolDBQuery {
			t.Fatalf("settlement turn exposes an execution action %q", name)
		}
		if name == tools.ToolExecutionCaseFinish {
			t.Fatal("the settlement turn must not finalize the case; Finalize writes the verdict with the budget cause")
		}
	}

	// The instruction names the pending assertion of the executed step and
	// never asks about a step that was not executed.
	instruction := ""
	if messages := settlement.Messages; len(messages) > 0 {
		instruction = messages[len(messages)-1].Content
	}
	for _, want := range []string{
		"execution.assertion_record", "不能再调用 execution.case_finish", "step 1 assertion 1",
		"不得记录未执行的步骤",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("settlement instruction is missing %q: %s", want, instruction)
		}
	}
	if strings.Contains(instruction, "step 2 assertion 1") {
		t.Fatalf("settlement instruction asks for an unexecuted step: %s", instruction)
	}

	// The recording calls really reach the execution context.
	if execution.assertCalls != 1 {
		t.Fatalf("assertion record calls = %d, want the settlement turn to record the executed assertion", execution.assertCalls)
	}
	if execution.assertInput.StepSeq != 1 || execution.assertInput.AssertionSeq != 1 ||
		execution.assertInput.Verdict != "passed" || execution.assertInput.Actual != "登录弹窗可见" {
		t.Fatalf("recorded assertion = %#v", execution.assertInput)
	}
	if execution.finishCalls != 0 {
		t.Fatalf("case finish calls = %d, want the deterministic Finalize to own the terminal verdict", execution.finishCalls)
	}
	if len(execution.browserCalls) != 0 {
		t.Fatalf("the settlement turn ran a new browser action: %v", execution.browserCalls)
	}

	// The rejected action is reported back as a tool error, not executed.
	rejected := ""
	for _, message := range fixture.repo.messages {
		if message.Role != agent.MessageRoleTool || len(message.Parts) == 0 {
			continue
		}
		rejected += message.Parts[0].Text
	}
	if !strings.Contains(rejected, "budget settlement turn") {
		t.Fatalf("the rejected settlement action has no tool error: %s", rejected)
	}
}
