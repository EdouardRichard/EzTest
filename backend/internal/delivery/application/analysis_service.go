package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisMaxOpenIssues bounds the open issue summaries injected into a triage
// input (spec §5.6/§9.4).
const analysisMaxOpenIssues = 50

// analysisMaxOpenIssueScan bounds the open issue read used to validate an
// explicit mergeIntoIssueId (spec §9.4). It is the §7.1 list limit of 100.
const analysisMaxOpenIssueScan = 100

// AnalysisDeps are the ports of the analysis-run service. Runs and Agents are
// always required; Execution is required by StartFailure, Issues by the triage
// side, and OpenIssues/Profiles/Anchors/Events/Audit are optional best-effort
// ports (OpenIssues is required to validate mergeIntoIssueId in production).
type AnalysisDeps struct {
	Runs       AnalysisStore
	Execution  AnalysisExecutionReader
	Issues     AnalysisIssueWriter
	OpenIssues AnalysisIssueReader
	Anchors    AnalysisCodeAnchorPort
	Agents     AnalysisAgentRuntime
	Profiles   AnalysisAgentProfileReader
	Events     AnalysisEventPublisher
	Audit      AnalysisAuditRecorder
	IDs        AnalysisIDGenerator
	Clock      AnalysisClock
	// MaxCases is the per-run failed-case budget
	// (EZTEST_ANALYSIS_MAX_CASES, design spec §10.5/§11.4). Zero keeps the
	// spec default domain.AnalysisMaxFailureCandidates (20).
	MaxCases int
}

// AnalysisService coordinates the failure-analysis and issue-triage runs of
// spec §5.6/§6.3: it starts scoped agent runs, polls them, validates the
// structured submission and materializes the issues. An unavailable LLM degrades
// the run instead of failing the batch chain.
type AnalysisService struct {
	deps AnalysisDeps

	mu      sync.Mutex
	cancel  context.CancelFunc
	running bool
	wg      sync.WaitGroup
}

// NewAnalysisService wires the service to its ports.
func NewAnalysisService(deps AnalysisDeps) *AnalysisService {
	return &AnalysisService{deps: deps}
}

// StartFailure creates and starts the failure-analysis run of one terminal
// batch. Only failed(application) case runs are accepted, at most
// EZTEST_ANALYSIS_MAX_CASES (default 20); the overflow is recorded as
// stats.truncated. A batch without such a failure is analysis_not_applicable
// and an already active run is analysis_active.
func (s *AnalysisService) StartFailure(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error) {
	if err := s.requireFailurePorts(); err != nil {
		return domain.AnalysisRun{}, err
	}
	if existing, err := s.deps.Runs.Active(ctx, batchID, domain.AnalysisKindFailure); err == nil {
		return existing, domain.NewAnalysisConflict(domain.ReasonAnalysisActive,
			fmt.Sprintf("failure analysis of batch %d is already active", batchID))
	} else if !errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, fmt.Errorf("delivery: check active failure analysis: %w", err)
	}

	cases, truncated, err := s.listFailedApplicationCases(ctx, versionID, batchID, s.maxFailureCases())
	if err != nil {
		return domain.AnalysisRun{}, fmt.Errorf("delivery: list failed cases of batch %d: %w", batchID, err)
	}
	caseRunIDs := make([]int64, 0, len(cases))
	for _, failedCase := range cases {
		caseRunIDs = append(caseRunIDs, failedCase.CaseRunID)
	}
	if len(caseRunIDs) == 0 {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("batch %d has no failed application case run to analyze", batchID))
	}

	runID, err := s.nextID()
	if err != nil {
		return domain.AnalysisRun{}, err
	}
	now := s.now()
	run := domain.AnalysisRun{
		ID:            runID,
		VersionID:     versionID,
		BatchID:       batchID,
		Kind:          domain.AnalysisKindFailure,
		Status:        domain.AnalysisQueued,
		PromptVersion: domain.AnalysisPromptVersionFailure,
		Input:         domain.EncodeFailureInput(versionID, batchID, caseRunIDs),
		CreatedBy:     actor,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	if truncated {
		run.Stats = json.RawMessage(`{"truncated":true}`)
	}
	run, err = s.deps.Runs.Create(ctx, run)
	if err != nil {
		return run, err
	}
	return s.startRun(ctx, run, AnalysisAgentProfileFailure, failureAnalysisInstruction(run, caseRunIDs))
}

// StartTriage creates and starts the issue-triage run of one terminal batch. It
// requires a done failure analysis with at least one validated candidate; the
// failure candidates and the open issue summaries are copied into the input.
func (s *AnalysisService) StartTriage(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error) {
	if err := s.requireTriagePorts(); err != nil {
		return domain.AnalysisRun{}, err
	}
	if existing, err := s.deps.Runs.Active(ctx, batchID, domain.AnalysisKindIssueTriage); err == nil {
		return existing, domain.NewAnalysisConflict(domain.ReasonAnalysisActive,
			fmt.Sprintf("issue triage of batch %d is already active", batchID))
	} else if !errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, fmt.Errorf("delivery: check active issue triage: %w", err)
	}

	failure, err := s.deps.Runs.Latest(ctx, batchID, domain.AnalysisKindFailure)
	if errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("batch %d has no failure analysis to triage", batchID))
	}
	if err != nil {
		return domain.AnalysisRun{}, fmt.Errorf("delivery: read latest failure analysis of batch %d: %w", batchID, err)
	}
	if failure.Status != domain.AnalysisDone {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("failure analysis %d of batch %d is %s, not done", failure.ID, batchID, failure.Status))
	}
	failureResult, err := domain.DecodeFailureResult(failure.Result)
	if err != nil {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("failure analysis %d of batch %d has no readable result", failure.ID, batchID))
	}
	if len(failureResult.Candidates) == 0 {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("failure analysis %d of batch %d has no candidate to triage", failure.ID, batchID))
	}

	openIssues := s.openIssues(ctx, versionID)
	runID, err := s.nextID()
	if err != nil {
		return domain.AnalysisRun{}, err
	}
	now := s.now()
	run := domain.AnalysisRun{
		ID:            runID,
		VersionID:     versionID,
		BatchID:       batchID,
		Kind:          domain.AnalysisKindIssueTriage,
		Status:        domain.AnalysisQueued,
		PromptVersion: domain.AnalysisPromptVersionTriage,
		Input:         domain.EncodeTriageInput(versionID, batchID, failureResult.Candidates, openIssues),
		CreatedBy:     actor,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	run, err = s.deps.Runs.Create(ctx, run)
	if err != nil {
		return run, err
	}
	// A triage run degraded at start already ran its §5.6 fallback inside
	// startRun, so nothing else is needed here.
	return s.startRun(ctx, run, AnalysisAgentProfileTriage, triageInstruction(run, len(failureResult.Candidates)))
}

