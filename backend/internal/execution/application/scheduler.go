package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Scheduler defaults and stable identifiers (spec §5.3, §5.4).
const (
	// DefaultMaxConcurrentCases is the concurrency cap used when
	// EZTEST_EXEC_MAX_CONCURRENT_CASES is not configured (spec §5.3).
	DefaultMaxConcurrentCases = 3
	// MaxConcurrentCasesLimit is the upper bound of the concurrency cap.
	MaxConcurrentCasesLimit = 8
	// DefaultSchedulerTick is the scheduler wake-up interval (spec §5.3).
	DefaultSchedulerTick = 2 * time.Second
	// DefaultCaseLeaseTTL is the session lease lifetime handed to
	// SessionPort.Acquire for one case run.
	DefaultCaseLeaseTTL = 30 * time.Minute
	// envMaxConcurrentCases overrides DefaultMaxConcurrentCases.
	envMaxConcurrentCases = "EZTEST_EXEC_MAX_CONCURRENT_CASES"

	// gateEventHumanRequested, gateEventPending and gateEventResolved are the
	// version-channel event types of a manual takeover gate (spec §5.10).
	gateEventHumanRequested = "human.requested"
	gateEventPending        = "gate.pending"
	gateEventResolved       = "gate.resolved"
)

// Session outcome sentinels of the SessionPort adapter (spec §5.3, §5.9).
//
// The execution context never drives the resource state machine itself; it
// only interprets the adapter's errors:
//
//   - ErrSessionBusy means another case holds the only usable slot. The case
//     stays pending and is retried on a later tick; it is not a failure.
//   - ErrSessionUnavailable means no session can be leased (no valid session,
//     no eligible auto-login). The case is blocked(session, no_session).
//   - ErrSessionNeedsManual means auto-login reached MFA/captcha/approval. The
//     case is blocked(session) and the adapter should return the waiting
//     session id in SessionRef.SessionID so the gate scan can surface it.
var (
	// ErrSessionBusy reports that no session slot is currently free.
	ErrSessionBusy = errors.New("execution: session is busy")
	// ErrSessionUnavailable reports that no session can be leased.
	ErrSessionUnavailable = errors.New("execution: no session available")
	// ErrSessionNeedsManual reports that a human takeover is required.
	ErrSessionNeedsManual = errors.New("execution: session needs manual takeover")
)

// BatchQueue enumerates the batches a scheduler tick may dispatch. The
// production adapter lists the planned/running batches (P6 is single-process,
// spec §5.3); tests use an in-memory queue. A scheduled batch with no pending
// run is harmless, so the adapter does not need to be sorted.
type BatchQueue interface {
	ActiveBatches(ctx context.Context) ([]domain.RunBatch, error)
}

// SchedulerDeps are the ports of the execution scheduler. Every dependency is
// required except Mindmap and Events.
type SchedulerDeps struct {
	Queue    BatchQueue
	Batches  BatchRepository
	CaseRuns CaseRunRepository
	Versions VersionPort
	Sessions SessionPort
	Mindmap  MindmapPort
	Events   EventPublisher
	Clock    Clock
	IDs      IDGenerator
	// Runner executes one dispatched case run. It is required.
	Runner *CaseRunner
	// Browser is the controller of the tools' per-case browser context. It is
	// propagated to the runner when the runner was built without one, so the
	// composition root can wire the tool set once at the scheduler level. The
	// runner must not create a context of its own.
	Browser CaseBrowserController
	// MaxConcurrent overrides DefaultMaxConcurrentCases. The effective value is
	// clamped to 1..MaxConcurrentCasesLimit.
	MaxConcurrent int
	// Tick overrides DefaultSchedulerTick.
	Tick time.Duration
	// LeaseTTL overrides DefaultCaseLeaseTTL.
	LeaseTTL time.Duration
}

// Scheduler dispatches pending case runs of the active batches: it acquires a
// session per case, starts the case runner, and drives the batch-level side
// effects (takeover gates, interrupted requeue and batch completion). It is
// deliberately single-instance (spec §5.3); multi-instance dispatch needs a
// distributed lease that P8 owns.
type Scheduler struct {
	deps     SchedulerDeps
	now      func() time.Time
	tick     time.Duration
	leaseTTL time.Duration
	notify   chan struct{}

	mu     sync.Mutex
	active map[int64]struct{}
	gates  map[int64]int64
	wg     sync.WaitGroup
}

