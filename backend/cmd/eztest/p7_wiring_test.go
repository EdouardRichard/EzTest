package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/knowledgeadapter"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	settingsdomain "github.com/EdouardRichard/EzTest/backend/internal/settings/domain"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// p7FakeLeaderGate is the leader signal double of the gating tests.
type p7FakeLeaderGate bool

func (g p7FakeLeaderGate) IsLeader() bool { return bool(g) }

// p7FakeAudit records the audit events the knowledge adapter submits.
type p7FakeAudit struct {
	events []auditapp.Event
	err    error
}

func (f *p7FakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return f.err
}

// p7FakeSettingGetter is the settings repository double used by the raw reader.
type p7FakeSettingGetter struct {
	values map[string]settingsdomain.Setting
	err    error
}

func (f p7FakeSettingGetter) Get(_ context.Context, key string) (settingsdomain.Setting, error) {
	if f.err != nil {
		return settingsdomain.Setting{}, f.err
	}
	setting, ok := f.values[key]
	if !ok {
		return settingsdomain.Setting{}, errors.New("setting not found")
	}
	return setting, nil
}

// p7FakeExecutorLister is the executor roster double of the dispatch tests.
type p7FakeExecutorLister struct {
	rows []execution.ExecutorInstance
	err  error
}

func (f p7FakeExecutorLister) List(context.Context) ([]execution.ExecutorInstance, error) {
	return f.rows, f.err
}

// p7FakeVersionCaseSource is the version read-model double of the knowledge
// success-hook binding adapter.
type p7FakeVersionCaseSource struct {
	view versionapp.VersionView
	err  error
	ids  []int64
}

func (f *p7FakeVersionCaseSource) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return versionapp.VersionView{}, f.err
	}
	return f.view, nil
}

func TestP7LeaderGating(t *testing.T) {
	ran := 0
	fn := func() { ran++ }

	if p7RunGated(nil, fn) {
		t.Fatal("nil leader gate executed the gated work")
	}
	if p7RunGated(p7FakeLeaderGate(false), fn) {
		t.Fatal("follower gate executed the gated work")
	}
	if ran != 0 {
		t.Fatalf("gated work ran %d times on a follower, want 0", ran)
	}
	if !p7RunGated(p7FakeLeaderGate(true), fn) {
		t.Fatal("leader gate did not execute the gated work")
	}
	if ran != 1 {
		t.Fatalf("gated work ran %d times on the leader, want 1", ran)
	}
}

func TestP7KnowledgeAuditMapsContractFields(t *testing.T) {
	recorder := &p7FakeAudit{}
	adapter := p7KnowledgeAudit{recorder: recorder}
	metadata := map[string]any{"actor_id": "42", "scope": "global"}
	if err := adapter.Record(context.Background(), "knowledge.approve", 77, metadata); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if len(recorder.events) != 1 {
		t.Fatalf("audit events = %d, want 1", len(recorder.events))
	}
	event := recorder.events[0]
	if event.Action != "knowledge.approve" {
		t.Fatalf("action = %q", event.Action)
	}
	if event.TargetKind != "knowledge_item" || event.TargetID != "77" {
		t.Fatalf("target = %q/%q, want knowledge_item/77", event.TargetKind, event.TargetID)
	}
	if event.ActorID == nil || *event.ActorID != 42 {
		t.Fatalf("actor = %v, want 42", event.ActorID)
	}
	if event.Metadata["scope"] != "global" {
		t.Fatalf("metadata = %v, want the caller metadata preserved", event.Metadata)
	}
}

func TestP7KnowledgeAuditToleratesNilSinkAndBadActor(t *testing.T) {
	if err := (p7KnowledgeAudit{}).Record(context.Background(), "knowledge.create", 1, nil); err != nil {
		t.Fatalf("nil recorder error = %v, want nil", err)
	}
	recorder := &p7FakeAudit{}
	adapter := p7KnowledgeAudit{recorder: recorder}
	if err := adapter.Record(context.Background(), "knowledge.create", 1, map[string]any{"actor_id": "not-a-number"}); err != nil {
		t.Fatalf("bad actor error = %v, want nil", err)
	}
	if recorder.events[0].ActorID != nil {
		t.Fatalf("actor = %v, want nil for an unparseable actor", recorder.events[0].ActorID)
	}
}

