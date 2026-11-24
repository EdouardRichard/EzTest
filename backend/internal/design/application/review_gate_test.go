package designapp

// T9 (spec SC-E2/SC-E4/SC-E5): the shared designing -> case_review review gate.
// ONE predicate guards both advance paths: the design reconciler's automatic
// converge and the version stage service's manual advance (via the
// versionapp.DesignReviewGate port). These tests pin the predicate itself, the
// converge integration, and the single-source mutation contract (changing the
// gate's facts flips BOTH paths together).

import (
	"context"
	"errors"
	"net/http"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// The design gate service is the production satisfier of the version stage
// service's cross-context port (T13 wires the same instance into both paths).
var _ versionapp.DesignReviewGate = (*ReviewGateService)(nil)

// fakeReviewStates is the T6 review-state authority stand-in, keyed by logical
// case ID. An unseeded case is pending (never judged).
type fakeReviewStates struct {
	states map[int64]design.ReviewState
	err    error
}

func (f *fakeReviewStates) ReviewStateFor(_ context.Context, _, caseID, _ int64) (design.ReviewState, error) {
	if f.err != nil {
		return "", f.err
	}
	if state, ok := f.states[caseID]; ok {
		return state, nil
	}
	return design.ReviewStatePending, nil
}

func (f *fakeReviewStates) set(caseID int64, state design.ReviewState) {
	if f.states == nil {
		f.states = map[int64]design.ReviewState{}
	}
	f.states[caseID] = state
}

// newReviewGateFixture wires the real gate service over the planner's fake graph
// read and a programmable review-state authority, plus the planner converge
// path (coverage list + fake converger).
func newReviewGateFixture() (*Planner, *fakeRepo, *fakeReviewStates, *ReviewGateService, *fakeDesignConverger) {
	planner, repo, _, _, _, _, _ := newPlannerFixture()
	states := &fakeReviewStates{}
	gate := NewReviewGateService(repo, states)
	converger := &fakeDesignConverger{}
	planner.SetDesignConverger(converger)
	planner.SetReviewGate(gate)
	// The legacy coverage trigger (NeedsRedesign) sees usable coverage, so the
	// converge path reaches the shared gate; the graph read drives the predicate.
	repo.coverage = []design.RequirementCoverage{{
		Requirement: design.RequirementRef{
			ID: 10, VersionID: 1001, CurrentVersionID: 100, Revision: 1, Status: "clarified",
		},
		ScenarioCount: 1, DraftCount: 1,
	}}
	return planner, repo, states, gate, converger
}

// gateStageRepo is the minimal version.StageRepository stand-in for the manual
// advance path (compare-and-swap semantics preserved).
type gateStageRepo struct {
	stage   version.Stage
	applied []version.StageState
}

func (r *gateStageRepo) GetStage(_ context.Context, versionID int64) (version.StageState, error) {
	return version.StageState{VersionID: versionID, Stage: r.stage}, nil
}

func (r *gateStageRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	if r.stage != expected {
		return version.ErrStageConflict
	}
	r.stage = next.Stage
	r.applied = append(r.applied, next)
	return nil
}

// newManualAdvanceService builds the version stage service at designing with the
// count/coverage gates open, sharing the given review gate instance.
func newManualAdvanceService(gate versionapp.DesignReviewGate) (*versionapp.StageService, *gateStageRepo) {
	repo := &gateStageRepo{stage: version.StageDesigning}
	svc := versionapp.NewStageService(versionapp.StageServiceDeps{
		Repo: repo,
		Gates: versionapp.StageGates{
			ConfirmedCaseDrafts: func(context.Context, int64) (int, error) { return 1, nil },
			CoverageComputed:    func(context.Context, int64) (bool, error) { return true, nil },
		},
	})
	svc.SetDesignReviewGate(gate)
	return svc, repo
}

// TestReviewGateAllowsEscalatedToCaseReview pins R-E4r: fail-exhausted and
// judge-unavailable drafts are review-complete and proceed to case_review for a
// human decision; only pending/reworking block the gate.
func TestReviewGateAllowsEscalatedToCaseReview(t *testing.T) {
	_, repo, states, gate, _ := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	states.set(7001, design.ReviewStatePass)
	states.set(7002, design.ReviewStateEscalatedFail)
	if err := gate.ReviewGate(ctx, 1001); err != nil {
		t.Fatalf("escalated fail must pass the review gate: %v", err)
	}

	// judge_unavailable is also review-complete (human decision point).
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}, {"AC-1"}})
	states.set(7003, design.ReviewStateJudgeUnavailable)
	if err := gate.ReviewGate(ctx, 1001); err != nil {
		t.Fatalf("judge unavailable must pass the review gate: %v", err)
	}

	// A reworking draft blocks the whole version and is named.
	states.set(7003, design.ReviewStateReworking)
	err := gate.ReviewGate(ctx, 1001)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != JudgmentPendingCode {
		t.Fatalf("reworking gate error = %v, want a 409 %s", err, JudgmentPendingCode)
	}
	if apiErr.Details["case_id"] != "7003" {
		t.Fatalf("reworking gate must name case 7003: %#v", apiErr.Details)
	}

	// pending blocks the same way.
	states.set(7003, design.ReviewStatePending)
	if err := gate.ReviewGate(ctx, 1001); err == nil {
		t.Fatal("pending draft must block the review gate")
	}
}

