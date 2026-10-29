package httpapi

import (
	"net/http"
	"strconv"
)

// ReportHandler serves the derived Batch 1 report and its Markdown export
// (spec §6.2/§6.3).
type ReportHandler struct {
	reports ReportService
}

// RegisterRoutes mounts the report routes. Authentication is the server's global
// middleware; the report is a read of the version data.
func (h *ReportHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/report", h.Get)
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/report/export", h.Export)
}

// Get returns the derived Batch 1 report JSON.
func (h *ReportHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	report, err := h.reports.BatchReport(r.Context(), versionID, batchID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newReportDTO(&report))
}

// Export streams the server-rendered Markdown with Content-Disposition and
// X-Content-SHA256 (spec §6.2). P6 ships Markdown only; another format is a 422
// export_failed.
func (h *ReportHandler) Export(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	format := r.URL.Query().Get("format")
	if format == "" {
		format = "markdown"
	}
	if format != "markdown" {
		writeServiceError(w, r, newInvalid("export_failed", "only the markdown report export is supported"))
		return
	}
	body, digest, err := h.reports.Markdown(r.Context(), versionID, batchID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	filename := "execution-report-" + strconv.FormatInt(batchID, 10) + ".md"
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition",
		`attachment; filename="`+filename+`"; filename*=UTF-8''`+filename)
	w.Header().Set("X-Content-SHA256", digest)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}
