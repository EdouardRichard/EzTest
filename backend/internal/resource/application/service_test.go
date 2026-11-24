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

// fakeUsage answers the version-binding usage checks and the referencing-version
// lookup used to build the delete conflict details.
type fakeUsage struct {
	inUse    map[string]bool
	refs     map[string][]VersionRef
	err      error
	refErr   error
	calls    []string
	refCalls []string
}

func (f *fakeUsage) check(kind string) (bool, error) {
	f.calls = append(f.calls, kind)
	if f.err != nil {
		return false, f.err
	}
	return f.inUse[kind], nil
}

func (f *fakeUsage) ReferencingVersions(_ context.Context, kind string, _ int64) ([]VersionRef, error) {
	f.refCalls = append(f.refCalls, kind)
	if f.refErr != nil {
		return nil, f.refErr
	}
	return f.refs[kind], nil
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
	environments  []resource.Environment
	suts          []resource.SUT
	pages         []resource.Page
	apis          []resource.API
	databases     []resource.TargetDatabase
	accounts      []resource.TestAccount
	repositories  []resource.CodeRepository
	sutRepos      []resource.SUTRepository
	registrations []resource.SUTRegistration

	lastEnvLimit    int
	lastEnvBefore   *int64
	lastSUTLimit    int
	lastSUTBefore   *int64
	lastRepoLimit   int
	lastRepoBefore  *int64
	createPageCalls int
	baselineWrites  []baselineWrite
	// createSUTWithRegistrationCalls pins that Service.CreateSUT uses the one-tx
	// creation path (I-1). A rewire to sequential CreateSUT + AppendSUTRegistration
	// leaves this at 0 and fails the admin-path test.
	createSUTWithRegistrationCalls int
}

// baselineWrite records one SetSUTRepositoryBaselineCommit call.
type baselineWrite struct {
	sutID  int64
	repoID int64
	commit string
}

// ---------- environment (top level) ----------

