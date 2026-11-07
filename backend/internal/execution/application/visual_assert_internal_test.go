package executionapp

import "testing"

func TestClassifyVisualConfigChanged(t *testing.T) {
	classification, reason := classifyVisual(false, false, true, true, 0, 0.005)
	if classification != VisualClassificationInconclusive || reason != VisualReasonConfigChanged {
		t.Fatalf("classify = %s/%s", classification, reason)
	}
}

// TestW2VisualThresholdBoundary pins the W2 threshold effect: the configured
// 0.02 threshold is strict (ratio > threshold fails, ratio == threshold passes)
// and the non-measurable outcomes stay inconclusive with their stable reason.
func TestW2VisualThresholdBoundary(t *testing.T) {
	cases := []struct {
		name             string
		hasBaseline      bool
		baselineInvalid  bool
		configChanged    bool
		environmentMatch bool
		ratio            float64
		threshold        float64
		wantClass        string
		wantReason       string
	}{
		{"below threshold", true, false, false, true, 0.019, 0.02, VisualClassificationPassed, VisualReasonMatch},
		{"equal threshold", true, false, false, true, 0.02, 0.02, VisualClassificationPassed, VisualReasonMatch},
		{"above threshold", true, false, false, true, 0.0201, 0.02, VisualClassificationFailed, VisualReasonMismatch},
		{"missing baseline", false, false, false, true, 0, 0.02, VisualClassificationInconclusive, VisualReasonMissingBase},
		{"environment mismatch", true, false, false, false, 0, 0.02, VisualClassificationInconclusive, VisualReasonEnvironment},
		{"baseline invalid", true, true, false, true, 0, 0.02, VisualClassificationInconclusive, VisualReasonBaseInvalid},
		{"config changed", false, false, true, true, 0, 0.02, VisualClassificationInconclusive, VisualReasonConfigChanged},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			classification, reason := classifyVisual(tc.hasBaseline, tc.baselineInvalid, tc.configChanged, tc.environmentMatch, tc.ratio, tc.threshold)
			if classification != tc.wantClass || reason != tc.wantReason {
				t.Fatalf("classify = %s/%s, want %s/%s", classification, reason, tc.wantClass, tc.wantReason)
			}
		})
	}
}
