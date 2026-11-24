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
	evidence     EvidenceRepository
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
	Evidence     EvidenceRepository
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
		evidence:     deps.Evidence,
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
	// The executor snapshots run_wave.counts before it triggers this analysis,
	// so a run reclassified from blocked to failed(application) would keep
	// counting as blocked in the report headline. Refresh the snapshot from the
	// final case_run states (the same recomputation pause/cancel use).
	if err := s.refreshWaveCounts(ctx, wave); err != nil {
		return err
	}
	if err := s.analyzeCoverage(ctx, wave, runs); err != nil {
		return err
	}
	return s.EnsureTasks(ctx, waveID)
}

// refreshWaveCounts recomputes and persists the wave counts snapshot after the
// deterministic classification changed case_run states.
func (s *AnalysisService) refreshWaveCounts(ctx context.Context, wave execution.RunWave) error {
	counts, err := s.waves.Counts(ctx, wave.ID)
	if err != nil {
		return mapExecutionError(err, "wave")
	}
	wave.Counts = counts
	wave.UpdatedAt = s.now()
	if _, err := s.waves.Update(ctx, wave); err != nil {
		return mapExecutionError(err, "wave")
	}
	return nil
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

// classifyRun re-judges the persisted terminal failure verdict of one
// failed/blocked/inconclusive case_run through the single terminal entry's
// guarded terminal->terminal reclassification mode (V-0022). The verdict comes
// from the single domain authority execution.Aggregate (Prior input): the
// hand-written deterministicResult second truth is gone, and the evidence gate
// applies — a failed verdict with zero persisted evidence degrades inconclusive
// and never produces an issue candidate. The settlement CAS invariant is
// untouched: reclassification is a distinct sanctioned mode, not a settlement.
func (s *AnalysisService) classifyRun(ctx context.Context, wave execution.RunWave, run execution.CaseRun) error {
	steps, err := s.results.ListSteps(ctx, run.ID)
	if err != nil {
		return mapExecutionError(err, "step result")
	}
	assertions, err := s.results.ListAssertions(ctx, run.ID)
	if err != nil {
		return mapExecutionError(err, "assertion result")
	}
	status, class, summary, err := aggregateWithEvidenceFacts(ctx, s.evidence, run, execution.VerdictInput{
		Prior: &execution.PriorVerdict{
			Status:       run.Status,
			FailureClass: run.FailureClass,
			Summary:      run.Summary,
		},
		Steps:      steps,
		Assertions: assertions,
	})
	if err != nil {
		return mapExecutionError(err, "case run")
	}
	changed := status != run.Status || class != run.FailureClass || !summaryEqual(summary, run.Summary)
	if changed {
		reclassified, _, err := reclassifyTerminalVerdict(ctx, s.caseRuns, s.now(), run, status, class, summary)
		if err != nil {
			return err
		}
		run = reclassified
	}
	// Emit the deterministic issue candidate even when the executor already
	// persisted the same class: the wave-completion analysis is the handoff to
	// the issue pipeline and must not depend on who classified the run first
	// (P3 §8.1). The hint set keeps repeat analysis in this process idempotent.
	if run.FailureClass.IsIssueCandidate() {
		return s.emitFailureCandidate(ctx, wave, run)
	}
	return nil
}

// emitFailureCandidate publishes the application failure candidate event and
// team-room post. Only failed(application) with failed assertion evidence, an
// observed requirement deviation on a failed step, or an observed missing
// control/feature on a failed/blocked step is emitted.
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
	evidenceStep := execution.FirstApplicationDeviationStep(steps)
	if failedAssertion == nil && evidenceStep == nil {
		return nil
	}
	// A structured executor capability limitation is never a SUT defect: no
	// issue candidate is emitted even if a failed assertion is present (X-02).
	if _, _, limited := execution.ExecutorLimitationStep(steps); limited {
		return nil
	}
	category := "ui"
	stepSeq, assertionSeq := 0, 0
	detail := ""
	switch {
	case failedAssertion != nil:
		category = failureCategory(failedAssertion.Kind)
		stepSeq, assertionSeq = failedAssertion.StepSeq, failedAssertion.AssertionSeq
		detail = fmt.Sprintf("%s assertion %d.%d", failedAssertion.Kind, failedAssertion.StepSeq, failedAssertion.AssertionSeq)
	case evidenceStep != nil:
		stepSeq = evidenceStep.Seq
		if execution.StepObservesMissingFeature(*evidenceStep) {
			detail = fmt.Sprintf("step %d observed the required control/feature missing", evidenceStep.Seq)
		} else {
			detail = fmt.Sprintf("step %d observed requirement deviation", evidenceStep.Seq)
		}
	}
	// Task 112: a missing required control/feature recorded on an earlier step is
	// the root cause, even when later steps also failed assertions. Anchor the
	// candidate on that step so its category/step agree with the P4 issue.
	anchor := execution.MissingFeatureAnchor(steps, firstFailedStepSeq(failedAssertion))
	if anchor != nil {
		category = "ui"
		stepSeq, assertionSeq = anchor.Seq, 0
		detail = fmt.Sprintf("step %d observed the required control/feature missing", anchor.Seq)
	}
	hint := analysisCandidateHint(wave.VersionID, run.CaseVersionID, stepSeq, assertionSeq, run.FailureClass)
	s.mu.Lock()
	if _, seen := s.candidates[hint]; seen {
		s.mu.Unlock()
		return nil
	}
	s.candidates[hint] = struct{}{}
	s.mu.Unlock()

	priority := s.casePriority(ctx, wave.VersionID, run.CaseVersionID)
	severity := execution.IssueSeverity(priority)
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
		Content: fmt.Sprintf("failed(application) candidate: case_run %s failed %s",
			platformid.Format(run.ID), detail),
		Refs: []TeamPostRef{
			{Kind: "case_run", ID: platformid.Format(run.ID)},
			{Kind: "wave", ID: platformid.Format(wave.ID)},
		},
	})
	return nil
}

