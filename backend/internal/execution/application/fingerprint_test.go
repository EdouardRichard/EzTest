package executionapp

import (
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

func testBinding() VersionBinding {
	return VersionBinding{
		VersionID: 7,
		Status:    "running",
		SUTs: []VersionSUTBinding{
			{
				VersionSUTID:    5001,
				SUTID:           100,
				EnvironmentID:   200,
				SUTName:         "Order Center",
				EnvironmentName: "staging",
				BaseURL:         "http://127.0.0.1:13335",
				PageIDs:         []int64{11, 12},
				APIIDs:          []int64{21},
				DatabaseIDs:     []int64{31},
				AccountRoles:    []string{"admin", "user"},
			},
			{
				VersionSUTID:    5002,
				SUTID:           101,
				EnvironmentID:   201,
				SUTName:         "Payment Gateway",
				EnvironmentName: "staging",
				BaseURL:         "http://127.0.0.1:13337",
				PageIDs:         []int64{41},
				AccountRoles:    []string{"user"},
			},
		},
	}
}

func TestEnvironmentFingerprintStableAndChanges(t *testing.T) {
	binding := testBinding()
	first, err := EnvironmentFingerprint(binding)
	if err != nil {
		t.Fatalf("fingerprint error: %v", err)
	}
	if len(first) != 64 {
		t.Fatalf("fingerprint must be 64 hex characters, got %d", len(first))
	}
	second, err := EnvironmentFingerprint(binding)
	if err != nil || second != first {
		t.Fatalf("fingerprint must be deterministic: %v %q vs %q", err, first, second)
	}

	// Order of SUTs and of IDs must not change the fingerprint.
	reordered := testBinding()
	reordered.SUTs[0], reordered.SUTs[1] = reordered.SUTs[1], reordered.SUTs[0]
	reordered.SUTs[1].PageIDs = []int64{12, 11}
	reordered.SUTs[1].AccountRoles = []string{"user", "admin"}
	reorderedHash, err := EnvironmentFingerprint(reordered)
	if err != nil || reorderedHash != first {
		t.Fatalf("order must be normalized, got %q vs %q err=%v", reorderedHash, first, err)
	}

	changes := []struct {
		name   string
		mutate func(*VersionBinding)
	}{
		{"base_url", func(b *VersionBinding) { b.SUTs[0].BaseURL = "http://127.0.0.1:14000" }},
		{"environment", func(b *VersionBinding) { b.SUTs[0].EnvironmentID = 999 }},
		{"sut", func(b *VersionBinding) { b.SUTs[0].SUTID = 999 }},
		{"page", func(b *VersionBinding) { b.SUTs[0].PageIDs = []int64{11, 12, 13} }},
		{"api", func(b *VersionBinding) { b.SUTs[0].APIIDs = []int64{22} }},
		{"database", func(b *VersionBinding) { b.SUTs[0].DatabaseIDs = []int64{32} }},
		{"account_roles", func(b *VersionBinding) { b.SUTs[0].AccountRoles = []string{"admin"} }},
		{"suts", func(b *VersionBinding) { b.SUTs = b.SUTs[:1] }},
	}
	for _, change := range changes {
		changed := testBinding()
		change.mutate(&changed)
		hash, err := EnvironmentFingerprint(changed)
		if err != nil {
			t.Fatalf("%s: fingerprint error: %v", change.name, err)
		}
		if hash == first {
			t.Errorf("%s change must produce a new fingerprint", change.name)
		}
	}
}

func TestEnvironmentFingerprintRejectsEmptyBinding(t *testing.T) {
	if _, err := EnvironmentFingerprint(VersionBinding{VersionID: 1}); err == nil {
		t.Fatal("binding without version SUTs must be rejected")
	}
}

func TestCodeBaseline(t *testing.T) {
	empty := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{{VersionSUTID: 5001}}}
	if baseline := CodeBaseline(empty); baseline != "" {
		t.Fatalf("no code repo must produce an empty baseline, got %q", baseline)
	}

	repoA := int64(900)
	repoB := int64(901)
	binding := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release"},
	}}
	two := CodeBaseline(binding)
	if len(two) != 64 {
		t.Fatalf("expected a 64 hex baseline, got %q", two)
	}

	reordered := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release"},
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
	}}
	if got := CodeBaseline(reordered); got != two {
		t.Fatalf("baseline must not depend on binding order: %q vs %q", got, two)
	}

	sameRepoTwice := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "main"},
	}}
	one := CodeBaseline(sameRepoTwice)
	singleRepo := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"}}}
	if got := CodeBaseline(singleRepo); got != one {
		t.Fatalf("duplicated repo must be deduplicated: %q vs %q", got, one)
	}

	otherBranch := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "dev"}}}
	if got := CodeBaseline(otherBranch); got == one {
		t.Fatal("branch change must change the baseline")
	}
}

func TestDedupeKey(t *testing.T) {
	key := DedupeKey(300, "fingerprint", "baseline", execution.PurposeInitial)
	if len(key) != len("exec:")+32 {
		t.Fatalf("dedupe key must be exec:+32 hex chars, got %d (%q)", len(key), key)
	}
	if key != DedupeKey(300, "fingerprint", "baseline", execution.PurposeInitial) {
		t.Fatal("dedupe key must be deterministic")
	}
	cases := []struct {
		name string
		key  string
	}{
		{"case", DedupeKey(301, "fingerprint", "baseline", execution.PurposeInitial)},
		{"fingerprint", DedupeKey(300, "other", "baseline", execution.PurposeInitial)},
		{"baseline", DedupeKey(300, "fingerprint", "other", execution.PurposeInitial)},
		{"purpose", DedupeKey(300, "fingerprint", "baseline", execution.PurposeRetest)},
	}
	for _, tc := range cases {
		if tc.key == key {
			t.Errorf("%s change must change the dedupe key", tc.name)
		}
	}
}
