package executionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Case-run defaults. The lease must outlive one executor action: the executor
// caps an action at 60s and the HTTP client bounds it at 75s (see
// executor.DefaultTimeout). 90s leaves headroom, and the independent heartbeat
// renews the lease while an action is in flight, so a slow action never makes
// recovery misread the run as executor_lost.
const (
	// minCaseLease stays above the 75s executor client bound so a single slow
	// action can never outlive the lease even before the first heartbeat.
	minCaseLease = 80 * time.Second
	// DefaultCaseLease is the case_run execution lease the agent stamps in
	// Prepare and keeps alive with the independent heartbeat. It is deliberately
	// shorter than the scheduler's DefaultDispatchLease: the scheduler stamps the
	// dispatch lease when the work item is only queued, and Prepare replaces it
	// with this lease once an agent worker actually claims the run.
	DefaultCaseLease        = 90 * time.Second
	heartbeatRetryDelay     = time.Second
	defaultEvidenceMaxBytes = int64(5 << 20)
	maxSUTFilenameLength    = 255
	// executorSessionKeyFormat is the executor v2 session key of one
	// (case_run, SUT, account_role) triple, and what orphan reconciliation
	// matches on. This literal is duplicated in
	// infrastructure/executor/client.go SessionKey — the two MUST change in the
	// same commit or reconciliation silently stops matching.
	executorSessionKeyFormat = "run:%d:sut:%d:role:%s"
	// terminalScreenshotTimeout bounds the P3 §6.4 automatic screenshot of a
	// failed/blocked/inconclusive run. It is independent from the (possibly
	// canceled) run context: the capture is best-effort and must never extend a
	// terminal write.
	terminalScreenshotTimeout = 8 * time.Second
	// sessionEvidenceSettleTimeout bounds the terminal recording-ledger drain.
	// It is deliberately NOT the screenshot budget: the drain pulls every
	// command/file/trace artifact of every session, recomputes sha256 and writes
	// content + evidence rows, which for a real run is tens/hundreds of MB. An
	// 8s shared budget was the most likely way to lose a whole ledger.
	sessionEvidenceSettleTimeout = 60 * time.Second
	// evidenceDropFactTimeout bounds the evidence-drop FACT write. It runs on a
	// fresh background context because the settlement budget that just failed
	// may already be exhausted, and an unrecorded loss is an invisible loss.
	evidenceDropFactTimeout = 10 * time.Second
	// runFailureEvidenceFilename names the text evidence carrying the concrete
	// error of a run that recorded no step/assertion result.
	runFailureEvidenceFilename = "agent-failure.txt"
)

// SUTBindingResolver resolves the task-context bindings of a version (base URL,
// page URLs, API endpoints, account roles). The version adapter implements it
// from the version application read model.
type SUTBindingResolver interface {
	Resolve(ctx context.Context, versionID int64) ([]execution.SUTBinding, error)
}

// SUTAPI is one version-bound API endpoint resolved for http.request.
type SUTAPI struct {
	Method string
	Path   string
}

// SUTAPIResolver resolves a SUT-bound api_id. Optional: when nil,
// http.request requires a literal path instead of api_id.
type SUTAPIResolver interface {
	API(ctx context.Context, versionID, sutID, apiID int64) (SUTAPI, error)
}

// CaseSuccessFact is the passed-case observation handed to the optional
// knowledge success hook (spec §10.1 成功钩子). It carries identifiers only:
// page/API/account references, the case title and the evidence count, never
// knowledge content or credentials. The knowledge context turns it into
// page_locator / api_auth / login_recipe candidates; the executor itself stays
// knowledge-agnostic.
type CaseSuccessFact struct {
	VersionID     int64
	CaseRunID     int64
	CaseVersionID int64
	Title         string
	AccountRoles  []string
	PageIDs       []int64
	APIIDs        []int64
	EvidenceCount int
}

// CaseSuccessHook is notified once when a case_run finishes passed. It is best
// effort: a returned error or a panic is logged and never changes the case_run
// result or blocks the run (spec §10.1; knowledge hooks are supplemental).
// A nil hook (the default before assembly wiring) disables the notification.
type CaseSuccessHook interface {
	OnCaseRunPassed(ctx context.Context, fact CaseSuccessFact) error
}

// CaseFailureFact is the failed-case observation handed to the optional
// knowledge failure hook (spec §10.1 失败复用 → suspect). It mirrors
// CaseSuccessFact: identifiers (page/API/account-role back-references), the
// version and the run identity plus the failure class, never knowledge content,
// a case title or a credential.
type CaseFailureFact struct {
	VersionID     int64
	CaseRunID     int64
	CaseVersionID int64
	FailureClass  execution.FailureClass
	AccountRoles  []string
	PageIDs       []int64
	APIIDs        []int64
}

// CaseFailureHook is notified once when a case_run finishes failed. It exists so
// the knowledge lifecycle can move the knowledge reused by the failed run to
// suspect. Like CaseSuccessHook it is deliberately best effort: an error or a
// panic is logged and never changes the persisted verdict. A nil hook disables
// the notification.
type CaseFailureHook interface {
	OnCaseRunFailed(ctx context.Context, fact CaseFailureFact) error
}

// SessionSpec is one lazily created executor session (protocol v2). Workspace is
// the case_run-scoped directory anchor every command runs in and every recording
// spills to; BaseURL is an informational hint for the retained browser bridge.
type SessionSpec struct {
	Key       string
	Workspace string
	BaseURL   string
	// ExecutorID selects the owning executor endpoint. Zero/unknown ids fall
	// back to the primary executor endpoint for single-endpoint deployments.
	ExecutorID int64
	Metadata   map[string]any
	// LeaseEpoch mirrors the case_run dispatch epoch: the executor fences every
	// write with it, so a stale dispatch cannot mutate a taken-over session.
	LeaseEpoch int64
	LeaseTTL   time.Duration
}

// ExecCommand is one general-execution command issued inside a session.
type ExecCommand struct {
	Argv       []string
	Shell      bool
	Stdin      string
	TimeoutMS  int
	Background bool
	Env        map[string]string
}

// ExecOutcome is the settled result of one command plus the evidence it produced.
type ExecOutcome struct {
	SessionID       string
	CommandID       string
	Exit            *int
	Outcome         string
	DurationMS      int64
	Stdout          string
	Stderr          string
	StdoutTruncated bool
	StderrTruncated bool
	StdoutRef       string
	StderrRef       string
	Background      bool
	TimedOut        bool
	Cancelled       bool
	EvidenceIDs     []string
	EvidenceSkipped int
}

// ShellOperation is one PTY shell operation (protocol v2 shell channel). Op is
// the protocol verb (create/write/read/close/wait); the unused fields of another
// op stay zero. The shell.* and pty.* tool families share this ONE channel:
// shell.open omits Argv (the executor's platform default shell), pty.open names
// an explicit interactive program (ssh/psql/mysql/REPL) and terminal size.
type ShellOperation struct {
	Op        string
	ShellID   string
	Argv      []string
	Cols      int
	Rows      int
	Env       map[string]string
	Data      string
	Since     int64
	TimeoutMS int
}

// ShellOutcome is the union answer of the shell operations; which fields are
// populated depends on the requested op.
type ShellOutcome struct {
	ShellID    string
	PID        int
	Status     string
	Data       string
	NextCursor int64
	TotalBytes int64
	Truncated  bool
	Exit       *int
	Outcome    string
	OK         bool
}

// FileOperation is one workspace files operation (upload/download/list). Base64
// carries the upload payload; the workspace confinement is the executor's.
type FileOperation struct {
	Op     string
	Path   string
	Base64 string
}

// FileEntry is one workspace directory entry.
type FileEntry struct {
	Path  string
	Bytes int64
	MIME  string
	Dir   bool
}

// FileOutcome is the union answer of the files operations.
type FileOutcome struct {
	Path      string
	Bytes     int64
	SHA256    string
	MIME      string
	Base64    string
	Files     []FileEntry
	Truncated bool
}

// ExecutionArtifact is one recording-layer artifact of a session ledger.
type ExecutionArtifact struct {
	Kind            string
	CommandID       string
	Path            string
	SHA256          string
	Size            int64
	MIME            string
	Filename        string
	Base64          string
	ContentOmitted  bool
	ContentReason   string
	RecordingFailed bool
	Error           string
}

// ArtifactPage is one incremental pull of the recording ledger. Gap/Missed mean
// the settlement cursor fell behind the executor's bounded buffer: the missed
// artifacts are counted as evidence drops, never silently ignored.
type ArtifactPage struct {
	Artifacts      []ExecutionArtifact
	NextCursor     int64
	BaseCursor     int64
	Evicted        int64
	Gap            bool
	Missed         int64
	EvictionFailed int64
}

// TraceRef describes the trace file of a session, when one exists.
type TraceRef struct {
	Available bool
	Path      string
	Bytes     int64
	SHA256    string
	MIME      string
}

// ExecutionExecutor is the executor protocol v2 port consumed by the execution
// use cases. executorID selects the endpoint that owns the session (multi-executor
// routing); zero falls back to the primary endpoint. Every write carries the
// session lease epoch so a stale dispatch is fenced by the executor.
//
// The transitional Playwright-library browser bridge (BrowserAction) was removed
// in v0.2.4-C T16: the browser capability domain runs the official playwright-cli
// over Exec (T15), and browser traffic is pure-recorded via a passive CDP observer
// read through the optional NetworkLogReader surface (design §4.3).
type ExecutionExecutor interface {
	CreateSession(ctx context.Context, spec SessionSpec) (string, error)
	Exec(ctx context.Context, executorID int64, sessionID string, command ExecCommand, leaseEpoch int64) (ExecOutcome, error)
	// Shell runs one PTY shell operation (the exec/shell/file capability domains'
	// interactive channel). Every write carries the session lease epoch.
	Shell(ctx context.Context, executorID int64, sessionID string, op ShellOperation, leaseEpoch int64) (ShellOutcome, error)
	// Files runs one workspace files operation (upload/download/list).
	Files(ctx context.Context, executorID int64, sessionID string, op FileOperation, leaseEpoch int64) (FileOutcome, error)
	Artifacts(ctx context.Context, executorID int64, sessionID string, since int64) (ArtifactPage, error)
	Trace(ctx context.Context, executorID int64, sessionID string) (TraceRef, error)
	// RenewLease keeps a session lease alive while a long command runs: the
	// command deadline (15min) is far above the session TTL (300s), so without
	// renewal the executor's orphan sweeper would reclaim a running session.
	RenewLease(ctx context.Context, executorID int64, sessionID string, leaseEpoch int64) error
	Cancel(ctx context.Context, executorID int64, sessionID string) error
	Close(ctx context.Context, executorID int64, sessionID string) error
}

// NetworkLogReader is the OPTIONAL browser-traffic read surface (T16, design
// §4.3 pure recording). An executor adapter that supports passive CDP traffic
// recording implements it; the terminal discovery capture type-asserts for it
// (the GeneralExecutionPort precedent) and skips best-effort when absent. The
// raw payload is the recorded network log {items:[...], recording, degraded}.
type NetworkLogReader interface {
	NetworkLog(ctx context.Context, executorID int64, sessionID string, tail int) (json.RawMessage, error)
}

// HTTPRequestData is one target-SUT HTTP request issued by http.request.
// TimeoutMS bounds the whole request (connect + body read); zero uses the
// adapter default (15s). v0.2.4-C T12 (P8): configurable per call.
type HTTPRequestData struct {
	Method    string
	URL       string
	Headers   map[string]string
	Body      []byte
	TimeoutMS int
}

// HTTPResponseData is one target-SUT HTTP response.
type HTTPResponseData struct {
	Status     int
	Headers    map[string][]string
	Body       []byte
	DurationMS int64
}

// HTTPDoer performs the target-SUT HTTP request. The adapter binds no base URL:
// the caller resolves and validates the SUT environment URL.
type HTTPDoer interface {
	Do(ctx context.Context, req HTTPRequestData) (HTTPResponseData, error)
}

