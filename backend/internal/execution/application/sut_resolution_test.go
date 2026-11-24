package executionapp

import (
	"context"
	"errors"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// --- fakes -----------------------------------------------------------------

// fakeSUTCatalog is the effective-scope catalog reader stub. It returns exactly
// the rows it was given (the production adapter gates on EffectiveSUTs, so the
// service trusts the projection and never re-implements the SUT-status gate).
type fakeSUTCatalog struct {
	suts []ResolvedSUT
	err  error
}

func (f *fakeSUTCatalog) EffectiveSUTCatalog(context.Context, int64) ([]ResolvedSUT, error) {
	return f.suts, f.err
}

// fakeResolutionStore is the append-only fact store stub. It records every
// Append call so a test can assert what was written (and that an idempotent
// re-resolution appends nothing).
type fakeResolutionStore struct {
	appended  []execution.SUTResolution
	list      []execution.SUTResolution
	appendErr error
	listErr   error
}

func (f *fakeResolutionStore) AppendSUTResolution(_ context.Context, r execution.SUTResolution) (execution.SUTResolution, error) {
	if f.appendErr != nil {
		return execution.SUTResolution{}, f.appendErr
	}
	r.ID = int64(len(f.appended) + 1)
	f.appended = append(f.appended, r)
	return r, nil
}

func (f *fakeResolutionStore) ListSUTResolutions(context.Context, int64) ([]execution.SUTResolution, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.list, nil
}

// fakeResolutionPublisher captures published events.
type fakeResolutionPublisher struct {
	events []eventingapp.Event
}

func (f *fakeResolutionPublisher) Publish(_ context.Context, e eventingapp.Event) error {
	f.events = append(f.events, e)
	return nil
}

type fakeResolutionClock struct{ at time.Time }

func (f fakeResolutionClock) Now() time.Time { return f.at }

func newResolutionService(catalog *fakeSUTCatalog, store *fakeResolutionStore, pub *fakeResolutionPublisher) *SUTResolutionService {
	return NewSUTResolutionService(SUTResolutionDeps{
		Catalog:     catalog,
		Resolutions: store,
		Events:      pub,
		Clock:       fakeResolutionClock{at: time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)},
	})
}

// --- tests -----------------------------------------------------------------

// TestSUTResolutionRecordsTouchedAssets pins Review Focus 2: a directory-hit run
// (the case's sut_description matches a catalog SUT that has effective assets)
// records a sut_resolution fact whose touched page/API ids are NON-EMPTY — the
// server-truth source the knowledge dimension reads even though the run never
// explores. The fact carries the resolved sut_ids and method=catalog_hit, and a
// sut.resolved event is published.
func TestSUTResolutionRecordsTouchedAssets(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{
			SUTID: 9001, EnvironmentID: 7001, Name: "订单系统", Intro: "订单管理",
			BaseURL: "https://orders.example.com", Source: "admin", Status: "approved",
			PageIDs: []int64{101, 102}, APIIDs: []int64{201},
		},
	}}
	store := &fakeResolutionStore{}
	pub := &fakeResolutionPublisher{}
	svc := newResolutionService(catalog, store, pub)

	out, err := svc.ResolveForRun(context.Background(), 2001, 5001, "验证订单系统的下单流程")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if out.ResolutionMethod != ResolutionMethodCatalogHit {
		t.Fatalf("method = %q, want catalog_hit", out.ResolutionMethod)
	}
	if out.NeedsExploration {
		t.Fatal("a directory hit with valid assets must not require exploration")
	}
	if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9001 {
		t.Fatalf("sut_ids = %v, want [9001]", out.SUTIDs)
	}
	if len(out.TouchedPageIDs) == 0 || len(out.TouchedAPIIDs) == 0 {
		t.Fatalf("touched assets must be non-empty on a directory hit: pages=%v apis=%v", out.TouchedPageIDs, out.TouchedAPIIDs)
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended %d facts, want 1", len(store.appended))
	}
	fact := store.appended[0]
	if fact.CaseRunID != 5001 {
		t.Fatalf("fact case_run_id = %d, want 5001", fact.CaseRunID)
	}
	if fact.ResolutionMethod != ResolutionMethodCatalogHit {
		t.Fatalf("fact method = %q", fact.ResolutionMethod)
	}
	if len(fact.TouchedPageIDs) == 0 || len(fact.TouchedAPIIDs) == 0 {
		t.Fatalf("fact touched assets empty: %+v", fact)
	}
	if fact.Ambiguous {
		t.Fatal("a single unambiguous match must record Ambiguous=false")
	}
	if len(pub.events) != 1 || pub.events[0].Type != EventSUTResolved {
		t.Fatalf("events = %+v, want one sut.resolved", pub.events)
	}
	if pub.events[0].Channel != "version:2001" {
		t.Fatalf("event channel = %q, want version:2001", pub.events[0].Channel)
	}
	if ambiguous, _ := pub.events[0].Payload["ambiguous"].(bool); ambiguous {
		t.Fatalf("event payload ambiguous = %#v, want false", pub.events[0].Payload["ambiguous"])
	}
	if needs, _ := pub.events[0].Payload["needs_exploration"].(bool); needs {
		t.Fatalf("event payload needs_exploration = %#v, want false on a catalog hit", pub.events[0].Payload["needs_exploration"])
	}
}

