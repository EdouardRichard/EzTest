package executionapp

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// PauseService implements wave/use-case pause, resume, cancel and intent cancel
// (P3 §5.7/§5.8). Paused waves keep the single-active-wave slot (P3-D-10).
type PauseService struct {
	intents   IntentRepository
	waves     WaveRepository
	caseRuns  CaseRunRepository
	humans    HumanRequestRepository
	contexts  SUTContextRepository
	canceller RunCanceller
	executor  ExecutorControl
	clock     Clock
	events    EventPublisher
	audit     AuditRecorder
	// stageNotifier is the v0.22 wave-terminal stage callback (Task 25 fix
	// round 1). B §7.1 counts a cancelled wave as terminal, so CancelWave must
	// notify the stage machine just like the scheduler's done/failed paths. nil
	// keeps the pre-v0.22 behavior.
	stageNotifier WaveTerminalStageNotifier
}

// PauseServiceDeps wires the pause/resume service. contexts/executor/canceller
// may be nil in unit tests (cleanup is best effort).
type PauseServiceDeps struct {
	Intents   IntentRepository
	Waves     WaveRepository
	CaseRuns  CaseRunRepository
	Humans    HumanRequestRepository
	Contexts  SUTContextRepository
	Canceller RunCanceller
	Executor  ExecutorControl
	Clock     Clock
	Events    EventPublisher
	Audit     AuditRecorder
	// StageNotifier is the v0.22 wave-terminal stage callback (Task 25 fix
	// round 1). nil keeps the pre-v0.22 behavior; Task 36 wires
	// *versionapp.StageService here.
	StageNotifier WaveTerminalStageNotifier
}

// NewPauseService builds the pause/resume service.
func NewPauseService(deps PauseServiceDeps) *PauseService {
	return &PauseService{
		intents:       deps.Intents,
		waves:         deps.Waves,
		caseRuns:      deps.CaseRuns,
		humans:        deps.Humans,
		contexts:      deps.Contexts,
		canceller:     deps.Canceller,
		executor:      deps.Executor,
		clock:         deps.Clock,
		events:        deps.Events,
		audit:         deps.Audit,
		stageNotifier: deps.StageNotifier,
	}
}

// PauseWave pauses a wave. graceful (default) lets in-flight runs finish;
// immediate cancels them and stores their finished results.
func (s *PauseService) PauseWave(ctx context.Context, versionID, waveID, actorID int64, mode, reason string) (execution.RunWave, error) {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return execution.RunWave{}, err
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = execution.PauseModeGraceful
	}
	if !execution.ValidPauseMode(mode) {
		return execution.RunWave{}, platform.Invalid("mode", "must be graceful or immediate")
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	wave, err := s.waveOfVersion(ctx, versionID, waveID)
	if err != nil {
		return execution.RunWave{}, err
	}
	if wave.Status == execution.WavePaused {
		return wave, nil
	}
	if wave.Status.Terminal() {
		return execution.RunWave{}, platform.StateConflict("the wave is already finished")
	}
	now := s.now()
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePlanned, execution.WaveRunning}, execution.WavePaused,
		execution.RunWave{PausedAt: &now, PausedBy: actorID, PauseMode: mode, PauseReason: reason})
	if err != nil {
		return execution.RunWave{}, mapExecutionError(err, "wave")
	}
	if !changed {
		return s.waveOfVersion(ctx, versionID, waveID)
	}
	wave.Status = execution.WavePaused
	wave.PausedAt = &now
	wave.PausedBy = actorID
	wave.PauseMode = mode
	wave.PauseReason = reason
	if mode == execution.PauseModeImmediate {
		if err := s.pauseRunningRuns(ctx, wave, actorID, reason); err != nil {
			return execution.RunWave{}, err
		}
	}
	if s.waves != nil {
		if counts, err := s.waves.Counts(ctx, wave.ID); err == nil {
			wave.Counts = counts
			_, _ = s.waves.Update(ctx, wave)
		}
	}
	publishEvent(ctx, s.events, EventWavePaused, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{"wave_id": platformid.Format(wave.ID), "mode": mode, "reason": reason},
		actorPtr(actorID))
	s.record(ctx, "execution.wave_paused", "execution_wave", wave.ID, actorID, map[string]any{"mode": mode, "reason": reason})
	return wave, nil
}

