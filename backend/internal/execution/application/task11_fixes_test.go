package executionapp_test

// v0.2.3 Task 11 review fixes: the executor-capability no-escalation guard must
// run on the production CaseRunner.FailureSubmit path, the marker producer must
// be sourced from the structured browser-tool error code (closed allowlist), and
// the Finalize retry CAS must never clobber a run paused by a human.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// executorLimitationStep returns one marked step as the executor records it.
func executorLimitationStep(seq int, code string) execution.StepResult {
	return execution.StepResult{
		CaseRunID: 3001,
		Seq:       seq,
		Status:    execution.StepBlocked,
		Observed: map[string]any{
			execution.ExecutorLimitationKey: map[string]any{"code": code},
		},
	}
}

// seedRunState mutates the stored case_run under the fake repository lock.
func seedRunState(fixture *caseRunnerFixture, status execution.CaseRunStatus, class execution.FailureClass) {
	fixture.caseRuns.mu.Lock()
	defer fixture.caseRuns.mu.Unlock()
	run := fixture.caseRuns.rows[3001]
	run.Status = status
	run.FailureClass = class
	fixture.caseRuns.rows[3001] = run
}

// TestFailureSubmitRejectsExecutorCapabilityEscalation is the Critical review
// finding: the no-escalation guard lives on AnalysisService.SubmitFailure, but
// production wires the analyst tool to *CaseRunner. A marked run (blocked with
// another unmarked deviation, or already stored failed(application)) must be
// rejected by runner.FailureSubmit and must emit no failure candidate.
func TestFailureSubmitRejectsExecutorCapabilityEscalation(t *testing.T) {
	t.Run("blocked marked run with an unmarked deviation", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		fixture.results.steps[1] = executorLimitationStep(1, "file_chooser_unavailable")
		fixture.results.steps[2] = execution.StepResult{
			CaseRunID: 3001, Seq: 2, Status: execution.StepFailed,
			Observed: map[string]any{"summary": "编辑页没有「发布时间」控件，无法设置定时发布"},
		}
		seedRunState(fixture, execution.CaseRunBlocked, execution.FailureClassBlocked)

		if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
			CaseRunID: 3001, FailureClass: "application", Summary: "product defect",
		}); err == nil {
			t.Fatal("marked blocked run was escalated to application through runner.FailureSubmit")
		} else if apiErr := platformError(t, err); apiErr.Code != "state_conflict" {
			t.Fatalf("error code = %q, want state_conflict", apiErr.Code)
		}
		if stored := fixture.caseRuns.get(t, 3001); stored.FailureClass != execution.FailureClassBlocked {
			t.Fatalf("failure class = %q, want blocked", stored.FailureClass)
		}
		for _, eventType := range fixture.events.types() {
			if eventType == executionapp.EventFailureCandidate {
				t.Fatal("rejected escalation still emitted execution.failure_candidate")
			}
		}
		if len(fixture.room.posts) != 0 {
			t.Fatalf("rejected escalation posted %d team-room finding(s)", len(fixture.room.posts))
		}
	})

	t.Run("already failed(application) marked run", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		fixture.results.steps[1] = executorLimitationStep(1, "hover_unavailable")
		seedRunState(fixture, execution.CaseRunFailed, execution.FailureClassApplication)

		if _, err := fixture.runner.FailureSubmit(context.Background(), p3Scope(), tools.FailureSubmitInput{
			CaseRunID: 3001, FailureClass: "application", Summary: "product defect",
		}); err == nil {
			t.Fatal("marked failed(application) run bypassed the executor capability guard")
		}
		for _, eventType := range fixture.events.types() {
			if eventType == executionapp.EventFailureCandidate {
				t.Fatal("rejected escalation still emitted execution.failure_candidate")
			}
		}
	})
}

