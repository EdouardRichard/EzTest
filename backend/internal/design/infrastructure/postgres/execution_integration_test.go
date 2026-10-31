//go:build integration

package designpg_test

import (
	"context"
	"errors"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

func TestListExecutableReturnsCurrentConfirmedCases(t *testing.T) {
	repo, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, true)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "退款", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "退款场景", 0)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, design.CaseStatusConfirmed, "退款成功")
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID+10, testCaseVersion+10, 2, design.CaseStatusDraft, "草稿用例")
	// Two assertions on step 1; assertion_seq derives from ascending assertion ID.
	ctx := context.Background()
	for _, item := range []struct {
		id  int64
		seq int
	}{{8001, 1}, {8002, 2}} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO case_assertion (id, case_version_id, step_seq, kind, target, expected)
			VALUES ($1, $2, 1, 'ui', '#result', '成功')
		`, item.id, testCaseVersion); err != nil {
			t.Fatalf("seed assertion: %v", err)
		}
	}

	cases, err := repo.ListExecutableCases(ctx, testVersionID)
	if err != nil {
		t.Fatalf("ListExecutableCases: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("executable cases = %d, want 1 (draft must be excluded)", len(cases))
	}
	executable := cases[0]
	if executable.CaseID != testCaseID || executable.Version.ID != testCaseVersion {
		t.Fatalf("executable = %+v", executable)
	}
	if executable.RequirementID != testReqID || executable.RequirementVersionID != testReqVersion {
		t.Fatalf("requirement context = %+v", executable)
	}
	if len(executable.Version.Steps) != 1 || executable.Version.Steps[0].Seq != 1 {
		t.Fatalf("steps = %+v", executable.Version.Steps)
	}
	if len(executable.Version.Assertions) != 2 {
		t.Fatalf("assertions = %+v", executable.Version.Assertions)
	}
	if executable.Version.Assertions[0].AssertionSeq != 1 || executable.Version.Assertions[1].AssertionSeq != 2 {
		t.Fatalf("assertion seq = %d,%d", executable.Version.Assertions[0].AssertionSeq, executable.Version.Assertions[1].AssertionSeq)
	}

	single, err := repo.ReadExecutableCase(ctx, testVersionID, testCaseVersion)
	if err != nil {
		t.Fatalf("ReadExecutableCase: %v", err)
	}
	if single.CaseID != testCaseID || len(single.Version.Assertions) != 2 {
		t.Fatalf("single = %+v", single)
	}

	// A draft case version is not executable.
	if _, err := repo.ReadExecutableCase(ctx, testVersionID, testCaseVersion+10); !errors.Is(err, design.ErrNotFound) {
		t.Fatalf("draft case err = %v, want ErrNotFound", err)
	}
	// A foreign version id is not executable.
	if _, err := repo.ReadExecutableCase(ctx, 9999, testCaseVersion); !errors.Is(err, design.ErrNotFound) {
		t.Fatalf("foreign version err = %v, want ErrNotFound", err)
	}

	// Once the requirement revision moves, the old confirmed case is stale and
	// no longer executable.
	if _, err := pool.Exec(ctx, `UPDATE requirement SET current_version_id = $2 WHERE id = $1`, testReqID, testReqVersion+1); err != nil {
		t.Fatalf("move requirement revision: %v", err)
	}
	if _, err := repo.ReadExecutableCase(ctx, testVersionID, testCaseVersion); !errors.Is(err, design.ErrNotFound) {
		t.Fatalf("stale case err = %v, want ErrNotFound", err)
	}
	cases, err = repo.ListExecutableCases(ctx, testVersionID)
	if err != nil || len(cases) != 0 {
		t.Fatalf("stale list = %+v err=%v", cases, err)
	}
}
