package executionapp

import (
	"context"
	"errors"
	"sort"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// planVersion creates the next wave of a version when it has open intents and
// no active wave (P3 §5.2 step 1). Everything else is left to dispatch.
//
// A wave only ever contains case_runs for case versions that are still
// current+confirmed at planning time: a selected case that left the executable
// set between intent creation and planning (a late design revision de-confirms
// it) is retired before any case_run row exists. Without this guard the run is
// inserted first and then superseded on dispatch with attempts=0, leaving a
// terminal placeholder whose case_id cannot be resolved (retry-7 S4→S5,
// run 375994775003729921).
func (s *Scheduler) planVersion(ctx context.Context, versionID int64) error {
	active, err := s.waves.GetActive(ctx, versionID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	if active != nil {
		return nil
	}
	open, err := s.intents.ListOpen(ctx, versionID)
	if err != nil {
		return mapExecutionError(err, "intent")
	}
	if len(open) == 0 {
		return nil
	}
	open, err = s.dispatchableIntents(ctx, versionID, open)
	if err != nil {
		return err
	}
	if len(open) == 0 {
		return nil
	}
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	now := s.now()
	wave := execution.RunWave{
		ID:        s.ids.Next(),
		VersionID: versionID,
		Status:    execution.WavePlanned,
		Trigger:   plannedTrigger(open),
		Counts:    execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	caseRuns := make([]execution.CaseRun, 0, len(open))
	for _, intent := range open {
		caseRuns = append(caseRuns, execution.CaseRun{
			ID:            s.ids.Next(),
			WaveID:        wave.ID,
			IntentID:      intent.ID,
			CaseVersionID: intent.CaseVersionID,
			Status:        execution.CaseRunPending,
			Summary:       execution.CaseRunSummary{},
			CreatedAt:     now,
			UpdatedAt:     now,
		})
	}
	if err := s.waves.CreateWithCaseRuns(ctx, wave, open, caseRuns); err != nil {
		if errors.Is(err, execution.ErrDuplicate) || errors.Is(err, execution.ErrStateConflict) {
			// Another tick won the race; the single-active-wave index owns the
			// invariant. Retry on the next tick.
			return nil
		}
		return mapExecutionError(err, "wave")
	}
	publishEvent(ctx, s.events, EventWaveCreated, versionID,
		map[string]any{"wave_id": platformid.Format(wave.ID)},
		map[string]any{
			"wave_id":      platformid.Format(wave.ID),
			"trigger":      wave.Trigger,
			"intent_count": len(open),
		},
		nil)
	return nil
}

// dispatchableIntents filters the open intents of one version down to the ones
// whose case version is still current+confirmed. A selected case that left the
// executable set is superseded here, before any case_run is inserted (P3 §4.9
// retires the intent; no run ever exists for it, so no attempts=0 terminal
// placeholder is manufactured). Task 106 also auto-queues the same logical
// case's replacement confirmed revision, and the superseded event carries the
// reason/replacement so a selected case is never silently dropped. A case read
// failure that is not "not found" is surfaced instead of retiring the intent.
func (s *Scheduler) dispatchableIntents(ctx context.Context, versionID int64, open []execution.ExecutionIntent) ([]execution.ExecutionIntent, error) {
	dispatchable := make([]execution.ExecutionIntent, 0, len(open))
	for _, intent := range open {
		if _, err := s.cases.GetCaseVersion(ctx, versionID, intent.CaseVersionID); err != nil {
			if !caseVersionNotExecutable(err) {
				return nil, mapExecutionError(err, "case version")
			}
			result, err := s.supersedeStaleIntent(ctx, intent)
			if err != nil {
				return nil, err
			}
			if result.ReplannedIntent != nil {
				// Task 106: the same logical case was re-confirmed as a new
				// revision; queue it in this wave instead of silently dropping
				// the selected case.
				dispatchable = append(dispatchable, *result.ReplannedIntent)
			}
			continue
		}
		dispatchable = append(dispatchable, intent)
	}
	return dispatchable, nil
}

// supersedeStaleIntent retires one no-longer-executable intent through the
// injected re-planner (Task 106), falling back to the plain P3 §4.9 retire.
func (s *Scheduler) supersedeStaleIntent(ctx context.Context, intent execution.ExecutionIntent) (SupersedeResult, error) {
	return retireStaleIntent(ctx, s.intents, s.events, s.replanner, intent)
}

// caseVersionNotExecutable reports whether a case read error means "not a
// current+confirmed case version" (a typed 404 from the design read model or
// execution.ErrNotFound from a degraded reader). Every other error is a real
// read failure and must not retire an intent.
func caseVersionNotExecutable(err error) bool {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr.Status == 404
	}
	return errors.Is(err, execution.ErrNotFound)
}

// plannedTrigger applies the frozen priority retest > manual > auto (§5.2).
func plannedTrigger(intents []execution.ExecutionIntent) string {
	trigger := execution.WaveTriggerAuto
	for _, intent := range intents {
		switch intent.Purpose {
		case execution.PurposeRetest:
			return execution.WaveTriggerRetest
		case execution.PurposeManual:
			trigger = execution.WaveTriggerManual
		}
	}
	return trigger
}

// now returns the scheduler clock, defaulting to UTC time.
func (s *Scheduler) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}
