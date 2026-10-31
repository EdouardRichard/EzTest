package executionapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sort"
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
	ids         IDGenerator
	clock       Clock
	events      EventPublisher
	instance    string
	leaseTTL    time.Duration
}

// SchedulerDeps wires the scheduler. quotaReader may be nil: the running set is
// then read from CaseRunRepository.ListRunning. leaseTTL defaults to 30s.
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
}

// NewScheduler builds the scheduler.
func NewScheduler(deps SchedulerDeps) *Scheduler {
	quota := deps.Quota
	if quota.Global <= 0 && quota.PerSUT <= 0 && quota.CrossSystem <= 0 {
		quota = execution.QuotaFromSettings(0, 0, 0)
	}
	lease := deps.LeaseTTL
	if lease <= 0 {
		lease = 30 * time.Second
	}
	return &Scheduler{
		intents:     deps.Intents,
		waves:       deps.Waves,
		caseRuns:    deps.CaseRuns,
		cases:       deps.Cases,
		versions:    deps.Versions,
		executors:   deps.Executors,
		workQueue:   deps.WorkQueue,
		quota:       quota,
		quotaReader: deps.QuotaReader,
		analyzer:    deps.Analyzer,
		ids:         deps.IDs,
		clock:       deps.Clock,
		events:      deps.Events,
		instance:    deps.Instance,
		leaseTTL:    lease,
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
	return nil
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
	executor, err := s.executors.Select(ctx)
	if err != nil {
		return mapExecutionError(err, "executor")
	}
	if executor == nil || !executor.Status.Dispatchable() {
		return nil
	}
	usage, err := s.runningUsage(ctx)
	if err != nil {
		return err
	}
	for index := range pending {
		run := pending[index]
		intent, err := s.intents.Get(ctx, run.IntentID)
		if err != nil {
			return mapExecutionError(err, "intent")
		}
		snapshot, err := s.cases.GetCaseVersion(ctx, wave.VersionID, run.CaseVersionID)
		if err != nil {
			// The case left the current+confirmed set between planning and
			// dispatch: retire the run and intent, never execute stale cases.
			if _, transitionErr := s.caseRuns.Transition(ctx, run.ID,
				[]execution.CaseRunStatus{execution.CaseRunPending}, execution.CaseRunSuperseded, 0, ""); transitionErr != nil {
				return mapExecutionError(transitionErr, "case run")
			}
			publishEvent(ctx, s.events, EventCaseRunFinished, wave.VersionID,
				map[string]any{"case_run_id": platformid.Format(run.ID), "intent_id": platformid.Format(intent.ID)},
				map[string]any{
					"case_run_id": platformid.Format(run.ID),
					"wave_id":     platformid.Format(wave.ID),
					"status":      string(execution.CaseRunSuperseded),
					"reason":      "not_current_confirmed",
				}, nil)
			if _, transitionErr := s.intents.Transition(ctx, intent.ID,
				activeIntentStatuses, execution.IntentSuperseded); transitionErr != nil {
				return mapExecutionError(transitionErr, "intent")
			}
			publishEvent(ctx, s.events, EventIntentSuperseded, wave.VersionID,
				map[string]any{
					"intent_id":       platformid.Format(intent.ID),
					"case_version_id": platformid.Format(intent.CaseVersionID),
				},
				map[string]any{
					"intent_id":       platformid.Format(intent.ID),
					"case_version_id": platformid.Format(intent.CaseVersionID),
					"reason":          "not_current_confirmed",
				}, nil)
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
		run.UpdatedAt = now
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return mapExecutionError(err, "case run")
		}
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
	return nil
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
