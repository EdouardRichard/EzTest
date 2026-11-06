// Package qualityapp implements the P6 quality use cases: recording terminal
// real-LLM evaluation runs with server-derived metrics and reading the quality
// overview. It depends only on the quality domain and on the ports declared
// here; infrastructure adapters implement storage and cross-context reads.
package qualityapp

import (
	"context"
	"time"

	quality "github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// Settings key consumed by the eval service (contracts/settings.md §4f).
const SettingEvalBaselineRegression = "quality.eval_baseline_regression"

// Eval run list limits of the frozen P6 contract (1–50, default 20).
const (
	DefaultListLimit = 20
	MaxListLimit     = 50
)

// EvalRun is one persisted terminal evaluation. Metrics is populated by the
// repository on reads; Record passes the derived metrics to Insert separately
// so the write adapter owns the run+metrics transaction.
type EvalRun struct {
	ID            int64
	Mode          string
	CorpusVersion string
	Model         string
	Status        string
	Summary       map[string]any
	VersionID     *int64
	StartedAt     time.Time
	FinishedAt    *time.Time
	CreatedAt     time.Time
	Metrics       []quality.Metric
}

// TrendPoint is one eval_metric value projected for the version/metric trend
// series (GET /api/quality/trends, W2-14). Baseline/Regressed explain the
// warn-only baseline comparison stored in metric detail.
type TrendPoint struct {
	RunID     int64
	VersionID *int64
	Mode      string
	Status    string
	Metric    string
	Value     float64
	Threshold float64
	Passed    bool
	Baseline  *float64
	Regressed bool
	CreatedAt time.Time
}

// Repository persists and reads eval_run/eval_metric rows. Insert must write the
// run and its metrics in one transaction. Missing runs are reported as
// quality.ErrNotFound.
type Repository interface {
	Insert(ctx context.Context, run EvalRun, metrics []quality.Metric) error
	Get(ctx context.Context, id int64) (EvalRun, error)
	List(ctx context.Context, limit int) ([]EvalRun, error)
	Latest(ctx context.Context) (*EvalRun, error)
	// LatestForVersion returns the newest run of one version (any mode), or
	// (nil, nil) when the version has no run yet. Snapshot evaluation uses it
	// to build the warn-only baseline.
	LatestForVersion(ctx context.Context, versionID int64) (*EvalRun, error)
	// Trends returns one metric series ordered by created_at ASC, id ASC. A
	// zero from/to bound is unbounded.
	Trends(ctx context.Context, versionID int64, metric string, from, to time.Time) ([]TrendPoint, error)
}

// OperationalSource aggregates the seven operational KPI counters through the
// other contexts' application query ports. It is implemented by
// infrastructure/metrics (Task 6) and may be nil before the wiring task, in
// which case the overview reports seven unavailable metrics.
type OperationalSource interface {
	Snapshot(ctx context.Context, versionID *int64) (quality.OperationalSnapshot, error)
}

// ExecutionSnapshotPort is the execution context's pass-rate counter
// (executionapp.QueryService satisfies it).
type ExecutionSnapshotPort interface {
	CountExecutionPass(ctx context.Context, versionID *int64) (int, int, error)
}

// VersionSnapshotPort is the version context's requirement-closure counter
// (versionapp.QueryService satisfies it).
type VersionSnapshotPort interface {
	CountRequirementClosure(ctx context.Context, versionID *int64) (int, int, error)
}

// DesignSnapshotPort is the design context's case-confirmation counter
// (designapp.QueryService satisfies it).
type DesignSnapshotPort interface {
	CountCaseConfirmation(ctx context.Context, versionID *int64) (int, int, error)
}

// VisualSnapshotPort is the visual context's diff-classification counter. The
// adapter returns passed and failed visual diffs; inconclusive/blocked diffs are
// not an opportunity and stay out of both counters.
type VisualSnapshotPort interface {
	VisualClassification(ctx context.Context, versionID *int64) (passed, failed int, err error)
}

// EventPublisher publishes the eval.* lifecycle events on the version channel
// (W2-14, EV-20). Publish failures never roll back the eval run.
type EventPublisher interface {
	Publish(ctx context.Context, eventType string, versionID int64, payload map[string]any) error
}

// SettingsReader is the quality view of the settings centre.
type SettingsReader interface {
	Float(ctx context.Context, key string) (float64, error)
}

// RecordInput is one POST /api/quality/eval-runs request after DTO conversion.
// Sample is nil only when Status declares an error run; Sample, Baseline and
// Summary are server-validated/derived, never trusted verdicts.
type RecordInput struct {
	Mode          string
	CorpusVersion string
	Model         string
	Status        string
	Sample        *quality.Sample
	Baseline      map[string]float64
	Summary       map[string]any
	StartedAt     *time.Time
	FinishedAt    *time.Time
}

// EvalSummary is the compact latest-eval block of the quality overview
// (EvalSummary schema of the frozen contract).
type EvalSummary struct {
	ID            int64
	Mode          string
	Status        string
	CreatedAt     time.Time
	MetricsCount  int
	MetricsPassed int
}

// Overview is the read model of GET /api/quality/overview.
type Overview struct {
	ScopeVersionID *int64
	GeneratedAt    time.Time
	Operational    []quality.OperationalMetric
	LatestEval     *EvalSummary
}
