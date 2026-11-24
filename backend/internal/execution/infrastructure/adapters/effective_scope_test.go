package adapters

import (
	"context"
	"strconv"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// v0.2.4-B review round 1, Important 1: the execution adapters must read the
// EFFECTIVE (approved pages / approved APIs / active databases) projection of the
// bound environment's SUTs, not the raw nested catalog lists. Before the fix they
// read the raw lists, so a page a discovery run merely PROPOSED moved the
// execution fingerprint (R-EX-4/DB-19 wave scope) and a DISABLED target database
// still handed out its credentials.

// scopeFixtureVersion is one version bound to an environment whose single SUT
// carries every asset status the catalog can hold. The flat binding lists are
// populated the way the version service populates them (effective only); the
// nested SUT lists are raw catalog state and deliberately keep the
// proposed/disabled rows.
func scopeFixtureVersion() versionapp.VersionView {
	sut := resourceapp.SUTView{
		ID: "3", EnvironmentID: "4", Name: "orders", BaseURL: "http://sut.test",
		Pages: []resourceapp.PageView{
			{ID: "71", SUTID: "3", Name: "登录页", URL: "http://sut.test/login", RouteTemplate: "/login", Status: "approved"},
			{ID: "74", SUTID: "3", Name: "候选页", URL: "http://sut.test/proposed", RouteTemplate: "/proposed", Status: "proposed"},
		},
		APIs: []resourceapp.APIView{
			{ID: "72", SUTID: "3", Method: "GET", PathTemplate: "/api/orders", Status: "approved"},
			{ID: "75", SUTID: "3", Method: "GET", PathTemplate: "/api/proposed", Status: "proposed"},
		},
		Databases: []resourceapp.DatabaseView{
			{ID: "81", SUTID: "3", Name: "order_db", Status: "active", ReadOnly: true},
			{ID: "83", SUTID: "3", Name: "legacy_db", Status: "disabled", ReadOnly: false},
		},
	}
	environment := resourceapp.EnvironmentView{ID: "4", Name: "test", Status: "active", SUTs: []resourceapp.SUTView{sut}}
	return versionapp.VersionView{
		ID: 10, Status: "running", EnvironmentID: 4,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 10, EnvironmentID: 4, Environment: environment,
			Pages:     []resourceapp.PageView{sut.Pages[0]},
			APIs:      []resourceapp.APIView{sut.APIs[0]},
			Databases: []resourceapp.DatabaseView{sut.Databases[0]},
		},
	}
}

// TestScopeFixtureKeepsRawCatalogRows proves the fixture below is non-vacuous:
// the nested catalog lists really do carry a proposed page, a proposed API and a
// disabled database, so an adapter that reads them leaks all three.
func TestScopeFixtureKeepsRawCatalogRows(t *testing.T) {
	sut := scopeFixtureVersion().Binding.Environment.SUTs[0]
	if len(sut.Pages) != 2 || len(sut.APIs) != 2 || len(sut.Databases) != 2 {
		t.Fatalf("raw catalog lists = %d pages / %d apis / %d databases, want 2/2/2",
			len(sut.Pages), len(sut.APIs), len(sut.Databases))
	}
}

func TestFingerprintBindingExcludesUnapprovedAssets(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: scopeFixtureVersion()})

	binding, err := adapter.Get(context.Background(), 10)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(binding.SUTs) != 1 {
		t.Fatalf("fingerprint SUTs = %#v, want exactly one", binding.SUTs)
	}
	got := binding.SUTs[0]
	if len(got.PageIDs) != 1 || got.PageIDs[0] != 71 {
		t.Fatalf("fingerprint page ids = %v, want only the approved page 71 (a proposed page must not move the wave scope)", got.PageIDs)
	}
	if len(got.APIIDs) != 1 || got.APIIDs[0] != 72 {
		t.Fatalf("fingerprint api ids = %v, want only the approved API 72", got.APIIDs)
	}
	if len(got.DatabaseIDs) != 1 || got.DatabaseIDs[0] != 81 {
		t.Fatalf("fingerprint database ids = %v, want only the active database 81", got.DatabaseIDs)
	}
}

