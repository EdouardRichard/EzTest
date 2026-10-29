package agent_tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// schemaVisualAssert is the one-argument object of execution.visual_assert
// (design spec §9.3): the agent supplies only the assertion id. Selector,
// region, threshold, viewport, DPR and the baseline are read server-side from
// the pinned snapshot, so additionalProperties=false rejects any agent-supplied
// comparison configuration.
const schemaVisualAssert = `{"type":"object","properties":{"assertionId":{"type":"string","pattern":"^[0-9]+$"}},"required":["assertionId"],"additionalProperties":false}`

// visualAssertDefinitions returns the P8 execution.visual_assert tool. It is
// appended after execution.db_assert so the frozen P6 tool order is unchanged.
func visualAssertDefinitions() []toolDefinition {
	return []toolDefinition{
		{
			name:        "execution.visual_assert",
			description: "Compare one kind=visual assertion of this case snapshot against its admin-approved baseline and record visual_actual/visual_diff evidence plus the assertion result. Missing baseline finishes inconclusive(case); a viewport/device-scale mismatch finishes inconclusive(environment); a diff above the threshold is failed(application); otherwise passed. Completed by execution.step_record/case_finish.",
			schema:      schemaVisualAssert,
			run:         runVisualAssert,
		},
	}
}

// visualAssertInput is the validated argument shape of execution.visual_assert.
// Only assertionId is accepted (design spec §9.3).
type visualAssertInput struct {
	AssertionID string `json:"assertionId"`
}

// visualAssertValue is the model-facing result of one visual comparison. It
// never carries image bytes or filesystem paths, only ids, statistics and the
// four-state classification.
type visualAssertValue struct {
	AssertionID      string  `json:"assertionId"`
	Verdict          string  `json:"verdict"`
	Classification   string  `json:"classification"`
	FailureClass     string  `json:"failureClass,omitempty"`
	Reason           string  `json:"reason,omitempty"`
	DiffRatio        float64 `json:"diffRatio"`
	Threshold        float64 `json:"threshold"`
	EnvironmentMatch bool    `json:"environmentMatch"`
	ActualEvidenceID string  `json:"actualEvidenceId,omitempty"`
	DiffEvidenceID   string  `json:"diffEvidenceId,omitempty"`
	Repeated         bool    `json:"repeated,omitempty"`
}

// runVisualAssert executes execution.visual_assert. The assertion configuration
// is resolved from the pinned case snapshot before any executor call so an
// unapproved baseline is classified without opening a browser; an executor that
// cannot compare visuals never fakes a pass.
func runVisualAssert(ctx context.Context, set *toolSet, args json.RawMessage) (agentdomain.ToolResult, error) {
	invocation, err := caseScope(ctx)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	input, err := decodeArguments[visualAssertInput](args)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	assertionID, err := parseDecimalID(input.AssertionID)
	if err != nil {
		return agentdomain.ToolResult{}, invalidArguments("assertionId must be a positive decimal string")
	}
	recorder, ok := set.recorder.(application.VisualAssertor)
	if !ok || recorder == nil {
		return agentdomain.ToolResult{}, internalError("the execution recorder does not support visual assertions")
	}
	view, err := set.caseView(ctx, invocation.CaseRunID)
	if err != nil {
		return agentdomain.ToolResult{}, err
	}
	assertion, ok := findCaseViewAssertion(view, assertionID)
	if !ok {
		return agentdomain.ToolResult{}, invalidArguments(
			fmt.Sprintf("assertion %d is not part of this case snapshot", assertionID))
	}
	if !strings.EqualFold(strings.TrimSpace(assertion.Kind), "visual") {
		return agentdomain.ToolResult{}, invalidArguments(
			fmt.Sprintf("assertion %d is not a visual assertion", assertionID))
	}

	opts := application.VisualAssertOptions{
		Baselines:        set.visualBaseline,
		Capture:          set.evidence,
		MaxBaselineBytes: set.limits.VisualBaselineMaxBytes,
		MaxArtifactBytes: set.limits.MaxArtifactBytes,
	}
	// Only an assertion with an approved baseline and fully wired baseline/
	// evidence ports needs an executor context. A missing baseline is classified
	// case-side; an adapter without the visual comparison slice is classified
	// without opening a browser.
	if assertion.Visual != nil && assertion.Visual.BaselineEvidenceID != nil &&
		*assertion.Visual.BaselineEvidenceID > 0 && set.visualBaseline != nil && set.evidence != nil {
		if comparator, supported := set.browser.(application.VisualComparator); supported {
			opts.Comparator = comparator
			if _, contextRef, contextErr := set.browserFor(ctx, invocation.CaseRunID); contextErr != nil {
				opts.ContextError = contextErr
			} else {
				opts.Context = contextRef
			}
		}
	}

	outcome, err := recorder.VisualAssert(ctx, invocation.CaseRunID, assertionID, opts)
	if err != nil {
		var browserErr *application.BrowserError
		if errors.As(err, &browserErr) {
			return agentdomain.ToolResult{}, mapBrowserError(err)
		}
		return agentdomain.ToolResult{}, mapExecutionError(err)
	}
	return jsonResult(visualAssertValue{
		AssertionID:      decimalID(outcome.AssertionID),
		Verdict:          outcome.Verdict,
		Classification:   outcome.Classification,
		FailureClass:     outcome.FailureClass,
		Reason:           outcome.Reason,
		DiffRatio:        outcome.DiffRatio,
		Threshold:        outcome.Threshold,
		EnvironmentMatch: outcome.EnvironmentMatch,
		ActualEvidenceID: optionalDecimalID(outcome.ActualEvidenceID),
		DiffEvidenceID:   optionalDecimalID(outcome.DiffEvidenceID),
		Repeated:         outcome.Repeated,
	})
}

// findCaseViewAssertion finds a snapshot assertion by id.
func findCaseViewAssertion(view application.CaseContextView, assertionID int64) (executiondomain.CaseSnapshotAssertion, bool) {
	for _, assertion := range view.CaseRun.Snapshot.Assertions {
		if assertion.ID == assertionID {
			return assertion, true
		}
	}
	return executiondomain.CaseSnapshotAssertion{}, false
}

// optionalDecimalID formats an optional positive id.
func optionalDecimalID(id int64) string {
	if id <= 0 {
		return ""
	}
	return decimalID(id)
}
