package executionapp_test

// Task 37 fix round 1: execution.result_read is the analyze.failure task result
// the failure_analyst reads. It already carried the nine-value P3 failure class;
// this test pins the deterministic four-value attribution (application /
// automation / environment / data plus retry-vs-human and the application
// problem draft) so the failure_analyst prompt's "server supplies the
// attribution" contract is actually true.

import (
	"context"
	"encoding/json"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func resultReadCaseRuns(t *testing.T, value any) map[string]map[string]any {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal result_read: %v", err)
	}
	var payload struct {
		CaseRuns []map[string]any `json:"case_runs"`
	}
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("decode result_read: %v", err)
	}
	runs := make(map[string]map[string]any, len(payload.CaseRuns))
	for _, run := range payload.CaseRuns {
		id, _ := run["case_run_id"].(string)
		runs[id] = run
	}
	return runs
}

func TestFailureAttributionInResultRead(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	fixture.caseRuns.rows[3001] = execution.CaseRun{
		ID: 3001, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassApplication,
		Attempts: 2,
		Summary:  execution.CaseRunSummary{LastError: "expected button text 提交订单 got 提交"},
	}
	fixture.caseRuns.rows[3002] = execution.CaseRun{
		ID: 3002, WaveID: 2001, IntentID: 1001, CaseVersionID: 41,
		Status: execution.CaseRunFailed, FailureClass: execution.FailureClassEnvironment,
		Attempts: 1,
		Summary:  execution.CaseRunSummary{LastError: "net::ERR_CONNECTION_TIMED_OUT while opening SUT"},
	}
	fixture.results.assertions[1] = execution.AssertionResult{
		ID: 1, CaseRunID: 3001, StepSeq: 1, AssertionSeq: 1, Kind: "ui",
		Verdict: execution.AssertionFailed, Expected: "提交订单", Actual: "提交", Reason: "按钮文案不符",
	}
	fixture.evidence.rows = append(fixture.evidence.rows, execution.Evidence{
		ID: 9001, CaseRunID: 3001, Kind: execution.EvidenceScreenshot, MIME: "image/png",
	})

	value, err := fixture.runner.ResultRead(context.Background(), p3Scope(), tools.ResultReadInput{})
	if err != nil {
		t.Fatalf("ResultRead: %v", err)
	}
	runs := resultReadCaseRuns(t, value)
	application, ok := runs["3001"]
	if !ok {
		t.Fatalf("case_run 3001 missing from result_read: %+v", runs)
	}
	if application["failure_class"] != "application" {
		t.Fatalf("nine-value class = %v", application["failure_class"])
	}
	attribution, ok := application["attribution"].(map[string]any)
	if !ok {
		t.Fatalf("result_read has no deterministic attribution: %+v", application)
	}
	if attribution["classification"] != exploreapp.FailureClassificationApplication ||
		attribution["action"] != exploreapp.FailureActionHuman || attribution["retryable"] != false {
		t.Fatalf("application attribution = %+v", attribution)
	}
	problem, ok := attribution["problem_draft"].(map[string]any)
	if !ok {
		t.Fatalf("application attribution has no problem draft: %+v", attribution)
	}
	if problem["dedupe_key"] == "" || problem["title"] == "" {
		t.Fatalf("problem draft = %+v", problem)
	}
	evidenceIDs, _ := problem["evidence_ids"].([]any)
	if len(evidenceIDs) != 1 || evidenceIDs[0] != "9001" {
		t.Fatalf("problem draft evidence = %v", problem["evidence_ids"])
	}

	environment, ok := runs["3002"]
	if !ok {
		t.Fatalf("case_run 3002 missing from result_read: %+v", runs)
	}
	environmentAttribution, ok := environment["attribution"].(map[string]any)
	if !ok {
		t.Fatalf("environment result_read has no attribution: %+v", environment)
	}
	if environmentAttribution["classification"] != exploreapp.FailureClassificationEnvironment ||
		environmentAttribution["action"] != exploreapp.FailureActionRetry ||
		environmentAttribution["retryable"] != true {
		t.Fatalf("environment attribution = %+v", environmentAttribution)
	}
	if environmentAttribution["problem_draft"] != nil {
		t.Fatalf("environment failure produced a problem draft: %+v", environmentAttribution)
	}
}
