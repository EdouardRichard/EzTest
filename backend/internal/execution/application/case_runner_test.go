package application

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// CancelCase records the per-case cancel the runner routes through the tools'
// context registry. The scheduler fixtures keep asserting one cancel per
// immediate pause while the runner stops creating its own context.
func (b *schedBrowser) CancelCase(ctx context.Context, caseRunID int64) error {
	return b.Cancel(ctx, ContextRef{ID: fmt.Sprintf("case:%d", caseRunID)})
}

// CloseCase records the per-case close the runner routes through the tools'
// context registry.
func (b *schedBrowser) CloseCase(ctx context.Context, caseRunID int64) error {
	return b.Close(ctx, ContextRef{ID: fmt.Sprintf("case:%d", caseRunID)})
}

// newCaseRunnerHarness wires a CaseRunner over the scheduler fakes.
func newCaseRunnerHarness(t *testing.T) (*CaseRunner, *schedWorld) {
	t.Helper()
	world := newSchedWorld()
	runner := NewCaseRunner(CaseRunnerDeps{
		CaseRuns:     world.runs,
		Steps:        world.steps,
		Batches:      world.batches,
		Sessions:     world.sessions,
		Runtime:      world.runtime,
		Mindmap:      world.mindmap,
		Events:       world.events,
		Clock:        world.clock,
		Browser:      world.browser,
		Signals:      world.browser,
		Timeout:      time.Second,
		PollInterval: time.Millisecond,
	})
	return runner, world
}

// runningCaseRun seeds one pending run and moves it to running, the state the
// scheduler hands to the runner.
func runningCaseRun(t *testing.T, world *schedWorld) domain.CaseRun {
	t.Helper()
	seeded := world.seedPending(1, "P0")
	if _, err := world.runs.SetStatus(context.Background(), seeded.ID,
		domain.CaseRunStatusPending, domain.CaseRunStatusRunning, testNow); err != nil {
		t.Fatalf("start case run %d: %v", seeded.ID, err)
	}
	return world.run(t, seeded.ID)
}

// TestImmediatePauseCancelsToolOwnedCaseContext pins that the runner cancels
// through the per-case controller (the tools' context registry) and never opens
// an executor context of its own.
func TestImmediatePauseCancelsToolOwnedCaseContext(t *testing.T) {
	runner, world := newCaseRunnerHarness(t)
	run := runningCaseRun(t, world)
	world.runtime.setState(AgentRunState{Status: "running"})

	done := make(chan struct{})
	go func() {
		defer close(done)
		runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)
	}()
	world.waitStarted(t, 1)
	world.batches.setPaused(schedBatchID, true, domain.PauseModeImmediate)

	waitFor(t, "immediate pause to interrupt the run", func() bool {
		return world.run(t, 1).Status == domain.CaseRunStatusInterrupted
	})
	<-done

	if contexts := world.browser.contextSpecs(); len(contexts) != 0 {
		t.Fatalf("case runner created %d executor contexts; the tools own the context", len(contexts))
	}
	if got := world.browser.canceledIDs(); !reflect.DeepEqual(got, []string{"case:1"}) {
		t.Fatalf("CancelCase calls = %v, want [case:1]", got)
	}
	if got := world.browser.closedIDs(); !reflect.DeepEqual(got, []string{"case:1"}) {
		t.Fatalf("CloseCase calls = %v, want [case:1]", got)
	}
}

// TestTeardownClosesToolOwnedCaseContext pins that every exit path releases the
// tools' context through CloseCase, again without the runner creating one.
func TestTeardownClosesToolOwnedCaseContext(t *testing.T) {
	runner, world := newCaseRunnerHarness(t)
	run := runningCaseRun(t, world)
	world.runtime.setState(AgentRunState{Status: "done"})

	runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

	stored := world.run(t, 1)
	if stored.Status != domain.CaseRunStatusInconclusive {
		t.Fatalf("status = %s, want inconclusive", stored.Status)
	}
	if contexts := world.browser.contextSpecs(); len(contexts) != 0 {
		t.Fatalf("case runner created %d executor contexts; the tools own the context", len(contexts))
	}
	if got := world.browser.closedIDs(); !reflect.DeepEqual(got, []string{"case:1"}) {
		t.Fatalf("CloseCase calls = %v, want [case:1]", got)
	}
	if got := world.browser.canceledIDs(); len(got) != 0 {
		t.Fatalf("CancelCase calls = %v, want none", got)
	}
}

