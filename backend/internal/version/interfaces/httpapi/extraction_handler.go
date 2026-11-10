package versionapi

// Task 18 (v0.22 A-7/A-8, spec API-5/API-6/API-7, R-A7-9/R-A7-10):
// requirement-extraction progress, trigger and pause/resume/retry surface.
//
//	GET  /api/versions/{id}/requirements/extractions
//	POST /api/versions/{id}/requirements/extractions
//	POST /api/versions/{id}/requirements/extractions/{runID}/pause
//	POST /api/versions/{id}/requirements/extractions/{runID}/resume
//	POST /api/versions/{id}/requirements/extractions/{runID}/retry
//
// The handler only parses/renders and maps typed application errors
// (ErrExtractionRunNotFound/…State -> 404/409, platform errors pass through).
// The plan/fact milestone (R-A7-10) is owned by the application service and is
// already applied by Start/ProcessRun; the handler never writes plan/fact rows
// directly. Route registration and OpenAPI stay with Task 37.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// ExtractionService is the requirement-extraction application surface the
// progress API needs. *versionapp.ExtractionService implements it.
type ExtractionService interface {
	GetRun(ctx context.Context, runID int64) (versionapp.ExtractionRun, error)
	ListRuns(ctx context.Context, versionID int64) ([]versionapp.ExtractionRun, error)
	LatestRun(ctx context.Context, versionID, attachmentID int64) (versionapp.ExtractionRun, error)
	ListSegments(ctx context.Context, runID int64) ([]versionapp.ExtractionSegment, error)
	StartVersion(ctx context.Context, versionID, attachmentID int64) ([]versionapp.ExtractionRun, error)
	Pause(ctx context.Context, runID int64) (versionapp.ExtractionRun, error)
	Resume(ctx context.Context, runID int64) (versionapp.ExtractionRun, error)
	RetryRun(ctx context.Context, runID int64) (versionapp.ExtractionRun, error)
	RetrySegment(ctx context.Context, runID int64, seq int) (versionapp.ExtractionRun, error)
}

var _ ExtractionService = (*versionapp.ExtractionService)(nil)

// ExtractionHandler serves the requirement-extraction routes. The server mounts
// it behind the shared bearer-authentication middleware (Task 37); the handler
// enforces the authenticated actor like the sibling mutating handlers.
type ExtractionHandler struct {
	svc ExtractionService
}

// NewExtractionHandler builds the extraction handler.
func NewExtractionHandler(svc ExtractionService) *ExtractionHandler {
	return &ExtractionHandler{svc: svc}
}

// Get GET /api/versions/{id}/requirements/extractions.
//
// Returns the latest run of the version (newest id), or the run selected by
// `?run_id=` / the newest run of `?attachment_id=`. The body is one run
// projection with the persisted reconciliation report flattened into
// expected/extracted/missing/missing_ids/planned_count/planned_ids/
// low_confidence/skipped_images plus the segment cursor and coverage ratio.
// A version without any run and no explicit selector is an empty read: 200
// with a null data body (G-09). An explicit `?run_id=`/`?attachment_id=` that
// does not resolve, or a run of another version, stays a typed 404.
func (h *ExtractionHandler) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.svc == nil {
		httpx.Error(w, r, errors.New("requirement extraction service is not configured"))
		return
	}

	query := r.URL.Query()
	var run versionapp.ExtractionRun
	switch {
	case strings.TrimSpace(query.Get("run_id")) != "":
		runID, parseErr := validation.NonEmptyID("run_id", query.Get("run_id"))
		if parseErr != nil {
			httpx.Error(w, r, parseErr)
			return
		}
		run, err = h.svc.GetRun(r.Context(), runID)
	case strings.TrimSpace(query.Get("attachment_id")) != "":
		attachmentID, parseErr := validation.NonEmptyID("attachment_id", query.Get("attachment_id"))
		if parseErr != nil {
			httpx.Error(w, r, parseErr)
			return
		}
		run, err = h.svc.LatestRun(r.Context(), versionID, attachmentID)
	default:
		runs, listErr := h.svc.ListRuns(r.Context(), versionID)
		if listErr != nil {
			httpx.Error(w, r, mapExtractionHTTPError(listErr))
			return
		}
		if len(runs) == 0 {
			// Default "latest for the version" with nothing recorded is an
			// empty read: 200 with a null data body (G-09), so the browser
			// network layer records no 404. Explicit selectors never reach
			// this branch and keep their typed 404.
			httpx.JSON(w, http.StatusOK, nil)
			return
		}
		run = runs[len(runs)-1]
	}
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return
	}
	if run.VersionID != versionID {
		httpx.Error(w, r, platform.NotFound("requirement extraction run"))
		return
	}
	h.writeRun(w, r, run)
}

