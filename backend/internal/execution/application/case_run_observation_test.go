package executionapp

import (
	"context"
	"errors"
	"reflect"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// --- knowledge-dimension fact fakes -----------------------------------------
//
// The knowledge dimension reads the append-only sut_resolution /
// asset_registration facts. The fakes below serve the READ surface from memory
// and PANIC on the append surface: the projection under test must never write
// a fact (method discipline: a stub refuses questions it was never asked).

type fakeSUTResolutionStore struct {
	facts []execution.SUTResolution
	err   error
}

func (f *fakeSUTResolutionStore) AppendSUTResolution(context.Context, execution.SUTResolution) (execution.SUTResolution, error) {
	panic("knowledge dimension must not append sut_resolution facts")
}

func (f *fakeSUTResolutionStore) ListSUTResolutions(_ context.Context, _ int64) ([]execution.SUTResolution, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]execution.SUTResolution(nil), f.facts...), nil
}

type fakeAssetRegistrationStore struct {
	bySUT map[int64][]execution.AssetRegistration
	err   error
}

func (f *fakeAssetRegistrationStore) AppendAssetRegistration(context.Context, execution.AssetRegistration) (execution.AssetRegistration, error) {
	panic("knowledge dimension must not append asset_registration facts")
}

func (f *fakeAssetRegistrationStore) ListAssetRegistrations(_ context.Context, sutID int64) ([]execution.AssetRegistration, error) {
	if f.err != nil {
		return nil, f.err
	}
	return append([]execution.AssetRegistration(nil), f.bySUT[sutID]...), nil
}

type captureSuccessHook struct{ facts []CaseSuccessFact }

func (h *captureSuccessHook) OnCaseRunPassed(_ context.Context, fact CaseSuccessFact) error {
	h.facts = append(h.facts, fact)
	return nil
}

type captureFailureHook struct{ facts []CaseFailureFact }

func (h *captureFailureHook) OnCaseRunFailed(_ context.Context, fact CaseFailureFact) error {
	h.facts = append(h.facts, fact)
	return nil
}

func observationSnapshot() execution.CaseSnapshot {
	return execution.CaseSnapshot{
		Title: "下单流程",
		Steps: []execution.CaseStep{{Seq: 1, AccountRole: "admin", TargetDescription: "订单页"}},
	}
}

// TestCaseSuccessFactSkipsAnonymousRole pins R2-OBS-02: `anonymous` is the
// documented label of an unauthenticated step (see executionSUTBindings), not a
// bound test account. It must never seed the knowledge success fact, otherwise
// the success hook warns `account role "anonymous" is not bound`. Other roles
// (bound or not) keep their existing projection.
//
// v0.2.4-F T14 (SC-F10): the page/API dimension is restored from the execution
// facts (sut_resolution validated assets + proven asset registrations). With no
// facts the dimension stays empty — a prose step target is never turned into an
// id. The account-role dimension is unchanged.
func TestCaseSuccessFactSkipsAnonymousRole(t *testing.T) {
	run := execution.CaseRun{ID: 3001, CaseVersionID: 41}
	snapshot := execution.CaseSnapshot{
		Title: "未登录访问后台（负向）",
		Steps: []execution.CaseStep{
			{Seq: 1, AccountRole: "anonymous", TargetDescription: "后台首页"},
			{Seq: 2, AccountRole: " admin ", TargetDescription: "订单接口"},
			{Seq: 3, AccountRole: "ANONYMOUS"},
		},
	}

	fact := caseSuccessFact(10, run, snapshot, execution.CaseRunSummary{EvidenceCount: 2}, execution.SUTResolution{}, nil)
	if !reflect.DeepEqual(fact.AccountRoles, []string{"admin"}) {
		t.Fatalf("account roles = %#v, want [admin]", fact.AccountRoles)
	}
	if len(fact.PageIDs) != 0 {
		t.Fatalf("page ids = %#v, want none without resolution/registration facts", fact.PageIDs)
	}
	if len(fact.APIIDs) != 0 {
		t.Fatalf("api ids = %#v, want none without resolution/registration facts", fact.APIIDs)
	}

	onlyAnonymous := execution.CaseSnapshot{
		Title: "未登录访问",
		Steps: []execution.CaseStep{{Seq: 1, AccountRole: "anonymous"}},
	}
	fact = caseSuccessFact(10, run, onlyAnonymous, execution.CaseRunSummary{}, execution.SUTResolution{}, nil)
	if len(fact.AccountRoles) != 0 {
		t.Fatalf("account roles = %#v, want none for an unauthenticated-only case", fact.AccountRoles)
	}
}

