package httpapi

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// authFlowHTTPTestRepo is an in-memory domain.AuthFlowRepository that mints ids
// on create like the postgres implementation.
type authFlowHTTPTestRepo struct {
	flows       map[int64]domain.AuthFlow
	nextID      int64
	createCalls int
	updateCalls int
	deleteCalls int
}

func newAuthFlowHTTPTestRepo() *authFlowHTTPTestRepo {
	return &authFlowHTTPTestRepo{flows: map[int64]domain.AuthFlow{}, nextID: 1}
}

func (r *authFlowHTTPTestRepo) seed(flow domain.AuthFlow) domain.AuthFlow {
	if flow.Kind == "" {
		flow.Kind = domain.AuthFlowKindForm
	}
	if flow.ID == 0 {
		flow.ID = r.nextID
	}
	if flow.ID >= r.nextID {
		r.nextID = flow.ID + 1
	}
	r.flows[flow.ID] = flow
	return flow
}

func (r *authFlowHTTPTestRepo) CreateAuthFlow(_ context.Context, f *domain.AuthFlow) error {
	f.ID = r.nextID
	r.nextID++
	r.createCalls++
	r.flows[f.ID] = *f
	return nil
}

func (r *authFlowHTTPTestRepo) GetAuthFlow(_ context.Context, id int64) (*domain.AuthFlow, error) {
	flow, ok := r.flows[id]
	if !ok {
		return nil, domain.NewNotFound("auth flow", id)
	}
	return &flow, nil
}

