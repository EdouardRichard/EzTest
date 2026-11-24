package executionapp_test

import (
	"context"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestFinishNoEvidenceDegradesFailed proves V-0029 on the settlement path: a
// failed assertion with no evidence row settles inconclusive (constitution
// art.1.2), never a failed defect nobody can inspect. The judgment is the
// domain Aggregate's; the runner only supplies the live evidence count.
func TestFinishNoEvidenceDegradesFailed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "Login 按钮不存在",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: a failed verdict with no evidence degrades", finished.Status)
	}
	if finished.FailureClass != "" {
		t.Fatalf("failure class = %q, want empty (a degraded run must not create an issue)", finished.FailureClass)
	}
	if finished.Summary.VerdictReason != execution.VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want %q", finished.Summary.VerdictReason, execution.VerdictReasonNoEvidence)
	}
}

// TestEvidenceCountFillErrorNotSwallowed proves a count read failure is surfaced,
// not silently turned into a wrong verdict (constitution art.7.6): the terminal
// write aborts with a retryable error and no fabricated terminal status is
// persisted. A wrong count could let a no-evidence failed stand or degrade a real
// defect, so the aggregation refuses to guess.
func TestEvidenceCountFillErrorNotSwallowed(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), executionScope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "ok",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), executionScope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "failed", Actual: "mismatch",
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.EvidenceText(context.Background(), executionScope(), tools.EvidenceTextInput{
		Kind: "text", Text: "failure evidence",
	}); err != nil {
		t.Fatalf("EvidenceText: %v", err)
	}
	// The evidence count read fails after the results/evidence are recorded.
	fixture.evidence.mu.Lock()
	fixture.evidence.countErr = errors.New("count unavailable")
	fixture.evidence.mu.Unlock()

	_, err := fixture.runner.Finish(context.Background(), executionScope(), execution.FinishCandidate{
		Verdict: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
	})
	if err == nil {
		t.Fatal("a count read failure must surface, not silently produce a verdict")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "unavailable" {
		t.Fatalf("error = %v, want a retryable unavailable error", err)
	}
	if run := fixture.caseRuns.get(t, 3001); run.Status == execution.CaseRunFailed || run.Status == execution.CaseRunPassed {
		t.Fatalf("run settled to %q despite the count failure", run.Status)
	}
}
