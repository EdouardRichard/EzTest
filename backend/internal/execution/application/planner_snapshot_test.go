package executionapp

import (
	"strings"
	"testing"
	"time"
)

// TestBuildWaveScopeSnapshotFreezesAndNormalizesScope pins R-C5-1/DB-19: the
// wave scope document is deterministic (ordered ids) and carries the effective
// binding sets, so a rerun of the same wave observes the same frozen scope.
func TestBuildWaveScopeSnapshotFreezesAndNormalizesScope(t *testing.T) {
	repoID := int64(77)
	binding := VersionBinding{
		VersionID: 5,
		SUTs: []VersionSUTBinding{{
			VersionSUTID:  12,
			SUTID:         2,
			EnvironmentID: 3,
			PageIDs:       []int64{9, 7},
			APIIDs:        []int64{6},
			DatabaseIDs:   []int64{8},
			AccountRoles:  []string{"author", "admin"},
			CodeRepoID:    &repoID,
		}},
	}
	frozenAt := time.Date(2026, 11, 5, 1, 2, 3, 0, time.UTC)
	first, err := buildWaveScopeSnapshot(binding, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot: %v", err)
	}
	for _, needle := range []string{
		`"version_id":5`,
		`"frozen_at":"2026-11-05T01:02:03Z"`,
		`"version_sut_id":12`,
		`"page_ids":[7,9]`,
		`"api_ids":[6]`,
		`"database_ids":[8]`,
		`"account_roles":["admin","author"]`,
		`"repository_ids":[77]`,
	} {
		if !strings.Contains(first, needle) {
			t.Fatalf("snapshot %s does not contain %s", first, needle)
		}
	}
	reordered := binding
	reordered.SUTs = []VersionSUTBinding{binding.SUTs[0]}
	reordered.SUTs[0].PageIDs = []int64{7, 9}
	reordered.SUTs[0].AccountRoles = []string{"admin", "author"}
	second, err := buildWaveScopeSnapshot(reordered, frozenAt)
	if err != nil {
		t.Fatalf("buildWaveScopeSnapshot reordered: %v", err)
	}
	if first != second {
		t.Fatalf("scope snapshot is not deterministic:\nfirst=%s\nsecond=%s", first, second)
	}
}
