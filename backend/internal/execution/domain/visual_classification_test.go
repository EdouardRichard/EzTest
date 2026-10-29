package domain_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// TestClassifyVisualFourStates pins the design spec §9.3 result mapping:
// missing baseline and environment mismatches are inconclusive (never a false
// failure), a diff above the threshold is an application failure and a diff at
// or below the threshold passes.
func TestClassifyVisualFourStates(t *testing.T) {
	tests := []struct {
		name        string
		hasBaseline bool
		environment bool
		diffRatio   float64
		threshold   float64
		wantVerdict domain.Verdict
		wantClass   domain.FailureClass
		wantReason  string
	}{
		{
			name: "no baseline is inconclusive case", hasBaseline: false, environment: true,
			diffRatio: 0, threshold: 0.01,
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "missing_visual_baseline",
		},
		{
			name: "no baseline wins over an environment mismatch", hasBaseline: false, environment: false,
			diffRatio: 1, threshold: 0.01,
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassCase,
			wantReason: "missing_visual_baseline",
		},
		{
			name: "environment mismatch is inconclusive environment", hasBaseline: true, environment: false,
			diffRatio: 0.9, threshold: 0.01,
			wantVerdict: domain.VerdictInconclusive, wantClass: domain.FailureClassEnvironment,
			wantReason: "visual_environment_mismatch",
		},
		{
			name: "diff above threshold fails the application", hasBaseline: true, environment: true,
			diffRatio: 0.02, threshold: 0.01,
			wantVerdict: domain.VerdictFailed, wantClass: domain.FailureClassApplication,
			wantReason: "visual_mismatch",
		},
		{
			name: "diff equal to threshold passes", hasBaseline: true, environment: true,
			diffRatio: 0.01, threshold: 0.01,
			wantVerdict: domain.VerdictPassed, wantClass: "", wantReason: "visual_match",
		},
		{
			name: "diff below threshold passes", hasBaseline: true, environment: true,
			diffRatio: 0.0001, threshold: 0.005,
			wantVerdict: domain.VerdictPassed, wantClass: "", wantReason: "visual_match",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			verdict, class, reason := domain.ClassifyVisual(
				testCase.hasBaseline, testCase.environment, testCase.diffRatio, testCase.threshold)
			if verdict != testCase.wantVerdict || class != testCase.wantClass || reason != testCase.wantReason {
				t.Fatalf("ClassifyVisual = (%s, %s, %q), want (%s, %s, %q)",
					verdict, class, reason, testCase.wantVerdict, testCase.wantClass, testCase.wantReason)
			}
		})
	}
}

// TestClassifyVisualToolCodes proves the visual tool codes flow through the
// generic terminal classifier with the same four-state semantics.
func TestClassifyVisualToolCodes(t *testing.T) {
	verdict, class, reason := domain.Classify("missing_visual_baseline", "", 0)
	if verdict != domain.VerdictInconclusive || class != domain.FailureClassCase || reason != "missing_visual_baseline" {
		t.Fatalf("missing baseline = (%s, %s, %q)", verdict, class, reason)
	}

	verdict, class, reason = domain.Classify("visual_environment_mismatch", "", 0)
	if verdict != domain.VerdictInconclusive || class != domain.FailureClassEnvironment || reason != "visual_environment_mismatch" {
		t.Fatalf("environment mismatch = (%s, %s, %q)", verdict, class, reason)
	}

	verdict, class, reason = domain.Classify("visual_mismatch", "", 2)
	if verdict != domain.VerdictFailed || class != domain.FailureClassApplication || reason != "visual_mismatch" {
		t.Fatalf("visual mismatch with evidence = (%s, %s, %q)", verdict, class, reason)
	}

	verdict, class, reason = domain.Classify("visual_mismatch", "", 0)
	if verdict != domain.VerdictInconclusive || class != domain.FailureClassCase || reason != "evidence_missing" {
		t.Fatalf("visual mismatch without evidence = (%s, %s, %q)", verdict, class, reason)
	}
}
