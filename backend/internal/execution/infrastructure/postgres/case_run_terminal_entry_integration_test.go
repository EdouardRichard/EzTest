//go:build integration

package executionpg

// V-0022 / SC-F9 repository-level pins for the single terminal entry:
//   - the generic Update primitive structurally refuses terminal transitions
//     (into or out of a terminal status); only non-terminal changes and
//     same-status rewrites are allowed, and a refusal is a loud conflict;
//   - ReclassifyTerminal is the guarded, fenced terminal->terminal primitive:
//     it only touches failed/blocked/inconclusive rows, keeps the result inside
//     that set, and honours the dispatch fence;
//   - the settlement CAS keeps refusing already-terminal rows (the invariant
//     the reclassification mode must not weaken).

import (
	"context"
	"errors"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func TestUpdateRefusesTerminalTransition(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)

	// A direct non-terminal -> terminal write through Update is a bypass and
	// must fail loudly, leaving the row untouched.
	run, _ := repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunFailed
	run.FailureClass = execution.FailureClassApplication
	ok, err := repo.CaseRuns.Update(ctx, run)
	if ok || !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("rogue terminal Update = ok:%v err:%v, want refused with ErrStateConflict", ok, err)
	}
	if stored, _ := repo.CaseRuns.Get(ctx, 500); stored.Status != execution.CaseRunPending {
		t.Fatalf("rogue terminal Update changed the row: %s", stored.Status)
	}

	// Non-terminal changes stay allowed.
	run, _ = repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunRunning
	run.DispatchEpoch = 2
	run.DispatchToken = "tok"
	if ok, err := repo.CaseRuns.Update(ctx, run); err != nil || !ok {
		t.Fatalf("non-terminal Update = ok:%v err:%v", ok, err)
	}

	// Settlement through the fenced CAS wins.
	ok, err = repo.CaseRuns.UpdateCAS(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunFailed
		row.FailureClass = execution.FailureClassApplication
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("settlement CAS = ok:%v err:%v", ok, err)
	}

	// A same-status rewrite of the settled row (summary/class refinement) is
	// still allowed through Update.
	run, _ = repo.CaseRuns.Get(ctx, 500)
	run.Summary.LastError = "refined explanation"
	if ok, err := repo.CaseRuns.Update(ctx, run); err != nil || !ok {
		t.Fatalf("same-status Update = ok:%v err:%v", ok, err)
	}

	// Resurrecting a terminal row through Update is refused.
	run, _ = repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunRunning
	if ok, err := repo.CaseRuns.Update(ctx, run); ok || !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("resurrecting Update = ok:%v err:%v, want refused", ok, err)
	}
	// A terminal -> different-terminal change through Update is refused too:
	// reclassification must go through ReclassifyTerminal.
	run, _ = repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunBlocked
	if ok, err := repo.CaseRuns.Update(ctx, run); ok || !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("terminal->terminal Update = ok:%v err:%v, want refused", ok, err)
	}
	if stored, _ := repo.CaseRuns.Get(ctx, 500); stored.Status != execution.CaseRunFailed {
		t.Fatalf("refused Updates changed the row: %s", stored.Status)
	}

	// The settlement CAS invariant is intact: a terminal row is never settled
	// a second time.
	ok, err = repo.CaseRuns.UpdateCAS(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunPassed
		return nil
	})
	if err != nil || ok {
		t.Fatalf("late settlement CAS = ok:%v err:%v, want refused", ok, err)
	}
}

