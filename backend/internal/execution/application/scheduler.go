package executionapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/url"
	"sort"
	"sync"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// P3 work item types and team (contracts/task-types.md §5).
const (
	WorkTypeExecuteCase     = "execute.case"
	WorkTypeAnalyzeFailure  = "analyze.failure"
	WorkTypeAnalyzeCoverage = "analyze.coverage"
	TeamExecution           = "execution"
)

// EventCaseRunBlocked is the 0.2.1 dispatch-health event (spec §5.6): a pending
// case_run could not be dispatched because no healthy executor is available.
// It is emitted once per (case_run, reason) until the run is dispatched, so
// "no executor" never looks like a silent pending row.
const EventCaseRunBlocked = "case_run.blocked"

// Reasons carried by EventCaseRunBlocked.
const (
	BlockedReasonNoExecutor          = "no_executor"
	BlockedReasonExecutorDegraded    = "executor_degraded"
	BlockedReasonExecutorUnavailable = "executor_unavailable"
)

// Scheduler is the deterministic execution planner/reconciler (ARCH §7.1,
// P3-D-15): it groups open intents into waves, dispatches pending case_runs
// under the frozen quota, and finalizes waves into intent results plus the
// failure/coverage analysis trigger. It is intentionally single-instance.
type Scheduler struct {
	intents     IntentRepository
	waves       WaveRepository
	caseRuns    CaseRunRepository
	cases       CaseReader
	versions    VersionReader
	executors   ExecutorRegistry
	workQueue   WorkQueue
	quota       execution.Quota
	quotaReader QuotaReader
	analyzer    WaveAnalyzer
	replanner   SupersededIntentReplanner
	ids         IDGenerator
	clock       Clock
	events      EventPublisher
	instance    string
	leaseTTL    time.Duration
	// stageNotifier is the v0.22 production wave-terminal stage callback
	// (R-B4-3). It is optional so the pre-v0.22 scheduler assemblies keep
	// working: nil means "do not drive the stage machine from execution".
	stageNotifier WaveTerminalStageNotifier
	// stageReconciler re-drives a version whose wave-terminal callback failed
	// transiently on an earlier tick (Task 25 fix round 1). It is optional;
	// *versionapp.StageService implements it. pendingStage holds the version IDs
	// whose callback failed and which are retried until a reconcile succeeds.
	stageReconciler StageReconciler
	pendingStageMu  sync.Mutex
	pendingStage    map[int64]struct{}
	// retestNotifier is the v0.22 W2-5 retest writeback callback. nil keeps the
	// pre-v0.22 behavior; the composition root binds the delivery adapter.
	retestNotifier WaveTerminalRetestNotifier
	// delta is the optional v0.22 version-context discovery-delta trigger
	// (W1-14A): the scheduler calls it once when a wave becomes terminal
	// (done or failed); PauseService does the same for a cancelled wave.
	delta DiscoveryDeltaEvaluator
	// blocked deduplicates EventCaseRunBlocked by case_run ID; it holds the
	// last published reason. A successful dispatch clears the entry.
	blockedMu sync.Mutex
	blocked   map[int64]string
}

// WaveTerminalStageNotifier is the narrow production callback the scheduler
// invokes when a wave becomes terminal (done/failed/cancelled), so the version
// stage machine can advance executing -> reporting without a manual API call
// (spec §4.12 R-B4-3). Implementations must be idempotent and must not block the
// wave finalization: the scheduler treats the callback as best effort, logs a
// failure and retries it on a later tick through StageReconciler.
type WaveTerminalStageNotifier interface {
	OnWaveTerminal(ctx context.Context, waveID int64) error
}

// WaveTerminalRetestNotifier is the narrow v0.22 W2-5 callback the scheduler and
// PauseService invoke once when a wave becomes terminal (done/failed/cancelled),
// so the delivery issue service can write the retest outcome back. Implementations
// must be idempotent and must not block wave finalization: the call is best
// effort, bounded by a deadline, and a failure is logged rather than rolled back.
type WaveTerminalRetestNotifier interface {
	OnWaveTerminalRetest(ctx context.Context, waveID int64) error
}

