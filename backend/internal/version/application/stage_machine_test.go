package versionapp_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- stage machine fakes ----------

type fakeStageRepo struct {
	state          version.StageState
	found          bool
	getErr         error
	applyErr       error
	expectedStages []version.Stage
	applied        []version.StageState
}

func newFakeStageRepo(state version.StageState) *fakeStageRepo {
	return &fakeStageRepo{state: state, found: true}
}

func (f *fakeStageRepo) GetStage(_ context.Context, versionID int64) (version.StageState, error) {
	if f.getErr != nil {
		return version.StageState{}, f.getErr
	}
	if !f.found {
		return version.StageState{}, version.ErrNotFound
	}
	state := f.state
	state.VersionID = versionID
	return state, nil
}

func (f *fakeStageRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.state.Stage != expected {
		return version.ErrStageConflict
	}
	f.expectedStages = append(f.expectedStages, expected)
	f.state = next
	f.applied = append(f.applied, next)
	return nil
}

var _ version.StageRepository = (*fakeStageRepo)(nil)

type fakeStageJobs struct {
	jobs []versionapp.StageJob
	err  error
}

func (f *fakeStageJobs) EnqueueStageJob(_ context.Context, job versionapp.StageJob) error {
	f.jobs = append(f.jobs, job)
	return f.err
}

var _ versionapp.StageJobEnqueuer = (*fakeStageJobs)(nil)

// fakeRequirementGate records the requirement batch-confirm calls the stage
// machine makes before moving the requirement hard gate.
type fakeRequirementGate struct {
	versionIDs []int64
	calls      [][]int64
	err        error
}

func (f *fakeRequirementGate) BatchConfirm(_ context.Context, versionID int64, requirementIDs []int64) error {
	f.versionIDs = append(f.versionIDs, versionID)
	f.calls = append(f.calls, append([]int64(nil), requirementIDs...))
	return f.err
}

var _ versionapp.RequirementGateConfirmer = (*fakeRequirementGate)(nil)

// fakeCaseGate records the case confirmation calls the stage machine makes
// before moving the case hard gate.
type fakeCaseGate struct {
	versionIDs []int64
	calls      [][]int64
	err        error
}

func (f *fakeCaseGate) ConfirmCases(_ context.Context, versionID int64, caseIDs []int64) error {
	f.versionIDs = append(f.versionIDs, versionID)
	f.calls = append(f.calls, append([]int64(nil), caseIDs...))
	return f.err
}

var _ versionapp.CaseGateConfirmer = (*fakeCaseGate)(nil)

type stageEnv struct {
	machine      *versionapp.StageMachine
	repo         *fakeStageRepo
	jobs         *fakeStageJobs
	audit        *fakeAudit
	publisher    *fakePublisher
	requirements *fakeRequirementGate
	cases        *fakeCaseGate
}

func newStageEnv(t *testing.T, initial version.StageState) *stageEnv {
	t.Helper()
	repo := newFakeStageRepo(initial)
	jobs := &fakeStageJobs{}
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	requirements := &fakeRequirementGate{}
	cases := &fakeCaseGate{}
	machine := versionapp.NewStageMachine(repo, jobs, audit, publisher, fixedClock{at: testTime},
		versionapp.WithRequirementGateConfirmer(requirements),
		versionapp.WithCaseGateConfirmer(cases),
	)
	return &stageEnv{
		machine: machine, repo: repo, jobs: jobs, audit: audit, publisher: publisher,
		requirements: requirements, cases: cases,
	}
}

// requireGateConflict asserts a wrong-source hard-gate call is a typed 409 and
// not a generic internal error.
func requireGateConflict(t *testing.T, err error) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a *platform.Error", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "invalid_stage_for_gate" {
		t.Fatalf("error = %d/%s, want 409/invalid_stage_for_gate", apiErr.Status, apiErr.Code)
	}
}

