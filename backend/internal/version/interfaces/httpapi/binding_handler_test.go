package versionapi_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type fakeBindingService struct {
	overview  versionapp.BindingOverview
	err       error
	lastID    int64
	lastPatch versionapp.BindingPatch
	lastActor int64
	getCalls  int
	updCalls  int
}

func (f *fakeBindingService) GetBinding(_ context.Context, versionID int64) (versionapp.BindingOverview, error) {
	f.getCalls++
	f.lastID = versionID
	if f.err != nil {
		return versionapp.BindingOverview{}, f.err
	}
	return f.overview, nil
}

func (f *fakeBindingService) UpdateBinding(
	_ context.Context, versionID int64, patch versionapp.BindingPatch, actorID int64,
) (versionapp.BindingOverview, error) {
	f.updCalls++
	f.lastID = versionID
	f.lastPatch = patch
	f.lastActor = actorID
	if f.err != nil {
		return versionapp.BindingOverview{}, f.err
	}
	return f.overview, nil
}

var _ versionapi.BindingService = (*fakeBindingService)(nil)

func sampleBindingOverview() versionapp.BindingOverview {
	return versionapp.BindingOverview{
		EnvironmentSUTCatalog: versionapp.EnvironmentSUTCatalog{
			VersionID:     7,
			EnvironmentID: 1101,
			Environment: resourceapp.EnvironmentView{
				ID: "1101", Name: "prod", Description: "production",
				Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				SUTs: []resourceapp.SUTView{
					{
						ID: "1001", EnvironmentID: "1101", Name: "Orders",
						BaseURL: "https://orders.example.test", CreatedAt: testTime, UpdatedAt: testTime,
					},
					{
						ID: "1002", EnvironmentID: "1101", Name: "Billing",
						BaseURL: "https://billing.example.test", CreatedAt: testTime, UpdatedAt: testTime,
					},
				},
			},
			Pages: []resourceapp.PageView{
				{ID: "1302", SUTID: "1001", Name: "Order detail", RouteTemplate: "/orders/:id", Status: "approved"},
			},
			APIs: []resourceapp.APIView{
				{ID: "1401", SUTID: "1001", Method: "GET", PathTemplate: "/api/orders", Status: "approved"},
			},
			Databases: []resourceapp.DatabaseView{
				{ID: "1501", SUTID: "1001", Name: "orders-db", Status: "active"},
			},
			// The version-bound account subset, never the global pool.
			Accounts: []resourceapp.AccountView{
				{ID: "1601", Username: "order-admin", Roles: []string{"admin"}, Status: "active"},
			},
			Repositories: []versionapp.BindingRepository{
				{
					Repository: resourceapp.CodeRepositoryView{
						ID: "1201", Name: "orders-backend", Path: "C:/repos/backend", Branch: "main",
						Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
					},
					SUTID: 1001, Role: "backend", CodeBaselineCommit: sampleCodeBaselineCommit,
				},
				{
					Repository: resourceapp.CodeRepositoryView{
						ID: "1203", Name: "billing-infra", Path: "C:/repos/infra", Branch: "infra",
						Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
					},
					SUTID: 1002, Role: "",
				},
			},
			CodeBaselineCommit: sampleCodeBaselineCommit,
		},
		Stage:    version.StageDesigning,
		Editable: true,
		Counts: versionapp.BindingCounts{
			Pages:     versionapp.BindingResourceCounts{Approved: 2, Proposed: 1},
			APIs:      versionapp.BindingResourceCounts{Approved: 1},
			Databases: versionapp.BindingResourceCounts{Approved: 2},
		},
		Snapshot: versionapp.BindingSnapshot{ScopeVersion: "scope-v2:abc"},
	}
}

func newBindingServer(svc versionapi.BindingService) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/binding", versionapi.NewBindingHandler(svc).Get)
	mux.HandleFunc("PUT /api/versions/{id}/binding", versionapi.NewBindingHandler(svc).Update)
	return mux
}