// DBQueryResult is one target-database query result. v0.2.4-C T12 (P8): general
// read-write SQL — RowsAffected/CommandTag describe write statements; Rows holds
// the returned rows (unbounded unless MaxRows explicitly caps them).
type DBQueryResult struct {
	Columns      []string
	Rows         [][]string
	RowCount     int
	Truncated    bool
	RowsAffected int64
	CommandTag   string
}

// DBQueryOptions bounds one db.query call. MaxRows <= 0 means unlimited (the
// old hard 200-row cap is lifted); StatementTimeoutMS <= 0 uses the adapter
// default (5s). Both are configurable per call (P8).
type DBQueryOptions struct {
	MaxRows            int
	StatementTimeoutMS int
}

// TableColumn is one column of a target database table.
type TableColumn struct {
	Name     string
	Type     string
	Nullable bool
}

// TableSchema is one table schema summary.
type TableSchema struct {
	Schema  string
	Table   string
	Columns []TableColumn
}

// DatabaseQuerier runs general read-write SQL and schema summaries on the target
// PostgreSQL database (adapter owns the DSN/statement timeout/transaction mode).
// v0.2.4-C T12 (P8): the read-only restriction is lifted.
type DatabaseQuerier interface {
	Query(ctx context.Context, dsn, username, password, sql string, opts DBQueryOptions) (DBQueryResult, error)
	Schema(ctx context.Context, dsn, username, password, schema string) ([]TableSchema, error)
}

// HumanRequestService creates one login-takeover request from the tool. It is
// implemented by the Task 9 human service; nil disables the tool.
type HumanRequestService interface {
	CreateFromTool(ctx context.Context, scope tools.Scope, in tools.HumanRequestInput) (int64, error)
}

// CaseRunnerDeps wires the P3 case executor runtime.
type CaseRunnerDeps struct {
	Intents     IntentRepository
	CaseRuns    CaseRunRepository
	SUTContexts SUTContextRepository
	Results     ResultRepository
	Evidence    EvidenceRepository
	Humans      HumanRequestRepository
	Cases       CaseReader
	Bindings    SUTBindingResolver
	// Versions resolves the current environment fingerprint / code baseline for
	// the command-log replay same-fingerprint gate (EV-006). Optional: a nil
	// reader disables replay (the entry returns a configuration error).
	Versions    VersionReader
	APIs        SUTAPIResolver
	Execution   ExecutionExecutor
	Credentials CredentialReader
	Content     ContentStore
	HTTPClient  HTTPDoer
	Database    DatabaseQuerier
	Coverage    CoverageReader
	Gaps        GapEnsurer
	TeamRoom    TeamRoom
	Human       HumanRequestService
	// Discovery receives the terminal-state discovery observations (WP-1).
	// Optional: a nil writer skips the observation write; W1-11 provides the
	// postgres adapter and assembles it.
	Discovery DiscoveryObservationWriter
	// KnowledgeHook receives passed-case observations for knowledge
	// sedimentation (spec §10.1). Optional and best effort.
	KnowledgeHook CaseSuccessHook
	// KnowledgeFailureHook receives failed-case observations so the knowledge
	// reused by the failed run can be marked suspect. Optional and best effort.
	KnowledgeFailureHook CaseFailureHook
	// Resolutions reads the run's sut_resolution facts — the knowledge page/API
	// dimension true source (v0.2.4-F T14, SC-F10): the latest fact carries the
	// resolved SUT's validated (touched) asset ids, non-empty even for a
	// directory-hit run that never explores (controller ruling P20). Optional:
	// a nil reader keeps the dimension empty (never fabricated).
	Resolutions execution.SUTResolutionRepository
	// Registrations reads the resolved SUTs' asset_registration facts — the
	// proven-discovery share of the knowledge page/API dimension (SC-F10).
	// Optional: a nil reader keeps that share empty.
	Registrations execution.AssetRegistrationRepository
	Settings      SettingsReader
	Clock         Clock
	IDs           IDGenerator
	Events        EventPublisher
	Audit         AuditRecorder
	InstanceID    string
	// LeaseTTL overrides the case_run lease. Values below 80s (the executor
	// client bound is 75s) are raised to the minimum; zero selects the 90s
	// default.
	LeaseTTL time.Duration
	// HeartbeatInterval overrides the independent lease renewal interval.
	// Zero selects LeaseTTL/3.
	HeartbeatInterval time.Duration
}

// CaseRunner executes one case_run as an API-side agent: it claims the run
// lease, records fenced step/assertion/evidence results, exposes the browser/db/
// http tools and aggregates the deterministic four-state verdict. It is also the
// tools.ExecutionPort implementation and the pause/cancel RunCanceller.
type CaseRunner struct {
	intents     IntentRepository
	caseRuns    CaseRunRepository
	sutContexts SUTContextRepository
	results     ResultRepository
	evidence    EvidenceRepository
	humans      HumanRequestRepository
	cases       CaseReader
	bindings    SUTBindingResolver
	versions    VersionReader
	apis        SUTAPIResolver
	execution   ExecutionExecutor
	credentials CredentialReader
	content     ContentStore
	httpClient  HTTPDoer
	database    DatabaseQuerier
	coverage    CoverageReader
	gaps        GapEnsurer
	teamRoom    TeamRoom
	human       HumanRequestService
	discovery   DiscoveryObservationWriter
	knowledge   CaseSuccessHook
	// knowledgeFailure is the optional failed-case knowledge hook.
	knowledgeFailure CaseFailureHook
	// resolutions/registrations feed the knowledge page/API dimension true
	// source (SC-F10); nil keeps the dimension empty.
	resolutions   execution.SUTResolutionRepository
	registrations execution.AssetRegistrationRepository
	settings      SettingsReader
	clock         Clock
	ids           IDGenerator
	events        EventPublisher
	audit         AuditRecorder
	instanceID    string
	leaseTTL      time.Duration
	heartbeat     time.Duration
	// attribution is the deterministic four-value failure attribution the
	// analyze.failure result exposes to the failure_analyst model (Task 37).
	attribution *FailureAttribution

	mu          sync.Mutex
	activeRuns  map[int64]context.CancelFunc
	heartbeats  map[int64]*leaseHeartbeat
	waitTotals  map[int64]time.Duration
	waitStarted map[int64]time.Time
}

// leaseHeartbeat is one independent renewal timer. The dispatch epoch is part of
// its identity: a stale Finish/Finalize from an earlier epoch must not stop the
// heartbeat of the dispatch that superseded it.
type leaseHeartbeat struct {
	epoch  int64
	cancel context.CancelFunc
}

var (
	_ tools.ExecutionPort = (*CaseRunner)(nil)
	_ RunCanceller        = (*CaseRunner)(nil)
)

// NewCaseRunner wires the case executor runtime.
func NewCaseRunner(deps CaseRunnerDeps) *CaseRunner {
	lease := deps.LeaseTTL
	if lease <= 0 {
		lease = DefaultCaseLease
	}
	if lease < minCaseLease {
		lease = minCaseLease
	}
	heartbeat := deps.HeartbeatInterval
	if heartbeat <= 0 {
		heartbeat = lease / 3
	}
	return &CaseRunner{
		intents:     deps.Intents,
		caseRuns:    deps.CaseRuns,
		sutContexts: deps.SUTContexts,
		results:     deps.Results,
		evidence:    deps.Evidence,
		humans:      deps.Humans,
		cases:       deps.Cases,
		bindings:    deps.Bindings,
		versions:    deps.Versions,
		apis:        deps.APIs,
		execution:   deps.Execution,
		credentials: deps.Credentials,
		content:     deps.Content,
		httpClient:  deps.HTTPClient,
		database:    deps.Database,
		coverage:    deps.Coverage,
		gaps:        deps.Gaps,
		teamRoom:    deps.TeamRoom,
		human:       deps.Human,
		discovery:   deps.Discovery,
		knowledge:   deps.KnowledgeHook,
		// knowledgeFailure is optional: a nil field disables the failed-case
		// notification.
		knowledgeFailure: deps.KnowledgeFailureHook,
		resolutions:      deps.Resolutions,
		registrations:    deps.Registrations,
		settings:         deps.Settings,
		clock:            deps.Clock,
		ids:              deps.IDs,
		events:           deps.Events,
		audit:            deps.Audit,
		instanceID:       deps.InstanceID,
		leaseTTL:         lease,
		heartbeat:        heartbeat,
		attribution:      NewFailureAttribution(),
		activeRuns:       map[int64]context.CancelFunc{},
		heartbeats:       map[int64]*leaseHeartbeat{},
		waitTotals:       map[int64]time.Duration{},
		waitStarted:      map[int64]time.Time{},
	}
}

// RegisterRun records the cancel function of the in-flight agent run so a pause,
// cancel or human takeover can interrupt it.
func (s *CaseRunner) RegisterRun(caseRunID int64, cancel context.CancelFunc) {
	if caseRunID <= 0 || cancel == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeRuns[caseRunID] = cancel
}

// CancelRun interrupts the in-flight run of a case_run (pause/immediate cancel).
func (s *CaseRunner) CancelRun(caseRunID int64) {
	s.mu.Lock()
	cancel := s.activeRuns[caseRunID]
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// ClearRun forgets the cancel function after a run finished. It deliberately
// does not stop the lease heartbeat: the heartbeat is bound to the run context
// (the worker cancels it when the run returns), and a late ClearRun from an old
// dispatch epoch must not kill the heartbeat of a superseding run.
func (s *CaseRunner) ClearRun(caseRunID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.activeRuns, caseRunID)
}

