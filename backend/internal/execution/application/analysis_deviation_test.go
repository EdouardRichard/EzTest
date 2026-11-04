package executionapp

// QA Q1 reproduction: AC-FR-011-02 failed with a real SUT requirement deviation
// ("登录已过期，请重新登录" instead of the required "账号或密码错误，请重试")
// but the wave analysis classified it as automation and emitted no issue
// candidate. The fixture is copied from the QA evidence case-run detail.

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type qaQ1Fixture struct {
	CaseRun struct {
		ID              int64  `json:"id"`
		CaseVersionID   int64  `json:"case_version_id"`
		Title           string `json:"title"`
		Status          string `json:"status"`
		FailureClass    string `json:"failure_class"`
		StepTotal       int    `json:"step_total"`
		StepFailed      int    `json:"step_failed"`
		AssertionTotal  int    `json:"assertion_total"`
		AssertionFailed int    `json:"assertion_failed"`
		EvidenceCount   int    `json:"evidence_count"`
	} `json:"case_run"`
	Steps []struct {
		Seq      int            `json:"seq"`
		Status   string         `json:"status"`
		Observed map[string]any `json:"observed"`
		Error    string         `json:"error"`
	} `json:"steps"`
	Assertions []struct {
		StepSeq      int    `json:"step_seq"`
		AssertionSeq int    `json:"assertion_seq"`
		Kind         string `json:"kind"`
		Verdict      string `json:"verdict"`
		Expected     string `json:"expected"`
		Actual       string `json:"actual"`
		Reason       string `json:"reason"`
	} `json:"assertions"`
	Evidence []struct {
		ID   int64  `json:"id"`
		Kind string `json:"kind"`
	} `json:"evidence"`
}

func loadQAQ1Fixture(t *testing.T) qaQ1Fixture {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "qa-q1-ac-fr-011-02.json"))
	if err != nil {
		t.Fatalf("read QA Q1 fixture: %v", err)
	}
	var fixture qaQ1Fixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatalf("decode QA Q1 fixture: %v", err)
	}
	return fixture
}

func TestRealRequirementDeviationIsApplication(t *testing.T) {
	fixture := loadQAQ1Fixture(t)
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(fixture.CaseRun.CaseVersionID, 375970593205391360, 375970593205391361, "P0")
	f.addRun(fixture.CaseRun.ID, 1, fixture.CaseRun.CaseVersionID,
		execution.CaseRunStatus(fixture.CaseRun.Status), execution.FailureClass(fixture.CaseRun.FailureClass))
	for _, step := range fixture.Steps {
		f.results.steps[fixture.CaseRun.ID] = append(f.results.steps[fixture.CaseRun.ID], execution.StepResult{
			CaseRunID: fixture.CaseRun.ID, Seq: step.Seq,
			Status: execution.StepStatus(step.Status), Observed: step.Observed, Error: step.Error,
		})
	}
	for _, assertion := range fixture.Assertions {
		f.results.assertions[fixture.CaseRun.ID] = append(f.results.assertions[fixture.CaseRun.ID], execution.AssertionResult{
			CaseRunID: fixture.CaseRun.ID, StepSeq: assertion.StepSeq, AssertionSeq: assertion.AssertionSeq,
			Kind: assertion.Kind, Verdict: execution.AssertionVerdict(assertion.Verdict),
			Expected: assertion.Expected, Actual: assertion.Actual, Reason: assertion.Reason,
		})
	}

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	run, err := f.runs.Get(ctx, fixture.CaseRun.ID)
	if err != nil {
		t.Fatalf("get analyzed run: %v", err)
	}
	if run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("failure class = %q, want application (observed requirement deviation must not be automation)", run.FailureClass)
	}
	if run.Status != execution.CaseRunFailed || run.Summary.VerdictReason != execution.VerdictReasonStepFailed {
		t.Fatalf("run = %s/%q, want failed/%q", run.Status, run.Summary.VerdictReason, execution.VerdictReasonStepFailed)
	}
	if run.Summary.StepTotal != 2 || run.Summary.StepFailed != 1 || run.Summary.AssertionFailed != 0 {
		t.Fatalf("summary = %+v, want 2 steps / 1 failed / 0 failed assertions", run.Summary)
	}

	events := f.caseRunEvents(EventFailureCandidate)
	if len(events) != 1 {
		t.Fatalf("failure_candidate events = %d, want 1", len(events))
	}
	if events[0].Payload["failure_class"] != string(execution.FailureClassApplication) {
		t.Fatalf("candidate payload = %+v", events[0].Payload)
	}
	if hint, _ := events[0].Payload["dedupe_hint"].(string); len(hint) != 32 {
		t.Fatalf("dedupe_hint = %q", hint)
	}
	if f.team.count() != 1 {
		t.Fatalf("team finding posts = %d, want 1", f.team.count())
	}
}

