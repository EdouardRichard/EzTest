package version_test

import (
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestStageTransitions(t *testing.T) {
	workflow := []version.Stage{
		version.StageCreated,
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
		version.StageExecuting,
		version.StageReporting,
	}
	type transition struct{ from, to version.Stage }

	allowed := []transition{
		// Forward progression (spec §5.3).
		{version.StageCreated, version.StageClarifying},
		{version.StageClarifying, version.StageRequirementReview},
		{version.StageRequirementReview, version.StageDesigning},
		{version.StageDesigning, version.StageCaseReview},
		{version.StageCaseReview, version.StageExecuting},
		{version.StageExecuting, version.StageReporting},
		{version.StageReporting, version.StageCompleted},
		// Rejection from the two hard gates (spec §5.5).
		{version.StageRequirementReview, version.StageClarifying},
		{version.StageCaseReview, version.StageDesigning},
		// Partial execution returns to the case gate (spec §5.3).
		{version.StageExecuting, version.StageCaseReview},
		// A new goal reopens a completed version at clarifying (spec §5.3).
		{version.StageCompleted, version.StageClarifying},
	}
	for _, stage := range workflow {
		allowed = append(allowed,
			transition{stage, version.StageBlocked},
			transition{stage, version.StagePaused},
		)
	}
	for _, tc := range allowed {
		if !tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be allowed", tc.from, tc.to)
		}
	}

	denied := []transition{
		// The two hard gates must not be bypassed (spec §5.5).
		{version.StageCreated, version.StageExecuting},
		{version.StageCreated, version.StageRequirementReview},
		{version.StageClarifying, version.StageDesigning},
		{version.StageRequirementReview, version.StageExecuting},
		{version.StageDesigning, version.StageExecuting},
		{version.StageCaseReview, version.StageReporting},
		{version.StageCaseReview, version.StageClarifying},
		// Stages only advance forward, except for the documented back edges.
		{version.StageExecuting, version.StageCompleted},
		{version.StageReporting, version.StageExecuting},
		{version.StageClarifying, version.StageCreated},
		// completed is terminal: only an incremental reopen is allowed.
		{version.StageCompleted, version.StageDesigning},
		{version.StageCompleted, version.StageExecuting},
		{version.StageCompleted, version.StageBlocked},
		{version.StageCompleted, version.StagePaused},
		// A suspended version never resumes through CanTransitionTo: the resume
		// target must be checked against the persisted suspended_from stage
		// (CanResumeTo), so no arbitrary workflow stage is reachable here.
		{version.StageBlocked, version.StageClarifying},
		{version.StageBlocked, version.StageExecuting},
		{version.StagePaused, version.StageDesigning},
		{version.StageBlocked, version.StageCompleted},
		{version.StagePaused, version.StageCompleted},
		{version.StageBlocked, version.StagePaused},
		{version.StagePaused, version.StageBlocked},
		// Unknown stages are never valid.
		{version.Stage("bogus"), version.StageClarifying},
		{version.StageClarifying, version.Stage("bogus")},
	}
	for _, stage := range append(workflow,
		version.StageCompleted,
		version.StageBlocked,
		version.StagePaused,
	) {
		denied = append(denied, transition{stage, stage})
	}
	for _, tc := range denied {
		if tc.from.CanTransitionTo(tc.to) {
			t.Errorf("%s -> %s must be rejected", tc.from, tc.to)
		}
	}
}