// TestCaseRunnerHonorsPauseWhenAgentEnds pins the pause/terminal race: a pause
// that cancelled or ended the agent run must be honored before the terminal
// state is classified. Otherwise the case run is classified inconclusive even
// though the user asked for a graceful park or an immediate interrupt.
func TestCaseRunnerHonorsPauseWhenAgentEnds(t *testing.T) {
	cases := []struct {
		name        string
		state       AgentRunState
		mode        domain.PauseMode
		wantStatus  domain.CaseRunStatus
		wantCleanup bool
		wantStepSeq *int
	}{
		{
			name:       "graceful parks an aborted agent run",
			state:      AgentRunState{Status: "aborted", Error: "canceled by pause", TerminalReason: "shutdown"},
			mode:       domain.PauseModeGraceful,
			wantStatus: domain.CaseRunStatusPending,
		},
		{
			name:       "graceful parks a terminal run without case_finish",
			state:      AgentRunState{Status: "done", TerminalReason: "completed"},
			mode:       domain.PauseModeGraceful,
			wantStatus: domain.CaseRunStatusPending,
		},
		{
			name:        "immediate interrupts an aborted agent run",
			state:       AgentRunState{Status: "canceled", TerminalReason: "shutdown"},
			mode:        domain.PauseModeImmediate,
			wantStatus:  domain.CaseRunStatusInterrupted,
			wantCleanup: true,
			wantStepSeq: intPointer(2),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, world := newCaseRunnerHarness(t)
			run := runningCaseRun(t, world)
			world.steps.seed(domain.StepResult{
				ID: 11, BatchID: run.BatchID, CaseRunID: run.ID, Seq: 1,
				Status: domain.StepStatusPassed,
			})
			world.steps.seed(domain.StepResult{
				ID: 12, BatchID: run.BatchID, CaseRunID: run.ID, Seq: 2,
				Status: domain.StepStatusRunning,
			})
			world.runtime.setState(tc.state)
			world.batches.setPaused(run.BatchID, true, tc.mode)

			runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

			stored := world.run(t, run.ID)
			if stored.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (class=%v reason=%q)",
					stored.Status, tc.wantStatus, stored.FailureClass, stored.FailureReason)
			}
			if stored.Attempt != run.Attempt {
				t.Fatalf("attempt = %d, want %d unchanged", stored.Attempt, run.Attempt)
			}
			if stored.FailureClass != nil {
				t.Fatalf("failure class = %s, want no classification", *stored.FailureClass)
			}
			if tc.wantStatus == domain.CaseRunStatusPending &&
				(stored.CleanupRequired || stored.InterruptedStepSeq != nil || stored.FinishedAt != nil) {
				t.Fatalf("a graceful park must keep the checkpoint clean: %+v", stored)
			}
			if stored.CleanupRequired != tc.wantCleanup {
				t.Fatalf("cleanup_required = %v, want %v", stored.CleanupRequired, tc.wantCleanup)
			}
			if !reflect.DeepEqual(stored.InterruptedStepSeq, tc.wantStepSeq) {
				t.Fatalf("interrupted_step_seq = %v, want %v",
					stored.InterruptedStepSeq, tc.wantStepSeq)
			}
		})
	}
}

// TestCaseRunnerHonorsPauseWhenAgentReadFails pins the error terminal path: a
// runtime read failure that races a pause must park the run, not classify it as
// an internal failure.
func TestCaseRunnerHonorsPauseWhenAgentReadFails(t *testing.T) {
	runner, world := newCaseRunnerHarness(t)
	run := runningCaseRun(t, world)
	world.runtime.getErr = errors.New("agent runtime unavailable")
	world.batches.setPaused(run.BatchID, true, domain.PauseModeGraceful)

	runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

	stored := world.run(t, run.ID)
	if stored.Status != domain.CaseRunStatusPending {
		t.Fatalf("status = %s, want pending (class=%v reason=%q)",
			stored.Status, stored.FailureClass, stored.FailureReason)
	}
	if stored.FailureClass != nil {
		t.Fatalf("failure class = %s, want no classification", *stored.FailureClass)
	}
}

