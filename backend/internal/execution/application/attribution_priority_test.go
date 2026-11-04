package executionapp_test

// Task 112: execution.result_read feeds the failure analyst. Its attribution
// severity must agree with the issue projection (case priority), so a P2 API
// failure is minor in both places.

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestResultReadAttributionSeverityMatchesCasePriority(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.cases.snapshot.Priority = "P2"
	const caseRunID = int64(3011)
	fixture.caseRuns.rows[caseRunID] = execution.CaseRun{
		ID: caseRunID, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
	}
	fixture.results.assertions[11] = execution.AssertionResult{
		ID: 11, CaseRunID: caseRunID, StepSeq: 2, AssertionSeq: 1, Kind: execution.AssertionKindAPI,
		Verdict: execution.AssertionFailed, Expected: "201", Actual: "500", Reason: "publish API missing",
	}

	value, err := fixture.runner.ResultRead(context.Background(), p3Scope(), tools.ResultReadInput{})
	if err != nil {
		t.Fatalf("ResultRead: %v", err)
	}
	runs := resultReadCaseRuns(t, value)
	run, ok := runs["3011"]
	if !ok {
		t.Fatalf("case_run 3011 missing from result_read: %+v", runs)
	}
	attribution, ok := run["attribution"].(map[string]any)
	if !ok {
		t.Fatalf("result_read has no attribution: %+v", run)
	}
	if attribution["severity"] != "minor" {
		t.Fatalf("attribution severity = %v, want minor for P2", attribution["severity"])
	}
	problem, ok := attribution["problem_draft"].(map[string]any)
	if !ok {
		t.Fatalf("result_read has no problem draft: %+v", attribution)
	}
	if problem["severity"] != "minor" {
		t.Fatalf("problem draft severity = %v, want minor for P2", problem["severity"])
	}
}
