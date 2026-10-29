package agent_tool

import (
	"context"
	"encoding/json"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Schemas of the three structured submission tools (design spec §5.5). Every
// execution id is a decimal string; only step seq is a number.
const (
	schemaStepRecord      = `{"type":"object","properties":{"seq":{"type":"integer","minimum":1},"status":{"type":"string","enum":["passed","failed"]},"actual":{"type":"string"},"evidence_ids":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}}},"required":["seq","status"],"additionalProperties":false}`
	schemaAssertionRecord = `{"type":"object","properties":{"assertion_id":{"type":"string","pattern":"^[0-9]+$"},"verdict":{"type":"string","enum":["passed","failed"]},"actual":{"type":"string"},"evidence_ids":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}}},"required":["assertion_id","verdict","evidence_ids"],"additionalProperties":false}`
	schemaCaseFinish      = `{"type":"object","properties":{"verdict":{"type":"string","enum":["passed","failed","blocked","inconclusive"]},"failure_class":{"type":"string","enum":["application","environment","session","model","case"]},"reason":{"type":"string"},"evidence_ids":{"type":"array","items":{"type":"string","pattern":"^[0-9]+$"}}},"required":["verdict"],"additionalProperties":false}`
)

// submitDefinitions returns the three submission tools of design spec §7.1.
func submitDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			name:        "execution.step_record",
			description: "Record the next unfinished step. seq must be the current next step; failed requires an actual/reason.",
			schema:      schemaStepRecord,
			run:         runStepRecord,
		},
		{
			name:        "execution.assertion_record",
			description: "Record one assertion of this case snapshot exactly once. passed and failed both require at least one evidence id of this case run.",
			schema:      schemaAssertionRecord,
			run:         runAssertionRecord,
		},
		{
			name:        "execution.case_finish",
			description: "Finish the case run with passed, failed, blocked or inconclusive and an optional proposed failure class/reason. The server validates the final combination.",
			schema:      schemaCaseFinish,
			run:         runCaseFinish,
		},
	}
}

