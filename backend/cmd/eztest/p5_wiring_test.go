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
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const p5TestCommit = "0123456789abcdef0123456789abcdef01234567"

// TestP5WiringAPIVersion pins the composition-root API version constant. The
// value is what /api/health reports and what the frozen OpenAPI declares.
func TestP5WiringAPIVersion(t *testing.T) {
	if version != "0.2.0-p8" {
		t.Fatalf("API version = %q, want 0.2.0-p8", version)
	}
}

// TestP5WiringRuntimeConstructs pins that the P5 composition root builds every
// surface without touching the database (the constructor must stay pure: the
// repositories/services are lazy). It is the startup-nil-safety companion of the
// route tests.
func TestP5WiringRuntimeConstructs(t *testing.T) {
	runtime := newP5Runtime(context.Background(), p5RuntimeDeps{
		IDs: fakeP5IDs{}, Clock: clock.System{}, Execution: &executionpg.Repository{},
	})
	if runtime == nil || runtime.CodeIntel == nil || runtime.Visual == nil ||
		runtime.VisualConfigs == nil || runtime.VisualPort == nil || runtime.Worker == nil {
		t.Fatalf("runtime = %+v", runtime)
	}
	if runtime.CodeIntel.Index == nil || runtime.CodeIntel.Anchors == nil ||
		runtime.CodeIntel.Reviews == nil || runtime.CodeIntel.Impact == nil ||
		runtime.CodeIntel.Proposals == nil {
		t.Fatalf("codeintel handlers = %+v", runtime.CodeIntel)
	}
	if runtime.VisualPort.Configs == nil || runtime.VisualPort.Baselines == nil || runtime.VisualPort.Diffs == nil {
		t.Fatalf("visual port = %+v", runtime.VisualPort)
	}
	if runtime.Tools.Propose == nil || runtime.Tools.Index == nil || runtime.Tools.Query == nil ||
		runtime.Tools.Analysis == nil || runtime.Tools.Impact == nil || runtime.Tools.Anchors == nil {
		t.Fatalf("P5 tool deps = %+v", runtime.Tools)
	}
}

// TestP5WiringToolRegistryContainsFrozenTools pins that the P5 deps built by
// the composition root expose all eleven frozen P5 tools through the registry
// the agent runner uses.
func TestP5WiringToolRegistryContainsFrozenTools(t *testing.T) {
	runtime := newP5Runtime(context.Background(), p5RuntimeDeps{
		IDs: fakeP5IDs{}, Clock: clock.System{}, Execution: &executionpg.Repository{},
	})
	registry := tools.NewP5Registry(tools.Deps{P5: runtime.Tools})
	for _, name := range tools.P5ToolNames() {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("P5 registry is missing tool %q (names=%v)", name, registry.Names())
		}
	}
}

type fakeP5IDs struct{}

func (fakeP5IDs) Next() int64 { return 1 }

/* ------------------------------- work queue ------------------------------- */

type fakeP5Queue struct {
	ensured   agent.NewWorkItem
	ensureRsp agent.WorkItem
	ensureOK  bool

	claimTeams []agent.Team

	running        bool
	heartbeat      bool
	heartbeatLease time.Duration
	completed      bool

	failCause     error
	failRetryable bool
	failed        bool

	recovered int64
}

func (f *fakeP5Queue) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	f.ensured = in
	return f.ensureRsp, f.ensureOK, nil
}

func (f *fakeP5Queue) ClaimForTeams(_ context.Context, _ string, teams []agent.Team) (agent.WorkItem, bool, error) {
	f.claimTeams = append([]agent.Team(nil), teams...)
	return agent.WorkItem{
		ID: 44, VersionID: 7, Type: "index.repo", Payload: json.RawMessage(`{"index_run_id":"9"}`),
		Attempts: 2, MaxAttempts: 3, LeaseEpoch: 5,
	}, true, nil
}

func (f *fakeP5Queue) MarkRunning(context.Context, int64, int64) error {
	f.running = true
	return nil
}

func (f *fakeP5Queue) Heartbeat(_ context.Context, _, _ int64, lease time.Duration) error {
	f.heartbeat = true
	f.heartbeatLease = lease
	return nil
}

func (f *fakeP5Queue) Complete(context.Context, int64, int64) error {
	f.completed = true
	return nil
}

func (f *fakeP5Queue) Fail(_ context.Context, _, _ int64, cause error, retryable bool) error {
	f.failed = true
	f.failCause = cause
	f.failRetryable = retryable
	return nil
}

func (f *fakeP5Queue) RecoverExpired(context.Context) (int64, error) {
	return f.recovered, nil
}

