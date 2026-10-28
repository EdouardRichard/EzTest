package application

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ErrBaselineRequired is the MindmapPort sentinel for a version without a
// frozen case_baseline row. The adapter translates the mindmap context's
// baseline_required error into it so BatchService can answer spec §6.4
// baseline_required without importing another context's domain.
var ErrBaselineRequired = errors.New("execution: case baseline is required")

// CodeHistoryPort is the optional code-intelligence surface used to place an
// incremental batch between the previous batch head and the current branch head
// (D49, spec §6.8). A nil dependency degrades: the batch records the parent head
// or the version baseline and leaves head_commit empty rather than blocking.
type CodeHistoryPort interface {
	// HeadCommit returns the current commit of the version's fixed branch.
	HeadCommit(ctx context.Context, versionID int64) (string, error)
	// IsAncestor reports whether base is an ancestor of head.
	IsAncestor(ctx context.Context, versionID int64, base, head string) (bool, error)
}

// SupplementGraphPort is the optional controlled mindmap write surface used
// after a batch is created: every supplement node adopted by the batch becomes
// merged (spec §5.2/§5.3). The mindmap service implements it in P7; until it is
// wired, a batch that selects supplement nodes fails closed instead of
// silently skipping the state transition.
type SupplementGraphPort interface {
	MarkMerged(ctx context.Context, versionID int64, caseNodeIDs []int64) error
}

// ContinuePhasePort is the optional version surface that records
// continue_phase=executing when a continuing version starts an incremental
// batch (spec §5.1/§5.3). It is asserted on VersionPort because the P6 adapter
// predates the method; a missing capability is logged after the committed
// batch.
type ContinuePhasePort interface {
	SetContinuePhase(ctx context.Context, versionID int64, phase string) error
}

// VersionPort is the version context's execution-state surface (spec §4.2,
// plan Shared Interfaces). Execution never owns version status: it only reads
// the version, mirrors the pause flag and records the latest batch number.
type VersionPort interface {
	// GetVersion returns the version projection. A missing or invisible version
	// is the adapter's not-found error.
	GetVersion(ctx context.Context, versionID int64) (VersionRef, error)
	// MarkPaused mirrors the batch pause flag onto the version. It must not
	// change the version status.
	MarkPaused(ctx context.Context, versionID int64, paused bool, mode, reason string, actor int64) error
	// AllocateBatchNo atomically increments version.batch_no and returns the new
	// value (T17 implements it as a single UPDATE ... RETURNING). Batch creation
	// allocates before it inserts the batch, so a created batch always carries a
	// number the version already recorded and the unique (version_id, batch_no)
	// index can only reject a genuinely concurrent creator. Numbers are
	// monotonic, not gapless: a failed create leaves the number unused.
	AllocateBatchNo(ctx context.Context, versionID int64) (int, error)
	// ReleaseBatchNo compensates a failed create: it decrements batch_no back to
	// allocated-1 only while the stored value still equals allocated, so an
	// allocation made by a concurrent creator is never rolled back. (T17
	// implements it.)
	ReleaseBatchNo(ctx context.Context, versionID int64, allocated int) error
	// SetBatchNo records the newest batch number monotonically.
	SetBatchNo(ctx context.Context, versionID int64, batchNo int, actor int64) error
	// CompleteExecution moves an executing, unpaused version to completed.
	CompleteExecution(ctx context.Context, versionID, actor int64) error
}

// MindmapPort is the frozen mindmap projection consumed by execution
// (spec §4.2, §5.2). Execution only reads the baseline and snapshots and marks
// case nodes executed; it never writes mindmap content.
type MindmapPort interface {
	// Baseline returns the frozen baseline of versionID. A version without one
	// returns ErrBaselineRequired.
	Baseline(ctx context.Context, versionID int64) (BaselineRef, error)
	// SnapshotCases returns the immutable snapshot of every requested baseline
	// case. An id outside the frozen baseline is domain.ErrInvalidCaseIDs.
	SnapshotCases(ctx context.Context, versionID int64, caseIDs []int64) ([]CaseSnapshotSourceView, error)
	// MarkExecuted moves reviewed case nodes to executed.
	MarkExecuted(ctx context.Context, versionID int64, caseNodeIDs []int64) error
}

