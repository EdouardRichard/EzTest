// Package application implements the delivery use cases (issues, reports and
// analysis runs) on top of the delivery domain. This file declares the ports of
// the analysis-run service; the infrastructure adapters of this context and the
// composition root implement them.
//
// Every port and value type of this file carries the Analysis prefix on purpose:
// the delivery issue, report and analysis tasks are written in parallel into the
// same Go package, so unprefixed names (ExecutionReader, IssueReader,
// EventPublisher, ...) would collide with the other services' declarations.
package application

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// Analysis agent profile keys seeded by T13 (spec §9.1).
const (
	AnalysisAgentProfileFailure = "failure-analysis-agent"
	AnalysisAgentProfileTriage  = "issue-triage-agent"
)

// Sentinel errors of the analysis store. The application maps them to the
// stable domain reasons of spec §7.11; infrastructure returns them so no pgx
// error leaks into the service.
var (
	// ErrAnalysisRunNotFound reports an unknown analysis run id or a run that
	// does not belong to the addressed version/batch.
	ErrAnalysisRunNotFound = errors.New("delivery: analysis run not found")
	// ErrAnalysisStateConflict reports a lost status compare-and-swap.
	ErrAnalysisStateConflict = errors.New("delivery: analysis run state conflict")
)

// AnalysisRunPatch is the optional part of an analysis run status transition.
// Nil fields are left unchanged by the store.
type AnalysisRunPatch struct {
	AgentSessionID *int64
	AgentRunID     *int64
	Model          *string
	PromptVersion  *string
	Input          json.RawMessage
	Result         json.RawMessage
	Stats          json.RawMessage
	Errors         json.RawMessage
	StartedAt      *time.Time
	FinishedAt     *time.Time
}

// AnalysisStore persists analysis_run rows. Create, Active and UpdateStatus must
// preserve the (batch_id, kind) single-flight unique index and the status CAS.
type AnalysisStore interface {
	Create(ctx context.Context, run domain.AnalysisRun) (domain.AnalysisRun, error)
	Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error)
	GetByID(ctx context.Context, analysisID int64) (domain.AnalysisRun, error)
	List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error)
	// Active returns the queued/running run of one (batch, kind), or
	// ErrAnalysisRunNotFound when there is none.
	Active(ctx context.Context, batchID int64, kind string) (domain.AnalysisRun, error)
	// Latest returns the newest run of one (batch, kind), or
	// ErrAnalysisRunNotFound when there is none.
	Latest(ctx context.Context, batchID int64, kind string) (domain.AnalysisRun, error)
	// ListActive returns queued/running runs (oldest first) bounded by limit.
	ListActive(ctx context.Context, limit int) ([]domain.AnalysisRun, error)
	// UpdateStatus is a status compare-and-swap: the patch is applied only when
	// the stored status equals from and the updated row is returned. A lost CAS
	// is ErrAnalysisStateConflict; an unknown id is ErrAnalysisRunNotFound.
	UpdateStatus(ctx context.Context, analysisID int64, from, to string, patch AnalysisRunPatch) (domain.AnalysisRun, error)
	// RecoverStale fails every queued/running run left behind by a previous
	// process (spec §6.7) and returns the recovered rows so the service can run
	// the §5.6 triage fallback of recovered issue_triage runs.
	RecoverStale(ctx context.Context) ([]domain.AnalysisRun, error)
}

// AnalysisFailedCase is one failed case run projection used to build the
// failure-analysis input. FailureReason is the short classification summary, not
// evidence or assertion body text. EvidenceIDs must list the evidence of the
// failed case run: the service uses their union as the per-candidate/per-draft
// evidence whitelist (spec §5.6), so an adapter that leaves it empty makes every
// cited evidence id foreign.
type AnalysisFailedCase struct {
	CaseRunID     int64
	CaseNodeID    int64
	FailureClass  string
	Title         string
	FailureReason string
	EvidenceIDs   []int64
}

// AnalysisExecutionReader is the read-only execution port: the failed case runs
// of a batch. Implementations must bound the result by limit.
type AnalysisExecutionReader interface {
	ListFailedCases(ctx context.Context, versionID, batchID int64, limit int) ([]AnalysisFailedCase, error)
}

