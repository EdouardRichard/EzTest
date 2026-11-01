package workspaceapi

import "net/http"

// RegisterRoutes mounts the workspace read-only routes behind the supplied
// authentication middleware. The P6 server wiring (Task 22) owns the actual
// mounting, exactly like the other P6 contexts, so this context never edits the
// shared server routes.
func RegisterRoutes(mux *http.ServeMux, authenticated func(http.Handler) http.Handler, handler *Handler) {
	mux.Handle("GET /api/todos", authenticated(http.HandlerFunc(handler.ListTodos)))
}