// Start POST /api/versions/{id}/requirements/extractions.
//
// Body (optional): {"attachment_id":"<decimal>"}. A missing/empty attachment_id
// means every ready attachment of the version. The response is {"runs":[...]}
// with every started/replayed run; the operation is idempotent by
// (attachment_id, input_sha256), so replaying "重新抽取" never duplicates a run.
func (h *ExtractionHandler) Start(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.svc == nil {
		httpx.Error(w, r, errors.New("requirement extraction service is not configured"))
		return
	}
	var request extractionStartRequest
	if err := decodeOptionalExtractionBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	var attachmentID int64
	if strings.TrimSpace(request.AttachmentID) != "" {
		attachmentID, err = validation.NonEmptyID("attachment_id", request.AttachmentID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	runs, err := h.svc.StartVersion(r.Context(), versionID, attachmentID)
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return
	}
	dto := extractionStartResultDTO{Runs: make([]extractionRunDTO, 0, len(runs))}
	for _, run := range runs {
		dto.Runs = append(dto.Runs, toExtractionRunDTO(run, nil))
	}
	httpx.JSON(w, http.StatusOK, dto)
}

// Pause POST /api/versions/{id}/requirements/extractions/{runID}/pause.
func (h *ExtractionHandler) Pause(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "pause")
}

// Resume POST /api/versions/{id}/requirements/extractions/{runID}/resume.
func (h *ExtractionHandler) Resume(w http.ResponseWriter, r *http.Request) {
	h.transition(w, r, "resume")
}

// Retry POST /api/versions/{id}/requirements/extractions/{runID}/retry.
//
// An optional body {"segment_seq":<n>} retries one failed segment (and a failed
// run); an empty body retries the whole failed run (fresh attempt budget for
// every failed segment).
func (h *ExtractionHandler) Retry(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, runID, ok := h.runPath(w, r)
	if !ok {
		return
	}
	var request extractionRetryRequest
	if err := decodeOptionalExtractionBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if request.SegmentSeq != nil && *request.SegmentSeq <= 0 {
		httpx.Error(w, r, platform.Invalid("segment_seq", "must be a positive integer"))
		return
	}
	if _, ok := h.requireRunOfVersion(w, r, versionID, runID); !ok {
		return
	}
	var (
		run versionapp.ExtractionRun
		err error
	)
	if request.SegmentSeq != nil {
		run, err = h.svc.RetrySegment(r.Context(), runID, *request.SegmentSeq)
	} else {
		run, err = h.svc.RetryRun(r.Context(), runID)
	}
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return
	}
	h.writeRun(w, r, run)
}

// transition implements pause/resume: parse, verify run ownership, apply the
// typed application transition and render the refreshed run.
func (h *ExtractionHandler) transition(w http.ResponseWriter, r *http.Request, action string) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, runID, ok := h.runPath(w, r)
	if !ok {
		return
	}
	if _, ok := h.requireRunOfVersion(w, r, versionID, runID); !ok {
		return
	}
	var (
		run versionapp.ExtractionRun
		err error
	)
	switch action {
	case "pause":
		run, err = h.svc.Pause(r.Context(), runID)
	case "resume":
		run, err = h.svc.Resume(r.Context(), runID)
	default:
		httpx.Error(w, r, errors.New("unknown extraction action"))
		return
	}
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return
	}
	h.writeRun(w, r, run)
}

