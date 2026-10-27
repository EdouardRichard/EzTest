package httpapi

import (
	"net/http"

	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
)

// Register mounts the repository-scoped codeintel routes (design spec §7.2) on
// mux. Authentication itself is the server's global bearer middleware; the one
// administrator action (triggering a T0 index) is wrapped in the identity
// context's existing RequireAdmin guard, so this package never invents a
// parallel role check.
//
// A route group is only mounted when its application service is wired: GET
// /api/code/repos needs both the repository catalog and the index service for
// its latest-run projection, the index-run reads need the index service, and the
// live Git/facts reads need the query service.
func (h *Handler) Register(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	if h.index != nil && h.repos != nil {
		mux.HandleFunc("GET /api/code/repos", h.ListRepos)
	}
	if h.index != nil {
		mux.Handle("POST /api/code/repos/{repoId}/index", identityhttp.RequireAdmin(http.HandlerFunc(h.StartIndex)))
		mux.HandleFunc("GET /api/code/repos/{repoId}/index-runs/latest", h.LatestIndexRun)
		mux.HandleFunc("GET /api/code/repos/{repoId}/index-runs/{runId}", h.GetIndexRun)
	}
	if h.query != nil {
		mux.HandleFunc("GET /api/code/repos/{repoId}/tree", h.Tree)
		mux.HandleFunc("GET /api/code/repos/{repoId}/file", h.File)
		mux.HandleFunc("GET /api/code/repos/{repoId}/diff", h.Diff)
		mux.HandleFunc("GET /api/code/repos/{repoId}/diff/file", h.FileDiff)
		mux.HandleFunc("GET /api/code/repos/{repoId}/facts", h.Facts)
		mux.HandleFunc("GET /api/code/repos/{repoId}/search", h.Search)
		mux.HandleFunc("GET /api/code/repos/{repoId}/commits/{sha}", h.Commit)
	}
}