// StageReconciler is the narrow retry port the scheduler uses when a
// wave-terminal callback failed. *versionapp.StageService implements it: a
// reconcile advances an executing version whose waves are all terminal to
// reporting (and completes a reporting version whose report is published),
// idempotently and without a wave ID. It is optional so assemblies without the
// v0.22 stage wiring keep working.
type StageReconciler interface {
	Reconcile(ctx context.Context, versionID int64) error
}

// SchedulerDeps wires the scheduler. quotaReader may be nil: the running set is
// then read from CaseRunRepository.ListRunning. LeaseTTL is the dispatch-time
// case_run lease; it is clamped to at least DefaultCaseLease (the agent-side
// lease), because the work item may wait in the queue before any agent claims
// it.
type SchedulerDeps struct {
	Intents     IntentRepository
	Waves       WaveRepository
	CaseRuns    CaseRunRepository
	Cases       CaseReader
	Versions    VersionReader
	Executors   ExecutorRegistry
	WorkQueue   WorkQueue
	Quota       execution.Quota
	QuotaReader QuotaReader
	Analyzer    WaveAnalyzer
	IDs         IDGenerator
	Clock       Clock
	Events      EventPublisher
	Instance    string
	LeaseTTL    time.Duration
	// Replanner is the Task 106 stale-intent re-plan (implemented by
	// *IntentService). nil keeps the P3-only retire behavior.
	Replanner SupersededIntentReplanner
	// StageNotifier is the v0.22 wave-terminal stage callback (Task 25). nil
	// keeps the pre-v0.22 behavior; Task 36 wires *versionapp.StageService here.
	StageNotifier WaveTerminalStageNotifier
	// StageReconciler is the v0.22 retry sink for a failed wave-terminal stage
	// callback (Task 25 fix round 1). nil disables retries; Task 36 wires
	// *versionapp.StageService here too.
	StageReconciler StageReconciler
	// Delta is the W1-14A wave-terminal discovery-delta trigger implemented by
	// the version context. nil disables the trigger (degraded assemblies/tests).
	Delta DiscoveryDeltaEvaluator
	// RetestNotifier is the v0.22 W2-5 a wave-terminal retest writeback callback
	// implemented by the delivery issue service adapter. nil disables it.
	RetestNotifier WaveTerminalRetestNotifier
}

// NewScheduler builds the scheduler.
func NewScheduler(deps SchedulerDeps) *Scheduler {
	quota := deps.Quota
	if quota.Global <= 0 && quota.PerSUT <= 0 && quota.CrossSystem <= 0 {
		quota = execution.QuotaFromSettings(0, 0, 0)
	}
	lease := deps.LeaseTTL
	if lease < DefaultCaseLease {
		// The dispatch lease must outlive the agent lease: a queued work item
		// can wait behind a busy worker, and Recovery interrupts a running
		// case_run whose lease expired before the agent even claimed it.
		lease = DefaultCaseLease
	}
	return &Scheduler{
		intents:         deps.Intents,
		waves:           deps.Waves,
		caseRuns:        deps.CaseRuns,
		cases:           deps.Cases,
		versions:        deps.Versions,
		executors:       deps.Executors,
		workQueue:       deps.WorkQueue,
		quota:           quota,
		quotaReader:     deps.QuotaReader,
		analyzer:        deps.Analyzer,
		replanner:       deps.Replanner,
		ids:             deps.IDs,
		clock:           deps.Clock,
		events:          deps.Events,
		instance:        deps.Instance,
		leaseTTL:        lease,
		stageNotifier:   deps.StageNotifier,
		stageReconciler: deps.StageReconciler,
		delta:           deps.Delta,
		retestNotifier:  deps.RetestNotifier,
		pendingStage:    map[int64]struct{}{},
		blocked:         map[int64]string{},
	}
}

