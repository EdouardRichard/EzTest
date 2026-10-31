package execution

import "testing"

func caseSnapshotForVerdict(stepSeqs ...int) CaseSnapshot {
	snapshot := CaseSnapshot{CaseID: 100, CaseVersionID: 200, DefaultVersionSUTID: 500}
	for _, seq := range stepSeqs {
		snapshot.Steps = append(snapshot.Steps, CaseStep{Seq: seq, Action: "step", TargetVersionSUTID: 500})
		snapshot.Assertions = append(snapshot.Assertions, CaseAssertion{
			StepSeq: seq, AssertionSeq: 1, Kind: "ui", Expected: "expected", TargetVersionSUTID: 500,
		})
	}
	return snapshot
}

func passedStep(seq int) StepResult { return StepResult{CaseRunID: 1, Seq: seq, Status: StepPassed} }

func failedAssertion(stepSeq, assertionSeq int, kind string) AssertionResult {
	return AssertionResult{CaseRunID: 1, StepSeq: stepSeq, AssertionSeq: assertionSeq, Kind: kind, Verdict: AssertionFailed}
}

func passedAssertion(stepSeq, assertionSeq int, kind string) AssertionResult {
	return AssertionResult{CaseRunID: 1, StepSeq: stepSeq, AssertionSeq: assertionSeq, Kind: kind, Verdict: AssertionPassed}
}

