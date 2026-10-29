package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// analysisServer wires the analysis handlers with a fake service and a terminal
// batch of the addressed version.
func analysisServer(t *testing.T, analyses *fakeAnalysisService) (http.Handler, *fakeBatchReader) {
	t.Helper()
	batches := &fakeBatchReader{batch: terminalBatch(testVersionID, testBatchID)}
	return newTestServer(t, HandlerDeps{Analyses: analyses, Batches: batches}), batches
}

// TestAnalysisRoutesRequireAuthentication proves every analysis route is behind
// the global bearer auth.
func TestAnalysisRoutesRequireAuthentication(t *testing.T) {
	h, _ := analysisServer(t, &fakeAnalysisService{})
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/batches/7001/analysis", ""},
		{http.MethodGet, "/api/versions/101/batches/7001/analysis/9201", ""},
		{http.MethodPost, "/api/versions/101/batches/7001/analysis", `{"kind":"failure"}`},
	} {
		rec := doRequest(h, route.method, route.path, "", route.body)
		requireStatus(t, rec, http.StatusUnauthorized)
	}
}

// TestAnalysisListFiltersByKind proves the list is version/batch scoped, checks
// batch ownership and filters the optional kind at the HTTP boundary.
func TestAnalysisListFiltersByKind(t *testing.T) {
	failure := sampleAnalysisRun()
	triage := sampleAnalysisRun()
	triage.ID = 9202
	triage.Kind = domain.AnalysisKindIssueTriage
	var gotVersionID, gotBatchID int64
	analyses := &fakeAnalysisService{list: func(_ context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error) {
		gotVersionID, gotBatchID = versionID, batchID
		return []domain.AnalysisRun{failure, triage}, nil
	}}
	h, batches := analysisServer(t, analyses)

	rec := doRequest(h, http.MethodGet, "/api/versions/101/batches/7001/analysis?kind=failure", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if gotVersionID != testVersionID || gotBatchID != testBatchID {
		t.Fatalf("version=%d batch=%d", gotVersionID, gotBatchID)
	}
	if len(batches.calls) != 1 || batches.calls[0] != "101/7001" {
		t.Fatalf("batch gate calls=%v", batches.calls)
	}

	var envelope struct {
		Data []map[string]any `json:"data"`
	}
	decodeJSONBody(t, rec, &envelope)
	if len(envelope.Data) != 1 {
		t.Fatalf("kind filter returned %d runs want 1: %s", len(envelope.Data), rec.Body)
	}
	run := envelope.Data[0]
	if run["kind"] != domain.AnalysisKindFailure {
		t.Fatalf("kind=%v", run["kind"])
	}
	if got := requireStringField(t, run, "id"); got != "9201" {
		t.Fatalf("id=%q", got)
	}
	requireStringField(t, run, "versionId")
	requireStringField(t, run, "batchId")
}

// TestAnalysisListRejectsInvalidKind proves an unknown kind filter is 422.
func TestAnalysisListRejectsInvalidKind(t *testing.T) {
	h, _ := analysisServer(t, &fakeAnalysisService{list: func(context.Context, int64, int64) ([]domain.AnalysisRun, error) {
		t.Fatal("list must not be called for an invalid kind")
		return nil, nil
	}})
	rec := doRequest(h, http.MethodGet, "/api/versions/101/batches/7001/analysis?kind=summary", "ezt_user", "")
	requireError(t, rec, http.StatusUnprocessableEntity, "invalid_argument", "invalid_argument", false)
}

// TestAnalysisGet proves the detail view carries string ids and structured
// result/stats/errors.
func TestAnalysisGet(t *testing.T) {
	run := sampleAnalysisRun()
	var gotIDs [3]int64
	analyses := &fakeAnalysisService{get: func(_ context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error) {
		gotIDs = [3]int64{versionID, batchID, analysisID}
		return run, nil
	}}
	h, _ := analysisServer(t, analyses)
	rec := doRequest(h, http.MethodGet, "/api/versions/101/batches/7001/analysis/9201", "ezt_user", "")
	requireStatus(t, rec, http.StatusOK)
	if gotIDs != [3]int64{testVersionID, testBatchID, testAnalysisID} {
		t.Fatalf("ids=%v", gotIDs)
	}
	data := decodeData(t, rec)
	requireStringField(t, data, "id")
	requireStringField(t, data, "versionId")
	requireStringField(t, data, "batchId")
	if data["result"] == nil || data["stats"] == nil {
		t.Fatalf("result/stats missing: %v", data)
	}
	requireNoJSONField(t, data, "input")
	requireNoJSONField(t, data, "prompt")
}

// TestAnalysisGetMissingIs404 proves an unknown or foreign run is 404
// analysis_not_found.
func TestAnalysisGetMissingIs404(t *testing.T) {
	analyses := &fakeAnalysisService{get: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotFound(testAnalysisID)
	}}
	h, _ := analysisServer(t, analyses)
	rec := doRequest(h, http.MethodGet, "/api/versions/101/batches/7001/analysis/9201", "ezt_user", "")
	requireError(t, rec, http.StatusNotFound, "not_found", "analysis_not_found", false)
}

