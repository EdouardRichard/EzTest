package execution

import "fmt"

// Verdict reasons written to case_run.summary.verdict_reason. They are stable
// strings: P4 and the UI display them.
const (
	VerdictReasonPassed          = "passed"
	VerdictReasonAssertionFailed = "assertion_failed"
	VerdictReasonStepFailed      = "step_failed"
	VerdictReasonIncomplete      = "incomplete"
	VerdictReasonEmptyCase       = "empty_case"
	VerdictReasonNoAssertions    = "no_assertions"
	VerdictReasonBlocked         = "blocked"
	VerdictReasonAgentFailure    = "agent_failure"
	// VerdictReasonEvidenceDropped marks a run whose conclusion was degraded
	// because evidence was lost (constitution art.4.5). It is not a SUT defect:
	// the run simply cannot be trusted, so it never carries a failure class.
	VerdictReasonEvidenceDropped = "evidence_dropped"
	// VerdictReasonNoEvidence marks a failed conclusion degraded because the run
	// produced no evidence at all (constitution art.1.2 "no-evidence failed
	// degrades inconclusive"). A failure nobody can inspect is not a trustworthy
	// defect, so it never carries a failure class.
	VerdictReasonNoEvidence = "no_evidence"
)

// FinishCandidate is the model-submitted execution.case_finish candidate. The
// server never trusts it directly: Aggregate overwrites it with the
// deterministic §4.3 verdict.
type FinishCandidate struct {
	Verdict      CaseRunStatus
	Summary      string
	FailureClass FailureClass
}

// Valid reports whether the candidate verdict is one of the four result states.
func (c FinishCandidate) Valid() bool { return c.Verdict.FourState() }

// VerdictInput is the deterministic aggregation input for one finished run.
type VerdictInput struct {
	Case         CaseSnapshot
	Steps        []StepResult
	Assertions   []AssertionResult
	Finish       *FinishCandidate
	AgentFailure FailureClass
	// EvidenceDropped is the persisted count of evidence items this run lost.
	// Above zero the deterministic verdict degrades to inconclusive: a
	// conclusion drawn from incomplete evidence is not trustworthy
	// (constitution art.4.5, art.1.1 "no-evidence failed degrades").
	EvidenceDropped int64
	// EvidenceCount is the persisted count of evidence items this run holds.
	// A failed verdict with zero evidence degrades to inconclusive: a failure
	// with no immutable evidence to inspect is not a trustworthy defect
	// (constitution art.1.2). Callers supply it from the evidence repository;
	// Aggregate stays pure and never reads the database itself.
	EvidenceCount int
	// Prior, when non-nil, selects the RECLASSIFICATION question (V-0022): the
	// run is already terminal (failed/blocked/inconclusive) and the analysis
	// fallback re-judges its persisted verdict against the recorded facts. The
	// verdict is derived from the recorded steps/assertions and the prior
	// verdict — never from the case definition, which may no longer be
	// executable (a superseded case version still has terminal runs to analyze).
	// The evidence gates apply exactly as in settlement. Mutually exclusive
	// with AgentSettlement.
	Prior *PriorVerdict
	// AgentSettlement, when non-nil, selects the AGENT-LEVEL settlement question
	// (V-0022): the run ended outside the agent loop (human timeout/cancel,
	// recovery dispatch-budget exhaustion) and is settled failed(Class) with the
	// stable Reason code, independent of the case definition and the recorded
	// results (the analysis fallback re-judges those afterwards). The evidence
	// gates apply: a zero-evidence failed still degrades inconclusive. Mutually
	// exclusive with Prior.
	AgentSettlement *AgentSettlement
}

// Aggregate is the single deterministic verdict authority (constitution
// art.3.3/3.6). It answers exactly one of three questions, selected by the
// input, and applies the shared evidence gates and the structured executor
// capability limitation to every answer:
//
//   - settlement (default): did the confirmed case pass? The model candidate is
//     only a hint; assertion failures are authoritative for application defects,
//     step failures keep their mapped class, missing required results are
//     inconclusive, and blocked prerequisites win over incomplete.
//   - reclassification (Prior): is the persisted terminal failure verdict
//     correctly classified given the recorded facts?
//   - agent-level settlement (AgentSettlement): the run was abandoned for a
//     session/infrastructure reason; the verdict is the deterministic failure
//     mapping, not a re-judgment of results.
func Aggregate(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	var status CaseRunStatus
	var class FailureClass
	var summary CaseRunSummary
	var err error
	switch {
	case in.AgentSettlement != nil:
		status, class, summary, err = agentSettlementVerdict(in)
	case in.Prior != nil:
		status, class, summary, err = reclassifyResults(in)
	default:
		status, class, summary, err = aggregateResults(in)
	}
	if err != nil {
		return "", "", CaseRunSummary{}, err
	}
	// Lost evidence degrades first so a run that already cannot be trusted keeps
	// the more specific evidence_dropped reason; the no-evidence gate then only
	// sees a verdict that is still failed (constitution art.4.5 before art.1.2).
	status, class, summary, err = degradeForEvidenceDrop(in.EvidenceDropped, status, class, summary)
	if err != nil {
		return "", "", CaseRunSummary{}, err
	}
	status, class, summary, err = degradeForNoEvidence(in.EvidenceCount, status, class, summary)
	if err != nil {
		return "", "", CaseRunSummary{}, err
	}
	// A structured executor capability limitation wins over the text heuristics
	// and assertion evidence in every mode: the executor could not perform the
	// action, so the run is blocked, never a SUT application defect (v0.2.3 X-02).
	status, class, summary = ApplyExecutorCapabilityLimitation(status, class, summary, in.Steps)
	return status, class, summary, nil
}