// Reconcile runs one scheduling tick over every version that has an open intent
// or an active wave.
func (s *Scheduler) Reconcile(ctx context.Context) error {
	versionIDs := map[int64]struct{}{}
	open, err := s.intents.ListOpen(ctx, 0)
	if err != nil {
		return mapExecutionError(err, "intent")
	}
	for _, intent := range open {
		versionIDs[intent.VersionID] = struct{}{}
	}
	activeWaves, err := s.waves.ListActive(ctx)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	for _, wave := range activeWaves {
		versionIDs[wave.VersionID] = struct{}{}
	}
	sorted := make([]int64, 0, len(versionIDs))
	for versionID := range versionIDs {
		sorted = append(sorted, versionID)
	}
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	for _, versionID := range sorted {
		if err := s.ReconcileVersion(ctx, versionID); err != nil {
			return err
		}
	}
	// A failed wave-terminal stage callback from an earlier tick is retried here:
	// the version may have no open intent and no active wave any more, so it is
	// not part of this tick's version set (Task 25 fix round 1).
	return s.retryPendingStageNotifications(ctx)
}

// ReconcileVersion plans, dispatches and finalizes one version.
func (s *Scheduler) ReconcileVersion(ctx context.Context, versionID int64) error {
	if err := requirePositiveID("version_id", versionID); err != nil {
		return err
	}
	if err := s.planVersion(ctx, versionID); err != nil {
		return err
	}
	wave, err := s.waves.GetActive(ctx, versionID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if wave == nil {
		return nil
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	if len(runs) == 0 {
		return s.failEmptyWave(ctx, *wave)
	}
	if wave.Status != execution.WavePaused {
		if err := s.dispatchPending(ctx, *wave, runs); err != nil {
			return err
		}
	}
	runs, err = s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	for _, run := range runs {
		if run.Status.Active() {
			return nil
		}
	}
	return s.completeWave(ctx, *wave, runs)
}

// failEmptyWave marks an abnormal planned wave without case_runs as failed
// (§5.2 step 4).
func (s *Scheduler) failEmptyWave(ctx context.Context, wave execution.RunWave) error {
	now := s.now()
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePlanned, execution.WaveRunning},
		execution.WaveFailed, execution.RunWave{FinishedAt: &now})
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if !changed {
		return nil
	}
	publishEvent(ctx, s.events, EventWaveFinished, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{"wave_id": platformid.Format(wave.ID), "status": string(execution.WaveFailed)},
		nil)
	s.notifyStageWaveTerminal(ctx, wave.VersionID, wave.ID)
	// v0.22 W2-5: an empty/failed retest wave must still settle the issue
	// retest state; the callback is best effort and idempotent.
	s.notifyRetestWaveTerminal(ctx, wave.VersionID, wave.ID)
	// W1-14A: a failed wave is terminal too, so the discovery delta runs exactly
	// once here (the changed guard makes the transition itself once-only). An
	// empty wave produced no observation, so the version evaluator naturally
	// no-ops.
	s.notifyDiscoveryDelta(ctx, wave.VersionID, wave.ID)
	return nil
}

