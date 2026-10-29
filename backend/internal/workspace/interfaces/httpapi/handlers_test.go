package httpapi_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	workspace "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
	workspacehttp "github.com/EdouardRichard/EzTest/backend/internal/workspace/interfaces/httpapi"
)

// fakeLister records every forwarded query and returns a canned snapshot.
type fakeLister struct {
	snapshot workspace.Snapshot
	err      error
	queries  []workspace.Query
}

func (f *fakeLister) List(_ context.Context, query workspace.Query) (workspace.Snapshot, error) {
	f.queries = append(f.queries, query)
	return f.snapshot, f.err
}

// tokenAuth is the minimal identity Authenticator used by the auth middleware.
type tokenAuth map[string]identity.User

func (a tokenAuth) Authenticate(_ context.Context, token string) (identity.User, error) {
	user, ok := a[token]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

// Login and Logout complete the identity AuthService surface needed only by the
// server-wiring test; the todo handler never exercises them.
func (tokenAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, errors.New("login is not used by the todo tests")
}

func (tokenAuth) Logout(context.Context, string) error { return nil }

// newServer mounts the todo handler behind the real bearer middleware, exactly
// like server.New does for /api routes.
func newServer(t *testing.T, lister *fakeLister) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	handlers := workspacehttp.NewHandlers(lister, func(ctx context.Context) bool {
		user, ok := identityhttp.UserFromContext(ctx)
		return ok && user.Role == identity.RoleAdmin
	})
	if handlers == nil {
		t.Fatal("NewHandlers returned nil for a wired lister")
	}
	handlers.RegisterRoutes(mux)
	return identityhttp.AuthMiddleware(tokenAuth{
		"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
		"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	})(mux)
}

// newHandlersOnly mounts the handler without auth middleware for the pure query
// parameter tests.
func newHandlersOnly(t *testing.T, lister *fakeLister, admin bool) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	workspacehttp.NewHandlers(lister, func(context.Context) bool { return admin }).RegisterRoutes(mux)
	return mux
}

func doGet(t *testing.T, handler http.Handler, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, target, http.NoBody)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

type todosEnvelope struct {
	Data struct {
		Items []struct {
			Kind        string `json:"kind"`
			Title       string `json:"title"`
			VersionID   string `json:"versionId"`
			VersionName string `json:"versionName"`
			Severity    string `json:"severity"`
			CreatedAt   string `json:"createdAt"`
			URL         string `json:"url"`
		} `json:"items"`
		Counts struct {
			Review   int `json:"review"`
			Takeover int `json:"takeover"`
			Gate     int `json:"gate"`
		} `json:"counts"`
	} `json:"data"`
}

type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func sampleSnapshot(t *testing.T) workspace.Snapshot {
	t.Helper()
	createdAt := time.Date(2026, 10, 29, 10, 0, 0, 0, time.UTC)
	return workspace.Snapshot{
		Items: []workspace.Item{
			{Kind: workspace.KindReview, Title: "版本甲", VersionID: "11", VersionName: "版本甲",
				Severity: workspace.SeverityInfo, CreatedAt: createdAt, URL: "/versions/11?tab=mindmap"},
			{Kind: workspace.KindTakeover, Title: "登录流程", VersionID: "12", VersionName: "版本乙",
				Severity: workspace.SeverityWarning, CreatedAt: createdAt, URL: "/versions/12?tab=sessions"},
		},
		Counts: workspace.Counts{Review: 1, Takeover: 1, Gate: 0},
	}
}

// TestTodosSuccessEnvelope proves the handler forwards limit and admin state and
// renders the §6.3 `{data:{items,counts}}` envelope with no-store caching.
func TestTodosSuccessEnvelope(t *testing.T) {
	lister := &fakeLister{snapshot: sampleSnapshot(t)}
	recorder := doGet(t, newServer(t, lister), "/api/todos?limit=7", "ezt_admin")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if len(lister.queries) != 1 {
		t.Fatalf("lister calls = %d, want 1", len(lister.queries))
	}
	if lister.queries[0].Limit != 7 || !lister.queries[0].Admin {
		t.Fatalf("forwarded query = %+v, want limit=7 admin=true", lister.queries[0])
	}

	var envelope todosEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body)
	}
	if len(envelope.Data.Items) != 2 {
		t.Fatalf("items = %d, want 2: %s", len(envelope.Data.Items), recorder.Body)
	}
	first := envelope.Data.Items[0]
	if first.Kind != "review" || first.VersionID != "11" || first.VersionName != "版本甲" ||
		first.CreatedAt == "" || first.URL != "/versions/11?tab=mindmap" {
		t.Fatalf("first item = %+v", first)
	}
	if envelope.Data.Counts.Review != 1 || envelope.Data.Counts.Takeover != 1 || envelope.Data.Counts.Gate != 0 {
		t.Fatalf("counts = %+v", envelope.Data.Counts)
	}
}

// TestTodosDefaultLimit proves an absent `limit` selects the documented 50.
func TestTodosDefaultLimit(t *testing.T) {
	lister := &fakeLister{snapshot: workspace.Snapshot{Items: []workspace.Item{}}}
	recorder := doGet(t, newServer(t, lister), "/api/todos", "ezt_admin")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(lister.queries) != 1 || lister.queries[0].Limit != 50 {
		t.Fatalf("forwarded query = %+v, want limit=50", lister.queries)
	}
}