func TestAggregateFourStates(t *testing.T) {
	visualCase := CaseSnapshot{
		CaseID: 100, CaseVersionID: 200, DefaultVersionSUTID: 500,
		Steps: []CaseStep{{Seq: 1, Action: "step", TargetVersionSUTID: 500}},
		Assertions: []CaseAssertion{
			{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "expected", TargetVersionSUTID: 500},
			{StepSeq: 1, AssertionSeq: 2, Kind: "visual", Expected: "expected", TargetVersionSUTID: 500},
		},
	}
	cases := []struct {
		name          string
		in            VerdictInput
		wantStatus    CaseRunStatus
		wantClass     FailureClass
		wantReason    string
		wantVisual    int
		wantAssertion int
		wantStep      int
	}{
		{
			name: "all steps and assertions pass",
			in: VerdictInput{
				Case:       caseSnapshotForVerdict(1, 2),
				Steps:      []StepResult{passedStep(1), passedStep(2)},
				Assertions: []AssertionResult{passedAssertion(1, 1, "ui"), passedAssertion(2, 1, "ui")},
			},
			wantStatus:    CaseRunPassed,
			wantReason:    VerdictReasonPassed,
			wantAssertion: 2,
			wantStep:      2,
		},
		{
			name: "failed UI assertion is an application failure",
			in: VerdictInput{
				Case:       caseSnapshotForVerdict(1),
				Steps:      []StepResult{passedStep(1)},
				Assertions: []AssertionResult{failedAssertion(1, 1, "ui")},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassApplication,
			wantReason:    VerdictReasonAssertionFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "failed step without candidate maps to automation",
			in: VerdictInput{
				Case:  caseSnapshotForVerdict(1),
				Steps: []StepResult{{CaseRunID: 1, Seq: 1, Status: StepFailed, Error: "target_not_found"}},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassAutomation,
			wantReason:    VerdictReasonStepFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "failed step keeps a non-application candidate",
			in: VerdictInput{
				Case:   caseSnapshotForVerdict(1),
				Steps:  []StepResult{{CaseRunID: 1, Seq: 1, Status: StepFailed, Error: "connection refused"}},
				Finish: &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassEnvironment},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassEnvironment,
			wantReason:    VerdictReasonStepFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "failed step cannot be upgraded to application",
			in: VerdictInput{
				Case:   caseSnapshotForVerdict(1),
				Steps:  []StepResult{{CaseRunID: 1, Seq: 1, Status: StepFailed}},
				Finish: &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassApplication},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassAutomation,
			wantReason:    VerdictReasonStepFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "failed assertion may be downgraded to automation",
			in: VerdictInput{
				Case:       caseSnapshotForVerdict(1),
				Steps:      []StepResult{passedStep(1)},
				Assertions: []AssertionResult{failedAssertion(1, 1, "ui")},
				Finish:     &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassAutomation},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassAutomation,
			wantReason:    VerdictReasonAssertionFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "missing required step is inconclusive",
			in: VerdictInput{
				Case:       caseSnapshotForVerdict(1, 2),
				Steps:      []StepResult{passedStep(1)},
				Assertions: []AssertionResult{passedAssertion(1, 1, "ui")},
			},
			wantStatus:    CaseRunInconclusive,
			wantReason:    VerdictReasonIncomplete,
			wantAssertion: 2,
			wantStep:      2,
		},
		{
			name: "case without steps is inconclusive",
			in: VerdictInput{
				Case: caseSnapshotForVerdict(),
			},
			wantStatus: CaseRunInconclusive,
			wantReason: VerdictReasonEmptyCase,
		},
		{
			name: "candidate blocked before any result is blocked",
			in: VerdictInput{
				Case:   caseSnapshotForVerdict(1),
				Finish: &FinishCandidate{Verdict: CaseRunBlocked, FailureClass: FailureClassBlocked},
			},
			wantStatus:    CaseRunBlocked,
			wantClass:     FailureClassBlocked,
			wantReason:    VerdictReasonBlocked,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "blocked step is blocked",
			in: VerdictInput{
				Case:  caseSnapshotForVerdict(1),
				Steps: []StepResult{{CaseRunID: 1, Seq: 1, Status: StepBlocked, Error: "missing account"}},
			},
			wantStatus:    CaseRunBlocked,
			wantClass:     FailureClassBlocked,
			wantReason:    VerdictReasonBlocked,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "agent model failure is failed model",
			in: VerdictInput{
				Case:         caseSnapshotForVerdict(1),
				AgentFailure: FailureClassModel,
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassModel,
			wantReason:    VerdictReasonAgentFailure,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "agent executor loss is interrupted",
			in: VerdictInput{
				Case:         caseSnapshotForVerdict(1),
				AgentFailure: FailureClassExecutorLost,
			},
			wantStatus:    CaseRunInterrupted,
			wantClass:     FailureClassExecutorLost,
			wantReason:    VerdictReasonAgentFailure,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "visual assertion skipped still passes",
			in: VerdictInput{
				Case:       visualCase,
				Steps:      []StepResult{passedStep(1)},
				Assertions: []AssertionResult{passedAssertion(1, 1, "ui"), {CaseRunID: 1, StepSeq: 1, AssertionSeq: 2, Kind: "visual", Verdict: AssertionSkipped}},
			},
			wantStatus:    CaseRunPassed,
			wantReason:    VerdictReasonPassed,
			wantVisual:    1,
			wantAssertion: 2,
			wantStep:      1,
		},
		{
			name: "finish passed is overwritten by a failed step",
			in: VerdictInput{
				Case:   caseSnapshotForVerdict(1),
				Steps:  []StepResult{{CaseRunID: 1, Seq: 1, Status: StepFailed}},
				Finish: &FinishCandidate{Verdict: CaseRunPassed},
			},
			wantStatus:    CaseRunFailed,
			wantClass:     FailureClassAutomation,
			wantReason:    VerdictReasonStepFailed,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "application candidate without assertion or observed evidence is inconclusive",
			in: VerdictInput{
				Case:   caseSnapshotForVerdict(1),
				Finish: &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassApplication},
			},
			wantStatus:    CaseRunInconclusive,
			wantReason:    VerdictReasonIncomplete,
			wantAssertion: 1,
			wantStep:      1,
		},
		{
			name: "passed finish candidate with complete green results stays passed",
			in: VerdictInput{
				Case:       caseSnapshotForVerdict(1),
				Steps:      []StepResult{passedStep(1)},
				Assertions: []AssertionResult{passedAssertion(1, 1, "text")},
				Finish:     &FinishCandidate{Verdict: CaseRunPassed},
			},
			wantStatus:    CaseRunPassed,
			wantReason:    VerdictReasonPassed,
			wantAssertion: 1,
			wantStep:      1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, class, summary, err := Aggregate(tc.in)
			if err != nil {
				t.Fatalf("Aggregate returned error: %v", err)
			}
			if status != tc.wantStatus || class != tc.wantClass {
				t.Fatalf("got status=%s class=%s, want status=%s class=%s", status, class, tc.wantStatus, tc.wantClass)
			}
			if summary.VerdictReason != tc.wantReason {
				t.Fatalf("got verdict reason %q, want %q", summary.VerdictReason, tc.wantReason)
			}
			if summary.VisualSkipped != tc.wantVisual {
				t.Fatalf("got visual skipped %d, want %d", summary.VisualSkipped, tc.wantVisual)
			}
			if summary.AssertionTotal != tc.wantAssertion {
				t.Fatalf("got assertion total %d, want %d", summary.AssertionTotal, tc.wantAssertion)
			}
			if summary.StepTotal != tc.wantStep {
				t.Fatalf("got step total %d, want %d", summary.StepTotal, tc.wantStep)
			}
		})
	}
}

func TestAggregateCountsFailures(t *testing.T) {
	in := VerdictInput{
		Case: caseSnapshotForVerdict(1, 2),
		Steps: []StepResult{
			{CaseRunID: 1, Seq: 1, Status: StepPassed},
			{CaseRunID: 1, Seq: 2, Status: StepFailed},
		},
		Assertions: []AssertionResult{
			{CaseRunID: 1, StepSeq: 1, AssertionSeq: 1, Kind: "ui", Verdict: AssertionPassed},
			{CaseRunID: 1, StepSeq: 2, AssertionSeq: 1, Kind: "ui", Verdict: AssertionFailed},
			{CaseRunID: 1, StepSeq: 2, AssertionSeq: 2, Kind: "api", Verdict: AssertionFailed},
		},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate error: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("got %s/%s", status, class)
	}
	if summary.AssertionFailed != 2 || summary.StepFailed != 1 {
		t.Fatalf("got assertion_failed=%d step_failed=%d", summary.AssertionFailed, summary.StepFailed)
	}
}

func TestAggregateRejectsInvalidFinishStatus(t *testing.T) {
	in := VerdictInput{
		Case:   caseSnapshotForVerdict(1),
		Finish: &FinishCandidate{Verdict: CaseRunStatus("bogus")},
	}
	if _, _, _, err := Aggregate(in); err == nil {
		t.Fatal("invalid finish verdict must be rejected")
	}
}

func TestAggregateFailedStepDoesNotUpgradeToApplication(t *testing.T) {
	in := VerdictInput{
		Case: caseSnapshotForVerdict(1),
		Steps: []StepResult{
			{CaseRunID: 1, Seq: 1, Status: StepFailed, Observed: map[string]any{"output": "boom"}},
		},
		Finish: &FinishCandidate{Verdict: CaseRunFailed, FailureClass: FailureClassApplication},
	}
	status, class, _, err := Aggregate(in)
	if err != nil || status != CaseRunFailed || class != FailureClassAutomation {
		t.Fatalf("got %s/%s err=%v", status, class, err)
	}
}
