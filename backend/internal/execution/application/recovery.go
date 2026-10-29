package application

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Crash-recovery constants (spec §5.4, §5.12 invariant 7).
const (
	// recoveryReasonRestart is stored on the interrupted case runs, the paused
	// batches and the released leases so the UI and the audit trail can tell a
	// crash/restart apart from a user pause.
	recoveryReasonRestart = "restart"
	// recoveryActorSystem is the actor recorded on the version pause mirror; a
	// boot recovery has no human actor.
	recoveryActorSystem = int64(0)
)

// ResidualLeaseScan is the optional scan behind Recovery.Sweep. The active-batch
// queue cannot see a batch that already reached a terminal status, yet its case
// runs may still hold a session lease whose release write was lost. The adapter
// scans the case_run rows that are not running and still carry a session_id and
// returns the distinct batch ids, without filtering on run_batch.status. It is
// optional so a composition root that cannot provide the scan still sweeps the
// active batches; see Recovery.Sweep.
type ResidualLeaseScan interface {
	ResidualLeaseBatchIDs(ctx context.Context) ([]int64, error)
}

// RecoveryDeps are the ports of the boot/restart recovery worker. Queue,
// Batches, CaseRuns, Steps, Versions and Sessions are required; Events, Clock
// and LeaseScan are optional. Queue is the scheduler's BatchQueue port, so
// recovery and the scheduler see the same active batches without a separate
// repository query.
//
// Recovery never talks to the browser executor: a crashed executor is left for
// its own restart cleanup, and no browser is started until a user resumes the
// paused batch (spec §5.4).
type RecoveryDeps struct {
	Queue    BatchQueue
	Batches  BatchRepository
	CaseRuns CaseRunRepository
	Steps    StepRepository
	Versions VersionPort
	Sessions SessionPort
	Events   EventPublisher
	Clock    Clock
	// LeaseScan optionally widens Sweep to terminal batches (see the type doc).
	LeaseScan ResidualLeaseScan
}

// Recovery restores the execution state after a process crash/restart and
// optionally sweeps stale session leases (spec §5.4, §5.12 invariant 7).
//
// The spec asks for one boot transaction, but the storage ports expose no
// shared transaction or unit-of-work port, so this is an accepted ordered-CAS
// deviation (Lead ruling): the sequence is ordered so a crash in the middle of
// recovery is itself recoverable, because every step is an idempotent
// conditional (compare-and-swap) write that the next pass re-applies. The order
// is:
//
//  1. every running case run of a running batch becomes interrupted with
//     cleanup_required=true and the seq of its in-flight step_result,
//  2. the session lease of that run is released (idempotent),
//  3. the batch tally is recomputed so it no longer counts the crashed runs as
//     running,
//  4. the batch is paused (immediate/restart) and keeps its running status,
//  5. the version pause mirror is written; the batch flag is authoritative, so
//     a crash between 4 and 5 is repaired by the next pass,
//  6. run.paused / batch.paused events are published for the writes above.
//
// Re-running Recover after a partial pass completes the missing work and is a
// no-op once the batch is paused and the version mirror is set. Publication is
// advisory and happens only after the write.
type Recovery struct {
	deps RecoveryDeps
	now  func() time.Time
}

// NewRecovery wires the recovery worker to its ports.
func NewRecovery(deps RecoveryDeps) *Recovery {
	now := time.Now
	if deps.Clock != nil {
		now = deps.Clock.Now
	}
	return &Recovery{deps: deps, now: now}
}

