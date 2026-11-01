package codeintelapi_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	testCommitA = "1111111111111111111111111111111111111111"
	testCommitB = "2222222222222222222222222222222222222222"
)

var testTime = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

type startIndexCall struct {
	versionID int64
	repoID    int64
	commit    string
}

type stubIndexService struct {
	startRun     codeintel.IndexRun
	startCreated bool
	startErr     error
	startCalls   []startIndexCall
	latestRun    codeintel.IndexRun
	latestErr    error
	getRun       codeintel.IndexRun
	getErr       error
	statuses     []codeintelapp.RepoStatus
	statusErr    error
	tree         []codeintel.Entity
	treeCursor   string
	treeMore     bool
	treeErr      error
}

func (s *stubIndexService) Start(_ context.Context, versionID, repoID int64, commit string) (codeintel.IndexRun, bool, error) {
	s.startCalls = append(s.startCalls, startIndexCall{versionID: versionID, repoID: repoID, commit: commit})
	return s.startRun, s.startCreated, s.startErr
}

func (s *stubIndexService) Latest(_ context.Context, _ int64) (codeintel.IndexRun, error) {
	return s.latestRun, s.latestErr
}

func (s *stubIndexService) Get(_ context.Context, _ int64) (codeintel.IndexRun, error) {
	return s.getRun, s.getErr
}

func (s *stubIndexService) RepoStatus(_ context.Context, _ int64) ([]codeintelapp.RepoStatus, error) {
	return s.statuses, s.statusErr
}

func (s *stubIndexService) Tree(_ context.Context, _ int64, _, _, _ string, _ int) ([]codeintel.Entity, string, bool, error) {
	return s.tree, s.treeCursor, s.treeMore, s.treeErr
}

type stubQueryService struct {
	window      codeintel.FileWindow
	windowErr   error
	diff        codeintel.DiffResult
	diffCursor  string
	diffMore    bool
	diffErr     error
	fileDiff    codeintel.FileDiff
	fileDiffErr error
	commit      codeintelapp.CommitView
	commitErr   error
	filePaths   []string
}

func (s *stubQueryService) File(_ context.Context, _ int64, _, _ string, _, _ int) (codeintel.FileWindow, error) {
	return s.window, s.windowErr
}

func (s *stubQueryService) Diff(_ context.Context, _ int64, _, _, _, _ string, _ int) (codeintel.DiffResult, string, bool, error) {
	return s.diff, s.diffCursor, s.diffMore, s.diffErr
}

func (s *stubQueryService) FileDiff(_ context.Context, _ int64, _, _, path string) (codeintel.FileDiff, error) {
	s.filePaths = append(s.filePaths, path)
	return s.fileDiff, s.fileDiffErr
}

func (s *stubQueryService) Commit(_ context.Context, _ int64, _ string) (codeintelapp.CommitView, error) {
	return s.commit, s.commitErr
}

func testMux(t *testing.T, handler *codeintelapi.Handler) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "admin"})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
	handler.Register(mux, authenticated)
	return mux
}

func doRequest(t *testing.T, mux http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func dataObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, recorder.Body.String())
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %s", recorder.Body.String())
	}
	return data
}

func dataList(t *testing.T, recorder *httptest.ResponseRecorder) ([]any, map[string]any) {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v (%s)", err, recorder.Body.String())
	}
	data, ok := envelope["data"].([]any)
	if !ok {
		t.Fatalf("data is not an array: %s", recorder.Body.String())
	}
	return data, envelope
}

func errorObject(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope: %v (%s)", err, recorder.Body.String())
	}
	apiErr, ok := envelope["error"].(map[string]any)
	if !ok {
		t.Fatalf("error envelope missing: %s", recorder.Body.String())
	}
	return apiErr
}

