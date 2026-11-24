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
//
// v0.2.4-B hierarchy: environment 1101 "prod" owns SUT 1001 (Orders) and SUT
// 1002 (Billing); environment 1102 "staging" owns SUT 1003 (Shipping). Assets
// belong to their SUT, accounts are a global pool, and the environment code pool
// is the union of its SUTs' repositories.

type fakeCatalog struct {
	suts         map[int64]resourceapp.SUTView
	environments map[int64]resourceapp.EnvironmentView
	pages        map[int64]resourceapp.PageView
	apis         map[int64]resourceapp.APIView
	databases    map[int64]resourceapp.DatabaseView
	accounts     map[int64]resourceapp.AccountView
	repositories map[int64]resourceapp.CodeRepositoryView
	sutRepos     map[int64][]resourceapp.SUTRepositoryView
	// baselineWrites records every SetSUTRepositoryBaselineCommit call as
	// "sutID:repoID=commit" so the tests can pin the resolution path.
	baselineWrites []string
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

	catalog.environments[1101] = resourceapp.EnvironmentView{ID: "1101", Name: "prod", Description: "production", Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.environments[1102] = resourceapp.EnvironmentView{ID: "1102", Name: "staging", Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.suts[1001] = resourceapp.SUTView{ID: "1001", EnvironmentID: "1101", Name: "Orders", Intro: "order center", BaseURL: "https://orders.example.test", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.suts[1002] = resourceapp.SUTView{ID: "1002", EnvironmentID: "1101", Name: "Billing", Intro: "billing", BaseURL: "https://billing.example.test", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.suts[1003] = resourceapp.SUTView{ID: "1003", EnvironmentID: "1102", Name: "Shipping", Intro: "shipping", BaseURL: "https://shipping.example.test", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.pages[1301] = resourceapp.PageView{ID: "1301", SUTID: "1001", Name: "Order list", URL: "https://orders.example.test/orders", RouteTemplate: "/orders", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1302] = resourceapp.PageView{ID: "1302", SUTID: "1001", Name: "Order detail", URL: "https://orders.example.test/orders/:id", RouteTemplate: "/orders/:id", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1303] = resourceapp.PageView{ID: "1303", SUTID: "1002", Name: "Invoice list", URL: "https://billing.example.test/invoices", RouteTemplate: "/invoices", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1304] = resourceapp.PageView{ID: "1304", SUTID: "1001", Name: "Order draft", URL: "https://orders.example.test/draft", RouteTemplate: "/draft", Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.pages[1305] = resourceapp.PageView{ID: "1305", SUTID: "1003", Name: "Shipment list", URL: "https://shipping.example.test/shipments", RouteTemplate: "/shipments", Status: "approved", Source: "admin", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.apis[1401] = resourceapp.APIView{ID: "1401", SUTID: "1001", Method: "GET", PathTemplate: "/api/orders", Name: "List orders", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.apis[1402] = resourceapp.APIView{ID: "1402", SUTID: "1002", Method: "POST", PathTemplate: "/api/invoices", Name: "Create invoice", Status: "approved", Source: "manual", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.apis[1403] = resourceapp.APIView{ID: "1403", SUTID: "1001", Method: "POST", PathTemplate: "/api/orders", Name: "Create order", Status: "proposed", Source: "ai", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.databases[1501] = resourceapp.DatabaseView{ID: "1501", SUTID: "1001", Name: "orders-db", DSN: "postgres://orders", Username: "orders", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.databases[1502] = resourceapp.DatabaseView{ID: "1502", SUTID: "1002", Name: "billing-db", DSN: "postgres://billing", Username: "billing", ReadOnly: true, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.databases[1503] = resourceapp.DatabaseView{ID: "1503", SUTID: "1001", Name: "orders-ro", DSN: "postgres://orders-ro", Username: "reader", ReadOnly: true, Status: "disabled", CreatedAt: testTime, UpdatedAt: testTime}

	catalog.accounts[1601] = resourceapp.AccountView{ID: "1601", Username: "order-admin", Roles: []string{"admin"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1602] = resourceapp.AccountView{ID: "1602", Username: "buyer", Roles: []string{"buyer"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1603] = resourceapp.AccountView{ID: "1603", Username: "retired", Roles: []string{"buyer"}, Status: "disabled", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}
	catalog.accounts[1604] = resourceapp.AccountView{ID: "1604", SUTID: "1001", Username: "auditor", Roles: []string{"auditor"}, Status: "active", PasswordSet: true, CreatedAt: testTime, UpdatedAt: testTime}

	catalog.repositories[1201] = resourceapp.CodeRepositoryView{ID: "1201", Name: "orders-backend", Path: "C:/repos/orders-backend", Branch: "main", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.repositories[1202] = resourceapp.CodeRepositoryView{ID: "1202", Name: "orders-frontend", Path: "C:/repos/orders-frontend", Branch: "main", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}
	catalog.repositories[1203] = resourceapp.CodeRepositoryView{ID: "1203", Name: "billing-infra", Path: "C:/repos/billing-infra", Branch: "infra", Include: []string{}, Exclude: []string{}, CreatedBy: 42, Status: "active", CreatedAt: testTime, UpdatedAt: testTime}

	// SUT 1001 associates two repositories (backend + frontend), listed in
	// creation order to prove the pool is normalized by repository ID. SUT 1002
	// adds a third, so the prod environment pool is 1201, 1202, 1203.
	catalog.sutRepos[1001] = []resourceapp.SUTRepositoryView{
		{ID: "2", SUTID: "1001", Repository: catalog.repositories[1202], Role: "frontend", Enabled: true, CreatedAt: testTime, UpdatedAt: testTime},
		{ID: "1", SUTID: "1001", Repository: catalog.repositories[1201], Role: "backend", Enabled: true, CreatedAt: testTime, UpdatedAt: testTime},
	}
	catalog.sutRepos[1002] = []resourceapp.SUTRepositoryView{
		{ID: "3", SUTID: "1002", Repository: catalog.repositories[1203], Role: "infra", Enabled: true, CreatedAt: testTime, UpdatedAt: testTime},
	}

	catalog.rebuild()
	return catalog
}

// rebuild re-derives the nested environment/SUT asset trees from the flat maps,
// mirroring what the resource catalog projects.
func (f *fakeCatalog) rebuild() {
	for sutID, sut := range f.suts {
		view := sut
		view.Pages = nil
		view.APIs = nil
		view.Databases = nil
		for _, page := range f.pages {
			if page.SUTID == sut.ID {
				view.Pages = append(view.Pages, page)
			}
		}
		for _, api := range f.apis {
			if api.SUTID == sut.ID {
				view.APIs = append(view.APIs, api)
			}
		}
		for _, database := range f.databases {
			if database.SUTID == sut.ID {
				view.Databases = append(view.Databases, database)
			}
		}
		sort.Slice(view.Pages, func(i, j int) bool { return view.Pages[i].ID < view.Pages[j].ID })
		sort.Slice(view.APIs, func(i, j int) bool { return view.APIs[i].ID < view.APIs[j].ID })
		sort.Slice(view.Databases, func(i, j int) bool { return view.Databases[i].ID < view.Databases[j].ID })
		f.suts[sutID] = view
	}
	for environmentID, environment := range f.environments {
		view := environment
		view.SUTs = nil
		for _, sut := range f.suts {
			if sut.EnvironmentID == environment.ID {
				view.SUTs = append(view.SUTs, sut)
			}
		}
		sort.Slice(view.SUTs, func(i, j int) bool { return view.SUTs[i].ID < view.SUTs[j].ID })
		f.environments[environmentID] = view
	}
}

// sutIDsOfEnvironment returns the SUT ids of one environment in ID order.
func (f *fakeCatalog) sutIDsOfEnvironment(environmentID int64) []int64 {
	ids := make([]int64, 0)
	for _, sut := range f.environments[environmentID].SUTs {
		if id, err := strconv.ParseInt(sut.ID, 10, 64); err == nil {
			ids = append(ids, id)
		}
	}
	return ids
}

// environmentOfSUT resolves the environment that owns one SUT.
func (f *fakeCatalog) environmentOfSUT(sutID int64) int64 {
	sut, ok := f.suts[sutID]
	if !ok {
		return 0
	}
	id, err := strconv.ParseInt(sut.EnvironmentID, 10, 64)
	if err != nil {
		return 0
	}
	return id
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

func (f *fakeCatalog) ListSUTsByEnvironment(_ context.Context, environmentID int64) ([]resourceapp.SUTView, error) {
	environment, ok := f.environments[environmentID]
	if !ok {
		return nil, notFound("environment")
	}
	return append([]resourceapp.SUTView(nil), environment.SUTs...), nil
}

func (f *fakeCatalog) ListAssetsBySUT(_ context.Context, sutID int64) (resourceapp.SUTAssets, error) {
	sut, ok := f.suts[sutID]
	if !ok {
		return resourceapp.SUTAssets{}, notFound("sut")
	}
	return resourceapp.SUTAssets{
		Pages:     append([]resourceapp.PageView(nil), sut.Pages...),
		APIs:      append([]resourceapp.APIView(nil), sut.APIs...),
		Databases: append([]resourceapp.DatabaseView(nil), sut.Databases...),
	}, nil
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

// ListEnvironmentRepositories mirrors the resource catalog's environment code
// pool (GD-03): the union of the SUT repositories, de-duplicated by repository
// ID and ordered by repository ID.
func (f *fakeCatalog) ListEnvironmentRepositories(_ context.Context, environmentID int64) ([]resourceapp.SUTRepositoryView, error) {
	if _, ok := f.environments[environmentID]; !ok {
		return nil, notFound("environment")
	}
	views := make([]resourceapp.SUTRepositoryView, 0)
	for _, sutID := range f.sutIDsOfEnvironment(environmentID) {
		associations := append([]resourceapp.SUTRepositoryView(nil), f.sutRepos[sutID]...)
		sort.Slice(associations, func(i, j int) bool {
			return associations[i].Repository.ID < associations[j].Repository.ID
		})
		views = append(views, associations...)
	}
	sort.SliceStable(views, func(i, j int) bool {
		return views[i].Repository.ID < views[j].Repository.ID
	})
	pool := make([]resourceapp.SUTRepositoryView, 0, len(views))
	seen := map[string]struct{}{}
	for _, view := range views {
		if !view.Enabled || view.Repository.Status != "active" {
			continue
		}
		if _, duplicate := seen[view.Repository.ID]; duplicate {
			continue
		}
		seen[view.Repository.ID] = struct{}{}
		pool = append(pool, view)
	}
	return pool, nil
}

// SetSUTRepositoryBaselineCommit is the resource-context baseline write path.
func (f *fakeCatalog) SetSUTRepositoryBaselineCommit(_ context.Context, sutID, repoID int64, commitSHA string) error {
	associations, ok := f.sutRepos[sutID]
	if !ok {
		return notFound("sut repository")
	}
	for index := range associations {
		if associations[index].Repository.ID != idString(repoID) {
			continue
		}
		associations[index].CodeBaselineCommit = commitSHA
		f.sutRepos[sutID] = associations
		f.baselineWrites = append(f.baselineWrites,
			idString(sutID)+":"+idString(repoID)+"="+commitSHA)
		f.rebuild()
		return nil
	}
	return notFound("sut repository")
}

func (f *fakeCatalog) ListAllAccounts(_ context.Context) ([]resourceapp.AccountView, error) {
	views := make([]resourceapp.AccountView, 0, len(f.accounts))
	for _, account := range f.accounts {
		views = append(views, account)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views, nil
}

var (
	_ resourceapp.CatalogReader               = (*fakeCatalog)(nil)
	_ resourceapp.SUTRepositoryBaselineWriter = (*fakeCatalog)(nil)
)

// ---------- fake version repository ----------

type fakeRepo struct {
	versions map[int64]version.Version
	accounts map[int64][]version.AccountBinding
	stages   map[int64]version.StageState
	catalog  *fakeCatalog

	createErr          error
	updateErr          error
	stats              version.VersionStats
	initialWithContent []int64
	lastInitialLimit   int
	updateCalls        int
	createCalls        int
	lastAccounts       []version.AccountBinding
	lastVersion        version.Version
	lastExpectedUpdate time.Time
	updateStages       []version.Stage
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		versions: map[int64]version.Version{},
		accounts: map[int64][]version.AccountBinding{},
		stages:   map[int64]version.StageState{},
	}
}

func (f *fakeRepo) Create(_ context.Context, v version.Version, accounts []version.AccountBinding) error {
	if f.createErr != nil {
		return f.createErr
	}
	f.createCalls++
	f.versions[v.ID] = v
	f.accounts[v.ID] = accounts
	return nil
}

func (f *fakeRepo) Get(_ context.Context, id int64) (version.Version, []version.AccountBinding, error) {
	v, ok := f.versions[id]
	if !ok {
		return version.Version{}, nil, version.ErrNotFound
	}
	accounts := f.accounts[id]
	copied := make([]version.AccountBinding, len(accounts))
	copy(copied, accounts)
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

func (f *fakeRepo) UpdateBinding(_ context.Context, v version.Version, accounts []version.AccountBinding, expectedUpdatedAt time.Time) error {
	if f.updateErr != nil {
		return f.updateErr
	}
	if _, ok := f.versions[v.ID]; !ok {
		return version.ErrNotFound
	}
	f.updateCalls++
	f.lastVersion = v
	f.lastAccounts = accounts
	f.lastExpectedUpdate = expectedUpdatedAt
	f.versions[v.ID] = v
	f.accounts[v.ID] = accounts
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
		sutCount := 0
		if f.catalog != nil {
			sutCount = len(f.catalog.sutIDsOfEnvironment(v.EnvironmentID))
		}
		summaries = append(summaries, version.Summary{
			ID: v.ID, Title: v.Title, Status: v.Status,
			SUTCount:  sutCount,
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

func (f *fakeRepo) Stats(_ context.Context, _ int64) (version.VersionStats, error) {
	return f.stats, nil
}

func (f *fakeRepo) ListInitialVersionsWithContent(_ context.Context, limit int) ([]int64, error) {
	f.lastInitialLimit = limit
	return f.initialWithContent, nil
}

// boundEnvironmentIDs returns the set of environments some version binds.
func (f *fakeRepo) boundEnvironmentIDs() map[int64][]int64 {
	out := map[int64][]int64{}
	for id, entity := range f.versions {
		if entity.EnvironmentID > 0 {
			out[entity.EnvironmentID] = append(out[entity.EnvironmentID], id)
		}
	}
	return out
}

func (f *fakeRepo) SUTInUse(_ context.Context, id int64) (bool, error) {
	if f.catalog == nil {
		return false, nil
	}
	environmentID := f.catalog.environmentOfSUT(id)
	if environmentID == 0 {
		return false, nil
	}
	for _, entity := range f.versions {
		if entity.EnvironmentID == environmentID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeRepo) EnvironmentInUse(_ context.Context, id int64) (bool, error) {
	for _, entity := range f.versions {
		if entity.EnvironmentID == id {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeRepo) PageInUse(_ context.Context, id int64) (bool, error) {
	return f.assetInUse(id, f.catalog.pages[id].SUTID, f.catalog.pages[id].Status)
}

func (f *fakeRepo) APIInUse(_ context.Context, id int64) (bool, error) {
	return f.assetInUse(id, f.catalog.apis[id].SUTID, f.catalog.apis[id].Status)
}

func (f *fakeRepo) DatabaseInUse(_ context.Context, id int64) (bool, error) {
	return f.assetInUse(id, f.catalog.databases[id].SUTID, f.catalog.databases[id].Status)
}

// assetInUse mirrors the SQL guard: an asset is in use when it is in range
// (approved page/API, active database) and its SUT belongs to an environment
// some version binds. Exclusions no longer exist.
func (f *fakeRepo) assetInUse(_ int64, sutID, status string) (bool, error) {
	if f.catalog == nil || sutID == "" {
		return false, nil
	}
	if status != "approved" && status != "active" {
		return false, nil
	}
	id, err := strconv.ParseInt(sutID, 10, 64)
	if err != nil {
		return false, nil
	}
	environmentID := f.catalog.environmentOfSUT(id)
	for _, entity := range f.versions {
		if entity.EnvironmentID == environmentID {
			return true, nil
		}
	}
	return false, nil
}

func (f *fakeRepo) AccountInUse(_ context.Context, id int64) (bool, error) {
	for _, accounts := range f.accounts {
		for _, account := range accounts {
			if account.AccountID == id {
				return true, nil
			}
		}
	}
	return false, nil
}

func (f *fakeRepo) RepositoryInUse(_ context.Context, id int64) (bool, error) {
	// Any SUT-level association references the repository; the version-level
	// overrides of v0.22 are gone.
	if f.catalog != nil {
		for _, associations := range f.catalog.sutRepos {
			for _, association := range associations {
				if association.Repository.ID == idString(id) {
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
	case "environment":
		for versionID, entity := range f.versions {
			if entity.EnvironmentID == id {
				add(versionID)
			}
		}
	case "sut":
		if f.catalog != nil {
			environmentID := f.catalog.environmentOfSUT(id)
			for versionID, entity := range f.versions {
				if entity.EnvironmentID == environmentID {
					add(versionID)
				}
			}
		}
	case "page", "api", "database":
		if f.catalog != nil {
			var sutID, status string
			switch kind {
			case "page":
				sutID, status = f.catalog.pages[id].SUTID, f.catalog.pages[id].Status
			case "api":
				sutID, status = f.catalog.apis[id].SUTID, f.catalog.apis[id].Status
			case "database":
				sutID, status = f.catalog.databases[id].SUTID, f.catalog.databases[id].Status
			}
			if status == "approved" || status == "active" {
				if sutIDValue, err := strconv.ParseInt(sutID, 10, 64); err == nil {
					environmentID := f.catalog.environmentOfSUT(sutIDValue)
					for versionID, entity := range f.versions {
						if entity.EnvironmentID == environmentID {
							add(versionID)
						}
					}
				}
			}
		}
	case "account":
		for versionID, accounts := range f.accounts {
			for _, account := range accounts {
				if account.AccountID == id {
					add(versionID)
				}
			}
		}
	case "repository":
		if f.catalog != nil {
			sutIDs := map[int64]struct{}{}
			for sutID, associations := range f.catalog.sutRepos {
				for _, association := range associations {
					if association.Repository.ID == idString(id) {
						sutIDs[sutID] = struct{}{}
					}
				}
			}
			for versionID, entity := range f.versions {
				for _, sutID := range f.catalog.sutIDsOfEnvironment(entity.EnvironmentID) {
					if _, ok := sutIDs[sutID]; ok {
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
	if fake != nil {
		svc.SetBaselineWriter(catalog)
	}
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

// storedAccounts returns the persisted version_account subset of one version.
func storedAccounts(t *testing.T, env *testEnv, versionID int64) []version.AccountBinding {
	t.Helper()
	_, accounts, err := env.repo.Get(requestContext(), versionID)
	if err != nil {
		t.Fatalf("stored Get: %v", err)
	}
	return accounts
}

// storedEnvironment returns the persisted environment binding of one version.
func storedEnvironment(t *testing.T, env *testEnv, versionID int64) int64 {
	t.Helper()
	entity, _, err := env.repo.Get(requestContext(), versionID)
	if err != nil {
		t.Fatalf("stored Get: %v", err)
	}
	return entity.EnvironmentID
}

// ---------- tests ----------

// TestCreateVersionEnvironmentOnly is the API-15 contract after the v0.2.4-B
// inversion: creation takes title/description/goal/environment_id only, persists
// environment_id on the version row, binds every active global account, derives
// the SUT catalog / assets / code pool from the environment and pins the code
// baseline of the deterministic baseline repository.
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
	if view.EnvironmentID != 1101 {
		t.Fatalf("environment binding = %d, want 1101", view.EnvironmentID)
	}

	binding := view.Binding
	if binding.Environment.ID != "1101" || binding.Environment.Name != "prod" {
		t.Fatalf("environment = %#v", binding.Environment)
	}
	// The environment owns two SUTs; nothing is derived into a per-version row.
	if len(binding.Environment.SUTs) != 2 ||
		binding.Environment.SUTs[0].ID != "1001" || binding.Environment.SUTs[1].ID != "1002" {
		t.Fatalf("sut catalog = %#v", binding.Environment.SUTs)
	}
	// Effective scope = every approved page / approved API / active database of
	// every SUT of the environment. Proposed and disabled rows stay out.
	if len(binding.Pages) != 3 || binding.Pages[0].ID != "1301" ||
		binding.Pages[1].ID != "1302" || binding.Pages[2].ID != "1303" {
		t.Fatalf("pages = %#v", binding.Pages)
	}
	if len(binding.APIs) != 2 || binding.APIs[0].ID != "1401" || binding.APIs[1].ID != "1402" {
		t.Fatalf("apis = %#v", binding.APIs)
	}
	if len(binding.Databases) != 2 || binding.Databases[0].ID != "1501" || binding.Databases[1].ID != "1502" {
		t.Fatalf("databases = %#v", binding.Databases)
	}
	// Accounts default to the active global pool; disabled accounts are skipped.
	if len(binding.Accounts) != 3 {
		t.Fatalf("accounts = %#v", binding.Accounts)
	}
	// The code pool is the union of the environment's SUT repositories, ordered
	// by repository ID; the baseline is the lowest one.
	if len(binding.Repositories) != 3 ||
		binding.Repositories[0].Repository.ID != "1201" ||
		binding.Repositories[1].Repository.ID != "1202" ||
		binding.Repositories[2].Repository.ID != "1203" {
		t.Fatalf("repositories = %#v", binding.Repositories)
	}
	if binding.Repositories[0].SUTID != 1001 || binding.Repositories[2].SUTID != 1002 {
		t.Fatalf("repository ownership = %#v", binding.Repositories)
	}
	if binding.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("code baseline = %q", binding.CodeBaselineCommit)
	}
	if len(env.resolver.calls) != 1 || env.resolver.calls[0].ID != "1201" {
		t.Fatalf("resolver calls = %#v, want the deterministic 1201 baseline", env.resolver.calls)
	}
	// The resolved commit is persisted on the (SUT, repository) row, which is the
	// store codeintel backfills and execution fingerprints read.
	if len(env.catalog.baselineWrites) != 1 ||
		env.catalog.baselineWrites[0] != "1001:1201="+codeBaselineCommit {
		t.Fatalf("baseline writes = %#v", env.catalog.baselineWrites)
	}

	if got := storedEnvironment(t, env, view.ID); got != 1101 {
		t.Fatalf("stored environment_id = %d, want 1101", got)
	}
	if accounts := storedAccounts(t, env, view.ID); len(accounts) != 3 {
		t.Fatalf("stored accounts = %#v", accounts)
	}
	storedVersion := env.repo.versions[view.ID]
	if storedVersion.Description != "Q4 order flows" || storedVersion.Goal != "verify checkout" {
		t.Fatalf("stored description/goal = %q/%q", storedVersion.Description, storedVersion.Goal)
	}

	// The version.created audit entry carries the environment, not a SUT count.
	if len(env.audit.events) != 1 {
		t.Fatalf("audit = %#v", env.audit.events)
	}
	if env.audit.events[0].Action != "version.created" {
		t.Fatalf("first audit action = %q", env.audit.events[0].Action)
	}
	if got := env.audit.events[0].Metadata["environment_id"]; got != "1101" {
		t.Fatalf("audit environment_id = %#v", env.audit.events[0].Metadata)
	}
	if _, present := env.audit.events[0].Metadata["sut_count"]; present {
		t.Fatalf("sut_count must be gone with the derived binding: %#v", env.audit.events[0].Metadata)
	}
	// One event per channel (version:{id} and system).
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
	env.catalog.sutRepos[1002] = nil
	view, err := env.svc.Create(requestContext(), validCommand(), 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(env.resolver.calls) != 0 {
		t.Fatalf("resolver calls = %#v, want none without a SUT repository", env.resolver.calls)
	}
	if view.Binding.CodeBaselineCommit != "" || len(view.Binding.Repositories) != 0 {
		t.Fatalf("baseline = %#v", view.Binding)
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
			if view.Binding.CodeBaselineCommit != "" {
				t.Fatalf("binding view = %#v, want an empty code baseline", view.Binding)
			}
			if len(env.catalog.baselineWrites) != 0 {
				t.Fatalf("an unresolved baseline must not be persisted: %#v", env.catalog.baselineWrites)
			}
			created, warnings := 0, 0
			for _, entry := range env.audit.events {
				switch entry.Action {
				case "version.created":
					created++
				case "version.code_baseline_unresolved":
					warnings++
					if entry.Metadata["sut_id"] != "1001" || entry.Metadata["repo_id"] != "1201" {
						t.Fatalf("warning metadata = %#v", entry.Metadata)
					}
				}
			}
			if created != 1 || warnings != 1 {
				t.Fatalf("audit actions = %#v", env.audit.events)
			}
		})
	}
}

// TestCreateKeepsAnAlreadyPinnedBaseline proves the baseline store is stable: a
// second version bound to the same environment never re-resolves or overwrites a
// commit the first version (or the code index) already pinned.
func TestCreateKeepsAnAlreadyPinnedBaseline(t *testing.T) {
	env := newTestEnv(t)
	ctx := requestContext()
	if _, err := env.svc.Create(ctx, validCommand(), 42); err != nil {
		t.Fatalf("first Create: %v", err)
	}
	env.resolver.calls = nil
	env.catalog.baselineWrites = nil

	second, err := env.svc.Create(ctx, validCommand(), 42)
	if err != nil {
		t.Fatalf("second Create: %v", err)
	}
	if len(env.resolver.calls) != 0 {
		t.Fatalf("resolver calls = %#v, want none for an already pinned baseline", env.resolver.calls)
	}
	if len(env.catalog.baselineWrites) != 0 {
		t.Fatalf("baseline writes = %#v, want none", env.catalog.baselineWrites)
	}
	if second.Binding.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("baseline = %q, want the pinned commit", second.Binding.CodeBaselineCommit)
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

// TestBindingAccountsAreTheVersionSubset pins DB-14 after the inversion: the
// account selection is the only per-version resource choice left, unknown or
// disabled accounts are typed 400s, and the code pool stays derived (so no
// repository selection is accepted).
func TestBindingAccountsAreTheVersionSubset(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	ctx := requestContext()

	overview, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1602}),
	}, 42)
	if err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	if len(overview.Accounts) != 1 || overview.Accounts[0].ID != "1602" {
		t.Fatalf("accounts = %#v", overview.Accounts)
	}
	if accounts := storedAccounts(t, env, created.ID); len(accounts) != 1 || accounts[0].AccountID != 1602 {
		t.Fatalf("stored accounts = %#v", accounts)
	}
	// The account subset is version-owned, so the environment catalog still shows
	// the SUT assets unchanged.
	if len(overview.Pages) != 3 || len(overview.Repositories) != 3 {
		t.Fatalf("derived scope changed with the accounts: %#v", overview)
	}

	// An explicit empty list clears the selection.
	overview, err = env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{}),
	}, 42)
	if err != nil {
		t.Fatalf("clear accounts: %v", err)
	}
	if len(overview.Accounts) != 0 {
		t.Fatalf("cleared accounts = %#v", overview.Accounts)
	}

	// Unknown and disabled accounts are field-level 400s.
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{9999}),
	}, 42); err == nil {
		t.Fatal("unknown account accepted")
	} else if field := fieldName(t, err); field != "account_ids" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1603}),
	}, 42); err == nil {
		t.Fatal("disabled account accepted")
	} else if field := fieldName(t, err); field != "account_ids" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{0}),
	}, 42); err == nil {
		t.Fatal("non-positive account accepted")
	}
}

// TestBindingEnvironmentSwitchReDerivesTheScope pins the v0.2.4-B rebinding: the
// environment is the only binding key, so switching it re-derives the whole SUT
// catalog, asset unions and code pool, and re-resolves the baseline of the new
// pool only when it is not pinned yet.
func TestBindingEnvironmentSwitchReDerivesTheScope(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	ctx := requestContext()
	env.resolver.calls = nil
	env.catalog.baselineWrites = nil

	overview, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1102),
	}, 42)
	if err != nil {
		t.Fatalf("change environment: %v", err)
	}
	if overview.EnvironmentID != 1102 || overview.Environment.ID != "1102" {
		t.Fatalf("environment = %#v", overview.Environment)
	}
	if len(overview.Environment.SUTs) != 1 || overview.Environment.SUTs[0].ID != "1003" {
		t.Fatalf("sut catalog = %#v", overview.Environment.SUTs)
	}
	if len(overview.Pages) != 1 || overview.Pages[0].ID != "1305" {
		t.Fatalf("pages = %#v", overview.Pages)
	}
	if len(overview.Repositories) != 0 {
		t.Fatalf("the staging environment has no repositories: %#v", overview.Repositories)
	}
	if overview.CodeBaselineCommit != "" {
		t.Fatalf("baseline = %q, want empty without a repository", overview.CodeBaselineCommit)
	}
	if got := storedEnvironment(t, env, created.ID); got != 1102 {
		t.Fatalf("stored environment_id = %d, want 1102", got)
	}
	if len(env.resolver.calls) != 0 {
		t.Fatalf("resolver calls = %#v, want none without a repository", env.resolver.calls)
	}

	// An unknown environment is a field-level 400 and leaves the binding alone.
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(9999),
	}, 42); err == nil {
		t.Fatal("unknown environment accepted")
	} else if field := fieldName(t, err); field != "environment_id" {
		t.Fatalf("field = %q", field)
	}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(0),
	}, 42); err == nil {
		t.Fatal("non-positive environment accepted")
	}
	if got := storedEnvironment(t, env, created.ID); got != 1102 {
		t.Fatalf("a rejected rebind moved the binding to %d", got)
	}
}

func TestBindingStageRules(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	ctx := requestContext()

	readOnlyStages := []version.Stage{
		version.StageExecuting,
		version.StageReporting,
		version.StageCompleted,
	}
	for _, stage := range readOnlyStages {
		env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: stage}
		_, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{
			AccountIDs: int64SlicePtr([]int64{1601}),
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
			AccountIDs: int64SlicePtr([]int64{1601}),
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

	// A completed version is read-only even when the stage projection lags.
	entity := env.repo.versions[created.ID]
	entity.Status = version.StatusCompleted
	env.repo.versions[created.ID] = entity
	env.repo.stages[created.ID] = version.StageState{VersionID: created.ID, Stage: version.StageDesigning}
	if _, err := env.svc.UpdateBinding(ctx, created.ID, versionapp.BindingPatch{}, 42); err == nil {
		t.Fatal("a completed version accepted a binding update")
	} else if got := apiError(t, err).Status; got != http.StatusConflict {
		t.Fatalf("completed status = %d, want 409", got)
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
	if overview.VersionID != created.ID || overview.EnvironmentID != 1101 {
		t.Fatalf("overview identity = %#v", overview)
	}
	// The counters report the whole catalog, including the proposed rows that are
	// not part of the effective scope.
	if overview.Counts.Pages.Approved != 3 || overview.Counts.Pages.Proposed != 1 {
		t.Fatalf("page counts = %#v", overview.Counts.Pages)
	}
	if overview.Counts.APIs.Approved != 2 || overview.Counts.APIs.Proposed != 1 {
		t.Fatalf("api counts = %#v", overview.Counts.APIs)
	}
	if overview.Counts.Databases.Approved != 2 || overview.Counts.Databases.Proposed != 0 {
		t.Fatalf("database counts = %#v", overview.Counts.Databases)
	}
	if overview.Snapshot.ScopeVersion == "" {
		t.Fatal("snapshot reference is empty")
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

// TestResolveEnvironmentIsTheBindingPort pins the port the downstream contexts
// consume in place of the removed version_sut chain.
func TestResolveEnvironmentIsTheBindingPort(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	ctx := requestContext()

	var port versionapp.BindingReader = env.svc
	catalog, err := port.ResolveEnvironment(ctx, created.ID)
	if err != nil {
		t.Fatalf("ResolveEnvironment: %v", err)
	}
	if catalog.VersionID != created.ID || catalog.EnvironmentID != 1101 {
		t.Fatalf("catalog identity = %#v", catalog)
	}
	if len(catalog.Environment.SUTs) != 2 || len(catalog.Pages) != 3 ||
		len(catalog.APIs) != 2 || len(catalog.Databases) != 2 {
		t.Fatalf("catalog = %#v", catalog)
	}
	if len(catalog.Repositories) != 3 || catalog.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("catalog repositories = %#v", catalog.Repositories)
	}
	if len(catalog.Accounts) != 3 {
		t.Fatalf("catalog accounts = %#v", catalog.Accounts)
	}
	if _, err := port.ResolveEnvironment(ctx, 9999); err == nil {
		t.Fatal("a missing version resolved")
	} else if got := apiError(t, err).Status; got != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", got)
	}
	if _, err := port.ResolveEnvironment(ctx, 0); err == nil {
		t.Fatal("a non-positive version id resolved")
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
	if view.EnvironmentID != 1101 {
		t.Fatalf("environment binding = %d", view.EnvironmentID)
	}
	binding := view.Binding
	if binding.Environment.ID != "1101" || len(binding.Environment.SUTs) != 2 {
		t.Fatalf("binding = %#v", binding)
	}
	if len(binding.Repositories) != 3 || binding.Repositories[0].Repository.ID != "1201" {
		t.Fatalf("repositories = %#v", binding.Repositories)
	}
	if binding.CodeBaselineCommit != codeBaselineCommit {
		t.Fatalf("baseline = %q", binding.CodeBaselineCommit)
	}
	if len(binding.Pages) != 3 || len(binding.APIs) != 2 || len(binding.Databases) != 2 || len(binding.Accounts) != 3 {
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
	// SUTCount is derived: environment 1101 owns two SUTs.
	if page[0].SUTCount != 2 || page[0].Title != "Version three" || page[0].Status != "initial" {
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
		// v0.2.4-B: a SUT is referenced when a version binds its environment, so
		// both SUTs of the bound environment are guarded and the SUT of the
		// unbound staging environment is not.
		{"sut of the bound environment", usage.SUTInUse, 1001, true},
		{"second sut of the bound environment", usage.SUTInUse, 1002, true},
		{"sut of an unbound environment", usage.SUTInUse, 1003, false},
		{"environment bound", usage.EnvironmentInUse, 1101, true},
		{"environment unbound", usage.EnvironmentInUse, 1102, false},
		// A resource of the bound environment is in use when it is in range
		// (approved page/API, active database). Proposed and disabled rows are
		// not part of the version range; exclusions no longer exist.
		{"page in range", usage.PageInUse, 1302, true},
		{"page of the second sut", usage.PageInUse, 1303, true},
		{"page proposed", usage.PageInUse, 1304, false},
		{"page of an unbound environment", usage.PageInUse, 1305, false},
		{"api in range", usage.APIInUse, 1401, true},
		{"api proposed", usage.APIInUse, 1403, false},
		{"database in range", usage.DatabaseInUse, 1501, true},
		{"database disabled", usage.DatabaseInUse, 1503, false},
		{"account bound", usage.AccountInUse, 1602, true},
		{"account unbound", usage.AccountInUse, 1603, false},
		{"repository associated with a sut", usage.RepositoryInUse, 1201, true},
		{"repository of the second sut", usage.RepositoryInUse, 1203, true},
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
	// A proposed page and a page of an unbound environment are not referenced.
	for _, id := range []int64{1304, 1305} {
		unreferenced, err := usage.ReferencingVersions(requestContext(), "page", id)
		if err != nil {
			t.Fatalf("ReferencingVersions(page %d): %v", id, err)
		}
		if len(unreferenced) != 0 {
			t.Fatalf("page %d referencing versions = %#v, want none", id, unreferenced)
		}
	}
	// Narrowing the account subset releases the deselected account.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1601}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	after := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"still bound account", usage.AccountInUse, 1601, true},
		{"deselected account", usage.AccountInUse, 1602, false},
		// Rebinding an environment cannot release a SUT-owned asset of the new
		// environment, and releases the assets of the old one.
		{"page still in range", usage.PageInUse, 1302, true},
		{"sut repository association", usage.RepositoryInUse, 1201, true},
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

	// After rebinding to the staging environment the prod assets are released and
	// the staging ones become guarded.
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		EnvironmentID: int64Ptr(1102),
	}, 42); err != nil {
		t.Fatalf("rebind environment: %v", err)
	}
	rebound := []struct {
		name  string
		check func(context.Context, int64) (bool, error)
		id    int64
		want  bool
	}{
		{"previous environment", usage.EnvironmentInUse, 1101, false},
		{"new environment", usage.EnvironmentInUse, 1102, true},
		{"previous sut", usage.SUTInUse, 1001, false},
		{"new sut", usage.SUTInUse, 1003, true},
		{"previous page", usage.PageInUse, 1302, false},
		{"new page", usage.PageInUse, 1305, true},
	}
	for _, tc := range rebound {
		used, err := tc.check(requestContext(), tc.id)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if used != tc.want {
			t.Fatalf("%s(%d) = %v, want %v", tc.name, tc.id, used, tc.want)
		}
	}
	environmentRefs, err := usage.ReferencingVersions(requestContext(), "environment", 1102)
	if err != nil {
		t.Fatalf("ReferencingVersions(environment): %v", err)
	}
	if len(environmentRefs) != 1 || environmentRefs[0].ID != created.ID {
		t.Fatalf("environment referencing versions = %#v", environmentRefs)
	}
	repositoryRefs, err := usage.ReferencingVersions(requestContext(), "repository", 1201)
	if err != nil {
		t.Fatalf("ReferencingVersions(repository): %v", err)
	}
	if len(repositoryRefs) != 0 {
		t.Fatalf("a repository of the unbound environment must not be referenced: %#v", repositoryRefs)
	}
	if _, err := usage.ReferencingVersions(requestContext(), "bogus", 1); err == nil {
		t.Fatal("an unknown resource kind was accepted")
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

// TestKnowledgeBindingKeysUsesEffectiveResources pins the knowledge key
// projection after the inversion: the system IDs are every SUT of the bound
// environment, the page keys are the effective (approved) routes of those SUTs
// and the account roles come from the version account subset.
func TestKnowledgeBindingKeysUsesEffectiveResources(t *testing.T) {
	env := newTestEnv(t)
	created := createVersion(t, env, "Orders Q4")
	if _, err := env.svc.UpdateBinding(requestContext(), created.ID, versionapp.BindingPatch{
		AccountIDs: int64SlicePtr([]int64{1601}),
	}, 42); err != nil {
		t.Fatalf("UpdateBinding: %v", err)
	}
	keys, err := env.svc.KnowledgeBindingKeys(requestContext(), created.ID)
	if err != nil {
		t.Fatalf("KnowledgeBindingKeys: %v", err)
	}
	if keys.VersionID != created.ID {
		t.Fatalf("version id = %d", keys.VersionID)
	}
	if len(keys.EnvironmentIDs) != 1 || keys.EnvironmentIDs[0] != 1101 {
		t.Fatalf("environment ids = %#v", keys.EnvironmentIDs)
	}
	if len(keys.SystemIDs) != 2 || keys.SystemIDs[0] != 1001 || keys.SystemIDs[1] != 1002 {
		t.Fatalf("system ids = %#v", keys.SystemIDs)
	}
	// The three approved pages of the two SUTs; the proposed page 1304 is not a
	// knowledge key because it is not part of the effective scope.
	want := map[string]bool{"/orders": true, "/orders/:id": true, "/invoices": true}
	if len(keys.PageKeys) != len(want) {
		t.Fatalf("page keys = %#v", keys.PageKeys)
	}
	for _, key := range keys.PageKeys {
		if !want[key] {
			t.Fatalf("unexpected page key %q in %#v", key, keys.PageKeys)
		}
	}
	if len(keys.AccountRoles) != 1 || keys.AccountRoles[0] != "admin" {
		t.Fatalf("account roles = %#v", keys.AccountRoles)
	}

	if _, err := env.svc.KnowledgeBindingKeys(requestContext(), 9999); err == nil {
		t.Fatal("a missing version produced keys")
	}
	if _, err := env.svc.KnowledgeBindingKeys(requestContext(), 0); err == nil {
		t.Fatal("a non-positive version id produced keys")
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
