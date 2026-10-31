package versionapi

import (
	"context"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// RequirementService is the application surface the requirement handlers need.
type RequirementService interface {
	Create(ctx context.Context, versionID int64, in versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error)
	Update(ctx context.Context, versionID, requirementID int64, in versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Confirm(ctx context.Context, versionID, requirementID, actorID int64) (versionapp.ConfirmResult, error)
	BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64, actorID int64) (versionapp.BatchConfirmResult, error)
	Rollback(ctx context.Context, versionID, requirementID int64, reason string, proposalMessageID *int64, actorID int64) (versionapp.RequirementDetail, error)
	ListRelations(ctx context.Context, versionID, requirementID int64) ([]versionapp.RelationView, error)
}

// RequirementHandler serves the /api/versions/{id}/requirements routes.
type RequirementHandler struct {
	svc RequirementService
}

// NewRequirementHandler builds the requirement handler.
func NewRequirementHandler(svc RequirementService) *RequirementHandler {
	return &RequirementHandler{svc: svc}
}

var _ RequirementService = (*versionapp.RequirementService)(nil)

// List GET /api/versions/{id}/requirements?status=&limit=&before=.
func (h *RequirementHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	limit := pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	before, err := parseBefore(query.Get("before"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	status := strings.TrimSpace(query.Get("status"))
	views, hasMore, err := h.svc.List(r.Context(), versionID, status, limit, before)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]requirementSummaryDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toRequirementSummaryDTO(view))
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// Create POST /api/versions/{id}/requirements.
func (h *RequirementHandler) Create(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request createRequirementRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.svc.Create(r.Context(), versionID, request.draft(), actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toRequirementDetailDTO(detail))
}

// Get GET /api/versions/{id}/requirements/{rid}.
func (h *RequirementHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, requirementID, err := requirementPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.svc.Get(r.Context(), versionID, requirementID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRequirementDetailDTO(detail))
}

// Update PATCH /api/versions/{id}/requirements/{rid}.
func (h *RequirementHandler) Update(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, requirementID, err := requirementPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request updateRequirementRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.svc.Update(r.Context(), versionID, requirementID, request.draft(), request.Reason, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRequirementDetailDTO(detail))
}

// Confirm POST /api/versions/{id}/requirements/{rid}/confirmations.
func (h *RequirementHandler) Confirm(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, requirementID, err := requirementPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	result, err := h.svc.Confirm(r.Context(), versionID, requirementID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConfirmRequirementDTO(result))
}

// BatchConfirm POST /api/versions/{id}/requirements/confirmations.
func (h *RequirementHandler) BatchConfirm(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request batchConfirmRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ids, err := request.ids()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	result, err := h.svc.BatchConfirm(r.Context(), versionID, ids, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toBatchConfirmDTO(result))
}

// Rollback POST /api/versions/{id}/requirements/{rid}/rollback.
func (h *RequirementHandler) Rollback(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, requirementID, err := requirementPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request rollbackRequirementRequest
	if err := decodeOptionalBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	var proposalMessageID *int64
	if request.ProposalMessageID != nil && strings.TrimSpace(*request.ProposalMessageID) != "" {
		parsed, err := validation.NonEmptyID("proposal_message_id", *request.ProposalMessageID)
		if err != nil {
			httpx.Error(w, r, err)
			return
		}
		proposalMessageID = &parsed
	}
	detail, err := h.svc.Rollback(r.Context(), versionID, requirementID, request.Reason, proposalMessageID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRequirementDetailDTO(detail))
}

// ListRelations GET /api/versions/{id}/requirements/{rid}/relations.
func (h *RequirementHandler) ListRelations(w http.ResponseWriter, r *http.Request) {
	versionID, requirementID, err := requirementPathIDs(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListRelations(r.Context(), versionID, requirementID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]requirementRelationDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toRequirementRelationDTO(view))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

func requirementPathIDs(r *http.Request) (int64, int64, error) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		return 0, 0, err
	}
	requirementID, err := validation.NonEmptyID("rid", r.PathValue("rid"))
	if err != nil {
		return 0, 0, err
	}
	return versionID, requirementID, nil
}

func parseBefore(raw string) (*int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	value, err := pagination.Cursor(raw)
	if err != nil || value <= 0 {
		return nil, platform.Invalid("before", "must be a positive decimal ID")
	}
	return &value, nil
}

func decodeOptionalBody(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decodeBody(w, r, dst)
}
