package versionapp_test

// T9: the manual designing -> case_review advance consumes the SAME shared
// design review gate the design reconciler's converge path calls (the
// designapp.ReviewGateService satisfies DesignReviewGate; T13 wires one instance
// into both). These tests pin the version-side contract: nil fails closed, the
// gate's typed verdict propagates untouched, and the gate runs only after the
// legacy count/coverage gates pass. The cross-context single-source mutation
// proof lives in designapp's TestConvergeAndManualShareGate.

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeStageReviewGate is the programmable DesignReviewGate port stand-in.
type fakeStageReviewGate struct {
	err   error
	calls int
}

func (f *fakeStageReviewGate) ReviewGate(context.Context, int64) error {
	f.calls++
	return f.err
}

// TestManualAdvanceFailsClosedWithoutReviewGate pins ruling T9-4: a stage service
// without the shared review gate cannot advance designing -> case_review (no
// bypass), and the rejection is the typed gate_not_configured envelope.
func TestManualAdvanceFailsClosedWithoutReviewGate(t *testing.T) {
	repo := newFakeStageRepo(version.StageState{Stage: version.StageDesigning, StageEnteredAt: testTime})
	svc := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo:  repo,
		Gates: newStageGateState().gates(),
	})
	err := svc.Request(stageUserContext(42), stageServiceVersionID, version.StageCaseReview)
	var gateErr *versionapp.StageGateError
	if !errors.As(err, &gateErr) || gateErr.Gate != "design_review" || gateErr.Reason != "gate_not_configured" {
		t.Fatalf("unwired review gate error = %v, want design_review gate_not_configured", err)
	}
	if len(repo.applied) != 0 {
		t.Fatalf("unwired review gate wrote stage: %#v", repo.applied)
	}
}

// TestManualAdvancePropagatesReviewGateVerdict pins that the shared gate's typed
// 409 (naming the coverage gap / unreviewed cases) reaches the caller untouched
// and nothing is written.
func TestManualAdvancePropagatesReviewGateVerdict(t *testing.T) {
	env := newStageServiceEnv(t, version.StageDesigning)
	verdict := &platform.Error{
		Status:  http.StatusConflict,
		Code:    "judgment_pending",
		Message: "case 7002 version 8002 is not reviewed yet (pending)",
		Details: map[string]any{"case_id": "7002"},
	}
	gate := &fakeStageReviewGate{err: verdict}
	env.svc.SetDesignReviewGate(gate)

	err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageCaseReview)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "judgment_pending" || apiErr.Status != http.StatusConflict {
		t.Fatalf("review gate verdict = %v, want the untouched 409 judgment_pending", err)
	}
	if gate.calls != 1 {
		t.Fatalf("review gate calls = %d, want 1", gate.calls)
	}
	env.requireStage(t, version.StageDesigning)
	if len(env.repo.applied) != 0 || len(env.publisher.events) != 0 {
		t.Fatalf("blocked review gate wrote state/events: %#v %#v", env.repo.applied, env.publisher.events)
	}
}

// TestManualAdvanceRunsReviewGateAfterCountCoverage pins the gate ordering: the
// shared review predicate is consulted only once the legacy confirmed-draft
// count and coverage-computed gates pass (it adds to them, never replaces them).
func TestManualAdvanceRunsReviewGateAfterCountCoverage(t *testing.T) {
	env := newStageServiceEnv(t, version.StageDesigning)
	gate := &fakeStageReviewGate{}
	env.svc.SetDesignReviewGate(gate)

	// Count gate closed: the review gate must not even be consulted.
	env.gates.confirmedCaseDrafts = 0
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageCaseReview); err == nil {
		t.Fatal("closed count gate advanced the stage")
	}
	if gate.calls != 0 {
		t.Fatalf("review gate consulted before the count gate passed: %d calls", gate.calls)
	}

	// Coverage gate closed: still not consulted.
	env.gates.confirmedCaseDrafts = 3
	env.gates.coverageComputed = false
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageCaseReview); err == nil {
		t.Fatal("closed coverage gate advanced the stage")
	}
	if gate.calls != 0 {
		t.Fatalf("review gate consulted before the coverage gate passed: %d calls", gate.calls)
	}

	// Both legacy gates open and the review gate passes: the edge advances.
	env.gates.coverageComputed = true
	if err := env.svc.Request(stageUserContext(42), stageServiceVersionID, version.StageCaseReview); err != nil {
		t.Fatalf("Request(case_review) with every gate open: %v", err)
	}
	if gate.calls != 1 {
		t.Fatalf("review gate calls = %d, want 1", gate.calls)
	}
	env.requireStage(t, version.StageCaseReview)
}
