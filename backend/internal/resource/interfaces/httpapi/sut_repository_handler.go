package resourceapi

// Task 29 (v0.22 R-C2/API-17): SUT-level multi-repository management.
//
//	GET  /api/suts/{id}/repositories
//	POST /api/suts/{id}/repositories
//
// The handler only parses the path/body and renders the association: repository
// ownership, role trimming, the (sut_id, repo_id) uniqueness conflict and the
// audit record belong to *resourceapp.Service. Task 37 mounts the routes through
// RegisterSUTRepositoryRoutes and adds the OpenAPI schema.

import (
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
)

// ListSUTRepositories GET /api/suts/{id}/repositories. A missing SUT is a typed
// 404; an invalid path ID is a 400.
func (h *Handler) ListSUTRepositories(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListSUTRepositories(r.Context(), sutID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]sutRepositoryDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toSUTRepositoryDTO(view))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// AddSUTRepository POST /api/suts/{id}/repositories. The body carries
// repository_id (required, positive decimal) and an optional role label. An
// unknown SUT/repository is a 404, a malformed ID a 400 and an existing
// association a 409.
func (h *Handler) AddSUTRepository(w http.ResponseWriter, r *http.Request) {
	sutID, err := pathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	actor, err := actorID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var body sutRepositoryCreateRequest
	if err := decodeJSON(w, r, &body, bodyLimit); err != nil {
		httpx.Error(w, r, err)
		return
	}
	repositoryID, err := bodyID("repository_id", body.RepositoryID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.AddSUTRepository(r.Context(), sutID, resourceapp.SUTRepositoryInput{
		RepositoryID: repositoryID,
		Role:         body.Role,
	}, actor)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toSUTRepositoryDTO(view))
}

// RegisterSUTRepositoryRoutes mounts API-17 on mux for Task 37. A nil mux or
// authentication middleware is a wiring bug (an authenticated route must never
// be silently unmounted), so the helper panics; a nil handler keeps the route
// table of a deployment without the resource context.
func RegisterSUTRepositoryRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *Handler) {
	if mux == nil {
		panic("resourceapi: RegisterSUTRepositoryRoutes requires a non-nil mux")
	}
	if auth == nil {
		panic("resourceapi: RegisterSUTRepositoryRoutes requires an authentication middleware")
	}
	if h == nil {
		return
	}
	mux.Handle("GET /api/suts/{id}/repositories", auth(http.HandlerFunc(h.ListSUTRepositories)))
	mux.Handle("POST /api/suts/{id}/repositories", auth(http.HandlerFunc(h.AddSUTRepository)))
}
