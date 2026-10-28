package application

import (
	"context"
	"io"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Report event types and audit actions of spec §8.1/§11.2. The report service
// publishes report.published and report.stale on the version channel and records
// delivery.report.published/delivery.report.exported; none of these entries ever
// carries report content, file paths or credentials.
const (
	// ReportEventPublished is emitted after a new revision is committed.
	ReportEventPublished = "report.published"
	// ReportEventStale is emitted once when a published report is marked stale.
	ReportEventStale = "report.stale"

	// ReportActionPublished is the audit action of a published revision.
	ReportActionPublished = "delivery.report.published"
	// ReportActionExported is the audit action of a verified Markdown export.
	ReportActionExported = "delivery.report.exported"
)

// Report stale reasons. The reason is persisted in report.stale_reason and in
// the report.stale event detail.
const (
	// ReportStaleReasonInputChanged means the recomputed fingerprint differs.
	ReportStaleReasonInputChanged = "input_fingerprint_changed"
)

// ReportFilter is the version-scoped report list filter of spec §7.4. BatchID is
// optional; Status is empty or one of draft/published/stale; Cursor is the
// opaque keyset cursor; Limit is clamped to platform.DefaultLimit/MaxLimit.
type ReportFilter struct {
	BatchID *int64
	Status  string
	Cursor  string
	Limit   int
}

// ReportVersionSource is the version header projection of a report.
type ReportVersionSource struct {
	ID                 int64
	Name               string
	Label              string
	Status             string
	CodeBaselineCommit string
}

// ReportBatchSource is the batch projection a report is generated from. It
// carries ids/counts/status only: never evidence bodies, credentials or file
// paths. FlakyCaseIDs and Degraded are computed by the execution-context adapter
// from the batch's attempts and the autonomy chain.
type ReportBatchSource struct {
	ID             int64
	VersionID      int64
	BatchNo        int
	Kind           string
	Status         string
	BaselineHash   string
	GraphHash      string
	CodeBaseCommit string
	HeadCommit     string
	StartedAt      *time.Time
	FinishedAt     *time.Time
	Environment    string
	System         string

	CaseCount         int
	PassedCount       int
	FailedCount       int
	BlockedCount      int
	InconclusiveCount int
	SkippedCount      int
	InterruptedCount  int
	FlakyCaseIDs      []int64
	Degraded          bool
}

// ReportCaseRunSource is the latest attempt of one case node in the batch.
type ReportCaseRunSource struct {
	CaseRunID      int64
	CaseNodeID     int64
	Title          string
	Source         string
	Status         string
	FailureClass   string
	Attempt        int
	StepCount      int
	AssertionCount int
	EvidenceCount  int
	UpdatedAt      time.Time
}

// ReportRequirementSource is one version requirement with the case nodes it
// covers. Only ids/titles/status and case ids are exposed.
type ReportRequirementSource struct {
	RequirementID int64
	Title         string
	Status        string
	CaseNodeIDs   []int64
}

// ReportIssueSource is one issue of the report: the issues linked to the batch
// plus every open/confirmed issue of the version (deduplicated by the adapter).
// The body fields (description, repro steps, actual, evidence) are deliberately
// absent.
type ReportIssueSource struct {
	IssueID    int64
	Title      string
	Severity   string
	Status     string
	Confidence string
	Category   string
	CaseNodeID *int64
	CodeRefs   []domain.ReportCodeRef
	Revision   int
	UpdatedAt  time.Time
}

// ReportVersionReader loads the version header of a report.
type ReportVersionReader interface {
	GetReportVersion(ctx context.Context, versionID int64) (ReportVersionSource, error)
}

// ReportExecutionReader loads the batch, its latest case runs and the derived
// autonomy signals a report needs. It reads the execution context through its
// application boundary only.
type ReportExecutionReader interface {
	GetReportBatch(ctx context.Context, versionID, batchID int64) (ReportBatchSource, error)
	ListReportCaseRuns(ctx context.Context, batchID int64) ([]ReportCaseRunSource, error)
	// PreviousBatchID resolves the id of the batch immediately before batchID
	// in versionID (batch_no-1). ok is false when batchID is the first batch of
	// the version. The resolution never depends on a report's created_at: the
	// latest report of that previous batch is the default diff base even when
	// it was regenerated after the target report (spec §6.5/§7.4).
	PreviousBatchID(ctx context.Context, versionID, batchID int64) (prevBatchID int64, ok bool, err error)
}

// ReportCaseGraphReader loads the version requirements and their case coverage.
type ReportCaseGraphReader interface {
	ListReportRequirements(ctx context.Context, versionID int64) ([]ReportRequirementSource, error)
}

// ReportIssueReader loads the issues that belong in a batch report.
type ReportIssueReader interface {
	ListReportIssues(ctx context.Context, versionID, batchID int64) ([]ReportIssueSource, error)
}

// ReportStore persists immutable report revisions and their report_ref rows.
type ReportStore interface {
	// Create inserts a fully populated revision. A duplicate
	// (batch_id, revision) is reported as domain.ErrReportRevisionConflict.
	Create(ctx context.Context, report domain.Report) (domain.Report, error)
	// Get returns one revision of versionID, or domain.ErrReportNotFound.
	Get(ctx context.Context, versionID, reportID int64) (domain.Report, error)
	// LatestByBatch returns the highest revision of the batch.
	LatestByBatch(ctx context.Context, versionID, batchID int64) (domain.Report, error)
	// List returns one page newest first with an opaque keyset cursor. List
	// items omit Content and Markdown metadata.
	List(ctx context.Context, versionID int64, filter ReportFilter) ([]domain.Report, string, bool, error)
	// NextRevision returns max(revision)+1 of the batch.
	NextRevision(ctx context.Context, versionID, batchID int64) (int, error)
	// MarkStale flips published→stale with a CAS on the stored fingerprint.
	MarkStale(ctx context.Context, versionID, reportID int64, fingerprint, reason string, at time.Time) (bool, error)
	// MarkPublished flips draft→published with a CAS on the status.
	MarkPublished(ctx context.Context, versionID, reportID int64, at time.Time) (bool, error)
	// Delete removes a revision whose Markdown file could not be written.
	Delete(ctx context.Context, versionID, reportID int64) error
}

// ReportFileStore is the Markdown filesystem port of spec §6.5/D30: root-relative
// paths only, atomic write, 0600 file / 0700 directory, and the returned sha256
// and size are recorded on the report row.
type ReportFileStore interface {
	Write(ctx context.Context, rel string, data []byte) (path, sha256Hex string, size int64, err error)
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
}

// ReportEvent is one delivery event published on the version channel.
// Detail carries ids/counts/status only.
type ReportEvent struct {
	VersionID int64
	BatchID   int64
	Type      string
	Summary   string
	ActorID   int64
	Detail    map[string]any
}

// ReportEventPublisher publishes one delivery event after the database commit.
// A publish failure is logged, never returned to the caller.
type ReportEventPublisher interface {
	PublishReportEvent(ctx context.Context, event ReportEvent) error
}

// ReportAuditEvent is one audit entry. Report content and file paths are never
// part of it (spec §11.2).
type ReportAuditEvent struct {
	Action     string
	ActorID    int64
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

// ReportAuditRecorder records one delivery audit entry. A failure is logged,
// never returned.
type ReportAuditRecorder interface {
	RecordReportAudit(ctx context.Context, event ReportAuditEvent) error
}

// ReportIDGenerator mints Snowflake ids in the application layer (D29).
type ReportIDGenerator interface {
	Next() int64
}

// ReportClock returns the current time; injectable so report generation is
// deterministic in tests.
type ReportClock interface {
	Now() time.Time
}

// reportActorContextKey carries the authenticated actor for use cases (export)
// whose shared interface signature cannot take one. The HTTP layer may attach it
// with WithReportActor; without it the audit entry records actor 0.
type reportActorContextKey struct{}

// WithReportActor attaches the authenticated actor to ctx for report use cases
// that do not take an explicit actor argument.
func WithReportActor(ctx context.Context, actor int64) context.Context {
	return context.WithValue(ctx, reportActorContextKey{}, actor)
}

// reportActorFromContext returns the actor attached by WithReportActor.
func reportActorFromContext(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	actor, _ := ctx.Value(reportActorContextKey{}).(int64)
	return actor
}