// degradeForEvidenceDrop applies constitution art.4.5 to an aggregated verdict.
// Only the two CONCLUSIVE states degrade: passed and failed become inconclusive
// with no failure class (an untrustworthy conclusion must never create an issue).
// blocked already reports "cannot determine" and keeps its blocker signal;
// process states (interrupted/skipped/superseded) are not verdicts at all.
func degradeForEvidenceDrop(dropped int64, status CaseRunStatus, class FailureClass, summary CaseRunSummary) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	summary.EvidenceDropped = dropped
	if dropped <= 0 {
		return status, class, summary, nil
	}
	switch status {
	case CaseRunPassed, CaseRunFailed:
		summary.VerdictReason = VerdictReasonEvidenceDropped
		return CaseRunInconclusive, "", summary, nil
	default:
		return status, class, summary, nil
	}
}

// degradeForNoEvidence applies constitution art.1.2 to an aggregated verdict: a
// failed conclusion backed by zero evidence is not a trustworthy defect and
// degrades to inconclusive with no failure class (it must never create an issue).
// Only failed degrades — passed is a clean result that needs no defect evidence,
// blocked already reports "cannot determine", and process states are not verdicts.
// It runs after degradeForEvidenceDrop, so a run already degraded for lost
// evidence keeps evidence_dropped rather than being relabelled no_evidence.
func degradeForNoEvidence(count int, status CaseRunStatus, class FailureClass, summary CaseRunSummary) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	summary.EvidenceCount = count
	if status == CaseRunFailed && count == 0 {
		summary.VerdictReason = VerdictReasonNoEvidence
		return CaseRunInconclusive, "", summary, nil
	}
	return status, class, summary, nil
}