// Recover marks the leftovers of a previous process as interrupted and pauses
// their batches (spec §5.4 crash/restart row). It never acquires sessions,
// creates browser contexts or resumes execution: the user must resume the paused
// batch explicitly.
func (r *Recovery) Recover(ctx context.Context) error {
	if err := r.requireDeps(); err != nil {
		return err
	}
	batches, err := r.runningBatches(ctx)
	if err != nil {
		return err
	}

	now := r.now()
	for _, batch := range batches {
		if batch.Status != domain.BatchStatusRunning {
			continue
		}
		runs, err := r.listRunningCaseRuns(ctx, batch.ID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			interrupted, stepSeq, err := r.interruptRun(ctx, run, now)
			if err != nil {
				return err
			}
			if interrupted {
				r.publishRunPaused(ctx, run, stepSeq)
			}
		}
		// Recount even when this pass interrupted nothing: a previous pass may
		// have committed the interrupts and failed before the tally, and a
		// recount is idempotent and serialized on the batch row. A failure is
		// logged rather than fatal, like every other recount in this package;
		// the next pass (or the next finish) repairs it.
		if _, err := r.deps.Batches.UpdateCounts(ctx, batch.ID); err != nil {
			slog.WarnContext(ctx, "execution recovery recount batch failed",
				"batch_id", batch.ID, "error", err)
		}
		if batch.Paused {
			// A previous pass already paused the batch. The crash window between
			// SetPaused and MarkPaused leaves batch.paused=true with no version
			// mirror, so repair the mirror; never re-pause the batch and never
			// publish a second batch.paused.
			if err := r.mirrorVersionPause(ctx, batch, batch.PauseMode); err != nil {
				return err
			}
			continue
		}
		if err := r.pauseBatch(ctx, batch); err != nil {
			return err
		}
	}
	return nil
}

// Sweep releases session leases whose case_run holder is no longer running (a
// finished run whose release write was lost, or a stale holder left by a
// crashed worker) whatever the status of the owning batch. It inspects the
// active batches and, when the optional ResidualLeaseScan is wired, the batches
// that already left the active set (done/failed/cancelled), whose leftover
// leases the active-batch queue can no longer see. It is safe to call on a
// ticker and is idempotent: a release of an already released lease is a no-op in
// the resource context. It deliberately does not emit events or touch
// case-run/batch state.
func (r *Recovery) Sweep(ctx context.Context) error {
	if err := r.requireDeps(); err != nil {
		return err
	}
	batchIDs, err := r.sweepBatchIDs(ctx)
	if err != nil {
		return err
	}
	for _, batchID := range batchIDs {
		runs, err := r.listBatchCaseRuns(ctx, batchID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.Status == domain.CaseRunStatusRunning || run.SessionID == nil {
				continue
			}
			if err := r.deps.Sessions.Release(ctx, *run.SessionID,
				caseRunHolder(run.ID), recoveryReasonRestart); err != nil {
				return err
			}
		}
	}
	return nil
}

// sweepBatchIDs returns every batch the sweep must inspect: the active batches
// of the scheduler queue plus the ids of the optional terminal-batch scan,
// deduplicated. Without the optional scan the sweep still covers the active
// batches; a composition root that wants terminal-batch leftovers released must
// wire it.
func (r *Recovery) sweepBatchIDs(ctx context.Context) ([]int64, error) {
	batches, err := r.runningBatches(ctx)
	if err != nil {
		return nil, err
	}
	seen := make(map[int64]struct{}, len(batches))
	ids := make([]int64, 0, len(batches))
	for _, batch := range batches {
		if _, ok := seen[batch.ID]; ok {
			continue
		}
		seen[batch.ID] = struct{}{}
		ids = append(ids, batch.ID)
	}
	if r.deps.LeaseScan == nil {
		return ids, nil
	}
	residual, err := r.deps.LeaseScan.ResidualLeaseBatchIDs(ctx)
	if err != nil {
		return nil, err
	}
	for _, batchID := range residual {
		if _, ok := seen[batchID]; ok {
			continue
		}
		seen[batchID] = struct{}{}
		ids = append(ids, batchID)
	}
	return ids, nil
}

// interruptRun releases the run's lease, then stores the interrupted outcome
// with the restart reason, cleanup flag and in-flight step sequence. The lease is
// released first so a failure to finish the run leaves it running and the next
// pass retries the whole step. The returned bool reports whether this pass
// actually interrupted the run; a run finished concurrently by another writer is
// left untouched.
func (r *Recovery) interruptRun(ctx context.Context, run domain.CaseRun, now time.Time) (bool, *int, error) {
	stepSeq, err := r.runningStepSeq(ctx, run.ID)
	if err != nil {
		return false, nil, err
	}
	if run.SessionID != nil {
		if err := r.deps.Sessions.Release(ctx, *run.SessionID,
			caseRunHolder(run.ID), recoveryReasonRestart); err != nil {
			return false, nil, err
		}
	}

	finished := run
	finished.Status = domain.CaseRunStatusInterrupted
	finished.FailureClass = nil
	finished.FailureReason = recoveryReasonRestart
	finished.CleanupRequired = true
	finished.InterruptedStepSeq = stepSeq
	finished.FinishedAt = &now
	finished.DurationMS = recoveryDurationMS(run.StartedAt, now)
	finished.UpdatedAt = now

	if _, err := r.deps.CaseRuns.FinishCaseRun(ctx, finished); err != nil {
		if errors.Is(err, domain.ErrCaseRunTerminal) || errors.Is(err, ErrConflict) {
			// The run left running while recovery was working on it. Re-read to
			// tell a benign lost race from a real state conflict.
			current, readErr := r.deps.CaseRuns.GetCaseRun(ctx, run.BatchID, run.ID)
			if readErr != nil {
				return false, nil, readErr
			}
			if current.Status != domain.CaseRunStatusRunning {
				return false, nil, nil
			}
		}
		return false, nil, err
	}
	return true, stepSeq, nil
}

