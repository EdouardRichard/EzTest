package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	search "github.com/EdouardRichard/EzTest/backend/internal/search/application"
	searchhttp "github.com/EdouardRichard/EzTest/backend/internal/search/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
)

// fakeProvider is the per-context provider used by the degraded-search
// integration test; it serves canned groups or a canned failure.
type fakeProvider struct {
	groups []search.Group
	err    error
}

func (f fakeProvider) Search(_ context.Context, _ string, _ int) ([]search.Group, error) {
	return f.groups, f.err
}

// fakeSearcher records every forwarded query and serves a canned snapshot.
type fakeSearcher struct {
	snapshot search.Snapshot
	err      error
	queries  []search.Query
}

func (f *fakeSearcher) Search(_ context.Context, query search.Query) (search.Snapshot, error) {
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

func (tokenAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, errors.New("login is not used by the search tests")
}

func (tokenAuth) Logout(context.Context, string) error { return nil }

// newServer mounts the search handler behind the real bearer middleware, exactly
// like server.New does for /api routes.
func newServer(t *testing.T, searcher searchhttp.Searcher) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	handlers := searchhttp.NewHandlers(searcher)
	if handlers == nil {
		t.Fatal("NewHandlers returned nil for a wired searcher")
	}
	handlers.RegisterRoutes(mux)
	return identityhttp.AuthMiddleware(tokenAuth{
		"ezt_user": {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
	})(mux)
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

type searchEnvelope struct {
	Data struct {
		Groups []struct {
			Type  string `json:"type"`
			Items []struct {
				ID          string `json:"id"`
				Title       string `json:"title"`
				Subtitle    string `json:"subtitle"`
				VersionID   string `json:"versionId"`
				VersionName string `json:"versionName"`
				Status      string `json:"status"`
				URL         string `json:"url"`
			} `json:"items"`
		} `json:"groups"`
		Total     int  `json:"total"`
		Truncated bool `json:"truncated"`
		Degraded  bool `json:"degraded"`
	} `json:"data"`
}

type errorEnvelope struct {
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func sampleSnapshot() search.Snapshot {
	return search.Snapshot{
		Groups: []search.Group{
			{Type: search.TypeCase, Items: []search.Item{{
				ID: "300", Title: "登录流程", Subtitle: "版本甲", VersionID: "11",
				VersionName: "版本甲", Status: "reviewed",
				URL: "/versions/11?tab=mindmap&case=300",
			}}},
			{Type: search.TypeIssue, Items: []search.Item{{
				ID: "400", Title: "下单失败", Subtitle: "版本甲", VersionID: "11",
				VersionName: "版本甲", Status: "open",
				URL: "/versions/11?tab=issues&issue=400",
			}}},
		},
		Total:     2,
		Truncated: true,
		Degraded:  true,
	}
}

// TestSearchSuccessEnvelope proves the handler forwards q/types/limit and renders
// the §6.2 `{data:{groups,total,truncated,degraded}}` envelope with no-store.
func TestSearchSuccessEnvelope(t *testing.T) {
	searcher := &fakeSearcher{snapshot: sampleSnapshot()}
	recorder := doGet(t, newServer(t, searcher),
		"/api/search?q=%E7%99%BB%E5%BD%95&types=case,issue&limit=7", "ezt_user")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if len(searcher.queries) != 1 {
		t.Fatalf("searcher calls = %d, want 1", len(searcher.queries))
	}
	query := searcher.queries[0]
	if query.Text != "登录" || query.Limit != 7 {
		t.Fatalf("forwarded query = %+v, want text=登录 limit=7", query)
	}
	if len(query.Types) != 2 || query.Types[0] != search.TypeCase || query.Types[1] != search.TypeIssue {
		t.Fatalf("forwarded types = %v, want [case issue]", query.Types)
	}

	var envelope searchEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v body=%s", err, recorder.Body)
	}
	if len(envelope.Data.Groups) != 2 {
		t.Fatalf("groups = %d, want 2: %s", len(envelope.Data.Groups), recorder.Body)
	}
	first := envelope.Data.Groups[0]
	if first.Type != search.TypeCase || len(first.Items) != 1 {
		t.Fatalf("first group = %+v", first)
	}
	item := first.Items[0]
	if item.ID != "300" || item.Title != "登录流程" || item.VersionID != "11" ||
		item.VersionName != "版本甲" || item.URL != "/versions/11?tab=mindmap&case=300" {
		t.Fatalf("first item = %+v", item)
	}
	if envelope.Data.Total != 2 || !envelope.Data.Truncated || !envelope.Data.Degraded {
		t.Fatalf("envelope data = %+v, want total=2 truncated=true degraded=true", envelope.Data)
	}
}

// TestSearchDefaults proves an absent q/types/limit forwards the documented
// default (all types, per-type limit 5).
func TestSearchDefaults(t *testing.T) {
	searcher := &fakeSearcher{snapshot: search.Snapshot{Groups: []search.Group{}}}
	recorder := doGet(t, newServer(t, searcher), "/api/search?q=%E7%99%BB%E5%BD%95", "ezt_user")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want 200", recorder.Code, recorder.Body)
	}
	if len(searcher.queries) != 1 {
		t.Fatalf("searcher calls = %d, want 1", len(searcher.queries))
	}
	query := searcher.queries[0]
	if query.Text != "登录" || query.Limit != search.DefaultLimit {
		t.Fatalf("forwarded query = %+v, want default limit", query)
	}
	if len(query.Types) != 0 {
		t.Fatalf("forwarded types = %v, want nil for the default all-types filter", query.Types)
	}
}

// TestSearchRejectsInvalidQuery pins the 400 invalid_argument cases of spec §6.2.
func TestSearchRejectsInvalidQuery(t *testing.T) {
	cases := []struct {
		target string
		field  string
	}{
		{"/api/search", "q"},
		{"/api/search?q=", "q"},
		{"/api/search?q=%20%20", "q"},
		{"/api/search?q=" + strings.Repeat("a", search.MaxQueryLength+1), "q"},
		{"/api/search?q=x&limit=0", "limit"},
		{"/api/search?q=x&limit=21", "limit"},
		{"/api/search?q=x&limit=abc", "limit"},
		{"/api/search?q=x&types=bogus", "types"},
		{"/api/search?q=x&types=", "types"},
		{"/api/search?q=x&types=,,", "types"},
		{"/api/search?q=x&types=%20", "types"},
	}
	for _, tc := range cases {
		searcher := &fakeSearcher{}
		recorder := doGet(t, newServer(t, searcher), tc.target, "ezt_user")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d body=%s, want 400", tc.target, recorder.Code, recorder.Body)
		}
		var envelope errorEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s: decode: %v body=%s", tc.target, err, recorder.Body)
		}
		if envelope.Error.Code != "invalid_argument" {
			t.Fatalf("%s: code = %q, want invalid_argument", tc.target, envelope.Error.Code)
		}
		if envelope.Error.Details["field"] != tc.field {
			t.Fatalf("%s: details = %+v, want field=%s", tc.target, envelope.Error.Details, tc.field)
		}
		if len(searcher.queries) != 0 {
			t.Fatalf("%s: reached the service %d times, want 0", tc.target, len(searcher.queries))
		}
	}
}

