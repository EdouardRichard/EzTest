package execution

import "strings"

// StepObservedSummary returns the human-readable observation the case executor
// recorded for one executed step. The server stores execution.step_record
// observed as step_result.observed.summary.
func StepObservedSummary(step StepResult) string {
	if len(step.Observed) == 0 {
		return ""
	}
	summary, _ := step.Observed["summary"].(string)
	return strings.TrimSpace(summary)
}

// FailedStepShowsRequirementDeviation reports whether a failed step carries an
// observed product behavior instead of a tool/automation error.
//
// The case executor records a failed step with an observed summary when the
// product behavior it observed contradicts the step expectation (QA Q1: the SUT
// showed "登录已过期，请重新登录" while the requirement asks for "账号或密码错误，
// 请重试"). Genuine automation failures carry the selector/target/timeout error
// in step_result.error, so they keep their automation/environment class.
//
// This is the deterministic discriminator that keeps a real requirement
// deviation on the failed(application) path; it never upgrades a step that has
// no observation at all.
func FailedStepShowsRequirementDeviation(step StepResult) bool {
	if step.Status != StepFailed {
		return false
	}
	if strings.TrimSpace(step.Error) != "" {
		return false
	}
	return StepObservedSummary(step) != ""
}
