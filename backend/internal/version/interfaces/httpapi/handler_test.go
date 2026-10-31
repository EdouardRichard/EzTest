package versionapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type stubService struct {
	view         versionapp.VersionView
	summaries    []versionapp.VersionSummary
	hasMore      bool
	err          error
	lastCommand  versionapp.CreateCommand
	lastPatch    versionapp.Patch
	lastID       int64
	lastActor    int64
	lastLimit    int
	lastBeforeID *int64
	calls        []string
}

func (s *stubService) Create(_ context.Context, cmd versionapp.CreateCommand, actorID int64) (versionapp.VersionView, error) {
	s.calls = append(s.calls, "create")
	s.lastCommand = cmd
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.VersionView{}, s.err
	}
	return s.view, nil
}

func (s *stubService) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	s.calls = append(s.calls, "get")
	s.lastID = id
	if s.err != nil {
		return versionapp.VersionView{}, s.err
	}
	return s.view, nil
}

func (s *stubService) List(_ context.Context, limit int, beforeID *int64) ([]versionapp.VersionSummary, bool, error) {
	s.calls = append(s.calls, "list")
	s.lastLimit = limit
	s.lastBeforeID = beforeID
	if s.err != nil {
		return nil, false, s.err
	}
	return s.summaries, s.hasMore, nil
}

func (s *stubService) Update(_ context.Context, id int64, patch versionapp.Patch, actorID int64) (versionapp.VersionView, error) {
	s.calls = append(s.calls, "update")
	s.lastID = id
	s.lastPatch = patch
	s.lastActor = actorID
	if s.err != nil {
		return versionapp.VersionView{}, s.err
	}
	return s.view, nil
}

var _ versionapi.Service = (*stubService)(nil)

func newTestServer(t *testing.T) (http.Handler, *stubService) {
	t.Helper()
	stub := &stubService{}
	handler := versionapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions", handler.List)
	mux.HandleFunc("POST /api/versions", handler.Create)
	mux.HandleFunc("GET /api/versions/{id}", handler.Get)
	mux.HandleFunc("PATCH /api/versions/{id}", handler.Update)
	return mux, stub
}

func call(t *testing.T, handler http.Handler, method, path, body string, actor *platform.Actor) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return payload
}

func dataBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := decodeBody(t, recorder)
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("response data is not an object: %s", recorder.Body.String())
	}
	return data
}

func dataList(t *testing.T, recorder *httptest.ResponseRecorder) []any {
	t.Helper()
	payload := decodeBody(t, recorder)
	data, ok := payload["data"].([]any)
	if !ok {
		t.Fatalf("response data is not an array: %s", recorder.Body.String())
	}
	return data
}

func errorBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	payload := decodeBody(t, recorder)
	errorData, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("response error is not an object: %s", recorder.Body.String())
	}
	return errorData
}

func actor() *platform.Actor { return &platform.Actor{ID: 42, Role: "user"} }

