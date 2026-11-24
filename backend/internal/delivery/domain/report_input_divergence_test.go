package delivery

import (
	"strings"
	"testing"
)

// TestDescribeEnvironmentDivergenceSUTSetDiff pins the item-level SUT-set diff
// branch (fix round minor #3): when the actual resolved SUT set differs from the
// wave's planned SUT set, the readable divergence names the concrete SUT sets,
// not just the fingerprint hashes.
func TestDescribeEnvironmentDivergenceSUTSetDiff(t *testing.T) {
	planned := []WaveSUTEnvironmentSnapshot{
		{SUTID: 70, BaseURL: "https://planned.example.com", AccountRoles: []string{"admin"}},
	}
	diffs := DescribeEnvironmentDivergence("aaaa", "bbbb", "", "", planned, []int64{80})
	joined := strings.Join(diffs, "\n")
	if !strings.Contains(joined, "被测系统集不同") {
		t.Fatalf("divergence missing the SUT-set item-level diff; got:\n%s", joined)
	}
	if !strings.Contains(joined, "70") || !strings.Contains(joined, "80") {
		t.Fatalf("SUT-set diff must name the planned (70) and actual (80) SUT ids; got:\n%s", joined)
	}
}

// TestDescribeEnvironmentDivergenceEmptyPlanned pins the plannedSUTs-empty
// fallback (fix round minor #3): with no planned SUT items but a divergent
// fingerprint, the divergence still names the environment dimensions rather than
// emitting nothing or only hashes.
func TestDescribeEnvironmentDivergenceEmptyPlanned(t *testing.T) {
	diffs := DescribeEnvironmentDivergence("aaaa", "bbbb", "", "", nil, nil)
	if len(diffs) == 0 {
		t.Fatal("empty planned SUTs with a divergent fingerprint must still produce a readable difference")
	}
	joined := strings.Join(diffs, "\n")
	if !strings.Contains(joined, "base_url") || !strings.Contains(joined, "账号角色") {
		t.Fatalf("empty-planned fallback must name the env dimensions; got:\n%s", joined)
	}
}

// TestDescribeEnvironmentDivergenceBaselineOnly pins that a code-baseline-only
// divergence (environment fingerprint equal, baseline different) names the code
// baseline dimension and does not fabricate an environment-config difference.
func TestDescribeEnvironmentDivergenceBaselineOnly(t *testing.T) {
	planned := []WaveSUTEnvironmentSnapshot{
		{SUTID: 70, RepositoryBaselines: []WaveRepoBaselineSnapshot{{RepoID: 5, CommitSHA: strings.Repeat("a", 40)}}},
	}
	diffs := DescribeEnvironmentDivergence("same", "same", "baseA", "baseB", planned, []int64{70})
	joined := strings.Join(diffs, "\n")
	if !strings.Contains(joined, "代码基线不同") {
		t.Fatalf("baseline-only divergence must name the code baseline; got:\n%s", joined)
	}
	if strings.Contains(joined, "环境配置不同") {
		t.Fatalf("equal environment fingerprint must not claim an environment-config difference; got:\n%s", joined)
	}
}

// TestDescribeEnvironmentDivergenceNoDivergence pins that identical carrier and
// actual values produce no difference entries.
func TestDescribeEnvironmentDivergenceNoDivergence(t *testing.T) {
	planned := []WaveSUTEnvironmentSnapshot{{SUTID: 70}}
	diffs := DescribeEnvironmentDivergence("same", "same", "base", "base", planned, []int64{70})
	if len(diffs) != 0 {
		t.Fatalf("no divergence must yield no differences; got %v", diffs)
	}
}