func (r *authFlowHTTPTestRepo) ListAuthFlows(_ context.Context, f domain.AuthFlowFilter) ([]domain.AuthFlow, bool, error) {
	ids := make([]int64, 0, len(r.flows))
	for id := range r.flows {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.AuthFlow, 0, len(ids))
	for _, id := range ids {
		flow := r.flows[id]
		if flow.ID <= f.Page.AfterID {
			continue
		}
		if f.SystemID != 0 && flow.SystemID != f.SystemID {
			continue
		}
		matches = append(matches, flow)
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

func (r *authFlowHTTPTestRepo) UpdateAuthFlow(_ context.Context, f *domain.AuthFlow) error {
	if _, ok := r.flows[f.ID]; !ok {
		return domain.NewNotFound("auth flow", f.ID)
	}
	r.updateCalls++
	r.flows[f.ID] = *f
	return nil
}

func (r *authFlowHTTPTestRepo) DeleteAuthFlow(_ context.Context, id int64) error {
	if _, ok := r.flows[id]; !ok {
		return domain.NewNotFound("auth flow", id)
	}
	r.deleteCalls++
	delete(r.flows, id)
	return nil
}

// authFlowHTTPTestRepos bundles the fakes behind one AuthFlowHandler.
type authFlowHTTPTestRepos struct {
	flows   *authFlowHTTPTestRepo
	systems *catalogHTTPTestSystemRepo
}

// newAuthFlowHandlerTestServer mounts the auth-flow routes behind the real auth
// middleware and RequireAdmin.
func newAuthFlowHandlerTestServer(t *testing.T) (*http.ServeMux, *authFlowHTTPTestRepos) {
	t.Helper()
	repos := &authFlowHTTPTestRepos{
		flows:   newAuthFlowHTTPTestRepo(),
		systems: newCatalogHTTPTestSystemRepo(),
	}
	service := application.NewAuthFlowService(repos.flows, repos.systems)
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewAuthFlowHandler(service).RegisterRoutes(mux, guard)
	return mux, repos
}

// authFlowRequest performs one request against the auth-flow test mux.
func authFlowRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
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

// authFlowCreateBody is a valid snake_case POST body matching spec §3.4.
const authFlowCreateBody = `{"system_id":"1","name":"fixture login","kind":"form","definition":{` +
	`"login_url":"https://app.test/login","username_selector":"#username",` +
	`"password_selector":"#password","submit_selector":"button[type=submit]",` +
	`"success":{"url_contains":"/home"},"failure":{"mfa_selector":"#mfa"},` +
	`"timeout_ms":20000},"enabled":true}`

func TestAuthFlowHandlerCRUD(t *testing.T) {
	mux, repos := newAuthFlowHandlerTestServer(t)
	system := repos.systems.seed("fixture-web")

	rr := authFlowRequest(mux, http.MethodPost, "/api/resources/auth-flows", catalogAdminToken, authFlowCreateBody)
	if rr.Code != http.StatusCreated {
		t.Fatalf("create status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeAccessData[map[string]any](t, rr)
	id, ok := data["id"].(string)
	if !ok || id == "" {
		t.Fatalf("id=%v want a decimal string", data["id"])
	}
	if got := data["system_id"]; got != "1" {
		t.Fatalf("system_id=%v want the decimal string \"1\"", got)
	}
	if data["name"] != "fixture login" || data["kind"] != domain.AuthFlowKindForm || data["enabled"] != true {
		t.Fatalf("data=%v", data)
	}
	if _, ok := data["systemId"]; ok {
		t.Fatal("DTO leaks a camelCase systemId key")
	}
	definition, ok := data["definition"].(map[string]any)
	if !ok {
		t.Fatalf("definition=%v", data["definition"])
	}
	if definition["login_url"] != "https://app.test/login" || definition["timeout_ms"] != float64(20000) {
		t.Fatalf("definition=%v", definition)
	}
	success, ok := definition["success"].(map[string]any)
	if !ok || success["url_contains"] != "/home" {
		t.Fatalf("success=%v", definition["success"])
	}
	if len(repos.flows.flows) != 1 {
		t.Fatalf("stored %d auth flows", len(repos.flows.flows))
	}
	if storedSystem := repos.flows.flows[repos.flows.nextID-1].SystemID; storedSystem != system.ID {
		t.Fatalf("stored system_id=%d want %d", storedSystem, system.ID)
	}

	// The list is admin-only and cursor-paged.
	rr = authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows?system_id=1", catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, _, hasMore := decodeAccessPage(t, rr)
	if len(items) != 1 || hasMore || items[0]["id"] != id {
		t.Fatalf("list items=%v hasMore=%v", items, hasMore)
	}

	rr = authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows/"+id, catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	if got := decodeAccessData[map[string]any](t, rr); got["name"] != "fixture login" {
		t.Fatalf("data=%v", got)
	}

	rr = authFlowRequest(mux, http.MethodPatch, "/api/resources/auth-flows/"+id, catalogAdminToken, `{"enabled":false}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	patched := decodeAccessData[map[string]any](t, rr)
	if patched["enabled"] != false || patched["name"] != "fixture login" {
		t.Fatalf("patched=%v want the toggle only", patched)
	}

	rr = authFlowRequest(mux, http.MethodDelete, "/api/resources/auth-flows/"+id, catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
	rr = authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows/"+id, catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestAuthFlowHandlerListPagination(t *testing.T) {
	mux, repos := newAuthFlowHandlerTestServer(t)
	repos.systems.seed("fixture-web")
	definition := domain.FormDefinition{
		LoginURL:         "https://app.test/login",
		UsernameSelector: "#username",
		PasswordSelector: "#password",
		SubmitSelector:   "button",
		Success:          domain.SuccessRule{URLContains: "/home"},
		TimeoutMS:        20000,
	}
	for _, name := range []string{"one", "two", "three"} {
		repos.flows.seed(domain.AuthFlow{SystemID: 1, Name: name, Definition: definition, Enabled: true})
	}

	rr := authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows?limit=2", catalogAdminToken, "")
	items, next, hasMore := decodeAccessPage(t, rr)
	if len(items) != 2 || !hasMore || next == "" {
		t.Fatalf("first page items=%d next=%q hasMore=%v", len(items), next, hasMore)
	}
	rr = authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows?limit=2&cursor="+next, catalogAdminToken, "")
	items, _, hasMore = decodeAccessPage(t, rr)
	if len(items) != 1 || hasMore {
		t.Fatalf("second page items=%d hasMore=%v", len(items), hasMore)
	}
}

func TestAuthFlowHandlerValidatesDefinition(t *testing.T) {
	mux, repos := newAuthFlowHandlerTestServer(t)
	repos.systems.seed("fixture-web")

	body := strings.Replace(authFlowCreateBody, "https://app.test/login", "ftp://app.test/login", 1)
	rr := authFlowRequest(mux, http.MethodPost, "/api/resources/auth-flows", catalogAdminToken, body)
	apiErr := assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "auth_flow_invalid")
	if apiErr.Details["field"] != "definition.login_url" {
		t.Fatalf("details.field=%v", apiErr.Details["field"])
	}
	if repos.flows.createCalls != 0 {
		t.Fatal("an invalid recipe reached the repository")
	}

	rr = authFlowRequest(mux, http.MethodGet, "/api/resources/auth-flows?system_id=abc", catalogAdminToken, "")
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "auth_flow_invalid")

	rr = authFlowRequest(mux, http.MethodPost, "/api/resources/auth-flows", catalogAdminToken,
		`{"systemId":"1","name":"x"}`)
	assertAccessErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")
}

func TestAuthFlowHandlerRequiresAdmin(t *testing.T) {
	mux, repos := newAuthFlowHandlerTestServer(t)
	repos.systems.seed("fixture-web")
	repos.flows.seed(domain.AuthFlow{
		SystemID: 1, Name: "fixture login", Enabled: true,
		Definition: domain.FormDefinition{
			LoginURL:         "https://app.test/login",
			UsernameSelector: "#username",
			PasswordSelector: "#password",
			SubmitSelector:   "button",
			Success:          domain.SuccessRule{URLContains: "/home"},
			TimeoutMS:        20000,
		},
	})

	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/api/resources/auth-flows", ""},
		{http.MethodPost, "/api/resources/auth-flows", authFlowCreateBody},
		{http.MethodGet, "/api/resources/auth-flows/1", ""},
		{http.MethodPatch, "/api/resources/auth-flows/1", `{"enabled":false}`},
		{http.MethodDelete, "/api/resources/auth-flows/1", ""},
	}
	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			rr := authFlowRequest(mux, route.method, route.path, catalogUserToken, route.body)
			assertAccessErrorEnvelope(t, rr, http.StatusForbidden, platform.CodeForbidden, "")

			rr = authFlowRequest(mux, route.method, route.path, "", route.body)
			if rr.Code != http.StatusUnauthorized {
				t.Fatalf("anonymous status=%d want 401 (body=%s)", rr.Code, rr.Body.String())
			}
		})
	}
	if repos.flows.createCalls != 0 || repos.flows.updateCalls != 0 || repos.flows.deleteCalls != 0 {
		t.Fatalf("forbidden writes reached the repository: create=%d update=%d delete=%d",
			repos.flows.createCalls, repos.flows.updateCalls, repos.flows.deleteCalls)
	}
}
