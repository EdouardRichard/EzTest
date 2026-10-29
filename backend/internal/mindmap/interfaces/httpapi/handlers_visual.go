package httpapi

import (
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// updateVisualAssertionRequest is PUT /assertions/{assertionId}/visual (design
// spec §9.3). The node revision is the same OCC token as every graph write.
type updateVisualAssertionRequest struct {
	NodeRevision      int                    `json:"nodeRevision"`
	Selector          string                 `json:"selector"`
	Region            *visualRegionRequest   `json:"region"`
	Threshold         float64                `json:"threshold"`
	Viewport          *visualViewportRequest `json:"viewport"`
	DeviceScaleFactor float64                `json:"deviceScaleFactor"`
}

// visualRegionRequest is the normalized {x,y,width,height} crop.
type visualRegionRequest struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// visualViewportRequest is the expected browser viewport of the comparison.
type visualViewportRequest struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

// approveVisualBaselineRequest is POST /assertions/{assertionId}/visual/baseline.
type approveVisualBaselineRequest struct {
	EvidenceID string `json:"evidenceId"`
}

// updateVisualAssertion handles PUT /assertions/{assertionId}/visual (200).
func (h *Handler) updateVisualAssertion(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertionID, err := pathID(r, "assertionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body updateVisualAssertionRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	cmd := application.UpdateVisualAssertionCommand{
		NodeRevision:      body.NodeRevision,
		Selector:          body.Selector,
		Threshold:         body.Threshold,
		DeviceScaleFactor: body.DeviceScaleFactor,
	}
	if body.Region != nil {
		cmd.Region = &domain.VisualRegion{
			X: body.Region.X, Y: body.Region.Y,
			Width: body.Region.Width, Height: body.Region.Height,
		}
	}
	if body.Viewport != nil {
		cmd.ViewportWidth = body.Viewport.Width
		cmd.ViewportHeight = body.Viewport.Height
	}
	visual, err := h.deps.Visual.Update(ctx, versionID, assertionID, actorID(ctx), cmd)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newVisualAssertionDTO(visual))
}

// approveVisualBaseline handles POST /assertions/{assertionId}/visual/baseline
// (200). Only an administrator may approve a baseline (design spec §9.3); the
// role is enforced here because the service is actor-agnostic.
func (h *Handler) approveVisualBaseline(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if err := requireAdmin(ctx, "forbidden", "approving a visual baseline requires the admin role"); err != nil {
		writeServiceError(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	assertionID, err := pathID(r, "assertionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body approveVisualBaselineRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	evidenceID, err := parseIDString("evidenceId", body.EvidenceID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if evidenceID <= 0 {
		writeServiceError(w, r, domain.NewInvalid("evidenceId", "invalid_evidence_id",
			"evidenceId must be a positive decimal id"))
		return
	}
	visual, err := h.deps.Visual.ApproveBaseline(ctx, versionID, assertionID, actorID(ctx), evidenceID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newVisualAssertionDTO(visual))
}
