package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// maxRequirementBodyBytes caps the ordinary requirement request bodies.
const maxRequirementBodyBytes int64 = 64 << 10

// RequirementHandler serves the /api/versions/{id}/requirements endpoints
// (spec §7.3, §6.2).
type RequirementHandler struct {
	requirements RequirementService
	stats        RequirementStatsReader
	relations    RelationService
	attachments  AttachmentService
}

// RegisterRoutes mounts the requirement routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *RequirementHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/requirements", h.List)
	mux.HandleFunc("POST /api/versions/{id}/requirements", h.Create)
	mux.HandleFunc("GET /api/versions/{id}/requirements/{rid}", h.Get)
	mux.HandleFunc("PATCH /api/versions/{id}/requirements/{rid}", h.Update)
	mux.HandleFunc("DELETE /api/versions/{id}/requirements/{rid}", h.Delete)
	mux.HandleFunc("POST /api/versions/{id}/requirements/{rid}/confirm", h.Confirm)
	mux.HandleFunc("POST /api/versions/{id}/requirements/{rid}/reopen", h.Reopen)
}

// Create adds a draft requirement to a version and returns 201.
func (h *RequirementHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var request createRequirementRequest
	if err := DecodeJSON(w, r, &request, maxRequirementBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requirement, err := h.requirements.Create(r.Context(), versionID, versionapp.CreateRequirementCommand{
		Title:              request.Title,
		Description:        request.Description,
		AcceptanceCriteria: request.AcceptanceCriteria,
		Priority:           request.Priority,
		Source:             domain.SourceUser,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeRequirement(w, r, requirement, http.StatusCreated, false)
}

// List returns one cursor page of a version's requirements ordered by
// created_at, id ascending, with the per-row aggregate counters.
func (h *RequirementHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requirements, hasMore, err := h.requirements.List(r.Context(), versionID, r.URL.Query().Get("status"), page)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	relationCounts, err := h.relationCounts(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]requirementDTO, 0, len(requirements))
	for i := range requirements {
		item, err := h.buildRequirement(r.Context(), &requirements[i], relationCounts, false)
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		items = append(items, item)
	}
	next := ""
	if len(requirements) > 0 {
		next = NextCursor(len(requirements), requirements[len(requirements)-1].ID, hasMore)
	}
	WritePage(w, items, next, hasMore)
}

// Get returns one requirement with its counters and attachment metadata.
func (h *RequirementHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	requirement, err := h.requirements.Get(r.Context(), versionID, reqID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeRequirement(w, r, requirement, http.StatusOK, true)
}

// Update applies a partial edit to a draft requirement under the revision lock.
func (h *RequirementHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	var request updateRequirementRequest
	if err := DecodeJSON(w, r, &request, maxRequirementBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requirement, err := h.requirements.Update(r.Context(), versionID, reqID, versionapp.UpdateRequirementCommand{
		Revision:           request.Revision,
		Title:              request.Title,
		Description:        request.Description,
		AcceptanceCriteria: request.AcceptanceCriteria,
		Priority:           request.Priority,
		Source:             domain.SourceUser,
	}, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeRequirement(w, r, requirement, http.StatusOK, false)
}

// Delete hard-deletes a draft requirement. The attachment storage paths are
// captured before the delete (the cascade removes their metadata rows) and the
// files are removed after the committed delete; cleanup failures are logged and
// only leave orphans for the P8 GC (lead ruling 6, spec §6.7/§6.10).
func (h *RequirementHandler) Delete(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	var storagePaths []string
	if h.attachments != nil {
		paths, err := h.attachments.StoragePathsForRequirement(ctx, versionID, reqID)
		if err != nil {
			WriteServiceError(w, r, err)
			return
		}
		storagePaths = paths
	}
	if err := h.requirements.Delete(ctx, versionID, reqID, ActorID(ctx)); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if h.attachments != nil {
		if err := h.attachments.RemoveStoredFiles(ctx, storagePaths); err != nil {
			slog.WarnContext(ctx, "version: requirement attachment cleanup failed",
				"version_id", versionID,
				"requirement_id", reqID,
				"error", err,
			)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// Confirm moves a draft requirement to clarified (spec §6.2).
func (h *RequirementHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	requirement, err := h.requirements.Confirm(r.Context(), versionID, reqID, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeRequirement(w, r, requirement, http.StatusOK, false)
}

// Reopen moves a clarified requirement back to draft (spec §6.2).
func (h *RequirementHandler) Reopen(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	requirement, err := h.requirements.Reopen(r.Context(), versionID, reqID, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeRequirement(w, r, requirement, http.StatusOK, false)
}

// requirementPathIDs parses and validates the {id}/{rid} path pair. On failure
// it renders the 422 and reports false.
func (h *RequirementHandler) requirementPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	reqID, err := PathID(r, "rid")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	return versionID, reqID, true
}

// writeRequirement renders one requirement DTO, resolving the version-level
// relation counts used by the row counters.
func (h *RequirementHandler) writeRequirement(w http.ResponseWriter, r *http.Request, requirement *versionapp.Requirement, status int, includeAttachments bool) {
	relationCounts, err := h.relationCounts(r.Context(), requirement.VersionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	dto, err := h.buildRequirement(r.Context(), requirement, relationCounts, includeAttachments)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, status, dto)
}

// buildRequirement resolves the aggregate counters of one requirement. The
// attachment metadata array is only included on the detail response.
func (h *RequirementHandler) buildRequirement(ctx context.Context, requirement *versionapp.Requirement, relationCounts map[int64]int, includeAttachments bool) (requirementDTO, error) {
	openQuestionCount := 0
	attachmentCount := 0
	if h.stats != nil {
		open, err := h.stats.CountOpenQuestions(ctx, requirement.ID)
		if err != nil {
			return requirementDTO{}, err
		}
		openQuestionCount = open
		attachments, _, err := h.stats.CountAttachments(ctx, requirement.ID, requirement.VersionID)
		if err != nil {
			return requirementDTO{}, err
		}
		attachmentCount = attachments
	}
	var attachments []attachmentDTO
	if includeAttachments && h.attachments != nil {
		items, err := h.attachments.List(ctx, requirement.VersionID, requirement.ID)
		if err != nil {
			return requirementDTO{}, err
		}
		attachments = newAttachmentDTOs(items)
	}
	return newRequirementDTO(requirement, openQuestionCount, relationCounts[requirement.ID], attachmentCount, attachments, includeAttachments), nil
}

// relationCounts counts the relations of one version per requirement. The
// relation list is not paginated (it is capped at 500), so one call serves the
// whole requirement page.
func (h *RequirementHandler) relationCounts(ctx context.Context, versionID int64) (map[int64]int, error) {
	counts := map[int64]int{}
	if h.relations == nil {
		return counts, nil
	}
	relations, err := h.relations.List(ctx, versionID)
	if err != nil {
		return nil, err
	}
	for i := range relations {
		from := relations[i].FromRequirementID
		to := relations[i].ToRequirementID
		counts[from]++
		if to != from {
			counts[to]++
		}
	}
	return counts, nil
}
