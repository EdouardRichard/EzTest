package executionapp

import (
	"context"
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
// fails it as an environment failure (§5.5).
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
	retry := run.Attempts < s.maxAttempts && intent.Status.Active()
	oldEpoch := run.DispatchEpoch
	if retry {
		run.Status = execution.CaseRunInterrupted
		run.FailureClass = execution.FailureClassExecutorLost
		run.Summary.VerdictReason = "executor_lost"
		run.Summary.LastError = "executor_lost"
		run.DispatchEpoch++
		run.DispatchToken = ""
		run.LeaseUntil = nil
		run.OwnerExecutorID = nil
		run.UpdatedAt = now
		run.Status = execution.CaseRunPending
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
				"failure_class": string(execution.FailureClassExecutorLost),
				"redispatched":  true,
			}, nil)
		s.record(ctx, "execution.case_run_redispatched", run, oldEpoch, map[string]any{"reason": "executor_lost"})
		return nil
	}
	run.Status = execution.CaseRunFailed
	run.FailureClass = execution.FailureClassEnvironment
	run.Summary.VerdictReason = "executor_lost"
	run.Summary.LastError = "executor_lost"
	run.FinishedAt = &now
	run.LeaseUntil = nil
	run.UpdatedAt = now
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return mapExecutionError(err, "case run")
	}
	publishEvent(ctx, s.events, EventCaseRunFinished, intent.VersionID,
		map[string]any{"wave_id": platformid.Format(run.WaveID), "case_run_id": platformid.Format(run.ID)},
		map[string]any{
			"wave_id":       platformid.Format(run.WaveID),
			"case_run_id":   platformid.Format(run.ID),
			"status":        string(execution.CaseRunFailed),
			"failure_class": string(execution.FailureClassEnvironment),
		}, nil)
	s.record(ctx, "execution.case_run_failed", run, oldEpoch, map[string]any{"reason": "executor_lost", "attempts": run.Attempts})
	return nil
}

// recoverStale supersedes active intents whose case version left the
// current+confirmed set (P3 §4.9).
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
		}
		if changed, err := s.intents.Transition(ctx, intent.ID,
			activeIntentStatuses, execution.IntentSuperseded); err != nil {
			return mapExecutionError(err, "intent")
		} else if changed {
			s.supersedeIntentRuns(ctx, intent)
			publishEvent(ctx, s.events, EventIntentSuperseded, intent.VersionID,
				map[string]any{
					"intent_id":       platformid.Format(intent.ID),
					"case_version_id": platformid.Format(intent.CaseVersionID),
				},
				map[string]any{
					"intent_id":       platformid.Format(intent.ID),
					"case_version_id": platformid.Format(intent.CaseVersionID),
					"reason":          "not_current_confirmed",
				}, nil)
			s.record(ctx, "execution.intent_superseded", execution.CaseRun{}, 0, map[string]any{"intent_id": platformid.Format(intent.ID)})
		}
	}
	return nil
}

// supersedeIntentRuns retires the non-terminal case_runs of a superseded intent.
func (s *Recovery) supersedeIntentRuns(ctx context.Context, intent execution.ExecutionIntent) {
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
		_, _ = s.caseRuns.Transition(ctx, run.ID,
			[]execution.CaseRunStatus{execution.CaseRunPending, execution.CaseRunRunning, execution.CaseRunPaused, execution.CaseRunInterrupted},
			execution.CaseRunSuperseded, 0, "")
		publishEvent(ctx, s.events, EventCaseRunFinished, intent.VersionID,
			map[string]any{"wave_id": platformid.Format(wave.ID), "case_run_id": platformid.Format(run.ID)},
			map[string]any{
				"wave_id":     platformid.Format(wave.ID),
				"case_run_id": platformid.Format(run.ID),
				"status":      string(execution.CaseRunSuperseded),
			}, nil)
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
		if item.BrowserContextID != "" {
			_ = s.executor.DeleteContext(ctx, item.BrowserContextID)
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
		_ = s.executor.DeleteContext(ctx, ref.ID)
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
		if item.BrowserContextID != "" && s.executor != nil {
			_ = s.executor.DeleteContext(ctx, item.BrowserContextID)
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
