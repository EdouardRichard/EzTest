package executionapp

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"testing"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func testBinding() VersionBinding {
	return VersionBinding{
		VersionID: 7,
		Status:    "running",
		SUTs: []SUTBinding{
			{
				// v0.2.4-B: the binding-row id leg is gone; SUTID is the only
				// key, so the fingerprint payload is one id narrower and every
				// hash value changes (GD-05 discards the old intents).
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
		t.Fatal("binding without SUTs must be rejected")
	}
}

func TestCodeBaselineWithoutRepository(t *testing.T) {
	empty := VersionBinding{VersionID: 1, SUTs: []SUTBinding{{SUTID: 5001}}}
	baseline, err := CodeBaseline(empty)
	if err != nil {
		t.Fatalf("no code repo must not error, got %v", err)
	}
	if baseline != "" {
		t.Fatalf("no code repo must produce an empty baseline, got %q", baseline)
	}
}

// TestCommitStrongBinding pins V-0024: the code baseline is the union of the
// environment's repositories, each paired with ITS OWN resolved 40-hex commit.
// The branch fallback and empty-string acceptance are gone — an unresolved or
// malformed commit is a typed rejection, never a silently hashed branch or a
// bare {repo_id}. A repository's commit is never borrowed from another repo.
func TestCommitStrongBinding(t *testing.T) {
	commitA, commitB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	t.Run("resolved commits hash the canonical union", func(t *testing.T) {
		binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{
				{RepoID: 900, Commit: commitA}, {RepoID: 901, Commit: commitB},
			}},
		}}
		baseline, err := CodeBaseline(binding)
		if err != nil {
			t.Fatalf("resolved union must hash, got %v", err)
		}
		// Each repo_id is paired with its OWN commit, ascending by repo_id.
		canonical := `{"repositories":[{"repo_id":900,"commit_sha":"` + commitA +
			`"},{"repo_id":901,"commit_sha":"` + commitB + `"}]}`
		if expected := sha256Hex(canonical); baseline != expected {
			t.Fatalf("baseline = %q, want sha256(%s) = %q", baseline, canonical, expected)
		}
	})

	t.Run("empty commit is rejected, not branch-fallback", func(t *testing.T) {
		binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: ""}}},
		}}
		baseline, err := CodeBaseline(binding)
		if err == nil {
			t.Fatalf("an unresolved commit must be rejected, got baseline %q", baseline)
		}
		if baseline != "" {
			t.Fatalf("a rejected baseline must be empty, got %q", baseline)
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
			t.Fatalf("unresolved commit must be a typed 409 state conflict, got %v", err)
		}
	})

	t.Run("a partially resolved union is rejected whole", func(t *testing.T) {
		// One repo resolved, one not: the union must not silently drop the
		// unresolved repo nor borrow the resolved commit for it.
		binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{
				{RepoID: 900, Commit: commitA}, {RepoID: 901, Commit: ""},
			}},
		}}
		if _, err := CodeBaseline(binding); err == nil {
			t.Fatal("a partially resolved union must be rejected, not silently narrowed")
		}
	})

	t.Run("malformed SHA is rejected", func(t *testing.T) {
		for _, bad := range []string{
			"main",                                   // a branch name, not a commit
			strings.Repeat("a", 39),                  // too short
			strings.Repeat("a", 41),                  // too long
			strings.ToUpper(strings.Repeat("a", 40)), // uppercase
			strings.Repeat("g", 40),                  // non-hex
		} {
			binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
				{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: bad}}},
			}}
			if _, err := CodeBaseline(binding); err == nil {
				t.Fatalf("malformed commit %q must be rejected", bad)
			}
		}
	})

	t.Run("repo_id is never paired with another repo's commit", func(t *testing.T) {
		// Two SUTs share the pool; each repository keeps its own commit. The
		// canonical hash pins repo 900→commitA and repo 901→commitB, so a
		// first-non-empty-across-repos pairing (the V-0024 defect) would differ.
		binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
			{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 901, Commit: commitB}}},
		}}
		baseline, err := CodeBaseline(binding)
		if err != nil {
			t.Fatalf("union: %v", err)
		}
		canonical := `{"repositories":[{"repo_id":900,"commit_sha":"` + commitA +
			`"},{"repo_id":901,"commit_sha":"` + commitB + `"}]}`
		if expected := sha256Hex(canonical); baseline != expected {
			t.Fatalf("baseline = %q, want each repo paired with its own commit %q", baseline, expected)
		}
		// Swapping the commits across repos must change the hash (proves the
		// pairing is per-repo, not a single shared commit).
		swapped := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitB}}},
			{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 901, Commit: commitA}}},
		}}
		swappedBaseline, err := CodeBaseline(swapped)
		if err != nil {
			t.Fatalf("swapped union: %v", err)
		}
		if swappedBaseline == baseline {
			t.Fatal("pairing each repo with its own commit must differ from a cross-repo swap")
		}
	})

	t.Run("binding order does not change the union hash", func(t *testing.T) {
		binding := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
			{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 901, Commit: commitB}}},
		}}
		reordered := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
			{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 901, Commit: commitB}}},
			{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
		}}
		first, err := CodeBaseline(binding)
		if err != nil {
			t.Fatalf("binding: %v", err)
		}
		second, err := CodeBaseline(reordered)
		if err != nil || second != first {
			t.Fatalf("union must not depend on binding order: %q vs %q err=%v", first, second, err)
		}
	})
}

func TestCodeBaselineDedupesRepositories(t *testing.T) {
	commitA, commitB := strings.Repeat("a", 40), strings.Repeat("b", 40)

	single := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
		{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
	}}
	one, err := CodeBaseline(single)
	if err != nil || len(one) != 64 {
		t.Fatalf("expected a 64 hex commit baseline, got %q err=%v", one, err)
	}

	// The same repository surfaced by two SUT bindings is deduplicated.
	sameRepoTwice := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
		{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
		{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
	}}
	if got, err := CodeBaseline(sameRepoTwice); err != nil || got != one {
		t.Fatalf("duplicated repo must be deduplicated: %q vs %q err=%v", got, one, err)
	}

	// Conflicting commits for the same repository still produce one
	// deterministic fingerprint independent of binding order (lexicographically
	// smallest SHA wins, the frozen §5.5 tie-break).
	conflict := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
		{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
		{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitB}}},
	}}
	conflictReordered := VersionBinding{VersionID: 1, SUTs: []SUTBinding{
		{SUTID: 5002, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitB}}},
		{SUTID: 5001, RepositoryBaselines: []RepositoryBaseline{{RepoID: 900, Commit: commitA}}},
	}}
	got, err := CodeBaseline(conflict)
	if err != nil {
		t.Fatalf("conflict: %v", err)
	}
	again, err := CodeBaseline(conflictReordered)
	if err != nil || got != again {
		t.Fatalf("conflicting commits must hash deterministically: %q vs %q err=%v", got, again, err)
	}
	if want := sha256Hex(`{"repositories":[{"repo_id":900,"commit_sha":"` + commitA + `"}]}`); got != want {
		t.Fatalf("conflict tie-break = %q, want the lexicographically smallest SHA %q", got, want)
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
