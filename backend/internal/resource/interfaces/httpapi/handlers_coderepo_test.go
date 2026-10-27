package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"
	"time"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// codeRepoHTTPTestNow is the fixed clock instant of the handler tests.
var codeRepoHTTPTestNow = time.Date(2026, 10, 27, 9, 30, 0, 0, time.UTC)

// codeRepoHTTPTestCommit is a plausible full Git commit id.
const codeRepoHTTPTestCommit = "0123456789abcdef0123456789abcdef01234567"

// codeRepoHTTPTestRepo is an in-memory domain.CodeRepoRepository.
type codeRepoHTTPTestRepo struct {
	codeRepos   map[int64]domain.CodeRepository
	nextID      int64
	createCalls int
	updateCalls int
	deleteCalls int
}

func newCodeRepoHTTPTestRepo() *codeRepoHTTPTestRepo {
	return &codeRepoHTTPTestRepo{codeRepos: map[int64]domain.CodeRepository{}, nextID: 1}
}

// seed stores an existing row and returns the stored copy.
func (r *codeRepoHTTPTestRepo) seed(codeRepo domain.CodeRepository) domain.CodeRepository {
	if codeRepo.Status == "" {
		codeRepo.Status = domain.StatusActive
	}
	codeRepo.ID = r.nextID
	r.nextID++
	if codeRepo.CreatedAt.IsZero() {
		codeRepo.CreatedAt = codeRepoHTTPTestNow.Add(-time.Hour)
	}
	if codeRepo.UpdatedAt.IsZero() {
		codeRepo.UpdatedAt = codeRepo.CreatedAt
	}
	r.codeRepos[codeRepo.ID] = codeRepo
	return codeRepo
}

func (r *codeRepoHTTPTestRepo) CreateCodeRepo(_ context.Context, codeRepo *domain.CodeRepository) error {
	r.createCalls++
	codeRepo.ID = r.nextID
	r.nextID++
	codeRepo.CreatedAt = codeRepoHTTPTestNow
	codeRepo.UpdatedAt = codeRepoHTTPTestNow
	r.codeRepos[codeRepo.ID] = *codeRepo
	return nil
}

func (r *codeRepoHTTPTestRepo) GetCodeRepo(_ context.Context, id int64) (*domain.CodeRepository, error) {
	codeRepo, ok := r.codeRepos[id]
	if !ok {
		return nil, domain.NewNotFound("code repository", id)
	}
	return &codeRepo, nil
}

