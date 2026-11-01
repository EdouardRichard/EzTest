package versionapp_test

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

var testTime = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 {
	s.next++
	return s.next
}

func idString(value int64) string { return strconv.FormatInt(value, 10) }

func int64Ptr(value int64) *int64 { return &value }

func stringPtr(value string) *string { return &value }

func boolPtr(value bool) *bool { return &value }

// ---------- fake catalog (resourceapp.CatalogReader) ----------

type fakeCatalog struct {
	suts         map[int64]resourceapp.SUTView
	environments map[int64]resourceapp.EnvironmentView
	pages        map[int64]resourceapp.PageView
	apis         map[int64]resourceapp.APIView
	databases    map[int64]resourceapp.DatabaseView
	accounts     map[int64]resourceapp.AccountView
	repositories map[int64]resourceapp.CodeRepositoryView
}

func newFakeCatalog() *fakeCatalog {
	catalog := &fakeCatalog{
		suts:         map[int64]resourceapp.SUTView{},
		environments: map[int64]resourceapp.EnvironmentView{},
		pages:        map[int64]resourceapp.PageView{},
		apis:         map[int64]resourceapp.APIView{},
		databases:    map[int64]resourceapp.DatabaseView{},
		accounts:     map[int64]resourceapp.AccountView{},
		repositories: map[int64]resourceapp.CodeRepositoryView{},
	}

	catalog.suts[1001] = resourceapp.SUTView{ID: "1001", Name: "Orders", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.suts[1002] = resourceapp.SUTView{ID: "1002", Name: "Billing", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.environments[1101] = resourceapp.EnvironmentView{ID: "1101", SUTID: "1001", Name: "prod", BaseURL: "https://orders.example.test", IsDefault: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.environments[1102] = resourceapp.EnvironmentView{ID: "1102", SUTID: "1002", Name: "staging", BaseURL: "https://billing.example.test", Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.pages[1301] = resourceapp.PageView{ID: "1301", SUTID: "1001", EnvironmentID: "1101", Name: "Order list", URL: "https://orders.example.test/orders", RouteTemplate: "/orders", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1302] = resourceapp.PageView{ID: "1302", SUTID: "1001", EnvironmentID: "1101", Name: "Order detail", URL: "https://orders.example.test/orders/:id", RouteTemplate: "/orders/:id", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1303] = resourceapp.PageView{ID: "1303", SUTID: "1002", EnvironmentID: "1102", Name: "Invoice list", URL: "https://billing.example.test/invoices", RouteTemplate: "/invoices", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.apis[1401] = resourceapp.APIView{ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET", PathTemplate: "/api/orders", Name: "List orders", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.apis[1402] = resourceapp.APIView{ID: "1402", SUTID: "1002", EnvironmentID: "1102", Method: "POST", PathTemplate: "/api/invoices", Name: "Create invoice", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.databases[1501] = resourceapp.DatabaseView{ID: "1501", SUTID: "1001", EnvironmentID: "1101", Name: "orders-db", DSN: "postgres://orders", Username: "orders", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.databases[1502] = resourceapp.DatabaseView{ID: "1502", SUTID: "1002", EnvironmentID: "1102", Name: "billing-db", DSN: "postgres://billing", Username: "billing", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.accounts[1601] = resourceapp.AccountView{ID: "1601", SUTID: "1001", EnvironmentID: "1101", Username: "order-admin", Roles: []string{"admin"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1602] = resourceapp.AccountView{ID: "1602", SUTID: "1002", EnvironmentID: "1102", Username: "billing-admin", Roles: []string{"admin"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}

	catalog.repositories[1201] = resourceapp.CodeRepositoryView{ID: "1201", Name: "orders-repo", Path: "C:/repos/orders", Branch: "main", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	return catalog
}

func notFound(kind string) error { return platform.NotFound(kind) }

func (f *fakeCatalog) GetSUT(_ context.Context, id int64) (resourceapp.SUTView, error) {
	view, ok := f.suts[id]
	if !ok {
		return resourceapp.SUTView{}, notFound("sut")
	}
	return view, nil
}

func (f *fakeCatalog) GetEnvironment(_ context.Context, id int64) (resourceapp.EnvironmentView, error) {
	view, ok := f.environments[id]
	if !ok {
		return resourceapp.EnvironmentView{}, notFound("environment")
	}
	return view, nil
}

func (f *fakeCatalog) GetPage(_ context.Context, id int64) (resourceapp.PageView, error) {
	view, ok := f.pages[id]
	if !ok {
		return resourceapp.PageView{}, notFound("page")
	}
	return view, nil
}

func (f *fakeCatalog) GetAPI(_ context.Context, id int64) (resourceapp.APIView, error) {
	view, ok := f.apis[id]
	if !ok {
		return resourceapp.APIView{}, notFound("api")
	}
	return view, nil
}

func (f *fakeCatalog) GetDatabase(_ context.Context, id int64) (resourceapp.DatabaseView, error) {
	view, ok := f.databases[id]
	if !ok {
		return resourceapp.DatabaseView{}, notFound("database")
	}
	return view, nil
}

func (f *fakeCatalog) GetAccount(_ context.Context, id int64) (resourceapp.AccountView, error) {
	view, ok := f.accounts[id]
	if !ok {
		return resourceapp.AccountView{}, notFound("account")
	}
	return view, nil
}

func (f *fakeCatalog) GetRepository(_ context.Context, id int64) (resourceapp.CodeRepositoryView, error) {
	view, ok := f.repositories[id]
	if !ok {
		return resourceapp.CodeRepositoryView{}, notFound("code repository")
	}
	return view, nil
}

var _ resourceapp.CatalogReader = (*fakeCatalog)(nil)

// ---------- fake version repository ----------

type fakeRepo struct {
	versions           map[int64]version.Version
	bindings           map[int64][]version.Binding
	createErr          error
	stats              version.VersionStats
	initialWithContent []int64
	lastInitialLimit   int
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{versions: map[int64]version.Version{}, bindings: map[int64][]version.Binding{}}
}

func (f *fakeRepo) Create(_ context.Context, v version.Version, bindings []version.Binding) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.versions[v.ID] = v
	f.bindings[v.ID] = bindings
	return nil
}

func (f *fakeRepo) Get(_ context.Context, id int64) (version.Version, []version.Binding, error) {
	v, ok := f.versions[id]
	if !ok {
		return version.Version{}, nil, version.ErrNotFound
	}
	bindings := f.bindings[id]
	copied := make([]version.Binding, len(bindings))
	copy(copied, bindings)
	return v, copied, nil
}

func (f *fakeRepo) List(_ context.Context, limit int, beforeID *int64) ([]version.Summary, error) {
	ids := make([]int64, 0, len(f.versions))
	for id := range f.versions {
		if beforeID != nil && id >= *beforeID {
			continue
		}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] > ids[j] })
	if len(ids) > limit {
		ids = ids[:limit]
	}
	summaries := make([]version.Summary, 0, len(ids))
	for _, id := range ids {
		v := f.versions[id]
		summaries = append(summaries, version.Summary{
			ID: v.ID, Title: v.Title, Status: v.Status,
			SUTCount:  len(f.bindings[id]),
			CreatedAt: v.CreatedAt, UpdatedAt: v.UpdatedAt,
		})
	}
	return summaries, nil
}

func (f *fakeRepo) Update(_ context.Context, v version.Version) (version.Version, error) {
	if _, ok := f.versions[v.ID]; !ok {
		return version.Version{}, version.ErrNotFound
	}
	f.versions[v.ID] = v
	return v, nil
}

func (f *fakeRepo) MarkRunning(_ context.Context, id int64, updatedAt time.Time) (bool, error) {
	v, ok := f.versions[id]
	if !ok {
		return false, nil
	}
	if v.Status != version.StatusInitial {
		return false, nil
	}
	v.Status = version.StatusRunning
	v.UpdatedAt = updatedAt
	f.versions[id] = v
	return true, nil
}

func (f *fakeRepo) Reopen(_ context.Context, id int64, updatedAt time.Time) (bool, error) {
	v, ok := f.versions[id]
	if !ok {
		return false, nil
	}
	if v.Status != version.StatusCompleted {
		return false, nil
	}
	v.Status = version.StatusRunning
	v.ClosedAt = nil
	v.CloseReason = ""
	v.CloseRunID = nil
	v.UpdatedAt = updatedAt
	f.versions[id] = v
	return true, nil
}

func (f *fakeRepo) Stats(_ context.Context, _ int64) (version.VersionStats, error) {
	return f.stats, nil
}

func (f *fakeRepo) SetCodeBaselineCommit(_ context.Context, versionSUTID int64, commitSHA string) error {
	for versionID, bindings := range f.bindings {
		for index := range bindings {
			if bindings[index].ID == versionSUTID {
				bindings[index].CodeBaselineCommit = commitSHA
				f.bindings[versionID] = bindings
				return nil
			}
		}
	}
	return version.ErrNotFound
}

func (f *fakeRepo) ListInitialVersionsWithContent(_ context.Context, limit int) ([]int64, error) {
	f.lastInitialLimit = limit
	return f.initialWithContent, nil
}

func (f *fakeRepo) SUTInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			if binding.SUTID == id {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) EnvironmentInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			if binding.EnvironmentID == id {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) PageInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			for _, pageID := range binding.Scope.Pages {
				if pageID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) APIInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			for _, apiID := range binding.Scope.APIs {
				if apiID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) DatabaseInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			for _, databaseID := range binding.DatabaseIDs {
				if databaseID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) AccountInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			for _, account := range binding.Accounts {
				if account.AccountID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) RepositoryInUse(_ context.Context, id int64) (bool, error) {
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			if binding.CodeRepositoryID != nil && *binding.CodeRepositoryID == id {
				return true, nil
			}
		}
	}
	return false, nil
}

var _ version.Repository = (*fakeRepo)(nil)

// ---------- fake code baseline resolver ----------

const codeBaselineCommit = "0123456789abcdef0123456789abcdef01234567"

type fakeCodeBaselineResolver struct {
	commit string
	err    error
	calls  []resourceapp.CodeRepositoryView
}

func (f *fakeCodeBaselineResolver) ResolveHeadCommit(_ context.Context, repo resourceapp.CodeRepositoryView) (string, error) {
	f.calls = append(f.calls, repo)
	if f.err != nil {
		return "", f.err
	}
	return f.commit, nil
}

var _ versionapp.CodeBaselineResolver = (*fakeCodeBaselineResolver)(nil)

// ---------- fake audit / publisher ----------

type fakeAudit struct{ events []auditapp.Event }

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

type fakePublisher struct{ events []eventingapp.Event }

func (f *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	f.events = append(f.events, event)
	return nil
}

// ---------- helpers ----------

type testEnv struct {
	svc       *versionapp.Service
	repo      *fakeRepo
	catalog   *fakeCatalog
	audit     *fakeAudit
	publisher *fakePublisher
	resolver  *fakeCodeBaselineResolver
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	return newTestEnvWithResolver(t, &fakeCodeBaselineResolver{commit: codeBaselineCommit})
}

func newTestEnvWithResolver(t *testing.T, resolver versionapp.CodeBaselineResolver) *testEnv {
	t.Helper()
	repo := newFakeRepo()
	catalog := newFakeCatalog()
	audit := &fakeAudit{}
	publisher := &fakePublisher{}
	svc := versionapp.New(repo, catalog, &seqIDs{}, fixedClock{at: testTime}, audit, publisher, resolver)
	fake, _ := resolver.(*fakeCodeBaselineResolver)
	return &testEnv{svc: svc, repo: repo, catalog: catalog, audit: audit, publisher: publisher, resolver: fake}
}

func requestContext() context.Context {
	ctx := context.Background()
	return requestid.With(ctx, "req_version_tests")
}

func validCommand() versionapp.CreateCommand {
	return versionapp.CreateCommand{
		Title: "Orders Q4",
		Bindings: []versionapp.BindingInput{
			{
				SUTID:            1001,
				EnvironmentID:    1101,
				CodeRepositoryID: int64Ptr(1201),
				PageIDs:          []int64{1301},
				APIIDs:           []int64{1401},
				DatabaseIDs:      []int64{1501},
				Accounts:         []versionapp.AccountInput{{AccountID: 1601, Role: "order_admin"}},
			},
		},
	}
}

func apiError(t *testing.T, err error) *platform.Error {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a platform error", err)
	}
	return apiErr
}

func fieldName(t *testing.T, err error) string {
	t.Helper()
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", apiErr.Status)
	}
	field, _ := apiErr.Details["field"].(string)
	if field == "" {
		t.Fatalf("error details have no field: %#v", apiErr.Details)
	}
	return field
}

