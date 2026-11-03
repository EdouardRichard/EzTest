package executionapp

// Task 112: the issue candidate category/severity has one deterministic
// derivation, shared by the execution analyst submission and the failure
// candidate event. The severity is the case priority (execution.IssueSeverity);
// the category follows the root-cause missing-feature anchor when there is one,
// else the first failed assertion kind.

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// caseIssueAttribution resolves the canonical issue attribution of one case run.
func (s *CaseRunner) caseIssueAttribution(ctx context.Context, versionID int64, run execution.CaseRun, steps []execution.StepResult, assertions []execution.AssertionResult) (string, string) {
	priority := "P2"
	if s != nil && s.cases != nil {
		if snapshot, err := s.cases.GetCaseVersion(ctx, versionID, run.CaseVersionID); err == nil && snapshot.Priority != "" {
			priority = snapshot.Priority
		}
	}
	return canonicalIssueAttribution(priority, steps, assertions)
}

// canonicalIssueAttribution derives category and severity from the recorded
// evidence and the case priority: the same truth the P4 issue projection uses.
func canonicalIssueAttribution(priority string, steps []execution.StepResult, assertions []execution.AssertionResult) (string, string) {
	severity := execution.IssueSeverity(priority)
	var firstFailed *execution.AssertionResult
	for index := range assertions {
		if assertions[index].Verdict == execution.AssertionFailed {
			firstFailed = &assertions[index]
			break
		}
	}
	if anchor := execution.MissingFeatureAnchor(steps, firstFailedStepSeq(firstFailed)); anchor != nil {
		return "ui", severity
	}
	if firstFailed != nil {
		return failureCategory(firstFailed.Kind), severity
	}
	return "ui", severity
}
