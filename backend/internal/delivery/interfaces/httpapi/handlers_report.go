package httpapi

import (
	"context"
	"net/http"
	"strconv"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// ReportHandler serves the version-scoped report endpoints of spec §7.4. It
// only calls the report application service; generation, stale detection and
// export verification live there.
type ReportHandler struct {
	reports ReportService
	batches BatchReader
}

// RegisterRoutes mounts the report routes. Authentication is the server's global
// middleware: the report is a version deliverable readable by any logged-in user
// (spec §11.1). The ensure route is mounted only when the batch reader is
// wired, because the batch ownership gate must never be silently skipped; the
// read/export routes stay available either way.
func (h *ReportHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/reports", h.List)
	if h.batches != nil {
		mux.HandleFunc("POST /api/versions/{id}/reports", h.Ensure)
	}
	mux.HandleFunc("GET /api/versions/{id}/reports/{reportId}", h.Get)
	mux.HandleFunc("POST /api/versions/{id}/reports/{reportId}/publish", h.Publish)
	mux.HandleFunc("POST /api/versions/{id}/reports/{reportId}/regenerate", h.Regenerate)
	mux.HandleFunc("GET /api/versions/{id}/reports/{reportId}/export", h.Export)
	mux.HandleFunc("GET /api/versions/{id}/reports/{reportId}/diff", h.Diff)
}

// List returns one page of the version's reports without their content.
func (h *ReportHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	filter := deliveryapp.ReportFilter{Status: optionalString(r.URL.Query().Get("status"))}
	if filter.Status != "" && !domain.IsValidReportStatus(filter.Status) {
		writeDeliveryError(w, r, domain.NewInvalid("status", domain.ReasonInvalidArgument,
			"status must be draft, published or stale"))
		return
	}
	if filter.BatchID, err = parseOptionalQueryID(r, "batchId"); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Cursor, err = parseOpaqueCursor(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if filter.Limit, err = parseLimit(r); err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	reports, next, hasMore, err := h.reports.ListPage(r.Context(), versionID, filter)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	items := make([]reportSummaryDTO, 0, len(reports))
	for _, report := range reports {
		items = append(items, newReportSummaryDTO(report))
	}
	writePage(w, items, next, hasMore)
}

// ensureReportBody is the POST /versions/{id}/reports body.
type ensureReportBody struct {
	BatchID *string `json:"batchId"`
}

// Ensure makes the report of a batch exist: 201 when this call created the
// revision, 200 when a live revision already existed (spec §7.4).
func (h *ReportHandler) Ensure(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	var body ensureReportBody
	if err := decodeJSON(r, &body, 0); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	batchID, err := optionalDecimalID("batchId", body.BatchID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if batchID == nil {
		writeDeliveryError(w, r, domain.NewInvalid("batchId", domain.ReasonInvalidArgument,
			"batchId is required"))
		return
	}
	if err := requireBatch(r.Context(), h.batches, versionID, *batchID, false); err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	report, created, err := h.reports.Ensure(r.Context(), versionID, *batchID, actorID(r.Context()))
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	status := http.StatusOK
	if created {
		status = http.StatusCreated
	}
	writeData(w, status, newReportDTO(report))
}

// Get returns one report revision with its content (the service refreshes the
// stale state on read).
func (h *ReportHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, reportID, err := reportPathIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	report, err := h.reports.Get(r.Context(), versionID, reportID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newReportDTO(report))
}

// Publish flips a draft report to published. Another status is 409
// report_not_publishable (the service owns the rule).
func (h *ReportHandler) Publish(w http.ResponseWriter, r *http.Request) {
	versionID, reportID, err := reportPathIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	report, err := h.reports.Publish(r.Context(), versionID, reportID, actorID(r.Context()))
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newReportDTO(report))
}

// Regenerate appends a new revision (201). Spec §7.4 says a request addressing
// an older revision whose batch already has a newer one returns that latest
// revision with 200 instead. T3.Regenerate applies the same rule but exposes no
// "created" flag, so the handler pre-reads the latest revision of the addressed
// report's batch to choose the status code (no T3 signature change).
func (h *ReportHandler) Regenerate(w http.ResponseWriter, r *http.Request) {
	versionID, reportID, err := reportPathIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	ctx := r.Context()
	addressed, err := h.reports.Get(ctx, versionID, reportID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	latest, err := h.latestReportOfBatch(ctx, versionID, addressed.BatchID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if latest != nil && latest.ID != reportID {
		// A newer revision already exists: return it unchanged, do not append.
		// The list row is the projection (no content, no batchNo/kind), so
		// re-read it through Get to render the same detail shape as the 201 path.
		newer, err := h.reports.Get(ctx, versionID, latest.ID)
		if err != nil {
			writeDeliveryError(w, r, err)
			return
		}
		writeData(w, http.StatusOK, newReportDTO(newer))
		return
	}
	report, err := h.reports.Regenerate(ctx, versionID, reportID, actorID(ctx))
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newReportDTO(report))
}

// latestReportOfBatch returns the newest revision of one batch, which the report
// list returns first (created_at DESC, id DESC).
func (h *ReportHandler) latestReportOfBatch(ctx context.Context, versionID, batchID int64) (*domain.Report, error) {
	reports, _, _, err := h.reports.ListPage(ctx, versionID,
		deliveryapp.ReportFilter{BatchID: &batchID, Limit: 1})
	if err != nil {
		return nil, err
	}
	if len(reports) == 0 {
		return nil, nil
	}
	return &reports[0], nil
}

// Export streams the verified Markdown file with its SHA256 header. The
// authenticated actor is attached with WithReportActor so the T3 audit entry
// carries it (the frozen Export signature has no actor argument).
func (h *ReportHandler) Export(w http.ResponseWriter, r *http.Request) {
	versionID, reportID, err := reportPathIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	format := optionalString(r.URL.Query().Get("format"))
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" {
		writeDeliveryError(w, r, domain.NewInvalid("format", domain.ReasonInvalidArgument,
			"only the markdown report export is supported"))
		return
	}

	ctx := deliveryapp.WithReportActor(r.Context(), actorID(r.Context()))
	exported, err := h.reports.Export(ctx, versionID, reportID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	filename := exported.Filename
	if filename == "" {
		filename = "report-" + strconv.FormatInt(exported.ReportID, 10) +
			"-r" + strconv.Itoa(exported.Revision) + ".md"
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filename+`"; filename*=UTF-8''`+filename)
	w.Header().Set("X-Content-SHA256", exported.SHA256)
	w.Header().Set("Content-Length", strconv.Itoa(len(exported.Content)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(exported.Content)
}

// Diff compares the addressed revision with an explicit base or the previous
// batch report (spec §7.4).
func (h *ReportHandler) Diff(w http.ResponseWriter, r *http.Request) {
	versionID, reportID, err := reportPathIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	baseReportID, err := parseOptionalQueryID(r, "baseReportId")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	diff, err := h.reports.Diff(r.Context(), versionID, reportID, baseReportID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newReportDiffDTO(diff))
}

// reportPathIDs parses the {id} and {reportId} path values.
func reportPathIDs(r *http.Request) (int64, int64, error) {
	versionID, err := pathID(r, "id")
	if err != nil {
		return 0, 0, err
	}
	reportID, err := pathID(r, "reportId")
	if err != nil {
		return 0, 0, err
	}
	return versionID, reportID, nil
}
