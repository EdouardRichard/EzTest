package codeintelapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelapi "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type stubAnalysisService struct {
	changeSet   codeintelapp.ChangeSet
	changesErr  error
	startRun    codeintel.Analysis
	startCreate bool
	startErr    error
	startCalls  []startAnalysisCall
	latest      codeintel.Analysis
	latestErr   error
	get         codeintel.Analysis
	getErr      error
	facts       []codeintel.Fact
	factsCursor string
	factsMore   bool
	factsErr    error
	search      []codeintel.Entity
	searchErr   error
}

type startAnalysisCall struct {
	versionID int64
	repoID    int64
	head      string
	force     bool
}

func (s *stubAnalysisService) Start(_ context.Context, versionID, repoID int64, headCommit string, force bool) (codeintel.Analysis, bool, error) {
	s.startCalls = append(s.startCalls, startAnalysisCall{versionID: versionID, repoID: repoID, head: headCommit, force: force})
	return s.startRun, s.startCreate, s.startErr
}

func (s *stubAnalysisService) Latest(_ context.Context, _, _ int64) (codeintel.Analysis, error) {
	return s.latest, s.latestErr
}

func (s *stubAnalysisService) Get(_ context.Context, _ int64) (codeintel.Analysis, error) {
	return s.get, s.getErr
}

func (s *stubAnalysisService) Facts(_ context.Context, _ int64, _, _, _, _ string, _ int) ([]codeintel.Fact, string, bool, error) {
	return s.facts, s.factsCursor, s.factsMore, s.factsErr
}

func (s *stubAnalysisService) Search(_ context.Context, _ int64, _, _, _ string, _ int) ([]codeintel.Entity, error) {
	return s.search, s.searchErr
}

func (s *stubAnalysisService) Changes(_ context.Context, _, _ int64, _ string) (codeintelapp.ChangeSet, error) {
	return s.changeSet, s.changesErr
}

func sampleAnalysis() codeintel.Analysis {
	return codeintel.Analysis{
		ID: 7, VersionID: 1, RepoID: 10,
		BaseCommitSHA: testCommitA, HeadCommitSHA: testCommitB,
		Status: codeintel.AnalysisDone, Degraded: true,
		Model: "deepseek-flash", PromptVersion: "p5.1", AnalysisVersion: 2,
		ChangedFiles: json.RawMessage(`[{"path":"src/a.ts","old_path":null,"status":"modified","additions":2,"deletions":1,"binary":false,"head_blob_hash":"` + testCommitA + `"}]`),
		Stats:        json.RawMessage(`{"facts":2,"edges":1}`),
		Unmapped:     json.RawMessage(`[{"path":"src/b.ts","reason":"unsupported_content"}]`),
		Errors:       json.RawMessage(`[{"code":"ai_unavailable","message":"one file was skipped"}]`),
		FactVersions: json.RawMessage(`{"route:/orders":1}`),
		CreatedAt:    testTime, UpdatedAt: testTime,
	}
}

func TestChangesAndCreateAnalysis(t *testing.T) {
	analysis := &stubAnalysisService{
		changeSet: codeintelapp.ChangeSet{
			RepoID: 10, BaseCommitSHA: testCommitA, HeadCommitSHA: testCommitB,
			Files: []codeintel.ChangedFile{{Path: "src/a.ts", Status: "modified", Additions: 2}},
		},
		startRun: sampleAnalysis(), startCreate: true,
	}
	mux := testMux(t, codeintelapi.New(&stubIndexService{}, &stubQueryService{}, codeintelapi.WithAnalysis(analysis)))

	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/changes?repoId=10", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("changes status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	changes := dataObject(t, recorder)
	if changes["repo_id"] != "10" || changes["base_commit_sha"] != testCommitA || changes["truncated"] != false {
		t.Fatalf("changes = %+v", changes)
	}
	files := changes["files"].([]any)
	if len(files) != 1 {
		t.Fatalf("files = %+v", files)
	}

	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/analyses", `{"repoId":"10","force":true}`)
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("analysis create status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	created := dataObject(t, recorder)
	if created["id"] != "7" || created["version_id"] != "1" || created["repo_id"] != "10" ||
		created["status"] != "done" || created["degraded"] != true || created["analysis_version"] != float64(2) {
		t.Fatalf("analysis = %+v", created)
	}
	if created["changed_files_count"] != float64(1) || created["unmapped_count"] != float64(1) || created["errors_count"] != float64(1) {
		t.Fatalf("analysis counts = %+v", created)
	}
	if len(analysis.startCalls) != 1 || !analysis.startCalls[0].force || analysis.startCalls[0].repoID != 10 {
		t.Fatalf("start calls = %+v", analysis.startCalls)
	}

	analysis.startCreate = false
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/analyses", `{"repoId":"10"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("reused analysis status = %d", recorder.Code)
	}

	conflict := platform.StateConflict("analysis in progress")
	conflict.Details = map[string]any{"reason": "analysis_in_progress"}
	analysis.startErr = conflict
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/analyses", `{"repoId":"10"}`)
	if recorder.Code != http.StatusConflict || errorObject(t, recorder)["code"] != "state_conflict" {
		t.Fatalf("conflict status = %d (%s)", recorder.Code, recorder.Body.String())
	}

	analysis.startErr = &platform.Error{Status: http.StatusUnprocessableEntity, Code: "baseline_commit_missing", Message: "missing"}
	recorder = doRequest(t, mux, http.MethodPost, "/api/versions/1/code/analyses", `{"repoId":"10"}`)
	if recorder.Code != http.StatusUnprocessableEntity || errorObject(t, recorder)["code"] != "baseline_commit_missing" {
		t.Fatalf("baseline status = %d", recorder.Code)
	}
}