func (r *codeRepoHTTPTestRepo) ListCodeRepos(_ context.Context, f domain.CodeRepoFilter) ([]domain.CodeRepository, bool, error) {
	ids := make([]int64, 0, len(r.codeRepos))
	for id := range r.codeRepos {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	matches := make([]domain.CodeRepository, 0, len(ids))
	for _, id := range ids {
		codeRepo := r.codeRepos[id]
		if codeRepo.ID <= f.Page.AfterID {
			continue
		}
		if f.Status != "" && codeRepo.Status != f.Status {
			continue
		}
		matches = append(matches, codeRepo)
	}
	limit := f.Page.Limit
	if limit <= 0 {
		limit = platform.DefaultLimit
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

func (r *codeRepoHTTPTestRepo) UpdateCodeRepo(_ context.Context, codeRepo *domain.CodeRepository) error {
	if _, ok := r.codeRepos[codeRepo.ID]; !ok {
		return domain.NewNotFound("code repository", codeRepo.ID)
	}
	r.updateCalls++
	codeRepo.UpdatedAt = codeRepoHTTPTestNow
	r.codeRepos[codeRepo.ID] = *codeRepo
	return nil
}

func (r *codeRepoHTTPTestRepo) DeleteCodeRepo(_ context.Context, id int64) error {
	if _, ok := r.codeRepos[id]; !ok {
		return domain.NewNotFound("code repository", id)
	}
	r.deleteCalls++
	delete(r.codeRepos, id)
	return nil
}

// codeRepoHTTPTestGit is a domain.GitValidator whose verdict the test controls.
type codeRepoHTTPTestGit struct {
	commit string
	err    error
	calls  int
	paths  []string
	labels []string
}

func (g *codeRepoHTTPTestGit) Validate(_ context.Context, path, branch string) (domain.GitValidation, error) {
	g.calls++
	g.paths = append(g.paths, path)
	g.labels = append(g.labels, branch)
	if g.err != nil {
		return domain.GitValidation{}, g.err
	}
	return domain.GitValidation{Commit: g.commit}, nil
}

// codeRepoHTTPTestEnv bundles the fakes behind the handler test server.
type codeRepoHTTPTestEnv struct {
	repos *codeRepoHTTPTestRepo
	git   *codeRepoHTTPTestGit
}

// newCodeRepoHandlerTestServer mounts the code repository routes behind the real
// auth middleware and RequireAdmin.
func newCodeRepoHandlerTestServer(t *testing.T) (*http.ServeMux, *codeRepoHTTPTestEnv) {
	t.Helper()
	env := &codeRepoHTTPTestEnv{
		repos: newCodeRepoHTTPTestRepo(),
		git:   &codeRepoHTTPTestGit{commit: codeRepoHTTPTestCommit},
	}
	service := application.NewCodeRepoService(env.repos, env.git,
		catalogHTTPTestClock{now: codeRepoHTTPTestNow})
	mux := http.NewServeMux()
	guard := func(next http.Handler) http.Handler {
		return identityhttp.AuthMiddleware(catalogHTTPTestAuthenticator{})(identityhttp.RequireAdmin(next))
	}
	NewCodeRepoHandler(service).RegisterRoutes(mux, guard)
	return mux, env
}

// codeRepoRequest performs one request against the code repository test mux.
func codeRepoRequest(mux *http.ServeMux, method, path, token, body string) *httptest.ResponseRecorder {
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// decodeCodeRepoData decodes the {"data": ...} success envelope.
func decodeCodeRepoData[T any](t *testing.T, rr *httptest.ResponseRecorder) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode %q: %v", rr.Body.String(), err)
	}
	return envelope.Data
}

// decodeCodeRepoPage decodes the cursor page envelope.
func decodeCodeRepoPage(t *testing.T, rr *httptest.ResponseRecorder) ([]map[string]any, string, bool) {
	t.Helper()
	var envelope struct {
		Data       []map[string]any `json:"data"`
		NextCursor string           `json:"next_cursor"`
		HasMore    bool             `json:"has_more"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode page %q: %v", rr.Body.String(), err)
	}
	return envelope.Data, envelope.NextCursor, envelope.HasMore
}

// decodeCodeRepoError decodes the shared error envelope.
func decodeCodeRepoError(t *testing.T, rr *httptest.ResponseRecorder) platform.APIError {
	t.Helper()
	var envelope struct {
		Error platform.APIError `json:"error"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error body %q: %v", rr.Body.String(), err)
	}
	return envelope.Error
}

// assertCodeRepoErrorEnvelope asserts the status, code and details.reason.
func assertCodeRepoErrorEnvelope(t *testing.T, rr *httptest.ResponseRecorder, status int, code, reason string) platform.APIError {
	t.Helper()
	if rr.Code != status {
		t.Fatalf("status=%d want %d (body=%s)", rr.Code, status, rr.Body.String())
	}
	apiErr := decodeCodeRepoError(t, rr)
	if apiErr.Code != code {
		t.Fatalf("code=%q want %q", apiErr.Code, code)
	}
	if reason != "" {
		if got := apiErr.Details["reason"]; got != reason {
			t.Fatalf("details.reason=%v want %q (details=%v)", got, reason, apiErr.Details)
		}
	}
	return apiErr
}

func TestCodeRepoHandlerCreateCodeRepo(t *testing.T) {
	mux, env := newCodeRepoHandlerTestServer(t)

	rr := codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos", catalogAdminToken,
		`{"key":"web","name":"Web","localPath":"/srv/repo","fixedBranch":"main","includeGlobs":["src/**"],"excludeGlobs":["dist/**"]}`)
	if rr.Code != http.StatusCreated {
		t.Fatalf("status=%d want 201 (body=%s)", rr.Code, rr.Body.String())
	}
	if contentType := rr.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "application/json") {
		t.Fatalf("content-type=%q", contentType)
	}
	data := decodeCodeRepoData[map[string]any](t, rr)
	if data["id"] != float64(1) || data["key"] != "web" || data["name"] != "Web" {
		t.Fatalf("data=%v", data)
	}
	if data["localPath"] != "/srv/repo" || data["fixedBranch"] != "main" || data["status"] != domain.StatusActive {
		t.Fatalf("data=%v", data)
	}
	if data["lastCommit"] != codeRepoHTTPTestCommit || data["lastValidationError"] != "" {
		t.Fatalf("data=%v", data)
	}
	if data["lastValidatedAt"] == nil {
		t.Fatalf("lastValidatedAt is null: %v", data)
	}
	if data["lastIndexedAt"] != nil {
		t.Fatalf("lastIndexedAt=%v want null: P2 never indexes", data["lastIndexedAt"])
	}
	include, ok := data["includeGlobs"].([]any)
	if !ok || len(include) != 1 || include[0] != "src/**" {
		t.Fatalf("includeGlobs=%v", data["includeGlobs"])
	}
	exclude, ok := data["excludeGlobs"].([]any)
	if !ok || len(exclude) != 1 || exclude[0] != "dist/**" {
		t.Fatalf("excludeGlobs=%v", data["excludeGlobs"])
	}
	if env.repos.createCalls != 1 {
		t.Fatalf("createCalls=%d want 1", env.repos.createCalls)
	}
}