// LatestByKind returns the newest analysis run of one batch and kind. The
// execution autonomy adapter addresses analyses by (batch, kind) without a
// version id (spec §6.2).
func (s *AnalysisService) LatestByKind(ctx context.Context, batchID int64, kind string) (domain.AnalysisRun, error) {
	if s.deps.Runs == nil {
		return domain.AnalysisRun{}, errors.New("delivery: analysis store is not configured")
	}
	run, err := s.deps.Runs.Latest(ctx, batchID, kind)
	if errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotFoundForBatch(batchID, kind)
	}
	return run, err
}

// Get returns one analysis run scoped to its version and batch.
func (s *AnalysisService) Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error) {
	if s.deps.Runs == nil {
		return domain.AnalysisRun{}, errors.New("delivery: analysis store is not configured")
	}
	run, err := s.deps.Runs.Get(ctx, versionID, batchID, analysisID)
	if errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotFound(analysisID)
	}
	return run, err
}

// List returns the analysis runs of one batch, newest first.
func (s *AnalysisService) List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error) {
	if s.deps.Runs == nil {
		return nil, errors.New("delivery: analysis store is not configured")
	}
	return s.deps.Runs.List(ctx, versionID, batchID)
}

// ProposeFailureCandidates is the analysis.candidate_propose submission path
// (spec §9.3). It validates the candidates against the run input, persists the
// valid ones in result and returns the warnings for the agent. Invalid entries
// never fail the run.
func (s *AnalysisService) ProposeFailureCandidates(ctx context.Context, analysisID int64, candidates []domain.FailureCandidate) (domain.AnalysisRun, []string, error) {
	run, err := s.runningRun(ctx, analysisID, domain.AnalysisKindFailure)
	if err != nil {
		return domain.AnalysisRun{}, nil, err
	}
	if s.deps.Execution == nil {
		return domain.AnalysisRun{}, nil, errors.New("delivery: execution reader is not configured")
	}
	cases, _, err := s.listFailedApplicationCases(ctx, run.VersionID, run.BatchID, s.maxFailureCases())
	if err != nil {
		return domain.AnalysisRun{}, nil, fmt.Errorf("delivery: list failed cases of analysis run %d: %w", analysisID, err)
	}
	allowed := make(map[int64]int64, len(cases))
	allowedEvidence := make(map[int64]struct{})
	for _, failedCase := range cases {
		allowed[failedCase.CaseRunID] = failedCase.CaseNodeID
		for _, evidenceID := range failedCase.EvidenceIDs {
			if evidenceID > 0 {
				allowedEvidence[evidenceID] = struct{}{}
			}
		}
	}
	// Validate the merged submission (previously proposed candidates included)
	// against the authoritative failed(application) set of the batch.
	existing, err := domain.DecodeFailureResult(run.Result)
	if err != nil {
		return domain.AnalysisRun{}, nil, fmt.Errorf("delivery: decode failure analysis %d result: %w", analysisID, err)
	}
	merged := append(existing.Candidates, candidates...)
	valid, warnings := domain.ValidateFailureCandidates(allowed, allowedEvidence, merged)
	result := domain.EncodeFailureResult(valid, warnings)
	stats := analysisMergeStats(run.Stats, map[string]any{
		"candidateCount": len(valid),
		"warningCount":   len(warnings),
	})
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.AnalysisRunning, domain.AnalysisRunning,
		AnalysisRunPatch{Result: result, Stats: stats})
	if err != nil {
		return domain.AnalysisRun{}, nil, err
	}
	return updated, warnings, nil
}

// ProposeIssueDrafts is the triage.issue_draft submission path (spec §9.4). It
// validates the drafts, persists the valid ones in result and returns the
// warnings for the agent. An explicit mergeIntoIssueId is only accepted for an
// open issue of the version; every other target is stripped with a warning.
func (s *AnalysisService) ProposeIssueDrafts(ctx context.Context, analysisID int64, drafts []domain.IssueDraft) (domain.AnalysisRun, []string, error) {
	run, err := s.runningRun(ctx, analysisID, domain.AnalysisKindIssueTriage)
	if err != nil {
		return domain.AnalysisRun{}, nil, err
	}
	existing, err := domain.DecodeTriageResult(run.Result)
	if err != nil {
		return domain.AnalysisRun{}, nil, fmt.Errorf("delivery: decode issue triage %d result: %w", analysisID, err)
	}
	merged := append(existing.Drafts, drafts...)
	allowedEvidence, _ := s.allowedEvidenceIDs(ctx, run.VersionID, run.BatchID)
	valid, warnings := domain.ValidateIssueDrafts(merged, allowedEvidence)
	valid, mergeWarnings := s.filterMergeTargets(ctx, run.VersionID, valid)
	warnings = append(warnings, mergeWarnings...)
	result, err := domain.EncodeTriageResult(valid, warnings)
	if err != nil {
		return domain.AnalysisRun{}, nil, err
	}
	stats := analysisMergeStats(run.Stats, map[string]any{
		"issueDraftCount": len(valid),
		"warningCount":    len(warnings),
	})
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.AnalysisRunning, domain.AnalysisRunning,
		AnalysisRunPatch{Result: result, Stats: stats})
	if err != nil {
		return domain.AnalysisRun{}, nil, err
	}
	return updated, warnings, nil
}

