package executionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
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
	// DefaultCaseLease is the case_run lease the agent stamps in Prepare and
	// keeps alive with the independent heartbeat. The scheduler's dispatch lease
	// must never be shorter (SchedulerDeps.LeaseTTL is clamped to it): a queued
	// work item may wait a long time before any worker claims it, and Recovery
	// must not interrupt that wait.
	DefaultCaseLease         = 90 * time.Second
	heartbeatRetryDelay      = time.Second
	defaultEvidenceMaxBytes  = int64(5 << 20)
	evidenceMaxPerCaseRun    = execution.EvidenceMaxPerCaseRun
	maxSUTFilenameLength     = 255
	executorContextKeyFormat = "run:%d:vs:%d:role:%s"
	// terminalScreenshotTimeout bounds the P3 §6.4 automatic screenshot of a
	// failed/blocked/inconclusive run. It is independent from the (possibly
	// canceled) run context: the capture is best-effort and must never extend a
	// terminal write.
	terminalScreenshotTimeout = 8 * time.Second
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

// SUTAPIResolver resolves a version_sut-bound api_id. Optional: when nil,
// http.request requires a literal path instead of api_id.
type SUTAPIResolver interface {
	API(ctx context.Context, versionID, versionSUTID, apiID int64) (SUTAPI, error)
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

// BrowserContextSpec is one lazily created executor browser context.
type BrowserContextSpec struct {
	Key          string
	AllowedHosts []string
	BaseURL      string
	// ExecutorID selects the owning executor endpoint. Zero/unknown ids fall
	// back to the primary executor endpoint for single-endpoint deployments.
	ExecutorID int64
	Metadata   map[string]any
}

// BrowserArtifact is one binary action artifact (base64 transport).
type BrowserArtifact struct {
	Kind     string
	MIME     string
	Base64   string
	SHA256   string
	Filename string
	Bytes    int
}

// BrowserActionError is the executor's typed action failure.
type BrowserActionError struct {
	Code    string
	Message string
}

// BrowserActionResult is one executor action envelope.
type BrowserActionResult struct {
	OK        bool
	Result    json.RawMessage
	Artifacts []BrowserArtifact
	Error     *BrowserActionError
}

// BrowserContextRef is one executor context (orphan reconciliation).
type BrowserContextRef struct {
	ExecutorID int64
	ID         string
	Key        string
}

// BrowserExecutor is the executor protocol port consumed by the browser tools.
// executorID selects the endpoint that owns the context (P8 multi-executor
// routing); zero falls back to the primary endpoint.
type BrowserExecutor interface {
	CreateContext(ctx context.Context, spec BrowserContextSpec) (string, error)
	Action(ctx context.Context, executorID int64, contextID, action string, args json.RawMessage) (BrowserActionResult, error)
	Cancel(ctx context.Context, executorID int64, contextID string) error
	Close(ctx context.Context, executorID int64, contextID string) error
	ListContexts(ctx context.Context, executorID int64) ([]BrowserContextRef, error)
}

// HTTPRequestData is one target-SUT HTTP request issued by http.request.
type HTTPRequestData struct {
	Method  string
	URL     string
	Headers map[string]string
	Body    []byte
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

// DBQueryResult is one read-only target-database query result.
type DBQueryResult struct {
	Columns   []string
	Rows      [][]string
	RowCount  int
	Truncated bool
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

// DatabaseQuerier runs read-only queries and schema summaries on the target
// PostgreSQL database (adapter owns the DSN/statement timeout/READ ONLY tx).
type DatabaseQuerier interface {
	Query(ctx context.Context, dsn, username, password, sql string, maxRows int) (DBQueryResult, error)
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
	APIs        SUTAPIResolver
	Browser     BrowserExecutor
	Credentials CredentialReader
	Content     ContentStore
	HTTPClient  HTTPDoer
	Database    DatabaseQuerier
	Coverage    CoverageReader
	Gaps        GapEnsurer
	TeamRoom    TeamRoom
	Human       HumanRequestService
	Visual      VisualAssertPort
	// KnowledgeHook receives passed-case observations for knowledge
	// sedimentation (spec §10.1). Optional and best effort.
	KnowledgeHook CaseSuccessHook
	// KnowledgeFailureHook receives failed-case observations so the knowledge
	// reused by the failed run can be marked suspect. Optional and best effort.
	KnowledgeFailureHook CaseFailureHook
	Settings             SettingsReader
	Clock                Clock
	IDs                  IDGenerator
	Events               EventPublisher
	Audit                AuditRecorder
	InstanceID           string
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
	apis        SUTAPIResolver
	browser     BrowserExecutor
	credentials CredentialReader
	content     ContentStore
	httpClient  HTTPDoer
	database    DatabaseQuerier
	coverage    CoverageReader
	gaps        GapEnsurer
	teamRoom    TeamRoom
	human       HumanRequestService
	visual      VisualAssertPort
	knowledge   CaseSuccessHook
	// knowledgeFailure is the optional failed-case knowledge hook.
	knowledgeFailure CaseFailureHook
	settings         SettingsReader
	clock            Clock
	ids              IDGenerator
	events           EventPublisher
	audit            AuditRecorder
	instanceID       string
	leaseTTL         time.Duration
	heartbeat        time.Duration
	// attribution is the deterministic four-value failure attribution the
	// analyze.failure result exposes to the failure_analyst model (Task 37).
	attribution *exploreapp.FailureAttribution

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
		apis:        deps.APIs,
		browser:     deps.Browser,
		credentials: deps.Credentials,
		content:     deps.Content,
		httpClient:  deps.HTTPClient,
		database:    deps.Database,
		coverage:    deps.Coverage,
		gaps:        deps.Gaps,
		teamRoom:    deps.TeamRoom,
		human:       deps.Human,
		visual:      deps.Visual,
		knowledge:   deps.KnowledgeHook,
		// knowledgeFailure is optional: a nil field disables the failed-case
		// notification.
		knowledgeFailure: deps.KnowledgeFailureHook,
		settings:         deps.Settings,
		clock:            deps.Clock,
		ids:              deps.IDs,
		events:           deps.Events,
		audit:            deps.Audit,
		instanceID:       deps.InstanceID,
		leaseTTL:         lease,
		heartbeat:        heartbeat,
		attribution:      exploreapp.NewFailureAttribution(),
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

	return execution.TaskSnapshot{
		Case:            caseSnapshot,
		Intent:          intent,
		CaseRun:         startedRun,
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
	caseStep, ok := findStep(caseSnapshot, in.Seq)
	if !ok {
		return nil, platform.Invalid("seq", fmt.Sprintf("step %d is not part of the case", in.Seq))
	}
	status := execution.StepStatus(in.Status)
	if !status.Valid() {
		return nil, platform.Invalid("status", "unsupported step status")
	}
	target := caseStep.TargetVersionSUTID
	if in.VersionSUTID != nil {
		// An unresolved case target (a snapshot read before the default
		// inheritance was applied) accepts the model's resolved target instead
		// of rejecting the recording call.
		if caseStep.TargetVersionSUTID > 0 && *in.VersionSUTID != caseStep.TargetVersionSUTID {
			return nil, platform.Invalid("version_sut_id", "does not match the case step target")
		}
		target = *in.VersionSUTID
	}
	now := s.now()
	result := execution.StepResult{
		ID:                 s.nextID(),
		CaseRunID:          caseRun.ID,
		Seq:                in.Seq,
		TargetVersionSUTID: optionalID(target),
		Status:             status,
		Observed:           map[string]any{},
		Error:              truncateRunes(in.Error, execution.MaxStepErrorLength),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if strings.TrimSpace(in.Observed) != "" {
		result.Observed["summary"] = truncateRunes(in.Observed, 4000)
	}
	if status == execution.StepRunning {
		result.StartedAt = &now
	}
	if status.Terminal() {
		result.FinishedAt = &now
	}
	ok, err = s.results.UpsertStep(ctx, result, caseRun.DispatchEpoch, caseRun.DispatchToken)
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
	target := assertion.TargetVersionSUTID
	if in.VersionSUTID != nil {
		if assertion.TargetVersionSUTID > 0 && *in.VersionSUTID != assertion.TargetVersionSUTID {
			return nil, platform.Invalid("version_sut_id", "does not match the case assertion target")
		}
		target = *in.VersionSUTID
	}
	now := s.now()
	result := execution.AssertionResult{
		ID:                 s.nextID(),
		CaseRunID:          caseRun.ID,
		StepSeq:            in.StepSeq,
		AssertionSeq:       in.AssertionSeq,
		Kind:               assertion.Kind,
		Verdict:            verdict,
		Reason:             truncateRunes(in.Reason, execution.MaxAssertionReasonLength),
		Expected:           truncateRunes(assertion.Expected, execution.MaxExpectedLength),
		Actual:             truncateRunes(in.Actual, execution.MaxActualLength),
		TargetVersionSUTID: optionalID(target),
		CreatedAt:          now,
		UpdatedAt:          now,
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
	steps = execution.NormalizeVisualSteps(steps, assertions)
	status, class, summary, err := execution.Aggregate(execution.VerdictInput{
		Case:       caseSnapshot,
		Steps:      steps,
		Assertions: assertions,
		Finish:     &candidate,
	})
	if err != nil {
		return execution.CaseRun{}, platform.Invalid("verdict", err.Error())
	}
	status, class, summary = execution.ApplyVisualOutcome(status, class, summary, assertions)
	// P3 §6.4 automatic evidence: a failed/blocked/inconclusive run with a live
	// browser context captures one screenshot before the terminal write so the
	// failure is triageable instead of a zero-evidence row (QA-F1).
	s.captureTerminalEvidence(ctx, caseRun, status)
	if evidenceCount, countErr := s.evidence.CountByCaseRun(ctx, caseRun.ID); countErr == nil {
		summary.EvidenceCount = evidenceCount
	}
	if caseRun.Summary.HumanRequestID > 0 {
		summary.HumanRequestID = caseRun.Summary.HumanRequestID
	}
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
	}
	return finished, nil
}

// Finalize is called by the worker run sink after the agent loop ends.
// dispatchEpoch is the case_run dispatch epoch carried by the work item: a late
// Finalize from an older epoch has lost the run to a newer dispatch and must not
// finalize it nor stop the new owner's heartbeat. A retryable run error before
// the attempt budget leaves the case_run running for a redispatch; otherwise the
// deterministic aggregation writes the terminal state. A lease-lost or paused
// run is left to its new owner.
func (s *CaseRunner) Finalize(ctx context.Context, caseRunID, dispatchEpoch int64, runErr error, attempt, maxAttempts int) error {
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
	if caseRun.Status.Finished() || caseRun.Status == execution.CaseRunPaused {
		return nil
	}
	if runErr != nil {
		class, retryable := classifyFinalRunError(ctx, runErr)
		if retryable && maxAttempts > 0 && attempt < maxAttempts {
			now := s.now()
			_, _ = s.caseRuns.UpdateCAS(ctx, caseRun.ID, caseRun.DispatchEpoch, caseRun.DispatchToken, func(run *execution.CaseRun) error {
				run.Summary.LastError = truncateRunes(runErr.Error(), 2000)
				run.UpdatedAt = now
				return nil
			})
			return nil
		}
		candidate := execution.FinishCandidate{
			Verdict:      execution.CaseRunFailed,
			FailureClass: class,
		}
		if class == execution.FailureClassBlocked {
			candidate.Verdict = execution.CaseRunBlocked
		}
		// A run that dies before recording any step/assertion result must not
		// look like a silent zero-row failure: persist the concrete error as
		// text evidence so the reason is visible in the report/UI.
		s.recordRunFailureEvidence(ctx, caseRun, runErr)
		if err := s.finalizeWithCandidate(ctx, caseRun, candidate, runErr); err != nil {
			return err
		}
		return nil
	}
	// The agent loop succeeded but never finished the case (for example the turn
	// budget was exhausted): aggregate whatever was recorded.
	if err := s.finalizeWithCandidate(ctx, caseRun, execution.FinishCandidate{Verdict: execution.CaseRunInconclusive}, nil); err != nil {
		return err
	}
	return nil
}

func (s *CaseRunner) finalizeWithCandidate(ctx context.Context, caseRun execution.CaseRun, candidate execution.FinishCandidate, failure error) error {
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
	steps = execution.NormalizeVisualSteps(steps, assertions)
	status, class, summary, err := execution.Aggregate(execution.VerdictInput{
		Case: caseSnapshot, Steps: steps, Assertions: assertions, Finish: &candidate,
	})
	if err != nil {
		return platform.Invalid("verdict", err.Error())
	}
	status, class, summary = execution.ApplyVisualOutcome(status, class, summary, assertions)
	if failure != nil {
		// Persist the concrete failure reason (delivery reports and attribution
		// read summary.last_error): a failed/blocked run must explain itself.
		summary.LastError = truncateRunes(failure.Error(), 2000)
	}
	s.captureTerminalEvidence(ctx, caseRun, status)
	if evidenceCount, countErr := s.evidence.CountByCaseRun(ctx, caseRun.ID); countErr == nil {
		summary.EvidenceCount = evidenceCount
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
	})
	s.forgetWaitTracking(caseRun.ID)
	// A failed finalization (agent-run failure or exhausted redispatch) notifies
	// the optional knowledge failure hook exactly once, from the terminal owner.
	if terminalWrote {
		s.notifyCaseFailure(ctx, s.versionForCaseRun(caseRun), finished, caseSnapshot, finished.FailureClass)
	}
	return nil
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

// classifyFinalRunError maps an agent run failure to the P3 failure taxonomy and
// its retry eligibility.
func classifyFinalRunError(ctx context.Context, err error) (execution.FailureClass, bool) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return execution.FailureClassTimeout, false
	}
	var runErr *agentapp.RunError
	if errors.As(err, &runErr) {
		switch runErr.Reason {
		case "case_blocked":
			// The execution task provider rejected the case scope before the
			// agent loop (unresolvable step target, stale case, missing binding):
			// this is a blocked case, not an automation failure.
			return execution.FailureClassBlocked, false
		case "budget", "model_error", "ai_unavailable", "invalid_payload", "profile_not_found":
			return execution.FailureClassModel, runErr.Retryable
		case "tool_failure":
			var apiErr *platform.Error
			if errors.As(err, &apiErr) && apiErr.Code == "executor_unavailable" {
				// "executor" is its own failure class (executor_lost), never
				// environment: the UI shows 执行器失联 and the run may be retried
				// on a recovered executor.
				return execution.FailureClassExecutorLost, runErr.Retryable
			}
			return execution.FailureClassAutomation, runErr.Retryable
		case "interrupted", "aborted":
			return execution.FailureClassTimeout, false
		default:
			return execution.FailureClassUnknown, runErr.Retryable
		}
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return execution.FailureClassTimeout, false
	}
	return execution.FailureClassUnknown, agentapp.IsRetryable(err)
}

// writeTerminal persists the terminal verdict when this owner still holds the
// dispatch fence. The bool reports whether this call won the CAS: false means
// another owner already finalized the run (or the fence was lost), so callers
// must not repeat one-time side effects such as the knowledge success hook.
func (s *CaseRunner) writeTerminal(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus, class execution.FailureClass, summary execution.CaseRunSummary) (execution.CaseRun, bool, error) {
	now := s.now()
	ok, err := s.caseRuns.UpdateCAS(ctx, caseRun.ID, caseRun.DispatchEpoch, caseRun.DispatchToken, func(run *execution.CaseRun) error {
		run.Status = status
		run.FailureClass = class
		run.Summary = summary
		if run.StartedAt == nil {
			run.StartedAt = &now
		}
		run.FinishedAt = &now
		run.LeaseUntil = nil
		return nil
	})
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	if !ok {
		// Another owner already finalized the run; return the stored truth
		// (Finish is idempotent for terminal rows).
		current, getErr := s.caseRuns.Get(ctx, caseRun.ID)
		if getErr != nil {
			return execution.CaseRun{}, false, mapExecutionError(getErr, "case_run")
		}
		if current.Status.Finished() {
			return current, false, nil
		}
		return execution.CaseRun{}, false, platform.LeaseLost()
	}
	finished, err := s.caseRuns.Get(ctx, caseRun.ID)
	if err != nil {
		return execution.CaseRun{}, false, mapExecutionError(err, "case_run")
	}
	return finished, true, nil
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

// executionSUTBindings resolves the SUTs referenced by the case snapshot and
// validates every step/assertion target against the version bindings. P2 allows
// a step target to be empty (inherit case_version.default_version_sut_id); a
// case with no default target inherits the version's sole binding. The resolved
// targets are written back to the snapshot so the task context, the SUT context
// rows and the recorded results all carry the same version_sut_id.
//
// account_role is deliberately not validated against the bound test accounts:
// the design contract allows any role label (for example "anonymous" for a
// logged-out step), an unbound role simply runs an unauthenticated browser
// context, and account.credentials returns a typed business error if the model
// asks for credentials that do not exist. Rejecting the role here killed the
// whole run before any step/assertion/evidence could be recorded (QA-F1).
func executionSUTBindings(snapshot *execution.CaseSnapshot, bindings []execution.SUTBinding) ([]execution.SUTBinding, error) {
	if snapshot == nil {
		return nil, platform.StateConflict("the case snapshot is missing")
	}
	byID := make(map[int64]execution.SUTBinding, len(bindings))
	for _, binding := range bindings {
		byID[binding.VersionSUTID] = binding
	}
	fallback := snapshot.DefaultVersionSUTID
	if fallback <= 0 && len(bindings) == 1 {
		fallback = bindings[0].VersionSUTID
		snapshot.DefaultVersionSUTID = fallback
	}
	used := make([]int64, 0, len(bindings))
	seen := map[int64]struct{}{}
	add := func(id int64) {
		if id <= 0 {
			return
		}
		if _, ok := seen[id]; ok {
			return
		}
		seen[id] = struct{}{}
		used = append(used, id)
	}
	for _, id := range snapshot.VersionSUTIDs {
		add(id)
	}
	add(fallback)
	for i := range snapshot.Steps {
		step := &snapshot.Steps[i]
		target := step.TargetVersionSUTID
		if target <= 0 {
			target = fallback
			step.TargetVersionSUTID = target
		}
		if target <= 0 {
			return nil, platform.StateConflict(fmt.Sprintf(
				"step %d has no target version_sut: set case_step.target_version_sut_id or case_version.default_version_sut_id", step.Seq))
		}
		if _, ok := byID[target]; !ok {
			return nil, platform.StateConflict(fmt.Sprintf("step %d targets unbound version_sut %s", step.Seq, platformid.Format(target)))
		}
		add(target)
	}
	for i := range snapshot.Assertions {
		assertion := &snapshot.Assertions[i]
		target := assertion.TargetVersionSUTID
		if target <= 0 {
			target = fallback
			assertion.TargetVersionSUTID = target
		}
		if target <= 0 {
			// The assertion carries only informational target metadata; the
			// step target still routes the browser/API action.
			continue
		}
		if _, ok := byID[target]; !ok {
			return nil, platform.StateConflict(fmt.Sprintf(
				"assertion %d.%d targets unbound version_sut %s", assertion.StepSeq, assertion.AssertionSeq, platformid.Format(target)))
		}
		add(target)
	}
	snapshot.VersionSUTIDs = sortedSUTIDs(used)
	resolved := make([]execution.SUTBinding, 0, len(used))
	for _, id := range used {
		binding, ok := byID[id]
		if !ok {
			return nil, platform.StateConflict(fmt.Sprintf("version_sut %s is not bound to the version", platformid.Format(id)))
		}
		resolved = append(resolved, binding)
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
		byKey[sutContextKey(row.VersionSUTID, row.AccountRole)] = row
	}
	for _, step := range snapshot.Steps {
		key := sutContextKey(step.TargetVersionSUTID, step.AccountRole)
		if row, ok := byKey[key]; ok {
			rowID := row.ID
			if _, updateErr := s.sutContexts.UpdateCAS(ctx, rowID, func(target *execution.CaseRunSUTContext) error {
				target.LeaseEpoch = epoch
				target.ExecutorID = caseRun.OwnerExecutorID
				if target.Status == execution.SUTContextClosed || target.Status == execution.SUTContextFailed {
					target.Status = execution.SUTContextCreating
					target.BrowserContextID = ""
					target.ClosedAt = nil
				}
				return nil
			}); updateErr != nil {
				return mapExecutionError(updateErr, "case_run_sut_context")
			}
			continue
		}
		if err := s.sutContexts.Create(ctx, execution.CaseRunSUTContext{
			ID:           s.nextID(),
			CaseRunID:    caseRun.ID,
			VersionSUTID: step.TargetVersionSUTID,
			AccountRole:  step.AccountRole,
			ExecutorID:   caseRun.OwnerExecutorID,
			LeaseEpoch:   epoch,
			Status:       execution.SUTContextCreating,
			CreatedAt:    s.now(),
			UpdatedAt:    s.now(),
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
	if request.Status != execution.HumanResolved {
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
// case_run reaches failed/blocked/inconclusive and one of its browser contexts
// is still active, take one screenshot and store it as evidence. The capture is
// best effort and bounded; a missing/dead context never changes the verdict.
func (s *CaseRunner) captureTerminalEvidence(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus) {
	if s.browser == nil || s.evidence == nil || s.content == nil || s.sutContexts == nil {
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
		if rows[i].Status == execution.SUTContextActive && rows[i].BrowserContextID != "" {
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
	result, actionErr := s.browser.Action(actionCtx, executorID, target.BrowserContextID, "screenshot", json.RawMessage(`{}`))
	if actionErr != nil || result.Error != nil {
		return
	}
	_, _ = s.storeBrowserEvidence(actionCtx, caseRun, target.VersionSUTID, "screenshot", result, 0)
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
		if row.BrowserContextID != "" && s.browser != nil {
			_ = s.browser.Close(ctx, executorID, row.BrowserContextID)
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
	fact := caseSuccessFact(versionID, caseRun, snapshot, summary)
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
	fact := caseFailureFact(versionID, caseRun, snapshot, class)
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
// identifiers only, deduplicated and in case order.
func caseSuccessFact(versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, summary execution.CaseRunSummary) CaseSuccessFact {
	fact := CaseSuccessFact{
		VersionID:     versionID,
		CaseRunID:     caseRun.ID,
		CaseVersionID: caseRun.CaseVersionID,
		Title:         snapshot.Title,
		EvidenceCount: summary.EvidenceCount,
	}
	fact.AccountRoles, fact.PageIDs, fact.APIIDs = caseObservationIDs(snapshot)
	return fact
}

// caseFailureFact projects one failed case snapshot into the failure hook
// observation. It deliberately carries no title and no summary text.
func caseFailureFact(versionID int64, caseRun execution.CaseRun, snapshot execution.CaseSnapshot, class execution.FailureClass) CaseFailureFact {
	fact := CaseFailureFact{
		VersionID:     versionID,
		CaseRunID:     caseRun.ID,
		CaseVersionID: caseRun.CaseVersionID,
		FailureClass:  class,
	}
	fact.AccountRoles, fact.PageIDs, fact.APIIDs = caseObservationIDs(snapshot)
	return fact
}

// caseObservationIDs extracts the deduplicated page/API/account-role
// back-references of one case snapshot in case order.
func caseObservationIDs(snapshot execution.CaseSnapshot) (roles []string, pages []int64, apis []int64) {
	seenRoles := map[string]struct{}{}
	seenPages := map[int64]struct{}{}
	seenAPIs := map[int64]struct{}{}
	for _, step := range snapshot.Steps {
		if role := strings.TrimSpace(step.AccountRole); role != "" {
			if _, seen := seenRoles[role]; !seen {
				seenRoles[role] = struct{}{}
				roles = append(roles, role)
			}
		}
		if step.TargetPageID != nil && *step.TargetPageID > 0 {
			if _, seen := seenPages[*step.TargetPageID]; !seen {
				seenPages[*step.TargetPageID] = struct{}{}
				pages = append(pages, *step.TargetPageID)
			}
		}
		if step.TargetAPIID != nil && *step.TargetAPIID > 0 {
			if _, seen := seenAPIs[*step.TargetAPIID]; !seen {
				seenAPIs[*step.TargetAPIID] = struct{}{}
				apis = append(apis, *step.TargetAPIID)
			}
		}
	}
	return roles, pages, apis
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

func caseContextKey(caseRunID, versionSUTID int64, role string) string {
	return fmt.Sprintf(executorContextKeyFormat, caseRunID, versionSUTID, role)
}

func sutContextKey(versionSUTID int64, role string) string {
	return platformid.Format(versionSUTID) + "|" + role
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

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

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
