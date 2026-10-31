package versionapp

import (
	"context"
	"errors"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// LatestClosure returns the newest closure round summary of one requirement. A
// requirement that never closed reports found=false and no error. The method is
// the read surface the HTTP layer uses to project RequirementView.closure
// (spec §4.2); it never recomputes the predicate.
func (s *ClosureService) LatestClosure(ctx context.Context, requirementID int64) (RequirementClosureView, bool, error) {
	if requirementID <= 0 {
		return RequirementClosureView{}, false, platform.Invalid("requirement_id", "must be a positive decimal ID")
	}
	if s == nil || s.closures == nil {
		return RequirementClosureView{}, false, errors.New("closure service is missing its closure repository")
	}
	closure, found, err := s.closures.Latest(ctx, requirementID)
	if err != nil {
		return RequirementClosureView{}, false, mapClosureRepositoryError(err)
	}
	if !found {
		return RequirementClosureView{}, false, nil
	}
	return RequirementClosureView{
		ID:          closure.ID,
		CloseRound:  closure.CloseRound,
		ClosedAt:    closure.ClosedAt,
		ReopenCount: closure.ReopenCount,
	}, true, nil
}