// EnsureFallbackIssues materializes the latest done failure-analysis candidates
// as issues when the triage run failed or degraded (spec §5.6). It is
// idempotent through IssueService.ApplyDrafts' dedupe key and returns the number
// of drafted issues.
func (s *AnalysisService) EnsureFallbackIssues(ctx context.Context, versionID, batchID, actor int64) (int, error) {
	if s.deps.Runs == nil || s.deps.Issues == nil {
		return 0, errors.New("delivery: analysis fallback ports are not configured")
	}
	failure, err := s.deps.Runs.Latest(ctx, batchID, domain.AnalysisKindFailure)
	if errors.Is(err, ErrAnalysisRunNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("delivery: read failure analysis of batch %d: %w", batchID, err)
	}
	if failure.Status != domain.AnalysisDone && failure.Status != domain.AnalysisDegraded && failure.Status != domain.AnalysisFailed {
		return 0, nil
	}
	result, err := domain.DecodeFailureResult(failure.Result)
	if err != nil || len(result.Candidates) == 0 {
		return 0, nil
	}
	input, _ := domain.DecodeFailureInput(failure.Input)
	allowed := make(map[int64]int64, len(input.CaseRunIDs))
	for _, caseRunID := range input.CaseRunIDs {
		allowed[caseRunID] = 0
	}
	for _, candidate := range result.Candidates {
		if candidate.CaseRunID > 0 {
			allowed[candidate.CaseRunID] = candidate.CaseNodeID
		}
	}
	allowedEvidence, _ := s.allowedEvidenceIDs(ctx, versionID, batchID)
	valid, warnings := domain.ValidateFailureCandidates(allowed, allowedEvidence, result.Candidates)
	if len(warnings) > 0 {
		slog.WarnContext(ctx, "delivery: fallback candidates were sanitized",
			"batch_id", batchID, "warnings", len(warnings))
	}
	drafts := domain.FallbackIssueDrafts(versionID, valid)
	if len(drafts) == 0 {
		return 0, nil
	}
	if actor <= 0 {
		actor = failure.CreatedBy
	}
	issues, err := s.deps.Issues.ApplyDrafts(ctx, versionID, batchID, actor, drafts)
	if err != nil {
		return 0, fmt.Errorf("delivery: materialize fallback issues of batch %d: %w", batchID, err)
	}
	if anchorWarnings := s.createIssueAnchors(ctx, versionID, actor, drafts, issues); len(anchorWarnings) > 0 {
		slog.WarnContext(ctx, "delivery: fallback issue anchors reported warnings",
			"batch_id", batchID, "warnings", len(anchorWarnings))
	}
	return len(issues), nil
}

// runningRun loads one analysis run and requires the expected kind and a
// running status.
func (s *AnalysisService) runningRun(ctx context.Context, analysisID int64, kind string) (domain.AnalysisRun, error) {
	if s.deps.Runs == nil {
		return domain.AnalysisRun{}, errors.New("delivery: analysis store is not configured")
	}
	run, err := s.deps.Runs.GetByID(ctx, analysisID)
	if errors.Is(err, ErrAnalysisRunNotFound) {
		return domain.AnalysisRun{}, domain.NewAnalysisNotFound(analysisID)
	}
	if err != nil {
		return domain.AnalysisRun{}, err
	}
	if run.Kind != kind {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("analysis run %d is %s, not %s", analysisID, run.Kind, kind))
	}
	if run.Status != domain.AnalysisRunning {
		return domain.AnalysisRun{}, domain.NewAnalysisNotApplicable(
			fmt.Sprintf("analysis run %d is %s, not running", analysisID, run.Status))
	}
	return run, nil
}

// startRun creates the agent session and starts the run on it, then moves the
// persisted run to running. An unavailable agent runtime degrades the run so the
// autonomous chain continues (spec §9.7).
func (s *AnalysisService) startRun(ctx context.Context, run domain.AnalysisRun, profileKey, instruction string) (domain.AnalysisRun, error) {
	if s.deps.Agents == nil {
		return domain.AnalysisRun{}, errors.New("delivery: agent runtime is not configured")
	}
	sessionID, err := s.deps.Agents.CreateSession(ctx, profileKey, analysisSessionTitle(run), run.CreatedBy,
		fmt.Sprintf("version:%d", run.VersionID), strconv.FormatInt(run.VersionID, 10))
	if err != nil {
		return s.degradeRun(ctx, run, domain.ReasonAIUnavailable)
	}
	agentRunID, err := s.deps.Agents.Start(ctx, sessionID, instruction, run.CreatedBy, AnalysisAgentRunScope{
		VersionID:     run.VersionID,
		BatchID:       run.BatchID,
		AnalysisRunID: run.ID,
	})
	if err != nil {
		_ = s.deps.Agents.Abort(ctx, sessionID, run.CreatedBy)
		return s.degradeRun(ctx, run, domain.ReasonAIUnavailable)
	}
	startedAt := s.now()
	patch := AnalysisRunPatch{
		AgentSessionID: &sessionID,
		AgentRunID:     &agentRunID,
		StartedAt:      &startedAt,
	}
	if model, found := s.profileModel(ctx, profileKey); found {
		patch.Model = &model
	}
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.AnalysisRunning, patch)
	if err != nil {
		_ = s.deps.Agents.Abort(ctx, sessionID, run.CreatedBy)
		return run, fmt.Errorf("delivery: mark analysis run %d running: %w", run.ID, err)
	}
	s.publishStarted(ctx, updated)
	s.recordStarted(ctx, updated)
	return updated, nil
}