// dispatchPending attempts to start every pending case_run of a wave. Quota is
// reserved in one shot per candidate: a cross-system run either gets all of its
// version_sut slots or is skipped without blocking later runs (§5.3).
func (s *Scheduler) dispatchPending(ctx context.Context, wave execution.RunWave, runs []execution.CaseRun) error {
	pending := make([]execution.CaseRun, 0)
	for _, run := range runs {
		if run.Status == execution.CaseRunPending {
			pending = append(pending, run)
		}
	}
	if len(pending) == 0 {
		return nil
	}
	usage, err := s.runningUsage(ctx)
	if err != nil {
		return err
	}
	for index := range pending {
		run := pending[index]
		// P7 multi-endpoint selection: re-select the best executor for every
		// candidate so one tick can use several endpoints (capacity DESC,
		// last_heartbeat DESC, id) instead of pinning the whole wave to the
		// executor chosen first.
		executor, err := s.executors.Select(ctx)
		if err != nil {
			return mapExecutionError(err, "executor")
		}
		if !dispatchableExecutor(executor) {
			// Do not leave pending case_runs silent (spec §5.6): publish a
			// visible blocked event per run and stop this tick. A later tick
			// retries the dispatch once a healthy executor is registered.
			s.publishBlockedPending(ctx, wave, pending[index:], blockedReason(executor))
			return nil
		}
		intent, err := s.intents.Get(ctx, run.IntentID)
		if err != nil {
			return mapExecutionError(err, "intent")
		}
		snapshot, err := s.cases.GetCaseVersion(ctx, wave.VersionID, run.CaseVersionID)
		if err != nil {
			if !caseVersionNotExecutable(err) {
				return mapExecutionError(err, "case version")
			}
			// The case left the current+confirmed set between planning and
			// dispatch: retire the run and intent, never execute stale cases.
			// Task 106 also re-queues the same logical case's replacement
			// confirmed revision and persists the supersede reason on the run so
			// the drop is visible in the wave/UI/report.
			result, err := s.supersedeStaleIntent(ctx, intent)
			if err != nil {
				return err
			}
			if _, err := retireSupersededRun(ctx, s.caseRuns, s.events, wave.VersionID, run,
				result.ReplacementCaseVersionID, result.Replanned); err != nil {
				return err
			}
			continue
		}
		sutIDs := intent.VersionSUTIDs
		if len(sutIDs) == 0 {
			sutIDs = caseIntentVersionSUTIDs(snapshot)
		}
		candidate := execution.RunningCase{
			CaseRunID:     run.ID,
			VersionSUTIDs: sutIDs,
			CrossSystem:   len(sutIDs) > 1,
		}
		ok, _ := execution.CanDispatch(s.quota, usage, candidate,
			executor.Capacity, executor.ActiveContexts, requiredContextCount(snapshot))
		if !ok {
			continue
		}
		now := s.now()
		run.Attempts++
		run.DispatchEpoch++
		run.DispatchToken = newDispatchToken()
		run.Status = execution.CaseRunRunning
		if run.StartedAt == nil {
			run.StartedAt = &now
		}
		run.LeaseUntil = timePtr(now.Add(s.leaseTTL))
		run.OwnerInstance = s.instance
		run.OwnerExecutorID = int64PtrValue(executor.ID)
		run.Summary.Dispatch = dispatchDiagnostic(executor, now, run.Attempts)
		run.UpdatedAt = now
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return mapExecutionError(err, "case run")
		}
		s.clearBlocked(run.ID)
		// Reserve the slots in the local usage snapshot so the next candidate
		// of this tick sees the one-shot reservation.
		usage = append(usage, candidate)
		workItem := WorkItemInput{
			VersionID: wave.VersionID,
			Type:      WorkTypeExecuteCase,
			Team:      TeamExecution,
			DedupeKey: "execute:" + platformid.Format(run.IntentID) + ":" + platformid.Format(run.DispatchEpoch),
			Payload: map[string]any{
				"intent_id":       platformid.Format(run.IntentID),
				"wave_id":         platformid.Format(run.WaveID),
				"case_run_id":     platformid.Format(run.ID),
				"case_version_id": platformid.Format(run.CaseVersionID),
				"dispatch_epoch":  platformid.Format(run.DispatchEpoch),
				"dispatch_token":  run.DispatchToken,
			},
		}
		if _, _, err := s.workQueue.Ensure(ctx, workItem); err != nil {
			return mapExecutionError(err, "work item")
		}
		if wave.Status == execution.WavePlanned {
			changed, err := s.waves.Transition(ctx, wave.ID,
				[]execution.WaveStatus{execution.WavePlanned}, execution.WaveRunning,
				execution.RunWave{StartedAt: &now})
			if err != nil {
				return mapExecutionError(err, "wave")
			}
			if changed {
				wave.Status = execution.WaveRunning
				publishEvent(ctx, s.events, EventWaveStarted, wave.VersionID,
					map[string]any{"wave_id": platformid.Format(wave.ID)},
					map[string]any{"wave_id": platformid.Format(wave.ID)}, nil)
			}
		}
		if _, err := s.intents.Transition(ctx, intent.ID, activeIntentStatuses, execution.IntentRunning); err != nil {
			return mapExecutionError(err, "intent")
		}
		publishEvent(ctx, s.events, EventCaseRunStarted, wave.VersionID,
			map[string]any{
				"wave_id":         platformid.Format(wave.ID),
				"case_run_id":     platformid.Format(run.ID),
				"case_version_id": platformid.Format(run.CaseVersionID),
			},
			map[string]any{
				"wave_id":         platformid.Format(wave.ID),
				"case_run_id":     platformid.Format(run.ID),
				"case_version_id": platformid.Format(run.CaseVersionID),
				"version_sut_ids": idsToStrings(sutIDs),
			}, nil)
	}
	return nil
}

