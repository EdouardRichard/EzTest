package execution

// V-0022 / SC-F9: the reclassification (Prior) and agent-level settlement
// (AgentSettlement) branches of the single verdict authority, the sanctioned
// terminal->terminal reclassification rule, and the derived terminal status set.

import "testing"

func reclassifyInput(prior PriorVerdict, steps []StepResult, assertions []AssertionResult, evidenceCount int) VerdictInput {
	return VerdictInput{
		Prior:         &prior,
		Steps:         steps,
		Assertions:    assertions,
		EvidenceCount: evidenceCount,
	}
}

func TestReclassifyAssertionFailureIsApplication(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunBlocked, FailureClass: FailureClassBlocked}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	status, class, summary, err := Aggregate(reclassifyInput(prior, nil, assertions, 3))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("verdict = %s/%s, want failed/application (recorded assertion outranks blocked)", status, class)
	}
	if summary.VerdictReason != VerdictReasonAssertionFailed {
		t.Fatalf("verdict reason = %q, want assertion_failed", summary.VerdictReason)
	}
	if summary.AssertionFailed != 1 || summary.AssertionTotal != 1 || summary.EvidenceCount != 3 {
		t.Fatalf("summary = %+v, want recorded totals and live evidence count", summary)
	}
}

func TestReclassifyAutomationClassPreserved(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassAutomation}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	status, class, _, err := Aggregate(reclassifyInput(prior, nil, assertions, 1))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassAutomation {
		t.Fatalf("verdict = %s/%s, want failed/automation (prior automation downgrade preserved)", status, class)
	}
}

func TestReclassifyDeviationBlockedStepIsApplication(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunBlocked, FailureClass: FailureClassBlocked}
	steps := []StepResult{
		{Seq: 1, Status: StepBlocked, Observed: map[string]any{"summary": "详情页没有任何点赞按钮/点赞计数元素，点赞功能不存在，无法执行点击操作。"}},
	}
	status, class, summary, err := Aggregate(reclassifyInput(prior, steps, nil, 14))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("verdict = %s/%s, want failed/application (observed missing feature is application evidence)", status, class)
	}
	if summary.VerdictReason != VerdictReasonStepFailed {
		t.Fatalf("verdict reason = %q, want step_failed", summary.VerdictReason)
	}
}

func TestReclassifyPureBlockedStaysBlocked(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunBlocked, FailureClass: FailureClassBlocked}
	steps := []StepResult{
		{Seq: 1, Status: StepPassed},
		{Seq: 2, Status: StepBlocked, Observed: map[string]any{"summary": "执行在该步中断（token 预算耗尽），未提交表单。"}},
	}
	status, class, summary, err := Aggregate(reclassifyInput(prior, steps, nil, 2))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunBlocked || class != FailureClassBlocked {
		t.Fatalf("verdict = %s/%s, want blocked/blocked (a budget stop is not product evidence)", status, class)
	}
	if summary.VerdictReason != VerdictReasonBlocked {
		t.Fatalf("verdict reason = %q, want blocked", summary.VerdictReason)
	}
}

func TestReclassifyNoEvidenceDegradesFailed(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassApplication}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	status, class, summary, err := Aggregate(reclassifyInput(prior, nil, assertions, 0))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" {
		t.Fatalf("verdict = %s/%q, want inconclusive with no class (art.1.2 evidence gate)", status, class)
	}
	if summary.VerdictReason != VerdictReasonNoEvidence {
		t.Fatalf("verdict reason = %q, want no_evidence", summary.VerdictReason)
	}
}

func TestReclassifyEvidenceDroppedDegrades(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassApplication}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	in := reclassifyInput(prior, nil, assertions, 5)
	in.EvidenceDropped = 2
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" {
		t.Fatalf("verdict = %s/%q, want inconclusive (art.4.5 dropped-evidence gate)", status, class)
	}
	if summary.VerdictReason != VerdictReasonEvidenceDropped {
		t.Fatalf("verdict reason = %q, want evidence_dropped (more specific than no_evidence)", summary.VerdictReason)
	}
}

func TestReclassifyNoFailureEvidenceKeepsValidPriorClass(t *testing.T) {
	// A recovery-settled infra failure with recorded evidence but no failing
	// result keeps its valid non-application class; a claimed application
	// failure without evidence degrades inconclusive (P3-D-12).
	prior := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassEnvironment}
	status, class, _, err := Aggregate(reclassifyInput(prior, nil, nil, 1))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassEnvironment {
		t.Fatalf("verdict = %s/%s, want failed/environment preserved", status, class)
	}
	application := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassApplication}
	status, class, summary, err := Aggregate(reclassifyInput(application, nil, nil, 1))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" || summary.VerdictReason != VerdictReasonIncomplete {
		t.Fatalf("verdict = %s/%q/%q, want inconclusive/incomplete for an unproven application claim", status, class, summary.VerdictReason)
	}
}

func TestReclassifyLimitationWinsOverAssertion(t *testing.T) {
	prior := PriorVerdict{Status: CaseRunFailed, FailureClass: FailureClassApplication}
	steps := []StepResult{
		{Seq: 1, Status: StepFailed, Observed: map[string]any{ExecutorLimitationKey: map[string]any{"code": "file_chooser_unavailable"}}},
	}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	status, class, summary, err := Aggregate(reclassifyInput(prior, steps, assertions, 3))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunBlocked || class != FailureClassBlocked {
		t.Fatalf("verdict = %s/%s, want blocked/blocked (X-02 limitation is never a SUT defect)", status, class)
	}
	if summary.TerminalCause != TerminalCauseExecutorCapability || summary.NextAction != NextActionResolveBlocker {
		t.Fatalf("summary cause/action = %q/%q, want executor_capability/resolve_blocker", summary.TerminalCause, summary.NextAction)
	}
}

