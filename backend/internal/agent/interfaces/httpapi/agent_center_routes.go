package agentapi

import "net/http"

// RegisterAgentCenterRoutes mounts the three P6 Agent center read-only
// operations. The assembly root owns the middleware and mounting call (Task
// 22), so this package stays independent from server. Every route requires a
// logged-in user (spec §4.3.4/§8.5); prompts and transcripts are not exposed.
func RegisterAgentCenterRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *AgentCenterHandler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	mux.Handle("GET /api/agent-center/catalog", auth(http.HandlerFunc(h.Catalog)))
	mux.Handle("GET /api/agent-center/runs", auth(http.HandlerFunc(h.Runs)))
	mux.Handle("GET /api/agent-center/quality", auth(http.HandlerFunc(h.Quality)))
}
