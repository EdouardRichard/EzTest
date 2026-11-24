package resourceapp

// H-B1: the handoff resource read port. The effective-scope decision must come
// from the ONE authority (SUTView.IsApproved / EffectiveAssets and the
// environment code pool), never from a second implementation — the former
// version adapter re-stated the rule as SQL status predicates over resource
// tables. These tests pin the port's projection on the domain's own vocabulary.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// handoffFixture seeds the catalog directly so every status is explicit: an
// approved SUT with one in-scope and one out-of-scope asset of each kind, a
// PROPOSED and a REJECTED SUT that own in-scope-looking assets, and a code pool
// shared by two approved SUTs plus a proposed SUT's own repository.
func handoffFixture(t *testing.T) *harness {
	t.Helper()
	h := newHarness()
	h.repo.suts = []resource.SUT{
		{ID: 2001, EnvironmentID: testEnvironmentID, Name: "Order SUT", BaseURL: "https://orders.example.test", Status: resource.SUTStatusApproved},
		{ID: 2002, EnvironmentID: testEnvironmentID, Name: "Second SUT", BaseURL: "https://second.example.test", Status: resource.SUTStatusApproved},
		{ID: 2003, EnvironmentID: testEnvironmentID, Name: "Proposed SUT", BaseURL: "https://proposed.example.test", Status: resource.SUTStatusProposed},
		{ID: 2004, EnvironmentID: testEnvironmentID, Name: "Rejected SUT", BaseURL: "https://rejected.example.test", Status: resource.SUTStatusRejected},
	}
	h.repo.pages = []resource.Page{
		{ID: 3001, SUTID: 2001, Name: "Refund page", URL: "https://orders.example.test/refund", RouteTemplate: "/refund", Status: resource.PageStatusApproved},
		{ID: 3002, SUTID: 2001, Name: "Proposed page", URL: "https://orders.example.test/proposed", RouteTemplate: "/proposed", Status: resource.PageStatusProposed},
		{ID: 3003, SUTID: 2003, Name: "Proposed SUT page", URL: "https://proposed.example.test/p", RouteTemplate: "/p", Status: resource.PageStatusApproved},
	}
	h.repo.apis = []resource.API{
		{ID: 3101, SUTID: 2001, Name: "Create refund", Method: "POST", PathTemplate: "/api/refunds", Status: resource.APIStatusApproved},
		{ID: 3102, SUTID: 2001, Name: "Proposed API", Method: "GET", PathTemplate: "/api/proposed", Status: resource.APIStatusProposed},
		{ID: 3103, SUTID: 2004, Name: "Rejected SUT API", Method: "GET", PathTemplate: "/api/rejected", Status: resource.APIStatusApproved},
	}
	h.repo.databases = []resource.TargetDatabase{
		{ID: 3201, SUTID: 2001, Name: "orders", Kind: "postgres", DSN: "postgres://u:secret@db/orders", Username: "u", Password: "secret", ReadOnly: true, Status: resource.StatusActive},
		{ID: 3202, SUTID: 2001, Name: "disabled-db", Kind: "postgres", DSN: "postgres://u:off@db/off", Status: resource.StatusDisabled},
		{ID: 3203, SUTID: 2003, Name: "proposed-db", Kind: "postgres", DSN: "postgres://u:p@db/p", Status: resource.StatusActive},
	}
	h.repo.repositories = []resource.CodeRepository{
		{ID: 3301, Name: "shared-repo", Path: "/repos/shared", Branch: "main", Status: resource.StatusActive},
		{ID: 3302, Name: "proposed-repo", Path: "/repos/proposed", Branch: "dev", Status: resource.StatusActive},
		{ID: 3303, Name: "inactive-repo", Path: "/repos/inactive", Branch: "main", Status: resource.StatusDisabled},
	}
	h.repo.sutRepos = []resource.SUTRepository{
		// The shared repository resolves to the LOWEST sut_id's role (2001).
		{ID: 3401, SUTID: 2002, RepoID: 3301, Role: "backend", Enabled: true},
		{ID: 3402, SUTID: 2001, RepoID: 3301, Role: "frontend", Enabled: true},
		// A proposed SUT must not contribute to the pool (it would decide pool[0]).
		{ID: 3403, SUTID: 2003, RepoID: 3302, Role: "backend", Enabled: true},
		// A disabled association contributes nothing.
		{ID: 3404, SUTID: 2001, RepoID: 3303, Role: "backend", Enabled: false},
	}
	h.repo.accounts = []resource.TestAccount{
		{ID: 3501, Username: "qa-refund", Password: "account-password", Roles: []string{"admin"}, Notes: "internal note", Status: resource.StatusActive},
		{ID: 3502, Username: "qa-disabled", Password: "disabled-password", Roles: []string{"user"}, Status: resource.StatusDisabled},
		{ID: 3503, Username: "qa-second", Password: "second-password", Roles: []string{"user"}, Status: resource.StatusActive},
	}
	return h
}