func (f *fakeRepo) ListEnvironments(_ context.Context, limit int, beforeID *int64) ([]resource.Environment, error) {
	f.lastEnvLimit = limit
	f.lastEnvBefore = beforeID
	out := make([]resource.Environment, 0, len(f.environments))
	for _, item := range f.environments {
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
		if strings.EqualFold(item.Name, environment.Name) {
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
			if other.ID != environment.ID && strings.EqualFold(other.Name, environment.Name) {
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
		if item.ID != id {
			continue
		}
		f.environments = append(f.environments[:i], f.environments[i+1:]...)
		for _, sut := range f.sutsOfEnvironment(id) {
			f.deleteBySUT(sut.ID)
		}
		f.suts = filterSlice(f.suts, func(sut resource.SUT) bool { return sut.EnvironmentID != id })
		return nil
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) sutsOfEnvironment(environmentID int64) []resource.SUT {
	out := make([]resource.SUT, 0)
	for _, sut := range f.suts {
		if sut.EnvironmentID == environmentID {
			out = append(out, sut)
		}
	}
	return out
}

// ---------- SUT ----------

func (f *fakeRepo) ListSUTsByEnvironment(_ context.Context, environmentID int64) ([]resource.SUT, error) {
	out := f.sutsOfEnvironment(environmentID)
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
		if item.EnvironmentID == sut.EnvironmentID && strings.EqualFold(item.Name, sut.Name) {
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
			if other.ID != sut.ID && other.EnvironmentID == sut.EnvironmentID && strings.EqualFold(other.Name, sut.Name) {
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
	f.pages = filterSlice(f.pages, func(item resource.Page) bool { return item.SUTID != id })
	f.apis = filterSlice(f.apis, func(item resource.API) bool { return item.SUTID != id })
	f.databases = filterSlice(f.databases, func(item resource.TargetDatabase) bool { return item.SUTID != id })
	f.sutRepos = filterSlice(f.sutRepos, func(item resource.SUTRepository) bool { return item.SUTID != id })
	f.registrations = filterSlice(f.registrations, func(item resource.SUTRegistration) bool { return item.SUTID != id })
	f.accounts = filterSlice(f.accounts, func(item resource.TestAccount) bool {
		return item.SUTID == nil || *item.SUTID != id
	})
}

// ---------- SUT registrations (append-only, R-B4) ----------

func (f *fakeRepo) AppendSUTRegistration(_ context.Context, registration resource.SUTRegistration) (resource.SUTRegistration, error) {
	found := false
	for _, item := range f.suts {
		if item.ID == registration.SUTID {
			found = true
			break
		}
	}
	if !found {
		return resource.SUTRegistration{}, resource.ErrNotFound
	}
	f.registrations = append(f.registrations, registration)
	return registration, nil
}

func (f *fakeRepo) ListSUTRegistrations(_ context.Context, sutID int64) ([]resource.SUTRegistration, error) {
	out := make([]resource.SUTRegistration, 0)
	for _, item := range f.registrations {
		if item.SUTID == sutID {
			out = append(out, item)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out, nil
}

// CreateSUTWithRegistration mirrors the one-transaction contract (I-1): if the
// registration append fails, the SUT is rolled back so it never exists without its
// birth record.
func (f *fakeRepo) CreateSUTWithRegistration(ctx context.Context, sut resource.SUT, registration resource.SUTRegistration) (resource.SUT, error) {
	f.createSUTWithRegistrationCalls++
	created, err := f.CreateSUT(ctx, sut)
	if err != nil {
		return resource.SUT{}, err
	}
	if _, err := f.AppendSUTRegistration(ctx, registration); err != nil {
		f.suts = filterSlice(f.suts, func(s resource.SUT) bool { return s.ID != created.ID })
		return resource.SUT{}, err
	}
	return created, nil
}

// ---------- pages ----------

func (f *fakeRepo) ListPages(_ context.Context, sutID int64) ([]resource.Page, error) {
	out := make([]resource.Page, 0)
	for _, item := range f.pages {
		if item.SUTID == sutID {
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

// normalizeTemplateTestKey mirrors the SQL rtrim lookup used by the template
// finders.
func normalizeTemplateTestKey(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

func (f *fakeRepo) GetPageByRouteTemplate(_ context.Context, sutID int64, routeTemplate string) (resource.Page, error) {
	key := normalizeTemplateTestKey(routeTemplate)
	for _, item := range f.pages {
		if item.SUTID == sutID && normalizeTemplateTestKey(item.RouteTemplate) == key {
			return item, nil
		}
	}
	return resource.Page{}, resource.ErrNotFound
}

// pageURLKey mirrors the sut_page unique key: (sut_id, normalized_url).
func pageURLKey(page resource.Page) string {
	normalized, err := resource.NormalizePageURL(page.URL)
	if err != nil {
		return page.URL
	}
	return normalized
}

func (f *fakeRepo) CreatePage(_ context.Context, page resource.Page) (resource.Page, error) {
	f.createPageCalls++
	key := pageURLKey(page)
	for _, item := range f.pages {
		if item.SUTID == page.SUTID && pageURLKey(item) == key {
			return resource.Page{}, resource.ErrPageURLDuplicate
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
		key := pageURLKey(page)
		for _, other := range f.pages {
			if other.ID != page.ID && other.SUTID == page.SUTID && pageURLKey(other) == key {
				return resource.Page{}, resource.ErrPageURLDuplicate
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

// ---------- APIs ----------

func (f *fakeRepo) ListAPIs(_ context.Context, sutID int64) ([]resource.API, error) {
	out := make([]resource.API, 0)
	for _, item := range f.apis {
		if item.SUTID == sutID {
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

func (f *fakeRepo) GetAPIByTemplate(_ context.Context, sutID int64, method, pathTemplate string) (resource.API, error) {
	key := normalizeTemplateTestKey(pathTemplate)
	for _, item := range f.apis {
		if item.SUTID == sutID && strings.EqualFold(item.Method, method) &&
			normalizeTemplateTestKey(item.PathTemplate) == key {
			return item, nil
		}
	}
	return resource.API{}, resource.ErrNotFound
}

func (f *fakeRepo) CreateAPI(_ context.Context, api resource.API) (resource.API, error) {
	for _, item := range f.apis {
		if item.SUTID == api.SUTID && item.Method == api.Method && item.PathTemplate == api.PathTemplate {
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
			if other.ID != api.ID && other.SUTID == api.SUTID &&
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

// ---------- target databases ----------

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
		if item.SUTID == database.SUTID && strings.EqualFold(item.Name, database.Name) {
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
			if other.ID != database.ID && other.SUTID == database.SUTID && strings.EqualFold(other.Name, database.Name) {
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

// ---------- test accounts (global pool) ----------

func (f *fakeRepo) ListAccounts(_ context.Context) ([]resource.TestAccount, error) {
	out := append([]resource.TestAccount(nil), f.accounts...)
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
		if strings.EqualFold(item.Username, account.Username) {
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
			if other.ID != account.ID && strings.EqualFold(other.Username, account.Username) {
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

// ---------- repositories ----------

func (f *fakeRepo) ListSUTRepositories(_ context.Context, sutID int64) ([]resource.SUTRepository, error) {
	out := make([]resource.SUTRepository, 0)
	for _, item := range f.sutRepos {
		if item.SUTID == sutID {
			out = append(out, item)
		}
	}
	sortSUTRepositories(out)
	return out, nil
}

func (f *fakeRepo) ListEnvironmentSUTRepositories(_ context.Context, environmentID int64) ([]resource.SUTRepository, error) {
	sutIDs := make(map[int64]struct{})
	for _, sut := range f.sutsOfEnvironment(environmentID) {
		sutIDs[sut.ID] = struct{}{}
	}
	out := make([]resource.SUTRepository, 0)
	for _, item := range f.sutRepos {
		if _, ok := sutIDs[item.SUTID]; ok {
			out = append(out, item)
		}
	}
	sortSUTRepositories(out)
	return out, nil
}

func sortSUTRepositories(out []resource.SUTRepository) {
	sort.Slice(out, func(i, j int) bool {
		if out[i].RepoID != out[j].RepoID {
			return out[i].RepoID < out[j].RepoID
		}
		if out[i].SUTID != out[j].SUTID {
			return out[i].SUTID < out[j].SUTID
		}
		return out[i].ID < out[j].ID
	})
}

func (f *fakeRepo) CreateSUTRepository(_ context.Context, repository resource.SUTRepository) (resource.SUTRepository, error) {
	for _, item := range f.sutRepos {
		if item.SUTID == repository.SUTID && item.RepoID == repository.RepoID {
			return resource.SUTRepository{}, resource.ErrDuplicate
		}
	}
	f.sutRepos = append(f.sutRepos, repository)
	return repository, nil
}

func (f *fakeRepo) DeleteSUTRepository(_ context.Context, id int64) error {
	for i, item := range f.sutRepos {
		if item.ID == id {
			f.sutRepos = append(f.sutRepos[:i], f.sutRepos[i+1:]...)
			return nil
		}
	}
	return resource.ErrNotFound
}

func (f *fakeRepo) SetSUTRepositoryBaselineCommit(_ context.Context, sutID, repoID int64, commit string) error {
	for i, item := range f.sutRepos {
		if item.SUTID == sutID && item.RepoID == repoID {
			f.sutRepos[i].CodeBaselineCommit = commit
			f.baselineWrites = append(f.baselineWrites, baselineWrite{sutID: sutID, repoID: repoID, commit: commit})
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

// ---------- aggregates ----------

func (f *fakeRepo) GetEnvironmentGraph(_ context.Context, environmentID int64) (resource.EnvironmentGraph, error) {
	environment, err := f.GetEnvironment(context.Background(), environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	return f.graph(environment), nil
}

func (f *fakeRepo) ListCatalog(context.Context) ([]resource.EnvironmentGraph, error) {
	out := make([]resource.EnvironmentGraph, 0, len(f.environments))
	for _, environment := range f.environments {
		out = append(out, f.graph(environment))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Environment.ID < out[j].Environment.ID })
	return out, nil
}

func (f *fakeRepo) graph(environment resource.Environment) resource.EnvironmentGraph {
	graph := resource.EnvironmentGraph{Environment: environment}
	for _, sut := range f.suts {
		if sut.EnvironmentID != environment.ID {
			continue
		}
		graph.SUTs = append(graph.SUTs, sut)
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
		for _, database := range f.databases {
			if database.SUTID == sut.ID {
				graph.Databases = append(graph.Databases, database)
			}
		}
	}
	sort.Slice(graph.SUTs, func(i, j int) bool { return graph.SUTs[i].ID < graph.SUTs[j].ID })
	sort.Slice(graph.Pages, func(i, j int) bool { return graph.Pages[i].ID < graph.Pages[j].ID })
	sort.Slice(graph.APIs, func(i, j int) bool { return graph.APIs[i].ID < graph.APIs[j].ID })
	sort.Slice(graph.Databases, func(i, j int) bool { return graph.Databases[i].ID < graph.Databases[j].ID })
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

func familyPtr(value string) *string { return &value }

// testEnvironmentID is the top-level environment newHarness pre-creates so the
// SUTs the helpers add keep stable, predictable IDs (the first SUT is 1002 and
// the assets created after it start at 1003).
const testEnvironmentID = int64(1001)

// testSUTID is the ID of the first SUT createTestSUT creates.
const testSUTID = int64(1002)

type harness struct {
	svc    *Service
	repo   *fakeRepo
	usage  *fakeUsage
	audit  *fakeAudit
	events *fakeEvents
}

func newHarness() *harness {
	repo := &fakeRepo{}
	usage := &fakeUsage{inUse: map[string]bool{}, refs: map[string][]VersionRef{}}
	audit := &fakeAudit{}
	events := &fakeEvents{}
	svc := New(repo, &seqIDs{next: 1000}, fixedClock{at: testNow}, usage, audit, events)
	h := &harness{svc: svc, repo: repo, usage: usage, audit: audit, events: events}
	// v0.2.4-B: environments are top-level, so the harness owns one before any
	// SUT can be created inside it.
	if _, err := svc.CreateEnvironment(context.Background(), EnvironmentInput{
		Name: "prod", Description: "production", Status: resource.StatusActive,
	}, 42); err != nil {
		panic("newHarness: seed environment: " + err.Error())
	}
	h.events.published = nil
	h.audit.entries = nil
	return h
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

// createTestSUT creates one SUT inside the harness environment.
func createTestSUT(t *testing.T, h *harness, name string) SUTView {
	t.Helper()
	view, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: name, Intro: name + " intro", BaseURL: testBaseURL(name),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT(%q): %v", name, err)
	}
	return view
}

// testBaseURL derives a valid absolute http(s) base_url from a SUT name.
func testBaseURL(name string) string {
	slug := strings.ToLower(strings.Join(strings.Fields(name), "-"))
	return "https://" + slug + ".example.com"
}

// createTestEnvironment creates an extra top-level environment.
func createTestEnvironment(t *testing.T, h *harness, name string) EnvironmentView {
	t.Helper()
	view, err := h.svc.CreateEnvironment(context.Background(), EnvironmentInput{
		Name: name, Description: name + " environment",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment(%q): %v", name, err)
	}
	return view
}

func TestCreateSUTAssignsIDsTimestampsAndRecords(t *testing.T) {
	h := newHarness()
	view, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Order Center", Intro: "orders", BaseURL: "https://order.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if view.ID != "1002" || view.Name != "Order Center" || view.Intro != "orders" {
		t.Fatalf("view = %#v", view)
	}
	if view.EnvironmentID != "1001" || view.BaseURL != "https://order.example.com" {
		t.Fatalf("hierarchy = %#v", view)
	}
	if !view.CreatedAt.Equal(testNow) || !view.UpdatedAt.Equal(testNow) {
		t.Fatalf("timestamps = %v / %v", view.CreatedAt, view.UpdatedAt)
	}
	if len(view.Pages) != 0 || len(view.APIs) != 0 || len(view.Databases) != 0 {
		t.Fatalf("assets = %#v", view)
	}
	stored, err := h.repo.GetSUT(context.Background(), testSUTID)
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
	if event.ActorID == nil || *event.ActorID != 42 || event.Payload["id"] != "1002" {
		t.Fatalf("event = %#v", event)
	}
	if len(h.audit.entries) != 1 {
		t.Fatalf("audit = %d", len(h.audit.entries))
	}
	entry := h.audit.entries[0]
	if entry.Action != "resource.sut.created" || entry.TargetKind != "sut" || entry.TargetID != "1002" {
		t.Fatalf("audit = %#v", entry)
	}
	if entry.ActorID == nil || *entry.ActorID != 42 {
		t.Fatalf("audit actor = %v", entry.ActorID)
	}
}

func TestCreateSUTRejectsDuplicateAndInvalid(t *testing.T) {
	h := newHarness()
	createTestSUT(t, h, "Order Center")

	_, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "order center", BaseURL: "https://dup.example.com",
	}, 42)
	assertAPIError(t, err, 409, "conflict")

	_, err = h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "   ", BaseURL: "https://blank.example.com",
	}, 42)
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
	view, err := h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Name: &name}, 42)
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
	_, err = h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Name: &blank}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "name" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestCreateSUTFamilyNormalizesAndClears(t *testing.T) {
	h := newHarness()
	view, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Payments", BaseURL: "https://payments.example.com", Family: familyPtr("  payments  "),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if view.Family == nil || *view.Family != "payments" {
		t.Fatalf("family = %v", view.Family)
	}
	stored, err := h.repo.GetSUT(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("stored SUT: %v", err)
	}
	if stored.Family == nil || *stored.Family != "payments" {
		t.Fatalf("stored family = %v", stored.Family)
	}

	without, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "No Family", BaseURL: "https://nofamily.example.com", Family: familyPtr(""),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT with empty family: %v", err)
	}
	if without.Family != nil {
		t.Fatalf("family = %v, want nil", without.Family)
	}

	for _, tc := range []struct {
		name   string
		family string
	}{
		{name: "uppercase", family: "Payments"},
		{name: "too long", family: strings.Repeat("a", 65)},
		{name: "inner whitespace", family: "order center"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
				Name: "Bad " + tc.name, BaseURL: "https://bad.example.com", Family: familyPtr(tc.family),
			}, 42)
			apiErr := assertAPIError(t, err, 400, "invalid_argument")
			if apiErr.Details["field"] != "family" {
				t.Fatalf("details = %#v", apiErr.Details)
			}
		})
	}
}

func TestUpdateSUTFamilySetKeepClear(t *testing.T) {
	h := newHarness()
	created, err := h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "Order Center", BaseURL: "https://order.example.com", Family: familyPtr("payments"),
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if created.Family == nil || *created.Family != "payments" {
		t.Fatalf("family = %v", created.Family)
	}

	// Omitted family keeps the stored value.
	name := "Orders Platform"
	view, err := h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Name: &name}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT: %v", err)
	}
	if view.Family == nil || *view.Family != "payments" {
		t.Fatalf("kept family = %v", view.Family)
	}

	// A present family is trimmed and replaces the stored value.
	view, err = h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Family: familyPtr("  order-center  ")}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT set family: %v", err)
	}
	if view.Family == nil || *view.Family != "order-center" {
		t.Fatalf("set family = %v", view.Family)
	}

	// The empty string is the explicit clear.
	view, err = h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Family: familyPtr("")}, 42)
	if err != nil {
		t.Fatalf("UpdateSUT clear family: %v", err)
	}
	if view.Family != nil {
		t.Fatalf("cleared family = %v", view.Family)
	}
	stored, err := h.repo.GetSUT(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("stored SUT: %v", err)
	}
	if stored.Family != nil {
		t.Fatalf("stored family = %v", stored.Family)
	}

	// Invalid present values are rejected and leave the row untouched.
	_, err = h.svc.UpdateSUT(context.Background(), testSUTID, SUTPatch{Family: familyPtr("Payments")}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "family" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	stored, err = h.repo.GetSUT(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("stored SUT: %v", err)
	}
	if stored.Family != nil {
		t.Fatalf("stored family after invalid update = %v", stored.Family)
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
	if first[0].ID != "1010" || first[1].ID != "1008" {
		t.Fatalf("order = %#v", first)
	}
	if h.repo.lastSUTLimit != 3 {
		t.Fatalf("repo limit = %d", h.repo.lastSUTLimit)
	}

	before := int64(1008)
	second, hasMore, err := h.svc.ListSUTs(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 2: %v", err)
	}
	if len(second) != 2 || !hasMore {
		t.Fatalf("second page = %#v, hasMore = %v", second, hasMore)
	}
	if second[0].ID != "1006" || second[1].ID != "1004" {
		t.Fatalf("second page order = %#v", second)
	}
	if h.repo.lastSUTBefore == nil || *h.repo.lastSUTBefore != 1008 {
		t.Fatalf("before = %v", h.repo.lastSUTBefore)
	}

	before = int64(1004)
	last, hasMore, err := h.svc.ListSUTs(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListSUTs page 3: %v", err)
	}
	if len(last) != 1 || hasMore || last[0].ID != "1002" {
		t.Fatalf("last page = %#v, hasMore = %v", last, hasMore)
	}
}

func TestCreateEnvironmentIsTopLevelAndUniqueByName(t *testing.T) {
	h := newHarness()

	first, err := h.svc.CreateEnvironment(context.Background(), EnvironmentInput{
		Name: "staging", Description: "staging environment",
	}, 42)
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if first.ID != "1002" || first.Name != "staging" || first.Description != "staging environment" {
		t.Fatalf("first = %#v", first)
	}
	if first.Status != resource.StatusActive {
		t.Fatalf("default status = %q", first.Status)
	}
	if len(first.SUTs) != 0 || first.SUTs == nil {
		t.Fatalf("nested suts = %#v", first.SUTs)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.environment.created" {
		t.Fatalf("event type = %q", got)
	}

	// The name is globally unique, case-insensitively.
	_, err = h.svc.CreateEnvironment(context.Background(), EnvironmentInput{Name: "STAGING"}, 42)
	assertAPIError(t, err, 409, "conflict")

	views, hasMore, err := h.svc.ListEnvironments(context.Background(), 10, nil)
	if err != nil {
		t.Fatalf("ListEnvironments: %v", err)
	}
	if len(views) != 2 || hasMore {
		t.Fatalf("environments = %#v hasMore=%v", views, views)
	}
	if views[0].ID != "1002" || views[1].ID != "1001" {
		t.Fatalf("environments are not ordered by ID descending: %#v", views)
	}
}

func TestUpdateEnvironmentAppliesPatch(t *testing.T) {
	h := newHarness()
	created := createTestEnvironment(t, h, "staging")

	name := "pre-production"
	description := "renamed"
	status := resource.StatusDisabled
	updated, err := h.svc.UpdateEnvironment(context.Background(), mustID(t, created.ID), EnvironmentPatch{
		Name: &name, Description: &description, Status: &status,
	}, 42)
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if updated.Name != "pre-production" || updated.Description != "renamed" || updated.Status != resource.StatusDisabled {
		t.Fatalf("updated = %#v", updated)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.environment.updated" {
		t.Fatalf("event type = %q", got)
	}

	_, err = h.svc.UpdateEnvironment(context.Background(), 9999, EnvironmentPatch{Name: &name}, 42)
	assertAPIError(t, err, 404, "not_found")
}

func TestEnvironmentValidationErrors(t *testing.T) {
	h := newHarness()

	_, err := h.svc.CreateEnvironment(context.Background(), EnvironmentInput{Name: "   "}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "name" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	_, err = h.svc.CreateEnvironment(context.Background(), EnvironmentInput{
		Name: "prod", Status: "archived",
	}, 42)
	apiErr = assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "status" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	_, err = h.svc.CreateEnvironment(context.Background(), EnvironmentInput{
		Name: "prod", Description: strings.Repeat("x", resource.MaxEnvironmentDescriptionRunes+1),
	}, 42)
	apiErr = assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "description" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestCreateSUTRequiresAnExistingEnvironment(t *testing.T) {
	h := newHarness()

	_, err := h.svc.CreateSUT(context.Background(), 9999, SUTInput{
		Name: "Order Center", BaseURL: "https://order.example.com",
	}, 42)
	assertAPIError(t, err, 404, "not_found")

	// base_url moved down from the environment to the SUT and is mandatory.
	_, err = h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{Name: "Order Center"}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "base_url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	// The SUT name is unique per environment, not globally: the same name in a
	// second environment is accepted.
	createTestSUT(t, h, "Order Center")
	other := createTestEnvironment(t, h, "staging")
	if _, err := h.svc.CreateSUT(context.Background(), mustID(t, other.ID), SUTInput{
		Name: "Order Center", BaseURL: "https://staging.order.example.com",
	}, 42); err != nil {
		t.Fatalf("the same SUT name in another environment must be accepted: %v", err)
	}
	_, err = h.svc.CreateSUT(context.Background(), testEnvironmentID, SUTInput{
		Name: "order center", BaseURL: "https://dup.example.com",
	}, 42)
	assertAPIError(t, err, 409, "conflict")
}

func TestListSUTsByEnvironmentAndAssets(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	first := createTestSUT(t, h, "Order Center")
	other := createTestEnvironment(t, h, "staging")
	second, err := h.svc.CreateSUT(ctx, mustID(t, other.ID), SUTInput{
		Name: "Billing", BaseURL: "https://billing.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT in the second environment: %v", err)
	}

	suts, err := h.svc.ListSUTsByEnvironment(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("ListSUTsByEnvironment: %v", err)
	}
	if len(suts) != 1 || suts[0].ID != first.ID {
		t.Fatalf("suts = %#v", suts)
	}
	suts, err = h.svc.ListSUTsByEnvironment(ctx, mustID(t, other.ID))
	if err != nil {
		t.Fatalf("ListSUTsByEnvironment second: %v", err)
	}
	if len(suts) != 1 || suts[0].ID != second.ID {
		t.Fatalf("second environment suts = %#v", suts)
	}
	if _, err := h.svc.ListSUTsByEnvironment(ctx, 9999); err == nil {
		t.Fatal("a missing environment must be a 404")
	} else {
		assertAPIError(t, err, 404, "not_found")
	}

	if _, err := h.svc.CreatePage(ctx, mustID(t, first.ID), PageInput{
		Name: "orders", URL: "https://order.example.com/orders",
	}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	assets, err := h.svc.ListAssetsBySUT(ctx, mustID(t, first.ID))
	if err != nil {
		t.Fatalf("ListAssetsBySUT: %v", err)
	}
	if len(assets.Pages) != 1 || len(assets.APIs) != 0 || len(assets.Databases) != 0 {
		t.Fatalf("assets = %#v", assets)
	}
	if _, err := h.svc.ListAssetsBySUT(ctx, 9999); err == nil {
		t.Fatal("a missing SUT must be a 404")
	}
}

func TestDeleteEnvironmentCascadesItsSUTs(t *testing.T) {
	h := newHarness()
	ctx := context.Background()
	other := createTestEnvironment(t, h, "staging")
	sut, err := h.svc.CreateSUT(ctx, mustID(t, other.ID), SUTInput{
		Name: "Billing", BaseURL: "https://billing.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if _, err := h.svc.CreatePage(ctx, mustID(t, sut.ID), PageInput{
		Name: "invoices", URL: "https://billing.example.com/invoices",
	}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	if err := h.svc.DeleteEnvironment(ctx, mustID(t, other.ID), 42); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if len(h.repo.suts) != 0 || len(h.repo.pages) != 0 || len(h.repo.environments) != 1 {
		t.Fatalf("cascade left environments=%d suts=%d pages=%d",
			len(h.repo.environments), len(h.repo.suts), len(h.repo.pages))
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.environment.deleted" {
		t.Fatalf("event type = %q", got)
	}
}

// newPageEnv creates the SUT (1002) the page/API tests hang their assets off.
// The environment (1001) is pre-seeded by newHarness.
func newPageEnv(t *testing.T, h *harness) {
	t.Helper()
	createTestSUT(t, h, "Order Center")
}

func TestCreatePageDerivesRouteAndDefaults(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)

	view, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{
		Name: "Order list",
		URL:  "https://app.example.com/orders?state=open",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if view.ID != "1004" || view.SUTID != "1002" {
		t.Fatalf("ids = %#v", view)
	}
	if view.RouteTemplate != "/orders" || view.PageType != "page" {
		t.Fatalf("defaults = %#v", view)
	}
	if view.Status != resource.PageStatusApproved || view.Source != resource.PageSourceAdmin {
		t.Fatalf("status/source = %#v", view)
	}

	stored, err := h.svc.GetPage(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if stored.URL != "https://app.example.com/orders?state=open" {
		t.Fatalf("stored = %#v", stored)
	}
	pages, err := h.svc.ListPages(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListPages: %v", err)
	}
	if len(pages) != 1 || pages[0].ID != "1004" {
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

	_, err = h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "x", URL: "not-a-url"}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	if _, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	_, err = h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "orders dup", URL: "https://app.example.com/orders?x=1"}, 42)
	apiErr = assertAPIError(t, err, 409, "page_url_exists")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestUpdatePageAppliesPatch(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	page, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	route := "/orders/list"
	name := "Order list"
	updated, err := h.svc.UpdatePage(context.Background(), 1004, PagePatch{RouteTemplate: &route, Name: &name}, 42)
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

	second, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "users", URL: "https://app.example.com/users"}, 42)
	if err != nil {
		t.Fatalf("CreatePage second: %v", err)
	}
	_ = second
	// route_template is no longer unique: reusing one is accepted.
	route = "/orders/list"
	reused, err := h.svc.UpdatePage(context.Background(), 1005, PagePatch{RouteTemplate: &route}, 42)
	if err != nil {
		t.Fatalf("reusing a route_template must be accepted: %v", err)
	}
	if reused.RouteTemplate != "/orders/list" {
		t.Fatalf("route = %q", reused.RouteTemplate)
	}
	// The normalized URL is the unique key now.
	duplicateURL := "https://app.example.com/orders?dup=1"
	_, err = h.svc.UpdatePage(context.Background(), 1005, PagePatch{URL: &duplicateURL}, 42)
	apiErr := assertAPIError(t, err, 409, "page_url_exists")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestPageSameTemplateDifferentUrlsAccepted(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	ctx := context.Background()

	first, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
		Name: "Orders list", URL: "https://app.example.com/orders", RouteTemplate: "/shared",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage first: %v", err)
	}
	second, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
		Name: "Users list", URL: "https://app.example.com/users", RouteTemplate: "/shared",
	}, 42)
	if err != nil {
		t.Fatalf("same route_template with a different URL must be accepted: %v", err)
	}
	if first.ID == second.ID {
		t.Fatalf("pages share id %q", first.ID)
	}

	// Omitted route templates derive from each URL and are likewise independent.
	for _, tc := range []struct{ name, url, route string }{
		{name: "Reports", url: "https://app.example.com/reports", route: "/reports"},
		{name: "Settings", url: "https://app.example.com/settings", route: "/settings"},
	} {
		view, err := h.svc.CreatePage(ctx, testSUTID, PageInput{Name: tc.name, URL: tc.url}, 42)
		if err != nil {
			t.Fatalf("CreatePage(%s): %v", tc.url, err)
		}
		if view.RouteTemplate != tc.route {
			t.Fatalf("route for %s = %q, want %q", tc.url, view.RouteTemplate, tc.route)
		}
	}
	if len(h.repo.pages) != 4 {
		t.Fatalf("stored pages = %d, want 4", len(h.repo.pages))
	}

	// Clearing route_template re-derives it from the page URL.
	empty := ""
	updated, err := h.svc.UpdatePage(ctx, 1005, PagePatch{RouteTemplate: &empty}, 42)
	if err != nil {
		t.Fatalf("empty route_template must derive from the URL: %v", err)
	}
	if updated.RouteTemplate != "/users" {
		t.Fatalf("derived route = %q, want /users", updated.RouteTemplate)
	}
}

func TestDuplicateNormalizedUrlFieldError(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	ctx := context.Background()

	if _, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
		Name: "Orders", URL: "https://app.example.com/orders?state=open",
	}, 42); err != nil {
		t.Fatalf("CreatePage first: %v", err)
	}

	_, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
		Name: "Orders duplicate",
		URL:  "HTTPS://APP.EXAMPLE.COM:443/orders/?tab=all",
	}, 42)
	apiErr := assertAPIError(t, err, 409, "page_url_exists")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	users, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
		Name: "Users", URL: "https://app.example.com/users",
	}, 42)
	if err != nil {
		t.Fatalf("CreatePage users: %v", err)
	}
	usersID, err := strconv.ParseInt(users.ID, 10, 64)
	if err != nil {
		t.Fatalf("parse users id %q: %v", users.ID, err)
	}
	duplicateURL := "https://app.example.com/orders?dup=1"
	_, err = h.svc.UpdatePage(ctx, usersID, PagePatch{URL: &duplicateURL}, 42)
	apiErr = assertAPIError(t, err, 409, "page_url_exists")
	if apiErr.Details["field"] != "url" {
		t.Fatalf("details = %#v", apiErr.Details)
	}
}

func TestSpaHashAndQueryNormalization(t *testing.T) {
	ctx := context.Background()

	t.Run("query is dropped", func(t *testing.T) {
		h := newHarness()
		newPageEnv(t, h)
		if _, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders", URL: "https://app.example.com/orders?state=open",
		}, 42); err != nil {
			t.Fatalf("CreatePage: %v", err)
		}
		_, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders other query", URL: "https://app.example.com/orders?state=closed",
		}, 42)
		apiErr := assertAPIError(t, err, 409, "page_url_exists")
		if apiErr.Details["field"] != "url" {
			t.Fatalf("details = %#v", apiErr.Details)
		}
	})

	t.Run("hash is kept", func(t *testing.T) {
		h := newHarness()
		newPageEnv(t, h)
		first, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders list", URL: "https://app.example.com/orders#/list",
		}, 42)
		if err != nil {
			t.Fatalf("CreatePage first: %v", err)
		}
		second, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders detail", URL: "https://app.example.com/orders#/detail",
		}, 42)
		if err != nil {
			t.Fatalf("different SPA hashes are different pages: %v", err)
		}
		if first.ID == second.ID || len(h.repo.pages) != 2 {
			t.Fatalf("pages = %#v", h.repo.pages)
		}
	})

	t.Run("same hash with different query is duplicate", func(t *testing.T) {
		h := newHarness()
		newPageEnv(t, h)
		if _, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders list", URL: "https://app.example.com/orders?x=1#/list",
		}, 42); err != nil {
			t.Fatalf("CreatePage: %v", err)
		}
		_, err := h.svc.CreatePage(ctx, testSUTID, PageInput{
			Name: "Orders list other query", URL: "https://app.example.com/orders?y=2#/list",
		}, 42)
		apiErr := assertAPIError(t, err, 409, "page_url_exists")
		if apiErr.Details["field"] != "url" {
			t.Fatalf("details = %#v", apiErr.Details)
		}
	})
}