// requireNoGateSideEffects asserts a failed gate attempt wrote no stage, job,
// event or decision audit.
func requireNoGateSideEffects(t *testing.T, env *stageEnv, wantStage version.Stage) {
	t.Helper()
	if env.repo.state.Stage != wantStage || len(env.repo.applied) != 0 {
		t.Fatalf("stage written: state=%#v applied=%#v", env.repo.state, env.repo.applied)
	}
	if len(env.jobs.jobs) != 0 {
		t.Fatalf("gate failure enqueued jobs: %#v", env.jobs.jobs)
	}
	if len(env.publisher.events) != 0 {
		t.Fatalf("gate failure published events: %#v", env.publisher.events)
	}
	if len(env.audit.events) != 0 {
		t.Fatalf("gate failure wrote audit: %#v", env.audit.events)
	}
}

func stageUserContext(actorID int64) context.Context {
	return platform.WithActor(requestContext(), platform.Actor{ID: actorID, Role: "user"})
}

func stageEvents(publisher *fakePublisher) []map[string]any {
	events := make([]map[string]any, 0)
	for _, event := range publisher.events {
		if event.Type != "version.stage_changed" {
			continue
		}
		events = append(events, map[string]any{
			"channel": event.Channel,
			"payload": event.Payload,
		})
	}
	return events
}

func findStageEvent(t *testing.T, publisher *fakePublisher, channel string) map[string]any {
	t.Helper()
	events := stageEvents(publisher)
	for index := len(events) - 1; index >= 0; index-- {
		if events[index]["channel"] == channel {
			return events[index]["payload"].(map[string]any)
		}
	}
	t.Fatalf("no version.stage_changed event on channel %q (events = %#v)", channel, publisher.events)
	return nil
}

func lastDecisionAudit(t *testing.T, audit *fakeAudit) map[string]any {
	t.Helper()
	for index := len(audit.events) - 1; index >= 0; index-- {
		if audit.events[index].Action == "version.stage_decision" {
			return audit.events[index].Metadata
		}
	}
	t.Fatalf("no version.stage_decision audit entry (audits = %#v)", audit.events)
	return nil
}

func stringMetadata(t *testing.T, metadata map[string]any, key string) string {
	t.Helper()
	value, ok := metadata[key].(string)
	if !ok {
		t.Fatalf("metadata[%q] = %#v, want string", key, metadata[key])
	}
	return value
}