// TestAssetGapTriggersExploration pins R-E8: a SUT that IS in the catalog (the
// description matches it) but has NO effective page/API assets — the no-indexed-
// code-repo case where no code-fact proposal ever populated assets — must trigger
// autonomous exploration rather than a hollow catalog_hit. The fact still records
// the resolved target SUT, with method=exploration and empty touched assets.
func TestAssetGapTriggersExploration(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{
			SUTID: 9002, EnvironmentID: 7001, Name: "库存系统", Intro: "无已索引代码仓",
			BaseURL: "https://stock.example.com", Source: "admin", Status: "approved",
			PageIDs: nil, APIIDs: nil,
		},
	}}
	store := &fakeResolutionStore{}
	pub := &fakeResolutionPublisher{}
	svc := newResolutionService(catalog, store, pub)

	out, err := svc.ResolveForRun(context.Background(), 2001, 5002, "验证库存系统的库存扣减")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if !out.NeedsExploration {
		t.Fatal("an asset-level gap (matched SUT, no assets) must trigger exploration (R-E8)")
	}
	if out.ResolutionMethod != ResolutionMethodExploration {
		t.Fatalf("method = %q, want exploration", out.ResolutionMethod)
	}
	if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9002 {
		t.Fatalf("sut_ids = %v, want the matched target [9002]", out.SUTIDs)
	}
	if len(out.TouchedPageIDs) != 0 || len(out.TouchedAPIIDs) != 0 {
		t.Fatalf("asset-gap run has no verified assets yet: %v / %v", out.TouchedPageIDs, out.TouchedAPIIDs)
	}
	if len(store.appended) != 1 || store.appended[0].ResolutionMethod != ResolutionMethodExploration {
		t.Fatalf("fact = %+v, want one exploration fact", store.appended)
	}
	if len(pub.events) != 1 {
		t.Fatalf("events = %d, want 1", len(pub.events))
	}
	if needs, _ := pub.events[0].Payload["needs_exploration"].(bool); !needs {
		t.Fatalf("event payload needs_exploration = %#v, want true on an asset gap", pub.events[0].Payload["needs_exploration"])
	}
	if ambiguous, _ := pub.events[0].Payload["ambiguous"].(bool); ambiguous {
		t.Fatalf("event payload ambiguous = %#v, want false (single matched SUT)", pub.events[0].Payload["ambiguous"])
	}
}

// TestSUTResolutionNoMatchTriggersExploration: a description that matches no
// catalog SUT requires exploration; the fact records an empty resolved set.
func TestSUTResolutionNoMatchTriggersExploration(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://orders.example.com", PageIDs: []int64{101}},
	}}
	store := &fakeResolutionStore{}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	out, err := svc.ResolveForRun(context.Background(), 2001, 5003, "验证一个完全不存在的第三方系统")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if !out.NeedsExploration || out.ResolutionMethod != ResolutionMethodExploration {
		t.Fatalf("no-match must explore: %+v", out)
	}
	if len(out.SUTIDs) != 0 {
		t.Fatalf("no-match resolved set = %v, want empty", out.SUTIDs)
	}
}

// TestSUTResolutionDomainMatchPrimary pins R-B4: base_url/domain is a PRIMARY
// match criterion. A description that names the SUT's domain (but not its name)
// resolves to that SUT.
func TestSUTResolutionDomainMatchPrimary(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://orders.example.com", PageIDs: []int64{101}},
		{SUTID: 9002, Name: "库存系统", BaseURL: "https://stock.example.com", PageIDs: []int64{102}},
	}}
	store := &fakeResolutionStore{}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	out, err := svc.ResolveForRun(context.Background(), 2001, 5004, "打开 https://orders.example.com/checkout 下单")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9001 {
		t.Fatalf("domain match resolved = %v, want [9001]", out.SUTIDs)
	}
	if out.ResolutionMethod != ResolutionMethodCatalogHit {
		t.Fatalf("method = %q, want catalog_hit", out.ResolutionMethod)
	}
}

