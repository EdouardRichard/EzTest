package agentapp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// Task 98: per-turn cost and budget-exit recording of the case executor.
//
// Usage is cumulative across the turns of one execute.case run and every turn
// re-sends the whole transcript. The browser read tools return large payloads
// (browser.snapshot is capped at 12KB ARIA by the executor protocol), so
// replaying every older page read made the S5 retry-7 four-step case spend
// 406,167 tokens in 23 turns while still on step 1/4. Only the newest page reads
// are replayed verbatim; older ones become a bounded stub. The tools are
// re-readable and a stale page tree is not needed to act on the current page.
const (
	// caseExecutorKeepRecentBrowserReads is the number of newest browser read
	// results kept verbatim in the prompt transcript.
	caseExecutorKeepRecentBrowserReads = 2
	// caseExecutorPrunedBrowserRead is the bounded replacement of one older
	// browser read tool result. It names the tool so the model knows it can ask
	// for the current page state again instead of trusting a stale tree.
	caseExecutorPrunedBrowserRead = `{"pruned":true,"tool":%q,"note":"older browser read elided to bound the transcript; call this tool again if the current page state is still needed"}`
	// caseExecutorPendingAssertionLimit bounds the settlement instructions.
	caseExecutorPendingAssertionLimit = 12
)

// caseExecutorBrowserReadTools are the page-read tools whose older results are
// pruned from the prompt transcript. They are re-readable and their full content
// is also kept by the auto-evidence path (console/network) where applicable.
var caseExecutorBrowserReadTools = map[string]struct{}{
	tools.ToolBrowserSnapshot:     {},
	tools.ToolBrowserConsole:      {},
	tools.ToolBrowserNetwork:      {},
	tools.ToolBrowserStorageState: {},
}

// caseExecutorSettlementTools is the recording-only tool surface of a budget
// settlement turn. The turn may write the results of steps it already executed
// (the server still overwrites expected from the P2 assertion truth and
// aggregates the verdict deterministically); it may not start a new browser,
// network or database action. execution.case_finish is deliberately absent: the
// deterministic Finalize after the run aggregates the recorded results and
// writes the terminal verdict together with the budget cause/handoff (Task 92).
// Unexecuted steps stay unrecorded and therefore cannot become a pass.
var caseExecutorSettlementTools = []string{
	tools.ToolExecutionStepRecord,
	tools.ToolExecutionAssertionRecord,
	tools.ToolExecutionEvidence,
}

// settlementRecordingActive reports whether the current turn is the case
// executor's recording-only budget settlement turn.
func settlementRecordingActive(state *runState) bool {
	return state != nil && state.softNoted && state.profile.Key == ProfileKeyCaseExecutor &&
		state.input.Execution != nil
}

// isCaseExecutorSettlementTool reports whether name is part of the settlement
// recording surface.
func isCaseExecutorSettlementTool(name string) bool {
	for _, allowed := range caseExecutorSettlementTools {
		if allowed == name {
			return true
		}
	}
	return false
}

// caseExecutorSettlementRejection is the tool error of a call outside the
// recording-only settlement surface (a browser/network action or
// execution.case_finish). It never reaches the SUT and explicitly tells the
// model who writes the terminal verdict.
func caseExecutorSettlementRejection(name string) *tools.Error {
	return tools.Invalid("tool", "the budget settlement turn only allows recording already executed results ("+
		strings.Join(caseExecutorSettlementTools, ", ")+"); "+name+
		" was not executed. The server aggregates the recorded results and writes the verdict after the run.")
}

// caseExecutorSettlementDefinitions filters the run's exposed tool definitions to
// the settlement recording surface. It is empty outside the case executor's
// settlement turn, which keeps every other profile on the existing tool-less
// settlement behavior.
func caseExecutorSettlementDefinitions(state *runState) []llmapp.ToolDefinition {
	if !settlementRecordingActive(state) {
		return nil
	}
	definitions := make([]llmapp.ToolDefinition, 0, len(caseExecutorSettlementTools))
	for _, definition := range state.definitions {
		if isCaseExecutorSettlementTool(definition.Name) {
			definitions = append(definitions, definition)
		}
	}
	return definitions
}

// pruneCaseExecutorBrowserReads rewrites the in-memory prompt transcript of a
// case-executor run: the newest caseExecutorKeepRecentBrowserReads browser read
// results stay verbatim and every older one becomes a bounded stub. The tool
// call identity is preserved (the assistant tool_call/tool message pairing stays
// valid) and the persisted transcript is not touched; only the per-turn model
// prompt is bounded.
func (r *Runner) pruneCaseExecutorBrowserReads(state *runState) {
	if state == nil || state.profile.Key != ProfileKeyCaseExecutor || len(state.messages) == 0 {
		return
	}
	callNames := make(map[string]string)
	for _, message := range state.messages {
		for _, call := range message.ToolCalls {
			if call.ID == "" || call.Name == "" {
				continue
			}
			callNames[call.ID] = call.Name
		}
	}
	kept := 0
	for index := len(state.messages) - 1; index >= 0; index-- {
		message := &state.messages[index]
		if message.Role != "tool" {
			continue
		}
		name := callNames[message.ToolCallID]
		if _, ok := caseExecutorBrowserReadTools[name]; !ok {
			continue
		}
		if kept < caseExecutorKeepRecentBrowserReads {
			kept++
			continue
		}
		message.Content = fmt.Sprintf(caseExecutorPrunedBrowserRead, name)
	}
}

