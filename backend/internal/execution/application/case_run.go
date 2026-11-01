package executionapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

// Case-run defaults. Settings override the bounded values; the lease is a
// runtime constant because a case-run is renewed by the API-side agent loop.
const (
	defaultCaseLeaseSeconds  = 30
	defaultEvidenceMaxBytes  = int64(5 << 20)
	evidenceMaxPerCaseRun    = execution.EvidenceMaxPerCaseRun
	maxSUTFilenameLength     = 255
	executorContextKeyFormat = "run:%d:vs:%d:role:%s"
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
	Settings    SettingsReader
	Clock       Clock
	IDs         IDGenerator
	Events      EventPublisher
	Audit       AuditRecorder
	InstanceID  string
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
	settings    SettingsReader
	clock       Clock
	ids         IDGenerator
	events      EventPublisher
	audit       AuditRecorder
	instanceID  string

	mu          sync.Mutex
	activeRuns  map[int64]context.CancelFunc
	waitTotals  map[int64]time.Duration
	waitStarted map[int64]time.Time
}

var (
	_ tools.ExecutionPort = (*CaseRunner)(nil)
	_ RunCanceller        = (*CaseRunner)(nil)
)

// NewCaseRunner wires the case executor runtime.
func NewCaseRunner(deps CaseRunnerDeps) *CaseRunner {
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
		settings:    deps.Settings,
		clock:       deps.Clock,
		ids:         deps.IDs,
		events:      deps.Events,
		audit:       deps.Audit,
		instanceID:  deps.InstanceID,
		activeRuns:  map[int64]context.CancelFunc{},
		waitTotals:  map[int64]time.Duration{},
		waitStarted: map[int64]time.Time{},
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

// ClearRun forgets the cancel function after a run finished.
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
	suts, err := executionSUTBindings(caseSnapshot, bindings)
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
		if *in.VersionSUTID != caseStep.TargetVersionSUTID {
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
		if *in.VersionSUTID != assertion.TargetVersionSUTID {
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
	if evidenceCount, countErr := s.evidence.CountByCaseRun(ctx, caseRun.ID); countErr == nil {
		summary.EvidenceCount = evidenceCount
	}
	if caseRun.Summary.HumanRequestID > 0 {
		summary.HumanRequestID = caseRun.Summary.HumanRequestID
	}
	finished, err := s.writeTerminal(ctx, caseRun, status, class, summary)
	if err != nil {
		return execution.CaseRun{}, err
	}
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
	return finished, nil
}

// Finalize is called by the worker run sink after the agent loop ends. A
// retryable run error before the attempt budget leaves the case_run running for
// a redispatch; otherwise the deterministic aggregation writes the terminal
// state. A lease-lost or paused run is left to its new owner.
func (s *CaseRunner) Finalize(ctx context.Context, caseRunID int64, runErr error, attempt, maxAttempts int) error {
	if s.caseRuns == nil {
		return errors.New("case runner is not configured")
	}
	caseRun, err := s.caseRuns.Get(ctx, caseRunID)
	if err != nil {
		return mapExecutionError(err, "case_run")
	}
	if caseRun.Status.Finished() || caseRun.Status == execution.CaseRunPaused {
		return nil
	}
	if IsRunLeaseLost(runErr) {
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
		if err := s.finalizeWithCandidate(ctx, caseRun, candidate); err != nil {
			return err
		}
		return nil
	}
	// The agent loop succeeded but never finished the case (for example the turn
	// budget was exhausted): aggregate whatever was recorded.
	if err := s.finalizeWithCandidate(ctx, caseRun, execution.FinishCandidate{Verdict: execution.CaseRunInconclusive}); err != nil {
		return err
	}
	return nil
}

func (s *CaseRunner) finalizeWithCandidate(ctx context.Context, caseRun execution.CaseRun, candidate execution.FinishCandidate) error {
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
	if evidenceCount, countErr := s.evidence.CountByCaseRun(ctx, caseRun.ID); countErr == nil {
		summary.EvidenceCount = evidenceCount
	}
	finished, err := s.writeTerminal(ctx, caseRun, status, class, summary)
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
		case "budget", "model_error", "ai_unavailable", "invalid_payload", "profile_not_found":
			return execution.FailureClassModel, runErr.Retryable
		case "tool_failure":
			var apiErr *platform.Error
			if errors.As(err, &apiErr) && apiErr.Code == "executor_unavailable" {
				return execution.FailureClassEnvironment, runErr.Retryable
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

func (s *CaseRunner) writeTerminal(ctx context.Context, caseRun execution.CaseRun, status execution.CaseRunStatus, class execution.FailureClass, summary execution.CaseRunSummary) (execution.CaseRun, error) {
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
		return execution.CaseRun{}, mapExecutionError(err, "case_run")
	}
	if !ok {
		// Another owner already finalized the run; return the stored truth
		// (Finish is idempotent for terminal rows).
		current, getErr := s.caseRuns.Get(ctx, caseRun.ID)
		if getErr != nil {
			return execution.CaseRun{}, mapExecutionError(getErr, "case_run")
		}
		if current.Status.Finished() {
			return current, nil
		}
		return execution.CaseRun{}, platform.LeaseLost()
	}
	return s.caseRuns.Get(ctx, caseRun.ID)
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
// validates every step/assertion target against the version bindings.
func executionSUTBindings(snapshot execution.CaseSnapshot, bindings []execution.SUTBinding) ([]execution.SUTBinding, error) {
	byID := make(map[int64]execution.SUTBinding, len(bindings))
	for _, binding := range bindings {
		byID[binding.VersionSUTID] = binding
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
	for _, step := range snapshot.Steps {
		add(step.TargetVersionSUTID)
	}
	for _, assertion := range snapshot.Assertions {
		add(assertion.TargetVersionSUTID)
	}
	if snapshot.DefaultVersionSUTID > 0 {
		add(snapshot.DefaultVersionSUTID)
	}
	resolved := make([]execution.SUTBinding, 0, len(used))
	for _, id := range used {
		binding, ok := byID[id]
		if !ok {
			return nil, platform.StateConflict(fmt.Sprintf("version_sut %s is not bound to the version", platformid.Format(id)))
		}
		resolved = append(resolved, binding)
	}
	for _, step := range snapshot.Steps {
		binding, ok := byID[step.TargetVersionSUTID]
		if !ok {
			return nil, platform.StateConflict(fmt.Sprintf("step %d targets unbound version_sut %s", step.Seq, platformid.Format(step.TargetVersionSUTID)))
		}
		if step.AccountRole != "" && len(binding.AccountRoles) > 0 && !containsString(binding.AccountRoles, step.AccountRole) {
			return nil, platform.StateConflict(fmt.Sprintf("step %d account role %q is not bound to version_sut %s", step.Seq, step.AccountRole, platformid.Format(step.TargetVersionSUTID)))
		}
	}
	return resolved, nil
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
	return defaultCaseLeaseSeconds * time.Second
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
