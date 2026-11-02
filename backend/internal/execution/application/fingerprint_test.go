package executionapp

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
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

func TestCodeBaselineWithoutRepository(t *testing.T) {
	empty := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{{VersionSUTID: 5001}}}
	if baseline := CodeBaseline(empty); baseline != "" {
		t.Fatalf("no code repo must produce an empty baseline, got %q", baseline)
	}
}

func TestCodeBaselineHashesCommits(t *testing.T) {
	repoA, repoB := int64(900), int64(901)
	commitA, commitB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	binding := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release", Commit: commitB},
	}}
	baseline := CodeBaseline(binding)
	if len(baseline) != 64 {
		t.Fatalf("expected a 64 hex baseline, got %q", baseline)
	}
	// The canonical JSON is the commit list, sorted by repo_id, branch omitted.
	canonical := `{"repositories":[{"repo_id":900,"commit_sha":"` + commitA +
		`"},{"repo_id":901,"commit_sha":"` + commitB + `"}]}`
	if expected := sha256Hex(canonical); baseline != expected {
		t.Fatalf("baseline = %q, want sha256(%s) = %q", baseline, canonical, expected)
	}

	reordered := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release", Commit: commitB},
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
	}}
	if got := CodeBaseline(reordered); got != baseline {
		t.Fatalf("baseline must not depend on binding order: %q vs %q", got, baseline)
	}

	changed := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitB},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release", Commit: commitB},
	}}
	if got := CodeBaseline(changed); got == baseline {
		t.Fatal("a commit change must change the baseline")
	}
}

func TestCodeBaselineDedupesRepositories(t *testing.T) {
	repoA := int64(900)
	commitA, commitB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	single := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
	}}
	one := CodeBaseline(single)
	if len(one) != 64 {
		t.Fatalf("expected a 64 hex baseline, got %q", one)
	}

	sameRepoTwice := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
	}}
	if got := CodeBaseline(sameRepoTwice); got != one {
		t.Fatalf("duplicated repo must be deduplicated: %q vs %q", got, one)
	}

	// A resolved commit wins over the branch fallback of the same repository,
	// so a second version_sut without a resolved commit does not churn intents.
	commitPlusBranch := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "main"},
	}}
	if got := CodeBaseline(commitPlusBranch); got != one {
		t.Fatalf("a commit must win over the branch fallback: %q vs %q", got, one)
	}

	// Conflicting commits for the same repository (analyses answer 409) still
	// produce one deterministic fingerprint independent of binding order.
	conflict := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "main", Commit: commitB},
	}}
	conflictReordered := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "main", Commit: commitB},
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
	}}
	if got, again := CodeBaseline(conflict), CodeBaseline(conflictReordered); got != again {
		t.Fatalf("conflicting commits must hash deterministically: %q vs %q", got, again)
	}

	// Two branch fallbacks for the same repository are deduplicated
	// deterministically too.
	branchConflict := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "release"},
	}}
	branchConflictReordered := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5002, CodeRepoID: &repoA, Branch: "release"},
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
	}}
	if got, again := CodeBaseline(branchConflict), CodeBaseline(branchConflictReordered); got != again {
		t.Fatalf("conflicting branches must hash deterministically: %q vs %q", got, again)
	}
}

func TestCodeBaselineFallsBackToBranch(t *testing.T) {
	repoA, repoB := int64(900), int64(901)
	commitA := strings.Repeat("a", 40)

	branchOnly := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main"},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release"},
	}}
	baseline := CodeBaseline(branchOnly)
	if len(baseline) != 64 {
		t.Fatalf("expected a 64 hex branch baseline, got %q", baseline)
	}
	canonical := `{"repositories":[{"repo_id":900,"branch":"main"},{"repo_id":901,"branch":"release"}]}`
	if expected := sha256Hex(canonical); baseline != expected {
		t.Fatalf("branch baseline = %q, want sha256(%s) = %q", baseline, canonical, expected)
	}

	otherBranch := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "dev"},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release"},
	}}
	if got := CodeBaseline(otherBranch); got == baseline {
		t.Fatal("branch change must change the fallback baseline")
	}

	// One repo resolved, one repository without a commit: mixed fingerprint.
	mixed := VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release"},
	}}
	mixedBaseline := CodeBaseline(mixed)
	if len(mixedBaseline) != 64 {
		t.Fatalf("expected a 64 hex mixed baseline, got %q", mixedBaseline)
	}
	mixedCanonical := `{"repositories":[{"repo_id":900,"commit_sha":"` + commitA +
		`"},{"repo_id":901,"branch":"release"}]}`
	if expected := sha256Hex(mixedCanonical); mixedBaseline != expected {
		t.Fatalf("mixed baseline = %q, want sha256(%s) = %q", mixedBaseline, mixedCanonical, expected)
	}
	if mixedBaseline == baseline {
		t.Fatal("mixing a commit and a branch must differ from the pure branch baseline")
	}
	commitOnly := CodeBaseline(VersionBinding{VersionID: 1, SUTs: []VersionSUTBinding{
		{VersionSUTID: 5001, CodeRepoID: &repoA, Branch: "main", Commit: commitA},
		{VersionSUTID: 5002, CodeRepoID: &repoB, Branch: "release", Commit: commitA},
	}})
	if mixedBaseline == commitOnly {
		t.Fatal("mixing a commit and a branch must differ from the pure commit baseline")
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

func sha256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