// TestStageMachineTransitions covers the two hard gates, the rejection back
// edges, illegal transitions and the stage-entry job dispatch.
func TestStageMachineTransitions(t *testing.T) {
	t.Run("requirement confirmation enters designing and enqueues design.cases", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{
			Stage: version.StageRequirementReview, StageEnteredAt: testTime,
		})
		ctx := stageUserContext(42)

		view, err := env.machine.OnRequirementConfirmed(ctx, 101, []int64{701, 702})
		if err != nil {
			t.Fatalf("OnRequirementConfirmed: %v", err)
		}
		if len(env.requirements.calls) != 1 || env.requirements.versionIDs[0] != 101 ||
			len(env.requirements.calls[0]) != 2 ||
			env.requirements.calls[0][0] != 701 || env.requirements.calls[0][1] != 702 {
			t.Fatalf("requirement confirmer calls = %#v (versions %#v)", env.requirements.calls, env.requirements.versionIDs)
		}
		if view.Stage != version.StageDesigning {
			t.Fatalf("stage = %q, want designing", view.Stage)
		}
		if env.repo.state.Stage != version.StageDesigning {
			t.Fatalf("persisted stage = %q, want designing", env.repo.state.Stage)
		}
		if len(env.jobs.jobs) != 1 {
			t.Fatalf("stage jobs = %#v, want one design.cases", env.jobs.jobs)
		}
		job := env.jobs.jobs[0]
		if job.VersionID != 101 || job.Kind != versionapp.StageJobDesignCases {
			t.Fatalf("job = %#v, want version 101 / design.cases", job)
		}
		if len(job.RequirementIDs) != 2 || job.RequirementIDs[0] != 701 || job.RequirementIDs[1] != 702 {
			t.Fatalf("job requirement ids = %#v", job.RequirementIDs)
		}
		payload := findStageEvent(t, env.publisher, "version:101")
		if payload["from"] != "requirement_review" || payload["to"] != "designing" {
			t.Fatalf("event payload = %#v", payload)
		}
		if payload["reason"] != "requirement_confirmed" {
			t.Fatalf("event reason = %#v", payload["reason"])
		}
		if payload["stage_entered_at"] != testTime.UTC().Format(time.RFC3339) {
			t.Fatalf("stage_entered_at = %#v", payload["stage_entered_at"])
		}
		metadata := lastDecisionAudit(t, env.audit)
		if stringMetadata(t, metadata, "gate") != "requirement" ||
			stringMetadata(t, metadata, "decision") != "confirmed" {
			t.Fatalf("audit metadata = %#v", metadata)
		}
	})

	t.Run("case confirmation enters executing and enqueues execute.batch", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{
			Stage: version.StageCaseReview, StageEnteredAt: testTime,
		})
		view, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{501, 502})
		if err != nil {
			t.Fatalf("OnCasesConfirmed: %v", err)
		}
		if len(env.cases.calls) != 1 || env.cases.versionIDs[0] != 101 ||
			len(env.cases.calls[0]) != 2 || env.cases.calls[0][0] != 501 || env.cases.calls[0][1] != 502 {
			t.Fatalf("case confirmer calls = %#v (versions %#v)", env.cases.calls, env.cases.versionIDs)
		}
		if view.Stage != version.StageExecuting || env.repo.state.Stage != version.StageExecuting {
			t.Fatalf("stage = %q, want executing", env.repo.state.Stage)
		}
		if len(env.jobs.jobs) != 1 {
			t.Fatalf("stage jobs = %#v, want one execute.batch", env.jobs.jobs)
		}
		job := env.jobs.jobs[0]
		if job.Kind != versionapp.StageJobExecuteBatch || len(job.CaseIDs) != 2 ||
			job.CaseIDs[0] != 501 || job.CaseIDs[1] != 502 {
			t.Fatalf("job = %#v", job)
		}
		payload := findStageEvent(t, env.publisher, "system")
		if payload["from"] != "case_review" || payload["to"] != "executing" ||
			payload["reason"] != "cases_confirmed" {
			t.Fatalf("event payload = %#v", payload)
		}
	})

	t.Run("reject returns the hard gate to its previous stage", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{
			Stage: version.StageRequirementReview, StageEnteredAt: testTime,
		})
		if _, err := env.machine.Reject(stageUserContext(42), 101, "missing acceptance criteria", nil); err != nil {
			t.Fatalf("Reject requirement gate: %v", err)
		}
		if env.repo.state.Stage != version.StageClarifying {
			t.Fatalf("stage = %q, want clarifying", env.repo.state.Stage)
		}
		if len(env.jobs.jobs) != 0 {
			t.Fatalf("reject must not enqueue stage jobs: %#v", env.jobs.jobs)
		}
		metadata := lastDecisionAudit(t, env.audit)
		if stringMetadata(t, metadata, "decision") != "rejected" ||
			stringMetadata(t, metadata, "target_stage") != "clarifying" {
			t.Fatalf("audit metadata = %#v", metadata)
		}

		env = newStageEnv(t, version.StageState{
			Stage: version.StageCaseReview, StageEnteredAt: testTime,
		})
		if _, err := env.machine.Reject(stageUserContext(42), 101, "coverage gap", nil); err != nil {
			t.Fatalf("Reject case gate: %v", err)
		}
		if env.repo.state.Stage != version.StageDesigning {
			t.Fatalf("stage = %q, want designing", env.repo.state.Stage)
		}

		// A reject outside a hard gate is a state conflict.
		env = newStageEnv(t, version.StageState{Stage: version.StageExecuting, StageEnteredAt: testTime})
		if _, err := env.machine.Reject(stageUserContext(42), 101, "nope", nil); err == nil {
			t.Fatal("reject outside a hard gate must fail")
		}
	})

	t.Run("illegal transitions are rejected without writing", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})
		for _, target := range []version.Stage{
			version.StageExecuting, // skips the requirement gate
			version.Stage("bogus"),
			version.StageBlocked, // suspension uses Suspend
			version.StageClarifying,
		} {
			if err := env.machine.Transition(requestContext(), 101, target, "attempt"); err == nil {
				t.Fatalf("transition clarifying -> %s must fail", target)
			}
		}
		if env.repo.state.Stage != version.StageClarifying {
			t.Fatalf("illegal transition wrote stage %q", env.repo.state.Stage)
		}
		if len(env.repo.applied) != 0 || len(env.jobs.jobs) != 0 || len(env.publisher.events) != 0 {
			t.Fatalf("illegal transition wrote: applied=%#v jobs=%#v events=%#v",
				env.repo.applied, env.jobs.jobs, env.publisher.events)
		}
	})

	t.Run("wave terminal enters reporting and enqueues report.version", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageExecuting, StageEnteredAt: testTime})
		if _, err := env.machine.OnWaveTerminal(requestContext(), 101); err != nil {
			t.Fatalf("OnWaveTerminal: %v", err)
		}
		if env.repo.state.Stage != version.StageReporting {
			t.Fatalf("stage = %q, want reporting", env.repo.state.Stage)
		}
		if len(env.jobs.jobs) != 1 || env.jobs.jobs[0].Kind != versionapp.StageJobReportVersion {
			t.Fatalf("jobs = %#v, want report.version", env.jobs.jobs)
		}
		payload := findStageEvent(t, env.publisher, "version:101")
		if payload["reason"] != "wave_terminal" {
			t.Fatalf("event reason = %#v", payload["reason"])
		}
	})

	t.Run("public transition publishes and enqueues the entry job", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageCreated, StageEnteredAt: testTime})
		if err := env.machine.Transition(requestContext(), 101, version.StageClarifying, "reopen"); err != nil {
			t.Fatalf("Transition created -> clarifying: %v", err)
		}
		if env.repo.state.Stage != version.StageClarifying {
			t.Fatalf("stage = %q, want clarifying", env.repo.state.Stage)
		}
		if len(env.jobs.jobs) != 0 {
			t.Fatalf("clarifying must not enqueue a stage job: %#v", env.jobs.jobs)
		}
		if len(stageEvents(env.publisher)) != 2 {
			t.Fatalf("stage events = %#v, want version + system", env.publisher.events)
		}
	})

	t.Run("transition publishes the trimmed reason", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageCreated, StageEnteredAt: testTime})
		if err := env.machine.Transition(requestContext(), 101, version.StageClarifying, "  reopen  "); err != nil {
			t.Fatalf("Transition created -> clarifying: %v", err)
		}
		payload := findStageEvent(t, env.publisher, "version:101")
		if payload["reason"] != "reopen" {
			t.Fatalf("event reason = %#v, want the trimmed reason", payload["reason"])
		}
	})

	t.Run("entering case_review does not enqueue review.cases", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageDesigning, StageEnteredAt: testTime})
		if err := env.machine.Transition(requestContext(), 101, version.StageCaseReview, "generated"); err != nil {
			t.Fatalf("Transition designing -> case_review: %v", err)
		}
		if len(env.jobs.jobs) != 0 {
			t.Fatalf("case_review must not enqueue a stage job: %#v", env.jobs.jobs)
		}
	})

	t.Run("missing version and concurrent stage writes map to stable errors", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})
		env.repo.found = false
		if err := env.machine.Transition(requestContext(), 999, version.StageRequirementReview, "advance"); err == nil {
			t.Fatal("transition of a missing version must fail")
		}

		env = newStageEnv(t, version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})
		env.repo.applyErr = version.ErrStageConflict
		if err := env.machine.Transition(requestContext(), 101, version.StageRequirementReview, "advance"); err == nil {
			t.Fatal("a lost stage race must fail")
		}
	})
}

