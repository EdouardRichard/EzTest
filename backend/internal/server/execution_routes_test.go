package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// stubExecutionBatches is the minimal executionhttp.BatchService used to prove
// the P6 route group is mounted; the remaining methods return their defaults.
// Create takes the P7 composite command (T7) so the test adapter satisfies the
// widened consumer port.
type stubExecutionBatches struct{}

func (stubExecutionBatches) Create(context.Context, int64, int64, executionapp.CreateBatchCommand) (*domain.RunBatch, error) {
	return &domain.RunBatch{ID: 1842, VersionID: 101, BatchNo: 1, Status: domain.BatchStatusRunning}, nil
}

func (stubExecutionBatches) Get(_ context.Context, versionID, batchID int64) (*domain.RunBatch, error) {
	if versionID != 101 || batchID != 1842 {
		return nil, executionapp.ErrBatchNotFound
	}
	return &domain.RunBatch{ID: batchID, VersionID: versionID, BatchNo: 1, Status: domain.BatchStatusRunning}, nil
}

func (stubExecutionBatches) List(_ context.Context, versionID int64, _ int64, _ int) ([]domain.RunBatch, bool, error) {
	return []domain.RunBatch{{ID: 1842, VersionID: versionID, BatchNo: 1, Status: domain.BatchStatusRunning}}, false, nil
}

func (stubExecutionBatches) Pause(_ context.Context, versionID, batchID int64, mode domain.PauseMode, _ int64) (*domain.RunBatch, error) {
	return &domain.RunBatch{ID: batchID, VersionID: versionID, BatchNo: 1, Status: domain.BatchStatusRunning, Paused: true, PauseMode: mode}, nil
}

func (stubExecutionBatches) Resume(_ context.Context, versionID, batchID, _ int64) (*domain.RunBatch, error) {
	return &domain.RunBatch{ID: batchID, VersionID: versionID, BatchNo: 1, Status: domain.BatchStatusRunning}, nil
}

func (stubExecutionBatches) Cancel(_ context.Context, versionID, batchID, _ int64) (*domain.RunBatch, error) {
	return &domain.RunBatch{ID: batchID, VersionID: versionID, BatchNo: 1, Status: domain.BatchStatusCancelled}, nil
}

func (stubExecutionBatches) Rerun(_ context.Context, versionID, batchID, caseRunID, _ int64) (*domain.CaseRun, error) {
	return &domain.CaseRun{ID: caseRunID + 1, BatchID: batchID, VersionID: versionID, Attempt: 2, Status: domain.CaseRunStatusPending}, nil
}

// executionHealthStub reports a healthy executor so batch creation reaches the
// batch service in the route-mounting test.
type executionHealthStub struct{}

func (executionHealthStub) Health(context.Context) error { return nil }

// executionRoutesDeps wires the P6 handler group and the two bearer tokens.
func executionRoutesDeps() Dependencies {
	admin := identity.User{ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}
	regular := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	return Dependencies{
		Auth: tokenMapAuth{"ezt_admin": admin, "ezt_user": regular},
		Execution: executionhttp.NewHandlers(executionhttp.HandlerDeps{
			Batches:  stubExecutionBatches{},
			Executor: executionHealthStub{},
		}),
	}
}

// TestExecutionRoutesMounted proves Dependencies.Execution mounts the P6 routes
// behind the global bearer auth; a regular logged-in user reaches them because
// execution has no admin-only endpoint (spec §5.11).
func TestExecutionRoutesMounted(t *testing.T) {
	h := New(config.Config{Env: "test"}, testLogger(), executionRoutesDeps())

	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/batches", ""},
		{http.MethodPost, "/api/versions/101/batches", `{}`},
		{http.MethodGet, "/api/versions/101/batches/1842", ""},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(route.method, route.path, "", route.body))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s anonymous=%d body=%s want 401", route.method, route.path, rec.Code, rec.Body)
		}
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions/101/batches", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("user GET batches=%d body=%s want 200 (no admin guard on P6)", rec.Code, rec.Body)
	}
	if !strings.Contains(rec.Body.String(), `"id":"1842"`) {
		t.Fatalf("user GET batches body=%s want the string batch id", rec.Body)
	}

	// A regular user may also create: P6 has no 403 path for execution routes.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodPost, "/api/versions/101/batches", "ezt_user", `{}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("user POST batches=%d body=%s want 201 (no admin guard on P6)", rec.Code, rec.Body)
	}
}

// TestExecutionRoutesNilSafe proves a process without the P6 wiring leaves the
// route surface unmounted instead of panicking.
func TestExecutionRoutesNilSafe(t *testing.T) {
	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	for name, deps := range map[string]Dependencies{
		"nil dependency": {Auth: tokenMapAuth{"ezt_user": user}},
		"empty handlers": {Auth: tokenMapAuth{"ezt_user": user}, Execution: executionhttp.NewHandlers(executionhttp.HandlerDeps{})},
		"empty-nil":      {Auth: tokenMapAuth{"ezt_user": user}, Execution: &executionhttp.Handlers{}},
	} {
		h := New(config.Config{Env: "test"}, testLogger(), deps)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions/101/batches", "ezt_user", ""))
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: GET batches=%d body=%s want 404", name, rec.Code, rec.Body)
		}
	}
}

// executionVersionStub reuses the P3 route stub but returns a version carrying
// the P6 execution-state additions.
type executionVersionStub struct{ stubP3Versions }

func (executionVersionStub) Get(_ context.Context, id int64) (*versionapp.Version, error) {
	return &versionapp.Version{
		ID: id, Name: "execution version", Status: "executing", Revision: 2,
		Paused: true, PauseMode: "immediate", BatchNo: 3,
	}, nil
}

// TestVersionDTOIncludesExecutionState proves GET /api/versions/{id} adds
// paused/pause_mode/batch_no and never an active_batch field (spec §6.3).
func TestVersionDTOIncludesExecutionState(t *testing.T) {
	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	deps := Dependencies{
		Auth: tokenMapAuth{"ezt_user": user},
		Version: versionhttp.NewHandlers(versionhttp.HandlersDeps{
			Versions: executionVersionStub{},
		}),
	}
	h := New(config.Config{Env: "test"}, testLogger(), deps)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, versionRequest(http.MethodGet, "/api/versions/101", "ezt_user", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET version=%d body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	for _, want := range []string{`"paused":true`, `"pause_mode":"immediate"`, `"batch_no":3`} {
		if !strings.Contains(body, want) {
			t.Fatalf("body=%s misses %s", body, want)
		}
	}
	if strings.Contains(body, "active_batch") {
		t.Fatalf("body=%s must not add active_batch", body)
	}
}

// TestCORSExposesExecutionDownloadHeaders proves the browser can read the
// evidence/report headers added by P6 (T11 finding): X-Content-SHA256 and
// Content-Disposition.
func TestCORSExposesExecutionDownloadHeaders(t *testing.T) {
	h := CORS("http://localhost:6868")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/api/versions/101/batches/1842/report/export", nil)
	req.Header.Set("Origin", "http://localhost:6868")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	exposed := rec.Header().Get("Access-Control-Expose-Headers")
	for _, header := range []string{"Content-Disposition", "X-Content-SHA256", "X-Request-ID"} {
		if !strings.Contains(exposed, header) {
			t.Fatalf("Access-Control-Expose-Headers=%q misses %s", exposed, header)
		}
	}
}
