package versionapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// WaiverService is the application surface for the two-level admin waiver
// (v0.2.4-G SC-G11, GD-06/P23, Q7=A). *versionapp.WaiverService satisfies it.
type WaiverService interface {
	WaiveClose(ctx context.Context, versionID int64, reason string, actorID int64) (versionapp.WaiveCloseResult, error)
	WaiveRequirement(ctx context.Context, requirementID int64, reason string, actorID int64) error
}

// WaiverHandler serves the admin waiver endpoints. The routes are mounted behind
// the server's admin middleware; each handler additionally re-checks the role so
// a direct handler call can never waive without an admin actor.
type WaiverHandler struct {
	svc WaiverService
}

// NewWaiverHandler builds the waiver handler.
func NewWaiverHandler(svc WaiverService) *WaiverHandler {
	return &WaiverHandler{svc: svc}
}

var _ WaiverService = (*versionapp.WaiverService)(nil)

// RegisterWaiverRoutes mounts the two admin waiver endpoints behind the admin
// gate. A nil handler leaves them unmounted (degraded assemblies).
func RegisterWaiverRoutes(mux *http.ServeMux, admin func(http.Handler) http.Handler, h *WaiverHandler) {
	if h == nil {
		return
	}
	mux.Handle("POST /api/versions/{versionId}/waive-close", admin(http.HandlerFunc(h.WaiveClose)))
	mux.Handle("POST /api/versions/{versionId}/requirements/{requirementId}/waive", admin(http.HandlerFunc(h.WaiveRequirement)))
}

// waiveRequest is the reason body of both waiver endpoints.
type waiveRequest struct {
	Reason string `json:"reason"`
}

// waiveCloseResponseDTO projects the waived-closed version and the synchronously
// published unverified report revision.
type waiveCloseResponseDTO struct {
	VersionID      string `json:"version_id"`
	Status         string `json:"status"`
	ClosureKind    string `json:"closure_kind"`
	Unverified     bool   `json:"unverified"`
	CloseReason    string `json:"close_reason"`
	ReportID       string `json:"report_id"`
	ReportRevision int32  `json:"report_revision"`
}

// waiveRequirementResponseDTO confirms the requirement waiver.
type waiveRequirementResponseDTO struct {
	RequirementID string `json:"requirement_id"`
	Waived        bool   `json:"waived"`
}

// WaiveClose POST /api/versions/{versionId}/waive-close (admin only).
func (h *WaiverHandler) WaiveClose(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireWaiverAdminActor(w, r)
	if !ok {
		return
	}
	versionID, err := validation.NonEmptyID("versionId", r.PathValue("versionId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request waiveRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	result, err := h.svc.WaiveClose(r.Context(), versionID, strings.TrimSpace(request.Reason), actor.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, waiveCloseResponseDTO{
		VersionID:      strconv.FormatInt(result.Version.ID, 10),
		Status:         result.Version.Status,
		ClosureKind:    result.Version.ClosureKind,
		Unverified:     result.Version.Unverified,
		CloseReason:    result.Version.CloseReason,
		ReportID:       strconv.FormatInt(result.ReportID, 10),
		ReportRevision: result.Revision,
	})
}

// WaiveRequirement POST /api/versions/{versionId}/requirements/{requirementId}/waive
// (admin only).
func (h *WaiverHandler) WaiveRequirement(w http.ResponseWriter, r *http.Request) {
	actor, ok := requireWaiverAdminActor(w, r)
	if !ok {
		return
	}
	if _, err := validation.NonEmptyID("versionId", r.PathValue("versionId")); err != nil {
		httpx.Error(w, r, err)
		return
	}
	requirementID, err := validation.NonEmptyID("requirementId", r.PathValue("requirementId"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request waiveRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.svc.WaiveRequirement(r.Context(), requirementID, strings.TrimSpace(request.Reason), actor.ID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, waiveRequirementResponseDTO{
		RequirementID: strconv.FormatInt(requirementID, 10),
		Waived:        true,
	})
}

// requireWaiverAdminActor re-checks the admin role at the handler (the route is
// already behind the admin middleware; this guards direct handler calls).
func requireWaiverAdminActor(w http.ResponseWriter, r *http.Request) (platform.Actor, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return platform.Actor{}, false
	}
	if !actor.IsAdmin() {
		httpx.Error(w, r, platform.Forbidden())
		return platform.Actor{}, false
	}
	return actor, true
}
