package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strconv"
	"strings"
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

// fakeLeaderGate is the leader signal double of the gating tests.
type fakeLeaderGate bool

func (g fakeLeaderGate) IsLeader() bool { return bool(g) }

// fakeAudit records the audit events the knowledge adapter submits.
type fakeAudit struct {
	events []auditapp.Event
	err    error
}

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return f.err
}

// fakeSettingGetter is the settings repository double used by the raw reader.
type fakeSettingGetter struct {
	values map[string]settingsdomain.Setting
	err    error
}

func (f fakeSettingGetter) Get(_ context.Context, key string) (settingsdomain.Setting, error) {
	if f.err != nil {
		return settingsdomain.Setting{}, f.err
	}
	setting, ok := f.values[key]
	if !ok {
		return settingsdomain.Setting{}, errors.New("setting not found")
	}
	return setting, nil
}

// fakeExecutorLister is the executor roster double of the dispatch tests.
type fakeExecutorLister struct {
	rows []execution.ExecutorInstance
	err  error
}

func (f fakeExecutorLister) List(context.Context) ([]execution.ExecutorInstance, error) {
	return f.rows, f.err
}

// fakeVersionCaseSource is the version read-model double of the knowledge
// success-hook binding adapter.
type fakeVersionCaseSource struct {
	view versionapp.VersionView
	err  error
	ids  []int64
}

func (f *fakeVersionCaseSource) Get(_ context.Context, id int64) (versionapp.VersionView, error) {
	f.ids = append(f.ids, id)
	if f.err != nil {
		return versionapp.VersionView{}, f.err
	}
	return f.view, nil
}

// fakeBindingReader is the version BindingReader double of the knowledge
// system-key adapter.
type fakeBindingReader struct {
	catalog versionapp.EnvironmentSUTCatalog
	err     error
	ids     []int64
}

func (f *fakeBindingReader) ResolveEnvironment(_ context.Context, versionID int64) (versionapp.EnvironmentSUTCatalog, error) {
	f.ids = append(f.ids, versionID)
	if f.err != nil {
		return versionapp.EnvironmentSUTCatalog{}, f.err
	}
	return f.catalog, nil
}

// TestVersionSystemsProjectsEffectiveFamilies pins the v0.2.4-B rekey of the
// knowledge system-key reader: it projects EffectiveSUTs() (the single
// effective-scope projection) rather than querying the dropped version_sut
// table. Families are deduplicated and sorted; a proposed/rejected SUT
// contributes no key (R-B4); a SUT without a family is omitted.
func TestVersionSystemsProjectsEffectiveFamilies(t *testing.T) {
	shop := "shop"
	auth := "auth"
	billing := "billing"
	blank := "   "
	catalog := versionapp.EnvironmentSUTCatalog{
		EnvironmentID: 7101,
		Environment: resourceapp.EnvironmentView{
			ID: "7101",
			SUTs: []resourceapp.SUTView{
				{ID: "7001", EnvironmentID: "7101", Family: &shop, Status: "approved"},
				{ID: "7002", EnvironmentID: "7101", Family: &auth, Status: "approved"},
				{ID: "7003", EnvironmentID: "7101", Family: &shop, Status: "approved"},
				{ID: "7004", EnvironmentID: "7101", Family: &billing, Status: "proposed"},
				{ID: "7005", EnvironmentID: "7101", Family: &blank, Status: "approved"},
				{ID: "7006", EnvironmentID: "7101", Status: "approved"},
			},
		},
	}
	reader := &fakeBindingReader{catalog: catalog}
	got, err := (orchestrationVersionSystems{bindings: reader}).VersionSystemKeys(context.Background(), 10)
	if err != nil {
		t.Fatalf("VersionSystemKeys: %v", err)
	}
	want := []string{"auth", "shop"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("VersionSystemKeys = %#v, want %#v", got, want)
	}
	if len(reader.ids) != 1 || reader.ids[0] != 10 {
		t.Fatalf("binding reader queried version IDs = %v, want [10]", reader.ids)
	}

	// A non-positive version ID short-circuits without reading the binding.
	empty := &fakeBindingReader{}
	if keys, err := (orchestrationVersionSystems{bindings: empty}).VersionSystemKeys(context.Background(), 0); err != nil || keys != nil {
		t.Fatalf("VersionSystemKeys(0) = %#v/%v, want nil/nil", keys, err)
	}
	if len(empty.ids) != 0 {
		t.Fatalf("binding reader queried %v for a non-positive version ID", empty.ids)
	}

	// A binding read error propagates.
	broken := &fakeBindingReader{err: errors.New("version read failed")}
	if _, err := (orchestrationVersionSystems{bindings: broken}).VersionSystemKeys(context.Background(), 10); err == nil {
		t.Fatal("VersionSystemKeys swallowed the binding read error")
	}
}

