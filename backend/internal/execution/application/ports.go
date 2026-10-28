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
// case must agree with at batch creation (spec §5.2).
type CaseSnapshotSourceView struct {
	CaseNodeID     int64
	Revision       int
	GraphHash      string
	Node           domain.CaseSnapshotNode
	Steps          []domain.CaseSnapshotStep
	Assertions     []domain.CaseSnapshotAssertion
	RequirementIDs []int64
}

// VersionRef is the version projection needed by the batch gates: status and
// resource bindings, the monotonic batch number and the pause mirror.
type VersionRef struct {
	ID                 int64
	Status             string
	Paused             bool
	PauseMode          string
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

// AgentRunState is the polled state of one execution agent run.
type AgentRunState struct {
	RunID      int64
	Status     string
	Error      string
	FinishedAt *time.Time
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

// Recorder is the structured submission surface consumed by the execution
// agent tools (spec §5.5, plan Shared Interfaces). Implemented by T15.
type Recorder interface {
	CaseContext(ctx context.Context, caseRunID int64) (CaseContextView, error)
	RecordStep(ctx context.Context, caseRunID int64, in StepRecordInput) (domain.StepResult, error)
	RecordAssertion(ctx context.Context, caseRunID int64, in AssertionRecordInput) (domain.AssertionResult, error)
	FinishCase(ctx context.Context, caseRunID int64, in FinishInput) (domain.CaseRun, error)
}