func TestSuspensionRequiresRecordedStage(t *testing.T) {
	// Entering a suspension is a normal forward edge from any workflow stage.
	if !version.StageClarifying.CanTransitionTo(version.StageBlocked) {
		t.Error("clarifying -> blocked must be allowed")
	}
	// Leaving a suspension is never a regular transition: it must be checked
	// against the stage recorded in version.suspended_from via CanResumeTo.
	if version.StageClarifying.CanTransitionTo(version.StageExecuting) {
		t.Error("clarifying -> executing must be rejected (two hard gates)")
	}
	if version.StageBlocked.CanTransitionTo(version.StageExecuting) {
		t.Error("blocked -> executing must be rejected; resume needs the recorded stage")
	}

	// A recorded stage is always a workflow stage: a version can be blocked or
	// paused at any point of the workflow, including while executing.
	resumable := []struct{ from, prev version.Stage }{
		{version.StageBlocked, version.StageCreated},
		{version.StageBlocked, version.StageClarifying},
		{version.StageBlocked, version.StageRequirementReview},
		{version.StagePaused, version.StageDesigning},
		{version.StagePaused, version.StageCaseReview},
		{version.StageBlocked, version.StageExecuting},
		{version.StagePaused, version.StageReporting},
	}
	for _, tc := range resumable {
		if !tc.from.CanResumeTo(tc.prev) {
			t.Errorf("%s.CanResumeTo(%s) = false, want true", tc.from, tc.prev)
		}
	}
	// A resume never finishes the version, re-suspends it or accepts garbage.
	for _, prev := range []version.Stage{
		version.StageCompleted, version.StageBlocked, version.StagePaused,
		version.Stage("bogus"),
	} {
		if version.StageBlocked.CanResumeTo(prev) {
			t.Errorf("blocked.CanResumeTo(%s) = true, want false", prev)
		}
	}
	// Only a suspended stage resumes; a workflow or terminal stage cannot.
	for _, from := range []version.Stage{
		version.StageCreated, version.StageClarifying, version.StageRequirementReview,
		version.StageExecuting, version.StageReporting, version.StageCompleted,
		version.Stage("bogus"),
	} {
		if from.CanResumeTo(version.StageClarifying) {
			t.Errorf("%s.CanResumeTo(clarifying) = true, want false", from)
		}
	}
}

func TestStageSuspended(t *testing.T) {
	cases := []struct {
		stage     version.Stage
		suspended bool
	}{
		{version.StageCreated, false},
		{version.StageClarifying, false},
		{version.StageRequirementReview, false},
		{version.StageDesigning, false},
		{version.StageCaseReview, false},
		{version.StageExecuting, false},
		{version.StageReporting, false},
		{version.StageCompleted, false},
		{version.StageBlocked, true},
		{version.StagePaused, true},
		{version.Stage("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.stage.IsSuspended(); got != tc.suspended {
			t.Errorf("%s.IsSuspended() = %v, want %v", tc.stage, got, tc.suspended)
		}
	}
}

func TestStageWireValues(t *testing.T) {
	// The persisted strings must equal chk_version_stage in
	// db/migrations/version_0007_stage_plan_fact.sql (spec §5.3).
	cases := []struct {
		stage version.Stage
		wire  string
	}{
		{version.StageCreated, "created"},
		{version.StageClarifying, "clarifying"},
		{version.StageRequirementReview, "requirement_review"},
		{version.StageDesigning, "designing"},
		{version.StageCaseReview, "case_review"},
		{version.StageExecuting, "executing"},
		{version.StageReporting, "reporting"},
		{version.StageCompleted, "completed"},
		{version.StageBlocked, "blocked"},
		{version.StagePaused, "paused"},
	}
	for _, tc := range cases {
		if string(tc.stage) != tc.wire {
			t.Errorf("stage constant = %q, want %q (must match chk_version_stage)", tc.stage, tc.wire)
		}
	}
}

func TestStageHumanGates(t *testing.T) {
	cases := []struct {
		stage version.Stage
		gate  bool
	}{
		{version.StageCreated, false},
		{version.StageClarifying, false},
		{version.StageRequirementReview, true},
		{version.StageDesigning, false},
		{version.StageCaseReview, true},
		{version.StageExecuting, false},
		{version.StageReporting, false},
		{version.StageCompleted, false},
		{version.StageBlocked, false},
		{version.StagePaused, false},
		{version.Stage("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.stage.IsHumanGate(); got != tc.gate {
			t.Errorf("%s.IsHumanGate() = %v, want %v", tc.stage, got, tc.gate)
		}
	}
}

func TestStageTerminal(t *testing.T) {
	cases := []struct {
		stage    version.Stage
		terminal bool
	}{
		{version.StageCreated, false},
		{version.StageClarifying, false},
		{version.StageRequirementReview, false},
		{version.StageDesigning, false},
		{version.StageCaseReview, false},
		{version.StageExecuting, false},
		{version.StageReporting, false},
		{version.StageCompleted, true},
		{version.StageBlocked, false},
		{version.StagePaused, false},
		{version.Stage("bogus"), false},
	}
	for _, tc := range cases {
		if got := tc.stage.IsTerminal(); got != tc.terminal {
			t.Errorf("%s.IsTerminal() = %v, want %v", tc.stage, got, tc.terminal)
		}
	}
}