// ResumeWave resumes a paused wave; an open human request must be resolved
// first (§5.7). Paused case_runs go back to pending with a new epoch.
func (s *PauseService) ResumeWave(ctx context.Context, versionID, waveID, actorID int64) (execution.RunWave, error) {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return execution.RunWave{}, err
	}
	wave, err := s.waveOfVersion(ctx, versionID, waveID)
	if err != nil {
		return execution.RunWave{}, err
	}
	if wave.Status == execution.WaveRunning {
		return wave, nil
	}
	if wave.Status != execution.WavePaused {
		return execution.RunWave{}, platform.StateConflict("the wave is not paused")
	}
	if err := s.requireNoOpenHuman(ctx, wave.ID); err != nil {
		return execution.RunWave{}, err
	}
	now := s.now()
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePaused}, execution.WaveRunning,
		execution.RunWave{PausedAt: &now})
	if err != nil {
		return execution.RunWave{}, mapExecutionError(err, "wave")
	}
	if !changed {
		return s.waveOfVersion(ctx, versionID, waveID)
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return execution.RunWave{}, mapExecutionError(err, "case run")
	}
	for _, run := range runs {
		if run.Status != execution.CaseRunPaused {
			continue
		}
		run.Status = execution.CaseRunPending
		run.DispatchEpoch++
		run.DispatchToken = ""
		run.LeaseUntil = nil
		run.PausedAt = nil
		run.PausedBy = nil
		run.PauseReason = ""
		run.UpdatedAt = now
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return execution.RunWave{}, mapExecutionError(err, "case run")
		}
		publishEvent(ctx, s.events, EventCaseRunResumed, wave.VersionID,
			map[string]any{"wave_id": platformid.Format(wave.ID), "case_run_id": platformid.Format(run.ID)},
			map[string]any{"case_run_id": platformid.Format(run.ID), "wave_id": platformid.Format(wave.ID)},
			actorPtr(actorID))
	}
	wave.Status = execution.WaveRunning
	wave.PausedAt = &now
	publishEvent(ctx, s.events, EventWaveResumed, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{"wave_id": platformid.Format(wave.ID)}, actorPtr(actorID))
	s.record(ctx, "execution.wave_resumed", "execution_wave", wave.ID, actorID, nil)
	return wave, nil
}

// PauseCaseRun pauses one case_run without changing the wave status.
func (s *PauseService) PauseCaseRun(ctx context.Context, versionID, caseRunID, actorID int64, mode, reason string) (execution.CaseRun, error) {
	if err := requirePositiveID("case_run_id", caseRunID); err != nil {
		return execution.CaseRun{}, err
	}
	mode = strings.TrimSpace(mode)
	if mode == "" {
		mode = execution.PauseModeImmediate
	}
	if mode != execution.PauseModeImmediate && mode != execution.PauseModeGraceful {
		return execution.CaseRun{}, platform.Invalid("mode", "must be graceful or immediate")
	}
	if len(reason) > 200 {
		reason = reason[:200]
	}
	run, err := s.caseRunOfVersion(ctx, versionID, caseRunID)
	if err != nil {
		return execution.CaseRun{}, err
	}
	if run.Status == execution.CaseRunPaused {
		return run, nil
	}
	if run.Status.Terminal() || run.Status == execution.CaseRunInterrupted {
		return execution.CaseRun{}, platform.StateConflict("the case run is already finished")
	}
	now := s.now()
	if run.Status == execution.CaseRunPending {
		run.Status = execution.CaseRunPaused
		run.PausedAt = &now
		run.PausedBy = actorPtr(actorID)
		run.PauseReason = reason
		run.UpdatedAt = now
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return execution.CaseRun{}, mapExecutionError(err, "case run")
		}
	} else {
		// running: immediate cancel, keep finished results
		if s.canceller != nil {
			s.canceller.CancelRun(run.ID)
		}
		changed, err := s.caseRuns.Transition(ctx, run.ID,
			[]execution.CaseRunStatus{execution.CaseRunRunning}, execution.CaseRunPaused, 0, "")
		if err != nil {
			return execution.CaseRun{}, mapExecutionError(err, "case run")
		}
		if !changed {
			return s.caseRunOfVersion(ctx, versionID, caseRunID)
		}
		run, _ = s.caseRuns.Get(ctx, run.ID)
		run.PausedAt = &now
		run.PausedBy = actorPtr(actorID)
		run.PauseReason = reason
		run.UpdatedAt = now
		if _, err := s.caseRuns.Update(ctx, run); err != nil {
			return execution.CaseRun{}, mapExecutionError(err, "case run")
		}
		s.cleanupContexts(ctx, run.ID)
	}
	publishEvent(ctx, s.events, EventCaseRunPaused, versionID,
		map[string]any{"case_run_id": platformid.Format(run.ID)},
		map[string]any{"case_run_id": platformid.Format(run.ID), "reason": reason}, actorPtr(actorID))
	s.record(ctx, "execution.case_run_paused", "case_run", run.ID, actorID, map[string]any{"mode": mode, "reason": reason})
	return run, nil
}

