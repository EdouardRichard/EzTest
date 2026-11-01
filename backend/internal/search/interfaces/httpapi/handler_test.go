package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// fakeSearcher records the normalized request the handler forwards.
type fakeSearcher struct {
	snapshot searchapp.Snapshot
	err      error

	calls int
	q     string
	types []string
	limit int
}

func (f *fakeSearcher) Search(_ context.Context, q string, types []string, limit int) (searchapp.Snapshot, error) {
	f.calls++
	f.q = q
	f.types = types
	f.limit = limit
	if f.err != nil {
		return searchapp.Snapshot{}, f.err
	}
	return f.snapshot, nil
}

func passThrough(next http.Handler) http.Handler { return next }

func newSearchMux(t *testing.T, svc Searcher, authenticated func(http.Handler) http.Handler) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	if authenticated == nil {
		authenticated = passThrough
	}
	RegisterRoutes(mux, authenticated, New(svc))
	return mux
}

func searchRequest(t *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
	return recorder
}

func decodeSearchData(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response %s: %v", recorder.Body.String(), err)
	}
	if envelope.Data == nil {
		t.Fatalf("response has no data object: %s", recorder.Body.String())
	}
	return envelope.Data
}

func sampleSnapshot() searchapp.Snapshot {
	groups := make([]searchapp.Group, 0, len(searchapp.AllTypes))
	for index, typeName := range searchapp.AllTypes {
		groups = append(groups, searchapp.Group{
			Type: typeName,
			Items: []searchapp.Item{{
				ID:           string(rune('1' + index)),
				Title:        "登录 " + typeName,
				Subtitle:     "版本甲",
				VersionID:    "11",
				VersionTitle: "版本甲",
				Status:       "running",
				URL:          "/versions/11?tab=overview",
			}},
		})
	}
	return searchapp.Snapshot{Groups: groups, Total: len(groups), Truncated: true, Degraded: true}
}

func TestSearchHandlerMapsFiveGroupsToTheContractShape(t *testing.T) {
	svc := &fakeSearcher{snapshot: sampleSnapshot()}
	mux := newSearchMux(t, svc, nil)

	recorder := searchRequest(t, mux, "/api/search?q=login")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if contentType := recorder.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content-type = %q", contentType)
	}
	if cacheControl := recorder.Header().Get("Cache-Control"); cacheControl != "no-store" {
		t.Fatalf("cache-control = %q, want no-store", cacheControl)
	}
	if svc.calls != 1 || svc.q != "login" || svc.types != nil || svc.limit != searchapp.DefaultLimit {
		t.Fatalf("forwarded query = q:%q types:%v limit:%d calls:%d",
			svc.q, svc.types, svc.limit, svc.calls)
	}

	data := decodeSearchData(t, recorder)
	if data["total"] != float64(5) {
		t.Fatalf("total = %v, want 5", data["total"])
	}
	if data["truncated"] != true || data["degraded"] != true {
		t.Fatalf("flags = truncated:%v degraded:%v", data["truncated"], data["degraded"])
	}

	groups, ok := data["groups"].([]any)
	if !ok || len(groups) != 5 {
		t.Fatalf("groups = %v, want five groups", data["groups"])
	}
	wantTypes := []string{"version", "requirement", "case", "issue", "report"}
	for index, wantType := range wantTypes {
		group, ok := groups[index].(map[string]any)
		if !ok {
			t.Fatalf("group %d = %v", index, groups[index])
		}
		if group["type"] != wantType {
			t.Fatalf("group %d type = %v, want %q", index, group["type"], wantType)
		}
		items, ok := group["items"].([]any)
		if !ok || len(items) != 1 {
			t.Fatalf("group %s items = %v", wantType, group["items"])
		}
		item, ok := items[0].(map[string]any)
		if !ok {
			t.Fatalf("group %s item = %v", wantType, items[0])
		}
		wantKeys := []string{
			"id", "title", "subtitle", "version_id", "version_title", "status", "url",
		}
		if len(item) != len(wantKeys) {
			t.Fatalf("item keys = %v, want %v", item, wantKeys)
		}
		for _, key := range wantKeys {
			if _, present := item[key]; !present {
				t.Fatalf("item %v is missing the contract key %q", item, key)
			}
		}
		if item["id"] != string(rune('1'+index)) {
			t.Fatalf("item id = %v, want the decimal string", item["id"])
		}
		if item["version_id"] != "11" || item["version_title"] != "版本甲" {
			t.Fatalf("version context = %v/%v", item["version_id"], item["version_title"])
		}
		if item["url"] != "/versions/11?tab=overview" {
			t.Fatalf("url = %v, want the server deep link", item["url"])
		}
	}
}