func TestImportPagesSkipsDuplicates(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	if _, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	result, err := h.svc.ImportPages(context.Background(), testSUTID, []PageInput{
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
	pages, err := h.svc.ListPages(context.Background(), testSUTID)
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

	_, err := h.svc.ImportPages(context.Background(), testSUTID, []PageInput{
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

	view, err := h.svc.CreateAPI(context.Background(), testSUTID, APIInput{
		Method: "GET", PathTemplate: "/api/orders", Name: "List orders",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	if view.ID != "1004" || view.SUTID != "1002" {
		t.Fatalf("ids = %#v", view)
	}
	if view.Source != resource.APISourceManual || view.Status != resource.APIStatusApproved {
		t.Fatalf("defaults = %#v", view)
	}
	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.api.created" {
		t.Fatalf("event = %q", got)
	}

	_, err = h.svc.CreateAPI(context.Background(), testSUTID, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "dup"}, 42)
	assertAPIError(t, err, 409, "conflict")

	_, err = h.svc.CreateAPI(context.Background(), testSUTID, APIInput{Method: "get", PathTemplate: "/api/users", Name: "bad"}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "method" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	_, err = h.svc.CreateAPI(context.Background(), 9999, APIInput{Method: "GET", PathTemplate: "/x", Name: "x"}, 42)
	assertAPIError(t, err, 404, "not_found")

	gem := "POST"
	updated, err := h.svc.UpdateAPI(context.Background(), 1004, APIPatch{Method: &gem}, 42)
	if err != nil {
		t.Fatalf("UpdateAPI: %v", err)
	}
	if updated.Method != "POST" {
		t.Fatalf("updated = %#v", updated)
	}
	apis, err := h.svc.ListAPIs(context.Background(), testSUTID)
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
	if _, err := h.svc.CreateAPI(context.Background(), testSUTID, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}

	result, err := h.svc.ImportAPIs(context.Background(), testSUTID, []APIInput{
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

	_, err = h.svc.ImportAPIs(context.Background(), testSUTID, []APIInput{
		{Method: "POST", PathTemplate: "/api/ok", Name: "ok"},
		{Method: "TRACE", PathTemplate: "/api/bad", Name: "bad"},
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if index, ok := apiErr.Details["index"].(int); !ok || index != 1 {
		t.Fatalf("details = %#v", apiErr.Details)
	}
	apis, err := h.svc.ListAPIs(context.Background(), testSUTID)
	if err != nil {
		t.Fatalf("ListAPIs: %v", err)
	}
	if len(apis) != 3 {
		t.Fatalf("apis = %#v", apis)
	}
}

// newDataEnv creates the SUT (1002) the database/account tests hang off.
func newDataEnv(t *testing.T, h *harness) {
	t.Helper()
	newPageEnv(t, h)
}

func TestDeleteGuardsUsageForEveryResource(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)
	page, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42)
	if err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	api, err := h.svc.CreateAPI(context.Background(), testSUTID, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42)
	if err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	database, err := h.svc.CreateDatabase(context.Background(), testSUTID, DatabaseInput{
		Name: "orders-db", DSN: "postgres://db.example.com:5432/orders", Username: "reader",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	account, err := h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "order_admin", Password: "s3cret",
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
		{"page", 1004, func() error { return h.svc.DeletePage(context.Background(), 1004, 42) }},
		{"api", 1005, func() error { return h.svc.DeleteAPI(context.Background(), 1005, 42) }},
		{"database", 1006, func() error { return h.svc.DeleteDatabase(context.Background(), 1006, 42) }},
		{"account", 1007, func() error { return h.svc.DeleteAccount(context.Background(), 1007, 42) }},
		{"repository", 1008, func() error { return h.svc.DeleteRepository(context.Background(), 1008, 42) }},
		// The SUT is deleted before its environment: deleting the environment
		// would cascade the SUT and leave nothing for its own guard to check.
		{"sut", testSUTID, func() error { return h.svc.DeleteSUT(context.Background(), testSUTID, 42) }},
		{"environment", testEnvironmentID, func() error { return h.svc.DeleteEnvironment(context.Background(), testEnvironmentID, 42) }},
	}
	_ = page
	_ = api
	_ = database
	_ = account
	_ = repo

	for _, tc := range cases {
		t.Run(tc.kind, func(t *testing.T) {
			h.usage.inUse[tc.kind] = true
			h.usage.refs[tc.kind] = []VersionRef{{ID: 501, Title: "Q4 orders"}}
			refCallsBefore := len(h.usage.refCalls)
			err := tc.delete()
			apiErr := assertAPIError(t, err, 409, "resource_referenced")
			if apiErr.Status != 409 {
				t.Fatalf("status = %d", apiErr.Status)
			}
			if !strings.Contains(apiErr.Message, "referenced") {
				t.Fatalf("message = %q", apiErr.Message)
			}
			if apiErr.Details["kind"] != tc.kind {
				t.Fatalf("details kind = %#v", apiErr.Details["kind"])
			}
			versions, ok := apiErr.Details["referencing_versions"].([]map[string]any)
			if !ok || len(versions) != 1 {
				t.Fatalf("referencing_versions = %#v", apiErr.Details["referencing_versions"])
			}
			if versions[0]["id"] != "501" || versions[0]["title"] != "Q4 orders" {
				t.Fatalf("referencing version = %#v", versions[0])
			}
			if resolution, _ := apiErr.Details["resolution"].(string); resolution == "" {
				t.Fatalf("details resolution = %#v", apiErr.Details["resolution"])
			}
			if !strings.Contains(strings.Join(h.usage.calls, ","), tc.kind) {
				t.Fatalf("usage calls = %#v", h.usage.calls)
			}
			if len(h.usage.refCalls) != refCallsBefore+1 || h.usage.refCalls[len(h.usage.refCalls)-1] != tc.kind {
				t.Fatalf("reference lookup calls = %#v", h.usage.refCalls)
			}

			h.usage.inUse[tc.kind] = false
			delete(h.usage.refs, tc.kind)
			if err := tc.delete(); err != nil {
				t.Fatalf("delete after clearing usage: %v", err)
			}
		})
	}

	if got := h.events.published[len(h.events.published)-1].Type; got != "resource.environment.deleted" {
		t.Fatalf("last event = %q", got)
	}
}

func TestDeleteConflictCapsAndDedupesReferencingVersions(t *testing.T) {
	h := newHarness()
	h.usage.inUse["page"] = true
	refs := []VersionRef{
		{ID: 7001, Title: "first"},
		{ID: 7001, Title: "duplicate id"},
	}
	for i := int64(1); i <= 25; i++ {
		refs = append(refs, VersionRef{ID: i, Title: "v" + strconv.FormatInt(i, 10)})
	}
	h.usage.refs["page"] = refs

	apiErr := assertAPIError(t, h.svc.DeletePage(context.Background(), 1003, 42), 409, "resource_referenced")
	versions, ok := apiErr.Details["referencing_versions"].([]map[string]any)
	if !ok {
		t.Fatalf("referencing_versions = %#v", apiErr.Details["referencing_versions"])
	}
	if len(versions) != 20 {
		t.Fatalf("referencing versions = %d, want 20", len(versions))
	}
	seen := make(map[any]struct{}, len(versions))
	for _, item := range versions {
		if _, duplicate := seen[item["id"]]; duplicate {
			t.Fatalf("duplicate referencing version %#v", item)
		}
		seen[item["id"]] = struct{}{}
	}
	// The duplicated 7001 entry keeps its first title and the 20-item cap stops
	// after v19.
	if versions[0]["id"] != "7001" || versions[0]["title"] != "first" {
		t.Fatalf("first referencing version = %#v", versions[0])
	}
	if last := versions[len(versions)-1]; last["id"] != "19" || last["title"] != "v19" {
		t.Fatalf("last referencing version = %#v", last)
	}
}

// TestDeleteConflictDetailsCarryVersionStatusAndEditable pins R2-S-02: the 409
// must let the client tell an editable version from a read-only terminal one so
// it never suggests an impossible binding edit on executing/completed versions.
func TestDeleteConflictDetailsCarryVersionStatusAndEditable(t *testing.T) {
	h := newHarness()
	h.usage.inUse["page"] = true

	terminal := []VersionRef{
		{ID: 9001, Title: "执行中版本", Status: "running", Stage: "executing", Editable: false},
		{ID: 9002, Title: "已完成版本", Status: "completed", Stage: "completed", Editable: false},
	}
	designing := VersionRef{ID: 9003, Title: "设计中的版本", Status: "running", Stage: "designing", Editable: true}

	t.Run("mixed editable and terminal", func(t *testing.T) {
		h.usage.refs["page"] = append(append([]VersionRef{}, terminal...), designing)

		apiErr := assertAPIError(t, h.svc.DeletePage(context.Background(), 1003, 42), 409, "resource_referenced")
		if got := apiErr.Details["resource_id"]; got != "1003" {
			t.Fatalf("details.resource_id = %#v, want 1003", got)
		}
		if got := apiErr.Details["kind"]; got != "page" {
			t.Fatalf("details.kind = %#v, want page", got)
		}
		versions, ok := apiErr.Details["referencing_versions"].([]map[string]any)
		if !ok || len(versions) != 3 {
			t.Fatalf("referencing_versions = %#v", apiErr.Details["referencing_versions"])
		}
		want := map[string]struct {
			status   string
			stage    string
			editable bool
		}{
			"9001": {"running", "executing", false},
			"9002": {"completed", "completed", false},
			"9003": {"running", "designing", true},
		}
		for _, item := range versions {
			id, _ := item["id"].(string)
			expected, present := want[id]
			if !present {
				t.Fatalf("unexpected referencing version %#v", item)
			}
			if item["status"] != expected.status || item["stage"] != expected.stage || item["editable"] != expected.editable {
				t.Fatalf("referencing version %s = %#v", id, item)
			}
		}
		resolution, _ := apiErr.Details["resolution"].(string)
		if !strings.Contains(resolution, "read-only") || !strings.Contains(resolution, "administrator") {
			t.Fatalf("resolution must flag the read-only terminal path: %q", resolution)
		}
	})

	t.Run("terminal only", func(t *testing.T) {
		h.usage.refs["page"] = terminal

		apiErr := assertAPIError(t, h.svc.DeletePage(context.Background(), 1003, 42), 409, "resource_referenced")
		resolution, _ := apiErr.Details["resolution"].(string)
		if strings.Contains(resolution, "Exclude this page from the version scope") {
			t.Fatalf("resolution suggests an impossible scope edit on terminal versions: %q", resolution)
		}
		if !strings.Contains(resolution, "read-only") || !strings.Contains(resolution, "administrator") {
			t.Fatalf("resolution must name the administrator/operations path: %q", resolution)
		}
	})
}

func TestDeleteReferencedConflictIsDistinctFromPageURLDuplicate(t *testing.T) {
	h := newHarness()
	newPageEnv(t, h)
	if _, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{
		Name: "orders", URL: "https://app.example.com/orders",
	}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	_, err := h.svc.CreatePage(context.Background(), testSUTID, PageInput{
		Name: "orders dup", URL: "https://app.example.com/orders?x=1",
	}, 42)
	duplicate := assertAPIError(t, err, 409, "page_url_exists")
	if duplicate.Details["field"] != "url" {
		t.Fatalf("duplicate details = %#v", duplicate.Details)
	}
	if _, exists := duplicate.Details["referencing_versions"]; exists {
		t.Fatalf("url duplicate must not carry referencing_versions: %#v", duplicate.Details)
	}

	h.usage.inUse["page"] = true
	h.usage.refs["page"] = []VersionRef{{ID: 42, Title: "Q4 orders"}}
	referenced := assertAPIError(t, h.svc.DeletePage(context.Background(), 1003, 42), 409, "resource_referenced")
	if _, exists := referenced.Details["field"]; exists {
		t.Fatalf("referenced conflict must not carry a field: %#v", referenced.Details)
	}
	if referenced.Details["kind"] != "page" {
		t.Fatalf("referenced details = %#v", referenced.Details)
	}
}

func TestDeleteUsageReferenceLookupErrorPropagates(t *testing.T) {
	h := newHarness()
	h.usage.inUse["page"] = true
	h.usage.refErr = errors.New("referencing lookup failed")
	err := h.svc.DeletePage(context.Background(), 1003, 42)
	if err == nil || !strings.Contains(err.Error(), "referencing lookup failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestAccountPasswordIsMaskedAndKeptOnUpdate(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)

	account, err := h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "order_admin", Password: "s3cret",
		Roles: []string{"order_admin"}, Notes: "seed",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if !account.PasswordSet || account.Roles[0] != "order_admin" {
		t.Fatalf("account = %#v", account)
	}
	stored, err := h.repo.GetAccount(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if stored.Password != "s3cret" {
		t.Fatalf("stored password = %q", stored.Password)
	}

	notes := "updated"
	updated, err := h.svc.UpdateAccount(context.Background(), 1004, AccountPatch{Notes: &notes}, 42)
	if err != nil {
		t.Fatalf("UpdateAccount: %v", err)
	}
	if updated.Notes != "updated" || !updated.PasswordSet {
		t.Fatalf("updated = %#v", updated)
	}
	stored, err = h.repo.GetAccount(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if stored.Password != "s3cret" {
		t.Fatalf("password changed to %q", stored.Password)
	}

	password := "n3w-s3cret"
	updated, err = h.svc.UpdateAccount(context.Background(), 1004, AccountPatch{Password: &password}, 42)
	if err != nil {
		t.Fatalf("UpdateAccount password: %v", err)
	}
	if !updated.PasswordSet {
		t.Fatal("password_set = false after password update")
	}

	_, err = h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "no-password",
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "password" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	if _, err := h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "order_admin", Password: "x",
	}, 42); err == nil {
		t.Fatal("duplicate username accepted")
	}

	// The optional SUT affinity must name an existing SUT.
	missing := int64(9999)
	_, err = h.svc.CreateAccount(context.Background(), AccountInput{
		SUTID: &missing, Username: "other", Password: "x",
	}, 42)
	assertAPIError(t, err, 404, "not_found")

	// A valid affinity is stored and surfaced on the view.
	affinity := testSUTID
	bound, err := h.svc.CreateAccount(context.Background(), AccountInput{
		SUTID: &affinity, Username: "affinity", Password: "x",
	}, 42)
	if err != nil {
		t.Fatalf("CreateAccount with affinity: %v", err)
	}
	if bound.SUTID != "1002" {
		t.Fatalf("affinity = %q, want 1002", bound.SUTID)
	}
}

func TestDatabaseDefaultsAndSUTOwnership(t *testing.T) {
	h := newHarness()
	newDataEnv(t, h)

	database, err := h.svc.CreateDatabase(context.Background(), testSUTID, DatabaseInput{
		Name: "orders-db",
		DSN:  "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
	}, 42)
	if err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if !database.ReadOnly || !database.PasswordSet || database.Status != resource.StatusActive {
		t.Fatalf("database = %#v", database)
	}
	stored, err := h.repo.GetDatabase(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if stored.Kind != "postgres" || stored.Password != "pw" {
		t.Fatalf("stored = %#v", stored)
	}

	readOnly := false
	updated, err := h.svc.UpdateDatabase(context.Background(), 1004, DatabasePatch{ReadOnly: &readOnly}, 42)
	if err != nil {
		t.Fatalf("UpdateDatabase: %v", err)
	}
	if updated.ReadOnly {
		t.Fatal("read_only not updated")
	}
	stored, err = h.repo.GetDatabase(context.Background(), 1004)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if stored.Password != "pw" {
		t.Fatalf("password = %q", stored.Password)
	}

	_, err = h.svc.CreateDatabase(context.Background(), testSUTID, DatabaseInput{
		Name: "bad-dsn", DSN: "mysql://db/orders", Username: "reader",
	}, 42)
	apiErr := assertAPIError(t, err, 400, "invalid_argument")
	if apiErr.Details["field"] != "dsn" {
		t.Fatalf("details = %#v", apiErr.Details)
	}

	// v0.2.4-B: a target database belongs to a SUT alone, so its name is unique
	// per SUT. A second SUT may register the same database name.
	otherSUT := createTestSUT(t, h, "Other SUT")
	if _, err := h.svc.CreateDatabase(context.Background(), mustID(t, otherSUT.ID), DatabaseInput{
		Name: "orders-db", DSN: "postgres://db/other", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("the same database name under another SUT must be accepted: %v", err)
	}
	_, err = h.svc.CreateDatabase(context.Background(), testSUTID, DatabaseInput{
		Name: "ORDERS-DB", DSN: "postgres://db/dup", Username: "reader",
	}, 42)
	apiErr = assertAPIError(t, err, 409, "conflict")
	_ = apiErr

	// A missing SUT is a 404.
	_, err = h.svc.CreateDatabase(context.Background(), 9999, DatabaseInput{
		Name: "ghost", DSN: "postgres://db/ghost", Username: "reader",
	}, 42)
	assertAPIError(t, err, 404, "not_found")

	// Accounts are a global pool (DB-15): creating one needs no SUT at all.
	if _, err := h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "cross-sut", Password: "x",
	}, 42); err != nil {
		t.Fatalf("global account creation rejected: %v", err)
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
	if len(first) != 2 || !hasMore || first[0].ID != "1004" || first[1].ID != "1003" {
		t.Fatalf("first page = %#v, hasMore = %v", first, hasMore)
	}
	if first[0].CreatedBy != 42 || first[0].Include[0] != "src/**" {
		t.Fatalf("first = %#v", first[0])
	}
	before := int64(1003)
	second, hasMore, err := h.svc.ListRepositories(context.Background(), 2, &before)
	if err != nil {
		t.Fatalf("ListRepositories page 2: %v", err)
	}
	if len(second) != 1 || hasMore || second[0].ID != "1002" {
		t.Fatalf("second page = %#v, hasMore = %v", second, hasMore)
	}

	_, err = h.svc.CreateRepository(context.Background(), RepositoryInput{
		Name: "REPO-A", Path: "https://git.example.com/dup.git",
	}, 42)
	assertAPIError(t, err, 409, "conflict")

	name := "renamed"
	updated, err := h.svc.UpdateRepository(context.Background(), 1002, RepositoryPatch{Name: &name}, 42)
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
	ctx := context.Background()
	newDataEnv(t, h)
	if _, err := h.svc.CreatePage(ctx, testSUTID, PageInput{Name: "orders", URL: "https://app.example.com/orders"}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}
	if _, err := h.svc.CreateAPI(ctx, testSUTID, APIInput{Method: "GET", PathTemplate: "/api/orders", Name: "orders"}, 42); err != nil {
		t.Fatalf("CreateAPI: %v", err)
	}
	if _, err := h.svc.CreateDatabase(ctx, testSUTID, DatabaseInput{
		Name: "orders-db", DSN: "postgres://db.example.com/orders", Username: "reader",
	}, 42); err != nil {
		t.Fatalf("CreateDatabase: %v", err)
	}
	if _, err := h.svc.CreateAccount(ctx, AccountInput{
		Username: "order_admin", Password: "x",
	}, 42); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	// A second environment with its own SUT: the catalog is a forest of
	// environments, each nesting its SUTs and their assets.
	secondEnv := createTestEnvironment(t, h, "dev")
	secondSUT, err := h.svc.CreateSUT(ctx, mustID(t, secondEnv.ID), SUTInput{
		Name: "Billing", BaseURL: "https://billing.example.com",
	}, 42)
	if err != nil {
		t.Fatalf("CreateSUT: %v", err)
	}
	if _, err := h.svc.CreatePage(ctx, mustID(t, secondSUT.ID), PageInput{
		Name: "invoices", URL: "https://billing.example.com/invoices",
	}, 42); err != nil {
		t.Fatalf("CreatePage: %v", err)
	}

	catalog, err := h.svc.Catalog(ctx)
	if err != nil {
		t.Fatalf("Catalog: %v", err)
	}
	if len(catalog) != 2 {
		t.Fatalf("catalog = %d environments", len(catalog))
	}
	if catalog[0].ID != "1001" || catalog[1].ID != "1008" {
		t.Fatalf("catalog order = %#v", catalog)
	}
	firstSUTs := catalog[0].SUTs
	if len(firstSUTs) != 1 || firstSUTs[0].ID != "1002" {
		t.Fatalf("first environment suts = %#v", firstSUTs)
	}
	first := firstSUTs[0]
	if len(first.Pages) != 1 || len(first.APIs) != 1 || len(first.Databases) != 1 {
		t.Fatalf("nested = %#v", first)
	}
	if first.Databases[0].PasswordSet || first.Pages[0].Source != resource.PageSourceAdmin {
		t.Fatalf("nested = %#v", first)
	}
	if first.EnvironmentID != "1001" {
		t.Fatalf("sut parent = %#v", first)
	}
	if catalog[1].SUTs[0].Pages[0].RouteTemplate != "/invoices" {
		t.Fatalf("second nested = %#v", catalog[1].SUTs[0])
	}

	detail, err := h.svc.GetSUT(ctx, testSUTID)
	if err != nil {
		t.Fatalf("GetSUT: %v", err)
	}
	if len(detail.Pages) != 1 || len(detail.APIs) != 1 || len(detail.Databases) != 1 {
		t.Fatalf("detail = %#v", detail)
	}

	environment, err := h.svc.GetEnvironment(ctx, testEnvironmentID)
	if err != nil {
		t.Fatalf("GetEnvironment: %v", err)
	}
	if environment.ID != "1001" || len(environment.SUTs) != 1 {
		t.Fatalf("environment = %#v", environment)
	}
	if len(environment.SUTs[0].Pages) != 1 {
		t.Fatalf("environment suts = %#v", environment.SUTs)
	}

	page, err := h.svc.GetPage(ctx, 1004)
	if err != nil {
		t.Fatalf("GetPage: %v", err)
	}
	if page.RouteTemplate != "/orders" || page.SUTID != "1002" {
		t.Fatalf("page = %#v", page)
	}
	api, err := h.svc.GetAPI(ctx, 1005)
	if err != nil {
		t.Fatalf("GetAPI: %v", err)
	}
	if api.Method != "GET" || api.SUTID != "1002" {
		t.Fatalf("api = %#v", api)
	}
	database, err := h.svc.GetDatabase(ctx, 1006)
	if err != nil {
		t.Fatalf("GetDatabase: %v", err)
	}
	if database.PasswordSet || database.SUTID != "1002" {
		t.Fatalf("database = %#v", database)
	}
	account, err := h.svc.GetAccount(ctx, 1007)
	if err != nil {
		t.Fatalf("GetAccount: %v", err)
	}
	if !account.PasswordSet {
		t.Fatalf("account = %#v", account)
	}

	accounts, err := h.svc.ListAccounts(ctx)
	if err != nil {
		t.Fatalf("ListAccounts: %v", err)
	}
	if len(accounts) != 1 {
		t.Fatalf("accounts = %#v", accounts)
	}

	if _, err := h.svc.GetSUT(ctx, 9999); err == nil {
		t.Fatal("GetSUT missing returned nil error")
	}
	if _, err := h.svc.GetEnvironment(ctx, 9999); err == nil {
		t.Fatal("GetEnvironment missing returned nil error")
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
	account, err := h.svc.CreateAccount(context.Background(), AccountInput{
		Username: "order_admin", Password: "s3cret", Roles: []string{"order_admin"},
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
	database, err := h.svc.CreateDatabase(context.Background(), testSUTID, DatabaseInput{
		Name: "orders-db",
		DSN:  "postgres://db.example.com:5432/orders", Username: "reader", Password: "pw",
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
