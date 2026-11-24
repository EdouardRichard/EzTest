package main

// v0.2.4-F T14 (SC-F10, R-E7): the case_author discovery-fact producer wiring.
// The design read face consumes the execution-side facts (P17 asset_registration
// -> page/api, P18 session fixtures -> login_session) through neutral design
// ports; these adapters are the composition-root join (the only place that knows
// both contexts). The tests pin the effective-scope gate (a proposed/rejected
// SUT or a later-rejected asset never reaches the design read face) and the
// production wiring itself (main.go source pin — no database in unit tests, the
// resource_decision_wiring_test precedent).

import (
	"context"
	"os"
	"strings"
	"testing"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

type discoveryFakeVersions struct {
	view versionapp.VersionView
	err  error
}

func (f *discoveryFakeVersions) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

type discoveryFakeRegistrations struct {
	bySUT map[int64][]execution.AssetRegistration
}

func (f *discoveryFakeRegistrations) AppendAssetRegistration(context.Context, execution.AssetRegistration) (execution.AssetRegistration, error) {
	panic("the discovery producer must not append asset_registration facts")
}

func (f *discoveryFakeRegistrations) ListAssetRegistrations(_ context.Context, sutID int64) ([]execution.AssetRegistration, error) {
	return append([]execution.AssetRegistration(nil), f.bySUT[sutID]...), nil
}

type discoveryFakeFixtures struct {
	rows []execution.SessionFixture
}

func (f *discoveryFakeFixtures) ListSessionFixturesByVersion(_ context.Context, _ int64, _ int, _ *int64) ([]execution.SessionFixture, bool, error) {
	return append([]execution.SessionFixture(nil), f.rows...), false, nil
}

// discoveryCatalogFixture: SUT 7 approved (page 101 + api 201 approved, page 999
// rejected), SUT 8 proposed (approved page 102 must stay out of scope).
func discoveryCatalogFixture() versionapp.VersionView {
	return versionapp.VersionView{
		ID: 1001,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID:     1001,
			EnvironmentID: 3,
			Environment: resourceapp.EnvironmentView{
				ID: "3",
				SUTs: []resourceapp.SUTView{
					{
						ID: "7", EnvironmentID: "3", Name: "订单系统", Status: "approved",
						Pages: []resourceapp.PageView{
							{ID: "101", SUTID: "7", Name: "订单页", URL: "http://sut/orders", RouteTemplate: "/orders", Status: "approved"},
							{ID: "999", SUTID: "7", Name: "已驳回页", URL: "http://sut/rejected", Status: "rejected"},
						},
						APIs: []resourceapp.APIView{
							{ID: "201", SUTID: "7", Method: "POST", PathTemplate: "/orders", AuthRole: "bearer", Status: "approved"},
						},
					},
					{
						ID: "8", EnvironmentID: "3", Name: "待审系统", Status: "proposed",
						Pages: []resourceapp.PageView{
							{ID: "102", SUTID: "8", Name: "待审页", URL: "http://other/app", Status: "approved"},
						},
					},
				},
			},
		},
	}
}