func aggregateResults(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	if in.Finish != nil && !in.Finish.Valid() {
		return "", "", CaseRunSummary{}, fmt.Errorf("%w: %q", ErrInvalidVerdict, in.Finish.Verdict)
	}
	summary := CaseRunSummary{
		AssertionTotal: len(in.Case.Assertions),
		StepTotal:      len(in.Case.Steps),
		VerdictReason:  VerdictReasonPassed,
	}
	if len(in.Case.Steps) == 0 {
		summary.VerdictReason = VerdictReasonEmptyCase
		return CaseRunInconclusive, "", summary, nil
	}

	stepResults := make(map[int]StepResult, len(in.Steps))
	var failedStep *StepResult
	var blockedStep *StepResult
	for i := range in.Steps {
		result := in.Steps[i]
		stepResults[result.Seq] = result
		switch result.Status {
		case StepFailed:
			summary.StepFailed++
			if failedStep == nil {
				failedStep = &in.Steps[i]
			}
		case StepBlocked:
			if blockedStep == nil {
				blockedStep = &in.Steps[i]
			}
		}
	}

	assertionResults := make(map[[2]int]AssertionResult, len(in.Assertions))
	var failedAssertion *AssertionResult
	for i := range in.Assertions {
		result := in.Assertions[i]
		assertionResults[[2]int{result.StepSeq, result.AssertionSeq}] = result
		if result.Verdict == AssertionFailed {
			summary.AssertionFailed++
			if failedAssertion == nil {
				failedAssertion = &in.Assertions[i]
			}
		}
	}

	if failedStep != nil && failedStep.Error != "" {
		summary.LastError = failedStep.Error
	}

	// Executor loss is infrastructure state, not a case verdict.
	if in.AgentFailure == FailureClassExecutorLost {
		summary.VerdictReason = VerdictReasonAgentFailure
		return CaseRunInterrupted, FailureClassExecutorLost, summary, nil
	}

	// Assertion failures are the strongest evidence: only they may produce
	// failed(application). The model may downgrade to automation but never
	// upgrade.
	if failedAssertion != nil {
		class := FailureClassApplication
		if in.Finish != nil && in.Finish.FailureClass == FailureClassAutomation {
			class = FailureClassAutomation
		}
		summary.VerdictReason = VerdictReasonAssertionFailed
		return CaseRunFailed, class, summary, nil
	}

	// Agent-level failures that are not a deterministic result.
	switch in.AgentFailure {
	case FailureClassBlocked:
		summary.VerdictReason = VerdictReasonBlocked
		return CaseRunBlocked, FailureClassBlocked, summary, nil
	case "", FailureClassApplication:
		// application needs assertion evidence; handled deterministically below.
	default:
		if in.AgentFailure.Valid() {
			summary.VerdictReason = VerdictReasonAgentFailure
			return CaseRunFailed, in.AgentFailure, summary, nil
		}
	}

	if blockedStep != nil {
		summary.VerdictReason = VerdictReasonBlocked
		return CaseRunBlocked, FailureClassBlocked, summary, nil
	}

	if failedStep != nil {
		summary.VerdictReason = VerdictReasonStepFailed
		// A failed step that recorded an observed product behavior (and no
		// automation/tool error) is a requirement deviation, not a script
		// failure (QA Q1). It must produce failed(application) so the issue
		// candidate path is not suppressed. An explicit environment/model/
		// timeout/session candidate stays on that class: it describes a
		// separate infrastructure failure.
		if FailedStepShowsRequirementDeviation(*failedStep) &&
			(in.Finish == nil ||
				in.Finish.FailureClass == "" ||
				in.Finish.FailureClass == FailureClassApplication ||
				in.Finish.FailureClass == FailureClassAutomation) {
			return CaseRunFailed, FailureClassApplication, summary, nil
		}
		return CaseRunFailed, stepFailureClass(in.Finish), summary, nil
	}

	// A blocked candidate with no executed result is a genuine pre-execution
	// blocked verdict (missing account/DB/page/environment).
	if in.Finish != nil && in.Finish.Verdict == CaseRunBlocked && len(in.Steps) == 0 && len(in.Assertions) == 0 {
		summary.VerdictReason = VerdictReasonBlocked
		return CaseRunBlocked, FailureClassBlocked, summary, nil
	}

	// An application candidate without any assertion/step evidence is not
	// allowed to report an application defect (P3-D-12).
	if in.Finish != nil && in.Finish.FailureClass == FailureClassApplication &&
		len(in.Assertions) == 0 && !hasObservedStep(in.Steps) {
		summary.VerdictReason = VerdictReasonIncomplete
		return CaseRunInconclusive, "", summary, nil
	}

	// A non-application infra failure candidate with no results at all is
	// accepted: there is nothing to contradict it and no assertion evidence to
	// preserve.
	if in.Finish != nil && in.Finish.Verdict == CaseRunFailed &&
		len(in.Steps) == 0 && len(in.Assertions) == 0 && in.Finish.FailureClass.Valid() &&
		in.Finish.FailureClass != "" && in.Finish.FailureClass != FailureClassApplication {
		summary.VerdictReason = VerdictReasonAgentFailure
		return CaseRunFailed, in.Finish.FailureClass, summary, nil
	}

	if status, class, complete := completeResults(in, stepResults, assertionResults, &summary); !complete {
		return status, class, summary, nil
	}
	summary.VerdictReason = VerdictReasonPassed
	return CaseRunPassed, "", summary, nil
}

// completeResults checks that every required step and assertion has a final
// passing result.
func completeResults(
	in VerdictInput,
	stepResults map[int]StepResult,
	assertionResults map[[2]int]AssertionResult,
	summary *CaseRunSummary,
) (CaseRunStatus, FailureClass, bool) {
	if len(in.Case.Assertions) == 0 && len(in.Assertions) == 0 {
		summary.VerdictReason = VerdictReasonNoAssertions
		return CaseRunInconclusive, "", false
	}
	for _, step := range in.Case.Steps {
		result, ok := stepResults[step.Seq]
		if !ok || result.Status != StepPassed {
			summary.VerdictReason = VerdictReasonIncomplete
			return CaseRunInconclusive, "", false
		}
	}
	if len(in.Case.Assertions) > 0 {
		for _, assertion := range in.Case.Assertions {
			result, ok := assertionResults[[2]int{assertion.StepSeq, assertion.AssertionSeq}]
			if !ok || result.Verdict != AssertionPassed {
				summary.VerdictReason = VerdictReasonIncomplete
				return CaseRunInconclusive, "", false
			}
		}
	} else {
		for _, result := range in.Assertions {
			if result.Verdict != AssertionPassed {
				summary.VerdictReason = VerdictReasonIncomplete
				return CaseRunInconclusive, "", false
			}
		}
	}
	return "", "", true
}

func hasObservedStep(steps []StepResult) bool {
	for _, step := range steps {
		if len(step.Observed) > 0 || step.Error != "" {
			return true
		}
	}
	return false
}

// stepFailureClass maps a failed step to a failure class. The model candidate
// is only accepted when it is a non-application, non-blocked class.
func stepFailureClass(candidate *FinishCandidate) FailureClass {
	if candidate == nil {
		return FailureClassAutomation
	}
	class := candidate.FailureClass
	if class == "" || !class.Valid() {
		return FailureClassAutomation
	}
	switch class {
	case FailureClassApplication, FailureClassBlocked, FailureClassExecutorLost:
		return FailureClassAutomation
	default:
		return class
	}
}