// runningStepSeq returns the seq of the in-flight step_result of a case run, or
// nil when the crash happened between steps. More than one running row is not a
// legal state; the highest seq is chosen deterministically if it happens.
func (r *Recovery) runningStepSeq(ctx context.Context, caseRunID int64) (*int, error) {
	steps, err := r.deps.Steps.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return nil, err
	}
	var seq *int
	for i := range steps {
		if steps[i].Status != domain.StepStatusRunning {
			continue
		}
		candidate := steps[i].Seq
		if seq == nil || candidate > *seq {
			seq = &candidate
		}
	}
	return seq, nil
}

// pauseBatch pauses one running batch, then mirrors the flag onto its version
// and publishes batch.paused (spec §5.4 crash/restart row). The batch write comes
// first because the batch flag is authoritative for dispatch: a crash between
// the two writes leaves a paused batch with no version mirror, which the next
// pass repairs through mirrorVersionPause, whereas mirroring first would leave a
// paused version unable to explain a still-running batch. A batch that reached a
// terminal status in the meantime (finished/cancelled) must not pause the version
// of a batch that no longer runs, so the mirror is skipped and the race is
// logged.
func (r *Recovery) pauseBatch(ctx context.Context, batch domain.RunBatch) error {
	updated, err := r.deps.Batches.SetPaused(ctx, batch.ID, true,
		domain.PauseModeImmediate, recoveryReasonRestart)
	if err != nil {
		switch {
		case errors.Is(err, domain.ErrBatchFinished):
			// Finished or cancelled between the active-batch scan and this
			// write: the batch no longer runs, so leave the version alone.
			slog.WarnContext(ctx, "execution recovery skips version pause mirror: batch is terminal",
				"version_id", batch.VersionID, "batch_id", batch.ID, "error", err)
			return nil
		case errors.Is(err, domain.ErrAlreadyPaused):
			// A concurrent writer paused it; only the mirror can be missing.
			current, readErr := r.deps.Batches.GetBatch(ctx, batch.VersionID, batch.ID)
			if readErr != nil {
				return readErr
			}
			return r.mirrorVersionPause(ctx, current, current.PauseMode)
		case errors.Is(err, ErrConflict):
			current, readErr := r.deps.Batches.GetBatch(ctx, batch.VersionID, batch.ID)
			if readErr != nil {
				return readErr
			}
			switch {
			case current.Paused:
				return r.mirrorVersionPause(ctx, current, current.PauseMode)
			case current.Status != domain.BatchStatusRunning:
				slog.WarnContext(ctx, "execution recovery skips version pause mirror: batch is terminal",
					"version_id", batch.VersionID, "batch_id", batch.ID, "status", current.Status)
				return nil
			}
		}
		return err
	}

	// No rollback on failure: the batch is paused, which is the state that stops
	// dispatch, and the next pass repairs the mirror from the batch flag.
	if err := r.mirrorVersionPause(ctx, updated, updated.PauseMode); err != nil {
		return err
	}

	r.publish(ctx, Event{
		Type:      "batch.paused",
		VersionID: batch.VersionID,
		BatchID:   batch.ID,
		Summary:   "batch paused on restart",
		Detail: map[string]any{
			"mode":     string(domain.PauseModeImmediate),
			"paused":   true,
			"reason":   recoveryReasonRestart,
			"actor_id": recoveryActorSystem,
		},
	})
	return nil
}