// degradeRun finishes an active run as degraded with reason, runs the §5.6
// triage fallback when needed and publishes the finished telemetry. It returns
// the degraded run and nil: the caller must not fail the batch chain because the
// LLM is unavailable.
func (s *AnalysisService) degradeRun(ctx context.Context, run domain.AnalysisRun, reason string) (domain.AnalysisRun, error) {
	finished := s.now()
	failures := analysisErrorsDocument(reason)
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.AnalysisDegraded, AnalysisRunPatch{
		Errors:     failures,
		FinishedAt: &finished,
	})
	if err != nil {
		return run, fmt.Errorf("delivery: degrade analysis run %d: %w", run.ID, err)
	}
	fallbackIssues := s.materializeTriageFallback(ctx, updated, domain.AnalysisDegraded)
	s.publishFinished(ctx, updated, 0, fallbackIssues)
	s.recordFinished(ctx, updated, 0, fallbackIssues)
	return updated, nil
}

// failRun finishes an active run as failed, runs the triage fallback when the
// failed run was the issue triage and publishes the telemetry with the fallback
// issue count.
func (s *AnalysisService) failRun(ctx context.Context, run domain.AnalysisRun, reason string) error {
	finished := s.now()
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, domain.AnalysisFailed, AnalysisRunPatch{
		Errors:     analysisErrorsDocument(reason),
		FinishedAt: &finished,
	})
	if err != nil {
		return fmt.Errorf("delivery: fail analysis run %d (%s): %w", run.ID, reason, err)
	}
	fallbackIssues := s.materializeTriageFallback(ctx, updated, domain.AnalysisFailed)
	s.publishFinished(ctx, updated, 0, fallbackIssues)
	s.recordFinished(ctx, updated, 0, fallbackIssues)
	return nil
}

// materializeTriageFallback materializes the §5.6 failure-candidate fallback of
// a non-done triage run and returns the number of issues it created. Other kinds
// and a done triage run are a no-op.
func (s *AnalysisService) materializeTriageFallback(ctx context.Context, run domain.AnalysisRun, status string) int {
	if status == domain.AnalysisDone || run.Kind != domain.AnalysisKindIssueTriage {
		return 0
	}
	issues, err := s.EnsureFallbackIssues(ctx, run.VersionID, run.BatchID, run.CreatedBy)
	if err != nil {
		slog.WarnContext(ctx, "delivery: triage fallback materialization failed",
			"batch_id", run.BatchID, "error", err)
		return 0
	}
	return issues
}

// analysisDegradedTerminalReasons is the P1 terminal-reason set that degrades an
// analysis run instead of failing it: timeouts, budget exhaustion and
// model/context failures never block the autonomous batch chain (spec §9.7).
var analysisDegradedTerminalReasons = map[string]struct{}{
	"timeout":          {},
	"run_hard_timeout": {},
	"max_turns":        {},
	"model_error":      {},
	"context_overflow": {},
}

// processRun advances one active analysis run from its agent session state. A
// queued run either advances to running (when it already carries a session) or
// is failed stale once the in-flight start grace elapsed, so a lost
// queued->running CAS never leaks the (batch, kind) active slot.
func (s *AnalysisService) processRun(ctx context.Context, run domain.AnalysisRun) error {
	switch run.Status {
	case domain.AnalysisQueued:
		return s.advanceQueued(ctx, run)
	case domain.AnalysisRunning:
	default:
		return nil
	}
	if run.AgentSessionID == nil || s.deps.Agents == nil {
		return s.failRun(ctx, run, domain.ReasonStaleRunRecovered)
	}
	session, err := s.deps.Agents.GetSession(ctx, *run.AgentSessionID)
	if err != nil {
		return fmt.Errorf("delivery: get agent session %d of analysis run %d: %w", *run.AgentSessionID, run.ID, err)
	}
	if reason := analysisDegradedTerminalReason(session); reason != "" {
		return s.finish(ctx, run, domain.AnalysisDegraded, AnalysisRunPatch{
			Stats: analysisMergeStats(run.Stats, map[string]any{"terminalReason": reason}),
		}, reason, 0, 0)
	}
	status := session.Status
	if session.LastRun != nil && session.LastRun.Status != "" {
		status = session.LastRun.Status
	}
	switch status {
	case "done", "completed", "succeeded":
		return s.materialize(ctx, run)
	case "failed", "aborted", "interrupted":
		return s.failRun(ctx, run, "agent_"+status)
	default:
		return nil
	}
}

// advanceQueued moves a queued run forward: a run that already carries an agent
// session is flipped to running so the next tick polls it; a queued run without
// a session is failed stale after the in-flight start grace period, which
// releases the unique active slot.
func (s *AnalysisService) advanceQueued(ctx context.Context, run domain.AnalysisRun) error {
	if run.AgentSessionID == nil || s.deps.Agents == nil {
		if s.now().Sub(run.CreatedAt) < analysisQueuedStaleAfter {
			return nil
		}
		return s.failRun(ctx, run, domain.ReasonStaleRunRecovered)
	}
	startedAt := s.now()
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, domain.AnalysisQueued, domain.AnalysisRunning,
		AnalysisRunPatch{StartedAt: &startedAt})
	if err != nil {
		if errors.Is(err, ErrAnalysisStateConflict) {
			return nil
		}
		return fmt.Errorf("delivery: advance queued analysis run %d: %w", run.ID, err)
	}
	s.publishStarted(ctx, updated)
	s.recordStarted(ctx, updated)
	return nil
}

// analysisDegradedTerminalReason returns the P1 terminal reason that must
// degrade the run (spec §9.7), or "".
func analysisDegradedTerminalReason(session AnalysisAgentSessionStatus) string {
	if session.LastRun == nil {
		return ""
	}
	reason := strings.ToLower(strings.TrimSpace(session.LastRun.TerminalReason))
	if _, degrade := analysisDegradedTerminalReasons[reason]; degrade {
		return reason
	}
	return ""
}

// materialize validates the stored submission and finishes the run.
func (s *AnalysisService) materialize(ctx context.Context, run domain.AnalysisRun) error {
	switch run.Kind {
	case domain.AnalysisKindFailure:
		return s.materializeFailure(ctx, run)
	case domain.AnalysisKindIssueTriage:
		return s.materializeTriage(ctx, run)
	default:
		return s.failRun(ctx, run, domain.ReasonMaterializeFailed)
	}
}

