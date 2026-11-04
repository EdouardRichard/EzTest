package versionapp

import (
	"context"
	"fmt"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// requirementGateConfirmer adapts RequirementService.BatchConfirm to the stage
// machine's narrow RequirementGateConfirmer port. The actor is taken from the
// request context: the HTTP layer has already authorized the user-only gate, so
// the service records the same actor in its confirmation/audit rows.
type requirementGateConfirmer struct {
	svc *RequirementService
}

// NewRequirementGateConfirmer wires the requirement batch-confirm use case into
// the requirement hard gate.
func NewRequirementGateConfirmer(svc *RequirementService) RequirementGateConfirmer {
	return &requirementGateConfirmer{svc: svc}
}

// BatchConfirm confirms the selected requirements before the gate advances. An
// empty requirementIDs selection means "confirm all current draft requirements"
// (contract StageConfirmRequirementsRequest), so it is expanded to the version's
// draft requirements first; a version with no draft requirement is a typed 409
// no_requirements_to_confirm rather than an empty batch or a stage advance.
// Service validation errors (invalid_argument, not_found, state_conflict)
// propagate untouched, so a failed confirmation never reaches the stage write.
func (c *requirementGateConfirmer) BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64) error {
	if c == nil || c.svc == nil {
		return fmt.Errorf("requirement gate confirmer is not configured")
	}
	ids := requirementIDs
	if len(ids) == 0 {
		expanded, err := c.draftRequirementIDs(ctx, versionID)
		if err != nil {
			return err
		}
		if len(expanded) == 0 {
			return platform.NewError(http.StatusConflict, "no_requirements_to_confirm",
				"the version has no draft requirement to confirm")
		}
		ids = expanded
	}
	var actorID int64
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID = actor.ID
	}
	_, err := c.svc.BatchConfirm(ctx, versionID, ids, actorID)
	return err
}

// draftRequirementIDs pages through the version's current draft requirements
// (ID-descending) so an empty gate selection can be expanded to the full list.
func (c *requirementGateConfirmer) draftRequirementIDs(ctx context.Context, versionID int64) ([]int64, error) {
	ids := make([]int64, 0)
	var beforeID *int64
	for {
		views, hasMore, err := c.svc.List(ctx, versionID, string(version.RequirementDraft), pagination.MaxLimit, beforeID)
		if err != nil {
			return nil, err
		}
		for _, view := range views {
			ids = append(ids, view.ID)
		}
		if !hasMore || len(views) == 0 {
			return ids, nil
		}
		lastID := views[len(views)-1].ID
		beforeID = &lastID
	}
}

var _ RequirementGateConfirmer = (*requirementGateConfirmer)(nil)
