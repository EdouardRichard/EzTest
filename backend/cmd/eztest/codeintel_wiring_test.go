package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const (
	codeIntelTestCommit      = "0123456789abcdef0123456789abcdef01234567"
	codeIntelOtherTestCommit = "89abcdef0123456789abcdef0123456789abcdef"
)

// TestCodeIntelWiringAPIVersion pins the composition-root API version default. Release
// builds inject the real identity from the repository-root VERSION file; an
// unstamped build must report "dev". The single-source VERSION assertion lives
// in health_version_test.go (TestHealthVersionSingleSource).
func TestCodeIntelWiringAPIVersion(t *testing.T) {
	if version != "dev" {
		t.Fatalf("API version default = %q, want %q (release builds inject VERSION)", version, "dev")
	}
}

// TestCodeIntelWiringRuntimeConstructs pins that the P5 composition root builds every
// surface without touching the database (the constructor must stay pure: the
// repositories/services are lazy). It is the startup-nil-safety companion of the
// route tests.
func TestCodeIntelWiringRuntimeConstructs(t *testing.T) {
	runtime := newCodeIntelRuntime(context.Background(), codeIntelRuntimeDeps{
		IDs: fakeCodeIntelIDs{}, Clock: clock.System{}, Execution: &executionpg.Repository{},
	})
	if runtime == nil || runtime.CodeIntel == nil || runtime.Worker == nil {
		t.Fatalf("runtime = %+v", runtime)
	}
	if runtime.CodeIntel.Index == nil || runtime.CodeIntel.Anchors == nil ||
		runtime.CodeIntel.Reviews == nil || runtime.CodeIntel.Impact == nil ||
		runtime.CodeIntel.Proposals == nil {
		t.Fatalf("codeintel handlers = %+v", runtime.CodeIntel)
	}
	if runtime.Tools.Propose == nil || runtime.Tools.Index == nil || runtime.Tools.Query == nil ||
		runtime.Tools.Analysis == nil || runtime.Tools.Impact == nil || runtime.Tools.Anchors == nil {
		t.Fatalf("codeintel tool deps = %+v", runtime.Tools)
	}
}

// TestCodeIntelWiringToolRegistryContainsFrozenTools pins that the P5 deps built by
// the composition root expose all ten frozen P5 tools through the registry
// the agent runner uses.
func TestCodeIntelWiringToolRegistryContainsFrozenTools(t *testing.T) {
	runtime := newCodeIntelRuntime(context.Background(), codeIntelRuntimeDeps{
		IDs: fakeCodeIntelIDs{}, Clock: clock.System{}, Execution: &executionpg.Repository{},
	})
	registry := tools.NewCodeIntelRegistry(tools.Deps{CodeIntel: runtime.Tools})
	for _, name := range tools.CodeIntelToolNames() {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("codeintel registry is missing tool %q (names=%v)", name, registry.Names())
		}
	}
}

type fakeCodeIntelIDs struct{}

func (fakeCodeIntelIDs) Next() int64 { return 1 }

/* ------------------------------- work queue ------------------------------- */

type fakeCodeIntelQueue struct {
	ensured   agent.NewWorkItem
	ensureRsp agent.WorkItem
	ensureOK  bool

	claimTeams []agent.Team

	running        bool
	heartbeat      bool
	heartbeatLease time.Duration
	completed      bool
	// lease is the duration this queue grants on claim (0 = reports none, so
	// the adapter falls back to the platform default authority).
	lease time.Duration

	failCause     error
	failRetryable bool
	failed        bool

	recovered int64
}

func (f *fakeCodeIntelQueue) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	f.ensured = in
	return f.ensureRsp, f.ensureOK, nil
}

func (f *fakeCodeIntelQueue) ClaimForTeams(_ context.Context, _ string, teams []agent.Team) (agent.WorkItem, bool, error) {
	f.claimTeams = append([]agent.Team(nil), teams...)
	return agent.WorkItem{
		ID: 44, VersionID: 7, Type: "index.repo", Payload: json.RawMessage(`{"index_run_id":"9"}`),
		Attempts: 2, MaxAttempts: 3, LeaseEpoch: 5,
	}, true, nil
}

