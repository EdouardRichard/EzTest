package executionapp_test

// QA Q1: once the deterministic verdict classifies an observed requirement
// deviation as failed(application), the failure analyst must not be able to
// re-suppress it as automation through analysis.failure_submit.

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestFailureSubmitCannotDowngradeObservedDeviation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "failed",
		Observed: "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	finished, err := fixture.runner.Finish(context.Background(), p3Scope(), execution.FinishCandidate{Verdict: execution.CaseRunFailed})
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if finished.FailureClass != execution.FailureClassApplication {
		t.Fatalf("finish class = %q, want application", finished.FailureClass)
	}

	if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
		CaseRunID: 3001, FailureClass: "automation", Category: "ui", Severity: "minor", Summary: "script bug",
	}); err == nil {
		t.Fatal("observed requirement deviation must not be downgraded to automation")
	}
	if run := fixture.caseRuns.get(t, 3001); run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("failure class after rejected downgrade = %q, want application", run.FailureClass)
	}
}

// QA Q1 recovery path: a run that was already persisted as automation (e.g. by
// an older deployment or a missed fallback) can still be triaged to application
// when the stored executor observation is a requirement deviation. Without
// recorded observed evidence the escalation stays rejected.
func TestFailureSubmitEscalatesObservedDeviation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "failed",
		Observed: "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	fixture.caseRuns.mu.Lock()
	run := fixture.caseRuns.rows[3001]
	run.Status = execution.CaseRunFailed
	run.FailureClass = execution.FailureClassAutomation
	fixture.caseRuns.rows[3001] = run
	fixture.caseRuns.mu.Unlock()

	if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
		CaseRunID: 3001, FailureClass: "application", Category: "ui", Severity: "major", Summary: "login error text mismatch",
	}); err != nil {
		t.Fatalf("observed deviation escalation rejected: %v", err)
	}
	if run := fixture.caseRuns.get(t, 3001); run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("failure class after escalation = %q, want application", run.FailureClass)
	}
}
