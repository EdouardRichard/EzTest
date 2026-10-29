package agent_tool

import (
	"context"
	"encoding/json"
	"regexp"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// schemaCaseContext is the empty argument object of execution.case_context.
const schemaCaseContext = `{"type":"object","properties":{},"additionalProperties":false}`

// sensitiveAssignmentRE masks "password=..." / "token: ..." assignments inside
// the free-text preconditions field. The value runs to the next separator or
// line break so a value containing spaces is masked completely. The key
// alternation covers api_key, x-api-key and apikey spellings (spec §5.9). The
// JSON key redaction of test data uses the evidence service's tested RedactJSON.
var sensitiveAssignmentRE = regexp.MustCompile(`(?i)(password|passwd|token|secret|(?:x-)?api[_-]?key)(\s*["']?\s*[:=]\s*["']?)([^;,\r\n]*)`)

// runCaseContext executes execution.case_context: the agent's read model of the
// pinned case run, with test data and preconditions redacted (design spec
// §5.9: the execution agent never receives credentials).
func runCaseContext(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	if _, err := decodeArguments[struct{}](args); err != nil {
		return agentdomain.ToolResult{}, err
	}
	view, err := loadCaseContext(ctx, set.recorder, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	return jsonResult(caseContextFrom(view))
}

// caseContextFrom projects the Recorder read model onto the tool output.
func caseContextFrom(view application.CaseContextView) caseContextValue {
	snapshot := view.CaseRun.Snapshot
	steps := make([]caseStep, 0, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		steps = append(steps, caseStep{
			Seq:      step.Seq,
			Action:   step.Action,
			Expected: step.Expected,
			Data:     redactSensitiveMap(step.Data),
		})
	}
	assertions := make([]caseAssert, 0, len(snapshot.Assertions))
	for _, assertion := range snapshot.Assertions {
		assertions = append(assertions, caseAssert{
			AssertionID: decimalID(assertion.ID),
			StepSeq:     assertion.StepSeq,
			Kind:        assertion.Kind,
			Target:      assertion.Target,
			Expression:  assertion.Expression,
			Expected:    assertion.Expected,
		})
	}

	completed := make([]int, len(view.CompletedSeqs))
	copy(completed, view.CompletedSeqs)
	completedSteps := make([]caseCompletedStep, 0, len(view.CompletedSteps))
	for _, step := range view.CompletedSteps {
		completedSteps = append(completedSteps, caseCompletedStep{
			Seq:           step.Seq,
			Status:        string(step.Status),
			Actual:        step.Actual,
			EvidenceCount: step.EvidenceCount,
		})
	}
	completedSet := make(map[int]struct{}, len(completed))
	for _, seq := range completed {
		completedSet[seq] = struct{}{}
	}
	remaining := make([]caseStep, 0, len(steps))
	if len(view.RemainingSteps) > 0 {
		// The Recorder resolved the authoritative remaining checkpoint; only the
		// step summary is projected so snapshot data cannot leak past redaction.
		for _, step := range view.RemainingSteps {
			remaining = append(remaining, caseStep{Seq: step.Seq, Action: step.Action, Expected: step.Expected})
		}
	} else {
		for _, step := range steps {
			if _, done := completedSet[step.Seq]; done {
				continue
			}
			remaining = append(remaining, caseStep{Seq: step.Seq, Action: step.Action, Expected: step.Expected})
		}
	}

	return caseContextValue{
		CaseRunID:          decimalID(view.CaseRun.ID),
		BatchID:            decimalID(view.CaseRun.BatchID),
		VersionID:          decimalID(view.CaseRun.VersionID),
		CaseNodeID:         decimalID(view.CaseRun.CaseNodeID),
		Title:              snapshot.Node.Title,
		Status:             string(view.CaseRun.Status),
		Attempt:            view.CaseRun.Attempt,
		Steps:              steps,
		RemainingSteps:     remaining,
		Assertions:         assertions,
		CompletedSeqs:      completed,
		CompletedSteps:     completedSteps,
		InterruptedStepSeq: view.InterruptedStepSeq,
		CleanupRequired:    view.CleanupRequired,
		BaseURL:            view.BaseURL,
		AllowedHosts:       append([]string(nil), view.AllowedHosts...),
		TestData:           redactSensitiveMap(snapshot.Node.TestData),
		Preconditions:      redactPreconditions(snapshot.Node.Preconditions),
	}
}

// redactSensitiveMap masks password/passwd/token/secret/apikey values in a
// snapshot data map, including nested objects and arrays.
func redactSensitiveMap(data map[string]any) map[string]any {
	if len(data) == 0 {
		return nil
	}
	raw, err := json.Marshal(data)
	if err != nil {
		// A snapshot with non-JSON data cannot be redacted reliably; the safest
		// projection is to omit it rather than risk leaking a value.
		return nil
	}
	var redacted map[string]any
	if err := json.Unmarshal(application.RedactJSON(raw), &redacted); err != nil {
		return nil
	}
	if len(redacted) == 0 {
		return nil
	}
	return redacted
}

// redactPreconditions masks sensitive assignments embedded in the free-text
// preconditions field.
func redactPreconditions(text string) string {
	if text == "" {
		return text
	}
	return sensitiveAssignmentRE.ReplaceAllString(text, "${1}${2}***")
}