// TestDBAssertionFailureCodesKeepTheirClass pins the P7 db error mapping
// (spec §5.7/§6.6.2) end to end inside the runner: the whitelist must recognize
// every db tool code the classifier knows, and the resulting case-run status must
// follow the verdict (a connection/target problem blocks, a SQL/read-only/
// truncation/expected-value problem stays inconclusive(case)).
func TestDBAssertionFailureCodesKeepTheirClass(t *testing.T) {
	cases := []struct {
		code        string
		wantVerdict domain.Verdict
		wantClass   domain.FailureClass
		wantStatus  domain.CaseRunStatus
	}{
		{
			code: "db_connection_failed", wantVerdict: domain.VerdictBlocked,
			wantClass: domain.FailureClassEnvironment, wantStatus: domain.CaseRunStatusBlocked,
		},
		{
			code: "db_target_missing", wantVerdict: domain.VerdictBlocked,
			wantClass: domain.FailureClassEnvironment, wantStatus: domain.CaseRunStatusBlocked,
		},
		{
			code: "db_query_failed", wantVerdict: domain.VerdictInconclusive,
			wantClass: domain.FailureClassCase, wantStatus: domain.CaseRunStatusInconclusive,
		},
		{
			code: "db_readonly_violation", wantVerdict: domain.VerdictInconclusive,
			wantClass: domain.FailureClassCase, wantStatus: domain.CaseRunStatusInconclusive,
		},
		{
			code: "db_result_truncated", wantVerdict: domain.VerdictInconclusive,
			wantClass: domain.FailureClassCase, wantStatus: domain.CaseRunStatusInconclusive,
		},
		{
			code: "db_expected_invalid", wantVerdict: domain.VerdictInconclusive,
			wantClass: domain.FailureClassCase, wantStatus: domain.CaseRunStatusInconclusive,
		},
	}
	for _, tc := range cases {
		t.Run(tc.code, func(t *testing.T) {
			if !knownAgentToolCode(tc.code) {
				t.Fatalf("%q is not in the runner's known tool-code whitelist", tc.code)
			}
			verdict, class, reason := domain.Classify(tc.code, "", 0)
			if verdict != tc.wantVerdict || class != tc.wantClass || reason != tc.code {
				t.Fatalf("Classify(%q) = %s/%s/%q, want %s/%s/%q",
					tc.code, verdict, class, reason, tc.wantVerdict, tc.wantClass, tc.code)
			}
			if got := caseRunStatusForVerdict(verdict); got != tc.wantStatus {
				t.Fatalf("case run status = %s, want %s", got, tc.wantStatus)
			}
		})
	}
}

// TestAgentTerminalCodeMapsTerminalReason pins the terminal-reason mapping: an
// LLM/model failure must never be blamed on the case, a normal agent end that
// skipped case_finish stays case, and a pause/shutdown is never turned into a
// model reason.
func TestAgentTerminalCodeMapsTerminalReason(t *testing.T) {
	cases := []struct {
		name       string
		state      AgentRunState
		wantCode   string
		wantReason string
	}{
		{
			name:       "model error",
			state:      AgentRunState{Status: "failed", Error: "upstream request failed", TerminalReason: "model_error"},
			wantCode:   "model_error",
			wantReason: "model_error",
		},
		{
			name:       "llm error alias",
			state:      AgentRunState{Status: "failed", TerminalReason: "llm_error"},
			wantCode:   "model_error",
			wantReason: "llm_error",
		},
		{
			name:       "gateway error alias",
			state:      AgentRunState{Status: "failed", TerminalReason: "gateway_error"},
			wantCode:   "model_error",
			wantReason: "gateway_error",
		},
		{
			name:       "context overflow",
			state:      AgentRunState{Status: "failed", TerminalReason: "context_overflow"},
			wantCode:   "budget_exhausted",
			wantReason: "context_overflow",
		},
		{
			name:       "max turns",
			state:      AgentRunState{Status: "done", TerminalReason: "max_turns"},
			wantCode:   "budget_exhausted",
			wantReason: "max_turns",
		},
		{
			name:       "run hard timeout",
			state:      AgentRunState{Status: "aborted", TerminalReason: "run_hard_timeout"},
			wantCode:   "budget_exhausted",
			wantReason: "run_hard_timeout",
		},
		{
			name:       "timeout",
			state:      AgentRunState{Status: "done", TerminalReason: "timeout"},
			wantCode:   "budget_exhausted",
			wantReason: "timeout",
		},
		{
			name:       "done without case_finish",
			state:      AgentRunState{Status: "done", TerminalReason: "completed"},
			wantCode:   "agent_output_invalid",
			wantReason: "case run ended without case_finish",
		},
		{
			name:       "failed with an unknown error stays model",
			state:      AgentRunState{Status: "failed", Error: "upstream socket closed"},
			wantCode:   "internal",
			wantReason: "upstream socket closed",
		},
		{
			name:       "aborted with an unknown error stays model",
			state:      AgentRunState{Status: "aborted", Error: "user aborted", TerminalReason: "aborted_streaming"},
			wantCode:   "internal",
			wantReason: "user aborted",
		},
		{
			name:       "known tool code is preserved",
			state:      AgentRunState{Status: "failed", Error: "host_not_allowed"},
			wantCode:   "host_not_allowed",
			wantReason: "host_not_allowed",
		},
		{
			name:       "db connection failure is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_connection_failed"},
			wantCode:   "db_connection_failed",
			wantReason: "db_connection_failed",
		},
		{
			name:       "db query failure is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_query_failed"},
			wantCode:   "db_query_failed",
			wantReason: "db_query_failed",
		},
		{
			name:       "db readonly violation is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_readonly_violation"},
			wantCode:   "db_readonly_violation",
			wantReason: "db_readonly_violation",
		},
		{
			name:       "db result truncation is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_result_truncated"},
			wantCode:   "db_result_truncated",
			wantReason: "db_result_truncated",
		},
		{
			name:       "db expected invalid is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_expected_invalid"},
			wantCode:   "db_expected_invalid",
			wantReason: "db_expected_invalid",
		},
		{
			name:       "db target missing is preserved",
			state:      AgentRunState{Status: "failed", Error: "db_target_missing"},
			wantCode:   "db_target_missing",
			wantReason: "db_target_missing",
		},
		{
			name:       "interrupted reason untouched",
			state:      AgentRunState{Status: "interrupted", TerminalReason: "interrupted"},
			wantCode:   "internal",
			wantReason: "agent run interrupted",
		},
		{
			name:       "shutdown reason untouched",
			state:      AgentRunState{Status: "aborted", TerminalReason: "shutdown"},
			wantCode:   "internal",
			wantReason: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, reason := agentTerminalCode(tc.state)
			if code != tc.wantCode || reason != tc.wantReason {
				t.Fatalf("agentTerminalCode(%+v) = (%q, %q), want (%q, %q)",
					tc.state, code, reason, tc.wantCode, tc.wantReason)
			}
		})
	}
}