// Prepare validates and claims one execute.case work item, then returns the
// immutable task snapshot for the agent context. It creates no browser context
// (those are lazy, on the first browser tool).
func (s *CaseRunner) Prepare(ctx context.Context, item agent.WorkItem) (execution.TaskSnapshot, error) {
	if s.caseRuns == nil || s.cases == nil || s.bindings == nil {
		return execution.TaskSnapshot{}, errors.New("case runner is not configured")
	}
	payload, err := agentapp.ParseWorkItemPayload(item.Payload)
	if err != nil {
		return execution.TaskSnapshot{}, platform.Invalid("payload", err.Error())
	}
	if err := validateExecutionPayload(payload); err != nil {
		return execution.TaskSnapshot{}, err
	}
	caseRun, err := s.caseRuns.Get(ctx, payload.CaseRunID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "case_run")
	}
	if caseRun.WaveID != payload.WaveID || caseRun.IntentID != payload.IntentID ||
		caseRun.CaseVersionID != payload.CaseVersionID {
		return execution.TaskSnapshot{}, platform.LeaseLost()
	}
	if caseRun.DispatchEpoch != payload.DispatchEpoch || caseRun.DispatchToken != payload.DispatchToken {
		return execution.TaskSnapshot{}, platform.LeaseLost()
	}
	if caseRun.Status != execution.CaseRunPending && caseRun.Status != execution.CaseRunRunning {
		return execution.TaskSnapshot{}, platform.StateConflict("case_run is not dispatchable in status " + string(caseRun.Status))
	}

	intent, err := s.intents.Get(ctx, payload.IntentID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "execution_intent")
	}
	if !intent.Status.Active() {
		return execution.TaskSnapshot{}, platform.StateConflict("execution_intent is no longer active")
	}
	if intent.VersionID != item.VersionID {
		return execution.TaskSnapshot{}, platform.StateConflict("work item version does not match the execution intent")
	}

	caseSnapshot, err := s.cases.GetCaseVersion(ctx, item.VersionID, payload.CaseVersionID)
	if err != nil {
		return execution.TaskSnapshot{}, mapCaseReadError(err)
	}
	if caseSnapshot.CaseVersionID == 0 {
		caseSnapshot.CaseVersionID = payload.CaseVersionID
	}

	bindings, err := s.bindings.Resolve(ctx, item.VersionID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "version")
	}
	suts, err := executionSUTBindings(&caseSnapshot, bindings)
	if err != nil {
		return execution.TaskSnapshot{}, err
	}

	if open, err := s.openHumanRequest(ctx, payload.CaseRunID); err != nil {
		return execution.TaskSnapshot{}, err
	} else if open != nil {
		return execution.TaskSnapshot{}, platform.StateConflict("case_run is waiting for a human takeover")
	}
	humanResponse, err := s.humanResponse(ctx, caseRun)
	if err != nil {
		return execution.TaskSnapshot{}, err
	}

	// Claim the intent and start the case run under the dispatch fence before
	// creating any side effect rows.
	if intent.Status != execution.IntentRunning {
		if _, err := s.intents.Transition(ctx, intent.ID,
			[]execution.IntentStatus{execution.IntentOpen, execution.IntentClaimed}, execution.IntentRunning); err != nil {
			return execution.TaskSnapshot{}, mapExecutionError(err, "execution_intent")
		}
	}
	now := s.now()
	leaseUntil := now.Add(s.leaseDuration())
	ok, err := s.caseRuns.UpdateCAS(ctx, caseRun.ID, payload.DispatchEpoch, payload.DispatchToken, func(run *execution.CaseRun) error {
		if run.Status != execution.CaseRunPending && run.Status != execution.CaseRunRunning {
			return execution.ErrStateConflict
		}
		run.Status = execution.CaseRunRunning
		if run.StartedAt == nil {
			started := now
			run.StartedAt = &started
		}
		run.OwnerInstance = s.instanceID
		run.LeaseUntil = &leaseUntil
		if run.Attempts < 1 {
			run.Attempts = 1
		}
		// Claiming the work item is the only proof the run left the queue:
		// Recovery uses this marker to tell a dispatch (queue) timeout from an
		// execution lease expiry.
		if run.Summary.Dispatch == nil {
			run.Summary.Dispatch = &execution.CaseRunDispatchDiagnostic{Attempt: run.Attempts}
		}
		if run.Summary.Dispatch.ClaimedAt == "" {
			run.Summary.Dispatch.ClaimedAt = now.UTC().Format(time.RFC3339Nano)
		}
		return nil
	})
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "case_run")
	}
	if !ok {
		return execution.TaskSnapshot{}, platform.LeaseLost()
	}
	startedRun, err := s.caseRuns.Get(ctx, caseRun.ID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "case_run")
	}

	if err := s.ensureSUTContexts(ctx, startedRun, caseSnapshot, payload.DispatchEpoch); err != nil {
		return execution.TaskSnapshot{}, err
	}
	priorSteps, err := s.results.ListSteps(ctx, caseRun.ID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "step_result")
	}
	priorAssertions, err := s.results.ListAssertions(ctx, caseRun.ID)
	if err != nil {
		return execution.TaskSnapshot{}, mapExecutionError(err, "assertion_result")
	}

	s.resetWaitTracking(caseRun.ID, now)
	// Renew the case_run lease independently of tool result writes: one
	// executor action may take up to 75s while no step/assertion row is
	// written. The heartbeat stops when the worker cancels the run context, or
	// explicitly on Finish/Finalize/ClearRun.
	s.startLeaseHeartbeat(ctx, startedRun)
	s.publishCaseRunEvent(EventCaseRunStarted, startedRun, map[string]any{
		"status": string(startedRun.Status),
	})

	// Every binding of a version shares one environment (version binds an
	// environment directly, v0.2.4-B); stamp it once for the task context.
	environmentID := int64(0)
	if len(suts) > 0 {
		environmentID = suts[0].EnvironmentID
	}
	return execution.TaskSnapshot{
		Case:            caseSnapshot,
		Intent:          intent,
		CaseRun:         startedRun,
		EnvironmentID:   environmentID,
		SUTs:            suts,
		PriorSteps:      priorSteps,
		PriorAssertions: priorAssertions,
		HumanResponse:   humanResponse,
	}, nil
}

