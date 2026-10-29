package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// catalogTestClock is a fixed clock for the catalog service tests.
type catalogTestClock struct {
	now time.Time
}

// Now returns the fixed instant.
func (c catalogTestClock) Now() time.Time { return c.now }

// fakeCatalogCompanyRepo is an in-memory domain.CompanyRepository.
type fakeCatalogCompanyRepo struct {
	company      *domain.Company
	err          error
	requestedKey string
	calls        int
}

// GetCompanyByKey returns the configured company.
func (r *fakeCatalogCompanyRepo) GetCompanyByKey(_ context.Context, key string) (*domain.Company, error) {
	r.calls++
	r.requestedKey = key
	if r.err != nil {
		return nil, r.err
	}
	if r.company == nil {
		return nil, domain.NewNotFound("company", 0)
	}
	company := *r.company
	return &company, nil
}

// fakeCatalogSystemRepo is an in-memory domain.SystemRepository.
type fakeCatalogSystemRepo struct {
	systems       map[int64]domain.System
	nextID        int64
	countEnvs     int
	countAccesses int
	countErr      error
	countCalls    int
	updateCalls   int
	deleteCalls   int
}

func newFakeCatalogSystemRepo() *fakeCatalogSystemRepo {
	return &fakeCatalogSystemRepo{systems: map[int64]domain.System{}, nextID: 1}
}

func (r *fakeCatalogSystemRepo) CreateSystem(_ context.Context, s *domain.System) error {
	if s.Status == "" {
		s.Status = domain.StatusActive
	}
	if s.DefaultSessionTTLSeconds == 0 {
		s.DefaultSessionTTLSeconds = 3600
	}
	s.ID = r.nextID
	r.nextID++
	r.systems[s.ID] = *s
	return nil
}

func (r *fakeCatalogSystemRepo) GetSystem(_ context.Context, id int64) (*domain.System, error) {
	s, ok := r.systems[id]
	if !ok {
		return nil, domain.NewNotFound("system", id)
	}
	return &s, nil
}