// TestKnowledgePageAPIDimensionDecision is the POSITIVE pin of the SC-F10
// restoration (v0.2.4-F T14, R-E7). The C T22 decision ("dimension NOT restored,
// no clean source") is explicitly revisited: the P16 sut_resolution fact now
// records the resolved SUT's validated page/API assets server-side (controller
// ruling P20), and the P17 asset_registration fact records proven discoveries.
// Both hook facts (success and failure) must project that true source — non-empty
// when the facts carry ids — while the account-role dimension stays intact.
// Expectations come from the fact contract, not observed output.
func TestKnowledgePageAPIDimensionDecision(t *testing.T) {
	run := execution.CaseRun{ID: 3001, CaseVersionID: 41}
	snapshot := observationSnapshot()
	resolution := execution.SUTResolution{
		CaseRunID:        3001,
		SUTIDs:           []int64{7},
		ResolutionMethod: ResolutionMethodCatalogHit,
		TouchedPageIDs:   []int64{101},
		TouchedAPIIDs:    []int64{201},
	}

	success := caseSuccessFact(10, run, snapshot, execution.CaseRunSummary{EvidenceCount: 1}, resolution, nil)
	failure := caseFailureFact(10, run, snapshot, execution.FailureClassApplication, resolution, nil)
	if !reflect.DeepEqual(success.PageIDs, []int64{101}) || !reflect.DeepEqual(success.APIIDs, []int64{201}) {
		t.Fatalf("success dimension = pages %#v apis %#v, want the resolution true source [101]/[201]", success.PageIDs, success.APIIDs)
	}
	if !reflect.DeepEqual(failure.PageIDs, []int64{101}) || !reflect.DeepEqual(failure.APIIDs, []int64{201}) {
		t.Fatalf("failure dimension = pages %#v apis %#v, want the resolution true source [101]/[201]", failure.PageIDs, failure.APIIDs)
	}
	if len(success.AccountRoles) != 1 || success.AccountRoles[0] != "admin" ||
		len(failure.AccountRoles) != 1 || failure.AccountRoles[0] != "admin" {
		t.Fatalf("account-role dimension must stay intact: success=%#v failure=%#v", success.AccountRoles, failure.AccountRoles)
	}

	// No facts -> legitimately empty, never fabricated (C T22 ruling stands:
	// heuristic id fabrication from prose/URLs is forgery).
	emptySuccess := caseSuccessFact(10, run, snapshot, execution.CaseRunSummary{}, execution.SUTResolution{}, nil)
	emptyFailure := caseFailureFact(10, run, snapshot, execution.FailureClassApplication, execution.SUTResolution{}, nil)
	if len(emptySuccess.PageIDs) != 0 || len(emptySuccess.APIIDs) != 0 ||
		len(emptyFailure.PageIDs) != 0 || len(emptyFailure.APIIDs) != 0 {
		t.Fatal("without facts the dimension must stay empty (no fabricated ids)")
	}
}

