package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Fixture commits are full lowercase 40-hex ids so they satisfy the gitx commit
// contract the application services forward.
const (
	commitA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	commitB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"

	adminToken = "test-admin-token"
	userToken  = "test-user-token"

	testAdminID = int64(1)
	testUserID  = int64(2)
)

// startedAt and finishedAt are the fixed run timestamps the DTO tests assert.
var (
	startedAt  = time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	finishedAt = time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
)

// testAuthenticator resolves the two bearer tokens the handler tests use: an
// administrator and a regular user. Any other token is unauthenticated.
type testAuthenticator struct{}

// Authenticate implements identityhttp.Authenticator.
func (testAuthenticator) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	switch rawToken {
	case adminToken:
		return identity.User{ID: testAdminID, Username: "admin", Role: identity.RoleAdmin, Status: identity.StatusActive}, nil
	case userToken:
		return identity.User{ID: testUserID, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}, nil
	default:
		return identity.User{}, identity.ErrUnauthenticated
	}
}

// startCall records one IndexService.Start invocation.
type startCall struct {
	RepoID  int64
	ActorID int64
}

// getRunCall records one IndexService.Get invocation.
type getRunCall struct {
	RepoID int64
	RunID  int64
}

// fakeIndexService is the hand-written application.IndexService consumer
// double. Each method records its arguments and delegates to the configured
// function.
type fakeIndexService struct {
	startFn  func(context.Context, int64, int64) (domain.IndexRun, error)
	getFn    func(context.Context, int64, int64) (domain.IndexRun, error)
	latestFn func(context.Context, int64) (domain.IndexRun, error)

	startCalls  []startCall
	getCalls    []getRunCall
	latestCalls []int64
}

func (f *fakeIndexService) Start(ctx context.Context, repoID, actorID int64) (domain.IndexRun, error) {
	f.startCalls = append(f.startCalls, startCall{RepoID: repoID, ActorID: actorID})
	if f.startFn == nil {
		return domain.IndexRun{}, nil
	}
	return f.startFn(ctx, repoID, actorID)
}

func (f *fakeIndexService) Get(ctx context.Context, repoID, runID int64) (domain.IndexRun, error) {
	f.getCalls = append(f.getCalls, getRunCall{RepoID: repoID, RunID: runID})
	if f.getFn == nil {
		return domain.IndexRun{}, nil
	}
	return f.getFn(ctx, repoID, runID)
}

func (f *fakeIndexService) GetLatest(ctx context.Context, repoID int64) (domain.IndexRun, error) {
	f.latestCalls = append(f.latestCalls, repoID)
	if f.latestFn == nil {
		return domain.IndexRun{}, nil
	}
	return f.latestFn(ctx, repoID)
}

// treeCall records one QueryService.Tree invocation.
type treeCall struct {
	RepoID int64
	Prefix string
	Commit string
	Cursor string
	Limit  int
}

// fileCall records one QueryService.File invocation.
type fileCall struct {
	ActorID   int64
	RepoID    int64
	Commit    string
	Path      string
	StartLine int
	EndLine   int
}

// diffCall records one QueryService.Diff invocation.
type diffCall struct {
	RepoID int64
	Base   string
	Head   string
	Limit  int
	Cursor string
}

// fileDiffCall records one QueryService.FileDiff invocation.
type fileDiffCall struct {
	RepoID int64
	Base   string
	Head   string
	Path   string
}

// factsCall records one QueryService.Facts invocation.
type factsCall struct {
	RepoID int64
	Filter domain.EntityFilter
}

// searchCall records one QueryService.Search invocation.
type searchCall struct {
	ActorID int64
	RepoID  int64
	Commit  string
	Query   string
	Kind    string
	Limit   int
	Cursor  string
}

// commitCall records one QueryService.Commit invocation.
type commitCall struct {
	RepoID int64
	SHA    string
}

// versionChangesCall records one QueryService.VersionChanges invocation.
type versionChangesCall struct {
	VersionID int64
	Head      string
}

