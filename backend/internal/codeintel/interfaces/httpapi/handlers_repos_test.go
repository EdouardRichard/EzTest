package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestRepoRoutesRequireAuthAndAdminIndex proves every repository read needs a
// login, that starting an index is admin-only, and that an admin trigger returns
// the fixed 202 {runId,status,commit} body with the actor forwarded.
func TestRepoRoutesRequireAuthAndAdminIndex(t *testing.T) {
	index := &fakeIndexService{
		startFn: func(context.Context, int64, int64) (domain.IndexRun, error) {
			return testIndexRun(9001, domain.RunQueued), nil
		},
		latestFn: func(context.Context, int64) (domain.IndexRun, error) {
			return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "codeintel: repository has no index run")
		},
	}
	repos := &fakeRepoCatalog{listFn: func(context.Context) ([]application.RepoInfo, error) {
		return []application.RepoInfo{testRepoInfo(3, "web-main")}, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Index: index, Repos: repos})

	t.Run("repository reads require authentication", func(t *testing.T) {
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos", "")
		assertErrorResponse(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
	})

	t.Run("a logged-in user can list repositories", func(t *testing.T) {
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
	})

	t.Run("a regular user cannot start an index", func(t *testing.T) {
		index.startCalls = nil
		recorder := serve(t, handler, http.MethodPost, "/api/code/repos/3/index", userToken)
		assertErrorResponse(t, recorder, http.StatusForbidden, platform.CodeForbidden, "")
		if len(index.startCalls) != 0 {
			t.Fatalf("Start calls = %+v, want none for a non-admin", index.startCalls)
		}
	})

	t.Run("an admin trigger returns 202 with the run identity", func(t *testing.T) {
		index.startCalls = nil
		recorder := serve(t, handler, http.MethodPost, "/api/code/repos/3/index", adminToken)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data struct {
				RunID  string `json:"runId"`
				Status string `json:"status"`
				Commit string `json:"commit"`
			} `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.RunID != "9001" {
			t.Fatalf("runId = %q, want the decimal string \"9001\"", body.Data.RunID)
		}
		if body.Data.Status != domain.RunQueued || body.Data.Commit != commitA {
			t.Fatalf("accepted body = %+v, want queued/%s", body.Data, commitA)
		}
		if len(index.startCalls) != 1 {
			t.Fatalf("Start calls = %+v, want one", index.startCalls)
		}
		if got := index.startCalls[0]; got.RepoID != 3 || got.ActorID != testAdminID {
			t.Fatalf("Start call = %+v, want repo 3 actor %d", got, testAdminID)
		}
	})
}

// TestRepoListProjectsLatestIndexState proves GET /api/code/repos joins the
// latest index run, keeps the index null when a repository has no run (it must
// not fail the whole list), and serializes repo ids as numbers and run ids as
// decimal strings (R6).
func TestRepoListProjectsLatestIndexState(t *testing.T) {
	done := testIndexRun(9001, domain.RunDone)
	done.Stats = []byte(`{"files":1200,"truncated":false,"durationMs":123}`)
	index := &fakeIndexService{latestFn: func(_ context.Context, repoID int64) (domain.IndexRun, error) {
		if repoID == 3 {
			return done, nil
		}
		return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "codeintel: repository has no index run")
	}}
	repos := &fakeRepoCatalog{listFn: func(context.Context) ([]application.RepoInfo, error) {
		return []application.RepoInfo{
			testRepoInfo(3, "web-main"),
			testRepoInfo(4, "api-main"),
		}, nil
	}}
	handler := newTestHandler(t, HandlerDeps{Index: index, Repos: repos})

	recorder := serve(t, handler, http.MethodGet, "/api/code/repos", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data []repoSummaryWire `json:"data"`
	}
	decodeBody(t, recorder, &body)
	if len(body.Data) != 2 {
		t.Fatalf("repositories = %d, want 2", len(body.Data))
	}

	first := body.Data[0]
	if first.ID != 3 {
		t.Fatalf("id = %d, want the P2 numeric id 3", first.ID)
	}
	if first.Key != "web-main" || first.Status != "active" || first.FixedBranch != "main" {
		t.Fatalf("summary = %+v, want the repository projection", first)
	}
	if first.LastCommit != commitA {
		t.Fatalf("lastCommit = %q, want the latest index commit", first.LastCommit)
	}
	if first.LastIndexedAt == nil || *first.LastIndexedAt == "" {
		t.Fatalf("lastIndexedAt = %v, want an ISO timestamp", first.LastIndexedAt)
	}
	if first.Index == nil {
		t.Fatal("index = null, want the latest run projection")
	}
	if first.Index.RunID != "9001" {
		t.Fatalf("runId = %q, want the decimal string \"9001\"", first.Index.RunID)
	}
	if first.Index.RepoID != 3 || first.Index.Commit != commitA || first.Index.Status != domain.RunDone {
		t.Fatalf("index projection = %+v", first.Index)
	}
	if first.Index.Stats == nil || first.Index.Stats.Files != 1200 || first.Index.Stats.Truncated {
		t.Fatalf("index stats = %+v, want files=1200 truncated=false", first.Index.Stats)
	}
	if first.Index.StartedAt == nil || first.Index.FinishedAt == nil {
		t.Fatalf("index timestamps = %v/%v, want both set", first.Index.StartedAt, first.Index.FinishedAt)
	}

	if body.Data[1].ID != 4 {
		t.Fatalf("second id = %d, want 4", body.Data[1].ID)
	}
	if body.Data[1].Index != nil {
		t.Fatalf("second index = %+v, want null for a repository with no runs", body.Data[1].Index)
	}
	if got := index.latestCalls; len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("GetLatest calls = %v, want [3 4]", got)
	}
	if repos.listCall != 1 {
		t.Fatalf("RepoCatalog.List calls = %d, want 1", repos.listCall)
	}
}

// TestStartIndexConflictAndQueueFull pins the 409 index_in_progress and the
// retryable 429 analysis_queue_full mappings of POST /index.
func TestStartIndexConflictAndQueueFull(t *testing.T) {
	t.Run("index in progress maps to 409", func(t *testing.T) {
		index := &fakeIndexService{startFn: func(context.Context, int64, int64) (domain.IndexRun, error) {
			return domain.IndexRun{}, domain.NewConflict("index_in_progress", "codeintel: repository already has an active index run")
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodPost, "/api/code/repos/3/index", adminToken)
		body := assertErrorResponse(t, recorder, http.StatusConflict, platform.CodeConflict, "index_in_progress")
		if body.Error.Retryable {
			t.Fatal("409 index_in_progress must not be retryable")
		}
	})

	t.Run("a full queue maps to retryable 429", func(t *testing.T) {
		queueErr := fmt.Errorf("%w: %w",
			domain.NewConflict("analysis_queue_full", "codeintel: the analysis queue is full"),
			application.ErrQueueFull)
		index := &fakeIndexService{startFn: func(context.Context, int64, int64) (domain.IndexRun, error) {
			return domain.IndexRun{}, queueErr
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodPost, "/api/code/repos/3/index", adminToken)
		body := assertErrorResponse(t, recorder, http.StatusTooManyRequests, platform.CodeRateLimited, "analysis_queue_full")
		if !body.Error.Retryable {
			t.Fatal("429 analysis_queue_full must be retryable")
		}
	})
}

// TestIndexRunLatestAndDetail covers the latest/detail run reads: run ids parse
// as decimal strings, missing runs are 404 index_run_not_found and a malformed
// id is a 422 invalid_id.
func TestIndexRunLatestAndDetail(t *testing.T) {
	run := testIndexRun(9001, domain.RunDone)
	run.Errors = []domain.AnalysisError{{Code: "internal", Message: "index failed"}}

	t.Run("latest run", func(t *testing.T) {
		index := &fakeIndexService{latestFn: func(context.Context, int64) (domain.IndexRun, error) {
			return run, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/index-runs/latest", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data indexRunWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.RunID != "9001" || body.Data.Status != domain.RunDone {
			t.Fatalf("run = %+v, want runId \"9001\" status done", body.Data)
		}
		if len(body.Data.Errors) != 1 || body.Data.Errors[0].Code != "internal" {
			t.Fatalf("errors = %+v, want the run error entry", body.Data.Errors)
		}
		if got := index.latestCalls; len(got) != 1 || got[0] != 3 {
			t.Fatalf("GetLatest calls = %v, want [3]", got)
		}
	})

	t.Run("latest run missing is 404", func(t *testing.T) {
		index := &fakeIndexService{latestFn: func(context.Context, int64) (domain.IndexRun, error) {
			return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "codeintel: repository has no index run")
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/index-runs/latest", userToken)
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "index_run_not_found")
	})

	t.Run("run detail parses the run id and scopes it to the repository", func(t *testing.T) {
		index := &fakeIndexService{getFn: func(_ context.Context, repoID, runID int64) (domain.IndexRun, error) {
			if repoID != 3 || runID != 9001 {
				t.Fatalf("Get(%d, %d), want (3, 9001)", repoID, runID)
			}
			return run, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/index-runs/9001", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data indexRunWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.RunID != "9001" {
			t.Fatalf("runId = %q, want \"9001\"", body.Data.RunID)
		}
	})

	t.Run("run detail missing is 404", func(t *testing.T) {
		index := &fakeIndexService{getFn: func(context.Context, int64, int64) (domain.IndexRun, error) {
			return domain.IndexRun{}, domain.NewNotFound("index_run_not_found", "codeintel: index run not found")
		}}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/index-runs/4040", userToken)
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "index_run_not_found")
	})

	t.Run("malformed run id is 422 invalid_id", func(t *testing.T) {
		index := &fakeIndexService{}
		handler := newTestHandler(t, HandlerDeps{Index: index})
		recorder := serve(t, handler, http.MethodGet, "/api/code/repos/3/index-runs/not-a-number", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(index.getCalls) != 0 {
			t.Fatalf("Get calls = %+v, want none for a malformed id", index.getCalls)
		}
	})
}

// TestRepoIndexRoutesRequireAuthentication proves the index reads are also
// behind the login middleware, not only the trigger.
func TestRepoIndexRoutesRequireAuthentication(t *testing.T) {
	handler := newTestHandler(t, HandlerDeps{Index: &fakeIndexService{}})
	for _, target := range []string{
		"/api/code/repos/3/index-runs/latest",
		"/api/code/repos/3/index-runs/9001",
	} {
		recorder := serve(t, handler, http.MethodGet, target, "")
		assertErrorResponse(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
	}
}