// TestKnowledgeDimensionDirectoryHitNonEmpty pins Review Focus 2 end to end at
// the runner level: a DIRECTORY-HIT run (catalog_hit resolution, no exploration,
// no asset registrations) still projects a non-empty page/API dimension — the
// touched/validated asset ids recorded on the sut_resolution fact at Prepare
// (controller ruling P20). Both the success and the failure hook receive them.
func TestKnowledgeDimensionDirectoryHitNonEmpty(t *testing.T) {
	resolutions := &fakeSUTResolutionStore{facts: []execution.SUTResolution{{
		ID:               1,
		CaseRunID:        3001,
		SUTIDs:           []int64{7},
		ResolutionMethod: ResolutionMethodCatalogHit,
		TouchedPageIDs:   []int64{101},
		TouchedAPIIDs:    []int64{201},
	}}}
	success := &captureSuccessHook{}
	failure := &captureFailureHook{}
	runner := NewCaseRunner(CaseRunnerDeps{
		Resolutions:          resolutions,
		Registrations:        &fakeAssetRegistrationStore{},
		KnowledgeHook:        success,
		KnowledgeFailureHook: failure,
	})
	ctx := context.Background()
	snapshot := observationSnapshot()

	passed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunPassed}
	runner.notifyCaseSuccess(ctx, 10, passed, snapshot, execution.CaseRunSummary{EvidenceCount: 1})
	if len(success.facts) != 1 {
		t.Fatalf("success hook fired %d times, want 1", len(success.facts))
	}
	if !reflect.DeepEqual(success.facts[0].PageIDs, []int64{101}) || !reflect.DeepEqual(success.facts[0].APIIDs, []int64{201}) {
		t.Fatalf("directory-hit success dimension = pages %#v apis %#v, want [101]/[201] from the resolution fact", success.facts[0].PageIDs, success.facts[0].APIIDs)
	}

	failed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunFailed}
	runner.notifyCaseFailure(ctx, 10, failed, snapshot, execution.FailureClassApplication)
	if len(failure.facts) != 1 {
		t.Fatalf("failure hook fired %d times, want 1", len(failure.facts))
	}
	if !reflect.DeepEqual(failure.facts[0].PageIDs, []int64{101}) || !reflect.DeepEqual(failure.facts[0].APIIDs, []int64{201}) {
		t.Fatalf("directory-hit failure dimension = pages %#v apis %#v, want [101]/[201] from the resolution fact", failure.facts[0].PageIDs, failure.facts[0].APIIDs)
	}
}

// TestKnowledgeDimensionNoFabricatedID: a run without resolution/registration
// facts (or with an exploration resolution that touched nothing and registered
// nothing) gets an EMPTY dimension on both hooks. The projection must never
// invent ids from the prose step targets. Mutating the projection to fabricate
// ids (or to force non-empty without facts) fails this test.
func TestKnowledgeDimensionNoFabricatedID(t *testing.T) {
	for name, resolutions := range map[string]*fakeSUTResolutionStore{
		"no facts": {},
		"exploration touched nothing": {facts: []execution.SUTResolution{{
			ID:               1,
			CaseRunID:        3001,
			ResolutionMethod: ResolutionMethodExploration,
		}}},
	} {
		success := &captureSuccessHook{}
		failure := &captureFailureHook{}
		runner := NewCaseRunner(CaseRunnerDeps{
			Resolutions:          resolutions,
			Registrations:        &fakeAssetRegistrationStore{},
			KnowledgeHook:        success,
			KnowledgeFailureHook: failure,
		})
		ctx := context.Background()
		snapshot := observationSnapshot()

		passed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunPassed}
		runner.notifyCaseSuccess(ctx, 10, passed, snapshot, execution.CaseRunSummary{EvidenceCount: 1})
		failed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunFailed}
		runner.notifyCaseFailure(ctx, 10, failed, snapshot, execution.FailureClassApplication)

		if len(success.facts) != 1 || len(failure.facts) != 1 {
			t.Fatalf("%s: hooks fired success=%d failure=%d, want 1/1", name, len(success.facts), len(failure.facts))
		}
		if len(success.facts[0].PageIDs) != 0 || len(success.facts[0].APIIDs) != 0 ||
			len(failure.facts[0].PageIDs) != 0 || len(failure.facts[0].APIIDs) != 0 {
			t.Fatalf("%s: dimension must be empty without facts (fabrication is forgery): success=%+v failure=%+v", name, success.facts[0], failure.facts[0])
		}
		if !reflect.DeepEqual(success.facts[0].AccountRoles, []string{"admin"}) {
			t.Fatalf("%s: account-role dimension must stay intact: %#v", name, success.facts[0].AccountRoles)
		}
	}
}