func sampleRun() codeintel.IndexRun {
	return codeintel.IndexRun{
		ID: 5, VersionID: 1, RepoID: 10, CommitSHA: testCommitA, Branch: "main",
		Status: "done", FilesTotal: 3, FilesIndexed: 3,
		Stats: json.RawMessage(`{"entries_indexed":3}`), Errors: json.RawMessage(`[]`),
		StartedAt: &testTime, FinishedAt: &testTime, CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func sampleRepository() codeintelapp.RepoRef {
	return codeintelapp.RepoRef{
		ID: 10, Name: "repo", Path: "/tmp/repo", Branch: "main",
		Include: []string{"src/**"}, Exclude: []string{}, Status: "active",
		CreatedBy: 42, CreatedAt: testTime, UpdatedAt: testTime,
	}
}

func TestStartIndexReturns202And200(t *testing.T) {
	index := &stubIndexService{startRun: sampleRun(), startCreated: true}
	handler := codeintelapi.New(index, &stubQueryService{})
	mux := testMux(t, handler)

	recorder := doRequest(t, mux, http.MethodPost, "/api/versions/1/code/index", `{"repoId":"10"}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	data := dataObject(t, recorder)
	if data["id"] != "5" || data["repo_id"] != "10" || data["commit_sha"] != testCommitA {
		t.Fatalf("data = %+v", data)
	}
	if data["status"] != "done" || data["source"] != "git" {
		t.Fatalf("data = %+v", data)
	}
	if len(index.startCalls) != 1 || index.startCalls[0] != (startIndexCall{versionID: 1, repoID: 10, commit: ""}) {
		t.Fatalf("start calls = %+v", index.startCalls)
	}

	index.startCreated = false
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/index", `{"repoId":"10"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reused status = %d (%s)", recorder.Code, recorder.Body.String())
	}
}

func TestStartIndexMapsErrors(t *testing.T) {
	conflict := platform.StateConflict("an index run is already in progress")
	conflict.Details = map[string]any{"reason": "index_in_progress"}
	index := &stubIndexService{startErr: conflict}
	mux := testMux(t, codeintelapi.New(index, &stubQueryService{}))
	recorder := doRequest(t, mux, http.MethodPost, "/api/versions/1/code/index", `{"repoId":"10"}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d", recorder.Code)
	}
	apiErr := errorObject(t, recorder)
	if apiErr["code"] != "state_conflict" {
		t.Fatalf("error = %+v", apiErr)
	}
	if details, _ := apiErr["details"].(map[string]any); details["reason"] != "index_in_progress" {
		t.Fatalf("details = %+v", apiErr["details"])
	}

	index.startErr = &platform.Error{Status: http.StatusUnprocessableEntity, Code: "repo_not_bound", Message: "not bound"}
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/index", `{"repoId":"10"}`)
	if recorder.Code != http.StatusUnprocessableEntity || errorObject(t, recorder)["code"] != "repo_not_bound" {
		t.Fatalf("unbound response = %d %s", recorder.Code, recorder.Body.String())
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/index", `{"repoId":10}`)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("numeric repoId status = %d", recorder.Code)
	}
}

func TestListReposLatestAndGetRun(t *testing.T) {
	run := sampleRun()
	latest := run
	index := &stubIndexService{
		statuses: []codeintelapp.RepoStatus{{
			RepoID: 10, Repository: sampleRepository(), Latest: &latest,
			BaselineCommit: testCommitA,
		}},
		latestRun: run,
		getRun:    run,
	}
	mux := testMux(t, codeintelapi.New(index, &stubQueryService{}))

	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/repos", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("repos status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, envelope := dataList(t, recorder)
	if len(items) != 1 || envelope["has_more"] != false {
		t.Fatalf("repos = %s", recorder.Body.String())
	}
	status := items[0].(map[string]any)
	if status["repo_id"] != "10" || status["baseline_commit"] != testCommitA || status["index_in_progress"] != false {
		t.Fatalf("status = %+v", status)
	}
	repository := status["repository"].(map[string]any)
	if repository["id"] != "10" || repository["name"] != "repo" || repository["created_by"] != "42" {
		t.Fatalf("repository = %+v", repository)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/index-runs/latest", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("latest status = %d", recorder.Code)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/index-runs/5", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("get run status = %d", recorder.Code)
	}

	index.getRun = sampleRun()
	index.getRun.RepoID = 11
	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/index-runs/5", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-repo run status = %d", recorder.Code)
	}

	index.latestErr = codeintel.ErrNotFound
	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/index-runs/latest", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing latest status = %d", recorder.Code)
	}
}

func TestTreeFileDiffAndCommit(t *testing.T) {
	entity := codeintel.Entity{
		ID: 31, RepoID: 10, CommitSHA: testCommitA, Kind: codeintel.EntityFile,
		Path: "src/a.ts", Name: "a.ts", BlobHash: "git-sha1:" + testCommitA,
		Source: "git", Confidence: codeintel.ConfidenceHigh,
		Meta: json.RawMessage(`{"size_bytes":42}`),
	}
	blob := testCommitA
	query := &stubQueryService{
		window: codeintel.FileWindow{
			Path: "src/a.ts", Commit: testCommitA, Lines: []string{"one", "two"},
			StartLine: 1, EndLine: 2, TotalLines: 2,
		},
		diff: codeintel.DiffResult{Files: []codeintel.ChangedFile{{
			Path: "src/a.ts", OldPath: "", Status: "modified", Additions: 2, Deletions: 1, HeadBlobSHA: &blob,
		}}},
		fileDiff: codeintel.FileDiff{Path: "src/a.ts", Patch: "diff --git a/src/a.ts b/src/a.ts\n"},
		commit: codeintelapp.CommitView{
			SHA: testCommitA, Branch: "main", Message: "commit A", AuthorName: "Dev",
			AuthoredAt: testTime, CommittedAt: testTime, Parents: []string{testCommitB}, IsHead: true,
		},
	}
	index := &stubIndexService{tree: []codeintel.Entity{entity}, treeCursor: "src/b.ts", treeMore: true}
	mux := testMux(t, codeintelapi.New(index, query))

	recorder := doRequest(t, mux, http.MethodGet, "/api/code/repos/10/tree?commit="+testCommitA+"&limit=50", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("tree status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	items, envelope := dataList(t, recorder)
	if len(items) != 1 || envelope["next_cursor"] != "src/b.ts" || envelope["has_more"] != true {
		t.Fatalf("tree envelope = %s", recorder.Body.String())
	}
	tree := items[0].(map[string]any)
	if tree["path"] != "src/a.ts" || tree["kind"] != "file" || tree["size_bytes"] != float64(42) {
		t.Fatalf("tree = %+v", tree)
	}
	if recorder := doRequest(t, mux, http.MethodGet, "/api/code/repos/10/tree", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing commit status = %d", recorder.Code)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/file?commit="+testCommitA+"&path=src/a.ts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("file status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	file := dataObject(t, recorder)
	sum := sha256.Sum256([]byte("one\ntwo"))
	if file["content"] != "one\ntwo" || file["sha256"] != hex.EncodeToString(sum[:]) || file["binary"] != false {
		t.Fatalf("file = %+v", file)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/diff?base="+testCommitA+"&head="+testCommitB, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("diff status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	diffItems, _ := dataList(t, recorder)
	changed := diffItems[0].(map[string]any)
	if changed["path"] != "src/a.ts" || changed["status"] != "modified" || changed["head_blob_hash"] != testCommitA {
		t.Fatalf("changed file = %+v", changed)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/diff/file?base="+testCommitA+"&head="+testCommitB+"&path=src/a.ts", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("file diff status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	fileDiff := dataObject(t, recorder)
	if fileDiff["patch"] == nil || !strings.Contains(fileDiff["patch"].(string), "diff --git") {
		t.Fatalf("file diff = %+v", fileDiff)
	}
	if fileDiff["large"] != false || fileDiff["binary"] != false || fileDiff["deleted"] != false {
		t.Fatalf("file diff flags = %+v", fileDiff)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/commits/"+testCommitA, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("commit status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	commit := dataObject(t, recorder)
	if commit["sha"] != testCommitA || commit["is_head"] != true || commit["branch"] != "main" {
		t.Fatalf("commit = %+v", commit)
	}
}

func TestBinaryFileReturnsEmptyWindow(t *testing.T) {
	query := &stubQueryService{windowErr: &platform.Error{Status: http.StatusUnprocessableEntity, Code: "binary_file", Message: "binary"}}
	mux := testMux(t, codeintelapi.New(&stubIndexService{}, query))
	recorder := doRequest(t, mux, http.MethodGet, "/api/code/repos/10/file?commit="+testCommitA+"&path=logo.png", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("binary status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	file := dataObject(t, recorder)
	if file["binary"] != true || file["content"] != "" {
		t.Fatalf("binary file = %+v", file)
	}
	sum := sha256.Sum256(nil)
	if file["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("binary sha = %v", file["sha256"])
	}
}