func sampleView() versionapp.VersionView {
	return versionapp.VersionView{
		ID:                        101,
		Title:                     "Orders Q4",
		Status:                    "initial",
		AutoDesignNewRequirements: true,
		Bindings: []versionapp.BindingView{
			{
				ID: 201,
				SUT: resourceapp.SUTView{
					ID: "1001", Name: "Orders", Intro: "order center",
					CreatedAt: testTime, UpdatedAt: testTime,
				},
				Environment: resourceapp.EnvironmentView{
					ID: "1101", SUTID: "1001", Name: "prod", BaseURL: "https://orders.example.test",
					IsDefault: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Repository: &resourceapp.CodeRepositoryView{
					ID: "1201", Name: "orders-repo", Path: "C:/repos/orders", Branch: "main",
					Include: []string{"src"}, Exclude: []string{"node_modules"}, CreatedBy: 42,
					Status: "active", CreatedAt: testTime, UpdatedAt: testTime,
				},
				Pages: []resourceapp.PageView{
					{ID: "1301", SUTID: "1001", EnvironmentID: "1101", Name: "Order list", URL: "https://orders.example.test/orders", RouteTemplate: "/orders", PageType: "page", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime},
				},
				APIs: []resourceapp.APIView{
					{ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET", PathTemplate: "/api/orders", Name: "List orders", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime},
				},
				Databases: []resourceapp.DatabaseView{
					{ID: "1501", SUTID: "1001", EnvironmentID: "1101", Name: "orders-db", DSN: "postgres://orders", Username: "orders", PasswordSet: true, ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime},
				},
				Accounts: []resourceapp.AccountView{
					{ID: "1601", SUTID: "1001", EnvironmentID: "1101", Username: "order-admin", Roles: []string{"admin"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime},
				},
			},
		},
		CreatedBy: 42,
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}

func TestCreateVersionParsesPayloadAndReturnsNestedView(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.view = sampleView()

	body := `{
		"title": "Orders Q4",
		"bindings": [
			{
				"sut_id": "1001",
				"environment_id": "1101",
				"code_repository_id": "1201",
				"page_ids": ["1301", "1302"],
				"api_ids": ["1401"],
				"database_ids": ["1501"],
				"accounts": [{"account_id": "1601", "role": "order_admin"}, {"account_id": "1602"}]
			},
			{
				"sut_id": "1002",
				"environment_id": "1102"
			}
		]
	}`
	recorder := call(t, handler, http.MethodPost, "/api/versions", body, actor())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastActor != 42 {
		t.Fatalf("actor = %d", stub.lastActor)
	}
	cmd := stub.lastCommand
	if cmd.Title != "Orders Q4" || len(cmd.Bindings) != 2 {
		t.Fatalf("command = %#v", cmd)
	}
	first := cmd.Bindings[0]
	if first.SUTID != 1001 || first.EnvironmentID != 1101 {
		t.Fatalf("first binding = %#v", first)
	}
	if first.CodeRepositoryID == nil || *first.CodeRepositoryID != 1201 {
		t.Fatalf("repository = %#v", first.CodeRepositoryID)
	}
	if len(first.PageIDs) != 2 || first.PageIDs[0] != 1301 || first.PageIDs[1] != 1302 {
		t.Fatalf("page ids = %#v", first.PageIDs)
	}
	if len(first.APIIDs) != 1 || first.APIIDs[0] != 1401 {
		t.Fatalf("api ids = %#v", first.APIIDs)
	}
	if len(first.DatabaseIDs) != 1 || first.DatabaseIDs[0] != 1501 {
		t.Fatalf("database ids = %#v", first.DatabaseIDs)
	}
	if len(first.Accounts) != 2 || first.Accounts[0].AccountID != 1601 || first.Accounts[0].Role != "order_admin" {
		t.Fatalf("accounts = %#v", first.Accounts)
	}
	if first.Accounts[1].Role != "" {
		t.Fatalf("second account role = %q", first.Accounts[1].Role)
	}
	second := cmd.Bindings[1]
	if second.SUTID != 1002 || second.EnvironmentID != 1102 || second.CodeRepositoryID != nil {
		t.Fatalf("second binding = %#v", second)
	}

	data := dataBody(t, recorder)
	if data["id"] != "101" || data["title"] != "Orders Q4" || data["status"] != "initial" {
		t.Fatalf("data = %#v", data)
	}
	if data["auto_design_new_requirements"] != true || data["created_by"] != "42" {
		t.Fatalf("data = %#v", data)
	}
	if data["created_at"] != "2026-10-31T12:00:00Z" {
		t.Fatalf("created_at = %#v", data["created_at"])
	}
	bindings := data["bindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("bindings = %#v", bindings)
	}
	binding := bindings[0].(map[string]any)
	if binding["id"] != "201" {
		t.Fatalf("binding id = %#v", binding["id"])
	}
	if binding["sut"].(map[string]any)["name"] != "Orders" {
		t.Fatalf("sut = %#v", binding["sut"])
	}
	if binding["environment"].(map[string]any)["name"] != "prod" {
		t.Fatalf("environment = %#v", binding["environment"])
	}
	if binding["repository"].(map[string]any)["name"] != "orders-repo" {
		t.Fatalf("repository = %#v", binding["repository"])
	}
	if len(binding["pages"].([]any)) != 1 || len(binding["apis"].([]any)) != 1 {
		t.Fatalf("pages/apis = %#v", binding)
	}
	if len(binding["databases"].([]any)) != 1 || len(binding["accounts"].([]any)) != 1 {
		t.Fatalf("databases/accounts = %#v", binding)
	}
	if strings.Contains(recorder.Body.String(), `"password"`) {
		t.Fatalf("response exposes a password field: %s", recorder.Body.String())
	}
}

func TestCreateVersionAcceptsMissingRepository(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.view = sampleView()
	body := `{"title":"T","bindings":[{"sut_id":"1001","environment_id":"1101","code_repository_id":"","page_ids":[],"api_ids":[],"database_ids":[],"accounts":[]}]}`
	recorder := call(t, handler, http.MethodPost, "/api/versions", body, actor())
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastCommand.Bindings[0].CodeRepositoryID != nil {
		t.Fatalf("empty repository id was not treated as unset")
	}
}

func TestCreateVersionValidationErrors(t *testing.T) {
	handler, stub := newTestServer(t)

	t.Run("malformed body", func(t *testing.T) {
		recorder := call(t, handler, http.MethodPost, "/api/versions", `{"title":`, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d", recorder.Code)
		}
		if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != "body" {
			t.Fatalf("field = %#v", field)
		}
	})
	t.Run("invalid ids", func(t *testing.T) {
		cases := []struct {
			body  string
			field string
		}{
			{`{"title":"T","bindings":[{"sut_id":"abc","environment_id":"1"}]}`, "bindings[0].sut_id"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"0"}]}`, "bindings[0].environment_id"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"1","page_ids":["nope"]}]}`, "bindings[0].page_ids[0]"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"1","api_ids":["nope"]}]}`, "bindings[0].api_ids[0]"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"1","database_ids":["nope"]}]}`, "bindings[0].database_ids[0]"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"1","accounts":[{"account_id":"nope"}]}]}`, "bindings[0].accounts[0].account_id"},
			{`{"title":"T","bindings":[{"sut_id":"1","environment_id":"1","code_repository_id":"nope"}]}`, "bindings[0].code_repository_id"},
		}
		for _, tc := range cases {
			stub.calls = nil
			recorder := call(t, handler, http.MethodPost, "/api/versions", tc.body, actor())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("body %s status = %d", tc.body, recorder.Code)
			}
			if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
				t.Fatalf("body %s field = %#v, want %q", tc.body, field, tc.field)
			}
			if len(stub.calls) != 0 {
				t.Fatalf("service was called for invalid input: %#v", stub.calls)
			}
		}
	})
	t.Run("missing actor", func(t *testing.T) {
		recorder := call(t, handler, http.MethodPost, "/api/versions", `{"title":"T","bindings":[]}`, nil)
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d", recorder.Code)
		}
	})
}

func TestCreateVersionSurfacesServiceValidationDetails(t *testing.T) {
	cases := []struct {
		name  string
		field string
	}{
		{"cross sut page", "bindings[0].page_ids"},
		{"duplicate sut", "bindings[1].sut_id"},
		{"invalid repository", "bindings[0].code_repository_id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler, stub := newTestServer(t)
			stub.err = platform.Invalid(tc.field, "invalid binding")
			recorder := call(t, handler, http.MethodPost, "/api/versions", `{"title":"T","bindings":[]}`, actor())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			errorData := errorBody(t, recorder)
			if errorData["code"] != "invalid_argument" {
				t.Fatalf("error = %#v", errorData)
			}
			details := errorData["details"].(map[string]any)
			if details["field"] != tc.field {
				t.Fatalf("field = %#v, want %q", details["field"], tc.field)
			}
		})
	}
}

func TestGetVersionValidatesPathAndNestsBindings(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.view = sampleView()

	recorder := call(t, handler, http.MethodGet, "/api/versions/101", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastID != 101 {
		t.Fatalf("id = %d", stub.lastID)
	}
	data := dataBody(t, recorder)
	if data["id"] != "101" || data["title"] != "Orders Q4" {
		t.Fatalf("data = %#v", data)
	}
	bindings := data["bindings"].([]any)
	binding := bindings[0].(map[string]any)
	if binding["sut"].(map[string]any)["id"] != "1001" {
		t.Fatalf("sut = %#v", binding["sut"])
	}
	if binding["pages"].([]any)[0].(map[string]any)["route_template"] != "/orders" {
		t.Fatalf("page = %#v", binding["pages"])
	}
	if binding["accounts"].([]any)[0].(map[string]any)["password_set"] != true {
		t.Fatalf("account = %#v", binding["accounts"])
	}

	invalid := call(t, handler, http.MethodGet, "/api/versions/not-an-id", "", actor())
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", invalid.Code)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("service called for invalid id: %#v", stub.calls)
	}

	stub.err = platform.NotFound("version")
	missing := call(t, handler, http.MethodGet, "/api/versions/999", "", actor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
	if errorBody(t, missing)["code"] != "not_found" {
		t.Fatalf("error = %#v", errorBody(t, missing))
	}
}

func TestListVersionsParsesCursorAndReturnsNextCursor(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.summaries = []versionapp.VersionSummary{
		{ID: 2, Title: "Second", Status: "initial", SUTCount: 2, CreatedAt: testTime, UpdatedAt: testTime},
		{ID: 1, Title: "First", Status: "initial", SUTCount: 1, CreatedAt: testTime, UpdatedAt: testTime},
	}
	stub.hasMore = true

	recorder := call(t, handler, http.MethodGet, "/api/versions?limit=1&cursor=5", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastLimit != 1 || stub.lastBeforeID == nil || *stub.lastBeforeID != 5 {
		t.Fatalf("list args limit=%d before=%v", stub.lastLimit, stub.lastBeforeID)
	}
	items := dataList(t, recorder)
	if len(items) != 2 {
		t.Fatalf("items = %#v", items)
	}
	first := items[0].(map[string]any)
	if first["id"] != "2" || first["title"] != "Second" || first["sut_count"] != float64(2) {
		t.Fatalf("first summary = %#v", first)
	}
	payload := decodeBody(t, recorder)
	if payload["next_cursor"] != "1" || payload["has_more"] != true {
		t.Fatalf("envelope = %#v", payload)
	}

	if err := stub.err; err != nil {
		t.Fatalf("unexpected stub error: %v", err)
	}

	emptyHandler, emptyStub := newTestServer(t)
	emptyStub.summaries = []versionapp.VersionSummary{}
	emptyStub.hasMore = false
	emptyRecorder := call(t, emptyHandler, http.MethodGet, "/api/versions", "", actor())
	if emptyRecorder.Code != http.StatusOK {
		t.Fatalf("empty status = %d", emptyRecorder.Code)
	}
	if payload := decodeBody(t, emptyRecorder); payload["next_cursor"] != "" || payload["has_more"] != false {
		t.Fatalf("empty envelope = %#v", payload)
	}
	if emptyStub.lastLimit != 20 {
		t.Fatalf("default limit = %d", emptyStub.lastLimit)
	}

	invalidCursor := call(t, handler, http.MethodGet, "/api/versions?cursor=abc", "", actor())
	if invalidCursor.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", invalidCursor.Code)
	}
	if field := errorBody(t, invalidCursor)["details"].(map[string]any)["field"]; field != "cursor" {
		t.Fatalf("field = %#v", field)
	}
}

func TestUpdateVersionSendsPartialPatch(t *testing.T) {
	handler, stub := newTestServer(t)
	stub.view = sampleView()

	recorder := call(t, handler, http.MethodPatch, "/api/versions/101", `{"title":"Renamed","auto_design_new_requirements":true}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if stub.lastID != 101 || stub.lastActor != 42 {
		t.Fatalf("id/actor = %d/%d", stub.lastID, stub.lastActor)
	}
	if stub.lastPatch.Title == nil || *stub.lastPatch.Title != "Renamed" {
		t.Fatalf("title = %#v", stub.lastPatch.Title)
	}
	if stub.lastPatch.AutoDesignNewRequirements == nil || !*stub.lastPatch.AutoDesignNewRequirements {
		t.Fatalf("auto = %#v", stub.lastPatch.AutoDesignNewRequirements)
	}

	partial := call(t, handler, http.MethodPatch, "/api/versions/101", `{"auto_design_new_requirements":false}`, actor())
	if partial.Code != http.StatusOK {
		t.Fatalf("partial status = %d", partial.Code)
	}
	if stub.lastPatch.Title != nil {
		t.Fatalf("omitted title was forwarded: %#v", stub.lastPatch.Title)
	}
	if stub.lastPatch.AutoDesignNewRequirements == nil || *stub.lastPatch.AutoDesignNewRequirements {
		t.Fatalf("auto = %#v", stub.lastPatch.AutoDesignNewRequirements)
	}

	malformed := call(t, handler, http.MethodPatch, "/api/versions/101", `{`, actor())
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed status = %d", malformed.Code)
	}
	unauthorized := call(t, handler, http.MethodPatch, "/api/versions/101", `{"title":"x"}`, nil)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	stub.err = platform.NotFound("version")
	missing := call(t, handler, http.MethodPatch, "/api/versions/999", `{"title":"x"}`, actor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", missing.Code)
	}
}