// NewScheduler wires the scheduler. A zero MaxConcurrent reads
// EZTEST_EXEC_MAX_CONCURRENT_CASES and otherwise falls back to
// DefaultMaxConcurrentCases; values outside 1..8 are clamped (spec §5.3).
func NewScheduler(deps SchedulerDeps) *Scheduler {
	if deps.Clock == nil {
		deps.Clock = wallClock{}
	}
	if deps.IDs == nil {
		deps.IDs = defaultIDGenerator()
	}
	now := deps.Clock.Now
	maxConcurrent := deps.MaxConcurrent
	if maxConcurrent <= 0 {
		maxConcurrent = configuredConcurrency()
	}
	if maxConcurrent < 1 {
		maxConcurrent = 1
	}
	if maxConcurrent > MaxConcurrentCasesLimit {
		maxConcurrent = MaxConcurrentCasesLimit
	}
	deps.MaxConcurrent = maxConcurrent
	tick := deps.Tick
	if tick <= 0 {
		tick = DefaultSchedulerTick
	}
	leaseTTL := deps.LeaseTTL
	if leaseTTL <= 0 {
		leaseTTL = DefaultCaseLeaseTTL
	}
	if deps.Runner != nil && deps.Runner.deps.Browser == nil {
		deps.Runner.deps.Browser = deps.Browser
	}
	return &Scheduler{
		deps:     deps,
		now:      now,
		tick:     tick,
		leaseTTL: leaseTTL,
		notify:   make(chan struct{}, 1),
		active:   make(map[int64]struct{}),
		gates:    make(map[int64]int64),
	}
}