func (r *fakeCatalogSystemRepo) ListSystems(_ context.Context, f domain.SystemFilter) ([]domain.System, bool, error) {
	ids := make([]int64, 0, len(r.systems))
	for id := range r.systems {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.System, 0, len(ids))
	for _, id := range ids {
		s := r.systems[id]
		if s.ID <= f.Page.AfterID {
			continue
		}
		if f.Status != "" && s.Status != f.Status {
			continue
		}
		if f.Kind != "" && s.Kind != f.Kind {
			continue
		}
		matches = append(matches, s)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *fakeCatalogSystemRepo) UpdateSystem(_ context.Context, s *domain.System) error {
	if _, ok := r.systems[s.ID]; !ok {
		return domain.NewNotFound("system", s.ID)
	}
	r.updateCalls++
	r.systems[s.ID] = *s
	return nil
}

func (r *fakeCatalogSystemRepo) DeleteSystem(_ context.Context, id int64) error {
	if _, ok := r.systems[id]; !ok {
		return domain.NewNotFound("system", id)
	}
	r.deleteCalls++
	delete(r.systems, id)
	return nil
}

func (r *fakeCatalogSystemRepo) CountEnvironments(_ context.Context, _ int64) (int, error) {
	r.countCalls++
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.countEnvs, nil
}

func (r *fakeCatalogSystemRepo) CountAccesses(_ context.Context, _ int64) (int, error) {
	r.countCalls++
	if r.countErr != nil {
		return 0, r.countErr
	}
	return r.countAccesses, nil
}

// fakeCatalogEnvironmentRepo is an in-memory domain.EnvironmentRepository.
type fakeCatalogEnvironmentRepo struct {
	environments  map[int64]domain.Environment
	nextID        int64
	countSessions int
	updateCalls   int
	deleteCalls   int
}

func newFakeCatalogEnvironmentRepo() *fakeCatalogEnvironmentRepo {
	return &fakeCatalogEnvironmentRepo{environments: map[int64]domain.Environment{}, nextID: 1}
}

func (r *fakeCatalogEnvironmentRepo) CreateEnvironment(_ context.Context, e *domain.Environment) error {
	if e.AllowedHosts == nil {
		e.AllowedHosts = []string{}
	}
	e.ID = r.nextID
	r.nextID++
	r.environments[e.ID] = *e
	return nil
}

func (r *fakeCatalogEnvironmentRepo) GetEnvironment(_ context.Context, id int64) (*domain.Environment, error) {
	e, ok := r.environments[id]
	if !ok {
		return nil, domain.NewNotFound("environment", id)
	}
	return &e, nil
}

func (r *fakeCatalogEnvironmentRepo) ListEnvironments(_ context.Context, systemID int64) ([]domain.Environment, error) {
	ids := make([]int64, 0, len(r.environments))
	for id := range r.environments {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	out := make([]domain.Environment, 0, len(ids))
	for _, id := range ids {
		if env := r.environments[id]; env.SystemID == systemID {
			out = append(out, env)
		}
	}
	return out, nil
}

func (r *fakeCatalogEnvironmentRepo) UpdateEnvironment(_ context.Context, e *domain.Environment) error {
	if _, ok := r.environments[e.ID]; !ok {
		return domain.NewNotFound("environment", e.ID)
	}
	r.updateCalls++
	r.environments[e.ID] = *e
	return nil
}

func (r *fakeCatalogEnvironmentRepo) DeleteEnvironment(_ context.Context, id int64) error {
	if _, ok := r.environments[id]; !ok {
		return domain.NewNotFound("environment", id)
	}
	r.deleteCalls++
	delete(r.environments, id)
	return nil
}

func (r *fakeCatalogEnvironmentRepo) CountSessions(_ context.Context, _ int64) (int, error) {
	return r.countSessions, nil
}

// fakeCatalogAppModelRepo is an in-memory domain.AppModelRepository.
type fakeCatalogAppModelRepo struct {
	nodes       map[int64]domain.AppModelNode
	order       []int64
	nextID      int64
	createCalls int
	updateCalls int
	deleteCalls int
}

func newFakeCatalogAppModelRepo() *fakeCatalogAppModelRepo {
	return &fakeCatalogAppModelRepo{nodes: map[int64]domain.AppModelNode{}, nextID: 1}
}

func (r *fakeCatalogAppModelRepo) CreateAppModel(_ context.Context, n *domain.AppModelNode) error {
	if n.Source == "" {
		n.Source = "manual"
	}
	if len(n.Meta) == 0 {
		n.Meta = json.RawMessage(`{}`)
	}
	n.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.nodes[n.ID] = *n
	r.order = append(r.order, n.ID)
	return nil
}

func (r *fakeCatalogAppModelRepo) GetAppModel(_ context.Context, id int64) (*domain.AppModelNode, error) {
	n, ok := r.nodes[id]
	if !ok {
		return nil, domain.NewNotFound("app model", id)
	}
	return &n, nil
}

func (r *fakeCatalogAppModelRepo) ListAppModels(_ context.Context, f domain.AppModelFilter) ([]domain.AppModelNode, bool, error) {
	var parentID *int64
	if f.ParentKey != "" {
		for id := range r.nodes {
			node := r.nodes[id]
			if node.SystemID == f.SystemID && node.Key == f.ParentKey {
				parentID = &id
				break
			}
		}
		if parentID == nil {
			return []domain.AppModelNode{}, false, nil
		}
	}
	matches := make([]domain.AppModelNode, 0, len(r.order))
	for _, id := range r.order {
		n := r.nodes[id]
		if n.SystemID != f.SystemID || n.ID <= f.Page.AfterID {
			continue
		}
		if f.Kind != "" && n.Kind != f.Kind {
			continue
		}
		if f.ParentKey != "" && (n.ParentID == nil || *n.ParentID != *parentID) {
			continue
		}
		matches = append(matches, n)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *fakeCatalogAppModelRepo) UpdateAppModel(_ context.Context, n *domain.AppModelNode) error {
	if _, ok := r.nodes[n.ID]; !ok {
		return domain.NewNotFound("app model", n.ID)
	}
	r.updateCalls++
	r.nodes[n.ID] = *n
	return nil
}

func (r *fakeCatalogAppModelRepo) DeleteAppModel(_ context.Context, id int64) error {
	if _, ok := r.nodes[id]; !ok {
		return domain.NewNotFound("app model", id)
	}
	r.deleteCalls++
	r.deleteSubtree(id)
	return nil
}

func (r *fakeCatalogAppModelRepo) deleteSubtree(id int64) {
	delete(r.nodes, id)
	for childID, child := range r.nodes {
		if child.ParentID != nil && *child.ParentID == id {
			r.deleteSubtree(childID)
		}
	}
}

func (r *fakeCatalogAppModelRepo) ListAppModelsByKeys(_ context.Context, systemID int64, keys []string) (map[string]domain.AppModelNode, error) {
	wanted := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		wanted[key] = struct{}{}
	}
	out := make(map[string]domain.AppModelNode, len(keys))
	for id := range r.nodes {
		node := r.nodes[id]
		if node.SystemID != systemID {
			continue
		}
		if _, ok := wanted[node.Key]; ok {
			out[node.Key] = node
		}
	}
	return out, nil
}

// catalogTestEnv bundles the fake repositories behind one service.
type catalogTestEnv struct {
	companies *fakeCatalogCompanyRepo
	systems   *fakeCatalogSystemRepo
	envs      *fakeCatalogEnvironmentRepo
	appModels *fakeCatalogAppModelRepo
	clock     catalogTestClock
	service   *CatalogService
}

// newCatalogTestEnv builds a CatalogService wired to fresh fakes and a default
// company, mirroring the seeded production company.
func newCatalogTestEnv(t *testing.T) *catalogTestEnv {
	t.Helper()
	env := &catalogTestEnv{
		companies: &fakeCatalogCompanyRepo{company: &domain.Company{
			ID: 1, Key: DefaultCompanyKey, Name: "Default", Status: domain.StatusActive,
		}},
		systems:   newFakeCatalogSystemRepo(),
		envs:      newFakeCatalogEnvironmentRepo(),
		appModels: newFakeCatalogAppModelRepo(),
		clock:     catalogTestClock{now: time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)},
	}
	env.service = NewCatalogService(env.companies, env.systems, env.envs, env.appModels, env.clock)
	return env
}

// newCatalogServiceWithFakes returns the service and its fake system repository.
func newCatalogServiceWithFakes(t *testing.T) (*CatalogService, *fakeCatalogSystemRepo) {
	t.Helper()
	env := newCatalogTestEnv(t)
	return env.service, env.systems
}

// seedCatalogSystem inserts a system row owned by the default company.
func (env *catalogTestEnv) seedCatalogSystem(id int64, key string) domain.System {
	system := domain.System{
		ID: id, CompanyID: 1, Key: key, Name: key,
		Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335",
		DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	if id >= env.systems.nextID {
		env.systems.nextID = id + 1
	}
	env.systems.systems[id] = system
	return system
}

// seedCatalogAppModel inserts a node directly into the fake repository.
func (env *catalogTestEnv) seedCatalogAppModel(node domain.AppModelNode) domain.AppModelNode {
	if node.Source == "" {
		node.Source = "manual"
	}
	if len(node.Meta) == 0 {
		node.Meta = json.RawMessage(`{}`)
	}
	if node.ID == 0 {
		node.ID = env.appModels.nextID
	}
	if node.ID >= env.appModels.nextID {
		env.appModels.nextID = node.ID + 1
	}
	env.appModels.nodes[node.ID] = node
	env.appModels.order = append(env.appModels.order, node.ID)
	return node
}

// assertCatalogServiceError asserts err is a domain error of kind/reason.
func assertCatalogServiceError(t *testing.T, err error, kind domain.ErrorKind, reason string) *domain.Error {
	t.Helper()
	if err == nil {
		t.Fatalf("want a %s/%s error, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("err=%v (%T), want *domain.Error", err, err)
	}
	if domainErr.Kind != kind {
		t.Fatalf("kind=%q want %q (err=%v)", domainErr.Kind, kind, err)
	}
	if reason != "" && domainErr.Reason != reason {
		t.Fatalf("reason=%q want %q (err=%v)", domainErr.Reason, reason, err)
	}
	return domainErr
}

func TestCatalogServiceCreateSystemUsesDefaultCompany(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.companies.company.ID = 42

	created, err := env.service.CreateSystem(context.Background(), CreateSystemInput{
		Key: "shop", Name: "Shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335",
	})
	if err != nil {
		t.Fatalf("CreateSystem: %v", err)
	}
	if env.companies.requestedKey != DefaultCompanyKey {
		t.Fatalf("company key=%q want %q", env.companies.requestedKey, DefaultCompanyKey)
	}
	if created.CompanyID != 42 {
		t.Fatalf("company id=%d want 42", created.CompanyID)
	}
	if created.Status != domain.StatusActive {
		t.Fatalf("status=%q want active", created.Status)
	}
	if created.DefaultSessionTTLSeconds != 3600 {
		t.Fatalf("ttl=%d want the 3600 default", created.DefaultSessionTTLSeconds)
	}
	if created.ID == 0 {
		t.Fatal("created system has no id")
	}
	if stored, ok := env.systems.systems[created.ID]; !ok || stored.Key != "shop" {
		t.Fatalf("stored system=%+v ok=%v", stored, ok)
	}
}

func TestCatalogServiceCreateSystemValidations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateSystemInput)
		reason string
		field  string
	}{
		{"uppercase key", func(in *CreateSystemInput) { in.Key = "Shop" }, "invalid_key", "key"},
		{"short key", func(in *CreateSystemInput) { in.Key = "a" }, "invalid_key", "key"},
		{"empty base url", func(in *CreateSystemInput) { in.BaseURL = "" }, "invalid_base_url", "baseUrl"},
		{"unparseable base url", func(in *CreateSystemInput) { in.BaseURL = "not a url" }, "invalid_base_url", "baseUrl"},
		{"non http base url", func(in *CreateSystemInput) { in.BaseURL = "ftp://example.test" }, "invalid_base_url", "baseUrl"},
		{"unknown kind", func(in *CreateSystemInput) { in.Kind = "mobile" }, "invalid_kind", "kind"},
		{"ttl too small", func(in *CreateSystemInput) { in.DefaultSessionTTLSeconds = 59 }, "invalid_session_ttl", "defaultSessionTtlSeconds"},
		{"ttl too large", func(in *CreateSystemInput) { in.DefaultSessionTTLSeconds = 604801 }, "invalid_session_ttl", "defaultSessionTtlSeconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCatalogTestEnv(t)
			in := CreateSystemInput{Key: "shop", Name: "Shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335"}
			tc.mutate(&in)
			_, err := env.service.CreateSystem(context.Background(), in)
			domainErr := assertCatalogServiceError(t, err, domain.KindInvalid, tc.reason)
			if domainErr.Field != tc.field {
				t.Fatalf("field=%q want %q", domainErr.Field, tc.field)
			}
			if len(env.systems.systems) != 0 {
				t.Fatalf("invalid create wrote %d systems", len(env.systems.systems))
			}
		})
	}
}

