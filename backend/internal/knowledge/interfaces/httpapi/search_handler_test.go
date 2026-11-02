package httpapi_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	knowledgeapp "github.com/EdouardRichard/EzTest/backend/internal/knowledge/application"
	knowledge "github.com/EdouardRichard/EzTest/backend/internal/knowledge/domain"
	knowledgeapi "github.com/EdouardRichard/EzTest/backend/internal/knowledge/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

/* -------------------------------- fixtures -------------------------------- */

// fixtureBoundItem is a fully bound 0.2.1 item: every structured binding is set
// and the success pipeline has stamped last_verified_at.
func fixtureBoundItem() knowledge.Item {
	item := fixtureItem()
	item.Bindings = knowledge.Bindings{
		SystemID:      ptr[int64](101),
		EnvironmentID: ptr[int64](202),
		PageKey:       ptr("/login"),
		APIKey:        ptr("POST /auth"),
		AccountRole:   ptr("admin"),
	}
	item.LastVerifiedAt = ptr(handlerNow)
	return item
}

/* ---------------------------------- mux ----------------------------------- */

// newSearchMux mounts the item routes (reusing the shared helper) plus the
// search route the assembly root adds explicitly.
func newSearchMux(service knowledgeapi.Service, role string) *http.ServeMux {
	mux := newKnowledgeMux(service, role)
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if role == "" {
				next.ServeHTTP(w, r)
				return
			}
			next.ServeHTTP(w, r.WithContext(
				platform.WithActor(r.Context(), platform.Actor{ID: 77, Role: role})))
		})
	}
	knowledgeapi.New(service).RegisterSearch(mux, authenticated)
	return mux
}

/* --------------------------------- search --------------------------------- */

// TestKnowledgeSearchRoute pins the 0.2.1 GET /api/knowledge/search surface:
// approved-only by default, structured binding filters, an explicit status
// filter, cursor pagination in the shared page envelope and the login gate.
func TestKnowledgeSearchRoute(t *testing.T) {
	t.Run("defaults to approved and forwards query bindings and pagination", func(t *testing.T) {
		fake := &fakeService{listPage: knowledgeapp.Page{
			Items:      []knowledge.Item{fixtureBoundItem()},
			NextCursor: 9001,
			HasMore:    true,
		}}
		mux := newSearchMux(fake, "user")
		path := "/api/knowledge/search?" + url.Values{
			"query":          {"retry"},
			"system_id":      {"101"},
			"environment_id": {"202"},
			"page_key":       {"/login"},
			"api_key":        {"POST /auth"},
			"account_role":   {"admin"},
			"cursor":         {"9500"},
			"limit":          {"2"},
		}.Encode()
		recorder, body := perform(t, mux, http.MethodGet, path, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v", recorder.Code, body)
		}

		filter := fake.listFilter
		if filter.Status != knowledge.StatusApproved {
			t.Fatalf("status filter = %q, want approved by default", filter.Status)
		}
		if filter.Query != "retry" {
			t.Fatalf("query = %q", filter.Query)
		}
		if filter.Bindings.SystemID == nil || *filter.Bindings.SystemID != 101 ||
			filter.Bindings.EnvironmentID == nil || *filter.Bindings.EnvironmentID != 202 ||
			filter.Bindings.PageKey == nil || *filter.Bindings.PageKey != "/login" ||
			filter.Bindings.APIKey == nil || *filter.Bindings.APIKey != "POST /auth" ||
			filter.Bindings.AccountRole == nil || *filter.Bindings.AccountRole != "admin" {
			t.Fatalf("bindings filter = %+v", filter.Bindings)
		}
		if filter.Cursor != 9500 || filter.Limit != 2 {
			t.Fatalf("pagination filter = cursor %d limit %d", filter.Cursor, filter.Limit)
		}

		if body["next_cursor"] != "9001" || body["has_more"] != true {
			t.Fatalf("page envelope = %v / %v", body["next_cursor"], body["has_more"])
		}
		items, ok := body["data"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("data = %#v", body["data"])
		}
		view := items[0].(map[string]any)
		bindings, ok := view["bindings"].(map[string]any)
		if !ok {
			t.Fatalf("bindings = %#v", view["bindings"])
		}
		if bindings["system_id"] != "101" || bindings["environment_id"] != "202" ||
			bindings["page_key"] != "/login" || bindings["api_key"] != "POST /auth" ||
			bindings["account_role"] != "admin" {
			t.Fatalf("binding view = %#v", bindings)
		}
		if view["last_verified_at"] != handlerNow.Format(time.RFC3339) {
			t.Fatalf("last_verified_at = %v", view["last_verified_at"])
		}
	})

	t.Run("explicit status and q alias with default limit", func(t *testing.T) {
		fake := &fakeService{listPage: knowledgeapp.Page{Items: []knowledge.Item{}}}
		mux := newSearchMux(fake, "user")
		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/search?q=retry&status=stale", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v", recorder.Code, body)
		}
		if fake.listFilter.Status != knowledge.StatusStale || fake.listFilter.Query != "retry" {
			t.Fatalf("filter = %+v", fake.listFilter)
		}
		if fake.listFilter.Limit != knowledgeapp.DefaultListLimit {
			t.Fatalf("limit = %d", fake.listFilter.Limit)
		}
		if items, ok := body["data"].([]any); !ok || len(items) != 0 {
			t.Fatalf("data = %#v", body["data"])
		}
		if body["next_cursor"] != "" || body["has_more"] != false {
			t.Fatalf("page envelope = %v / %v", body["next_cursor"], body["has_more"])
		}
	})

	t.Run("invalid filters are rejected before the service", func(t *testing.T) {
		tests := []struct {
			name  string
			query string
			field string
		}{
			{name: "invalid status", query: "status=pending", field: "status"},
			{name: "invalid system id", query: "system_id=abc", field: "system_id"},
			{name: "zero environment id", query: "environment_id=0", field: "environment_id"},
			{name: "oversized page key", query: "page_key=" + strings.Repeat("p", knowledge.MaxBindingKeyRunes+1), field: "page_key"},
			{name: "oversized account role", query: "account_role=" + strings.Repeat("r", knowledge.MaxAccountRoleRunes+1), field: "account_role"},
			{name: "oversized query", query: "query=" + strings.Repeat("x", knowledgeapp.MaxRetrievalTextRunes+1), field: "query"},
			{name: "bad cursor", query: "cursor=abc", field: "cursor"},
			{name: "large limit", query: "limit=101", field: "limit"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				fake := &fakeService{}
				mux := newSearchMux(fake, "user")
				recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/search?"+test.query, "")
				assertErrorCode(t, recorder.Code, body, http.StatusBadRequest, "invalid_argument")
				envelope := body["error"].(map[string]any)
				details := envelope["details"].(map[string]any)
				if details["field"] != test.field {
					t.Fatalf("field = %v, want %s", details["field"], test.field)
				}
				if fake.listCalls != 0 {
					t.Fatalf("service calls = %d", fake.listCalls)
				}
			})
		}
	})

	t.Run("requires authentication", func(t *testing.T) {
		fake := &fakeService{}
		mux := newSearchMux(fake, "")
		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/search", "")
		assertErrorCode(t, recorder.Code, body, http.StatusUnauthorized, "unauthenticated")
		if fake.listCalls != 0 {
			t.Fatalf("service calls = %d", fake.listCalls)
		}
	})
}

