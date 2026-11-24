package version_test

// T11e-5 Part A5 (H-A22⑤): the closure DENOMINATOR rule is a domain rule, not a
// repository detail. It used to live in version/infrastructure/query_reader.go,
// where it would have become a second truth the day the domain grew its own
// countable-status authority. These assertions pin the rule where it now lives.

import (
	"slices"
	"testing"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestCountableRequirementStatusesIsTheClosureDenominator(t *testing.T) {
	got := version.CountableRequirementStatuses()
	want := []version.RequirementStatus{
		version.RequirementDraft, version.RequirementClarified, version.RequirementClosed,
	}
	if len(got) != len(want) {
		t.Fatalf("countable statuses = %v, want %v", got, want)
	}
	for i, status := range want {
		if got[i] != status {
			t.Fatalf("countable status %d = %q, want %q (declaration order)", i, got[i], status)
		}
	}
	// obsolete is deliberately NOT countable: an obsolete requirement is outside
	// the version's promised scope, so counting it would deflate closure forever.
	if slices.Contains(got, version.RequirementObsolete) {
		t.Fatal("obsolete requirements must not enter the closure denominator")
	}
	// Every countable status is a real requirement status, and the set is a strict
	// subset of the enum: a new status must be adjudicated here, not silently counted.
	all := []version.RequirementStatus{
		version.RequirementDraft, version.RequirementClarified,
		version.RequirementClosed, version.RequirementObsolete,
	}
	for _, status := range got {
		if !slices.Contains(all, status) {
			t.Fatalf("countable status %q is not a requirement status", status)
		}
	}
	if len(got) >= len(all) {
		t.Fatal("the countable set must stay a strict subset; obsolete is excluded by rule")
	}
}
