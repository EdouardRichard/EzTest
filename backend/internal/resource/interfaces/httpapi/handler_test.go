package resourceapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fakeService struct {
	err error

	sut        resourceapp.SUTView
	env        resourceapp.EnvironmentView
	page       resourceapp.PageView
	api        resourceapp.APIView
	database   resourceapp.DatabaseView
	account    resourceapp.AccountView
	repository resourceapp.CodeRepositoryView

	suts         []resourceapp.SUTView
	envs         []resourceapp.EnvironmentView
	pages        []resourceapp.PageView
	apis         []resourceapp.APIView
	databases    []resourceapp.DatabaseView
	accounts     []resourceapp.AccountView
	repositories []resourceapp.CodeRepositoryView
	catalog      []resourceapp.SUTView
	hasMore      bool

	importPagesResult resourceapp.ImportPagesResult
	importAPIsResult  resourceapp.ImportAPIsResult

	lastActor     int64
	lastID        int64
	lastParentID  int64
	lastLimit     int
	lastBefore    *int64
	lastSUTInput  resourceapp.SUTInput
	lastSUTPatch  resourceapp.SUTPatch
	lastEnvInput  resourceapp.EnvironmentInput
	lastEnvPatch  resourceapp.EnvironmentPatch
	lastPageInput resourceapp.PageInput
	lastPagePatch resourceapp.PagePatch
	lastAPIInput  resourceapp.APIInput
	lastAPIPatch  resourceapp.APIPatch
	lastDBInput   resourceapp.DatabaseInput
	lastDBPatch   resourceapp.DatabasePatch
	lastAcctInput resourceapp.AccountInput
	lastAcctPatch resourceapp.AccountPatch
	lastRepoInput resourceapp.RepositoryInput
	lastRepoPatch resourceapp.RepositoryPatch
	lastPageItems []resourceapp.PageInput
	lastAPIItems  []resourceapp.APIInput
}

func (f *fakeService) Catalog(context.Context) ([]resourceapp.SUTView, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.catalog, nil
}

func (f *fakeService) CreateSUT(_ context.Context, input resourceapp.SUTInput, actorID int64) (resourceapp.SUTView, error) {
	f.lastSUTInput, f.lastActor = input, actorID
	return f.sut, f.err
}

func (f *fakeService) UpdateSUT(_ context.Context, id int64, patch resourceapp.SUTPatch, actorID int64) (resourceapp.SUTView, error) {
	f.lastID, f.lastSUTPatch, f.lastActor = id, patch, actorID
	return f.sut, f.err
}