func (f *fakeCodeIntelQueue) MarkRunning(context.Context, int64, int64) error {
	f.running = true
	return nil
}

func (f *fakeCodeIntelQueue) Heartbeat(_ context.Context, _, _ int64, lease time.Duration) error {
	f.heartbeat = true
	f.heartbeatLease = lease
	return nil
}

func (f *fakeCodeIntelQueue) Lease() time.Duration { return f.lease }

func (f *fakeCodeIntelQueue) Complete(context.Context, int64, int64) error {
	f.completed = true
	return nil
}

func (f *fakeCodeIntelQueue) Fail(_ context.Context, _, _ int64, cause error, retryable bool) error {
	f.failed = true
	f.failCause = cause
	f.failRetryable = retryable
	return nil
}

func (f *fakeCodeIntelQueue) RecoverExpired(context.Context) (int64, error) {
	return f.recovered, nil
}

func TestCodeIntelWiringQueueMapsWorkItems(t *testing.T) {
	fake := &fakeCodeIntelQueue{ensureRsp: agent.WorkItem{ID: 11, VersionID: 7, Type: "code.analyze"}, ensureOK: true}
	queue := newCodeIntelQueue(fake)
	ctx := context.Background()

	item, created, err := queue.Ensure(ctx, codeintelapp.NewWorkItem{
		VersionID: 7, Team: "code", Type: "code.analyze", Priority: 50,
		DedupeKey: "code:analyze:11", Payload: map[string]any{"analysis_id": "11"}, MaxAttempts: 3,
	})
	if err != nil || !created || item.ID != 11 {
		t.Fatalf("Ensure = %+v, %v, %v", item, created, err)
	}
	if fake.ensured.VersionID != 7 || fake.ensured.Team != "code" || fake.ensured.Type != "code.analyze" ||
		fake.ensured.DedupeKey != "code:analyze:11" || fake.ensured.MaxAttempts != 3 {
		t.Fatalf("mapped ensure input = %+v", fake.ensured)
	}

	claimed, ok, err := queue.ClaimForTeams(ctx, "codeintel-test", []string{"code"})
	if err != nil || !ok {
		t.Fatalf("ClaimForTeams = %v, %v", ok, err)
	}
	if len(fake.claimTeams) != 1 || fake.claimTeams[0] != agent.TeamCode {
		t.Fatalf("claim teams = %#v", fake.claimTeams)
	}
	if claimed.ID != 44 || claimed.VersionID != 7 || claimed.LeaseEpoch != 5 || claimed.Attempts != 2 ||
		claimed.MaxAttempts != 3 || string(claimed.Payload) != `{"index_run_id":"9"}` {
		t.Fatalf("mapped work item = %+v", claimed)
	}

	if err := queue.MarkRunning(ctx, 44, 5); err != nil || !fake.running {
		t.Fatalf("MarkRunning = %v running=%v", err, fake.running)
	}
	if err := queue.Renew(ctx, 44, 5, time.Second); err != nil || !fake.heartbeat {
		t.Fatalf("Renew = %v heartbeat=%v", err, fake.heartbeat)
	}
	if err := queue.Complete(ctx, 44, 5); err != nil || !fake.completed {
		t.Fatalf("Complete = %v completed=%v", err, fake.completed)
	}
	if err := queue.Fail(ctx, 44, 5, "boom", true, time.Second); err != nil || !fake.failed {
		t.Fatalf("Fail = %v failed=%v", err, fake.failed)
	}
	if fake.failCause == nil || fake.failCause.Error() != "boom" || !fake.failRetryable {
		t.Fatalf("fail mapping = %v retryable=%v", fake.failCause, fake.failRetryable)
	}
	if recovered, err := queue.RecoverExpired(ctx); err != nil || recovered != 0 {
		t.Fatalf("RecoverExpired = %d, %v", recovered, err)
	}
}

/* -------------------------------- eventing -------------------------------- */

type fakeCodeIntelPublisher struct{ events []eventingapp.Event }

