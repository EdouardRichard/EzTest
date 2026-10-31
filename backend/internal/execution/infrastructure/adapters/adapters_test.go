package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorinfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// --- version service fake --------------------------------------------------

type fakeVersionService struct {
	view versionapp.VersionView
	err  error
}

func (f *fakeVersionService) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

func (f *fakeVersionService) Reopen(context.Context, int64) error { return f.err }

func resourceSUTView() resourceapp.SUTView {
	return resourceapp.SUTView{ID: "3", Name: "orders"}
}

func resourceEnvironmentView() resourceapp.EnvironmentView {
	return resourceapp.EnvironmentView{ID: "4", SUTID: "3", Name: "test", BaseURL: "http://127.0.0.1:13335"}
}

func resourceRepositoryView() resourceapp.CodeRepositoryView {
	return resourceapp.CodeRepositoryView{ID: "5", Name: "orders-repo", Branch: "main"}
}

func versionViewFixture() versionapp.VersionView {
	repository := resourceRepositoryView()
	return versionapp.VersionView{
		ID: 10, Status: "running",
		Bindings: []versionapp.BindingView{{
			ID:          7,
			SUT:         resourceSUTView(),
			Environment: resourceEnvironmentView(),
			Repository:  &repository,
			Pages:       []resourceapp.PageView{{ID: "71", URL: "http://127.0.0.1:13335/login", Name: "登录页"}},
			APIs:        []resourceapp.APIView{{ID: "72", Method: "GET", PathTemplate: "/api/orders"}},
			Databases:   []resourceapp.DatabaseView{{ID: "81", Status: "active", ReadOnly: true}},
			Accounts: []resourceapp.AccountView{
				{ID: "91", Username: "alice", Roles: []string{"admin"}},
				{ID: "92", Username: "bob", Roles: []string{"viewer"}},
			},
		}},
	}
}

func TestVersionAdapterMapsFingerprintAndTaskBindings(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: versionViewFixture()})
	ctx := context.Background()

	binding, err := adapter.Get(ctx, 10)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if binding.VersionID != 10 || binding.Status != "running" || len(binding.SUTs) != 1 {
		t.Fatalf("binding = %+v", binding)
	}
	sut := binding.SUTs[0]
	if sut.VersionSUTID != 7 || sut.SUTID != 3 || sut.EnvironmentID != 4 ||
		sut.BaseURL != "http://127.0.0.1:13335" ||
		len(sut.PageIDs) != 1 || sut.PageIDs[0] != 71 ||
		len(sut.APIIDs) != 1 || sut.APIIDs[0] != 72 ||
		len(sut.DatabaseIDs) != 1 || sut.DatabaseIDs[0] != 81 ||
		sut.CodeRepoID == nil || *sut.CodeRepoID != 5 || sut.Branch != "main" ||
		len(sut.AccountRoles) != 2 {
		t.Fatalf("version_sut binding = %+v", sut)
	}

	taskBindings, err := adapter.Resolve(ctx, 10)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(taskBindings) != 1 {
		t.Fatalf("taskBindings = %+v", taskBindings)
	}
	taskBinding := taskBindings[0]
	if taskBinding.SUTName != "orders" || taskBinding.EnvironmentName != "test" ||
		len(taskBinding.PageURLs) != 1 || taskBinding.PageURLs[0] != "http://127.0.0.1:13335/login" ||
		len(taskBinding.APIEndpoints) != 1 || taskBinding.APIEndpoints[0] != "GET /api/orders" {
		t.Fatalf("task binding = %+v", taskBinding)
	}

	api, err := adapter.API(ctx, 10, 7, 72)
	if err != nil {
		t.Fatalf("API: %v", err)
	}
	if api.Method != "GET" || api.Path != "/api/orders" {
		t.Fatalf("api = %+v", api)
	}
	if _, err := adapter.API(ctx, 10, 7, 999); err == nil {
		t.Fatalf("expected not_found for an unknown api_id")
	}
}

// --- credential adapter fake ----------------------------------------------

type fakeAccountSecrets struct {
	username, password string
	err                error
	calls              []int64
}

