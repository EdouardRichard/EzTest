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

func visualAssertion(stepSeq, assertionSeq int, verdict AssertionVerdict, reason string) AssertionResult {
	return AssertionResult{
		CaseRunID: 1, StepSeq: stepSeq, AssertionSeq: assertionSeq, Kind: AssertionKindVisual,
		Verdict: verdict, Reason: reason,
	}
}

func visualCaseSnapshot() CaseSnapshot {
	return CaseSnapshot{
		CaseID: 100, CaseVersionID: 200, DefaultVersionSUTID: 500,
		Steps: []CaseStep{{Seq: 1, Action: "step", TargetVersionSUTID: 500}},
		Assertions: []CaseAssertion{
			{StepSeq: 1, AssertionSeq: 1, Kind: "ui", Expected: "expected", TargetVersionSUTID: 500},
			{StepSeq: 1, AssertionSeq: 2, Kind: "visual", Expected: "expected", TargetVersionSUTID: 500},
		},
	}
}

func TestAggregateFourStates(t *testing.T) {
	cases := []struct {
		name          string
		in            VerdictInput
		wantStatus    CaseRunStatus
		wantClass     FailureClass
		wantReason    string
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
			if summary.VisualSkipped != 0 {
				t.Fatalf("visual skipped = %d, want 0 (P5 removed the false pass)", summary.VisualSkipped)
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

func TestAggregateVisualAssertions(t *testing.T) {
	cases := []struct {
		name             string
		assertions       []AssertionResult
		wantStatus       CaseRunStatus
		wantClass        FailureClass
		wantReason       string
		wantVisualPassed int
		wantVisualFailed int
		wantVisualIncon  int
	}{
		{
			name: "passed visual assertion keeps the case green",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionPassed, "visual_match"),
			},
			wantStatus:       CaseRunPassed,
			wantReason:       VerdictReasonPassed,
			wantVisualPassed: 1,
		},
		{
			name: "failed visual assertion fails the application",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionFailed, "visual_mismatch"),
			},
			wantStatus:       CaseRunFailed,
			wantClass:        FailureClassApplication,
			wantReason:       VerdictReasonAssertionFailed,
			wantVisualFailed: 1,
		},
		{
			name: "visual assertion that never ran blocks a false pass",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
			},
			wantStatus:      CaseRunInconclusive,
			wantReason:      VerdictReasonVisualAssertionNotRun,
			wantVisualIncon: 1,
		},
		{
			name: "legacy skipped visual assertion counts as not run",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionSkipped, ""),
			},
			wantStatus:      CaseRunInconclusive,
			wantReason:      VerdictReasonVisualAssertionNotRun,
			wantVisualIncon: 1,
		},
		{
			name: "missing baseline is inconclusive case",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionInconclusive, "missing_visual_baseline"),
			},
			wantStatus:      CaseRunInconclusive,
			wantReason:      "missing_visual_baseline",
			wantVisualIncon: 1,
		},
		{
			name: "environment mismatch is inconclusive environment",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionInconclusive, "visual_environment_mismatch"),
			},
			wantStatus:      CaseRunInconclusive,
			wantClass:       FailureClassEnvironment,
			wantReason:      "visual_environment_mismatch",
			wantVisualIncon: 1,
		},
		{
			name: "config change is inconclusive case",
			assertions: []AssertionResult{
				passedAssertion(1, 1, "ui"),
				visualAssertion(1, 2, AssertionInconclusive, "visual_config_changed"),
			},
			wantStatus:      CaseRunInconclusive,
			wantReason:      "visual_config_changed",
			wantVisualIncon: 1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, class, summary, err := Aggregate(VerdictInput{
				Case:       visualCaseSnapshot(),
				Steps:      []StepResult{passedStep(1)},
				Assertions: tc.assertions,
			})
			if err != nil {
				t.Fatalf("Aggregate: %v", err)
			}
			if status != tc.wantStatus || class != tc.wantClass || summary.VerdictReason != tc.wantReason {
				t.Fatalf("got %s/%s reason=%q, want %s/%s reason=%q",
					status, class, summary.VerdictReason, tc.wantStatus, tc.wantClass, tc.wantReason)
			}
			if summary.VisualPassed != tc.wantVisualPassed ||
				summary.VisualFailed != tc.wantVisualFailed ||
				summary.VisualInconclusive != tc.wantVisualIncon {
				t.Fatalf("visual counts = %d/%d/%d, want %d/%d/%d",
					summary.VisualPassed, summary.VisualFailed, summary.VisualInconclusive,
					tc.wantVisualPassed, tc.wantVisualFailed, tc.wantVisualIncon)
			}
			if summary.VisualSkipped != 0 {
				t.Fatalf("visual skipped = %d, want 0", summary.VisualSkipped)
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

func TestVisualMissingBaselineBlockedStepIsInconclusive(t *testing.T) {
	in := VerdictInput{
		Case: visualCaseSnapshot(),
		Steps: []StepResult{
			{Seq: 1, Status: StepBlocked, Error: "missing_visual_baseline: no approved baseline available"},
		},
		Assertions: []AssertionResult{
			passedAssertion(1, 1, "ui"),
			visualAssertion(1, 2, AssertionInconclusive, "missing_visual_baseline"),
		},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunInconclusive || class != "" || summary.VerdictReason != "missing_visual_baseline" {
		t.Fatalf("got %s/%s reason=%q", status, class, summary.VerdictReason)
	}
	if summary.VisualInconclusive != 1 {
		t.Fatalf("visual inconclusive = %d", summary.VisualInconclusive)
	}
}

func TestVisualResultOverridesAgentBlocked(t *testing.T) {
	in := VerdictInput{
		Case:         visualCaseSnapshot(),
		AgentFailure: FailureClassBlocked,
		Steps:        []StepResult{{Seq: 1, Status: StepBlocked, Error: "missing_visual_baseline"}},
		Assertions: []AssertionResult{
			passedAssertion(1, 1, "ui"),
			visualAssertion(1, 2, AssertionInconclusive, "missing_visual_baseline"),
		},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil || status != CaseRunInconclusive || class != "" || summary.VerdictReason != "missing_visual_baseline" {
		t.Fatalf("got %s/%s reason=%q err=%v", status, class, summary.VerdictReason, err)
	}
}

func TestVisualEnvironmentOverridesAgentFailure(t *testing.T) {
	in := VerdictInput{
		Case:         visualCaseSnapshot(),
		AgentFailure: FailureClassEnvironment,
		Steps:        []StepResult{{Seq: 1, Status: StepFailed}},
		Assertions: []AssertionResult{
			passedAssertion(1, 1, "ui"),
			visualAssertion(1, 2, AssertionInconclusive, "visual_environment_mismatch"),
		},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil || status != CaseRunInconclusive || class != FailureClassEnvironment || summary.VerdictReason != "visual_environment_mismatch" {
		t.Fatalf("got %s/%s reason=%q err=%v", status, class, summary.VerdictReason, err)
	}
}

func TestApplyVisualOutcomeOverridesBlocked(t *testing.T) {
	summary := CaseRunSummary{VerdictReason: VerdictReasonBlocked, VisualInconclusive: 1}
	status, class, got := ApplyVisualOutcome(CaseRunBlocked, FailureClassBlocked, summary, []AssertionResult{
		visualAssertion(1, 1, AssertionInconclusive, "missing_visual_baseline"),
	})
	if status != CaseRunInconclusive || class != "" || got.VerdictReason != "missing_visual_baseline" {
		t.Fatalf("got %s/%s reason=%q", status, class, got.VerdictReason)
	}
}

func TestApplyVisualOutcomeOverridesFailedEnvironment(t *testing.T) {
	summary := CaseRunSummary{VerdictReason: VerdictReasonStepFailed, VisualInconclusive: 1}
	status, class, got := ApplyVisualOutcome(CaseRunFailed, FailureClassEnvironment, summary, []AssertionResult{
		visualAssertion(1, 1, AssertionInconclusive, "visual_environment_mismatch"),
	})
	if status != CaseRunInconclusive || class != FailureClassEnvironment || got.VerdictReason != "visual_environment_mismatch" {
		t.Fatalf("got %s/%s reason=%q", status, class, got.VerdictReason)
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