// AnalysisIssueWriter materializes triage issue drafts. It is satisfied by the
// delivery IssueService.ApplyDrafts method of the Shared Interfaces contract.
type AnalysisIssueWriter interface {
	ApplyDrafts(ctx context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error)
}

// AnalysisIssueReader loads the open issue summaries injected into the triage
// input and validates an explicit mergeIntoIssueId (spec §5.6/§9.4). It is
// required in production: without it every explicit merge target is stripped
// (merging into a confirmed/dismissed issue is forbidden).
type AnalysisIssueReader interface {
	ListOpen(ctx context.Context, versionID int64, limit int) ([]domain.OpenIssue, error)
}

// AnalysisCodeAnchorPort creates issue code anchors after materialization. It is
// best-effort: a failure or warning never blocks the issue or the report.
type AnalysisCodeAnchorPort interface {
	CreateIssueAnchors(ctx context.Context, versionID, issueID, actor int64, refs []domain.CodeRef) ([]string, error)
}

// AnalysisAgentRunScope is the additional P1 run scope of an analysis run
// (spec §9.1). The adapter forwards it as RunScope.AnalysisRunID.
type AnalysisAgentRunScope struct {
	VersionID     int64
	BatchID       int64
	AnalysisRunID int64
}

// AnalysisAgentRunStatus is the terminal/running state of the P1 run behind an
// agent session. TerminalReason is the P1 agent_run.terminal_reason
// (completed, timeout, run_hard_timeout, max_turns, model_error,
// context_overflow, aborted, ...); it decides whether a stopped analysis run is
// degraded or failed (spec §9.7).
type AnalysisAgentRunStatus struct {
	ID             int64
	Status         string
	Error          string
	TerminalReason string
}

// AnalysisAgentSessionStatus is the P1 session projection polled by ProcessOnce.
type AnalysisAgentSessionStatus struct {
	SessionID int64
	Status    string
	LastRun   *AnalysisAgentRunStatus
}

// AnalysisAgentRuntime is the P1 agent runtime port: sessions, scoped text runs,
// status polling and abort. It is implemented by the T13 delivery agent adapter.
type AnalysisAgentRuntime interface {
	CreateSession(ctx context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error)
	Start(ctx context.Context, sessionID int64, instruction string, actor int64, scope AnalysisAgentRunScope) (int64, error)
	GetSession(ctx context.Context, sessionID int64) (AnalysisAgentSessionStatus, error)
	Abort(ctx context.Context, sessionID int64, actor int64) error
}

// AnalysisAgentProfileReader supplies the model of a seeded agent profile so the
// run (and the §7.5 AnalysisView) exposes it. It is optional: an unwired reader
// leaves analysis_run.model empty instead of failing the run.
type AnalysisAgentProfileReader interface {
	ProfileModel(ctx context.Context, profileKey string) (string, error)
}

// AnalysisEvent is one analysis event on the version channel (spec §8.1). Detail
// carries ids/counts/status only: never a prompt, candidate prose or evidence
// body.
type AnalysisEvent struct {
	VersionID int64
	BatchID   int64
	Type      string
	Summary   string
	Detail    map[string]any
}

// AnalysisEventPublisher publishes one analysis event after the commit.
// Publishing failures are logged and never fail the request.
type AnalysisEventPublisher interface {
	Publish(ctx context.Context, event AnalysisEvent) error
}

// AnalysisAuditEvent is one delivery.analysis.* audit entry (spec §11.2). The
// metadata carries ids/counts/status/duration only.
type AnalysisAuditEvent struct {
	Action     string
	ActorID    int64
	TargetType string
	TargetID   string
	Metadata   map[string]any
}

// AnalysisAuditRecorder records the analysis audit entries. A failure is logged
// and never fails the run.
type AnalysisAuditRecorder interface {
	Record(ctx context.Context, event AnalysisAuditEvent) error
}

// AnalysisIDGenerator returns the next Snowflake id (D29).
type AnalysisIDGenerator interface {
	Next() int64
}

// AnalysisClock supplies the current time for deterministic tests.
type AnalysisClock interface {
	Now() time.Time
}
