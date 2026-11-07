package versionapi_test

// Task 18 (v0.22 A-7/A-8, R-A7-9/R-A7-10, API-5/API-6/API-7): TDD contract for
// the requirement extraction progress API.
//
//   - GET /api/versions/{id}/requirements/extractions returns the latest run
//     with expected/extracted/missing/planned_count/low_confidence/
//     skipped_images/segments/status and is a typed 404 when no run exists;
//   - POST starts/replays one attachment (attachment_id) or every ready
//     attachment of the version;
//   - pause/resume/retry are typed operations (400 bad input, 404 unknown or
//     foreign run, 409 wrong run/segment state) and require an authenticated
//     actor like every other mutating handler;
//   - route registration stays with Task 37, so the tests mount the handler
//     themselves.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// fakeExtractionService is the HTTP-layer double: it pins what the handler
// parses and renders without a database or an LLM.
type fakeExtractionService struct {
	run     versionapp.ExtractionRun
	runErr  error
	runs    []versionapp.ExtractionRun
	listErr error

	latestRun versionapp.ExtractionRun
	latestErr error

	segments    []versionapp.ExtractionSegment
	segmentsErr error

	startRuns []versionapp.ExtractionRun
	startErr  error

	pauseRun  versionapp.ExtractionRun
	pauseErr  error
	resumeRun versionapp.ExtractionRun
	resumeErr error
	retryRun  versionapp.ExtractionRun
	retryErr  error

	calls            []string
	lastVersionID    int64
	lastAttachmentID int64
	lastRunID        int64
	lastSeq          int
}

var _ versionapi.ExtractionService = (*fakeExtractionService)(nil)

func (s *fakeExtractionService) GetRun(_ context.Context, runID int64) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "get_run")
	s.lastRunID = runID
	if s.runErr != nil {
		return versionapp.ExtractionRun{}, s.runErr
	}
	return s.run, nil
}

func (s *fakeExtractionService) ListRuns(_ context.Context, versionID int64) ([]versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "list_runs")
	s.lastVersionID = versionID
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.runs, nil
}

func (s *fakeExtractionService) LatestRun(_ context.Context, versionID, attachmentID int64) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "latest_run")
	s.lastVersionID = versionID
	s.lastAttachmentID = attachmentID
	if s.latestErr != nil {
		return versionapp.ExtractionRun{}, s.latestErr
	}
	return s.latestRun, nil
}

func (s *fakeExtractionService) ListSegments(_ context.Context, runID int64) ([]versionapp.ExtractionSegment, error) {
	s.calls = append(s.calls, "list_segments")
	s.lastRunID = runID
	if s.segmentsErr != nil {
		return nil, s.segmentsErr
	}
	return s.segments, nil
}

func (s *fakeExtractionService) StartVersion(_ context.Context, versionID, attachmentID int64) ([]versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "start_version")
	s.lastVersionID = versionID
	s.lastAttachmentID = attachmentID
	if s.startErr != nil {
		return nil, s.startErr
	}
	return s.startRuns, nil
}

func (s *fakeExtractionService) Pause(_ context.Context, runID int64) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "pause")
	s.lastRunID = runID
	if s.pauseErr != nil {
		return versionapp.ExtractionRun{}, s.pauseErr
	}
	return s.pauseRun, nil
}

func (s *fakeExtractionService) Resume(_ context.Context, runID int64) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "resume")
	s.lastRunID = runID
	if s.resumeErr != nil {
		return versionapp.ExtractionRun{}, s.resumeErr
	}
	return s.resumeRun, nil
}

func (s *fakeExtractionService) RetryRun(_ context.Context, runID int64) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "retry_run")
	s.lastRunID = runID
	if s.retryErr != nil {
		return versionapp.ExtractionRun{}, s.retryErr
	}
	return s.retryRun, nil
}