// stepRecordInput is the validated argument shape of execution.step_record.
type stepRecordInput struct {
	Seq         int      `json:"seq"`
	Status      string   `json:"status"`
	Actual      string   `json:"actual"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// runStepRecord executes execution.step_record.
func runStepRecord(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[stepRecordInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if input.Seq < 1 {
		return agentdomain.ToolResult{}, invalidArguments("seq must be a positive step number")
	}
	if input.Status != string(executiondomain.StepStatusPassed) && input.Status != string(executiondomain.StepStatusFailed) {
		return agentdomain.ToolResult{}, invalidArguments("status must be passed or failed")
	}
	if input.Status == string(executiondomain.StepStatusFailed) && strings.TrimSpace(input.Actual) == "" {
		return agentdomain.ToolResult{}, invalidArguments("a failed step requires actual/reason")
	}
	evidenceIDs, err := parseEvidenceIDs(input.EvidenceIDs)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if set.recorder == nil {
		return agentdomain.ToolResult{}, internalError("the execution recorder is not configured")
	}
	result, err := set.recorder.RecordStep(ctx, invocation.CaseRunID, application.StepRecordInput{
		Seq:         input.Seq,
		Status:      executiondomain.StepStatus(input.Status),
		Actual:      input.Actual,
		EvidenceIDs: evidenceIDs,
	})
	if err != nil {
		return agentdomain.ToolResult{}, mapExecutionError(err)
	}
	return jsonResult(struct {
		StepResultID  string `json:"step_result_id"`
		Seq           int    `json:"seq"`
		Status        string `json:"status"`
		EvidenceCount int    `json:"evidence_count"`
	}{
		StepResultID:  decimalID(result.ID),
		Seq:           result.Seq,
		Status:        string(result.Status),
		EvidenceCount: result.EvidenceCount,
	})
}

// assertionRecordInput is the validated argument shape of
// execution.assertion_record.
type assertionRecordInput struct {
	AssertionID string   `json:"assertion_id"`
	Verdict     string   `json:"verdict"`
	Actual      string   `json:"actual"`
	EvidenceIDs []string `json:"evidence_ids"`
}

// runAssertionRecord executes execution.assertion_record.
func runAssertionRecord(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[assertionRecordInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	assertionID, err := parseDecimalID(input.AssertionID)
	if err != nil {
		return agentdomain.ToolResult{}, invalidArguments("assertion_id must be a positive decimal string")
	}
	if input.Verdict != string(executiondomain.StepStatusPassed) && input.Verdict != string(executiondomain.StepStatusFailed) {
		return agentdomain.ToolResult{}, invalidArguments("verdict must be passed or failed")
	}
	if len(input.EvidenceIDs) == 0 {
		return agentdomain.ToolResult{}, invalidArguments("a passed or failed assertion requires at least one evidence id")
	}
	evidenceIDs, err := parseEvidenceIDs(input.EvidenceIDs)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if set.recorder == nil {
		return agentdomain.ToolResult{}, internalError("the execution recorder is not configured")
	}
	result, err := set.recorder.RecordAssertion(ctx, invocation.CaseRunID, application.AssertionRecordInput{
		AssertionID: assertionID,
		Verdict:     input.Verdict,
		Actual:      input.Actual,
		EvidenceIDs: evidenceIDs,
	})
	if err != nil {
		return agentdomain.ToolResult{}, mapExecutionError(err)
	}
	return jsonResult(struct {
		AssertionResultID string `json:"assertion_result_id"`
		AssertionID       string `json:"assertion_id"`
		Verdict           string `json:"verdict"`
	}{
		AssertionResultID: decimalID(result.ID),
		AssertionID:       decimalID(result.AssertionID),
		Verdict:           result.Verdict,
	})
}

// caseFinishInput is the validated argument shape of execution.case_finish.
type caseFinishInput struct {
	Verdict      string   `json:"verdict"`
	FailureClass string   `json:"failure_class"`
	Reason       string   `json:"reason"`
	EvidenceIDs  []string `json:"evidence_ids"`
}

// runCaseFinish executes execution.case_finish and disposes the case browser
// context on success.
func runCaseFinish(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[caseFinishInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	verdict, ok := parseVerdict(input.Verdict)
	if !ok {
		return agentdomain.ToolResult{}, invalidArguments("verdict must be passed, failed, blocked or inconclusive")
	}
	var failureClass *executiondomain.FailureClass
	if input.FailureClass != "" {
		parsed, ok := parseFailureClass(input.FailureClass)
		if !ok {
			return agentdomain.ToolResult{}, invalidArguments("failure_class must be application, environment, session, model or case")
		}
		failureClass = &parsed
	}
	evidenceIDs, err := parseEvidenceIDs(input.EvidenceIDs)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if set.recorder == nil {
		return agentdomain.ToolResult{}, internalError("the execution recorder is not configured")
	}
	result, err := set.recorder.FinishCase(ctx, invocation.CaseRunID, application.FinishInput{
		Decision: executiondomain.FinishDecision{
			Verdict:      verdict,
			FailureClass: failureClass,
			Reason:       input.Reason,
		},
		EvidenceIDs: evidenceIDs,
	})
	if err != nil {
		return agentdomain.ToolResult{}, mapExecutionError(err)
	}
	set.closeCaseBrowser(ctx, invocation.CaseRunID)
	return jsonResult(struct {
		CaseRunID string `json:"case_run_id"`
		Status    string `json:"status"`
	}{
		CaseRunID: decimalID(result.ID),
		Status:    string(result.Status),
	})
}

// parseVerdict validates one case-level verdict.
func parseVerdict(raw string) (executiondomain.Verdict, bool) {
	verdict := executiondomain.Verdict(raw)
	switch verdict {
	case executiondomain.VerdictPassed, executiondomain.VerdictFailed,
		executiondomain.VerdictBlocked, executiondomain.VerdictInconclusive:
		return verdict, true
	default:
		return "", false
	}
}

// parseFailureClass validates one failure class.
func parseFailureClass(raw string) (executiondomain.FailureClass, bool) {
	failureClass := executiondomain.FailureClass(raw)
	switch failureClass {
	case executiondomain.FailureClassApplication, executiondomain.FailureClassEnvironment,
		executiondomain.FailureClassSession, executiondomain.FailureClassModel,
		executiondomain.FailureClassCase:
		return failureClass, true
	default:
		return "", false
	}
}
