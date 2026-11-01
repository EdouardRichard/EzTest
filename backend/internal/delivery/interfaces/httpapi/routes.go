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
	mux.Handle("POST /api/versions/{versionId}/reports/{reportId}/regenerate", http.HandlerFunc(h.RegenerateReport))
}
