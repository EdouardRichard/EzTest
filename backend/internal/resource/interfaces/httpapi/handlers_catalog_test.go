package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

const (
	catalogAdminToken = "admin-token"
	catalogUserToken  = "user-token"
)

// catalogHTTPTestClock is a fixed clock for the catalog handler tests.
type catalogHTTPTestClock struct {
	now time.Time
}

// Now returns the fixed instant.
func (c catalogHTTPTestClock) Now() time.Time { return c.now }

// catalogHTTPTestAuthenticator resolves the two test tokens to their users.
type catalogHTTPTestAuthenticator struct{}

// Authenticate maps a raw bearer token to the seeded admin or regular user.
func (catalogHTTPTestAuthenticator) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	switch rawToken {
	case catalogAdminToken:
		return identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}, nil
	case catalogUserToken:
		return identity.User{ID: 2, Username: "user", Role: identity.RoleUser, Status: identity.StatusActive}, nil
	default:
		return identity.User{}, identity.ErrUnauthenticated
	}
}

// catalogHTTPTestCompanyRepo is an in-memory domain.CompanyRepository.
type catalogHTTPTestCompanyRepo struct{}

// GetCompanyByKey always returns the seeded default company.
func (catalogHTTPTestCompanyRepo) GetCompanyByKey(_ context.Context, key string) (*domain.Company, error) {
	if key != application.DefaultCompanyKey {
		return nil, domain.NewNotFound("company", 0)
	}
	return &domain.Company{ID: 1, Key: key, Name: "Default", Status: domain.StatusActive}, nil
}

// catalogHTTPTestSystemRepo is an in-memory domain.SystemRepository.
type catalogHTTPTestSystemRepo struct {
	systems       map[int64]domain.System
	nextID        int64
	countEnvs     int
	countAccesses int
	createCalls   int
	deleteCalls   int
}

func newCatalogHTTPTestSystemRepo() *catalogHTTPTestSystemRepo {
	return &catalogHTTPTestSystemRepo{systems: map[int64]domain.System{}, nextID: 1}
}

func (r *catalogHTTPTestSystemRepo) seed(key string) domain.System {
	system := domain.System{
		ID: r.nextID, CompanyID: 1, Key: key, Name: key, Kind: domain.SystemKindWeb,
		BaseURL: "http://localhost:13335", DefaultSessionTTLSeconds: 3600, Status: domain.StatusActive,
	}
	r.nextID++
	r.systems[system.ID] = system
	return system
}

func (r *catalogHTTPTestSystemRepo) CreateSystem(_ context.Context, s *domain.System) error {
	r.createCalls++
	s.ID = r.nextID
	r.nextID++
	if s.Status == "" {
		s.Status = domain.StatusActive
	}
	if s.DefaultSessionTTLSeconds == 0 {
		s.DefaultSessionTTLSeconds = 3600
	}
	s.CreatedAt = time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	s.UpdatedAt = s.CreatedAt
	r.systems[s.ID] = *s
	return nil
}

func (r *catalogHTTPTestSystemRepo) GetSystem(_ context.Context, id int64) (*domain.System, error) {
	s, ok := r.systems[id]
	if !ok {
		return nil, domain.NewNotFound("system", id)
	}
	return &s, nil
}

func (r *catalogHTTPTestSystemRepo) ListSystems(_ context.Context, f domain.SystemFilter) ([]domain.System, bool, error) {
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
		limit = platform.DefaultLimit
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *catalogHTTPTestSystemRepo) UpdateSystem(_ context.Context, s *domain.System) error {
	if _, ok := r.systems[s.ID]; !ok {
		return domain.NewNotFound("system", s.ID)
	}
	r.systems[s.ID] = *s
	return nil
}

func (r *catalogHTTPTestSystemRepo) DeleteSystem(_ context.Context, id int64) error {
	if _, ok := r.systems[id]; !ok {
		return domain.NewNotFound("system", id)
	}
	r.deleteCalls++
	delete(r.systems, id)
	return nil
}

func (r *catalogHTTPTestSystemRepo) CountEnvironments(_ context.Context, _ int64) (int, error) {
	return r.countEnvs, nil
}

func (r *catalogHTTPTestSystemRepo) CountAccesses(_ context.Context, _ int64) (int, error) {
	return r.countAccesses, nil
}