func TestLeaderGating(t *testing.T) {
	ran := 0
	fn := func() { ran++ }

	if orchestrationRunGated(nil, fn) {
		t.Fatal("nil leader gate executed the gated work")
	}
	if orchestrationRunGated(fakeLeaderGate(false), fn) {
		t.Fatal("follower gate executed the gated work")
	}
	if ran != 0 {
		t.Fatalf("gated work ran %d times on a follower, want 0", ran)
	}
	if !orchestrationRunGated(fakeLeaderGate(true), fn) {
		t.Fatal("leader gate did not execute the gated work")
	}
	if ran != 1 {
		t.Fatalf("gated work ran %d times on the leader, want 1", ran)
	}
}

func TestKnowledgeAuditMapsContractFields(t *testing.T) {
	recorder := &fakeAudit{}
	adapter := knowledgeAudit{recorder: recorder}
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

func TestKnowledgeAuditToleratesNilSinkAndBadActor(t *testing.T) {
	if err := (knowledgeAudit{}).Record(context.Background(), "knowledge.create", 1, nil); err != nil {
		t.Fatalf("nil recorder error = %v, want nil", err)
	}
	recorder := &fakeAudit{}
	adapter := knowledgeAudit{recorder: recorder}
	if err := adapter.Record(context.Background(), "knowledge.create", 1, map[string]any{"actor_id": "not-a-number"}); err != nil {
		t.Fatalf("bad actor error = %v, want nil", err)
	}
	if recorder.events[0].ActorID != nil {
		t.Fatalf("actor = %v, want nil for an unparseable actor", recorder.events[0].ActorID)
	}
}

func TestRawSettingsReadsTypedValues(t *testing.T) {
	getter := fakeSettingGetter{values: map[string]settingsdomain.Setting{
		"number": {Key: "number", Value: json.RawMessage(`42`)},
		"text":   {Key: "text", Value: json.RawMessage(`"hello"`)},
		"flag":   {Key: "flag", Value: json.RawMessage(`true`)},
	}}
	settings := rawSettings{repo: getter}
	if value, err := settings.Int(context.Background(), "number"); err != nil || value != 42 {
		t.Fatalf("Int(number) = %d/%v, want 42/nil", value, err)
	}
	if value, err := settings.String(context.Background(), "text"); err != nil || value != "hello" {
		t.Fatalf("String(text) = %q/%v, want hello/nil", value, err)
	}
	if value, err := settings.Bool(context.Background(), "flag"); err != nil || !value {
		t.Fatalf("Bool(flag) = %v/%v, want true/nil", value, err)
	}
	if _, err := (rawSettings{}).Int(context.Background(), "number"); err == nil {
		t.Fatal("nil settings repository returned no error")
	}
}

func TestControlPlaneAdaptersFailClosedWithoutDatabase(t *testing.T) {
	ctx := context.Background()
	if _, err := (orchestrationSourceVersions{}).SourceVersionExists(ctx, 1); err == nil {
		t.Fatal("source version adapter accepted a nil pool")
	}
	if _, err := (orchestrationVersionSystems{}).VersionSystemKeys(ctx, 1); err == nil {
		t.Fatal("version system adapter accepted a nil binding reader")
	}
	lookup := orchestrationOwnerLookup{}
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

// TestVersionCaseBindingsProjectsSuccessBindingKeys pins the composition-root
// projection consumed by the knowledge success hook: route templates only (a
// page without one is omitted, see TestBindingProjectionOmitsEmptyRouteTemplate),
// uppercase/default-GET API keys, per-binding role deduplication and invalid
// resource IDs degrading to unset dimensions instead of zero IDs.
func TestVersionCaseBindingsProjectsSuccessBindingKeys(t *testing.T) {
	family := "shop"
	// v0.2.4-B: the SUT rows come from the bound environment's catalog and the
	// account roles from the version-level account subset.
	fullView := versionapp.VersionView{
		EnvironmentID: 7101,
		Binding: versionapp.EnvironmentSUTCatalog{
			EnvironmentID: 7101,
			Environment: resourceapp.EnvironmentView{
				ID: "7101",
				SUTs: []resourceapp.SUTView{{
					ID: "7001", EnvironmentID: "7101", Family: &family,
					Pages: []resourceapp.PageView{
						{ID: "501", SUTID: "7001", RouteTemplate: "/login", URL: "http://shop.test/login", Status: "approved"},
						{ID: "502", SUTID: "7001", URL: "http://shop.test/cart", Status: "approved"},
						{ID: "503", SUTID: "7001", Status: "approved"},
						{ID: "not-an-id", SUTID: "7001", RouteTemplate: "/ignored", Status: "approved"},
					},
					APIs: []resourceapp.APIView{
						{ID: "601", SUTID: "7001", Method: "post", PathTemplate: "/api/login", Status: "approved"},
						{ID: "602", SUTID: "7001", PathTemplate: "/api/items", Status: "approved"},
						{ID: "603", SUTID: "7001", Method: "GET", PathTemplate: " /api/orders ", Status: "approved"},
						{ID: "604", SUTID: "7001", Method: "GET", Status: "approved"},
						{ID: "not-an-id", SUTID: "7001", Method: "GET", PathTemplate: "/ignored", Status: "approved"},
					},
				}},
			},
			Accounts: []resourceapp.AccountView{
				{Roles: []string{"admin", " admin "}},
				{Roles: []string{"user", "admin"}},
				{Roles: []string{"", "  "}},
			},
		},
	}
	wantFull := []knowledgeadapter.VersionBinding{{
		SystemID:      7001,
		EnvironmentID: 7101,
		SystemKey:     "shop",
		Pages: []knowledgeadapter.PageBinding{
			{ID: 501, Key: "/login"},
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
		EnvironmentID: 0,
		Binding: versionapp.EnvironmentSUTCatalog{
			Environment: resourceapp.EnvironmentView{
				ID: "-5",
				SUTs: []resourceapp.SUTView{{
					ID: "not-an-id", EnvironmentID: "-5", Family: &blankFamily,
					Pages: []resourceapp.PageView{{ID: "bad", RouteTemplate: "/ignored", Status: "approved"}},
					APIs:  []resourceapp.APIView{{ID: "bad", Method: "GET", PathTemplate: "/ignored", Status: "approved"}},
				}},
			},
			Accounts: []resourceapp.AccountView{{Roles: []string{"admin"}}},
		},
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
			name:      "projects route templates (URL-only pages omitted), API keys and deduplicated roles",
			source:    &fakeVersionCaseSource{view: fullView},
			versionID: 10,
			want:      wantFull,
		},
		{
			name:      "invalid resource IDs degrade to unset dimensions",
			source:    &fakeVersionCaseSource{view: degradedView},
			versionID: 10,
			want:      wantDegraded,
		},
		{
			name:      "a version without bindings yields an empty projection",
			source:    &fakeVersionCaseSource{},
			versionID: 10,
			want:      []knowledgeadapter.VersionBinding{},
		},
		{name: "rejects a missing version source", source: nil, versionID: 10, wantErr: true},
		{name: "rejects a non-positive version ID", source: &fakeVersionCaseSource{}, versionID: 0, wantErr: true},
		{name: "propagates the version read error", source: &fakeVersionCaseSource{err: sourceErr}, versionID: 10, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := (orchestrationVersionCaseBindings{versions: tt.source}).VersionBindings(context.Background(), tt.versionID)
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
			if fake, ok := tt.source.(*fakeVersionCaseSource); ok {
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

// TestBindingProjectionOmitsEmptyRouteTemplate pins R97: the knowledge page_key
// is the route template, never the page URL. A page whose route template is
// empty (or whitespace) is omitted from the success-hook binding projection, so
// the success hook cannot sediment a URL-shaped page_key while the reader and
// MarkBindingsStale expect a route template.
func TestBindingProjectionOmitsEmptyRouteTemplate(t *testing.T) {
	view := versionapp.VersionView{
		EnvironmentID: 7101,
		Binding: versionapp.EnvironmentSUTCatalog{
			EnvironmentID: 7101,
			Environment: resourceapp.EnvironmentView{
				ID: "7101",
				SUTs: []resourceapp.SUTView{{
					ID: "7001", EnvironmentID: "7101",
					Pages: []resourceapp.PageView{
						{ID: "501", SUTID: "7001", RouteTemplate: "/login", URL: "http://shop.test/login", Status: "approved"},
						{ID: "502", SUTID: "7001", URL: "http://shop.test/cart", Status: "approved"},
						{ID: "503", SUTID: "7001", RouteTemplate: "   ", URL: "http://shop.test/checkout", Status: "approved"},
						{ID: "504", SUTID: "7001", RouteTemplate: "/products/:id", URL: "http://shop.test/products/42", Status: "approved"},
					},
				}},
			},
		},
	}
	got, err := (orchestrationVersionCaseBindings{versions: &fakeVersionCaseSource{view: view}}).
		VersionBindings(context.Background(), 10)
	if err != nil {
		t.Fatalf("VersionBindings: %v", err)
	}
	want := map[int64]string{
		501: "/login",
		504: "/products/:id",
	}
	if len(got) != 1 || len(got[0].Pages) != len(want) {
		t.Fatalf("binding projection = %#v, want exactly the two template pages", got)
	}
	for _, page := range got[0].Pages {
		key, ok := want[page.ID]
		if !ok {
			t.Fatalf("page %d was projected without a route template (key %q)", page.ID, page.Key)
		}
		if page.Key != key {
			t.Fatalf("page %d key = %q, want the route template %q", page.ID, page.Key, key)
		}
		if strings.Contains(page.Key, "http://") || strings.Contains(page.Key, "https://") {
			t.Fatalf("page %d key fell back to the page URL: %q", page.ID, page.Key)
		}
	}
}

func TestRenderQueueDrainGate(t *testing.T) {
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

func TestRenderSettingsFallsBackToDefaults(t *testing.T) {
	enabled, attempts := orchestrationRenderSettings(context.Background(), nil)
	if !enabled || attempts != deliveryapp.DefaultRenderMaxAttempts {
		t.Fatalf("nil settings = %v/%d, want true/%d", enabled, attempts, deliveryapp.DefaultRenderMaxAttempts)
	}
	settings := &fakeSettings{
		bools: map[string]bool{"report.render_pdf": false},
		ints:  map[string]int64{"report.render_max_attempts": 5},
	}
	enabled, attempts = orchestrationRenderSettings(context.Background(), settings)
	if enabled || attempts != 5 {
		t.Fatalf("settings = %v/%d, want false/5", enabled, attempts)
	}
	outOfRange := &fakeSettings{
		bools: map[string]bool{"report.render_pdf": true},
		ints:  map[string]int64{"report.render_max_attempts": 99},
	}
	if _, attempts := orchestrationRenderSettings(context.Background(), outOfRange); attempts != deliveryapp.DefaultRenderMaxAttempts {
		t.Fatalf("attempts = %d, want the default %d", attempts, deliveryapp.DefaultRenderMaxAttempts)
	}
}

func TestGCIntervalUsesSettings(t *testing.T) {
	if got := orchestrationGCInterval(context.Background(), nil); got != storageapp.DefaultGCInterval {
		t.Fatalf("nil settings interval = %v, want %v", got, storageapp.DefaultGCInterval)
	}
	settings := &fakeSettings{ints: map[string]int64{storageapp.GCSettingIntervalMinutes: 5}}
	if got := orchestrationGCInterval(context.Background(), settings); got != 5*time.Minute {
		t.Fatalf("interval = %v, want 5m", got)
	}
	missing := &fakeSettings{}
	if got := orchestrationGCInterval(context.Background(), missing); got != storageapp.DefaultGCInterval {
		t.Fatalf("missing setting interval = %v, want %v", got, storageapp.DefaultGCInterval)
	}
}

func TestPrimaryExecutorRegistryNeverSelectsSecondary(t *testing.T) {
	primary := "http://127.0.0.1:13336"
	rows := []execution.ExecutorInstance{
		{ID: 9, Endpoint: primary + "/", Status: execution.ExecutorActive},
		{ID: 4, Endpoint: "http://127.0.0.1:13337", Status: execution.ExecutorActive, Capacity: 100},
		{ID: 2, Endpoint: primary, Status: execution.ExecutorDraining},
	}
	registry := primaryExecutorRegistry{
		registry: fakeExecutorLister{rows: rows},
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

func TestInstanceKeyIsHostPID(t *testing.T) {
	key := orchestrationInstanceKey()
	if key == "" {
		t.Fatal("instance key is empty")
	}
	suffix := ":" + strconv.Itoa(os.Getpid())
	if len(key) <= len(suffix) || key[len(key)-len(suffix):] != suffix {
		t.Fatalf("instance key = %q, want a hostname:pid suffix %q", key, suffix)
	}
}

func TestReportSectionOptionsIncludeAppendix(t *testing.T) {
	settings := &fakeSettings{bools: map[string]bool{"report.section_appendix": false}}
	if options := deliverySectionOptions(context.Background(), settings); options.Appendix {
		t.Fatal("Appendix = true when report.section_appendix is false")
	}
	if options := deliverySectionOptions(context.Background(), &fakeSettings{}); !options.Appendix {
		t.Fatal("Appendix = false when the setting is unreadable, want the enabled fallback")
	}
}

func TestOrchestrationRuntimeRejectsNilPool(t *testing.T) {
	if _, err := newOrchestrationRuntime(context.Background(), orchestrationRuntimeDeps{}); err == nil {
		t.Fatal("newOrchestrationRuntime accepted an empty dependency set")
	}
}

func TestOrchestrationRejectsNilPool(t *testing.T) {
	if _, err := startOrchestration(context.Background(), orchestrationComponentsDeps{}); err == nil {
		t.Fatal("startOrchestration accepted a nil pool")
	}
}

// compile-time assertions keep the composition-root adapters bound to the
// context ports they implement.
var (
	_ orchestrationapp.LeaderGate = fakeLeaderGate(false)
)

// TestVersionCaseBindingsUseTheEffectiveScope pins v0.2.4-B review round 1
// (Important 1) for the knowledge success hook: a page/API binding key must name
// a row inside the version's EFFECTIVE scope. Keying a merely PROPOSED page would
// sediment a page_key that MarkBindingsStale can never match against an approved
// catalog row, so knowledge bound to it would look permanently fresh while the
// page it names is not part of any version under test.
func TestVersionCaseBindingsUseTheEffectiveScope(t *testing.T) {
	view := versionapp.VersionView{
		EnvironmentID: 7101,
		Binding: versionapp.EnvironmentSUTCatalog{
			EnvironmentID: 7101,
			Environment: resourceapp.EnvironmentView{
				ID: "7101",
				SUTs: []resourceapp.SUTView{{
					ID: "7001", EnvironmentID: "7101",
					Pages: []resourceapp.PageView{
						{ID: "501", SUTID: "7001", RouteTemplate: "/orders", Status: "approved"},
						{ID: "502", SUTID: "7001", RouteTemplate: "/proposed", Status: "proposed"},
						{ID: "503", SUTID: "7001", RouteTemplate: "/rejected", Status: "rejected"},
					},
					APIs: []resourceapp.APIView{
						{ID: "601", SUTID: "7001", Method: "GET", PathTemplate: "/api/orders", Status: "approved"},
						{ID: "602", SUTID: "7001", Method: "GET", PathTemplate: "/api/proposed", Status: "proposed"},
					},
				}},
			},
		},
	}

	got, err := (orchestrationVersionCaseBindings{versions: &fakeVersionCaseSource{view: view}}).
		VersionBindings(context.Background(), 10)
	if err != nil {
		t.Fatalf("VersionBindings: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("bindings = %#v, want exactly one SUT binding", got)
	}
	if len(got[0].Pages) != 1 || got[0].Pages[0].Key != "/orders" || got[0].Pages[0].ID != 501 {
		t.Fatalf("page bindings = %#v, want only the approved /orders", got[0].Pages)
	}
	if len(got[0].APIs) != 1 || got[0].APIs[0].Key != "GET /api/orders" || got[0].APIs[0].ID != 601 {
		t.Fatalf("api bindings = %#v, want only the approved GET /api/orders", got[0].APIs)
	}
}
