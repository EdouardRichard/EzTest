package agentapi

import "net/http"

// RegisterProfileRoutes mounts the P6 admin profile/tool operations. The
// assembly root owns the middleware and mounting call (Task 22), so this
// package stays independent from server. Every route requires the admin role
// (spec §8.5); normal users never receive prompts or tool schemas.
func RegisterProfileRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *ProfileHandler) {
	if mux == nil || admin == nil || h == nil {
		return
	}
	mux.Handle("GET /api/agents/profiles", admin(http.HandlerFunc(h.List)))
	mux.Handle("GET /api/agents/profiles/{key}", admin(http.HandlerFunc(h.Get)))
	mux.Handle("PATCH /api/agents/profiles/{key}", admin(http.HandlerFunc(h.Patch)))
	mux.Handle("PUT /api/agents/profiles/{key}/prompt", admin(http.HandlerFunc(h.SavePrompt)))
	mux.Handle("POST /api/agents/profiles/{key}/prompt/reset", admin(http.HandlerFunc(h.ResetPrompt)))
	mux.Handle("POST /api/agents/profiles/{key}/prompt/restore", admin(http.HandlerFunc(h.RestorePrompt)))
	mux.Handle("GET /api/agents/profiles/{key}/prompt/revisions", admin(http.HandlerFunc(h.Revisions)))
	mux.Handle("POST /api/agents/profiles/{key}/prompt/preview", admin(http.HandlerFunc(h.Preview)))
	mux.Handle("GET /api/agents/tools", admin(http.HandlerFunc(h.Tools)))
}