// TestBrowserCapabilityErrorMarksRecordedStep is the real producer path: the
// structured executor error code returned by the browser tool is remembered and
// attached to the next failed/blocked step record, so the marker does not depend
// on the model copying a code into free text.
func TestBrowserCapabilityErrorMarksRecordedStep(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.browser.result = executionapp.BrowserActionResult{
		OK: false,
		Error: &executionapp.BrowserActionError{
			Code: "file_chooser_unavailable", Message: "the executor cannot drive the native file chooser",
		},
	}
	args := json.RawMessage(`{"target":"#upload","files":["/tmp/a.pdf"]}`)
	if _, err := fixture.runner.Browser(context.Background(), p3Scope(), "upload_file", args); err == nil {
		t.Fatal("Browser(capability error) returned no error")
	}
	// The model only records prose; the structured origin must supply the code.
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "blocked",
		Observed: "无法选择本地文件，上传动作未能发起",
		Error:    "the browser action could not be performed",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps = %+v, err = %v", steps, err)
	}
	code, ok := execution.StepExecutorLimitation(steps[0])
	if !ok || code != "file_chooser_unavailable" {
		t.Fatalf("step observed = %+v, want the structured file_chooser_unavailable marker", steps[0].Observed)
	}
}

// TestServiceUnavailableErrorIsNotExecutorLimitation is the negative SUT-error
// case: a closed capability allowlist never turns a real service/endpoint
// failure (or the product observation next to it) into an executor block.
func TestServiceUnavailableErrorIsNotExecutorLimitation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.browser.result = executionapp.BrowserActionResult{
		OK: false,
		Error: &executionapp.BrowserActionError{
			Code: "service_unavailable", Message: "the SUT service is unavailable",
		},
	}
	args := json.RawMessage(`{"target":"#upload","files":["/tmp/a.pdf"]}`)
	if _, err := fixture.runner.Browser(context.Background(), p3Scope(), "upload_file", args); err == nil {
		t.Fatal("Browser(service error) returned no error")
	}
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "failed",
		Observed: "页面上没有「上传」按钮",
		Error:    "service_unavailable: the SUT returned 503",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps = %+v, err = %v", steps, err)
	}
	if code, ok := execution.StepExecutorLimitation(steps[0]); ok {
		t.Fatalf("service_unavailable was persisted as an executor limitation (%q)", code)
	}
	if !execution.StepShowsApplicationDeviation(steps[0]) {
		t.Fatal("a real SUT error/observation was suppressed as an executor limitation")
	}
}

// TestFinalizeRetryCASDoesNotClobberPausedRun reproduces the human-pause race:
// PauseCaseRun moves running->paused without bumping the dispatch epoch/token, so
// the retry CAS must refuse to reset the row to pending once it is no longer
// running. beforeUpdateCAS flips the stored row between the read and the CAS.
func TestFinalizeRetryCASDoesNotClobberPausedRun(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	before := fixture.caseRuns.get(t, 3001)
	fixture.caseRuns.mu.Lock()
	fixture.caseRuns.beforeUpdateCAS = func() {
		paused := fixture.caseRuns.rows[3001]
		paused.Status = execution.CaseRunPaused
		fixture.caseRuns.rows[3001] = paused
	}
	fixture.caseRuns.mu.Unlock()

	runErr := &agentapp.RunError{
		Reason:    "tool_failure",
		Retryable: true,
		Cause:     executionError("executor_timeout", map[string]any{"phase": "request", "method": "POST", "path": "/v1/contexts"}),
	}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 1, 3); err != nil {
		t.Fatalf("Finalize retry over a paused row returned %v, want a benign no-op", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunPaused {
		t.Fatalf("status = %q, the retry CAS clobbered the human pause", stored.Status)
	}
	if stored.FailureClass != "" || stored.Summary.VerdictReason != "" || stored.Summary.RetryReason != "" {
		t.Fatalf("paused row was written with terminal/retry semantics: %+v", stored.Summary)
	}
	if stored.DispatchEpoch != before.DispatchEpoch {
		t.Fatalf("dispatch epoch = %d, want %d (paused owner must keep the fence)", stored.DispatchEpoch, before.DispatchEpoch)
	}
}

// TestDispatchClearsStaleRetryReason: recovery must not type a later lease
// expiry from a retry reason recorded by a previous attempt, so a redispatch
// starts with no stale retry reason / last error.
func TestDispatchClearsStaleRetryReason(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	heartbeat := fixture.clock.Now().Add(-time.Second)
	executor := &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &heartbeat,
	}
	scheduler, queue := newDispatchScheduler(fixture, executor)
	fixture.caseRuns.mu.Lock()
	run := fixture.caseRuns.rows[3001]
	run.Summary.RetryReason = execution.TerminalCauseExecutorTimeout
	run.Summary.LastError = "executor_timeout: executor request timed out while creating the browser context"
	fixture.caseRuns.rows[3001] = run
	fixture.caseRuns.mu.Unlock()

	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	dispatched := fixture.caseRuns.get(t, 3001)
	if dispatched.Status != execution.CaseRunRunning {
		t.Fatalf("status = %q, want running", dispatched.Status)
	}
	if dispatched.Summary.RetryReason != "" {
		t.Fatalf("dispatch kept stale retry reason %q", dispatched.Summary.RetryReason)
	}
	if dispatched.Summary.LastError != "" {
		t.Fatalf("dispatch kept stale last error %q", dispatched.Summary.LastError)
	}
	if queue.count() != 1 {
		t.Fatalf("work items = %d, want 1", queue.count())
	}
}

