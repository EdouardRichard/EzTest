package application

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// Autonomous chain vocabulary (spec §6.2/§6.3/§6.4). Execution deliberately
// mirrors the delivery analysis and mindmap run status strings instead of
// importing another context's domain, so the worker only speaks its own ports.
const (
	// AutonomyStatusQueued is a run row not started with an agent yet.
	AutonomyStatusQueued = "queued"
	// AutonomyStatusRunning is a run whose agent is being polled.
	AutonomyStatusRunning = "running"
	// AutonomyStatusDone is a run whose submission was materialized.
	AutonomyStatusDone = "done"
	// AutonomyStatusFailed is a run that could not produce a usable result.
	AutonomyStatusFailed = "failed"
	// AutonomyStatusDegraded is a run that did not block the chain (spec §9.7).
	AutonomyStatusDegraded = "degraded"

	// AutonomyAnalysisKindFailure is the failure-analysis run kind.
	AutonomyAnalysisKindFailure = "failure"
	// AutonomyAnalysisKindIssueTriage is the issue-triage run kind.
	AutonomyAnalysisKindIssueTriage = "issue_triage"

	// AutonomyReportStatusDraft is an unpublished report.
	AutonomyReportStatusDraft = "draft"
	// AutonomyReportStatusPublished is a generated, downloadable report.
	AutonomyReportStatusPublished = "published"
	// AutonomyReportStatusStale is a published report whose inputs changed.
	AutonomyReportStatusStale = "stale"

	// AutonomyRunKindSupplement is the post-batch supplement run kind.
	AutonomyRunKindSupplement = "supplement"
	// AutonomyRunKindIncrementalDesign is the continuing-design run kind.
	AutonomyRunKindIncrementalDesign = "incremental_design"
)

// Autonomous chain adapter sentinels. The T17 adapters translate the delivery /
// mindmap / codeintel error reasons into these values; the worker treats every
// other start error as a degradation so an AI or adapter outage never wedges a
// batch chain (spec §6.7).
var (
	// ErrAutonomyAnalysisActive reports that a failure-analysis or triage run is
	// already active. The worker re-reads the existing run and continues it.
	ErrAutonomyAnalysisActive = errors.New("execution autonomy: analysis already active")
	// ErrAutonomyAnalysisNotApplicable reports a batch without a failed
	// application case (failure) or without candidates (triage); the chain skips
	// the step. Re-materializing the triage fallback stays inside delivery.
	ErrAutonomyAnalysisNotApplicable = errors.New("execution autonomy: analysis not applicable")
	// ErrAutonomyRunInProgress reports that a supplement / incremental_design run
	// of the batch already occupies the single-flight slot.
	ErrAutonomyRunInProgress = errors.New("execution autonomy: supplement run in progress")
	// ErrAutonomyImpactInProgress reports that a code impact analysis is already
	// active for the version.
	ErrAutonomyImpactInProgress = errors.New("execution autonomy: code impact analysis in progress")
	// ErrAutonomyImpactUnavailable reports that the version cannot run a code
	// impact analysis (no repository/baseline); the design run proceeds with an
	// empty impact and a warning.
	ErrAutonomyImpactUnavailable = errors.New("execution autonomy: code impact unavailable")
	// ErrAutonomySupplementRejected reports a permanent supplement-batch creation
	// rejection (for example invalid_case_ids or too_many_cases). Retrying the
	// same case set can never succeed, so the chain settles without a supplement
	// child instead of retrying forever.
	ErrAutonomySupplementRejected = errors.New("execution autonomy: supplement batch rejected")
)

// AnalysisView is the worker's projection of one delivery analysis run. A nil
// view means no run exists. CandidateCount is the number of validated failure
// candidates the run produced; it gates the issue-triage step. CreatedAt orders
// a manual failure re-analysis against an older triage run (the adapter projects
// the delivery run's created_at; a zero value falls back to the id ordering).
type AnalysisView struct {
	ID             int64
	Kind           string
	Status         string
	CandidateCount int
	CreatedAt      time.Time
	Degraded       bool
}