/* ------------------------------ item view DTO ----------------------------- */

// TestKnowledgeViewBindings pins the 0.2.1 KnowledgeItemView extension: the
// structured bindings render IDs as decimal strings, last_verified_at renders
// as RFC3339 (or null), and the view adds no credential-shaped field.
func TestKnowledgeViewBindings(t *testing.T) {
	t.Run("renders binding ids and last_verified_at", func(t *testing.T) {
		fake := &fakeService{getItem: fixtureBoundItem(), revisions: []knowledge.Revision{}}
		mux := newKnowledgeMux(fake, "user")
		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v", recorder.Code, body)
		}
		view := body["data"].(map[string]any)
		bindings, ok := view["bindings"].(map[string]any)
		if !ok {
			t.Fatalf("bindings = %#v", view["bindings"])
		}
		want := map[string]any{
			"system_id":      "101",
			"environment_id": "202",
			"page_key":       "/login",
			"api_key":        "POST /auth",
			"account_role":   "admin",
		}
		allowed := make(map[string]struct{}, len(want))
		for field, value := range want {
			allowed[field] = struct{}{}
			if bindings[field] != value {
				t.Fatalf("binding %s = %#v, want %#v", field, bindings[field], value)
			}
		}
		for field := range bindings {
			if _, ok := allowed[field]; !ok {
				t.Fatalf("unexpected binding field %q", field)
			}
		}
		if view["last_verified_at"] != handlerNow.Format(time.RFC3339) {
			t.Fatalf("last_verified_at = %v", view["last_verified_at"])
		}
		for _, field := range []string{
			"password", "passwd", "token", "secret", "credential", "authorization", "cookie",
		} {
			if _, exposed := view[field]; exposed {
				t.Fatalf("view exposes credential-shaped field %q", field)
			}
		}
	})

	t.Run("renders unset bindings and missing verification as null", func(t *testing.T) {
		fake := &fakeService{getItem: fixtureItem(), revisions: []knowledge.Revision{}}
		mux := newKnowledgeMux(fake, "user")
		recorder, body := perform(t, mux, http.MethodGet, "/api/knowledge/items/9001", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d body=%v", recorder.Code, body)
		}
		view := body["data"].(map[string]any)
		bindings, ok := view["bindings"].(map[string]any)
		if !ok {
			t.Fatalf("bindings = %#v", view["bindings"])
		}
		for _, field := range []string{
			"system_id", "environment_id", "page_key", "api_key", "account_role",
		} {
			if bindings[field] != nil {
				t.Fatalf("binding %s = %#v, want null", field, bindings[field])
			}
		}
		if view["last_verified_at"] != nil {
			t.Fatalf("last_verified_at = %#v, want null", view["last_verified_at"])
		}
	})
}