// StepRecord upserts one step result under the case_run dispatch fence.
func (s *CaseRunner) StepRecord(ctx context.Context, scope tools.Scope, in tools.StepRecordInput) (any, error) {
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	caseSnapshot, err := s.caseSnapshot(ctx, scope, caseRun)
	if err != nil {
		return nil, err
	}
	// v0.2.4-B: the step is looked up only to validate the seq — a step no
	// longer carries a target id to inherit, so there is nothing else to read.
	if _, ok := findStep(caseSnapshot, in.Seq); !ok {
		return nil, platform.Invalid("seq", fmt.Sprintf("step %d is not part of the case", in.Seq))
	}
	status := execution.StepStatus(in.Status)
	if !status.Valid() {
		return nil, platform.Invalid("status", "unsupported step status")
	}
	// in.VersionSUTID is the agent-owned tool DTO Go field
	// (tools.StepRecordInput / tools.AssertionRecordInput); its value is a SUT
	// id. The model-facing WIRE key is sut_id since v0.2.4-C T14 (R-C1); the Go
	// field name is internal and kept. See the lockstep note in tools.go.
	target, err := s.recordedStepTarget(ctx, scope, caseRun, in.VersionSUTID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	result := execution.StepResult{
		ID:          s.nextID(),
		CaseRunID:   caseRun.ID,
		Seq:         in.Seq,
		TargetSUTID: optionalID(target),
		Status:      status,
		Observed:    map[string]any{},
		Error:       truncateRunes(in.Error, execution.MaxStepErrorLength),
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if strings.TrimSpace(in.Observed) != "" {
		result.Observed["summary"] = truncateRunes(in.Observed, 4000)
	}
	// The executor action error carries a structured capability code when the
	// executor cannot perform the action at all (for example
	// file_chooser_unavailable). The code token inside the recorded error is
	// matched against the domain's CLOSED allowlist (ExecutorLimitationCodeFromError)
	// and attached to this step as a structured marker; nothing is inferred from
	// prose. (X-02: the in-memory remembered-code producer was the retired browser
	// bridge; with the bridge deleted in v0.2.4-C the error-text allowlist is the
	// single producer, and the dead remember/take mechanism was removed in T22.)
	if status == execution.StepFailed || status == execution.StepBlocked {
		if code, ok := execution.ExecutorLimitationCodeFromError(in.Error); ok {
			result.Observed[execution.ExecutorLimitationKey] = map[string]any{
				execution.ExecutorLimitationCodeKey: code,
			}
		}
	}
	if status == execution.StepRunning {
		result.StartedAt = &now
	}
	if status.Terminal() {
		result.FinishedAt = &now
	}
	ok, err := s.results.UpsertStep(ctx, result, caseRun.DispatchEpoch, caseRun.DispatchToken)
	if err != nil {
		return nil, mapExecutionError(err, "step_result")
	}
	if !ok {
		return nil, platform.LeaseLost()
	}
	s.renewLeaseQuietly(ctx, caseRun)
	s.publishCaseRunEvent(EventStepFinished, caseRun, map[string]any{
		"seq":    in.Seq,
		"status": in.Status,
	})
	return map[string]any{
		"case_run_id": platformid.Format(caseRun.ID),
		"seq":         in.Seq,
		"status":      in.Status,
	}, nil
}

// AssertionRecord upserts one assertion result. expected/kind/target are always
// overwritten from the P2 case snapshot; the model's expected is ignored.
func (s *CaseRunner) AssertionRecord(ctx context.Context, scope tools.Scope, in tools.AssertionRecordInput) (any, error) {
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return nil, err
	}
	caseSnapshot, err := s.caseSnapshot(ctx, scope, caseRun)
	if err != nil {
		return nil, err
	}
	assertion, ok := findAssertion(caseSnapshot, in.StepSeq, in.AssertionSeq)
	if !ok {
		return nil, platform.Invalid("assertion_seq", fmt.Sprintf("assertion %d.%d is not part of the case", in.StepSeq, in.AssertionSeq))
	}
	verdict := execution.AssertionVerdict(in.Verdict)
	if !verdict.Valid() {
		return nil, platform.Invalid("verdict", "unsupported assertion verdict")
	}
	target := assertion.SUTID
	if in.VersionSUTID != nil {
		if assertion.SUTID > 0 && *in.VersionSUTID != assertion.SUTID {
			return nil, platform.Invalid("sut_id", "does not match the case assertion target")
		}
		target = *in.VersionSUTID
	}
	if target <= 0 {
		// v0.2.4-B: only a db assertion still carries a design SUT id, so a
		// ui/api/text assertion resolves its recorded target the same way a
		// step does.
		resolved, resolveErr := s.recordedStepTarget(ctx, scope, caseRun, nil)
		if resolveErr != nil {
			return nil, resolveErr
		}
		target = resolved
	}
	now := s.now()
	result := execution.AssertionResult{
		ID:           s.nextID(),
		CaseRunID:    caseRun.ID,
		StepSeq:      in.StepSeq,
		AssertionSeq: in.AssertionSeq,
		Kind:         assertion.Kind,
		Verdict:      verdict,
		Reason:       truncateRunes(in.Reason, execution.MaxAssertionReasonLength),
		Expected:     truncateRunes(assertion.Expected, execution.MaxExpectedLength),
		Actual:       truncateRunes(in.Actual, execution.MaxActualLength),
		TargetSUTID:  optionalID(target),
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	ok, err = s.results.UpsertAssertion(ctx, result, caseRun.DispatchEpoch, caseRun.DispatchToken)
	if err != nil {
		return nil, mapExecutionError(err, "assertion_result")
	}
	if !ok {
		return nil, platform.LeaseLost()
	}
	s.renewLeaseQuietly(ctx, caseRun)
	s.publishCaseRunEvent(EventAssertionFinished, caseRun, map[string]any{
		"step_seq":      in.StepSeq,
		"assertion_seq": in.AssertionSeq,
		"verdict":       in.Verdict,
	})
	return map[string]any{
		"case_run_id":   platformid.Format(caseRun.ID),
		"step_seq":      in.StepSeq,
		"assertion_seq": in.AssertionSeq,
		"verdict":       in.Verdict,
		"expected":      result.Expected,
	}, nil
}

// Finish aggregates the deterministic four-state verdict, persists the terminal
// case_run and closes its browser contexts (best-effort).
func (s *CaseRunner) Finish(ctx context.Context, scope tools.Scope, candidate execution.FinishCandidate) (execution.CaseRun, error) {
	if !candidate.Valid() {
		return execution.CaseRun{}, platform.Invalid("verdict", "finish candidate must be one of passed, failed, blocked, inconclusive")
	}
	caseRun, err := s.requireRunningScope(ctx, scope)
	if err != nil {
		return execution.CaseRun{}, err
	}
	caseSnapshot, err := s.caseSnapshot(ctx, scope, caseRun)
	if err != nil {
		return execution.CaseRun{}, err
	}
	steps, err := s.results.ListSteps(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRun{}, mapExecutionError(err, "step_result")
	}
	assertions, err := s.results.ListAssertions(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRun{}, mapExecutionError(err, "assertion_result")
	}
	status, class, summary, err := s.aggregateRunVerdict(ctx, caseRun, caseSnapshot, steps, assertions, candidate)
	if err != nil {
		return execution.CaseRun{}, err
	}
	// Terminal-state discovery capture (R-EX-1): every active session is read
	// before it is closed, at all four terminal states, and stored as DB-29
	// observations plus verbatim network evidence (GD-01). Best effort only.
	s.captureTerminalObservations(ctx, caseRun, status)
	// P3 §6.4 automatic evidence: a failed/blocked/inconclusive run with a live
	// session captures one screenshot before the terminal write so the failure
	// is triageable instead of a zero-evidence row (QA-F1).
	s.captureTerminalEvidence(ctx, caseRun, status)
	// Recording ledger → evidence, at every terminal state.
	s.settleSessionEvidence(ctx, caseRun)
	// The captures/drain above may have dropped evidence: re-aggregate so the
	// art.4.5 degradation sees the final fact instead of a stale count.
	status, class, summary, err = s.aggregateRunVerdict(ctx, caseRun, caseSnapshot, steps, assertions, candidate)
	if err != nil {
		return execution.CaseRun{}, err
	}
	if caseRun.Summary.HumanRequestID > 0 {
		summary.HumanRequestID = caseRun.Summary.HumanRequestID
	}
	// Every terminal non-clean run explains itself: an explicit settlement cause
	// wins, a legacy/other terminal outcome derives one from the persisted
	// verdict/class (QA Q2). The deterministic verdict itself is untouched.
	summary.TerminalCause, summary.NextAction = execution.TerminalExplanation(status, class, summary)
	finished, terminalWrote, err := s.writeTerminal(ctx, caseRun, status, class, summary)
	if err != nil {
		return execution.CaseRun{}, err
	}
	s.stopLeaseHeartbeat(caseRun.ID, caseRun.DispatchEpoch)
	s.closeSUTContexts(ctx, caseRun.ID)
	s.publishCaseRunEvent(EventCaseRunFinished, finished, map[string]any{
		"status":           string(status),
		"failure_class":    string(class),
		"verdict_reason":   summary.VerdictReason,
		"terminal_cause":   summary.TerminalCause,
		"next_action":      summary.NextAction,
		"assertion_total":  summary.AssertionTotal,
		"assertion_failed": summary.AssertionFailed,
		"step_total":       summary.StepTotal,
		"step_failed":      summary.StepFailed,
		"evidence_count":   summary.EvidenceCount,
	})
	s.forgetWaitTracking(caseRun.ID)
	// Knowledge sedimentation/invalidation is supplemental: a hook failure never
	// changes the persisted verdict (spec §10.1, best effort). Only the owner
	// that won the terminal CAS notifies; an idempotent Finish that read another
	// owner's row must not notify a second time.
	if terminalWrote {
		s.notifyCaseSuccess(ctx, scope.VersionID, finished, caseSnapshot, finished.Summary)
		s.notifyCaseFailure(ctx, scope.VersionID, finished, caseSnapshot, finished.FailureClass)
		s.postTerminalHandoff(ctx, caseRun, finished)
	}
	return finished, nil
}

// ReplayCaseRun is the command-log replay entry (v0.2.4-C EV-006). It re-executes
// a terminal source run's recorded command log under the same environment
// fingerprint + workspace snapshot and re-validates the assertions SERVER-side
// (no model run, no model self-report). Replay is a verification, not a retest:
// it never replaces the authoritative source verdict. The body lives in
// replay.go; the deterministic settlement aggregation (execution.Aggregate) is
// reused and the four-state terminal write entry (writeTerminal) is untouched.
func (s *CaseRunner) ReplayCaseRun(ctx context.Context, versionID, sourceCaseRunID, actorID int64) (ReplayResult, error) {
	if versionID <= 0 || sourceCaseRunID <= 0 {
		return ReplayResult{}, platform.Invalid("case_run_id", "replay requires a version and a source case_run id")
	}
	return s.runReplay(ctx, versionID, sourceCaseRunID, actorID)
}

// Finalize is called by the worker run sink after the agent loop ends.
// dispatchEpoch is the case_run dispatch epoch carried by the work item: a late
// Finalize from an older epoch has lost the run to a newer dispatch and must not
// finalize it nor stop the new owner's heartbeat. result carries the run's turns,
// usage and settlement cause so a budget-ended run is explained instead of being
// aggregated as a silent inconclusive(incomplete) (QA Q2). A retryable run error
// before the attempt budget leaves the case_run running for a redispatch;
// otherwise the deterministic aggregation writes the terminal state. A
// lease-lost or paused run is left to its new owner.
func (s *CaseRunner) Finalize(ctx context.Context, caseRunID, dispatchEpoch int64, result agentapp.RunResult, runErr error, attempt, maxAttempts int) error {
	if s.caseRuns == nil {
		return errors.New("case runner is not configured")
	}
	caseRun, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		return mapExecutionError(err, "case_run")
	}
	// Fence before touching the heartbeat: a newer dispatch owns the run and its
	// renewal timer.
	if dispatchEpoch > 0 && caseRun.DispatchEpoch != dispatchEpoch {
		return nil
	}
	if IsRunLeaseLost(runErr) {
		// The dispatch fence is gone; the new owner owns the heartbeat.
		return nil
	}
	// The agent loop is over and this dispatch still owns the case_run: no
	// further renewal is needed.
	s.stopLeaseHeartbeat(caseRunID, caseRun.DispatchEpoch)
	if caseRun.Status.Finished() {
		// The verdict was already written while the agent run was still active
		// (execution.case_finish). The run result now carries the observed usage;
		// attach it to the terminal summary without touching the verdict (QA Q2).
		if caseRun.Status.Terminal() {
			s.attachRunUsage(ctx, caseRun.ID, result)
		}
		return nil
	}
	if caseRun.Status == execution.CaseRunPaused {
		return nil
	}
	usage := execution.NewCaseRunUsage(result.Turns, result.Usage)
	if runErr != nil {
		classification := classifyFinalRunError(ctx, runErr)
		if classification.Retryable && maxAttempts > 0 && attempt < maxAttempts {
			// Release the lease immediately under the dispatch CAS fence: the
			// scheduler redispatch runs in seconds instead of waiting for the
			// 90s lease to expire (v0.2.3 X-01). The row is non-terminal, so it
			// carries no terminal failure class/verdict reason.
			now := s.now()
			_, err := s.caseRuns.UpdateCAS(ctx, caseRun.ID, caseRun.DispatchEpoch, caseRun.DispatchToken, func(run *execution.CaseRun) error {
				// A human pause moves running->paused without bumping the epoch or
				// token, so the fence alone cannot reject this retry. Never reset a
				// row the human has taken over (X-01 follow-up).
				if run.Status != execution.CaseRunRunning {
					return execution.ErrStateConflict
				}
				run.Status = execution.CaseRunPending
				run.FailureClass = ""
				run.Summary.VerdictReason = ""
				run.Summary.LastError = truncateRunes(runErr.Error(), 2000)
				run.Summary.RetryReason = classification.Cause
				run.DispatchEpoch++
				run.DispatchToken = ""
				run.LeaseUntil = nil
				run.OwnerExecutorID = nil
				run.StartedAt = nil
				run.UpdatedAt = now
				return nil
			})
			if errors.Is(err, execution.ErrStateConflict) {
				// The run left running between the read and the CAS (paused by a
				// human or taken over): it is not ours to retry.
				return nil
			}
			if err != nil {
				return mapExecutionError(err, "case_run")
			}
			// A lost fence means a newer dispatch owns the row and finalizes it.
			return nil
		}
		if classification.Exhausted {
			// P6 (design §4.5): the dispatch attempt budget is spent and the run
			// exhausted its budget. This is never a failed verdict — settle
			// inconclusive with the concrete budget cause and a rerun next action,
			// exactly like a settlement-ended run that never called case_finish.
			outcome := newIncompleteRunOutcome(result)
			if err := s.finalizeWithCandidate(ctx, caseRun, execution.FinishCandidate{Verdict: execution.CaseRunInconclusive}, runErr, &outcome, usage, ""); err != nil {
				return err
			}
			return nil
		}
		candidate := execution.FinishCandidate{
			Verdict:      execution.CaseRunFailed,
			FailureClass: classification.Class,
		}
		if classification.Class == execution.FailureClassBlocked {
			candidate.Verdict = execution.CaseRunBlocked
		}
		// A run that dies before recording any step/assertion result must not
		// look like a silent zero-row failure: persist the concrete error as
		// text evidence so the reason is visible in the report/UI.
		s.recordRunFailureEvidence(ctx, caseRun, runErr)
		if err := s.finalizeWithCandidate(ctx, caseRun, candidate, runErr, nil, usage, classification.Cause); err != nil {
			return err
		}
		return nil
	}
	// The agent loop succeeded but never finished the case (for example the
	// budget was exhausted): aggregate whatever was recorded and explain the
	// stop instead of writing a silent inconclusive(incomplete).
	outcome := newIncompleteRunOutcome(result)
	if err := s.finalizeWithCandidate(ctx, caseRun, execution.FinishCandidate{Verdict: execution.CaseRunInconclusive}, nil, &outcome, usage, ""); err != nil {
		return err
	}
	return nil
}

// incompleteRunOutcome is the typed explanation of an agent run that ended
// without execution.case_finish. The deterministic verdict stays inconclusive;
// these fields make the stop explainable and hand it to a human (rerun).
type incompleteRunOutcome struct {
	Cause      string
	Detail     string
	NextAction string
}

// newIncompleteRunOutcome derives the cause from the finished run result. A run
// whose settlement was caused by a budget reports that budget; a run that
// simply stopped without the completion tool reports agent_stopped. The cause is
// never inferred from the model's prose.
func newIncompleteRunOutcome(result agentapp.RunResult) incompleteRunOutcome {
	cause := strings.TrimSpace(result.SettlementCause)
	if cause == "" {
		return incompleteRunOutcome{
			Cause:      execution.TerminalCauseAgentStopped,
			Detail:     fmt.Sprintf("the agent run ended without calling execution.case_finish after %d turns", result.Turns),
			NextAction: execution.NextActionRerun,
		}
	}
	cause = incompleteCauseCode(cause)
	detail := strings.TrimSpace(result.SettlementDetail)
	if detail == "" {
		detail = incompleteCauseFallbackDetail(cause, result)
	}
	return incompleteRunOutcome{Cause: cause, Detail: detail, NextAction: execution.NextActionRerun}
}

// incompleteCauseCode maps the runner's stable budget cause onto the case_run
// summary vocabulary. Unknown (future) causes are preserved verbatim.
func incompleteCauseCode(cause string) string {
	switch cause {
	case agentapp.BudgetCauseTokens:
		return execution.TerminalCauseTokenBudget
	case agentapp.BudgetCauseTime:
		return execution.TerminalCauseTimeBudget
	case agentapp.BudgetCauseTurns:
		return execution.TerminalCauseTurnBudget
	default:
		return cause
	}
}

func incompleteCauseFallbackDetail(cause string, result agentapp.RunResult) string {
	switch cause {
	case execution.TerminalCauseTokenBudget:
		return fmt.Sprintf("token budget: the run ended in its settlement turn after %d turns (%d tokens)",
			result.Turns, runResultTokens(result.Usage))
	case execution.TerminalCauseTimeBudget:
		return fmt.Sprintf("time budget: the run ended in its settlement turn after %d turns", result.Turns)
	case execution.TerminalCauseTurnBudget:
		return fmt.Sprintf("turn budget: the run reached its turn fuse after %d turns", result.Turns)
	default:
		return fmt.Sprintf("the agent run ended before completing the case after %d turns", result.Turns)
	}
}

// runResultTokens is the token count shown in the incomplete explanation. The
// provider's total wins; older providers report prompt+completion components.
func runResultTokens(usage map[string]int) int {
	if total := usage["total_tokens"]; total > 0 {
		return total
	}
	total := 0
	for _, key := range []string{"prompt_tokens", "completion_tokens"} {
		if value := usage[key]; value > 0 {
			total += value
		}
	}
	return total
}

func (s *CaseRunner) finalizeWithCandidate(ctx context.Context, caseRun execution.CaseRun, candidate execution.FinishCandidate, failure error, incomplete *incompleteRunOutcome, usage *execution.CaseRunUsage, terminalCause string) error {
	caseSnapshot, err := s.caseForRun(ctx, caseRun)
	if err != nil {
		return err
	}
	steps, err := s.results.ListSteps(ctx, caseRun.ID)
	if err != nil {
		return mapExecutionError(err, "step_result")
	}
	assertions, err := s.results.ListAssertions(ctx, caseRun.ID)
	if err != nil {
		return mapExecutionError(err, "assertion_result")
	}
	status, class, summary, err := s.aggregateRunVerdict(ctx, caseRun, caseSnapshot, steps, assertions, candidate)
	if err != nil {
		return err
	}
	// Terminal-state discovery capture (R-EX-1) on the agent-loop finalization
	// path too: read every active session before it is closed.
	s.captureTerminalObservations(ctx, caseRun, status)
	s.captureTerminalEvidence(ctx, caseRun, status)
	// Recording ledger → evidence, then re-aggregate so a drop produced by the
	// captures/drain degrades the verdict (constitution art.4.5).
	s.settleSessionEvidence(ctx, caseRun)
	status, class, summary, err = s.aggregateRunVerdict(ctx, caseRun, caseSnapshot, steps, assertions, candidate)
	if err != nil {
		return err
	}
	if failure != nil {
		// Persist the concrete failure reason (delivery reports and attribution
		// read summary.last_error): a failed/blocked run must explain itself.
		summary.LastError = truncateRunes(failure.Error(), 2000)
	}
	// An incomplete verdict explains why the run stopped and offers the human
	// next step. The verdict itself is untouched: a 1/4-step run stays
	// inconclusive, never a pass.
	explained := incomplete != nil && status == execution.CaseRunInconclusive &&
		summary.VerdictReason == execution.VerdictReasonIncomplete
	if explained {
		summary.TerminalCause = incomplete.Cause
		summary.NextAction = incomplete.NextAction
		summary.LastError = truncateRunes(fmt.Sprintf("%s; the case recorded %d of %d steps before the run ended",
			incomplete.Detail, len(steps), len(caseSnapshot.Steps)), 2000)
	}
	// All other terminal outcomes (blocked, failed, non-budget inconclusive) also
	// carry a stable cause and next action derived from the persisted terminal
	// data, so no terminal row is left unexplained (QA Q2).
	summary.TerminalCause, summary.NextAction = execution.TerminalExplanation(status, class, summary)
	// The executor classification (executor_timeout / executor_context_create_failed
	// / executor_lost) is a more specific stable cause than the generic
	// failure-class derivation and wins for a terminal row (v0.2.3 X-01/X-07).
	if terminalCause != "" && status != execution.CaseRunPassed && status != execution.CaseRunSkipped {
		summary.TerminalCause = terminalCause
	}
	if usage != nil {
		summary.Usage = usage
	}
	finished, terminalWrote, err := s.writeTerminal(ctx, caseRun, status, class, summary)
	if err != nil {
		return err
	}
	s.closeSUTContexts(ctx, caseRun.ID)
	s.publishCaseRunEvent(EventCaseRunFinished, finished, map[string]any{
		"status":         string(status),
		"failure_class":  string(class),
		"verdict_reason": summary.VerdictReason,
		"terminal_cause": summary.TerminalCause,
		"next_action":    summary.NextAction,
	})
	s.forgetWaitTracking(caseRun.ID)
	// A failed finalization (agent-run failure or exhausted redispatch) notifies
	// the optional knowledge failure hook exactly once, from the terminal owner.
	if terminalWrote {
		s.notifyCaseFailure(ctx, s.versionForCaseRun(caseRun), finished, caseSnapshot, finished.FailureClass)
		s.postTerminalHandoff(ctx, caseRun, finished)
	}
	return nil
}

// postTerminalHandoff records the human handoff of a terminal run whose cause
// has no automatic recovery path in the execution team room. A terminal case run
// cannot create a human_request (the run is already terminal and the P3 human
// flow is limited to login takeover), so the durable handoff is a typed
// team-room post carrying the cause, the explanation and the suggested next
// action. Best effort: the verdict and its summary fields never depend on it.
func (s *CaseRunner) postTerminalHandoff(ctx context.Context, caseRun execution.CaseRun, finished execution.CaseRun) {
	if s.teamRoom == nil {
		return
	}
	if !execution.TerminalCauseNeedsHuman(finished.Summary.TerminalCause) {
		return
	}
	content := fmt.Sprintf("case run %s needs a human (%s); %s; next action: %s",
		platformid.Format(caseRun.ID),
		finished.Summary.TerminalCause,
		finished.Summary.LastError,
		finished.Summary.NextAction)
	_ = s.teamRoom.Post(ctx, TeamPost{
		VersionID: s.versionForCaseRun(caseRun),
		Team:      "execution",
		Kind:      "handoff",
		Content:   truncateRunes(content, 2000),
		Refs:      []TeamPostRef{{Kind: "case_run", ID: platformid.Format(caseRun.ID)}},
	})
}

// attachRunUsage persists the observed usage of a run that ended after its
// terminal verdict was already written (the execution.case_finish path). It is a
// best-effort enrichment: the status, verdict reason and explanation are
// untouched, and a row that already carries usage is never overwritten.
func (s *CaseRunner) attachRunUsage(ctx context.Context, caseRunID int64, result agentapp.RunResult) {
	usage := execution.NewCaseRunUsage(result.Turns, result.Usage)
	if usage == nil {
		return
	}
	current, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil || !current.Status.Terminal() || current.Summary.Usage != nil {
		return
	}
	current.Summary.Usage = usage
	current.UpdatedAt = s.now()
	_, _ = s.caseRuns.Update(ctx, current)
}

// RenewLease extends the case_run lease during a long agent run.
func (s *CaseRunner) RenewLease(ctx context.Context, caseRunID, epoch int64, token string) error {
	caseRun, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		return mapExecutionError(err, "case_run")
	}
	if caseRun.DispatchEpoch != epoch || caseRun.DispatchToken != token {
		return platform.LeaseLost()
	}
	ok, err := s.caseRuns.ClaimLease(ctx, caseRunID, epoch, s.now().Add(s.leaseDuration()))
	if err != nil {
		return mapExecutionError(err, "case_run")
	}
	if !ok {
		return platform.LeaseLost()
	}
	return nil
}

