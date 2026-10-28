package application

import (
	"context"
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
