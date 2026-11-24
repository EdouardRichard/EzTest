package versionapp_test

import (
	"testing"

	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// v0.2.4-B R-B4 trust boundary, version side: EffectiveSUTs is the per-SUT
// projection every execution/agent/cmd adapter reads, so it must skip a proposed
// or rejected SUT whole — not merely empty its assets. The gate is the single
// resourceapp.SUTView.IsApproved predicate (no re-implemented status check here).
// The raw catalog (Environment.SUTs) must still carry the proposed/rejected SUTs
// so an admin can approve them.

func effectiveSUTsFixture() versionapp.EnvironmentSUTCatalog {
	mk := func(id, status string) resourceapp.SUTView {
		return resourceapp.SUTView{
			ID: id, EnvironmentID: "900", Name: "sut-" + id, BaseURL: "http://" + id + ".test",
			Status: status,
			Pages: []resourceapp.PageView{
				{ID: id + "1", SUTID: id, Name: "p", RouteTemplate: "/p", Status: resource.PageStatusApproved},
			},
			APIs: []resourceapp.APIView{
				{ID: id + "2", SUTID: id, Method: "GET", PathTemplate: "/api", Status: resource.APIStatusApproved},
			},
			Databases: []resourceapp.DatabaseView{
				{ID: id + "3", SUTID: id, Name: "db", Status: resource.StatusActive},
			},
		}
	}
	environment := resourceapp.EnvironmentView{
		ID: "900", Name: "prod", Status: resource.StatusActive,
		SUTs: []resourceapp.SUTView{
			mk("801", resource.SUTStatusApproved),
			mk("802", resource.SUTStatusProposed),
			mk("803", resource.SUTStatusRejected),
		},
	}
	return versionapp.EnvironmentSUTCatalog{
		VersionID: 50, EnvironmentID: 900, Environment: environment,
	}
}

func TestEffectiveSUTsSkipsProposedAndRejectedSUTs(t *testing.T) {
	catalog := effectiveSUTsFixture()

	effective := catalog.EffectiveSUTs()

	if len(effective) != 1 {
		t.Fatalf("EffectiveSUTs = %d rows, want only the approved SUT (proposed/rejected must not appear)", len(effective))
	}
	if effective[0].SUTID != 801 {
		t.Fatalf("EffectiveSUTs[0].SUTID = %d, want the approved 801", effective[0].SUTID)
	}
	if len(effective[0].Pages) != 1 || len(effective[0].APIs) != 1 || len(effective[0].Databases) != 1 {
		t.Fatalf("approved SUT lost its assets: %#v", effective[0])
	}

	// The raw catalog still carries all three SUTs so an admin can approve the
	// proposed one.
	if len(catalog.Environment.SUTs) != 3 {
		t.Fatalf("raw catalog SUTs = %d, want 3", len(catalog.Environment.SUTs))
	}
}

func TestEffectiveSUTsLegacyZeroStatusStaysInScope(t *testing.T) {
	catalog := effectiveSUTsFixture()
	// A SUT that predates the status column carries the zero value, which
	// resource_0005 backfills as approved; it must remain in scope.
	catalog.Environment.SUTs[1].Status = ""

	effective := catalog.EffectiveSUTs()

	if len(effective) != 2 {
		t.Fatalf("EffectiveSUTs = %d rows, want the approved 801 and the legacy zero-status 802", len(effective))
	}
}