// runningUsage reads the running case set for quota accounting.
func (s *Scheduler) runningUsage(ctx context.Context) ([]execution.RunningCase, error) {
	if s.quotaReader != nil {
		usage, err := s.quotaReader.RunningCases(ctx)
		if err != nil {
			return nil, mapExecutionError(err, "quota")
		}
		return usage, nil
	}
	running, err := s.caseRuns.ListRunning(ctx)
	if err != nil {
		return nil, mapExecutionError(err, "case run")
	}
	usage := make([]execution.RunningCase, 0, len(running))
	for _, run := range running {
		usage = append(usage, execution.RunningCase{CaseRunID: run.ID, CrossSystem: false})
	}
	return usage, nil
}

// completeWave finalizes a wave whose case_runs all reached a finished state
// (§5.2 step 3): counts, intents, wave.finished and the analysis trigger.
func (s *Scheduler) completeWave(ctx context.Context, wave execution.RunWave, runs []execution.CaseRun) error {
	now := s.now()
	counts, err := s.waves.Counts(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	wave.Counts = counts
	wave.FinishedAt = &now
	wave.UpdatedAt = now
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePlanned, execution.WaveRunning},
		execution.WaveDone, execution.RunWave{FinishedAt: &now})
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if !changed {
		return nil
	}
	wave.Status = execution.WaveDone
	if _, err := s.waves.Update(ctx, wave); err != nil {
		return mapExecutionError(err, "wave")
	}
	for _, run := range runs {
		if err := s.completeIntent(ctx, run); err != nil {
			return err
		}
	}
	publishEvent(ctx, s.events, EventWaveFinished, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{
			"wave_id": platformid.Format(wave.ID),
			"status":  string(execution.WaveDone),
			"counts":  counts,
		}, nil)
	if s.analyzer != nil {
		// Analysis failure never blocks the wave terminal state: the
		// deterministic fallback runs inside AnalyzeWave and the agent tasks are
		// deduplicated by the work queue.
		_ = s.analyzer.AnalyzeWave(ctx, wave.ID)
	}
	// The wave row is terminal now, so the version stage machine may advance
	// executing -> reporting (R-B4-3). The callback runs after analysis so the
	// version report snapshot includes the analyzed issues.
	s.notifyStageWaveTerminal(ctx, wave.VersionID, wave.ID)
	// v0.22 W2-5: write the retest outcome back to the delivery issue after the
	// wave row is terminal. Best effort: a delivery failure never rolls back the
	// completed wave.
	s.notifyRetestWaveTerminal(ctx, wave.VersionID, wave.ID)
	// W1-14A: classify this wave's execution-time observations into resources/
	// facts and publish resource.delta. It runs after the terminal transition
	// (the changed guard makes it once per wave) and after the stage callback, is
	// bounded to 15s and never fails the scheduler tick.
	s.notifyDiscoveryDelta(ctx, wave.VersionID, wave.ID)
	return nil
}