// fakeQueryService is the hand-written application.QueryService consumer
// double. Each method records its arguments and delegates to the configured
// function.
type fakeQueryService struct {
	treeFn     func(context.Context, int64, string, string, string, int) (domain.TreePage, error)
	fileFn     func(context.Context, int64, int64, string, string, int, int) (domain.FileWindow, error)
	diffFn     func(context.Context, int64, string, string, int, string) (domain.ChangeSetData, error)
	fileDiffFn func(context.Context, int64, string, string, string) (domain.FileDiff, error)
	factsFn    func(context.Context, int64, domain.EntityFilter) ([]domain.CodeEntity, string, bool, error)
	searchFn   func(context.Context, int64, int64, string, string, string, int, string) ([]domain.CodeEntity, string, bool, error)
	commitFn   func(context.Context, int64, string) (domain.CommitInfo, error)
	// versionChangesFn backs the version-scoped change preview added to the
	// QueryService consumer port by T15.
	versionChangesFn func(context.Context, int64, string) (domain.ChangeSetData, string, string, error)

	treeCalls           []treeCall
	fileCalls           []fileCall
	diffCalls           []diffCall
	fileDiffCalls       []fileDiffCall
	factsCalls          []factsCall
	searchCalls         []searchCall
	commitCalls         []commitCall
	versionChangesCalls []versionChangesCall
}

func (f *fakeQueryService) Tree(ctx context.Context, repoID int64, prefix, commit, cursor string, limit int) (domain.TreePage, error) {
	f.treeCalls = append(f.treeCalls, treeCall{RepoID: repoID, Prefix: prefix, Commit: commit, Cursor: cursor, Limit: limit})
	if f.treeFn == nil {
		return domain.TreePage{}, nil
	}
	return f.treeFn(ctx, repoID, prefix, commit, cursor, limit)
}

func (f *fakeQueryService) File(ctx context.Context, actorID, repoID int64, commit, path string, startLine, endLine int) (domain.FileWindow, error) {
	f.fileCalls = append(f.fileCalls, fileCall{ActorID: actorID, RepoID: repoID, Commit: commit, Path: path, StartLine: startLine, EndLine: endLine})
	if f.fileFn == nil {
		return domain.FileWindow{}, nil
	}
	return f.fileFn(ctx, actorID, repoID, commit, path, startLine, endLine)
}

func (f *fakeQueryService) Diff(ctx context.Context, repoID int64, base, head string, limit int, cursor string) (domain.ChangeSetData, error) {
	f.diffCalls = append(f.diffCalls, diffCall{RepoID: repoID, Base: base, Head: head, Limit: limit, Cursor: cursor})
	if f.diffFn == nil {
		return domain.ChangeSetData{}, nil
	}
	return f.diffFn(ctx, repoID, base, head, limit, cursor)
}

func (f *fakeQueryService) FileDiff(ctx context.Context, repoID int64, base, head, path string) (domain.FileDiff, error) {
	f.fileDiffCalls = append(f.fileDiffCalls, fileDiffCall{RepoID: repoID, Base: base, Head: head, Path: path})
	if f.fileDiffFn == nil {
		return domain.FileDiff{}, nil
	}
	return f.fileDiffFn(ctx, repoID, base, head, path)
}

func (f *fakeQueryService) Facts(ctx context.Context, repoID int64, filter domain.EntityFilter) ([]domain.CodeEntity, string, bool, error) {
	f.factsCalls = append(f.factsCalls, factsCall{RepoID: repoID, Filter: filter})
	if f.factsFn == nil {
		return nil, "", false, nil
	}
	return f.factsFn(ctx, repoID, filter)
}

func (f *fakeQueryService) Search(ctx context.Context, actorID, repoID int64, commit, q, kind string, limit int, cursor string) ([]domain.CodeEntity, string, bool, error) {
	f.searchCalls = append(f.searchCalls, searchCall{ActorID: actorID, RepoID: repoID, Commit: commit, Query: q, Kind: kind, Limit: limit, Cursor: cursor})
	if f.searchFn == nil {
		return nil, "", false, nil
	}
	return f.searchFn(ctx, actorID, repoID, commit, q, kind, limit, cursor)
}

