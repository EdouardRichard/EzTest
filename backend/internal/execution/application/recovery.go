package executionapp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// humanExpirer is the human takeover expiry step used by recovery.
type humanExpirer interface {
	Expire(ctx context.Context, now time.Time) (int, error)
}

// Recovery is the P3 execution recovery sweep (P3 §5.5/§4.9/§6.9): executor
// offline, expired leases, overdue human requests, stale cases and executor
// context TTL/orphans. It never bulk-interrupts running case_runs because the
// API restarted (P1 rule).
type Recovery struct {
	intents     IntentRepository
	waves       WaveRepository
	caseRuns    CaseRunRepository
	humans      humanExpirer
	executors   ExecutorRepository
	contexts    SUTContextRepository
	cases       CaseReader
	workQueue   WorkQueue
	canceller   RunCanceller
	executor    ExecutorControl
	replanner   SupersededIntentReplanner
	clock       Clock
	events      EventPublisher
	audit       AuditRecorder
	maxAttempts int
	contextTTL  time.Duration
}

// RecoveryDeps wires the recovery sweep. humans/executor/canceller may be nil.
type RecoveryDeps struct {
	Intents     IntentRepository
	Waves       WaveRepository
	CaseRuns    CaseRunRepository
	Humans      humanExpirer
	Executors   ExecutorRepository
	Contexts    SUTContextRepository
	Cases       CaseReader
	WorkQueue   WorkQueue
	Canceller   RunCanceller
	Executor    ExecutorControl
	Clock       Clock
	Events      EventPublisher
	Audit       AuditRecorder
	MaxAttempts int
	ContextTTL  time.Duration
	// Replanner is the Task 106 stale-intent re-plan (implemented by
	// *IntentService). nil keeps the P3-only retire behavior.
	Replanner SupersededIntentReplanner
}

// NewRecovery builds the recovery sweep.
func NewRecovery(deps RecoveryDeps) *Recovery {
	maxAttempts := deps.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = execution.DefaultMaxDispatchAttempts
	}
	ttl := deps.ContextTTL
	if ttl <= 0 {
		ttl = 300 * time.Second
	}
	return &Recovery{
		intents:     deps.Intents,
		waves:       deps.Waves,
		caseRuns:    deps.CaseRuns,
		humans:      deps.Humans,
		executors:   deps.Executors,
		contexts:    deps.Contexts,
		cases:       deps.Cases,
		workQueue:   deps.WorkQueue,
		canceller:   deps.Canceller,
		executor:    deps.Executor,
		replanner:   deps.Replanner,
		clock:       deps.Clock,
		events:      deps.Events,
		audit:       deps.Audit,
		maxAttempts: maxAttempts,
		contextTTL:  ttl,
	}
}

// Reconcile runs one recovery tick. Each step is idempotent.
func (s *Recovery) Reconcile(ctx context.Context) error {
	if err := s.recoverExecutors(ctx); err != nil {
		return err
	}
	if err := s.recoverLeases(ctx); err != nil {
		return err
	}
	if s.humans != nil {
		if _, err := s.humans.Expire(ctx, s.now()); err != nil {
			return mapExecutionError(err, "human_request")
		}
	}
	if err := s.recoverStale(ctx); err != nil {
		return err
	}
	if err := s.cleanupContexts(ctx); err != nil {
		return err
	}
	return s.reconcileOrphans(ctx)
}