// startLeaseHeartbeat runs the independent lease renewal timer (spec §5.7:
// heartbeat renews independently; lease > executor single-action timeout). It
// stops when ctx is canceled (the worker cancels its run context), when
// Finish/Finalize stops this exact run+epoch, or when the dispatch fence is
// definitively lost.
func (s *CaseRunner) startLeaseHeartbeat(ctx context.Context, caseRun execution.CaseRun) {
	if s.caseRuns == nil || s.heartbeat <= 0 {
		return
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	heartbeat := &leaseHeartbeat{epoch: caseRun.DispatchEpoch, cancel: cancel}
	s.mu.Lock()
	if previous := s.heartbeats[caseRun.ID]; previous != nil {
		previous.cancel()
	}
	s.heartbeats[caseRun.ID] = heartbeat
	s.mu.Unlock()

	go func() {
		ticker := time.NewTicker(s.heartbeat)
		defer ticker.Stop()
		defer s.forgetLeaseHeartbeat(caseRun.ID, heartbeat)
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if !s.renewLease(heartbeatCtx, caseRun) {
					return
				}
				// The executor session leases ride the same heartbeat.
				s.renewSessionLeases(heartbeatCtx, caseRun)
			}
		}
	}()
}

// renewLease renews the lease once. Transient store errors are retried with a
// short backoff while ctx is alive: one Postgres blip must not disable renewal
// for the rest of a long action and turn it into executor_lost. Only a
// definitive fence loss (!ok) stops the timer; the recovery sweep then owns the
// redispatch.
func (s *CaseRunner) renewLease(ctx context.Context, caseRun execution.CaseRun) bool {
	for {
		ok, err := s.caseRuns.ClaimLease(ctx, caseRun.ID, caseRun.DispatchEpoch,
			s.now().Add(s.leaseDuration()))
		if err == nil {
			return ok
		}
		if ctx.Err() != nil {
			return false
		}
		timer := time.NewTimer(heartbeatRetryDelay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

// renewSessionLeases keeps the executor session leases of one run alive on the
// same cadence as the case_run lease. A command may run far longer than the
// session TTL, so without this the executor's orphan sweeper would reclaim a
// session that is still working. Best effort: a failure is logged and retried on
// the next tick; a genuinely lost lease surfaces as a typed 409 on the next write.
func (s *CaseRunner) renewSessionLeases(ctx context.Context, caseRun execution.CaseRun) {
	if s.execution == nil || s.sutContexts == nil {
		return
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return
	}
	for _, row := range rows {
		if row.SessionID == "" || row.Status != execution.SUTContextActive {
			continue
		}
		executorID := int64(0)
		if row.ExecutorID != nil {
			executorID = *row.ExecutorID
		}
		if renewErr := s.execution.RenewLease(ctx, executorID, row.SessionID, caseRun.DispatchEpoch); renewErr != nil {
			slog.WarnContext(ctx, "executor session lease renewal failed",
				"case_run_id", caseRun.ID, "session_id", row.SessionID,
				"lease_epoch", caseRun.DispatchEpoch, "error", renewErr)
		}
	}
}

// stopLeaseHeartbeat cancels and forgets the renewal timer of one dispatch. The
// epoch is part of the identity so a stale owner cannot stop the heartbeat of
// the dispatch that superseded it.
func (s *CaseRunner) stopLeaseHeartbeat(caseRunID, dispatchEpoch int64) {
	s.mu.Lock()
	heartbeat := s.heartbeats[caseRunID]
	if heartbeat == nil || heartbeat.epoch != dispatchEpoch {
		s.mu.Unlock()
		return
	}
	delete(s.heartbeats, caseRunID)
	s.mu.Unlock()
	heartbeat.cancel()
}

// forgetLeaseHeartbeat drops the timer entry when its goroutine exits, unless a
// newer dispatch already replaced it.
func (s *CaseRunner) forgetLeaseHeartbeat(caseRunID int64, heartbeat *leaseHeartbeat) {
	s.mu.Lock()
	if s.heartbeats[caseRunID] != heartbeat {
		s.mu.Unlock()
		return
	}
	delete(s.heartbeats, caseRunID)
	s.mu.Unlock()
	heartbeat.cancel()
}

// --- helpers ---------------------------------------------------------------

func validateExecutionPayload(payload agentapp.WorkItemPayload) error {
	if payload.IntentID <= 0 || payload.WaveID <= 0 || payload.CaseRunID <= 0 || payload.CaseVersionID <= 0 {
		return platform.Invalid("payload", "execute.case payload requires intent_id, wave_id, case_run_id and case_version_id")
	}
	if payload.DispatchEpoch <= 0 || strings.TrimSpace(payload.DispatchToken) == "" {
		return platform.Invalid("payload", "execute.case payload requires dispatch_epoch and dispatch_token")
	}
	return nil
}

func (s *CaseRunner) requireRunningScope(ctx context.Context, scope tools.Scope) (execution.CaseRun, error) {
	if scope.CaseRunID <= 0 || scope.DispatchEpoch <= 0 || strings.TrimSpace(scope.DispatchToken) == "" {
		return execution.CaseRun{}, platform.Invalid("scope", "execution tool calls require the injected case_run dispatch scope")
	}
	caseRun, err := s.caseRuns.Get(ctx, scope.CaseRunID)
	if err != nil {
		return execution.CaseRun{}, mapExecutionError(err, "case_run")
	}
	if caseRun.DispatchEpoch != scope.DispatchEpoch || caseRun.DispatchToken != scope.DispatchToken {
		return execution.CaseRun{}, platform.LeaseLost()
	}
	if caseRun.IntentID != scope.IntentID || caseRun.WaveID != scope.WaveID {
		return execution.CaseRun{}, platform.LeaseLost()
	}
	if caseRun.Status != execution.CaseRunRunning {
		return execution.CaseRun{}, platform.StateConflict("case_run is not running (status " + string(caseRun.Status) + ")")
	}
	return caseRun, nil
}

// IsRunLeaseLost reports whether an agent run error means the case_run dispatch
// fence was taken over.
func IsRunLeaseLost(err error) bool {
	if err == nil {
		return false
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) && apiErr.Code == "lease_lost" {
		return true
	}
	var runErr *agentapp.RunError
	return errors.As(err, &runErr) && runErr.Reason == "lease_lost"
}

// finalRunFailure is the typed classification of an agent run failure: the P3
// failure class, whether the dispatch can be retried, and the stable terminal
// cause persisted for the UI/analysis (v0.2.3 X-01/X-07). Cause is empty when
// no more specific cause than the failure class exists.
type finalRunFailure struct {
	Class     execution.FailureClass
	Retryable bool
	Cause     string
	// Exhausted marks a P6 budget exhaustion (design §4.5): it is re-dispatched
	// within the attempt budget and, once that budget is spent, settles
	// inconclusive (rerun) — NEVER failed. Budget starvation is a GOV defect, not
	// a model-incapable failure verdict (constitution art.3).
	Exhausted bool
}

// executorSessionsPath is the executor endpoint that lazily creates the session
// (the first execution call). A failure there is the pre-first-action failure the
// UAT evidence pointed at. The published terminal cause code keeps its
// pre-v2 spelling (executor_context_create_failed) for contract stability.
const executorSessionsPath = "/v1/sessions"

// classifyFinalRunError maps an agent run failure to the P3 failure taxonomy,
// its retry eligibility and a stable terminal cause. Only a refused dial (the
// executor cannot be reached), an offline executor row or an expired heartbeat
// is executor_lost; a request timeout/cancellation is a timeout and a reachable
// executor returning 5xx/protocol failures is an environment failure (v0.2.3
// X-01).
func classifyFinalRunError(ctx context.Context, err error) finalRunFailure {
	// A typed run error is the authoritative cause. The caller context may have
	// expired for a run-level reason (budget, model failure), so a
	// DeadlineExceeded alone must not relabel it as an executor timeout
	// (X-01 follow-up).
	var runErr *agentapp.RunError
	if errors.As(err, &runErr) {
		switch runErr.Reason {
		case "case_blocked":
			// The execution task provider rejected the case scope before the
			// agent loop (unresolvable step target, stale case, missing binding):
			// this is a blocked case, not an automation failure.
			return finalRunFailure{Class: execution.FailureClassBlocked, Cause: execution.VerdictReasonBlocked}
		case "budget":
			// P6 (design §4.5): a turn/time/stall budget exhaustion is re-dispatched
			// within the attempt budget and then settles inconclusive, never failed.
			// The retry/terminal decision is server-side here (constitution art.3),
			// independent of the agent loop's own retryable flag.
			return finalRunFailure{Class: execution.FailureClassModel, Retryable: true, Exhausted: true}
		case "model_error", "ai_unavailable", "invalid_payload", "profile_not_found":
			return finalRunFailure{Class: execution.FailureClassModel, Retryable: runErr.Retryable}
		case "tool_failure":
			var apiErr *platform.Error
			if errors.As(err, &apiErr) {
				if class, cause, ok := classifyExecutorError(apiErr); ok {
					return finalRunFailure{Class: class, Retryable: runErr.Retryable, Cause: cause}
				}
			}
			return finalRunFailure{Class: execution.FailureClassAutomation, Retryable: runErr.Retryable}
		case "interrupted", "aborted":
			return finalRunFailure{Class: execution.FailureClassTimeout, Cause: execution.TerminalCauseExecutorTimeout}
		default:
			return finalRunFailure{Class: execution.FailureClassUnknown, Retryable: runErr.Retryable}
		}
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return finalRunFailure{Class: execution.FailureClassTimeout, Cause: execution.TerminalCauseExecutorTimeout}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return finalRunFailure{Class: execution.FailureClassTimeout, Cause: execution.TerminalCauseExecutorTimeout}
	}
	return finalRunFailure{Class: execution.FailureClassUnknown, Retryable: agentapp.IsRetryable(err)}
}

// classifyExecutorError types one executor platform error. The executor client
// reports the transport phase in Details.phase; only a dial failure (connection
// refused/unreachable) keeps the executor_lost class.
func classifyExecutorError(apiErr *platform.Error) (execution.FailureClass, string, bool) {
	sessionCreate := executorErrorField(apiErr, "method") == http.MethodPost &&
		executorErrorField(apiErr, "path") == executorSessionsPath
	switch apiErr.Code {
	case "executor_timeout":
		cause := execution.TerminalCauseExecutorTimeout
		if sessionCreate {
			cause = execution.TerminalCauseExecutorContextCreateFailed
		}
		return execution.FailureClassTimeout, cause, true
	case "executor_unavailable":
		if executorErrorField(apiErr, "phase") == "dial" {
			return execution.FailureClassExecutorLost, string(execution.FailureClassExecutorLost), true
		}
		cause := execution.TerminalCauseExecutorUnavailable
		if sessionCreate {
			cause = execution.TerminalCauseExecutorContextCreateFailed
		}
		return execution.FailureClassEnvironment, cause, true
	default:
		return "", "", false
	}
}

func executorErrorField(apiErr *platform.Error, key string) string {
	if apiErr == nil || apiErr.Details == nil {
		return ""
	}
	value, _ := apiErr.Details[key].(string)
	return value
}

// writeTerminal persists the terminal verdict through the single settlement
// entry (settleTerminalVerdict). The bool reports whether this call won the
// CAS: false means another owner already finalized the run (or the fence was
// lost), so callers must not repeat one-time side effects such as the knowledge
// success hook.
func (s *CaseRunner) writeTerminal(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus, class execution.FailureClass, summary execution.CaseRunSummary) (execution.CaseRun, bool, error) {
	return settleTerminalVerdict(ctx, s.caseRuns, s.now(), caseRun, status, class, summary)
}

func (s *CaseRunner) caseSnapshot(ctx context.Context, scope tools.Scope, caseRun execution.CaseRun) (execution.CaseSnapshot, error) {
	if s.cases == nil {
		return execution.CaseSnapshot{}, errors.New("case runner has no case reader")
	}
	snapshot, err := s.cases.GetCaseVersion(ctx, scope.VersionID, caseRun.CaseVersionID)
	if err != nil {
		return execution.CaseSnapshot{}, mapCaseReadError(err)
	}
	return snapshot, nil
}

func (s *CaseRunner) caseForRun(ctx context.Context, caseRun execution.CaseRun) (execution.CaseSnapshot, error) {
	intent, err := s.intents.Get(ctx, caseRun.IntentID)
	if err != nil {
		return execution.CaseSnapshot{}, mapExecutionError(err, "execution_intent")
	}
	snapshot, err := s.cases.GetCaseVersion(ctx, intent.VersionID, caseRun.CaseVersionID)
	if err != nil {
		return execution.CaseSnapshot{}, mapCaseReadError(err)
	}
	return snapshot, nil
}

func mapCaseReadError(err error) error {
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		if apiErr.Status == 404 {
			return platform.StateConflict("the case version is not current and confirmed")
		}
		return apiErr
	}
	if errors.Is(err, execution.ErrNotFound) {
		return platform.StateConflict("the case version is not current and confirmed")
	}
	return mapExecutionError(err, "case_version")
}

// recordedStepTarget resolves the SUT a step/assertion result is recorded
// against.
//
// v0.2.4-B (T9 design contract): a case step no longer carries a target SUT id,
// so the model's explicit tool argument is authoritative and must name a bound
// SUT. When it is omitted the run falls back to the intent's SUT set if that is
// unambiguous (exactly one bound SUT), and otherwise leaves the nullable
// step_result.target_sut_id empty rather than rejecting the record — QA-F1: the
// agent must always be able to record a step, and per-SUT attribution is
// best-effort metadata, not a gate.
func (s *CaseRunner) recordedStepTarget(ctx context.Context, scope tools.Scope, caseRun execution.CaseRun, explicit *int64) (int64, error) {
	if explicit != nil {
		if _, err := s.bindingFor(ctx, scope.VersionID, *explicit); err != nil {
			return 0, err
		}
		return *explicit, nil
	}
	if s.intents != nil {
		if intent, err := s.intents.Get(ctx, caseRun.IntentID); err == nil &&
			len(intent.SUTIDs) == 1 && intent.SUTIDs[0] > 0 {
			return intent.SUTIDs[0], nil
		}
	}
	return 0, nil
}

// executionSUTBindings resolves the SUTs a case run may use and validates the
// one target id design still supplies. The resolved set is written back to the
// snapshot so the task context, the SUT context rows and the recorded results
// all carry the same sut_id.
//
// v0.2.4-B (T9 design contract): a case no longer names SUTs, pages or APIs —
// steps carry prose target_description instead — so the case cannot narrow the
// version's binding set and the per-step "targets unbound SUT" gate is gone
// with the ids it checked. What remains is:
//
//   - every effective SUT of the bound environment is in scope (the caller
//     already filtered to EffectiveSUTs, so proposed/disabled resources are out);
//   - a db assertion still names a SUT (design case_assertion.sut_id) and that
//     id must be bound, because design now validates only its existence;
//   - the single-binding fallback that stamps DefaultSUTID, so a recorded
//     step/assertion whose tool call omitted sut_id still resolves.
//     QA-F1: a run must never be rejected before the agent can record anything,
//     so an unresolvable multi-SUT target is left 0 for the model to supply
//     rather than failing the run here.
//
// account_role is deliberately not validated against the bound test accounts:
// the design contract allows any role label (for example "anonymous" for a
// logged-out step), an unbound role simply runs an unauthenticated browser
// context, and account.credentials returns a typed business error if the model
// asks for credentials that do not exist.
func executionSUTBindings(snapshot *execution.CaseSnapshot, bindings []execution.SUTBinding) ([]execution.SUTBinding, error) {
	if snapshot == nil {
		return nil, platform.StateConflict("the case snapshot is missing")
	}
	if len(bindings) == 0 {
		return nil, platform.StateConflict("the version has no active SUT bindings")
	}
	byID := make(map[int64]execution.SUTBinding, len(bindings))
	ids := make([]int64, 0, len(bindings))
	for _, binding := range bindings {
		if binding.SUTID <= 0 {
			continue
		}
		if _, ok := byID[binding.SUTID]; ok {
			continue
		}
		byID[binding.SUTID] = binding
		ids = append(ids, binding.SUTID)
	}
	if len(ids) == 0 {
		return nil, platform.StateConflict("the version has no active SUT bindings")
	}
	// A case with no explicit target is unambiguous when the version binds a
	// single SUT, so that binding is the recorded-target fallback.
	if len(ids) == 1 {
		snapshot.DefaultSUTID = ids[0]
	} else {
		snapshot.DefaultSUTID = 0
	}
	snapshot.SUTIDs = sortedSUTIDs(ids)
	for i := range snapshot.Assertions {
		assertion := &snapshot.Assertions[i]
		if assertion.SUTID <= 0 {
			assertion.SUTID = snapshot.DefaultSUTID
			continue
		}
		if _, ok := byID[assertion.SUTID]; !ok {
			return nil, platform.StateConflict(fmt.Sprintf(
				"assertion %d.%d targets unbound SUT %s", assertion.StepSeq, assertion.AssertionSeq, platformid.Format(assertion.SUTID)))
		}
	}
	resolved := make([]execution.SUTBinding, 0, len(snapshot.SUTIDs))
	for _, id := range snapshot.SUTIDs {
		resolved = append(resolved, byID[id])
	}
	return resolved, nil
}

// sortedSUTIDs deduplicates and sorts one resolved SUT id set.
func sortedSUTIDs(ids []int64) []int64 {
	out := append([]int64(nil), ids...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func (s *CaseRunner) ensureSUTContexts(ctx context.Context, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, epoch int64) error {
	existing, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return mapExecutionError(err, "case_run_sut_context")
	}
	byKey := make(map[string]execution.CaseRunSUTContext, len(existing))
	for _, row := range existing {
		byKey[sutContextKey(row.SUTID, row.AccountRole)] = row
	}
	// v0.2.4-B (T9 design contract): a step no longer names its SUT, so the
	// (SUT, role) pairs to pre-warm cannot be derived per step. Only the
	// unambiguous single-binding case is pre-warmed here, exactly as before; a
	// multi-SUT version creates each context lazily in ensureBrowserContext once
	// the model picks a SUT for a tool call, which avoids reserving executor
	// browser contexts for SUTs the run may never touch.
	prewarmSUTID := snapshot.DefaultSUTID
	prewarmSteps := snapshot.Steps
	if prewarmSUTID <= 0 {
		prewarmSteps = nil
	}
	seenRoles := make(map[string]struct{}, len(prewarmSteps))
	for _, step := range prewarmSteps {
		if _, seen := seenRoles[step.AccountRole]; seen {
			continue
		}
		seenRoles[step.AccountRole] = struct{}{}
		key := sutContextKey(prewarmSUTID, step.AccountRole)
		if row, ok := byKey[key]; ok {
			rowID := row.ID
			if _, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
				target.LeaseEpoch = epoch
				target.ExecutorID = caseRun.OwnerExecutorID
				if target.Status == execution.SUTContextClosed || target.Status == execution.SUTContextFailed {
					target.Status = execution.SUTContextCreating
					target.SessionID = ""
					target.ClosedAt = nil
				}
				return nil
			}); updateErr != nil {
				return mapExecutionError(updateErr, "case_run_sut_context")
			}
			continue
		}
		if err := s.sutContexts.Create(ctx, execution.CaseRunSUTContext{
			ID:          s.nextID(),
			CaseRunID:   caseRun.ID,
			SUTID:       prewarmSUTID,
			AccountRole: step.AccountRole,
			ExecutorID:  caseRun.OwnerExecutorID,
			LeaseEpoch:  epoch,
			Status:      execution.SUTContextCreating,
			CreatedAt:   s.now(),
			UpdatedAt:   s.now(),
		}); err != nil {
			if errors.Is(err, execution.ErrDuplicate) {
				continue
			}
			return mapExecutionError(err, "case_run_sut_context")
		}
	}
	return nil
}

func (s *CaseRunner) openHumanRequest(ctx context.Context, caseRunID int64) (*execution.HumanRequest, error) {
	if s.humans == nil {
		return nil, nil
	}
	request, err := s.humans.GetOpenByCaseRun(ctx, caseRunID)
	if err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			return nil, nil
		}
		return nil, mapExecutionError(err, "human_request")
	}
	return request, nil
}