// materializeFailure re-validates the submitted candidates (shape, enum and
// proof rules) and finishes the run done, or degraded when nothing valid was
// submitted.
func (s *AnalysisService) materializeFailure(ctx context.Context, run domain.AnalysisRun) error {
	result, err := domain.DecodeFailureResult(run.Result)
	if err != nil {
		return s.finish(ctx, run, domain.AnalysisDegraded, AnalysisRunPatch{}, domain.ReasonNoCandidates, 0, 0)
	}
	input, _ := domain.DecodeFailureInput(run.Input)
	allowed := make(map[int64]int64, len(input.CaseRunIDs))
	for _, caseRunID := range input.CaseRunIDs {
		allowed[caseRunID] = 0
	}
	for _, candidate := range result.Candidates {
		if candidate.CaseRunID > 0 {
			allowed[candidate.CaseRunID] = candidate.CaseNodeID
		}
	}
	allowedEvidence, _ := s.allowedEvidenceIDs(ctx, run.VersionID, run.BatchID)
	valid, warnings := domain.ValidateFailureCandidates(allowed, allowedEvidence, result.Candidates)
	warnings = append(result.Warnings, warnings...)
	patch := AnalysisRunPatch{
		Result: domain.EncodeFailureResult(valid, warnings),
		Stats: analysisMergeStats(run.Stats, map[string]any{
			"candidateCount": len(valid),
			"warningCount":   len(warnings),
		}),
	}
	if len(valid) == 0 {
		return s.finish(ctx, run, domain.AnalysisDegraded, patch, domain.ReasonNoCandidates, 0, 0)
	}
	return s.finish(ctx, run, domain.AnalysisDone, patch, "", len(valid), 0)
}

// materializeTriage validates the drafts, materializes them through IssueWriter
// and finishes the run. A failed materialization degrades the run and falls back
// to the failure candidates; issue anchors are best-effort.
func (s *AnalysisService) materializeTriage(ctx context.Context, run domain.AnalysisRun) error {
	result, err := domain.DecodeTriageResult(run.Result)
	if err != nil {
		return s.finish(ctx, run, domain.AnalysisDegraded, AnalysisRunPatch{}, domain.ReasonNoCandidates, 0, 0)
	}
	allowedEvidence, _ := s.allowedEvidenceIDs(ctx, run.VersionID, run.BatchID)
	valid, warnings := domain.ValidateIssueDrafts(result.Drafts, allowedEvidence)
	warnings = append(result.Warnings, warnings...)
	valid, mergeWarnings := s.filterMergeTargets(ctx, run.VersionID, valid)
	warnings = append(warnings, mergeWarnings...)
	if len(valid) == 0 {
		patch := AnalysisRunPatch{Result: analysisMustEncodeTriage(valid, warnings)}
		return s.finish(ctx, run, domain.AnalysisDegraded, patch, domain.ReasonNoCandidates, 0, 0)
	}
	if s.deps.Issues == nil {
		return s.finish(ctx, run, domain.AnalysisDegraded, AnalysisRunPatch{}, domain.ReasonMaterializeFailed, 0, 0)
	}
	issues, err := s.deps.Issues.ApplyDrafts(ctx, run.VersionID, run.BatchID, run.CreatedBy, valid)
	if err != nil {
		warnings = append(warnings, "issue materialization failed")
		patch := AnalysisRunPatch{Result: analysisMustEncodeTriage(valid, warnings)}
		return s.finish(ctx, run, domain.AnalysisFailed, patch, domain.ReasonMaterializeFailed, 0, 0)
	}
	warnings = append(warnings, s.createIssueAnchors(ctx, run.VersionID, run.CreatedBy, valid, issues)...)
	patch := AnalysisRunPatch{
		Result: analysisMustEncodeTriage(valid, warnings),
		Stats: analysisMergeStats(run.Stats, map[string]any{
			"issueCount":      len(issues),
			"warningCount":    len(warnings),
			"issueDraftCount": len(valid),
		}),
	}
	return s.finish(ctx, run, domain.AnalysisDone, patch, "", 0, len(issues))
}

// finish applies a terminal status through the status CAS, runs the §5.6
// fallback for a non-done triage run and publishes the finished telemetry with
// the fallback issue count included.
func (s *AnalysisService) finish(ctx context.Context, run domain.AnalysisRun, status string, patch AnalysisRunPatch, reason string, candidateCount, issueCount int) error {
	finished := s.now()
	patch.FinishedAt = &finished
	if reason != "" {
		patch.Errors = analysisErrorsDocument(reason)
	}
	updated, err := s.deps.Runs.UpdateStatus(ctx, run.ID, run.Status, status, patch)
	if err != nil {
		return fmt.Errorf("delivery: finish analysis run %d as %s: %w", run.ID, status, err)
	}
	fallbackIssues := s.materializeTriageFallback(ctx, updated, status)
	s.publishFinished(ctx, updated, candidateCount, issueCount+fallbackIssues)
	s.recordFinished(ctx, updated, candidateCount, issueCount+fallbackIssues)
	return nil
}