func TestCatalogServiceCreateSystemMissingCompany(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.companies.company = nil

	_, err := env.service.CreateSystem(context.Background(), CreateSystemInput{
		Key: "shop", Name: "Shop", Kind: domain.SystemKindWeb, BaseURL: "http://localhost:13335",
	})
	if !errors.Is(err, ErrDefaultCompanyMissing) {
		t.Fatalf("err=%v want ErrDefaultCompanyMissing", err)
	}
}

func TestCatalogServiceUpdateSystem(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	name := "Shop v2"
	updated, err := env.service.UpdateSystem(context.Background(), 7, UpdateSystemInput{Name: &name})
	if err != nil {
		t.Fatalf("UpdateSystem: %v", err)
	}
	if updated.Name != name || updated.Key != "shop" || updated.Kind != domain.SystemKindWeb ||
		updated.BaseURL != "http://localhost:13335" || updated.DefaultSessionTTLSeconds != 3600 ||
		updated.Status != domain.StatusActive {
		t.Fatalf("partial update changed unrelated fields: %+v", updated)
	}

	kind := domain.SystemKindAPI
	baseURL := "https://shop.example.test"
	ttl := 7200
	status := domain.StatusDisabled
	updated, err = env.service.UpdateSystem(context.Background(), 7, UpdateSystemInput{
		Kind: &kind, BaseURL: &baseURL, DefaultSessionTTLSeconds: &ttl, Status: &status,
	})
	if err != nil {
		t.Fatalf("UpdateSystem full: %v", err)
	}
	if updated.Kind != kind || updated.BaseURL != baseURL || updated.DefaultSessionTTLSeconds != ttl || updated.Status != status {
		t.Fatalf("updated system=%+v", updated)
	}

	if _, err := env.service.UpdateSystem(context.Background(), 404, UpdateSystemInput{Name: &name}); err == nil {
		t.Fatal("UpdateSystem on a missing system: want error")
	}

	badURL := "ftp://example.test"
	callsBefore := env.systems.updateCalls
	_, err = env.service.UpdateSystem(context.Background(), 7, UpdateSystemInput{BaseURL: &badURL})
	assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_base_url")
	if env.systems.updateCalls != callsBefore {
		t.Fatal("invalid update reached the repository")
	}
}