func (s *CaseRunner) humanResponse(ctx context.Context, caseRun execution.CaseRun) (string, error) {
	if s.humans == nil || caseRun.Summary.HumanRequestID <= 0 {
		return "", nil
	}
	request, err := s.humans.Get(ctx, caseRun.Summary.HumanRequestID)
	if err != nil {
		if errors.Is(err, execution.ErrNotFound) {
			return "", nil
		}
		return "", mapExecutionError(err, "human_request")
	}
	if request.Status != execution.HumanAnswered {
		return "", nil
	}
	parts := make([]string, 0, 2)
	if code, ok := request.Response[execution.HumanResponseCode]; ok {
		if text := strings.TrimSpace(fmt.Sprint(code)); text != "" {
			parts = append(parts, "验证码/操作值: "+text)
		}
	}
	if note, ok := request.Response[execution.HumanResponseNote]; ok {
		if text := strings.TrimSpace(fmt.Sprint(note)); text != "" {
			parts = append(parts, "备注: "+text)
		}
	}
	return strings.Join(parts, "；"), nil
}

// recordRunFailureEvidence persists the concrete error of a run that never
// recorded a step or assertion result as immutable text evidence (QA-F1): a
// failed/blocked case_run must explain itself instead of showing zero rows and
// zero evidence. Best effort: the terminal verdict never depends on it.
func (s *CaseRunner) recordRunFailureEvidence(ctx context.Context, caseRun execution.CaseRun, runErr error) {
	if runErr == nil || s.evidence == nil || s.content == nil || s.results == nil {
		return
	}
	steps, err := s.results.ListSteps(ctx, caseRun.ID)
	if err != nil {
		return
	}
	assertions, err := s.results.ListAssertions(ctx, caseRun.ID)
	if err != nil {
		return
	}
	if len(steps) > 0 || len(assertions) > 0 {
		return
	}
	if count, countErr := s.evidence.CountByCaseRun(ctx, caseRun.ID); countErr != nil || count > 0 {
		return
	}
	message := fmt.Sprintf("agent run ended before recording any step/assertion result: %s",
		truncateRunes(runErr.Error(), 1800))
	_, _ = s.addEvidenceForRun(ctx, caseRun, 0, EvidenceInput{
		Kind:     execution.EvidenceText,
		Data:     []byte(message),
		Filename: runFailureEvidenceFilename,
	})
}

