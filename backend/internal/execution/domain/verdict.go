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
}

// Aggregate applies the §4.3 four-state aggregation. The model candidate is
// only a hint: assertion failures are authoritative for application defects,
// step failures keep their mapped class, missing required results are
// inconclusive, and blocked prerequisites win over incomplete.
func Aggregate(in VerdictInput) (CaseRunStatus, FailureClass, CaseRunSummary, error) {
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

	summary.VisualSkipped = countVisualSkipped(in.Case.Assertions, assertionResults)
	if failedStep != nil && failedStep.Error != "" {
		summary.LastError = failedStep.Error
	}

	// Agent-level failures that are not a deterministic result.
	switch in.AgentFailure {
	case FailureClassBlocked:
		summary.VerdictReason = VerdictReasonBlocked
		return CaseRunBlocked, FailureClassBlocked, summary, nil
	case FailureClassExecutorLost:
		summary.VerdictReason = VerdictReasonAgentFailure
		return CaseRunInterrupted, FailureClassExecutorLost, summary, nil
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

	if failedStep != nil {
		summary.VerdictReason = VerdictReasonStepFailed
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
// passing result. Visual assertions are skipped (P5) and only counted.
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
			if assertion.Kind == AssertionKindVisual {
				continue
			}
			result, ok := assertionResults[[2]int{assertion.StepSeq, assertion.AssertionSeq}]
			if !ok || result.Verdict != AssertionPassed {
				summary.VerdictReason = VerdictReasonIncomplete
				return CaseRunInconclusive, "", false
			}
		}
	} else {
		for _, result := range in.Assertions {
			if result.Kind == AssertionKindVisual {
				continue
			}
			if result.Verdict != AssertionPassed {
				summary.VerdictReason = VerdictReasonIncomplete
				return CaseRunInconclusive, "", false
			}
		}
	}
	return "", "", true
}

func countVisualSkipped(assertions []CaseAssertion, results map[[2]int]AssertionResult) int {
	count := 0
	seen := make(map[[2]int]struct{}, len(assertions))
	for _, assertion := range assertions {
		if assertion.Kind != AssertionKindVisual {
			continue
		}
		key := [2]int{assertion.StepSeq, assertion.AssertionSeq}
		seen[key] = struct{}{}
		result, ok := results[key]
		if !ok || result.Verdict != AssertionPassed {
			count++
		}
	}
	for key, result := range results {
		if result.Kind != AssertionKindVisual {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		if result.Verdict != AssertionPassed {
			count++
		}
	}
	return count
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