func TestCatalogServiceDeleteSystemRejectsDependencies(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	env.systems.countEnvs = 1

	err := env.service.DeleteSystem(context.Background(), 7)
	assertCatalogServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.systems.deleteCalls != 0 {
		t.Fatal("system was deleted despite environments")
	}

	env.systems.countEnvs = 0
	env.systems.countAccesses = 1
	err = env.service.DeleteSystem(context.Background(), 7)
	assertCatalogServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.systems.deleteCalls != 0 {
		t.Fatal("system was deleted despite accesses")
	}

	env.systems.countAccesses = 0
	if err := env.service.DeleteSystem(context.Background(), 7); err != nil {
		t.Fatalf("DeleteSystem: %v", err)
	}
	if env.systems.deleteCalls != 1 {
		t.Fatalf("delete calls=%d want 1", env.systems.deleteCalls)
	}
}

func TestCatalogServiceListSystemsHasMore(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(1, "one")
	env.seedCatalogSystem(2, "two")
	env.seedCatalogSystem(3, "three")

	systems, more, err := env.service.ListSystems(context.Background(), domain.SystemFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatalf("ListSystems: %v", err)
	}
	if len(systems) != 2 || !more {
		t.Fatalf("systems=%d more=%v", len(systems), more)
	}

	rest, more, err := env.service.ListSystems(context.Background(), domain.SystemFilter{
		Page: domain.Page{Limit: 2, AfterID: systems[len(systems)-1].ID},
	})
	if err != nil {
		t.Fatalf("ListSystems page 2: %v", err)
	}
	if len(rest) != 1 || more || rest[0].Key != "three" {
		t.Fatalf("rest=%+v more=%v", rest, more)
	}
}

