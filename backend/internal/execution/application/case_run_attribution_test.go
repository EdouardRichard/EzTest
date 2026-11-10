package executionapp_test

// v0.2.3 Task 11 (X-01 API side): executor failures must be typed instead of
// being flattened into executor_lost, and a retryable Finalize must release the
// lease immediately so the next dispatch happens in seconds, not after 90s.

import (
	"context"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func executionError(code string, details map[string]any) *platform.Error {
	err := platform.NewError(504, code, code+": executor request failed")
	err.Retryable = true
	err.Details = details
	return err
}

// TestTransportTimeoutNotExecutorLost: an executor_timeout is a timeout, never
// an executor_lost. The terminal row carries the stable executor cause so the UI
// can show 执行超时 instead of 执行器失联.
func TestTransportTimeoutNotExecutorLost(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cause := executionError("executor_timeout", map[string]any{
		"phase": "request", "method": "POST", "path": "/v1/contexts/ctx_1/actions",
	})
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: cause}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.Status != execution.CaseRunFailed {
		t.Fatalf("status = %q, want failed", stored.Status)
	}
	if stored.FailureClass != execution.FailureClassTimeout {
		t.Fatalf("failure class = %q, want timeout (not executor_lost)", stored.FailureClass)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseExecutorTimeout {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseExecutorTimeout)
	}
	if strings.Contains(stored.Summary.LastError, "executor_lost") {
		t.Fatalf("last error = %q, must keep the real transport failure", stored.Summary.LastError)
	}
	if stored.Summary.VerdictReason != execution.VerdictReasonAgentFailure {
		t.Fatalf("verdict reason = %q, want agent_failure", stored.Summary.VerdictReason)
	}
}

// TestContextCreateTimeoutCause: a timeout on POST /v1/contexts gets the stable
// executor_context_create_failed cause (the browser context is lazily created,
// so this is the pre-first-action failure the UAT hit).
func TestContextCreateTimeoutCause(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cause := executionError("executor_timeout", map[string]any{
		"phase": "request", "method": "POST", "path": "/v1/contexts",
	})
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: cause}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.FailureClass != execution.FailureClassTimeout {
		t.Fatalf("failure class = %q, want timeout", stored.FailureClass)
	}
	if stored.Summary.TerminalCause != execution.TerminalCauseExecutorContextCreateFailed {
		t.Fatalf("terminal cause = %q, want %q", stored.Summary.TerminalCause, execution.TerminalCauseExecutorContextCreateFailed)
	}
}

// TestExecutorUnavailableWithoutDialIsEnvironment: only a refused dial (or an
// offline/heartbeat-expired executor) is executor_lost; a 5xx/protocol failure
// from a reachable executor is an environment failure.
func TestExecutorUnavailableWithoutDialIsEnvironment(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	cause := executionError("executor_unavailable", map[string]any{
		"phase": "response", "method": "POST", "path": "/v1/contexts/ctx_1/actions", "status_code": 503,
	})
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: cause}
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 3, 3); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	stored := fixture.caseRuns.get(t, 3001)
	if stored.FailureClass != execution.FailureClassEnvironment {
		t.Fatalf("failure class = %q, want environment", stored.FailureClass)
	}
	if stored.Summary.TerminalCause == execution.TerminalCauseExecutorContextCreateFailed {
		t.Fatalf("response failure was typed as a context-create failure: %+v", stored.Summary)
	}
}

// TestFinalizeRetryReleasesLease: a retryable run error releases the lease and
// resets the row to pending under the dispatch CAS fence, so the scheduler can
// redispatch on its next tick (seconds) instead of waiting for the 90s lease.
func TestFinalizeRetryReleasesLease(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	heartbeat := fixture.clock.Now().Add(-time.Second)
	executor := &execution.ExecutorInstance{
		ID: 7, Endpoint: "http://127.0.0.1:13336", Capacity: 8,
		Status: execution.ExecutorActive, LastHeartbeat: &heartbeat,
	}
	scheduler, queue := newDispatchScheduler(fixture, executor)
	prepareCaseRun(t, fixture)

	cause := executionError("executor_unavailable", map[string]any{
		"phase": "response", "method": "POST", "path": "/v1/contexts", "status_code": 503,
	})
	runErr := &agentapp.RunError{Reason: "tool_failure", Retryable: true, Cause: cause}
	before := fixture.caseRuns.get(t, 3001)
	if err := fixture.runner.Finalize(fixture.runCtx, 3001, 3, agentapp.RunResult{}, runErr, 1, 3); err != nil {
		t.Fatalf("Finalize retry: %v", err)
	}

	retried := fixture.caseRuns.get(t, 3001)
	if retried.Status != execution.CaseRunPending {
		t.Fatalf("retry status = %q, want pending", retried.Status)
	}
	if retried.LeaseUntil != nil {
		t.Fatalf("retry kept the lease until %v; scheduler redispatch must not wait for it", retried.LeaseUntil)
	}
	if retried.DispatchEpoch != before.DispatchEpoch+1 || retried.DispatchToken != "" {
		t.Fatalf("retry fence = epoch %d token %q, want epoch %d and an empty token", retried.DispatchEpoch, retried.DispatchToken, before.DispatchEpoch+1)
	}
	if retried.FailureClass != "" {
		t.Fatalf("non-terminal retry wrote failure class %q", retried.FailureClass)
	}
	if retried.Summary.VerdictReason != "" {
		t.Fatalf("non-terminal retry wrote verdict reason %q", retried.Summary.VerdictReason)
	}
	if !strings.Contains(retried.Summary.LastError, "executor_unavailable") {
		t.Fatalf("last error = %q, want the real transport failure", retried.Summary.LastError)
	}
	if retried.Summary.RetryReason != execution.TerminalCauseExecutorContextCreateFailed {
		t.Fatalf("retry reason = %q, want %q", retried.Summary.RetryReason, execution.TerminalCauseExecutorContextCreateFailed)
	}

	// No clock advance: the scheduler tick that follows immediately redispatches
	// the released pending run.
	if err := scheduler.ReconcileVersion(context.Background(), 10); err != nil {
		t.Fatalf("ReconcileVersion: %v", err)
	}
	dispatched := fixture.caseRuns.get(t, 3001)
	if dispatched.Status != execution.CaseRunRunning || dispatched.DispatchEpoch != retried.DispatchEpoch+1 {
		t.Fatalf("run was not redispatched immediately: %+v", dispatched)
	}
	if queue.count() != 1 {
		t.Fatalf("work items = %d, want 1 immediate redispatch", queue.count())
	}
}

// TestStepRecordPersistsExecutorLimitation: the executor capability code carried
// by a step tool error is persisted as a structured observed marker, not as
// prose the analysis would have to guess at.
func TestStepRecordPersistsExecutorLimitation(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.StepRecord(context.Background(), p3Scope(), tools.StepRecordInput{
		Seq: 1, Status: "failed",
		Observed: "无法选择本地文件，上传动作未能发起",
		Error:    "file_chooser_unavailable: the executor cannot drive the native file chooser",
	}); err != nil {
		t.Fatalf("StepRecord: %v", err)
	}
	steps, err := fixture.results.ListSteps(context.Background(), 3001)
	if err != nil || len(steps) != 1 {
		t.Fatalf("steps = %+v, err = %v", steps, err)
	}
	code, ok := execution.StepExecutorLimitation(steps[0])
	if !ok || code != "file_chooser_unavailable" {
		t.Fatalf("step observed = %+v, want structured executor_limitation file_chooser_unavailable", steps[0].Observed)
	}
}