// discoveryDeltaTimeout bounds the best-effort wave-terminal delta evaluation.
const discoveryDeltaTimeout = 15 * time.Second

// notifyDiscoveryDelta invokes the optional v0.22 discovery delta after a wave
// is terminal. The wave finalization is the source of truth: a failed or slow
// evaluation is logged and never rolls the wave back.
func (s *Scheduler) notifyDiscoveryDelta(ctx context.Context, versionID, waveID int64) {
	evaluateDiscoveryDelta(ctx, s.delta, versionID, waveID)
}

// evaluateDiscoveryDelta is the shared best-effort terminal-wave trigger used by
// the scheduler's done/failed paths and by PauseService.CancelWave. It is a
// no-op when the dependency is absent; every terminal path bounds the pass to
// the same 15s deadline so a slow evaluator can neither block nor fail the
// wave/tick. Callers invoke it only after a terminal transition actually
// committed, which keeps "one call per terminal wave".
func evaluateDiscoveryDelta(ctx context.Context, delta DiscoveryDeltaEvaluator, versionID, waveID int64) {
	if delta == nil || waveID <= 0 {
		return
	}
	evalCtx, cancel := context.WithTimeout(ctx, discoveryDeltaTimeout)
	defer cancel()
	if err := delta.Evaluate(evalCtx, versionID, waveID); err != nil {
		slog.Default().WarnContext(ctx,
			"execution: discovery delta evaluation failed; the wave stays terminal",
			"version_id", versionID, "wave_id", waveID, "error", err)
	}
}

// notifyStageWaveTerminal invokes the optional v0.22 production callback after a
// wave is terminal. It is best effort: the wave finalization is the source of
// truth and a failed stage advance is retried by StageReconciler on a later
// scheduler tick, so a callback failure never rolls the wave back or fails the
// scheduler tick. The failure is logged instead of silently swallowed (Task 25
// fix round 1).
func (s *Scheduler) notifyStageWaveTerminal(ctx context.Context, versionID, waveID int64) {
	if s.stageNotifier == nil || waveID <= 0 {
		return
	}
	if err := s.stageNotifier.OnWaveTerminal(ctx, waveID); err != nil {
		slog.Default().WarnContext(ctx,
			"execution: wave-terminal stage callback failed; the version is retried on a later scheduler tick",
			"version_id", versionID, "wave_id", waveID, "error", err)
		s.markPendingStage(versionID)
		return
	}
	s.clearPendingStage(versionID)
}

// retestWritebackTimeout bounds the best-effort retest writeback so a slow
// delivery adapter can neither block nor fail the wave finalization.
const retestWritebackTimeout = 15 * time.Second

// notifyRetestWaveTerminal invokes the optional v0.22 W2-5 retest writeback
// after a wave becomes terminal. The wave finalization is the source of truth:
// a delivery failure is logged and never rolls the wave back or fails the tick.
func (s *Scheduler) notifyRetestWaveTerminal(ctx context.Context, versionID, waveID int64) {
	if s == nil || s.retestNotifier == nil || waveID <= 0 {
		return
	}
	writeCtx, cancel := context.WithTimeout(ctx, retestWritebackTimeout)
	defer cancel()
	if err := s.retestNotifier.OnWaveTerminalRetest(writeCtx, waveID); err != nil {
		slog.Default().WarnContext(ctx,
			"execution: wave-terminal retest writeback failed; the issue keeps its current retest status",
			"version_id", versionID, "wave_id", waveID, "error", err)
	}
}