func (f *fakeService) DeleteSUT(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListSUTs(_ context.Context, limit int, beforeID *int64) ([]resourceapp.SUTView, bool, error) {
	f.lastLimit, f.lastBefore = limit, beforeID
	return f.suts, f.hasMore, f.err
}

func (f *fakeService) GetSUT(_ context.Context, id int64) (resourceapp.SUTView, error) {
	f.lastID = id
	return f.sut, f.err
}

func (f *fakeService) CreateEnvironment(_ context.Context, sutID int64, input resourceapp.EnvironmentInput, actorID int64) (resourceapp.EnvironmentView, error) {
	f.lastParentID, f.lastEnvInput, f.lastActor = sutID, input, actorID
	return f.env, f.err
}

func (f *fakeService) UpdateEnvironment(_ context.Context, id int64, patch resourceapp.EnvironmentPatch, actorID int64) (resourceapp.EnvironmentView, error) {
	f.lastID, f.lastEnvPatch, f.lastActor = id, patch, actorID
	return f.env, f.err
}

func (f *fakeService) DeleteEnvironment(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListEnvironments(_ context.Context, sutID int64) ([]resourceapp.EnvironmentView, error) {
	f.lastParentID = sutID
	return f.envs, f.err
}

func (f *fakeService) CreatePage(_ context.Context, environmentID int64, input resourceapp.PageInput, actorID int64) (resourceapp.PageView, error) {
	f.lastParentID, f.lastPageInput, f.lastActor = environmentID, input, actorID
	return f.page, f.err
}

func (f *fakeService) UpdatePage(_ context.Context, id int64, patch resourceapp.PagePatch, actorID int64) (resourceapp.PageView, error) {
	f.lastID, f.lastPagePatch, f.lastActor = id, patch, actorID
	return f.page, f.err
}

func (f *fakeService) DeletePage(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListPages(_ context.Context, environmentID int64) ([]resourceapp.PageView, error) {
	f.lastParentID = environmentID
	return f.pages, f.err
}

func (f *fakeService) ImportPages(_ context.Context, environmentID int64, items []resourceapp.PageInput, actorID int64) (resourceapp.ImportPagesResult, error) {
	f.lastParentID, f.lastPageItems, f.lastActor = environmentID, items, actorID
	return f.importPagesResult, f.err
}

func (f *fakeService) CreateAPI(_ context.Context, environmentID int64, input resourceapp.APIInput, actorID int64) (resourceapp.APIView, error) {
	f.lastParentID, f.lastAPIInput, f.lastActor = environmentID, input, actorID
	return f.api, f.err
}

func (f *fakeService) UpdateAPI(_ context.Context, id int64, patch resourceapp.APIPatch, actorID int64) (resourceapp.APIView, error) {
	f.lastID, f.lastAPIPatch, f.lastActor = id, patch, actorID
	return f.api, f.err
}

func (f *fakeService) DeleteAPI(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListAPIs(_ context.Context, environmentID int64) ([]resourceapp.APIView, error) {
	f.lastParentID = environmentID
	return f.apis, f.err
}

func (f *fakeService) ImportAPIs(_ context.Context, environmentID int64, items []resourceapp.APIInput, actorID int64) (resourceapp.ImportAPIsResult, error) {
	f.lastParentID, f.lastAPIItems, f.lastActor = environmentID, items, actorID
	return f.importAPIsResult, f.err
}

func (f *fakeService) CreateDatabase(_ context.Context, sutID int64, input resourceapp.DatabaseInput, actorID int64) (resourceapp.DatabaseView, error) {
	f.lastParentID, f.lastDBInput, f.lastActor = sutID, input, actorID
	return f.database, f.err
}

func (f *fakeService) UpdateDatabase(_ context.Context, id int64, patch resourceapp.DatabasePatch, actorID int64) (resourceapp.DatabaseView, error) {
	f.lastID, f.lastDBPatch, f.lastActor = id, patch, actorID
	return f.database, f.err
}

func (f *fakeService) DeleteDatabase(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListDatabases(_ context.Context, sutID int64) ([]resourceapp.DatabaseView, error) {
	f.lastParentID = sutID
	return f.databases, f.err
}

func (f *fakeService) CreateAccount(_ context.Context, sutID int64, input resourceapp.AccountInput, actorID int64) (resourceapp.AccountView, error) {
	f.lastParentID, f.lastAcctInput, f.lastActor = sutID, input, actorID
	return f.account, f.err
}

func (f *fakeService) UpdateAccount(_ context.Context, id int64, patch resourceapp.AccountPatch, actorID int64) (resourceapp.AccountView, error) {
	f.lastID, f.lastAcctPatch, f.lastActor = id, patch, actorID
	return f.account, f.err
}

func (f *fakeService) DeleteAccount(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListAccounts(_ context.Context, sutID int64) ([]resourceapp.AccountView, error) {
	f.lastParentID = sutID
	return f.accounts, f.err
}

func (f *fakeService) CreateRepository(_ context.Context, input resourceapp.RepositoryInput, actorID int64) (resourceapp.CodeRepositoryView, error) {
	f.lastRepoInput, f.lastActor = input, actorID
	return f.repository, f.err
}

func (f *fakeService) UpdateRepository(_ context.Context, id int64, patch resourceapp.RepositoryPatch, actorID int64) (resourceapp.CodeRepositoryView, error) {
	f.lastID, f.lastRepoPatch, f.lastActor = id, patch, actorID
	return f.repository, f.err
}

func (f *fakeService) DeleteRepository(_ context.Context, id int64, actorID int64) error {
	f.lastID, f.lastActor = id, actorID
	return f.err
}

func (f *fakeService) ListRepositories(_ context.Context, limit int, beforeID *int64) ([]resourceapp.CodeRepositoryView, bool, error) {
	f.lastLimit, f.lastBefore = limit, beforeID
	return f.repositories, f.hasMore, f.err
}

func (f *fakeService) GetRepository(_ context.Context, id int64) (resourceapp.CodeRepositoryView, error) {
	f.lastID = id
	return f.repository, f.err
}

func sampleViews() (resourceapp.SUTView, resourceapp.EnvironmentView, resourceapp.PageView, resourceapp.APIView, resourceapp.DatabaseView, resourceapp.AccountView, resourceapp.CodeRepositoryView) {
	page := resourceapp.PageView{
		ID: "1003", SUTID: "1001", EnvironmentID: "1002", Name: "orders",
		URL: "https://app.example.com/orders", RouteTemplate: "/orders", PageType: "page",
		AuthRole: "user", Module: "orders", Status: resource.PageStatusApproved,
		Source: resource.PageSourceAdmin, CreatedAt: testNow, UpdatedAt: testNow,
	}
	api := resourceapp.APIView{
		ID: "1004", SUTID: "1001", EnvironmentID: "1002", Method: "GET",
		PathTemplate: "/api/orders", Name: "list", Source: resource.APISourceManual,
		Status: resource.APIStatusApproved, CreatedAt: testNow, UpdatedAt: testNow,
	}
	database := resourceapp.DatabaseView{
		ID: "1005", SUTID: "1001", EnvironmentID: "1002", Name: "orders-db",
		DSN: "postgres://db/orders", Username: "reader", PasswordSet: true,
		ReadOnly: true, Status: resource.StatusActive, CreatedAt: testNow, UpdatedAt: testNow,
	}
	account := resourceapp.AccountView{
		ID: "1006", SUTID: "1001", EnvironmentID: "1002", Username: "admin",
		Roles: []string{"admin"}, Notes: "seed", Status: resource.StatusActive,
		PasswordSet: true, CreatedAt: testNow, UpdatedAt: testNow,
	}
	environment := resourceapp.EnvironmentView{
		ID: "1002", SUTID: "1001", Name: "prod", BaseURL: "https://app.example.com",
		IsDefault: true, Status: resource.StatusActive,
		Pages: []resourceapp.PageView{page}, APIs: []resourceapp.APIView{api},
		Databases: []resourceapp.DatabaseView{database}, Accounts: []resourceapp.AccountView{account},
		CreatedAt: testNow, UpdatedAt: testNow,
	}
	sut := resourceapp.SUTView{
		ID: "1001", Name: "Order Center", Intro: "orders",
		Environments: []resourceapp.EnvironmentView{environment},
		CreatedAt:    testNow, UpdatedAt: testNow,
	}
	repository := resourceapp.CodeRepositoryView{
		ID: "1007", Name: "orders", Path: "https://git.example.com/orders.git",
		Branch: "main", Include: []string{"src/**"}, Exclude: []string{"vendor/**"},
		CreatedBy: 42, Status: resource.StatusActive, CreatedAt: testNow, UpdatedAt: testNow,
	}
	return sut, environment, page, api, database, account, repository
}

func newHandler() (*Handler, *fakeService) {
	sut, env, page, api, database, account, repository := sampleViews()
	svc := &fakeService{
		sut: sut, env: env, page: page, api: api,
		database: database, account: account, repository: repository,
		suts:         []resourceapp.SUTView{sut},
		envs:         []resourceapp.EnvironmentView{env},
		pages:        []resourceapp.PageView{page},
		apis:         []resourceapp.APIView{api},
		databases:    []resourceapp.DatabaseView{database},
		accounts:     []resourceapp.AccountView{account},
		repositories: []resourceapp.CodeRepositoryView{repository},
		catalog:      []resourceapp.SUTView{sut},
	}
	return New(svc), svc
}

func request(method, target, body string, actor *platform.Actor) *http.Request {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if actor != nil {
		req = req.WithContext(platform.WithActor(req.Context(), *actor))
	}
	return req
}

var admin = &platform.Actor{ID: 42, Role: "admin"}
var plainUser = &platform.Actor{ID: 77, Role: "user"}

func decodeData(t *testing.T, rec *httptest.ResponseRecorder, target any) {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (body %s)", err, rec.Body.String())
	}
	if len(envelope.Data) == 0 {
		t.Fatalf("missing data: %s", rec.Body.String())
	}
	if err := json.Unmarshal(envelope.Data, target); err != nil {
		t.Fatalf("decode data: %v (%s)", err, envelope.Data)
	}
}

func TestCreateSUTShape(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPost, "/api/resources/suts", `{"name":"Order Center","intro":"orders"}`, admin)
	rec := httptest.NewRecorder()
	handler.CreateSUT(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var dto sutDTO
	decodeData(t, rec, &dto)
	if dto.ID != "1001" || dto.Name != "Order Center" {
		t.Fatalf("dto = %#v", dto)
	}
	if svc.lastActor != 42 || svc.lastSUTInput.Name != "Order Center" || svc.lastSUTInput.Intro != "orders" {
		t.Fatalf("service call = %#v actor=%d", svc.lastSUTInput, svc.lastActor)
	}
}

func TestWriteEndpointsRequireActor(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodPost, "/api/resources/suts", `{"name":"x"}`, nil)
	rec := httptest.NewRecorder()
	handler.CreateSUT(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}

	req = request(http.MethodDelete, "/api/resources/suts/1001", "", nil)
	req.SetPathValue("id", "1001")
	rec = httptest.NewRecorder()
	handler.DeleteSUT(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("delete status = %d", rec.Code)
	}
}

func TestCreateSUTRejectsInvalidBody(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodPost, "/api/resources/suts", `{"name":`, admin)
	rec := httptest.NewRecorder()
	handler.CreateSUT(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "invalid_argument" || body.Error.Details["field"] != "body" {
		t.Fatalf("error = %#v", body.Error)
	}
}

func TestListSUTsPaginates(t *testing.T) {
	handler, svc := newHandler()
	svc.suts = append(svc.suts, resourceapp.SUTView{ID: "1000", Name: "Older"})
	svc.hasMore = true
	req := request(http.MethodGet, "/api/resources/suts?limit=2&cursor=9002", "", admin)
	rec := httptest.NewRecorder()
	handler.ListSUTs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data       []sutDTO `json:"data"`
		NextCursor string   `json:"next_cursor"`
		HasMore    bool     `json:"has_more"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Data) != 2 || !body.HasMore || body.NextCursor != "1000" {
		t.Fatalf("body = %#v", body)
	}
	if svc.lastLimit != 2 || svc.lastBefore == nil || *svc.lastBefore != 9002 {
		t.Fatalf("limit = %d before = %v", svc.lastLimit, svc.lastBefore)
	}

	req = request(http.MethodGet, "/api/resources/suts?cursor=abc", "", admin)
	rec = httptest.NewRecorder()
	handler.ListSUTs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid cursor status = %d", rec.Code)
	}
}

func TestGetSUTNestsResourcesAndHidesPasswords(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodGet, "/api/resources/suts/1001", "", plainUser)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.GetSUT(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	var raw struct {
		Data struct {
			ID           string           `json:"id"`
			CreatedAt    string           `json:"created_at"`
			Environments []map[string]any `json:"environments"`
			Accounts     []map[string]any `json:"accounts"`
			Databases    []map[string]any `json:"databases"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if raw.Data.ID != "1001" || len(raw.Data.Environments) != 1 {
		t.Fatalf("data = %#v", raw.Data)
	}
	if raw.Data.CreatedAt != "2026-10-31T12:00:00Z" {
		t.Fatalf("created_at = %q", raw.Data.CreatedAt)
	}
	env := raw.Data.Environments[0]
	pages, _ := env["pages"].([]any)
	if len(pages) != 1 {
		t.Fatalf("pages = %#v", env["pages"])
	}
	accounts, _ := env["accounts"].([]any)
	if len(accounts) != 1 {
		t.Fatalf("accounts = %#v", env["accounts"])
	}
	account := accounts[0].(map[string]any)
	if _, ok := account["password"]; ok {
		t.Fatalf("account exposes password: %#v", account)
	}
	if account["password_set"] != true {
		t.Fatalf("account = %#v", account)
	}
	databases, _ := env["databases"].([]any)
	database := databases[0].(map[string]any)
	if _, ok := database["password"]; ok {
		t.Fatalf("database exposes password: %#v", database)
	}
	if database["password_set"] != true {
		t.Fatalf("database = %#v", database)
	}
}

func TestCreateAccountMasksPassword(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPost, "/api/resources/suts/1001/accounts",
		`{"environment_id":"1002","username":"admin","password":"s3cret","roles":["admin"]}`, admin)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.CreateAccount(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"password"`) {
		t.Fatalf("response leaks password: %s", rec.Body.String())
	}
	var dto accountDTO
	decodeData(t, rec, &dto)
	if !dto.PasswordSet || dto.Username != "admin" {
		t.Fatalf("dto = %#v", dto)
	}
	if svc.lastAcctInput.Password != "s3cret" || svc.lastAcctInput.EnvironmentID != 1002 {
		t.Fatalf("input = %#v", svc.lastAcctInput)
	}
	if svc.lastParentID != 1001 || svc.lastActor != 42 {
		t.Fatalf("parent = %d actor = %d", svc.lastParentID, svc.lastActor)
	}
}

func TestCreateDatabaseMapsReadOnly(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPost, "/api/resources/suts/1001/databases",
		`{"environment_id":"1002","name":"orders","dsn":"postgres://db/orders","username":"reader","password":"pw"}`, admin)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.CreateDatabase(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastDBInput.ReadOnly != nil {
		t.Fatalf("read_only = %v, want nil default", svc.lastDBInput.ReadOnly)
	}
	req = request(http.MethodPost, "/api/resources/suts/1001/databases",
		`{"environment_id":"1002","name":"orders","dsn":"postgres://db/orders","username":"reader","read_only":false}`, admin)
	req.SetPathValue("id", "1001")
	rec = httptest.NewRecorder()
	handler.CreateDatabase(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d", rec.Code)
	}
	if svc.lastDBInput.ReadOnly == nil || *svc.lastDBInput.ReadOnly {
		t.Fatalf("read_only = %v", svc.lastDBInput.ReadOnly)
	}
}

func TestImportPagesShape(t *testing.T) {
	handler, svc := newHandler()
	svc.importPagesResult = resourceapp.ImportPagesResult{
		Created: 1, Skipped: 1, Items: []resourceapp.PageView{svc.page},
	}
	req := request(http.MethodPost, "/api/resources/environments/1002/pages/import",
		`{"items":[{"name":"orders","url":"https://app.example.com/orders"},{"name":"dup","url":"https://app.example.com/orders?x=1"}]}`, admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.ImportPages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Data struct {
			Created int       `json:"created"`
			Skipped int       `json:"skipped"`
			Items   []pageDTO `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Data.Created != 1 || body.Data.Skipped != 1 || len(body.Data.Items) != 1 {
		t.Fatalf("data = %#v", body.Data)
	}
	if svc.lastParentID != 1002 || len(svc.lastPageItems) != 2 {
		t.Fatalf("parent = %d items = %d", svc.lastParentID, len(svc.lastPageItems))
	}
	if svc.lastPageItems[0].Name != "orders" || svc.lastPageItems[0].URL != "https://app.example.com/orders" {
		t.Fatalf("items = %#v", svc.lastPageItems)
	}
}

func TestImportInvalidItemReturns400WithIndex(t *testing.T) {
	handler, svc := newHandler()
	svc.err = &platform.Error{
		Status: 400, Code: "invalid_argument", Message: "import item is invalid",
		Details: map[string]any{"field": "items", "index": 1},
	}
	req := request(http.MethodPost, "/api/resources/environments/1002/apis/import",
		`{"items":[{"method":"GET","path_template":"/api/a","name":"a"}]}`, admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.ImportAPIs(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "invalid_argument" || body.Error.Details["index"] != float64(1) {
		t.Fatalf("error = %#v", body.Error)
	}
}

func TestImportAPIsShape(t *testing.T) {
	handler, svc := newHandler()
	svc.importAPIsResult = resourceapp.ImportAPIsResult{
		Created: 1, Skipped: 0, Items: []resourceapp.APIView{svc.api},
	}
	req := request(http.MethodPost, "/api/resources/environments/1002/apis/import",
		`{"items":[{"method":"POST","path_template":"/api/orders","name":"create"}]}`, admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.ImportAPIs(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if len(svc.lastAPIItems) != 1 || svc.lastAPIItems[0].Method != "POST" {
		t.Fatalf("items = %#v", svc.lastAPIItems)
	}
}

func TestDeleteUsageConflictReturns409(t *testing.T) {
	handler, svc := newHandler()
	svc.err = platform.Conflict("sut is referenced by a version binding")
	req := request(http.MethodDelete, "/api/resources/suts/1001", "", admin)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.DeleteSUT(rec, req)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Error.Code != "conflict" {
		t.Fatalf("code = %q", body.Error.Code)
	}
	if svc.lastID != 1001 {
		t.Fatalf("last id = %d", svc.lastID)
	}
}

func TestDeleteReturns204(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodDelete, "/api/resources/pages/1003", "", admin)
	req.SetPathValue("id", "1003")
	rec := httptest.NewRecorder()
	handler.DeletePage(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("body = %s", rec.Body.String())
	}
	if svc.lastID != 1003 || svc.lastActor != 42 {
		t.Fatalf("id = %d actor = %d", svc.lastID, svc.lastActor)
	}
}

func TestInvalidPathIDReturns400(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodGet, "/api/resources/suts/abc", "", admin)
	req.SetPathValue("id", "abc")
	rec := httptest.NewRecorder()
	handler.GetSUT(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}

func TestUpdateSUTOnlyPatchesProvidedFields(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPatch, "/api/resources/suts/1001", `{"name":"Renamed"}`, admin)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.UpdateSUT(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastSUTPatch.Name == nil || *svc.lastSUTPatch.Name != "Renamed" || svc.lastSUTPatch.Intro != nil {
		t.Fatalf("patch = %#v", svc.lastSUTPatch)
	}
}

func TestEnvironmentAndPageCRUDShapes(t *testing.T) {
	handler, svc := newHandler()

	req := request(http.MethodPost, "/api/resources/suts/1001/environments",
		`{"name":"prod","base_url":"https://app.example.com","is_default":true}`, admin)
	req.SetPathValue("id", "1001")
	rec := httptest.NewRecorder()
	handler.CreateEnvironment(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("env status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastEnvInput.Name != "prod" || !svc.lastEnvInput.IsDefault || svc.lastParentID != 1001 {
		t.Fatalf("env input = %#v parent = %d", svc.lastEnvInput, svc.lastParentID)
	}
	var envDTO environmentDTO
	decodeData(t, rec, &envDTO)
	if envDTO.ID != "1002" || !envDTO.IsDefault {
		t.Fatalf("env dto = %#v", envDTO)
	}

	req = request(http.MethodPost, "/api/resources/environments/1002/pages",
		`{"name":"orders","url":"https://app.example.com/orders"}`, admin)
	req.SetPathValue("id", "1002")
	rec = httptest.NewRecorder()
	handler.CreatePage(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("page status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastPageInput.RouteTemplate != "" || svc.lastPageInput.URL != "https://app.example.com/orders" {
		t.Fatalf("page input = %#v", svc.lastPageInput)
	}

	route := "/orders/list"
	req = request(http.MethodPatch, "/api/resources/pages/1003", `{"route_template":"/orders/list"}`, admin)
	req.SetPathValue("id", "1003")
	rec = httptest.NewRecorder()
	handler.UpdatePage(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch page status = %d", rec.Code)
	}
	if svc.lastPagePatch.RouteTemplate == nil || *svc.lastPagePatch.RouteTemplate != route {
		t.Fatalf("page patch = %#v", svc.lastPagePatch)
	}

	req = request(http.MethodGet, "/api/resources/environments/1002/pages", "", plainUser)
	req.SetPathValue("id", "1002")
	rec = httptest.NewRecorder()
	handler.ListPages(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list pages status = %d", rec.Code)
	}
	if svc.lastParentID != 1002 {
		t.Fatalf("parent = %d", svc.lastParentID)
	}
}

func TestAPICRUDShapes(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPost, "/api/resources/environments/1002/apis",
		`{"method":"GET","path_template":"/api/orders","name":"list"}`, admin)
	req.SetPathValue("id", "1002")
	rec := httptest.NewRecorder()
	handler.CreateAPI(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastAPIInput.Method != "GET" || svc.lastAPIInput.PathTemplate != "/api/orders" {
		t.Fatalf("input = %#v", svc.lastAPIInput)
	}
	req = request(http.MethodPatch, "/api/resources/apis/1004", `{"name":"list orders"}`, admin)
	req.SetPathValue("id", "1004")
	rec = httptest.NewRecorder()
	handler.UpdateAPI(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d", rec.Code)
	}
	if svc.lastAPIPatch.Name == nil || *svc.lastAPIPatch.Name != "list orders" {
		t.Fatalf("patch = %#v", svc.lastAPIPatch)
	}
}

func TestCodeRepositoriesCRUDAndPagination(t *testing.T) {
	handler, svc := newHandler()
	req := request(http.MethodPost, "/api/resources/code-repos",
		`{"name":"orders","path":"https://git.example.com/orders.git","branch":"main","include":["src/**"]}`, plainUser)
	rec := httptest.NewRecorder()
	handler.CreateRepository(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if svc.lastRepoInput.Branch != "main" || len(svc.lastRepoInput.Include) != 1 || svc.lastActor != 77 {
		t.Fatalf("input = %#v actor = %d", svc.lastRepoInput, svc.lastActor)
	}
	var repoDTO codeRepositoryDTO
	decodeData(t, rec, &repoDTO)
	if repoDTO.ID != "1007" || repoDTO.CreatedBy != "42" {
		t.Fatalf("dto = %#v", repoDTO)
	}

	req = request(http.MethodGet, "/api/resources/code-repos/1007", "", plainUser)
	req.SetPathValue("id", "1007")
	rec = httptest.NewRecorder()
	handler.GetRepository(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d", rec.Code)
	}

	svc.hasMore = true
	req = request(http.MethodGet, "/api/resources/code-repos?limit=5", "", plainUser)
	rec = httptest.NewRecorder()
	handler.ListRepositories(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d", rec.Code)
	}
	if svc.lastLimit != 5 {
		t.Fatalf("limit = %d", svc.lastLimit)
	}

	name := "renamed"
	_ = name
	req = request(http.MethodPatch, "/api/resources/code-repos/1007", `{"name":"renamed"}`, plainUser)
	req.SetPathValue("id", "1007")
	rec = httptest.NewRecorder()
	handler.UpdateRepository(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d", rec.Code)
	}
	if svc.lastRepoPatch.Name == nil || *svc.lastRepoPatch.Name != "renamed" {
		t.Fatalf("patch = %#v", svc.lastRepoPatch)
	}
}

func TestCatalogShape(t *testing.T) {
	handler, _ := newHandler()
	req := request(http.MethodGet, "/api/resources/catalog", "", plainUser)
	rec := httptest.NewRecorder()
	handler.Catalog(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			SUTs         []json.RawMessage `json:"suts"`
			Repositories []json.RawMessage `json:"repositories"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data.SUTs) != 1 {
		t.Fatalf("catalog = %s", rec.Body.String())
	}
}

func TestServiceErrorsAreMapped(t *testing.T) {
	handler, svc := newHandler()
	svc.err = platform.NotFound("sut")
	req := request(http.MethodGet, "/api/resources/suts/9999", "", plainUser)
	req.SetPathValue("id", "9999")
	rec := httptest.NewRecorder()
	handler.GetSUT(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}

	svc.err = errors.New("boom")
	req = request(http.MethodGet, "/api/resources/suts/1001", "", plainUser)
	req.SetPathValue("id", "1001")
	rec = httptest.NewRecorder()
	handler.GetSUT(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
}
