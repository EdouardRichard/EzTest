// Package visualapp implements the P5 visual use cases: assertion configuration,
// baseline approval/history, comparison recording/listing and quality overview
// aggregation. It depends on the visual domain and on the ports declared here;
// infrastructure adapters implement the design/execution/eventing/audit reads
// and writes so the context stays independent from the other bounded contexts.
package visualapp

import (
	"context"
	"time"

	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// Settings keys consumed by the visual services (contracts/settings.md §4e).
const (
	SettingDefaultThreshold         = "visual.default_threshold"
	SettingBaselineMaxBytes         = "visual.baseline_max_bytes"
	SettingDefaultViewportWidth     = "visual.default_viewport_width"
	SettingDefaultViewportHeight    = "visual.default_viewport_height"
	SettingDefaultDeviceScaleFactor = "visual.default_device_scale_factor"
	SettingCompareTimeoutSeconds    = "visual.compare_timeout_seconds"
	SettingWaitTimeoutSeconds       = "visual.wait_timeout_seconds"
)

// CaseReader is the read port into the design context: resolve one
// case_assertion to the execution-relevant identity. It accepts draft and
// confirmed case versions (configuration is allowed before confirmation);
// execution itself only consumes current+confirmed versions.
type CaseReader interface {
	VisualAssertionContext(ctx context.Context, versionID, caseAssertionID int64) (CaseAssertionRef, error)
}

// CaseAssertionRef is the design projection of one visual case assertion.
type CaseAssertionRef struct {
	CaseAssertionID     int64
	CaseVersionID       int64
	CaseID              int64
	StepSeq             int
	AssertionSeq        int
	Kind                string
	TargetVersionSUTID  int64
	DefaultVersionSUTID int64
	CaseVersionStatus   string
}

// EvidenceReader is the read port into the execution context: one visual_actual
// evidence plus its bytes. It never exposes filesystem paths or credentials.
type EvidenceReader interface {
	VisualEvidence(ctx context.Context, versionID, evidenceID int64) (VisualEvidence, error)
}

// VisualEvidence is one visual evidence row with its immutable bytes and the
// capture metadata needed to approve a baseline (spec §7.2). ConfigRevision is
// the configuration revision the actual capture was produced under; adapters
// that cannot recover it return 0 and the service falls back to the capture
// timestamp.
type VisualEvidence struct {
	ID             int64
	CaseRunID      int64
	CaseVersionID  int64
	VersionSUTID   *int64
	Kind           string
	SHA256         string
	SizeBytes      int64
	MIME           string
	Data           []byte
	Width          int
	Height         int
	ConfigRevision int
	CreatedAt      time.Time
}

// AssertionRepository persists visual_assertion rows.
type AssertionRepository interface {
	Create(ctx context.Context, assertion visual.Assertion) (visual.Assertion, error)
	// UpdateWithRevision updates the configuration and supersedes the current
	// approved baseline in the same transaction (spec §7.1).
	UpdateWithRevision(ctx context.Context, assertion visual.Assertion, supersededAt time.Time) (visual.Assertion, error)
	Get(ctx context.Context, id int64) (visual.Assertion, error)
	GetByCaseAssertion(ctx context.Context, versionID, caseAssertionID int64) (visual.Assertion, bool, error)
	List(ctx context.Context, versionID int64, caseVersionID, versionSUTID *int64) ([]visual.Assertion, error)
}

// BaselineRepository persists visual_baseline rows.
type BaselineRepository interface {
	// InsertApproved inserts the new approved baseline and supersedes the
	// previous approved row in one transaction (partial unique index).
	InsertApproved(ctx context.Context, baseline visual.Baseline, supersededAt time.Time) (visual.Baseline, error)
	List(ctx context.Context, assertionID int64) ([]visual.Baseline, error)
	Active(ctx context.Context, assertionID int64) (visual.Baseline, bool, error)
}

// DiffRepository persists visual_diff rows and aggregates the quality overview.
type DiffRepository interface {
	// Insert is idempotent by assertion_result_id: it returns the existing row
	// and created=false on replay.
	Insert(ctx context.Context, diff visual.Diff) (visual.Diff, bool, error)
	Get(ctx context.Context, id int64) (visual.Diff, error)
	List(ctx context.Context, versionID int64, filter visual.DiffFilter) ([]visual.Diff, bool, error)
	ByAssertionResult(ctx context.Context, assertionResultID int64) (visual.Diff, bool, error)
	Overview(ctx context.Context, versionID *int64) (OverviewData, error)
}

// OverviewData is the repository aggregate consumed by OverviewService.
type OverviewData struct {
	AssertionTotal      int
	BaselineCovered     int
	DiffCounts          map[visual.Classification]int
	InconclusiveReasons []ReasonCount
	DiffRatios          []float64
	RecentFailures      []visual.Diff
}

// ReasonCount is one inconclusive reason bucket.
type ReasonCount struct {
	Reason string
	Count  int
}

// SettingsReader is the visual view of the settings center. Missing settings
// fall back to the frozen defaults.
type SettingsReader interface {
	Float(ctx context.Context, key string) (float64, error)
	Int(ctx context.Context, key string) (int64, error)
}

// Clock is the injectable time source.
type Clock interface {
	Now() time.Time
}

// IDGenerator hands out snowflake IDs.
type IDGenerator interface {
	Next() int64
}

// Event is one visual event published on the version channel.
type Event struct {
	Type      string
	VersionID int64
	Scope     map[string]any
	Payload   map[string]any
	ActorID   *int64
}

// EventPublisher publishes one visual event. Publish failures never roll back
// the business transaction.
type EventPublisher interface {
	Publish(ctx context.Context, event Event) error
}

// AuditEntry is one best-effort visual audit record.
type AuditEntry struct {
	ActorID    *int64
	Action     string
	TargetKind string
	TargetID   string
	Metadata   map[string]any
}

// AuditRecorder records the best-effort audit entry of one visual write.
type AuditRecorder interface {
	Record(ctx context.Context, entry AuditEntry) error
}