func createVersion(t *testing.T, env *testEnv, title string) versionapp.VersionView {
	t.Helper()
	cmd := validCommand()
	cmd.Title = title
	view, err := env.svc.Create(requestContext(), cmd, 42)
	if err != nil {
		t.Fatalf("Create(%q): %v", title, err)
	}
	return view
}

func findBinding(t *testing.T, bindings []version.Binding, sutID int64) version.Binding {
	t.Helper()
	for _, binding := range bindings {
		if binding.SUTID == sutID {
			return binding
		}
	}
	t.Fatalf("binding for SUT %d not stored", sutID)
	return version.Binding{}
}

// ---------- tests ----------

func TestCreatePersistsMultiSUTBindingsAndEmitsAuditAndEvents(t *testing.T) {
	env := newTestEnv(t)
	ctx := requestContext()

	cmd := versionapp.CreateCommand{
		Title: "  Orders & Billing Q4  ",
		Bindings: []versionapp.BindingInput{
			{
				SUTID:            1001,
				EnvironmentID:    1101,
				CodeRepositoryID: int64Ptr(1201),
				PageIDs:          []int64{1301, 1302, 1301},
				APIIDs:           []int64{1401, 1401},
				DatabaseIDs:      []int64{1501},
				Accounts: []versionapp.AccountInput{
					{AccountID: 1601, Role: "order_admin"},
					{AccountID: 1601, Role: "order_admin"},
				},
			},
			{
				SUTID:         1002,
				EnvironmentID: 1102,
				PageIDs:       []int64{1303},
				APIIDs:        []int64{1402},
				DatabaseIDs:   []int64{1502},
				Accounts:      []versionapp.AccountInput{{AccountID: 1602}},
			},
		},
	}

	view, err := env.svc.Create(ctx, cmd, 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if view.ID <= 0 {
		t.Fatalf("id = %d", view.ID)
	}
	if view.Title != "Orders & Billing Q4" {
		t.Fatalf("title = %q", view.Title)
	}
	if view.Status != "initial" || view.AutoDesignNewRequirements {
		t.Fatalf("status/auto = %q/%v", view.Status, view.AutoDesignNewRequirements)
	}
	if view.CreatedBy != 42 || !view.CreatedAt.Equal(testTime) || !view.UpdatedAt.Equal(testTime) {
		t.Fatalf("created = %#v", view)
	}
	if len(view.Bindings) != 2 {
		t.Fatalf("bindings = %d", len(view.Bindings))
	}

	first := findBindingView(t, view.Bindings, "1001")
	if first.SUT.Name != "Orders" || first.Environment.Name != "prod" {
		t.Fatalf("binding sut/env = %#v/%#v", first.SUT, first.Environment)
	}
	if first.Repository == nil || first.Repository.Name != "orders-repo" {
		t.Fatalf("repository = %#v", first.Repository)
	}
	if first.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("code baseline commit = %q, want %q", first.CodeBaselineCommit, codeBaselineCommit)
	}
	if len(first.Pages) != 2 || first.Pages[0].ID != "1301" || first.Pages[1].ID != "1302" {
		t.Fatalf("pages = %#v", first.Pages)
	}
	if len(first.APIs) != 1 || first.APIs[0].ID != "1401" {
		t.Fatalf("apis = %#v", first.APIs)
	}
	if len(first.Databases) != 1 || first.Databases[0].ID != "1501" {
		t.Fatalf("databases = %#v", first.Databases)
	}
	if len(first.Accounts) != 1 || first.Accounts[0].ID != "1601" {
		t.Fatalf("accounts = %#v", first.Accounts)
	}

	second := findBindingView(t, view.Bindings, "1002")
	if second.Repository != nil {
		t.Fatalf("repository = %#v, want nil", second.Repository)
	}
	if len(second.Pages) != 1 || second.Pages[0].ID != "1303" {
		t.Fatalf("pages = %#v", second.Pages)
	}

	storedVersion, storedBindings, err := env.repo.Get(ctx, view.ID)
	if err != nil {
		t.Fatalf("Get stored: %v", err)
	}
	if storedVersion.Title != "Orders & Billing Q4" {
		t.Fatalf("stored title = %q", storedVersion.Title)
	}
	firstStored := findBinding(t, storedBindings, 1001)
	if got := firstStored.Scope.Pages; len(got) != 2 || got[0] != 1301 || got[1] != 1302 {
		t.Fatalf("scope pages = %#v", got)
	}
	if got := firstStored.Scope.APIs; len(got) != 1 || got[0] != 1401 {
		t.Fatalf("scope apis = %#v", got)
	}
	if got := firstStored.DatabaseIDs; len(got) != 1 || got[0] != 1501 {
		t.Fatalf("database ids = %#v", got)
	}
	if len(firstStored.Accounts) != 1 || firstStored.Accounts[0].AccountID != 1601 || firstStored.Accounts[0].Role != "order_admin" {
		t.Fatalf("accounts = %#v", firstStored.Accounts)
	}
	secondStored := findBinding(t, storedBindings, 1002)
	if got := secondStored.Scope.APIs; len(got) != 1 || got[0] != 1402 {
		t.Fatalf("second scope apis = %#v", got)
	}
	if stored := findBinding(t, storedBindings, 1001); stored.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("stored code baseline commit = %q, want %q", stored.CodeBaselineCommit, codeBaselineCommit)
	}
	if len(env.resolver.calls) != 1 || env.resolver.calls[0].ID != "1201" || env.resolver.calls[0].Branch != "main" {
		t.Fatalf("resolver calls = %#v, want one call for repository 1201", env.resolver.calls)
	}

	if len(env.audit.events) != 1 {
		t.Fatalf("audit events = %d", len(env.audit.events))
	}
	entry := env.audit.events[0]
	if entry.Action != "version.created" || entry.TargetKind != "version" {
		t.Fatalf("audit = %#v", entry)
	}
	if entry.TargetID != idString(view.ID) {
		t.Fatalf("audit target = %q", entry.TargetID)
	}
	if entry.ActorID == nil || *entry.ActorID != 42 || entry.RequestID != "req_version_tests" {
		t.Fatalf("audit actor/request = %#v", entry)
	}

	if len(env.publisher.events) != 2 {
		t.Fatalf("events = %d", len(env.publisher.events))
	}
	channels := map[string]eventingapp.Event{}
	for _, event := range env.publisher.events {
		if event.Type != "version.created" {
			t.Fatalf("event type = %q", event.Type)
		}
		channels[event.Channel] = event
	}
	for _, channel := range []string{"version:" + idString(view.ID), "system"} {
		event, ok := channels[channel]
		if !ok {
			t.Fatalf("missing event on channel %q: %#v", channel, channels)
		}
		if event.Scope["version_id"] != idString(view.ID) {
			t.Fatalf("scope = %#v", event.Scope)
		}
		if event.Payload["title"] != "Orders & Billing Q4" || event.Payload["status"] != "initial" {
			t.Fatalf("payload = %#v", event.Payload)
		}
		count, ok := event.Payload["sut_count"].(int)
		if !ok || count != 2 {
			t.Fatalf("sut_count = %#v", event.Payload["sut_count"])
		}
		if event.ActorID == nil || *event.ActorID != 42 || event.RequestID != "req_version_tests" {
			t.Fatalf("event actor/request = %#v", event)
		}
	}
}

