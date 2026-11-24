package designapi

import (
	"net/http"
	"strings"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// OverrideJudgment POST /api/versions/{id}/design/judgments/{caseId}/override.
//
// ADMIN ONLY (spec SC-E5, T16): a human decision on a case version whose
// judge review escalated (fail with the rework budget exhausted, or the judge
// became unavailable). The overridable precondition itself is JudgmentService.
// Override's own server-side check -- this handler only enforces the admin
// role and parses input; it never decides whether a case is overridable.
func (h *Handler) OverrideJudgment(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireAdmin(w, r)
	if !ok {
		return
	}
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
	var request overrideJudgmentRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if err := validation.String("reason", reason, 1, designapp.OverrideReasonMax); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.judgments == nil {
		httpx.Error(w, r, platform.Unavailable("the judgment service is not wired; a judge verdict cannot be overridden"))
		return
	}
	override, err := h.judgments.Override(r.Context(), versionID, caseID, actorID, reason)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toJudgmentOverrideDTO(override))
}

// ListJudgments GET /api/versions/{id}/design/judgments?requirement_id=.
//
// Read-only judgment report (spec SC-E5, T16): one entry per current case
// (judged or not) with its review_state and latest current-version judge fact.
// Visibility matches the design graph/case reads (admin or user) -- the
// case_review report is user-facing, not an admin-only surface.
func (h *Handler) ListJudgments(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	requirementID, err := queryOptionalID(r, "requirement_id", "requirement_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.judgments == nil {
		httpx.Error(w, r, platform.Unavailable("the judgment service is not wired; the judgment report is unavailable"))
		return
	}
	view, err := h.judgments.ListJudgments(r.Context(), versionID, requirementID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	dto, err := toJudgmentReportDTO(view)
	if err != nil {
		// Loud, attributable failure instead of a silently emptied matrix
		// (constitution VII.6/VII.7).
		httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "judgment_matrix_encode_failed", err.Error()))
		return
	}
	httpx.JSON(w, http.StatusOK, dto)
}
