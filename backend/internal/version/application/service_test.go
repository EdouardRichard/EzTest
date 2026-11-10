package versionapp_test

import (
	"context"
	"errors"
	"fmt"
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

func int64SlicePtr(values []int64) *[]int64 { return &values }

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
	sutRepos     map[int64][]resourceapp.SUTRepositoryView
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
		sutRepos:     map[int64][]resourceapp.SUTRepositoryView{},
	}

	catalog.suts[1001] = resourceapp.SUTView{ID: "1001", Name: "Orders", Intro: "order center", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.suts[1002] = resourceapp.SUTView{ID: "1002", Name: "Billing", Intro: "billing", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.environments[1101] = resourceapp.EnvironmentView{ID: "1101", SUTID: "1001", Name: "prod", BaseURL: "https://orders.example.test", IsDefault: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.environments[1102] = resourceapp.EnvironmentView{ID: "1102", SUTID: "1002", Name: "staging", BaseURL: "https://billing.example.test", Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.environments[1103] = resourceapp.EnvironmentView{ID: "1103", SUTID: "1001", Name: "dev", BaseURL: "https://dev.example.test", Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.pages[1301] = resourceapp.PageView{ID: "1301", SUTID: "1001", EnvironmentID: "1101", Name: "Order list", URL: "https://orders.example.test/orders", RouteTemplate: "/orders", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1302] = resourceapp.PageView{ID: "1302", SUTID: "1001", EnvironmentID: "1101", Name: "Order detail", URL: "https://orders.example.test/orders/:id", RouteTemplate: "/orders/:id", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1303] = resourceapp.PageView{ID: "1303", SUTID: "1002", EnvironmentID: "1102", Name: "Invoice list", URL: "https://billing.example.test/invoices", RouteTemplate: "/invoices", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1304] = resourceapp.PageView{ID: "1304", SUTID: "1001", EnvironmentID: "1101", Name: "Order draft", URL: "https://orders.example.test/draft", RouteTemplate: "/draft", Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1305] = resourceapp.PageView{ID: "1305", SUTID: "1001", EnvironmentID: "1103", Name: "Dev page", URL: "https://dev.example.test/dev", RouteTemplate: "/dev", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.apis[1401] = resourceapp.APIView{ID: "1401", SUTID: "1001", EnvironmentID: "1101", Method: "GET", PathTemplate: "/api/orders", Name: "List orders", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.apis[1402] = resourceapp.APIView{ID: "1402", SUTID: "1002", EnvironmentID: "1102", Method: "POST", PathTemplate: "/api/invoices", Name: "Create invoice", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.apis[1403] = resourceapp.APIView{ID: "1403", SUTID: "1001", EnvironmentID: "1101", Method: "POST", PathTemplate: "/api/orders", Name: "Create order", Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.databases[1501] = resourceapp.DatabaseView{ID: "1501", SUTID: "1001", EnvironmentID: "1101", Name: "orders-db", DSN: "postgres://orders", Username: "orders", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.databases[1502] = resourceapp.DatabaseView{ID: "1502", SUTID: "1002", EnvironmentID: "1102", Name: "billing-db", DSN: "postgres://billing", Username: "billing", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.databases[1503] = resourceapp.DatabaseView{ID: "1503", SUTID: "1001", EnvironmentID: "1101", Name: "orders-ro", DSN: "postgres://orders-ro", Username: "reader", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.accounts[1601] = resourceapp.AccountView{ID: "1601", Username: "order-admin", Roles: []string{"admin"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1602] = resourceapp.AccountView{ID: "1602", Username: "buyer", Roles: []string{"buyer"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1603] = resourceapp.AccountView{ID: "1603", Username: "retired", Roles: []string{"buyer"}, Status: "disabled", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1604] = resourceapp.AccountView{ID: "1604", Username: "auditor", Roles: []string{"auditor"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}

	catalog.repositories[1201] = resourceapp.CodeRepositoryView{ID: "1201", Name: "orders-backend", Path: "C:/repos/orders-backend", Branch: "main", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.repositories[1202] = resourceapp.CodeRepositoryView{ID: "1202", Name: "orders-frontend", Path: "C:/repos/orders-frontend", Branch: "main", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.repositories[1203] = resourceapp.CodeRepositoryView{ID: "1203", Name: "orders-infra", Path: "C:/repos/orders-infra", Branch: "infra", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	// SUT 1001 inherits two repositories (backend + frontend). The fake returns
	// them in creation order to prove the service normalizes the baseline by
	// repository ID.
	catalog.sutRepos[1001] = []resourceapp.SUTRepositoryView{
		{ID: "1", SUTID: "1001", Repository: catalog.repositories[1202], Role: "frontend", Enabled: true, CreatedAt: testTime, UpdatedAt: testTime},
		{ID: "2", SUTID: "1001", Repository: catalog.repositories[1201], Role: "backend", Enabled: true, CreatedAt: testTime, UpdatedAt: testTime},
	}

	for id, environment := range catalog.environments {
		view := environment
		for pageID, page := range catalog.pages {
			if page.EnvironmentID == environment.ID {
				view.Pages = append(view.Pages, catalog.pages[pageID])
			}
		}
		for apiID, api := range catalog.apis {
			if api.EnvironmentID == environment.ID {
				view.APIs = append(view.APIs, catalog.apis[apiID])
			}
		}
		for databaseID, database := range catalog.databases {
			if database.EnvironmentID == environment.ID {
				view.Databases = append(view.Databases, catalog.databases[databaseID])
			}
		}
		sort.Slice(view.Pages, func(left, right int) bool { return view.Pages[left].ID < view.Pages[right].ID })
		sort.Slice(view.APIs, func(left, right int) bool { return view.APIs[left].ID < view.APIs[right].ID })
		sort.Slice(view.Databases, func(left, right int) bool { return view.Databases[left].ID < view.Databases[right].ID })
		catalog.environments[id] = view
	}
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

func (f *fakeCatalog) ListSUTRepositories(_ context.Context, sutID int64) ([]resourceapp.SUTRepositoryView, error) {
	if _, ok := f.suts[sutID]; !ok {
		return nil, notFound("sut")
	}
	views := append([]resourceapp.SUTRepositoryView(nil), f.sutRepos[sutID]...)
	sort.Slice(views, func(i, j int) bool {
		return views[i].Repository.ID < views[j].Repository.ID
	})
	return views, nil
}

func (f *fakeCatalog) ListAllAccounts(_ context.Context) ([]resourceapp.AccountView, error) {
	views := make([]resourceapp.AccountView, 0, len(f.accounts))
	for _, account := range f.accounts {
		views = append(views, account)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views, nil
}

var _ resourceapp.CatalogReader = (*fakeCatalog)(nil)

// ---------- fake version repository ----------

type fakeRepo struct {
	versions map[int64]version.Version
	bindings map[int64][]version.Binding
	stages   map[int64]version.StageState
	catalog  *fakeCatalog

	createErr          error
	updateErr          error
	stats              version.VersionStats
	initialWithContent []int64
	lastInitialLimit   int
	updateCalls        int
	createCalls        int
	lastBinding        version.Binding
	lastVersion        version.Version
	lastExpectedUpdate time.Time
	updateStages       []version.Stage
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		versions: map[int64]version.Version{},
		bindings: map[int64][]version.Binding{},
		stages:   map[int64]version.StageState{},
	}
}

func (f *fakeRepo) Create(_ context.Context, v version.Version, bindings []version.Binding) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createCalls++
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

func (f *fakeRepo) GetStage(_ context.Context, id int64) (version.StageState, error) {
	if state, ok := f.stages[id]; ok {
		return state, nil
	}
	if _, ok := f.versions[id]; !ok {
		return version.StageState{}, version.ErrNotFound
	}
	return version.StageState{VersionID: id, Stage: version.StageCreated}, nil
}

func (f *fakeRepo) UpdateBinding(_ context.Context, v version.Version, binding version.Binding, expectedUpdatedAt time.Time) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if _, ok := f.versions[v.ID]; !ok {
		return version.ErrNotFound
	}
	f.updateCalls++
	f.lastVersion = v
	f.lastBinding = binding
	f.lastExpectedUpdate = expectedUpdatedAt
	f.versions[v.ID] = v
	if len(f.bindings[v.ID]) == 0 {
		f.bindings[v.ID] = []version.Binding{binding}
		return nil
	}
	f.bindings[v.ID][0] = binding
	return nil
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
	return f.resourceInUse(id, func(binding version.Binding) bool {
		return containsInt64(binding.ScopeExclude.Pages, id)
	})
}

func (f *fakeRepo) APIInUse(_ context.Context, id int64) (bool, error) {
	return f.resourceInUse(id, func(binding version.Binding) bool {
		return containsInt64(binding.ScopeExclude.APIs, id)
	})
}

func (f *fakeRepo) DatabaseInUse(_ context.Context, id int64) (bool, error) {
	return f.resourceInUse(id, func(binding version.Binding) bool {
		return containsInt64(binding.ScopeExclude.Databases, id)
	})
}

// resourceInUse mirrors the SQL guard: a resource is in use when it belongs to
// a bound environment and the binding does not exclude it.
func (f *fakeRepo) resourceInUse(id int64, excluded func(version.Binding) bool) (bool, error) {
	if f.catalog == nil {
		return false, nil
	}
	var environmentID, sutID, status string
	switch {
	case f.catalog.pages[id].ID != "":
		page := f.catalog.pages[id]
		environmentID, sutID, status = page.EnvironmentID, page.SUTID, page.Status
	case f.catalog.apis[id].ID != "":
		api := f.catalog.apis[id]
		environmentID, sutID, status = api.EnvironmentID, api.SUTID, api.Status
	case f.catalog.databases[id].ID != "":
		database := f.catalog.databases[id]
		environmentID, sutID, status = database.EnvironmentID, database.SUTID, database.Status
	default:
		return false, nil
	}
	inRangeStatus := status == "approved" || status == "active"
	if !inRangeStatus {
		return false, nil
	}
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			if idString(binding.EnvironmentID) != environmentID || idString(binding.SUTID) != sutID {
				continue
			}
			if excluded(binding) {
				continue
			}
			return true, nil
		}
	}
	return false, nil
}

func containsInt64(values []int64, target int64) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
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
	// Any SUT-level association references the repository (SQL guard), and so
	// does an explicit version override.
	if f.catalog != nil {
		for _, associations := range f.catalog.sutRepos {
			for _, association := range associations {
				if association.Repository.ID == idString(id) {
					return true, nil
				}
			}
		}
	}
	for _, bindings := range f.bindings {
		for _, binding := range bindings {
			for _, override := range binding.RepositoryOverrides {
				if override.RepoID == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) ReferencingVersions(_ context.Context, kind string, id int64) ([]version.ReferencingVersion, error) {
	matched := map[int64]struct{}{}
	add := func(versionID int64) { matched[versionID] = struct{}{} }
	switch kind {
	case "sut":
		for versionID, bindings := range f.bindings {
			for _, binding := range bindings {
				if binding.SUTID == id {
					add(versionID)
				}
			}
		}
	case "environment":
		for versionID, bindings := range f.bindings {
			for _, binding := range bindings {
				if binding.EnvironmentID == id {
					add(versionID)
				}
			}
		}
	case "page", "api", "database":
		if f.catalog != nil {
			var environmentID, sutID, status string
			switch kind {
			case "page":
				page := f.catalog.pages[id]
				environmentID, sutID, status = page.EnvironmentID, page.SUTID, page.Status
			case "api":
				api := f.catalog.apis[id]
				environmentID, sutID, status = api.EnvironmentID, api.SUTID, api.Status
			case "database":
				database := f.catalog.databases[id]
				environmentID, sutID, status = database.EnvironmentID, database.SUTID, database.Status
			}
			if status == "approved" || status == "active" {
				for versionID, bindings := range f.bindings {
					for _, binding := range bindings {
						if idString(binding.EnvironmentID) != environmentID || idString(binding.SUTID) != sutID {
							continue
						}
						var excluded bool
						switch kind {
						case "page":
							excluded = containsInt64(binding.ScopeExclude.Pages, id)
						case "api":
							excluded = containsInt64(binding.ScopeExclude.APIs, id)
						case "database":
							excluded = containsInt64(binding.ScopeExclude.Databases, id)
						}
						if !excluded {
							add(versionID)
						}
					}
				}
			}
		}
	case "account":
		for versionID, bindings := range f.bindings {
			for _, binding := range bindings {
				for _, account := range binding.Accounts {
					if account.AccountID == id {
						add(versionID)
					}
				}
			}
		}
	case "repository":
		sutIDs := map[string]struct{}{}
		if f.catalog != nil {
			for _, associations := range f.catalog.sutRepos {
				for _, association := range associations {
					if association.Repository.ID == idString(id) {
						sutIDs[association.SUTID] = struct{}{}
					}
				}
			}
		}
		for versionID, bindings := range f.bindings {
			for _, binding := range bindings {
				if _, ok := sutIDs[idString(binding.SUTID)]; ok {
					add(versionID)
				}
				for _, override := range binding.RepositoryOverrides {
					if override.RepoID == id {
						add(versionID)
					}
				}
			}
		}
	default:
		return nil, fmt.Errorf("unknown resource kind %q", kind)
	}
	out := make([]version.ReferencingVersion, 0, len(matched))
	for versionID := range matched {
		entity := f.versions[versionID]
		out = append(out, version.ReferencingVersion{
			ID:            versionID,
			Title:         entity.Title,
			Status:        entity.Status,
			Stage:         entity.Stage,
			SuspendedFrom: entity.SuspendedFrom,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
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
	repo.catalog = catalog
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
		Title:         "Orders Q4",
		Description:   "Q4 order flows",
		Goal:          "verify checkout",
		EnvironmentID: 1101,
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

func storedBinding(t *testing.T, env *testEnv, versionID int64) version.Binding {
	t.Helper()
	_, bindings, err := env.repo.Get(requestContext(), versionID)
	if err != nil {
		t.Fatalf("stored Get: %v", err)
	}
	if len(bindings) != 1 {
		t.Fatalf("stored bindings = %#v", bindings)
	}
	return bindings[0]
}

// ---------- tests ----------

// TestCreateVersionEnvironmentOnly is the v0.22 R-C3/API-15 contract: creation
// takes title/description/goal/environment_id only, derives the SUT, writes the
// version_sut binding, inherits the SUT repositories (no override rows), binds
// every active global account and derives the baseline from the lowest repo ID.
func TestCreateVersionEnvironmentOnly(t *testing.T) {
	env := newTestEnv(t)
	ctx := requestContext()

	view, err := env.svc.Create(ctx, validCommand(), 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if view.Title != "Orders Q4" || view.Description != "Q4 order flows" || view.Goal != "verify checkout" {
		t.Fatalf("version metadata = %#v", view)
	}
	if view.Status != version.StatusInitial || view.CreatedBy != 42 {
		t.Fatalf("version = %#v", view)
	}
	if len(view.Bindings) != 1 {
		t.Fatalf("bindings = %#v", view.Bindings)
	}
	binding := view.Bindings[0]
	if binding.SUT.ID != "1001" || binding.SUT.Name != "Orders" {
		t.Fatalf("derived SUT = %#v", binding.SUT)
	}
	if binding.Environment.ID != "1101" || binding.Environment.SUTID != "1001" {
		t.Fatalf("environment = %#v", binding.Environment)
	}
	if len(binding.ScopeExclude.Pages) != 0 || len(binding.ScopeExclude.APIs) != 0 || len(binding.ScopeExclude.Databases) != 0 {
		t.Fatalf("default exclusions = %#v", binding.ScopeExclude)
	}
	// Default range = every approved environment resource; proposed resources
	// are not part of the version range.
	if len(binding.Pages) != 2 || binding.Pages[0].ID != "1301" || binding.Pages[1].ID != "1302" {
		t.Fatalf("pages = %#v", binding.Pages)
	}
	if len(binding.APIs) != 1 || binding.APIs[0].ID != "1401" {
		t.Fatalf("apis = %#v", binding.APIs)
	}
	if len(binding.Databases) != 2 || binding.Databases[0].ID != "1501" || binding.Databases[1].ID != "1503" {
		t.Fatalf("databases = %#v", binding.Databases)
	}
	// Accounts default to the active global pool; disabled accounts are skipped.
	if len(binding.Accounts) != 3 {
		t.Fatalf("accounts = %#v", binding.Accounts)
	}
	// Repositories inherit the SUT association; the baseline is the lowest repo
	// ID and the resolver is called exactly once for it.
	if len(binding.Repositories) != 2 || binding.Repositories[0].Repository.ID != "1201" || binding.Repositories[1].Repository.ID != "1202" {
		t.Fatalf("repositories = %#v", binding.Repositories)
	}
	if binding.Repository == nil || binding.Repository.ID != "1201" {
		t.Fatalf("baseline repository = %#v", binding.Repository)
	}
	if binding.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("code baseline = %q", binding.CodeBaselineCommit)
	}
	if len(env.resolver.calls) != 1 || env.resolver.calls[0].ID != "1201" {
		t.Fatalf("resolver calls = %#v, want the deterministic 1201 baseline", env.resolver.calls)
	}

	stored := storedBinding(t, env, view.ID)
	if stored.SUTID != 1001 || stored.EnvironmentID != 1101 {
		t.Fatalf("stored binding = %#v", stored)
	}
	if len(stored.RepositoryOverrides) != 0 {
		t.Fatalf("create must not write repository overrides: %#v", stored.RepositoryOverrides)
	}
	if len(stored.Accounts) != 3 {
		t.Fatalf("stored accounts = %#v", stored.Accounts)
	}
	if stored.ScopeExclude.Pages == nil || stored.ScopeExclude.APIs == nil || stored.ScopeExclude.Databases == nil {
		t.Fatalf("stored exclusions must be non-nil: %#v", stored.ScopeExclude)
	}
	storedVersion := env.repo.versions[view.ID]
	if storedVersion.Description != "Q4 order flows" || storedVersion.Goal != "verify checkout" {
		t.Fatalf("stored description/goal = %q/%q", storedVersion.Description, storedVersion.Goal)
	}

	// version.created audit and event (sut_count is 1 in the environment-only
	// model).
	if len(env.audit.events) != 1 || env.audit.events[0].Action != "version.created" {
		t.Fatalf("audit = %#v", env.audit.events)
	}
	if count, _ := env.audit.events[0].Metadata["sut_count"].(int); count != 1 {
		t.Fatalf("audit sut_count = %#v", env.audit.events[0].Metadata)
	}
	if len(env.publisher.events) != 2 {
		t.Fatalf("events = %#v", env.publisher.events)
	}
}

func TestCreateVersionValidatesEnvironmentOnly(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(*versionapp.CreateCommand)
		wantField string
	}{
		{"blank title", func(cmd *versionapp.CreateCommand) { cmd.Title = "   " }, "title"},
		{"missing environment", func(cmd *versionapp.CreateCommand) { cmd.EnvironmentID = 0 }, "environment_id"},
		{"unknown environment", func(cmd *versionapp.CreateCommand) { cmd.EnvironmentID = 9999 }, "environment_id"},
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
			if env.repo.createCalls != 0 {
				t.Fatalf("invalid input reached the repository: %d", env.repo.createCalls)
			}
			if len(env.audit.events) != 0 || len(env.publisher.events) != 0 {
				t.Fatalf("invalid input recorded side effects")
			}
		})
	}
}

func TestCreateVersionWithoutSUTRepositoriesSkipsBaseline(t *testing.T) {
	env := newTestEnv(t)
	env.catalog.sutRepos[1001] = nil
	view, err := env.svc.Create(requestContext(), validCommand(), 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(env.resolver.calls) != 0 {
		t.Fatalf("resolver calls = %#v, want none without a SUT repository", env.resolver.calls)
	}
	if view.Bindings[0].CodeBaselineCommit != "" || view.Bindings[0].Repository != nil {
		t.Fatalf("baseline = %#v", view.Bindings[0])
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
			if view.Bindings[0].CodeBaselineCommit != "" {
				t.Fatalf("binding view = %#v, want an empty code baseline", view.Bindings[0])
			}
			created, warnings := 0, 0
			for _, entry := range env.audit.events {
				switch entry.Action {
				case "version.created":
					created++
				case "version.code_baseline_unresolved":
					warnings++
				}
			}
			if created != 1 || warnings != 1 {
				t.Fatalf("audit actions = %#v", env.audit.events)
			}
		})
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

// TestBindingMultiRepoAndAccounts pins R-C2/DB-12..14: a binding update selects
// an explicit effective repository subset (included overrides for appended
// repositories, excluded overrides for removed inherited ones) and an explicit
// account subset. Unknown or disabled accounts are typed 400s.
func TestBindingMultiRepoAndAccounts(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		RepositoryIDs: int64SlicePtr([]int64{1202, 1203}),
		AccountIDs:    int64SlicePtr([]int64{1602}),
	}, 42)
	if err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	if len(overview.Repositories) != 3 {
		t.Fatalf("overview repositories = %#v", overview.Repositories)
	}
	included := map[int64]bool{}
	for _, repository := range overview.Repositories {
		included[parseID(t, repository.Repository.ID)] = repository.Included
	}
	if !included[1202] || !included[1203] || included[1201] {
		t.Fatalf("effective repositories = %#v", overview.Repositories)
	}
	if len(overview.Accounts) != 1 || overview.Accounts[0].ID != "1602" {
		t.Fatalf("overview accounts = %#v", overview.Accounts)
	}
	// The lowest effective repository is the deterministic baseline.
	if overview.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("code baseline = %q", overview.CodeBaselineCommit)
	}
	if len(env.resolver.calls) != 2 || env.resolver.calls[0].ID != "1201" || env.resolver.calls[1].ID != "1202" {
		t.Fatalf("resolver calls = %#v, want create 1201 then update 1202", env.resolver.calls)
	}

	stored := storedBinding(t, env, created.ID)
	overrides := map[int64]bool{}
	for _, override := range stored.RepositoryOverrides {
		overrides[override.RepoID] = override.Included
	}
	if included, ok := overrides[1201]; !ok || included {
		t.Fatalf("1201 override = %v/%v", included, ok)
	}
	if included, ok := overrides[1203]; !ok || !included {
		t.Fatalf("1203 override = %v/%v", included, ok)
	}
	if _, ok := overrides[1202]; ok {
		t.Fatalf("inherited repository 1202 must not be overridden: %#v", stored.RepositoryOverrides)
	}
	if len(stored.Accounts) != 1 || stored.Accounts[0].AccountID != 1602 {
		t.Fatalf("stored accounts = %#v", stored.Accounts)
	}

	// The binding change is audited and published as version.binding.changed.
	var changed bool
	for _, event := range env.audit.events {
		if event.Action == "version.binding.changed" {
			changed = true
		}
	}
	if !changed {
		t.Fatalf("version.binding.changed audit missing: %#v", env.audit.events)
	}
	changed = false
	for _, event := range env.publisher.events {
		if event.Type == "version.binding.changed" {
			changed = true
			if event.Scope["version_id"] != idString(created.ID) {
				t.Fatalf("event scope = %#v", event.Scope)
			}
		}
	}
	if !changed {
		t.Fatalf("version.binding.changed event missing: %#v", env.publisher.events)
	}

	// Typed errors: unknown/disabled resources are 400, missing version 404.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1603}),
	}, 42); err == nil {
		t.Fatal("disabled account accepted")
	} else if field := fieldName(t, err); field != "account_ids" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{9999}),
	}, 42); err == nil {
		t.Fatal("unknown account accepted")
	} else if field := fieldName(t, err); field != "account_ids" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		RepositoryIDs: int64SlicePtr([]int64{9999}),
	}, 42); err == nil {
		t.Fatal("unknown repository accepted")
	} else if field := fieldName(t, err); field != "repository_ids" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(9999),
	}, 42); err == nil {
		t.Fatal("unknown environment accepted")
	} else if field := fieldName(t, err); field != "environment_id" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(requestContext(), 9999, versionapp.BindingPatch{}, 42); err == nil {
		t.Fatal("missing version accepted")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
}

// TestBindingScopeExclude pins DB-11: pages/APIs/databases are excluded through
// scope_exclude, persisted verbatim, exposed with counts, and only resources of
// the bound environment are selectable.
func TestBindingScopeExclude(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")

	overview, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude: &version.ScopeExclude{
			Pages:     []int64{1301},
			APIs:      []int64{1401},
			Databases: []int64{1501},
		},
	}, 42)
	if err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	if overview.Counts.Pages.Approved != 2 || overview.Counts.Pages.Excluded != 1 || overview.Counts.Pages.Included != 1 {
		t.Fatalf("page counts = %#v", overview.Counts.Pages)
	}
	if overview.Counts.APIs.Approved != 1 || overview.Counts.APIs.Included != 0 {
		t.Fatalf("api counts = %#v", overview.Counts.APIs)
	}
	if overview.Counts.Databases.Approved != 2 || overview.Counts.Databases.Included != 1 {
		t.Fatalf("database counts = %#v", overview.Counts.Databases)
	}
	if len(overview.Pages) != 1 || overview.Pages[0].ID != "1302" {
		t.Fatalf("effective pages = %#v", overview.Pages)
	}
	if len(overview.APIs) != 0 {
		t.Fatalf("effective apis = %#v", overview.APIs)
	}
	if len(overview.Databases) != 1 || overview.Databases[0].ID != "1503" {
		t.Fatalf("effective databases = %#v", overview.Databases)
	}

	stored := storedBinding(t, env, created.ID)
	if len(stored.ScopeExclude.Pages) != 1 || stored.ScopeExclude.Pages[0] != 1301 ||
		len(stored.ScopeExclude.APIs) != 1 || stored.ScopeExclude.APIs[0] != 1401 ||
		len(stored.ScopeExclude.Databases) != 1 || stored.ScopeExclude.Databases[0] != 1501 {
		t.Fatalf("stored exclusions = %#v", stored.ScopeExclude)
	}
	// The version detail read model also applies the exclusions.
	detail, err := env.svc.Get(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(detail.Bindings[0].Pages) != 1 || detail.Bindings[0].Pages[0].ID != "1302" {
		t.Fatalf("detail pages = %#v", detail.Bindings[0].Pages)
	}

	// Clearing the exclusions restores the whole environment.
	cleared, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude: &version.ScopeExclude{},
	}, 42)
	if err != nil {
		t.Fatalf("clear exclusions: %v", err)
	}
	if len(cleared.Pages) != 2 || len(cleared.APIs) != 1 || len(cleared.Databases) != 2 {
		t.Fatalf("cleared overview = %#v", cleared)
	}

	// Only the bound environment's resources are selectable.
	invalid := map[string]version.ScopeExclude{
		"page of another environment": {Pages: []int64{1303}},
		"database of another sut":     {Databases: []int64{1502}},
		"unknown page":                {Pages: []int64{9999}},
		"zero api id":                 {APIs: []int64{0}},
	}
	for name, scope := range invalid {
		t.Run(name, func(t *testing.T) {
			_, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{ScopeExclude: &scope}, 42)
			if err == nil {
				t.Fatal("invalid exclusion accepted")
			}
			if got := apiError(t, err).Status; got != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400", got)
			}
		})
	}
}

// TestBindingEnvironmentAndStageRules pins the environment-scoped binding model
// and the R-C4-2 stage gate: editable through case_review, read-only while
// executing/reporting/completed, and suspended versions inherit the gate of the
// stage they were suspended from.
func TestBindingEnvironmentAndStageRules(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	ctx := requestContext()

	// The environment may change within the same SUT, and its resources are
	// validated against the new environment.
	overview, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1103),
	}, 42)
	if err != nil {
		t.Fatalf("change environment: %v", err)
	}
	if overview.Environment.ID != "1103" || len(overview.Pages) != 1 || overview.Pages[0].ID != "1305" {
		t.Fatalf("environment override = %#v", overview)
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1102),
		ScopeExclude:  &version.ScopeExclude{Pages: []int64{1305}},
	}, 42); err == nil {
		t.Fatal("environment of another SUT accepted")
	} else if field := fieldName(t, err); field != "environment_id" {
		t.Fatalf("field = %q", field)
	}

	readOnlyStages := []version.Stage{
		version.StageExecuting,
		version.StageReporting,
		version.StageCompleted,
	}
	for _, stage := range readOnlyStages {
		env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: stage}
		_, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
			ScopeExclude: &version.ScopeExclude{},
		}, 42)
		if err == nil {
			t.Fatalf("binding update accepted at stage %q", stage)
		}
		if got := apiError(t, err).Status; got != http.StatusConflict {
			t.Fatalf("stage %q status = %d, want 409", stage, got)
		}
	}
	for _, stage := range []version.Stage{
		version.StageCreated,
		version.StageClarifying,
		version.StageRequirementReview,
		version.StageDesigning,
		version.StageCaseReview,
	} {
		env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: stage}
		if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
			ScopeExclude: &version.ScopeExclude{},
		}, 42); err != nil {
			t.Fatalf("binding update rejected at stage %q: %v", stage, err)
		}
	}

	// Suspended versions use the recorded workflow stage.
	executing := version.StageExecuting
	env.repo.stages[created.ID] = version.StageState{
		VersionID: created.ID, Stage: version.StagePaused, SuspendedFrom: &executing,
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{}, 42); err == nil {
		t.Fatal("paused-from-executing binding was editable")
	} else if got := apiError(t, err).Status; got != http.StatusConflict {
		t.Fatalf("status = %d, want 409", got)
	}
	designing := version.StageDesigning
	env.repo.stages[created.ID] = version.StageState{
		VersionID: created.ID, Stage: version.StageBlocked, SuspendedFrom: &designing,
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{}, 42); err != nil {
		t.Fatalf("blocked-from-designing binding rejected: %v", err)
	}
}