func TestAutomationFailureStaysAutomation(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(301, 902, 903, "P2")
	f.addRun(501, 2, 301, execution.CaseRunFailed, execution.FailureClassAutomation)
	f.results.steps[501] = append(f.results.steps[501], execution.StepResult{
		CaseRunID: 501, Seq: 1, Status: execution.StepFailed,
		Error: "target_not_found: selector #login-submit",
	})
	// A failed step with no observation and no error has no requirement
	// evidence either: it must stay automation, never become application.
	f.addCase(303, 906, 907, "P2")
	f.addRun(503, 4, 303, execution.CaseRunFailed, "")
	f.results.steps[503] = append(f.results.steps[503], execution.StepResult{
		CaseRunID: 503, Seq: 1, Status: execution.StepFailed,
	})

	if err := f.analysis.AnalyzeWave(ctx, f.wave.ID); err != nil {
		t.Fatalf("AnalyzeWave: %v", err)
	}
	run, _ := f.runs.Get(ctx, 501)
	if run.FailureClass != execution.FailureClassAutomation {
		t.Fatalf("failure class = %q, want automation for a selector/target error", run.FailureClass)
	}
	unobserved, _ := f.runs.Get(ctx, 503)
	if unobserved.FailureClass != execution.FailureClassAutomation {
		t.Fatalf("failure class = %q, want automation for a failed step without evidence", unobserved.FailureClass)
	}
	if events := f.caseRunEvents(EventFailureCandidate); len(events) != 0 {
		t.Fatalf("failure_candidate events = %d, want 0 for automation failures", len(events))
	}
	if f.team.count() != 0 {
		t.Fatalf("team finding posts = %d, want 0 for automation failures", f.team.count())
	}
}

func TestSubmitFailureCannotDowngradeObservedDeviation(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(302, 904, 905, "P1")
	f.addRun(502, 3, 302, execution.CaseRunFailed, execution.FailureClassApplication)
	f.results.steps[502] = append(f.results.steps[502], execution.StepResult{
		CaseRunID: 502, Seq: 2, Status: execution.StepFailed,
		Observed: map[string]any{
			"summary": "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
		},
	})

	scope := tools.Scope{VersionID: f.versionID, WaveID: f.wave.ID}
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: 502, FailureClass: "automation", Category: "ui", Severity: "minor", Summary: "script bug",
	}); err == nil {
		t.Fatal("observed requirement deviation must not be downgraded to automation")
	}
	if run, _ := f.runs.Get(ctx, 502); run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("failure class after rejected downgrade = %q, want application", run.FailureClass)
	}
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: 502, FailureClass: "application", Category: "ui", Severity: "major", Summary: "login error text mismatch",
	}); err != nil {
		t.Fatalf("application submission must stay accepted: %v", err)
	}
}

func TestSubmitFailureEscalatesObservedDeviation(t *testing.T) {
	f := newAnalysisFixture()
	ctx := context.Background()
	f.addCase(304, 908, 909, "P0")
	f.addRun(504, 5, 304, execution.CaseRunFailed, execution.FailureClassAutomation)
	f.results.steps[504] = append(f.results.steps[504], execution.StepResult{
		CaseRunID: 504, Seq: 2, Status: execution.StepFailed,
		Observed: map[string]any{
			"summary": "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
		},
	})

	scope := tools.Scope{VersionID: f.versionID, WaveID: f.wave.ID}
	if _, err := f.analysis.SubmitFailure(ctx, scope, tools.FailureSubmitInput{
		CaseRunID: 504, FailureClass: "application", Category: "ui", Severity: "major",
		Summary: "login error text mismatch",
	}); err != nil {
		t.Fatalf("observed deviation escalation rejected: %v", err)
	}
	if run, _ := f.runs.Get(ctx, 504); run.FailureClass != execution.FailureClassApplication {
		t.Fatalf("failure class after escalation = %q, want application", run.FailureClass)
	}
	if events := f.caseRunEvents(EventFailureCandidate); len(events) != 1 {
		t.Fatalf("failure_candidate events = %d, want 1 after escalation", len(events))
	}
}
