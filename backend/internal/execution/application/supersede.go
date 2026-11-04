package executionapp

import (
	"context"
	"errors"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Supersede reasons/next actions written to case_run.summary and execution
// events when a confirmed case version leaves the current+confirmed executable
// set (Task 106). They are stable codes: the UI localizes them and the report
// projects them, so a superseded case is never an unexplained row.
const (
	SupersedeReasonCaseVersion   = "case_version_superseded"
	SupersedeNextActionReplanned = "case_version_superseded_replanned"
	SupersedeNextActionReconfirm = "reconfirm_case_version"
)

// SupersedeResult reports one stale-intent retirement.
type SupersedeResult struct {
	// Superseded is false when another tick/leader already retired the intent.
	Superseded bool
	// Reason is the stable code surfaced on the event and run summary. It stays
	// SupersedeReasonCaseVersion for the normal invalidation and may name the
	// concrete blocker when the replacement could not be queued.
	Reason string
	// ReplacementCaseVersionID is the same logical case's current+confirmed
	// replacement, when the design read found one.
	ReplacementCaseVersionID int64
	// ReplannedIntent is the freshly enqueued initial intent for the
	// replacement. It is nil when no replacement exists, one is already queued,
	// or the version is not executable yet.
	ReplannedIntent *execution.ExecutionIntent
	// Replanned reports that the replacement confirmed revision is now queued
	// (freshly created or already queued/executed). It drives the visible
	// event/report field, so a found-but-unexecutable replacement is not
	// reported as re-planned.
	Replanned bool
}

// retireStaleIntent retires one active intent whose case version is no longer
// executable. The injected replanner owns the P3 §4.9 retire and the Task 106
// auto re-plan; degraded assemblies fall back to the plain retire so a stale
// intent is never left active.
func retireStaleIntent(
	ctx context.Context,
	intents IntentRepository,
	events EventPublisher,
	replanner SupersededIntentReplanner,
	intent execution.ExecutionIntent,
) (SupersedeResult, error) {
	if replanner != nil {
		return replanner.SupersedeAndReplan(ctx, intent)
	}
	result := SupersedeResult{Reason: SupersedeReasonCaseVersion}
	changed, err := intents.Transition(ctx, intent.ID, activeIntentStatuses, execution.IntentSuperseded)
	if err != nil {
		return SupersedeResult{}, mapExecutionError(err, "intent")
	}
	result.Superseded = changed
	if !changed {
		return result, nil
	}
	publishEvent(ctx, events, EventIntentSuperseded, intent.VersionID,
		map[string]any{
			"intent_id":       platformid.Format(intent.ID),
			"case_version_id": platformid.Format(intent.CaseVersionID),
		},
		map[string]any{
			"intent_id":       platformid.Format(intent.ID),
			"case_version_id": platformid.Format(intent.CaseVersionID),
			"reason":          result.Reason,
			"replanned":       false,
			"next_action":     SupersedeNextActionReconfirm,
		}, nil)
	return result, nil
}

// SupersedeAndReplan retires an active intent whose case version left the
// current+confirmed set and auto-enqueues the same logical case's replacement
// confirmed revision (Task 106). The stale intent is always retired (P3 §4.9);
// the replacement is queued when the case is executable again. Both outcomes
// are surfaced through the superseded event, so a selected case can never
// silently disappear from an execute.batch selection.
func (s *IntentService) SupersedeAndReplan(ctx context.Context, intent execution.ExecutionIntent) (SupersedeResult, error) {
	if err := requirePositiveID("intent_id", intent.ID); err != nil {
		return SupersedeResult{}, err
	}
	if err := requirePositiveID("version_id", intent.VersionID); err != nil {
		return SupersedeResult{}, err
	}
	result := SupersedeResult{Reason: SupersedeReasonCaseVersion}
	replacement, found, err := s.cases.ResolveReplacement(ctx, intent.VersionID, intent.CaseVersionID)
	if err != nil {
		return SupersedeResult{}, mapExecutionError(err, "case version")
	}
	if found && replacement.CaseVersionID > 0 && replacement.CaseVersionID != intent.CaseVersionID {
		result.ReplacementCaseVersionID = replacement.CaseVersionID
		next, queued, reason, err := s.enqueueReplacement(ctx, intent, replacement)
		if err != nil {
			return SupersedeResult{}, err
		}
		result.ReplannedIntent = next
		result.Replanned = queued
		if reason != "" {
			result.Reason = reason
		}
	}

	changed, err := s.intents.Transition(ctx, intent.ID, activeIntentStatuses, execution.IntentSuperseded)
	if err != nil {
		return SupersedeResult{}, mapExecutionError(err, "intent")
	}
	result.Superseded = changed
	if !changed {
		return result, nil
	}
	nextAction := SupersedeNextActionReconfirm
	if result.Replanned {
		nextAction = SupersedeNextActionReplanned
	}
	publishEvent(ctx, s.events, EventIntentSuperseded, intent.VersionID,
		map[string]any{
			"intent_id":       platformid.Format(intent.ID),
			"case_version_id": platformid.Format(intent.CaseVersionID),
		},
		map[string]any{
			"intent_id":                   platformid.Format(intent.ID),
			"case_version_id":             platformid.Format(intent.CaseVersionID),
			"reason":                      result.Reason,
			"replanned":                   result.Replanned,
			"replacement_case_version_id": formatOptionalID(result.ReplacementCaseVersionID),
			"next_action":                 nextAction,
		}, nil)
	s.recordAudit(ctx, "execution.intent_superseded", intent, 0)
	return result, nil
}

// enqueueReplacement creates one fresh initial intent for the replacement
// confirmed revision. A non-empty reason reports a surfaced blocker that is not
// a runtime error (the version is not executing yet, the replacement is
// incomplete, or its requirement is closed): the stale intent is still retired
// and the event tells the user what to do next. An already queued/executed
// replacement is a no-op.
func (s *IntentService) enqueueReplacement(ctx context.Context, stale execution.ExecutionIntent, replacement execution.CaseSnapshot) (*execution.ExecutionIntent, bool, string, error) {
	binding, err := s.versions.Get(ctx, stale.VersionID)
	if err != nil {
		return nil, false, "", mapExecutionError(err, "version")
	}
	if binding.Stage != intentExecutableStage && binding.Stage != intentCompletedStage {
		return nil, false, "version_not_executing", nil
	}
	if err := ensureExecutableCaseCompleteness([]execution.CaseSnapshot{replacement}); err != nil {
		return nil, false, "replacement_incomplete", nil
	}
	fingerprint, err := EnvironmentFingerprint(binding)
	if err != nil {
		return nil, false, "", err
	}
	baseline := CodeBaseline(binding)
	count, err := s.intents.CountInitialFor(ctx, replacement.CaseVersionID, fingerprint, baseline)
	if err != nil {
		return nil, false, "", mapExecutionError(err, "intent")
	}
	if count > 0 {
		return nil, true, "", nil
	}
	if binding.Status == "completed" {
		if err := s.versions.Reopen(ctx, stale.VersionID); err != nil {
			return nil, false, "", mapExecutionError(err, "version")
		}
	}
	now := s.now()
	next := execution.ExecutionIntent{
		ID:                     s.ids.Next(),
		VersionID:              stale.VersionID,
		CaseVersionID:          replacement.CaseVersionID,
		VersionSUTIDs:          caseIntentVersionSUTIDs(replacement),
		EnvironmentFingerprint: fingerprint,
		CodeBaseline:           baseline,
		Purpose:                execution.PurposeInitial,
		Status:                 execution.IntentOpen,
		DedupeKey:              DedupeKey(replacement.CaseVersionID, fingerprint, baseline, execution.PurposeInitial),
		AttemptNo:              1,
		CreatedAt:              now,
		UpdatedAt:              now,
	}
	if err := next.Validate(); err != nil {
		return nil, false, "", platform.Invalid("intent", "could not build a valid execution intent")
	}
	if err := s.createIntent(ctx, next); err != nil {
		switch {
		case errors.Is(err, execution.ErrDuplicate):
			// Another tick enqueued the same replacement; it is covered.
			return nil, true, "", nil
		case errors.Is(err, execution.ErrRequirementClosed):
			return nil, false, "replacement_requirement_closed", nil
		default:
			return nil, false, "", mapExecutionError(err, "intent")
		}
	}
	s.publishCreated(ctx, next, 0, stale.CaseVersionID)
	return &next, true, "", nil
}

// retireSupersededRun retires one non-terminal case_run whose confirmed case
// version left the executable set. The supersede reason is persisted on the run
// summary (so the run detail/report can explain the row), the run is moved to
// superseded, and the finished event carries the reason and replacement. It
// returns false when the run already reached a terminal state.
func retireSupersededRun(
	ctx context.Context,
	runs CaseRunRepository,
	events EventPublisher,
	versionID int64,
	run execution.CaseRun,
	replacementCaseVersionID int64,
	replanned bool,
) (bool, error) {
	nextAction := SupersedeNextActionReconfirm
	if replanned {
		nextAction = SupersedeNextActionReplanned
	}
	mutated, err := runs.UpdateCAS(ctx, run.ID, 0, "", func(current *execution.CaseRun) error {
		current.Summary.VerdictReason = SupersedeReasonCaseVersion
		current.Summary.TerminalCause = SupersedeReasonCaseVersion
		current.Summary.NextAction = nextAction
		if current.Summary.LastError == "" {
			current.Summary.LastError = "the confirmed case version was superseded before the run started"
		}
		return nil
	})
	if err != nil {
		return false, mapExecutionError(err, "case run")
	}
	if !mutated {
		return false, nil
	}
	changed, err := runs.Transition(ctx, run.ID,
		[]execution.CaseRunStatus{
			execution.CaseRunPending, execution.CaseRunRunning,
			execution.CaseRunPaused, execution.CaseRunInterrupted,
		},
		execution.CaseRunSuperseded, 0, "")
	if err != nil {
		return false, mapExecutionError(err, "case run")
	}
	if !changed {
		return false, nil
	}
	publishEvent(ctx, events, EventCaseRunFinished, versionID,
		map[string]any{
			"wave_id":     platformid.Format(run.WaveID),
			"case_run_id": platformid.Format(run.ID),
		},
		map[string]any{
			"case_run_id":                 platformid.Format(run.ID),
			"wave_id":                     platformid.Format(run.WaveID),
			"status":                      string(execution.CaseRunSuperseded),
			"reason":                      SupersedeReasonCaseVersion,
			"replanned":                   replanned,
			"replacement_case_version_id": formatOptionalID(replacementCaseVersionID),
			"next_action":                 nextAction,
		}, nil)
	return true, nil
}

// formatOptionalID renders a positive decimal ID for an event payload and an
// empty string for "no replacement".
func formatOptionalID(id int64) string {
	if id <= 0 {
		return ""
	}
	return platformid.Format(id)
}