func TestCatalogServiceCreateEnvironment(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	created, err := env.service.CreateEnvironment(context.Background(), 7, CreateEnvironmentInput{
		Key: "dev", Name: "Development", BaseURL: "http://localhost:13335/", Kind: domain.EnvironmentKindDev,
		LoginCheck: &domain.LoginCheck{URL: "/account", ExpectStatus: []int{200}},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}
	if created.SystemID != 7 || created.Key != "dev" || created.BaseURL != "http://localhost:13335/" {
		t.Fatalf("created environment=%+v", created)
	}
	if created.AllowedHosts == nil || len(created.AllowedHosts) != 0 {
		t.Fatalf("allowedHosts=%v want a non-nil empty slice", created.AllowedHosts)
	}
	if created.LoginCheck == nil || created.LoginCheck.URL != "/account" {
		t.Fatalf("loginCheck=%+v", created.LoginCheck)
	}

	if _, err := env.service.CreateEnvironment(context.Background(), 404, CreateEnvironmentInput{
		Key: "dev", Name: "Dev", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
	}); err == nil {
		t.Fatal("CreateEnvironment on a missing system: want error")
	}
}

func TestCatalogServiceCreateEnvironmentValidations(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CreateEnvironmentInput)
		reason string
	}{
		{"invalid key", func(in *CreateEnvironmentInput) { in.Key = "Dev" }, "invalid_key"},
		{"invalid base url", func(in *CreateEnvironmentInput) { in.BaseURL = "localhost" }, "invalid_base_url"},
		{"invalid kind", func(in *CreateEnvironmentInput) { in.Kind = "qa" }, "invalid_kind"},
		{"invalid login check", func(in *CreateEnvironmentInput) {
			in.LoginCheck = &domain.LoginCheck{ExpectStatus: []int{42}}
		}, "invalid_login_check"},
		{"too many allowed hosts", func(in *CreateEnvironmentInput) {
			hosts := make([]string, 51)
			for i := range hosts {
				hosts[i] = fmt.Sprintf("host-%d", i)
			}
			in.AllowedHosts = hosts
		}, "invalid_allowed_hosts"},
		{"allowed host too long", func(in *CreateEnvironmentInput) {
			in.AllowedHosts = []string{strings.Repeat("a", 254)}
		}, "invalid_allowed_hosts"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCatalogTestEnv(t)
			env.seedCatalogSystem(7, "shop")
			in := CreateEnvironmentInput{
				Key: "dev", Name: "Dev", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
			}
			tc.mutate(&in)
			_, err := env.service.CreateEnvironment(context.Background(), 7, in)
			assertCatalogServiceError(t, err, domain.KindInvalid, tc.reason)
			if len(env.envs.environments) != 0 {
				t.Fatal("invalid create wrote an environment")
			}
		})
	}
}

func TestCatalogServiceUpdateEnvironment(t *testing.T) {
	env := newCatalogTestEnv(t)
	system := env.seedCatalogSystem(7, "shop")
	seeded, err := env.service.CreateEnvironment(context.Background(), system.ID, CreateEnvironmentInput{
		Key: "dev", Name: "Dev", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
		LoginCheck: &domain.LoginCheck{URL: "/account"},
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	name := "Dev v2"
	hosts := []string{"localhost:13335"}
	updated, err := env.service.UpdateEnvironment(context.Background(), seeded.ID, UpdateEnvironmentInput{
		Name: &name, AllowedHosts: &hosts,
	})
	if err != nil {
		t.Fatalf("UpdateEnvironment: %v", err)
	}
	if updated.Name != name || len(updated.AllowedHosts) != 1 ||
		updated.Kind != domain.EnvironmentKindDev || updated.BaseURL != "http://localhost:13335" {
		t.Fatalf("partial update changed unrelated fields: %+v", updated)
	}
	if updated.LoginCheck == nil {
		t.Fatal("omitted loginCheck was cleared")
	}

	updated, err = env.service.UpdateEnvironment(context.Background(), seeded.ID, UpdateEnvironmentInput{ClearLoginCheck: true})
	if err != nil {
		t.Fatalf("UpdateEnvironment clear: %v", err)
	}
	if updated.LoginCheck != nil {
		t.Fatalf("loginCheck=%+v want nil after clear", updated.LoginCheck)
	}

	badCheck := &domain.LoginCheck{BodyContains: strings.Repeat("a", 513)}
	_, err = env.service.UpdateEnvironment(context.Background(), seeded.ID, UpdateEnvironmentInput{LoginCheck: badCheck})
	assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_login_check")
}

func TestCatalogServiceDeleteEnvironmentRejectsSessions(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	seeded, err := env.service.CreateEnvironment(context.Background(), 7, CreateEnvironmentInput{
		Key: "dev", Name: "Dev", BaseURL: "http://localhost:13335", Kind: domain.EnvironmentKindDev,
	})
	if err != nil {
		t.Fatalf("CreateEnvironment: %v", err)
	}

	env.envs.countSessions = 1
	err = env.service.DeleteEnvironment(context.Background(), seeded.ID)
	assertCatalogServiceError(t, err, domain.KindDependency, "has_dependencies")
	if env.envs.deleteCalls != 0 {
		t.Fatal("environment was deleted despite sessions")
	}

	env.envs.countSessions = 0
	if err := env.service.DeleteEnvironment(context.Background(), seeded.ID); err != nil {
		t.Fatalf("DeleteEnvironment: %v", err)
	}
	if env.envs.deleteCalls != 1 {
		t.Fatalf("delete calls=%d want 1", env.envs.deleteCalls)
	}
}

func TestCatalogServiceCreateAppModel(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	root, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "page:/orders", Kind: domain.AppModelKindPage, Name: "Orders", Path: "/orders",
	})
	if err != nil {
		t.Fatalf("CreateAppModel root: %v", err)
	}
	if root.ParentID != nil || root.Source != "manual" || root.SystemID != 7 {
		t.Fatalf("root node=%+v", root)
	}

	child, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/orders", Kind: domain.AppModelKindRoute, Name: "Route", Path: "/orders",
		ParentKey: "page:/orders",
	})
	if err != nil {
		t.Fatalf("CreateAppModel child: %v", err)
	}
	if child.ParentID == nil || *child.ParentID != root.ID {
		t.Fatalf("child parent=%v want %d", child.ParentID, root.ID)
	}

	api, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "api:GET /api/orders", Kind: domain.AppModelKindAPI, Name: "List", Path: "/api/orders",
		ParentKey: "route:/orders",
	})
	if err != nil {
		t.Fatalf("CreateAppModel api: %v", err)
	}
	if api.Method != "GET" {
		t.Fatalf("api method=%q want the method derived from the key", api.Method)
	}
}