func TestLatestAndGetAnalysisDetail(t *testing.T) {
	analysis := &stubAnalysisService{latestErr: codeintel.ErrNotFound, get: sampleAnalysis()}
	mux := testMux(t, codeintelapi.New(&stubIndexService{}, &stubQueryService{}, codeintelapi.WithAnalysis(analysis)))

	recorder := doRequest(t, mux, http.MethodGet, "/api/versions/1/code/analyses/latest?repoId=10", "")
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("latest missing status = %d", recorder.Code)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/versions/1/code/analyses/7", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("detail status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	detail := dataObject(t, recorder)
	analysisView := detail["analysis"].(map[string]any)
	if analysisView["id"] != "7" || analysisView["model"] != "deepseek-flash" {
		t.Fatalf("analysis view = %+v", analysisView)
	}
	changed := detail["changed_files"].([]any)
	if len(changed) != 1 || changed[0].(map[string]any)["head_blob_hash"] != testCommitA {
		t.Fatalf("changed files = %+v", changed)
	}
	unmapped := detail["unmapped"].([]any)
	if len(unmapped) != 1 || unmapped[0].(map[string]any)["reason"] != "unsupported_content" {
		t.Fatalf("unmapped = %+v", unmapped)
	}
	candidates := detail["candidates"].([]any)
	if len(candidates) != 0 {
		t.Fatalf("candidates = %+v", candidates)
	}
	versions := detail["fact_versions"].(map[string]any)
	if versions["route:/orders"] != float64(1) {
		t.Fatalf("fact versions = %+v", versions)
	}
	errorsList := detail["errors"].([]any)
	if len(errorsList) != 1 || errorsList[0].(map[string]any)["code"] != "ai_unavailable" {
		t.Fatalf("errors = %+v", errorsList)
	}
}

func TestFactsAndSearchRoutes(t *testing.T) {
	fact := codeintel.Fact{
		ID: 9, RepoID: 10, CommitSHA: testCommitA, FactKey: "route:/orders", Version: 1,
		Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "/orders", Source: "ai",
		Confidence: codeintel.ConfidenceHigh, Model: "deepseek-flash", PromptVersion: "p5.1",
		Body: json.RawMessage(`{"route":"/orders"}`), Evidence: json.RawMessage(`[{"file":"src/routes.ts","start_line":1,"end_line":2,"excerpt_hash":"sha256:abc"}]`),
		CreatedAt: testTime, UpdatedAt: testTime,
	}
	entity := codeintel.Entity{
		ID: 31, RepoID: 10, CommitSHA: testCommitA, Kind: codeintel.EntityRoute,
		Path: "src/routes.ts", Name: "/orders", Source: "ai", Confidence: codeintel.ConfidenceHigh,
	}
	analysis := &stubAnalysisService{facts: []codeintel.Fact{fact}, factsCursor: "", factsMore: false, search: []codeintel.Entity{entity}}
	mux := testMux(t, codeintelapi.New(&stubIndexService{}, &stubQueryService{}, codeintelapi.WithAnalysis(analysis)))

	recorder := doRequest(t, mux, http.MethodGet, "/api/code/repos/10/facts?commit="+testCommitA, "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("facts status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	facts, envelope := dataList(t, recorder)
	if len(facts) != 1 || envelope["has_more"] != false {
		t.Fatalf("facts envelope = %s", recorder.Body.String())
	}
	factView := facts[0].(map[string]any)
	if factView["id"] != "9" || factView["fact_key"] != "route:/orders" || factView["version"] != float64(1) {
		t.Fatalf("fact view = %+v", factView)
	}
	if evidence := factView["evidence"].([]any); len(evidence) != 1 {
		t.Fatalf("evidence = %+v", evidence)
	}

	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/search?commit="+testCommitA+"&q=orders", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("search status = %d (%s)", recorder.Code, recorder.Body.String())
	}
	entities, _ := dataList(t, recorder)
	if len(entities) != 1 || entities[0].(map[string]any)["id"] != "31" {
		t.Fatalf("search = %s", recorder.Body.String())
	}

	analysis.factsErr = platform.Invalid("commit", "invalid commit")
	recorder = doRequest(t, mux, http.MethodGet, "/api/code/repos/10/facts?commit=bad", "")
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad facts status = %d", recorder.Code)
	}
}