// catalogHTTPTestEnvironmentRepo is an in-memory domain.EnvironmentRepository.
type catalogHTTPTestEnvironmentRepo struct {
	environments  map[int64]domain.Environment
	nextID        int64
	countSessions int
	deleteCalls   int
}

func newCatalogHTTPTestEnvironmentRepo() *catalogHTTPTestEnvironmentRepo {
	return &catalogHTTPTestEnvironmentRepo{environments: map[int64]domain.Environment{}, nextID: 1}
}

func (r *catalogHTTPTestEnvironmentRepo) CreateEnvironment(_ context.Context, e *domain.Environment) error {
	if e.AllowedHosts == nil {
		e.AllowedHosts = []string{}
	}
	e.ID = r.nextID
	r.nextID++
	r.environments[e.ID] = *e
	return nil
}

func (r *catalogHTTPTestEnvironmentRepo) GetEnvironment(_ context.Context, id int64) (*domain.Environment, error) {
	e, ok := r.environments[id]
	if !ok {
		return nil, domain.NewNotFound("environment", id)
	}
	return &e, nil
}

func (r *catalogHTTPTestEnvironmentRepo) ListEnvironments(_ context.Context, systemID int64) ([]domain.Environment, error) {
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

func (r *catalogHTTPTestEnvironmentRepo) UpdateEnvironment(_ context.Context, e *domain.Environment) error {
	if _, ok := r.environments[e.ID]; !ok {
		return domain.NewNotFound("environment", e.ID)
	}
	r.environments[e.ID] = *e
	return nil
}

func (r *catalogHTTPTestEnvironmentRepo) DeleteEnvironment(_ context.Context, id int64) error {
	if _, ok := r.environments[id]; !ok {
		return domain.NewNotFound("environment", id)
	}
	r.deleteCalls++
	delete(r.environments, id)
	return nil
}

func (r *catalogHTTPTestEnvironmentRepo) CountSessions(_ context.Context, _ int64) (int, error) {
	return r.countSessions, nil
}

// catalogHTTPTestAppModelRepo is an in-memory domain.AppModelRepository.
type catalogHTTPTestAppModelRepo struct {
	nodes       map[int64]domain.AppModelNode
	order       []int64
	nextID      int64
	createCalls int
}

func newCatalogHTTPTestAppModelRepo() *catalogHTTPTestAppModelRepo {
	return &catalogHTTPTestAppModelRepo{nodes: map[int64]domain.AppModelNode{}, nextID: 1}
}

func (r *catalogHTTPTestAppModelRepo) seed(node domain.AppModelNode) domain.AppModelNode {
	if node.Source == "" {
		node.Source = "manual"
	}
	if len(node.Meta) == 0 {
		node.Meta = json.RawMessage(`{}`)
	}
	node.ID = r.nextID
	r.nextID++
	r.nodes[node.ID] = node
	r.order = append(r.order, node.ID)
	return node
}

func (r *catalogHTTPTestAppModelRepo) CreateAppModel(_ context.Context, n *domain.AppModelNode) error {
	r.createCalls++
	if n.Source == "" {
		n.Source = "manual"
	}
	if len(n.Meta) == 0 {
		n.Meta = json.RawMessage(`{}`)
	}
	n.ID = r.nextID
	r.nextID++
	r.nodes[n.ID] = *n
	r.order = append(r.order, n.ID)
	return nil
}

func (r *catalogHTTPTestAppModelRepo) GetAppModel(_ context.Context, id int64) (*domain.AppModelNode, error) {
	n, ok := r.nodes[id]
	if !ok {
		return nil, domain.NewNotFound("app model", id)
	}
	return &n, nil
}

func (r *catalogHTTPTestAppModelRepo) ListAppModels(_ context.Context, f domain.AppModelFilter) ([]domain.AppModelNode, bool, error) {
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
		limit = platform.DefaultLimit
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *catalogHTTPTestAppModelRepo) UpdateAppModel(_ context.Context, n *domain.AppModelNode) error {
	if _, ok := r.nodes[n.ID]; !ok {
		return domain.NewNotFound("app model", n.ID)
	}
	r.nodes[n.ID] = *n
	return nil
}

func (r *catalogHTTPTestAppModelRepo) DeleteAppModel(_ context.Context, id int64) error {
	if _, ok := r.nodes[id]; !ok {
		return domain.NewNotFound("app model", id)
	}
	delete(r.nodes, id)
	return nil
}

func (r *catalogHTTPTestAppModelRepo) ListAppModelsByKeys(_ context.Context, systemID int64, keys []string) (map[string]domain.AppModelNode, error) {
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

// catalogHTTPTestRepos bundles the fake repositories.
type catalogHTTPTestRepos struct {
	companies catalogHTTPTestCompanyRepo
	systems   *catalogHTTPTestSystemRepo
	envs      *catalogHTTPTestEnvironmentRepo
	appModels *catalogHTTPTestAppModelRepo
}

// newCatalogHandlerTestServer mounts the catalog routes behind a guard made of
// the real auth middleware and RequireAdmin.
func newCatalogHandlerTestServer(t *testing.T) (*http.ServeMux, *catalogHTTPTestRepos) {
	t.Helper()
	repos := &catalogHTTPTestRepos{
		systems:   newCatalogHTTPTestSystemRepo(),
		envs:      newCatalogHTTPTestEnvironmentRepo(),
		appModels: newCatalogHTTPTestAppModelRepo(),
	}
	service := application.NewCatalogService(repos.companies, repos.systems, repos.envs, repos.appModels,
		catalogHTTPTestClock{now: time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)})
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewCatalogHandler(service).RegisterRoutes(mux, guard)
	return mux, repos
}

// catalogRequest performs one request against the catalog test mux.
func catalogRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// decodeCatalogData decodes the {"data": ...} success envelope.
func decodeCatalogData[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return envelope.Data
}

// decodeCatalogPage decodes the cursor page envelope.
func decodeCatalogPage(t *testing.T, rr *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page %q: %v", rr.Body.String(), err)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// decodeCatalogError decodes the shared error envelope.
func decodeCatalogError(t *testing.T, rr *httptest.ResponseRecorder) platform.APIError {
	t.Helper()
	var envelope struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return envelope.Error
}

// assertCatalogErrorEnvelope asserts the status, code and details.reason.
func assertCatalogErrorEnvelope(t *testing.T, rr *httptest.ResponseRecorder, status int, code, reason string) platform.APIError {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status=%d want %d (body=%s)", rr.Code, status, rr.Body.String())
	}
	apiErr := decodeCatalogError(t, rr)
	if apiErr.Code != code {
		t.Fatalf("code=%q want %q", apiErr.Code, code)
	}
	if reason != "" {
		if got := apiErr.Details["reason"]; got != reason {
			t.Fatalf("details.reason=%v want %q", got, reason)
		}
	}
	return apiErr
}

func TestCatalogHandlerCreateSystem(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)

	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems", catalogAdminToken,
		`{"key":"shop","name":"Shop","kind":"web","baseUrl":"http://localhost:13335","defaultSessionTtlSeconds":7200,"notes":"primary"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeCatalogData[map[string]any](t, rr)
	if data["key"] != "shop" || data["name"] != "Shop" || data["kind"] != "web" ||
		data["baseUrl"] != "http://localhost:13335" || data["notes"] != "primary" {
		t.Fatalf("data=%v", data)
	}
	if data["defaultSessionTtlSeconds"] != float64(7200) {
		t.Fatalf("defaultSessionTtlSeconds=%v want 7200", data["defaultSessionTtlSeconds"])
	}
	if data["status"] != domain.StatusActive {
		t.Fatalf("status=%v want active", data["status"])
	}
	if _, ok := data["id"].(float64); !ok {
		t.Fatalf("id=%v want a number", data["id"])
	}
	if _, ok := data["createdAt"].(string); !ok {
		t.Fatalf("createdAt=%v want an RFC3339 string", data["createdAt"])
	}
	if _, ok := data["updatedAt"].(string); !ok {
		t.Fatalf("updatedAt=%v want an RFC3339 string", data["updatedAt"])
	}
	if _, ok := data["companyId"]; ok {
		t.Fatal("DTO leaks companyId")
	}
	if len(repos.systems.systems) != 1 {
		t.Fatalf("stored %d systems", len(repos.systems.systems))
	}
	for _, system := range repos.systems.systems {
		if system.CompanyID != 1 {
			t.Fatalf("company id=%d want the default company", system.CompanyID)
		}
	}
}

func TestCatalogHandlerCreateSystemRejectsInvalidBody(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		reason string
	}{
		{"malformed", `{"key":`, "invalid_json"},
		{"unknown field", `{"key":"shop","name":"Shop","kind":"web","baseUrl":"http://localhost:13335","bogus":1}`, "invalid_json"},
		{"not an object", `["shop"]`, "invalid_json"},
		{"invalid key", `{"key":"BAD","name":"Shop","kind":"web","baseUrl":"http://localhost:13335"}`, "invalid_key"},
		{"invalid base url", `{"key":"shop","name":"Shop","kind":"web","baseUrl":"localhost"}`, "invalid_base_url"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repos := newCatalogHandlerTestServer(t)
			rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems", catalogAdminToken, tc.body)
			assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, tc.reason)
			if repos.systems.createCalls != 0 {
				t.Fatal("invalid request reached the service")
			}
		})
	}
}

func TestCatalogHandlerCreateSystemRejectsOversizedBody(t *testing.T) {
	mux, _ := newCatalogHandlerTestServer(t)
	body := `{"key":"shop","name":"Shop","kind":"web","baseUrl":"http://localhost:13335","notes":"` +
		strings.Repeat("a", int(64<<10)) + `"}`
	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems", catalogAdminToken, body)
	assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "body_too_large")
}

func TestCatalogHandlerListSystemsPagination(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("one")
	second := repos.systems.seed("two")
	repos.systems.seed("three")

	rr := catalogRequest(mux, http.MethodGet, "/api/resources/systems?limit=2", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, next, hasMore := decodeCatalogPage(t, rr)
	if len(items) != 2 || !hasMore {
		t.Fatalf("items=%d hasMore=%v", len(items), hasMore)
	}
	if next != platform.EncodeCursor(second.ID) {
		t.Fatalf("next_cursor=%q want %q", next, platform.EncodeCursor(second.ID))
	}

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems?limit=2&cursor="+next, catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore = decodeCatalogPage(t, rr)
	if len(items) != 1 || hasMore {
		t.Fatalf("page 2 items=%d hasMore=%v", len(items), hasMore)
	}

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems?limit=101", catalogUserToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_limit")

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems?cursor=not-a-cursor!", catalogUserToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_cursor")
}

