package execution

import "fmt"

// This file holds the reclassification and agent-level settlement branches of
// the single verdict authority (Aggregate). They live HERE, in the domain
// verdict module, so every verdict rule has one home (constitution art.5.1/5.4):
// the analysis fallback used to hand-write its own deterministicResult copy of
// the §4.3/§4.5 rules in the application layer, which never saw the evidence
// facts (V-0022). Both branches share the recorded-fact primitives
// (ExecutorLimitationStep, StepShowsApplicationDeviation,
// FailedStepShowsRequirementDeviation) and the evidence gates with settlement.

// PriorVerdict is the persisted terminal verdict of a run being reclassified.
// Summary is the stored summary projection the reclassification decorates; the
// verdict fields (status/class) are the prior server settlement, never a model
// candidate.
type PriorVerdict struct {
	Status       CaseRunStatus
	FailureClass FailureClass
	Summary      CaseRunSummary
}

// AgentSettlement is the deterministic agent-level failure settlement input:
// the run ended outside the agent loop (human timeout/cancel, recovery
// dispatch-budget exhaustion). Class is the server-classified failure cause
// (session/executor_lost/environment/timeout/blocked — never application, which
// requires assertion evidence the analysis fallback owns); Reason is the stable
// domain verdict-reason code (VerdictReasonHumanTimeout / VerdictReasonHumanCancelled
// / VerdictReasonAgentFailure); Summary is the stored summary to decorate.
type AgentSettlement struct {
	Class   FailureClass
	Reason  string
	Summary CaseRunSummary
}

// agentSettlementVerdict maps an agent-level failure settlement onto the
// deterministic verdict: failed(Class) with the stable reason code. It never
// re-judges recorded results and never depends on the case definition (the
// case version may no longer be executable). The evidence gates still apply.
func agentSettlementVerdict(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	settlement := *in.AgentSettlement
	if !settlement.Class.Valid() || settlement.Class == "" || settlement.Class == FailureClassApplication {
		return "", "", CaseRunSummary{}, fmt.Errorf("%w: agent settlement class %q", ErrInvalidVerdict, settlement.Class)
	}
	summary := settlement.Summary
	summary.VerdictReason = settlement.Reason
	return CaseRunFailed, settlement.Class, summary, nil
}

// reclassifyResults re-judges one persisted terminal failure verdict against
// the recorded facts (the deterministic analysis fallback, §4.3/§4.5). The
// totals describe the recorded results; the prior verdict is the server's
// earlier settlement, used only to preserve a still-valid classification when
// the recorded facts carry no failure evidence.
func reclassifyResults(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	prior := *in.Prior
	summary := prior.Summary
	summary.StepTotal = len(in.Steps)
	summary.AssertionTotal = len(in.Assertions)
	summary.StepFailed = 0
	summary.AssertionFailed = 0
	var failedAssertion *AssertionResult
	var failedStep *StepResult
	var blockedStep *StepResult
	for index := range in.Steps {
		switch in.Steps[index].Status {
		case StepFailed:
			summary.StepFailed++
			if failedStep == nil {
				failedStep = &in.Steps[index]
			}
		case StepBlocked:
			if blockedStep == nil {
				blockedStep = &in.Steps[index]
			}
		}
	}
	for index := range in.Assertions {
		if in.Assertions[index].Verdict == AssertionFailed {
			summary.AssertionFailed++
			if failedAssertion == nil {
				failedAssertion = &in.Assertions[index]
			}
		}
	}

	// Recorded application evidence is authoritative over a blocked state (Task
	// 107, PO trap E2E FR-113): the run observed a missing like control/count,
	// missing like fields and a 404 like endpoint, recorded one failed ui
	// assertion and 14 evidence rows, yet a later blocked step used to rewrite
	// the whole run to blocked and drop the issue candidate. A blocked later
	// step only says the remaining steps could not run; it does not erase the
	// product deviation already observed. The failed assertion keeps the
	// Aggregate precedence (assertions are authoritative); the observed
	// requirement deviation deliberately outranks blocked here because the
	// analysis fallback is the deterministic source of truth for the report and
	// issue projection, and there the recorded product deviation must win.
	if failedAssertion != nil {
		class := FailureClassApplication
		if prior.FailureClass == FailureClassAutomation {
			class = FailureClassAutomation
		}
		summary.VerdictReason = VerdictReasonAssertionFailed
		return CaseRunFailed, class, summary, nil
	}
	// Recorded application evidence is authoritative over a blocked state.
	// Task 107 (PO trap E2E FR-113) covers the failed assertion and the failed
	// step that observed a product deviation; Task 110 (PO trap E2E FR-106-02)
	// covers the observed missing UI/API control itself: the editor had no
	// publish-time/schedule control, so the remaining steps could not run and
	// no assertion was ever executed. The observation sits on the failed step
	// that tried to set the time (with its explanatory error) or on a blocked
	// step that could not even start; both name the absent product capability
	// and are application evidence. A run whose blocked steps only record their
	// downstream consequences (no control/feature) carries no such observation
	// and still falls through to blocked.
	if evidenceStep := FirstApplicationDeviationStep(in.Steps); evidenceStep != nil {
		return applicationDeviationVerdict(prior, summary)
	}
	if blockedStep != nil || prior.Status == CaseRunBlocked {
		summary.VerdictReason = VerdictReasonBlocked
		return CaseRunBlocked, FailureClassBlocked, summary, nil
	}
	if failedStep != nil {
		return failedStepVerdict(prior, *failedStep, summary)
	}
	if prior.Status == CaseRunInconclusive || prior.Status == CaseRunFailed {
		// No failing result: a claimed application failure has no evidence and
		// must not produce an issue candidate (P3-D-12).
		if prior.FailureClass == FailureClassApplication || prior.Status == CaseRunInconclusive {
			summary.VerdictReason = VerdictReasonIncomplete
			return CaseRunInconclusive, "", summary, nil
		}
		if prior.FailureClass.Valid() && prior.FailureClass != "" {
			return CaseRunFailed, prior.FailureClass, summary, nil
		}
		summary.VerdictReason = VerdictReasonIncomplete
		return CaseRunInconclusive, "", summary, nil
	}
	summary.VerdictReason = VerdictReasonIncomplete
	return CaseRunInconclusive, "", summary, nil
}