func TestResolveBindingExcludesUnapprovedAssets(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: scopeFixtureVersion()})

	bindings, err := adapter.Resolve(context.Background(), 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("task-context bindings = %#v, want exactly one", bindings)
	}
	if len(bindings[0].PageURLs) != 1 || bindings[0].PageURLs[0] != "http://sut.test/login" {
		t.Fatalf("discovery page urls = %#v, want only the approved page URL", bindings[0].PageURLs)
	}
	if len(bindings[0].APIEndpoints) != 1 || bindings[0].APIEndpoints[0] != "GET /api/orders" {
		t.Fatalf("discovery api endpoints = %#v, want only the approved endpoint", bindings[0].APIEndpoints)
	}
}

func TestAPIResolverRefusesAnUnapprovedAPI(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: scopeFixtureVersion()})

	if _, err := adapter.API(context.Background(), 10, 3, 75); err == nil {
		t.Fatal("API resolved a PROPOSED api_id; only approved APIs are in the version's scope")
	} else {
		apiErr := apiErrorOf(t, err)
		if apiErr.Status != 404 {
			t.Fatalf("status = %d, want 404", apiErr.Status)
		}
	}
	resolved, err := adapter.API(context.Background(), 10, 3, 72)
	if err != nil {
		t.Fatalf("API on the approved id: %v", err)
	}
	if resolved.Method != "GET" || resolved.Path != "/api/orders" {
		t.Fatalf("resolved api = %#v", resolved)
	}
}

// scopeFakeDatabaseSecrets records every database id it was asked for, so a test
// can prove a disabled database's secret was never even read.
type scopeFakeDatabaseSecrets struct {
	ids []int64
}

func (f *scopeFakeDatabaseSecrets) GetDatabaseSecret(_ context.Context, databaseID int64) (string, string, string, error) {
	f.ids = append(f.ids, databaseID)
	return "postgres://db/" + strconv.FormatInt(databaseID, 10), "reader", "secret", nil
}

func TestDatabaseCredentialNeverLeaksADisabledDatabase(t *testing.T) {
	secrets := &scopeFakeDatabaseSecrets{}
	adapter := NewCredentialAdapter(
		credentialFakeVersions{view: scopeFixtureVersion()},
		&credentialFakeSecrets{}, secrets,
	)
	ctx := context.Background()

	t.Run("an explicit disabled database_id is refused", func(t *testing.T) {
		secrets.ids = nil
		disabled := int64(83)
		_, err := adapter.Database(ctx, 10, 3, &disabled)
		if err == nil {
			t.Fatal("Database handed out the credential of a DISABLED target database")
		}
		if apiErr := apiErrorOf(t, err); apiErr.Status != 404 {
			t.Fatalf("status = %d, want 404", apiErr.Status)
		}
		if len(secrets.ids) != 0 {
			t.Fatalf("the disabled database secret was read: %v", secrets.ids)
		}
	})

	t.Run("the default selection picks the active database", func(t *testing.T) {
		secrets.ids = nil
		credential, err := adapter.Database(ctx, 10, 3, nil)
		if err != nil {
			t.Fatalf("Database: %v", err)
		}
		if credential.DatabaseID != 81 {
			t.Fatalf("database id = %d, want the active 81", credential.DatabaseID)
		}
		if !credential.ReadOnly {
			t.Fatalf("read_only = %v, want the active database's true", credential.ReadOnly)
		}
		if len(secrets.ids) != 1 || secrets.ids[0] != 81 {
			t.Fatalf("secret reads = %v, want exactly [81]", secrets.ids)
		}
	})

	t.Run("a SUT with only disabled databases has no credential", func(t *testing.T) {
		secrets.ids = nil
		view := scopeFixtureVersion()
		view.Binding.Environment.SUTs[0].Databases = []resourceapp.DatabaseView{
			{ID: "83", SUTID: "3", Name: "legacy_db", Status: "disabled"},
		}
		view.Binding.Databases = nil
		only := NewCredentialAdapter(credentialFakeVersions{view: view}, &credentialFakeSecrets{}, secrets)
		if _, err := only.Database(ctx, 10, 3, nil); err == nil {
			t.Fatal("Database fell back to a DISABLED database when the SUT has no active one")
		}
		if len(secrets.ids) != 0 {
			t.Fatalf("the disabled database secret was read: %v", secrets.ids)
		}
	})
}

func apiErrorOf(t *testing.T, err error) *platform.Error {
	t.Helper()
	apiErr, ok := err.(*platform.Error)
	if !ok {
		t.Fatalf("error %v (%T) is not a platform error", err, err)
	}
	return apiErr
}
