package httpapi

import (
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ListAnchors returns the anchor list of the version filtered by subject (design
// spec §7.3). Both filters are optional; subjectKind is forwarded to the
// application service (which owns the enum and the ordering) and subjectId is a
// decimal string parsed to the domain's int64.
func (h *Handler) ListAnchors(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	query := r.URL.Query()
	filter := domain.AnchorFilter{SubjectKind: strings.TrimSpace(query.Get("subjectKind"))}
	if raw := strings.TrimSpace(query.Get("subjectId")); raw != "" {
		subjectID, err := ParseDecimalID(raw, "subjectId")
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		filter.SubjectID = &subjectID
	}
	anchors, err := h.anchors.List(r.Context(), versionID, filter)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAnchorDTOs(anchors))
}

// CreateAnchor creates one user anchor of the version (design spec §6.7/§7.3).
// The handler only enforces the transport shape (required fields, decimal-string
// subjectId); the application service owns the subject kind enum, the subject
// ownership, the file existence, the line-range rule and the point-uniqueness
// conflict, so every verdict is the documented 422/409 reason.
func (h *Handler) CreateAnchor(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var body createAnchorRequest
	if err := DecodeJSON(r, &body, maxRequestBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	subjectKind := strings.TrimSpace(body.SubjectKind)
	if subjectKind == "" {
		WriteServiceError(w, r, newBadRequest("subjectKind", reasonInvalidRequest, "subjectKind is required"))
		return
	}
	rawSubjectID := strings.TrimSpace(body.SubjectID)
	if rawSubjectID == "" {
		WriteServiceError(w, r, newBadRequest("subjectId", reasonInvalidRequest, "subjectId is required"))
		return
	}
	subjectID, err := ParseDecimalID(rawSubjectID, "subjectId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	filePath := strings.TrimSpace(body.FilePath)
	if filePath == "" {
		WriteServiceError(w, r, newBadRequest("filePath", reasonInvalidRequest, "filePath is required"))
		return
	}
	anchor, err := h.anchors.Create(r.Context(), versionID, ActorID(r.Context()), domain.CreateAnchorParams{
		SubjectKind: subjectKind,
		SubjectID:   subjectID,
		FilePath:    filePath,
		StartLine:   body.StartLine,
		EndLine:     body.EndLine,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAnchorDTO(anchor))
}

// DeleteAnchor removes one anchor of the version. The application service
// scopes the deletion to the version, so an anchor of another version (or a
// missing one) is the documented 404 anchor_not_found. A successful deletion is
// a 204 without a body, like the other P3 deletes.
func (h *Handler) DeleteAnchor(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	anchorID, err := PathID(r, "anchorId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.anchors.Delete(r.Context(), versionID, anchorID); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
