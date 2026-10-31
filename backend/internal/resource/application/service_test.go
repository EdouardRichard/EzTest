package resourceapp

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// fixedClock is a deterministic clock.Clock.
type fixedClock struct{ at time.Time }

func (f fixedClock) Now() time.Time { return f.at }

// seqIDs is a deterministic id.Generator.
type seqIDs struct{ next int64 }

func (s *seqIDs) Next() int64 { s.next++; return s.next }

// fakeAudit records audit events.
type fakeAudit struct{ entries []auditapp.Event }

func (f *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	f.entries = append(f.entries, event)
	return nil
}

// fakeEvents records published events.
type fakeEvents struct{ published []eventingapp.Event }

func (f *fakeEvents) Publish(_ context.Context, event eventingapp.Event) error {
	f.published = append(f.published, event)
	return nil
}

// fakeUsage answers the version-binding usage checks.
type fakeUsage struct {
	inUse map[string]bool
	err   error
	calls []string
}

func (f *fakeUsage) check(kind string) (bool, error) {
	f.calls = append(f.calls, kind)
	if f.err != nil {
		return false, f.err
	}
	return f.inUse[kind], nil
}

func (f *fakeUsage) SUTInUse(context.Context, int64) (bool, error) { return f.check("sut") }
func (f *fakeUsage) EnvironmentInUse(context.Context, int64) (bool, error) {
	return f.check("environment")
}
func (f *fakeUsage) PageInUse(context.Context, int64) (bool, error)     { return f.check("page") }
func (f *fakeUsage) APIInUse(context.Context, int64) (bool, error)      { return f.check("api") }
func (f *fakeUsage) DatabaseInUse(context.Context, int64) (bool, error) { return f.check("database") }
func (f *fakeUsage) AccountInUse(context.Context, int64) (bool, error)  { return f.check("account") }
func (f *fakeUsage) RepositoryInUse(context.Context, int64) (bool, error) {
	return f.check("repository")
}

// fakeRepo is an in-memory resource.Repository with the same uniqueness
// constraints as the PostgreSQL schema.
type fakeRepo struct {
	suts         []resource.SUT
	environments []resource.Environment
	pages        []resource.Page
	apis         []resource.API
	databases    []resource.TargetDatabase
	accounts     []resource.TestAccount
	repositories []resource.CodeRepository

	lastSUTLimit    int
	lastSUTBefore   *int64
	lastRepoLimit   int
	lastRepoBefore  *int64
	createPageCalls int
}