// captureTerminalEvidence implements the P3 §6.4 automatic capture: when a
// case_run reaches failed/blocked/inconclusive and one of its sessions is still
// active, take one screenshot through the official playwright CLI (over the
// general exec channel) and store it as evidence. The executor binds the CLI
// named session (-s) to this session and settles the PNG as a screenshot
// artifact; the recording-ledger drain turns it into evidence. The capture is
// best effort and bounded: a session that never opened a browser has nothing to
// capture (the CLI errors) and a missing/dead session never changes the verdict.
func (s *CaseRunner) captureTerminalEvidence(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus) {
	if s.execution == nil || s.evidence == nil || s.content == nil || s.sutContexts == nil {
		return
	}
	switch status {
	case execution.CaseRunFailed, execution.CaseRunBlocked, execution.CaseRunInconclusive:
	default:
		return
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		return
	}
	var target *execution.CaseRunSUTContext
	for i := range rows {
		if rows[i].Status == execution.SUTContextActive && rows[i].SessionID != "" {
			target = &rows[i]
			break
		}
	}
	if target == nil {
		return
	}
	actionCtx, cancel := context.WithTimeout(context.Background(), terminalScreenshotTimeout)
	defer cancel()
	executorID := int64(0)
	if target.ExecutorID != nil {
		executorID = *target.ExecutorID
	}
	// CLI screenshot over exec (T15 replaces the retained browser bridge for this
	// capture). The executor injects -s=<session> and settles the screenshot
	// artifact; a best-effort capture failure (no browser opened, dead session)
	// never changes the verdict.
	_, execErr := s.execution.Exec(actionCtx, executorID, target.SessionID, ExecCommand{
		Argv: []string{"playwright-cli", "screenshot"},
	}, target.LeaseEpoch)
	if execErr != nil {
		return
	}
	// Drain the recording ledger so the screenshot artifact becomes evidence. A
	// SETTLEMENT failure (the screenshot was taken and then lost) is counted like
	// any other lost evidence — storeExecutionEvidence records the drop fact on a
	// fresh context and degrades the verdict (constitution art.4.5).
	if _, _, storeErr := s.storeExecutionEvidence(actionCtx, caseRun, *target, 0); storeErr != nil {
		slog.WarnContext(actionCtx, "terminal screenshot settlement failed",
			"case_run_id", caseRun.ID, "session_id", target.SessionID, "error", storeErr)
	}
}

// settleSessionEvidence drains the recording ledger of every session of the run
// into immutable evidence BEFORE the terminal write, at all four terminal states.
// Without it a passed run would never settle its command/file/trace artifacts,
// and a background command's evidence would be lost when the session closes.
func (s *CaseRunner) settleSessionEvidence(ctx context.Context, caseRun execution.CaseRun) {
	if s.execution == nil || s.sutContexts == nil || s.evidence == nil || s.content == nil {
		return
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRun.ID)
	if err != nil {
		// The session ledger could not be read at all, so the WHOLE terminal
		// drain is abandoned — for a passed run this is its ONLY drain. Count one
		// explicit loss (the un-drained recording's size is unknown) on a FRESH
		// background context, mirroring recordSettlementFailure: the caller's
		// budget may already be expired, and a loss that cannot be recorded stays
		// invisible — exactly what art.4.5 forbids. The re-aggregation after this
		// drain reads the persisted fact and degrades the verdict to inconclusive.
		slog.ErrorContext(ctx, "terminal evidence settlement could not list the run's sessions; the un-drained recording is counted as dropped",
			"case_run_id", caseRun.ID, "error", err)
		factCtx, cancel := context.WithTimeout(context.Background(), evidenceDropFactTimeout)
		defer cancel()
		s.recordEvidenceDrops(factCtx, caseRun, 1, "evidence_settlement_failed", execution.EvidenceKind(""), nil)
		return
	}
	settleCtx, cancel := context.WithTimeout(context.Background(), sessionEvidenceSettleTimeout)
	defer cancel()
	for _, row := range rows {
		if row.SessionID == "" || row.Status == execution.SUTContextClosed {
			continue
		}
		// storeExecutionEvidence already counted the un-settled remainder as an
		// evidence drop (on a fresh context, so an expired budget cannot hide the
		// loss) — the settlement re-aggregates afterwards and degrades the verdict.
		// The error is still surfaced here: a terminal drain that silently
		// swallowed it is how a passed run lost its whole ledger.
		if _, _, err := s.storeExecutionEvidence(settleCtx, caseRun, row, 0); err != nil {
			slog.ErrorContext(ctx, "terminal evidence settlement failed",
				"case_run_id", caseRun.ID, "session_id", row.SessionID, "error", err)
		}
	}
}

// aggregateRunVerdict is the single settlement aggregation entry: it reads the
// persisted evidence-drop fact and applies the deterministic four-state verdict
// plus the structured executor-capability override. Settlement calls it again
// after the terminal captures so a drop produced by a capture is not missed
// (constitution art.4.5).
func (s *CaseRunner) aggregateRunVerdict(
	ctx context.Context,
	caseRun execution.CaseRun,
	snapshot execution.CaseSnapshot,
	steps []execution.StepResult,
	assertions []execution.AssertionResult,
	candidate execution.FinishCandidate,
) (execution.CaseRunStatus, execution.FailureClass, execution.CaseRunSummary, error) {
	dropped := caseRun.EvidenceDropped
	if s.caseRuns != nil {
		if fresh, err := s.caseRuns.Get(ctx, caseRun.ID); err == nil {
			dropped = fresh.EvidenceDropped
		}
	}
	caseRun.EvidenceDropped = dropped
	// The evidence gate reads the live count before the verdict; a read failure
	// aborts the terminal write (503, retryable). The structured executor
	// capability limitation is applied inside Aggregate (single verdict
	// authority, v0.2.3 X-02).
	status, class, summary, err := aggregateWithEvidenceFacts(ctx, s.evidence, caseRun, execution.VerdictInput{
		Case:       snapshot,
		Steps:      steps,
		Assertions: assertions,
		Finish:     &candidate,
	})
	if err != nil {
		var apiErr *platform.Error
		if errors.As(err, &apiErr) {
			return "", "", execution.CaseRunSummary{}, apiErr
		}
		return "", "", execution.CaseRunSummary{}, platform.Invalid("verdict", err.Error())
	}
	return status, class, summary, nil
}

func (s *CaseRunner) closeSUTContexts(ctx context.Context, caseRunID int64) {
	if s.sutContexts == nil {
		return
	}
	rows, err := s.sutContexts.ListByCaseRun(ctx, caseRunID)
	if err != nil {
		return
	}
	for _, row := range rows {
		executorID := int64(0)
		if row.ExecutorID != nil {
			executorID = *row.ExecutorID
		}
		if row.SessionID != "" && s.execution != nil {
			_ = s.execution.Close(ctx, executorID, row.SessionID)
		}
		if row.Status == execution.SUTContextClosed {
			continue
		}
		rowID := row.ID
		_, _ = s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
			target.Status = execution.SUTContextClosed
			target.ClosedAt = ptrTime(s.now())
			return nil
		})
	}
}

