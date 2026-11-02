package versionapp

import (
	"context"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// BlockingWorkItemCounts splits a version's blocking work items into the total
// and the version.close tasks themselves. The version close service subtracts
// the latter so the predicate never counts its own task as blocking work
// (spec §7.3 check 2).
//
// The reader counts statuses open/claimed/running. waiting_human is not
// persisted on work items in P3; the state it represents is covered by the open
// human_request and paused-wave checks (P3-D-25).
type BlockingWorkItemCounts struct {
	Total        int
	VersionClose int
}

// VersionWorkItemReader is the agent task-board slice consumed by version close
// evaluation.
type VersionWorkItemReader interface {
	BlockingWorkItems(ctx context.Context, versionID int64) (BlockingWorkItemCounts, error)
}

// VersionExecutionReader is the execution-context slice consumed by version
// close evaluation. Waves in planned/running/paused are active (a paused wave
// keeps its slot and carries a pending human decision).
type VersionExecutionReader interface {
	CountActiveIntents(ctx context.Context, versionID int64) (int, error)
	CountActiveWaves(ctx context.Context, versionID int64) (int, error)
	CountInflightCaseRuns(ctx context.Context, versionID int64) (int, error)
}

// VersionHumanRequestReader is the execution-context human_request slice
// consumed by version close evaluation.
type VersionHumanRequestReader interface {
	CountOpenHumanRequests(ctx context.Context, versionID int64) (int, error)
}

// VersionRequirementReader is the version-context slice consumed by close
// evaluation: every logical requirement of one version regardless of status.
type VersionRequirementReader interface {
	ListByVersion(ctx context.Context, versionID int64) ([]version.Requirement, error)
}

// ReportStatusReader is the delivery-context report slice consumed by version
// close evaluation. The implementation must return the effective status: the
// latest kind='version' report counts as "published" only when the report row
// is published, its current revision is published with render_status=done and an
// HTML ref, and the payload carries that revision's input_fingerprint.
// CurrentFingerprint is the recomputed fingerprint of the current report input;
// VersionReportCurrent compares it with the stored fingerprint.
type ReportStatusReader interface {
	VersionReportStatus(ctx context.Context, versionID int64) (version.ReportStatus, error)
}

// VersionReportStaler is the delivery-context projection invoked after a
// completed version is reopened. It marks the latest version report stale and
// publishes report.stale. It is best effort: the reopen is already committed
// and the reconciler retries the projection.
type VersionReportStaler interface {
	MarkVersionReportStale(ctx context.Context, versionID int64, reason string) error
}

// VersionCloseTaskEnsurer ensures the steward version.close work item for the
// current report revision and predicate fingerprint. It is idempotent: the
// dedupe key is ver-close:{version_id}:{report_revision_id}:{fingerprint}, so a
// changed predicate naturally produces a new item.
type VersionCloseTaskEnsurer interface {
	EnsureVersionClose(ctx context.Context, versionID, reportRevisionID int64, predicateFingerprint string) error
}