// SessionPort is the resource context's session surface (spec §4.2, §5.9).
// Acquire internally performs selection, auto-login and takeover creation; the
// execution context never drives the session state machine itself.
type SessionPort interface {
	Acquire(ctx context.Context, environmentID, systemID int64, holder string, ttl time.Duration) (SessionRef, error)
	Release(ctx context.Context, sessionID int64, holder, reason string) error
	MarkInvalid(ctx context.Context, sessionID int64, reason string) error
	PendingTakeover(ctx context.Context, sessionID int64) (*TakeoverRef, error)
	Environment(ctx context.Context, environmentID int64) (EnvironmentRef, error)
}

// AgentRuntimePort is the P1 agent runtime surface used to run one case
// (spec §5.5, §7.3). The adapter owns agent sessions, runs and the activity
// channel; execution only starts, polls and cancels a run.
type AgentRuntimePort interface {
	// EnsureProfile idempotently registers the execution-agent profile.
	EnsureProfile(ctx context.Context) error
	// StartCaseRun creates one agent session/run for a case run.
	StartCaseRun(ctx context.Context, req CaseRunRequest) (AgentRunRef, error)
	// GetRun returns the current state of an execution run.
	GetRun(ctx context.Context, agentSessionID int64) (AgentRunState, error)
	// CancelRun aborts the active run of an agent session.
	CancelRun(ctx context.Context, agentSessionID int64) error
	// ListSessions lists the execution agent sessions of a version for the
	// Sessions tab.
	ListSessions(ctx context.Context, versionID int64) ([]AgentSessionRef, error)
}

// EventPublisher publishes one execution event on the version channel
// (spec §5.10). Events are published only after the owning write commits.
type EventPublisher interface {
	Publish(ctx context.Context, ev Event) error
}

// EvidenceStore is the filesystem evidence port (D30, spec §5.7): it validates
// the path stays under the evidence root, writes atomically and returns the
// relative path, sha256 and size.
type EvidenceStore interface {
	Write(ctx context.Context, rel string, data []byte) (path, sha256 string, size int64, err error)
	Open(ctx context.Context, rel string) (io.ReadCloser, error)
}

// Clock returns the current time. It is injectable so services can be tested
// without sleeping.
type Clock interface {
	Now() time.Time
}

// IDGenerator mints Snowflake ids. Every execution id is generated in the
// application layer and passed explicitly to repositories (D29).
type IDGenerator interface {
	Next() int64
}

// BaselineRef is the frozen baseline projection (spec §4.2 MindmapPort):
// canonical graph hash, optional code baseline commit and every baseline case
// node id in ascending order.
type BaselineRef struct {
	Hash       string
	CodeCommit *string
	CaseIDs    []int64
}

// CaseSnapshotSourceView is one frozen case as returned by the mindmap context.
// Revision is the case_node revision; GraphHash is the frozen graph hash every
// case must agree with at batch creation (spec §5.2). Origin is the P7 case
// provenance (baseline or AI supplement) and Status the mindmap node status;
// both are read-only gate inputs. An empty Origin is baseline (P6 snapshots
// have no origin).
type CaseSnapshotSourceView struct {
	CaseNodeID     int64
	Revision       int
	GraphHash      string
	Origin         domain.CaseSource
	Status         string
	Node           domain.CaseSnapshotNode
	Steps          []domain.CaseSnapshotStep
	Assertions     []domain.CaseSnapshotAssertion
	RequirementIDs []int64
}

// VersionRef is the version projection needed by the batch gates: status and
// resource bindings, the monotonic batch number, the pause mirror and the P7
// continue phase.
type VersionRef struct {
	ID                 int64
	Status             string
	Paused             bool
	PauseMode          string
	ContinuePhase      string
	BatchNo            int
	SystemID           *int64
	EnvironmentID      *int64
	CodeBaselineCommit *string
}