// TestKnowledgeDimensionProvenRegistrations: an EXPLORATION run's dimension
// comes from the proven asset registrations of the resolved SUTs, unioned with
// the resolution's validated assets. Only the LATEST fact per asset counts and
// only gate_result=approved survives; db registrations are not a page/API
// dimension member. Ids are deduplicated and sorted.
func TestKnowledgeDimensionProvenRegistrations(t *testing.T) {
	resolutions := &fakeSUTResolutionStore{facts: []execution.SUTResolution{{
		ID:               1,
		CaseRunID:        3001,
		SUTIDs:           []int64{7},
		ResolutionMethod: ResolutionMethodExploration,
		TouchedPageIDs:   []int64{101},
	}}}
	registrations := &fakeAssetRegistrationStore{bySUT: map[int64][]execution.AssetRegistration{
		7: {
			{ID: 1, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 301, GateResult: AssetGateApproved},
			{ID: 2, SUTID: 7, AssetKind: execution.AssetKindAPI, AssetID: 401, GateResult: AssetGateApproved},
			{ID: 3, SUTID: 7, AssetKind: execution.AssetKindDB, AssetID: 501, GateResult: AssetGateApproved},
			{ID: 4, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 302, GateResult: AssetGateApproved},
			{ID: 5, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 302, GateResult: "rejected"},
			{ID: 6, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 303, GateResult: AssetGateApproved},
			{ID: 7, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 303, GateResult: AssetGateApproved},
			{ID: 8, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 101, GateResult: AssetGateApproved},
		},
	}}
	success := &captureSuccessHook{}
	runner := NewCaseRunner(CaseRunnerDeps{
		Resolutions:   resolutions,
		Registrations: registrations,
		KnowledgeHook: success,
	})
	passed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunPassed}
	runner.notifyCaseSuccess(context.Background(), 10, passed, observationSnapshot(), execution.CaseRunSummary{EvidenceCount: 1})
	if len(success.facts) != 1 {
		t.Fatalf("success hook fired %d times, want 1", len(success.facts))
	}
	if !reflect.DeepEqual(success.facts[0].PageIDs, []int64{101, 301, 303}) {
		t.Fatalf("page ids = %#v, want [101 301 303] (validated ∪ approved registrations, latest wins, deduped)", success.facts[0].PageIDs)
	}
	if !reflect.DeepEqual(success.facts[0].APIIDs, []int64{401}) {
		t.Fatalf("api ids = %#v, want [401]", success.facts[0].APIIDs)
	}
}

// TestKnowledgeDimensionReadFailureDegradesObservably: the knowledge hooks are
// best effort and must never fail settlement. A fact-read failure degrades the
// dimension to empty (logged loudly by the runner) while the hook still fires
// with the intact account-role dimension — never a fabricated id.
func TestKnowledgeDimensionReadFailureDegradesObservably(t *testing.T) {
	success := &captureSuccessHook{}
	runner := NewCaseRunner(CaseRunnerDeps{
		Resolutions:   &fakeSUTResolutionStore{err: errors.New("db down")},
		Registrations: &fakeAssetRegistrationStore{err: errors.New("db down")},
		KnowledgeHook: success,
	})
	passed := execution.CaseRun{ID: 3001, CaseVersionID: 41, Status: execution.CaseRunPassed}
	runner.notifyCaseSuccess(context.Background(), 10, passed, observationSnapshot(), execution.CaseRunSummary{EvidenceCount: 1})
	if len(success.facts) != 1 {
		t.Fatalf("success hook fired %d times, want 1 (best effort, never blocks)", len(success.facts))
	}
	if len(success.facts[0].PageIDs) != 0 || len(success.facts[0].APIIDs) != 0 {
		t.Fatalf("dimension = %+v, want empty on read failure (no fabrication)", success.facts[0])
	}
	if !reflect.DeepEqual(success.facts[0].AccountRoles, []string{"admin"}) {
		t.Fatalf("account-role dimension must stay intact: %#v", success.facts[0].AccountRoles)
	}
}