func TestHandoffResourcesGatesApprovedSUTsAndEffectiveAssets(t *testing.T) {
	h := handoffFixture(t)
	bundle, err := h.svc.HandoffResources(context.Background(), testEnvironmentID)
	if err != nil {
		t.Fatalf("HandoffResources: %v", err)
	}
	if bundle.Environment.ID != testEnvironmentID || bundle.Environment.Name != "prod" || bundle.Environment.Status != resource.StatusActive {
		t.Fatalf("environment summary = %+v", bundle.Environment)
	}
	if len(bundle.SUTs) != 2 || bundle.SUTs[0].ID != 2001 || bundle.SUTs[1].ID != 2002 {
		t.Fatalf("suts = %+v, want only the approved SUTs 2001/2002 (proposed 2003 and rejected 2004 excluded whole)", bundle.SUTs)
	}
	sut := bundle.SUTs[0]
	if sut.Name != "Order SUT" || sut.BaseURL != "https://orders.example.test" {
		t.Fatalf("sut identity = %+v", sut)
	}
	if len(sut.Pages) != 1 || sut.Pages[0].ID != 3001 || sut.Pages[0].RouteTemplate != "/refund" {
		t.Fatalf("pages = %+v, want only the approved page 3001", sut.Pages)
	}
	if len(sut.APIs) != 1 || sut.APIs[0].ID != 3101 || sut.APIs[0].Path != "/api/refunds" || sut.APIs[0].Method != "POST" {
		t.Fatalf("apis = %+v, want only the approved API 3101", sut.APIs)
	}
	if len(sut.Databases) != 1 || sut.Databases[0].ID != 3201 {
		t.Fatalf("databases = %+v, want only the active database 3201 (disabled 3202 excluded)", sut.Databases)
	}
	// The engine label and the owning SUT id are both carried: a design db
	// assertion takes sut_id, not the database id.
	if sut.Databases[0].Kind != "postgres" || sut.Databases[0].SUTID != 2001 || !sut.Databases[0].ReadOnly {
		t.Fatalf("database projection = %+v", sut.Databases[0])
	}
	// Nothing from a non-approved SUT may appear anywhere in the bundle.
	for _, s := range bundle.SUTs {
		for _, page := range s.Pages {
			if page.ID == 3003 {
				t.Fatalf("proposed SUT's page leaked: %+v", page)
			}
		}
		for _, api := range s.APIs {
			if api.ID == 3103 {
				t.Fatalf("rejected SUT's API leaked: %+v", api)
			}
		}
		for _, database := range s.Databases {
			if database.ID == 3203 {
				t.Fatalf("proposed SUT's database leaked: %+v", database)
			}
		}
	}
}