// TestSearchAnonymousIsUnauthorized proves the route sits behind the global
// bearer middleware (spec §6.2: logged-in users).
func TestSearchAnonymousIsUnauthorized(t *testing.T) {
	recorder := doGet(t, newServer(t, &fakeSearcher{}), "/api/search?q=x", "")
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body=%s, want 401", recorder.Code, recorder.Body)
	}
}

// TestSearchServiceErrorIsInternal proves a service failure renders the shared
// 500 envelope without leaking the underlying detail.
func TestSearchServiceErrorIsInternal(t *testing.T) {
	searcher := &fakeSearcher{err: errors.New("secret database outage detail")}
	recorder := doGet(t, newServer(t, searcher), "/api/search?q=x", "ezt_user")
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d body=%s, want 500", recorder.Code, recorder.Body)
	}
	if strings.Contains(recorder.Body.String(), "secret database outage detail") {
		t.Fatalf("body leaked the service error: %s", recorder.Body)
	}
}

// TestSearchProviderFailureIsLoggedButNotLeaked proves a degraded search still
// answers 200 with the healthy groups, records the provider cause server-side,
// and never exposes that cause to the client (spec §6.2).
func TestSearchProviderFailureIsLoggedButNotLeaked(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	service := search.NewService(search.Providers{
		Version: fakeProvider{groups: []search.Group{{
			Type: search.TypeVersion,
			Items: []search.Item{{
				ID: "11", Title: "登录版本", VersionID: "11", VersionName: "登录版本",
				URL: "/versions/11?tab=overview",
			}},
		}}},
		Mindmap:  fakeProvider{err: errors.New("case database connection refused")},
		Delivery: fakeProvider{groups: []search.Group{}},
	}, search.WithLogger(logger))

	recorder := doGet(t, newServer(t, service), "/api/search?q=%E7%99%BB%E5%BD%95", "ezt_user")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s, want a degraded 200", recorder.Code, recorder.Body)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "case database connection refused") {
		t.Fatalf("body leaked the provider error: %s", body)
	}
	if !strings.Contains(body, `"degraded":true`) {
		t.Fatalf("body = %s, want degraded:true", body)
	}
	if !strings.Contains(logs.String(), "case database connection refused") {
		t.Fatalf("server log %q does not record the provider error", logs.String())
	}
}

// TestServerMountsSearchRoute proves the composition root's Dependencies.Search
// wiring mounts GET /api/search behind the global bearer auth.
func TestServerMountsSearchRoute(t *testing.T) {
	searcher := &fakeSearcher{snapshot: search.Snapshot{Groups: []search.Group{}}}
	handler := server.New(
		config.Config{Env: "test"},
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		server.Dependencies{
			Auth: tokenAuth{
				"ezt_user": {ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive},
			},
			Search: searchhttp.NewHandlers(searcher),
		},
	)

	anonymous := doGet(t, handler, "/api/search?q=x", "")
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d body=%s, want 401", anonymous.Code, anonymous.Body)
	}

	authorized := doGet(t, handler, "/api/search?q=x", "ezt_user")
	if authorized.Code != http.StatusOK {
		t.Fatalf("authorized status = %d body=%s, want 200", authorized.Code, authorized.Body)
	}
	if len(searcher.queries) != 1 {
		t.Fatalf("searcher calls = %d, want 1", len(searcher.queries))
	}
}

// TestNewHandlersNil proves an unwired process mounts no search route at all.
func TestNewHandlersNil(t *testing.T) {
	if searchhttp.NewHandlers(nil) != nil {
		t.Fatal("NewHandlers(nil) must return nil")
	}
}
