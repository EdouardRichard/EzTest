package execution

import "testing"

// Constitution art.1.2 (V-0029): a failed conclusion with no evidence at all is
// not a trustworthy defect and must degrade to inconclusive. The judgment lives
// in Aggregate (the single verdict authority), never in a caller.

func TestAggregateNoEvidenceDegradesFailed(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:       caseSnapshotForVerdict(1),
		Steps:      []StepResult{passedStep(1)},
		Assertions: []AssertionResult{failedAssertion(1, 1, "ui")},
		// EvidenceCount defaults to 0: a failed verdict nobody can inspect.
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: a failed verdict with no evidence is not trustworthy", status)
	}
	if class != "" {
		t.Fatalf("failure class = %q, want empty (a degraded run must not create an issue)", class)
	}
	if summary.VerdictReason != VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonNoEvidence)
	}
	if summary.EvidenceCount != 0 {
		t.Fatalf("summary evidence_count = %d, want 0", summary.EvidenceCount)
	}
}

func TestAggregateWithEvidenceKeepsFailed(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:          caseSnapshotForVerdict(1),
		Steps:         []StepResult{passedStep(1)},
		Assertions:    []AssertionResult{failedAssertion(1, 1, "ui")},
		EvidenceCount: 1,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("verdict = %s/%q, want failed/application: evidence backs the defect", status, class)
	}
	if summary.VerdictReason != VerdictReasonAssertionFailed {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonAssertionFailed)
	}
	if summary.EvidenceCount != 1 {
		t.Fatalf("summary evidence_count = %d, want 1", summary.EvidenceCount)
	}
}

// TestEvidenceDroppedStillDegrades proves the art.4.5 drop mechanism is intact
// alongside the new gate: lost evidence degrades a failed verdict even when some
// evidence remains, and keeps the more specific evidence_dropped reason.
func TestEvidenceDroppedStillDegrades(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:            caseSnapshotForVerdict(1),
		Steps:           []StepResult{passedStep(1)},
		Assertions:      []AssertionResult{failedAssertion(1, 1, "ui")},
		EvidenceDropped: 2,
		EvidenceCount:   3,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: lost evidence makes the conclusion untrustworthy", status)
	}
	if class != "" {
		t.Fatalf("failure class = %q, want empty", class)
	}
	if summary.VerdictReason != VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonEvidenceDropped)
	}
	if summary.EvidenceDropped != 2 {
		t.Fatalf("summary evidence_dropped = %d, want 2", summary.EvidenceDropped)
	}
}

// TestEvidenceDropReasonWinsOverNoEvidence pins the ordering: when both gates
// could fire (failed, evidence dropped, and zero evidence left) the drop reason
// is the more specific cause and must not be relabelled no_evidence.
func TestEvidenceDropReasonWinsOverNoEvidence(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:            caseSnapshotForVerdict(1),
		Steps:           []StepResult{passedStep(1)},
		Assertions:      []AssertionResult{failedAssertion(1, 1, "ui")},
		EvidenceDropped: 1,
		// EvidenceCount 0: every evidence item was lost.
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" {
		t.Fatalf("verdict = %s/%q, want inconclusive/empty", status, class)
	}
	if summary.VerdictReason != VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q (drop is more specific than no_evidence)",
			summary.VerdictReason, VerdictReasonEvidenceDropped)
	}
}

// TestAggregateNoEvidenceKeepsPassed guards the gate's scope: only failed
// degrades. A clean pass needs no defect evidence, so zero evidence must not
// turn a passed run inconclusive.
func TestAggregateNoEvidenceKeepsPassed(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:       caseSnapshotForVerdict(1),
		Steps:      []StepResult{passedStep(1)},
		Assertions: []AssertionResult{passedAssertion(1, 1, "ui")},
		// EvidenceCount 0.
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunPassed || class != "" {
		t.Fatalf("verdict = %s/%q, want passed/empty: the no-evidence gate is failed-only", status, class)
	}
	if summary.VerdictReason != VerdictReasonPassed {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonPassed)
	}
}

// TestAggregateNoEvidenceKeepsBlocked guards the other side of the scope: a
// blocked prerequisite already reports "cannot determine" and keeps its blocker
// signal rather than being relabelled no_evidence.
func TestAggregateNoEvidenceKeepsBlocked(t *testing.T) {
	status, class, summary, err := Aggregate(VerdictInput{
		Case:  caseSnapshotForVerdict(1),
		Steps: []StepResult{{CaseRunID: 1, Seq: 1, Status: StepBlocked, Error: "missing account"}},
		// EvidenceCount 0.
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunBlocked || class != FailureClassBlocked {
		t.Fatalf("verdict = %s/%q, want blocked/blocked", status, class)
	}
	if summary.VerdictReason != VerdictReasonBlocked {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonBlocked)
	}
}
