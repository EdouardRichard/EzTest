package execution

// Task 112: the issue severity has one deterministic source of truth — the case
// priority (P3-D-12). The execution analysis, the attribution draft and the P4
// issue projection must all use this rule so an Issue record can never disagree
// with the run narrative or the report.

import "testing"

func TestIssueSeverityFollowsCasePriority(t *testing.T) {
	cases := map[string]string{
		"P0":      "major",
		"P1":      "major",
		"P2":      "minor",
		"P3":      "trivial",
		"":        "trivial",
		"unknown": "trivial",
	}
	for priority, want := range cases {
		if got := IssueSeverity(priority); got != want {
			t.Fatalf("IssueSeverity(%q) = %q, want %q", priority, got, want)
		}
	}
}
