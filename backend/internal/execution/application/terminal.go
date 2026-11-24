package executionapp

// The single terminal write entry of case_run (V-0022, constitution art.3.6).
// Every terminal verdict write goes through exactly one of these two guarded
// modes, and every verdict is computed by the single domain authority
// execution.Aggregate before it reaches the entry:
//
//   - settleTerminalVerdict: non-terminal -> terminal settlement (the CaseRunner
//     Finish/Finalize path, the human timeout/cancel path, the recovery
//     dispatch-budget-exhausted path). Fenced UpdateCAS; the CAS keeps refusing
//     already-terminal rows, so a late settlement is an idempotent read of the
//     stored truth, never a second write.
//   - reclassifyTerminalVerdict: terminal -> terminal reclassification (the
//     deterministic analysis fallback and the analyst refinement). Guarded by
//     the domain CanReclassifyTo rule (failed/blocked/inconclusive only) and
//     fenced; the settlement CAS invariant is untouched.
//
// A direct terminal write outside these entries is a bypass: the repository
// Update primitive structurally refuses terminal transitions, and the
// TestTerminalSingleEntryGate drift gate fails on a new one.

import (
	"context"
	"fmt"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// settleTerminalVerdict persists a terminal verdict when this owner still holds
// the dispatch fence. The bool reports whether this call won the CAS: false
// means another owner already finalized the run (or the fence was lost), so
// callers must not repeat one-time side effects such as the knowledge success
// hook or the finished event.
func settleTerminalVerdict(
	ctx context.Context,
	caseRuns CaseRunRepository,
	now time.Time,
	caseRun execution.CaseRun,
	status execution.CaseRunStatus,
	class execution.FailureClass,
	summary execution.CaseRunSummary,
) (execution.CaseRun, bool, error) {
	if !status.Finished() {
		return execution.CaseRun{}, false, platform.Invalid("verdict", "terminal settlement requires a finished verdict status")
	}
	// The domain state machine is the single transition authority. A terminal
	// snapshot skips the pre-check: the CAS itself refuses terminal rows and
	// the idempotent read below returns the stored truth.
	if !caseRun.Status.Terminal() && !caseRun.Status.CanTransitionTo(status) {
		return execution.CaseRun{}, false, platform.StateConflict(
			fmt.Sprintf("case_run %d cannot settle %s -> %s", caseRun.ID, caseRun.Status, status))
	}
	ok, err := caseRuns.UpdateCAS(ctx, caseRun.ID, caseRun.DispatchEpoch, caseRun.DispatchToken, func(run *execution.CaseRun) error {
		// The aggregate rebuilds the verdict summary from scratch; keep the
		// dispatch-time executor snapshot assembled by the scheduler (X-01).
		summary.Dispatch = run.Summary.Dispatch
		run.Status = status
		run.FailureClass = class
		run.Summary = summary
		if run.StartedAt == nil {
			run.StartedAt = &now
		}
		run.FinishedAt = &now
		run.LeaseUntil = nil
		// A settled run is no longer paused (human timeout/cancel settles a
		// paused row; the agent paths never carry a pause surface).
		run.PausedAt = nil
		run.PausedBy = nil
		run.PauseReason = ""
		return nil
	})
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	if !ok {
		// Another owner already finalized the run; return the stored truth
		// (settlement is idempotent for terminal rows).
		current, getErr := caseRuns.Get(ctx, caseRun.ID)
		if getErr != nil {
			return execution.CaseRun{}, false, mapExecutionError(getErr, "case_run")
		}
		if current.Status.Finished() {
			return current, false, nil
		}
		return execution.CaseRun{}, false, platform.LeaseLost()
	}
	finished, err := caseRuns.Get(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	return finished, true, nil
}

// reclassifyTerminalVerdict persists a terminal->terminal reclassification of a
// failed/blocked/inconclusive verdict through the guarded, fenced repository
// primitive. The bool reports whether this call won the write: false means the
// row was not reclassifiable or another writer won, and the stored truth is
// returned (callers must not repeat one-time side effects).
func reclassifyTerminalVerdict(
	ctx context.Context,
	caseRuns CaseRunRepository,
	now time.Time,
	caseRun execution.CaseRun,
	status execution.CaseRunStatus,
	class execution.FailureClass,
	summary execution.CaseRunSummary,
) (execution.CaseRun, bool, error) {
	if !caseRun.Status.CanReclassifyTo(status) {
		return execution.CaseRun{}, false, platform.StateConflict(
			fmt.Sprintf("case_run %d cannot reclassify %s -> %s", caseRun.ID, caseRun.Status, status))
	}
	ok, err := caseRuns.ReclassifyTerminal(ctx, caseRun.ID, caseRun.DispatchEpoch, caseRun.DispatchToken, func(run *execution.CaseRun) error {
		summary.Dispatch = run.Summary.Dispatch
		run.Status = status
		run.FailureClass = class
		run.Summary = summary
		if run.FinishedAt == nil {
			run.FinishedAt = &now
		}
		return nil
	})
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	if !ok {
		current, getErr := caseRuns.Get(ctx, caseRun.ID)
		if getErr != nil {
			return execution.CaseRun{}, false, mapExecutionError(getErr, "case_run")
		}
		return current, false, nil
	}
	reclassified, err := caseRuns.Get(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	return reclassified, true, nil
}

// aggregateWithEvidenceFacts supplies the persisted evidence facts to the
// single verdict authority. The evidence gate (constitution art.1.2) needs the
// live count BEFORE the verdict is computed; a count read failure is surfaced,
// never swallowed: a wrong count could let a no-evidence failed stand or degrade
// a real defect, so the terminal write aborts and the caller retries (503,
// retryable). A nil repository fails closed for the same reason.
func aggregateWithEvidenceFacts(
	ctx context.Context,
	evidence EvidenceRepository,
	caseRun execution.CaseRun,
	in execution.VerdictInput,
) (execution.CaseRunStatus, execution.FailureClass, execution.CaseRunSummary, error) {
	if evidence == nil {
		return "", "", execution.CaseRunSummary{}, platform.Unavailable("evidence repository is not configured")
	}
	count, err := evidence.CountByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return "", "", execution.CaseRunSummary{}, platform.Unavailable(
			fmt.Sprintf("count evidence for case_run %d: %s", caseRun.ID, err.Error()))
	}
	in.EvidenceCount = count
	in.EvidenceDropped = caseRun.EvidenceDropped
	return execution.Aggregate(in)
}