func findBindingView(t *testing.T, bindings []versionapp.BindingView, sutID string) versionapp.BindingView {
	t.Helper()
	for _, binding := range bindings {
		if binding.SUT.ID == sutID {
			return binding
		}
	}
	t.Fatalf("binding view for SUT %q not found", sutID)
	return versionapp.BindingView{}
}

func TestCreateValidatesBindings(t *testing.T) {
	tooMany := make([]versionapp.BindingInput, 0, version.MaxBindings+1)
	for i := 0; i < version.MaxBindings+1; i++ {
		tooMany = append(tooMany, validCommand().Bindings[0])
	}

	cases := []struct {
		name      string
		mutate    func(*versionapp.CreateCommand)
		wantField string
	}{
		{"blank title", func(cmd *versionapp.CreateCommand) { cmd.Title = "   " }, "title"},
		{"no bindings", func(cmd *versionapp.CreateCommand) { cmd.Bindings = nil }, "bindings"},
		{"too many bindings", func(cmd *versionapp.CreateCommand) { cmd.Bindings = tooMany }, "bindings"},
		{"duplicate sut", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings = append(cmd.Bindings, cmd.Bindings[0])
		}, "bindings[1].sut_id"},
		{"unknown sut", func(cmd *versionapp.CreateCommand) { cmd.Bindings[0].SUTID = 9999 }, "bindings[0].sut_id"},
		{"environment of another sut", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].EnvironmentID = 1102
		}, "bindings[0].environment_id"},
		{"page of another sut", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].PageIDs = []int64{1303}
		}, "bindings[0].page_ids"},
		{"api of another environment", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].APIIDs = []int64{1402}
		}, "bindings[0].api_ids"},
		{"database of another sut", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].DatabaseIDs = []int64{1502}
		}, "bindings[0].database_ids"},
		{"account of another sut", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].Accounts = []versionapp.AccountInput{{AccountID: 1602}}
		}, "bindings[0].accounts"},
		{"invalid repository", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].CodeRepositoryID = int64Ptr(9999)
		}, "bindings[0].code_repository_id"},
		{"zero page id", func(cmd *versionapp.CreateCommand) {
			cmd.Bindings[0].PageIDs = []int64{0}
		}, "bindings[0].page_ids"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnv(t)
			cmd := validCommand()
			tc.mutate(&cmd)
			if _, err := env.svc.Create(requestContext(), cmd, 42); err == nil {
				t.Fatal("Create accepted invalid input")
			} else if field := fieldName(t, err); field != tc.wantField {
				t.Fatalf("field = %q, want %q", field, tc.wantField)
			}
			if len(env.repo.versions) != 0 {
				t.Fatalf("invalid input persisted: %#v", env.repo.versions)
			}
			if len(env.audit.events) != 0 || len(env.publisher.events) != 0 {
				t.Fatalf("invalid input recorded side effects: %d/%d", len(env.audit.events), len(env.publisher.events))
			}
		})
	}
}

