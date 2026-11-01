package versionapp

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// Closure summary statuses. A closure round whose requirement was reopened is
// still the newest historical round, so read models label it "reopened" until a
// new close round supersedes it (spec §7.5/§8.2).
const (
	ClosureSummaryStatusClosed   = "closed"
	ClosureSummaryStatusReopened = "reopened"
)

// ClosureSummary is the bounded closure projection consumed by the exploration
// chain provider. IDs stay int64 in the application layer; the chain provider
// maps them onto stable node IDs and decimal-string metadata.
type ClosureSummary struct {
	ID            int64
	RequirementID int64
	CloseRound    int
	Status        string
	ReopenCount   int
	ClosedAt      time.Time
}

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

// ClosedForVersion reports whether the newest closure round belongs to the
// given requirement version. It is the requirement.close task finalizer guard:
// a stale closure from an older requirement version must not satisfy a task for
// the current version.
func (s *ClosureService) ClosedForVersion(ctx context.Context, requirementID, requirementVersionID int64) (bool, error) {
	if requirementID <= 0 || requirementVersionID <= 0 {
		return false, platform.Invalid("requirement_id", "requirement_id and requirement_version_id must be positive decimal IDs")
	}
	if s == nil || s.closures == nil {
		return false, errors.New("closure service is missing its closure repository")
	}
	closure, found, err := s.closures.Latest(ctx, requirementID)
	if err != nil {
		return false, mapClosureRepositoryError(err)
	}
	return found && closure.RequirementVersionID == requirementVersionID, nil
}

// SummariesByRequirementIDs returns the newest closure summary of each requested
// requirement that belongs to versionID and has a closure round. Missing,
// duplicate, non-positive and foreign-version IDs are skipped, mirroring the
// design history reader used by the same read model. Status is "reopened" when
// the requirement is no longer closed or the newest round already recorded a
// reopen; the closure node must not claim "closed" for a reopened requirement.
//
// The method is the version-context application boundary the chain delivery
// provider consumes (spec §8.1); the provider never sees the closure repository.
func (s *ClosureService) SummariesByRequirementIDs(ctx context.Context, versionID int64, requirementIDs []int64) (map[int64]ClosureSummary, error) {
	if versionID <= 0 {
		return nil, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s == nil || s.requirements == nil || s.closures == nil {
		return nil, errors.New("closure service is missing its requirement or closure repository")
	}
	summaries := make(map[int64]ClosureSummary, len(requirementIDs))
	seen := make(map[int64]bool, len(requirementIDs))
	for _, requirementID := range requirementIDs {
		if requirementID <= 0 || seen[requirementID] {
			continue
		}
		seen[requirementID] = true
		requirement, err := s.requirements.GetRequirement(ctx, requirementID)
		if err != nil {
			if errors.Is(err, version.ErrRequirementNotFound) {
				continue
			}
			return nil, mapRequirementRepositoryError(err)
		}
		if requirement.VersionID != versionID {
			continue
		}
		closure, found, err := s.closures.Latest(ctx, requirementID)
		if err != nil {
			return nil, mapClosureRepositoryError(err)
		}
		if !found || closure.ID <= 0 {
			continue
		}
		status := ClosureSummaryStatusClosed
		if requirement.Status != version.RequirementClosed || closure.ReopenCount > 0 {
			status = ClosureSummaryStatusReopened
		}
		summaries[requirementID] = ClosureSummary{
			ID:            closure.ID,
			RequirementID: requirement.ID,
			CloseRound:    closure.CloseRound,
			Status:        status,
			ReopenCount:   closure.ReopenCount,
			ClosedAt:      closure.ClosedAt,
		}
	}
	return summaries, nil
}