func TestCatalogServiceCreateAppModelRejectsInvalidParent(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	_, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/orphan", Kind: domain.AppModelKindRoute, ParentKey: "page:/missing",
	})
	assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_app_model_parent")

	_, err = env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/self", Kind: domain.AppModelKindRoute, ParentKey: "route:/self",
	})
	assertCatalogServiceError(t, err, domain.KindInvalid, "app_model_cycle")

	_, err = env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/bad", Kind: domain.AppModelKindPage,
	})
	assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_app_model_key")

	if _, err := env.service.CreateAppModel(context.Background(), 404, AppModelInput{
		Key: "route:/x", Kind: domain.AppModelKindRoute,
	}); err == nil {
		t.Fatal("CreateAppModel on a missing system: want error")
	}
	if env.appModels.createCalls != 0 {
		t.Fatalf("invalid create wrote %d nodes", env.appModels.createCalls)
	}
}

func TestCatalogServiceAppModelDepthLimit(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	// 32 chained nodes: node 32 sits at the maximum allowed depth.
	parentKey := ""
	for i := 1; i <= 32; i++ {
		key := fmt.Sprintf("route:/n%d", i)
		node := env.seedCatalogAppModel(domain.AppModelNode{
			SystemID: 7, Kind: domain.AppModelKindRoute, Key: key,
		})
		if parentKey != "" {
			var parentID int64
			for id, candidate := range env.appModels.nodes {
				if candidate.Key == parentKey {
					parentID = id
				}
			}
			node.ParentID = &parentID
			env.appModels.nodes[node.ID] = node
		}
		parentKey = key
	}

	// Attaching to depth 31 yields depth 32 and is allowed.
	if _, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/depth-32", Kind: domain.AppModelKindRoute, ParentKey: "route:/n31",
	}); err != nil {
		t.Fatalf("depth 32 create: %v", err)
	}

	// Attaching to depth 32 would yield depth 33: rejected.
	_, err := env.service.CreateAppModel(context.Background(), 7, AppModelInput{
		Key: "route:/depth-33", Kind: domain.AppModelKindRoute, ParentKey: "route:/n32",
	})
	assertCatalogServiceError(t, err, domain.KindInvalid, "app_model_depth_exceeded")
}

func TestCatalogServiceUpdateAndDeleteAppModel(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	page := env.seedCatalogAppModel(domain.AppModelNode{
		SystemID: 7, Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Orders", Path: "/orders",
	})
	other := env.seedCatalogAppModel(domain.AppModelNode{
		SystemID: 7, Kind: domain.AppModelKindPage, Key: "page:/home", Name: "Home", Path: "/home",
	})

	updated, err := env.service.UpdateAppModel(context.Background(), page.ID, AppModelInput{
		Name: "Orders v2", Path: "/orders/v2",
	})
	if err != nil {
		t.Fatalf("UpdateAppModel: %v", err)
	}
	if updated.Name != "Orders v2" || updated.Path != "/orders/v2" || updated.Key != page.Key || updated.Kind != page.Kind {
		t.Fatalf("updated node=%+v", updated)
	}
	if updated.Source != "manual" {
		t.Fatalf("source=%q want manual to be preserved", updated.Source)
	}

	moved, err := env.service.UpdateAppModel(context.Background(), page.ID, AppModelInput{
		Name: "Orders v2", Path: "/orders/v2", ParentKey: other.Key,
	})
	if err != nil {
		t.Fatalf("UpdateAppModel move: %v", err)
	}
	if moved.ParentID == nil || *moved.ParentID != other.ID {
		t.Fatalf("parent=%v want %d", moved.ParentID, other.ID)
	}

	_, err = env.service.UpdateAppModel(context.Background(), other.ID, AppModelInput{
		Name: "Home", Path: "/home", ParentKey: page.Key,
	})
	assertCatalogServiceError(t, err, domain.KindInvalid, "app_model_cycle")

	if err := env.service.DeleteAppModel(context.Background(), page.ID); err != nil {
		t.Fatalf("DeleteAppModel: %v", err)
	}
	if env.appModels.deleteCalls != 1 {
		t.Fatalf("delete calls=%d want 1", env.appModels.deleteCalls)
	}
	if err := env.service.DeleteAppModel(context.Background(), 404); err == nil {
		t.Fatal("DeleteAppModel on a missing node: want error")
	}
}

