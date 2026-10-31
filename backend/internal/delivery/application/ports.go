// Package deliveryapp implements the P4 delivery use cases (issues, reports and
// analysis runs) and declares the ports it consumes. The interfaces here are
// consumer-side contracts; infrastructure adapters and later workstreams
// provide the implementations. All methods take a context and honor it.
package deliveryapp

import (
	"context"
	"encoding/json"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// IssueFilter is one descending-ID issue page filter. Status/Category/Severity
// filter when non-empty; VersionSUTID filters the issue refs' version SUT.
type IssueFilter struct {
	Status       string
	Category     string
	Severity     string
	VersionSUTID *int64
	Before       int64
	Limit        int
}

// IssueStatusUpdate is one issue status transition request. ActorKind is
// user/agent/system; ExpectedRevision enables the optimistic CAS when non-nil.
type IssueStatusUpdate struct {
	VersionID        int64
	IssueID          int64
	To               delivery.IssueStatus
	Reason           string
	ActorID          int64
	ActorKind        string
	ExpectedRevision *int32
}

// IssueMerge is one source-to-target issue merge request.
type IssueMerge struct {
	VersionID        int64
	SourceID         int64
	TargetID         int64
	ActorID          int64
	Reason           string
	ExpectedRevision *int32
}

// IssueRepository persists delivery_issue, delivery_issue_ref and
// delivery_issue_status_history rows.
type IssueRepository interface {
	// CreateCandidate inserts the issue and its refs idempotently. The bool
	// reports whether the issue was created (false on a dedupe conflict).
	CreateCandidate(ctx context.Context, issue delivery.Issue, refs []delivery.IssueRef) (delivery.Issue, bool, error)
	Get(ctx context.Context, versionID, issueID int64) (delivery.Issue, error)
	List(ctx context.Context, versionID int64, f IssueFilter) ([]delivery.Issue, error)
	UpdateStatus(ctx context.Context, u IssueStatusUpdate) (delivery.Issue, error)
	Merge(ctx context.Context, m IssueMerge) (delivery.Issue, error)
	AppendHistory(ctx context.Context, h delivery.IssueStatusChange) error
	ListRefs(ctx context.Context, issueID int64) ([]delivery.IssueRef, error)
	ListHistory(ctx context.Context, issueID int64) ([]delivery.IssueStatusChange, error)
	FindByCaseRun(ctx context.Context, versionID, caseRunID int64) (delivery.Issue, bool, error)
}

// ReportFilter is one descending-ID report page filter.
type ReportFilter struct {
	Kind   *delivery.ReportKind
	Before int64
	Limit  int
}

// ReportRepository persists delivery_report, delivery_report_revision and
// delivery_report_ref rows, including the publish finalization and stale
// marking of the report service.
type ReportRepository interface {
	CreateReport(ctx context.Context, r delivery.Report) (delivery.Report, error)
	GetReport(ctx context.Context, versionID, reportID int64) (delivery.Report, error)
	ListReports(ctx context.Context, versionID int64, f ReportFilter) ([]delivery.Report, error)
	NextRevision(ctx context.Context, reportID int64) (int32, error)
	CreateRevision(ctx context.Context, r delivery.ReportRevision) (delivery.ReportRevision, error)
	FinalizePublished(ctx context.Context, reportID int64, revision int32, htmlRef int64, fingerprint string) error
	MarkRenderMetadata(ctx context.Context, revisionID int64, status, errMsg string) error
	MarkStale(ctx context.Context, reportID int64) (bool, error)
	GetRevision(ctx context.Context, reportID int64, revision int32) (delivery.ReportRevision, error)
	ListRevisions(ctx context.Context, reportID int64) ([]delivery.ReportRevision, error)
	InsertRefs(ctx context.Context, revisionID int64, refs []delivery.ReportRef) error
	ListRefs(ctx context.Context, reportID int64) ([]delivery.ReportRef, error)
	SetWaveReportID(ctx context.Context, versionID, waveID, reportID int64) error
}

// AnalysisRepository persists delivery_analysis_run rows.
type AnalysisRepository interface {
	CreateQueued(ctx context.Context, r delivery.AnalysisRun) (delivery.AnalysisRun, error)
	MarkRunning(ctx context.Context, id int64, agentRunID int64) error
	Complete(ctx context.Context, id int64, status delivery.AnalysisStatus, output json.RawMessage, errMsg string) error
	LatestByIssue(ctx context.Context, issueID int64) (delivery.AnalysisRun, bool, error)
}

// TaskEnsurer ensures the P4 agent work items. Every method is idempotent.
type TaskEnsurer interface {
	EnsureIssueTriage(ctx context.Context, issueID int64) error
	EnsureWaveReport(ctx context.Context, versionID, waveID int64) error
	EnsureVersionReport(ctx context.Context, versionID int64) error
	EnsureRequirementClose(ctx context.Context, requirementID int64) error
	EnsureVersionClose(ctx context.Context, versionID int64) error
}

// ExecutionReader is the delivery context's read port into execution. It only
// exposes case_run/wave projections; steps, assertions and evidence bytes stay
// in the execution context.
type ExecutionReader interface {
	// ListFailedApplicationCaseRuns returns failed(application) case_runs of a
	// version that have no issue yet (the reconciler scan).
	ListFailedApplicationCaseRuns(ctx context.Context, versionID int64) ([]CaseRunSnapshot, error)
	CaseRunSnapshot(ctx context.Context, caseRunID int64) (CaseRunSnapshot, error)
	WaveSnapshot(ctx context.Context, versionID, waveID int64) (WaveSnapshot, error)
	VersionSnapshot(ctx context.Context, versionID int64) (VersionSnapshot, error)
}

// DesignReader is the delivery context's read port into design.
type DesignReader interface {
	// CurrentConfirmedCases returns the current+confirmed case versions of one
	// requirement.
	CurrentConfirmedCases(ctx context.Context, requirementID int64) ([]CaseVersionSnapshot, error)
	// CaseVersionsByIDs returns case versions by ID regardless of current or
	// confirmation status (historical refs keep their title).
	CaseVersionsByIDs(ctx context.Context, versionID int64, ids []int64) ([]CaseVersionSnapshot, error)
}

// VersionReader is the delivery context's read/write port into version.
type VersionReader interface {
	Get(ctx context.Context, versionID int64) (VersionSnapshot, error)
	// ReopenIfCompleted clears the completed projection (completed -> running)
	// and is a no-op for any other status.
	ReopenIfCompleted(ctx context.Context, versionID int64, trigger, reason string) error
}

// EventPublisher publishes the P4 UI projection events on the version channel.
// Publish failures never roll back the business transaction.
type EventPublisher interface {
	Publish(ctx context.Context, eventType string, versionID int64, payload map[string]any) error
}

// AuditSink records the best-effort audit entry of a P4 write.
type AuditSink interface {
	Write(ctx context.Context, actorID int64, action, targetKind, targetID string, metadata map[string]any) error
}

// Clock is the injectable time source.
type Clock interface {
	Now() time.Time
}

// Snapshots consumed by the delivery use cases. They mirror the report and
// closure inputs of spec §6.1/§7.2 but stay in the delivery application layer:
// IDs are int64 at this boundary and become decimal strings in the API layer.

// CaseRunSnapshot is one case_run projection. VersionSUTID is the version SUT
// that owns the failing step/assertion; VersionSUTIDs lists every SUT the case
// version uses (report grouping and issue refs both use version SUT IDs, never
// bare SUT IDs). Category/Severity carry the P3 deterministic derivation
// (ui/api/db and major/minor/trivial).
type CaseRunSnapshot struct {
	ID                   int64
	VersionID            int64
	WaveID               int64
	CaseVersionID        int64
	CaseTitle            string
	VersionSUTID         *int64
	VersionSUTIDs        []int64
	Status               string
	FailureClass         string
	Summary              string
	StepCount            int
	StepFailedCount      int
	AssertionCount       int
	AssertionFailedCount int
	FailedStepSeq        int
	FailedAssertionSeq   int
	Category             string
	Severity             string
	EvidenceIDs          []int64
	StartedAt            *time.Time
	FinishedAt           *time.Time
	CreatedAt            time.Time
}

// EvidenceSnapshot is one evidence metadata row; content bytes and paths are
// deliberately absent (spec §6.1).
type EvidenceSnapshot struct {
	ID           int64
	CaseRunID    int64
	VersionSUTID *int64
	StepSeq      *int
	AssertionSeq *int
	Kind         string
	SHA256       string
	SizeBytes    int64
}

// WaveSnapshot is one run_wave projection with its per-SUT counts and case-run
// children (spec §6.1).
type WaveSnapshot struct {
	ID           int64
	VersionID    int64
	Status       string
	Trigger      string
	Counts       map[string]int
	ByVersionSUT map[int64]SUTCounts
	ReportID     *int64
	StartedAt    *time.Time
	FinishedAt   *time.Time
	CaseRuns     []CaseRunSnapshot
	Evidence     []EvidenceSnapshot
}

// SUTCounts is one version SUT's section of a wave's by_version_sut counts.
type SUTCounts struct {
	Total        int
	Passed       int
	Failed       int
	Blocked      int
	Inconclusive int
}

// RequirementSnapshot is one version requirement projection with its latest
// closure summary (spec §6.1/§7.2).
type RequirementSnapshot struct {
	ID               int64
	VersionID        int64
	CurrentVersionID *int64
	Title            string
	Status           string
	ClosureID        *int64
	ClosedAt         *time.Time
	CloseRound       int32
	ReopenCount      int
	UpdatedAt        time.Time
}

// VersionSUTSnapshot is one version SUT binding used to label report rows and
// chain nodes (spec §6.1).
type VersionSUTSnapshot struct {
	ID              int64
	SUTID           int64
	SUTName         string
	EnvironmentID   int64
	EnvironmentName string
}

// CoverageSnapshot is the P3 execution coverage projection of a version.
type CoverageSnapshot struct {
	RequirementsTotal   int
	RequirementsCovered int
	CaseVersionsTotal   int
	CaseVersionsCovered int
	Gaps                []CoverageGapSnapshot
}

// CoverageGapSnapshot is one uncovered case version.
type CoverageGapSnapshot struct {
	RequirementID int64
	CaseVersionID int64
	Reason        string
}

// VersionSnapshot is the version projection used by report generation and the
// close/reopen path (spec §6.1/§7.2).
type VersionSnapshot struct {
	ID           int64
	Title        string
	Status       string
	ClosedAt     *time.Time
	CloseReason  string
	CloseRunID   *int64
	Requirements []RequirementSnapshot
	VersionSUTs  []VersionSUTSnapshot
	Waves        []WaveSnapshot
	CaseRuns     []CaseRunSnapshot
	Coverage     CoverageSnapshot
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// CaseVersionSnapshot is one design case-version projection. Status keeps
// historical values (for example superseded) so refs never lose their title.
type CaseVersionSnapshot struct {
	ID                   int64
	CaseID               int64
	Revision             int32
	Title                string
	Status               string
	RequirementID        int64
	RequirementVersionID int64
	VersionSUTIDs        []int64
	UpdatedAt            time.Time
}

// IssueSnapshot is one delivery issue projection used by reports and closure
// checks. SourceIssueIDs lists issues merged into this one; CaseRunIDs lists
// the case_run refs (spec §6.1/§7.2).
type IssueSnapshot struct {
	ID             int64
	VersionID      int64
	Title          string
	Status         delivery.IssueStatus
	Category       string
	Severity       string
	Source         string
	MergedIntoID   *int64
	SourceIssueIDs []int64
	CaseRunIDs     []int64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}