func (s *fakeExtractionService) RetrySegment(_ context.Context, runID int64, seq int) (versionapp.ExtractionRun, error) {
	s.calls = append(s.calls, "retry_segment")
	s.lastRunID = runID
	s.lastSeq = seq
	if s.retryErr != nil {
		return versionapp.ExtractionRun{}, s.retryErr
	}
	return s.retryRun, nil
}

func newExtractionMux(svc versionapi.ExtractionService) http.Handler {
	handler := versionapi.NewExtractionHandler(svc)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/requirements/extractions", handler.Get)
	mux.HandleFunc("POST /api/versions/{id}/requirements/extractions", handler.Start)
	mux.HandleFunc("POST /api/versions/{id}/requirements/extractions/{runID}/pause", handler.Pause)
	mux.HandleFunc("POST /api/versions/{id}/requirements/extractions/{runID}/resume", handler.Resume)
	mux.HandleFunc("POST /api/versions/{id}/requirements/extractions/{runID}/retry", handler.Retry)
	return mux
}

func extractionDoneRun() versionapp.ExtractionRun {
	expected := 58
	return versionapp.ExtractionRun{
		ID:             9001,
		VersionID:      7,
		AttachmentID:   8001,
		InputSHA256:    "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		Status:         versionapp.ExtractionRunDone,
		TotalSegments:  2,
		DoneSegments:   2,
		FailedSegments: 0,
		ExpectedCount:  &expected,
		ExtractedCount: 58,
		CreatedAt:      testTime,
		UpdatedAt:      testTime,
		Report: &versionapp.ReconciliationReport{
			Expected:      58,
			Extracted:     58,
			Missing:       1,
			MissingIDs:    []string{"FR-003"},
			LowConfidence: 2,
			SkippedImages: 1,
			PlannedCount:  32,
			PlannedIDs:    []string{"FR-059", "FR-060"},
			CurrentIDs:    []string{"FR-001"},
		},
	}
}

