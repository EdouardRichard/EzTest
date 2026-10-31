package executionapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// AnalysisService implements the deterministic failure/coverage fallback of
// P3 §8.1 plus the analyst submission entry points. It never creates P4 tables:
// results are case_run.failure_class/summary, events, team-room posts and
// design.gap work items.
type AnalysisService struct {
	waves        WaveRepository
	caseRuns     CaseRunRepository
	results      ResultRepository
	cases        CaseReader
	requirements CoverageReader
	gaps         GapEnsurer
	teamRoom     TeamRoom
	workQueue    WorkQueue
	settings     SettingsReader
	clock        Clock
	events       EventPublisher
	audit        AuditRecorder

	mu           sync.Mutex
	candidates   map[string]struct{}
	coverageKeys map[string]struct{}
}

// AnalysisServiceDeps wires the analysis service. gaps/teamRoom/settings/audit
// may be nil in degraded wiring.
type AnalysisServiceDeps struct {
	Waves        WaveRepository
	CaseRuns     CaseRunRepository
	Results      ResultRepository
	Cases        CaseReader
	Requirements CoverageReader
	Gaps         GapEnsurer
	TeamRoom     TeamRoom
	WorkQueue    WorkQueue
	Settings     SettingsReader
	Clock        Clock
	Events       EventPublisher
	Audit        AuditRecorder
}

// NewAnalysisService builds the analysis service.
func NewAnalysisService(deps AnalysisServiceDeps) *AnalysisService {
	return &AnalysisService{
		waves:        deps.Waves,
		caseRuns:     deps.CaseRuns,
		results:      deps.Results,
		cases:        deps.Cases,
		requirements: deps.Requirements,
		gaps:         deps.Gaps,
		teamRoom:     deps.TeamRoom,
		workQueue:    deps.WorkQueue,
		settings:     deps.Settings,
		clock:        deps.Clock,
		events:       deps.Events,
		audit:        deps.Audit,
		candidates:   map[string]struct{}{},
		coverageKeys: map[string]struct{}{},
	}
}

// AnalyzeWave is the deterministic fallback: classify every failed/blocked/
// inconclusive case_run, emit application failure candidates, compute execution
// coverage gaps and ensure the analyst work items. It runs before the agents and
// is safe to call again (the work queue and the in-process dedupe make it
// idempotent).
func (s *AnalysisService) AnalyzeWave(ctx context.Context, waveID int64) error {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return err
	}
	wave, err := s.waves.Get(ctx, waveID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, waveID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	for _, run := range runs {
		if !failureRelevant(run.Status) {
			continue
		}
		if err := s.classifyRun(ctx, wave, run); err != nil {
			return err
		}
	}
	if err := s.analyzeCoverage(ctx, wave, runs); err != nil {
		return err
	}
	return s.EnsureTasks(ctx, waveID)
}

