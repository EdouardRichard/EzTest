package executionapp_test

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestCaseRunPersistsStepsEvidence pins QA-F1: a real execution must persist the
// step/assertion results and the evidence the agent produced. The case snapshot
// is shaped like the design data that actually killed the QA run: the step and
// assertion target_version_sut_id are empty (the P2 contract says empty inherits
// case_version.default_version_sut_id) and the logged-out step uses an
// unauthenticated account role (`anonymous`) that is not a bound test account.
func TestCaseRunPersistsStepsEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	snapshot := caseRunSnapshot()
	snapshot.Steps[0].TargetVersionSUTID = 0
	snapshot.Steps[0].AccountRole = "anonymous"
	snapshot.Assertions[0].TargetVersionSUTID = 0
	fixture.cases.snapshot = snapshot

	prepared, err := fixture.runner.Prepare(fixture.runCtx, p3WorkItem())
	if err != nil {
		t.Fatalf("Prepare must inherit the case default target instead of rejecting the case: %v", err)
	}
	if got := prepared.Case.Steps[0].TargetVersionSUTID; got != 7 {
		t.Fatalf("prepared step target = %d, want the inherited default 7", got)
	}
	if got := prepared.Case.Assertions[0].TargetVersionSUTID; got != 7 {
		t.Fatalf("prepared assertion target = %d, want the inherited default 7", got)
	}
	if _, ok := fixture.sutContext.row(7, "anonymous"); !ok {
		t.Fatalf("unauthenticated (anonymous) step context was not prepared")
	}

	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "passed", Observed: "登录表单可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	if _, err := fixture.runner.AssertionRecord(context.Background(), p3Scope(), tools.AssertionRecordInput{
		StepSeq: 1, AssertionSeq: 1, Verdict: "passed", Actual: "Login 按钮可见", VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AssertionRecord: %v", err)
	}
	if _, err := fixture.runner.AddEvidence(context.Background(), p3Scope(), executionapp.EvidenceInput{
		Kind: execution.EvidenceText, Data: []byte("snapshot: 登录按钮可见"), VersionSUTID: int64ptr(7),
	}); err != nil {
		t.Fatalf("AddEvidence: %v", err)
	}

	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{
		Verdict: execution.CaseRunPassed,
	})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.Status != execution.CaseRunPassed || finished.FailureClass != "" {
		t.Fatalf("verdict = %s/%q, want passed", finished.Status, finished.FailureClass)
	}
	if finished.Summary.StepTotal != 1 || finished.Summary.AssertionTotal != 1 ||
		finished.Summary.EvidenceCount != 1 {
		t.Fatalf("summary = %+v, want 1 step / 1 assertion / 1 evidence", finished.Summary)
	}

	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil || len(steps) != 1 || steps[0].Status != execution.StepPassed {
		t.Fatalf("persisted steps = %+v (err %v)", steps, err)
	}
	assertions, err := fixture.results.ListAssertions(context.Background(), 3001)
	if err != nil || len(assertions) != 1 || assertions[0].Verdict != execution.AssertionPassed {
		t.Fatalf("persisted assertions = %+v (err %v)", assertions, err)
	}
	evidence, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil || len(evidence) != 1 || evidence[0].Kind != execution.EvidenceText {
		t.Fatalf("persisted evidence = %+v (err %v)", evidence, err)
	}
}

// TestFinalizePersistsBlockedReasonAndEvidence pins the second half of QA-F1: a
// case that genuinely cannot start must surface a typed blocked verdict with the
// concrete reason persisted as evidence, never a silent failed/agent_failure row
// with no steps, assertions or evidence.
func TestFinalizePersistsBlockedReasonAndEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)

	runErr := &agentapp.RunError{
		Reason:    "case_blocked",
		Retryable: false,
		Cause:     platform.StateConflict("step 1 has no resolvable target version_sut"),
	}
	if err := fixture.runner.Finalize(context.Background(), 3001, 3, runErr, 1, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunBlocked || stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("finalized case_run = %s/%q, want blocked/blocked", stored.Status, stored.FailureClass)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonBlocked {
		t.Fatalf("verdict reason = %q, want blocked", stored.Summary.VerdictReason)
	}
	if stored.Summary.LastError == "" {
		t.Fatalf("the blocker reason was not persisted on the case_run")
	}
	evidence, err := fixture.evidence.ListByCaseRun(context.Background(), 3001, 50)
	if err != nil || len(evidence) != 1 {
		t.Fatalf("blocker evidence = %+v (err %v), want one text evidence row", evidence, err)
	}
	if evidence[0].Kind != execution.EvidenceText {
		t.Fatalf("blocker evidence kind = %q, want text", evidence[0].Kind)
	}
}