func (f *fakeCodeIntelPublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestCodeIntelWiringEventsRouteVersionChannel(t *testing.T) {
	publisher := &fakeCodeIntelPublisher{}
	events := newCodeIntelEvents(publisher)
	actor := int64(3)
	ctx := context.Background()

	if err := events.Publish(ctx, codeintelapp.Event{
		Type: "code.index_started", VersionID: 7,
		Scope:   map[string]any{"version_id": "7"},
		Payload: map[string]any{"index_run_id": "9"},
		ActorID: &actor, RequestID: "req_1",
	}); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := events.Publish(ctx, codeintelapp.Event{Type: "system.warning"}); err != nil {
		t.Fatalf("Publish system: %v", err)
	}
	if len(publisher.events) != 2 {
		t.Fatalf("published = %d, want 2", len(publisher.events))
	}
	versionEvent := publisher.events[0]
	if versionEvent.Channel != "version:7" || versionEvent.Type != "code.index_started" ||
		versionEvent.ActorID == nil || *versionEvent.ActorID != 3 || versionEvent.RequestID != "req_1" {
		t.Fatalf("version event = %+v", versionEvent)
	}
	if publisher.events[1].Channel != "system" {
		t.Fatalf("system channel = %q", publisher.events[1].Channel)
	}
}

/* --------------------------------- audit ---------------------------------- */

type fakeCodeIntelAudit struct{ events []auditapp.Event }

func (f *fakeCodeIntelAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestCodeIntelWiringAuditMapsEntry(t *testing.T) {
	recorder := &fakeCodeIntelAudit{}
	audit := newCodeIntelAudit(recorder)
	actor := int64(5)
	if err := audit.Record(context.Background(), codeintelapp.AuditEntry{
		ActorID: &actor, Action: "code.index.start", TargetKind: "code_index_run",
		TargetID: "9", RequestID: "req_2", Metadata: map[string]any{"repo_id": "2"},
	}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit events = %d", len(recorder.events))
	}
	entry := recorder.events[0]
	if entry.Action != "code.index.start" || entry.TargetKind != "code_index_run" ||
		entry.TargetID != "9" || entry.RequestID != "req_2" || entry.ActorID == nil || *entry.ActorID != 5 {
		t.Fatalf("mapped audit entry = %+v", entry)
	}
}

/* ----------------------------- code bindings ------------------------------ */

type fakeCodeIntelBindingsVersions struct {
	view versionapp.VersionView
	err  error
}

func (f *fakeCodeIntelBindingsVersions) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

type fakeCodeIntelBindingsRepo struct {
	sutID  int64
	repoID int64
	commit string
	called bool
}

func (f *fakeCodeIntelBindingsRepo) SetSUTRepositoryBaselineCommit(_ context.Context, sutID, repoID int64, commitSHA string) error {
	f.called = true
	f.sutID = sutID
	f.repoID = repoID
	f.commit = commitSHA
	return nil
}

type fakeCodeIntelBindingResources struct {
	repo resourceapp.CodeRepositoryView
	err  error
}

func (f *fakeCodeIntelBindingResources) GetRepository(context.Context, int64) (resourceapp.CodeRepositoryView, error) {
	return f.repo, f.err
}

type fakeGitRunner struct {
	sha string
	err error
}

func (r fakeGitRunner) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return []byte(r.sha + "\n"), nil, nil
}

func newCodeIntelTestBindings(t *testing.T, versions *fakeCodeIntelBindingsVersions, repo *fakeCodeIntelBindingsRepo, resources *fakeCodeIntelBindingResources) *codeIntelBindings {
	t.Helper()
	bindings := newCodeIntelBindings(gitx.New(fakeGitRunner{sha: codeIntelTestCommit}, 0), resources)
	bindings.SetVersionService(versions, repo)
	return bindings
}

func codeIntelTestVersionView() versionapp.VersionView {
	// v0.2.4-B: the version binds an environment; the SUT catalog, the assets and
	// the code pool are all derived from it.
	return versionapp.VersionView{
		ID: 1, EnvironmentID: 200,
		Binding: versionapp.EnvironmentSUTCatalog{
			VersionID: 1, EnvironmentID: 200,
			Environment: resourceapp.EnvironmentView{
				ID: "200", Name: "prod",
				SUTs: []resourceapp.SUTView{{
					ID: "100", EnvironmentID: "200", Name: "SUT",
					BaseURL: "http://127.0.0.1:13337",
					Pages: []resourceapp.PageView{
						{ID: "500", SUTID: "100", Name: "Orders", RouteTemplate: "/orders", Status: "approved", Source: "admin"},
					},
					APIs: []resourceapp.APIView{
						{ID: "600", SUTID: "100", Name: "Orders API", Method: "GET", PathTemplate: "/api/orders", Status: "approved", Source: "admin"},
					},
				}},
			},
			Repositories: []versionapp.BindingRepository{{
				Repository: resourceapp.CodeRepositoryView{
					ID: "400", Name: "repo", Path: "C:\\repo", Branch: "main",
				},
				SUTID: 100, CodeBaselineCommit: codeIntelTestCommit,
			}},
			CodeBaselineCommit: codeIntelTestCommit,
		},
	}
}

func TestCodeIntelWiringCodeBindingsVersionReader(t *testing.T) {
	versions := &fakeCodeIntelBindingsVersions{view: codeIntelTestVersionView()}
	repo := &fakeCodeIntelBindingsRepo{}
	resources := &fakeCodeIntelBindingResources{repo: resourceapp.CodeRepositoryView{ID: "400"}}
	bindings := newCodeIntelTestBindings(t, versions, repo, resources)
	ctx := context.Background()

	rows, err := bindings.SUTRepoBindings(ctx, 1)
	if err != nil {
		t.Fatalf("SUTRepoBindings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("bindings = %+v", rows)
	}
	row := rows[0]
	if row.SUTID != 100 || row.VersionID != 1 || row.RepoID != 400 ||
		row.Branch != "main" || row.CodeBaselineCommit != codeIntelTestCommit {
		t.Fatalf("mapped binding = %+v", row)
	}

	if err := bindings.SetSUTRepositoryBaselineCommit(ctx, 100, 400, codeIntelTestCommit); err != nil {
		t.Fatalf("SetSUTRepositoryBaselineCommit: %v", err)
	}
	if !repo.called || repo.sutID != 100 || repo.repoID != 400 || repo.commit != codeIntelTestCommit {
		t.Fatalf("baseline writer call = %+v", repo)
	}
}

// TestCodeIntelWiringCodeBindingsExpandsMultipleRepositories pins the multi-repo
// mapping: an environment code pool with two repositories must produce one
// codeintel binding per pool row so index/analysis can cover both repositories.
func TestCodeIntelWiringCodeBindingsExpandsMultipleRepositories(t *testing.T) {
	view := codeIntelTestVersionView()
	view.Binding.Repositories = []versionapp.BindingRepository{
		{Repository: resourceapp.CodeRepositoryView{ID: "400", Name: "backend", Path: "C:\\backend", Branch: "main"}, SUTID: 100},
		{Repository: resourceapp.CodeRepositoryView{ID: "401", Name: "frontend", Path: "C:\\frontend", Branch: "main"}, SUTID: 100},
	}
	versions := &fakeCodeIntelBindingsVersions{view: view}
	bindings := newCodeIntelTestBindings(t, versions, &fakeCodeIntelBindingsRepo{}, &fakeCodeIntelBindingResources{})

	rows, err := bindings.SUTRepoBindings(context.Background(), 1)
	if err != nil {
		t.Fatalf("SUTRepoBindings: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("bindings = %+v, want one per effective repository", rows)
	}
	repoIDs := map[int64]bool{}
	for _, row := range rows {
		repoIDs[row.RepoID] = true
		if row.SUTID != 100 || row.VersionID != 1 {
			t.Fatalf("binding = %+v", row)
		}
	}
	if !repoIDs[400] || !repoIDs[401] {
		t.Fatalf("repo ids = %+v", repoIDs)
	}
}

// TestCodeIntelWiringCodeBindingsResolvesEnvironmentSUTUnion pins GD-03: the binding
// set is the environment code pool itself — one row per (SUT, repository) pair
// with that pair's own pinned baseline — not the cross product of every SUT with
// every repository. A second SUT owning a second repository must not inherit the
// first pair's baseline, and no phantom pair may appear.
func TestCodeIntelWiringCodeBindingsResolvesEnvironmentSUTUnion(t *testing.T) {
	view := codeIntelTestVersionView()
	view.Binding.Environment.SUTs = append(view.Binding.Environment.SUTs, resourceapp.SUTView{
		ID: "101", EnvironmentID: "200", Name: "Web", BaseURL: "http://127.0.0.1:13338",
	})
	view.Binding.Repositories = []versionapp.BindingRepository{
		{
			Repository: resourceapp.CodeRepositoryView{ID: "400", Name: "backend", Path: "C:\\backend", Branch: "main"},
			SUTID:      100, CodeBaselineCommit: codeIntelTestCommit,
		},
		{
			Repository: resourceapp.CodeRepositoryView{ID: "401", Name: "frontend", Path: "C:\\frontend", Branch: "release"},
			SUTID:      101, CodeBaselineCommit: codeIntelOtherTestCommit,
		},
	}
	bindings := newCodeIntelTestBindings(t, &fakeCodeIntelBindingsVersions{view: view}, &fakeCodeIntelBindingsRepo{}, &fakeCodeIntelBindingResources{})

	rows, err := bindings.SUTRepoBindings(context.Background(), 1)
	if err != nil {
		t.Fatalf("SUTRepoBindings: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("bindings = %+v, want exactly the two pool rows", rows)
	}
	byRepo := map[int64]codeintelapp.SUTRepoBinding{}
	for _, row := range rows {
		byRepo[row.RepoID] = row
	}
	if got := byRepo[400]; got.SUTID != 100 || got.Branch != "main" || got.CodeBaselineCommit != codeIntelTestCommit {
		t.Fatalf("repo 400 binding = %+v", got)
	}
	if got := byRepo[401]; got.SUTID != 101 || got.Branch != "release" || got.CodeBaselineCommit != codeIntelOtherTestCommit {
		t.Fatalf("repo 401 binding = %+v", got)
	}
	// Both SUTs stay resolvable for the asset/proposal reads even though only one
	// of them owns each repository.
	for _, sutID := range []int64{100, 101} {
		if _, err := bindings.ProposalTarget(context.Background(), sutID); err != nil {
			t.Fatalf("ProposalTarget(%d): %v", sutID, err)
		}
	}
}

func TestCodeIntelWiringCodeBindingsServesTargetsAndResources(t *testing.T) {
	versions := &fakeCodeIntelBindingsVersions{view: codeIntelTestVersionView()}
	repo := &fakeCodeIntelBindingsRepo{}
	resources := &fakeCodeIntelBindingResources{repo: resourceapp.CodeRepositoryView{
		ID: "400", Name: "repo", Path: "C:\\repo", Branch: "main", CreatedBy: 1,
	}}
	bindings := newCodeIntelTestBindings(t, versions, repo, resources)
	ctx := context.Background()

	if _, err := bindings.SUTRepoBindings(ctx, 1); err != nil {
		t.Fatalf("SUTRepoBindings: %v", err)
	}
	// The proposal destination is the SUT: pages/APIs are SUT-owned (v0.2.4-B).
	target, err := bindings.ProposalTarget(ctx, 100)
	if err != nil {
		t.Fatalf("ProposalTarget: %v", err)
	}
	if target.SUTID != 100 || target.BaseURL != "http://127.0.0.1:13337" {
		t.Fatalf("proposal target = %+v", target)
	}
	pages, err := bindings.SUTPages(ctx, 100)
	if err != nil || len(pages) != 1 || pages[0].ID != 500 || pages[0].RouteTemplate != "/orders" {
		t.Fatalf("SUTPages = %+v, %v", pages, err)
	}
	apis, err := bindings.SUTAPIs(ctx, 100)
	if err != nil || len(apis) != 1 || apis[0].ID != 600 || apis[0].Method != "GET" {
		t.Fatalf("SUTAPIs = %+v, %v", apis, err)
	}
	repository, err := bindings.CodeRepository(ctx, 400)
	if err != nil {
		t.Fatalf("CodeRepository: %v", err)
	}
	if repository.ID != 400 || repository.Path != "C:\\repo" || repository.Branch != "main" {
		t.Fatalf("repo ref = %+v", repository)
	}
}

// TestProposedSUTNotInImpactOrBaseline pins the U30 scope read-point (v0.2.4-F
// T11, SC-F4): the codeintel SUT resource cache is built from the SINGLE
// effective-scope projection (EffectiveSUTs), so a proposed or rejected SUT — e.g.
// one an Agent registered via sut.register that the evidence gate did not approve
// — never enters the cache the impact index / proposal target reads. Its pages and
// APIs are therefore invisible to codeintel. Reverting the cache to iterate the raw
// catalog (Environment.SUTs) without the IsApproved gate would cache the proposed
// SUT's assets and fail this test (mutation).
func TestProposedSUTNotInImpactOrBaseline(t *testing.T) {
	view := codeIntelTestVersionView()
	// An approved SUT (100, from the base fixture) plus a proposed (102) and a
	// rejected (103) SUT, each carrying pages/APIs that must NOT leak when unapproved.
	view.Binding.Environment.SUTs = append(view.Binding.Environment.SUTs,
		resourceapp.SUTView{
			ID: "102", EnvironmentID: "200", Name: "Proposed", BaseURL: "http://127.0.0.1:13339",
			Source: "ai", Status: "proposed",
			Pages: []resourceapp.PageView{
				{ID: "510", SUTID: "102", Name: "Leak Page", RouteTemplate: "/leak", Status: "approved", Source: "ai"},
			},
			APIs: []resourceapp.APIView{
				{ID: "610", SUTID: "102", Name: "Leak API", Method: "GET", PathTemplate: "/api/leak", Status: "approved", Source: "ai"},
			},
		},
		resourceapp.SUTView{
			ID: "103", EnvironmentID: "200", Name: "Rejected", BaseURL: "http://127.0.0.1:13340",
			Source: "ai", Status: "rejected",
			Pages: []resourceapp.PageView{
				{ID: "511", SUTID: "103", Name: "Rejected Page", RouteTemplate: "/rejected", Status: "approved", Source: "ai"},
			},
		},
	)
	bindings := newCodeIntelTestBindings(t, &fakeCodeIntelBindingsVersions{view: view}, &fakeCodeIntelBindingsRepo{}, &fakeCodeIntelBindingResources{})
	ctx := context.Background()
	if _, err := bindings.SUTRepoBindings(ctx, 1); err != nil {
		t.Fatalf("SUTRepoBindings: %v", err)
	}

	// The approved SUT's assets are cached and reachable.
	pages, err := bindings.SUTPages(ctx, 100)
	if err != nil || len(pages) != 1 || pages[0].ID != 500 {
		t.Fatalf("approved SUT pages = %+v, %v, want the approved page 500", pages, err)
	}

	// The proposed and rejected SUTs are NOT cached: their assets never reach the
	// impact index / proposal target (R-B4 effective-scope gate).
	for _, sutID := range []int64{102, 103} {
		if pages, err := bindings.SUTPages(ctx, sutID); err == nil {
			t.Fatalf("SUTPages(%d) = %+v, want not-found (unapproved SUT must not be cached)", sutID, pages)
		}
		if apis, err := bindings.SUTAPIs(ctx, sutID); err == nil {
			t.Fatalf("SUTAPIs(%d) = %+v, want not-found (unapproved SUT must not be cached)", sutID, apis)
		}
		if _, err := bindings.ProposalTarget(ctx, sutID); err == nil {
			t.Fatalf("ProposalTarget(%d) succeeded, want not-found (unapproved SUT must not be a proposal target)", sutID)
		}
	}
}

func TestCodeIntelWiringCodeBindingsResolveHeadCommit(t *testing.T) {
	dir := t.TempDir()
	bindings := newCodeIntelTestBindings(t, &fakeCodeIntelBindingsVersions{}, &fakeCodeIntelBindingsRepo{}, &fakeCodeIntelBindingResources{})
	commit, err := bindings.ResolveHeadCommit(context.Background(), resourceapp.CodeRepositoryView{
		Path: dir, Branch: "main",
	})
	if err != nil {
		t.Fatalf("ResolveHeadCommit: %v", err)
	}
	if commit != codeIntelTestCommit {
		t.Fatalf("commit = %q, want %q", commit, codeIntelTestCommit)
	}
}
