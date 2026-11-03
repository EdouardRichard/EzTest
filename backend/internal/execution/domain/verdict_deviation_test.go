package execution

// QA Q1 reproduction at the deterministic verdict boundary: the case executor
// recorded step 2 as failed with an observed SUT text that contradicts the
// requirement ("登录已过期，请重新登录" vs "账号或密码错误，请重试") and no
// automation/tool error. The verdict must be failed(application), not
// failed(automation); a genuine selector/target error must stay automation.

import "testing"

func TestFailedStepRequirementDeviationIsApplication(t *testing.T) {
	in := VerdictInput{
		Case: caseSnapshotForVerdict(1, 2),
		Steps: []StepResult{
			passedStep(1),
			{
				CaseRunID: 1, Seq: 2, Status: StepFailed,
				Observed: map[string]any{
					"summary": "弹窗内展示的提示文案为「登录已过期，请重新登录」，与需求要求的统一提示「账号或密码错误，请重试」不一致。",
				},
			},
		},
		Assertions: []AssertionResult{passedAssertion(1, 1, "ui")},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassApplication {
		t.Fatalf("verdict = %s/%q, want failed/application", status, class)
	}
	if summary.VerdictReason != VerdictReasonStepFailed {
		t.Fatalf("verdict reason = %q, want %q", summary.VerdictReason, VerdictReasonStepFailed)
	}
	if summary.StepFailed != 1 || summary.StepTotal != 2 {
		t.Fatalf("summary = %+v", summary)
	}
}

func TestFailedStepAutomationStaysAutomation(t *testing.T) {
	in := VerdictInput{
		Case:  caseSnapshotForVerdict(1),
		Steps: []StepResult{{CaseRunID: 1, Seq: 1, Status: StepFailed, Error: "target_not_found: selector #submit"}},
	}
	status, class, summary, err := Aggregate(in)
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if status != CaseRunFailed || class != FailureClassAutomation {
		t.Fatalf("verdict = %s/%q, want failed/automation", status, class)
	}
	if summary.VerdictReason != VerdictReasonStepFailed {
		t.Fatalf("verdict reason = %q", summary.VerdictReason)
	}
}
