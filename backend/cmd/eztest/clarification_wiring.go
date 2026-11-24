package main

// Clarification convergence composition. The agent runner observes that the
// clarification host asked the user to confirm the requirement drafts; the
// v0.22 StageService owns the actual clarifying→requirement_review edge
// (R-B4-1) and the requirement service proves the gate is not empty. This
// adapter is the only production caller of that forward edge from a Host turn.

import (
	"context"
	"errors"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// clarificationRequirementReader is the version requirement read surface the
// clarification convergence gate consumes (*versionapp.RequirementService
// satisfies it). Relocated here by v0.2.4-C T3 from the deleted explore assembly
// root: the interface is explore-agnostic and this is its only
// surviving consumer.
type clarificationRequirementReader interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
}

// stageClarificationConverger adapts the v0.22 stage service and requirement
// reader to the Runner's ClarificationConverger port. A version with no draft
// requirement is never advanced: the requirement gate would otherwise show an
// empty list.
type stageClarificationConverger struct {
	service      *versionapp.StageService
	requirements clarificationRequirementReader
}

func (c stageClarificationConverger) Converge(ctx context.Context, versionID int64) error {
	if c.service == nil {
		return errors.New("clarification converger: stage service is not configured")
	}
	if c.requirements != nil {
		views, _, err := c.requirements.List(ctx, versionID, string(versiondomain.RequirementDraft), 1, nil)
		if err != nil {
			return err
		}
		if len(views) == 0 {
			return nil
		}
	}
	return c.service.Request(ctx, versionID, versiondomain.StageRequirementReview)
}

var _ agentapp.ClarificationConverger = stageClarificationConverger{}