// TestSuspendResumeRestoresRecordedStage covers the spec §5.3 guarantee: the
// suspension records the pre-suspension stage and a resume can only return
// there, never jump to another stage.
func TestSuspendResumeRestoresRecordedStage(t *testing.T) {
	env := newStageEnv(t, version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})

	if err := env.machine.Suspend(requestContext(), 101, version.StageBlocked, "database is unreachable"); err != nil {
		t.Fatalf("Suspend: %v", err)
	}
	if env.repo.state.Stage != version.StageBlocked {
		t.Fatalf("stage = %q, want blocked", env.repo.state.Stage)
	}
	if env.repo.state.SuspendedFrom == nil || *env.repo.state.SuspendedFrom != version.StageClarifying {
		t.Fatalf("suspended_from = %#v, want clarifying", env.repo.state.SuspendedFrom)
	}
	if env.repo.state.Blocker != "database is unreachable" {
		t.Fatalf("blocker = %q", env.repo.state.Blocker)
	}
	payload := findStageEvent(t, env.publisher, "version:101")
	if payload["reason"] != "blocked" || payload["blocker"] != "database is unreachable" {
		t.Fatalf("event payload = %#v", payload)
	}

	// A resume cannot select another stage, even a legitimate workflow stage.
	if err := env.machine.ResumeTo(requestContext(), 101, version.StageExecuting); err == nil {
		t.Fatal("ResumeTo(executing) must be rejected while suspended_from=clarifying")
	}
	if env.repo.state.Stage != version.StageBlocked {
		t.Fatalf("rejected ResumeTo wrote stage %q", env.repo.state.Stage)
	}

	if err := env.machine.Resume(requestContext(), 101); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if env.repo.state.Stage != version.StageClarifying {
		t.Fatalf("stage = %q, want clarifying", env.repo.state.Stage)
	}
	if env.repo.state.SuspendedFrom != nil {
		t.Fatalf("suspended_from = %#v, want cleared", env.repo.state.SuspendedFrom)
	}
	if env.repo.state.Blocker != "" {
		t.Fatalf("blocker = %q, want cleared", env.repo.state.Blocker)
	}
	resumed := findStageEvent(t, env.publisher, "system")
	if resumed["reason"] != "resumed" || resumed["to"] != "clarifying" {
		t.Fatalf("resume event payload = %#v", resumed)
	}

	// Pausing during execution resumes back into executing.
	env = newStageEnv(t, version.StageState{Stage: version.StageExecuting, StageEnteredAt: testTime})
	if err := env.machine.Suspend(requestContext(), 101, version.StagePaused, "user paused"); err != nil {
		t.Fatalf("Suspend paused: %v", err)
	}
	if err := env.machine.Resume(requestContext(), 101); err != nil {
		t.Fatalf("Resume paused: %v", err)
	}
	if env.repo.state.Stage != version.StageExecuting {
		t.Fatalf("stage = %q, want executing", env.repo.state.Stage)
	}

	// Only blocked/paused are suspension targets.
	env = newStageEnv(t, version.StageState{Stage: version.StageClarifying, StageEnteredAt: testTime})
	if err := env.machine.Suspend(requestContext(), 101, version.StageCompleted, "done"); err == nil {
		t.Fatal("Suspend(completed) must be rejected")
	}
}

