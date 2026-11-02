package executionapp

import (
	"context"
	"errors"
	"sort"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// planVersion creates the next wave of a version when it has open intents and
// no active wave (P3 §5.2 step 1). Everything else is left to dispatch.
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