func TestCreateLeavesCodeBaselineEmptyWhenResolutionFails(t *testing.T) {
	cases := []struct {
		name     string
		resolver *fakeCodeBaselineResolver
	}{
		{"resolver error", &fakeCodeBaselineResolver{err: errors.New("git rev-parse failed")}},
		{"malformed commit", &fakeCodeBaselineResolver{commit: "not-a-commit"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newTestEnvWithResolver(t, tc.resolver)
			view, err := env.svc.Create(requestContext(), validCommand(), 42)
			if err != nil {
				t.Fatalf("Create must not be blocked by baseline resolution: %v", err)
			}
			if len(view.Bindings) != 1 || view.Bindings[0].CodeBaselineCommit != "" {
				t.Fatalf("binding view = %#v, want an empty code baseline", view.Bindings)
			}
			stored := findBinding(t, env.repo.bindings[view.ID], 1001)
			if stored.CodeBaselineCommit != "" {
				t.Fatalf("stored commit = %q, want empty", stored.CodeBaselineCommit)
			}

			created := 0
			warnings := 0
			for _, entry := range env.audit.events {
				switch entry.Action {
				case "version.created":
					created++
				case "version.code_baseline_unresolved":
					warnings++
					if entry.TargetKind != "version" || entry.TargetID != idString(view.ID) {
						t.Fatalf("warning audit target = %#v", entry)
					}
					if entry.Metadata["version_sut_id"] != idString(stored.ID) {
						t.Fatalf("warning metadata = %#v", entry.Metadata)
					}
				}
			}
			if created != 1 || warnings != 1 {
				t.Fatalf("audit actions = %#v, want one version.created and one warning", env.audit.events)
			}
			if len(env.publisher.events) != 2 {
				t.Fatalf("version.created events = %d, want 2", len(env.publisher.events))
			}
		})
	}
}

