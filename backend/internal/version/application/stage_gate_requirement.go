package versionapp

import (
	"context"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
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

// BatchConfirm confirms the selected requirements before the gate advances. It
// propagates the service's typed validation errors untouched (invalid_argument,
// not_found, state_conflict), so a failed confirmation never reaches the stage
// write.
func (c *requirementGateConfirmer) BatchConfirm(ctx context.Context, versionID int64, requirementIDs []int64) error {
	if c == nil || c.svc == nil {
		return fmt.Errorf("requirement gate confirmer is not configured")
	}
	var actorID int64
	if actor, ok := platform.ActorFrom(ctx); ok && actor.ID > 0 {
		actorID = actor.ID
	}
	_, err := c.svc.BatchConfirm(ctx, versionID, requirementIDs, actorID)
	return err
}

var _ RequirementGateConfirmer = (*requirementGateConfirmer)(nil)
