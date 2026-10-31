package versionapp

import (
	"context"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ClosureRequirementReader is the version-context slice consumed by closure:
// lookup by ID (the agent tool receives only the requirement ID) plus the
// clarified scan of one version used by the reconciler.
type ClosureRequirementReader interface {
	GetRequirement(ctx context.Context, requirementID int64) (version.Requirement, error)
	List(ctx context.Context, versionID int64, status version.RequirementStatus, limit int, beforeID *int64) ([]version.RequirementListItem, error)
}

// ClosureCaseReader is the design-context slice consumed by the requirement
// closure service: the current+confirmed case versions of one requirement.
type ClosureCaseReader interface {
	CurrentConfirmedCases(ctx context.Context, requirementID int64) ([]version.CaseVersionSnapshot, error)
}

// ClosureRunReader is the execution-context slice consumed by closure
// evaluation: latest case runs plus the count of in-flight execution work.
type ClosureRunReader interface {
	// LatestCaseRuns returns the newest case_run (id DESC) per case version of
	// the requirement; cases that never ran are absent.
	LatestCaseRuns(ctx context.Context, requirementID int64) (map[int64]version.CaseRunSnapshot, error)
	// CountActiveIntents counts open/claimed/running execution_intent rows of
	// the requirement's current+confirmed case versions.
	CountActiveIntents(ctx context.Context, requirementID int64) (int, error)
	// CountInflightCaseRuns counts pending/running case_run rows of those cases.
	CountInflightCaseRuns(ctx context.Context, requirementID int64) (int, error)
}

// ClosureCoverageReader reports whether every current+confirmed case version of
// the requirement appears in the design coverage statistics. A missing or
// unreadable projection is reported as false, not as an error, because the
// check treats absence as not recorded.
type ClosureCoverageReader interface {
	CoverageRecorded(ctx context.Context, requirementID int64) (bool, error)
}

// ClosureIssueRefReader is the delivery-context slice consumed by closure
// evaluation: every issue reference of the requirement's case runs, regardless
// of the issue status (an open issue ref does not block closure).
type ClosureIssueRefReader interface {
	CaseRunIssueRefs(ctx context.Context, requirementID int64) (map[int64][]int64, error)
}

// OpenWorkItemCounts splits a requirement's open work items into the total and
// the requirement.close tasks. The closure service subtracts the latter so the
// predicate never counts the closure task itself as blocking work.
type OpenWorkItemCounts struct {
	Total            int
	RequirementClose int
}

// ClosureWorkItemReader is the agent task-board slice consumed by closure
// evaluation. It counts open/claimed/running work items scoped to one
// requirement (design.gap, design.requirement, requirement.close, ...).
type ClosureWorkItemReader interface {
	OpenWorkItems(ctx context.Context, requirementID int64) (OpenWorkItemCounts, error)
}

// VersionReopener is the version-context reopen slice. The closure service
// calls it after a requirement reopen; the implementation is the version close
// service, and the reconciler remains the fallback when it is best-effort.
type VersionReopener interface {
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// ClosureTaskEnsurer ensures the requirement.close steward work item for one
// requirement version and close round. It is idempotent: repeated ensures for
// the same (requirement, requirement version, close round) return the existing
// item, while a new revision or close round produces a new work item.
type ClosureTaskEnsurer interface {
	EnsureRequirementClose(ctx context.Context, requirementID, requirementVersionID int64, closeRound int32) error
}