// mirrorVersionPause writes version.paused=true when the already-paused batch
// has no mirror yet: the crash window between BatchRepository.SetPaused and
// VersionPort.MarkPaused. The batch flag is authoritative, so a missing mirror is
// repaired with the batch's stored pause mode and the restart reason. It
// publishes nothing: the batch.paused event either already exists or is owned by
// the caller.
func (r *Recovery) mirrorVersionPause(ctx context.Context, batch domain.RunBatch, mode domain.PauseMode) error {
	version, err := r.deps.Versions.GetVersion(ctx, batch.VersionID)
	if err != nil {
		return err
	}
	if version.Paused {
		return nil
	}
	if mode == "" {
		mode = domain.PauseModeImmediate
	}
	return r.deps.Versions.MarkPaused(ctx, batch.VersionID, true, string(mode),
		recoveryReasonRestart, recoveryActorSystem)
}

// publishRunPaused emits run.paused after the interrupted run committed. The
// detail carries ids, the restart reason and the checkpoint pointer only.
func (r *Recovery) publishRunPaused(ctx context.Context, run domain.CaseRun, stepSeq *int) {
	detail := map[string]any{
		"status":           string(domain.CaseRunStatusInterrupted),
		"reason":           recoveryReasonRestart,
		"cleanup_required": true,
	}
	if stepSeq != nil {
		detail["interrupted_step_seq"] = *stepSeq
	}
	r.publish(ctx, Event{
		Type:      "run.paused",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case run interrupted on restart",
		Detail:    detail,
	})
}

// publish emits one event after the owning write. Publication failures are
// logged and swallowed: the database is authoritative and the event stream
// self-heals, while a crash recovery must not fail because of an advisory
// channel.
func (r *Recovery) publish(ctx context.Context, event Event) {
	if r.deps.Events == nil {
		return
	}
	if err := r.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "execution recovery event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"batch_id", event.BatchID,
			"error", err,
		)
	}
}

// runningBatches reads the batches left running by a previous process through
// the scheduler's active-batch queue. Planned batches have no crash artifact in
// P6 (Create writes a running batch) and are skipped by the callers.
func (r *Recovery) runningBatches(ctx context.Context) ([]domain.RunBatch, error) {
	return r.deps.Queue.ActiveBatches(ctx)
}

// listRunningCaseRuns scans every running case run of a batch.
func (r *Recovery) listRunningCaseRuns(ctx context.Context, batchID int64) ([]domain.CaseRun, error) {
	runs := make([]domain.CaseRun, 0)
	beforeID := int64(0)
	for {
		page, more, err := r.deps.CaseRuns.ListCaseRuns(ctx, batchID,
			domain.CaseRunStatusRunning, beforeID, caseRunPageSize)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page...)
		if !more || len(page) == 0 {
			return runs, nil
		}
		beforeID = page[len(page)-1].ID
	}
}

// listBatchCaseRuns scans every case run of a batch regardless of status; the
// periodic sweep needs the non-running holders too.
func (r *Recovery) listBatchCaseRuns(ctx context.Context, batchID int64) ([]domain.CaseRun, error) {
	runs := make([]domain.CaseRun, 0)
	beforeID := int64(0)
	for {
		page, more, err := r.deps.CaseRuns.ListCaseRuns(ctx, batchID, "", beforeID, caseRunPageSize)
		if err != nil {
			return nil, err
		}
		runs = append(runs, page...)
		if !more || len(page) == 0 {
			return runs, nil
		}
		beforeID = page[len(page)-1].ID
	}
}

// requireDeps rejects a mis-wired worker before it touches any row.
func (r *Recovery) requireDeps() error {
	switch {
	case r.deps.Queue == nil:
		return errors.New("execution: recovery batch queue is not configured")
	case r.deps.Batches == nil:
		return errors.New("execution: recovery batch repository is not configured")
	case r.deps.CaseRuns == nil:
		return errors.New("execution: recovery case-run repository is not configured")
	case r.deps.Steps == nil:
		return errors.New("execution: recovery step repository is not configured")
	case r.deps.Versions == nil:
		return errors.New("execution: recovery version port is not configured")
	case r.deps.Sessions == nil:
		return errors.New("execution: recovery session port is not configured")
	}
	return nil
}

// recoveryDurationMS reports how long the crashed run had been executing.
func recoveryDurationMS(started *time.Time, now time.Time) int64 {
	if started == nil || now.Before(*started) {
		return 0
	}
	return now.Sub(*started).Milliseconds()
}