// TestResumeWithNullSuspendedFromRejected pins the legacy/unknown-row rule: a
// suspension with no recorded previous stage cannot be resumed.
func TestResumeWithNullSuspendedFromRejected(t *testing.T) {
	env := newStageEnv(t, version.StageState{
		Stage: version.StageBlocked, StageEnteredAt: testTime, SuspendedFrom: nil,
	})
	if err := env.machine.Resume(requestContext(), 101); err == nil {
		t.Fatal("Resume with a NULL suspended_from must fail")
	}
	if env.repo.state.Stage != version.StageBlocked {
		t.Fatalf("failed Resume wrote stage %q", env.repo.state.Stage)
	}
	if err := env.machine.ResumeTo(requestContext(), 101, version.StageClarifying); err == nil {
		t.Fatal("ResumeTo with a NULL suspended_from must fail")
	}
	if len(env.repo.applied) != 0 {
		t.Fatalf("failed resume wrote %#v", env.repo.applied)
	}
}

// TestPartialCaseConfirm pins D23: an explicit case_ids list executes only
// those cases, while an empty list means "all confirmed cases".
func TestPartialCaseConfirm(t *testing.T) {
	env := newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
	if _, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{501, 502}); err != nil {
		t.Fatalf("OnCasesConfirmed: %v", err)
	}
	if len(env.jobs.jobs) != 1 || env.jobs.jobs[0].Kind != versionapp.StageJobExecuteBatch {
		t.Fatalf("jobs = %#v", env.jobs.jobs)
	}
	if got := env.jobs.jobs[0].CaseIDs; len(got) != 2 || got[0] != 501 || got[1] != 502 {
		t.Fatalf("case ids = %#v", got)
	}

	// Empty case_ids means all confirmed cases: the enqueuer receives no
	// restriction.
	env = newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
	if _, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, nil); err != nil {
		t.Fatalf("OnCasesConfirmed all: %v", err)
	}
	if env.jobs.jobs[0].CaseIDs != nil {
		t.Fatalf("case ids = %#v, want nil for all", env.jobs.jobs[0].CaseIDs)
	}
	if len(env.cases.calls) != 1 || env.cases.calls[0] != nil {
		t.Fatalf("case confirmer calls = %#v, want one nil (all) call", env.cases.calls)
	}

	// Invalid IDs never reach the stage write.
	env = newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
	if _, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{0}); err == nil {
		t.Fatal("zero case id must be rejected")
	}
	if env.repo.state.Stage != version.StageCaseReview || len(env.repo.applied) != 0 {
		t.Fatalf("invalid case ids wrote stage %q", env.repo.state.Stage)
	}
	if len(env.cases.calls) != 0 {
		t.Fatalf("invalid case ids reached the confirmer: %#v", env.cases.calls)
	}
}