func TestP7RawSettingsReadsTypedValues(t *testing.T) {
	getter := p7FakeSettingGetter{values: map[string]settingsdomain.Setting{
		"number": {Key: "number", Value: json.RawMessage(`42`)},
		"text":   {Key: "text", Value: json.RawMessage(`"hello"`)},
		"flag":   {Key: "flag", Value: json.RawMessage(`true`)},
	}}
	settings := p7RawSettings{repo: getter}
	if value, err := settings.Int(context.Background(), "number"); err != nil || value != 42 {
		t.Fatalf("Int(number) = %d/%v, want 42/nil", value, err)
	}
	if value, err := settings.String(context.Background(), "text"); err != nil || value != "hello" {
		t.Fatalf("String(text) = %q/%v, want hello/nil", value, err)
	}
	if value, err := settings.Bool(context.Background(), "flag"); err != nil || !value {
		t.Fatalf("Bool(flag) = %v/%v, want true/nil", value, err)
	}
	if _, err := (p7RawSettings{}).Int(context.Background(), "number"); err == nil {
		t.Fatal("nil settings repository returned no error")
	}
}

func TestP7AdaptersFailClosedWithoutDatabase(t *testing.T) {
	ctx := context.Background()
	if _, err := (p7SourceVersions{}).SourceVersionExists(ctx, 1); err == nil {
		t.Fatal("source version adapter accepted a nil pool")
	}
	if _, err := (p7VersionSystems{}).VersionSystemKeys(ctx, 1); err == nil {
		t.Fatal("version system adapter accepted a nil pool")
	}
	lookup := p7OwnerLookup{}
	if _, err := lookup.ReportRevisionExists(ctx, 1); err == nil {
		t.Fatal("owner lookup accepted a nil pool (report revision)")
	}
	if _, err := lookup.AttachmentExists(ctx, 1); err == nil {
		t.Fatal("owner lookup accepted a nil pool (attachment)")
	}
	if _, err := lookup.EvidenceExists(ctx, 1); err == nil {
		t.Fatal("owner lookup accepted a nil pool (evidence)")
	}
	if _, err := lookup.ExportArtifactExists(ctx, 1); err == nil {
		t.Fatal("owner lookup accepted a nil pool (export artifact)")
	}
}