func (f *fakeAccountSecrets) GetAccountSecret(_ context.Context, accountID int64) (string, string, error) {
	f.calls = append(f.calls, accountID)
	return f.username, f.password, f.err
}

type fakeDatabaseSecrets struct {
	dsn, username, password string
	err                     error
	calls                   []int64
}

func (f *fakeDatabaseSecrets) GetDatabaseSecret(_ context.Context, databaseID int64) (string, string, string, error) {
	f.calls = append(f.calls, databaseID)
	return f.dsn, f.username, f.password, f.err
}

func TestCredentialsRoleResolution(t *testing.T) {
	accounts := &fakeAccountSecrets{username: "alice", password: "pw"}
	databases := &fakeDatabaseSecrets{dsn: "postgres://db", username: "db", password: "dbpw"}
	adapter := NewCredentialAdapter(&fakeVersionService{view: versionViewFixture()}, accounts, databases)
	ctx := context.Background()

	credential, err := adapter.Account(ctx, 10, 7, "admin")
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if credential.AccountID != 91 || credential.Username != "alice" || credential.Password != "pw" {
		t.Fatalf("credential = %+v", credential)
	}
	if len(accounts.calls) != 1 || accounts.calls[0] != 91 {
		t.Fatalf("account secret calls = %v", accounts.calls)
	}

	// Empty role picks the first bound account.
	if _, err := adapter.Account(ctx, 10, 7, ""); err != nil {
		t.Fatalf("Account(empty role): %v", err)
	}
	if _, err := adapter.Account(ctx, 10, 7, "nobody"); err == nil {
		t.Fatalf("expected not_found for an unknown role")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			t.Fatalf("error = %+v", err)
		}
	}

	database, err := adapter.Database(ctx, 10, 7, nil)
	if err != nil {
		t.Fatalf("Database: %v", err)
	}
	if database.DatabaseID != 81 || database.DSN != "postgres://db" || database.Username != "db" ||
		database.Password != "dbpw" || !database.ReadOnly {
		t.Fatalf("database = %+v", database)
	}
	explicit := int64(81)
	if _, err := adapter.Database(ctx, 10, 7, &explicit); err != nil {
		t.Fatalf("Database(explicit): %v", err)
	}
	unknown := int64(999)
	if _, err := adapter.Database(ctx, 10, 7, &unknown); err == nil {
		t.Fatalf("expected not_found for an unknown database")
	}
}

func TestCredentialsRejectDuplicateRole(t *testing.T) {
	view := versionViewFixture()
	view.Bindings[0].Accounts = append(view.Bindings[0].Accounts,
		resourceapp.AccountView{ID: "93", Username: "carol", Roles: []string{"admin"}})
	adapter := NewCredentialAdapter(&fakeVersionService{view: view},
		&fakeAccountSecrets{username: "u", password: "p"}, &fakeDatabaseSecrets{})
	if _, err := adapter.Account(context.Background(), 10, 7, "admin"); err == nil {
		t.Fatalf("expected a state_conflict for a duplicate role")
	}
}

// --- content store fake ----------------------------------------------------

type fakeContentService struct {
	objects    map[string]storage.Object
	refs       []storage.Ref
	nextRef    int64
	uploaded   []byte
	uploadKind string
}

func (f *fakeContentService) Upload(_ context.Context, _ string, mime string, r io.Reader, _ int64, _ int64) (storage.Object, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return storage.Object{}, err
	}
	f.uploaded = data
	object := storage.Object{SHA256: "sha-256-" + strings.Repeat("a", 56), Size: int64(len(data)), MIME: mime}
	if f.objects == nil {
		f.objects = map[string]storage.Object{}
	}
	f.objects[object.SHA256] = object
	return object, nil
}

func (f *fakeContentService) AddRef(_ context.Context, objectID, ownerKind string, ownerID int64, role string) (storage.Ref, error) {
	f.nextRef++
	ref := storage.Ref{ID: f.nextRef, ObjectID: objectID, OwnerKind: ownerKind, OwnerID: ownerID, Role: role}
	f.refs = append(f.refs, ref)
	return ref, nil
}

func (f *fakeContentService) Open(context.Context, string) (storage.Object, io.ReadCloser, error) {
	return storage.Object{}, io.NopCloser(bytes.NewReader([]byte("hello"))), nil
}