// TestHandoffResourcesCodePoolMatchesAuthority pins the pool against the
// authority itself (not a hand-written copy of its rule): the handoff pool is
// exactly ListEnvironmentRepositories' projection, so the role of a shared
// repository can never disagree with the pool the execution/codeintel adapters
// resolve.
func TestHandoffResourcesCodePoolMatchesAuthority(t *testing.T) {
	h := handoffFixture(t)
	ctx := context.Background()
	bundle, err := h.svc.HandoffResources(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("HandoffResources: %v", err)
	}
	authority, err := h.svc.ListEnvironmentRepositories(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("ListEnvironmentRepositories: %v", err)
	}
	if len(bundle.Repositories) != len(authority) {
		t.Fatalf("handoff pool = %+v, authority = %+v", bundle.Repositories, authority)
	}
	for index, association := range authority {
		got := bundle.Repositories[index]
		if got.Name != association.Repository.Name || got.Branch != association.Repository.Branch || got.Role != association.Role {
			t.Fatalf("pool row %d = %+v, authority = %+v", index, got, association)
		}
	}
	if len(bundle.Repositories) != 1 || bundle.Repositories[0].ID != 3301 || bundle.Repositories[0].Role != "frontend" {
		t.Fatalf("pool = %+v, want only the shared repo 3301 with the lowest-sut_id role frontend", bundle.Repositories)
	}
	for _, repo := range bundle.Repositories {
		if repo.ID == 3302 {
			t.Fatalf("proposed SUT's repository leaked into the code pool: %+v", repo)
		}
		if repo.ID == 3303 {
			t.Fatalf("inactive/disabled-association repository leaked into the code pool: %+v", repo)
		}
	}
}

// TestHandoffResourcesUnboundOrMissingEnvironment keeps the former LEFT JOIN
// behaviour: the bound id survives with an empty name/status and an empty scope.
func TestHandoffResourcesUnboundOrMissingEnvironment(t *testing.T) {
	h := handoffFixture(t)
	for _, environmentID := range []int64{0, 999999} {
		bundle, err := h.svc.HandoffResources(context.Background(), environmentID)
		if err != nil {
			t.Fatalf("HandoffResources(%d): %v", environmentID, err)
		}
		if bundle.Environment.ID != environmentID || bundle.Environment.Name != "" || bundle.Environment.Status != "" {
			t.Fatalf("environment summary for %d = %+v", environmentID, bundle.Environment)
		}
		if len(bundle.SUTs) != 0 || len(bundle.Repositories) != 0 || len(bundle.Accounts) != 0 {
			t.Fatalf("scope for %d = %+v, want empty", environmentID, bundle)
		}
		if bundle.SUTs == nil || bundle.Repositories == nil || bundle.Accounts == nil {
			t.Fatalf("empty scope must be non-nil lists: %+v", bundle)
		}
	}
}

// TestHandoffAccountsActiveOnlyInIDOrder pins the account half: only active
// accounts, in id order, de-duplicated, missing ids skipped, and never a
// password or note.
func TestHandoffAccountsActiveOnlyInIDOrder(t *testing.T) {
	h := handoffFixture(t)
	accounts, err := h.svc.HandoffAccounts(context.Background(), []int64{3503, 3501, 3502, 3501, 424242, 0})
	if err != nil {
		t.Fatalf("HandoffAccounts: %v", err)
	}
	if len(accounts) != 2 || accounts[0].ID != 3501 || accounts[1].ID != 3503 {
		t.Fatalf("accounts = %+v, want the active 3501/3503 in id order (disabled 3502 and missing 424242 skipped)", accounts)
	}
	if accounts[0].Username != "qa-refund" || len(accounts[0].Roles) != 1 || accounts[0].Roles[0] != "admin" {
		t.Fatalf("account projection = %+v", accounts[0])
	}
	empty, err := h.svc.HandoffAccounts(context.Background(), nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("HandoffAccounts(nil) = %+v err=%v, want a non-nil empty list", empty, err)
	}
}

// TestHandoffResourceReaderCarriesNoCredentials pins the functional-summary
// boundary: the serialized bundle carries no password, DSN or account note. The
// read models have no such field today; adding one (and populating it from the
// catalog rows this fixture seeds with real secrets) makes this test fail.
func TestHandoffResourceReaderCarriesNoCredentials(t *testing.T) {
	h := handoffFixture(t)
	ctx := context.Background()
	bundle, err := h.svc.HandoffResources(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("HandoffResources: %v", err)
	}
	accounts, err := h.svc.HandoffAccounts(ctx, []int64{3501})
	if err != nil {
		t.Fatalf("HandoffAccounts: %v", err)
	}
	bundle.Accounts = accounts
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatalf("marshal bundle: %v", err)
	}
	for _, secret := range []string{"secret", "account-password", "postgres://", "internal note"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("handoff resource bundle carried %q: %s", secret, raw)
		}
	}
}
