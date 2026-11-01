package httpapi

import "net/http"

// Register mounts the P7 instance routes. Admin endpoints are wrapped in the
// admin middleware; the readiness probe is public by contract. The assembly
// root (Task 19) owns the middleware instances and the mounting call.
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	if h == nil {
		return
	}
	mux.Handle("GET /api/admin/instances", admin(http.HandlerFunc(h.ListInstances)))
	mux.Handle("POST /api/admin/instances/{id}/drain", admin(http.HandlerFunc(h.DrainInstance)))
	mux.Handle("POST /api/admin/instances/{id}/activate", admin(http.HandlerFunc(h.ActivateInstance)))
	mux.Handle("GET /api/health/ready", http.HandlerFunc(h.Ready))
}

// RegisterRoutes is the assembly-root convenience hook matching the
// Register*Routes convention of the other contexts (Task 19).
func RegisterRoutes(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler, handler *Handler) {
	handler.Register(mux, authenticated, admin)
}