func TestBindingOverviewReportsStageEditability(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: version.StageDesigning}
	overview, err := env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding: %v", err)
	}
	if overview.Stage != version.StageDesigning || !overview.Editable {
		t.Fatalf("overview = %#v", overview)
	}
	env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: version.StageExecuting}
	overview, err = env.svc.GetBinding(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("GetBinding executing: %v", err)
	}
	if overview.Editable {
		t.Fatal("executing binding reported editable")
	}
	if _, err := env.svc.GetBinding(requestContext(), 9999); err == nil {
		t.Fatal("missing version binding read accepted")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
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
	binding := view.Bindings[0]
	if binding.SUT.ID != "1001" || binding.Environment.ID != "1101" {
		t.Fatalf("binding = %#v", binding)
	}
	if binding.Repository == nil || binding.Repository.ID != "1201" {
		t.Fatalf("repository = %#v", binding.Repository)
	}
	if len(binding.Pages) != 2 || len(binding.APIs) != 1 || len(binding.Databases) != 2 || len(binding.Accounts) != 3 {
		t.Fatalf("nested resources = %#v", binding)
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
	if !hasMore || len(page) != 2 || page[0].ID != third.ID || page[1].ID != second.ID {
		t.Fatalf("page = %#v, hasMore = %v", page, hasMore)
	}
	if page[0].SUTCount != 1 || page[0].Title != "Version three" || page[0].Status != "initial" {
		t.Fatalf("summary = %#v", page[0])
	}
	next, more, err := env.svc.List(requestContext(), 2, &page[1].ID)
	if err != nil || more || len(next) != 1 || next[0].ID != first.ID {
		t.Fatalf("next = %#v, hasMore = %v, err = %v", next, more, err)
	}
	all, more, err := env.svc.List(requestContext(), 0, nil)
	if err != nil || more || len(all) != 3 {
		t.Fatalf("all = %#v, hasMore = %v, err = %v", all, more, err)
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
	if updated.Title != "Renamed" || !updated.AutoDesignNewRequirements || updated.Status != "initial" {
		t.Fatalf("updated = %#v", updated)
	}
	if len(env.audit.events) != 2 || env.audit.events[1].Action != "version.updated" {
		t.Fatalf("audit = %#v", env.audit.events)
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
		// A resource in the bound environment's approved range is in use: the
		// delete guard blocks it. Proposed resources are not in range.
		{"page in range", usage.PageInUse, 1302, true},
		{"page proposed", usage.PageInUse, 1304, false},
		{"api in range", usage.APIInUse, 1401, true},
		{"database in range", usage.DatabaseInUse, 1501, true},
		{"account bound", usage.AccountInUse, 1602, true},
		{"account unbound", usage.AccountInUse, 1603, false},
		{"repository inherited", usage.RepositoryInUse, 1201, true},
		{"repository unbound", usage.RepositoryInUse, 1203, false},
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
	// The conflict-details port resolves the same range to the referencing
	// versions, carrying the version title the resource context cannot read.
	refs, err := usage.ReferencingVersions(requestContext(), "page", 1302)
	if err != nil {
		t.Fatalf("ReferencingVersions(page): %v", err)
	}
	if len(refs) != 1 || refs[0].ID != created.ID || refs[0].Title != "Version" {
		t.Fatalf("page referencing versions = %#v", refs)
	}
	// An explicit exclusion removes the resource from the version range, so the
	// guard must stop blocking its deletion. In-range resources and explicit
	// repository overrides still count.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude:  &version.ScopeExclude{Pages: []int64{1301}, Databases: []int64{1501}},
		RepositoryIDs: int64SlicePtr([]int64{1203}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	after := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"excluded page", usage.PageInUse, 1301, false},
		{"in-range page", usage.PageInUse, 1302, true},
		{"excluded database", usage.DatabaseInUse, 1501, false},
		{"in-range database", usage.DatabaseInUse, 1503, true},
		{"sut repository association", usage.RepositoryInUse, 1201, true},
		{"version repository override", usage.RepositoryInUse, 1203, true},
	}
	for _, tc := range after {
		used, err := tc.check(requestContext(), tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if used != tc.want {
			t.Fatalf("%s(%d) = %v, want %v", tc.name, tc.id, used, tc.want)
		}
	}
	excludedRefs, err := usage.ReferencingVersions(requestContext(), "page", 1301)
	if err != nil {
		t.Fatalf("ReferencingVersions(excluded page): %v", err)
	}
	if len(excludedRefs) != 0 {
		t.Fatalf("excluded page referencing versions = %#v", excludedRefs)
	}
	repositoryRefs, err := usage.ReferencingVersions(requestContext(), "repository", 1203)
	if err != nil {
		t.Fatalf("ReferencingVersions(repository override): %v", err)
	}
	if len(repositoryRefs) != 1 || repositoryRefs[0].ID != created.ID {
		t.Fatalf("repository referencing versions = %#v", repositoryRefs)
	}
}

// TestReferencingVersionsCarriesStatusStageAndEditable pins the resource delete
// conflict projection: the resource context needs the lifecycle status, the
// effective stage and whether a binding edit is actually possible. A completed
// version is terminal regardless of a lagging stage projection, and a suspended
// version uses the stage it was suspended from.
func TestReferencingVersionsCarriesStatusStageAndEditable(t *testing.T) {
	env := newTestEnv(t)
	completed := createVersion(t, env, "Completed")
	executing := createVersion(t, env, "Executing")
	suspendedDesigning := createVersion(t, env, "Suspended designing")
	suspendedExecuting := createVersion(t, env, "Suspended executing")

	stageDesigning := version.StageDesigning
	stageExecuting := version.StageExecuting
	set := func(id int64, status string, stage version.Stage, suspendedFrom *version.Stage) {
		entity := env.repo.versions[id]
		entity.Status = status
		entity.Stage = stage
		entity.SuspendedFrom = suspendedFrom
		env.repo.versions[id] = entity
		env.repo.stages[id] = version.StageState{
			VersionID: id, Stage: stage, SuspendedFrom: suspendedFrom,
		}
	}
	// The persisted stage lags behind the completed status: status is the close
	// gate, so the effective stage must still project completed/read-only.
	set(completed.ID, version.StatusCompleted, version.StageReporting, nil)
	set(executing.ID, version.StatusRunning, version.StageExecuting, nil)
	set(suspendedDesigning.ID, version.StatusRunning, version.StageBlocked, &stageDesigning)
	set(suspendedExecuting.ID, version.StatusRunning, version.StagePaused, &stageExecuting)

	refs, err := env.svc.ReferencingVersions(requestContext(), "environment", 1101)
	if err != nil {
		t.Fatalf("ReferencingVersions: %v", err)
	}
	got := make(map[int64]resourceapp.VersionRef, len(refs))
	for _, ref := range refs {
		got[ref.ID] = ref
	}
	want := map[int64]resourceapp.VersionRef{
		completed.ID: {
			ID: completed.ID, Title: "Completed", Status: version.StatusCompleted,
			Stage: string(version.StageCompleted), Editable: false,
		},
		executing.ID: {
			ID: executing.ID, Title: "Executing", Status: version.StatusRunning,
			Stage: string(version.StageExecuting), Editable: false,
		},
		suspendedDesigning.ID: {
			ID: suspendedDesigning.ID, Title: "Suspended designing", Status: version.StatusRunning,
			Stage: string(version.StageDesigning), Editable: true,
		},
		suspendedExecuting.ID: {
			ID: suspendedExecuting.ID, Title: "Suspended executing", Status: version.StatusRunning,
			Stage: string(version.StageExecuting), Editable: false,
		},
	}
	if len(got) != len(want) {
		t.Fatalf("referencing versions = %#v, want %d entries", got, len(want))
	}
	for id, expected := range want {
		if got[id] != expected {
			t.Fatalf("referencing version %d = %#v, want %#v", id, got[id], expected)
		}
	}
}

func TestKnowledgeBindingKeysUsesEffectiveResources(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		ScopeExclude: &version.ScopeExclude{Pages: []int64{1301}},
		AccountIDs:   int64SlicePtr([]int64{1601}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	keys, err := env.svc.KnowledgeBindingKeys(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("KnowledgeBindingKeys: %v", err)
	}
	if len(keys.PageKeys) != 1 || keys.PageKeys[0] != "/orders/:id" {
		t.Fatalf("page keys = %#v", keys.PageKeys)
	}
	if len(keys.AccountRoles) != 1 || keys.AccountRoles[0] != "admin" {
		t.Fatalf("account roles = %#v", keys.AccountRoles)
	}
	if len(keys.SystemIDs) != 1 || keys.SystemIDs[0] != 1001 {
		t.Fatalf("system ids = %#v", keys.SystemIDs)
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
	if reopened.Status != version.StatusRunning || reopened.ClosedAt != nil || reopened.CloseReason != "" {
		t.Fatalf("reopened = %#v", reopened)
	}
	changed, audited := false, false
	for _, event := range env.publisher.events {
		if event.Type == "version.status_changed" && event.Payload["from"] == version.StatusCompleted {
			changed = true
		}
	}
	for _, event := range env.audit.events {
		if event.Action == "version.reopened" {
			audited = true
		}
	}
	if !changed || !audited {
		t.Fatalf("events/audit = %#v / %#v", env.publisher.events, env.audit.events)
	}
	eventsBefore := len(env.publisher.events)
	if err := env.svc.Reopen(requestContext(), created.ID); err != nil {
		t.Fatalf("second Reopen: %v", err)
	}
	if len(env.publisher.events) != eventsBefore {
		t.Fatal("reopen of a running version published status changes")
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

func parseID(t *testing.T, raw string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		t.Fatalf("parse ID %q: %v", raw, err)
	}
	return value
}