func TestBindingGetHandler(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	recorder := call(t, newBindingServer(svc), http.MethodGet, "/api/versions/7/binding", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.lastID != 7 || svc.getCalls != 1 {
		t.Fatalf("service call = %d/%d", svc.lastID, svc.getCalls)
	}
	data := dataBody(t, recorder)
	if data["version_id"] != "7" || data["stage"] != "designing" || data["editable"] != true {
		t.Fatalf("data = %#v", data)
	}
	if data["environment_id"] != "1101" {
		t.Fatalf("environment_id = %#v", data["environment_id"])
	}
	// scope_exclude is gone with the version_sut chain.
	if _, present := data["scope_exclude"]; present {
		t.Fatalf("scope_exclude must no longer be part of the contract: %#v", data)
	}
	if _, present := data["sut"]; present {
		t.Fatalf("the derived single SUT must no longer be part of the contract: %#v", data)
	}
	environmentData := data["environment"].(map[string]any)
	if environmentData["name"] != "prod" {
		t.Fatalf("environment = %#v", environmentData)
	}
	suts := environmentData["suts"].([]any)
	if len(suts) != 2 || suts[0].(map[string]any)["name"] != "Orders" {
		t.Fatalf("environment suts = %#v", suts)
	}
	if suts[0].(map[string]any)["base_url"] != "https://orders.example.test" {
		t.Fatalf("base_url must live on the SUT: %#v", suts[0])
	}
	counts := data["counts"].(map[string]any)
	pages := counts["pages"].(map[string]any)
	if pages["approved"] != float64(2) || pages["proposed"] != float64(1) {
		t.Fatalf("page counts = %#v", pages)
	}
	if _, present := pages["excluded"]; present {
		t.Fatalf("excluded count must be gone with scope_exclude: %#v", pages)
	}
	if len(data["pages"].([]any)) != 1 || len(data["apis"].([]any)) != 1 ||
		len(data["databases"].([]any)) != 1 || len(data["accounts"].([]any)) != 1 {
		t.Fatalf("resource lists = %#v", data)
	}
	repositories := data["repositories"].([]any)
	if len(repositories) != 2 {
		t.Fatalf("repositories = %#v", repositories)
	}
	first := repositories[0].(map[string]any)
	if first["role"] != "backend" || first["sut_id"] != "1001" {
		t.Fatalf("repository row = %#v", first)
	}
	if first["code_baseline_commit"] != sampleCodeBaselineCommit {
		t.Fatalf("per-repository baseline = %#v", first["code_baseline_commit"])
	}
	if first["repository"].(map[string]any)["name"] != "orders-backend" {
		t.Fatalf("repository = %#v", first["repository"])
	}
	// The second pool row has no resolved baseline yet, so the field is null.
	if second := repositories[1].(map[string]any); second["code_baseline_commit"] != nil {
		t.Fatalf("unresolved baseline = %#v", second["code_baseline_commit"])
	}
	if data["code_baseline_commit"] != sampleCodeBaselineCommit {
		t.Fatalf("baseline = %#v", data["code_baseline_commit"])
	}
	// R-C4-1 snapshot: the current scope reference is exposed; the frozen wave
	// id stays null while the execution wave snapshot is execution-owned.
	snapshot, ok := data["snapshot"].(map[string]any)
	if !ok || snapshot["scope_version"] != "scope-v2:abc" {
		t.Fatalf("snapshot = %#v", data["snapshot"])
	}
	if frozen, present := snapshot["frozen_wave_id"]; !present || frozen != nil {
		t.Fatalf("frozen_wave_id = %#v, want null", frozen)
	}
	// accounts is the version-bound subset, not the global pool.
	if accounts := data["accounts"].([]any); len(accounts) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}
	if recorder.Body.String() == "" || containsPassword(recorder.Body.String()) {
		t.Fatalf("response leaks credentials: %s", recorder.Body.String())
	}
}

func containsPassword(body string) bool {
	return strings.Contains(body, `"password":`)
}

