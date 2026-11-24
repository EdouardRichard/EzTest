package versionapp_test

import (
	"errors"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestReconcilerDirectCloseNoSteward pins GD-17 / R-G4 (SC-G10): the reconciler
// closure hooks evaluate the deterministic predicate and call the terminal Close
// entry DIRECTLY. No Steward LLM run and no requirement.close/version.close work
// item is involved. The requirement closure carries no closed_by_run_id (there is
// no agent run), and the version flips running -> completed through the single
// CAS Close entry.
func TestReconcilerDirectCloseNoSteward(t *testing.T) {
	closureEnv := newClosureEnv(t)
	requirement := seedClosureRequirement(closureEnv, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(closureEnv, requirement.ID)

	if err := closureEnv.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("EnsureClosures: %v", err)
	}
	if closureEnv.closures.closeCalls != 1 {
		t.Fatalf("requirement Close calls = %d, want 1 (direct terminal close, not a task ensure)",
			closureEnv.closures.closeCalls)
	}
	stored := closureEnv.requirements.requirements[requirement.ID]
	if stored.Status != version.RequirementClosed {
		t.Fatalf("requirement status = %q, want closed (reconciler direct close)", stored.Status)
	}
	if stored.ClosedByRunID != nil {
		t.Fatalf("requirement closed_by_run_id = %v, want nil (no Steward LLM run)", stored.ClosedByRunID)
	}
	if !containsAll(closureEventTypes(closureEnv.events.events), "requirement.closed") {
		t.Fatalf("requirement.closed event missing: %#v", closureEnv.events.events)
	}

	closeEnv := newVersionCloseEnv(t)
	configurePassingVersionClose(closeEnv, 701)
	if err := closeEnv.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose: %v", err)
	}
	if closeEnv.versions.closeCalls != 1 {
		t.Fatalf("version Close calls = %d, want 1 (direct terminal close, not a task ensure)",
			closeEnv.versions.closeCalls)
	}
	closed := closeEnv.versions.versions[701]
	if closed.Status != version.StatusCompleted {
		t.Fatalf("version status = %q, want completed (reconciler direct close)", closed.Status)
	}
	if closed.CloseRunID != nil {
		t.Fatalf("version close_run_id = %v, want nil (no Steward LLM run)", closed.CloseRunID)
	}
	if !containsAll(versionCloseEventTypes(closeEnv.events.events), "version.closed") {
		t.Fatalf("version.closed event missing: %#v", closeEnv.events.events)
	}
}

// TestEnsureClosuresDirectCloseIdempotent pins R-G4 idempotency: a second
// reconciler pass over an already-closed requirement does not write a second
// closure round (Close is idempotent on the closed state).
func TestEnsureClosuresDirectCloseIdempotent(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)

	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("first EnsureClosures: %v", err)
	}
	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("second EnsureClosures: %v", err)
	}
	if env.closures.closeCalls != 1 {
		t.Fatalf("requirement Close calls = %d, want 1 after two passes (idempotent)", env.closures.closeCalls)
	}
	if len(env.closures.closures[requirement.ID]) != 1 {
		t.Fatalf("closure rounds = %d, want 1", len(env.closures.closures[requirement.ID]))
	}
}

// TestEnsureVersionCloseDirectCloseIdempotent pins R-G4 idempotency at the version
// level: once completed, a second ensure pass is a no-op (the CAS Close entry
// rejects a non-running version; the ensure hook treats that as benign).
func TestEnsureVersionCloseDirectCloseIdempotent(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)

	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("first EnsureVersionClose: %v", err)
	}
	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("second EnsureVersionClose: %v", err)
	}
	if env.versions.closeCalls != 1 {
		t.Fatalf("version Close calls = %d, want 1 after two passes (idempotent)", env.versions.closeCalls)
	}
	if len(env.events.events) != 2 {
		t.Fatalf("version.closed events = %d, want 2 (one per channel, written once)", len(env.events.events))
	}
}

// TestEnsureClosuresPredicateFailureIsNotError pins that an unsatisfied predicate
// is a normal no-op for the reconciler (not a failure): a requirement with an
// unverified (inconclusive) latest run stays open and EnsureClosures returns nil.
// This is the REL-Q3 auto-close guard reborn on the direct-close path.
func TestEnsureClosuresPredicateFailureIsNotError(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedP0ClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configureInconclusiveClosure(env, requirement.ID)

	if err := env.svc.EnsureClosures(requestContext(), 701); err != nil {
		t.Fatalf("EnsureClosures on unverified requirement: %v", err)
	}
	if env.closures.closeCalls != 0 {
		t.Fatalf("requirement Close calls = %d, want 0 (unverified must not close)", env.closures.closeCalls)
	}
	if env.requirements.requirements[requirement.ID].Status != version.RequirementClarified {
		t.Fatal("unverified requirement was closed")
	}
}

// TestEnsureVersionClosePredicateFailureIsNotError pins that a failing version
// predicate (active wave) is a benign no-op for the ensure hook, not an error.
func TestEnsureVersionClosePredicateFailureIsNotError(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	env.execution.waves = 1

	if err := env.svc.EnsureVersionClose(requestContext(), 701); err != nil {
		t.Fatalf("EnsureVersionClose with active wave: %v", err)
	}
	if env.versions.closeCalls != 0 {
		t.Fatalf("version Close calls = %d, want 0 (active wave blocks close)", env.versions.closeCalls)
	}
	if env.versions.versions[701].Status != version.StatusRunning {
		t.Fatal("version completed despite an active wave")
	}
}

// TestEnsureClosuresPropagatesInfraErrors pins that a real port failure (not a
// predicate rejection) still surfaces to the reconciler error aggregate.
func TestEnsureClosuresPropagatesInfraErrors(t *testing.T) {
	env := newClosureEnv(t)
	requirement := seedClosureRequirement(env, 1001, 701, 1002, version.RequirementClarified)
	configurePassingClosure(env, requirement.ID)
	wantErr := errors.New("closure store down")
	env.closures.closeErr = wantErr

	err := env.svc.EnsureClosures(requestContext(), 701)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureClosures error = %v, want %v", err, wantErr)
	}
}

// TestEnsureVersionClosePropagatesInfraErrors pins the same for the version hook.
func TestEnsureVersionClosePropagatesInfraErrors(t *testing.T) {
	env := newVersionCloseEnv(t)
	configurePassingVersionClose(env, 701)
	wantErr := errors.New("version store down")
	env.versions.closeErr = wantErr

	err := env.svc.EnsureVersionClose(requestContext(), 701)
	if !errors.Is(err, wantErr) {
		t.Fatalf("EnsureVersionClose error = %v, want %v", err, wantErr)
	}
}