func TestReclassifyTerminalGuards(t *testing.T) {
	repo, _ := newTestRepository(t)
	ctx := context.Background()
	mustCreateIntent(t, repo, 1, 100, 300, testFingerprint, execution.PurposeInitial)
	mustCreateWave(t, repo, 50, 100)
	mustCreateCaseRun(t, repo, 500, 50, 1, 300)

	// A non-terminal row is not reclassifiable.
	ok, err := repo.CaseRuns.ReclassifyTerminal(ctx, 500, 0, "", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunBlocked
		return nil
	})
	if err != nil || ok {
		t.Fatalf("reclassify pending row = ok:%v err:%v, want false,nil", ok, err)
	}

	// Settle the run failed(application) with a dispatch fence.
	run, _ := repo.CaseRuns.Get(ctx, 500)
	run.Status = execution.CaseRunRunning
	run.DispatchEpoch = 2
	run.DispatchToken = "tok"
	if ok, err := repo.CaseRuns.Update(ctx, run); err != nil || !ok {
		t.Fatalf("seed running: ok:%v err:%v", ok, err)
	}
	if ok, err := repo.CaseRuns.UpdateCAS(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunFailed
		row.FailureClass = execution.FailureClassApplication
		return nil
	}); err != nil || !ok {
		t.Fatalf("settle: ok:%v err:%v", ok, err)
	}

	// A lost fence does not reclassify.
	ok, err = repo.CaseRuns.ReclassifyTerminal(ctx, 500, 9, "other", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunBlocked
		return nil
	})
	if err != nil || ok {
		t.Fatalf("fenced reclassify = ok:%v err:%v, want false,nil", ok, err)
	}

	// An illegal target (passed) is a loud conflict, never a silent rewrite.
	ok, err = repo.CaseRuns.ReclassifyTerminal(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunPassed
		return nil
	})
	if ok || !errors.Is(err, execution.ErrStateConflict) {
		t.Fatalf("illegal reclassify target = ok:%v err:%v, want ErrStateConflict", ok, err)
	}
	if stored, _ := repo.CaseRuns.Get(ctx, 500); stored.Status != execution.CaseRunFailed {
		t.Fatalf("illegal reclassify changed the row: %s", stored.Status)
	}

	// The sanctioned reclassification wins and keeps the fence.
	ok, err = repo.CaseRuns.ReclassifyTerminal(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunBlocked
		row.FailureClass = execution.FailureClassBlocked
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("sanctioned reclassify = ok:%v err:%v", ok, err)
	}
	stored, _ := repo.CaseRuns.Get(ctx, 500)
	if stored.Status != execution.CaseRunBlocked || stored.FailureClass != execution.FailureClassBlocked {
		t.Fatalf("reclassified row = %s/%s, want blocked/blocked", stored.Status, stored.FailureClass)
	}

	// Same-status class refinement is sanctioned (analyst refinement).
	ok, err = repo.CaseRuns.ReclassifyTerminal(ctx, 500, 2, "tok", func(row *execution.CaseRun) error {
		row.FailureClass = execution.FailureClassEnvironment
		return nil
	})
	if err != nil || !ok {
		t.Fatalf("same-status refinement = ok:%v err:%v", ok, err)
	}
	if stored, _ := repo.CaseRuns.Get(ctx, 500); stored.FailureClass != execution.FailureClassEnvironment {
		t.Fatalf("refined class = %s, want environment", stored.FailureClass)
	}

	// A passed row is never reclassifiable.
	mustCreateIntent(t, repo, 2, 100, 301, testFingerprint, execution.PurposeInitial)
	mustCreateCaseRun(t, repo, 501, 50, 2, 301)
	if ok, err := repo.CaseRuns.UpdateCAS(ctx, 501, 0, "", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunPassed
		return nil
	}); err != nil || !ok {
		t.Fatalf("settle passed: ok:%v err:%v", ok, err)
	}
	ok, err = repo.CaseRuns.ReclassifyTerminal(ctx, 501, 0, "", func(row *execution.CaseRun) error {
		row.Status = execution.CaseRunFailed
		return nil
	})
	if err != nil || ok {
		t.Fatalf("reclassify passed row = ok:%v err:%v, want false,nil", ok, err)
	}
	if stored, _ := repo.CaseRuns.Get(ctx, 501); stored.Status != execution.CaseRunPassed {
		t.Fatalf("passed row changed: %s", stored.Status)
	}
}