// FailureSubmit is the analysis.failure_submit entry point (ExecutionPort). It
// may keep or downgrade an application classification, and may escalate a
// non-application failure only when the stored step observation is a
// requirement deviation.
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
	if requested == execution.FailureClassApplication {
		// v0.2.3 X-02: a run limited by the executor's own capability is an
		// execution-environment condition, never a SUT application defect. The
		// analyst cannot escalate it with assertion evidence.
		steps, err := s.results.ListSteps(ctx, run.ID)
		if err != nil {
			return nil, mapExecutionError(err, "step result")
		}
		if err := executorCapabilityEscalationError(steps); err != nil {
			return nil, err
		}
	}
	if run.FailureClass != execution.FailureClassApplication && requested == execution.FailureClassApplication {
		// QA Q1: a stored observed requirement deviation is evidence the
		// deterministic fallback may have missed (for example a run classified
		// by an older deployment); only that evidence allows application. The
		// same applies to a blocked step observing a missing required control
		// (Task 110, FR-106-02).
		steps, err := s.results.ListSteps(ctx, run.ID)
		if err != nil {
			return nil, mapExecutionError(err, "step result")
		}
		hasDeviation := false
		for _, step := range steps {
			if execution.StepShowsApplicationDeviation(step) {
				hasDeviation = true
				break
			}
		}
		if !hasDeviation {
			return nil, platform.StateConflict("a non-application failure cannot be escalated to application without observed requirement deviation evidence")
		}
	}
	if requested == execution.FailureClassAutomation && run.FailureClass == execution.FailureClassApplication {
		// QA Q1: a real requirement deviation recorded on a failed step is
		// product evidence; the analyst must not be able to dismiss it as a
		// script/automation problem and suppress the issue candidate.
		steps, err := s.results.ListSteps(ctx, run.ID)
		if err != nil {
			return nil, mapExecutionError(err, "step result")
		}
		for _, step := range steps {
			if execution.StepShowsApplicationDeviation(step) {
				return nil, platform.StateConflict("an observed requirement deviation cannot be reclassified as automation")
			}
		}
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
			// QA Q1: a failed step whose observation contradicts the
			// requirement is application evidence even without a failed
			// assertion record; so is a blocked step observing a missing
			// required control/feature (Task 110).
			steps, err := s.results.ListSteps(ctx, run.ID)
			if err != nil {
				return nil, mapExecutionError(err, "step result")
			}
			for _, step := range steps {
				if execution.StepShowsApplicationDeviation(step) {
					hasEvidence = true
					break
				}
			}
		}
		if !hasEvidence {
			return nil, platform.StateConflict("failed(application) requires failed assertion or observed requirement deviation evidence")
		}
	}
	run.FailureClass = requested
	if summary := execution.SanitizeFailureSummary(in.Summary); summary != "" {
		if len(summary) > 2000 {
			summary = summary[:2000]
		}
		run.Summary.LastError = summary
	}
	// The analyst refinement is a terminal->terminal class change: it goes
	// through the single terminal entry's reclassification mode (V-0022), never
	// a direct Update. The status is unchanged; only the validated class moves.
	reclassified, _, err := reclassifyTerminalVerdict(ctx, s.caseRuns, s.now(), run, run.Status, run.FailureClass, run.Summary)
	if err != nil {
		return nil, err
	}
	run = reclassified
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
		left.EvidenceCount == right.EvidenceCount &&
		left.EvidenceDropped == right.EvidenceDropped &&
		left.LastError == right.LastError &&
		left.TerminalCause == right.TerminalCause &&
		left.NextAction == right.NextAction
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

// firstFailedStepSeq returns the step sequence of the first failed assertion, or
// 0 when the run has no failed assertion. It bounds the missing-feature anchor:
// an observation on a later step is a consequence, not the root cause.
func firstFailedStepSeq(assertion *execution.AssertionResult) int {
	if assertion == nil {
		return 0
	}
	return assertion.StepSeq
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