// ResumeCaseRun moves a paused case_run back to pending with a new epoch.
func (s *PauseService) ResumeCaseRun(ctx context.Context, versionID, caseRunID, actorID int64) (execution.CaseRun, error) {
	if err := requirePositiveID("case_run_id", caseRunID); err != nil {
		return execution.CaseRun{}, err
	}
	run, err := s.caseRunOfVersion(ctx, versionID, caseRunID)
	if err != nil {
		return execution.CaseRun{}, err
	}
	if run.Status == execution.CaseRunPending || run.Status == execution.CaseRunRunning {
		return run, nil
	}
	if run.Status != execution.CaseRunPaused {
		return execution.CaseRun{}, platform.StateConflict("the case run is not paused")
	}
	now := s.now()
	run.Status = execution.CaseRunPending
	run.DispatchEpoch++
	run.DispatchToken = ""
	run.LeaseUntil = nil
	run.PausedAt = nil
	run.PausedBy = nil
	run.PauseReason = ""
	run.UpdatedAt = now
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return execution.CaseRun{}, mapExecutionError(err, "case run")
	}
	publishEvent(ctx, s.events, EventCaseRunResumed, versionID,
		map[string]any{"case_run_id": platformid.Format(run.ID)},
		map[string]any{"case_run_id": platformid.Format(run.ID)}, actorPtr(actorID))
	s.record(ctx, "execution.case_run_resumed", "case_run", run.ID, actorID, nil)
	return run, nil
}

// CancelWave cancels an active wave: non-terminal case_runs become superseded
// and every still-active intent of the wave is cancelled, even when its
// case_run already reached a terminal status (QA-F2). Cancelled waves do not
// run failure analysis (P3-D-26). Repeating the call converges the remaining
// rows and returns the current value.
func (s *PauseService) CancelWave(ctx context.Context, versionID, waveID, actorID int64) (execution.RunWave, error) {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return execution.RunWave{}, err
	}
	wave, err := s.waveOfVersion(ctx, versionID, waveID)
	if err != nil {
		return execution.RunWave{}, err
	}
	if wave.Status.Terminal() && wave.Status != execution.WaveCancelled {
		return execution.RunWave{}, platform.StateConflict("the wave is already finished")
	}
	if err := s.cancelWaveRunsAndIntents(ctx, wave, actorID); err != nil {
		return execution.RunWave{}, err
	}
	if wave.Status == execution.WaveCancelled {
		// A repeated cancel is idempotent but re-notifies: the stage callback is
		// best effort, so this heals a stage advance lost after the first call.
		s.notifyStageWaveCancelled(ctx, wave)
		return wave, nil
	}
	now := s.now()
	changed, err := s.waves.Transition(ctx, wave.ID,
		[]execution.WaveStatus{execution.WavePlanned, execution.WaveRunning, execution.WavePaused},
		execution.WaveCancelled, execution.RunWave{FinishedAt: &now})
	if err != nil {
		return execution.RunWave{}, mapExecutionError(err, "wave")
	}
	if !changed {
		return s.waveOfVersion(ctx, versionID, waveID)
	}
	wave.Status = execution.WaveCancelled
	wave.FinishedAt = &now
	if counts, err := s.waves.Counts(ctx, wave.ID); err == nil {
		wave.Counts = counts
		_, _ = s.waves.Update(ctx, wave)
	}
	publishEvent(ctx, s.events, EventWaveFinished, wave.VersionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{"wave_id": platformid.Format(wave.ID), "status": string(execution.WaveCancelled), "counts": wave.Counts},
		actorPtr(actorID))
	s.record(ctx, "execution.wave_cancelled", "execution_wave", wave.ID, actorID, nil)
	// B §7.1 counts a cancelled wave as terminal: notify the v0.22 stage machine
	// after the cancel committed so executing -> reporting is not stranded on a
	// manual stage advance (Task 25 fix round 1).
	s.notifyStageWaveCancelled(ctx, wave)
	return wave, nil
}

