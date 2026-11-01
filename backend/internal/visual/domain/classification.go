package visual

// Classification is the four-state visual comparison result persisted in
// visual_diff.classification (spec §7.3). blocked is only set by the execution
// error path: a missing baseline or an environment mismatch is inconclusive, not
// failed.
type Classification string

const (
	ClassificationPassed       Classification = "passed"
	ClassificationFailed       Classification = "failed"
	ClassificationInconclusive Classification = "inconclusive"
	ClassificationBlocked      Classification = "blocked"
)

// Stable visual_diff.reason / case summary verdict reasons.
const (
	ReasonVisualMatch         = "visual_match"
	ReasonVisualMismatch      = "visual_mismatch"
	ReasonMissingBaseline     = "missing_visual_baseline"
	ReasonEnvironmentMismatch = "visual_environment_mismatch"
	ReasonConfigChanged       = "visual_config_changed"
	ReasonBaselineInvalid     = "visual_baseline_invalid"
	ReasonActualInvalid       = "visual_actual_invalid"
	ReasonAssertionNotRun     = "visual_assertion_not_run"
)

// Valid reports whether class is one of the four persisted classifications.
func (c Classification) Valid() bool {
	switch c {
	case ClassificationPassed, ClassificationFailed, ClassificationInconclusive, ClassificationBlocked:
		return true
	default:
		return false
	}
}

// Classify implements the fixed spec §7.3 order: no baseline or an environment
// mismatch is inconclusive before any pixel ratio is considered; a diff ratio
// strictly above the threshold fails; everything else passes.
func Classify(envMatch, hasBaseline bool, diffRatio, threshold float64) Classification {
	if !hasBaseline || !envMatch {
		return ClassificationInconclusive
	}
	if diffRatio > threshold {
		return ClassificationFailed
	}
	return ClassificationPassed
}

// ClassificationReason maps a passed/failed comparison to its stable reason.
func ClassificationReason(class Classification) string {
	switch class {
	case ClassificationPassed:
		return ReasonVisualMatch
	case ClassificationFailed:
		return ReasonVisualMismatch
	default:
		return ""
	}
}

// InconclusiveReason maps a pre-comparison condition to its stable reason.
func InconclusiveReason(hasBaseline, envMatch bool) string {
	if !hasBaseline {
		return ReasonMissingBaseline
	}
	if !envMatch {
		return ReasonEnvironmentMismatch
	}
	return ""
}
