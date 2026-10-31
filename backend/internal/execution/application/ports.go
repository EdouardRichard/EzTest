// Package executionapp implements the P3 execution use cases: intent planning,
// wave scheduling, quota reservation, dispatch fencing, pause/resume, human
// takeover, recovery and deterministic failure/coverage analysis. It depends on
// domain types and port interfaces only; infrastructure is injected by the
// assembly root.
package executionapp

import (
	"context"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// SettingsReader is the execution context's view of the settings service. The
// execution settings are frozen in contracts/settings.md; settingsapp.Service
// satisfies this port.
type SettingsReader interface {
	String(ctx context.Context, key string) (string, error)
	Int(ctx context.Context, key string) (int64, error)
	Bool(ctx context.Context, key string) (bool, error)
}

// IntentRepository persists execution_intent rows.
type IntentRepository interface {
	Create(ctx context.Context, intent execution.ExecutionIntent) error
	Get(ctx context.Context, id int64) (execution.ExecutionIntent, error)
	// List returns one descending-ID page. status filters when non-empty.
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.ExecutionIntent, bool, error)
	// ListOpen returns open intents. versionID <= 0 means every version.
	ListOpen(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error)
	// ListActive returns open/claimed/running intents. versionID <= 0 means
	// every version; the recovery sweep uses that for the superseded check.
	ListActive(ctx context.Context, versionID int64) ([]execution.ExecutionIntent, error)
	// Transition applies a guarded status change. It returns false,nil when the
	// row is no longer in one of the from statuses (idempotent no-op).
	Transition(ctx context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error)
	// CountInitialFor counts stored initial intents for the natural key,
	// regardless of status. It is the idempotency guard of EnsureInitialIntents.
	CountInitialFor(ctx context.Context, caseVersionID int64, fingerprint, baseline string) (int, error)
}

// WaveRepository persists run_wave rows.
type WaveRepository interface {
	// CreateWithCaseRuns writes the wave, claims the intents and creates the
	// pending case_runs in one transaction (one wave per version invariant).
	CreateWithCaseRuns(ctx context.Context, wave execution.RunWave, intents []execution.ExecutionIntent, caseRuns []execution.CaseRun) error
	Get(ctx context.Context, id int64) (execution.RunWave, error)
	// GetActive returns the planned/running/paused wave of a version.
	GetActive(ctx context.Context, versionID int64) (*execution.RunWave, error)
	List(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error)
	// Update persists the mutable wave columns. It returns false when the row
	// no longer exists.
	Update(ctx context.Context, wave execution.RunWave) (bool, error)
	// Transition applies a guarded status change (from -> to) and returns
	// false,nil when the row is no longer in one of the from statuses.
	Transition(ctx context.Context, id int64, from []execution.WaveStatus, to execution.WaveStatus, fields execution.RunWave) (bool, error)
	// Counts recomputes the wave counts from case_run/evidence rows.
	Counts(ctx context.Context, waveID int64) (execution.WaveCounts, error)
	// ListActive returns every planned/running/paused wave (recovery sweep).
	ListActive(ctx context.Context) ([]execution.RunWave, error)
}

// CaseRunRepository persists case_run and case_run_sut_context rows.
type CaseRunRepository interface {
	Get(ctx context.Context, id int64) (execution.CaseRun, error)
	ListByWave(ctx context.Context, waveID int64, status string, limit int, beforeID *int64) ([]execution.CaseRun, bool, error)
	// ListAllByWave returns every case_run of a wave for terminal/wave logic.
	ListAllByWave(ctx context.Context, waveID int64) ([]execution.CaseRun, error)
	// ListRunning returns every running case_run for quota accounting.
	ListRunning(ctx context.Context) ([]execution.CaseRun, error)
	Create(ctx context.Context, caseRun execution.CaseRun) error
	// UpdateCAS applies mutate to the row and writes it back only when the
	// row still carries the given dispatch epoch and token (both are checked
	// when non-zero). It returns false,nil on a fence mismatch.
	UpdateCAS(ctx context.Context, id int64, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error)
	// Update persists a server-side state change without a dispatch fence.
	Update(ctx context.Context, caseRun execution.CaseRun) (bool, error)
	// Transition applies a guarded status change with an optional fence.
	Transition(ctx context.Context, id int64, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) (bool, error)
	// ClaimLease extends the run lease for the given dispatch epoch.
	ClaimLease(ctx context.Context, id int64, epoch int64, until time.Time) (bool, error)
	ListRunningByExecutor(ctx context.Context, executorID int64) ([]execution.CaseRun, error)
	// LatestByCaseVersions returns the newest case_run per case_version_id of
	// a version (coverage analysis).
	LatestByCaseVersions(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error)
}

