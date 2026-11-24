package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// fakeFixtureVersionReader is the admin fixture listing stub (paginated).
type fakeFixtureVersionReader struct {
	fixtures  []execution.SessionFixture
	hasMore   bool
	err       error
	gotVer    int64
	gotLimit  int
	gotBefore *int64
}

func (f *fakeFixtureVersionReader) ListSessionFixturesByVersion(_ context.Context, versionID int64, limit int, beforeID *int64) ([]execution.SessionFixture, bool, error) {
	f.gotVer = versionID
	f.gotLimit = limit
	f.gotBefore = beforeID
	return f.fixtures, f.hasMore, f.err
}

// TestSessionFixturesAdminListing pins SC-F5's admin visibility: the endpoint
// lists the version's saved login-state fixtures as METADATA (ids/role/reuse
// key/content ref/created_at) — never the state bytes — with decimal-string ids.
func TestSessionFixturesAdminListing(t *testing.T) {
	created := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	reader := &fakeFixtureVersionReader{fixtures: []execution.SessionFixture{
		{
			ID: 9007199254740993, VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin",
			StorageStateRef: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			CreatedAt:       created, CreatedBy: 42,
		},
	}}
	h := New(Deps{
		Versions:        &waveHandlerVersions{ids: map[int64]bool{100: true}},
		SessionFixtures: reader,
	})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures", nil)
	req.SetPathValue("id", "100")
	h.SessionFixtures(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envelope.Data) != 1 {
		t.Fatalf("items = %d, want 1", len(envelope.Data))
	}
	item := envelope.Data[0]
	if item["id"] != "9007199254740993" || item["sut_id"] != "9001" || item["environment_id"] != "7001" {
		t.Fatalf("ids must be exact decimal strings: %+v", item)
	}
	if item["role"] != "admin" || item["storage_state_ref"] != "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" {
		t.Fatalf("item = %+v", item)
	}
	if _, leaked := item["storage_state"]; leaked {
		t.Fatal("the listing must never carry the storage state content")
	}
	if reader.gotVer != 100 {
		t.Fatalf("version scope = %d", reader.gotVer)
	}

	t.Run("pagination is honest (has_more + next_cursor, before/limit forwarded)", func(t *testing.T) {
		// Newest-first page of two facts with more remaining: the envelope must
		// report has_more=true and a next_cursor pointing at the page's last id
		// (never a silent truncation claiming completeness, constitution art.7.6).
		paged := &fakeFixtureVersionReader{
			fixtures: []execution.SessionFixture{
				{ID: 200, VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin", StorageStateRef: "b", CreatedAt: created, CreatedBy: 42},
				{ID: 100, VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin", StorageStateRef: "a", CreatedAt: created, CreatedBy: 42},
			},
			hasMore: true,
		}
		hp := New(Deps{Versions: &waveHandlerVersions{ids: map[int64]bool{100: true}}, SessionFixtures: paged})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures?limit=2&before=300", nil)
		req.SetPathValue("id", "100")
		hp.SessionFixtures(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
		}
		var env struct {
			Data       []map[string]any `json:"data"`
			NextCursor string           `json:"next_cursor"`
			HasMore    bool             `json:"has_more"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if !env.HasMore {
			t.Fatal("has_more must be true when more facts remain (no silent truncation)")
		}
		if env.NextCursor != "100" {
			t.Fatalf("next_cursor = %q, want the page's last id 100", env.NextCursor)
		}
		if paged.gotLimit != 2 || paged.gotBefore == nil || *paged.gotBefore != 300 {
			t.Fatalf("forwarded limit/before = %d/%v, want 2/300", paged.gotLimit, paged.gotBefore)
		}
	})

	t.Run("last page reports has_more=false and empty cursor", func(t *testing.T) {
		last := &fakeFixtureVersionReader{
			fixtures: []execution.SessionFixture{
				{ID: 100, VersionID: 100, SUTID: 9001, EnvironmentID: 7001, Role: "admin", StorageStateRef: "a", CreatedAt: created, CreatedBy: 42},
			},
			hasMore: false,
		}
		hl := New(Deps{Versions: &waveHandlerVersions{ids: map[int64]bool{100: true}}, SessionFixtures: last})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures", nil)
		req.SetPathValue("id", "100")
		hl.SessionFixtures(rec, req)
		var env struct {
			NextCursor string `json:"next_cursor"`
			HasMore    bool   `json:"has_more"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if env.HasMore || env.NextCursor != "" {
			t.Fatalf("last page has_more=%v next_cursor=%q, want false/empty", env.HasMore, env.NextCursor)
		}
		if last.gotBefore != nil {
			t.Fatalf("first page before = %v, want nil", last.gotBefore)
		}
	})

	t.Run("unwired reader answers 503", func(t *testing.T) {
		bare := New(Deps{Versions: &waveHandlerVersions{ids: map[int64]bool{100: true}}})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures", nil)
		req.SetPathValue("id", "100")
		bare.SessionFixtures(rec, req)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 for a degraded assembly", rec.Code)
		}
	})

	t.Run("read failure propagates", func(t *testing.T) {
		failing := New(Deps{
			Versions:        &waveHandlerVersions{ids: map[int64]bool{100: true}},
			SessionFixtures: &fakeFixtureVersionReader{err: errors.New("db down")},
		})
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures", nil)
		req.SetPathValue("id", "100")
		failing.SessionFixtures(rec, req)
		if rec.Code == http.StatusOK {
			t.Fatal("a read failure must not answer 200")
		}
	})

	t.Run("unknown version is 404", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/versions/999/session-fixtures", nil)
		req.SetPathValue("id", "999")
		h.SessionFixtures(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
	})
}

// TestSessionFixturesRouteAdminOnly pins that the listing route is mounted
// behind the ADMIN guard (fixture metadata is an admin surface, SC-F5).
func TestSessionFixturesRouteAdminOnly(t *testing.T) {
	h := New(Deps{SessionFixtures: &fakeFixtureVersionReader{}})
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Guard", "authenticated")
			next.ServeHTTP(w, r)
		})
	}
	admin := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Guard", "admin")
			next.ServeHTTP(w, r)
		})
	}
	h.Register(mux, authenticated, admin)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/versions/100/session-fixtures", nil))
	if rec.Header().Get("X-Guard") != "admin" {
		t.Fatalf("guard = %q, want admin", rec.Header().Get("X-Guard"))
	}
}