func TestReclassifyPreservesPriorSummaryFields(t *testing.T) {
	prior := PriorVerdict{
		Status:       CaseRunFailed,
		FailureClass: FailureClassApplication,
		Summary:      CaseRunSummary{LastError: "observed deviation", TerminalCause: "assertion_failed", NextAction: NextActionReviewFailure},
	}
	assertions := []AssertionResult{{StepSeq: 1, AssertionSeq: 1, Verdict: AssertionFailed}}
	_, _, summary, err := Aggregate(reclassifyInput(prior, nil, assertions, 2))
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if summary.LastError != "observed deviation" || summary.TerminalCause != "assertion_failed" || summary.NextAction != NextActionReviewFailure {
		t.Fatalf("summary = %+v, want the prior explanation fields preserved", summary)
	}
}

func TestAgentSettlementMapping(t *testing.T) {
	for _, tc := range []struct {
		class  FailureClass
		reason string
	}{
		{FailureClassSession, VerdictReasonHumanTimeout},
		{FailureClassSession, VerdictReasonHumanCancelled},
		{FailureClassExecutorLost, VerdictReasonAgentFailure},
		{FailureClassEnvironment, VerdictReasonAgentFailure},
		{FailureClassTimeout, VerdictReasonAgentFailure},
		{FailureClassBlocked, VerdictReasonAgentFailure},
	} {
		in := VerdictInput{
			AgentSettlement: &AgentSettlement{Class: tc.class, Reason: tc.reason, Summary: CaseRunSummary{LastError: "cause"}},
			EvidenceCount:   1,
		}
		status, class, summary, err := Aggregate(in)
		if err != nil {
			t.Fatalf("Aggregate(%s): %v", tc.class, err)
		}
		if status != CaseRunFailed || class != tc.class {
			t.Fatalf("verdict = %s/%s, want failed/%s", status, class, tc.class)
		}
		if summary.VerdictReason != tc.reason || summary.LastError != "cause" {
			t.Fatalf("summary = %+v, want reason %q and preserved last error", summary, tc.reason)
		}
	}
}

func TestAgentSettlementRejectsApplicationClass(t *testing.T) {
	in := VerdictInput{
		AgentSettlement: &AgentSettlement{Class: FailureClassApplication, Reason: VerdictReasonAgentFailure},
		EvidenceCount:   1,
	}
	if _, _, _, err := Aggregate(in); err == nil {
		t.Fatal("application class must be rejected: it requires assertion evidence the analysis fallback owns")
	}
}

func TestAgentSettlementNoEvidenceDegrades(t *testing.T) {
	in := VerdictInput{
		AgentSettlement: &AgentSettlement{Class: FailureClassSession, Reason: VerdictReasonHumanTimeout},
		EvidenceCount:   0,
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" || summary.VerdictReason != VerdictReasonNoEvidence {
		t.Fatalf("verdict = %s/%q/%q, want inconclusive/no_evidence (art.1.2)", status, class, summary.VerdictReason)
	}
}

func TestCanReclassifyTo(t *testing.T) {
	reclassifiable := []CaseRunStatus{CaseRunFailed, CaseRunBlocked, CaseRunInconclusive}
	for _, from := range reclassifiable {
		for _, to := range reclassifiable {
			if !from.CanReclassifyTo(to) {
				t.Errorf("%s -> %s must be reclassifiable (same status included for class refinement)", from, to)
			}
		}
		for _, to := range []CaseRunStatus{CaseRunPassed, CaseRunSkipped, CaseRunSuperseded, CaseRunPending, CaseRunRunning, CaseRunPaused, CaseRunInterrupted} {
			if from.CanReclassifyTo(to) {
				t.Errorf("%s -> %s must NOT be reclassifiable", from, to)
			}
		}
	}
	for _, from := range []CaseRunStatus{CaseRunPassed, CaseRunSkipped, CaseRunSuperseded, CaseRunPending, CaseRunRunning, CaseRunPaused, CaseRunInterrupted} {
		for _, to := range reclassifiable {
			if from.CanReclassifyTo(to) {
				t.Errorf("%s -> %s must NOT be reclassifiable (source is not a failure verdict)", from, to)
			}
		}
	}
}

func TestTerminalStatusSetsConsistent(t *testing.T) {
	terminal := TerminalStatuses()
	reclassifiable := ReclassifiableStatuses()
	all := []CaseRunStatus{
		CaseRunPending, CaseRunRunning, CaseRunPaused, CaseRunPassed, CaseRunFailed,
		CaseRunBlocked, CaseRunInconclusive, CaseRunSkipped, CaseRunInterrupted, CaseRunSuperseded,
		CaseRunStatus("bogus"),
	}
	contains := func(set []CaseRunStatus, s CaseRunStatus) bool {
		for _, item := range set {
			if item == s {
				return true
			}
		}
		return false
	}
	for _, status := range all {
		if status.Terminal() != contains(terminal, status) {
			t.Errorf("%s: Terminal() disagrees with TerminalStatuses()", status)
		}
	}
	for _, status := range reclassifiable {
		if !contains(terminal, status) {
			t.Errorf("%s: reclassifiable set must be a subset of the terminal set", status)
		}
		if !status.CanReclassifyTo(status) {
			t.Errorf("%s: every reclassifiable status must allow same-status refinement", status)
		}
	}
	// The returned slices are copies: mutating them must not corrupt the authority.
	terminal[0] = CaseRunPending
	if !CaseRunPassed.Terminal() {
		t.Fatal("TerminalStatuses() must return a copy of the authoritative set")
	}
}
