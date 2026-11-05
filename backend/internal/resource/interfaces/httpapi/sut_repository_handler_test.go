package resourceapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

func sampleSUTRepository() resourceapp.SUTRepositoryView {
	repository := resourceapp.CodeRepositoryView{
		ID: "1007", Name: "orders", Path: "https://git.example.com/orders.git",
		Branch: "main", CreatedBy: 42, Status: "active", CreatedAt: testNow, UpdatedAt: testNow,
	}
	return resourceapp.SUTRepositoryView{
		ID: "1010", SUTID: "1001", Repository: repository, Role: "backend",
		Enabled: true, CreatedAt: testNow, UpdatedAt: testNow,
	}
}

func sutRepositoryRequest(method, target, body string, actor *platform.Actor) *http.Request {
	request := httptest.NewRequest(method, target, strings.NewReader(body))
	request.SetPathValue("id", "1001")
	if actor != nil {
		request = request.WithContext(platform.WithActor(request.Context(), *actor))
	}
	return request
}

// TestListSUTRepositoriesHandler pins API-17 read: GET /api/suts/{id}/repositories
// returns the SUT-level repository associations (API-17).
func TestListSUTRepositoriesHandler(t *testing.T) {
	handler, svc := newHandler()
	svc.sutRepos = []resourceapp.SUTRepositoryView{sampleSUTRepository()}

	recorder := httptest.NewRecorder()
	handler.ListSUTRepositories(recorder, sutRepositoryRequest(http.MethodGet, "/api/suts/1001/repositories", "", plainUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if svc.lastParentID != 1001 {
		t.Fatalf("sut id = %d", svc.lastParentID)
	}
	var items []sutRepositoryDTO
	decodeData(t, recorder, &items)
	if len(items) != 1 || items[0].ID != "1010" || items[0].Role != "backend" ||
		items[0].Repository.ID != "1007" || !items[0].Enabled {
		t.Fatalf("items = %#v", items)
	}

	invalid := httptest.NewRecorder()
	request := sutRepositoryRequest(http.MethodGet, "/api/suts/not-an-id/repositories", "", plainUser)
	request.SetPathValue("id", "not-an-id")
	handler.ListSUTRepositories(invalid, request)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid path status = %d", invalid.Code)
	}

	svc.err = platform.NotFound("sut")
	missing := httptest.NewRecorder()
	handler.ListSUTRepositories(missing, sutRepositoryRequest(http.MethodGet, "/api/suts/9999/repositories", "", plainUser))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing sut status = %d", missing.Code)
	}
}

// TestAddSUTRepositoryHandler pins API-17 write: POST /api/suts/{id}/repositories
// validates the repository id, requires an authenticated actor and returns the
// created association as a typed 201.
func TestAddSUTRepositoryHandler(t *testing.T) {
	handler, svc := newHandler()
	svc.sutRepository = sampleSUTRepository()

	recorder := httptest.NewRecorder()
	handler.AddSUTRepository(recorder, sutRepositoryRequest(
		http.MethodPost, "/api/suts/1001/repositories", `{"repository_id":"1007","role":"backend"}`, plainUser))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	if svc.lastParentID != 1001 || svc.lastSUTRepoInput.RepositoryID != 1007 ||
		svc.lastSUTRepoInput.Role != "backend" || svc.lastActor != 77 {
		t.Fatalf("service call = %d/%#v actor=%d", svc.lastParentID, svc.lastSUTRepoInput, svc.lastActor)
	}
	var created sutRepositoryDTO
	decodeData(t, recorder, &created)
	if created.ID != "1010" || created.SUTID != "1001" {
		t.Fatalf("created = %#v", created)
	}

	anonymous := httptest.NewRecorder()
	handler.AddSUTRepository(anonymous, sutRepositoryRequest(
		http.MethodPost, "/api/suts/1001/repositories", `{"repository_id":"1007"}`, nil))
	if anonymous.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d", anonymous.Code)
	}

	cases := []struct {
		name   string
		body   string
		status int
	}{
		{"missing repository id", `{}`, http.StatusBadRequest},
		{"non decimal repository id", `{"repository_id":"abc"}`, http.StatusBadRequest},
		{"zero repository id", `{"repository_id":"0"}`, http.StatusBadRequest},
		{"malformed body", `{"repository_id":`, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			handler.AddSUTRepository(recorder, sutRepositoryRequest(
				http.MethodPost, "/api/suts/1001/repositories", tc.body, plainUser))
			if recorder.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", recorder.Code, tc.status, recorder.Body.String())
			}
		})
	}

	svc.err = platform.NotFound("code repository")
	missing := httptest.NewRecorder()
	handler.AddSUTRepository(missing, sutRepositoryRequest(
		http.MethodPost, "/api/suts/1001/repositories", `{"repository_id":"9999"}`, plainUser))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing repository status = %d", missing.Code)
	}

	svc.err = platform.Conflict("repository is already associated with this sut")
	duplicate := httptest.NewRecorder()
	handler.AddSUTRepository(duplicate, sutRepositoryRequest(
		http.MethodPost, "/api/suts/1001/repositories", `{"repository_id":"1007"}`, plainUser))
	if duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate status = %d", duplicate.Code)
	}
}

// TestRegisterSUTRepositoryRoutes pins the route helper Task 37 mounts: GET and
// POST /api/suts/{id}/repositories are registered behind the auth middleware and
// a nil middleware fails fast instead of silently unmounting an authenticated
// route.
func TestRegisterSUTRepositoryRoutes(t *testing.T) {
	handler, svc := newHandler()
	svc.sutRepos = []resourceapp.SUTRepositoryView{sampleSUTRepository()}
	svc.sutRepository = sampleSUTRepository()
	mux := http.NewServeMux()
	auth := func(next http.Handler) http.Handler { return next }
	RegisterSUTRepositoryRoutes(mux, auth, handler)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, sutRepositoryRequest(http.MethodGet, "/api/suts/1001/repositories", "", plainUser))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET registered route = %d", recorder.Code)
	}
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, sutRepositoryRequest(
		http.MethodPost, "/api/suts/1001/repositories", `{"repository_id":"1007"}`, plainUser))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST registered route = %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestRegisterSUTRepositoryRoutesRejectsNilAuth(t *testing.T) {
	assertPanics(t, "nil auth", func() {
		RegisterSUTRepositoryRoutes(http.NewServeMux(), nil, New(nil))
	})
	assertPanics(t, "nil mux", func() {
		RegisterSUTRepositoryRoutes(nil, func(next http.Handler) http.Handler { return next }, New(nil))
	})
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Fatalf("%s did not panic", name)
		}
	}()
	fn()
}
