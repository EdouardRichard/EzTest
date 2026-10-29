package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
)

// gcRouteService is a stub GC use case that records the request and returns a
// fixed report.
type gcRouteService struct {
	called  bool
	request executionapp.GCRequest
	report  executionapp.GCReport
	err     error
}

func (s *gcRouteService) Run(_ context.Context, req executionapp.GCRequest) (executionapp.GCReport, error) {
	s.called = true
	s.request = req
	return s.report, s.err
}

// gcRoutesHandler assembles the server with the admin auth stub and gc service.
func gcRoutesHandler(gcEnabled bool, service GCService) http.Handler {
	return New(config.Config{Env: "test", GCEnabled: gcEnabled}, testLogger(), Dependencies{
		Auth: acceptingAuth{token: "ezt_admin", user: identity.User{ID: 1, Role: identity.RoleAdmin}},
		GC:   service,
	})
}

// gcPost issues one POST /api/admin/gc request with the admin token.
func gcPost(h http.Handler, body, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/admin/gc", strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// TestAdminGCDefaultsToDryRun pins the safe default: an empty body (or an
// omitted dryRun field) previews, never purges.
func TestAdminGCDefaultsToDryRun(t *testing.T) {
	service := &gcRouteService{report: executionapp.GCReport{
		DryRun:         true,
		Candidates:     2,
		CandidateLimit: executionapp.GCCandidateLimit,
	}}
	h := gcRoutesHandler(false, service)

	rec := gcPost(h, `{}`, "ezt_admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s want 200", rec.Code, rec.Body)
	}
	if !service.called || !service.request.DryRun {
		t.Fatalf("request=%+v want dry-run", service.request)
	}
	if service.request.ActorID != 1 {
		t.Fatalf("actor=%d want 1", service.request.ActorID)
	}
	var body struct {
		Data executionapp.GCReport `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Data.Candidates != 2 || !body.Data.DryRun {
		t.Fatalf("data=%+v", body.Data)
	}
	if body.Data.CandidateLimit != executionapp.GCCandidateLimit {
		t.Fatalf("candidateLimit=%d want %d", body.Data.CandidateLimit, executionapp.GCCandidateLimit)
	}
}

// TestAdminGCDisabledReturns409 pins the production opt-in: a non-dry-run
// request is rejected before the service is called when EZTEST_GC_ENABLED is
// false.
func TestAdminGCDisabledReturns409(t *testing.T) {
	service := &gcRouteService{report: executionapp.GCReport{DryRun: false}}
	h := gcRoutesHandler(false, service)

	rec := gcPost(h, `{"dryRun":false}`, "ezt_admin")
	if rec.Code != http.StatusConflict {
		t.Fatalf("code=%d body=%s want 409", rec.Code, rec.Body)
	}
	if service.called {
		t.Fatal("disabled GC must not reach the service")
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Error.Code != "conflict" {
		t.Fatalf("error code=%q want conflict", body.Error.Code)
	}
}

// TestAdminGCExecutesWhenEnabled pins the explicit non-dry-run path: the flag is
// forwarded verbatim and the service result is returned.
func TestAdminGCExecutesWhenEnabled(t *testing.T) {
	service := &gcRouteService{report: executionapp.GCReport{DryRun: false, Deleted: 3}}
	h := gcRoutesHandler(true, service)

	rec := gcPost(h, `{"dryRun":false,"reason":"manual test"}`, "ezt_admin")
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s want 200", rec.Code, rec.Body)
	}
	if service.request.DryRun || service.request.Reason != "manual test" {
		t.Fatalf("request=%+v", service.request)
	}
	var body struct {
		Data executionapp.GCReport `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body=%q: %v", rec.Body.String(), err)
	}
	if body.Data.Deleted != 3 {
		t.Fatalf("data=%+v", body.Data)
	}
}

// TestAdminGCRequiresAdmin pins the admin guard and the invalid-body rejection.
func TestAdminGCRequiresAdmin(t *testing.T) {
	service := &gcRouteService{report: executionapp.GCReport{DryRun: true}}
	h := New(config.Config{Env: "test", GCEnabled: true}, testLogger(), Dependencies{
		Auth: acceptingAuth{token: "ezt_user", user: identity.User{ID: 2, Role: identity.RoleUser}},
		GC:   service,
	})
	if rec := gcPost(h, `{"dryRun":true}`, "ezt_user"); rec.Code != http.StatusForbidden {
		t.Fatalf("ordinary user code=%d body=%s want 403", rec.Code, rec.Body)
	}
	if rec := gcPost(h, `{"dryRun":true}`, ""); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous code=%d body=%s want 401", rec.Code, rec.Body)
	}
	admin := gcRoutesHandler(true, service)
	if rec := gcPost(admin, `{"dryRun":"yes"}`, "ezt_admin"); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid body code=%d body=%s want 400", rec.Code, rec.Body)
	}
}
