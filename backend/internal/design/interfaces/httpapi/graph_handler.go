package designapi

import (
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// ReadGraph GET /api/versions/{id}/design/graph.
func (h *Handler) ReadGraph(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.graph.ReadGraph(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toGraphDTO(view))
}

// Overview GET /api/versions/{id}/design/overview.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.graph.Overview(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toOverviewDTO(view))
}

// GetCase GET /api/versions/{id}/design/cases/{caseId}?version_id=.
func (h *Handler) GetCase(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseID, err := pathID(r, "case_id", "caseId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseVersionID, err := queryOptionalID(r, "version_id", "version_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.graph.GetCase(r.Context(), versionID, caseID, caseVersionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCaseDetailDTO(view))
}
