// Package application holds the P7 delivery use cases and the ports they consume
// from the outside world. The issue service is the transaction/orchestration
// owner: it validates the §5.4 issue rules, delegates persistence to IssueStore,
// delegates cross-context reference ownership to IssueRefValidator, and publishes
// issue events/audit after the commit.
//
// This file is the single home of the issue cross-context ports. It contains
// value types and interfaces only: no pgx, HTTP or other-context imports.
package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// ErrIssueDedupeConflict reports that Create lost the (version_id, dedupe_key)
// unique-key race to a concurrent transaction. It is an internal signal and is
// never returned to HTTP: ApplyDrafts re-reads the now-visible issue and merges
// into it with one retry outside the failed transaction (spec §6.9).
var ErrIssueDedupeConflict = errors.New("delivery: issue dedupe key already exists")

// IssueStore is the issue aggregate's persistence port. Implementations own the
// (version_id, dedupe_key) uniqueness, the issue_ref rows and the revision CAS
// (spec §5.4, §6.9); the service owns the state machine, the dedupe/merge
// decisions and the cross-context reference validation via IssueRefValidator.
//
// InTx must run fn inside one database transaction. When the store is already
// bound to a transaction it must join it; when it is bound to a pool it must
// open one. Unit tests use an in-memory fake, the production adapter is the
// delivery postgres repository.
type IssueStore interface {
	InTx(ctx context.Context, fn func(IssueStore) error) error
	// Create inserts the issue and its refs atomically. A lost
	// (version_id, dedupe_key) race is ErrIssueDedupeConflict.
	Create(ctx context.Context, issue domain.Issue, refs []domain.IssueRef) (domain.Issue, error)
	// Get returns one issue of versionID. found is false when the row does not
	// exist in that version, so a foreign issue is indistinguishable from a
	// missing one.
	Get(ctx context.Context, versionID, issueID int64) (issue domain.Issue, found bool, err error)
	// FindByDedupeKey looks up the issue with the same (version_id, dedupe_key).
	FindByDedupeKey(ctx context.Context, versionID int64, dedupeKey string) (issue domain.Issue, found bool, err error)
	// Update applies the CAS write: expectedRevision must equal the stored
	// revision. A mismatch is issue_state_conflict.
	Update(ctx context.Context, issue domain.Issue, expectedRevision int) (domain.Issue, error)
	// AddRefs appends refs idempotently (an existing (issue_id, ref_kind,
	// ref_id) row is left untouched).
	AddRefs(ctx context.Context, issueID int64, refs []domain.IssueRef) error
	// ListRefs returns every ref of one issue.
	ListRefs(ctx context.Context, issueID int64) ([]domain.IssueRef, error)
	// List returns one keyset page of the version's issues. Cursor is the
	// exclusive issue id; hasMore reports that more rows match the filter.
	List(ctx context.Context, versionID int64, filter IssueFilter) (issues []domain.Issue, hasMore bool, err error)
}

// IssueRefValidator is the cross-context port that checks every durable issue
// reference (spec §5.4): the per-kind format rules, that the target exists and
// that it belongs to versionID. A step_result must also belong to the referenced
// case_run when both are present. A violation is the issue_ref_invalid domain
// error.
//
// Implementations are delivery infrastructure adapters over the owning contexts'
// application interfaces (execution/mindmap/version/codeintel, wired by T17):
// the delivery context never reads those tables itself (spec §3.2, Agent.md
// §6.2). code_anchor refs are backfilled by the codeintel anchor adapter after
// the issue exists, so subject ownership is enforced there, not here.
type IssueRefValidator interface {
	ValidateRefs(ctx context.Context, versionID int64, refs []domain.IssueRef) error
}

// IssueVersionReader reports whether a version exists. Every issue create path
// checks it before writing so a nonexistent target version cannot create an
// orphan issue (spec §7.3). Implementations are delivery infrastructure
// adapters over the version application service; delivery never reads the
// version table itself (spec §3.2, Agent.md §6.2).
type IssueVersionReader interface {
	VersionExists(ctx context.Context, versionID int64) (bool, error)
}

// IssueIDGenerator mints Snowflake ids. Every delivery id is generated in the
// application layer and passed explicitly to the repository (D29).
type IssueIDGenerator interface {
	Next() int64
}

// IssueClock returns the current time; injectable so the service can be tested
// without sleeping.
type IssueClock interface {
	Now() time.Time
}

// IssueEvent is one delivery event published on the version channel (spec §8.1).
// Detail carries ids as decimal strings and status/count summaries only: never
// an issue body, an evidence body or a prompt.
type IssueEvent struct {
	Type           string
	VersionID      int64
	IssueID        int64
	Summary        string
	Detail         map[string]any
	RefCaseIDs     []int64
	RefEvidenceIDs []int64
}

// IssueEventPublisher publishes one delivery event. The service publishes only
// after the transaction committed and swallows a publication failure (spec
// §8.2: publish failures are logged, never rolled back).
type IssueEventPublisher interface {
	Publish(ctx context.Context, event IssueEvent) error
}

// IssueAuditEvent is one delivery audit entry. It carries identifiers, status
// and counts only: never an issue body or evidence content (spec §11.2).
type IssueAuditEvent struct {
	Action     string
	ActorID    int64
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

// IssueAuditRecorder records one delivery audit entry. A failure is logged and
// never fails the request (P6 audit convention).
type IssueAuditRecorder interface {
	Record(ctx context.Context, event IssueAuditEvent) error
}

// IssueServiceDeps are the ports of the issue use cases. Store is required;
// Versions is required for issue creation (an unwired reader fails closed);
// Refs is required whenever an issue carries references (writes without it fail
// closed); IDs is required for writes (no process fallback); Clock falls back to
// the process clock; Events and Audit are optional (a nil sink disables
// publication/auditing).
type IssueServiceDeps struct {
	Store    IssueStore
	Versions IssueVersionReader
	Refs     IssueRefValidator
	IDs      IssueIDGenerator
	Clock    IssueClock
	Events   IssueEventPublisher
	Audit    IssueAuditRecorder
}
