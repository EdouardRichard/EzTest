package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

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

func resourceSUTView() resourceapp.SUTView {
	// v0.2.4-B: base_url moved down from the environment to the SUT.
	return resourceapp.SUTView{
		ID: "3", EnvironmentID: "4", Name: "orders", BaseURL: "http://127.0.0.1:13335",
	}
}

func resourceEnvironmentView() resourceapp.EnvironmentView {
	return resourceapp.EnvironmentView{ID: "4", Name: "test", Status: "active"}
}

func resourceRepositoryView() resourceapp.CodeRepositoryView {
	return resourceapp.CodeRepositoryView{ID: "5", Name: "orders-repo", Branch: "main"}
}

const repositoryCommit = "0123456789abcdef0123456789abcdef01234567"

func versionViewFixture() versionapp.VersionView {
	repository := resourceRepositoryView()
	sut := resourceSUTView()
	// Statuses are set the way the resource catalog persists them: the adapters
	// read the EFFECTIVE scope, so a fixture row without a status would be treated
	// as not approved and silently drop out of the binding.
	sut.Pages = []resourceapp.PageView{{ID: "71", SUTID: "3", URL: "http://127.0.0.1:13335/login", Name: "登录页", Status: "approved"}}
	sut.APIs = []resourceapp.APIView{{ID: "72", SUTID: "3", Method: "GET", PathTemplate: "/api/orders", Status: "approved"}}
	sut.Databases = []resourceapp.DatabaseView{{ID: "81", SUTID: "3", Status: "active", ReadOnly: true}}
	environment := resourceEnvironmentView()
	environment.SUTs = []resourceapp.SUTView{sut}
	return versionapp.VersionView{
		ID: 10, Status: "running", EnvironmentID: 4,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID:     10,
			EnvironmentID: 4,
			Environment:   environment,
			Pages:         sut.Pages,
			APIs:          sut.APIs,
			Databases:     sut.Databases,
			Accounts: []resourceapp.AccountView{
				{ID: "91", Username: "alice", Roles: []string{"admin"}},
				{ID: "92", Username: "bob", Roles: []string{"viewer"}},
			},
			Repositories: []versionapp.BindingRepository{{
				Repository: repository, SUTID: 3, Role: "", CodeBaselineCommit: repositoryCommit,
			}},
			CodeBaselineCommit: repositoryCommit,
		},
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
	// v0.2.4-B: one SUT key. This used to assert the binding-row id and the SUT
	// id separately; the adapter filled both with the SUT id and the binding row
	// is gone, so a single check is the whole contract now.
	if sut.SUTID != 3 || sut.EnvironmentID != 4 ||
		sut.BaseURL != "http://127.0.0.1:13335" ||
		len(sut.PageIDs) != 1 || sut.PageIDs[0] != 71 ||
		len(sut.APIIDs) != 1 || sut.APIIDs[0] != 72 ||
		len(sut.DatabaseIDs) != 1 || sut.DatabaseIDs[0] != 81 ||
		sut.CodeRepoID == nil || *sut.CodeRepoID != 5 ||
		len(sut.RepositoryBaselines) != 1 || sut.RepositoryBaselines[0].RepoID != 5 ||
		sut.RepositoryBaselines[0].Commit != repositoryCommit ||
		len(sut.AccountRoles) != 2 {
		t.Fatalf("sut binding = %+v", sut)
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

	api, err := adapter.API(ctx, 10, 3, 72)
	if err != nil {
		t.Fatalf("API: %v", err)
	}
	if api.Method != "GET" || api.Path != "/api/orders" {
		t.Fatalf("api = %+v", api)
	}
	if _, err := adapter.API(ctx, 10, 3, 999); err == nil {
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

	credential, err := adapter.Account(ctx, 10, 3, "admin")
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
	if _, err := adapter.Account(ctx, 10, 3, ""); err != nil {
		t.Fatalf("Account(empty role): %v", err)
	}
	if _, err := adapter.Account(ctx, 10, 3, "nobody"); err == nil {
		t.Fatalf("expected not_found for an unknown role")
	} else {
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != 404 {
			t.Fatalf("error = %+v", err)
		}
	}

	database, err := adapter.Database(ctx, 10, 3, nil)
	if err != nil {
		t.Fatalf("Database: %v", err)
	}
	if database.DatabaseID != 81 || database.DSN != "postgres://db" || database.Username != "db" ||
		database.Password != "dbpw" || !database.ReadOnly {
		t.Fatalf("database = %+v", database)
	}
	explicit := int64(81)
	if _, err := adapter.Database(ctx, 10, 3, &explicit); err != nil {
		t.Fatalf("Database(explicit): %v", err)
	}
	unknown := int64(999)
	if _, err := adapter.Database(ctx, 10, 3, &unknown); err == nil {
		t.Fatalf("expected not_found for an unknown database")
	}
}

func TestCredentialsResolveDuplicateRoleDeterministic(t *testing.T) {
	view := versionViewFixture()
	view.Binding.Accounts = append(view.Binding.Accounts,
		resourceapp.AccountView{ID: "93", Username: "carol", Roles: []string{"admin"}})
	adapter := NewCredentialAdapter(&fakeVersionService{view: view},
		&fakeAccountSecrets{username: "u", password: "p"}, &fakeDatabaseSecrets{})
	credential, err := adapter.Account(context.Background(), 10, 3, "admin")
	if err != nil {
		t.Fatalf("duplicate role must resolve deterministically: %v", err)
	}
	if credential.AccountID != 91 {
		t.Fatalf("account ID = %d, want the lowest bound admin account 91", credential.AccountID)
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
	health         executorinfra.Health
	healthErr      error
	created        executorinfra.CreateSessionInput
	networkLog     json.RawMessage
	networkLogErr  error
	networkLogTail int
	cancelErr      error
	closeErr       error
	sessions       []executorinfra.SessionInfo
	listErr        error
	execResult     executorinfra.ExecResult
	execErr        error
	execInput      executorinfra.ExecInput
	artifactPage   executorinfra.ArtifactPage
	artifactSince  int64
	artifactsErr   error
	traceRef       executorinfra.TraceRef
	leaseEpochs    []int64
	shellReq       executorinfra.ShellRequest
	shellRes       executorinfra.ShellResult
	fileReq        executorinfra.FileRequest
	fileRes        executorinfra.FileResult
}

func (f *fakeExecutorClient) Health(context.Context) (executorinfra.Health, error) {
	return f.health, f.healthErr
}

func (f *fakeExecutorClient) CreateSession(_ context.Context, in executorinfra.CreateSessionInput) (executorinfra.SessionInfo, error) {
	f.created = in
	if in.Lease != nil {
		f.leaseEpochs = append(f.leaseEpochs, in.Lease.Epoch)
	}
	return executorinfra.SessionInfo{ID: "sess_1", Key: in.Key, Metadata: in.Metadata}, nil
}

func (f *fakeExecutorClient) GetSession(_ context.Context, id string) (executorinfra.SessionInfo, error) {
	return executorinfra.SessionInfo{ID: id}, nil
}

func (f *fakeExecutorClient) Exec(_ context.Context, _ string, in executorinfra.ExecInput) (executorinfra.ExecResult, error) {
	f.execInput = in
	f.leaseEpochs = append(f.leaseEpochs, in.LeaseEpoch)
	return f.execResult, f.execErr
}

func (f *fakeExecutorClient) Shell(_ context.Context, _ string, in executorinfra.ShellRequest) (executorinfra.ShellResult, error) {
	f.shellReq = in
	f.leaseEpochs = append(f.leaseEpochs, in.LeaseEpoch)
	if f.shellRes.ShellID == "" {
		return executorinfra.ShellResult{ShellID: "sh_1"}, nil
	}
	return f.shellRes, nil
}

func (f *fakeExecutorClient) Files(_ context.Context, _ string, in executorinfra.FileRequest) (executorinfra.FileResult, error) {
	f.fileReq = in
	f.leaseEpochs = append(f.leaseEpochs, in.LeaseEpoch)
	if f.fileRes.Path == "" && len(f.fileRes.Files) == 0 {
		return executorinfra.FileResult{Path: "in.txt"}, nil
	}
	return f.fileRes, nil
}

func (f *fakeExecutorClient) Artifacts(_ context.Context, _ string, since int64) (executorinfra.ArtifactPage, error) {
	f.artifactSince = since
	return f.artifactPage, f.artifactsErr
}

func (f *fakeExecutorClient) Trace(context.Context, string) (executorinfra.TraceRef, error) {
	return f.traceRef, nil
}

func (f *fakeExecutorClient) RenewLease(_ context.Context, id string, epoch int64) (executorinfra.LeaseInfo, error) {
	return executorinfra.LeaseInfo{SessionID: id, LeaseEpoch: epoch}, nil
}

func (f *fakeExecutorClient) NetworkLog(_ context.Context, _ string, tail int) (json.RawMessage, error) {
	f.networkLogTail = tail
	return f.networkLog, f.networkLogErr
}

func (f *fakeExecutorClient) Cancel(context.Context, string) (bool, error) {
	return true, f.cancelErr
}

func (f *fakeExecutorClient) CloseSession(context.Context, string) error { return f.closeErr }

func (f *fakeExecutorClient) ListSessions(context.Context) ([]executorinfra.SessionInfo, error) {
	return f.sessions, f.listErr
}

func TestExecutorAdapters(t *testing.T) {
	exit := 0
	client := &fakeExecutorClient{
		health:     executorinfra.Health{Status: "ok", Version: "2.0.0", Sessions: 1, MaxSessions: 8},
		networkLog: json.RawMessage(`{"recording":true,"degraded":false,"items":[{"method":"GET","url":"http://127.0.0.1:13335/home","status":200,"resource_type":"document"}]}`),
		sessions:   []executorinfra.SessionInfo{{ID: "sess_1", Key: "run:1"}},
		execResult: executorinfra.ExecResult{CommandID: "cmd_1", Exit: &exit, Outcome: "exited", Stdout: "hello"},
		artifactPage: executorinfra.ArtifactPage{
			Artifacts: []executorinfra.Artifact{{
				Kind: "command", CommandID: "cmd_1", Filename: "command-cmd_1.json", Base64: "e30=",
			}},
			NextCursor: 1,
		},
		traceRef: executorinfra.TraceRef{Available: true, Path: "trace.zip"},
	}
	adapter := NewExecutionAdapter(client)
	sessionID, err := adapter.CreateSession(context.Background(), executionapp.SessionSpec{
		Key: "run:1", Workspace: "/data/workspaces/run-1", BaseURL: "http://127.0.0.1:13335",
		Metadata: map[string]any{"case_run_id": "1"}, LeaseEpoch: 3, LeaseTTL: 300 * time.Second,
	})
	if err != nil || sessionID != "sess_1" {
		t.Fatalf("CreateSession = %q, %v", sessionID, err)
	}
	if client.created.Key != "run:1" || client.created.BaseURL != "http://127.0.0.1:13335" ||
		client.created.Workspace != "/data/workspaces/run-1" ||
		client.created.Metadata["case_run_id"] != "1" {
		t.Fatalf("create input = %+v", client.created)
	}
	if client.created.Lease == nil || client.created.Lease.Epoch != 3 || client.created.Lease.TTL != 300*time.Second {
		t.Fatalf("create lease = %+v", client.created.Lease)
	}

	outcome, err := adapter.Exec(context.Background(), 0, "sess_1", executionapp.ExecCommand{Argv: []string{"echo hello"}}, 3)
	if err != nil || outcome.CommandID != "cmd_1" || outcome.Stdout != "hello" || outcome.SessionID != "sess_1" {
		t.Fatalf("Exec = %+v, %v", outcome, err)
	}
	if client.execInput.LeaseEpoch != 3 {
		t.Fatalf("exec lease epoch = %d, want 3", client.execInput.LeaseEpoch)
	}

	page, err := adapter.Artifacts(context.Background(), 0, "sess_1", 7)
	if err != nil || page.NextCursor != 1 || len(page.Artifacts) != 1 || page.Artifacts[0].Kind != "command" {
		t.Fatalf("Artifacts = %+v, %v", page, err)
	}
	if client.artifactSince != 7 {
		t.Fatalf("artifacts cursor = %d, want 7", client.artifactSince)
	}
	ref, err := adapter.Trace(context.Background(), 0, "sess_1")
	if err != nil || !ref.Available || ref.Path != "trace.zip" {
		t.Fatalf("Trace = %+v, %v", ref, err)
	}

	payload, err := adapter.NetworkLog(context.Background(), 0, "sess_1", 200)
	if err != nil || client.networkLogTail != 200 {
		t.Fatalf("NetworkLog = %s, %v (tail %d)", payload, err, client.networkLogTail)
	}
	if !strings.Contains(string(payload), `"resource_type":"document"`) {
		t.Fatalf("network log payload = %s", payload)
	}
	for _, epoch := range client.leaseEpochs {
		if epoch != 3 {
			t.Fatalf("a write omitted the lease epoch: %v", client.leaseEpochs)
		}
	}

	client.cancelErr = platform.NewError(404, "session_not_found", "unknown session")
	if err := adapter.Cancel(context.Background(), 0, "sess_gone"); err != nil {
		t.Fatalf("Cancel of a missing session must be a no-op: %v", err)
	}
	client.closeErr = platform.NewError(404, "session_not_found", "unknown session")
	if err := adapter.Close(context.Background(), 0, "sess_gone"); err != nil {
		t.Fatalf("Close of a missing session must be a no-op: %v", err)
	}

	probe := NewExecutorProbe(client)
	health, err := probe.Health(context.Background())
	if err != nil || health.Status != "ok" || health.MaxSessions != 8 || health.Sessions != 1 {
		t.Fatalf("Health = %+v, %v", health, err)
	}
	control := NewExecutorControl(client)
	sessions, err := control.ListSessions(context.Background())
	if err != nil || len(sessions) != 1 || sessions[0].Key != "run:1" {
		t.Fatalf("ListSessions = %+v, %v", sessions, err)
	}
}

// TestExecutionAdapterShellFilesPassthrough pins the v2 shell/PTY and workspace
// files mapping: one port call maps one protocol operation, every field passes
// through verbatim, and every write carries the session lease epoch.
func TestExecutionAdapterShellFilesPassthrough(t *testing.T) {
	exit := 0
	client := &fakeExecutorClient{
		shellRes: executorinfra.ShellResult{
			ShellID: "sh_9", PID: 4242, Status: "exited", Data: "out", NextCursor: 12,
			TotalBytes: 3, Truncated: true, Exit: &exit, Outcome: "exited", OK: true,
		},
		fileRes: executorinfra.FileResult{
			Path: "sub/a.txt", Bytes: 3, SHA256: "abc", MIME: "text/plain", Base64: "YWJj",
			Files:     []executorinfra.FileEntry{{Path: "a.txt", Bytes: 3, MIME: "text/plain"}, {Path: "sub", Dir: true}},
			Truncated: true,
		},
	}
	adapter := NewExecutionAdapter(client)
	shell, err := adapter.Shell(context.Background(), 0, "sess_1", executionapp.ShellOperation{
		Op: executorinfra.ShellCreate, Argv: []string{"psql"}, Cols: 120, Rows: 40,
		Env: map[string]string{"TERM": "xterm"}, Data: "select 1;\n", Since: 7, TimeoutMS: 2000,
	}, 3)
	if err != nil {
		t.Fatalf("Shell: %v", err)
	}
	if shell.ShellID != "sh_9" || shell.PID != 4242 || shell.Status != "exited" || shell.Data != "out" ||
		shell.NextCursor != 12 || shell.TotalBytes != 3 || !shell.Truncated || shell.Exit == nil || *shell.Exit != 0 ||
		shell.Outcome != "exited" || !shell.OK {
		t.Fatalf("Shell outcome = %+v, want every field mapped", shell)
	}
	req := client.shellReq
	if req.Op != executorinfra.ShellCreate || len(req.Argv) != 1 || req.Cols != 120 || req.Rows != 40 ||
		req.Env["TERM"] != "xterm" || req.Data != "select 1;\n" || req.Since != 7 || req.TimeoutMS != 2000 || req.LeaseEpoch != 3 {
		t.Fatalf("Shell request = %+v, want the operation mapped verbatim with epoch 3", req)
	}
	file, err := adapter.Files(context.Background(), 0, "sess_1", executionapp.FileOperation{
		Op: executorinfra.FileDownload, Path: "sub/a.txt", Base64: "YWJj",
	}, 3)
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	if file.Path != "sub/a.txt" || file.Bytes != 3 || file.SHA256 != "abc" || file.MIME != "text/plain" ||
		file.Base64 != "YWJj" || !file.Truncated || len(file.Files) != 2 || !file.Files[1].Dir {
		t.Fatalf("Files outcome = %+v, want every field mapped", file)
	}
	fileReq := client.fileReq
	if fileReq.Op != executorinfra.FileDownload || fileReq.Path != "sub/a.txt" || fileReq.Base64 != "YWJj" || fileReq.LeaseEpoch != 3 {
		t.Fatalf("Files request = %+v, want the operation mapped verbatim with epoch 3", fileReq)
	}
} // --- task provider mapping -------------------------------------------------

func TestTaskProviderMapsSnapshot(t *testing.T) {
	snapshot := execution.TaskSnapshot{
		Case: execution.CaseSnapshot{
			CaseVersionID: 41, Title: "登录流程", Description: "desc", Preconditions: "cond", Priority: "P0",
			// v0.2.4-B (T9 design contract): a step carries a prose target and
			// no SUT/page/API id; v0.2.4-F T12 maps that prose into the DTO.
			DefaultSUTID: 7,
			Steps: []execution.CaseStep{{
				Seq: 1, Action: "打开登录页", Expected: "出现表单",
				TargetDescription: "登录页", AccountRole: "admin",
			}},
			Assertions: []execution.CaseAssertion{{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "按钮", SUTID: 7}},
		},
		CaseRun: execution.CaseRun{ID: 3001, WaveID: 2001, IntentID: 1001},
		SUTs: []execution.SUTBinding{{
			SUTID: 7, SUTName: "orders", EnvironmentName: "test", BaseURL: "http://127.0.0.1:13335",
			PageURLs: []string{"http://127.0.0.1:13335/login"}, AccountRoles: []string{"admin"},
		}},
		PriorSteps: []execution.StepResult{{
			Seq: 1, Status: execution.StepPassed, Observed: map[string]any{"summary": "表单可见"},
			TargetSUTID: int64ptrAdapter(7),
		}},
		PriorAssertions: []execution.AssertionResult{{
			StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: execution.AssertionPassed,
			Expected: "按钮", Actual: "按钮", TargetSUTID: int64ptrAdapter(7),
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
	if len(task.Steps) != 1 || task.Steps[0].Action != "打开登录页" {
		t.Fatalf("steps = %+v", task.Steps)
	}
	// The step's target is the design prose (TargetDescription); the
	// design-supplied target SUT/page/API id fields are gone from the DTO with
	// the design columns (v0.2.4-F T12, SC-F1).
	if task.Steps[0].TargetDescription != "登录页" {
		t.Fatalf("step target = %+v, want the prose TargetDescription mapped", task.Steps[0])
	}
	if len(task.Assertions) != 1 || task.Assertions[0].TargetSUTID != 7 {
		t.Fatalf("assertions = %+v", task.Assertions)
	}
	if len(task.PriorSteps) != 1 || task.PriorSteps[0].Status != "passed" || task.PriorSteps[0].Observed != "表单可见" {
		t.Fatalf("prior steps = %+v", task.PriorSteps)
	}
	// Prior-result target_sut_id is FACTUAL attribution and stays mapped.
	if task.PriorSteps[0].TargetSUTID != 7 {
		t.Fatalf("prior step lost its factual target sut id: %+v", task.PriorSteps[0])
	}
	if len(task.PriorAssertions) != 1 || task.PriorAssertions[0].Verdict != "passed" {
		t.Fatalf("prior assertions = %+v", task.PriorAssertions)
	}
	if task.PriorAssertions[0].TargetSUTID != 7 {
		t.Fatalf("prior assertion lost its factual target sut id: %+v", task.PriorAssertions[0])
	}
	if _, ok := any(task).(agentapp.ExecutionTask); !ok {
		t.Fatalf("task is not an agentapp.ExecutionTask")
	}
}

func int64ptrAdapter(value int64) *int64 { return &value }

// silence unused import if a future test drops it
var _ = strings.TrimSpace

// --- version reopen (P4) ---------------------------------------------------

type reopenCall struct {
	versionID int64
	trigger   string
	reason    string
}

type fakeVersionReopener struct {
	calls []reopenCall
	err   error
}

func (f *fakeVersionReopener) ReopenIfCompleted(_ context.Context, versionID int64, trigger, reason string) error {
	f.calls = append(f.calls, reopenCall{versionID: versionID, trigger: trigger, reason: reason})
	return f.err
}

// The P3 intent path must project the P4 version.reopened event with the stable
// execution_intent trigger instead of publishing only version.status_changed.
func TestVersionAdapterReopenUsesExecutionIntentTrigger(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: versionViewFixture()})
	reopener := &fakeVersionReopener{}
	adapter.SetVersionReopener(reopener)

	if err := adapter.Reopen(context.Background(), 10); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if len(reopener.calls) != 1 {
		t.Fatalf("reopen calls = %#v", reopener.calls)
	}
	call := reopener.calls[0]
	if call.versionID != 10 || call.trigger != "execution_intent" || call.reason != "" {
		t.Fatalf("reopen call = %#v", call)
	}
}

// Without the P4 reopen service the adapter must fail closed rather than emit
// only the P3 version.status_changed projection.
func TestVersionAdapterReopenRequiresReopenService(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: versionViewFixture()})
	if err := adapter.Reopen(context.Background(), 10); err == nil {
		t.Fatal("Reopen without the P4 reopen service must fail")
	}
}

// fakeVersionStageReader records the stage projection lookup used by the
// execution intent gate.
type fakeVersionStageReader struct {
	stage versionapp.StageView
	err   error
	calls []int64
}

func (r *fakeVersionStageReader) Stage(_ context.Context, versionID int64) (versionapp.StageView, error) {
	r.calls = append(r.calls, versionID)
	return r.stage, r.err
}

// TestVersionAdapterProjectsStage pins the Task 78 seam: the execution
// VersionBinding must carry the deterministic version stage so the intent scan
// can refuse to turn a case-gate confirmation into an execution wave.
func TestVersionAdapterProjectsStage(t *testing.T) {
	adapter := NewVersionAdapter(&fakeVersionService{view: versionViewFixture()})
	stages := &fakeVersionStageReader{stage: versionapp.StageView{VersionID: 10, Stage: "case_review"}}
	adapter.SetStageReader(stages)

	binding, err := adapter.Get(context.Background(), 10)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if binding.Stage != "case_review" {
		t.Fatalf("binding stage = %q, want case_review", binding.Stage)
	}
	if len(stages.calls) != 1 || stages.calls[0] != 10 {
		t.Fatalf("stage calls = %v", stages.calls)
	}

	stages.err = platform.NotFound("version")
	if _, err := adapter.Get(context.Background(), 10); err == nil {
		t.Fatal("Get must propagate the stage read failure")
	}
}

// NewExecutionAdapter is the test-only single-endpoint constructor (production
// wiring always goes through the per-executor router).
func NewExecutionAdapter(client executorinfra.Client) *ExecutionAdapter {
	return &ExecutionAdapter{client: client}
}

// NewExecutorControl is the test-only single-endpoint control constructor kept
// after W1-1 removed the unused production factory.
func NewExecutorControl(client executorinfra.Client) *ExecutorControlAdapter {
	return &ExecutorControlAdapter{client: client}
}