// createIssueAnchors creates the §5.4 issue code anchors best-effort.
// ApplyDrafts returns unique issues (several drafts may merge into one row), so a
// draft is mapped to its issue by the explicit mergeIntoIssueId, otherwise by
// its dedupe key. An unmapped draft yields a warning and its refs are never
// attached to another issue.
func (s *AnalysisService) createIssueAnchors(ctx context.Context, versionID, actor int64, drafts []domain.IssueDraft, issues []domain.Issue) []string {
	if s.deps.Anchors == nil || len(issues) == 0 || len(drafts) == 0 {
		return nil
	}
	issuesByID := make(map[int64]domain.Issue, len(issues))
	issuesByDedupeKey := make(map[string]domain.Issue, len(issues))
	for _, issue := range issues {
		issuesByID[issue.ID] = issue
		if issue.DedupeKey != "" {
			issuesByDedupeKey[issue.DedupeKey] = issue
		}
	}
	order := make([]int64, 0, len(drafts))
	refsByIssueID := make(map[int64][]domain.CodeRef, len(drafts))
	warnings := make([]string, 0)
	for index, draft := range drafts {
		if len(draft.CodeRefs) == 0 {
			continue
		}
		issue, mapped := analysisDraftIssue(draft, issuesByID, issuesByDedupeKey)
		if !mapped {
			warnings = append(warnings, fmt.Sprintf("draft %d: materialized issue not found, code anchors skipped", index+1))
			continue
		}
		if _, seen := refsByIssueID[issue.ID]; !seen {
			order = append(order, issue.ID)
		}
		refsByIssueID[issue.ID] = append(refsByIssueID[issue.ID], draft.CodeRefs...)
	}
	for _, issueID := range order {
		refs := analysisUniqueCodeRefs(refsByIssueID[issueID])
		if len(refs) == 0 {
			continue
		}
		anchorWarnings, err := s.deps.Anchors.CreateIssueAnchors(ctx, versionID, issueID, actor, refs)
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("issue %d: code anchor creation failed", issueID))
			continue
		}
		warnings = append(warnings, anchorWarnings...)
	}
	return warnings
}

// analysisDraftIssue maps one materialized draft to the issue row ApplyDrafts
// returned for it: the explicit merge target when set, otherwise the hard
// (version, dedupe_key) match of spec §5.4.
func analysisDraftIssue(draft domain.IssueDraft, issuesByID map[int64]domain.Issue, issuesByDedupeKey map[string]domain.Issue) (domain.Issue, bool) {
	if draft.MergeIntoIssueID != nil {
		issue, found := issuesByID[*draft.MergeIntoIssueID]
		return issue, found
	}
	if draft.DedupeKey == "" {
		return domain.Issue{}, false
	}
	issue, found := issuesByDedupeKey[draft.DedupeKey]
	return issue, found
}

// analysisUniqueCodeRefs removes exact duplicate refs so a merged draft does not
// create the same anchor twice.
func analysisUniqueCodeRefs(refs []domain.CodeRef) []domain.CodeRef {
	if len(refs) <= 1 {
		return refs
	}
	seen := make(map[domain.CodeRef]struct{}, len(refs))
	unique := make([]domain.CodeRef, 0, len(refs))
	for _, ref := range refs {
		if _, duplicate := seen[ref]; duplicate {
			continue
		}
		seen[ref] = struct{}{}
		unique = append(unique, ref)
	}
	return unique
}

// allowedEvidenceIDs collects the evidence ids of the failed application case
// runs of one batch (spec §5.6/§9.3): candidates and drafts may only cite that
// evidence. found reports whether the ownership set could be read; when it could
// not, the returned nil map tells the domain validators to skip the whitelist.
func (s *AnalysisService) allowedEvidenceIDs(ctx context.Context, versionID, batchID int64) (map[int64]struct{}, bool) {
	if s.deps.Execution == nil {
		return nil, false
	}
	cases, _, err := s.listFailedApplicationCases(ctx, versionID, batchID, s.maxFailureCases())
	if err != nil {
		slog.WarnContext(ctx, "delivery: list failed cases for evidence validation failed",
			"version_id", versionID, "batch_id", batchID, "error", err)
		return nil, false
	}
	allowed := make(map[int64]struct{})
	for _, failedCase := range cases {
		for _, evidenceID := range failedCase.EvidenceIDs {
			if evidenceID > 0 {
				allowed[evidenceID] = struct{}{}
			}
		}
	}
	return allowed, true
}

// maxFailureCases returns the configured per-run failed-case budget, defaulting
// to the spec default when the composition root did not wire one.
func (s *AnalysisService) maxFailureCases() int {
	if s.deps.MaxCases > 0 {
		return s.deps.MaxCases
	}
	return domain.AnalysisMaxFailureCandidates
}

// listFailedApplicationCases reads the failed(application) case runs of one
// batch bounded by max (design spec §10.5/§11.4). It filters malformed/other
// classes before applying the budget and reports whether the batch overflowed
// it. The extra read row tells truncation apart from an exact fit.
func (s *AnalysisService) listFailedApplicationCases(ctx context.Context, versionID, batchID int64, max int) ([]AnalysisFailedCase, bool, error) {
	if s.deps.Execution == nil {
		return nil, false, errors.New("delivery: execution reader is not configured")
	}
	cases, err := s.deps.Execution.ListFailedCases(ctx, versionID, batchID, max+1)
	if err != nil {
		return nil, false, err
	}
	filtered := make([]AnalysisFailedCase, 0, len(cases))
	for _, failedCase := range cases {
		if failedCase.FailureClass != domain.FailureClassApplication ||
			failedCase.CaseRunID <= 0 || failedCase.CaseNodeID <= 0 {
			continue
		}
		filtered = append(filtered, failedCase)
	}
	truncated := len(filtered) > max
	if truncated {
		filtered = filtered[:max]
	}
	return filtered, truncated, nil
}

// filterMergeTargets enforces the open-only mergeIntoIssueId rule of spec §9.4:
// an explicit target that is not an open issue of the version is stripped with a
// warning and the draft materializes on its own dedupe key instead.
func (s *AnalysisService) filterMergeTargets(ctx context.Context, versionID int64, drafts []domain.IssueDraft) ([]domain.IssueDraft, []string) {
	needsValidation := false
	for _, draft := range drafts {
		if draft.MergeIntoIssueID != nil {
			needsValidation = true
			break
		}
	}
	if !needsValidation {
		return drafts, nil
	}
	openIssues, known := s.openIssueIDs(ctx, versionID)
	warnings := make([]string, 0)
	filtered := make([]domain.IssueDraft, 0, len(drafts))
	for index, draft := range drafts {
		if draft.MergeIntoIssueID == nil {
			filtered = append(filtered, draft)
			continue
		}
		target := *draft.MergeIntoIssueID
		if known {
			if _, open := openIssues[target]; open {
				filtered = append(filtered, draft)
				continue
			}
		}
		draft.MergeIntoIssueID = nil
		warnings = append(warnings, fmt.Sprintf(
			"draft %d: mergeIntoIssueId %d is not an open issue of the version; the draft keeps its own dedupe key", index+1, target))
		filtered = append(filtered, draft)
	}
	return filtered, warnings
}