// recoverExecutors interrupts the running case_runs of offline executors.
func (s *Recovery) recoverExecutors(ctx context.Context) error {
	if s.executors == nil {
		return nil
	}
	instances, err := s.executors.List(ctx)
	if err != nil {
		return mapExecutionError(err, "executor")
	}
	for _, instance := range instances {
		if instance.Status != execution.ExecutorOffline {
			continue
		}
		runs, err := s.caseRuns.ListRunningByExecutor(ctx, instance.ID)
		if err != nil {
			return mapExecutionError(err, "case run")
		}
		for _, run := range runs {
			if err := s.interruptRun(ctx, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// recoverLeases interrupts running case_runs whose lease expired.
func (s *Recovery) recoverLeases(ctx context.Context) error {
	waves, err := s.waves.ListActive(ctx)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	now := s.now()
	for _, wave := range waves {
		runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
		if err != nil {
			return mapExecutionError(err, "case run")
		}
		for _, run := range runs {
			if run.Status != execution.CaseRunRunning || run.LeaseUntil == nil {
				continue
			}
			if run.LeaseUntil.After(now) {
				continue
			}
			if err := s.interruptRun(ctx, run); err != nil {
				return err
			}
		}
	}
	return nil
}

// interruptRun retries an interrupted case_run up to the dispatch budget, then
// fails it with the failure class derived from the executor state and its most
// recent transport error (v0.2.3 X-01). An expired lease is not by itself an
// executor_lost: only an offline row or an expired heartbeat keeps that
// classification; an online executor is typed from the recorded error (timeout,
// environment or blocked) and with no recent error as an environment
// lease_expired.
func (s *Recovery) interruptRun(ctx context.Context, run execution.CaseRun) error {
	if run.Status != execution.CaseRunRunning {
		return nil
	}
	intent, err := s.intents.Get(ctx, run.IntentID)
	if err != nil {
		if err == execution.ErrNotFound {
			return nil
		}
		return mapExecutionError(err, "intent")
	}
	if s.canceller != nil {
		s.canceller.CancelRun(run.ID)
	}
	s.closeRunContexts(ctx, run.ID)
	now := s.now()
	// The UAT could not tell a queue stall from an execution hang: every
	// interrupted lease now leaves one structured local diagnostic with the
	// dispatch/claim state (the executor HTTP diagnostics only fire on a real
	// request, and a never-claimed run makes none).
	s.logLeaseExpiryDiagnostic(ctx, intent, run)
	class, cause := s.classifyInterruptedRun(ctx, run)
	retry := run.Attempts < s.maxAttempts && intent.Status.Active()
	oldEpoch := run.DispatchEpoch
	if retry {
		// The row is non-terminal (pending/redispatched): it must not carry a
		// terminal failure class or verdict reason. The stable retry reason and
		// the real last error stay visible to the UI/next owner (X-07).
		lastError := strings.TrimSpace(run.Summary.LastError)
		if lastError == "" {
			lastError = cause
		}
		run.Status = execution.CaseRunPending
		run.FailureClass = ""
		run.Summary.VerdictReason = ""
		run.Summary.LastError = lastError
		run.Summary.RetryReason = cause
		run.DispatchEpoch++
		run.DispatchToken = ""
		run.LeaseUntil = nil
		run.OwnerExecutorID = nil
		run.UpdatedAt = now
		run.StartedAt = nil
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return mapExecutionError(err, "case run")
		}
		publishEvent(ctx, s.events, EventCaseRunFinished, intent.VersionID,
			map[string]any{"wave_id": platformid.Format(run.WaveID), "case_run_id": platformid.Format(run.ID)},
			map[string]any{
				"wave_id":       platformid.Format(run.WaveID),
				"case_run_id":   platformid.Format(run.ID),
				"status":        string(execution.CaseRunInterrupted),
				"failure_class": string(class),
				"cause":         cause,
				"redispatched":  true,
			}, nil)
		s.record(ctx, "execution.case_run_redispatched", run, oldEpoch, map[string]any{"reason": cause, "failure_class": string(class)})
		return nil
	}
	lastError := strings.TrimSpace(run.Summary.LastError)
	if lastError == "" {
		lastError = cause
	}
	run.Status = execution.CaseRunFailed
	run.FailureClass = class
	run.Summary.VerdictReason = execution.VerdictReasonAgentFailure
	run.Summary.TerminalCause = cause
	run.Summary.LastError = lastError
	run.FinishedAt = &now
	run.LeaseUntil = nil
	run.UpdatedAt = now
	run.Summary.TerminalCause, run.Summary.NextAction = execution.TerminalExplanation(run.Status, run.FailureClass, run.Summary)
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return mapExecutionError(err, "case run")
	}
	publishEvent(ctx, s.events, EventCaseRunFinished, intent.VersionID,
		map[string]any{"wave_id": platformid.Format(run.WaveID), "case_run_id": platformid.Format(run.ID)},
		map[string]any{
			"wave_id":        platformid.Format(run.WaveID),
			"case_run_id":    platformid.Format(run.ID),
			"status":         string(execution.CaseRunFailed),
			"failure_class":  string(class),
			"terminal_cause": cause,
		}, nil)
	s.record(ctx, "execution.case_run_failed", run, oldEpoch, map[string]any{"reason": cause, "failure_class": string(class), "attempts": run.Attempts})
	return nil
}

// logLeaseExpiryDiagnostic emits the local dispatch/claim diagnostic of one
// interrupted lease. agent_claimed=false means the work item was never picked up
// by an agent worker (the lease is the dispatch/queue lease), while true means an
// execution lease expired after the run had started.
func (s *Recovery) logLeaseExpiryDiagnostic(ctx context.Context, intent execution.ExecutionIntent, run execution.CaseRun) {
	claimed := !dispatchUnclaimed(run)
	ownerExecutorID := int64(0)
	if run.OwnerExecutorID != nil {
		ownerExecutorID = *run.OwnerExecutorID
	}
	leaseUntil := ""
	if run.LeaseUntil != nil {
		leaseUntil = run.LeaseUntil.UTC().Format(time.RFC3339Nano)
	}
	dispatchAt := ""
	if run.Summary.Dispatch != nil {
		dispatchAt = run.Summary.Dispatch.DispatchAt
	}
	slog.Default().WarnContext(ctx,
		"execution: case run lease expired",
		"case_run_id", platformid.Format(run.ID),
		"version_id", platformid.Format(intent.VersionID),
		"wave_id", platformid.Format(run.WaveID),
		"attempt", run.Attempts,
		"agent_claimed", claimed,
		"owner_executor_id", ownerExecutorID,
		"owner_instance", run.OwnerInstance,
		"lease_until", leaseUntil,
		"dispatch_at", dispatchAt,
		"last_error", run.Summary.LastError,
		"retry_reason", run.Summary.RetryReason,
	)
}

// dispatchUnclaimed reports whether the run was never claimed by an agent
// worker: no dispatch snapshot (legacy row) or an empty claimed_at marker.
func dispatchUnclaimed(run execution.CaseRun) bool {
	return run.Summary.Dispatch == nil || run.Summary.Dispatch.ClaimedAt == ""
}

// classifyInterruptedRun types one expired-lease interruption from the
// executor health and its most recent recorded transport error.
func (s *Recovery) classifyInterruptedRun(ctx context.Context, run execution.CaseRun) (execution.FailureClass, string) {
	if s.executorUnreachable(ctx, run) {
		return execution.FailureClassExecutorLost, string(execution.FailureClassExecutorLost)
	}
	cause := execution.ExecutorFailureCause(run.Summary.RetryReason)
	if cause == "" {
		cause = execution.ExecutorFailureCause(run.Summary.LastError)
	}
	// A recorded refused dial / unreachable network proves the executor is lost,
	// even when the text also carries the generic executor_unavailable code. This
	// is checked before the environment default (X-01 follow-up).
	if execution.ExecutorDialRefused(run.Summary.LastError) {
		return execution.FailureClassExecutorLost, string(execution.FailureClassExecutorLost)
	}
	// The executor is reachable but the run was never claimed by an agent worker:
	// the dispatch (queue) lease expired, which is a queue stall, not an
	// execution lease_expired. A recorded executor failure still wins.
	if cause == "" && dispatchUnclaimed(run) {
		return execution.FailureClassEnvironment, execution.TerminalCauseDispatchTimeout
	}
	switch cause {
	case execution.TerminalCauseExecutorContextCreateFailed, execution.TerminalCauseExecutorTimeout:
		return execution.FailureClassTimeout, cause
	case string(execution.FailureClassExecutorLost):
		return execution.FailureClassExecutorLost, cause
	case execution.TerminalCauseExecutorUnavailable:
		return execution.FailureClassEnvironment, cause
	case execution.TerminalCauseExecutorCapability:
		return execution.FailureClassBlocked, cause
	case execution.TerminalCauseDispatchTimeout:
		return execution.FailureClassEnvironment, cause
	default:
		// The executor is reachable (fresh heartbeat) and no known executor
		// failure was recorded: an expired lease is an environment condition,
		// never an automatic executor_lost.
		return execution.FailureClassEnvironment, execution.TerminalCauseLeaseExpired
	}
}

// executorUnreachable reports whether the run's executor is offline or its
// heartbeat left the offline window. A historical row without an owner cannot
// prove the executor is alive, so the legacy lease-expiry classification stays.
func (s *Recovery) executorUnreachable(ctx context.Context, run execution.CaseRun) bool {
	if run.OwnerExecutorID == nil || s.executors == nil {
		return true
	}
	instance, err := s.executors.Get(ctx, *run.OwnerExecutorID)
	if err != nil {
		return true
	}
	if instance.Status == execution.ExecutorOffline {
		return true
	}
	if instance.LastHeartbeat == nil {
		return true
	}
	window := time.Duration(execution.DefaultExecutorOfflineSeconds) * time.Second
	return s.now().Sub(*instance.LastHeartbeat) > window
}

// recoverStale supersedes active intents whose case version left the
// current+confirmed set (P3 §4.9). Task 106: when the same logical case is
// current+confirmed again as a new revision, its initial intent is enqueued so
// a selected case is re-planned instead of silently dropped. A real case read
// failure (not "no longer executable") is surfaced and never retires an intent.
func (s *Recovery) recoverStale(ctx context.Context) error {
	if s.cases == nil {
		return nil
	}
	intents, err := s.intents.ListActive(ctx, 0)
	if err != nil {
		return mapExecutionError(err, "intent")
	}
	for _, intent := range intents {
		if _, err := s.cases.GetCaseVersion(ctx, intent.VersionID, intent.CaseVersionID); err == nil {
			continue
		} else if !caseVersionNotExecutable(err) {
			return mapExecutionError(err, "case version")
		}
		result, err := retireStaleIntent(ctx, s.intents, s.events, s.replanner, intent)
		if err != nil {
			return err
		}
		if result.Superseded {
			s.supersedeIntentRuns(ctx, intent, result)
			s.record(ctx, "execution.intent_superseded", execution.CaseRun{}, 0,
				map[string]any{"intent_id": platformid.Format(intent.ID), "replanned": result.Replanned})
		}
	}
	return nil
}

// supersedeIntentRuns retires the non-terminal case_runs of a superseded intent
// and persists the supersede surface (reason/replacement) on each run.
func (s *Recovery) supersedeIntentRuns(ctx context.Context, intent execution.ExecutionIntent, result SupersedeResult) {
	wave, err := s.waves.GetActive(ctx, intent.VersionID)
	if err != nil || wave == nil {
		return
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return
	}
	for _, run := range runs {
		if run.IntentID != intent.ID || run.Status.Terminal() {
			continue
		}
		if s.canceller != nil {
			s.canceller.CancelRun(run.ID)
		}
		s.closeRunContexts(ctx, run.ID)
		_, _ = retireSupersededRun(ctx, s.caseRuns, s.events, wave.VersionID, run,
			result.ReplacementCaseVersionID, result.Replanned)
	}
}

// cleanupContexts closes SUT contexts older than the executor context TTL whose
// case_run is not running (§6.9).
func (s *Recovery) cleanupContexts(ctx context.Context) error {
	if s.contexts == nil || s.executor == nil {
		return nil
	}
	before := s.now().Add(-s.contextTTL)
	items, err := s.contexts.ListStale(ctx, before, 100)
	if err != nil {
		return mapExecutionError(err, "sut context")
	}
	for _, item := range items {
		executorID := int64(0)
		if item.ExecutorID != nil {
			executorID = *item.ExecutorID
		}
		if item.BrowserContextID != "" {
			_ = s.executor.DeleteContext(ctx, executorID, item.BrowserContextID)
		}
		closedAt := s.now()
		_, _ = s.contexts.UpdateCAS(ctx, item.ID, func(current *execution.CaseRunSUTContext) error {
			current.Status = execution.SUTContextClosed
			current.ClosedAt = &closedAt
			current.UpdatedAt = closedAt
			return nil
		})
	}
	return nil
}

// reconcileOrphans deletes executor contexts whose DB record is gone and whose
// key marks them as execution-owned (§6.9).
func (s *Recovery) reconcileOrphans(ctx context.Context) error {
	if s.executor == nil || s.contexts == nil {
		return nil
	}
	live, err := s.executor.ListContexts(ctx)
	if err != nil {
		return nil // executor unavailable: retry next tick
	}
	known := map[string]struct{}{}
	items, err := s.contexts.ListAll(ctx)
	if err != nil {
		return mapExecutionError(err, "sut context")
	}
	for _, item := range items {
		if item.BrowserContextID != "" {
			known[item.BrowserContextID] = struct{}{}
		}
	}
	for _, ref := range live {
		if !strings.HasPrefix(ref.Key, "run:") {
			continue
		}
		if _, ok := known[ref.ID]; ok {
			continue
		}
		_ = s.executor.DeleteContext(ctx, ref.ExecutorID, ref.ID)
	}
	return nil
}

// closeRunContexts closes the stored executor contexts of one case_run.
func (s *Recovery) closeRunContexts(ctx context.Context, caseRunID int64) {
	if s.contexts == nil {
		return
	}
	items, err := s.contexts.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return
	}
	for _, item := range items {
		executorID := int64(0)
		if item.ExecutorID != nil {
			executorID = *item.ExecutorID
		}
		if item.BrowserContextID != "" && s.executor != nil {
			_ = s.executor.DeleteContext(ctx, executorID, item.BrowserContextID)
		}
		if item.Status == execution.SUTContextClosed {
			continue
		}
		closedAt := s.now()
		_, _ = s.contexts.UpdateCAS(ctx, item.ID, func(current *execution.CaseRunSUTContext) error {
			current.Status = execution.SUTContextClosed
			current.ClosedAt = &closedAt
			current.UpdatedAt = closedAt
			return nil
		})
	}
}

func (s *Recovery) record(ctx context.Context, action string, run execution.CaseRun, previousEpoch int64, metadata map[string]any) {
	if s.audit == nil {
		return
	}
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["case_run_id"] = platformid.Format(run.ID)
	metadata["previous_epoch"] = previousEpoch
	_ = s.audit.Record(ctx, auditEvent(action, "case_run", run.ID, metadata))
}

func (s *Recovery) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}
