package executionapp

import "testing"

func TestClassifyVisualConfigChanged(t *testing.T) {
	classification, reason := classifyVisual(false, false, true, true, 0, 0.005)
	if classification != VisualClassificationInconclusive || reason != VisualReasonConfigChanged {
		t.Fatalf("classify = %s/%s", classification, reason)
	}
}