// EnsureTasks ensures the analyze.failure/analyze.coverage work items of a
// terminal wave. The work queue dedupe keys make repeated calls idempotent.
func (s *AnalysisService) EnsureTasks(ctx context.Context, waveID int64) error {
	if err := requirePositiveID("wave_id", waveID); err != nil {
		return err
	}
	wave, err := s.waves.Get(ctx, waveID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	runs, err := s.caseRuns.ListAllByWave(ctx, waveID)
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	failedIDs := make([]string, 0)
	for _, run := range runs {
		if failureRelevant(run.Status) {
			failedIDs = append(failedIDs, platformid.Format(run.ID))
		}
	}
	if s.workQueue == nil {
		return nil
	}
	if _, _, err := s.workQueue.Ensure(ctx, WorkItemInput{
		VersionID: wave.VersionID,
		Type:      WorkTypeAnalyzeFailure,
		Team:      TeamExecution,
		DedupeKey: "failure:" + platformid.Format(waveID),
		Payload: map[string]any{
			"wave_id":      platformid.Format(waveID),
			"case_run_ids": failedIDs,
		},
	}); err != nil {
		return mapExecutionError(err, "work item")
	}
	if _, _, err := s.workQueue.Ensure(ctx, WorkItemInput{
		VersionID: wave.VersionID,
		Type:      WorkTypeAnalyzeCoverage,
		Team:      TeamExecution,
		DedupeKey: "coverage:" + platformid.Format(waveID),
		Payload:   map[string]any{"wave_id": platformid.Format(waveID)},
	}); err != nil {
		return mapExecutionError(err, "work item")
	}
	return nil
}

// classifyRun persists the deterministic failure class/summary of one finished
// failed/blocked/inconclusive case_run.
func (s *AnalysisService) classifyRun(ctx context.Context, wave execution.RunWave, run execution.CaseRun) error {
	steps, err := s.results.ListSteps(ctx, run.ID)
	if err != nil {
		return mapExecutionError(err, "step result")
	}
	assertions, err := s.results.ListAssertions(ctx, run.ID)
	if err != nil {
		return mapExecutionError(err, "assertion result")
	}
	status, class, summary := deterministicResult(run, steps, assertions)
	if status == run.Status && class == run.FailureClass && summaryEqual(summary, run.Summary) {
		return nil
	}
	run.Status = status
	run.FailureClass = class
	run.Summary = summary
	if run.FinishedAt == nil {
		now := s.now()
		run.FinishedAt = &now
	}
	run.UpdatedAt = s.now()
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return mapExecutionError(err, "case run")
	}
	if class.IsIssueCandidate() {
		return s.emitFailureCandidate(ctx, wave, run)
	}
	return nil
}

// deterministicResult is the §4.3/§4.5 server-side classification. The agent
// candidate may only refine within the deterministic result.
func deterministicResult(run execution.CaseRun, steps []execution.StepResult, assertions []execution.AssertionResult) (execution.CaseRunStatus, execution.FailureClass, execution.CaseRunSummary) {
	summary := run.Summary
	summary.StepTotal = len(steps)
	summary.AssertionTotal = len(assertions)
	summary.StepFailed = 0
	summary.AssertionFailed = 0
	var failedAssertion *execution.AssertionResult
	var failedStep *execution.StepResult
	var blockedStep *execution.StepResult
	for index := range steps {
		switch steps[index].Status {
		case execution.StepFailed:
			summary.StepFailed++
			if failedStep == nil {
				failedStep = &steps[index]
			}
		case execution.StepBlocked:
			if blockedStep == nil {
				blockedStep = &steps[index]
			}
		}
	}
	for index := range assertions {
		if assertions[index].Verdict == execution.AssertionFailed {
			summary.AssertionFailed++
			if failedAssertion == nil {
				failedAssertion = &assertions[index]
			}
		}
	}

	if blockedStep != nil || run.Status == execution.CaseRunBlocked {
		summary.VerdictReason = execution.VerdictReasonBlocked
		return execution.CaseRunBlocked, execution.FailureClassBlocked, summary
	}
	if failedAssertion != nil {
		class := execution.FailureClassApplication
		if run.FailureClass == execution.FailureClassAutomation {
			class = execution.FailureClassAutomation
		}
		summary.VerdictReason = execution.VerdictReasonAssertionFailed
		return execution.CaseRunFailed, class, summary
	}
	if failedStep != nil {
		class := execution.FailureClassAutomation
		switch run.FailureClass {
		case "", execution.FailureClassApplication, execution.FailureClassBlocked, execution.FailureClassExecutorLost:
			// keep automation
		default:
			if run.FailureClass.Valid() {
				class = run.FailureClass
			}
		}
		summary.VerdictReason = execution.VerdictReasonStepFailed
		return execution.CaseRunFailed, class, summary
	}
	if run.Status == execution.CaseRunInconclusive || run.Status == execution.CaseRunFailed {
		// No failing result: a claimed application failure has no evidence and
		// must not produce an issue candidate (P3-D-12).
		if run.FailureClass == execution.FailureClassApplication || run.Status == execution.CaseRunInconclusive {
			summary.VerdictReason = execution.VerdictReasonIncomplete
			return execution.CaseRunInconclusive, "", summary
		}
		if run.FailureClass.Valid() && run.FailureClass != "" {
			return execution.CaseRunFailed, run.FailureClass, summary
		}
		summary.VerdictReason = execution.VerdictReasonIncomplete
		return execution.CaseRunInconclusive, "", summary
	}
	summary.VerdictReason = execution.VerdictReasonIncomplete
	return execution.CaseRunInconclusive, "", summary
}