// TestP7VersionCaseBindingsProjectsSuccessBindingKeys pins the composition-root
// projection consumed by the knowledge success hook: route templates with a URL
// fallback, uppercase/default-GET API keys, per-binding role deduplication and
// invalid resource IDs degrading to unset dimensions instead of zero IDs.
func TestP7VersionCaseBindingsProjectsSuccessBindingKeys(t *testing.T) {
	family := "shop"
	fullView := versionapp.VersionView{
		Bindings: []versionapp.BindingView{{
			SUT:         resourceapp.SUTView{ID: "7001", Family: &family},
			Environment: resourceapp.EnvironmentView{ID: "7101"},
			Pages: []resourceapp.PageView{
				{ID: "501", RouteTemplate: "/login", URL: "http://shop.test/login"},
				{ID: "502", URL: "http://shop.test/cart"},
				{ID: "503"},
				{ID: "not-an-id", RouteTemplate: "/ignored"},
			},
			APIs: []resourceapp.APIView{
				{ID: "601", Method: "post", PathTemplate: "/api/login"},
				{ID: "602", PathTemplate: "/api/items"},
				{ID: "603", Method: "GET", PathTemplate: " /api/orders "},
				{ID: "604", Method: "GET"},
				{ID: "not-an-id", Method: "GET", PathTemplate: "/ignored"},
			},
			Accounts: []resourceapp.AccountView{
				{Roles: []string{"admin", " admin "}},
				{Roles: []string{"user", "admin"}},
				{Roles: []string{"", "  "}},
			},
		}},
	}
	wantFull := []knowledgeadapter.VersionBinding{{
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		Pages: []knowledgeadapter.PageBinding{
			{ID: 501, Key: "/login"},
			{ID: 502, Key: "http://shop.test/cart"},
		},
		APIs: []knowledgeadapter.APIBinding{
			{ID: 601, Key: "POST /api/login"},
			{ID: 602, Key: "GET /api/items"},
			{ID: 603, Key: "GET /api/orders"},
		},
		AccountRoles: []string{"admin", "user"},
	}}

	blankFamily := "   "
	degradedView := versionapp.VersionView{
		Bindings: []versionapp.BindingView{{
			SUT:         resourceapp.SUTView{ID: "not-an-id", Family: &blankFamily},
			Environment: resourceapp.EnvironmentView{ID: "-5"},
			Pages:       []resourceapp.PageView{{ID: "bad", RouteTemplate: "/ignored"}},
			APIs:        []resourceapp.APIView{{ID: "bad", Method: "GET", PathTemplate: "/ignored"}},
			Accounts:    []resourceapp.AccountView{{Roles: []string{"admin"}}},
		}},
	}
	wantDegraded := []knowledgeadapter.VersionBinding{{
		AccountRoles: []string{"admin"},
	}}

	sourceErr := errors.New("version read failed")
	tests := []struct {
		name      string
		source    versionCaseBindingSource
		versionID int64
		want      []knowledgeadapter.VersionBinding
		wantErr   bool
	}{
		{
			name:      "projects route templates with URL fallback, API keys and deduplicated roles",
			source:    &p7FakeVersionCaseSource{view: fullView},
			versionID: 10,
			want:      wantFull,
		},
		{
			name:      "invalid resource IDs degrade to unset dimensions",
			source:    &p7FakeVersionCaseSource{view: degradedView},
			versionID: 10,
			want:      wantDegraded,
		},
		{
			name:      "a version without bindings yields an empty projection",
			source:    &p7FakeVersionCaseSource{},
			versionID: 10,
			want:      []knowledgeadapter.VersionBinding{},
		},
		{name: "rejects a missing version source", source: nil, versionID: 10, wantErr: true},
		{name: "rejects a non-positive version ID", source: &p7FakeVersionCaseSource{}, versionID: 0, wantErr: true},
		{name: "propagates the version read error", source: &p7FakeVersionCaseSource{err: sourceErr}, versionID: 10, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (p7VersionCaseBindings{versions: tt.source}).VersionBindings(context.Background(), tt.versionID)
			if tt.wantErr {
				if err == nil {
					t.Fatal("VersionBindings() error = nil, want an error")
				}
				if got != nil {
					t.Fatalf("VersionBindings() = %#v on error, want nil", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("VersionBindings() error = %v", err)
			}
			if fake, ok := tt.source.(*p7FakeVersionCaseSource); ok {
				if len(fake.ids) != 1 || fake.ids[0] != tt.versionID {
					t.Fatalf("source queried version IDs = %v, want [%d]", fake.ids, tt.versionID)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("VersionBindings() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestP7RenderQueueDrainGate(t *testing.T) {
	ctx := context.Background()
	draining := renderQueueAdapter{
		queue:    nil,
		draining: func() bool { return true },
	}
	job, ok, err := draining.ClaimForTypes(ctx, "render-worker", []string{deliveryapp.RenderTaskType}, time.Second)
	if err != nil || ok {
		t.Fatalf("draining claim = %+v/%v/%v, want no claim and no error", job, ok, err)
	}
	if _, _, err := (renderQueueAdapter{}).ClaimForTypes(ctx, "render-worker", nil, time.Second); err == nil {
		t.Fatal("nil work queue returned no error")
	}
	if err := (renderQueueAdapter{}).Heartbeat(ctx, 1, 1, time.Second); err == nil {
		t.Fatal("nil work queue heartbeat returned no error")
	}
}

func TestP7RenderSettingsFallsBackToDefaults(t *testing.T) {
	enabled, attempts := p7RenderSettings(context.Background(), nil)
	if !enabled || attempts != deliveryapp.DefaultRenderMaxAttempts {
		t.Fatalf("nil settings = %v/%d, want true/%d", enabled, attempts, deliveryapp.DefaultRenderMaxAttempts)
	}
	settings := &fakeSettings{
		bools: map[string]bool{"report.render_pdf": false},
		ints:  map[string]int64{"report.render_max_attempts": 5},
	}
	enabled, attempts = p7RenderSettings(context.Background(), settings)
	if enabled || attempts != 5 {
		t.Fatalf("settings = %v/%d, want false/5", enabled, attempts)
	}
	outOfRange := &fakeSettings{
		bools: map[string]bool{"report.render_pdf": true},
		ints:  map[string]int64{"report.render_max_attempts": 99},
	}
	if _, attempts := p7RenderSettings(context.Background(), outOfRange); attempts != deliveryapp.DefaultRenderMaxAttempts {
		t.Fatalf("attempts = %d, want the default %d", attempts, deliveryapp.DefaultRenderMaxAttempts)
	}
}

func TestP7GCIntervalUsesSettings(t *testing.T) {
	if got := p7GCInterval(context.Background(), nil); got != storageapp.DefaultGCInterval {
		t.Fatalf("nil settings interval = %v, want %v", got, storageapp.DefaultGCInterval)
	}
	settings := &fakeSettings{ints: map[string]int64{storageapp.GCSettingIntervalMinutes: 5}}
	if got := p7GCInterval(context.Background(), settings); got != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", got)
	}
	missing := &fakeSettings{}
	if got := p7GCInterval(context.Background(), missing); got != storageapp.DefaultGCInterval {
		t.Fatalf("missing setting interval = %v, want %v", got, storageapp.DefaultGCInterval)
	}
}

func TestP7PrimaryExecutorRegistryNeverSelectsSecondary(t *testing.T) {
	primary := "http://127.0.0.1:13336"
	rows := []execution.ExecutorInstance{
		{ID: 9, Endpoint: primary + "/", Status: execution.ExecutorActive},
		{ID: 4, Endpoint: "http://127.0.0.1:13337", Status: execution.ExecutorActive, Capacity: 100},
		{ID: 2, Endpoint: primary, Status: execution.ExecutorDraining},
	}
	registry := primaryExecutorRegistry{
		registry: p7FakeExecutorLister{rows: rows},
		primary:  primary + "/",
	}
	selected, err := registry.Select(context.Background())
	if err != nil {
		t.Fatalf("Select() error = %v", err)
	}
	if selected == nil || selected.ID != 9 {
		t.Fatalf("Select() = %+v, want the primary endpoint row", selected)
	}
	if none, err := (primaryExecutorRegistry{}).Select(context.Background()); err != nil || none != nil {
		t.Fatalf("nil registry Select() = %+v/%v, want nil/nil", none, err)
	}
}

func TestP7InstanceKeyIsHostPID(t *testing.T) {
	key := p7InstanceKey()
	if key == "" {
		t.Fatal("instance key is empty")
	}
	suffix := ":" + strconv.Itoa(os.Getpid())
	if len(key) <= len(suffix) || key[len(key)-len(suffix):] != suffix {
		t.Fatalf("instance key = %q, want a hostname:pid suffix %q", key, suffix)
	}
}

func TestP7SectionOptionsIncludeAppendix(t *testing.T) {
	settings := &fakeSettings{bools: map[string]bool{"report.section_appendix": false}}
	if options := p4SectionOptions(context.Background(), settings); options.Appendix {
		t.Fatal("Appendix = true when report.section_appendix is false")
	}
	if options := p4SectionOptions(context.Background(), &fakeSettings{}); !options.Appendix {
		t.Fatal("Appendix = false when the setting is unreadable, want the enabled fallback")
	}
}

func TestP7RuntimeRejectsNilPool(t *testing.T) {
	if _, err := newP7Runtime(context.Background(), p7RuntimeDeps{}); err == nil {
		t.Fatal("newP7Runtime accepted an empty dependency set")
	}
}

func TestP7OrchestrationRejectsNilPool(t *testing.T) {
	if _, err := startP7Orchestration(context.Background(), p7OrchestrationDeps{}); err == nil {
		t.Fatal("startP7Orchestration accepted a nil pool")
	}
}

// compile-time assertions keep the composition-root adapters bound to the
// context ports they implement.
var (
	_ orchestrationapp.LeaderGate = p7FakeLeaderGate(false)
)
