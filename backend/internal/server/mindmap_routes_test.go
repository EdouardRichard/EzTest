package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codeintelhttp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// mindmapExportStub is the minimal mindmaphttp.ExportService used to prove the P5
// route group is mounted behind the global bearer auth.
type mindmapExportStub struct{}

func (mindmapExportStub) Create(_ context.Context, versionID, actor int64, format string) (domain.CaseExport, error) {
	return domain.CaseExport{
		ID: 980, VersionID: versionID, Format: format, NodeCount: 1, SizeBytes: 10,
		FileSHA256: strings.Repeat("ab", 32), CreatedAt: time.Unix(0, 0).UTC(),
	}, nil
}

func (mindmapExportStub) List(context.Context, int64, string, int, string) ([]domain.CaseExport, string, bool, error) {
	return nil, "", false, nil
}

func (mindmapExportStub) Download(context.Context, int64, int64) (io.ReadCloser, domain.CaseExport, error) {
	return io.NopCloser(strings.NewReader("export")), domain.CaseExport{}, nil
}

// mindmapRoutesDeps wires the P5 handler group and the two bearer tokens.
func mindmapRoutesDeps() Dependencies {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth:    tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Mindmap: mindmaphttp.NewHandler(mindmaphttp.Deps{Exports: mindmapExportStub{}}),
	}
}

// TestMindmapRoutesMounted proves Dependencies.Mindmap mounts the P5 routes: they
// sit behind the global bearer auth (anonymous is 401) and require no admin role
// (a regular user reaches the handlers), matching the P5 contract.
func TestMindmapRoutesMounted(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), mindmapRoutesDeps())

	for _, route := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/versions/101/mindmap/export", `{"format":"markdown"}`},
		{http.MethodGet, "/api/versions/101/mindmap/exports", ""},
		{http.MethodGet, "/api/versions/101/mindmap/exports/980/download", ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(route.method, route.path, "", route.body))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous=%d body=%s want 401", route.method, route.path, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodPost, "/api/versions/101/mindmap/export", "ezt_user", `{"format":"markdown"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("user POST export=%d body=%s want 201 (no admin guard on P5)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"980"`) {
		t.Fatalf("user POST export body=%s want the string export id", rec.Body)
	}
}

// TestMindmapRoutesNilSafe proves a process without the P5 wiring leaves the
// route surface unmounted instead of panicking.
func TestMindmapRoutesNilSafe(t *testing.T) {
	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	for name, deps := range map[string]Dependencies{
		"nil dependency":      {Auth: tokenMapAuth{"ezt_user": user}},
		"empty handler group": {Auth: tokenMapAuth{"ezt_user": user}, Mindmap: mindmaphttp.NewHandler(mindmaphttp.Deps{})},
		"nil handler pointer": {Auth: tokenMapAuth{"ezt_user": user}, Mindmap: nil},
	} {
		h := New(config.Config{Env: "test"}, testLogger(), deps)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions/101/mindmap", "ezt_user", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: GET /mindmap=%d body=%s want 404", name, rec.Code, rec.Body)
		}
	}
}

// TestMindmapRoutesCoexistWithP3AndP4 proves the P5 patterns do not conflict with
// the P3 /api/versions/{id} surface or the P4 /api/versions/{id}/code one when the
// real composition root mounts every group: a pattern clash would panic inside
// http.ServeMux.Handle at startup.
func TestMindmapRoutesCoexistWithP3AndP4(t *testing.T) {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	deps := Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		// P3 and P4 groups with no services mounted still register their
		// version-level patterns.
		Version:   versionhttp.NewHandlers(versionhttp.HandlersDeps{Versions: stubP3Versions{}}),
		CodeIntel: codeintelhttp.NewHandler(codeintelhttp.HandlerDeps{}),
		Mindmap:   mindmaphttp.NewHandler(mindmaphttp.Deps{Exports: mindmapExportStub{}}),
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions/101", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/versions/101=%d body=%s want the P3 handler", rec.Code, rec.Body)
	}
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodPost, "/api/versions/101/mindmap/export", "ezt_user", `{"format":"opml"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST mindmap export=%d body=%s want the P5 handler", rec.Code, rec.Body)
	}
}