// TestSUTResolutionParentPathNotSoleCriterion pins R-B4: parent-path same-origin
// is AUXILIARY, never the sole criterion. Two SUTs share an origin (same host,
// different base paths); a description that only mentions a sibling path under
// that origin — matching neither SUT's name nor its exact base_url/domain — must
// NOT resolve on parent-path alone.
func TestSUTResolutionParentPathNotSoleCriterion(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://app.example.com/orders", PageIDs: []int64{101}},
		{SUTID: 9002, Name: "库存系统", BaseURL: "https://app.example.com/stock", PageIDs: []int64{102}},
	}}
	store := &fakeResolutionStore{}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	// Mentions a sibling path under the shared origin, but neither SUT name nor
	// either exact base_url. Parent-path same-origin alone must not pick a SUT.
	out, err := svc.ResolveForRun(context.Background(), 2001, 5005, "访问 https://app.example.com/reports 查看报表")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if !out.NeedsExploration {
		t.Fatalf("parent-path same-origin alone must not resolve: %+v", out)
	}
}

// TestSUTResolutionAmbiguousChoosesByContext pins controller ruling P19: on
// ambiguity the resolution SELECTS the top-ranked SUT (design §6.2 "歧义按上下文
// 选择" means select, not union). The touched/validated asset set is the SELECTED
// SUT's only — the UNSELECTED candidate's assets must NOT enter the knowledge
// true source (Review Focus 2). The Ambiguous flag is recorded in the fact AND
// the sut.resolved event payload so downstream can tell a selection-from-
// ambiguity apart from an unambiguous resolution.
//
// Discriminating power: restoring the union of all hits makes BOTH subtests fail
// (the unselected SUT's id/assets would appear). Deleting the ranking makes the
// asset-preference subtest fail.
func TestSUTResolutionAmbiguousChoosesByContext(t *testing.T) {
	t.Run("asset-bearing candidate preferred; unselected assets excluded", func(t *testing.T) {
		catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
			{SUTID: 9001, Name: "商城", BaseURL: "https://shop.example.com", PageIDs: nil, APIIDs: nil},
			{SUTID: 9002, Name: "商城", BaseURL: "https://mall.example.com", PageIDs: []int64{102}, APIIDs: []int64{202}},
		}}
		store := &fakeResolutionStore{}
		pub := &fakeResolutionPublisher{}
		svc := newResolutionService(catalog, store, pub)

		out, err := svc.ResolveForRun(context.Background(), 2001, 5006, "验证商城的购买流程")
		if err != nil {
			t.Fatalf("ResolveForRun: %v", err)
		}
		if !out.Ambiguous {
			t.Fatal("two matching SUTs must be flagged ambiguous")
		}
		if out.ResolutionMethod != ResolutionMethodCatalogHit {
			t.Fatalf("method = %q, want catalog_hit (the asset-bearing candidate)", out.ResolutionMethod)
		}
		// SELECT the asset-bearing 9002, not the union {9001,9002}.
		if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9002 {
			t.Fatalf("selected sut_ids = %v, want exactly [9002] (selection, not union)", out.SUTIDs)
		}
		if len(out.TouchedPageIDs) != 1 || out.TouchedPageIDs[0] != 102 ||
			len(out.TouchedAPIIDs) != 1 || out.TouchedAPIIDs[0] != 202 {
			t.Fatalf("touched = %v/%v, want the SELECTED SUT's [102]/[202] only", out.TouchedPageIDs, out.TouchedAPIIDs)
		}
		if len(store.appended) != 1 {
			t.Fatalf("appended %d facts, want 1", len(store.appended))
		}
		fact := store.appended[0]
		if !fact.Ambiguous {
			t.Fatal("the Ambiguous flag must be recorded in the sut_resolution fact")
		}
		if len(fact.SUTIDs) != 1 || fact.SUTIDs[0] != 9002 {
			t.Fatalf("fact sut_ids = %v, want the selected [9002]", fact.SUTIDs)
		}
		if len(pub.events) != 1 {
			t.Fatalf("events = %d, want 1", len(pub.events))
		}
		if ambiguous, _ := pub.events[0].Payload["ambiguous"].(bool); !ambiguous {
			t.Fatalf("event payload ambiguous = %#v, want true", pub.events[0].Payload["ambiguous"])
		}
	})

	t.Run("tie on assets broken by min id; unselected asset-bearing SUT excluded", func(t *testing.T) {
		// Both candidates carry assets and score identically: the deterministic
		// tie-break selects the lowest SUT id. The OTHER asset-bearing SUT's
		// assets must still be excluded (the strongest union-mutation probe).
		catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
			{SUTID: 9002, Name: "商城", BaseURL: "https://mall.example.com", PageIDs: []int64{102}, APIIDs: []int64{202}},
			{SUTID: 9001, Name: "商城", BaseURL: "https://shop.example.com", PageIDs: []int64{101}, APIIDs: []int64{201}},
		}}
		store := &fakeResolutionStore{}
		pub := &fakeResolutionPublisher{}
		svc := newResolutionService(catalog, store, pub)

		out, err := svc.ResolveForRun(context.Background(), 2001, 5016, "验证商城的购买流程")
		if err != nil {
			t.Fatalf("ResolveForRun: %v", err)
		}
		if !out.Ambiguous {
			t.Fatal("two matching SUTs must be flagged ambiguous")
		}
		if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9001 {
			t.Fatalf("selected sut_ids = %v, want the min-id [9001] (selection, not union)", out.SUTIDs)
		}
		if len(out.TouchedPageIDs) != 1 || out.TouchedPageIDs[0] != 101 ||
			len(out.TouchedAPIIDs) != 1 || out.TouchedAPIIDs[0] != 201 {
			t.Fatalf("touched = %v/%v, want [101]/[201] only — the unselected 9002's 102/202 must be EXCLUDED",
				out.TouchedPageIDs, out.TouchedAPIIDs)
		}
		if len(store.appended) != 1 || !store.appended[0].Ambiguous {
			t.Fatalf("fact = %+v, want one fact with Ambiguous=true", store.appended)
		}
		if ambiguous, _ := pub.events[0].Payload["ambiguous"].(bool); !ambiguous {
			t.Fatalf("event payload ambiguous = %#v, want true", pub.events[0].Payload["ambiguous"])
		}
	})
}

