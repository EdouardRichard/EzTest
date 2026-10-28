package domain

import (
	"fmt"
	"strings"
)

// Finish-validation reason identifiers. Every rejection is an invalid_arguments
// tool error (spec §5.5) whose Reason carries the stable, machine-readable
// cause; evidence_missing and unsupported_assertion_kind are the spec §6.4
// identifiers.
const (
	reasonUnsupportedAssertionKind = "unsupported_assertion_kind"
	reasonInvalidFinishVerdict     = "invalid_finish_verdict"
	reasonInvalidStepResults       = "invalid_step_results"
	reasonInvalidAssertionResults  = "invalid_assertion_results"
	reasonInvalidFailureClass      = "invalid_failure_class"
	reasonVerdictNotJustified      = "verdict_not_justified"
	reasonBudgetExhausted          = "budget_exhausted"
)

func finishError(reason, message string) error {
	return NewReasonError("invalid_arguments", reason, message, false, nil)
}

// ValidateFinish is the server-side gate for a case_finish submission. It
// enforces spec §5.5/§5.8 before the finish is persisted:
//
//   - every snapshot step has exactly one result, recorded once in ascending seq
//     order, and no result refers to a step outside the snapshot;
//   - every non-db snapshot assertion is submitted exactly once, and no result
//     refers to an assertion outside the snapshot;
//   - passed and failed require at least one piece of evidence;
//   - passed requires every step passed and every non-db assertion passed;
//   - failed requires at least one failed assertion;
//   - a snapshot containing a db assertion is only allowed to finish
//     inconclusive(case), reason unsupported_assertion_kind;
//   - the verdict/failure-class matrix of spec §5.8 must hold;
//   - a budget-exhausted finish must be inconclusive(model).
//
// It returns nil when the decision is admissible. Rejections are
// *ReasonError values so the recorder can surface a correctable tool error.
func ValidateFinish(snapshot CaseSnapshot, steps []StepResult, assertions []AssertionResult, decision FinishDecision) error {
	switch decision.Verdict {
	case VerdictPassed, VerdictFailed, VerdictBlocked, VerdictInconclusive:
	default:
		return finishError(reasonInvalidFinishVerdict,
			fmt.Sprintf("verdict %q is not a valid case-finish verdict", decision.Verdict))
	}

	// P6 does not support db assertions. Never let such a case pretend to pass,
	// fail or be blocked; it may only finish inconclusive(case).
	if snapshotHasDBAssertion(snapshot) {
		if decision.Verdict != VerdictInconclusive ||
			decision.FailureClass == nil || *decision.FailureClass != FailureClassCase {
			return finishError(reasonUnsupportedAssertionKind,
				"case contains a db assertion; finish as inconclusive(case, unsupported_assertion_kind)")
		}
	}

	if err := validateFinishClass(decision); err != nil {
		return err
	}

	// Budget exhaustion means the case did not finish; spec §5.8 forbids
	// turning it into failed.
	if isBudgetExhaustionReason(decision.Reason) {
		if decision.Verdict != VerdictInconclusive ||
			decision.FailureClass == nil || *decision.FailureClass != FailureClassModel {
			return finishError(reasonBudgetExhausted,
				"budget exhaustion must finish as inconclusive(model)")
		}
	}

	if err := validateFinishSteps(snapshot, steps); err != nil {
		return err
	}
	results, err := validateFinishAssertions(snapshot, assertions)
	if err != nil {
		return err
	}

	if decision.Verdict == VerdictPassed || decision.Verdict == VerdictFailed {
		if !hasEvidence(steps) {
			return finishError(reasonEvidenceMissing,
				"passed and failed finishes require at least one evidence item")
		}
	}

	switch decision.Verdict {
	case VerdictPassed:
		for _, step := range steps {
			if step.Status != StepStatusPassed {
				return finishError(reasonVerdictNotJustified,
					fmt.Sprintf("passed requires every step passed, step %d is %q", step.Seq, step.Status))
			}
		}
		for _, assertion := range snapshot.Assertions {
			if isDBAssertion(assertion.Kind) {
				continue
			}
			if !verdictEquals(results[assertion.ID].Verdict, "passed") {
				return finishError(reasonVerdictNotJustified,
					fmt.Sprintf("passed requires assertion %d passed", assertion.ID))
			}
		}
	case VerdictFailed:
		if !hasFailedAssertion(snapshot, results) {
			return finishError(reasonVerdictNotJustified,
				"failed requires at least one failed non-db assertion")
		}
	}

	return nil
}

