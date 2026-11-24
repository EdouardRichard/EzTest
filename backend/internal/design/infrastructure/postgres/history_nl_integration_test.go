//go:build integration

package designpg_test

import (
	"context"
	"testing"
)

// TestReadCaseVersionsByIDsCarriesNLContext pins the v0.2.4-G R-G8b widening: the
// historical case-version reader carries the immutable row's natural-language
// context (description/preconditions/sut_description) and its steps, so a report
// card of a run referencing a superseded version keeps the full NL context
// instead of reverting to title-only. The data EXISTS on the versioned row; the
// fix is read-port width.
func TestReadCaseVersionsByIDsCarriesNLContext(t *testing.T) {
	repo, pool := newRepo(t)
	ctx := context.Background()
	seedVersion(t, pool, testVersionID, false)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "R", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "S", 1)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, "confirmed", "历史用例")
	// The logical case entity stays confirmed; the VERSION is superseded (the
	// historical state a prior run references). Set a real description to prove
	// the reader carries it (the row is immutable versioned content).
	if _, err := pool.Exec(ctx, `UPDATE case_version SET status = 'superseded', description = '验证旧版导出流程' WHERE id = $1`, testCaseVersion); err != nil {
		t.Fatalf("set superseded/description: %v", err)
	}

	rows, err := repo.ReadCaseVersionsByIDs(ctx, testVersionID, []int64{testCaseVersion})
	if err != nil {
		t.Fatalf("ReadCaseVersionsByIDs: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(rows))
	}
	row := rows[0]
	if row.Description != "验证旧版导出流程" {
		t.Fatalf("description = %q, want the versioned row's description", row.Description)
	}
	if row.Preconditions != seedPreconditions || row.SutDescription != seedSutDescription {
		t.Fatalf("preconditions/sut_description = %q/%q, want %q/%q", row.Preconditions, row.SutDescription, seedPreconditions, seedSutDescription)
	}
	if len(row.Steps) != 1 {
		t.Fatalf("steps = %d, want the versioned row's 1 step", len(row.Steps))
	}
	if row.Steps[0].Action != "step 历史用例" || row.Steps[0].TargetDescription != seedTargetDescription {
		t.Fatalf("step = %+v, want the versioned row's action/target_description", row.Steps[0])
	}
}
