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
		VersionID: 7,
		Stage:     version.StageDesigning,
		Editable:  true,
		SUT: resourceapp.SUTView{
			ID: "1001", Name: "Orders", CreatedAt: testTime, UpdatedAt: testTime,
		},
		Environment: resourceapp.EnvironmentView{
			ID: "1101", SUTID: "1001", Name: "prod", BaseURL: "https://orders.example.test",
			Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
		},
		ScopeExclude: version.NewScopeExclude([]int64{1301}, nil, nil),
		Counts: versionapp.BindingCounts{
			Pages:     versionapp.BindingResourceCounts{Approved: 2, Proposed: 1, Excluded: 1, Included: 1},
			APIs:      versionapp.BindingResourceCounts{Approved: 1, Included: 1},
			Databases: versionapp.BindingResourceCounts{Approved: 2, Included: 2},
		},
		Pages: []resourceapp.PageView{
			{ID: "1302", SUTID: "1001", EnvironmentID: "1101", Name: "Order detail", RouteTemplate: "/orders/:id", Status: "approved"},
		},
		APIs: []resourceapp.APIView{
			{ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET", PathTemplate: "/api/orders", Status: "approved"},
		},
		Databases: []resourceapp.DatabaseView{
			{ID: "1501", SUTID: "1001", EnvironmentID: "1101", Name: "orders-db", Status: "active"},
		},
		Accounts: []resourceapp.AccountView{
			{ID: "1601", Username: "order-admin", Roles: []string{"admin"}, Status: "active"},
		},
		Repositories: []versionapp.BindingRepository{
			{
				Repository: resourceapp.CodeRepositoryView{
					ID: "1201", Name: "orders-backend", Path: "C:/repos/backend", Branch: "main",
					Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Role: "backend", Included: true, Source: "sut",
			},
			{
				Repository: resourceapp.CodeRepositoryView{
					ID: "1203", Name: "orders-infra", Path: "C:/repos/infra", Branch: "infra",
					Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Role: "", Included: false, Source: "override",
			},
		},
		CodeBaselineCommit: sampleCodeBaselineCommit,
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
	if data["sut"].(map[string]any)["name"] != "Orders" {
		t.Fatalf("sut = %#v", data["sut"])
	}
	if data["environment"].(map[string]any)["name"] != "prod" {
		t.Fatalf("environment = %#v", data["environment"])
	}
	scope := data["scope_exclude"].(map[string]any)
	if pages := scope["pages"].([]any); len(pages) != 1 || pages[0] != "1301" {
		t.Fatalf("scope_exclude = %#v", scope)
	}
	counts := data["counts"].(map[string]any)
	pages := counts["pages"].(map[string]any)
	if pages["approved"] != float64(2) || pages["excluded"] != float64(1) || pages["included"] != float64(1) {
		t.Fatalf("page counts = %#v", pages)
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
	if first["role"] != "backend" || first["included"] != true || first["source"] != "sut" {
		t.Fatalf("repository row = %#v", first)
	}
	if first["repository"].(map[string]any)["name"] != "orders-backend" {
		t.Fatalf("repository = %#v", first["repository"])
	}
	if data["code_baseline_commit"] != sampleCodeBaselineCommit {
		t.Fatalf("baseline = %#v", data["code_baseline_commit"])
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
		"environment_id": "1103",
		"repository_ids": ["1202", "1203"],
		"account_ids": ["1602"],
		"scope_exclude": {"pages": ["1301"], "apis": ["1401"], "databases": ["1501"]}
	}`
	recorder := call(t, newBindingServer(svc), http.MethodPut, "/api/versions/7/binding", body, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.updCalls != 1 || svc.lastID != 7 || svc.lastActor != 42 {
		t.Fatalf("service call = %#v", svc)
	}
	patch := svc.lastPatch
	if patch.EnvironmentID == nil || *patch.EnvironmentID != 1103 {
		t.Fatalf("environment = %#v", patch.EnvironmentID)
	}
	if patch.RepositoryIDs == nil || len(*patch.RepositoryIDs) != 2 || (*patch.RepositoryIDs)[1] != 1203 {
		t.Fatalf("repositories = %#v", patch.RepositoryIDs)
	}
	if patch.AccountIDs == nil || len(*patch.AccountIDs) != 1 || (*patch.AccountIDs)[0] != 1602 {
		t.Fatalf("accounts = %#v", patch.AccountIDs)
	}
	if patch.ScopeExclude == nil || len(patch.ScopeExclude.Pages) != 1 ||
		patch.ScopeExclude.Pages[0] != 1301 || patch.ScopeExclude.APIs[0] != 1401 ||
		patch.ScopeExclude.Databases[0] != 1501 {
		t.Fatalf("scope_exclude = %#v", patch.ScopeExclude)
	}
	if recorder.Code == http.StatusNoContent {
		t.Fatal("update must return the refreshed overview")
	}
}

func TestBindingUpdateHandlerPartialPayload(t *testing.T) {
	svc := &fakeBindingService{overview: sampleBindingOverview()}
	// An explicit empty selection is different from an omitted field: it clears
	// the version repositories/accounts.
	body := `{"repository_ids": [], "account_ids": []}`
	recorder := call(t, newBindingServer(svc), http.MethodPut, "/api/versions/7/binding", body, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	patch := svc.lastPatch
	if patch.EnvironmentID != nil || patch.ScopeExclude != nil {
		t.Fatalf("omitted fields were set: %#v", patch)
	}
	if patch.RepositoryIDs == nil || len(*patch.RepositoryIDs) != 0 {
		t.Fatalf("repository_ids = %#v", patch.RepositoryIDs)
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
		{"invalid field", platform.Invalid("scope_exclude.pages", "page 1 does not belong to the bound SUT environment"), http.StatusBadRequest},
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
	if recorder := call(t, handler, http.MethodPut, "/api/versions/7/binding", `{"repository_ids":["0"]}`, actor()); recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad repository status = %d", recorder.Code)
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