func TestCatalogServiceImportAppModelsUpserts(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	existing := env.seedCatalogAppModel(domain.AppModelNode{
		SystemID: 7, Kind: domain.AppModelKindPage, Key: "page:/orders", Name: "Old", Path: "/orders",
	})
	untouched := env.seedCatalogAppModel(domain.AppModelNode{
		SystemID: 7, Kind: domain.AppModelKindComponent, Key: "component:Chart", Name: "Chart",
	})

	created, updated, err := env.service.ImportAppModels(context.Background(), 7, []AppModelInput{
		{Key: "page:/orders", Kind: domain.AppModelKindPage, Name: "Orders", Path: "/orders"},
		{Key: "route:/orders", Kind: domain.AppModelKindRoute, Name: "Route", Path: "/orders", ParentKey: "page:/orders"},
		{Key: "api:GET /api/orders", Kind: domain.AppModelKindAPI, Name: "List", Path: "/api/orders", Method: "GET", ParentKey: "route:/orders"},
	})
	if err != nil {
		t.Fatalf("ImportAppModels: %v", err)
	}
	if created != 2 || updated != 1 {
		t.Fatalf("created=%d updated=%d want 2/1", created, updated)
	}

	page := env.appModels.nodes[existing.ID]
	if page.Name != "Orders" || page.Source != "import" {
		t.Fatalf("updated page=%+v want name Orders and source import", page)
	}
	if _, ok := env.appModels.nodes[untouched.ID]; !ok {
		t.Fatal("node absent from the payload was deleted")
	}

	var route, api domain.AppModelNode
	for _, node := range env.appModels.nodes {
		switch node.Key {
		case "route:/orders":
			route = node
		case "api:GET /api/orders":
			api = node
		}
	}
	if route.Source != "import" || route.ParentID == nil || *route.ParentID != existing.ID {
		t.Fatalf("route=%+v", route)
	}
	if api.Source != "import" || api.ParentID == nil || *api.ParentID != route.ID {
		t.Fatalf("api=%+v want parent %d", api, route.ID)
	}
	if api.Method != "GET" {
		t.Fatalf("api method=%q", api.Method)
	}
}

func TestCatalogServiceImportAppModelsDeduplicatesKeys(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	created, updated, err := env.service.ImportAppModels(context.Background(), 7, []AppModelInput{
		{Key: "page:/orders", Kind: domain.AppModelKindPage, Name: "First", Path: "/orders"},
		{Key: "page:/orders", Kind: domain.AppModelKindPage, Name: "Second", Path: "/orders"},
	})
	if err != nil {
		t.Fatalf("ImportAppModels: %v", err)
	}
	if created != 1 || updated != 0 {
		t.Fatalf("created=%d updated=%d want 1/0", created, updated)
	}
	count := 0
	for _, node := range env.appModels.nodes {
		if node.Key == "page:/orders" {
			count++
			if node.Name != "Second" {
				t.Fatalf("node name=%q want the last payload value", node.Name)
			}
		}
	}
	if count != 1 {
		t.Fatalf("stored %d nodes for one key", count)
	}
}

func TestCatalogServiceImportAppModelsRejectsInvalidGraph(t *testing.T) {
	cases := []struct {
		name   string
		inputs []AppModelInput
		reason string
	}{
		{
			name:   "missing parent",
			inputs: []AppModelInput{{Key: "route:/x", Kind: domain.AppModelKindRoute, ParentKey: "page:/missing"}},
			reason: "invalid_app_model_parent",
		},
		{
			name: "self cycle",
			inputs: []AppModelInput{
				{Key: "route:/x", Kind: domain.AppModelKindRoute, ParentKey: "route:/x"},
			},
			reason: "app_model_cycle",
		},
		{
			name: "two-node cycle",
			inputs: []AppModelInput{
				{Key: "route:/a", Kind: domain.AppModelKindRoute, ParentKey: "route:/b"},
				{Key: "route:/b", Kind: domain.AppModelKindRoute, ParentKey: "route:/a"},
			},
			reason: "app_model_cycle",
		},
		{
			name:   "invalid key prefix",
			inputs: []AppModelInput{{Key: "route:/x", Kind: domain.AppModelKindPage}},
			reason: "invalid_app_model_key",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := newCatalogTestEnv(t)
			env.seedCatalogSystem(7, "shop")
			_, _, err := env.service.ImportAppModels(context.Background(), 7, tc.inputs)
			assertCatalogServiceError(t, err, domain.KindInvalid, tc.reason)
			if env.appModels.createCalls != 0 || env.appModels.updateCalls != 0 {
				t.Fatal("an invalid import wrote nodes")
			}
		})
	}

	if _, _, err := newCatalogTestEnv(t).service.ImportAppModels(context.Background(), 404, nil); err == nil {
		t.Fatal("import into a missing system: want error")
	}
}

