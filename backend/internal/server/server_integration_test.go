//go:build integration

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

type testAPI struct {
	t       *testing.T
	baseURL string
	token   string
}

func newTestAPI(t *testing.T) *testAPI {
	t.Helper()
	pool := testutil.NewDB(t)
	ctx := context.Background()

	settingsRepo := settingspg.New(pool)
	defs := settingsapp.Definitions("http://127.0.0.1:13336")
	ids, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	clk := clock.System{}
	auditSvc := auditapp.New(auditpg.New(pool), ids, clk)
	eventSvc := eventingapp.New(eventingpg.New(pool), eventingapp.NewHub(eventingapp.DefaultHubBuffer))
	settingsSvc := settingsapp.New(settingsRepo, defs, ids, clk, auditSvc, eventSvc)
	if err := settingsSvc.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	identitySvc := identityapp.New(identityinfra.NewRepository(pool), identityinfra.NewHasher(identityinfra.DefaultBcryptCost), ids, clk, auditSvc)
	if _, err := identitySvc.EnsureSeedAdmin(ctx); err != nil {
		t.Fatalf("seed admin: %v", err)
	}
	llmSvc := llmapp.New(llminfra.NewRepository(pool), llminfra.NewClient(), ids, clk, auditSvc, eventSvc)
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settingsSvc, ids, auditSvc, eventSvc)
	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clk, nil, auditSvc, eventSvc)
	versionSvc := versionapp.New(versionpg.New(pool, ids), resourceSvc, ids, clk, auditSvc, eventSvc)
	resourceSvc.SetUsageChecker(versionSvc)

	// Keep integration uploads out of the repository working tree.
	if _, err := settingsSvc.Update(ctx, "paths.data_root", t.TempDir(), 1); err != nil {
		t.Fatalf("set data root: %v", err)
	}

	handler := server.New(server.Deps{
		Config:   config.Config{APIPort: 0, WebPort: 6868, ExecutorPort: 13336},
		Pool:     pool,
		Version:  "test",
		Settings: settingsSvc,
		Identity: identitySvc,
		Audit:    auditSvc,
		Events:   eventSvc,
		Storage:  storageSvc,
		LLM:      llmSvc,
		Resource: resourceSvc,
		Versions: versionSvc,
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return &testAPI{t: t, baseURL: httpServer.URL}
}

func (a *testAPI) do(method, path string, body any, wantStatus int) map[string]any {
	a.t.Helper()
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			a.t.Fatalf("marshal request: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, a.baseURL+path, reader)
	if err != nil {
		a.t.Fatalf("new request: %v", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		a.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != wantStatus {
		a.t.Fatalf("%s %s status = %d, want %d: %s", method, path, resp.StatusCode, wantStatus, raw)
	}
	if len(raw) == 0 {
		return nil
	}
	var envelope map[string]any
	if err := json.Unmarshal(raw, &envelope); err != nil {
		a.t.Fatalf("%s %s decode: %v (%s)", method, path, err, raw)
	}
	return envelope
}

func (a *testAPI) login(username, password string) {
	a.t.Helper()
	a.token = ""
	body := a.do(http.MethodPost, "/api/auth/login", map[string]any{"username": username, "password": password}, http.StatusOK)
	data := body["data"].(map[string]any)
	a.token = data["token"].(string)
	if a.token == "" {
		a.t.Fatal("empty token")
	}
}

func (a *testAPI) data(body map[string]any) map[string]any {
	a.t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		a.t.Fatalf("missing data object: %#v", body)
	}
	return data
}

func (a *testAPI) listData(body map[string]any) []any {
	a.t.Helper()
	data, ok := body["data"].([]any)
	if !ok {
		a.t.Fatalf("missing data list: %#v", body)
	}
	return data
}

func TestP0EndToEndFlow(t *testing.T) {
	api := newTestAPI(t)

	api.login("admin", "admin123")
	me := api.data(api.do(http.MethodGet, "/api/auth/me", nil, http.StatusOK))
	if me["username"] != "admin" || me["role"] != "admin" {
		t.Fatalf("me = %#v", me)
	}

	settings := api.listData(api.do(http.MethodGet, "/api/admin/settings", nil, http.StatusOK))
	if len(settings) != 44 {
		t.Fatalf("settings count = %d", len(settings))
	}
	updated := api.data(api.do(http.MethodPatch, "/api/admin/settings/execution.global_concurrency", map[string]any{"value": 5}, http.StatusOK))
	if updated["value"] != float64(5) {
		t.Fatalf("updated setting = %#v", updated)
	}

	sut := api.data(api.do(http.MethodPost, "/api/resources/suts", map[string]any{"name": "Order Center", "intro": "e2e"}, http.StatusCreated))
	sutID := sut["id"].(string)
	environment := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/environments", map[string]any{"name": "dev", "base_url": "http://localhost:8080", "is_default": true}, http.StatusCreated))
	environmentID := environment["id"].(string)
	page := api.data(api.do(http.MethodPost, "/api/resources/environments/"+environmentID+"/pages", map[string]any{"name": "Home", "url": "http://localhost:8080/home", "route_template": "/home"}, http.StatusCreated))
	pageID := page["id"].(string)
	apiEndpoint := api.data(api.do(http.MethodPost, "/api/resources/environments/"+environmentID+"/apis", map[string]any{"name": "Orders", "method": "GET", "path_template": "/api/orders"}, http.StatusCreated))
	apiID := apiEndpoint["id"].(string)
	database := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/databases", map[string]any{"environment_id": environmentID, "name": "order_db", "dsn": "postgres://localhost/order", "username": "readonly", "password": "secret", "read_only": true}, http.StatusCreated))
	databaseID := database["id"].(string)
	if database["password_set"] != true {
		t.Fatalf("database password_set = %#v", database["password_set"])
	}
	account := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/accounts", map[string]any{"environment_id": environmentID, "username": "buyer", "password": "secret", "roles": []string{"buyer"}}, http.StatusCreated))
	accountID := account["id"].(string)
	repository := api.data(api.do(http.MethodPost, "/api/resources/code-repos", map[string]any{"name": "order-service", "path": "D:/repo", "branch": "main"}, http.StatusCreated))
	repositoryID := repository["id"].(string)

	catalog := api.data(api.do(http.MethodGet, "/api/resources/catalog", nil, http.StatusOK))
	if len(catalog["suts"].([]any)) != 1 || len(catalog["repositories"].([]any)) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}

	version := api.data(api.do(http.MethodPost, "/api/versions", map[string]any{
		"title": "Order Center Q4",
		"bindings": []any{map[string]any{
			"sut_id": sutID, "environment_id": environmentID, "code_repository_id": repositoryID,
			"page_ids": []string{pageID}, "api_ids": []string{apiID}, "database_ids": []string{databaseID},
			"accounts": []any{map[string]any{"account_id": accountID, "role": "buyer"}},
		}},
	}, http.StatusCreated))
	versionID := version["id"].(string)
	if version["status"] != "initial" {
		t.Fatalf("version = %#v", version)
	}
	detail := api.data(api.do(http.MethodGet, "/api/versions/"+versionID, nil, http.StatusOK))
	bindings := detail["bindings"].([]any)
	if len(bindings) != 1 {
		t.Fatalf("detail = %#v", detail)
	}
	binding := bindings[0].(map[string]any)
	if binding["sut"].(map[string]any)["name"] != "Order Center" {
		t.Fatalf("binding = %#v", binding)
	}
	if len(binding["pages"].([]any)) != 1 || len(binding["accounts"].([]any)) != 1 {
		t.Fatalf("binding = %#v", binding)
	}

	events := api.do(http.MethodGet, "/api/events?channel=version:"+versionID, nil, http.StatusOK)
	eventItems := events["data"].([]any)
	if len(eventItems) != 1 || eventItems[0].(map[string]any)["type"] != "version.created" {
		t.Fatalf("version events = %#v", events)
	}

	// Usage guard: a version-bound SUT cannot be deleted.
	api.do(http.MethodDelete, "/api/resources/suts/"+sutID, nil, http.StatusConflict)

	// Admin creates a normal user; that user cannot read admin settings.
	user := api.data(api.do(http.MethodPost, "/api/admin/users", map[string]any{"username": "tester", "display_name": "Tester", "password": "tester-pass"}, http.StatusCreated))
	if user["role"] != "user" {
		t.Fatalf("created user = %#v", user)
	}
	api.login("tester", "tester-pass")
	api.do(http.MethodGet, "/api/admin/settings", nil, http.StatusForbidden)

	// Content upload/download over multipart.
	api.login("admin", "admin123")
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", "hello.txt")
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	_, _ = part.Write([]byte("hello eztest"))
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	req, _ := http.NewRequest(http.MethodPost, api.baseURL+"/api/content", &buffer)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+api.token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	uploadRaw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload status = %d: %s", resp.StatusCode, uploadRaw)
	}
	var upload struct {
		Data struct {
			SHA256 string `json:"sha256"`
		} `json:"data"`
	}
	if err := json.Unmarshal(uploadRaw, &upload); err != nil {
		t.Fatalf("upload decode: %v", err)
	}
	downloadReq, _ := http.NewRequest(http.MethodGet, api.baseURL+"/api/content/"+upload.Data.SHA256, nil)
	downloadReq.Header.Set("Authorization", "Bearer "+api.token)
	downloadResp, err := http.DefaultClient.Do(downloadReq)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer downloadResp.Body.Close()
	downloaded, _ := io.ReadAll(downloadResp.Body)
	if downloadResp.StatusCode != http.StatusOK || string(downloaded) != "hello eztest" {
		t.Fatalf("download status=%d body=%q", downloadResp.StatusCode, downloaded)
	}
	if got := downloadResp.Header.Get("X-Content-SHA256"); got != upload.Data.SHA256 {
		t.Fatalf("sha header = %q", got)
	}

	auditBody := api.do(http.MethodGet, "/api/admin/audit-logs?limit=10", nil, http.StatusOK)
	if len(auditBody["data"].([]any)) == 0 {
		t.Fatal("audit log is empty")
	}

	// Health stays public and reports the database.
	healthResp, err := http.Get(api.baseURL + "/api/health")
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	healthRaw, _ := io.ReadAll(healthResp.Body)
	healthResp.Body.Close()
	if healthResp.StatusCode != http.StatusOK || !strings.Contains(string(healthRaw), `"db":"ok"`) {
		t.Fatalf("health status=%d body=%s", healthResp.StatusCode, healthRaw)
	}

	t.Logf("P0 flow ok: version=%s sha=%s", versionID, upload.Data.SHA256)
}
