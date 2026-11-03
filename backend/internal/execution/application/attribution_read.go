package executionapp

// Task 37 fix round 1: execution.result_read is the analyze.failure task result
// the failure_analyst reads. The result already carried the nine-value P3
// failure class; this file adds the deterministic four-value attribution
// (application / automation / environment / data, retry-vs-human and the
// application problem-report draft) so the analyst prompt's contract is true and
// the model never invents its own classification.

import (
	"context"
	"strings"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// maxResultReadAttributionEvidence bounds the evidence ids echoed into the
// analyst result; the analyst only needs the draft provenance, not every row.
const maxResultReadAttributionEvidence = 20

// resultFailureAttribution classifies one failure-relevant case_run. It never
// changes the persisted nine-value failure class: the four-value attribution is
// the deterministic server baseline the analyst may only refine.
func (s *CaseRunner) resultFailureAttribution(ctx context.Context, versionID int64, run execution.CaseRun, steps []execution.StepResult, assertions []execution.AssertionResult) map[string]any {
	if s == nil || s.attribution == nil || !failureRelevant(run.Status) {
		return nil
	}
	signal := exploreapp.FailureSignal{
		CaseRunID:     run.ID,
		CaseVersionID: run.CaseVersionID,
		CaseTitle:     s.failureSignalCaseTitle(ctx, versionID, run.CaseVersionID),
		FailureClass:  string(run.FailureClass),
		Category:      failureAttributionCategory(assertions),
		Reason:        failureSignalReason(run, steps, assertions),
		EvidenceIDs:   s.failureSignalEvidenceIDs(ctx, run.ID),
		Attempts:      run.Attempts,
		MaxAttempts:   execution.DefaultMaxDispatchAttempts,
	}
	result, err := s.attribution.Classify(ctx, versionID, signal)
	if err != nil {
		return nil
	}
	return failureAttributionValue(result)
}

func (s *CaseRunner) failureSignalCaseTitle(ctx context.Context, versionID, caseVersionID int64) string {
	if s.cases == nil {
		return ""
	}
	snapshot, err := s.cases.GetCaseVersion(ctx, versionID, caseVersionID)
	if err != nil {
		return ""
	}
	return snapshot.Title
}

func (s *CaseRunner) failureSignalEvidenceIDs(ctx context.Context, caseRunID int64) []int64 {
	if s.evidence == nil || caseRunID <= 0 {
		return nil
	}
	rows, err := s.evidence.ListByCaseRun(ctx, caseRunID, maxResultReadAttributionEvidence)
	if err != nil {
		return nil
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		if row.ID > 0 {
			ids = append(ids, row.ID)
		}
	}
	return ids
}

// failureSignalReason collects the observable failure evidence: the run error
// first, then the failed assertion reasons and failed/blocked step errors. The
// classifier's keyword families match against this text.
func failureSignalReason(run execution.CaseRun, steps []execution.StepResult, assertions []execution.AssertionResult) string {
	parts := make([]string, 0, 1+len(assertions)+len(steps))
	appendPart := func(value string) {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	appendPart(run.Summary.LastError)
	for _, assertion := range assertions {
		if assertion.Verdict != execution.AssertionFailed {
			continue
		}
		appendPart(assertion.Reason)
		if expected, actual := strings.TrimSpace(assertion.Expected), strings.TrimSpace(assertion.Actual); expected != "" || actual != "" {
			appendPart("expected " + expected + " got " + actual)
		}
	}
	for _, step := range steps {
		if step.Status == execution.StepFailed || step.Status == execution.StepBlocked {
			appendPart(step.Error)
		}
	}
	return strings.Join(parts, "; ")
}

// failureAttributionCategory uses the first failed assertion kind (ui/api/db)
// as the attribution category hint; without assertion evidence the classifier
// derives a category from the classification.
func failureAttributionCategory(assertions []execution.AssertionResult) string {
	for _, assertion := range assertions {
		if assertion.Verdict == execution.AssertionFailed {
			return failureCategory(assertion.Kind)
		}
	}
	return ""
}

// failureAttributionValue renders the attribution as the JSON shape the
// failure_analyst consumes from execution.result_read.
func failureAttributionValue(result exploreapp.FailureAttributionResult) map[string]any {
	value := map[string]any{
		"classification": result.Classification,
		"category":       result.Category,
		"severity":       result.Severity,
		"retryable":      result.Retryable,
		"action":         result.Action,
		"reason":         result.Reason,
		"confidence":     result.Confidence,
		"problem_draft":  nil,
	}
	if result.Problem == nil {
		return value
	}
	evidenceIDs := make([]string, 0, len(result.Problem.EvidenceIDs))
	for _, id := range result.Problem.EvidenceIDs {
		evidenceIDs = append(evidenceIDs, platformid.Format(id))
	}
	value["problem_draft"] = map[string]any{
		"title":        result.Problem.Title,
		"summary":      result.Problem.Summary,
		"category":     result.Problem.Category,
		"severity":     result.Problem.Severity,
		"dedupe_key":   result.Problem.DedupeKey,
		"evidence_ids": evidenceIDs,
	}
	return value
}