// FirstApplicationDeviationStep returns the first step whose recorded
// observation is application evidence: a failed step with an observed
// requirement deviation (QA Q1) or a failed/blocked step that observed a
// required control/feature as absent (Task 110).
func FirstApplicationDeviationStep(steps []StepResult) *StepResult {
	for index := range steps {
		if StepShowsApplicationDeviation(steps[index]) {
			return &steps[index]
		}
	}
	return nil
}

// applicationDeviationVerdict maps recorded application evidence onto the
// deterministic verdict. A blocked run is normalized to application because the
// blocked steps only explain why the remaining steps could not run; an explicit
// environment/model/timeout/session class is preserved because it describes a
// separate infrastructure failure.
func applicationDeviationVerdict(prior PriorVerdict, summary CaseRunSummary) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	class := FailureClassApplication
	switch prior.FailureClass {
	case "", FailureClassApplication, FailureClassAutomation, FailureClassBlocked:
		// Recorded product evidence wins over blocked/automation.
	default:
		if prior.FailureClass.Valid() {
			class = prior.FailureClass
		}
	}
	summary.VerdictReason = VerdictReasonStepFailed
	return CaseRunFailed, class, summary, nil
}

// failedStepVerdict maps one failed step onto the deterministic verdict. A
// failed step that recorded an observed product behavior (and no
// automation/tool error) is a requirement deviation, not an automation failure
// (QA Q1). An environment/model/timeout/unknown class is a separate agent-level
// failure and is preserved.
func failedStepVerdict(prior PriorVerdict, step StepResult, summary CaseRunSummary) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
	class := FailureClassAutomation
	switch prior.FailureClass {
	case "", FailureClassAutomation, FailureClassApplication:
		if FailedStepShowsRequirementDeviation(step) {
			class = FailureClassApplication
		}
	default:
		if prior.FailureClass.Valid() {
			class = prior.FailureClass
		}
	}
	summary.VerdictReason = VerdictReasonStepFailed
	return CaseRunFailed, class, summary, nil
}

// ApplyExecutorCapabilityLimitation turns a failed/blocked/inconclusive run
// carrying the structured executor limitation marker into a blocked run with
// terminal_cause=executor_capability. It never touches a passed/skipped run.
// Aggregate applies it to every verdict mode (v0.2.3 X-02): the executor could
// not perform the action, so the run is never a SUT application defect.
func ApplyExecutorCapabilityLimitation(status CaseRunStatus, class FailureClass, summary CaseRunSummary, steps []StepResult) (CaseRunStatus, FailureClass, CaseRunSummary) {
	switch status {
	case CaseRunFailed, CaseRunBlocked, CaseRunInconclusive:
	default:
		return status, class, summary
	}
	step, code, ok := ExecutorLimitationStep(steps)
	if !ok {
		return status, class, summary
	}
	reason := "the executor could not perform the action"
	if code != "" {
		reason = "executor capability limitation " + code
	}
	summary.VerdictReason = VerdictReasonBlocked
	summary.TerminalCause = TerminalCauseExecutorCapability
	summary.NextAction = NextActionResolveBlocker
	summary.LastError = fmt.Sprintf("%s (step %d)", reason, step.Seq)
	return CaseRunBlocked, FailureClassBlocked, summary
}