// TestTodosAnonymousIsUnauthorized proves the route is protected by the global
// bearer middleware (spec §11.1: all logged-in users).
func TestTodosAnonymousIsUnauthorized(t *testing.T) {
	recorder := doGet(t, newServer(t, &fakeLister{}), "/api/todos", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body)
	}
}

// TestTodosRegularUserHasNoTakeover proves the handler passes admin=false to the
// service for a regular user; the service then reports no takeover/gate groups.
func TestTodosRegularUserHasNoTakeover(t *testing.T) {
	lister := &fakeLister{snapshot: workspace.Snapshot{
		Items:  []workspace.Item{{Kind: workspace.KindReview, Title: "版本甲", VersionID: "11"}},
		Counts: workspace.Counts{Review: 1},
	}}
	recorder := doGet(t, newServer(t, lister), "/api/todos", "ezt_user")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(lister.queries) != 1 || lister.queries[0].Admin {
		t.Fatalf("forwarded query = %+v, want admin=false", lister.queries)
	}
	var envelope todosEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Data.Counts.Takeover != 0 || envelope.Data.Counts.Gate != 0 {
		t.Fatalf("counts = %+v, want no takeover/gate", envelope.Data.Counts)
	}
	for _, item := range envelope.Data.Items {
		if item.Kind != "review" {
			t.Fatalf("regular-user item = %+v, want review only", item)
		}
	}
}

// TestTodosNilAdminChecker proves a handler built without the identity adapter
// treats every caller as a regular user instead of panicking.
func TestTodosNilAdminChecker(t *testing.T) {
	lister := &fakeLister{snapshot: workspace.Snapshot{Items: []workspace.Item{}}}
	mux := http.NewServeMux()
	workspacehttp.NewHandlers(lister, nil).RegisterRoutes(mux)
	recorder := doGet(t, mux, "/api/todos", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(lister.queries) != 1 || lister.queries[0].Admin {
		t.Fatalf("forwarded query = %+v, want admin=false", lister.queries)
	}
}

// TestTodosRejectsInvalidLimit pins the public 1–200 window of the endpoint.
func TestTodosRejectsInvalidLimit(t *testing.T) {
	for _, raw := range []string{"0", "-1", "201", "abc"} {
		lister := &fakeLister{}
		recorder := doGet(t, newHandlersOnly(t, lister, true), "/api/todos?limit="+raw, "")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s status = %d body=%s, want 400", raw, recorder.Code, recorder.Body)
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("limit=%s decode: %v body=%s", raw, err, recorder.Body)
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Fatalf("limit=%s code = %q, want invalid_argument", raw, envelope.Error.Code)
		}
		if envelope.Error.Details["field"] != "limit" {
			t.Fatalf("limit=%s details = %+v, want field=limit", raw, envelope.Error.Details)
		}
		if envelope.Error.Details["min"] != float64(1) || envelope.Error.Details["max"] != float64(200) {
			t.Fatalf("limit=%s details = %+v, want min=1 max=200", raw, envelope.Error.Details)
		}
		if len(lister.queries) != 0 {
			t.Fatalf("limit=%s reached the service %d times, want 0", raw, len(lister.queries))
		}
	}
}

// TestTodosServiceErrorIsInternal proves a provider failure renders the shared
// 500 envelope without leaking the underlying database message.
func TestTodosServiceErrorIsInternal(t *testing.T) {
	lister := &fakeLister{err: errors.New("secret database outage detail")}
	recorder := doGet(t, newHandlersOnly(t, lister, true), "/api/todos", "")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "secret database outage detail") {
		t.Fatalf("body leaked the provider error: %s", recorder.Body)
	}
}

// TestServerMountsTodosRoute proves the composition root's Dependencies.Workspace
// wiring mounts GET /api/todos behind the global bearer auth.
func TestServerMountsTodosRoute(t *testing.T) {
	lister := &fakeLister{snapshot: workspace.Snapshot{Items: []workspace.Item{}}}
	handler := server.New(
		config.Config{Env: "test"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		server.Dependencies{
			Auth: tokenAuth{
				"ezt_admin": {ID: 1, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive},
				"ezt_user":  {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
			},
			Workspace: workspacehttp.NewHandlers(lister, func(ctx context.Context) bool {
				user, ok := identityhttp.UserFromContext(ctx)
				return ok && user.Role == identity.RoleAdmin
			}),
		},
	)

	anonymous := doGet(t, handler, "/api/todos", "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d body=%s, want 401", anonymous.Code, anonymous.Body)
	}

	authorized := doGet(t, handler, "/api/todos", "ezt_admin")
	if authorized.Code != http.StatusOK {
		t.Fatalf("admin status = %d body=%s, want 200", authorized.Code, authorized.Body)
	}
	if len(lister.queries) != 1 || !lister.queries[0].Admin {
		t.Fatalf("forwarded queries = %+v, want one admin query", lister.queries)
	}
}

// TestNewHandlersNil proves an unwired process mounts no todo route at all.
func TestNewHandlersNil(t *testing.T) {
	if workspacehttp.NewHandlers(nil, nil) != nil {
		t.Fatal("NewHandlers(nil) must return nil")
	}
}