func TestContentStorePutEvidence(t *testing.T) {
	service := &fakeContentService{}
	store := NewContentStore(service)
	evidenceID := int64(5001)
	ref, err := store.PutEvidence(context.Background(), evidenceID, execution.EvidenceScreenshot, "shot.png", []byte("png-bytes"))
	if err != nil {
		t.Fatalf("PutEvidence: %v", err)
	}
	if ref.ID != 1 || ref.SizeBytes != 9 || ref.MIME != execution.EvidenceScreenshot.MIME() {
		t.Fatalf("content ref = %+v", ref)
	}
	if string(service.uploaded) != "png-bytes" {
		t.Fatalf("uploaded = %q", service.uploaded)
	}
	if len(service.refs) != 1 || service.refs[0].OwnerKind != "evidence" ||
		service.refs[0].OwnerID != evidenceID || service.refs[0].Role != "screenshot" {
		t.Fatalf("refs = %+v", service.refs)
	}
	data, err := store.Open(context.Background(), ref.SHA256)
	if err != nil || string(data) != "hello" {
		t.Fatalf("Open = %q, %v", data, err)
	}
}

// --- executor adapter fake -------------------------------------------------

type fakeExecutorClient struct {
	health          executorinfra.Health
	healthErr       error
	created         executorinfra.CreateContextInput
	action          executionapp.BrowserActionResult
	actionErr       error
	actionName      string
	cancelErr       error
	closeErr        error
	contexts        []executorinfra.ContextInfo
	listContextsErr error
}

func (f *fakeExecutorClient) Health(context.Context) (executorinfra.Health, error) {
	return f.health, f.healthErr
}

func (f *fakeExecutorClient) CreateContext(_ context.Context, in executorinfra.CreateContextInput) (executorinfra.ContextInfo, error) {
	f.created = in
	return executorinfra.ContextInfo{ID: "ctx_1", Key: in.Key, Metadata: in.Metadata}, nil
}

func (f *fakeExecutorClient) Action(_ context.Context, _, action string, _ json.RawMessage) (executorinfra.ActionResult, error) {
	f.actionName = action
	return executorinfra.ActionResult{
		OK:     f.action.OK,
		Result: f.action.Result,
		Artifacts: []executorinfra.Artifact{{
			Kind: "screenshot", MIME: "image/png", Base64: "AAAA", SHA256: "abc", Bytes: 3, Filename: "shot.png",
		}},
	}, f.actionErr
}

func (f *fakeExecutorClient) Cancel(context.Context, string) (bool, error) {
	return true, f.cancelErr
}

func (f *fakeExecutorClient) Close(context.Context, string) error { return f.closeErr }

func (f *fakeExecutorClient) ListContexts(context.Context) ([]executorinfra.ContextInfo, error) {
	return f.contexts, f.listContextsErr
}