// emitFailureCandidate publishes the application failure candidate event and
// team-room post. Only failed(application) with assertion evidence is emitted.
func (s *AnalysisService) emitFailureCandidate(ctx context.Context, wave execution.RunWave, run execution.CaseRun) error {
	steps, _ := s.results.ListSteps(ctx, run.ID)
	assertions, _ := s.results.ListAssertions(ctx, run.ID)
	var failedAssertion *execution.AssertionResult
	for index := range assertions {
		if assertions[index].Verdict == execution.AssertionFailed {
			failedAssertion = &assertions[index]
			break
		}
	}
	if failedAssertion == nil {
		return nil
	}
	hint := analysisCandidateHint(wave.VersionID, run.CaseVersionID, failedAssertion.StepSeq, failedAssertion.AssertionSeq, run.FailureClass)
	s.mu.Lock()
	if _, seen := s.candidates[hint]; seen {
		s.mu.Unlock()
		return nil
	}
	s.candidates[hint] = struct{}{}
	s.mu.Unlock()

	priority := s.casePriority(ctx, wave.VersionID, run.CaseVersionID)
	category := failureCategory(failedAssertion.Kind)
	severity := failureSeverity(priority)
	payload := map[string]any{
		"wave_id":       platformid.Format(wave.ID),
		"case_run_id":   platformid.Format(run.ID),
		"failure_class": string(execution.FailureClassApplication),
		"category":      category,
		"severity":      severity,
		"dedupe_hint":   hint,
	}
	publishEvent(ctx, s.events, EventFailureCandidate, wave.VersionID,
		map[string]any{
			"wave_id":     platformid.Format(wave.ID),
			"case_run_id": platformid.Format(run.ID),
		}, payload, nil)
	s.post(ctx, TeamPost{
		VersionID: wave.VersionID,
		Team:      TeamExecution,
		Kind:      "finding",
		Content: fmt.Sprintf("failed(application) candidate: case_run %s failed %s assertion %d.%d",
			platformid.Format(run.ID), failedAssertion.Kind, failedAssertion.StepSeq, failedAssertion.AssertionSeq),
		Refs: []TeamPostRef{
			{Kind: "case_run", ID: platformid.Format(run.ID)},
			{Kind: "wave", ID: platformid.Format(wave.ID)},
		},
	})
	_ = steps
	return nil
}

// FailureSubmit is the analysis.failure_submit entry point (ExecutionPort). It
// may only keep or downgrade an application classification; escalating a
// non-application failure is rejected.
func (s *AnalysisService) FailureSubmit(ctx context.Context, scope tools.Scope, in tools.FailureSubmitInput) (any, error) {
	return s.SubmitFailure(ctx, scope, in)
}