func validateFinishClass(decision FinishDecision) error {
	switch decision.Verdict {
	case VerdictPassed:
		if decision.FailureClass != nil {
			return finishError(reasonInvalidFailureClass, "passed must not carry a failure class")
		}
	case VerdictFailed:
		if decision.FailureClass == nil || *decision.FailureClass != FailureClassApplication {
			return finishError(reasonInvalidFailureClass, "failed requires failure class application")
		}
	case VerdictBlocked:
		if decision.FailureClass == nil ||
			(*decision.FailureClass != FailureClassEnvironment && *decision.FailureClass != FailureClassSession) {
			return finishError(reasonInvalidFailureClass,
				"blocked requires failure class environment or session")
		}
	case VerdictInconclusive:
		if decision.FailureClass == nil ||
			(*decision.FailureClass != FailureClassModel && *decision.FailureClass != FailureClassCase) {
			return finishError(reasonInvalidFailureClass,
				"inconclusive requires failure class model or case")
		}
	}
	return nil
}

// validateFinishSteps requires exactly the snapshot steps, once each, supplied
// in ascending seq order.
func validateFinishSteps(snapshot CaseSnapshot, steps []StepResult) error {
	snapshotSeqs := make(map[int]struct{}, len(snapshot.Steps))
	for _, step := range snapshot.Steps {
		snapshotSeqs[step.Seq] = struct{}{}
	}
	if len(steps) != len(snapshotSeqs) {
		return finishError(reasonInvalidStepResults,
			fmt.Sprintf("recorded %d steps, snapshot has %d", len(steps), len(snapshotSeqs)))
	}

	seen := make(map[int]struct{}, len(steps))
	previous := 0
	for _, step := range steps {
		if _, ok := snapshotSeqs[step.Seq]; !ok {
			return finishError(reasonInvalidStepResults,
				fmt.Sprintf("step %d is not part of the case snapshot", step.Seq))
		}
		if _, ok := seen[step.Seq]; ok {
			return finishError(reasonInvalidStepResults,
				fmt.Sprintf("step %d was recorded more than once", step.Seq))
		}
		if step.Seq <= previous {
			return finishError(reasonInvalidStepResults,
				fmt.Sprintf("step %d is out of seq order", step.Seq))
		}
		seen[step.Seq] = struct{}{}
		previous = step.Seq
	}
	return nil
}

// validateFinishAssertions requires every non-db snapshot assertion to be
// submitted exactly once and rejects results for assertions outside the
// snapshot. It returns the submitted results indexed by assertion id.
func validateFinishAssertions(snapshot CaseSnapshot, assertions []AssertionResult) (map[int64]AssertionResult, error) {
	snapshotByID := make(map[int64]CaseSnapshotAssertion, len(snapshot.Assertions))
	for _, assertion := range snapshot.Assertions {
		snapshotByID[assertion.ID] = assertion
	}

	results := make(map[int64]AssertionResult, len(assertions))
	for _, result := range assertions {
		if _, ok := snapshotByID[result.AssertionID]; !ok {
			return nil, finishError(reasonInvalidAssertionResults,
				fmt.Sprintf("assertion %d is not part of the case snapshot", result.AssertionID))
		}
		if _, ok := results[result.AssertionID]; ok {
			return nil, finishError(reasonInvalidAssertionResults,
				fmt.Sprintf("assertion %d was submitted more than once", result.AssertionID))
		}
		results[result.AssertionID] = result
	}

	for _, assertion := range snapshot.Assertions {
		if isDBAssertion(assertion.Kind) {
			// P6 never runs db assertions; the db rule itself is enforced above.
			continue
		}
		if _, ok := results[assertion.ID]; !ok {
			return nil, finishError(reasonInvalidAssertionResults,
				fmt.Sprintf("assertion %d was not submitted", assertion.ID))
		}
	}
	return results, nil
}

func snapshotHasDBAssertion(snapshot CaseSnapshot) bool {
	for _, assertion := range snapshot.Assertions {
		if isDBAssertion(assertion.Kind) {
			return true
		}
	}
	return false
}

func hasFailedAssertion(snapshot CaseSnapshot, results map[int64]AssertionResult) bool {
	for _, assertion := range snapshot.Assertions {
		if isDBAssertion(assertion.Kind) {
			continue
		}
		if verdictEquals(results[assertion.ID].Verdict, "failed") {
			return true
		}
	}
	return false
}

func hasEvidence(steps []StepResult) bool {
	for _, step := range steps {
		if step.EvidenceCount > 0 {
			return true
		}
	}
	return false
}

func isDBAssertion(kind string) bool {
	return strings.EqualFold(strings.TrimSpace(kind), "db")
}

func verdictEquals(value, want string) bool {
	return strings.EqualFold(strings.TrimSpace(value), want)
}

func isBudgetExhaustionReason(reason string) bool {
	return strings.Contains(strings.ToLower(reason), "budget")
}