// SessionRef is one leased session ready for an execution context (spec §5.9).
// StorageStateKind distinguishes a browser storage_state document from a
// token/apikey/basic header bundle; a token session exposes Headers only, with
// empty browser state, so downstream can skip creating a browser context.
// StorageState and SessionStorage are injected into the browser context;
// Headers carry token/apikey/basic authentication for http.request. Nothing
// here is ever logged or published.
type SessionRef struct {
	SessionID        int64
	SnapshotID       int64
	StorageStateKind string
	StorageState     json.RawMessage
	SessionStorage   json.RawMessage
	Headers          map[string]string
	ExpiresAt        *time.Time
}

// EnvironmentRef is the environment projection used by a case run
// (spec §5.9): the target base URL and the host allowlist.
type EnvironmentRef struct {
	ID           int64
	SystemID     int64
	Name         string
	BaseURL      string
	AllowedHosts []string
}

// TakeoverRef is a waiting human takeover surfaced for a blocked session
// (spec §5.9).
type TakeoverRef struct {
	ID           int64
	SessionID    int64
	Reason       string
	Instructions json.RawMessage
	ExpiresAt    *time.Time
}

// CaseRunRequest starts one execution agent run for a case run (spec §5.5,
// §7.3). The completed steps and cleanup requirement are the resume checkpoint
// handed to the agent.
type CaseRunRequest struct {
	VersionID          int64
	BatchID            int64
	CaseRunID          int64
	CaseNodeID         int64
	CaseNodeTitle      string
	Snapshot           domain.CaseSnapshot
	Session            SessionRef
	Environment        EnvironmentRef
	CompletedSeqs      []int
	InterruptedStepSeq *int
	CleanupRequired    bool
}

// AgentRunRef identifies the agent session and run created for a case run.
type AgentRunRef struct {
	SessionID int64
	RunID     int64
}

// AgentRunState is the polled state of one execution agent run. TerminalReason
// is the P1 run's terminal reason (agent_run.terminal_reason); it lets the
// runner tell an LLM/model failure from a normal end that skipped case_finish.
type AgentRunState struct {
	RunID          int64
	Status         string
	TerminalReason string
	Error          string
	FinishedAt     *time.Time
}