// TestStageGateRequiresSourceStage pins the source-stage guard on both hard
// gates. The graph has a legitimate case_review -> designing reject edge, so a
// blind transition-based gate would let a requirement confirmation at
// case_review silently walk the version backwards and record a false decision.
func TestStageGateRequiresSourceStage(t *testing.T) {
	t.Run("requirement gate rejects case_review", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
		_, err := env.machine.OnRequirementConfirmed(stageUserContext(42), 101, []int64{701})
		requireGateConflict(t, err)
		requireNoGateSideEffects(t, env, version.StageCaseReview)
		if len(env.requirements.calls) != 0 {
			t.Fatalf("wrong-source gate confirmed requirements: %#v", env.requirements.calls)
		}
	})

	t.Run("requirement gate rejects designing and executing", func(t *testing.T) {
		for _, stage := range []version.Stage{version.StageDesigning, version.StageExecuting, version.StageCreated} {
			env := newStageEnv(t, version.StageState{Stage: stage, StageEnteredAt: testTime})
			_, err := env.machine.OnRequirementConfirmed(stageUserContext(42), 101, []int64{701})
			requireGateConflict(t, err)
			requireNoGateSideEffects(t, env, stage)
		}
	})

	t.Run("case gate rejects requirement_review", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageRequirementReview, StageEnteredAt: testTime})
		_, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{501})
		requireGateConflict(t, err)
		requireNoGateSideEffects(t, env, version.StageRequirementReview)
		if len(env.cases.calls) != 0 {
			t.Fatalf("wrong-source gate confirmed cases: %#v", env.cases.calls)
		}
	})

	t.Run("case gate rejects designing and executing", func(t *testing.T) {
		for _, stage := range []version.Stage{version.StageDesigning, version.StageExecuting, version.StageCreated} {
			env := newStageEnv(t, version.StageState{Stage: stage, StageEnteredAt: testTime})
			_, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{501})
			requireGateConflict(t, err)
			requireNoGateSideEffects(t, env, stage)
		}
	})

	t.Run("reject still uses the legitimate back edges", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
		if _, err := env.machine.Reject(stageUserContext(42), 101, "coverage gap", nil); err != nil {
			t.Fatalf("Reject case gate: %v", err)
		}
		if env.repo.state.Stage != version.StageDesigning {
			t.Fatalf("stage = %q, want designing", env.repo.state.Stage)
		}
	})
}

// TestStageGateConfirmationFailure writes no stage, job, event or decision when
// the underlying entity confirmation fails.
func TestStageGateConfirmationFailure(t *testing.T) {
	t.Run("requirements", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageRequirementReview, StageEnteredAt: testTime})
		env.requirements.err = platform.NotFound("requirement")
		_, err := env.machine.OnRequirementConfirmed(stageUserContext(42), 101, []int64{701})
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			t.Fatalf("error = %v, want a typed 404 from the confirmer", err)
		}
		requireNoGateSideEffects(t, env, version.StageRequirementReview)
	})

	t.Run("cases", func(t *testing.T) {
		env := newStageEnv(t, version.StageState{Stage: version.StageCaseReview, StageEnteredAt: testTime})
		env.cases.err = platform.NotFound("case")
		_, err := env.machine.OnCasesConfirmed(stageUserContext(42), 101, []int64{501})
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusNotFound {
			t.Fatalf("error = %v, want a typed 404 from the confirmer", err)
		}
		requireNoGateSideEffects(t, env, version.StageCaseReview)
	})
}
