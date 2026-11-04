package execution

// Task 112: an issue anchors on the earliest missing-feature observation when
// the executor recorded one before (or at) the first failed assertion. That step
// is the root cause of the failure; later failed assertions are its
// consequences (FR-106-02 recorded the absent publish-time control on step 1
// while the API assertions on steps 2–3 also failed). Both the execution
// analysis candidate and the P4 issue projection use this helper so the issue
// title/category agree with the observed root cause.

// MissingFeatureAnchor returns the earliest failed/blocked step whose
// observation states a required product control/feature is absent, provided it
// is not after the first failed assertion step. A zero firstFailureStepSeq means
// there is no failed assertion. The returned pointer aliases the input slice.
func MissingFeatureAnchor(steps []StepResult, firstFailureStepSeq int) *StepResult {
	var anchor *StepResult
	for index := range steps {
		if !StepObservesMissingFeature(steps[index]) {
			continue
		}
		if firstFailureStepSeq > 0 && steps[index].Seq > firstFailureStepSeq {
			continue
		}
		if anchor == nil || steps[index].Seq < anchor.Seq {
			anchor = &steps[index]
		}
	}
	return anchor
}