// notifyStageWaveCancelled invokes the optional v0.22 wave-terminal stage
// callback for a cancelled wave. It is best effort: the cancellation is the
// source of truth, so a callback failure is logged and never fails or rolls back
// the cancel; the stage reconciler retries the advance.
func (s *PauseService) notifyStageWaveCancelled(ctx context.Context, wave execution.RunWave) {
	if s.stageNotifier == nil || wave.ID <= 0 {
		return
	}
	if err := s.stageNotifier.OnWaveTerminal(ctx, wave.ID); err != nil {
		slog.Default().WarnContext(ctx,
			"execution: cancelled-wave stage callback failed; the stage reconciler will retry",
			"version_id", wave.VersionID, "wave_id", wave.ID, "error", err)
	}
}

// cancelWaveRunsAndIntents propagates cancellation from a wave to its
// case_runs and intents. Case_runs that are still non-terminal become
// superseded; every still-active intent referenced by the wave's case_runs
// becomes cancelled. Terminal rows are left untouched, so the step is
// idempotent.
func (s *PauseService) cancelWaveRunsAndIntents(ctx context.Context, wave execution.RunWave, actorID int64) error {
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	intentIDs := make([]int64, 0, len(runs))
	seen := make(map[int64]struct{}, len(runs))
	for _, run := range runs {
		if run.IntentID > 0 {
			if _, ok := seen[run.IntentID]; !ok {
				seen[run.IntentID] = struct{}{}
				intentIDs = append(intentIDs, run.IntentID)
			}
		}
		if run.Status.Terminal() {
			continue
		}
		if s.canceller != nil {
			s.canceller.CancelRun(run.ID)
		}
		changed, err := s.caseRuns.Transition(ctx, run.ID,
			[]execution.CaseRunStatus{execution.CaseRunPending, execution.CaseRunRunning, execution.CaseRunPaused, execution.CaseRunInterrupted},
			execution.CaseRunSuperseded, 0, "")
		if err != nil {
			return mapExecutionError(err, "case run")
		}
		if !changed {
			continue
		}
		s.cleanupContexts(ctx, run.ID)
		publishEvent(ctx, s.events, EventCaseRunFinished, wave.VersionID,
			map[string]any{"wave_id": platformid.Format(wave.ID), "case_run_id": platformid.Format(run.ID)},
			map[string]any{
				"wave_id":     platformid.Format(wave.ID),
				"case_run_id": platformid.Format(run.ID),
				"status":      string(execution.CaseRunSuperseded),
			}, actorPtr(actorID))
	}
	sort.Slice(intentIDs, func(i, j int) bool { return intentIDs[i] < intentIDs[j] })
	for _, intentID := range intentIDs {
		changed, err := s.intents.Transition(ctx, intentID, activeIntentStatuses, execution.IntentCancelled)
		if err != nil {
			return mapExecutionError(err, "intent")
		}
		if !changed {
			continue
		}
		publishEvent(ctx, s.events, EventIntentCancelled, wave.VersionID,
			map[string]any{"intent_id": platformid.Format(intentID)},
			map[string]any{"intent_id": platformid.Format(intentID), "reason": "wave_cancelled"},
			actorPtr(actorID))
		s.record(ctx, "execution.intent_cancelled", "execution_intent", intentID, actorID, map[string]any{"reason": "wave_cancelled"})
	}
	return nil
}