// openIssueIDs loads the open issue ids of the version used to validate explicit
// merge targets. A nil/unavailable reader is treated as "cannot prove open", so
// every explicit target is stripped (fail closed); merging into a confirmed or
// dismissed issue is forbidden by spec §9.4.
func (s *AnalysisService) openIssueIDs(ctx context.Context, versionID int64) (map[int64]struct{}, bool) {
	if s.deps.OpenIssues == nil {
		return nil, false
	}
	issues, err := s.deps.OpenIssues.ListOpen(ctx, versionID, analysisMaxOpenIssueScan)
	if err != nil {
		slog.WarnContext(ctx, "delivery: list open issues for merge validation failed",
			"version_id", versionID, "error", err)
		return nil, false
	}
	open := make(map[int64]struct{}, len(issues))
	for _, issue := range issues {
		if issue.IssueID > 0 {
			open[issue.IssueID] = struct{}{}
		}
	}
	return open, true
}

// profileModel reads the model of the agent profile seed (spec §7.5). An
// unwired reader or a read failure leaves the run model empty.
func (s *AnalysisService) profileModel(ctx context.Context, profileKey string) (string, bool) {
	if s.deps.Profiles == nil {
		return "", false
	}
	model, err := s.deps.Profiles.ProfileModel(ctx, profileKey)
	if err != nil {
		slog.WarnContext(ctx, "delivery: read agent profile model failed",
			"profile", profileKey, "error", err)
		return "", false
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	return model, true
}

// openIssues loads the open issue summaries for a triage input. A read failure
// is logged and degraded to an empty list: the triage tools can still read the
// open issues, and a failed list must not block the autonomous chain.
func (s *AnalysisService) openIssues(ctx context.Context, versionID int64) []domain.OpenIssue {
	if s.deps.OpenIssues == nil {
		return nil
	}
	issues, err := s.deps.OpenIssues.ListOpen(ctx, versionID, analysisMaxOpenIssues)
	if err != nil {
		slog.WarnContext(ctx, "delivery: list open issues for triage failed",
			"version_id", versionID, "error", err)
		return nil
	}
	return issues
}

// requireFailurePorts validates the ports StartFailure cannot work without.
func (s *AnalysisService) requireFailurePorts() error {
	if s.deps.Runs == nil {
		return errors.New("delivery: analysis store is not configured")
	}
	if s.deps.Execution == nil {
		return errors.New("delivery: execution reader is not configured")
	}
	if s.deps.Agents == nil {
		return errors.New("delivery: agent runtime is not configured")
	}
	return nil
}

// requireTriagePorts validates the ports StartTriage cannot work without.
func (s *AnalysisService) requireTriagePorts() error {
	if s.deps.Runs == nil {
		return errors.New("delivery: analysis store is not configured")
	}
	if s.deps.Issues == nil {
		return errors.New("delivery: issue writer is not configured")
	}
	if s.deps.Agents == nil {
		return errors.New("delivery: agent runtime is not configured")
	}
	return nil
}

// publishStarted emits analysis.started with ids/kind only.
func (s *AnalysisService) publishStarted(ctx context.Context, run domain.AnalysisRun) {
	s.publish(ctx, AnalysisEvent{
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		Type:      domain.EventAnalysisStarted,
		Summary:   run.Kind + " analysis started",
		Detail: map[string]any{
			"analysisId": strconv.FormatInt(run.ID, 10),
			"kind":       run.Kind,
			"batchId":    strconv.FormatInt(run.BatchID, 10),
		},
	})
}

// publishFinished emits analysis.finished with status and counts only.
func (s *AnalysisService) publishFinished(ctx context.Context, run domain.AnalysisRun, candidateCount, issueCount int) {
	s.publish(ctx, AnalysisEvent{
		VersionID: run.VersionID,
		BatchID:   run.BatchID,
		Type:      domain.EventAnalysisFinished,
		Summary:   fmt.Sprintf("%s analysis %s", run.Kind, run.Status),
		Detail: map[string]any{
			"analysisId":     strconv.FormatInt(run.ID, 10),
			"kind":           run.Kind,
			"batchId":        strconv.FormatInt(run.BatchID, 10),
			"status":         run.Status,
			"candidateCount": candidateCount,
			"issueCount":     issueCount,
			"degraded":       run.Status != domain.AnalysisDone,
		},
	})
}

// publish sends one event; a nil publisher or a failure is only logged.
func (s *AnalysisService) publish(ctx context.Context, event AnalysisEvent) {
	if s.deps.Events == nil {
		return
	}
	if err := s.deps.Events.Publish(ctx, event); err != nil {
		slog.WarnContext(ctx, "delivery: analysis event publish failed", "type", event.Type, "error", err)
	}
}

// recordStarted records delivery.analysis.started (ids/kind/status only).
func (s *AnalysisService) recordStarted(ctx context.Context, run domain.AnalysisRun) {
	s.record(ctx, AnalysisAuditEvent{
		Action:     "delivery.analysis.started",
		ActorID:    run.CreatedBy,
		TargetType: "analysis_run",
		TargetID:   strconv.FormatInt(run.ID, 10),
		Metadata: map[string]any{
			"analysisId": strconv.FormatInt(run.ID, 10),
			"batchId":    strconv.FormatInt(run.BatchID, 10),
			"kind":       run.Kind,
			"status":     run.Status,
		},
	})
}

// recordFinished records delivery.analysis.finished (ids/status/counts/duration
// only; never a prompt or result body). The counts include the §5.6 fallback
// issues, which is why the caller passes the numbers instead of re-deriving them
// from the run documents.
func (s *AnalysisService) recordFinished(ctx context.Context, run domain.AnalysisRun, candidateCount, issueCount int) {
	s.record(ctx, AnalysisAuditEvent{
		Action:     "delivery.analysis.finished",
		ActorID:    run.CreatedBy,
		TargetType: "analysis_run",
		TargetID:   strconv.FormatInt(run.ID, 10),
		Metadata: map[string]any{
			"analysisId":     strconv.FormatInt(run.ID, 10),
			"batchId":        strconv.FormatInt(run.BatchID, 10),
			"kind":           run.Kind,
			"status":         run.Status,
			"candidateCount": candidateCount,
			"issueCount":     issueCount,
			"durationMs":     analysisDurationMS(run, s.now()),
		},
	})
}

// record sends one audit entry; a nil recorder or a failure is only logged.
func (s *AnalysisService) record(ctx context.Context, event AnalysisAuditEvent) {
	if s.deps.Audit == nil {
		return
	}
	if err := s.deps.Audit.Record(ctx, event); err != nil {
		slog.WarnContext(ctx, "delivery: analysis audit failed", "action", event.Action, "error", err)
	}
}

// nextID mints one Snowflake id (D29).
func (s *AnalysisService) nextID() (int64, error) {
	if s.deps.IDs == nil {
		return 0, errors.New("delivery: id generator is not configured")
	}
	id := s.deps.IDs.Next()
	if id <= 0 {
		return 0, errors.New("delivery: id generator returned a non-positive id")
	}
	return id, nil
}

// now returns the injected clock time.
func (s *AnalysisService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now()
	}
	return time.Now()
}