// TestReviewGateNamesCoverageGap pins the coverage half of the predicate: the
// confirmed-union-draft set must cover every requirement AC, and the 409 names
// the missing AC ids (spec SC-E2).
func TestReviewGateNamesCoverageGap(t *testing.T) {
	_, repo, states, gate, _ := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}})
	states.set(7001, design.ReviewStatePass)
	err := gate.ReviewGate(ctx, 1001)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict || apiErr.Code != "ac_coverage_gap" {
		t.Fatalf("coverage gap error = %v, want a 409 ac_coverage_gap", err)
	}
	missing, _ := apiErr.Details["missing_acs"].([]string)
	if len(missing) != 1 || missing[0] != "AC-2" {
		t.Fatalf("missing_acs = %#v, want [AC-2]", apiErr.Details["missing_acs"])
	}
	if apiErr.Details["requirement_id"] != "10" {
		t.Fatalf("coverage gap must name requirement 10: %#v", apiErr.Details)
	}

	// A confirmed case completes the union: the gate passes (coverage accumulates
	// across batches, spec SC-E2).
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, [][]string{{"AC-2"}}, [][]string{{"AC-1"}})
	states.set(7002, design.ReviewStatePass)
	if err := gate.ReviewGate(ctx, 1001); err != nil {
		t.Fatalf("confirmed union must complete coverage: %v", err)
	}
}

// TestReviewGateAggregatesEveryGap pins that one pass names ALL blocking
// requirements/cases (coverage gaps and unreviewed drafts together), so the user
// can resolve the whole version at once.
func TestReviewGateAggregatesEveryGap(t *testing.T) {
	_, repo, states, gate, _ := newReviewGateFixture()
	ctx := context.Background()
	// Two drafts: one covers AC-1 (AC-2 missing), the other is unreviewed.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-1"}})
	states.set(7001, design.ReviewStatePass)
	// 7002 stays pending.
	err := gate.ReviewGate(ctx, 1001)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		t.Fatalf("aggregate gate error = %v, want a 409", err)
	}
	blockers, _ := apiErr.Details["blockers"].([]map[string]any)
	if len(blockers) != 2 {
		t.Fatalf("aggregate blockers = %#v, want the coverage gap AND the unreviewed case", apiErr.Details)
	}
}

// TestReviewGateUnwiredFailsClosed pins constitution VII.6: a gate without its
// graph reader or review-state authority cannot decide and fails closed instead
// of silently passing.
func TestReviewGateUnwiredFailsClosed(t *testing.T) {
	_, repo, _, _, _ := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1"}, nil, [][]string{{"AC-1"}})

	noStates := NewReviewGateService(repo, nil)
	if err := noStates.ReviewGate(ctx, 1001); err == nil {
		t.Fatal("unwired review-state reader must fail closed on a draft case")
	}
	noGraph := NewReviewGateService(nil, &fakeReviewStates{})
	if err := noGraph.ReviewGate(ctx, 1001); err == nil {
		t.Fatal("unwired graph reader must fail closed")
	}
}

// TestReviewGateInfraErrorNotVerdict pins that a review-state read failure
// propagates as itself (infrastructure), never misattributed as a case verdict.
func TestReviewGateInfraErrorNotVerdict(t *testing.T) {
	_, repo, states, gate, _ := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1"}, nil, [][]string{{"AC-1"}})
	states.err = errors.New("judgment store unavailable")
	err := gate.ReviewGate(ctx, 1001)
	if err == nil || err.Error() != "judgment store unavailable" {
		t.Fatalf("infra error = %v, want the raw store failure", err)
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
		t.Fatalf("infra failure misattributed as a 409 verdict: %v", err)
	}
}