// SubmitFailure refines one case_run failure classification inside the wave
// scope of the analyst task (P3 §8.2).
func (s *AnalysisService) SubmitFailure(ctx context.Context, scope tools.Scope, in tools.FailureSubmitInput) (any, error) {
	if err := requirePositiveID("case_run_id", in.CaseRunID); err != nil {
		return nil, err
	}
	run, err := s.caseRuns.Get(ctx, in.CaseRunID)
	if err != nil {
		return nil, mapExecutionError(err, "case run")
	}
	if scope.CaseRunID > 0 && scope.CaseRunID != run.ID {
		return nil, platform.NotFound("case run")
	}
	if scope.WaveID > 0 && run.WaveID != scope.WaveID {
		return nil, platform.NotFound("case run")
	}
	requested := execution.FailureClass(strings.TrimSpace(in.FailureClass))
	if !requested.Valid() || requested == "" {
		return nil, platform.Invalid("failure_class", "must be one of application, environment, session, model, automation, timeout, executor_lost, blocked, unknown")
	}
	if run.FailureClass != execution.FailureClassApplication && requested == execution.FailureClassApplication {
		return nil, platform.StateConflict("a non-application failure cannot be escalated to application")
	}
	if !failureRelevant(run.Status) {
		return nil, platform.StateConflict("only failed, blocked or inconclusive case runs can be refined")
	}
	if requested == execution.FailureClassApplication {
		assertions, err := s.results.ListAssertions(ctx, run.ID)
		if err != nil {
			return nil, mapExecutionError(err, "assertion result")
		}
		hasEvidence := false
		for _, assertion := range assertions {
			if assertion.Verdict == execution.AssertionFailed {
				hasEvidence = true
				break
			}
		}
		if !hasEvidence {
			return nil, platform.StateConflict("failed(application) requires assertion evidence")
		}
	}
	run.FailureClass = requested
	if summary := strings.TrimSpace(in.Summary); summary != "" {
		if len(summary) > 2000 {
			summary = summary[:2000]
		}
		run.Summary.LastError = summary
	}
	run.UpdatedAt = s.now()
	if _, err := s.caseRuns.Update(ctx, run); err != nil {
		return nil, mapExecutionError(err, "case run")
	}
	wave, err := s.waves.Get(ctx, run.WaveID)
	if err != nil {
		return nil, mapExecutionError(err, "wave")
	}
	if requested.IsIssueCandidate() {
		if err := s.emitFailureCandidate(ctx, wave, run); err != nil {
			return nil, err
		}
	}
	return map[string]any{
		"case_run_id":   platformid.Format(run.ID),
		"failure_class": string(run.FailureClass),
		"status":        "recorded",
	}, nil
}

func failureRelevant(status execution.CaseRunStatus) bool {
	switch status {
	case execution.CaseRunFailed, execution.CaseRunBlocked, execution.CaseRunInconclusive:
		return true
	default:
		return false
	}
}

func summaryEqual(left, right execution.CaseRunSummary) bool {
	return left.VerdictReason == right.VerdictReason &&
		left.AssertionTotal == right.AssertionTotal &&
		left.AssertionFailed == right.AssertionFailed &&
		left.StepTotal == right.StepTotal &&
		left.StepFailed == right.StepFailed &&
		left.VisualSkipped == right.VisualSkipped &&
		left.LastError == right.LastError
}

func (s *AnalysisService) casePriority(ctx context.Context, versionID, caseVersionID int64) string {
	if s.cases == nil {
		return "P2"
	}
	snapshot, err := s.cases.GetCaseVersion(ctx, versionID, caseVersionID)
	if err != nil || snapshot.Priority == "" {
		return "P2"
	}
	return snapshot.Priority
}

func analysisCandidateHint(versionID, caseVersionID int64, stepSeq, assertionSeq int, class execution.FailureClass) string {
	raw := fmt.Sprintf("%d|%d|%d|%d|%s", versionID, caseVersionID, stepSeq, assertionSeq, class)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:32]
}

func failureCategory(assertionKind string) string {
	switch assertionKind {
	case execution.AssertionKindDB:
		return "db"
	case execution.AssertionKindAPI:
		return "api"
	default:
		return "ui"
	}
}

func failureSeverity(priority string) string {
	switch priority {
	case "P0", "P1":
		return "major"
	case "P2":
		return "minor"
	default:
		return "trivial"
	}
}

func (s *AnalysisService) post(ctx context.Context, post TeamPost) {
	if s.teamRoom == nil {
		return
	}
	_ = s.teamRoom.Post(ctx, post)
}

func (s *AnalysisService) now() time.Time {
	if s.clock == nil {
		return time.Now().UTC()
	}
	return s.clock.Now()
}

var _ = errors.Is