func TestCatalogServiceImportAppModelsDepthLimit(t *testing.T) {
	buildChain := func(n int) []AppModelInput {
		inputs := make([]AppModelInput, 0, n)
		for i := 1; i <= n; i++ {
			parent := ""
			if i > 1 {
				parent = fmt.Sprintf("route:/n%d", i-1)
			}
			inputs = append(inputs, AppModelInput{
				Key: fmt.Sprintf("route:/n%d", i), Kind: domain.AppModelKindRoute, ParentKey: parent,
			})
		}
		return inputs
	}

	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	created, updated, err := env.service.ImportAppModels(context.Background(), 7, buildChain(32))
	if err != nil {
		t.Fatalf("32-node chain: %v", err)
	}
	if created != 32 || updated != 0 {
		t.Fatalf("created=%d updated=%d", created, updated)
	}

	env = newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")
	_, _, err = env.service.ImportAppModels(context.Background(), 7, buildChain(33))
	assertCatalogServiceError(t, err, domain.KindInvalid, "app_model_depth_exceeded")
	if env.appModels.createCalls != 0 {
		t.Fatal("an over-deep import wrote nodes")
	}
}

func TestCatalogServiceImportAppModelsRejectsTooManyNodes(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	inputs := make([]AppModelInput, 5001)
	for i := range inputs {
		inputs[i] = AppModelInput{
			Key:  fmt.Sprintf("route:/n%d", i),
			Kind: domain.AppModelKindRoute,
		}
	}
	_, _, err := env.service.ImportAppModels(context.Background(), 7, inputs)
	assertCatalogServiceError(t, err, domain.KindInvalid, "too_many_nodes")
	if env.appModels.createCalls != 0 {
		t.Fatal("an oversized import wrote nodes")
	}
}

// TestImportAppModelsDefaultsToImport pins the P5 source default: callers that
// leave AppModelInput.Source empty keep the P2 import behavior.
func TestImportAppModelsDefaultsToImport(t *testing.T) {
	env := newCatalogTestEnv(t)
	env.seedCatalogSystem(7, "shop")

	created, updated, err := env.service.ImportAppModels(context.Background(), 7, []AppModelInput{
		{Key: "page:/home", Kind: domain.AppModelKindPage, Name: "Home", Path: "/home", Source: ""},
	})
	if err != nil {
		t.Fatalf("ImportAppModels: %v", err)
	}
	if created != 1 || updated != 0 {
		t.Fatalf("created=%d updated=%d want 1/0", created, updated)
	}
	if node := env.appModels.nodes[1]; node.Source != domain.AppModelSourceImport {
		t.Fatalf("source=%q want %q", node.Source, domain.AppModelSourceImport)
	}
}

// TestImportAppModelsAcceptsExploredAndRejectsOther pins the P5 source
// tri-state: manual/import/explored are stored as-is and any other value is a
// 422 without writing nodes.
func TestImportAppModelsAcceptsExploredAndRejectsOther(t *testing.T) {
	for _, source := range []string{domain.AppModelSourceManual, domain.AppModelSourceExplored} {
		t.Run("accepts_"+source, func(t *testing.T) {
			env := newCatalogTestEnv(t)
			env.seedCatalogSystem(7, "shop")

			created, updated, err := env.service.ImportAppModels(context.Background(), 7, []AppModelInput{
				{Key: "page:/home", Kind: domain.AppModelKindPage, Name: "Home", Path: "/home", Source: source},
			})
			if err != nil {
				t.Fatalf("ImportAppModels(%q): %v", source, err)
			}
			if created != 1 || updated != 0 {
				t.Fatalf("created=%d updated=%d want 1/0", created, updated)
			}
			if node := env.appModels.nodes[1]; node.Source != source {
				t.Fatalf("source=%q want %q", node.Source, source)
			}
		})
	}

	for _, source := range []string{"ai", "explore", "EXPLORED"} {
		t.Run("rejects_"+source, func(t *testing.T) {
			env := newCatalogTestEnv(t)
			env.seedCatalogSystem(7, "shop")

			_, _, err := env.service.ImportAppModels(context.Background(), 7, []AppModelInput{
				{Key: "page:/home", Kind: domain.AppModelKindPage, Name: "Home", Path: "/home", Source: source},
			})
			assertCatalogServiceError(t, err, domain.KindInvalid, "invalid_app_model_source")
			if env.appModels.createCalls != 0 || env.appModels.updateCalls != 0 {
				t.Fatal("an invalid source wrote nodes")
			}
		})
	}
}