func TestCodeRepoHandlerCreateValidationFailure(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		field  string
		reason string
	}{
		{
			name:   "path not found",
			err:    domain.NewInvalid("localPath", "path_not_found", "local path does not exist"),
			field:  "localPath",
			reason: "path_not_found",
		},
		{
			name:   "path not git repo",
			err:    domain.NewInvalid("localPath", "path_not_git_repo", "local path is not a Git work tree"),
			field:  "localPath",
			reason: "path_not_git_repo",
		},
		{
			name:   "branch not found",
			err:    domain.NewInvalid("fixedBranch", "branch_not_found", "branch does not resolve to a commit"),
			field:  "fixedBranch",
			reason: "branch_not_found",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			mux, env := newCodeRepoHandlerTestServer(t)
			env.git.err = tc.err

			rr := codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos", catalogAdminToken,
				`{"key":"web","name":"Web","localPath":"/srv/repo","fixedBranch":"main"}`)
			apiErr := assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity,
				platform.CodeInvalidArgument, tc.reason)
			if apiErr.Details["field"] != tc.field {
				t.Fatalf("details.field=%v want %q", apiErr.Details["field"], tc.field)
			}
			if env.repos.createCalls != 0 {
				t.Fatalf("createCalls=%d want 0 on a failed validation", env.repos.createCalls)
			}
			if len(env.repos.codeRepos) != 0 {
				t.Fatalf("persisted rows=%v want none", env.repos.codeRepos)
			}
		})
	}

	// An invalid branch format is rejected before any Git call.
	mux, env := newCodeRepoHandlerTestServer(t)
	rr := codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos", catalogAdminToken,
		`{"key":"web","name":"Web","localPath":"/srv/repo","fixedBranch":"-evil"}`)
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_branch")
	if env.git.calls != 0 {
		t.Fatalf("git calls=%d want 0", env.git.calls)
	}
}

