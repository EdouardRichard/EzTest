package delivery

// v0.2.3 X-02: report/triage use the same executor-capability vocabulary as the
// execution classification. A run blocked by an executor limitation stays
// blocked (not application/expected_failure) and is never presented as a SUT
// product defect.

import "testing"

func TestExecutorCapabilityRunStaysBlocked(t *testing.T) {
	row := CaseRunSnapshot{
		Status:        "blocked",
		FailureClass:  "blocked",
		TerminalCause: "executor_capability",
	}
	if got := row.EffectiveFailureClass(); got != FailureClassBlocked {
		t.Fatalf("effective failure class = %q, want blocked", got)
	}
	if !row.ExecutorCapabilityBlocked() {
		t.Fatal("executor_capability run was not recognized as an executor capability block")
	}
	if row.ExpectedFailureTriage() {
		t.Fatal("an executor capability block must not be presented as expected_failure triage")
	}
	// A triage classification never turns the block into a product defect.
	triaged := row
	triaged.TriageClassification = TriageClassificationFlaky
	if got := triaged.EffectiveFailureClass(); got == "application" {
		t.Fatalf("triage turned the capability block into an application defect: %q", got)
	}
}

func TestRealApplicationRunIsNotCapabilityBlocked(t *testing.T) {
	row := CaseRunSnapshot{Status: "failed", FailureClass: "application", TerminalCause: "assertion_failed"}
	if row.ExecutorCapabilityBlocked() {
		t.Fatal("a real application failure was read as an executor capability block")
	}
	if got := row.EffectiveFailureClass(); got != "application" {
		t.Fatalf("effective failure class = %q, want application", got)
	}
}