// TestAnalysisStart proves the manual failure start validates the body, the
// terminal batch and returns the created run with 202.
func TestAnalysisStart(t *testing.T) {
	run := sampleAnalysisRun()
	run.Status = domain.AnalysisQueued
	var gotVersionID, gotBatchID, gotActor int64
	analyses := &fakeAnalysisService{startFailure: func(_ context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error) {
		gotVersionID, gotBatchID, gotActor = versionID, batchID, actor
		return run, nil
	}}
	h, batches := analysisServer(t, analyses)

	rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
	requireStatus(t, rec, http.StatusAccepted)
	if gotVersionID != testVersionID || gotBatchID != testBatchID || gotActor != testActorID {
		t.Fatalf("args version=%d batch=%d actor=%d", gotVersionID, gotBatchID, gotActor)
	}
	if len(batches.calls) != 1 {
		t.Fatalf("batch gate calls=%v", batches.calls)
	}
	if data := decodeData(t, rec); data["status"] != domain.AnalysisQueued {
		t.Fatalf("status=%v", data["status"])
	}
}

// TestAnalysisStartRejectsNonTerminalBatch proves a running batch is 409
// analysis_not_applicable before the service is called.
func TestAnalysisStartRejectsNonTerminalBatch(t *testing.T) {
	running := terminalBatch(testVersionID, testBatchID)
	running.Status = executiondomain.BatchStatusRunning
	analyses := &fakeAnalysisService{startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
		t.Fatal("StartFailure must not be called for a non-terminal batch")
		return domain.AnalysisRun{}, nil
	}}
	h := newTestServer(t, HandlerDeps{Analyses: analyses, Batches: &fakeBatchReader{batch: running}})
	rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
	requireError(t, rec, http.StatusConflict, "conflict", "analysis_not_applicable", false)
}

// TestAnalysisStartRejectsUnknownOrForeignBatch proves an unknown batch or a
// batch of another version is 404.
func TestAnalysisStartRejectsUnknownOrForeignBatch(t *testing.T) {
	t.Run("unknown", func(t *testing.T) {
		h := newTestServer(t, HandlerDeps{
			Analyses: &fakeAnalysisService{},
			Batches:  &fakeBatchReader{err: notFoundBatch()},
		})
		rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
		requireError(t, rec, http.StatusNotFound, "not_found", "batch_not_found", false)
	})
	t.Run("foreign", func(t *testing.T) {
		foreign := terminalBatch(999, testBatchID)
		h := newTestServer(t, HandlerDeps{
			Analyses: &fakeAnalysisService{},
			Batches:  &fakeBatchReader{batch: foreign},
		})
		rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
		requireError(t, rec, http.StatusNotFound, "not_found", "batch_not_found", false)
	})
}

// TestAnalysisStartRejectsBadBody proves only the manual failure kind is
// accepted.
func TestAnalysisStartRejectsBadBody(t *testing.T) {
	h, _ := analysisServer(t, &fakeAnalysisService{startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
		t.Fatal("StartFailure must not be called for an invalid body")
		return domain.AnalysisRun{}, nil
	}})
	for name, body := range map[string]string{
		"missing kind": `{}`,
		"triage kind":  `{"kind":"issue_triage"}`,
		"unknown kind": `{"kind":"summary"}`,
		"malformed":    `{"kind":`,
	} {
		t.Run(name, func(t *testing.T) {
			rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", body)
			requireStatus(t, rec, http.StatusUnprocessableEntity)
		})
	}
}

// TestAnalysisStartMapsServiceConflicts proves analysis_active and
// analysis_not_applicable map to 409 with their stable reasons.
func TestAnalysisStartMapsServiceConflicts(t *testing.T) {
	active := &fakeAnalysisService{startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
		return domain.AnalysisRun{}, domain.NewAnalysisConflict(domain.ReasonAnalysisActive, "already active")
	}}
	h, _ := analysisServer(t, active)
	rec := doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
	requireError(t, rec, http.StatusConflict, "conflict", "analysis_active", false)

	notApplicable := &fakeAnalysisService{startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable("no failed application case")
	}}
	h, _ = analysisServer(t, notApplicable)
	rec = doRequest(h, http.MethodPost, "/api/versions/101/batches/7001/analysis", "ezt_user", `{"kind":"failure"}`)
	requireError(t, rec, http.StatusConflict, "conflict", "analysis_not_applicable", false)
}

// TestAnalysisRoutesAllowRegularUsers proves delivery analysis routes have no
// admin guard (spec §11.1).
func TestAnalysisRoutesAllowRegularUsers(t *testing.T) {
	run := sampleAnalysisRun()
	analyses := &fakeAnalysisService{
		list: func(context.Context, int64, int64) ([]domain.AnalysisRun, error) {
			return []domain.AnalysisRun{run}, nil
		},
		get: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) { return run, nil },
		startFailure: func(context.Context, int64, int64, int64) (domain.AnalysisRun, error) {
			return run, nil
		},
	}
	h, _ := analysisServer(t, analyses)
	for _, request := range []struct{ method, path, body string }{
		{http.MethodGet, "/api/versions/101/batches/7001/analysis", ""},
		{http.MethodGet, "/api/versions/101/batches/7001/analysis/9201", ""},
		{http.MethodPost, "/api/versions/101/batches/7001/analysis", `{"kind":"failure"}`},
	} {
		rec := doRequest(h, request.method, request.path, "ezt_user", request.body)
		if rec.Code == http.StatusForbidden {
			t.Fatalf("%s %s returned 403 for a regular user: %s", request.method, request.path, rec.Body)
		}
	}
}
