package httpapi

import "net/http"

// Register mounts the eight P7 knowledge operations on the shared mux. The
// assembly root owns the bearer/admin middleware and the mounting call, so this
// package stays independent from server. Reads and draft writes are available
// to every logged-in user; review transitions are admin-only (spec §6.1/P7-D-06).
func (h *Handler) Register(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler) {
	if h == nil {
		return
	}
	mux.Handle("GET /api/knowledge/items", authenticated(http.HandlerFunc(h.ListItems)))
	mux.Handle("POST /api/knowledge/items", authenticated(http.HandlerFunc(h.CreateItem)))
	mux.Handle("GET /api/knowledge/items/{knowledgeId}", authenticated(http.HandlerFunc(h.GetItem)))
	mux.Handle("PATCH /api/knowledge/items/{knowledgeId}", authenticated(http.HandlerFunc(h.UpdateItem)))
	mux.Handle("POST /api/knowledge/items/{knowledgeId}/approve", admin(http.HandlerFunc(h.ApproveItem)))
	mux.Handle("POST /api/knowledge/items/{knowledgeId}/reject", admin(http.HandlerFunc(h.RejectItem)))
	mux.Handle("POST /api/knowledge/items/{knowledgeId}/revoke", admin(http.HandlerFunc(h.RevokeItem)))
	mux.Handle("GET /api/knowledge/items/{knowledgeId}/revisions", authenticated(http.HandlerFunc(h.ListRevisions)))
}

// RegisterRoutes is the assembly-root convenience hook (Task 19) matching the
// Register*Routes convention of the other contexts.
func RegisterRoutes(mux *http.ServeMux, authenticated, admin func(http.Handler) http.Handler, handler *Handler) {
	handler.Register(mux, authenticated, admin)
}
