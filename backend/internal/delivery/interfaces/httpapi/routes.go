package deliveryapi

import "net/http"

// Register mounts the P4 delivery path items on the shared mux. The server
// assembly mounts the returned handler behind the existing bearer
// authentication middleware (Task 23); handlers never parse credentials.
func Register(mux *http.ServeMux, h *Handler) {
	// Issues.
	mux.Handle("GET /api/versions/{versionId}/issues", http.HandlerFunc(h.ListIssues))
	mux.Handle("GET /api/versions/{versionId}/issues/{issueId}", http.HandlerFunc(h.GetIssue))
	mux.Handle("POST /api/versions/{versionId}/issues/{issueId}/confirm", http.HandlerFunc(h.ConfirmIssue))
	mux.Handle("POST /api/versions/{versionId}/issues/{issueId}/dismiss", http.HandlerFunc(h.DismissIssue))
	mux.Handle("POST /api/versions/{versionId}/issues/{issueId}/reopen", http.HandlerFunc(h.ReopenIssue))
	mux.Handle("POST /api/versions/{versionId}/issues/{issueId}/merge", http.HandlerFunc(h.MergeIssue))

	// Reports.
	mux.Handle("GET /api/versions/{versionId}/reports", http.HandlerFunc(h.ListReports))
	mux.Handle("GET /api/versions/{versionId}/reports/{reportId}", http.HandlerFunc(h.GetReport))
	mux.Handle("GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}", http.HandlerFunc(h.GetReportRevision))
	mux.Handle("GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}/html", http.HandlerFunc(h.DownloadReportHTML))
	mux.Handle("GET /api/versions/{versionId}/reports/{reportId}/revisions/{revision}/pdf", http.HandlerFunc(h.DownloadReportPDF))
	mux.Handle("POST /api/versions/{versionId}/reports/{reportId}/regenerate", http.HandlerFunc(h.RegenerateReport))
}

// RegisterGlobal mounts the P6 cross-version read-only issue/report paths on the
// shared mux (spec §4.2 ops #7/#8). Both paths are login-only, so the caller
// passes the same bearer middleware as the other authenticated routes; the
// handlers never parse credentials and expose no write operation.
func RegisterGlobal(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, h *GlobalHandler) {
	if h == nil || authenticated == nil {
		return
	}
	mux.Handle("GET /api/issues", authenticated(http.HandlerFunc(h.ListGlobalIssues)))
	mux.Handle("GET /api/reports", authenticated(http.HandlerFunc(h.ListGlobalReports)))
}

// RegisterRoutes is the assembly-root convenience hook (Task 22) matching the
// Register*Routes convention of the other P6 contexts.
func RegisterRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, handler *GlobalHandler) {
	RegisterGlobal(mux, authenticated, handler)
}
