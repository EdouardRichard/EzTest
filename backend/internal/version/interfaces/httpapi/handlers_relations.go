package httpapi

import (
	"net/http"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxRelationBodyBytes caps the relation request bodies.
const maxRelationBodyBytes int64 = 16 << 10

// RelationHandler serves the /api/versions/{id}/relations endpoints (spec §7.4,
// §6.4).
type RelationHandler struct {
	relations RelationService
}

// RegisterRoutes mounts the relation routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *RelationHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/relations", h.List)
	mux.HandleFunc("POST /api/versions/{id}/relations", h.Create)
	mux.HandleFunc("PATCH /api/versions/{id}/relations/{relId}", h.Update)
	mux.HandleFunc("DELETE /api/versions/{id}/relations/{relId}", h.Delete)
}

// List returns every relation of the version (no pagination; capped at 500 by
// the service).
func (h *RelationHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	relations, err := h.relations.List(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newRelationDTOs(relations))
}

// Create adds one relation between two requirements of the version (201).
func (h *RelationHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request createRelationRequest
	if err := DecodeJSON(w, r, &request, maxRelationBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	relation, err := h.relations.Create(r.Context(), versionID, versionapp.CreateRelationCommand{
		FromRequirementID: request.FromRequirementID,
		ToRequirementID:   request.ToRequirementID,
		RelationType:      request.RelationType,
		Description:       request.Description,
		Source:            domain.SourceUser,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newRelationDTO(relation))
}

// Update rewrites a relation's type and/or description. Relations have no
// revision; the pointer fields keep true PATCH semantics, and the authenticated
// actor is forwarded so the relation event carries actorId.
func (h *RelationHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, relationID, ok := h.relationPathIDs(w, r)
	if !ok {
		return
	}
	var request updateRelationRequest
	if err := DecodeJSON(w, r, &request, maxRelationBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	relation, err := h.relations.Update(r.Context(), versionID, relationID,
		request.RelationType, request.Description, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newRelationDTO(relation))
}

// Delete removes one relation of the version and returns 204.
func (h *RelationHandler) Delete(w http.ResponseWriter, r *http.Request) {
	versionID, relationID, ok := h.relationPathIDs(w, r)
	if !ok {
		return
	}
	if err := h.relations.Delete(r.Context(), versionID, relationID, ActorID(r.Context())); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// relationPathIDs parses and validates the {id}/{relId} path pair. On failure
// it renders the 422 and reports false.
func (h *RelationHandler) relationPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	relationID, err := PathID(r, "relId")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	return versionID, relationID, true
}