// TestSUTResolutionIdempotentReResolve: re-resolving a run whose latest fact already
// matches appends nothing and publishes nothing (the production read path that
// keeps a resumed dispatch from duplicating the fact).
func TestSUTResolutionIdempotentReResolve(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://orders.example.com", PageIDs: []int64{101}, APIIDs: []int64{201}},
	}}
	store := &fakeResolutionStore{}
	pub := &fakeResolutionPublisher{}
	svc := newResolutionService(catalog, store, pub)

	if _, err := svc.ResolveForRun(context.Background(), 2001, 5007, "验证订单系统"); err != nil {
		t.Fatalf("first resolve: %v", err)
	}
	// The latest stored fact now equals what a re-resolution would compute.
	store.list = store.appended
	out, err := svc.ResolveForRun(context.Background(), 2001, 5007, "验证订单系统")
	if err != nil {
		t.Fatalf("second resolve: %v", err)
	}
	if out.Recorded {
		t.Fatal("an unchanged re-resolution must not append a duplicate fact")
	}
	if len(store.appended) != 1 {
		t.Fatalf("appended %d facts, want 1 (idempotent)", len(store.appended))
	}
	if len(pub.events) != 1 {
		t.Fatalf("published %d events, want 1 (idempotent)", len(pub.events))
	}
}

// TestSUTResolutionSingleBindingEmptyDescription: an empty description with a
// single effective SUT resolves to that SUT by context (the unambiguous single
// binding), mirroring executionSUTBindings' single-binding fallback.
func TestSUTResolutionSingleBindingEmptyDescription(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://orders.example.com", PageIDs: []int64{101}},
	}}
	store := &fakeResolutionStore{}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	out, err := svc.ResolveForRun(context.Background(), 2001, 5008, "  ")
	if err != nil {
		t.Fatalf("ResolveForRun: %v", err)
	}
	if len(out.SUTIDs) != 1 || out.SUTIDs[0] != 9001 {
		t.Fatalf("single-binding fallback = %v, want [9001]", out.SUTIDs)
	}
	if out.ResolutionMethod != ResolutionMethodCatalogHit {
		t.Fatalf("method = %q, want catalog_hit", out.ResolutionMethod)
	}
}

// TestSUTResolutionCatalogReadErrorPropagates: an infra read failure is returned
// (observable, retryable via the work queue), never a silent empty resolution.
func TestSUTResolutionCatalogReadErrorPropagates(t *testing.T) {
	catalog := &fakeSUTCatalog{err: errors.New("catalog unavailable")}
	store := &fakeResolutionStore{}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	if _, err := svc.ResolveForRun(context.Background(), 2001, 5009, "验证订单系统"); err == nil {
		t.Fatal("catalog read error must propagate, not silently resolve empty")
	}
	if len(store.appended) != 0 {
		t.Fatal("no fact may be appended when the catalog read failed")
	}
}

// TestSUTResolutionFactWriteErrorPropagates: a fact-write failure is returned
// (no silent degradation of the knowledge truth source).
func TestSUTResolutionFactWriteErrorPropagates(t *testing.T) {
	catalog := &fakeSUTCatalog{suts: []ResolvedSUT{
		{SUTID: 9001, Name: "订单系统", BaseURL: "https://orders.example.com", PageIDs: []int64{101}},
	}}
	store := &fakeResolutionStore{appendErr: errors.New("db down")}
	svc := newResolutionService(catalog, store, &fakeResolutionPublisher{})

	if _, err := svc.ResolveForRun(context.Background(), 2001, 5010, "验证订单系统"); err == nil {
		t.Fatal("fact write error must propagate")
	}
}
