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
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/p4"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chainproviders "github.com/EdouardRichard/EzTest/backend/internal/chain/infrastructure/providers"
	chainapi "github.com/EdouardRichard/EzTest/backend/internal/chain/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	deliveryadapters "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/adapters"
	deliverycontent "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	deliveryrender "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/storageexport"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
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
	versionadapters "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/adapters"
)

type testAPI struct {
	t         *testing.T
	baseURL   string
	token     string
	pool      *pgxpool.Pool
	reportSvc *deliveryapp.ReportService
	issueSvc  *deliveryapp.IssueService
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
	versionRepo := versionpg.New(pool, ids)
	versionSvc := versionapp.New(versionRepo, resourceSvc, ids, clk, auditSvc, eventSvc, nil)
	resourceSvc.SetUsageChecker(versionSvc)
	designRepo := designpg.New(pool)
	designGraph := designapp.NewGraphService(designRepo, ids, clk, auditSvc, eventSvc, nil)
	designStale := designapp.NewStaleService(designRepo, clk, auditSvc, eventSvc)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	requirementSvc := versionapp.NewRequirementService(
		requirementRepo, versionSvc, resourceSvc, designStale, auditSvc, eventSvc, ids, clk)
	storageExportSvc := storageapp.NewExportService(storagepg.NewExportRepository(pool), storageSvc, ids, clk)
	designExports := designapp.NewExportService(designRepo, storageexport.New(storageExportSvc), ids, clk, eventSvc, auditSvc)

	// Keep integration uploads out of the repository working tree.
	if _, err := settingsSvc.Update(ctx, "paths.data_root", t.TempDir(), 1); err != nil {
		t.Fatalf("set data root: %v", err)
	}

	// P3 execution surface: real repositories/services over the test schema; the
	// executor probe is left unregistered because the routes test never polls.
	executionRepo := executionpg.NewRepository(pool, ids)
	executionVersions := executionadapters.NewVersionAdapter(versionSvc)
	executionCases := executionadapters.NewDesignCaseReader(designGraph)
	executionContent := executionadapters.NewContentStore(storageSvc)
	executionRegistry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo: executionRepo.Executors, Settings: settingsSvc, Clock: clk, IDs: ids,
	})
	executionHumans := executionapp.NewHumanService(executionapp.HumanServiceDeps{
		Humans: executionRepo.Humans, CaseRuns: executionRepo.CaseRuns, Waves: executionRepo.Waves,
		Settings: settingsSvc, IDs: ids, Clock: clk, Events: eventSvc, Audit: auditSvc,
		Timeout: 1800 * time.Second,
	})
	executionIntents := executionapp.NewIntentService(executionapp.IntentServiceDeps{
		Intents: executionRepo.Intents, Cases: executionCases, Versions: executionVersions,
		IDs: ids, Clock: clk, Events: eventSvc, Audit: auditSvc,
	})
	executionPause := executionapp.NewPauseService(executionapp.PauseServiceDeps{
		Intents: executionRepo.Intents, Waves: executionRepo.Waves, CaseRuns: executionRepo.CaseRuns,
		Humans: executionRepo.Humans, Clock: clk, Events: eventSvc, Audit: auditSvc,
	})
	executionHandler := executionhttp.New(executionhttp.Deps{
		Intents: executionRepo.Intents, Waves: executionRepo.Waves, CaseRuns: executionRepo.CaseRuns,
		Results: executionRepo.Results, Evidence: executionRepo.Evidence, Humans: executionRepo.Humans,
		Executors: executionRepo.Executors, Cases: executionCases, Versions: executionVersions,
		Content: executionContent, Settings: settingsSvc,
		IntentService: executionIntents, PauseService: executionPause, HumanService: executionHumans,
		Registry: executionRegistry,
	})

	// P4 delivery/chain/steward composition (mirrors cmd/eztest with real
	// repositories and the real adapters).
	workQueue := agentapp.NewWorkQueue(agentpg.NewWorkItemRepository(pool, ids), clk, 30*time.Second, 3)
	issueRepo := deliverypg.NewIssueRepository(pool, ids)
	analysisRepo := deliverypg.NewAnalysisRepository(pool, ids)
	reportRepo := deliverypg.NewReportRepository(pool, ids)
	closureRepo := versionpg.NewClosureRepository(pool)
	deliveryContent := deliverycontent.New(storageSvc, deliverypg.NewContentRefReader(pool))
	reportRenderer, err := deliveryrender.NewHTMLRenderer("1")
	if err != nil {
		t.Fatalf("report renderer: %v", err)
	}
	p4Tasks := p4.NewTaskEnsurer(workQueue, reportRepo, testRequirementVersionLookup{repo: requirementRepo})
	readDeps := deliveryadapters.Deps{
		Versions: versionSvc, Requirements: requirementSvc, Closures: closureRepo, Design: designGraph,
		Waves: executionRepo.Waves, CaseRuns: executionRepo.CaseRuns, Results: executionRepo.Results,
		Evidence: executionRepo.Evidence, SUTContexts: executionRepo.SUTContexts,
		Intents: executionRepo.Intents, Humans: executionRepo.Humans, Issues: issueRepo,
	}
	deliveryEvents := deliveryadapters.NewEventPublisher(eventSvc)
	deliveryAudit := deliveryadapters.NewAuditSink(auditSvc)
	issueSvc := deliveryapp.NewIssueService(
		issueRepo, deliveryadapters.NewExecutionReader(readDeps), deliveryEvents, deliveryAudit, clk, p4Tasks)
	issueSvc.WithTriageReader(analysisRepo)
	reportSvc := deliveryapp.NewReportService(
		reportRepo, reportRenderer, deliveryContent, deliveryadapters.NewReportDataReader(readDeps),
		deliveryEvents, deliveryAudit, clk, p4Tasks,
		delivery.SectionOptions{Summary: true, Results: true, Issues: true, Coverage: true, Evidence: true})
	closureReaders := versionadapters.NewClosureReaders(versionadapters.Deps{
		Requirements: requirementRepo, Design: designGraph, CaseRuns: executionRepo.CaseRuns,
		Waves: executionRepo.Waves, Intents: executionRepo.Intents, Humans: executionRepo.Humans,
		WorkItems: workQueue, Issues: issueRepo,
	})
	versionCloseSvc := versionapp.NewVersionCloseService(versionapp.VersionCloseServiceDeps{
		Versions: versionRepo, WorkItems: closureReaders, Execution: versionadapters.NewVersionCloseExecution(closureReaders),
		HumanRequests: closureReaders, Requirements: requirementRepo,
		Reports: versionadapters.NewReportStatusReader(reportSvc), ReportStaler: reportSvc,
		Tasks: p4Tasks, Audit: auditSvc, Events: eventSvc, Clock: clk,
	})
	closureSvc := versionapp.NewClosureService(versionapp.ClosureServiceDeps{
		Closures: closureRepo, Requirements: requirementRepo, Cases: closureReaders,
		Runs: closureReaders, Coverage: closureReaders, Issues: closureReaders,
		WorkItems: closureReaders, Versions: versionCloseSvc, Tasks: p4Tasks,
		Audit: auditSvc, Events: eventSvc, IDs: ids, Clock: clk,
	})
	requirementSvc.SetVersionReopener(versionCloseSvc)
	designGraph.SetVersionReopener(versionCloseSvc)
	executionVersions.SetVersionReopener(versionCloseSvc)

	chainVersionProvider := chainproviders.NewVersionProvider(requirementSvc, versionSvc)
	chainDesignProvider := chainproviders.NewDesignProvider(designGraph)
	chainExecutionProvider := chainproviders.NewExecutionProvider(
		executionRepo.Waves, executionRepo.CaseRuns, executionRepo.SUTContexts)
	chainDeliveryProvider := chainproviders.NewDeliveryProvider(issueRepo, reportRepo, requirementSvc, closureSvc)
	chainResourceProvider := chainproviders.NewResourceProvider(versionSvc, resourceSvc)
	chainCursorProvider := chainproviders.NewEventCursorProvider(eventSvc)
	chainSvc := chainapp.NewChainService(
		chainVersionProvider, chainDesignProvider, chainExecutionProvider, chainDeliveryProvider,
		chainResourceProvider, chainCursorProvider, chainCursorProvider, clk)
	deliveryHandler := deliveryapi.New(issueSvc, deliveryapi.WithReports(reportSvc))
	chainHandler := chainapi.New(chainSvc)

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

		Requirements:  requirementSvc,
		DesignGraph:   designGraph,
		DesignExports: designExports,
		Execution:     executionHandler,
		Delivery:      deliveryHandler,
		Chain:         chainHandler,
		Closure:       closureSvc,
		VersionClose:  versionCloseSvc,
	})
	httpServer := httptest.NewServer(handler)
	t.Cleanup(httpServer.Close)
	return &testAPI{t: t, baseURL: httpServer.URL, pool: pool, reportSvc: reportSvc, issueSvc: issueSvc}
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
	if len(settings) != 83 {
		t.Fatalf("settings count = %d, want 83 (P7 72->83)", len(settings))
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

func TestP2DesignRoutesAndEmptyGraph(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", "admin123")

	sut := api.data(api.do(http.MethodPost, "/api/resources/suts", map[string]any{"name": "P2 Design SUT", "intro": "p2"}, http.StatusCreated))
	sutID := sut["id"].(string)
	environment := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/environments", map[string]any{"name": "dev", "base_url": "http://localhost:8080", "is_default": true}, http.StatusCreated))
	environmentID := environment["id"].(string)
	version := api.data(api.do(http.MethodPost, "/api/versions", map[string]any{
		"title": "P2 Design Version",
		"bindings": []any{map[string]any{
			"sut_id": sutID, "environment_id": environmentID,
			"page_ids": []string{}, "api_ids": []string{}, "database_ids": []string{}, "accounts": []any{},
		}},
	}, http.StatusCreated))
	versionID := version["id"].(string)

	graph := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/design/graph", nil, http.StatusOK))
	if graph["version_id"] != versionID {
		t.Fatalf("graph version_id = %#v", graph["version_id"])
	}
	if graph["pending_design_count"] != float64(0) {
		t.Fatalf("graph pending_design_count = %#v", graph["pending_design_count"])
	}
	if len(graph["requirements"].([]any)) != 0 {
		t.Fatalf("empty version graph requirements = %#v", graph["requirements"])
	}

	overview := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/design/overview", nil, http.StatusOK))
	if overview["active_round"] != nil || overview["round_count"] != float64(0) {
		t.Fatalf("overview = %#v", overview)
	}

	list := api.do(http.MethodGet, "/api/versions/"+versionID+"/exports", nil, http.StatusOK)
	if len(list["data"].([]any)) != 0 {
		t.Fatalf("initial export list = %#v", list)
	}

	export := api.data(api.do(http.MethodPost, "/api/versions/"+versionID+"/exports", map[string]any{
		"owner_kind": "case", "owner_id": versionID, "format": "markdown",
	}, http.StatusCreated))
	exportID := export["id"].(string)
	if export["version_id"] != versionID || export["format"] != "markdown" || export["sha256"] == "" {
		t.Fatalf("export view = %#v", export)
	}
	api.do(http.MethodGet, "/api/exports/"+exportID, nil, http.StatusOK)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/exports", nil, http.StatusOK)
	api.do(http.MethodPost, "/api/versions/"+versionID+"/exports", map[string]any{
		"owner_kind": "case", "owner_id": versionID, "format": "pdf",
	}, http.StatusBadRequest)

	api.token = ""
	api.do(http.MethodGet, "/api/versions/"+versionID+"/design/graph", nil, http.StatusUnauthorized)
	api.login("admin", "admin123")
}

// TestP3ExecutionRoutes pins the P3 routing/auth/empty-state contract (spec
// §9.2): 401 without a session, an empty but well-formed overview, empty list
// envelopes, 404 for unknown wave/evidence/human ids and admin-only executor
// management.
func TestP3ExecutionRoutes(t *testing.T) {
	api := newTestAPI(t)

	// Unauthenticated execution routes are 401.
	api.do(http.MethodGet, "/api/versions/1/execution/overview", nil, http.StatusUnauthorized)

	api.login("admin", "admin123")
	sut := api.data(api.do(http.MethodPost, "/api/resources/suts", map[string]any{"name": "P3 Execution SUT", "intro": "p3"}, http.StatusCreated))
	sutID := sut["id"].(string)
	environment := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/environments", map[string]any{"name": "dev", "base_url": "http://127.0.0.1:13335", "is_default": true}, http.StatusCreated))
	environmentID := environment["id"].(string)
	version := api.data(api.do(http.MethodPost, "/api/versions", map[string]any{
		"title": "P3 Execution Version",
		"bindings": []any{map[string]any{
			"sut_id": sutID, "environment_id": environmentID,
			"page_ids": []string{}, "api_ids": []string{}, "database_ids": []string{}, "accounts": []any{},
		}},
	}, http.StatusCreated))
	versionID := version["id"].(string)

	// Overview is readable in the empty state (no wave yet).
	overview := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/execution/overview", nil, http.StatusOK))
	if overview["version_id"] != versionID || overview["active_wave"] != nil {
		t.Fatalf("empty overview = %#v", overview)
	}
	if overview["counts"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("empty counts = %#v", overview["counts"])
	}
	if overview["case_run_counts"].(map[string]any)["total"] != float64(0) {
		t.Fatalf("empty case_run_counts = %#v", overview["case_run_counts"])
	}
	if overview["open_human_requests"] != float64(0) {
		t.Fatalf("open_human_requests = %#v", overview["open_human_requests"])
	}
	executor := overview["executor"].(map[string]any)
	if executor["status"] != "offline" || executor["endpoint"] != "http://127.0.0.1:13336" {
		t.Fatalf("executor = %#v", executor)
	}
	quotas := overview["quotas"].(map[string]any)
	if quotas["global"] != float64(3) || quotas["per_sut"] != float64(2) || quotas["cross_system"] != float64(1) {
		t.Fatalf("quotas = %#v", quotas)
	}
	if overview["human_request_timeout_seconds"] != float64(1800) {
		t.Fatalf("human_request_timeout_seconds = %#v", overview["human_request_timeout_seconds"])
	}

	// Empty list envelopes.
	waves := api.do(http.MethodGet, "/api/versions/"+versionID+"/waves", nil, http.StatusOK)
	if len(waves["data"].([]any)) != 0 || waves["has_more"] != false {
		t.Fatalf("waves = %#v", waves)
	}
	intents := api.do(http.MethodGet, "/api/versions/"+versionID+"/execution/intents", nil, http.StatusOK)
	if len(intents["data"].([]any)) != 0 || intents["has_more"] != false {
		t.Fatalf("intents = %#v", intents)
	}
	evidence := api.do(http.MethodGet, "/api/versions/"+versionID+"/evidence", nil, http.StatusOK)
	if len(evidence["data"].([]any)) != 0 {
		t.Fatalf("evidence = %#v", evidence)
	}
	humans := api.do(http.MethodGet, "/api/versions/"+versionID+"/human-requests?status=open", nil, http.StatusOK)
	if len(humans["data"].([]any)) != 0 {
		t.Fatalf("human requests = %#v", humans)
	}

	// Unknown entities are 404.
	api.do(http.MethodPost, "/api/versions/"+versionID+"/waves/999999999/pause", map[string]any{"mode": "graceful"}, http.StatusNotFound)
	api.do(http.MethodPost, "/api/versions/"+versionID+"/waves/999999999/resume", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/case-runs/999999999", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/evidence/999999999", nil, http.StatusNotFound)
	api.do(http.MethodGet, "/api/versions/"+versionID+"/evidence/999999999/content", nil, http.StatusNotFound)
	api.do(http.MethodPost, "/api/human-requests/999999999/resolve", map[string]any{"response": map[string]any{"code": "246810"}}, http.StatusNotFound)
	api.do(http.MethodPost, "/api/human-requests/999999999/cancel", nil, http.StatusNotFound)
	api.do(http.MethodPost, "/api/versions/"+versionID+"/execution/intents/999999999/cancel", nil, http.StatusNotFound)

	// Admin settings expose the P3 secret masked, never in clear text.
	token := api.data(api.do(http.MethodGet, "/api/admin/settings/execution.executor_token", nil, http.StatusOK))
	if token["is_secret"] != true || token["value"] != nil {
		t.Fatalf("executor token setting = %#v", token)
	}

	// Executor administration is admin-only.
	executors := api.do(http.MethodGet, "/api/admin/executors", nil, http.StatusOK)
	if len(executors["data"].([]any)) != 0 || executors["has_more"] != false {
		t.Fatalf("executors = %#v", executors)
	}
	api.do(http.MethodPost, "/api/admin/executors/999999999/drain", nil, http.StatusNotFound)
	api.do(http.MethodPost, "/api/admin/executors/999999999/activate", nil, http.StatusNotFound)

	api.data(api.do(http.MethodPost, "/api/admin/users", map[string]any{
		"username": "p3-user", "display_name": "P3 User", "password": "p3-user-pass",
	}, http.StatusCreated))
	api.login("p3-user", "p3-user-pass")
	api.do(http.MethodGet, "/api/admin/executors", nil, http.StatusForbidden)
	// Normal users can still read the execution overview.
	api.do(http.MethodGet, "/api/versions/"+versionID+"/execution/overview", nil, http.StatusOK)
	api.do(http.MethodGet, "/api/versions/1/execution/overview", nil, http.StatusNotFound)
}

// TestP3ExecutionReadAPIAndHumanResolve is the server-level happy path that does
// not need a live executor or LLM: a paused wave/case with a real content-ledger
// evidence blob and an open login takeover are read through the P3 API, the
// evidence bytes are downloaded and sha-verified, the human request is resolved
// and the wave is cancelled.
func TestP3ExecutionReadAPIAndHumanResolve(t *testing.T) {
	api := newTestAPI(t)
	api.login("admin", "admin123")

	sut := api.data(api.do(http.MethodPost, "/api/resources/suts", map[string]any{"name": "P3 Happy SUT", "intro": "p3"}, http.StatusCreated))
	sutID := sut["id"].(string)
	environment := api.data(api.do(http.MethodPost, "/api/resources/suts/"+sutID+"/environments", map[string]any{"name": "dev", "base_url": "http://127.0.0.1:13335", "is_default": true}, http.StatusCreated))
	environmentID := environment["id"].(string)
	version := api.data(api.do(http.MethodPost, "/api/versions", map[string]any{
		"title": "P3 Happy Version",
		"bindings": []any{map[string]any{
			"sut_id": sutID, "environment_id": environmentID,
			"page_ids": []string{}, "api_ids": []string{}, "database_ids": []string{}, "accounts": []any{},
		}},
	}, http.StatusCreated))
	versionID := version["id"].(string)
	versionIDValue, err := strconv.ParseInt(versionID, 10, 64)
	if err != nil {
		t.Fatalf("parse version id: %v", err)
	}

	// Upload the evidence bytes through the P0 content ledger.
	payload := []byte("p3-evidence-payload")
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	part, err := writer.CreateFormFile("file", "p3-shot.txt")
	if err != nil {
		t.Fatalf("multipart: %v", err)
	}
	_, _ = part.Write(payload)
	if err := writer.Close(); err != nil {
		t.Fatalf("multipart close: %v", err)
	}
	uploadReq, _ := http.NewRequest(http.MethodPost, api.baseURL+"/api/content", &buffer)
	uploadReq.Header.Set("Content-Type", writer.FormDataContentType())
	uploadReq.Header.Set("Authorization", "Bearer "+api.token)
	uploadResp, err := http.DefaultClient.Do(uploadReq)
	if err != nil {
		t.Fatalf("upload evidence: %v", err)
	}
	uploadRaw, _ := io.ReadAll(uploadResp.Body)
	uploadResp.Body.Close()
	if uploadResp.StatusCode != http.StatusCreated {
		t.Fatalf("upload evidence status = %d: %s", uploadResp.StatusCode, uploadRaw)
	}
	var upload struct {
		Data struct {
			SHA256 string `json:"sha256"`
			Size   int64  `json:"size"`
			MIME   string `json:"mime"`
		} `json:"data"`
	}
	if err := json.Unmarshal(uploadRaw, &upload); err != nil {
		t.Fatalf("decode upload: %v", err)
	}

	// Seed one paused wave/case with an open human takeover (executor/LLM-free).
	ctx := context.Background()
	seed := []struct {
		statement string
		args      []any
	}{
		{`INSERT INTO run_wave (id, version_id, status, trigger, counts, started_at, paused_at, pause_reason, pause_mode)
		  VALUES ($1, $2, 'paused', 'manual', $3::jsonb, now(), now(), 'waiting_human', 'graceful')`,
			[]any{int64(910001), versionIDValue, `{"total":1,"paused":1}`}},
		{`INSERT INTO execution_intent (id, version_id, case_version_id, version_sut_ids, environment_fingerprint, purpose, status, dedupe_key)
		  VALUES ($1, $2, $3, $4::jsonb, $5, 'manual', 'running', 'exec:happy')`,
			[]any{int64(920001), versionIDValue, int64(999001), `[1]`, strings.Repeat("a", 64)}},
		{`INSERT INTO case_run (id, wave_id, intent_id, case_version_id, status, attempts, summary, started_at, paused_at, pause_reason, dispatch_epoch)
		  VALUES ($1, $2, $3, $4, 'paused', 1, $5::jsonb, now(), now(), 'waiting_human', 2)`,
			[]any{int64(930001), int64(910001), int64(920001), int64(999001), `{"assertion_total":1,"assertion_failed":0,"step_total":1,"step_failed":0,"evidence_count":1}`}},
		{`INSERT INTO step_result (id, case_run_id, seq, target_version_sut_id, status, observed, error)
		  VALUES ($1, $2, 1, $3, 'passed', $4::jsonb, '')`,
			[]any{int64(940001), int64(930001), int64(1), `{"summary":"clicked login"}`}},
		{`INSERT INTO assertion_result (id, case_run_id, step_seq, assertion_seq, kind, verdict, reason, expected, actual, target_version_sut_id)
		  VALUES ($1, $2, 1, 1, 'ui', 'passed', '', 'home visible', 'home visible', $3)`,
			[]any{int64(950001), int64(930001), int64(1)}},
		{`INSERT INTO evidence (id, case_run_id, version_sut_id, step_seq, assertion_seq, kind, content_ref_id, sha256, size_bytes, mime, filename)
		  VALUES ($1, $2, $3, 1, NULL, 'screenshot', 555, $4, $5, $6, 'p3-shot.txt')`,
			[]any{int64(960001), int64(930001), int64(1), upload.Data.SHA256, upload.Data.Size, upload.Data.MIME}},
		{`INSERT INTO human_request (id, version_id, case_run_id, kind, payload, status, expires_at)
		  VALUES ($1, $2, $3, 'login_takeover', $4::jsonb, 'open', now() + interval '30 minutes')`,
			[]any{int64(970001), versionIDValue, int64(930001), `{"reason":"2FA code required","version_sut_id":"1","account_role":"admin","page_url":"http://127.0.0.1:13337/p3/2fa","challenge_kind":"totp"}`}},
	}
	for _, item := range seed {
		if _, err := api.pool.Exec(ctx, item.statement, item.args...); err != nil {
			t.Fatalf("seed execution row: %v\n%s", err, item.statement)
		}
	}

	// Overview projects the paused wave, open human and stored counts.
	overview := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/execution/overview", nil, http.StatusOK))
	activeWave := overview["active_wave"].(map[string]any)
	if activeWave["id"] != "910001" || activeWave["status"] != "paused" || activeWave["pause_reason"] != "waiting_human" {
		t.Fatalf("active wave = %#v", activeWave)
	}
	if overview["counts"].(map[string]any)["paused"] != float64(1) || overview["case_run_counts"].(map[string]any)["total"] != float64(1) {
		t.Fatalf("overview counts = %#v / %#v", overview["counts"], overview["case_run_counts"])
	}
	if overview["open_human_requests"] != float64(1) {
		t.Fatalf("open human requests = %#v", overview["open_human_requests"])
	}

	waves := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/waves", nil, http.StatusOK))
	if len(waves) != 1 || waves[0].(map[string]any)["status"] != "paused" {
		t.Fatalf("waves = %#v", waves)
	}
	wave := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/waves/910001", nil, http.StatusOK))
	if wave["status"] != "paused" || wave["trigger"] != "manual" {
		t.Fatalf("wave = %#v", wave)
	}

	runs := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/waves/910001/case-runs", nil, http.StatusOK))
	if len(runs) != 1 {
		t.Fatalf("case runs = %#v", runs)
	}
	runView := runs[0].(map[string]any)
	if runView["id"] != "930001" || runView["status"] != "paused" || runView["evidence_count"] != float64(1) {
		t.Fatalf("case run view = %#v", runView)
	}
	if suts := runView["version_sut_ids"].([]any); len(suts) != 1 || suts[0] != "1" {
		t.Fatalf("case run version_sut_ids = %#v", runView["version_sut_ids"])
	}

	detail := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/case-runs/930001", nil, http.StatusOK))
	if len(detail["steps"].([]any)) != 1 || len(detail["assertions"].([]any)) != 1 {
		t.Fatalf("case run detail = %#v", detail)
	}
	detailEvidence := detail["evidence"].([]any)
	if len(detailEvidence) != 1 || detailEvidence[0].(map[string]any)["sha256"] != upload.Data.SHA256 {
		t.Fatalf("detail evidence = %#v", detailEvidence)
	}

	evidenceList := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/evidence?kind=screenshot", nil, http.StatusOK))
	if len(evidenceList) != 1 {
		t.Fatalf("evidence list = %#v", evidenceList)
	}
	evidenceView := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/evidence/960001", nil, http.StatusOK))
	if evidenceView["sha256"] != upload.Data.SHA256 || evidenceView["filename"] != "p3-shot.txt" {
		t.Fatalf("evidence view = %#v", evidenceView)
	}
	if _, leaked := evidenceView["content_ref_id"]; leaked {
		t.Fatal("evidence view leaked content_ref_id")
	}

	// Authenticated download re-verifies sha256/size and never exposes a path.
	downloadReq, _ := http.NewRequest(http.MethodGet, api.baseURL+"/api/versions/"+versionID+"/evidence/960001/content", nil)
	downloadReq.Header.Set("Authorization", "Bearer "+api.token)
	downloadResp, err := http.DefaultClient.Do(downloadReq)
	if err != nil {
		t.Fatalf("download evidence: %v", err)
	}
	downloaded, _ := io.ReadAll(downloadResp.Body)
	downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK || !bytes.Equal(downloaded, payload) {
		t.Fatalf("download status=%d body=%q", downloadResp.StatusCode, downloaded)
	}
	if got := downloadResp.Header.Get("X-Content-SHA256"); got != upload.Data.SHA256 {
		t.Fatalf("download sha header = %q", got)
	}
	if disposition := downloadResp.Header.Get("Content-Disposition"); !strings.Contains(disposition, "p3-shot.txt") {
		t.Fatalf("content disposition = %q", disposition)
	}

	// Evidence of one version is not downloadable through another version.
	otherVersion := api.data(api.do(http.MethodPost, "/api/versions", map[string]any{
		"title": "P3 Other Version",
		"bindings": []any{map[string]any{
			"sut_id": sutID, "environment_id": environmentID,
			"page_ids": []string{}, "api_ids": []string{}, "database_ids": []string{}, "accounts": []any{},
		}},
	}, http.StatusCreated))
	api.do(http.MethodGet, "/api/versions/"+otherVersion["id"].(string)+"/evidence/960001/content", nil, http.StatusNotFound)

	// Human takeover: list open, resolve once, never echo the one-time code.
	humanList := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/human-requests?status=open", nil, http.StatusOK))
	if len(humanList) != 1 {
		t.Fatalf("human requests = %#v", humanList)
	}
	resolved := api.data(api.do(http.MethodPost, "/api/human-requests/970001/resolve", map[string]any{
		"response": map[string]any{"code": "246810", "note": "code entered"},
	}, http.StatusOK))
	if resolved["status"] != "resolved" {
		t.Fatalf("resolved human request = %#v", resolved)
	}
	response, _ := resolved["response"].(map[string]any)
	if response == nil || response["note"] != "code entered" {
		t.Fatalf("human response = %#v", resolved["response"])
	}
	if _, leaked := response["code"]; leaked {
		t.Fatal("human request response leaked the one-time code")
	}
	// Resolving the same request again is a state conflict.
	api.do(http.MethodPost, "/api/human-requests/970001/resolve", map[string]any{
		"response": map[string]any{"note": "again"},
	}, http.StatusConflict)

	// Resolve resumed the case_run (pending) and the wave (running).
	runAfterResolve := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/case-runs/930001", nil, http.StatusOK))
	if status := runAfterResolve["case_run"].(map[string]any)["status"]; status != "pending" {
		t.Fatalf("case run after resolve = %#v", status)
	}
	waveAfterResolve := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/waves/910001", nil, http.StatusOK))
	if waveAfterResolve["status"] != "running" {
		t.Fatalf("wave after resolve = %#v", waveAfterResolve["status"])
	}

	// Cancel the wave: non-terminal case runs become superseded, intents cancelled.
	cancelled := api.data(api.do(http.MethodPost, "/api/versions/"+versionID+"/waves/910001/cancel", nil, http.StatusOK))
	if cancelled["status"] != "cancelled" {
		t.Fatalf("cancelled wave = %#v", cancelled)
	}
	runCancelled := api.data(api.do(http.MethodGet, "/api/versions/"+versionID+"/case-runs/930001", nil, http.StatusOK))
	if status := runCancelled["case_run"].(map[string]any)["status"]; status != "superseded" {
		t.Fatalf("case run after cancel = %#v", status)
	}
	intents := api.listData(api.do(http.MethodGet, "/api/versions/"+versionID+"/execution/intents", nil, http.StatusOK))
	if len(intents) != 1 || intents[0].(map[string]any)["status"] != "cancelled" {
		t.Fatalf("intents after cancel = %#v", intents)
	}
}