func TestP5WiringCodeIntelQueueMapsWorkItems(t *testing.T) {
	fake := &fakeP5Queue{ensureRsp: agent.WorkItem{ID: 11, VersionID: 7, Type: "code.analyze"}, ensureOK: true}
	queue := newP5CodeIntelQueue(fake)
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

type fakeP5Publisher struct{ events []eventingapp.Event }

func (f *fakeP5Publisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestP5WiringCodeIntelEventsRouteVersionChannel(t *testing.T) {
	publisher := &fakeP5Publisher{}
	events := newP5CodeIntelEvents(publisher)
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

type fakeP5Audit struct{ events []auditapp.Event }

func (f *fakeP5Audit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

func TestP5WiringCodeIntelAuditMapsEntry(t *testing.T) {
	recorder := &fakeP5Audit{}
	audit := newP5CodeIntelAudit(recorder)
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

type fakeP5BindingsVersions struct {
	view versionapp.VersionView
	err  error
}

func (f *fakeP5BindingsVersions) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

type fakeP5BindingsRepo struct {
	sutID  int64
	commit string
	called bool
}

func (f *fakeP5BindingsRepo) SetCodeBaselineCommit(_ context.Context, versionSUTID int64, commitSHA string) error {
	f.called = true
	f.sutID = versionSUTID
	f.commit = commitSHA
	return nil
}

type fakeP5BindingResources struct {
	repo resourceapp.CodeRepositoryView
	err  error
}

func (f *fakeP5BindingResources) GetRepository(context.Context, int64) (resourceapp.CodeRepositoryView, error) {
	return f.repo, f.err
}

type fakeP5GitRunner struct {
	sha string
	err error
}

func (r fakeP5GitRunner) Run(context.Context, string, ...string) ([]byte, []byte, error) {
	if r.err != nil {
		return nil, nil, r.err
	}
	return []byte(r.sha + "\n"), nil, nil
}

func newP5TestBindings(t *testing.T, versions *fakeP5BindingsVersions, repo *fakeP5BindingsRepo, resources *fakeP5BindingResources) *p5CodeBindings {
	t.Helper()
	bindings := newP5CodeBindings(gitx.New(fakeP5GitRunner{sha: p5TestCommit}, 0), resources)
	bindings.SetVersionService(versions, repo)
	return bindings
}

func p5TestVersionView() versionapp.VersionView {
	return versionapp.VersionView{
		ID: 1,
		Bindings: []versionapp.BindingView{{
			ID:                 300,
			CodeBaselineCommit: p5TestCommit,
			SUT:                resourceapp.SUTView{ID: "100", Name: "SUT"},
			Environment:        resourceapp.EnvironmentView{ID: "200", BaseURL: "http://127.0.0.1:13337"},
			Repository: &resourceapp.CodeRepositoryView{
				ID: "400", Name: "repo", Path: "C:\\repo", Branch: "main",
			},
			Pages: []resourceapp.PageView{{ID: "500", Name: "Orders", RouteTemplate: "/orders", Status: "approved", Source: "admin"}},
			APIs:  []resourceapp.APIView{{ID: "600", Name: "Orders API", Method: "GET", PathTemplate: "/api/orders", Status: "approved", Source: "admin"}},
		}},
	}
}

func TestP5WiringCodeBindingsVersionReader(t *testing.T) {
	versions := &fakeP5BindingsVersions{view: p5TestVersionView()}
	repo := &fakeP5BindingsRepo{}
	resources := &fakeP5BindingResources{repo: resourceapp.CodeRepositoryView{ID: "400"}}
	bindings := newP5TestBindings(t, versions, repo, resources)
	ctx := context.Background()

	rows, err := bindings.VersionSUTBindings(ctx, 1)
	if err != nil {
		t.Fatalf("VersionSUTBindings: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("bindings = %+v", rows)
	}
	row := rows[0]
	if row.VersionSUTID != 300 || row.VersionID != 1 || row.SUTID != 100 || row.RepoID != 400 ||
		row.Branch != "main" || row.CodeBaselineCommit != p5TestCommit {
		t.Fatalf("mapped binding = %+v", row)
	}

	if err := bindings.SetCodeBaselineCommit(ctx, 300, p5TestCommit); err != nil {
		t.Fatalf("SetCodeBaselineCommit: %v", err)
	}
	if !repo.called || repo.sutID != 300 || repo.commit != p5TestCommit {
		t.Fatalf("repository call = %+v", repo)
	}
}

func TestP5WiringCodeBindingsServesTargetsAndResources(t *testing.T) {
	versions := &fakeP5BindingsVersions{view: p5TestVersionView()}
	repo := &fakeP5BindingsRepo{}
	resources := &fakeP5BindingResources{repo: resourceapp.CodeRepositoryView{
		ID: "400", Name: "repo", Path: "C:\\repo", Branch: "main", CreatedBy: 1,
	}}
	bindings := newP5TestBindings(t, versions, repo, resources)
	ctx := context.Background()

	if _, err := bindings.VersionSUTBindings(ctx, 1); err != nil {
		t.Fatalf("VersionSUTBindings: %v", err)
	}
	target, err := bindings.ProposalTarget(ctx, 300)
	if err != nil {
		t.Fatalf("ProposalTarget: %v", err)
	}
	if target.VersionSUTID != 300 || target.SUTID != 100 || target.EnvironmentID != 200 ||
		target.BaseURL != "http://127.0.0.1:13337" {
		t.Fatalf("proposal target = %+v", target)
	}
	pages, err := bindings.SUTPages(ctx, 300)
	if err != nil || len(pages) != 1 || pages[0].ID != 500 || pages[0].RouteTemplate != "/orders" {
		t.Fatalf("SUTPages = %+v, %v", pages, err)
	}
	apis, err := bindings.SUTAPIs(ctx, 300)
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

func TestP5WiringCodeBindingsResolveHeadCommit(t *testing.T) {
	dir := t.TempDir()
	bindings := newP5TestBindings(t, &fakeP5BindingsVersions{}, &fakeP5BindingsRepo{}, &fakeP5BindingResources{})
	commit, err := bindings.ResolveHeadCommit(context.Background(), resourceapp.CodeRepositoryView{
		Path: dir, Branch: "main",
	})
	if err != nil {
		t.Fatalf("ResolveHeadCommit: %v", err)
	}
	if commit != p5TestCommit {
		t.Fatalf("commit = %q, want %q", commit, p5TestCommit)
	}
}

/* ----------------------------- visual case reader ------------------------- */

type fakeP5DesignReader struct {
	graph    design.GraphData
	caseData design.CaseData
}

func (f *fakeP5DesignReader) ReadGraph(context.Context, int64) (design.GraphData, error) {
	return f.graph, nil
}

func (f *fakeP5DesignReader) ReadCase(context.Context, int64, int64, *int64) (design.CaseData, error) {
	return f.caseData, nil
}

func (f *fakeP5DesignReader) ReadCaseVersionsByIDs(context.Context, int64, []int64) ([]design.CaseVersionSummary, error) {
	return nil, nil
}

func (f *fakeP5DesignReader) ListExecutableCases(context.Context, int64) ([]design.ExecutableCase, error) {
	return nil, nil
}

func p5ID(value int64) *int64 { return &value }

// TestP5WiringVisualCaseReaderDerivesAssertionSeq pins the P3-D-06 positional
// join: the design read model does not store assertion_seq, so the visual
// configuration adapter must derive it (1-based per step) or the execution task
// context cannot attach the visual_assertion ID.
func TestP5WiringVisualCaseReaderDerivesAssertionSeq(t *testing.T) {
	reader := newP5VisualCaseReader(&fakeP5DesignReader{
		graph: design.GraphData{Requirements: []design.RequirementGraph{{
			Scenarios: []design.ScenarioGraph{{Cases: []design.CaseGraph{{
				Case:           design.CaseEntity{ID: 20},
				Current:        design.CaseVersion{ID: 30, Status: design.CaseStatusDraft},
				AssertionCount: 3,
			}}}},
		}}},
		caseData: design.CaseData{Selected: design.CaseVersion{
			ID: 30, DefaultVersionSUTID: p5ID(9),
			Assertions: []design.CaseAssertion{
				{ID: 100, StepSeq: 1, Kind: "visual", TargetVersionSUTID: p5ID(11)},
				{ID: 101, StepSeq: 1, Kind: "ui"},
				{ID: 102, StepSeq: 2, Kind: "visual"},
			},
		}},
	})

	ref, err := reader.VisualAssertionContext(context.Background(), 1, 101)
	if err != nil {
		t.Fatalf("VisualAssertionContext: %v", err)
	}
	if ref.CaseAssertionID != 101 || ref.CaseVersionID != 30 || ref.CaseID != 20 ||
		ref.StepSeq != 1 || ref.AssertionSeq != 2 || ref.Kind != "ui" ||
		ref.DefaultVersionSUTID != 9 || ref.TargetVersionSUTID != 0 ||
		ref.CaseVersionStatus != design.CaseStatusDraft {
		t.Fatalf("mapped assertion = %+v", ref)
	}

	// A new step restarts the derived sequence.
	ref, err = reader.VisualAssertionContext(context.Background(), 1, 102)
	if err != nil || ref.StepSeq != 2 || ref.AssertionSeq != 1 {
		t.Fatalf("second-step assertion = %+v, %v", ref, err)
	}

	// Unknown assertion IDs are a clean 404.
	if _, err := reader.VisualAssertionContext(context.Background(), 1, 999); err == nil ||
		platform.AsError(err).Status != 404 {
		t.Fatalf("unknown assertion error = %v", err)
	}
}