// caseExecutorSettlementInstruction is the settlement instruction of a case
// executor run. Unlike the generic tool-less settlement text it names the
// recording tools that are still available and the concrete assertions of already
// executed steps that still have no result, so the turn is a real recording
// opportunity instead of only a prose wrap-up.
func caseExecutorSettlementInstruction(cause, pending string) string {
	budget := "本次运行"
	switch cause {
	case BudgetCauseTokens:
		budget = "本次运行的 token 预算"
	case BudgetCauseTime:
		budget = "本次运行的时间预算"
	case BudgetCauseTurns:
		budget = "本次运行的轮次预算"
	}
	var builder strings.Builder
	builder.WriteString(budget)
	builder.WriteString("即将用尽，已停止新的浏览器/网络/数据库操作，也不能再调用 execution.case_finish；服务端会在运行结束后按已记录的步骤与断言聚合终态。你仍可调用以下记录工具，把已执行步骤的真实结果补记录完整：")
	builder.WriteString("\n- execution.step_record：补记已执行步骤的 seq/status/observed；")
	builder.WriteString("\n- execution.assertion_record：逐条补记已执行步骤的 verdict/actual/reason（expected 以服务端断言真值为准）；")
	builder.WriteString("\n- execution.evidence：为失败/不确定的结果补交文本证据。")
	builder.WriteString("\n不得记录未执行的步骤或未检查的断言，不得虚构 passed；无法确认的断言记 inconclusive。")
	if pending != "" {
		builder.WriteString("\n")
		builder.WriteString(pending)
	}
	builder.WriteString("\n补记完成后直接给出简短收尾说明，服务端会聚合已记录的结果并写入终态。")
	return builder.String()
}

// pendingCaseExecutorAssertions lists the assertions of already executed steps
// that have no recorded result yet. A step counts as executed only when the
// model recorded a terminal status for it (this run or a resumed prior result),
// so the settlement instruction never asks for an unexecuted step.
func pendingCaseExecutorAssertions(state *runState) string {
	if state == nil || state.input.Execution == nil {
		return ""
	}
	task := state.input.Execution
	stepStatus := make(map[int]string, len(task.PriorSteps)+len(state.recordedSteps))
	for _, prior := range task.PriorSteps {
		stepStatus[prior.Seq] = prior.Status
	}
	for seq, status := range state.recordedSteps {
		stepStatus[seq] = status
	}
	recorded := make(map[[2]int]struct{}, len(task.PriorAssertions)+len(state.recordedAssertions))
	for _, prior := range task.PriorAssertions {
		recorded[[2]int{prior.StepSeq, prior.AssertionSeq}] = struct{}{}
	}
	for key := range state.recordedAssertions {
		recorded[key] = struct{}{}
	}
	pending := make([]string, 0, 4)
	for _, assertion := range task.Assertions {
		if !caseExecutorStepExecuted(stepStatus[assertion.StepSeq]) {
			continue
		}
		if _, ok := recorded[[2]int{assertion.StepSeq, assertion.AssertionSeq}]; ok {
			continue
		}
		pending = append(pending, fmt.Sprintf("step %d assertion %d", assertion.StepSeq, assertion.AssertionSeq))
		if len(pending) >= caseExecutorPendingAssertionLimit {
			break
		}
	}
	if len(pending) == 0 {
		executedSteps := 0
		for _, status := range stepStatus {
			if caseExecutorStepExecuted(status) {
				executedSteps++
			}
		}
		if executedSteps == 0 {
			return "尚未记录任何已执行步骤；不得虚构步骤或断言，请如实说明未完成原因，服务端会按已记录结果聚合终态。"
		}
		return "所有已记录步骤的断言均已补记；请如实说明未完成部分，服务端会聚合已记录结果并写入终态。"
	}
	return "未记录断言的已执行步骤：" + strings.Join(pending, ", ") + "。请先逐条补记这些断言。"
}

// caseExecutorStepExecuted reports whether a recorded step status means the step
// was really executed. pending/running steps are not executed yet.
func caseExecutorStepExecuted(status string) bool {
	switch status {
	case "passed", "failed", "blocked", "skipped":
		return true
	default:
		return false
	}
}

// trackCaseExecutorRecording records one successful execution recording call of
// this run so the settlement turn can name the assertions still missing. A call
// that the execution context rejected (for example a stale dispatch fence) is
// never tracked: only real, persisted records count.
func (state *runState) trackCaseExecutorRecording(call llmapp.ToolCall) {
	if state == nil || state.profile.Key != ProfileKeyCaseExecutor {
		return
	}
	switch call.Name {
	case tools.ToolExecutionStepRecord:
		var args struct {
			Seq    int    `json:"seq"`
			Status string `json:"status"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil || args.Seq <= 0 {
			return
		}
		if state.recordedSteps == nil {
			state.recordedSteps = map[int]string{}
		}
		state.recordedSteps[args.Seq] = args.Status
	case tools.ToolExecutionAssertionRecord:
		var args struct {
			StepSeq      int `json:"step_seq"`
			AssertionSeq int `json:"assertion_seq"`
		}
		if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil ||
			args.StepSeq <= 0 || args.AssertionSeq <= 0 {
			return
		}
		if state.recordedAssertions == nil {
			state.recordedAssertions = map[[2]int]struct{}{}
		}
		state.recordedAssertions[[2]int{args.StepSeq, args.AssertionSeq}] = struct{}{}
	}
}
