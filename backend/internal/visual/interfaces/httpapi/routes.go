package httpapi

import "net/http"

// Register mounts the nine P5 visual operations and the quality metrics
// endpoint on the shared mux (spec §4.2 #30–#38). Reading/configuring visual
// assertions requires a logged-in user; approving a baseline is admin-only.
// The assembly root owns mounting so this package stays out of server wiring.
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	// Assertion configuration (#30–#32).
	mux.Handle("GET /api/versions/{versionId}/visual/assertions", authenticated(http.HandlerFunc(h.ListAssertions)))
	mux.Handle("PUT /api/versions/{versionId}/visual/assertions/{caseAssertionId}", authenticated(http.HandlerFunc(h.UpsertAssertion)))
	mux.Handle("GET /api/versions/{versionId}/visual/assertions/{id}", authenticated(http.HandlerFunc(h.GetAssertion)))

	// Baseline history and approval (#33–#34).
	mux.Handle("GET /api/versions/{versionId}/visual/assertions/{id}/baselines", authenticated(http.HandlerFunc(h.ListBaselines)))
	mux.Handle("POST /api/versions/{versionId}/visual/assertions/{id}/baselines", admin(http.HandlerFunc(h.ApproveBaseline)))

	// Diffs and overview (#35–#37).
	mux.Handle("GET /api/versions/{versionId}/visual/diffs", authenticated(http.HandlerFunc(h.ListDiffs)))
	mux.Handle("GET /api/versions/{versionId}/visual/diffs/{diffId}", authenticated(http.HandlerFunc(h.GetDiff)))
	mux.Handle("GET /api/versions/{versionId}/visual/overview", authenticated(http.HandlerFunc(h.GetOverview)))

	// Global or single-version quality metrics (#38).
	mux.Handle("GET /api/quality/visual-metrics", authenticated(http.HandlerFunc(h.GetMetrics)))
}