// ResultRepository persists step_result/assertion_result rows. The epoch/token
// arguments fence late writes from a stale dispatch.
type ResultRepository interface {
	UpsertStep(ctx context.Context, result execution.StepResult, epoch int64, token string) (bool, error)
	UpsertAssertion(ctx context.Context, result execution.AssertionResult, epoch int64, token string) (bool, error)
	ListSteps(ctx context.Context, caseRunID int64) ([]execution.StepResult, error)
	ListAssertions(ctx context.Context, caseRunID int64) ([]execution.AssertionResult, error)
}

// EvidenceRepository persists immutable evidence rows and their owner refs.
type EvidenceRepository interface {
	Insert(ctx context.Context, evidence execution.Evidence) error
	Get(ctx context.Context, id int64) (execution.Evidence, error)
	ListByCaseRun(ctx context.Context, caseRunID int64, limit int) ([]execution.Evidence, error)
	ListByVersion(ctx context.Context, versionID int64, filter execution.EvidenceFilter) ([]execution.Evidence, bool, error)
	InsertRef(ctx context.Context, ref execution.EvidenceRef) error
	CountByCaseRun(ctx context.Context, caseRunID int64) (int, error)
}

// ExecutorRepository persists executor_instance rows.
type ExecutorRepository interface {
	UpsertHeartbeat(ctx context.Context, instance execution.ExecutorInstance) error
	GetByEndpoint(ctx context.Context, endpoint string) (execution.ExecutorInstance, error)
	Get(ctx context.Context, id int64) (execution.ExecutorInstance, error)
	List(ctx context.Context) ([]execution.ExecutorInstance, error)
	SetStatus(ctx context.Context, id int64, status execution.ExecutorStatus) (bool, error)
}

// HumanRequestRepository persists human_request rows.
type HumanRequestRepository interface {
	Create(ctx context.Context, request execution.HumanRequest) error
	Get(ctx context.Context, id int64) (execution.HumanRequest, error)
	GetOpenByCaseRun(ctx context.Context, caseRunID int64) (*execution.HumanRequest, error)
	ListByVersion(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error)
	ListOpen(ctx context.Context) ([]execution.HumanRequest, error)
	CountByCaseRun(ctx context.Context, caseRunID int64) (int, error)
	// Transition applies a guarded status change with the optional response
	// fields. It returns false,nil on a status mismatch.
	Transition(ctx context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time) (bool, error)
	// Expire marks every open request whose expires_at is before now as
	// expired and returns the affected rows.
	Expire(ctx context.Context, now time.Time) ([]execution.HumanRequest, error)
}

// SUTContextRepository persists case_run_sut_context rows.
type SUTContextRepository interface {
	Create(ctx context.Context, sutContext execution.CaseRunSUTContext) error
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error)
	UpdateCAS(ctx context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error)
	// ListStale returns contexts older than createdBefore whose case_run is not
	// running (TTL cleanup).
	ListStale(ctx context.Context, createdBefore time.Time, limit int) ([]execution.CaseRunSUTContext, error)
	// ListAll returns every non-closed context (orphan reconciliation).
	ListAll(ctx context.Context) ([]execution.CaseRunSUTContext, error)
}

// CaseReader is the read port into the design context. It only exposes
// current+confirmed case versions for execution.
type CaseReader interface {
	GetCaseVersion(ctx context.Context, versionID, caseVersionID int64) (execution.CaseSnapshot, error)
	ListExecutable(ctx context.Context, versionID int64) ([]execution.CaseSnapshot, error)
}