// TestExtractionAPIProgress pins the GET contract of API-5: the latest run is
// returned with the full progress/reconciliation projection, latest-by-attachment
// and by-run selection work, and a missing run is a typed 404.
func TestExtractionAPIProgress(t *testing.T) {
	run := extractionDoneRun()
	svc := &fakeExtractionService{
		run:  run,
		runs: []versionapp.ExtractionRun{run},
		segments: []versionapp.ExtractionSegment{
			{ID: 1, RunID: run.ID, Seq: 1, HeadingPath: "需求", Status: versionapp.ExtractionSegmentDone, ExtractedCount: 40},
			{ID: 2, RunID: run.ID, Seq: 2, HeadingPath: "规划", Status: versionapp.ExtractionSegmentDone, ExtractedCount: 18},
		},
	}
	mux := newExtractionMux(svc)

	recorder := call(t, mux, http.MethodGet, "/api/versions/7/requirements/extractions", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data struct {
			ID            string   `json:"id"`
			VersionID     string   `json:"version_id"`
			AttachmentID  string   `json:"attachment_id"`
			Status        string   `json:"status"`
			TotalSegments int      `json:"total_segments"`
			DoneSegments  int      `json:"done_segments"`
			Expected      int      `json:"expected"`
			Extracted     int      `json:"extracted"`
			Missing       int      `json:"missing"`
			MissingIDs    []string `json:"missing_ids"`
			LowConfidence int      `json:"low_confidence"`
			SkippedImages int      `json:"skipped_images"`
			PlannedCount  int      `json:"planned_count"`
			PlannedIDs    []string `json:"planned_ids"`
			Coverage      float64  `json:"coverage"`
			Segments      []struct {
				Seq    int    `json:"seq"`
				Status string `json:"status"`
			} `json:"segments"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET body %q: %v", recorder.Body.String(), err)
	}
	data := body.Data
	if data.Expected != 58 || data.Extracted != 58 {
		t.Fatalf("expected/extracted = %d/%d, want 58/58", data.Expected, data.Extracted)
	}
	if data.Missing != 1 || len(data.MissingIDs) != 1 || data.MissingIDs[0] != "FR-003" {
		t.Fatalf("missing = %d %v, want 1 [FR-003]", data.Missing, data.MissingIDs)
	}
	if data.PlannedCount != 32 || len(data.PlannedIDs) != 2 || data.PlannedIDs[0] != "FR-059" {
		t.Fatalf("planned = %d %v, want 32 [FR-059 FR-060]", data.PlannedCount, data.PlannedIDs)
	}
	if data.LowConfidence != 2 || data.SkippedImages != 1 {
		t.Fatalf("low/skipped = %d/%d, want 2/1", data.LowConfidence, data.SkippedImages)
	}
	if data.Coverage != 1 {
		t.Fatalf("coverage = %v, want 1 (58/58)", data.Coverage)
	}
	if data.Status != "done" || data.TotalSegments != 2 || data.DoneSegments != 2 {
		t.Fatalf("status/segments = %q %d/%d", data.Status, data.DoneSegments, data.TotalSegments)
	}
	if len(data.Segments) != 2 || data.Segments[1].Seq != 2 || data.Segments[1].Status != "done" {
		t.Fatalf("segments = %#v", data.Segments)
	}
	if data.ID != "9001" || data.VersionID != "7" || data.AttachmentID != "8001" {
		t.Fatalf("ids = %q/%q/%q", data.ID, data.VersionID, data.AttachmentID)
	}

	// Latest-by-attachment selection.
	svc.latestRun = run
	recorder = call(t, mux, http.MethodGet,
		"/api/versions/7/requirements/extractions?attachment_id=8001", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET by attachment status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if svc.lastAttachmentID != 8001 || svc.lastVersionID != 7 {
		t.Fatalf("latest args = %d/%d, want 7/8001", svc.lastVersionID, svc.lastAttachmentID)
	}

	// Missing run -> typed 404.
	svc.runs = nil
	recorder = call(t, mux, http.MethodGet, "/api/versions/7/requirements/extractions", "", actor())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("empty GET status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if code := errorBody(t, recorder)["code"]; code != "not_found" {
		t.Fatalf("empty GET code = %#v", code)
	}
}

// TestExtractionAPIRejectsBadRequests pins the 400/404 boundaries of the read
// path: ids must be positive decimals and a run of another version is invisible.
func TestExtractionAPIRejectsBadRequests(t *testing.T) {
	run := extractionDoneRun()
	svc := &fakeExtractionService{run: run, runs: []versionapp.ExtractionRun{run}}
	mux := newExtractionMux(svc)

	for _, path := range []string{
		"/api/versions/not-a-number/requirements/extractions",
		"/api/versions/7/requirements/extractions?attachment_id=abc",
		"/api/versions/7/requirements/extractions?run_id=0",
	} {
		recorder := call(t, mux, http.MethodGet, path, "", actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("GET %s status = %d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}

	recorder := call(t, mux, http.MethodGet, "/api/versions/8/requirements/extractions?run_id=9001", "", actor())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign run status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	svc.latestErr = versionapp.ErrExtractionRunNotFound
	recorder = call(t, mux, http.MethodGet,
		"/api/versions/7/requirements/extractions?attachment_id=8001", "", actor())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("missing latest status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestExtractionAPIStart pins API-6: one attachment or every ready attachment
// can be triggered, the actor is required and typed service failures map to
// their HTTP status.
func TestExtractionAPIStart(t *testing.T) {
	run := extractionDoneRun()
	run.Status = versionapp.ExtractionRunPending
	svc := &fakeExtractionService{startRuns: []versionapp.ExtractionRun{run}}
	mux := newExtractionMux(svc)

	recorder := call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions", `{"attachment_id":"8001"}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if svc.lastVersionID != 7 || svc.lastAttachmentID != 8001 {
		t.Fatalf("start args = %d/%d, want 7/8001", svc.lastVersionID, svc.lastAttachmentID)
	}
	var body struct {
		Data struct {
			Runs []struct {
				ID     string `json:"id"`
				Status string `json:"status"`
			} `json:"runs"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode start body %q: %v", recorder.Body.String(), err)
	}
	if len(body.Data.Runs) != 1 || body.Data.Runs[0].ID != "9001" || body.Data.Runs[0].Status != "pending" {
		t.Fatalf("start runs = %#v", body.Data.Runs)
	}

	// Empty body means "every ready attachment of the version".
	recorder = call(t, mux, http.MethodPost, "/api/versions/7/requirements/extractions", "", actor())
	if recorder.Code != http.StatusOK || svc.lastAttachmentID != 0 {
		t.Fatalf("start-all status/attachment = %d/%d", recorder.Code, svc.lastAttachmentID)
	}

	// Actor required.
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions", `{"attachment_id":"8001"}`, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("actorless start status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Invalid body / ids are 400.
	for _, tc := range []struct{ body, path string }{
		{body: `{"attachment_id":"abc"}`, path: "/api/versions/7/requirements/extractions"},
		{body: `{"attachment_id":`, path: "/api/versions/7/requirements/extractions"},
		{body: `{}`, path: "/api/versions/x/requirements/extractions"},
	} {
		recorder = call(t, mux, http.MethodPost, tc.path, tc.body, actor())
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("start %q %q status = %d body=%s", tc.path, tc.body, recorder.Code, recorder.Body.String())
		}
	}

	// Typed service failures.
	svc.startErr = versionapp.ErrExtractionRunState
	recorder = call(t, mux, http.MethodPost, "/api/versions/7/requirements/extractions", `{}`, actor())
	if recorder.Code != http.StatusConflict {
		t.Fatalf("state start status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	svc.startErr = platform.Unavailable("requirement extraction input provider is not configured")
	recorder = call(t, mux, http.MethodPost, "/api/versions/7/requirements/extractions", `{}`, actor())
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unavailable start status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestExtractionAPIActions pins pause/resume/retry: authenticated typed actions,
// optional segment-level retry, wrong-state 409, unknown/foreign run 404 and bad
// ids 400.
func TestExtractionAPIActions(t *testing.T) {
	run := extractionDoneRun()
	svc := &fakeExtractionService{
		run:       run,
		pauseRun:  withExtractionStatus(run, versionapp.ExtractionRunPaused),
		resumeRun: withExtractionStatus(run, versionapp.ExtractionRunPending),
		retryRun:  withExtractionStatus(run, versionapp.ExtractionRunPending),
	}
	mux := newExtractionMux(svc)

	recorder := call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/pause", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("pause status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if svc.lastRunID != 9001 {
		t.Fatalf("pause run = %d", svc.lastRunID)
	}
	var body struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode pause: %v", err)
	}
	if body.Data.Status != "paused" {
		t.Fatalf("pause status = %q", body.Data.Status)
	}

	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/resume", "", actor())
	if recorder.Code != http.StatusOK || !fakeServiceCalled(svc, "resume") {
		t.Fatalf("resume status = %d calls=%v", recorder.Code, svc.calls)
	}

	// Wrong state -> 409.
	svc.pauseErr = versionapp.ErrExtractionRunState
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/pause", "", actor())
	if recorder.Code != http.StatusConflict {
		t.Fatalf("pause state status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if code := errorBody(t, recorder)["code"]; code != "state_conflict" {
		t.Fatalf("pause state code = %#v", code)
	}

	// Missing -> 404.
	svc.resumeErr = versionapp.ErrExtractionRunNotFound
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/resume", "", actor())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("resume missing status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Run-level retry.
	svc.retryErr = nil
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/retry", "", actor())
	if recorder.Code != http.StatusOK || !fakeServiceCalled(svc, "retry_run") {
		t.Fatalf("retry status = %d calls=%v", recorder.Code, svc.calls)
	}

	// Segment-level retry.
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/retry", `{"segment_seq":2}`, actor())
	if recorder.Code != http.StatusOK || !fakeServiceCalled(svc, "retry_segment") || svc.lastSeq != 2 {
		t.Fatalf("segment retry status=%d seq=%d", recorder.Code, svc.lastSeq)
	}
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/retry", `{"segment_seq":0}`, actor())
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("segment retry 0 status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Actor required, bad ids 400, foreign run 404.
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/pause", "", nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("actorless pause status = %d", recorder.Code)
	}
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/not-a-run/pause", "", actor())
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("bad run id status = %d", recorder.Code)
	}
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/8/requirements/extractions/9001/pause", "", actor())
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("foreign pause status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// A typed segment-state failure maps to 409.
	svc.retryErr = versionapp.ErrExtractionSegmentState
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/retry", `{"segment_seq":2}`, actor())
	if recorder.Code != http.StatusConflict {
		t.Fatalf("segment state status = %d body=%s", recorder.Code, recorder.Body.String())
	}

	// Unknown (non-typed) errors stay internal 500.
	svc.resumeErr = errors.New("boom")
	recorder = call(t, mux, http.MethodPost,
		"/api/versions/7/requirements/extractions/9001/resume", "", actor())
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected error status = %d body=%s", recorder.Code, recorder.Body.String())
	}
}

// TestExtractionAPILatestRunMultiAttachment pins the "latest run of the
// version" read when several attachments have runs: GET without selectors
// returns the newest run id, while ?attachment_id= selects that attachment's
// latest run.
func TestExtractionAPILatestRunMultiAttachment(t *testing.T) {
	older := extractionDoneRun()
	older.ID = 9001
	older.AttachmentID = 8001
	newer := extractionDoneRun()
	newer.ID = 9002
	newer.AttachmentID = 8002
	newer.Status = versionapp.ExtractionRunRunning
	svc := &fakeExtractionService{
		run:       older,
		runs:      []versionapp.ExtractionRun{older, newer},
		latestRun: older,
	}
	mux := newExtractionMux(svc)

	recorder := call(t, mux, http.MethodGet, "/api/versions/7/requirements/extractions", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET latest status = %d body=%s", recorder.Code, recorder.Body.String())
	}
	if !fakeServiceCalled(svc, "list_runs") {
		t.Fatalf("GET latest calls = %v, want list_runs", svc.calls)
	}
	var body struct {
		Data struct {
			ID           string `json:"id"`
			AttachmentID string `json:"attachment_id"`
			Status       string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode latest body %q: %v", recorder.Body.String(), err)
	}
	if body.Data.ID != "9002" || body.Data.AttachmentID != "8002" || body.Data.Status != "running" {
		t.Fatalf("latest run = %#v, want the newest run 9002 of attachment 8002", body.Data)
	}

	// The attachment selector still returns that attachment's latest run, not
	// the version's newest one.
	recorder = call(t, mux, http.MethodGet,
		"/api/versions/7/requirements/extractions?attachment_id=8001", "", actor())
	if recorder.Code != http.StatusOK || !fakeServiceCalled(svc, "latest_run") {
		t.Fatalf("GET by attachment status = %d calls=%v", recorder.Code, svc.calls)
	}
	if svc.lastVersionID != 7 || svc.lastAttachmentID != 8001 {
		t.Fatalf("latest args = %d/%d, want 7/8001", svc.lastVersionID, svc.lastAttachmentID)
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode attachment body %q: %v", recorder.Body.String(), err)
	}
	if body.Data.ID != "9001" || body.Data.AttachmentID != "8001" {
		t.Fatalf("attachment run = %#v, want 9001", body.Data)
	}
}

func withExtractionStatus(run versionapp.ExtractionRun, status versionapp.ExtractionRunStatus) versionapp.ExtractionRun {
	run.Status = status
	return run
}

func fakeServiceCalled(svc *fakeExtractionService, call string) bool {
	for _, recorded := range svc.calls {
		if recorded == call {
			return true
		}
	}
	return false
}