func (f *fakeRepo) ListSUTs(_ context.Context, limit int, beforeID *int64) ([]resource.SUT, error) {
	f.lastSUTLimit = limit
	f.lastSUTBefore = beforeID
	out := make([]resource.SUT, 0, len(f.suts))
	for _, item := range f.suts {
		if beforeID == nil || item.ID < *beforeID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) GetSUT(_ context.Context, id int64) (resource.SUT, error) {
	for _, item := range f.suts {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.SUT{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateSUT(_ context.Context, sut resource.SUT) (resource.SUT, error) {
	for _, item := range f.suts {
		if strings.EqualFold(item.Name, sut.Name) {
			return resource.SUT{}, resource.ErrDuplicate
		}
	}
	f.suts = append(f.suts, sut)
	return sut, nil
}

func (f *fakeRepo) UpdateSUT(_ context.Context, sut resource.SUT) (resource.SUT, error) {
	for i, item := range f.suts {
		if item.ID != sut.ID {
			continue
		}
		for _, other := range f.suts {
			if other.ID != sut.ID && strings.EqualFold(other.Name, sut.Name) {
				return resource.SUT{}, resource.ErrDuplicate
			}
		}
		f.suts[i] = sut
		return sut, nil
	}
	return resource.SUT{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteSUT(_ context.Context, id int64) error {
	for i, item := range f.suts {
		if item.ID == id {
			f.suts = append(f.suts[:i], f.suts[i+1:]...)
			f.deleteBySUT(id)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) deleteBySUT(id int64) {
	f.environments = filterSlice(f.environments, func(item resource.Environment) bool { return item.SUTID != id })
	f.pages = filterSlice(f.pages, func(item resource.Page) bool { return item.SUTID != id })
	f.apis = filterSlice(f.apis, func(item resource.API) bool { return item.SUTID != id })
	f.databases = filterSlice(f.databases, func(item resource.TargetDatabase) bool { return item.SUTID != id })
	f.accounts = filterSlice(f.accounts, func(item resource.TestAccount) bool { return item.SUTID != id })
}

func (f *fakeRepo) ListEnvironments(_ context.Context, sutID int64) ([]resource.Environment, error) {
	out := make([]resource.Environment, 0)
	for _, item := range f.environments {
		if item.SUTID == sutID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRepo) GetEnvironment(_ context.Context, id int64) (resource.Environment, error) {
	for _, item := range f.environments {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.Environment{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateEnvironment(_ context.Context, environment resource.Environment) (resource.Environment, error) {
	for _, item := range f.environments {
		if item.SUTID == environment.SUTID && strings.EqualFold(item.Name, environment.Name) {
			return resource.Environment{}, resource.ErrDuplicate
		}
	}
	f.environments = append(f.environments, environment)
	return environment, nil
}

func (f *fakeRepo) UpdateEnvironment(_ context.Context, environment resource.Environment) (resource.Environment, error) {
	for i, item := range f.environments {
		if item.ID != environment.ID {
			continue
		}
		for _, other := range f.environments {
			if other.ID != environment.ID && other.SUTID == environment.SUTID && strings.EqualFold(other.Name, environment.Name) {
				return resource.Environment{}, resource.ErrDuplicate
			}
		}
		f.environments[i] = environment
		return environment, nil
	}
	return resource.Environment{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteEnvironment(_ context.Context, id int64) error {
	for i, item := range f.environments {
		if item.ID == id {
			f.environments = append(f.environments[:i], f.environments[i+1:]...)
			f.pages = filterSlice(f.pages, func(page resource.Page) bool { return page.EnvironmentID != id })
			f.apis = filterSlice(f.apis, func(api resource.API) bool { return api.EnvironmentID != id })
			f.databases = filterSlice(f.databases, func(db resource.TargetDatabase) bool { return db.EnvironmentID != id })
			f.accounts = filterSlice(f.accounts, func(account resource.TestAccount) bool { return account.EnvironmentID != id })
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) ListPages(_ context.Context, environmentID int64) ([]resource.Page, error) {
	out := make([]resource.Page, 0)
	for _, item := range f.pages {
		if item.EnvironmentID == environmentID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRepo) GetPage(_ context.Context, id int64) (resource.Page, error) {
	for _, item := range f.pages {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.Page{}, resource.ErrNotFound
}

func (f *fakeRepo) CreatePage(_ context.Context, page resource.Page) (resource.Page, error) {
	f.createPageCalls++
	for _, item := range f.pages {
		if item.EnvironmentID == page.EnvironmentID && item.RouteTemplate == page.RouteTemplate {
			return resource.Page{}, resource.ErrDuplicate
		}
	}
	f.pages = append(f.pages, page)
	return page, nil
}

func (f *fakeRepo) UpdatePage(_ context.Context, page resource.Page) (resource.Page, error) {
	for i, item := range f.pages {
		if item.ID != page.ID {
			continue
		}
		for _, other := range f.pages {
			if other.ID != page.ID && other.EnvironmentID == page.EnvironmentID && other.RouteTemplate == page.RouteTemplate {
				return resource.Page{}, resource.ErrDuplicate
			}
		}
		f.pages[i] = page
		return page, nil
	}
	return resource.Page{}, resource.ErrNotFound
}

func (f *fakeRepo) DeletePage(_ context.Context, id int64) error {
	for i, item := range f.pages {
		if item.ID == id {
			f.pages = append(f.pages[:i], f.pages[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) ListAPIs(_ context.Context, environmentID int64) ([]resource.API, error) {
	out := make([]resource.API, 0)
	for _, item := range f.apis {
		if item.EnvironmentID == environmentID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRepo) GetAPI(_ context.Context, id int64) (resource.API, error) {
	for _, item := range f.apis {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.API{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateAPI(_ context.Context, api resource.API) (resource.API, error) {
	for _, item := range f.apis {
		if item.EnvironmentID == api.EnvironmentID && item.Method == api.Method && item.PathTemplate == api.PathTemplate {
			return resource.API{}, resource.ErrDuplicate
		}
	}
	f.apis = append(f.apis, api)
	return api, nil
}

func (f *fakeRepo) UpdateAPI(_ context.Context, api resource.API) (resource.API, error) {
	for i, item := range f.apis {
		if item.ID != api.ID {
			continue
		}
		for _, other := range f.apis {
			if other.ID != api.ID && other.EnvironmentID == api.EnvironmentID &&
				other.Method == api.Method && other.PathTemplate == api.PathTemplate {
				return resource.API{}, resource.ErrDuplicate
			}
		}
		f.apis[i] = api
		return api, nil
	}
	return resource.API{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteAPI(_ context.Context, id int64) error {
	for i, item := range f.apis {
		if item.ID == id {
			f.apis = append(f.apis[:i], f.apis[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) ListDatabases(_ context.Context, sutID int64) ([]resource.TargetDatabase, error) {
	out := make([]resource.TargetDatabase, 0)
	for _, item := range f.databases {
		if item.SUTID == sutID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRepo) GetDatabase(_ context.Context, id int64) (resource.TargetDatabase, error) {
	for _, item := range f.databases {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.TargetDatabase{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateDatabase(_ context.Context, database resource.TargetDatabase) (resource.TargetDatabase, error) {
	for _, item := range f.databases {
		if item.EnvironmentID == database.EnvironmentID && strings.EqualFold(item.Name, database.Name) {
			return resource.TargetDatabase{}, resource.ErrDuplicate
		}
	}
	f.databases = append(f.databases, database)
	return database, nil
}

func (f *fakeRepo) UpdateDatabase(_ context.Context, database resource.TargetDatabase) (resource.TargetDatabase, error) {
	for i, item := range f.databases {
		if item.ID != database.ID {
			continue
		}
		for _, other := range f.databases {
			if other.ID != database.ID && other.EnvironmentID == database.EnvironmentID && strings.EqualFold(other.Name, database.Name) {
				return resource.TargetDatabase{}, resource.ErrDuplicate
			}
		}
		f.databases[i] = database
		return database, nil
	}
	return resource.TargetDatabase{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteDatabase(_ context.Context, id int64) error {
	for i, item := range f.databases {
		if item.ID == id {
			f.databases = append(f.databases[:i], f.databases[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) ListAccounts(_ context.Context, sutID int64) ([]resource.TestAccount, error) {
	out := make([]resource.TestAccount, 0)
	for _, item := range f.accounts {
		if item.SUTID == sutID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (f *fakeRepo) GetAccount(_ context.Context, id int64) (resource.TestAccount, error) {
	for _, item := range f.accounts {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.TestAccount{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateAccount(_ context.Context, account resource.TestAccount) (resource.TestAccount, error) {
	for _, item := range f.accounts {
		if item.EnvironmentID == account.EnvironmentID && strings.EqualFold(item.Username, account.Username) {
			return resource.TestAccount{}, resource.ErrDuplicate
		}
	}
	f.accounts = append(f.accounts, account)
	return account, nil
}

func (f *fakeRepo) UpdateAccount(_ context.Context, account resource.TestAccount) (resource.TestAccount, error) {
	for i, item := range f.accounts {
		if item.ID != account.ID {
			continue
		}
		for _, other := range f.accounts {
			if other.ID != account.ID && other.EnvironmentID == account.EnvironmentID && strings.EqualFold(other.Username, account.Username) {
				return resource.TestAccount{}, resource.ErrDuplicate
			}
		}
		f.accounts[i] = account
		return account, nil
	}
	return resource.TestAccount{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteAccount(_ context.Context, id int64) error {
	for i, item := range f.accounts {
		if item.ID == id {
			f.accounts = append(f.accounts[:i], f.accounts[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) ListRepositories(_ context.Context, limit int, beforeID *int64) ([]resource.CodeRepository, error) {
	f.lastRepoLimit = limit
	f.lastRepoBefore = beforeID
	out := make([]resource.CodeRepository, 0, len(f.repositories))
	for _, item := range f.repositories {
		if beforeID == nil || item.ID < *beforeID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeRepo) GetRepository(_ context.Context, id int64) (resource.CodeRepository, error) {
	for _, item := range f.repositories {
		if item.ID == id {
			return item, nil
		}
	}
	return resource.CodeRepository{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateRepository(_ context.Context, repository resource.CodeRepository) (resource.CodeRepository, error) {
	for _, item := range f.repositories {
		if strings.EqualFold(item.Name, repository.Name) {
			return resource.CodeRepository{}, resource.ErrDuplicate
		}
	}
	f.repositories = append(f.repositories, repository)
	return repository, nil
}

func (f *fakeRepo) UpdateRepository(_ context.Context, repository resource.CodeRepository) (resource.CodeRepository, error) {
	for i, item := range f.repositories {
		if item.ID != repository.ID {
			continue
		}
		for _, other := range f.repositories {
			if other.ID != repository.ID && strings.EqualFold(other.Name, repository.Name) {
				return resource.CodeRepository{}, resource.ErrDuplicate
			}
		}
		f.repositories[i] = repository
		return repository, nil
	}
	return resource.CodeRepository{}, resource.ErrNotFound
}

func (f *fakeRepo) DeleteRepository(_ context.Context, id int64) error {
	for i, item := range f.repositories {
		if item.ID == id {
			f.repositories = append(f.repositories[:i], f.repositories[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) GetSUTGraph(_ context.Context, sutID int64) (resource.SUTGraph, error) {
	sut, err := f.GetSUT(context.Background(), sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	return f.graph(sut), nil
}

func (f *fakeRepo) ListCatalog(context.Context) ([]resource.SUTGraph, error) {
	out := make([]resource.SUTGraph, 0, len(f.suts))
	for _, sut := range f.suts {
		out = append(out, f.graph(sut))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SUT.ID < out[j].SUT.ID })
	return out, nil
}

func (f *fakeRepo) graph(sut resource.SUT) resource.SUTGraph {
	graph := resource.SUTGraph{SUT: sut}
	for _, env := range f.environments {
		if env.SUTID == sut.ID {
			graph.Environments = append(graph.Environments, env)
		}
	}
	for _, page := range f.pages {
		if page.SUTID == sut.ID {
			graph.Pages = append(graph.Pages, page)
		}
	}
	for _, api := range f.apis {
		if api.SUTID == sut.ID {
			graph.APIs = append(graph.APIs, api)
		}
	}
	for _, db := range f.databases {
		if db.SUTID == sut.ID {
			graph.Databases = append(graph.Databases, db)
		}
	}
	for _, account := range f.accounts {
		if account.SUTID == sut.ID {
			graph.Accounts = append(graph.Accounts, account)
		}
	}
	return graph
}

func filterSlice[T any](items []T, keep func(T) bool) []T {
	out := make([]T, 0, len(items))
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

var testNow = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)

type harness struct {
	svc    *Service
	repo   *fakeRepo
	usage  *fakeUsage
	audit  *fakeAudit
	events *fakeEvents
}

func newHarness() *harness {
	repo := &fakeRepo{}
	usage := &fakeUsage{inUse: map[string]bool{}}
	audit := &fakeAudit{}
	events := &fakeEvents{}
	svc := New(repo, &seqIDs{next: 1000}, fixedClock{at: testNow}, usage, audit, events)
	return &harness{svc: svc, repo: repo, usage: usage, audit: audit, events: events}
}

func assertAPIError(t *testing.T, err error, status int, code string) *platform.Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %v is not a *platform.Error", err)
	}
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %d/%s, want %d/%s", apiErr.Status, apiErr.Code, status, code)
	}
	return apiErr
}

func createTestSUT(t *testing.T, h *harness, name string) SUTView {
	t.Helper()
	view, err := h.svc.CreateSUT(context.Background(), SUTInput{Name: name, Intro: name + " intro"}, 42)
	if err != nil {
		t.Fatalf("CreateSUT(%q): %v", name, err)
	}
	return view
}

func TestCreateSUTAssignsIDsTimestampsAndRecords(t *testing.T) {
	h := newHarness()
	view, err := h.svc.CreateSUT(context.Background(), SUTInput{Name: "Order Center", Intro: "orders"}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if view.ID != "1001" || view.Name != "Order Center" || view.Intro != "orders" {
		t.Fatalf("view = %#v", view)
	}
	if !view.CreatedAt.Equal(testNow) || !view.UpdatedAt.Equal(testNow) {
		t.Fatalf("timestamps = %v / %v", view.CreatedAt, view.UpdatedAt)
	}
	if len(view.Environments) != 0 {
		t.Fatalf("environments = %#v", view.Environments)
	}
	stored, err := h.repo.GetSUT(context.Background(), 1001)
	if err != nil {
		t.Fatalf("stored SUT: %v", err)
	}
	if stored.CreatedBy != 42 || stored.Name != "Order Center" {
		t.Fatalf("stored = %#v", stored)
	}

	if len(h.events.published) != 1 {
		t.Fatalf("events = %d", len(h.events.published))
	}
	event := h.events.published[0]
	if event.Channel != "system" || event.Type != "resource.sut.created" {
		t.Fatalf("event = %#v", event)
	}
	if event.ActorID == nil || *event.ActorID != 42 || event.Payload["id"] != "1001" {
		t.Fatalf("event = %#v", event)
	}
	if len(h.audit.entries) != 1 {
		t.Fatalf("audit = %d", len(h.audit.entries))
	}
	entry := h.audit.entries[0]
	if entry.Action != "resource.sut.created" || entry.TargetKind != "sut" || entry.TargetID != "1001" {
		t.Fatalf("audit = %#v", entry)
	}
	if entry.ActorID == nil || *entry.ActorID != 42 {
		t.Fatalf("audit actor = %v", entry.ActorID)
	}
}

func TestCreateSUTRejectsDuplicateAndInvalid(t *testing.T) {
	h := newHarness()
	createTestSUT(t, h, "Order Center")

	_, err := h.svc.CreateSUT(context.Background(), SUTInput{Name: "order center"}, 42)
	assertAPIError(t, err, 409, "conflict")

	_, err = h.svc.CreateSUT(context.Background(), SUTInput{Name: "   "}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "name" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if len(h.repo.suts) != 1 {
		t.Fatalf("suts = %d", len(h.repo.suts))
	}
}

func TestUpdateSUTAppliesPatchAndReportsMissing(t *testing.T) {
	h := newHarness()
	created := createTestSUT(t, h, "Order Center")

	name := "Orders Platform"
	view, err := h.svc.UpdateSUT(context.Background(), 1001, SUTPatch{Name: &name}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT: %v", err)
	}
	if view.Name != "Orders Platform" || view.Intro != created.Intro {
		t.Fatalf("view = %#v", view)
	}
	if !view.UpdatedAt.Equal(testNow) {
		t.Fatalf("updated_at = %v", view.UpdatedAt)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.sut.updated" {
		t.Fatalf("event type = %q", got)
	}

	_, err = h.svc.UpdateSUT(context.Background(), 9999, SUTPatch{Name: &name}, 42)
	assertAPIError(t, err, 404, "not_found")

	blank := "  "
	_, err = h.svc.UpdateSUT(context.Background(), 1001, SUTPatch{Name: &blank}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "name" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestListSUTsPaginatesByIDDesc(t *testing.T) {
	h := newHarness()
	for _, name := range []string{"S1", "S2", "S3", "S4", "S5"} {
		createTestSUT(t, h, name)
	}

	first, hasMore, err := h.svc.ListSUTs(context.Background(), 2, nil)
	if err != nil {
		t.Fatalf("ListSUTs: %v", err)
	}
	if len(first) != 2 || !hasMore {
		t.Fatalf("first page = %#v, hasMore = %v", first, hasMore)
	}
	if first[0].ID != "1005" || first[1].ID != "1004" {
		t.Fatalf("order = %#v", first)
	}
	if h.repo.lastSUTLimit != 3 {
		t.Fatalf("repo limit = %d", h.repo.lastSUTLimit)
	}

	before := int64(1004)
	second, hasMore, err := h.svc.ListSUTs(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 2: %v", err)
	}
	if len(second) != 2 || !hasMore {
		t.Fatalf("second page = %#v, hasMore = %v", second, hasMore)
	}
	if second[0].ID != "1003" || second[1].ID != "1002" {
		t.Fatalf("second page order = %#v", second)
	}
	if h.repo.lastSUTBefore == nil || *h.repo.lastSUTBefore != 1004 {
		t.Fatalf("before = %v", h.repo.lastSUTBefore)
	}

	before = int64(1002)
	last, hasMore, err := h.svc.ListSUTs(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 3: %v", err)
	}
	if len(last) != 1 || hasMore || last[0].ID != "1001" {
		t.Fatalf("last page = %#v, hasMore = %v", last, hasMore)
	}
}

func TestCreateEnvironmentKeepsDefaultUnique(t *testing.T) {
	h := newHarness()
	createTestSUT(t, h, "Order Center")

	first, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "prod", BaseURL: "https://orders.example.com", IsDefault: true,
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment first: %v", err)
	}
	if first.ID != "1002" || !first.IsDefault || first.Status != resource.StatusActive {
		t.Fatalf("first = %#v", first)
	}

	second, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "staging", BaseURL: "https://staging.example.com", IsDefault: true,
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment second: %v", err)
	}
	if !second.IsDefault {
		t.Fatalf("second default = %v", second.IsDefault)
	}
	stored, err := h.repo.GetEnvironment(context.Background(), 1002)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if stored.IsDefault {
		t.Fatal("previous default environment was not unset")
	}

	views, err := h.svc.ListEnvironments(context.Background(), 1001)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(views) != 2 || views[0].ID != "1002" || views[1].ID != "1003" {
		t.Fatalf("environments = %#v", views)
	}
	if len(views[0].Pages) != 0 || views[0].Pages == nil {
		t.Fatalf("nested pages = %#v", views[0].Pages)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.environment.created" {
		t.Fatalf("event type = %q", got)
	}
}

func TestUpdateEnvironmentToDefaultUnsetsOthers(t *testing.T) {
	h := newHarness()
	createTestSUT(t, h, "Order Center")
	first, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "prod", BaseURL: "https://orders.example.com", IsDefault: true,
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	second, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "staging", BaseURL: "https://staging.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	isDefault := true
	updated, err := h.svc.UpdateEnvironment(context.Background(), 1003, EnvironmentPatch{IsDefault: &isDefault}, 42)
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if !updated.IsDefault {
		t.Fatal("updated environment is not the default")
	}
	stored, err := h.repo.GetEnvironment(context.Background(), 1002)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if stored.IsDefault {
		t.Fatal("previous default environment was not unset")
	}
	if second.IsDefault {
		t.Fatal("second environment unexpectedly default before update")
	}
	_ = first
}

func TestEnvironmentValidationAndLookupErrors(t *testing.T) {
	h := newHarness()
	createTestSUT(t, h, "Order Center")

	_, err := h.svc.CreateEnvironment(context.Background(), 9999, EnvironmentInput{
		Name: "prod", BaseURL: "https://orders.example.com",
	}, 42)
	assertAPIError(t, err, 404, "not_found")

	_, err = h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "prod", BaseURL: "orders.example.com",
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "base_url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	if _, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "prod", BaseURL: "https://orders.example.com",
	}, 42); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	_, err = h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "PROD", BaseURL: "https://orders2.example.com",
	}, 42)
	assertAPIError(t, err, 409, "conflict")

	_, err = h.svc.UpdateEnvironment(context.Background(), 9999, EnvironmentPatch{}, 42)
	assertAPIError(t, err, 404, "not_found")

	badStatus := "archived"
	_, err = h.svc.UpdateEnvironment(context.Background(), 1002, EnvironmentPatch{Status: &badStatus}, 42)
	apiErr = assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "status" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func newPageEnv(t *testing.T, h *harness) { // creates SUT 1001 and env 1002
	t.Helper()
	createTestSUT(t, h, "Order Center")
	if _, err := h.svc.CreateEnvironment(context.Background(), 1001, EnvironmentInput{
		Name: "prod", BaseURL: "https://app.example.com", IsDefault: true,
	}, 42); err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
}

func TestCreatePageDerivesRouteAndDefaults(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)

	view, err := h.svc.CreatePage(context.Background(), 1002, PageInput{
		Name: "Order list",
		URL:  "https://app.example.com/orders?state=open",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if view.ID != "1003" || view.SUTID != "1001" || view.EnvironmentID != "1002" {
		t.Fatalf("ids = %#v", view)
	}
	if view.RouteTemplate != "/orders" || view.PageType != "page" {
		t.Fatalf("defaults = %#v", view)
	}
	if view.Status != resource.PageStatusApproved || view.Source != resource.PageSourceAdmin {
		t.Fatalf("status/source = %#v", view)
	}

	stored, err := h.svc.GetPage(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if stored.URL != "https://app.example.com/orders?state=open" {
		t.Fatalf("stored = %#v", stored)
	}
	pages, err := h.svc.ListPages(context.Background(), 1002)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 || pages[0].ID != "1003" {
		t.Fatalf("pages = %#v", pages)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.page.created" {
		t.Fatalf("event = %q", got)
	}
}

func TestCreatePageValidationAndDuplicate(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)

	_, err := h.svc.CreatePage(context.Background(), 9999, PageInput{Name: "x", URL: "https://app.example.com/x"}, 42)
	assertAPIError(t, err, 404, "not_found")

	_, err = h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "x", URL: "not-a-url"}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	if _, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	_, err = h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders dup", URL: "https://app.example.com/orders?x=1"}, 42)
	assertAPIError(t, err, 409, "conflict")
}

func TestUpdatePageAppliesPatch(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	page, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	route := "/orders/list"
	name := "Order list"
	updated, err := h.svc.UpdatePage(context.Background(), 1003, PagePatch{RouteTemplate: &route, Name: &name}, 42)
	if err != nil {
		t.Fatalf("UpdatePage: %v", err)
	}
	if updated.RouteTemplate != "/orders/list" || updated.Name != "Order list" {
		t.Fatalf("updated = %#v", updated)
	}
	if updated.Source != page.Source || updated.Status != page.Status {
		t.Fatalf("source/status changed = %#v", updated)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.page.updated" {
		t.Fatalf("event = %q", got)
	}

	_, err = h.svc.UpdatePage(context.Background(), 9999, PagePatch{Name: &name}, 42)
	assertAPIError(t, err, 404, "not_found")

	second, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "users", URL: "https://app.example.com/users"}, 42)
	if err != nil {
		t.Fatalf("CreatePage second: %v", err)
	}
	_ = second
	route = "/orders/list"
	_, err = h.svc.UpdatePage(context.Background(), 1004, PagePatch{RouteTemplate: &route}, 42)
	assertAPIError(t, err, 409, "conflict")
}

func TestImportPagesSkipsDuplicates(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	if _, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	result, err := h.svc.ImportPages(context.Background(), 1002, []PageInput{
		{Name: "orders again", URL: "https://app.example.com/orders"},
		{Name: "users", URL: "https://app.example.com/users"},
		{Name: "users again", URL: "https://app.example.com/users?x=1"},
		{Name: "reports", URL: "https://app.example.com/reports"},
	}, 42)
	if err != nil {
		t.Fatalf("ImportPages: %v", err)
	}
	if result.Created != 2 || result.Skipped != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Items) != 2 || result.Items[0].Source != resource.PageSourceImport {
		t.Fatalf("items = %#v", result.Items)
	}
	if result.Items[0].RouteTemplate != "/users" || result.Items[1].RouteTemplate != "/reports" {
		t.Fatalf("items = %#v", result.Items)
	}
	pages, err := h.svc.ListPages(context.Background(), 1002)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 3 {
		t.Fatalf("pages = %#v", pages)
	}
}

func TestImportPagesInvalidItemRejectsWholeBatch(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)

	_, err := h.svc.ImportPages(context.Background(), 1002, []PageInput{
		{Name: "users", URL: "https://app.example.com/users"},
		{Name: "bad", URL: "not-a-url"},
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "items" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if index, ok := apiErr.Details["index"].(int); !ok || index != 1 {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	if h.repo.createPageCalls != 0 {
		t.Fatalf("created %d pages for a rejected batch", h.repo.createPageCalls)
	}

	_, err = h.svc.ImportPages(context.Background(), 9999, []PageInput{{Name: "x", URL: "https://app.example.com/x"}}, 42)
	assertAPIError(t, err, 404, "not_found")
}

func TestAPICreateDefaultsAndDuplicate(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)

	view, err := h.svc.CreateAPI(context.Background(), 1002, APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "List orders",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	if view.ID != "1003" || view.SUTID != "1001" || view.EnvironmentID != "1002" {
		t.Fatalf("ids = %#v", view)
	}
	if view.Source != resource.APISourceManual || view.Status != resource.APIStatusApproved {
		t.Fatalf("defaults = %#v", view)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.api.created" {
		t.Fatalf("event = %q", got)
	}

	_, err = h.svc.CreateAPI(context.Background(), 1002, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "dup"}, 42)
	assertAPIError(t, err, 409, "conflict")

	_, err = h.svc.CreateAPI(context.Background(), 1002, APIInput{Method: "get", PathTemplate: "/api/users", Name: "bad"}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "method" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	_, err = h.svc.CreateAPI(context.Background(), 9999, APIInput{Method: "GET", PathTemplate: "/x", Name: "x"}, 42)
	assertAPIError(t, err, 404, "not_found")

	gem := "POST"
	updated, err := h.svc.UpdateAPI(context.Background(), 1003, APIPatch{Method: &gem}, 42)
	if err != nil {
		t.Fatalf("UpdateAPI: %v", err)
	}
	if updated.Method != "POST" {
		t.Fatalf("updated = %#v", updated)
	}
	apis, err := h.svc.ListAPIs(context.Background(), 1002)
	if err != nil {
		t.Fatalf("ListAPIs: %v", err)
	}
	if len(apis) != 1 || apis[0].Method != "POST" {
		t.Fatalf("apis = %#v", apis)
	}
}

func TestImportAPIsSkipsDuplicates(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	if _, err := h.svc.CreateAPI(context.Background(), 1002, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}

	result, err := h.svc.ImportAPIs(context.Background(), 1002, []APIInput{
		{Method: "GET", PathTemplate: "/api/orders", Name: "orders again"},
		{Method: "POST", PathTemplate: "/api/orders", Name: "create order"},
		{Method: "POST", PathTemplate: "/api/orders", Name: "dup"},
		{Method: "GET", PathTemplate: "/api/users", Name: "users"},
	}, 42)
	if err != nil {
		t.Fatalf("ImportAPIs: %v", err)
	}
	if result.Created != 2 || result.Skipped != 2 {
		t.Fatalf("result = %#v", result)
	}
	if len(result.Items) != 2 || result.Items[0].Source != resource.APISourceImport {
		t.Fatalf("items = %#v", result.Items)
	}

	_, err = h.svc.ImportAPIs(context.Background(), 1002, []APIInput{
		{Method: "POST", PathTemplate: "/api/ok", Name: "ok"},
		{Method: "TRACE", PathTemplate: "/api/bad", Name: "bad"},
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if index, ok := apiErr.Details["index"].(int); !ok || index != 1 {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	apis, err := h.svc.ListAPIs(context.Background(), 1002)
	if err != nil {
		t.Fatalf("ListAPIs: %v", err)
	}
	if len(apis) != 3 {
		t.Fatalf("apis = %#v", apis)
	}
}

func newDataEnv(t *testing.T, h *harness) { // SUT 1001, env 1002
	t.Helper()
	newPageEnv(t, h)
}

func TestDeleteGuardsUsageForEveryResource(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)
	page, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	api, err := h.svc.CreateAPI(context.Background(), 1002, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	database, err := h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: 1002, Name: "orders-db", DSN: "postgres://db.example.com:5432/orders", Username: "reader",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	account, err := h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "order_admin", Password: "s3cret",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	repo, err := h.svc.CreateRepository(context.Background(), RepositoryInput{
		Name: "orders-service", Path: "https://git.example.com/orders.git",
	}, 42)
	if err != nil {
		t.Fatalf("CreateRepository: %v", err)
	}

	cases := []struct {
		kind   string
		id     int64
		delete func() error
	}{
		{"page", 1003, func() error { return h.svc.DeletePage(context.Background(), 1003, 42) }},
		{"api", 1004, func() error { return h.svc.DeleteAPI(context.Background(), 1004, 42) }},
		{"database", 1005, func() error { return h.svc.DeleteDatabase(context.Background(), 1005, 42) }},
		{"account", 1006, func() error { return h.svc.DeleteAccount(context.Background(), 1006, 42) }},
		{"repository", 1007, func() error { return h.svc.DeleteRepository(context.Background(), 1007, 42) }},
		{"environment", 1002, func() error { return h.svc.DeleteEnvironment(context.Background(), 1002, 42) }},
		{"sut", 1001, func() error { return h.svc.DeleteSUT(context.Background(), 1001, 42) }},
	}
	_ = page
	_ = api
	_ = database
	_ = account
	_ = repo

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			h.usage.inUse[tc.kind] = true
			err := tc.delete()
			apiErr := assertAPIError(t, err, 409, "conflict")
			if !strings.Contains(apiErr.Message, tc.kind) {
				t.Fatalf("message = %q", apiErr.Message)
			}
			if !strings.Contains(strings.Join(h.usage.calls, ","), tc.kind) {
				t.Fatalf("usage calls = %#v", h.usage.calls)
			}

			h.usage.inUse[tc.kind] = false
			if err := tc.delete(); err != nil {
				t.Fatalf("delete after clearing usage: %v", err)
			}
		})
	}

	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.sut.deleted" {
		t.Fatalf("last event = %q", got)
	}
}

func TestAccountPasswordIsMaskedAndKeptOnUpdate(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)

	account, err := h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "order_admin", Password: "s3cret",
		Roles: []string{"order_admin"}, Notes: "seed",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if !account.PasswordSet || account.Roles[0] != "order_admin" {
		t.Fatalf("account = %#v", account)
	}
	stored, err := h.repo.GetAccount(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if stored.Password != "s3cret" {
		t.Fatalf("stored password = %q", stored.Password)
	}

	notes := "updated"
	updated, err := h.svc.UpdateAccount(context.Background(), 1003, AccountPatch{Notes: &notes}, 42)
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updated.Notes != "updated" || !updated.PasswordSet {
		t.Fatalf("updated = %#v", updated)
	}
	stored, err = h.repo.GetAccount(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if stored.Password != "s3cret" {
		t.Fatalf("password changed to %q", stored.Password)
	}

	password := "n3w-s3cret"
	updated, err = h.svc.UpdateAccount(context.Background(), 1003, AccountPatch{Password: &password}, 42)
	if err != nil {
		t.Fatalf("UpdateAccount password: %v", err)
	}
	if !updated.PasswordSet {
		t.Fatal("password_set = false after password update")
	}

	_, err = h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "no-password",
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "password" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	if _, err := h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "order_admin", Password: "x",
	}, 42); err == nil {
		t.Fatal("duplicate username accepted")
	}

	_, err = h.svc.CreateAccount(context.Background(), 9999, AccountInput{
		EnvironmentID: 1002, Username: "other", Password: "x",
	}, 42)
	assertAPIError(t, err, 404, "not_found")
}

func TestDatabaseDefaultsAndEnvironmentOwnership(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)

	database, err := h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: 1002, Name: "orders-db",
		DSN: "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.ReadOnly || !database.PasswordSet || database.Status != resource.StatusActive {
		t.Fatalf("database = %#v", database)
	}
	stored, err := h.repo.GetDatabase(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if stored.Kind != "postgres" || stored.Password != "pw" {
		t.Fatalf("stored = %#v", stored)
	}

	readOnly := false
	updated, err := h.svc.UpdateDatabase(context.Background(), 1003, DatabasePatch{ReadOnly: &readOnly}, 42)
	if err != nil {
		t.Fatalf("UpdateDatabase: %v", err)
	}
	if updated.ReadOnly {
		t.Fatal("read_only not updated")
	}
	stored, err = h.repo.GetDatabase(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if stored.Password != "pw" {
		t.Fatalf("password = %q", stored.Password)
	}

	_, err = h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: 1002, Name: "bad-dsn", DSN: "mysql://db/orders", Username: "reader",
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "dsn" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	otherSUT := createTestSUT(t, h, "Other SUT")
	_ = otherSUT
	otherEnv, err := h.svc.CreateEnvironment(context.Background(), 1004, EnvironmentInput{
		Name: "dev", BaseURL: "https://other.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	otherEnvID, err := strconv.ParseInt(otherEnv.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse env id: %v", err)
	}
	_, err = h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: otherEnvID, Name: "cross-sut", DSN: "postgres://db/x", Username: "reader",
	}, 42)
	apiErr = assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "environment_id" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	_, err = h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: otherEnvID, Username: "cross-sut", Password: "x",
	}, 42)
	apiErr = assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "environment_id" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestRepositoriesCRUDAndPagination(t *testing.T) {
	h := newHarness()
	for _, name := range []string{"repo-a", "repo-b", "repo-c"} {
		if _, err := h.svc.CreateRepository(context.Background(), RepositoryInput{
			Name: name, Path: "https://git.example.com/" + name + ".git",
			Include: []string{"src/**"},
		}, 42); err != nil {
			t.Fatalf("CreateRepository(%s): %v", name, err)
		}
	}
	first, hasMore, err := h.svc.ListRepositories(context.Background(), 2, nil)
	if err != nil {
		t.Fatalf("ListRepositories: %v", err)
	}
	if len(first) != 2 || !hasMore || first[0].ID != "1003" || first[1].ID != "1002" {
		t.Fatalf("first page = %#v, hasMore = %v", first, hasMore)
	}
	if first[0].CreatedBy != 42 || first[0].Include[0] != "src/**" {
		t.Fatalf("first = %#v", first[0])
	}
	before := int64(1002)
	second, hasMore, err := h.svc.ListRepositories(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListRepositories page 2: %v", err)
	}
	if len(second) != 1 || hasMore || second[0].ID != "1001" {
		t.Fatalf("second page = %#v, hasMore = %v", second, hasMore)
	}

	_, err = h.svc.CreateRepository(context.Background(), RepositoryInput{
		Name: "REPO-A", Path: "https://git.example.com/dup.git",
	}, 42)
	assertAPIError(t, err, 409, "conflict")

	name := "renamed"
	updated, err := h.svc.UpdateRepository(context.Background(), 1001, RepositoryPatch{Name: &name}, 42)
	if err != nil {
		t.Fatalf("UpdateRepository: %v", err)
	}
	if updated.Name != "renamed" {
		t.Fatalf("updated = %#v", updated)
	}
	_, err = h.svc.UpdateRepository(context.Background(), 9999, RepositoryPatch{Name: &name}, 42)
	assertAPIError(t, err, 404, "not_found")

	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.code_repo.updated" {
		t.Fatalf("event = %q", got)
	}
}

func TestCatalogAggregatesNestedResources(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)
	if _, err := h.svc.CreatePage(context.Background(), 1002, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if _, err := h.svc.CreateAPI(context.Background(), 1002, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	if _, err := h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: 1002, Name: "orders-db", DSN: "postgres://db.example.com/orders", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "order_admin", Password: "x",
	}, 42); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	second := createTestSUT(t, h, "Billing")
	secondEnv, err := h.svc.CreateEnvironment(context.Background(), mustID(t, second.ID), EnvironmentInput{
		Name: "dev", BaseURL: "https://billing.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if _, err := h.svc.CreatePage(context.Background(), mustID(t, secondEnv.ID), PageInput{
		Name: "invoices", URL: "https://billing.example.com/invoices",
	}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	catalog, err := h.svc.Catalog(context.Background())
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog = %d SUTs", len(catalog))
	}
	if catalog[0].ID != "1001" || catalog[1].ID != "1007" {
		t.Fatalf("catalog order = %#v", catalog)
	}
	firstEnv := catalog[0].Environments[0]
	if len(firstEnv.Pages) != 1 || len(firstEnv.APIs) != 1 || len(firstEnv.Databases) != 1 || len(firstEnv.Accounts) != 1 {
		t.Fatalf("nested = %#v", firstEnv)
	}
	if firstEnv.Databases[0].PasswordSet || firstEnv.Pages[0].Source != resource.PageSourceAdmin {
		t.Fatalf("nested = %#v", firstEnv)
	}
	if catalog[1].Environments[0].Pages[0].RouteTemplate != "/invoices" {
		t.Fatalf("second nested = %#v", catalog[1].Environments[0])
	}

	detail, err := h.svc.GetSUT(context.Background(), 1001)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if len(detail.Environments) != 1 || len(detail.Environments[0].Pages) != 1 {
		t.Fatalf("detail = %#v", detail)
	}

	environment, err := h.svc.GetEnvironment(context.Background(), 1002)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if environment.ID != "1002" || environment.SUTID != "1001" || len(environment.Accounts) != 1 {
		t.Fatalf("environment = %#v", environment)
	}
	if !environment.Accounts[0].PasswordSet {
		t.Fatal("account password_set = false")
	}

	page, err := h.svc.GetPage(context.Background(), 1003)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.RouteTemplate != "/orders" {
		t.Fatalf("page = %#v", page)
	}
	api, err := h.svc.GetAPI(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetAPI: %v", err)
	}
	if api.Method != "GET" {
		t.Fatalf("api = %#v", api)
	}
	database, err := h.svc.GetDatabase(context.Background(), 1005)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if database.PasswordSet {
		t.Fatalf("database = %#v", database)
	}
	account, err := h.svc.GetAccount(context.Background(), 1006)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !account.PasswordSet {
		t.Fatalf("account = %#v", account)
	}

	if _, err := h.svc.GetSUT(context.Background(), 9999); err == nil {
		t.Fatal("GetSUT missing returned nil error")
	}
}

func mustID(t *testing.T, raw string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse id %q: %v", raw, err)
	}
	return value
}

func TestGetAccountSecretReturnsStoredCredentials(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)
	account, err := h.svc.CreateAccount(context.Background(), 1001, AccountInput{
		EnvironmentID: 1002, Username: "order_admin", Password: "s3cret", Roles: []string{"order_admin"},
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	username, password, err := h.svc.GetAccountSecret(context.Background(), mustID(t, account.ID))
	if err != nil {
		t.Fatalf("GetAccountSecret: %v", err)
	}
	if username != "order_admin" || password != "s3cret" {
		t.Fatalf("secret = %q/%q", username, password)
	}
	if _, _, err := h.svc.GetAccountSecret(context.Background(), 999999); err == nil {
		t.Fatal("missing account must report not_found")
	}
	if _, _, err := h.svc.GetAccountSecret(context.Background(), 0); err == nil {
		t.Fatal("invalid account id must be rejected")
	}
}

func TestGetDatabaseSecretReturnsStoredCredentials(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)
	database, err := h.svc.CreateDatabase(context.Background(), 1001, DatabaseInput{
		EnvironmentID: 1002, Name: "orders-db",
		DSN: "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	dsn, username, password, err := h.svc.GetDatabaseSecret(context.Background(), mustID(t, database.ID))
	if err != nil {
		t.Fatalf("GetDatabaseSecret: %v", err)
	}
	if dsn != "postgres://db.example.com:5432/orders" || username != "reader" || password != "pw" {
		t.Fatalf("secret = %q/%q/%q", dsn, username, password)
	}
	if _, _, _, err := h.svc.GetDatabaseSecret(context.Background(), 999999); err == nil {
		t.Fatal("missing database must report not_found")
	}
}