func (f *fakeQueryService) Commit(ctx context.Context, repoID int64, sha string) (domain.CommitInfo, error) {
	f.commitCalls = append(f.commitCalls, commitCall{RepoID: repoID, SHA: sha})
	if f.commitFn == nil {
		return domain.CommitInfo{}, nil
	}
	return f.commitFn(ctx, repoID, sha)
}

func (f *fakeQueryService) VersionChanges(ctx context.Context, versionID int64, headCommit string) (domain.ChangeSetData, string, string, error) {
	f.versionChangesCalls = append(f.versionChangesCalls, versionChangesCall{VersionID: versionID, Head: headCommit})
	if f.versionChangesFn == nil {
		return domain.ChangeSetData{}, "", "", nil
	}
	return f.versionChangesFn(ctx, versionID, headCommit)
}

// fakeRepoCatalog is the hand-written application.RepoCatalog double.
type fakeRepoCatalog struct {
	getFn    func(context.Context, int64) (application.RepoInfo, error)
	listFn   func(context.Context) ([]application.RepoInfo, error)
	listCall int
}

func (f *fakeRepoCatalog) Get(ctx context.Context, repoID int64) (application.RepoInfo, error) {
	if f.getFn == nil {
		return application.RepoInfo{}, domain.NewInvalid("repoId", "code_repository_not_found", "repository not found")
	}
	return f.getFn(ctx, repoID)
}

func (f *fakeRepoCatalog) List(ctx context.Context) ([]application.RepoInfo, error) {
	f.listCall++
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx)
}

// newTestHandler mounts the codeintel routes behind the real identity bearer
// middleware exactly like the server's global middleware chain does, so the
// authentication and role assertions exercise the production auth path.
func newTestHandler(t *testing.T, deps HandlerDeps) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	NewHandler(deps).Register(mux)
	return identityhttp.AuthMiddleware(testAuthenticator{})(mux)
}

// serve performs one request against handler with a bearer token when token is
// not empty.
func serve(t *testing.T, handler http.Handler, method, target, token string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, nil)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// decodeBody decodes a recorded JSON response body into dst.
func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder, dst any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), dst); err != nil {
		t.Fatalf("decode body %q: %v", recorder.Body.String(), err)
	}
}

// errorEnvelopeWire is the shared platform error envelope shape.
type errorEnvelopeWire struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

// assertErrorResponse asserts the HTTP status and the shared error envelope
// code plus (when reason is not empty) details.reason.
func assertErrorResponse(t *testing.T, recorder *httptest.ResponseRecorder, status int, code, reason string) errorEnvelopeWire {
	t.Helper()
	if recorder.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", recorder.Code, status, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "application/json") {
		t.Fatalf("content type = %q, want application/json", got)
	}
	var body errorEnvelopeWire
	decodeBody(t, recorder, &body)
	if body.Error.Code != code {
		t.Fatalf("error code = %q, want %q (body %s)", body.Error.Code, code, recorder.Body.String())
	}
	if reason != "" {
		got, _ := body.Error.Details["reason"].(string)
		if got != reason {
			t.Fatalf("details.reason = %q, want %q (body %s)", got, reason, recorder.Body.String())
		}
	}
	return body
}

// indexRunStatsWire is the IndexRun.stats projection of the wire contract.
type indexRunStatsWire struct {
	Files     int  `json:"files"`
	Truncated bool `json:"truncated"`
}