// runPath parses the version and run ids of a run action.
func (h *ExtractionHandler) runPath(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return 0, 0, false
	}
	runID, err := validation.NonEmptyID("runID", r.PathValue("runID"))
	if err != nil {
		httpx.Error(w, r, err)
		return 0, 0, false
	}
	if h.svc == nil {
		httpx.Error(w, r, errors.New("requirement extraction service is not configured"))
		return 0, 0, false
	}
	return versionID, runID, true
}

// requireRunOfVersion rejects a run id that belongs to another version with the
// same typed 404 as an unknown run, so the API never leaks cross-version cursors.
func (h *ExtractionHandler) requireRunOfVersion(w http.ResponseWriter, r *http.Request, versionID, runID int64) (versionapp.ExtractionRun, bool) {
	run, err := h.svc.GetRun(r.Context(), runID)
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return versionapp.ExtractionRun{}, false
	}
	if run.VersionID != versionID {
		httpx.Error(w, r, platform.NotFound("requirement extraction run"))
		return versionapp.ExtractionRun{}, false
	}
	return run, true
}

// writeRun renders one run plus its segment cursor.
func (h *ExtractionHandler) writeRun(w http.ResponseWriter, r *http.Request, run versionapp.ExtractionRun) {
	segments, err := h.svc.ListSegments(r.Context(), run.ID)
	if err != nil {
		httpx.Error(w, r, mapExtractionHTTPError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toExtractionRunDTO(run, segments))
}

// mapExtractionHTTPError maps the extraction sentinels to the API contract.
// Platform errors (validation, unavailable, not found) pass through unchanged.
func mapExtractionHTTPError(err error) error {
	switch {
	case errors.Is(err, versionapp.ErrExtractionRunNotFound):
		return platform.NotFound("requirement extraction run")
	case errors.Is(err, versionapp.ErrExtractionSegmentNotFound):
		return platform.NotFound("requirement extraction segment")
	case errors.Is(err, versionapp.ErrExtractionRunState):
		return platform.StateConflict("requirement extraction run state conflict")
	case errors.Is(err, versionapp.ErrExtractionSegmentState):
		return platform.StateConflict("requirement extraction segment state conflict")
	default:
		return err
	}
}

// decodeOptionalExtractionBody accepts an absent/empty body (all running
// actions are body-less) and rejects malformed JSON with the typed 400.
func decodeOptionalExtractionBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil {
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}

// ---------- DTOs ----------

type extractionStartRequest struct {
	AttachmentID string `json:"attachment_id"`
}

type extractionRetryRequest struct {
	SegmentSeq *int `json:"segment_seq"`
}

// extractionStartResultDTO is the POST response: every started or replayed run
// (one entry for an attachment_id start, up to one per ready attachment for the
// version-wide start).
type extractionStartResultDTO struct {
	Runs []extractionRunDTO `json:"runs"`
}

// extractionRunDTO is the progress projection of one requirement_extraction_run.
// expected/extracted/missing/planned_count/low_confidence/skipped_images come
// from the persisted reconciliation report (or the run counts before a report
// exists); ids are decimal strings (Agent.md §6.7); coverage is the
// extracted/expected ratio (segment ratio before expected is known).
type extractionRunDTO struct {
	ID             string                 `json:"id"`
	VersionID      string                 `json:"version_id"`
	AttachmentID   string                 `json:"attachment_id"`
	InputSHA256    string                 `json:"input_sha256"`
	Status         string                 `json:"status"`
	TotalSegments  int                    `json:"total_segments"`
	DoneSegments   int                    `json:"done_segments"`
	FailedSegments int                    `json:"failed_segments"`
	Expected       int                    `json:"expected"`
	Extracted      int                    `json:"extracted"`
	Missing        int                    `json:"missing"`
	MissingIDs     []string               `json:"missing_ids"`
	LowConfidence  int                    `json:"low_confidence"`
	SkippedImages  int                    `json:"skipped_images"`
	PlannedCount   int                    `json:"planned_count"`
	PlannedIDs     []string               `json:"planned_ids"`
	Coverage       float64                `json:"coverage"`
	Error          string                 `json:"error,omitempty"`
	CreatedAt      string                 `json:"created_at"`
	UpdatedAt      string                 `json:"updated_at"`
	Segments       []extractionSegmentDTO `json:"segments"`
}

type extractionSegmentDTO struct {
	ID             string  `json:"id"`
	RunID          string  `json:"run_id"`
	Seq            int     `json:"seq"`
	HeadingPath    string  `json:"heading_path"`
	CharStart      int     `json:"char_start"`
	CharEnd        int     `json:"char_end"`
	TextSHA256     string  `json:"text_sha256"`
	Status         string  `json:"status"`
	Attempts       int     `json:"attempts"`
	ExtractedCount int     `json:"extracted_count"`
	Error          string  `json:"error,omitempty"`
	RenewedAt      *string `json:"renewed_at"`
}

func toExtractionRunDTO(run versionapp.ExtractionRun, segments []versionapp.ExtractionSegment) extractionRunDTO {
	dto := extractionRunDTO{
		ID:             formatDecimal(run.ID),
		VersionID:      formatDecimal(run.VersionID),
		AttachmentID:   formatDecimal(run.AttachmentID),
		InputSHA256:    run.InputSHA256,
		Status:         string(run.Status),
		TotalSegments:  run.TotalSegments,
		DoneSegments:   run.DoneSegments,
		FailedSegments: run.FailedSegments,
		Extracted:      run.ExtractedCount,
		MissingIDs:     []string{},
		PlannedIDs:     []string{},
		Error:          run.Error,
		CreatedAt:      formatTime(run.CreatedAt),
		UpdatedAt:      formatTime(run.UpdatedAt),
		Segments:       make([]extractionSegmentDTO, 0, len(segments)),
	}
	if run.ExpectedCount != nil {
		dto.Expected = *run.ExpectedCount
	}
	if report := run.Report; report != nil {
		dto.Expected = report.Expected
		dto.Extracted = report.Extracted
		dto.Missing = report.Missing
		dto.LowConfidence = report.LowConfidence
		dto.SkippedImages = report.SkippedImages
		dto.PlannedCount = report.PlannedCount
		if report.MissingIDs != nil {
			dto.MissingIDs = report.MissingIDs
		}
		if report.PlannedIDs != nil {
			dto.PlannedIDs = report.PlannedIDs
		}
	}
	dto.Coverage = extractionCoverage(dto.Expected, dto.Extracted, run.TotalSegments, run.DoneSegments)
	for _, segment := range segments {
		dto.Segments = append(dto.Segments, extractionSegmentDTO{
			ID:             formatDecimal(segment.ID),
			RunID:          formatDecimal(segment.RunID),
			Seq:            segment.Seq,
			HeadingPath:    segment.HeadingPath,
			CharStart:      segment.CharStart,
			CharEnd:        segment.CharEnd,
			TextSHA256:     segment.TextSHA256,
			Status:         string(segment.Status),
			Attempts:       segment.Attempts,
			ExtractedCount: segment.ExtractedCount,
			Error:          segment.Error,
			RenewedAt:      formatOptionalTime(segment.RenewedAt),
		})
	}
	return dto
}

// extractionCoverage is the UI ratio: extracted/expected once the reconciliation
// expected count is known, done/total segments before that. It is clamped to
// [0,1] so a stale/over-counted run never renders an impossible progress bar.
func extractionCoverage(expected, extracted, total, done int) float64 {
	var ratio float64
	switch {
	case expected > 0:
		ratio = float64(extracted) / float64(expected)
	case total > 0:
		ratio = float64(done) / float64(total)
	default:
		return 0
	}
	if ratio < 0 {
		return 0
	}
	if ratio > 1 {
		return 1
	}
	return ratio
}
