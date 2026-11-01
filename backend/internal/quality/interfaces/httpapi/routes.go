package httpapi

import "net/http"

// Register mounts the four P6 quality operations on the shared mux. The
// assembly root owns the middleware and mounting call, so this package stays
// independent from server. Eval writes/reads are admin-only; the overview is
// visible to every logged-in user (spec §5.1/P6-D-02).
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	if h == nil {
		return
	}
	mux.Handle("GET /api/quality/eval-runs", admin(http.HandlerFunc(h.ListEvalRuns)))
	mux.Handle("POST /api/quality/eval-runs", admin(http.HandlerFunc(h.CreateEvalRun)))
	mux.Handle("GET /api/quality/eval-runs/{id}", admin(http.HandlerFunc(h.GetEvalRun)))
	mux.Handle("GET /api/quality/overview", authenticated(http.HandlerFunc(h.GetOverview)))
}

// RegisterRoutes is the assembly-root convenience hook (Task 22) matching the
// Register*Routes convention of the other P6 contexts.
func RegisterRoutes(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler, handler *Handler) {
	handler.Register(mux, authenticated, admin)
}