func TestSearchHandlerTrimsAndNormalizesQueryParameters(t *testing.T) {
	svc := &fakeSearcher{snapshot: searchapp.Snapshot{}}
	mux := newSearchMux(t, svc, nil)

	recorder := searchRequest(t, mux,
		"/api/search?q="+url.QueryEscape("  login  ")+"&types="+url.QueryEscape("Report, CASE")+"&limit=12")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.q != "login" {
		t.Fatalf("forwarded q = %q, want trimmed %q", svc.q, "login")
	}
	if len(svc.types) != 2 || svc.types[0] != "case" || svc.types[1] != "report" {
		t.Fatalf("forwarded types = %v, want canonical [case report]", svc.types)
	}
	if svc.limit != 12 {
		t.Fatalf("forwarded limit = %d, want 12", svc.limit)
	}
}

func TestSearchHandlerBadRequests(t *testing.T) {
	cases := []struct {
		name   string
		target string
		field  string
	}{
		{name: "missing q", target: "/api/search", field: "q"},
		{name: "blank q", target: "/api/search?q=%20%20", field: "q"},
		{name: "overlong q", target: "/api/search?q=" + url.QueryEscape(strings.Repeat("中", 101)), field: "q"},
		{name: "unknown type", target: "/api/search?q=x&types=version,bogus", field: "types"},
		{name: "empty types", target: "/api/search?q=x&types=,,", field: "types"},
		{name: "limit below range", target: "/api/search?q=x&limit=0", field: "limit"},
		{name: "limit above range", target: "/api/search?q=x&limit=21", field: "limit"},
		{name: "non-numeric limit", target: "/api/search?q=x&limit=abc", field: "limit"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			svc := &fakeSearcher{snapshot: searchapp.Snapshot{}}
			mux := newSearchMux(t, svc, nil)

			recorder := searchRequest(t, mux, testCase.target)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode error envelope: %v", err)
			}
			if envelope.Error.Code != "invalid_argument" {
				t.Fatalf("error code = %q, want invalid_argument", envelope.Error.Code)
			}
			if got := envelope.Error.Details["field"]; got != testCase.field {
				t.Fatalf("details.field = %v, want %q", got, testCase.field)
			}
			if svc.calls != 0 {
				t.Fatalf("service called %d times for a rejected request", svc.calls)
			}
		})
	}
}

func TestSearchHandlerMapsServiceErrors(t *testing.T) {
	t.Run("platform invalid argument", func(t *testing.T) {
		svc := &fakeSearcher{err: platform.Invalid("q", "rejected")}
		mux := newSearchMux(t, svc, nil)

		recorder := searchRequest(t, mux, "/api/search?q=login")
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400", recorder.Code)
		}
	})

	t.Run("unknown error becomes a safe 500", func(t *testing.T) {
		svc := &fakeSearcher{err: errors.New("database password leaked")}
		mux := newSearchMux(t, svc, nil)

		recorder := searchRequest(t, mux, "/api/search?q=login")
		if recorder.Code != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", recorder.Code)
		}
		if strings.Contains(recorder.Body.String(), "password") {
			t.Fatalf("500 leaked the provider error: %s", recorder.Body.String())
		}
	})
}

func TestSearchHandlerReturnsDegradedSnapshotAs200(t *testing.T) {
	svc := &fakeSearcher{snapshot: searchapp.Snapshot{
		Groups:   []searchapp.Group{{Type: "version", Items: []searchapp.Item{}}},
		Degraded: true,
	}}
	mux := newSearchMux(t, svc, nil)

	recorder := searchRequest(t, mux, "/api/search?q=login")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", recorder.Code)
	}
	data := decodeSearchData(t, recorder)
	if data["degraded"] != true {
		t.Fatalf("degraded = %v, want true", data["degraded"])
	}
	groups := data["groups"].([]any)
	group := groups[0].(map[string]any)
	items, ok := group["items"].([]any)
	if !ok {
		t.Fatalf("empty group items = %v, want []", group["items"])
	}
	if len(items) != 0 {
		t.Fatalf("empty group items = %v, want []", items)
	}
}

func TestSearchHandlerIsMountedBehindAuthentication(t *testing.T) {
	var calls int
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if r.Header.Get("Authorization") != "Bearer token" {
				httpx.Error(w, r, platform.Unauthenticated())
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	svc := &fakeSearcher{snapshot: searchapp.Snapshot{}}
	mux := newSearchMux(t, svc, authenticated)

	unauthenticated := searchRequest(t, mux, "/api/search?q=login")
	if unauthenticated.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", unauthenticated.Code)
	}
	if svc.calls != 0 {
		t.Fatalf("service called %d times for an unauthenticated request", svc.calls)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/search?q=login", nil)
	request.Header.Set("Authorization", "Bearer token")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if calls != 2 {
		t.Fatalf("auth middleware calls = %d, want 2", calls)
	}
	if svc.calls != 1 {
		t.Fatalf("service calls = %d, want 1", svc.calls)
	}
}