// analysisDurationMS is the run duration used by the audit entry.
func analysisDurationMS(run domain.AnalysisRun, now time.Time) int64 {
	start := run.CreatedAt
	if run.StartedAt != nil {
		start = *run.StartedAt
	}
	if start.IsZero() || now.Before(start) {
		return 0
	}
	return now.Sub(start).Milliseconds()
}

// analysisErrorsDocument builds a short errors document. It never carries the
// underlying error text: that could echo evidence or prompts.
func analysisErrorsDocument(reason string) json.RawMessage {
	return analysisMustMarshal(map[string]any{"reason": reason})
}

// analysisMustMarshal encodes value and degrades to an empty object instead of
// failing the run on a programming error.
func analysisMustMarshal(value any) json.RawMessage {
	encoded, err := json.Marshal(value)
	if err != nil {
		return json.RawMessage(`{}`)
	}
	return encoded
}

// analysisMergeStats merges update into an existing stats document.
func analysisMergeStats(raw json.RawMessage, update map[string]any) json.RawMessage {
	document := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &document)
	}
	for key, value := range update {
		document[key] = value
	}
	return analysisMustMarshal(document)
}

// analysisMustEncodeTriage encodes drafts, degrading to an empty document on the
// impossible marshal error.
func analysisMustEncodeTriage(drafts []domain.IssueDraft, warnings []string) json.RawMessage {
	encoded, err := domain.EncodeTriageResult(drafts, warnings)
	if err != nil {
		return json.RawMessage(`{"issues":[]}`)
	}
	return encoded
}

// analysisSessionTitle is the P1 session list title of one analysis run.
func analysisSessionTitle(run domain.AnalysisRun) string {
	switch run.Kind {
	case domain.AnalysisKindIssueTriage:
		return fmt.Sprintf("batch %d · issue triage", run.BatchID)
	default:
		return fmt.Sprintf("batch %d · failure analysis", run.BatchID)
	}
}

// failureAnalysisInstruction builds the failure-analysis agent instruction. It
// names the run scope and ids only: no evidence, prompt or case body is copied.
func failureAnalysisInstruction(run domain.AnalysisRun, caseRunIDs []int64) string {
	var builder strings.Builder
	builder.WriteString("## Task\n")
	builder.WriteString("You are the failure-analysis agent. Analyze only the failed application cases of this batch.\n")
	builder.WriteString("Every root cause, confidence and rerun suggestion must be explainable; never report an environment, session, model or case error as an application defect.\n")
	builder.WriteString("Read context with analysis.case_list / analysis.case_detail / analysis.evidence_meta / analysis.code_read first.\n")
	builder.WriteString("Each candidate needs at least one evidence id or code reference and failureClass=application.\n")
	builder.WriteString("Submit with analysis.candidate_propose (at most 20 candidates overall) and finish with analysis.finish.\n\n")
	builder.WriteString("## Scope\n")
	fmt.Fprintf(&builder, "versionId=%d\nbatchId=%d\nanalysisRunId=%d\n", run.VersionID, run.BatchID, run.ID)
	fmt.Fprintf(&builder, "caseRunIds=%s\n", analysisJoinIDs(caseRunIDs))
	return builder.String()
}

// triageInstruction builds the issue-triage agent instruction. It names ids and
// the tools only; candidate prose stays in the run result.
func triageInstruction(run domain.AnalysisRun, candidateCount int) string {
	var builder strings.Builder
	builder.WriteString("## Task\n")
	builder.WriteString("You are the issue-triage agent. Turn the failure-analysis candidates into reproducible issue drafts.\n")
	builder.WriteString("Merge candidates with the same root cause into one issue; never merge different failure classes and never file environment, session or model defects.\n")
	builder.WriteString("Read context with triage.candidate_list / triage.issue_list / triage.context_read, then submit drafts with triage.issue_draft (at most 20) and finish with triage.finish.\n")
	builder.WriteString("Every draft must be reproducible, name expected and actual behavior, and keep severity/confidence conservative. Never delete or rewrite a confirmed issue.\n\n")
	builder.WriteString("## Scope\n")
	fmt.Fprintf(&builder, "versionId=%d\nbatchId=%d\nanalysisRunId=%d\nfailureCandidateCount=%d\n", run.VersionID, run.BatchID, run.ID, candidateCount)
	return builder.String()
}

// analysisJoinIDs renders a decimal id list for an instruction.
func analysisJoinIDs(ids []int64) string {
	if len(ids) == 0 {
		return ""
	}
	parts := make([]string, 0, len(ids))
	for _, id := range ids {
		parts = append(parts, strconv.FormatInt(id, 10))
	}
	return strings.Join(parts, ",")
}