func TestBindingUpdateHandlerParsesPatch(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	body := `{
		"environment_id": "1102",
		"account_ids": ["1602"]
	}`
	recorder := call(t, newBindingServer(svc), http.MethodPut, "/api/versions/7/binding", body, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.updCalls != 1 || svc.lastID != 7 || svc.lastActor != 42 {
		t.Fatalf("service call = %#v", svc)
	}
	patch := svc.lastPatch
	if patch.EnvironmentID == nil || *patch.EnvironmentID != 1102 {
		t.Fatalf("environment = %#v", patch.EnvironmentID)
	}
	if patch.AccountIDs == nil || len(*patch.AccountIDs) != 1 || (*patch.AccountIDs)[0] != 1602 {
		t.Fatalf("accounts = %#v", patch.AccountIDs)
	}
	if recorder.Code == http.StatusNoContent {
		t.Fatal("update must return the refreshed overview")
	}
}

func TestBindingUpdateHandlerPartialPayload(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	// An explicit empty selection is different from an omitted field: it clears
	// the version account subset.
	body := `{"account_ids": []}`
	recorder := call(t, newBindingServer(svc), http.MethodPut, "/api/versions/7/binding", body, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	patch := svc.lastPatch
	if patch.EnvironmentID != nil {
		t.Fatalf("an omitted field was set: %#v", patch)
	}
	if patch.AccountIDs == nil || len(*patch.AccountIDs) != 0 {
		t.Fatalf("account_ids = %#v", patch.AccountIDs)
	}
}

func TestBindingHandlerTypedErrors(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
	}{
		{"invalid field", platform.Invalid("account_ids", "account 1603 is not active"), http.StatusBadRequest},
		{"not found", platform.NotFound("version"), http.StatusNotFound},
		{"read only", platform.StateConflict("version binding is read-only at stage executing"), http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeBindingService{err: tc.err}
			recorder := call(t, newBindingServer(svc), http.MethodPut, "/api/versions/7/binding", `{}`, actor())
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d (body %s)", recorder.Code, tc.status, recorder.Body.String())
			}
			errorData := errorBody(t, recorder)
			if errorData["code"] == "" || errorData["message"] == "" {
				t.Fatalf("error = %#v", errorData)
			}
		})
	}
}

func TestBindingHandlerRequiresActorAndValidPath(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	handler := newBindingServer(svc)
	if recorder := call(t, handler, http.MethodPut, "/api/versions/7/binding", `{}`, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", recorder.Code)
	}
	if recorder := call(t, handler, http.MethodGet, "/api/versions/not-an-id/binding", "", actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad path status = %d", recorder.Code)
	}
	if recorder := call(t, handler, http.MethodPut, "/api/versions/7/binding", `{`, actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d", recorder.Code)
	}
	if recorder := call(t, handler, http.MethodPut, "/api/versions/7/binding", `{"environment_id":"x"}`, actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad environment status = %d", recorder.Code)
	}
	if recorder := call(t, handler, http.MethodPut, "/api/versions/7/binding", `{"account_ids":["0"]}`, actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad account status = %d", recorder.Code)
	}
}

func TestRegisterBindingRoutes(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	mux := http.NewServeMux()
	auth := func(next http.Handler) http.Handler { return next }
	versionapi.RegisterBindingRoutes(mux, auth, versionapi.NewBindingHandler(svc))
	recorder := call(t, mux, http.MethodGet, "/api/versions/7/binding", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("registered route status = %d", recorder.Code)
	}
}

// A nil authentication middleware is a wiring bug: silently leaving the route
// unmounted (or mounting it without auth) would hide a security boundary bug,
// so the helper fails fast instead.
func TestRegisterBindingRoutesRejectsNilAuth(t *testing.T) {
	panics(t, "nil auth", func() {
		versionapi.RegisterBindingRoutes(http.NewServeMux(), nil, versionapi.NewBindingHandler(nil))
	})
	panics(t, "nil mux", func() {
		versionapi.RegisterBindingRoutes(nil, func(next http.Handler) http.Handler { return next }, versionapi.NewBindingHandler(nil))
	})
}