// TestCaseRunnerLeavesRunRunningOnShutdown pins that a run aborted by the
// process shutdown is never classified: the case run stays running for boot
// recovery, while the tools' browser context and the session lease are still
// released. Classifying the shutdown abort would turn an interrupted process
// into an inconclusive(case) verdict (spec §5.4, §5.12 invariant 7).
func TestCaseRunnerLeavesRunRunningOnShutdown(t *testing.T) {
	runner, world := newCaseRunnerHarness(t)
	run := runningCaseRun(t, world)
	world.runtime.setState(AgentRunState{Status: "aborted", TerminalReason: "shutdown"})

	runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

	stored := world.run(t, 1)
	if stored.Status != domain.CaseRunStatusRunning {
		t.Fatalf("status = %s, want running (shutdown must not classify)", stored.Status)
	}
	if stored.FailureClass != nil || stored.FailureReason != "" || stored.FinishedAt != nil {
		t.Fatalf("shutdown must not finish the run: %+v", stored)
	}
	if got := world.browser.closedIDs(); !reflect.DeepEqual(got, []string{"case:1"}) {
		t.Fatalf("CloseCase calls = %v, want [case:1]", got)
	}
	if !world.sessions.released(500, "released") {
		t.Fatalf("session lease was not released best-effort: %+v", world.sessions.releases)
	}
}

// TestCaseRunnerClassifiesAgentTerminalReason pins the case-run outcome of the
// two acceptance-critical terminal reasons: an LLM failure becomes
// inconclusive(model), while a normal end without case_finish stays
// inconclusive(case).
func TestCaseRunnerClassifiesAgentTerminalReason(t *testing.T) {
	cases := []struct {
		name       string
		state      AgentRunState
		wantStatus domain.CaseRunStatus
		wantClass  domain.FailureClass
		wantReason string
	}{
		{
			name:       "LLM error becomes inconclusive(model)",
			state:      AgentRunState{Status: "failed", Error: "upstream request failed", TerminalReason: "model_error"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassModel,
			wantReason: "model_error",
		},
		{
			name:       "done without case_finish becomes inconclusive(case)",
			state:      AgentRunState{Status: "done", TerminalReason: "completed"},
			wantStatus: domain.CaseRunStatusInconclusive,
			wantClass:  domain.FailureClassCase,
			wantReason: "case run ended without case_finish",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, world := newCaseRunnerHarness(t)
			run := runningCaseRun(t, world)
			world.runtime.setState(tc.state)

			runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

			stored := world.run(t, 1)
			if stored.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s", stored.Status, tc.wantStatus)
			}
			if stored.FailureClass == nil || *stored.FailureClass != tc.wantClass {
				t.Fatalf("failure class = %v, want %s", stored.FailureClass, tc.wantClass)
			}
			if stored.FailureReason != tc.wantReason {
				t.Fatalf("failure reason = %q, want %q", stored.FailureReason, tc.wantReason)
			}
		})
	}
}