// CancelIntent cancels one intent and supersedes its non-terminal case_runs.
func (s *PauseService) CancelIntent(ctx context.Context, versionID, intentID, actorID int64) error {
	if err := requirePositiveID("intent_id", intentID); err != nil {
		return err
	}
	intent, err := s.intents.Get(ctx, intentID)
	if err != nil {
		return mapExecutionError(err, "intent")
	}
	if versionID > 0 && intent.VersionID != versionID {
		return platform.NotFound("intent")
	}
	if intent.Status == execution.IntentCancelled {
		return nil
	}
	if intent.Status.Terminal() {
		return platform.StateConflict("the intent is already finished")
	}
	changed, err := s.intents.Transition(ctx, intentID, activeIntentStatuses, execution.IntentCancelled)
	if err != nil {
		return mapExecutionError(err, "intent")
	}
	if !changed {
		return nil
	}
	wave, err := s.waves.GetActive(ctx, intent.VersionID)
	if err == nil && wave != nil {
		runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
		if err == nil {
			for _, run := range runs {
				if run.IntentID != intentID || run.Status.Terminal() {
					continue
				}
				if s.canceller != nil {
					s.canceller.CancelRun(run.ID)
				}
				if _, err := s.caseRuns.Transition(ctx, run.ID,
					[]execution.CaseRunStatus{execution.CaseRunPending, execution.CaseRunRunning, execution.CaseRunPaused, execution.CaseRunInterrupted},
					execution.CaseRunSuperseded, 0, ""); err != nil {
					return mapExecutionError(err, "case run")
				}
				s.cleanupContexts(ctx, run.ID)
			}
		}
	}
	publishEvent(ctx, s.events, EventIntentCancelled, intent.VersionID,
		map[string]any{"intent_id": platformid.Format(intent.ID)},
		map[string]any{"intent_id": platformid.Format(intent.ID), "reason": "cancelled"}, actorPtr(actorID))
	s.record(ctx, "execution.intent_cancelled", "execution_intent", intent.ID, actorID, nil)
	return nil
}

func (s *PauseService) pauseRunningRuns(ctx context.Context, wave execution.RunWave, actorID int64, reason string) error {
	runs, err := s.caseRuns.ListAllByWave(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	now := s.now()
	for _, run := range runs {
		if run.Status != execution.CaseRunRunning {
			continue
		}
		if s.canceller != nil {
			s.canceller.CancelRun(run.ID)
		}
		if changed, err := s.caseRuns.Transition(ctx, run.ID,
			[]execution.CaseRunStatus{execution.CaseRunRunning}, execution.CaseRunPaused, 0, ""); err != nil {
			return mapExecutionError(err, "case run")
		} else if changed {
			item, _ := s.caseRuns.Get(ctx, run.ID)
			item.PausedAt = &now
			item.PausedBy = actorPtr(actorID)
			item.PauseReason = reason
			item.UpdatedAt = now
			if _, err := s.caseRuns.Update(ctx, item); err != nil {
				return mapExecutionError(err, "case run")
			}
			s.cleanupContexts(ctx, run.ID)
			publishEvent(ctx, s.events, EventCaseRunPaused, wave.VersionID,
				map[string]any{"case_run_id": platformid.Format(run.ID)},
				map[string]any{"case_run_id": platformid.Format(run.ID), "reason": reason}, actorPtr(actorID))
		}
	}
	return nil
}

// cleanupContexts closes the executor contexts of one case_run (best effort).
func (s *PauseService) cleanupContexts(ctx context.Context, caseRunID int64) {
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
			_ = s.executor.CancelContext(ctx, executorID, item.BrowserContextID)
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

func (s *PauseService) requireNoOpenHuman(ctx context.Context, waveID int64) error {
	if s.humans == nil {
		return nil
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, waveID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	for _, run := range runs {
		open, err := s.humans.GetOpenByCaseRun(ctx, run.ID)
		if err != nil {
			return mapExecutionError(err, "human_request")
		}
		if open != nil {
			return platform.StateConflict("resolve the open human request before resuming the wave")
		}
	}
	return nil
}

func (s *PauseService) waveOfVersion(ctx context.Context, versionID, waveID int64) (execution.RunWave, error) {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return execution.RunWave{}, err
	}
	wave, err := s.waves.Get(ctx, waveID)
	if err != nil {
		return execution.RunWave{}, mapExecutionError(err, "wave")
	}
	if versionID > 0 && wave.VersionID != versionID {
		return execution.RunWave{}, platform.NotFound("wave")
	}
	return wave, nil
}

func (s *PauseService) caseRunOfVersion(ctx context.Context, versionID, caseRunID int64) (execution.CaseRun, error) {
	run, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		return execution.CaseRun{}, mapExecutionError(err, "case run")
	}
	if versionID > 0 {
		wave, err := s.waves.Get(ctx, run.WaveID)
		if err != nil {
			return execution.CaseRun{}, mapExecutionError(err, "wave")
		}
		if wave.VersionID != versionID {
			return execution.CaseRun{}, platform.NotFound("case run")
		}
	}
	return run, nil
}

func (s *PauseService) record(ctx context.Context, action, kind string, id, actorID int64, metadata map[string]any) {
	recordAudit(ctx, s.audit, action, kind, id, actorID, metadata)
}

func (s *PauseService) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}
