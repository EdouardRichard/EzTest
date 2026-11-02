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
// workflow stage may enter them, but a suspended version never leaves a
// suspension through CanTransitionTo. Resuming is guarded separately by
// CanResumeTo with the stage recorded in version.suspended_from, so a resume
// cannot jump ahead and skip a hard gate. completed is terminal and is never a
// suspension or resume target.
func (s Stage) CanTransitionTo(next Stage) bool {
	if next == StageBlocked || next == StagePaused {
		return isWorkflowStage(s)
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

// IsSuspended reports whether stage is a suspension state (blocked or paused).
func (s Stage) IsSuspended() bool { return s == StageBlocked || s == StagePaused }

// CanResumeTo reports whether a version suspended at s may resume to prev.
// prev must be the workflow stage recorded in version.suspended_from when the
// version entered the suspension; the caller is responsible for passing that
// recorded value, so a suspended version cannot resume to any other stage and
// skip a hard gate. Terminal, suspended and unknown targets are rejected, as
// is any source that is not suspended.
func (s Stage) CanResumeTo(prev Stage) bool {
	return s.IsSuspended() && isWorkflowStage(prev)
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

// isWorkflowStage reports whether stage is a workflow stage: the stages a
// version can be suspended from and resumed to. Suspended and terminal stages
// are excluded.
func isWorkflowStage(s Stage) bool {
	switch s {
	case StageCreated, StageClarifying, StageRequirementReview, StageDesigning,
		StageCaseReview, StageExecuting, StageReporting:
		return true
	default:
		return false
	}
}
