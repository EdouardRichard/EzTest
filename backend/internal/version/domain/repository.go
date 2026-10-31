package version

import (
	"context"
	"errors"
	"time"
)

// Repository persistence errors.
var (
	ErrNotFound  = errors.New("version not found")
	ErrDuplicate = errors.New("version binding already exists")
)

// Summary is the list read model for one version.
type Summary struct {
	ID        int64
	Title     string
	Status    string
	SUTCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// VersionStats is the P1 detail increment: per-status requirement counts, the
// number of conversations and the pending-design placeholder.
type VersionStats struct {
	Requirements      RequirementCounts
	ConversationCount int
}

// Repository persists versions, their SUT bindings and the selected database/
// account rows. Create must write the whole aggregate in one transaction.
type Repository interface {
	Create(ctx context.Context, version Version, bindings []Binding) error
	Get(ctx context.Context, id int64) (Version, []Binding, error)
	List(ctx context.Context, limit int, beforeID *int64) ([]Summary, error)
	Update(ctx context.Context, version Version) (Version, error)

	// MarkRunning projects the version from initial to running. It reports
	// whether this call changed the status; the WHERE guard makes it idempotent.
	MarkRunning(ctx context.Context, id int64, updatedAt time.Time) (bool, error)
	// Reopen projects a completed version back to running and clears the close
	// fields (P3 §5.9). It reports whether this call changed the row.
	Reopen(ctx context.Context, id int64, updatedAt time.Time) (bool, error)
	// Stats returns the P1 detail aggregates for one version.
	Stats(ctx context.Context, id int64) (VersionStats, error)
	// ListInitialVersionsWithContent returns initial versions that already have a
	// conversation message or requirement; the P1 reconciler uses it to fix a
	// missed running projection.
	ListInitialVersionsWithContent(ctx context.Context, limit int) ([]int64, error)

	// Usage guards answer whether a resource is still referenced by a version
	// binding. They back resourceapp.UsageChecker.
	SUTInUse(ctx context.Context, id int64) (bool, error)
	EnvironmentInUse(ctx context.Context, id int64) (bool, error)
	PageInUse(ctx context.Context, id int64) (bool, error)
	APIInUse(ctx context.Context, id int64) (bool, error)
	DatabaseInUse(ctx context.Context, id int64) (bool, error)
	AccountInUse(ctx context.Context, id int64) (bool, error)
	RepositoryInUse(ctx context.Context, id int64) (bool, error)
}

// VersionCloseRepository persists the version close/reopen projection (spec
// §7.3-§7.5). Close flips running -> completed only while holding the version
// row lock and re-runs the predicate inside that transaction through the
// recheck callback, so a close cannot commit against an already stale
// evaluation. ReopenIfCompleted flips completed -> running and clears the close
// fields; a version in any other status is a no-op.
type VersionCloseRepository interface {
	// Get loads one version. It is the read used by Evaluate and by the
	// reconcile loop.
	Get(ctx context.Context, id int64) (Version, []Binding, error)
	// Close completes one running version. The callback runs after the row
	// lock is acquired and before the conditional UPDATE; a callback error
	// rolls the transaction back unchanged. A missing version yields
	// ErrNotFound; a version that is not running yields ErrStateConflict.
	Close(ctx context.Context, id int64, close VersionCloseFields, recheck func(context.Context, Version) error) (Version, error)
	// ReopenIfCompleted returns the version after a completed -> running
	// transition and whether this call changed the row. A version already
	// running (or still initial) is returned unchanged with changed=false.
	ReopenIfCompleted(ctx context.Context, id int64, reopenedAt time.Time) (Version, bool, error)
}

// RequirementClosureRepository persists requirement_closure rounds. Rows are
// append-only: Close inserts the next close_round and marks the requirement
// closed in one transaction; Reopen increments the newest row's reopen_count
// and returns the requirement to draft in one transaction. The unique
// (requirement_id, close_round) constraint and the requirement row lock make
// concurrent Close calls mutually exclusive. Close re-runs the closure
// predicate inside that transaction through the recheck callback, so a close
// cannot commit against an already stale evaluation.
type RequirementClosureRepository interface {
	// Latest returns the newest closure row of one requirement. A requirement
	// that was never closed yields found=false with a nil error.
	Latest(ctx context.Context, requirementID int64) (RequirementClosure, bool, error)
	// Close inserts the next close round and marks the requirement closed. The
	// callback runs after the requirement row lock is acquired and before the
	// closure insert; a callback error rolls the transaction back unchanged. A
	// requirement that is no longer clarified at commit time yields
	// ErrStateConflict. The returned closure carries the assigned round.
	Close(ctx context.Context, closure RequirementClosure, requirement Requirement, recheck func(context.Context, Requirement) error) (RequirementClosure, error)
	// Reopen increments the newest closure's reopen_count and moves the
	// requirement closed -> draft, clearing closed_at/closed_by_run_id. A
	// requirement that is not closed yields ErrStateConflict.
	Reopen(ctx context.Context, requirement Requirement, reopenedAt time.Time) (RequirementClosure, error)
}
