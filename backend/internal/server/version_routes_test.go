package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// stubP3Versions is a minimal versionhttp.VersionService. Only Create/List/Get
// are exercised by the route-mounting test; the remaining methods satisfy the
// interface and are never called.
type stubP3Versions struct{}

func (stubP3Versions) Create(_ context.Context, cmd versionapp.CreateVersionCommand, actor int64) (*versionapp.Version, error) {
	return &versionapp.Version{ID: 101, Name: cmd.Name, Status: "draft", Revision: 1, CreatedBy: actor, UpdatedBy: actor}, nil
}

func (stubP3Versions) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	return &versionapp.Version{ID: id, Name: "wiring version", Status: "draft", Revision: 1}, nil
}

func (stubP3Versions) List(context.Context, versionapp.VersionFilter) ([]versionapp.Version, bool, error) {
	return []versionapp.Version{{ID: 101, Name: "wiring version", Status: "draft", Revision: 1}}, false, nil
}

func (stubP3Versions) UpdateMetadata(_ context.Context, id int64, _ int, name, label, description string, _ int64) (*versionapp.Version, error) {
	return &versionapp.Version{ID: id, Name: name, Label: label, Description: description, Status: "draft", Revision: 2}, nil
}

func (stubP3Versions) UpdateBindings(_ context.Context, id int64, _ int, _ versionapp.VersionBindings, _ int64) (*versionapp.Version, error) {
	return &versionapp.Version{ID: id, Status: "draft", Revision: 2}, nil
}

func (stubP3Versions) StartClarification(_ context.Context, id int64, _ int64) (*versionapp.Version, error) {
	return &versionapp.Version{ID: id, Status: "clarifying", Revision: 2}, nil
}

func (stubP3Versions) EnterDesign(_ context.Context, id int64, _ bool, _ int64) (*versionapp.Version, versionapp.Warnings, error) {
	return &versionapp.Version{ID: id, Status: "designing", Revision: 2}, versionapp.Warnings{}, nil
}

// versionRoutesDeps wires the P3 handler group and the two bearer tokens.
func versionRoutesDeps() Dependencies {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Version: versionhttp.NewHandlers(versionhttp.HandlersDeps{
			Versions: stubP3Versions{},
		}),
	}
}

// versionRequest builds a request with an optional bearer token.
func versionRequest(method, path, token, body string) *http.Request {
	var reader *strings.Reader
	if body != "" {
		reader = strings.NewReader(body)
	} else {
		reader = strings.NewReader("")
	}
	req := httptest.NewRequest(method, path, reader)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

// TestVersionRoutesMounted proves the P3 routes are mounted when the
// composition root provides Dependencies.Version: they sit behind the global
// bearer auth (anonymous is 401) and require no admin role (a regular user
// reaches the handlers), matching the P3 contract.
func TestVersionRoutesMounted(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), versionRoutesDeps())

	// Anonymous callers are rejected by the global middleware on every route.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/versions"},
		{http.MethodPost, "/api/versions"},
		{http.MethodGet, "/api/versions/101"},
		{http.MethodPost, "/api/versions/101/start-clarification"},
		{http.MethodGet, "/api/versions/101/stream"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(route.method, route.path, "", ""))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous=%d body=%s want 401", route.method, route.path, rec.Code, rec.Body)
		}
	}

	// A regular user (not an admin) may read and create: P3 has no admin guard.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("user GET versions=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"101"`) {
		t.Fatalf("user GET versions body=%s want the string version id", rec.Body)
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodPost, "/api/versions", "ezt_user", `{"name":"wiring version"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("user POST versions=%d body=%s want 201 (no admin guard on P3)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"101"`) {
		t.Fatalf("user POST versions body=%s want the created version", rec.Body)
	}
}

// TestVersionRoutesNilSafe proves a process without the P3 wiring leaves the
// route surface unmounted instead of panicking: a nil dependency and an empty
// handler group both yield the mux 404.
func TestVersionRoutesNilSafe(t *testing.T) {
	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	for name, deps := range map[string]Dependencies{
		"nil dependency":   {Auth: tokenMapAuth{"ezt_user": user}},
		"empty handlers":   {Auth: tokenMapAuth{"ezt_user": user}, Version: versionhttp.NewHandlers(versionhttp.HandlersDeps{})},
		"empty-nil-safety": {Auth: tokenMapAuth{"ezt_user": user}, Version: &versionhttp.Handlers{}},
	} {
		h := New(config.Config{Env: "test"}, testLogger(), deps)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions", "ezt_user", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: GET /api/versions=%d body=%s want 404", name, rec.Code, rec.Body)
		}
	}
}
