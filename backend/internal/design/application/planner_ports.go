package designapp

import (
	"context"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// P2 design task types (contracts/task-types.md).
const (
	TaskTypeDesignRound       = "design.round"
	TaskTypeDesignRequirement = "design.requirement"
	TaskTypeDesignGap         = "design.gap"
	TaskTypeDesignCritic      = "design.critic"
	TaskTypeDesignDataPlan    = "design.data_plan"
)

// TeamDesign is the design team-room channel.
const TeamDesign = "design"

// TaskInput is the local work-queue contract consumed by the design planner. It
// mirrors the P1 work_item dedupe semantics without importing the agent context:
// main adapts it to the agent work-queue service.
type TaskInput struct {
	VersionID int64
	Team      string
	Type      string
	DedupeKey string
	Payload   map[string]any
	Priority  int
}

// TaskEnqueuer ensures one work item exists. It must be idempotent on
// (version_id, type, dedupe_key); callers treat enqueue as best effort.
type TaskEnqueuer interface {
	Ensure(ctx context.Context, in TaskInput) (bool, error)
}

// ConversationCreator creates the auto-design conversation. main adapts it to
// the version conversation service.
type ConversationCreator interface {
	CreateDesign(ctx context.Context, versionID int64, title string) (int64, error)
}

// VersionStarter projects a version from initial to running. main adapts it to
// versionapp.Service.MarkRunning.
type VersionStarter interface {
	MarkRunning(ctx context.Context, versionID int64) error
}

// VersionReopener clears the completed projection after a design write that
// changes the version scope (spec §7.5: a newly confirmed case reopens a
// completed version). main adapts it to
// versionapp.VersionCloseService.ReopenIfCompleted; the call is best effort and
// the reconciler is the fallback.
type VersionReopener interface {
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// Staler marks the confirmed current case versions of a changed requirement
// stale. StaleService implements it; the reconciler stale safety net uses it.
type Staler interface {
	MarkRequirementChanged(ctx context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error)
}

// GapInput requests design work for an execution gap or an uncovered
// requirement version (spec §7.5).
type GapInput struct {
	VersionID            int64
	RequirementID        int64
	CaseID               *int64
	CaseVersionID        *int64
	RequirementVersionID *int64
	Reason               string
}

// CaseCounts is the current-case projection consumed by the version detail.
type CaseCounts struct {
	Draft     int
	Confirmed int
	Stale     int
}

// requirementRefsUnion merges requirement refs by ID, keeping the first
// occurrence and returning a deterministic ID order.
func requirementRefsUnion(groups ...[]design.RequirementRef) []design.RequirementRef {
	seen := map[int64]struct{}{}
	merged := make([]design.RequirementRef, 0)
	for _, group := range groups {
		for _, ref := range group {
			if ref.ID <= 0 {
				continue
			}
			if _, ok := seen[ref.ID]; ok {
				continue
			}
			seen[ref.ID] = struct{}{}
			merged = append(merged, ref)
		}
	}
	return merged
}

// requirementIDs extracts IDs from refs in order.
func requirementIDs(refs []design.RequirementRef) []int64 {
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

// requirementVersionIDs extracts current version IDs from refs in order.
func requirementVersionIDs(refs []design.RequirementRef) []int64 {
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.CurrentVersionID)
	}
	return ids
}
