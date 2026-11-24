package resourceapp

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// v0.2.4-B R-B4 trust boundary: a SUT that is merely PROPOSED (e.g. one an Agent
// registered and an admin has not approved) or REJECTED must not enter the
// effective version scope, even if it carries approved pages/APIs/active
// databases. The gate lives in the single projection (SUTView.EffectiveAssets /
// EnvironmentView.EffectiveScope) and is expressed with the SUT domain's own
// status constants. The RAW catalog lists must still show the proposed/rejected
// SUT so an admin can approve it.

// sutStatusFixture builds one SUT with the given registration status carrying an
// approved page, an approved API and an active database — assets that WOULD be in
// scope if the SUT itself were approved.
func sutStatusFixture(id, status string) SUTView {
	return SUTView{
		ID: id, EnvironmentID: "1101", Name: "sut-" + id, BaseURL: "http://" + id + ".test",
		Status: status,
		Pages: []PageView{
			{ID: id + "01", SUTID: id, Name: "p", RouteTemplate: "/p", Status: resource.PageStatusApproved},
		},
		APIs: []APIView{
			{ID: id + "02", SUTID: id, Method: "GET", PathTemplate: "/api", Status: resource.APIStatusApproved},
		},
		Databases: []DatabaseView{
			{ID: id + "03", SUTID: id, Name: "db", Status: resource.StatusActive},
		},
	}
}

func TestEffectiveAssetsExcludesProposedAndRejectedSUTs(t *testing.T) {
	for _, status := range []string{resource.SUTStatusProposed, resource.SUTStatusRejected} {
		t.Run(status, func(t *testing.T) {
			scope := sutStatusFixture("2001", status).EffectiveAssets()
			if len(scope.Pages) != 0 || len(scope.APIs) != 0 || len(scope.Databases) != 0 {
				t.Fatalf("a %s SUT leaked into the effective scope: %#v", status, scope)
			}
			if scope.Pages == nil || scope.APIs == nil || scope.Databases == nil {
				t.Fatalf("effective scope of a %s SUT must be non-nil empty lists, got %#v", status, scope)
			}
		})
	}
}

func TestEffectiveAssetsIncludesAnApprovedSUT(t *testing.T) {
	scope := sutStatusFixture("2002", resource.SUTStatusApproved).EffectiveAssets()
	if len(scope.Pages) != 1 || len(scope.APIs) != 1 || len(scope.Databases) != 1 {
		t.Fatalf("an approved SUT must keep its approved assets: %#v", scope)
	}
}

// TestEffectiveAssetsLegacyZeroStatusIsApproved pins the compatibility default:
// rows and fixtures that predate the status column carry the zero value, which
// resource_0005 backfills as 'approved'. They must stay in scope.
func TestEffectiveAssetsLegacyZeroStatusIsApproved(t *testing.T) {
	sut := sutStatusFixture("2003", "")
	scope := sut.EffectiveAssets()
	if len(scope.Pages) != 1 || len(scope.APIs) != 1 || len(scope.Databases) != 1 {
		t.Fatalf("a legacy (zero-status) SUT must remain in scope: %#v", scope)
	}
}

func TestEffectiveScopeExcludesProposedAndRejectedSUTsButKeepsRawCatalog(t *testing.T) {
	approved := sutStatusFixture("2010", resource.SUTStatusApproved)
	proposed := sutStatusFixture("2011", resource.SUTStatusProposed)
	rejected := sutStatusFixture("2012", resource.SUTStatusRejected)
	environment := EnvironmentView{
		ID: "1101", Name: "prod", Status: resource.StatusActive,
		SUTs: []SUTView{approved, proposed, rejected},
	}

	scope := environment.EffectiveScope()

	// Only the approved SUT's assets are in the effective scope.
	if len(scope.Pages) != 1 || scope.Pages[0].ID != "201001" {
		t.Fatalf("effective pages = %#v, want only the approved SUT's page 201001", scope.Pages)
	}
	if len(scope.APIs) != 1 || scope.APIs[0].ID != "201002" {
		t.Fatalf("effective apis = %#v, want only 201002", scope.APIs)
	}
	if len(scope.Databases) != 1 || scope.Databases[0].ID != "201003" {
		t.Fatalf("effective databases = %#v, want only 201003", scope.Databases)
	}

	// The RAW catalog still carries all three SUTs with their assets, so an admin
	// can see and approve the proposed one.
	if len(environment.SUTs) != 3 {
		t.Fatalf("raw catalog SUTs = %d, want 3 (approved+proposed+rejected)", len(environment.SUTs))
	}
	for _, sut := range environment.SUTs {
		if len(sut.Pages) != 1 || len(sut.APIs) != 1 || len(sut.Databases) != 1 {
			t.Fatalf("raw catalog SUT %s lost its assets: %#v", sut.ID, sut)
		}
	}
}