// VersionReader is the read/write port into the version context.
type VersionReader interface {
	Get(ctx context.Context, id int64) (VersionBinding, error)
	// Reopen clears the completed projection (completed -> running). It is
	// idempotent for non-completed versions.
	Reopen(ctx context.Context, id int64) error
}

// ExecutorRegistry selects the active executor instance for dispatch.
type ExecutorRegistry interface {
	// Select returns nil,nil when no active executor is registered.
	Select(ctx context.Context) (*execution.ExecutorInstance, error)
}

// ExecutorContext is one executor-side browser context as listed by the
// executor client.
type ExecutorContext struct {
	ID  string
	Key string
}

// ExecutorControl is the executor control port used by pause/cancel/context
// cleanup. Endpoint/token are bound by the adapter.
type ExecutorControl interface {
	CancelContext(ctx context.Context, contextID string) error
	DeleteContext(ctx context.Context, contextID string) error
	ListContexts(ctx context.Context) ([]ExecutorContext, error)
}

// RunCanceller interrupts in-flight API-side case executor runs. CaseRunner
// implements it; pause/cancel/human/recovery services call it.
type RunCanceller interface {
	RegisterRun(caseRunID int64, cancel context.CancelFunc)
	CancelRun(caseRunID int64)
	ClearRun(caseRunID int64)
}

// WorkItemInput is one deduplicated work item to ensure.
type WorkItemInput struct {
	VersionID int64
	Type      string
	Team      string
	DedupeKey string
	Payload   map[string]any
	Priority  int
}

// WorkQueue is the execution context's port to the agent task board. The
// adapter maps it onto agentapp.WorkQueue / work_item repository.
type WorkQueue interface {
	Ensure(ctx context.Context, in WorkItemInput) (id int64, created bool, err error)
	// MarkWaitingHuman parks the currently running run's work item for a
	// human takeover (status waiting_human).
	MarkWaitingHuman(ctx context.Context, workItemID, epoch int64) error
	// Finish closes a waiting_human/pending work item without a failure.
	Finish(ctx context.Context, workItemID int64, reason string) error
}

// QuotaReader loads the currently running case_runs for quota accounting.
type QuotaReader interface {
	RunningCases(ctx context.Context) ([]execution.RunningCase, error)
}

// WaveAnalyzer is the deterministic failure/coverage analysis trigger called
// when a wave reaches its terminal state. AnalyzeWave is idempotent.
type WaveAnalyzer interface {
	AnalyzeWave(ctx context.Context, waveID int64) error
}

// RequirementRef is the coverage projection of one version requirement.
type RequirementRef struct {
	RequirementID        int64
	RequirementVersionID int64
	Status               string
	Title                string
}

// CoverageReader reads the requirement/case execution coverage projection.
type CoverageReader interface {
	// ListRequirements returns current clarified/closed requirements.
	ListRequirements(ctx context.Context, versionID int64) ([]RequirementRef, error)
}

// GapEnsurer ensures a P2 design.gap work item exists. The adapter maps it onto
// the P2 task-board ensure with dedupe gap:{requirement_id}:{case_version_id}.
type GapEnsurer interface {
	EnsureGap(ctx context.Context, versionID, requirementID, caseVersionID int64, reason string) error
}

// TeamPostRef is one structured reference of a team-room post.
type TeamPostRef struct {
	Kind string
	ID   string
}

// TeamPost is one execution team-room post (finding/blocker).
type TeamPost struct {
	VersionID int64
	Team      string
	Kind      string
	Content   string
	Refs      []TeamPostRef
	CreatedBy *int64
}

// TeamRoom is the team-room write port consumed by the execution services.
type TeamRoom interface {
	Post(ctx context.Context, post TeamPost) error
}

// EventPublisher is the shared event publisher port (eventingapp.Publisher
// satisfies it; the alias keeps test fakes small).
type EventPublisher = eventingapp.Publisher

// AuditRecorder is the shared audit port (auditapp.Recorder satisfies it).
type AuditRecorder = auditapp.Recorder

// Clock is the injectable time source used by the execution services.
type Clock interface {
	Now() time.Time
}

// IDGenerator hands out snowflake IDs.
type IDGenerator interface {
	Next() int64
}