// indexRunWire is the IndexRun DTO shape of frontend types.ts (R6). runId is a
// string; repoId/createdBy are numbers. Decoding a numeric runId into the string
// field fails, which is exactly the ID-type regression these tests catch.
type indexRunWire struct {
	RunID      string              `json:"runId"`
	RepoID     int64               `json:"repoId"`
	Commit     string              `json:"commit"`
	Source     string              `json:"source"`
	Status     string              `json:"status"`
	Stats      *indexRunStatsWire  `json:"stats"`
	Errors     []analysisErrorWire `json:"errors"`
	StartedAt  *string             `json:"startedAt"`
	FinishedAt *string             `json:"finishedAt"`
	CreatedBy  int64               `json:"createdBy"`
	CreatedAt  string              `json:"createdAt"`
}

// analysisErrorWire is the AnalysisError DTO shape (R6).
type analysisErrorWire struct {
	Code    string `json:"code"`
	Path    string `json:"path"`
	Message string `json:"message"`
}

// repoSummaryWire is the RepoSummary DTO shape (R6); index may be null.
type repoSummaryWire struct {
	ID            int64         `json:"id"`
	Key           string        `json:"key"`
	Name          string        `json:"name"`
	Status        string        `json:"status"`
	FixedBranch   string        `json:"fixedBranch"`
	LastCommit    string        `json:"lastCommit"`
	LastIndexedAt *string       `json:"lastIndexedAt"`
	Index         *indexRunWire `json:"index"`
}

// treeEntryWire is the tree entry DTO shape.
type treeEntryWire struct {
	Path        string `json:"path"`
	Type        string `json:"type"`
	Size        int64  `json:"size"`
	GitObjectID string `json:"gitObjectId"`
	Mode        string `json:"mode"`
	Unsupported bool   `json:"unsupported"`
}

// testIndexRun builds one index run fixture.
func testIndexRun(id int64, status string) domain.IndexRun {
	return domain.IndexRun{
		ID:         id,
		RepoID:     3,
		CommitSHA:  commitA,
		Source:     "t0",
		Status:     status,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
		CreatedBy:  11,
		CreatedAt:  startedAt,
	}
}

// testRepoInfo builds one repository fixture.
func testRepoInfo(id int64, key string) application.RepoInfo {
	indexedAt := finishedAt
	return application.RepoInfo{
		ID:            id,
		Key:           key,
		Name:          "Web 主仓",
		LocalPath:     "/srv/repos/web-main",
		FixedBranch:   "main",
		Status:        "active",
		LastIndexedAt: &indexedAt,
	}
}

// TestWriteServiceErrorMapsDomainReasons pins the §7.1/§6.10 error mapping:
// domain invalid 422, not-found 404, conflict 409, queue-full 429 before the
// generic conflict mapping, and operational (non-domain) failures 503
// git_unavailable.
func TestWriteServiceErrorMapsDomainReasons(t *testing.T) {
	queueFull := fmt.Errorf("%w: %w",
		domain.NewConflict("analysis_queue_full", "codeintel: the analysis queue is full"),
		application.ErrQueueFull)

	testCases := []struct {
		name      string
		err       error
		status    int
		code      string
		reason    string
		field     string
		retryable bool
	}{
		{
			name:   "invalid maps to 422 with reason",
			err:    domain.NewInvalid("path", "path_escape", "codeintel: path escapes the repository root"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "path_escape",
			field:  "path",
		},
		{
			name:   "not found maps to 404",
			err:    domain.NewNotFound("index_run_not_found", "codeintel: repository has no index run"),
			status: http.StatusNotFound,
			code:   platform.CodeNotFound,
			reason: "index_run_not_found",
		},
		{
			name:   "conflict maps to 409",
			err:    domain.NewConflict("index_in_progress", "codeintel: repository already has an active index run"),
			status: http.StatusConflict,
			code:   platform.CodeConflict,
			reason: "index_in_progress",
		},
		{
			name:      "queue full maps to retryable 429 before the conflict",
			err:       queueFull,
			status:    http.StatusTooManyRequests,
			code:      platform.CodeRateLimited,
			reason:    "analysis_queue_full",
			retryable: true,
		},
		{
			name:      "queue-full reason alone also maps to 429",
			err:       domain.NewConflict("analysis_queue_full", "codeintel: the analysis queue is full"),
			status:    http.StatusTooManyRequests,
			code:      platform.CodeRateLimited,
			reason:    "analysis_queue_full",
			retryable: true,
		},
		{
			name:      "operational git failure maps to retryable 503 git_unavailable",
			err:       errors.New("gitx: git show failed with exit code 128"),
			status:    http.StatusServiceUnavailable,
			code:      platform.CodeUnavailable,
			reason:    "git_unavailable",
			retryable: true,
		},
		{
			name:   "unknown domain kind is an internal error without its reason",
			err:    &domain.Error{Kind: errors.New("codeintel: unexpected"), Reason: "unexpected", Message: "boom"},
			status: http.StatusInternalServerError,
			code:   platform.CodeInternal,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/api/code/repos/3/tree", nil)
			WriteServiceError(recorder, request, testCase.err)

			body := assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
			if body.Error.Retryable != testCase.retryable {
				t.Fatalf("retryable = %v, want %v", body.Error.Retryable, testCase.retryable)
			}
			if testCase.field != "" {
				got, _ := body.Error.Details["field"].(string)
				if got != testCase.field {
					t.Fatalf("details.field = %q, want %q", got, testCase.field)
				}
			}
			// Operational failures must never echo the underlying command
			// detail (design spec §6.10: no internal paths / git output).
			if testCase.status == http.StatusServiceUnavailable && strings.Contains(body.Error.Message, "exit code") {
				t.Fatalf("503 message leaks the git failure: %q", body.Error.Message)
			}
			if testCase.status == http.StatusInternalServerError && body.Error.Message != "internal server error" {
				t.Fatalf("500 message = %q, want the generic message", body.Error.Message)
			}
		})
	}
}