// TestCaseRunnerFinishesExecutionToolFailures pins the P8 server-enforced
// terminal outcomes reported by the execution tools: an exhausted tool-call
// budget and an executor timeout both end the case as inconclusive(model) with
// the stable reason, cancel the agent run and release the case resources.
func TestCaseRunnerFinishesExecutionToolFailures(t *testing.T) {
	cases := []struct {
		name string
		code string
	}{
		{name: "tool call budget exhausted", code: "tool_call_budget_exhausted"},
		{name: "executor timeout", code: "executor_timeout"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, world := newCaseRunnerHarness(t)
			run := runningCaseRun(t, world)
			world.runtime.setState(AgentRunState{Status: "running"})
			world.browser.setFailure(run.ID, tc.code)

			runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

			stored := world.run(t, 1)
			if stored.Status != domain.CaseRunStatusInconclusive {
				t.Fatalf("status = %s, want inconclusive", stored.Status)
			}
			if stored.FailureClass == nil || *stored.FailureClass != domain.FailureClassModel {
				t.Fatalf("failure class = %v, want model", stored.FailureClass)
			}
			if stored.FailureReason != tc.code {
				t.Fatalf("failure reason = %q, want %q", stored.FailureReason, tc.code)
			}
			if got := world.runtime.canceled(); len(got) != 1 {
				t.Fatalf("CancelRun calls = %v, want exactly one", got)
			}
			if got := world.browser.closedIDs(); !reflect.DeepEqual(got, []string{"case:1"}) {
				t.Fatalf("CloseCase calls = %v, want [case:1]", got)
			}
			if !world.sessions.released(500, "finished") {
				t.Fatalf("session lease was not released: %+v", world.sessions.releases)
			}
		})
	}
}

// TestCaseRunnerPauseWinsOverSignal pins the precedence of the two server-side
// outcomes: an explicit pause is authoritative over a tool-enforced terminal
// signal, so a paused run parks/interrupts instead of being classified
// inconclusive(model).
func TestCaseRunnerPauseWinsOverSignal(t *testing.T) {
	cases := []struct {
		name       string
		mode       domain.PauseMode
		wantStatus domain.CaseRunStatus
	}{
		{name: "graceful parks", mode: domain.PauseModeGraceful, wantStatus: domain.CaseRunStatusPending},
		{name: "immediate interrupts", mode: domain.PauseModeImmediate, wantStatus: domain.CaseRunStatusInterrupted},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner, world := newCaseRunnerHarness(t)
			run := runningCaseRun(t, world)
			world.runtime.setState(AgentRunState{Status: "running"})
			world.browser.setFailure(run.ID, "tool_call_budget_exhausted")
			world.batches.setPaused(run.BatchID, true, tc.mode)

			runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

			stored := world.run(t, run.ID)
			if stored.Status != tc.wantStatus {
				t.Fatalf("status = %s, want %s (class=%v reason=%q)",
					stored.Status, tc.wantStatus, stored.FailureClass, stored.FailureReason)
			}
			if stored.FailureClass != nil {
				t.Fatalf("failure class = %s, want no classification when a pause wins", *stored.FailureClass)
			}
		})
	}
}

// TestCaseRunnerSignalWinsOverAgentTerminal pins that the server-side terminal
// signal stays authoritative when the agent run reaches a terminal state in the
// same poll: the case must carry executor_timeout, not the agent-derived reason.
func TestCaseRunnerSignalWinsOverAgentTerminal(t *testing.T) {
	runner, world := newCaseRunnerHarness(t)
	run := runningCaseRun(t, world)
	world.runtime.setState(AgentRunState{
		Status: "failed", TerminalReason: "model_error", Error: "upstream request failed",
	})
	world.browser.setFailure(run.ID, "executor_timeout")

	runner.Run(context.Background(), run, SessionRef{SessionID: 500}, world.sessions.environment)

	stored := world.run(t, 1)
	if stored.Status != domain.CaseRunStatusInconclusive {
		t.Fatalf("status = %s, want inconclusive", stored.Status)
	}
	if stored.FailureClass == nil || *stored.FailureClass != domain.FailureClassModel {
		t.Fatalf("failure class = %v, want model", stored.FailureClass)
	}
	if stored.FailureReason != "executor_timeout" {
		t.Fatalf("failure reason = %q, want executor_timeout", stored.FailureReason)
	}
}
