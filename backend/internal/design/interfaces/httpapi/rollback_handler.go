package designapi

import (
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
)

// maxRollbackReasonLength bounds the optional rollback reason (mirrors the
// issue reason limit).
const maxRollbackReasonLength = 2000

// RollbackCaseVersion POST /api/versions/{id}/case-versions/{caseVersionId}/rollback.
//
// It supersedes one auto-confirmed discovery revision and returns the
// replacement revision. The endpoint is idempotent: repeating the request
// returns the existing replacement. The optional body only carries an audit
// reason; a non-object body is a 400.
func (h *Handler) RollbackCaseVersion(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseVersionID, err := pathID(r, "case_version_id", "caseVersionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	reason := ""
	if r.Body != nil && r.ContentLength != 0 {
		var request rollbackCaseVersionRequest
		if err := decodeBody(w, r, &request); err != nil {
			httpx.Error(w, r, err)
			return
		}
		reason = strings.TrimSpace(request.Reason)
	}
	if len([]rune(reason)) > maxRollbackReasonLength {
		httpx.Error(w, r, platform.Invalid("reason", "must be at most 2000 characters"))
		return
	}
	view, err := h.graph.RollbackCaseVersion(r.Context(), versionID, caseVersionID, reason, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCaseVersionRollbackDTO(view))
}
