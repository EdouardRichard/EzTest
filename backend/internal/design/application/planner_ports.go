package designapp

import (
	"context"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// P2 design task types (contracts/task-types.md).
const (
	TaskTypeDesignRequirement = "design.requirement"
	TaskTypeDesignGap         = "design.gap"
	TaskTypeDesignCritic      = "design.critic"
	// TaskTypeDesignJudge is the independent judge run (spec SC-E3/SC-E4). It is
	// a 0.2.1 type-path work item in the same family as design.requirement /
	// design.critic, NOT a kind-path stage-entry job: the actorMayCreate /
	// requiredStage matrices are kind-path only and are deliberately untouched.
	TaskTypeDesignJudge = "design.judge"
	// TaskTypeDesignRework is the author revision triggered by a judge fail
	// within the rework budget (spec SC-E4). Same type-path family as above.
	TaskTypeDesignRework = "design.rework"
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
	// EnsureCoverageRepair ensures one fresh design.requirement generation for a
	// requirement version that is still uncovered. in.DedupeKey is the base key
	// of the first generation; the adapter derives a new generation key while
	// the previous generation is terminal, never duplicates an in-flight
	// generation, and enforces the bounded repair budget so a persistently
	// failing model cannot stampede the queue (Task 70). When the budget is
	// exhausted the adapter records an author_exhausted escalation fact for the
	// requirement revision (AuthorExhaustedStore) instead of leaving the
	// requirement permanently uncovered with no record (H-F5b, spec SC-E4
	// "无永久 pending"); an in-flight or retry-backed generation is a normal wait
	// and is never escalated.
	EnsureCoverageRepair(ctx context.Context, in TaskInput, requirementVersionID int64) (bool, error)
	// EnsureJudgeGeneration ensures one fresh design.judge generation for a
	// requirement version whose coverage union is complete (spec SC-E4).
	// in.DedupeKey is the base judge key (judge:<req>:<reqver>). drafts is the
	// FULL set of current draft case versions of the requirement: it anchors the
	// generation budget, so any change to the set (a revised version, an added
	// or removed draft) starts a fresh budget. pending is the subset of drafts
	// with no judgment matching their current version content: it is the run's
	// review scope (carried per case in in.Payload["cases"]) and the exhaustion
	// target set. The adapter serializes per requirement version (an in-flight
	// judge run is never duplicated), advances a monotonic dedupe sequence so a
	// reset budget cannot reuse an occupied key, and bounds the generation
	// budget per draft set. When the budget is exhausted the adapter records a
	// judge_exhausted escalation fact for EVERY pending case version and emits
	// design.judge_exhausted per case, instead of leaving a permanent pending (T8).
	EnsureJudgeGeneration(ctx context.Context, in TaskInput, drafts, pending []JudgeCaseRef) (bool, error)
}

// JudgeCaseRef identifies one current draft case version included in a judge
// generation. ContentHash binds the review to the exact immutable content the
// scheduler saw (the judge fact settles against the same server-read hash).
type JudgeCaseRef struct {
	CaseID        int64
	CaseVersionID int64
	ContentHash   string
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

// DesignConverger applies the designing -> case_review edge once the version's
// activated requirements have usable case coverage (Task 69). main adapts it to
// versionapp.StageMachine.ConvergeDesign; nil disables the edge for degraded
// assemblies.
type DesignConverger interface {
	ConvergeDesign(ctx context.Context, versionID int64) error
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