// notifyCaseSuccess invokes the optional knowledge success hook for one passed
// case_run. The hook never blocks or fails the executed case: an error is
// logged and a panic is recovered (spec §10.1 best effort).
func (s *CaseRunner) notifyCaseSuccess(ctx context.Context, versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, summary execution.CaseRunSummary) {
	if s.knowledge == nil || caseRun.Status != execution.CaseRunPassed || versionID <= 0 {
		return
	}
	resolution, registrations := s.knowledgeObservationFacts(ctx, caseRun.ID)
	fact := caseSuccessFact(versionID, caseRun, snapshot, summary, resolution, registrations)
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().ErrorContext(ctx, "knowledge success hook panicked",
				"case_run_id", caseRun.ID, "panic", recovered)
		}
	}()
	if err := s.knowledge.OnCaseRunPassed(ctx, fact); err != nil {
		slog.Default().WarnContext(ctx, "knowledge success hook failed",
			"case_run_id", caseRun.ID, "error", err)
	}
}

// notifyCaseFailure invokes the optional knowledge failure hook for one failed
// case_run. The hook never blocks or fails the executed case: an error is logged
// and a panic is recovered (spec §10.1 best effort).
func (s *CaseRunner) notifyCaseFailure(ctx context.Context, versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, class execution.FailureClass) {
	if s.knowledgeFailure == nil || caseRun.Status != execution.CaseRunFailed || versionID <= 0 {
		return
	}
	resolution, registrations := s.knowledgeObservationFacts(ctx, caseRun.ID)
	fact := caseFailureFact(versionID, caseRun, snapshot, class, resolution, registrations)
	defer func() {
		if recovered := recover(); recovered != nil {
			slog.Default().ErrorContext(ctx, "knowledge failure hook panicked",
				"case_run_id", caseRun.ID, "panic", recovered)
		}
	}()
	if err := s.knowledgeFailure.OnCaseRunFailed(ctx, fact); err != nil {
		slog.Default().WarnContext(ctx, "knowledge failure hook failed",
			"case_run_id", caseRun.ID, "error", err)
	}
}

// caseSuccessFact projects one passed case snapshot into the hook observation:
// identifiers only, deduplicated and in case order. The page/API dimension is
// the execution-fact true source (SC-F10): the run's latest sut_resolution
// validated assets plus the resolved SUTs' proven asset registrations.
func caseSuccessFact(versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, summary execution.CaseRunSummary, resolution execution.SUTResolution, registrations []execution.AssetRegistration) CaseSuccessFact {
	fact := CaseSuccessFact{
		VersionID:     versionID,
		CaseRunID:     caseRun.ID,
		CaseVersionID: caseRun.CaseVersionID,
		Title:         snapshot.Title,
		EvidenceCount: summary.EvidenceCount,
	}
	fact.AccountRoles, fact.PageIDs, fact.APIIDs = caseObservationIDs(snapshot, resolution, registrations)
	return fact
}

// caseFailureFact projects one failed case snapshot into the failure hook
// observation. It deliberately carries no title and no summary text.
func caseFailureFact(versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, class execution.FailureClass, resolution execution.SUTResolution, registrations []execution.AssetRegistration) CaseFailureFact {
	fact := CaseFailureFact{
		VersionID:     versionID,
		CaseRunID:     caseRun.ID,
		CaseVersionID: caseRun.CaseVersionID,
		FailureClass:  class,
	}
	fact.AccountRoles, fact.PageIDs, fact.APIIDs = caseObservationIDs(snapshot, resolution, registrations)
	return fact
}

// UnauthenticatedAccountRole is the conventional account-role label of a step
// that runs without credentials (a logged-out or negative case, such as the
// "not logged in" acceptance case). It is deliberately allowed by
// executionSUTBindings but is not a bound test account, so it must never seed
// knowledge bindings: the knowledge success hook would warn that the role is
// unbound (R2-OBS-02). Custom unbound roles keep their existing unmapped warning.
const UnauthenticatedAccountRole = "anonymous"

// caseObservationIDs extracts the knowledge back-references of one case run.
// The account-role dimension comes from the snapshot in case order; the
// documented unauthenticated role is skipped (it has no credentials to reuse
// and no knowledge binding dimension).
//
// v0.2.4-F T14 (SC-F10, R-E7) RESTORES the page/API dimension as a TRUE SOURCE
// (the C T22 "not restored, no clean source" decision is explicitly revisited —
// release notes §8.3/§19): the P16 sut_resolution fact records the resolved
// SUT's validated page/API asset ids server-side at Prepare (controller ruling
// P20 — non-empty even for a directory-hit run that never explores), and the
// P17 asset_registration facts record the proven discoveries of the resolved
// SUTs. The dimension is the deduplicated union of both. Without facts it stays
// legitimately EMPTY: a prose step target is never turned into an id (C T22
// ruling — heuristic id fabrication is forgery).
// TestKnowledgePageAPIDimensionDecision pins the positive projection;
// TestKnowledgeDimensionNoFabricatedID pins the empty-without-facts guard.
func caseObservationIDs(snapshot execution.CaseSnapshot, resolution execution.SUTResolution, registrations []execution.AssetRegistration) (roles []string, pages []int64, apis []int64) {
	seenRoles := map[string]struct{}{}
	for _, step := range snapshot.Steps {
		role := strings.TrimSpace(step.AccountRole)
		if role == "" || strings.EqualFold(role, UnauthenticatedAccountRole) {
			continue
		}
		if _, seen := seenRoles[role]; seen {
			continue
		}
		seenRoles[role] = struct{}{}
		roles = append(roles, role)
	}
	pageIDs := append([]int64(nil), resolution.TouchedPageIDs...)
	apiIDs := append([]int64(nil), resolution.TouchedAPIIDs...)
	for _, registration := range LatestApprovedAssetRegistrations(registrations) {
		switch registration.AssetKind {
		case execution.AssetKindPage:
			pageIDs = append(pageIDs, registration.AssetID)
		case execution.AssetKindAPI:
			apiIDs = append(apiIDs, registration.AssetID)
		}
	}
	return roles, sortedUniqueInt64(pageIDs), sortedUniqueInt64(apiIDs)
}

// knowledgeObservationFacts reads the server-recorded facts that carry the
// knowledge page/API dimension (SC-F10): the run's LATEST sut_resolution fact
// (append order, id ascending — latest wins) and the asset_registration facts
// of the resolved SUTs. A read failure degrades the dimension with a loud
// warning: the knowledge hooks are best effort and must never fail or block
// settlement, and the dimension is never fabricated to cover a read gap
// (constitution art.7.6 — the degradation is observable, not silent).
func (s *CaseRunner) knowledgeObservationFacts(ctx context.Context, caseRunID int64) (execution.SUTResolution, []execution.AssetRegistration) {
	if s.resolutions == nil {
		return execution.SUTResolution{}, nil
	}
	resolutions, err := s.resolutions.ListSUTResolutions(ctx, caseRunID)
	if err != nil {
		slog.WarnContext(ctx, "knowledge dimension: sut_resolution read failed; page/API dimension degraded to empty",
			"case_run_id", caseRunID, "error", err)
		return execution.SUTResolution{}, nil
	}
	if len(resolutions) == 0 {
		return execution.SUTResolution{}, nil
	}
	latest := resolutions[len(resolutions)-1]
	var registrations []execution.AssetRegistration
	if s.registrations != nil {
		for _, sutID := range latest.SUTIDs {
			rows, err := s.registrations.ListAssetRegistrations(ctx, sutID)
			if err != nil {
				slog.WarnContext(ctx, "knowledge dimension: asset_registration read failed; this SUT's proven registrations are missing from the dimension",
					"case_run_id", caseRunID, "sut_id", sutID, "error", err)
				continue
			}
			registrations = append(registrations, rows...)
		}
	}
	return latest, registrations
}

// LatestApprovedAssetRegistrations derives the effective proven registrations
// from append-only asset_registration facts (one or more SUTs, append order):
// per (asset_kind, asset_id) the LATEST fact wins, and only a latest
// gate_result=approved fact counts — a later non-approved fact retires the
// asset. It is the SINGLE "latest wins + approved" authority shared by the
// knowledge dimension projection (caseObservationIDs) and the case_author
// discovery producer's composition-root adapter (SC-F10).
func LatestApprovedAssetRegistrations(registrations []execution.AssetRegistration) []execution.AssetRegistration {
	type assetKey struct {
		kind string
		id   int64
	}
	latest := make(map[assetKey]execution.AssetRegistration, len(registrations))
	for _, registration := range registrations {
		key := assetKey{registration.AssetKind, registration.AssetID}
		if previous, seen := latest[key]; !seen || registration.ID >= previous.ID {
			latest[key] = registration
		}
	}
	out := make([]execution.AssetRegistration, 0, len(latest))
	for _, registration := range latest {
		if registration.GateResult == AssetGateApproved {
			out = append(out, registration)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (s *CaseRunner) resetWaitTracking(caseRunID int64, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.waitTotals[caseRunID] = 0
	s.waitStarted[caseRunID] = now
}

func (s *CaseRunner) forgetWaitTracking(caseRunID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.waitTotals, caseRunID)
	delete(s.waitStarted, caseRunID)
}

func (s *CaseRunner) publishCaseRunEvent(eventType string, caseRun execution.CaseRun, payload map[string]any) {
	if s.events == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	payload["case_run_id"] = platformid.Format(caseRun.ID)
	payload["wave_id"] = platformid.Format(caseRun.WaveID)
	payload["intent_id"] = platformid.Format(caseRun.IntentID)
	publishEvent(context.Background(), s.events, eventType, s.versionForCaseRun(caseRun), map[string]any{
		"case_run_id": platformid.Format(caseRun.ID),
		"wave_id":     platformid.Format(caseRun.WaveID),
		"intent_id":   platformid.Format(caseRun.IntentID),
	}, payload, nil)
}

func (s *CaseRunner) versionForCaseRun(caseRun execution.CaseRun) int64 {
	if s.intents == nil {
		return 0
	}
	intent, err := s.intents.Get(context.Background(), caseRun.IntentID)
	if err != nil {
		return 0
	}
	return intent.VersionID
}

func (s *CaseRunner) renewLeaseQuietly(ctx context.Context, caseRun execution.CaseRun) {
	if s.caseRuns == nil {
		return
	}
	_, _ = s.caseRuns.ClaimLease(ctx, caseRun.ID, caseRun.DispatchEpoch, s.now().Add(s.leaseDuration()))
}

func (s *CaseRunner) leaseDuration() time.Duration {
	if s.leaseTTL <= 0 {
		return DefaultCaseLease
	}
	return s.leaseTTL
}

func (s *CaseRunner) nextID() int64 {
	if s.ids != nil {
		return s.ids.Next()
	}
	return time.Now().UnixNano()
}

func (s *CaseRunner) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}

func (s *CaseRunner) intSetting(key string, fallback int64) int64 {
	if s.settings == nil {
		return fallback
	}
	value, err := s.settings.Int(context.Background(), key)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func caseSessionKey(caseRunID, sutID int64, role string) string {
	return fmt.Sprintf(executorSessionKeyFormat, caseRunID, sutID, role)
}

func sutContextKey(sutID int64, role string) string {
	return platformid.Format(sutID) + "|" + role
}

func findStep(snapshot execution.CaseSnapshot, seq int) (execution.CaseStep, bool) {
	for _, step := range snapshot.Steps {
		if step.Seq == seq {
			return step, true
		}
	}
	return execution.CaseStep{}, false
}

func findAssertion(snapshot execution.CaseSnapshot, stepSeq, assertionSeq int) (execution.CaseAssertion, bool) {
	for _, assertion := range snapshot.Assertions {
		if assertion.StepSeq == stepSeq && assertion.AssertionSeq == assertionSeq {
			return assertion, true
		}
	}
	return execution.CaseAssertion{}, false
}

func ptrTime(value time.Time) *time.Time { return &value }

// truncateRunes bounds a string by rune count so multibyte text is never cut in
// the middle of a character.
func truncateRunes(value string, limit int) string {
	if limit <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit])
}