// TestReviewGateIgnoresNonClarifiedRequirements pins the enumeration universe:
// only activated, still-clarified requirements on their current revision are
// gated (a draft requirement is not yet in the design scope).
func TestReviewGateIgnoresNonClarifiedRequirements(t *testing.T) {
	_, repo, _, gate, _ := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}})
	repo.requirement.Status = "draft"
	if err := gate.ReviewGate(ctx, 1001); err != nil {
		t.Fatalf("non-clarified requirement must not gate: %v", err)
	}
}

// TestConvergeRespectsReviewGate pins the automatic path: the design reconciler
// only converges designing -> case_review when the shared gate passes; a closed
// gate leaves the version designing (best-effort, retried next pass).
func TestConvergeRespectsReviewGate(t *testing.T) {
	planner, repo, states, _, converger := newReviewGateFixture()
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	states.set(7001, design.ReviewStatePass)
	// 7002 pending: coverage complete but review incomplete.
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with a closed gate: %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("closed review gate converged the version: %v", converger.versionIDs)
	}

	states.set(7002, design.ReviewStatePass)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with an open gate: %v", err)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != 1001 {
		t.Fatalf("converged versions = %v, want [1001] once the gate passes", converger.versionIDs)
	}
}

// TestConvergeUnwiredGateFailsClosed pins that a planner without the shared gate
// does NOT converge (the manual edge fails closed the same way; T13 wires the
// real gate into both).
func TestConvergeUnwiredGateFailsClosed(t *testing.T) {
	planner, repo, _, _, converger := newReviewGateFixture()
	planner.SetReviewGate(nil)
	ctx := context.Background()
	seedJudgeGraph(repo, 10, 100, []string{"AC-1"}, nil, [][]string{{"AC-1"}})
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile without a gate: %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("unwired gate converged the version: %v", converger.versionIDs)
	}
}

// TestConvergeAndManualShareGate is the single-source mutation contract (T9
// ruling 5): ONE real gate instance feeds both the design reconciler's converge
// path and the version stage service's manual advance. Mutating the gate's facts
// (coverage gap -> unreviewed draft -> review complete) flips BOTH paths
// together; neither path carries its own second truth.
func TestConvergeAndManualShareGate(t *testing.T) {
	planner, repo, states, gate, converger := newReviewGateFixture()
	ctx := context.Background()
	manual, manualRepo := newManualAdvanceService(gate)

	// Phase 1: coverage gap (AC-2 missing) blocks BOTH paths.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}})
	states.set(7001, design.ReviewStatePass)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (coverage gap): %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("coverage gap converged the version: %v", converger.versionIDs)
	}
	if err := manual.Request(ctx, 1001, version.StageCaseReview); err == nil {
		t.Fatal("manual advance passed despite the same coverage gap")
	}
	if manualRepo.stage != version.StageDesigning || len(manualRepo.applied) != 0 {
		t.Fatalf("manual advance wrote stage despite the gap: %s %#v", manualRepo.stage, manualRepo.applied)
	}

	// Phase 2: coverage complete but a draft unreviewed -> still BOTH blocked.
	seedJudgeGraph(repo, 10, 100, []string{"AC-1", "AC-2"}, nil, [][]string{{"AC-1"}, {"AC-2"}})
	// 7002 stays pending.
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (unreviewed): %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("unreviewed draft converged the version: %v", converger.versionIDs)
	}
	if err := manual.Request(ctx, 1001, version.StageCaseReview); err == nil {
		t.Fatal("manual advance passed despite the same unreviewed draft")
	}
	if manualRepo.stage != version.StageDesigning {
		t.Fatalf("manual advance moved stage despite the unreviewed draft: %s", manualRepo.stage)
	}

	// Phase 3: the SAME gate now passes -> BOTH paths advance.
	states.set(7002, design.ReviewStatePass)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile (review complete): %v", err)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != 1001 {
		t.Fatalf("converged versions = %v, want [1001] once the shared gate passes", converger.versionIDs)
	}
	if err := manual.Request(ctx, 1001, version.StageCaseReview); err != nil {
		t.Fatalf("manual advance blocked despite the shared gate passing: %v", err)
	}
	if manualRepo.stage != version.StageCaseReview || len(manualRepo.applied) != 1 {
		t.Fatalf("manual advance stage = %s applied = %#v, want one case_review write", manualRepo.stage, manualRepo.applied)
	}
}
