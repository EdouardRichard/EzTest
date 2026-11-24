package execution

import "testing"

// Constitution art.4.5: evidence that was dropped makes the conclusion
// untrustworthy, so the deterministic verdict degrades to inconclusive. The
// judgment lives in the aggregation (single authority), never in a caller.
func dropVerdictCase() CaseSnapshot {
	return CaseSnapshot{
		Steps:      []CaseStep{{Seq: 1, Action: "open"}},
		Assertions: []CaseAssertion{{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "ok"}},
	}
}

func passedResults() ([]StepResult, []AssertionResult) {
	return []StepResult{{Seq: 1, Status: StepPassed}},
		[]AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionPassed}}
}

func TestAggregateDegradesPassedWhenEvidenceWasDropped(t *testing.T) {
	steps, assertions := passedResults()
	status, class, summary, err := Aggregate(VerdictInput{
		Case:            dropVerdictCase(),
		Steps:           steps,
		Assertions:      assertions,
		EvidenceDropped: 2,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive when evidence was dropped", status)
	}
	if class != "" {
		t.Fatalf("failure class = %q, want empty (an inconclusive run must not create an issue)", class)
	}
	if summary.VerdictReason != VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonEvidenceDropped)
	}
	if summary.EvidenceDropped != 2 {
		t.Fatalf("summary evidence_dropped = %d, want 2", summary.EvidenceDropped)
	}
}

func TestAggregateDegradesFailedWhenEvidenceWasDropped(t *testing.T) {
	steps, assertions := passedResults()
	assertions[0].Verdict = AssertionFailed
	status, class, summary, err := Aggregate(VerdictInput{
		Case:            dropVerdictCase(),
		Steps:           steps,
		Assertions:      assertions,
		EvidenceDropped: 1,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive {
		t.Fatalf("status = %q, want inconclusive: a failed verdict without complete evidence is not trustworthy", status)
	}
	if class != "" {
		t.Fatalf("failure class = %q, want empty", class)
	}
	if summary.VerdictReason != VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonEvidenceDropped)
	}
}

func TestAggregateKeepsBlockedAndProcessStatesWhenEvidenceWasDropped(t *testing.T) {
	blocked, _, blockedSummary, err := Aggregate(VerdictInput{
		Case:            dropVerdictCase(),
		Steps:           []StepResult{{Seq: 1, Status: StepBlocked}},
		EvidenceDropped: 3,
	})
	if err != nil {
		t.Fatalf("Aggregate blocked: %v", err)
	}
	if blocked != CaseRunBlocked {
		t.Fatalf("blocked status = %q, want blocked (a missing prerequisite is already not a conclusion)", blocked)
	}
	if blockedSummary.EvidenceDropped != 3 {
		t.Fatalf("blocked summary must still record the drop count, got %d", blockedSummary.EvidenceDropped)
	}
	interrupted, class, _, err := Aggregate(VerdictInput{
		Case:            dropVerdictCase(),
		AgentFailure:    FailureClassExecutorLost,
		EvidenceDropped: 1,
	})
	if err != nil {
		t.Fatalf("Aggregate interrupted: %v", err)
	}
	if interrupted != CaseRunInterrupted || class != FailureClassExecutorLost {
		t.Fatalf("executor loss = %q/%q, want interrupted/executor_lost (a process state is redispatched, not degraded)", interrupted, class)
	}
}

func TestAggregateWithoutDropsIsUnchanged(t *testing.T) {
	steps, assertions := passedResults()
	status, _, summary, err := Aggregate(VerdictInput{
		Case:       dropVerdictCase(),
		Steps:      steps,
		Assertions: assertions,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunPassed || summary.VerdictReason != VerdictReasonPassed {
		t.Fatalf("status/reason = %q/%q, want passed/passed", status, summary.VerdictReason)
	}
	if summary.EvidenceDropped != 0 {
		t.Fatalf("evidence_dropped = %d, want 0", summary.EvidenceDropped)
	}
}