func TestCreateSkipsBaselineResolutionWithoutRepository(t *testing.T) {
	env := newTestEnv(t)
	cmd := validCommand()
	cmd.Bindings[0].CodeRepositoryID = nil
	if _, err := env.svc.Create(requestContext(), cmd, 42); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(env.resolver.calls) != 0 {
		t.Fatalf("resolver calls = %#v, want none for a binding without repository", env.resolver.calls)
	}
}

func TestCreateMapsDuplicatePersistenceError(t *testing.T) {
	env := newTestEnv(t)
	env.repo.createErr = version.ErrDuplicate
	if _, err := env.svc.Create(requestContext(), validCommand(), 42); err == nil {
		t.Fatal("Create swallowed the repository error")
	} else if got := apiError(t, err).Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	if len(env.audit.events) != 0 || len(env.publisher.events) != 0 {
		t.Fatal("failed create recorded side effects")
	}
}

func TestVersionViewExposesCloseProjection(t *testing.T) {
	env := newTestEnv(t)
	view := createVersion(t, env, "Orders Q4")
	closedAt := testTime.Add(time.Hour)
	stored := env.repo.versions[view.ID]
	stored.Status = version.StatusCompleted
	stored.ClosedAt = &closedAt
	stored.CloseReason = "all delivery work is done"
	env.repo.versions[view.ID] = stored

	got, err := env.svc.Get(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != string(version.StatusCompleted) {
		t.Fatalf("status = %q", got.Status)
	}
	if got.ClosedAt == nil || !got.ClosedAt.Equal(closedAt) {
		t.Fatalf("closed_at = %v", got.ClosedAt)
	}
	if got.CloseReason != "all delivery work is done" {
		t.Fatalf("close_reason = %q", got.CloseReason)
	}

	stored.Status = version.StatusRunning
	stored.ClosedAt = nil
	stored.CloseReason = ""
	env.repo.versions[view.ID] = stored
	reopened, err := env.svc.Get(requestContext(), view.ID)
	if err != nil {
		t.Fatalf("Get reopened: %v", err)
	}
	if reopened.ClosedAt != nil || reopened.CloseReason != "" {
		t.Fatalf("reopened close projection = %v/%q", reopened.ClosedAt, reopened.CloseReason)
	}
}

func TestGetReturnsNestedResourceViews(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	view, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if view.ID != created.ID || view.Title != "Orders Q4" || view.Status != "initial" {
		t.Fatalf("view = %#v", view)
	}
	if len(view.Bindings) != 1 {
		t.Fatalf("bindings = %d", len(view.Bindings))
	}
	binding := view.Bindings[0]
	if binding.SUT.ID != "1001" || binding.SUT.Name != "Orders" {
		t.Fatalf("sut = %#v", binding.SUT)
	}
	if binding.Environment.ID != "1101" || binding.Environment.SUTID != "1001" {
		t.Fatalf("environment = %#v", binding.Environment)
	}
	if binding.Repository == nil || binding.Repository.ID != "1201" {
		t.Fatalf("repository = %#v", binding.Repository)
	}
	if len(binding.Pages) != 1 || binding.Pages[0].RouteTemplate != "/orders" {
		t.Fatalf("pages = %#v", binding.Pages)
	}
	if len(binding.APIs) != 1 || binding.APIs[0].Method != "GET" {
		t.Fatalf("apis = %#v", binding.APIs)
	}
	if len(binding.Databases) != 1 || binding.Databases[0].Name != "orders-db" {
		t.Fatalf("databases = %#v", binding.Databases)
	}
	if len(binding.Accounts) != 1 || binding.Accounts[0].Username != "order-admin" {
		t.Fatalf("accounts = %#v", binding.Accounts)
	}

	if _, err := env.svc.Get(requestContext(), 9999); err == nil {
		t.Fatal("missing version returned")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

func TestListReturnsCursorSummaries(t *testing.T) {
	env := newTestEnv(t)
	first := createVersion(t, env, "Version one")
	second := createVersion(t, env, "Version two")
	third := createVersion(t, env, "Version three")

	page, hasMore, err := env.svc.List(requestContext(), 2, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if !hasMore || len(page) != 2 {
		t.Fatalf("page = %#v, hasMore = %v", page, hasMore)
	}
	if page[0].ID != third.ID || page[1].ID != second.ID {
		t.Fatalf("page order = %#v", page)
	}
	if page[0].SUTCount != 1 {
		t.Fatalf("sut count = %d", page[0].SUTCount)
	}
	if page[0].Title != "Version three" || page[0].Status != "initial" {
		t.Fatalf("summary = %#v", page[0])
	}

	next, more, err := env.svc.List(requestContext(), 2, &page[1].ID)
	if err != nil {
		t.Fatalf("List next: %v", err)
	}
	if more || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next = %#v, hasMore = %v", next, more)
	}

	all, more, err := env.svc.List(requestContext(), 0, nil)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if more || len(all) != 3 {
		t.Fatalf("all = %#v, hasMore = %v", all, more)
	}
}

func TestUpdateOnlyChangesTitleAndAutoFlag(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Original")

	updated, err := env.svc.Update(requestContext(), created.ID, versionapp.Patch{
		Title:                     stringPtr("  Renamed  "),
		AutoDesignNewRequirements: boolPtr(true),
	}, 42)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Title != "Renamed" || !updated.AutoDesignNewRequirements {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.Status != "initial" {
		t.Fatalf("status = %q, want initial", updated.Status)
	}

	stored, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get after update: %v", err)
	}
	if stored.Title != "Renamed" || !stored.AutoDesignNewRequirements || stored.Status != "initial" {
		t.Fatalf("stored = %#v", stored)
	}

	if len(env.audit.events) != 2 {
		t.Fatalf("audit events = %d", len(env.audit.events))
	}
	entry := env.audit.events[1]
	if entry.Action != "version.updated" || entry.TargetKind != "version" || entry.TargetID != idString(created.ID) {
		t.Fatalf("audit = %#v", entry)
	}
	if len(env.publisher.events) != 2 {
		t.Fatalf("update must not publish an event beyond the two version.created events, got %d", len(env.publisher.events))
	}

	if _, err := env.svc.Update(requestContext(), 9999, versionapp.Patch{Title: stringPtr("Nope")}, 42); err == nil {
		t.Fatal("missing version updated")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
	if _, err := env.svc.Update(requestContext(), created.ID, versionapp.Patch{}, 42); err == nil {
		t.Fatal("empty patch accepted")
	} else if field := fieldName(t, err); field != "body" {
		t.Fatalf("field = %q, want body", field)
	}
	if _, err := env.svc.Update(requestContext(), created.ID, versionapp.Patch{Title: stringPtr("   ")}, 42); err == nil {
		t.Fatal("blank title accepted")
	} else if field := fieldName(t, err); field != "title" {
		t.Fatalf("field = %q, want title", field)
	}
	if len(env.audit.events) != 2 {
		t.Fatalf("rejected updates recorded audit: %#v", env.audit.events)
	}
}

func TestServiceImplementsUsageChecker(t *testing.T) {
	env := newTestEnv(t)
	var usage resourceapp.UsageChecker = env.svc

	before := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
	}{
		{"sut", usage.SUTInUse},
		{"environment", usage.EnvironmentInUse},
		{"page", usage.PageInUse},
		{"api", usage.APIInUse},
		{"database", usage.DatabaseInUse},
		{"account", usage.AccountInUse},
		{"repository", usage.RepositoryInUse},
	}
	for _, check := range before {
		if used, err := check.check(requestContext(), 9999); err != nil || used {
			t.Fatalf("%s before create = %v/%v", check.name, used, err)
		}
	}

	created := createVersion(t, env, "Version")

	cases := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"sut bound", usage.SUTInUse, 1001, true},
		{"sut unbound", usage.SUTInUse, 1002, false},
		{"environment bound", usage.EnvironmentInUse, 1101, true},
		{"environment unbound", usage.EnvironmentInUse, 1102, false},
		{"page in scope", usage.PageInUse, 1301, true},
		{"page outside scope", usage.PageInUse, 1302, false},
		{"api in scope", usage.APIInUse, 1401, true},
		{"api outside scope", usage.APIInUse, 1402, false},
		{"database bound", usage.DatabaseInUse, 1501, true},
		{"database unbound", usage.DatabaseInUse, 1502, false},
		{"account bound", usage.AccountInUse, 1601, true},
		{"account unbound", usage.AccountInUse, 1602, false},
		{"repository bound", usage.RepositoryInUse, 1201, true},
		{"repository unbound", usage.RepositoryInUse, 1202, false},
	}
	for _, tc := range cases {
		used, err := tc.check(requestContext(), tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if used != tc.want {
			t.Fatalf("%s(%d) = %v, want %v", tc.name, tc.id, used, tc.want)
		}
	}

	// The created version is listed with one SUT binding.
	page, _, err := env.svc.List(requestContext(), 10, nil)
	if err != nil || len(page) != 1 || page[0].ID != created.ID || page[0].SUTCount != 1 {
		t.Fatalf("list = %#v, %v", page, err)
	}
}

func TestReopenCompletedVersionPublishesStatusChange(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Reopen me")
	stored := env.repo.versions[created.ID]
	stored.Status = version.StatusCompleted
	closedAt := testTime
	stored.ClosedAt = &closedAt
	stored.CloseReason = "done"
	env.repo.versions[created.ID] = stored
	env.publisher.events = nil
	env.audit.events = nil

	if err := env.svc.Reopen(requestContext(), created.ID); err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	reopened := env.repo.versions[created.ID]
	if reopened.Status != version.StatusRunning {
		t.Fatalf("status = %s, want running", reopened.Status)
	}
	if reopened.ClosedAt != nil || reopened.CloseReason != "" {
		t.Fatalf("closed fields not cleared: %+v", reopened)
	}
	changed := false
	for _, event := range env.publisher.events {
		if event.Type != "version.status_changed" {
			continue
		}
		if event.Payload["from"] != version.StatusCompleted || event.Payload["to"] != version.StatusRunning {
			t.Fatalf("status_changed payload = %+v", event.Payload)
		}
		changed = true
	}
	if !changed {
		t.Fatal("version.status_changed completed->running not published")
	}
	audited := false
	for _, event := range env.audit.events {
		if event.Action == "version.reopened" {
			audited = true
		}
	}
	if !audited {
		t.Fatal("version.reopened audit entry missing")
	}

	// Idempotent: a non-completed version publishes nothing new.
	eventsBefore := len(env.publisher.events)
	if err := env.svc.Reopen(requestContext(), created.ID); err != nil {
		t.Fatalf("second Reopen: %v", err)
	}
	if len(env.publisher.events) != eventsBefore {
		t.Fatal("reopen of a running version must not publish status changes")
	}
}

func TestReopenMissingVersion(t *testing.T) {
	env := newTestEnv(t)
	err := env.svc.Reopen(requestContext(), 999999)
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) || apiErr.Code != "not_found" {
		t.Fatalf("err = %v, want not_found", err)
	}
}