// retryPendingStageNotifications re-drives the stage machine for every version
// whose wave-terminal callback failed on an earlier tick. A still-failing
// reconcile keeps the version pending for the next tick; a success clears it.
// Reconcile failures never fail the scheduler tick (best effort, one version
// must not block the others).
func (s *Scheduler) retryPendingStageNotifications(ctx context.Context) error {
	versionIDs := s.pendingStageVersions()
	if len(versionIDs) == 0 || s.stageReconciler == nil {
		return nil
	}
	for _, versionID := range versionIDs {
		if err := s.stageReconciler.Reconcile(ctx, versionID); err != nil {
			slog.Default().WarnContext(ctx,
				"execution: stage reconcile retry failed; keeping the version pending",
				"version_id", versionID, "error", err)
			continue
		}
		s.clearPendingStage(versionID)
	}
	return nil
}

func (s *Scheduler) markPendingStage(versionID int64) {
	if versionID <= 0 {
		return
	}
	s.pendingStageMu.Lock()
	defer s.pendingStageMu.Unlock()
	if s.pendingStage == nil {
		s.pendingStage = map[int64]struct{}{}
	}
	s.pendingStage[versionID] = struct{}{}
}

func (s *Scheduler) clearPendingStage(versionID int64) {
	s.pendingStageMu.Lock()
	defer s.pendingStageMu.Unlock()
	delete(s.pendingStage, versionID)
}

func (s *Scheduler) pendingStageVersions() []int64 {
	s.pendingStageMu.Lock()
	defer s.pendingStageMu.Unlock()
	versionIDs := make([]int64, 0, len(s.pendingStage))
	for versionID := range s.pendingStage {
		versionIDs = append(versionIDs, versionID)
	}
	sort.Slice(versionIDs, func(i, j int) bool { return versionIDs[i] < versionIDs[j] })
	return versionIDs
}

// completeIntent projects one finished case_run onto its intent (§4.1).
func (s *Scheduler) completeIntent(ctx context.Context, run execution.CaseRun) error {
	intent, err := s.intents.Get(ctx, run.IntentID)
	if err != nil {
		if err == execution.ErrNotFound {
			return nil
		}
		return mapExecutionError(err, "intent")
	}
	if !intent.Status.Active() {
		return nil
	}
	target := execution.IntentDone
	switch {
	case run.Status == execution.CaseRunSuperseded:
		target = execution.IntentSuperseded
	case run.Status == execution.CaseRunFailed && isInfraFailure(run.FailureClass):
		target = execution.IntentFailed
	}
	if _, err := s.intents.Transition(ctx, intent.ID, activeIntentStatuses, target); err != nil {
		return mapExecutionError(err, "intent")
	}
	return nil
}

func isInfraFailure(class execution.FailureClass) bool {
	switch class {
	case execution.FailureClassEnvironment, execution.FailureClassExecutorLost, execution.FailureClassBlocked:
		return true
	default:
		return false
	}
}

// dispatchDiagnostic snapshots the executor selected for one attempt. It is
// persisted in case_run.summary["dispatch"] (no migration, X-01) so a later
// timeout/lost-executor verdict can say which endpoint the run was sent to.
// RegistryHit is selection-time only (executor.ID > 0): it records that the
// scheduler found a registered owner row, not that the later dial used the
// registry. The dial-route truth lives in the router/client error details.
func dispatchDiagnostic(executor *execution.ExecutorInstance, at time.Time, attempt int) *execution.CaseRunDispatchDiagnostic {
	diagnostic := &execution.CaseRunDispatchDiagnostic{
		DispatchAt: at.UTC().Format(time.RFC3339Nano),
		Attempt:    attempt,
	}
	if executor == nil {
		return diagnostic
	}
	diagnostic.OwnerExecutorID = executor.ID
	diagnostic.Endpoint = sanitizedExecutorEndpoint(executor.Endpoint)
	diagnostic.ExecutorStatus = string(executor.Status)
	diagnostic.ExecutorVersion = executor.Version
	diagnostic.RegistryHit = executor.ID > 0
	if executor.LastHeartbeat != nil {
		diagnostic.LastHeartbeat = executor.LastHeartbeat.UTC().Format(time.RFC3339Nano)
	}
	return diagnostic
}