// ReportStateView is the worker's projection of the latest report of a batch.
type ReportStateView struct {
	ID       int64
	Status   string
	Revision int
}

// SupplementRunView is the worker's projection of the latest supplement or
// incremental-design run of a parent batch. Kind distinguishes the two run
// kinds because both point at the same parent batch.
type SupplementRunView struct {
	ID         int64
	Kind       string
	Status     string
	NewCaseIDs []int64
	Degraded   bool
}

// ImpactView is the worker's projection of one code impact analysis.
type ImpactView struct {
	ID         int64
	Status     string
	BaseCommit string
	HeadCommit string
	Degraded   bool
}

// AnalysisPort is the delivery failure-analysis / issue-triage surface consumed
// by the autonomous worker (spec §6.2/§6.3). The T17 adapter delegates to
// delivery's AnalysisService: StartFailure -> StartFailure; StartIssueTriage ->
// StartTriage; GetAnalysis -> LatestByKind (a not-found run is nil, nil).
// A start that answers 409 analysis_active returns ErrAutonomyAnalysisActive
// and the existing run id; the worker then continues that run.
type AnalysisPort interface {
	StartFailureAnalysis(ctx context.Context, versionID, batchID, actor int64) (int64, error)
	StartIssueTriage(ctx context.Context, versionID, batchID, actor int64) (int64, error)
	GetAnalysis(ctx context.Context, batchID int64, kind string) (*AnalysisView, error)
}

// ReportPort is the delivery report surface consumed by the worker (spec
// §6.2/§6.3). EnsureReport/RegenerateReport keep the delivery service
// signatures (domain.Report is projected to ReportStateView); LatestReport is
// the read the worker needs to detect the stale status before regenerating.
type ReportPort interface {
	EnsureReport(ctx context.Context, versionID, batchID, actor int64) (reportID int64, created bool, err error)
	RegenerateReport(ctx context.Context, versionID, reportID, actor int64) (int64, error)
	LatestReport(ctx context.Context, versionID, batchID int64) (*ReportStateView, error)
}

// SupplementPort is the mindmap supplement / incremental-design surface consumed
// by the worker (spec §6.2/§6.4). StartSupplement starts a post-batch supplement
// run; StartIncrementalDesign starts the continuing-design run. GetSupplementRun
// returns the latest P7 run of the parent batch (both kinds; Kind discriminates)
// so the worker can wait for an active run and stop restarting a terminal one.
type SupplementPort interface {
	StartSupplement(ctx context.Context, versionID, batchID, actor int64) (int64, error)
	StartIncrementalDesign(ctx context.Context, versionID, batchID, actor int64) (int64, error)
	GetSupplementRun(ctx context.Context, batchID int64) (*SupplementRunView, error)
	CreateSupplementBatch(ctx context.Context, versionID, parentBatchID int64, caseIDs []int64, actor int64) (*domain.RunBatch, error)
	MarkNodesMerged(ctx context.Context, versionID int64, caseIDs []int64, actor int64) error
}

// CodeImpactPort is the P4 code-impact surface of the continuing-design segment
// (spec §6.1/§6.2). LatestImpact lets a restarted worker find the analysis it
// started before the restart; GetImpact reads one analysis by id. The T17
// adapter delegates to codeintel's AnalysisService (Start with
// StartAnalysisInput{BaseCommit}, Get, GetLatest); the methods mirror those
// public signatures.
type CodeImpactPort interface {
	StartImpact(ctx context.Context, versionID int64, baseCommit string) (int64, error)
	GetImpact(ctx context.Context, versionID, analysisID int64) (*ImpactView, error)
	LatestImpact(ctx context.Context, versionID int64) (*ImpactView, error)
}

// VersionFinalizer is the version-context finalize surface (spec §5.1/§6.2).
// The worker calls TryFinalize after it settles the last batch of a version; the
// version service owns the finalize conditions (no active batch, every terminal
// batch autonomy_done, at least one batch, no draft requirement for a
// continuing version).
type VersionFinalizer interface {
	TryFinalize(ctx context.Context, versionID int64) (bool, error)
	SetContinuePhase(ctx context.Context, versionID int64, phase string) error
}