func TestCodeRepoHandlerCRUDAndValidate(t *testing.T) {
	mux, env := newCodeRepoHandlerTestServer(t)
	old := codeRepoHTTPTestNow.Add(-time.Hour)
	seeded := env.repos.seed(domain.CodeRepository{
		Key: "web", Name: "Web", LocalPath: "/srv/old", FixedBranch: "main",
		Status: domain.StatusActive, LastCommit: "old-commit", LastValidatedAt: &old,
	})

	// Read routes are available to a regular user.
	rr := codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data := decodeCodeRepoData[map[string]any](t, rr)
	if data["id"] != float64(seeded.ID) || data["lastCommit"] != "old-commit" || data["fixedBranch"] != "main" {
		t.Fatalf("data=%v", data)
	}

	// A changed checkout is revalidated and its commit refreshed on update.
	env.git.commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	rr = codeRepoRequest(mux, http.MethodPatch, "/api/resources/code-repos/1", catalogAdminToken,
		`{"name":"Web v2","localPath":"/srv/new","status":"disabled"}`)
	if rr.Code != http.StatusOK {
		t.Fatalf("patch status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data = decodeCodeRepoData[map[string]any](t, rr)
	if data["name"] != "Web v2" || data["localPath"] != "/srv/new" || data["status"] != domain.StatusDisabled {
		t.Fatalf("data=%v", data)
	}
	if data["lastCommit"] != "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb" || data["lastValidatedAt"] == nil {
		t.Fatalf("data=%v", data)
	}
	if env.git.calls != 1 || env.git.paths[0] != "/srv/new" || env.git.labels[0] != "main" {
		t.Fatalf("git path=%v branch=%v", env.git.paths, env.git.labels)
	}

	// The recheck action refreshes the commit again.
	env.git.commit = "cccccccccccccccccccccccccccccccccccccccc"
	rr = codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos/1/validate", catalogAdminToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("validate status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	data = decodeCodeRepoData[map[string]any](t, rr)
	if data["lastCommit"] != "cccccccccccccccccccccccccccccccccccccccc" {
		t.Fatalf("lastCommit=%v", data["lastCommit"])
	}
	if env.git.calls != 2 || env.git.paths[1] != "/srv/new" {
		t.Fatalf("git calls=%d paths=%v", env.git.calls, env.git.paths)
	}

	// Delete removes the row and returns 204.
	rr = codeRepoRequest(mux, http.MethodDelete, "/api/resources/code-repos/1", catalogAdminToken, "")
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d want 204 (body=%s)", rr.Code, rr.Body.String())
	}
	if env.repos.deleteCalls != 1 {
		t.Fatalf("deleteCalls=%d want 1", env.repos.deleteCalls)
	}
	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/1", catalogUserToken, "")
	assertCodeRepoErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}

func TestCodeRepoHandlerUpdateValidationFailurePreservesRow(t *testing.T) {
	mux, env := newCodeRepoHandlerTestServer(t)
	old := codeRepoHTTPTestNow.Add(-time.Hour)
	env.repos.seed(domain.CodeRepository{
		Key: "web", Name: "Web", LocalPath: "/srv/repo", FixedBranch: "main",
		Status: domain.StatusActive, LastCommit: "old-commit", LastValidatedAt: &old,
	})
	env.git.err = domain.NewInvalid("fixedBranch", "branch_not_found", `branch "nope" does not resolve to a commit`)

	rr := codeRepoRequest(mux, http.MethodPatch, "/api/resources/code-repos/1", catalogAdminToken,
		`{"fixedBranch":"nope"}`)
	apiErr := assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity,
		platform.CodeInvalidArgument, "branch_not_found")
	if apiErr.Details["field"] != "fixedBranch" {
		t.Fatalf("details.field=%v", apiErr.Details["field"])
	}
	if env.repos.updateCalls != 0 {
		t.Fatalf("updateCalls=%d want 0", env.repos.updateCalls)
	}

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/1", catalogUserToken, "")
	data := decodeCodeRepoData[map[string]any](t, rr)
	if data["fixedBranch"] != "main" || data["lastCommit"] != "old-commit" {
		t.Fatalf("stored row changed: %v", data)
	}
}

