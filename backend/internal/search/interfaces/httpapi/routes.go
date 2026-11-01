package httpapi

import "net/http"

// RegisterRoutes mounts GET /api/search behind the server's bearer
// authentication middleware. Every logged-in user may search (spec §4.2/§6);
// there is no admin-only guard. The composition root calls this from the P6
// wiring task.
func RegisterRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, handler *Handler) {
	if mux == nil || handler == nil {
		return
	}
	mux.Handle("GET /api/search", authenticated(http.HandlerFunc(handler.Search)))
}