// TestParseQueryLimit pins the shared 1-100 limit contract with the 20 default.
func TestParseQueryLimit(t *testing.T) {
	testCases := []struct {
		name    string
		target  string
		limit   int
		wantErr bool
	}{
		{name: "absent limit defaults to 20", target: "/api/code/repos/3/tree", limit: platform.DefaultLimit},
		{name: "explicit limit", target: "/api/code/repos/3/tree?limit=50", limit: 50},
		{name: "maximum limit", target: "/api/code/repos/3/tree?limit=100", limit: 100},
		{name: "zero is rejected", target: "/api/code/repos/3/tree?limit=0", wantErr: true},
		{name: "over maximum is rejected", target: "/api/code/repos/3/tree?limit=101", wantErr: true},
		{name: "non-numeric is rejected", target: "/api/code/repos/3/tree?limit=many", wantErr: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, testCase.target, nil)
			limit, err := ParseQueryLimit(request)
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("limit = %d, want an error", limit)
				}
				var requestErr *requestError
				if !errors.As(err, &requestErr) || requestErr.Status != http.StatusBadRequest {
					t.Fatalf("error = %v, want a 400 requestError", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("limit error = %v", err)
			}
			if limit != testCase.limit {
				t.Fatalf("limit = %d, want %d", limit, testCase.limit)
			}
		})
	}
}

// TestPathIDRejectsMalformedIDs pins the decimal-string path id parsing.
func TestPathIDRejectsMalformedIDs(t *testing.T) {
	testCases := []struct {
		name    string
		value   string
		wantErr bool
	}{
		{name: "positive id", value: "1844"},
		{name: "zero id", value: "0", wantErr: true},
		{name: "negative id", value: "-1", wantErr: true},
		{name: "non-numeric id", value: "abc", wantErr: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/code/repos/"+testCase.value, nil)
			request.SetPathValue("repoId", testCase.value)
			id, err := PathID(request, "repoId")
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("id = %d, want an error", id)
				}
				if reason := domain.Reason(err); reason != "invalid_id" {
					t.Fatalf("reason = %q, want invalid_id", reason)
				}
				return
			}
			if err != nil {
				t.Fatalf("id error = %v", err)
			}
			if id <= 0 {
				t.Fatalf("id = %d, want a positive id", id)
			}
		})
	}
}
