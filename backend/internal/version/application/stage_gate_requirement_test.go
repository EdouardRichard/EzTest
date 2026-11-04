package versionapp_test

import (
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// newRequirementGateEnv wires the real requirement gate adapter into the stage
// machine, so the contract's "empty requirement_ids = confirm all current draft
// requirements" path is exercised end to end (list-and-expand in the adapter).
func newRequirementGateEnv(t *testing.T, requirements *requirementEnv) *stageEnv {
	t.Helper()
	repo := newFakeStageRepo(version.StageState{
		Stage: version.StageRequirementReview, StageEnteredAt: testTime,
	})
	jobs := &fakeStageJobs{}
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	cases := &fakeCaseGate{}
	machine := versionapp.NewStageMachine(repo, jobs, audit, publisher, fixedClock{at: testTime},
		versionapp.WithRequirementGateConfirmer(versionapp.NewRequirementGateConfirmer(requirements.svc)),
		versionapp.WithCaseGateConfirmer(cases),
	)
	return &stageEnv{
		machine: machine, repo: repo, jobs: jobs, audit: audit, publisher: publisher,
		cases: cases,
	}
}

// TestConfirmRequirementsEmptyMeansAll pins the contract-documented empty
// requirement_ids semantics (openapi StageConfirmRequirementsRequest): an empty
// selection is expanded to every current draft requirement of the version, the
// rows are confirmed and the gate advances to designing.
func TestConfirmRequirementsEmptyMeansAll(t *testing.T) {
	requirements := newRequirementEnv(t)
	first := createRequirement(t, requirements, "checkout applies coupons")
	second := createRequirement(t, requirements, "coupons expire")
	env := newRequirementGateEnv(t, requirements)
	ctx := stageUserContext(42)

	view, err := env.machine.OnRequirementConfirmed(ctx, requirements.versionID, nil)
	if err != nil {
		t.Fatalf("OnRequirementConfirmed(empty): %v", err)
	}
	if view.Stage != version.StageDesigning || env.repo.state.Stage != version.StageDesigning {
		t.Fatalf("stage = %q, want designing", env.repo.state.Stage)
	}
	for _, requirementID := range []int64{first.ID, second.ID} {
		stored, ok := requirements.repo.requirements[requirementID]
		if !ok {
			t.Fatalf("requirement %d disappeared from the repository", requirementID)
		}
		if stored.Status != version.RequirementClarified {
			t.Fatalf("requirement %d status = %q, want clarified", requirementID, stored.Status)
		}
	}
	if len(env.jobs.jobs) != 1 || env.jobs.jobs[0].Kind != versionapp.StageJobDesignCases {
		t.Fatalf("jobs = %#v, want one design.cases", env.jobs.jobs)
	}
	if env.jobs.jobs[0].RequirementIDs != nil {
		t.Fatalf("job requirement ids = %#v, want nil (all confirmed requirements)",
			env.jobs.jobs[0].RequirementIDs)
	}
	if len(env.audit.events) != 1 || env.audit.events[0].Action != "version.stage_decision" {
		t.Fatalf("audit = %#v, want one version.stage_decision", env.audit.events)
	}
}

// TestConfirmRequirementsEmptyNoDrafts pins the empty-gate edge: with no
// eligible draft requirement the gate answers a typed 409
// (no_requirements_to_confirm) and writes no stage, job, event or audit.
// TestRequirementGateAcceptsAlreadyConfirmedSelection pins the UI "开始设计"
// path after Task 78: requirements may be confirmed first through the plain
// confirmation API (which only converges to requirement_review); the explicit
// requirement gate must still accept those already-clarified IDs and advance to
// designing. Skipping already-confirmed requirements is not a failure.
func TestRequirementGateAcceptsAlreadyConfirmedSelection(t *testing.T) {
	requirements := newRequirementEnv(t)
	created := createRequirement(t, requirements, "checkout applies coupons")
	env := newRequirementGateEnv(t, requirements)
	ctx := stageUserContext(42)

	if _, err := requirements.svc.Confirm(ctx, requirements.versionID, created.ID, 42); err != nil {
		t.Fatalf("Confirm requirement: %v", err)
	}
	view, err := env.machine.OnRequirementConfirmed(ctx, requirements.versionID, []int64{created.ID})
	if err != nil {
		t.Fatalf("OnRequirementConfirmed(already clarified): %v", err)
	}
	if view.Stage != version.StageDesigning || env.repo.state.Stage != version.StageDesigning {
		t.Fatalf("stage = %q, want designing", env.repo.state.Stage)
	}
	if len(env.jobs.jobs) != 1 || env.jobs.jobs[0].Kind != versionapp.StageJobDesignCases {
		t.Fatalf("jobs = %#v, want one design.cases", env.jobs.jobs)
	}
	if got := env.jobs.jobs[0].RequirementIDs; len(got) != 1 || got[0] != created.ID {
		t.Fatalf("job requirement ids = %#v, want the selected clarified requirement", got)
	}
}

func TestConfirmRequirementsEmptyNoDrafts(t *testing.T) {
	requirements := newRequirementEnv(t)
	env := newRequirementGateEnv(t, requirements)
	ctx := stageUserContext(42)

	_, err := env.machine.OnRequirementConfirmed(ctx, requirements.versionID, nil)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v, want a typed *platform.Error", err)
	}
	if apiErr.Status != http.StatusConflict || apiErr.Code != "no_requirements_to_confirm" {
		t.Fatalf("error = %d/%s, want 409/no_requirements_to_confirm", apiErr.Status, apiErr.Code)
	}
	requireNoGateSideEffects(t, env, version.StageRequirementReview)
	if len(requirements.repo.confirmations) != 0 {
		t.Fatalf("gate wrote requirement confirmations: %#v", requirements.repo.confirmations)
	}
}
