package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
)

// routesTestAdminToken and routesTestUserToken are the bearer tokens the route
// test's authenticator resolves.
const (
	routesTestAdminToken = "routes-admin-token"
	routesTestUserToken  = "routes-user-token"
)

// routesTestAuth resolves the two route-test tokens to their seeded users.
type routesTestAuth struct{}

// Authenticate maps a raw bearer token to the seeded administrator or regular
// user; any other token is unauthenticated, matching the identity middleware.
func (routesTestAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	switch rawToken {
	case routesTestAdminToken:
		return identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}, nil
	case routesTestUserToken:
		return identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}, nil
	default:
		return identity.User{}, identity.ErrUnauthenticated
	}
}

// routesTestHandler mounts the resource routes with nil services behind the
// identity bearer middleware. The admin guard rejects a regular user before any
// handler runs, so the nil services are never touched; that is exactly what the
// guard test proves.
func routesTestHandler(t *testing.T) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRoutes(mux, Handlers{
		Catalog:  NewCatalogHandler(nil),
		Access:   NewAccessHandler(nil),
		Session:  NewSessionHandler(nil),
		CodeRepo: NewCodeRepoHandler(nil),
	}, identityhttp.RequireAdmin)
	return identityhttp.AuthMiddleware(routesTestAuth{})(mux)
}

// TestResourceWriteRoutesRequireAdmin proves every mounted write and action
// route is reachable but admin-only: a regular user gets the shared 403
// forbidden envelope before the handler runs, and an anonymous caller is stopped
// by the global auth middleware with 401. A missing or unguarded route fails the
// test (404 or a nil-service panic instead of 403).
func TestResourceWriteRoutesRequireAdmin(t *testing.T) {
	h := routesTestHandler(t)

	routes := []struct{ method, path string }{
		// Catalog: systems, environments and app models.
		{http.MethodPost, "/api/resources/systems"},
		{http.MethodPatch, "/api/resources/systems/1"},
		{http.MethodDelete, "/api/resources/systems/1"},
		{http.MethodPost, "/api/resources/systems/1/environments"},
		{http.MethodPatch, "/api/resources/environments/1"},
		{http.MethodDelete, "/api/resources/environments/1"},
		{http.MethodPost, "/api/resources/systems/1/app-models"},
		{http.MethodPost, "/api/resources/systems/1/app-models/import"},
		{http.MethodPatch, "/api/resources/app-models/1"},
		{http.MethodDelete, "/api/resources/app-models/1"},
		// Accounts and accesses.
		{http.MethodPost, "/api/resources/accounts"},
		{http.MethodPatch, "/api/resources/accounts/1"},
		{http.MethodDelete, "/api/resources/accounts/1"},
		{http.MethodPost, "/api/resources/accounts/1/accesses"},
		{http.MethodPatch, "/api/resources/accesses/1"},
		{http.MethodDelete, "/api/resources/accesses/1"},
		// Sessions, snapshots and manual takeovers.
		{http.MethodPost, "/api/resources/accesses/1/sessions"},
		{http.MethodPost, "/api/resources/sessions/1/validate"},
		{http.MethodPost, "/api/resources/sessions/1/confirm"},
		{http.MethodPost, "/api/resources/sessions/1/invalidate"},
		{http.MethodPost, "/api/resources/sessions/1/revoke"},
		{http.MethodPost, "/api/resources/sessions/1/takeovers"},
		{http.MethodPost, "/api/resources/takeovers/1/complete"},
		{http.MethodPost, "/api/resources/takeovers/1/cancel"},
		// Code repositories.
		{http.MethodPost, "/api/resources/code-repos"},
		{http.MethodPatch, "/api/resources/code-repos/1"},
		{http.MethodDelete, "/api/resources/code-repos/1"},
		{http.MethodPost, "/api/resources/code-repos/1/validate"},
	}

	for _, route := range routes {
		t.Run(route.method+" "+route.path, func(t *testing.T) {
			req := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			req.Header.Set("Authorization", "Bearer "+routesTestUserToken)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != http.StatusForbidden {
				t.Fatalf("%s %s as regular user = %d body=%s, want 403", route.method, route.path, rec.Code, rec.Body)
			}
			if !strings.Contains(rec.Body.String(), `"code":"forbidden"`) {
				t.Fatalf("%s %s body=%s, want the forbidden envelope", route.method, route.path, rec.Body)
			}

			anonymous := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			rec = httptest.NewRecorder()
			h.ServeHTTP(rec, anonymous)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("%s %s anonymous = %d body=%s, want 401", route.method, route.path, rec.Code, rec.Body)
			}
		})
	}
}

// TestRegisterRoutesSkipsNilHandlers proves a partially wired Handlers value
// mounts only the handlers it has instead of panicking at startup.
func TestRegisterRoutesSkipsNilHandlers(t *testing.T) {
	mux := http.NewServeMux()
	RegisterRoutes(mux, Handlers{}, identityhttp.RequireAdmin)

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/resources/systems", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unmounted resource route = %d, want 404", rec.Code)
	}
}
