package versionapi

// Task 9 (v0.22 A-8): the plan write entry point (spec R-A8-2/API-4, B §9.1).
//
//	PATCH /api/versions/{id}/plan-items/{itemId}
//
// The handler only parses the path/body and renders the written item: the
// revision optimistic lock, field validation and the plan.item_changed event
// belong to *versionapp.PlanService. Route registration and the OpenAPI schema
// are wired by the server task (Task 37).

import (
	"context"
	"errors"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// PlanService is the version plan write surface the PATCH handler needs.
// *versionapp.PlanService implements it.
type PlanService interface {
	Update(ctx context.Context, versionID, itemID int64, patch versionapp.PlanPatch, revision int) (version.PlanItem, error)
}

var _ PlanService = (*versionapp.PlanService)(nil)

// PlanHandler serves the plan-item write route. The server mounts it behind the
// shared bearer-authentication middleware; the handler never parses credentials
// itself.
type PlanHandler struct {
	svc PlanService
}

// NewPlanHandler builds the plan handler.
func NewPlanHandler(svc PlanService) *PlanHandler {
	return &PlanHandler{svc: svc}
}

// Update PATCH /api/versions/{id}/plan-items/{itemId}. The body is a partial
// write: title/status/decision/order are optional (an empty string or a missing
// order keeps the stored value) and revision is required. 200 returns the stored
// item including the new revision; a stale revision is a typed 409 conflict
// carrying the expected and stored revisions.
func (h *PlanHandler) Update(w http.ResponseWriter, r *http.Request) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	itemID, err := validation.NonEmptyID("itemId", r.PathValue("itemId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request planItemPatchRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if request.Revision == nil {
		httpx.Error(w, r, platform.Invalid("revision", "is required for a plan write"))
		return
	}
	if *request.Revision <= 0 {
		httpx.Error(w, r, platform.Invalid("revision", "must be a positive integer"))
		return
	}
	patch := versionapp.PlanPatch{
		Title:    request.Title,
		Status:   request.Status,
		Decision: request.Decision,
		Order:    request.Order,
	}
	stored, err := h.svc.Update(r.Context(), versionID, itemID, patch, *request.Revision)
	if err != nil {
		httpx.Error(w, r, mapPlanWriteHTTPError(err))
		return
	}
	httpx.JSON(w, http.StatusOK, toPlanItemWriteDTO(stored))
}

// mapPlanWriteHTTPError keeps the HTTP envelope typed. *versionapp.ConflictError
// is not a *platform.Error (the application contract requires that concrete
// type), so the handler must recognize it and build the 409 envelope itself; a
// missing item or a validation failure already arrives as a *platform.Error and
// passes through.
func mapPlanWriteHTTPError(err error) error {
	var conflict *versionapp.ConflictError
	if !errors.As(err, &conflict) {
		return err
	}
	apiErr := platform.Conflict(conflict.Error())
	details := map[string]any{
		"item_id":           formatDecimal(conflict.ItemID),
		"expected_revision": conflict.Expected,
	}
	if conflict.Actual > 0 {
		details["actual_revision"] = conflict.Actual
	}
	apiErr.Details = details
	return apiErr
}

// ---------- DTOs ----------

// planItemPatchRequest is the partial write body. Revision is a pointer so a
// missing value is distinguishable from 0 and is rejected with a field error.
type planItemPatchRequest struct {
	Title    string `json:"title"`
	Status   string `json:"status"`
	Decision string `json:"decision"`
	Order    *int   `json:"order"`
	Revision *int   `json:"revision"`
}

// planItemWriteDTO is the frozen write response shape (spec API-4): the stored
// plan item including the revision the client must present on the next write.
type planItemWriteDTO struct {
	ID         string   `json:"id"`
	VersionID  string   `json:"version_id"`
	Phase      string   `json:"phase"`
	Title      string   `json:"title"`
	Status     string   `json:"status"`
	Decision   string   `json:"decision"`
	Order      int      `json:"order"`
	Revision   int      `json:"revision"`
	DependsOn  []string `json:"depends_on"`
	WorkItemID *string  `json:"work_item_id"`
}

func toPlanItemWriteDTO(item version.PlanItem) planItemWriteDTO {
	return planItemWriteDTO{
		ID:         formatDecimal(item.ID),
		VersionID:  formatDecimal(item.VersionID),
		Phase:      item.Phase,
		Title:      item.Title,
		Status:     item.Status,
		Decision:   item.Decision,
		Order:      item.Order,
		Revision:   item.Revision,
		DependsOn:  decimalIDList(item.DependsOn),
		WorkItemID: formatOptionalDecimal(item.WorkItemID),
	}
}