func TestDesignDiscoveryAssetFactsEffectiveScope(t *testing.T) {
	adapter := newDesignDiscoveryAssetFacts(
		&discoveryFakeVersions{view: discoveryCatalogFixture()},
		&discoveryFakeRegistrations{bySUT: map[int64][]execution.AssetRegistration{
			7: {
				{ID: 11, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 101, GateResult: "approved"},
				{ID: 12, SUTID: 7, AssetKind: execution.AssetKindAPI, AssetID: 201, GateResult: "approved"},
				// Proven once, later rejected by an admin: out of the effective
				// catalog, so it must not reach the design read face.
				{ID: 13, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 999, GateResult: "approved"},
				// db assets are not a case_author discovery kind.
				{ID: 14, SUTID: 7, AssetKind: execution.AssetKindDB, AssetID: 501, GateResult: "approved"},
				// Latest fact not approved -> retired.
				{ID: 15, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 101, GateResult: "approved"},
				{ID: 16, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 101, GateResult: "rejected"},
			},
			8: {
				{ID: 17, SUTID: 8, AssetKind: execution.AssetKindPage, AssetID: 102, GateResult: "approved"},
			},
		}},
	)
	facts, err := adapter.ProvenAssetFacts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("ProvenAssetFacts: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts = %+v, want only the api 201 fact (page 101 retired by its latest rejected fact; 999 out of effective scope; db/proposed excluded)", facts)
	}
	got := facts[0]
	if got.FactID != 12 || got.AssetKind != designapp.DiscoveryFactKindAPI || got.AssetID != 201 ||
		got.Method != "POST" || got.PathTemplate != "/orders" || got.AuthRole != "bearer" {
		t.Fatalf("api fact = %+v, want fact 12 with the catalog labels", got)
	}

	// With the retirement removed, the page fact carries its catalog labels.
	adapter.registrations = &discoveryFakeRegistrations{bySUT: map[int64][]execution.AssetRegistration{
		7: {{ID: 11, SUTID: 7, AssetKind: execution.AssetKindPage, AssetID: 101, GateResult: "approved"}},
	}}
	facts, err = adapter.ProvenAssetFacts(context.Background(), 1001)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts = %+v err = %v, want one page fact", facts, err)
	}
	if facts[0].AssetKind != designapp.DiscoveryFactKindPage || facts[0].URL != "http://sut/orders" ||
		facts[0].RouteTemplate != "/orders" || facts[0].Name != "订单页" {
		t.Fatalf("page fact = %+v, want the effective catalog labels", facts[0])
	}
}

func TestDesignDiscoverySessionFactsEffectiveScope(t *testing.T) {
	adapter := newDesignDiscoverySessionFacts(
		&discoveryFakeVersions{view: discoveryCatalogFixture()},
		&discoveryFakeFixtures{rows: []execution.SessionFixture{
			{ID: 22, VersionID: 1001, SUTID: 7, EnvironmentID: 3, Role: "admin", StorageStateRef: "sha-new"},
			{ID: 21, VersionID: 1001, SUTID: 7, EnvironmentID: 3, Role: "admin", StorageStateRef: "sha-old"},
			{ID: 23, VersionID: 1001, SUTID: 8, EnvironmentID: 3, Role: "admin", StorageStateRef: "sha-proposed"},
		}},
	)
	facts, err := adapter.SavedSessionFacts(context.Background(), 1001)
	if err != nil {
		t.Fatalf("SavedSessionFacts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %+v, want the two SUT-7 fixtures only (proposed SUT 8 excluded)", facts)
	}
	for _, fact := range facts {
		if fact.SUTID != 7 || fact.Role != "admin" || fact.EnvironmentID != 3 {
			t.Fatalf("session fact = %+v, want SUT 7 / admin / env 3", fact)
		}
	}
}

// TestDiscoveryFactsProducerWiredInProduction pins the composition root: the
// design graph's discovery reader is non-nil in production (SetDiscoveryFacts
// with the F producer) and the case runner reads the knowledge-dimension facts.
// Source pin because no database is available in unit tests
// (resource_decision_wiring_test precedent).
func TestDiscoveryFactsProducerWiredInProduction(t *testing.T) {
	source, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	content := string(source)
	if !strings.Contains(content, "designGraph.SetDiscoveryFacts(designapp.NewDiscoveryFactsProducer(") {
		t.Fatal("main.go must wire the case_author discovery-fact producer via SetDiscoveryFacts (SC-F10: read face non-nil in production)")
	}
	start := strings.Index(content, "executionapp.NewCaseRunner(")
	if start < 0 {
		t.Fatal("main.go no longer constructs the case runner")
	}
	end := strings.Index(content[start:], "\n\t})")
	if end < 0 {
		t.Fatal("could not locate the NewCaseRunner deps block")
	}
	block := content[start : start+end]
	for _, fragment := range []string{"executionRepo.SUTResolutions", "executionRepo.AssetRegistrations"} {
		if !strings.Contains(block, fragment) {
			t.Fatalf("NewCaseRunner deps must read the knowledge-dimension fact stores (%q missing)", fragment)
		}
	}
}
