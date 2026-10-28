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