func TestCodeRepoHandlerListPagination(t *testing.T) {
	mux, env := newCodeRepoHandlerTestServer(t)
	env.repos.seed(domain.CodeRepository{Key: "one"})
	env.repos.seed(domain.CodeRepository{Key: "two"})
	env.repos.seed(domain.CodeRepository{Key: "three", Status: domain.StatusDisabled})

	rr := codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos?limit=2", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	items, next, hasMore := decodeCodeRepoPage(t, rr)
	if len(items) != 2 || items[0]["key"] != "one" || !hasMore || next == "" {
		t.Fatalf("items=%v next=%q hasMore=%v", items, next, hasMore)
	}
	if items[0]["lastIndexedAt"] != nil {
		t.Fatalf("lastIndexedAt=%v want null", items[0]["lastIndexedAt"])
	}

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos?limit=2&cursor="+next, catalogUserToken, "")
	items, next, hasMore = decodeCodeRepoPage(t, rr)
	if len(items) != 1 || items[0]["key"] != "three" || hasMore || next != "" {
		t.Fatalf("items=%v next=%q hasMore=%v", items, next, hasMore)
	}

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos?status=disabled", catalogUserToken, "")
	items, _, hasMore = decodeCodeRepoPage(t, rr)
	if len(items) != 1 || items[0]["key"] != "three" || hasMore {
		t.Fatalf("items=%v hasMore=%v", items, hasMore)
	}

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos?limit=101", catalogUserToken, "")
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_limit")
	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos?cursor=not-a-cursor!", catalogUserToken, "")
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_cursor")
}

func TestCodeRepoHandlerWriteRoutesRequireAdmin(t *testing.T) {
	mux, env := newCodeRepoHandlerTestServer(t)
	env.repos.seed(domain.CodeRepository{Key: "web"})

	writes := []struct{ method, path, body string }{
		{http.MethodPost, "/api/resources/code-repos", `{"key":"web","name":"Web","localPath":"/srv/repo","fixedBranch":"main"}`},
		{http.MethodPatch, "/api/resources/code-repos/1", `{"name":"x"}`},
		{http.MethodDelete, "/api/resources/code-repos/1", ""},
		{http.MethodPost, "/api/resources/code-repos/1/validate", ""},
	}
	for _, write := range writes {
		t.Run(write.method+" "+write.path, func(t *testing.T) {
			rr := codeRepoRequest(mux, write.method, write.path, catalogUserToken, write.body)
			assertCodeRepoErrorEnvelope(t, rr, http.StatusForbidden, platform.CodeForbidden, "")
		})
		rr := codeRepoRequest(mux, write.method, write.path, "", write.body)
		if rr.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without token: status=%d want 401", write.method, write.path, rr.Code)
		}
	}
	if env.repos.createCalls != 0 || env.repos.updateCalls != 0 || env.repos.deleteCalls != 0 {
		t.Fatalf("forbidden writes reached the repository: create=%d update=%d delete=%d",
			env.repos.createCalls, env.repos.updateCalls, env.repos.deleteCalls)
	}
	if env.git.calls != 0 {
		t.Fatalf("forbidden validate reached Git: calls=%d", env.git.calls)
	}

	// Reads stay available to regular users (the global auth middleware still
	// requires a login in production).
	rr := codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/1", catalogUserToken, "")
	if rr.Code != http.StatusOK {
		t.Fatalf("get status=%d want 200 (body=%s)", rr.Code, rr.Body.String())
	}
}

func TestCodeRepoHandlerRejectsBadRequests(t *testing.T) {
	mux, _ := newCodeRepoHandlerTestServer(t)

	oversized := `{"key":"web","name":"` + strings.Repeat("a", 64<<10) + `"}`
	rr := codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos", catalogAdminToken, oversized)
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "body_too_large")

	rr = codeRepoRequest(mux, http.MethodPost, "/api/resources/code-repos", catalogAdminToken, `{"key":`)
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_json")

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/not-a-number", catalogUserToken, "")
	assertCodeRepoErrorEnvelope(t, rr, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")

	rr = codeRepoRequest(mux, http.MethodGet, "/api/resources/code-repos/999", catalogUserToken, "")
	assertCodeRepoErrorEnvelope(t, rr, http.StatusNotFound, platform.CodeNotFound, "not_found")
}