func TestCatalogHandlerSystemCRUD(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	seeded := repos.systems.seed("shop")

	rr := catalogRequest(mux, http.MethodGet, "/api/resources/systems/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeCatalogData[map[string]any](t, rr)
	if data["id"] != float64(seeded.ID) || data["key"] != "shop" || data["baseUrl"] != seeded.BaseURL {
		t.Fatalf("data=%v", data)
	}

	rr = catalogRequest(mux, http.MethodPatch, "/api/resources/systems/1", catalogAdminToken, `{"name":"Shop v2"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data = decodeCatalogData[map[string]any](t, rr)
	if data["name"] != "Shop v2" || data["key"] != "shop" || data["kind"] != domain.SystemKindWeb {
		t.Fatalf("data=%v", data)
	}

	rr = catalogRequest(mux, http.MethodDelete, "/api/resources/systems/1", catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems/1", catalogUserToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestCatalogHandlerWriteRoutesRequireAdmin(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("shop")

	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/resources/systems", `{"key":"new","name":"New","kind":"web","baseUrl":"http://localhost:13335"}`},
		{http.MethodPatch, "/api/resources/systems/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/resources/systems/1", ""},
		{http.MethodPost, "/api/resources/systems/1/environments", `{"key":"dev","name":"Dev","kind":"dev","baseUrl":"http://localhost:13335"}`},
		{http.MethodPatch, "/api/resources/environments/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/resources/environments/1", ""},
		{http.MethodPost, "/api/resources/systems/1/app-models", `{"key":"page:/x","kind":"page"}`},
		{http.MethodPost, "/api/resources/systems/1/app-models/import", `{"nodes":[]}`},
		{http.MethodPatch, "/api/resources/app-models/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/resources/app-models/1", ""},
	}
	for _, write := range writes {
		t.Run(write.method+" "+write.path, func(t *testing.T) {
			rr := catalogRequest(mux, write.method, write.path, catalogUserToken, write.body)
			assertCatalogErrorEnvelope(t, rr, http.StatusForbidden, platform.CodeForbidden, "")
		})
		rr := catalogRequest(mux, write.method, write.path, "", write.body)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: status=%d want 401", write.method, write.path, rr.Code)
		}
	}
	if repos.systems.createCalls != 0 {
		t.Fatalf("a forbidden write reached the service: create calls=%d", repos.systems.createCalls)
	}

	// Reads stay available to regular users (in production the global auth
	// middleware still requires a login).
	rr := catalogRequest(mux, http.MethodGet, "/api/resources/systems", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestCatalogHandlerCreateEnvironment(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	system := repos.systems.seed("shop")

	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/environments", catalogAdminToken,
		`{"key":"dev","name":"Development","baseUrl":"http://localhost:13335","kind":"dev","allowedHosts":[],"loginCheck":{"url":"/account","expectStatus":[200],"bodyContains":"Sign out"},"allowAutoLogin":true}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeCatalogData[map[string]any](t, rr)
	if data["systemId"] != float64(system.ID) || data["key"] != "dev" || data["kind"] != "dev" {
		t.Fatalf("data=%v", data)
	}
	if data["isProd"] != false || data["allowAutoLogin"] != true || data["requireApproval"] != false {
		t.Fatalf("data=%v", data)
	}
	hosts, ok := data["allowedHosts"].([]any)
	if !ok || len(hosts) != 0 {
		t.Fatalf("allowedHosts=%v want []", data["allowedHosts"])
	}
	loginCheck, ok := data["loginCheck"].(map[string]any)
	if !ok || loginCheck["url"] != "/account" || loginCheck["bodyContains"] != "Sign out" {
		t.Fatalf("loginCheck=%v", data["loginCheck"])
	}
	statuses, ok := loginCheck["expectStatus"].([]any)
	if !ok || len(statuses) != 1 || statuses[0] != float64(200) {
		t.Fatalf("expectStatus=%v", loginCheck["expectStatus"])
	}

	rr = catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/environments", catalogAdminToken,
		`{"key":"dev","name":"Development","baseUrl":"http://localhost:13335","kind":"dev","loginCheck":{"url":"/account","expectStatus":[99]}}`)
	assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_login_check")

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems/1/environments", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	envs := decodeCatalogData[[]map[string]any](t, rr)
	if len(envs) != 1 || envs[0]["key"] != "dev" {
		t.Fatalf("environments=%v", envs)
	}

	rr = catalogRequest(mux, http.MethodPatch, "/api/resources/environments/"+catalogPathID(envs[0]["id"]), catalogAdminToken,
		`{"loginCheck":null,"allowAutoLogin":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeCatalogData[map[string]any](t, rr)
	if patched["loginCheck"] != nil {
		t.Fatalf("loginCheck=%v want nil after an explicit null", patched["loginCheck"])
	}
	if patched["allowAutoLogin"] != false || patched["key"] != "dev" {
		t.Fatalf("patched=%v", patched)
	}
}

func TestCatalogHandlerDeleteSystemRejectsDependencies(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("shop")
	repos.systems.countEnvs = 1

	rr := catalogRequest(mux, http.MethodDelete, "/api/resources/systems/1", catalogAdminToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusConflict, platform.CodeConflict, "has_dependencies")
	if repos.systems.deleteCalls != 0 {
		t.Fatal("a protected system was deleted")
	}
}

func TestCatalogHandlerAppModels(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("shop")

	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/app-models", catalogAdminToken,
		`{"key":"page:/orders","kind":"page","name":"Orders","path":"/orders","meta":{"scope":"read"}}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	page := decodeCatalogData[map[string]any](t, rr)
	if page["systemId"] != float64(1) || page["kind"] != "page" || page["source"] != "manual" ||
		page["parentId"] != nil {
		t.Fatalf("node=%v", page)
	}
	if meta, ok := page["meta"].(map[string]any); !ok || meta["scope"] != "read" {
		t.Fatalf("meta=%v", page["meta"])
	}

	rr = catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/app-models", catalogAdminToken,
		`{"key":"route:/orders","kind":"route","name":"Route","path":"/orders","parentKey":"page:/orders"}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	route := decodeCatalogData[map[string]any](t, rr)
	if route["parentId"] != page["id"] {
		t.Fatalf("parentId=%v want %v", route["parentId"], page["id"])
	}

	rr = catalogRequest(mux, http.MethodGet, "/api/resources/systems/1/app-models?kind=route", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore := decodeCatalogPage(t, rr)
	if len(items) != 1 || hasMore || items[0]["key"] != "route:/orders" {
		t.Fatalf("items=%v hasMore=%v", items, hasMore)
	}

	rr = catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/app-models/import", catalogAdminToken,
		`{"nodes":[{"key":"api:GET /api/orders","kind":"api","name":"List","path":"/api/orders","method":"GET","parentKey":"route:/orders"}]}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	counts := decodeCatalogData[map[string]any](t, rr)
	if counts["created"] != float64(1) || counts["updated"] != float64(0) {
		t.Fatalf("counts=%v", counts)
	}

	rr = catalogRequest(mux, http.MethodPatch, "/api/resources/app-models/"+catalogPathID(route["id"]), catalogAdminToken,
		`{"name":"Orders route v2"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeCatalogData[map[string]any](t, rr)
	if patched["name"] != "Orders route v2" || patched["path"] != "/orders" ||
		patched["parentId"] != page["id"] {
		t.Fatalf("patched=%v want the omitted fields preserved", patched)
	}

	rr = catalogRequest(mux, http.MethodPatch, "/api/resources/app-models/"+catalogPathID(route["id"]), catalogAdminToken,
		`{"parentKey":""}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	reparented := decodeCatalogData[map[string]any](t, rr)
	if reparented["parentId"] != nil {
		t.Fatalf("parentId=%v want null after an explicit empty parentKey", reparented["parentId"])
	}

	rr = catalogRequest(mux, http.MethodDelete, "/api/resources/app-models/"+catalogPathID(page["id"]), catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
	rr = catalogRequest(mux, http.MethodGet, "/api/resources/app-models/"+catalogPathID(page["id"]), catalogUserToken, "")
	assertCatalogErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

// catalogImportBody builds a valid import body of count page nodes. padding
// inflates every node so a test can cross the handler's body-size thresholds.
func catalogImportBody(count, padding int) string {
	var b strings.Builder
	b.WriteString(`{"nodes":[`)
	for i := 0; i < count; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"key":"page:/imported-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`","kind":"page","name":"`)
		b.WriteString(strings.Repeat("x", padding))
		b.WriteString(`","path":"/imported-`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`"}`)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestCatalogHandlerImportAppModelsAcceptsBodyOver64KiB(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("shop")

	const nodeCount = 200
	body := catalogImportBody(nodeCount, 600)
	if len(body) <= 64<<10 {
		t.Fatalf("test body is %d bytes; it must exceed the 64 KiB ordinary cap", len(body))
	}

	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/app-models/import", catalogAdminToken, body)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	counts := decodeCatalogData[map[string]any](t, rr)
	if counts["created"] != float64(nodeCount) || counts["updated"] != float64(0) {
		t.Fatalf("counts=%v want %d created", counts, nodeCount)
	}
	if len(repos.appModels.nodes) != nodeCount {
		t.Fatalf("service stored %d nodes want %d", len(repos.appModels.nodes), nodeCount)
	}
}

func TestCatalogHandlerImportAppModelsRejectsBodyOver5MiB(t *testing.T) {
	mux, repos := newCatalogHandlerTestServer(t)
	repos.systems.seed("shop")

	const importCap = 5 << 20
	body := catalogImportBody(1, importCap)
	if len(body) <= importCap {
		t.Fatalf("test body is %d bytes; it must exceed the 5 MiB import cap", len(body))
	}

	rr := catalogRequest(mux, http.MethodPost, "/api/resources/systems/1/app-models/import", catalogAdminToken, body)
	assertCatalogErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "body_too_large")
	if len(repos.appModels.nodes) != 0 {
		t.Fatalf("oversized import reached the service and stored %d nodes", len(repos.appModels.nodes))
	}
}

// catalogPathID renders a decoded JSON number as a path id without a decimal
// part.
func catalogPathID(value any) string {
	number, ok := value.(float64)
	if !ok {
		return "0"
	}
	return strconv.FormatInt(int64(number), 10)
}
