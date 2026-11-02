package version

// Stage is the version stage machine state (spec §5.3). The string values are
// persisted in version.stage and must stay in sync with the chk_version_stage
// CHECK in db/migrations/version_0007_stage_plan_fact.sql. Only the
// deterministic stage machine writes this value; LLM output never does.
type Stage string

// Version stages (spec §5.3).
const (
	StageCreated           Stage = "created"
	StageClarifying        Stage = "clarifying"
	StageRequirementReview Stage = "requirement_review"
	StageDesigning         Stage = "designing"
	StageCaseReview        Stage = "case_review"
	StageExecuting         Stage = "executing"
	StageReporting         Stage = "reporting"
	StageCompleted         Stage = "completed"
	StageBlocked           Stage = "blocked"
	StagePaused            Stage = "paused"
)

// CanTransitionTo reports whether the deterministic stage machine may move the
// version from s to next (spec §5.3). Unknown stages, self transitions and
// transitions that skip a hard gate are always rejected.
//
// blocked and paused are suspension stages orthogonal to the workflow: every
// workflow stage may enter them, and resuming accepts any workflow stage
// because only the persisted stage machine knows the pre-suspension stage.
// completed is terminal and is never a suspension or resume target.
func (s Stage) CanTransitionTo(next Stage) bool {
	if next == StageBlocked || next == StagePaused {
		return canSuspend(s)
	}
	if s == StageBlocked || s == StagePaused {
		return canSuspend(next)
	}
	switch s {
	case StageCreated:
		return next == StageClarifying
	case StageClarifying:
		return next == StageRequirementReview
	case StageRequirementReview:
		// User confirmation enters designing; rejecting the requirement gate
		// returns to clarifying (spec §5.5).
		return next == StageDesigning || next == StageClarifying
	case StageDesigning:
		return next == StageCaseReview
	case StageCaseReview:
		// User confirmation enters executing; rejecting the case gate returns
		// to designing (spec §5.5).
		return next == StageExecuting || next == StageDesigning
	case StageExecuting:
		// A terminal wave proceeds to reporting; partial execution with
		// unconfirmed cases returns to the case gate (spec §5.3).
		return next == StageReporting || next == StageCaseReview
	case StageReporting:
		return next == StageCompleted
	case StageCompleted:
		// A new goal reopens the version as an increment at clarifying.
		return next == StageClarifying
	default:
		return false
	}
}

// IsHumanGate reports whether stage is one of the two hard gates of the
// version lifecycle (spec §5.5). Only an explicit user decision may confirm a
// human gate; assistants and background roles never can.
func (s Stage) IsHumanGate() bool {
	return s == StageRequirementReview || s == StageCaseReview
}

// IsTerminal reports whether stage closes the current lifecycle. completed is
// the only terminal stage. blocked and paused are resumable suspension stages,
// and a completed version may still be reopened at clarifying for an
// increment (spec §5.3).
func (s Stage) IsTerminal() bool { return s == StageCompleted }

// canSuspend reports whether stage is a workflow stage that may be blocked or
// paused and is therefore a legal resume target. Suspended and terminal stages
// are excluded.
func canSuspend(s Stage) bool {
	switch s {
	case StageCreated, StageClarifying, StageRequirementReview, StageDesigning,
		StageCaseReview, StageExecuting, StageReporting:
		return true
	default:
		return false
	}
}
