package designapi

import (
	"net/http"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// Confirm POST /api/versions/{id}/design/confirmations.
func (h *Handler) Confirm(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request confirmRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	scope := request.Scope
	if err := design.ValidateConfirmationScope(scope); err != nil {
		httpx.Error(w, r, err)
		return
	}
	targetID, err := validation.NonEmptyID("target_id", request.TargetID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	batchID := ""
	if request.BatchID != nil {
		batchID = *request.BatchID
	}
	if err := design.ValidateBatchID(batchID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	result, err := h.graph.Confirm(r.Context(), versionID, designapp.ConfirmInput{
		Scope:    scope,
		TargetID: targetID,
		BatchID:  batchID,
	}, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toConfirmResultDTO(result))
}