// sanitizedExecutorEndpoint drops userinfo/query/path so the summary never
// persists a credential-bearing URL.
func sanitizedExecutorEndpoint(endpoint string) string {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// dispatchableExecutor reports whether a selected instance may start new
// case_runs. A degraded executor keeps Status=active (it still heartbeat for
// cleanup) but must not receive new work, and draining/offline rows are never
// dispatchable.
func dispatchableExecutor(executor *execution.ExecutorInstance) bool {
	return executor != nil && executor.Status.Dispatchable() && !isDegradedExecutor(executor)
}

// isDegradedExecutor reads the explicit degraded health marker the registry
// keeps on the row (executor health status, not last_error text: a degraded
// /healthz reply is not required to carry an error message). A healthy poll or
// heartbeat clears the marker again.
func isDegradedExecutor(executor *execution.ExecutorInstance) bool {
	return executor != nil && executor.Degraded()
}

// blockedReason maps the unavailable executor to the blocked-event reason.
func blockedReason(executor *execution.ExecutorInstance) string {
	switch {
	case executor == nil:
		return BlockedReasonNoExecutor
	case !executor.Status.Dispatchable():
		return BlockedReasonExecutorUnavailable
	case isDegradedExecutor(executor):
		return BlockedReasonExecutorDegraded
	default:
		return BlockedReasonExecutorUnavailable
	}
}

// publishBlockedPending publishes one deduplicated blocked event per pending
// case_run of the wave.
func (s *Scheduler) publishBlockedPending(ctx context.Context, wave execution.RunWave, runs []execution.CaseRun, reason string) {
	action := "等待执行器恢复后重试"
	if reason == BlockedReasonNoExecutor {
		action = "等待执行器注册后重试"
	}
	for _, run := range runs {
		if !s.markBlocked(run.ID, reason) {
			continue
		}
		publishEvent(ctx, s.events, EventCaseRunBlocked, wave.VersionID,
			map[string]any{
				"wave_id":     platformid.Format(wave.ID),
				"case_run_id": platformid.Format(run.ID),
				"intent_id":   platformid.Format(run.IntentID),
			},
			map[string]any{
				"wave_id":     platformid.Format(wave.ID),
				"case_run_id": platformid.Format(run.ID),
				"intent_id":   platformid.Format(run.IntentID),
				"reason":      reason,
				"action":      action,
			}, nil)
	}
}

// markBlocked reports whether this (case_run, reason) pair must be published.
func (s *Scheduler) markBlocked(caseRunID int64, reason string) bool {
	s.blockedMu.Lock()
	defer s.blockedMu.Unlock()
	if previous, ok := s.blocked[caseRunID]; ok && previous == reason {
		return false
	}
	s.blocked[caseRunID] = reason
	return true
}

// clearBlocked forgets the blocked state once the run is dispatched.
func (s *Scheduler) clearBlocked(caseRunID int64) {
	s.blockedMu.Lock()
	defer s.blockedMu.Unlock()
	delete(s.blocked, caseRunID)
}

// requiredContextCount is the number of executor contexts a case needs: one per
// distinct (version_sut, account_role) pair (§5.3).
func requiredContextCount(snapshot execution.CaseSnapshot) int {
	type contextKey struct {
		sutID int64
		role  string
	}
	seen := map[contextKey]struct{}{}
	for _, step := range snapshot.Steps {
		sutID := step.TargetVersionSUTID
		if sutID <= 0 {
			sutID = snapshot.DefaultVersionSUTID
		}
		seen[contextKey{sutID: sutID, role: step.AccountRole}] = struct{}{}
	}
	if len(seen) == 0 {
		return 1
	}
	return len(seen)
}

func newDispatchToken() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return platformid.Format(time.Now().UnixNano())
	}
	return hex.EncodeToString(buffer)
}

func timePtr(value time.Time) *time.Time { return &value }

func int64PtrValue(value int64) *int64 { return &value }
