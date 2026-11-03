package explore_test

// Task 112: the deterministic attribution draft is the value the failure analyst
// reads. Its severity must come from the case priority (the same source as the
// P4 issue record), not from the assertion kind: a P2 API failure is minor, not
// major, so the model never sees a contradictory "upgrade" signal.

import (
	"context"
	"testing"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
)

func TestFailureAttributionSeverityUsesCasePriority(t *testing.T) {
	attribution := exploreapp.NewFailureAttribution()
	cases := []struct {
		priority string
		category string
		want     string
	}{
		{"P0", "ui", "major"},
		{"P1", "api", "major"},
		{"P2", "api", "minor"},
		{"P2", "db", "minor"},
		{"P3", "ui", "trivial"},
	}
	for _, tc := range cases {
		result, err := attribution.Classify(context.Background(), 1001, exploreapp.FailureSignal{
			CaseRunID:    9001,
			CaseTitle:    "scheduled publish",
			FailureClass: "application",
			Category:     tc.category,
			Priority:     tc.priority,
			Reason:       "missing publish-time control",
		})
		if err != nil {
			t.Fatalf("Classify(%s/%s): %v", tc.priority, tc.category, err)
		}
		if result.Severity != tc.want {
			t.Fatalf("severity for %s/%s = %q, want %q", tc.priority, tc.category, result.Severity, tc.want)
		}
		if result.Problem == nil || result.Problem.Severity != tc.want {
			t.Fatalf("problem draft for %s/%s = %+v, want severity %q", tc.priority, tc.category, result.Problem, tc.want)
		}
	}
}
