package executionapp

// V-0022 / SC-F9 settlement pins: the human timeout/cancel path and the recovery
// dispatch-budget-exhausted path write their terminal verdict through the single
// settlement entry (settleTerminalVerdict -> fenced UpdateCAS), and the verdict
// comes from execution.Aggregate — including the evidence gate (a zero-evidence
// failed degrades inconclusive, constitution art.1.2). The stable human cause
// (human_timeout/human_cancelled) and the recovery terminal cause stay visible.

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// humanTakeoverFixture seeds one running case with an open login-takeover
// request (the run is paused by CreateFromTool), ready to expire.
func humanTakeoverFixture(t *testing.T) (*task9Fixture, int64) {
	t.Helper()
	f := newTask9Fixture()
	ctx := context.Background()
	f.seedWave(execution.WaveRunning)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	f.seedIntent(1, 300)
	scope := tools.Scope{VersionID: 100, WaveID: 50, CaseRunID: 500, DispatchEpoch: run.DispatchEpoch, DispatchToken: run.DispatchToken}
	requestID, err := f.human.CreateFromTool(ctx, scope, tools.HumanRequestInput{Kind: "login_takeover", Reason: "2FA"})
	if err != nil {
		t.Fatalf("CreateFromTool: %v", err)
	}
	return f, requestID
}

// TestHumanFailRunViaAggregate pins the settlement contract: a human timeout
// with persisted evidence settles failed(session) through the single entry and
// keeps the stable human_timeout verdict reason.
func TestHumanFailRunViaAggregate(t *testing.T) {
	ctx := context.Background()
	f, _ := humanTakeoverFixture(t)
	_ = f.evidence.Insert(ctx, execution.Evidence{CaseRunID: 500, Kind: execution.EvidenceText})

	f.clock.now = f.clock.Now().Add(2 * time.Hour)
	if count, err := f.human.Expire(ctx, f.clock.Now()); err != nil || count != 1 {
		t.Fatalf("Expire = %d err=%v", count, err)
	}
	failed, _ := f.runs.Get(ctx, 500)
	if failed.Status != execution.CaseRunFailed || failed.FailureClass != execution.FailureClassSession {
		t.Fatalf("run = %s/%s, want failed/session", failed.Status, failed.FailureClass)
	}
	if failed.Summary.VerdictReason != execution.VerdictReasonHumanTimeout {
		t.Fatalf("verdict reason = %q, want human_timeout", failed.Summary.VerdictReason)
	}
	if failed.FinishedAt == nil || failed.LeaseUntil != nil {
		t.Fatalf("finished_at/lease = %v/%v, want settled", failed.FinishedAt, failed.LeaseUntil)
	}
	if failed.PausedAt != nil || failed.PausedBy != nil || failed.PauseReason != "" {
		t.Fatalf("pause surface = %v/%v/%q, want cleared on settlement", failed.PausedAt, failed.PausedBy, failed.PauseReason)
	}
}

// TestHumanFailRunNoEvidenceDegrades pins the evidence gate on the human
// settlement path: a paused run with zero persisted evidence cannot settle as a
// trustworthy failed(session); it degrades inconclusive(no_evidence).
func TestHumanFailRunNoEvidenceDegrades(t *testing.T) {
	ctx := context.Background()
	f, _ := humanTakeoverFixture(t)
	// Deliberately no evidence rows.

	f.clock.now = f.clock.Now().Add(2 * time.Hour)
	if count, err := f.human.Expire(ctx, f.clock.Now()); err != nil || count != 1 {
		t.Fatalf("Expire = %d err=%v", count, err)
	}
	settled, _ := f.runs.Get(ctx, 500)
	if settled.Status != execution.CaseRunInconclusive || settled.FailureClass != "" {
		t.Fatalf("run = %s/%q, want inconclusive with no failure class (no-evidence failed degrades)", settled.Status, settled.FailureClass)
	}
	if settled.Summary.VerdictReason != execution.VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want %q", settled.Summary.VerdictReason, execution.VerdictReasonNoEvidence)
	}
}

// TestRecoveryTerminalViaAggregate pins the recovery settlement contract: an
// offline-executor run with an exhausted dispatch budget and persisted evidence
// settles failed(executor_lost) through the single entry, keeping the stable
// terminal cause.
func TestRecoveryTerminalViaAggregate(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	run.Attempts = 3
	run.OwnerExecutorID = int64PtrValue(1)
	f.runs.byID[500] = run
	f.executors.byID[1] = execution.ExecutorInstance{ID: 1, Name: "executor", Endpoint: "http://x", Status: execution.ExecutorOffline}
	f.cases.cases[300] = task6Snapshot(300, 5001)
	_ = f.evidence.Insert(ctx, execution.Evidence{CaseRunID: 500, Kind: execution.EvidenceText})

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	failed, _ := f.runs.Get(ctx, 500)
	if failed.Status != execution.CaseRunFailed || failed.FailureClass != execution.FailureClassExecutorLost {
		t.Fatalf("run = %s/%s, want failed/executor_lost", failed.Status, failed.FailureClass)
	}
	if failed.Summary.TerminalCause != string(execution.FailureClassExecutorLost) {
		t.Fatalf("terminal cause = %q, want executor_lost", failed.Summary.TerminalCause)
	}
	if failed.FinishedAt == nil || failed.LeaseUntil != nil {
		t.Fatalf("finished_at/lease = %v/%v, want settled", failed.FinishedAt, failed.LeaseUntil)
	}
}

// TestRecoveryTerminalNoEvidenceDegrades pins the evidence gate on the recovery
// settlement path: a budget-exhausted run with zero persisted evidence degrades
// inconclusive(no_evidence) while keeping the executor_lost terminal cause
// visible for triage.
func TestRecoveryTerminalNoEvidenceDegrades(t *testing.T) {
	ctx := context.Background()
	f := newTask9Fixture()
	f.seedWave(execution.WaveRunning)
	f.seedIntent(1, 300)
	run := f.seedRun(500, 1, execution.CaseRunRunning)
	run.Attempts = 3
	run.OwnerExecutorID = int64PtrValue(1)
	f.runs.byID[500] = run
	f.executors.byID[1] = execution.ExecutorInstance{ID: 1, Name: "executor", Endpoint: "http://x", Status: execution.ExecutorOffline}
	f.cases.cases[300] = task6Snapshot(300, 5001)
	// Deliberately no evidence rows.

	if err := f.recovery.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	settled, _ := f.runs.Get(ctx, 500)
	if settled.Status != execution.CaseRunInconclusive || settled.FailureClass != "" {
		t.Fatalf("run = %s/%q, want inconclusive with no failure class (no-evidence failed degrades)", settled.Status, settled.FailureClass)
	}
	if settled.Summary.VerdictReason != execution.VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want %q", settled.Summary.VerdictReason, execution.VerdictReasonNoEvidence)
	}
	if settled.Summary.TerminalCause != string(execution.FailureClassExecutorLost) {
		t.Fatalf("terminal cause = %q, want executor_lost kept for triage", settled.Summary.TerminalCause)
	}
}