// TestContextCreateCauseRequiresPost: /v1/contexts is only the lazy browser
// context creation on POST; a GET (context read) timeout is not a context-create
// failure.
func TestContextCreateCauseRequiresPost(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cause := executionError("executor_timeout", map[string]any{
		"phase": "request", "method": "GET", "path": "/v1/contexts",
	})
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: cause}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Summary.TerminalCause != execution.TerminalCauseExecutorTimeout {
		t.Fatalf("terminal cause = %q, want %q (GET is not context creation)", stored.Summary.TerminalCause, execution.TerminalCauseExecutorTimeout)
	}
}

// TestRunErrorClassificationPrefersTypedCause: a typed run error (budget, model,
// tool) wins over an already-expired caller context, so a run-level budget
// deadline is not stamped as an executor timeout.
func TestRunErrorClassificationPrefersTypedCause(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	ctx, cancel := context.WithDeadline(context.Background(), fixture.clock.Now().Add(-time.Minute))
	defer cancel()
	runErr := &agentapp.RunError{Reason: "budget", Retryable: false}
	if err := fixture.runner.Finalize(ctx, 3001, 3, agentapp.RunResult{}, runErr, 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.FailureClass != execution.FailureClassModel {
		t.Fatalf("failure class = %q, want model (typed run error before ctx deadline)", stored.FailureClass)
	}
}

// TestRunningRowHasNoVerdictReason (brief-named): the Finalize retry branch
// leaves a running->pending row with no terminal verdict reason, even when the
// row carried a legacy value before the retry.
func TestRunningRowHasNoVerdictReason(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.caseRuns.mu.Lock()
	run := fixture.caseRuns.rows[3001]
	run.Summary.VerdictReason = execution.VerdictReasonAgentFailure
	run.FailureClass = execution.FailureClassExecutorLost
	fixture.caseRuns.rows[3001] = run
	fixture.caseRuns.mu.Unlock()

	runErr := &agentapp.RunError{
		Reason:    "tool_failure",
		Retryable: true,
		Cause:     executionError("executor_timeout", map[string]any{"phase": "request", "method": "POST", "path": "/v1/contexts"}),
	}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 1, 3); err != nil {
		t.Fatalf("Finalize retry: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunPending {
		t.Fatalf("status = %q, want pending", stored.Status)
	}
	if stored.Summary.VerdictReason != "" || stored.FailureClass != "" {
		t.Fatalf("non-terminal row carries verdict semantics: class=%q verdict_reason=%q", stored.FailureClass, stored.Summary.VerdictReason)
	}
}