// Start runs the dispatch loop until ctx is cancelled. It is intended to be
// started once by the composition root (for example with `go scheduler.Start`).
func (s *Scheduler) Start(ctx context.Context) {
	ticker := time.NewTicker(s.tick)
	defer ticker.Stop()

	for {
		if err := s.RunOnce(ctx); err != nil && ctx.Err() == nil {
			slog.WarnContext(ctx, "execution scheduler tick failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.notify:
		}
	}
}

// Notify wakes the dispatch loop without blocking. It is called after a case
// run finishes so the next pending case can start immediately instead of
// waiting for the next tick.
func (s *Scheduler) Notify() {
	if s == nil {
		return
	}
	select {
	case s.notify <- struct{}{}:
	default:
	}
}

// Wait blocks until every dispatched case runner has returned. It is used by
// tests and by a graceful shutdown path; it does not stop new dispatches.
func (s *Scheduler) Wait() {
	s.wg.Wait()
}

// RunOnce performs one scheduling pass over every active batch: takeover gate
// scanning, interrupted requeue, pending dispatch up to the concurrency cap and
// batch completion. It never waits for a case run to finish.
func (s *Scheduler) RunOnce(ctx context.Context) error {
	if s == nil || s.deps.Queue == nil {
		return errors.New("execution: scheduler batch queue is not configured")
	}
	batches, err := s.deps.Queue.ActiveBatches(ctx)
	if err != nil {
		return fmt.Errorf("execution: list active batches: %w", err)
	}

	var firstErr error
	for i := range batches {
		if err := ctx.Err(); err != nil {
			return err
		}
		if batchErr := s.scheduleBatch(ctx, batches[i]); batchErr != nil {
			slog.WarnContext(ctx, "execution scheduler batch failed",
				"batch_id", batches[i].ID, "version_id", batches[i].VersionID, "error", batchErr)
			if firstErr == nil {
				firstErr = batchErr
			}
		}
	}
	return firstErr
}

// scheduleBatch advances one active batch by one tick.
func (s *Scheduler) scheduleBatch(ctx context.Context, batch domain.RunBatch) error {
	if !isActiveBatchStatus(batch.Status) {
		return nil
	}
	if s.deps.Versions == nil {
		return errors.New("execution: version port is not configured")
	}
	version, err := s.deps.Versions.GetVersion(ctx, batch.VersionID)
	if err != nil {
		return fmt.Errorf("execution: read version %d: %w", batch.VersionID, err)
	}
	// A pause writes the batch first and mirrors the version afterwards, so the
	// batch flag is authoritative; the version mirror is checked too so a
	// partially failed pause cannot resume dispatch (spec §5.4).
	paused := batch.Paused || version.Paused

	if gateErr := s.scanTakeoverGates(ctx, batch, paused); gateErr != nil {
		slog.WarnContext(ctx, "execution takeover scan failed",
			"batch_id", batch.ID, "error", gateErr)
	}
	if paused {
		return nil
	}
	if err := s.requeueInterrupted(ctx, batch); err != nil {
		return err
	}
	if err := s.dispatchPending(ctx, batch, version); err != nil {
		return err
	}
	return s.completeBatchIfFinished(ctx, batch)
}

// dispatchPending leases a session and starts a case run for each free
// concurrency slot, in the repository's deterministic priority order
// (P0..P3, then case_node_id ascending).
func (s *Scheduler) dispatchPending(ctx context.Context, batch domain.RunBatch, version VersionRef) error {
	if s.deps.Runner == nil {
		return errors.New("execution: case runner is not configured")
	}
	if s.deps.CaseRuns == nil || s.deps.Sessions == nil {
		return errors.New("execution: scheduler dispatch dependencies are not configured")
	}
	if version.SystemID == nil || version.EnvironmentID == nil {
		return nil
	}
	environment, err := s.deps.Sessions.Environment(ctx, *version.EnvironmentID)
	if err != nil {
		return fmt.Errorf("execution: read environment %d: %w", *version.EnvironmentID, err)
	}

	for s.availableSlots() > 0 {
		run, err := s.deps.CaseRuns.NextPending(ctx, batch.ID)
		if err != nil {
			if errors.Is(err, ErrNoPendingCaseRun) {
				return nil
			}
			return fmt.Errorf("execution: next pending case run: %w", err)
		}
		if s.isRunActive(run.ID) {
			// A graceful pause parks the row as pending after releasing its
			// resources but before the previous runner returns. Dispatching it here
			// would start a second runner for the same attempt; the old runner's
			// Notify wakes the next tick once it is gone.
			return nil
		}

		session, err := s.deps.Sessions.Acquire(ctx, *version.EnvironmentID, *version.SystemID,
			caseRunHolder(run.ID), s.leaseTTL)
		if err != nil {
			switch {
			case errors.Is(err, ErrSessionBusy):
				// The case waits in pending without consuming a failure slot or
				// an agent run (spec §5.3).
				return nil
			case errors.Is(err, ErrSessionNeedsManual):
				if blockErr := s.blockRun(ctx, batch, run, toolCodeSessionInvalid, "takeover_required", session.SessionID); blockErr != nil {
					return blockErr
				}
				continue
			case errors.Is(err, ErrSessionUnavailable):
				if blockErr := s.blockRun(ctx, batch, run, toolCodeSessionInvalid, "no_session", 0); blockErr != nil {
					return blockErr
				}
				continue
			default:
				// Infrastructure failures must not be recorded as a case
				// verdict; the run stays pending for the next tick.
				return fmt.Errorf("execution: acquire session for case run %d: %w", run.ID, err)
			}
		}

		started, err := s.startRun(ctx, batch, run, session)
		if err != nil {
			_ = s.deps.Sessions.Release(context.WithoutCancel(ctx), session.SessionID,
				caseRunHolder(run.ID), "start_failed")
			if errors.Is(err, domain.ErrCaseRunTerminal) || errors.Is(err, ErrConflict) {
				// A concurrent pause/cancel/finish won the run; skip it.
				continue
			}
			return err
		}
		s.launch(ctx, started, session, environment)
	}
	return nil
}

// startRun moves a pending run to running and pins the acquired session ids.
// It re-reads the run before the session write so a concurrent pause/cancel that
// already moved the row is not overwritten with a stale running status.
func (s *Scheduler) startRun(ctx context.Context, batch domain.RunBatch, run domain.CaseRun, session SessionRef) (domain.CaseRun, error) {
	now := s.now()
	if _, err := s.deps.CaseRuns.SetStatus(ctx, run.ID, domain.CaseRunStatusPending, domain.CaseRunStatusRunning, now); err != nil {
		return domain.CaseRun{}, fmt.Errorf("execution: start case run %d: %w", run.ID, err)
	}
	current, err := s.deps.CaseRuns.GetCaseRun(ctx, batch.ID, run.ID)
	if err != nil {
		return domain.CaseRun{}, fmt.Errorf("execution: read started case run %d: %w", run.ID, err)
	}
	if current.Status != domain.CaseRunStatusRunning {
		return current, fmt.Errorf("%w: case run %d is %s, not running", ErrConflict, run.ID, current.Status)
	}
	current.SessionID = int64Pointer(session.SessionID)
	current.SessionSnapshotID = int64Pointer(session.SnapshotID)
	current.UpdatedAt = now

	stored, err := s.deps.CaseRuns.UpdateCaseRun(ctx, current)
	if err != nil {
		// The status transition already committed; the runner can still persist
		// the agent ids later, so a failed session column write is logged but
		// does not abort the run.
		slog.WarnContext(ctx, "execution: pin case run session failed",
			"case_run_id", current.ID, "batch_id", batch.ID, "error", err)
		return current, nil
	}
	return stored, nil
}

// launch starts one case runner goroutine and counts it against the cap.
func (s *Scheduler) launch(ctx context.Context, run domain.CaseRun, session SessionRef, environment EnvironmentRef) {
	s.mu.Lock()
	s.active[run.ID] = struct{}{}
	s.mu.Unlock()

	s.publish(ctx, Event{
		Type:      "run.started",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case run started",
		Detail: map[string]any{
			"case_run_id": eventID(run.ID),
			"attempt":     run.Attempt,
			"status":      string(domain.CaseRunStatusRunning),
		},
		Refs: EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer func() {
			s.mu.Lock()
			delete(s.active, run.ID)
			s.mu.Unlock()
			s.Notify()
		}()
		s.deps.Runner.Run(ctx, run, session, environment)
	}()
}

// blockRun records a case that cannot get a session as blocked for the given
// class and keeps the batch going. FinishCaseRun requires a running row, so the
// run is transitioned through running even though no agent ever ran.
func (s *Scheduler) blockRun(ctx context.Context, batch domain.RunBatch, run domain.CaseRun, toolCode, reason string, sessionID int64) error {
	now := s.now()
	running, err := s.deps.CaseRuns.SetStatus(ctx, run.ID,
		domain.CaseRunStatusPending, domain.CaseRunStatusRunning, now)
	if err != nil {
		if errors.Is(err, domain.ErrCaseRunTerminal) || errors.Is(err, ErrConflict) {
			return nil
		}
		return fmt.Errorf("execution: mark case run %d running before blocking: %w", run.ID, err)
	}

	verdict, class, _ := domain.Classify(toolCode, "", 0)
	finished := running
	finished.Status = caseRunStatusForVerdict(verdict)
	finished.FailureClass = failureClassPointer(class)
	finished.FailureReason = reason
	finished.FinishedAt = timePointer(now)
	finished.UpdatedAt = now
	if running.StartedAt != nil {
		finished.DurationMS = now.Sub(*running.StartedAt).Milliseconds()
	}
	if sessionID > 0 {
		finished.SessionID = int64Pointer(sessionID)
	}

	stored, err := s.deps.CaseRuns.FinishCaseRun(ctx, finished)
	if err != nil {
		return fmt.Errorf("execution: block case run %d: %w", run.ID, err)
	}
	s.afterTerminal(ctx, stored)
	return nil
}

// scanTakeoverGates publishes human.requested/gate.pending once per waiting
// takeover, resolves the gate once a waiting takeover is gone, and requeues a
// blocked(session) case once its session is usable again (spec §5.9). Only the
// latest attempt of a case is requeued, so repeated ticks are idempotent. A
// session conflict leaves the case pending on a later tick; it never counts as a
// failure.
func (s *Scheduler) scanTakeoverGates(ctx context.Context, batch domain.RunBatch, paused bool) error {
	if s.deps.CaseRuns == nil || s.deps.Sessions == nil {
		return nil
	}
	runs, err := s.listCaseRuns(ctx, batch.ID, domain.CaseRunStatusBlocked)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	latestAttempt, retried, err := s.caseAttemptIndex(ctx, batch.ID)
	if err != nil {
		return err
	}
	for _, run := range runs {
		if run.FailureClass == nil || *run.FailureClass != domain.FailureClassSession {
			continue
		}
		if run.SessionID == nil {
			// No session row exists (manual import / missing recipe); there is
			// no takeover to poll and the admin-driven rerun owns recovery.
			continue
		}
		takeover, err := s.deps.Sessions.PendingTakeover(ctx, *run.SessionID)
		if err != nil {
			slog.WarnContext(ctx, "execution: pending takeover read failed",
				"case_run_id", run.ID, "session_id", *run.SessionID, "error", err)
			continue
		}
		if takeover != nil {
			if s.shouldEmitGate(run.ID, takeover.ID) {
				s.publishGate(ctx, run, takeover)
			}
			continue
		}
		// The waiting takeover is gone (completed or cancelled). Resolve the gate
		// exactly once even when a pause or a still-busy session delays the
		// requeue to a later tick.
		if paused || batch.Paused {
			s.publishGateResolved(ctx, run, "takeover_cancelled", 0)
			continue
		}
		if !isLatestCaseAttempt(latestAttempt, retried, run) {
			s.publishGateResolved(ctx, run, "takeover_cancelled", 0)
			continue
		}
		created, newCaseRunID, err := s.requeueBlockedSession(ctx, batch, run)
		if err != nil {
			slog.WarnContext(ctx, "execution: requeue blocked case run failed",
				"case_run_id", run.ID, "error", err)
			continue
		}
		reason := "takeover_cancelled"
		if created {
			reason = "takeover_completed"
		}
		s.publishGateResolved(ctx, run, reason, newCaseRunID)
	}
	return nil
}

// caseAttemptIndex reports the latest attempt per case node and the runs that
// already produced a retry, so a blocked row is requeued at most once.
func (s *Scheduler) caseAttemptIndex(ctx context.Context, batchID int64) (map[int64]int, map[int64]struct{}, error) {
	runs, err := s.listCaseRuns(ctx, batchID, "")
	if err != nil {
		return nil, nil, err
	}
	latest := make(map[int64]int, len(runs))
	retried := make(map[int64]struct{}, len(runs))
	for _, run := range runs {
		if run.RetryOfCaseRunID != nil {
			retried[*run.RetryOfCaseRunID] = struct{}{}
		}
		if attempt, ok := latest[run.CaseNodeID]; !ok || run.Attempt > attempt {
			latest[run.CaseNodeID] = run.Attempt
		}
	}
	return latest, retried, nil
}

// isLatestCaseAttempt reports whether run is the newest attempt of its case and
// has not already produced a retry.
func isLatestCaseAttempt(latest map[int64]int, retried map[int64]struct{}, run domain.CaseRun) bool {
	if _, ok := retried[run.ID]; ok {
		return false
	}
	return latest[run.CaseNodeID] == run.Attempt
}

// publishGate emits the two gate events of a waiting takeover.
func (s *Scheduler) publishGate(ctx context.Context, run domain.CaseRun, takeover *TakeoverRef) {
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"session_id":  eventIDPointer(run.SessionID),
		"takeover_id": eventID(takeover.ID),
		"reason":      takeover.Reason,
	}
	if takeover.ExpiresAt != nil {
		detail["expires_at"] = takeover.ExpiresAt.UTC().Format(time.RFC3339Nano)
	}
	s.publish(ctx, Event{
		Type:      gateEventHumanRequested,
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "human takeover requested",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
	s.publish(ctx, Event{
		Type:      gateEventPending,
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "gate pending",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
}

// publishGateResolved emits gate.resolved once for a gate that was announced and
// whose waiting takeover is gone. A case whose gate was never announced has
// nothing to resolve and stays silent.
func (s *Scheduler) publishGateResolved(ctx context.Context, run domain.CaseRun, reason string, newCaseRunID int64) {
	takeoverID, ok := s.consumeGate(run.ID)
	if !ok {
		return
	}
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"session_id":  eventIDPointer(run.SessionID),
		"reason":      reason,
	}
	if takeoverID > 0 {
		detail["takeover_id"] = eventID(takeoverID)
	}
	if newCaseRunID > 0 {
		detail["new_case_run_id"] = eventID(newCaseRunID)
	}
	s.publish(ctx, Event{
		Type:      gateEventResolved,
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "gate resolved",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
}

// consumeGate removes and returns the announced takeover id of a case run.
func (s *Scheduler) consumeGate(caseRunID int64) (int64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	takeoverID, ok := s.gates[caseRunID]
	if !ok {
		return 0, false
	}
	delete(s.gates, caseRunID)
	return takeoverID, true
}

// shouldEmitGate reports whether the takeover id is new for the case run.
func (s *Scheduler) shouldEmitGate(caseRunID, takeoverID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.gates[caseRunID] == takeoverID {
		return false
	}
	s.gates[caseRunID] = takeoverID
	return true
}

// requeueBlockedSession creates a new attempt of a blocked(session) case after
// its takeover completed or its session became valid again. It probes the
// session with Acquire because the resource port deliberately exposes no raw
// session state; the probe lease is released immediately and the runner
// acquires its own (spec §5.9). It reports whether a new attempt was created.
func (s *Scheduler) requeueBlockedSession(ctx context.Context, batch domain.RunBatch, run domain.CaseRun) (bool, int64, error) {
	if s.deps.Versions == nil || s.deps.CaseRuns == nil || s.deps.IDs == nil {
		return false, 0, nil
	}
	version, err := s.deps.Versions.GetVersion(ctx, batch.VersionID)
	if err != nil {
		return false, 0, err
	}
	if version.SystemID == nil || version.EnvironmentID == nil {
		return false, 0, nil
	}
	holder := caseRunHolder(run.ID) + ":requeue"
	session, err := s.deps.Sessions.Acquire(ctx, *version.EnvironmentID, *version.SystemID, holder, s.leaseTTL)
	if err != nil {
		switch {
		case errors.Is(err, ErrSessionBusy), errors.Is(err, ErrSessionUnavailable),
			errors.Is(err, ErrSessionNeedsManual):
			return false, 0, nil
		default:
			return false, 0, err
		}
	}
	releaseCtx := context.WithoutCancel(ctx)
	_ = s.deps.Sessions.Release(releaseCtx, session.SessionID, holder, "requeue")

	id, err := nextID(s.deps.IDs)
	if err != nil {
		return false, 0, err
	}
	now := s.now()
	retryOf := run.ID
	attempt := domain.CaseRun{
		ID:               id,
		BatchID:          run.BatchID,
		VersionID:        run.VersionID,
		CaseNodeID:       run.CaseNodeID,
		CaseRevision:     run.CaseRevision,
		CaseHash:         run.CaseHash,
		Snapshot:         cloneSnapshot(run.Snapshot),
		Attempt:          run.Attempt + 1,
		Status:           domain.CaseRunStatusPending,
		RetryOfCaseRunID: &retryOf,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if _, err := s.deps.CaseRuns.CreateCaseRun(ctx, attempt); err != nil {
		return false, 0, fmt.Errorf("execution: create requeued attempt for case run %d: %w", run.ID, err)
	}

	s.Notify()
	return true, attempt.ID, nil
}

// requeueInterrupted returns interrupted runs of a resumed batch to pending so
// the same attempt continues from its checkpoint with cleanup_required set
// (spec §5.4 invariant 5). The interrupted step and cleanup flag are preserved.
func (s *Scheduler) requeueInterrupted(ctx context.Context, batch domain.RunBatch) error {
	if s.deps.CaseRuns == nil {
		return nil
	}
	runs, err := s.listCaseRuns(ctx, batch.ID, domain.CaseRunStatusInterrupted)
	if err != nil {
		return err
	}
	now := s.now()
	for _, run := range runs {
		if _, err := s.deps.CaseRuns.SetStatus(ctx, run.ID,
			domain.CaseRunStatusInterrupted, domain.CaseRunStatusPending, now); err != nil {
			if errors.Is(err, domain.ErrCaseRunTerminal) || errors.Is(err, ErrConflict) {
				continue
			}
			return fmt.Errorf("execution: requeue interrupted case run %d: %w", run.ID, err)
		}
		s.publish(ctx, Event{
			Type:      "run.resumed",
			VersionID: run.VersionID,
			BatchID:   run.BatchID,
			CaseID:    run.CaseNodeID,
			Summary:   "case run requeued from checkpoint",
			Detail: map[string]any{
				"case_run_id":      eventID(run.ID),
				"attempt":          run.Attempt,
				"cleanup_required": run.CleanupRequired,
			},
			Refs: EventRefs{CaseIDs: []int64{run.CaseNodeID}},
		})
	}
	return nil
}

// completeBatchIfFinished settles a batch once every case run is terminal: it
// recounts, completes the version and marks the batch done (spec §5.4, §5.12
// invariant 1). A paused batch keeps its active slot until it is resumed.
func (s *Scheduler) completeBatchIfFinished(ctx context.Context, batch domain.RunBatch) error {
	if s.deps.CaseRuns == nil || s.deps.Batches == nil {
		return nil
	}
	runs, err := s.listCaseRuns(ctx, batch.ID, "")
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}
	for _, run := range runs {
		if !isTerminalCaseRun(run.Status) {
			return nil
		}
	}
	if batch.Paused {
		return nil
	}
	// A waiting human takeover keeps the batch active: the gate worker may
	// still requeue a blocked case as a new attempt and the batch must not be
	// completed underneath it (spec §5.9).
	if s.hasWaitingTakeover(ctx, runs) {
		return nil
	}

	counts, err := s.deps.Batches.UpdateCounts(ctx, batch.ID)
	if err != nil {
		return fmt.Errorf("execution: recount batch %d: %w", batch.ID, err)
	}
	// The batch CAS is the completion gate and comes first: if a concurrent
	// cancel moved the batch out of running, the CAS loses and the version must
	// stay executing (spec §5.12 invariant 10). The reverse order could leave a
	// completed version behind a cancelled batch.
	now := s.now()
	finished, err := s.deps.Batches.UpdateBatchStatus(ctx, batch.ID, batch.Status,
		domain.BatchStatusDone, timePointer(now))
	if err != nil {
		return fmt.Errorf("execution: finish batch %d: %w", batch.ID, err)
	}
	if s.deps.Versions != nil {
		if err := s.deps.Versions.CompleteExecution(ctx, batch.VersionID, 0); err != nil {
			// The batch is still finished; a version that cannot complete (for
			// example it was manually changed) must not leave the batch stuck.
			slog.WarnContext(ctx, "execution: complete version failed",
				"version_id", batch.VersionID, "batch_id", batch.ID, "error", err)
		}
	}
	s.publish(ctx, Event{
		Type:      "batch.finished",
		VersionID: finished.VersionID,
		BatchID:   finished.ID,
		Summary:   "batch finished",
		Detail: map[string]any{
			"status": string(finished.Status),
			"counts": eventCounts(counts),
		},
	})
	return nil
}

// hasWaitingTakeover reports whether any blocked(session) run still has a
// waiting human takeover. Such a run is terminal on the case but the batch must
// stay active until the gate resolves or is cancelled.
func (s *Scheduler) hasWaitingTakeover(ctx context.Context, runs []domain.CaseRun) bool {
	if s.deps.Sessions == nil {
		return false
	}
	for _, run := range runs {
		if run.Status != domain.CaseRunStatusBlocked || run.SessionID == nil {
			continue
		}
		if run.FailureClass == nil || *run.FailureClass != domain.FailureClassSession {
			continue
		}
		takeover, err := s.deps.Sessions.PendingTakeover(ctx, *run.SessionID)
		if err != nil {
			slog.WarnContext(ctx, "execution: pending takeover read failed",
				"case_run_id", run.ID, "error", err)
			return true
		}
		if takeover != nil {
			return true
		}
	}
	return false
}

// afterTerminal applies the batch-entry side effects of a case run the
// scheduler itself finished (blocked): mark the case node executed, recount and
// publish the finish events.
func (s *Scheduler) afterTerminal(ctx context.Context, run domain.CaseRun) {
	if s.deps.Mindmap != nil {
		if err := s.deps.Mindmap.MarkExecuted(context.WithoutCancel(ctx), run.VersionID,
			[]int64{run.CaseNodeID}); err != nil {
			slog.WarnContext(ctx, "execution: mark case node executed failed",
				"case_node_id", run.CaseNodeID, "error", err)
		}
	}
	if s.deps.Batches != nil {
		if _, err := s.deps.Batches.UpdateCounts(ctx, run.BatchID); err != nil {
			slog.WarnContext(ctx, "execution: recount batch failed",
				"batch_id", run.BatchID, "error", err)
		}
	}
	s.publishCaseFinished(ctx, run)
	s.Notify()
}

// publishCaseFinished emits run.finished and case.status_changed after a
// terminal write committed.
func (s *Scheduler) publishCaseFinished(ctx context.Context, run domain.CaseRun) {
	detail := map[string]any{
		"case_run_id": eventID(run.ID),
		"status":      string(run.Status),
		"attempt":     run.Attempt,
	}
	if run.FailureClass != nil {
		detail["failure_class"] = string(*run.FailureClass)
	}
	if run.FailureReason != "" {
		detail["failure_reason"] = run.FailureReason
	}
	s.publish(ctx, Event{
		Type:      "run.finished",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case run finished",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
	s.publish(ctx, Event{
		Type:      "case.status_changed",
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		CaseID:    run.CaseNodeID,
		Summary:   "case status changed",
		Detail:    detail,
		Refs:      EventRefs{CaseIDs: []int64{run.CaseNodeID}},
	})
}

// listCaseRuns scans every page of a batch's runs, optionally filtered by
// status.
func (s *Scheduler) listCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus) ([]domain.CaseRun, error) {
	const pageSize = 200
	all := make([]domain.CaseRun, 0)
	beforeID := int64(0)
	for {
		page, more, err := s.deps.CaseRuns.ListCaseRuns(ctx, batchID, status, beforeID, pageSize)
		if err != nil {
			return nil, fmt.Errorf("execution: list case runs: %w", err)
		}
		all = append(all, page...)
		if !more || len(page) == 0 {
			return all, nil
		}
		beforeID = page[len(page)-1].ID
	}
}

// availableSlots reports the free execution slots under the concurrency cap.
func (s *Scheduler) availableSlots() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.deps.MaxConcurrent - len(s.active)
}

// isRunActive reports whether this scheduler still counts a runner for the run.
func (s *Scheduler) isRunActive(caseRunID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.active[caseRunID]
	return ok
}

// publish emits one event after the committed write. Publication failures are
// logged and swallowed: the API is authoritative and the stream self-heals.
func (s *Scheduler) publish(ctx context.Context, event Event) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "execution event publish failed",
			"type", event.Type,
			"version_id", event.VersionID,
			"batch_id", event.BatchID,
			"error", err,
		)
	}
}

// configuredConcurrency reads and clamps EZTEST_EXEC_MAX_CONCURRENT_CASES.
func configuredConcurrency() int {
	raw := os.Getenv(envMaxConcurrentCases)
	if raw == "" {
		return DefaultMaxConcurrentCases
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return DefaultMaxConcurrentCases
	}
	if value > MaxConcurrentCasesLimit {
		return MaxConcurrentCasesLimit
	}
	return value
}

// isActiveBatchStatus reports whether a batch still owns the version's active
// slot.
func isActiveBatchStatus(status domain.BatchStatus) bool {
	return status == domain.BatchStatusPlanned || status == domain.BatchStatusRunning
}

// caseRunHolder is the lease holder identity of one case run. Acquire and
// Release must use the same value.
func caseRunHolder(caseRunID int64) string {
	return fmt.Sprintf("execution:case_run:%d", caseRunID)
}

// eventID formats one execution id for an event Detail payload. D29 requires
// every execution id to be a decimal string wherever it appears in JSON, so
// Detail values never rely on the JSON number type.
func eventID(id int64) string { return strconv.FormatInt(id, 10) }

// eventIDPointer formats an optional execution id; a nil id stays nil.
func eventIDPointer(id *int64) any {
	if id == nil {
		return nil
	}
	return eventID(*id)
}

// int64Pointer returns a pointer to a copy of value.
func int64Pointer(value int64) *int64 { return &value }

// timePointer returns a pointer to a copy of value.
func timePointer(value time.Time) *time.Time { return &value }

// failureClassPointer returns a pointer to a copy of value.
func failureClassPointer(value domain.FailureClass) *domain.FailureClass {
	copied := value
	return &copied
}

// wallClock is the default Clock.
type wallClock struct{}

// Now implements Clock.
func (wallClock) Now() time.Time { return time.Now() }

// toolCodeSessionInvalid is the classification input that maps a session
// problem to blocked(session) (spec §5.8).
const toolCodeSessionInvalid = "session_invalid"