func TestExecutorAdapters(t *testing.T) {
	client := &fakeExecutorClient{
		health:   executorinfra.Health{Status: "ok", Version: "0.2.0-p3", Contexts: 1, MaxContexts: 8, BrowserVersion: "Chromium"},
		action:   executionapp.BrowserActionResult{OK: true, Result: json.RawMessage(`{"status":200}`)},
		contexts: []executorinfra.ContextInfo{{ID: "ctx_1", Key: "run:1"}},
	}
	browser := NewBrowserAdapter(client)
	contextID, err := browser.CreateContext(context.Background(), executionapp.BrowserContextSpec{
		Key: "run:1", AllowedHosts: []string{"127.0.0.1"}, BaseURL: "http://127.0.0.1:13335",
		Metadata: map[string]any{"case_run_id": "1"},
	})
	if err != nil || contextID != "ctx_1" {
		t.Fatalf("CreateContext = %q, %v", contextID, err)
	}
	if client.created.Key != "run:1" || client.created.BaseURL != "http://127.0.0.1:13335" ||
		client.created.Metadata["case_run_id"] != "1" {
		t.Fatalf("create input = %+v", client.created)
	}
	result, err := browser.Action(context.Background(), "ctx_1", "screenshot", json.RawMessage(`{}`))
	if err != nil || !result.OK || client.actionName != "screenshot" {
		t.Fatalf("Action = %+v, %v", result, err)
	}
	if len(result.Artifacts) != 1 || result.Artifacts[0].Filename != "shot.png" {
		t.Fatalf("artifacts = %+v", result.Artifacts)
	}

	client.cancelErr = platform.NewError(404, "context_not_found", "unknown context")
	if err := browser.Cancel(context.Background(), "ctx_gone"); err != nil {
		t.Fatalf("Cancel of a missing context must be a no-op: %v", err)
	}
	client.closeErr = platform.NewError(404, "context_not_found", "unknown context")
	if err := browser.Close(context.Background(), "ctx_gone"); err != nil {
		t.Fatalf("Close of a missing context must be a no-op: %v", err)
	}

	probe := NewExecutorProbe(client)
	health, err := probe.Health(context.Background())
	if err != nil || health.Status != "ok" || health.MaxContexts != 8 {
		t.Fatalf("Health = %+v, %v", health, err)
	}
	control := NewExecutorControl(client)
	contexts, err := control.ListContexts(context.Background())
	if err != nil || len(contexts) != 1 || contexts[0].Key != "run:1" {
		t.Fatalf("ListContexts = %+v, %v", contexts, err)
	}
}

// --- task provider mapping -------------------------------------------------

func TestTaskProviderMapsSnapshot(t *testing.T) {
	snapshot := execution.TaskSnapshot{
		Case: execution.CaseSnapshot{
			CaseVersionID: 41, Title: "登录流程", Description: "desc", Preconditions: "cond", Priority: "P0",
			Steps: []execution.CaseStep{{
				Seq: 1, Action: "打开登录页", Expected: "出现表单", TargetVersionSUTID: 7,
				AccountRole: "admin", TargetPageID: int64ptrAdapter(71),
			}},
			Assertions: []execution.CaseAssertion{{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "按钮", TargetVersionSUTID: 7}},
		},
		CaseRun: execution.CaseRun{ID: 3001, WaveID: 2001, IntentID: 1001},
		SUTs: []execution.SUTBinding{{
			VersionSUTID: 7, SUTName: "orders", EnvironmentName: "test", BaseURL: "http://127.0.0.1:13335",
			PageURLs: []string{"http://127.0.0.1:13335/login"}, AccountRoles: []string{"admin"},
		}},
		PriorSteps: []execution.StepResult{{
			Seq: 1, Status: execution.StepPassed, Observed: map[string]any{"summary": "表单可见"},
			TargetVersionSUTID: int64ptrAdapter(7),
		}},
		PriorAssertions: []execution.AssertionResult{{
			StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: execution.AssertionPassed,
			Expected: "按钮", Actual: "按钮", TargetVersionSUTID: int64ptrAdapter(7),
		}},
		HumanResponse: "验证码 123456",
	}
	task := mapTaskSnapshot(snapshot)
	if task.CaseRunID != 3001 || task.WaveID != 2001 || task.IntentID != 1001 || task.CaseVersionID != 41 {
		t.Fatalf("task IDs = %+v", task)
	}
	if task.CaseTitle != "登录流程" || task.HumanResponse != "验证码 123456" {
		t.Fatalf("task = %+v", task)
	}
	if len(task.Steps) != 1 || task.Steps[0].Action != "打开登录页" || *task.Steps[0].PageID != 71 {
		t.Fatalf("steps = %+v", task.Steps)
	}
	if len(task.PriorSteps) != 1 || task.PriorSteps[0].Status != "passed" || task.PriorSteps[0].Observed != "表单可见" {
		t.Fatalf("prior steps = %+v", task.PriorSteps)
	}
	if len(task.PriorAssertions) != 1 || task.PriorAssertions[0].Verdict != "passed" {
		t.Fatalf("prior assertions = %+v", task.PriorAssertions)
	}
	if _, ok := any(task).(agentapp.ExecutionTask); !ok {
		t.Fatalf("task is not an agentapp.ExecutionTask")
	}
}

func int64ptrAdapter(value int64) *int64 { return &value }

// silence unused import if a future test drops it
var _ = strings.TrimSpace