// AgentSessionRef is one execution agent session listed for the Sessions tab.
type AgentSessionRef struct {
	SessionID int64
	Title     string
	Status    string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// EventRefs groups the ids an event references (spec §5.10). Every slice is
// bounded by the event envelope's refs limit of 20.
type EventRefs struct {
	EvidenceIDs    []int64
	CaseIDs        []int64
	RequirementIDs []int64
	Code           []string
}

// Event is one execution event published on the version:{VersionID} channel.
// Detail carries ids, counts and status summaries only: never case bodies,
// prompts, credentials or evidence bytes (spec §5.10).
type Event struct {
	Type      string
	VersionID int64
	BatchID   int64
	CaseID    int64
	StepID    int64
	AgentRole string
	SessionID string
	Summary   string
	Detail    map[string]any
	Refs      EventRefs
}

// StepRecordInput is one step_record submission from the execution agent
// (spec §5.5). Seq is the next unfinished step; Status is passed or failed.
type StepRecordInput struct {
	Seq         int
	Status      domain.StepStatus
	Actual      string
	EvidenceIDs []int64
}

// AssertionRecordInput is one assertion_record submission from the execution
// agent (spec §5.5). Verdict is passed or failed; evidence is mandatory for
// both.
type AssertionRecordInput struct {
	AssertionID int64
	Verdict     string
	Actual      string
	EvidenceIDs []int64
}

// FinishInput is one case_finish submission from the execution agent
// (spec §5.5/§5.8).
type FinishInput struct {
	Decision    domain.FinishDecision
	EvidenceIDs []int64
}

// CaseContextCompletedStep is one recorded step of the resume checkpoint
// (spec §5.5). The actual result and evidence count let a resumed agent continue
// after the step without replaying it or re-reading its transcript.
type CaseContextCompletedStep struct {
	Seq           int
	Status        domain.StepStatus
	Actual        string
	EvidenceCount int
}

// CaseContextView is the execution agent's read model of one case run
// (spec §5.5): the pinned run, its checkpoint and the target allowlist.
// CompletedSteps/RemainingSteps are the resume checkpoint the runtime prompt and
// the execution.case_context tool render; CompletedSeqs stays the compact form.
type CaseContextView struct {
	CaseRun            domain.CaseRun
	CompletedSeqs      []int
	CompletedSteps     []CaseContextCompletedStep
	RemainingSteps     []domain.CaseSnapshotStep
	InterruptedStepSeq *int
	CleanupRequired    bool
	BaseURL            string
	AllowedHosts       []string
}

// DB assertion vocabulary (spec §5.7/§7.11) shared by the execution.db_assert
// use case and the resource adapter. The reason strings are the stable
// machine-readable values of the error table; every db failure crossing this
// port carries one of them.
const (
	// DBReasonTargetMissing reports an unconfigured or disabled environment
	// target (environment failure).
	DBReasonTargetMissing = "db_target_missing"
	// DBReasonConnectionFailed reports connect/auth/DNS/timeout failures
	// (environment failure).
	DBReasonConnectionFailed = "db_connection_failed"
	// DBReasonQueryFailed reports SQL syntax/permission/execution failures
	// (case failure).
	DBReasonQueryFailed = "db_query_failed"
	// DBReasonReadonlyViolation reports a statement rejected by the read-only
	// gate (case failure).
	DBReasonReadonlyViolation = "db_readonly_violation"
	// DBReasonResultTruncated reports a result above the row/byte limit (case
	// failure).
	DBReasonResultTruncated = "db_result_truncated"
	// DBReasonExpectedInvalid reports an unparseable db assertion expected value
	// (case failure).
	DBReasonExpectedInvalid = "db_expected_invalid"
)

// DBTargetRef is the enabled read-only target of one environment as projected
// to the execution context. It deliberately carries no credentials: the
// resource adapter owns the password and the querier call.
type DBTargetRef struct {
	EnvironmentID      int64
	Name               string
	MaxRows            int
	StatementTimeoutMS int
}

// DBQueryLimits bounds one db_assert query. Zero fields fall back to the target
// configuration and the spec defaults inside the resource adapter.
type DBQueryLimits struct {
	MaxRows   int
	TimeoutMS int
	MaxBytes  int
}

// DBQueryResult is the normalized read-only result projection (spec §5.7):
// every cell is a string, SQL NULL is "null", numbers are plain decimals and
// timestamps are RFC3339. Truncated reports the row/byte cap was hit; it is not
// an error.
type DBQueryResult struct {
	Columns   []string
	Rows      [][]string
	RowCount  int
	Truncated bool
	ElapsedMS int64
}

// DBQueryPort resolves and executes the environment's read-only db target for
// the execution.db_assert tool. Target returns DBQueryError{db_target_missing}
// for an unconfigured or disabled target; Query returns a DBQueryError with one
// of the db_* reasons. Implementations never log or return the password.
type DBQueryPort interface {
	Target(ctx context.Context, environmentID int64) (DBTargetRef, error)
	Query(ctx context.Context, environmentID int64, sql string, limits DBQueryLimits) (DBQueryResult, error)
}

// DBQueryError is the classified database failure crossing the DBQueryPort. Its
// Message must be safe to show the agent: no credentials, DSNs or result data.
type DBQueryError struct {
	Reason  string
	Message string
}

// Error implements error.
func (e *DBQueryError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	return e.Reason
}

// EvidenceCapturePort is the narrow artifact-capture surface of the
// execution.db_assert use case. *EvidenceService satisfies it.
type EvidenceCapturePort interface {
	Capture(ctx context.Context, in CaptureInput) (domain.Evidence, error)
}

// DBAssertAuditEvent is one execution.db_assert.executed audit entry (spec
// §11.2). It carries ids, verdict and counts only: never the SQL, the result
// rows, credentials or the model preview.
type DBAssertAuditEvent struct {
	AssertionResultID int64
	CaseRunID         int64
	Verdict           string
	RowCount          int
	ElapsedMS         int64
}

// DBAssertAuditor is the optional audit capability of the execution.db_assert
// use case. The case-finish auditor is also the db audit sink when it implements
// this interface; auditing stays best-effort (spec §11.2).
type DBAssertAuditor interface {
	DBAssertExecuted(ctx context.Context, event DBAssertAuditEvent) error
}

// Recorder is the structured submission surface consumed by the execution
// agent tools (spec §5.5, plan Shared Interfaces). Implemented by T15.
type Recorder interface {
	CaseContext(ctx context.Context, caseRunID int64) (CaseContextView, error)
	RecordStep(ctx context.Context, caseRunID int64, in StepRecordInput) (domain.StepResult, error)
	RecordAssertion(ctx context.Context, caseRunID int64, in AssertionRecordInput) (domain.AssertionResult, error)
	// DBAssert executes one db assertion of the case run and records its
	// db_result evidence and assertion_result idempotently (spec §9.5).
	DBAssert(ctx context.Context, caseRunID, assertionID int64, limits DBAssertLimits) (DBAssertOutcome, error)
	FinishCase(ctx context.Context, caseRunID int64, in FinishInput) (domain.CaseRun, error)
}

// AutonomyTrigger is the scheduler's hand-off to the autonomous chain (spec
// §5.3/§6.2). The scheduler calls it after a batch terminal CAS committed so the
// worker can advance the chain immediately instead of waiting for its next tick.
// A nil trigger degrades: the worker still observes the batch on its next tick.
type AutonomyTrigger interface {
	OnBatchTerminal(ctx context.Context, batch domain.RunBatch)
}

// DesigningVersion is one version of the continuing+designing scan of the
// autonomous worker (spec §6.2 second section): the version projection and its
// newest terminal batch, which is the parent of the incremental design run.
type DesigningVersion struct {
	Version VersionRef
	Parent  *domain.RunBatch
}

// AutonomyBatchStore is the persistence surface of the autonomous worker. Every
// advance is derived from these reads and compare-and-swap writes, so a worker
// restart can resume the chain from the stored state (spec §6.2/§6.7).
//
// The adapter belongs to the execution context's persistence layer and is wired
// by T17. The implementation must:
//   - filter terminal batches by version status (executing/continuing) and
//     paused=false, and order newest first;
//   - implement the two CAS writes as single UPDATE ... WHERE <flag> = false
//     statements reporting whether this call won;
//   - return nil from SupplementChild when no supplement batch exists.
type AutonomyBatchStore interface {
	// PendingAutonomyBatches returns terminal batches (done/failed/cancelled)
	// with autonomy_done=false whose version is executing or continuing and not
	// paused, newest first, bounded by limit.
	PendingAutonomyBatches(ctx context.Context, limit int) ([]domain.RunBatch, error)
	// DesigningVersions returns the continuing versions whose continue_phase is
	// designing and that are not paused, each with its newest terminal batch
	// (Parent is nil when the version never ran a batch).
	DesigningVersions(ctx context.Context, limit int) ([]DesigningVersion, error)
	// VersionsAwaitingFinalize is the DB-derived finalize sweep: executing or
	// continuing versions that have at least one batch, no active (planned or
	// running) batch, and no terminal batch with autonomy_done=false. It lets a
	// restarted worker retry a finalize that a crash or an error skipped after
	// the batch's autonomy_done CAS. Bounded by limit; try-finalize itself
	// re-checks every condition and is idempotent.
	VersionsAwaitingFinalize(ctx context.Context, limit int) ([]int64, error)
	// MarkSupplementAttempted is the per-batch one-cycle CAS
	// (supplement_attempted false -> true). won reports whether this call won.
	MarkSupplementAttempted(ctx context.Context, batchID int64) (bool, error)
	// MarkAutonomyDone is the chain-settled CAS (autonomy_done false -> true).
	// won reports whether this call won; a lost CAS means the chain was already
	// settled (for example by a previous worker instance).
	MarkAutonomyDone(ctx context.Context, batchID int64) (bool, error)
	// ResetAutonomyDone reopens a settled terminal batch chain
	// (autonomy_done true -> false) so a manual analysis started on an already
	// completed chain is picked up by the worker again. won reports whether this
	// call performed the transition. supplement_attempted is intentionally left
	// true: a re-analysis must not run the post-batch supplement cycle twice.
	ResetAutonomyDone(ctx context.Context, batchID int64) (bool, error)
	// SupplementChild returns the supplement batch whose parent is
	// parentBatchID, or nil when none exists. A supplement batch is always a
	// child of a terminal baseline batch (spec §5.3).
	SupplementChild(ctx context.Context, parentBatchID int64) (*domain.RunBatch, error)
}
