//go:build integration

package designpg_test

import (
	"context"
	"strings"
	"testing"

	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// TestRequirementContextLoadsACIDSet pins the T8 -> T9 handoff against the real
// schema: design.RequirementRef.ACIDs is the requirement's acceptance-criterion
// id set, read out of the version context's requirement_version.acceptance_criteria
// JSONB document (design v1.3.1 §5.1), and it is what a case's covered_acs must
// be a subset of (§5.2). It reaches GraphService.Propose through the existing
// RequirementContext read, so no new port is involved.
//
// The fixture writes the array deliberately OUT of id order and with a
// two-digit id, so the test also pins the numeric-suffix ordering (AC-2 before
// AC-10) rather than a lexicographic one.
func TestRequirementContextLoadsACIDSet(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := designpg.New(pool)
	ctx := context.Background()

	const (
		versionID   = int64(981001)
		environment = int64(981002)
		reqID       = int64(981003)
		reqVersion  = int64(981004)
	)
	seedACRequirement(t, pool, versionID, environment, reqID, reqVersion)

	ref, err := repo.RequirementContext(ctx, versionID, reqID)
	if err != nil {
		t.Fatalf("RequirementContext: %v", err)
	}

	want := []string{"AC-1", "AC-2", "AC-10"}
	if len(ref.ACIDs) != len(want) {
		t.Fatalf("ACIDs = %#v, want %v", ref.ACIDs, want)
	}
	for i := range want {
		if ref.ACIDs[i] != want[i] {
			t.Fatalf("ACIDs = %#v, want %v (numeric-suffix order, not lexicographic)", ref.ACIDs, want)
		}
	}
	if ref.SutDescription != "商城下单页与订单接口。" {
		t.Fatalf("SutDescription = %q", ref.SutDescription)
	}
	if ref.ID != reqID || ref.CurrentVersionID != reqVersion || ref.Status != "clarified" || ref.Priority != "P1" {
		t.Fatalf("ref = %#v", ref)
	}
	if ref.Title != "下单" || ref.Revision != 1 {
		t.Fatalf("ref = %#v", ref)
	}
}

// TestRequirementContextACIDsEmptyWhenNoCriteria pins the empty case: a
// requirement with no criteria yields an empty, non-nil set, so a covered_acs
// subset check fails closed rather than panicking or vacuously passing on nil.
func TestRequirementContextACIDsEmptyWhenNoCriteria(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := designpg.New(pool)
	ctx := context.Background()

	const (
		versionID   = int64(982001)
		environment = int64(982002)
		reqID       = int64(982003)
		reqVersion  = int64(982004)
	)
	seedACVersion(t, pool, versionID, environment, reqID, reqVersion, "P2", "", "[]", strings.Repeat("a", 64))

	ref, err := repo.RequirementContext(ctx, versionID, reqID)
	if err != nil {
		t.Fatalf("RequirementContext: %v", err)
	}
	if ref.ACIDs == nil {
		t.Fatal("ACIDs = nil, want an empty non-nil slice")
	}
	if len(ref.ACIDs) != 0 {
		t.Fatalf("ACIDs = %#v, want empty", ref.ACIDs)
	}
}

// seedACRequirement writes one clarified requirement whose criteria are stored
// out of id order, exactly as the version context's JSONB column can hold them.
func seedACRequirement(t *testing.T, pool *pgxpool.Pool, versionID, environmentID, reqID, reqVersionID int64) {
	t.Helper()
	criteria := `[
		{"id":"AC-10","given":"已下单","when":"取消","then":"库存回滚"},
		{"id":"AC-1","given":"已登录","when":"提交订单","then":"下单成功"},
		{"id":"AC-2","given":"库存不足","when":"提交订单","then":"提示缺货"}
	]`
	seedACVersion(t, pool, versionID, environmentID, reqID, reqVersionID, "P1",
		"商城下单页与订单接口。", criteria, strings.Repeat("b", 64))
}

// seedACVersion inserts the environment -> version -> requirement ->
// requirement_version chain one statement at a time: pgx runs a parameterized
// Exec through the extended protocol, which rejects a multi-statement string.
func seedACVersion(t *testing.T, pool *pgxpool.Pool, versionID, environmentID, reqID, reqVersionID int64,
	priority, sutDescription, criteriaJSON, contentHash string) {
	t.Helper()
	ctx := context.Background()
	stmts := []struct {
		query string
		args  []any
	}{
		{`INSERT INTO environment (id, name, status) VALUES ($1, 'ac fixture env', 'active')`,
			[]any{environmentID}},
		{`INSERT INTO version (id, title, created_by, environment_id) VALUES ($1, 'ac fixture version', 1, $2)`,
			[]any{versionID, environmentID}},
		{`INSERT INTO requirement (id, version_id, current_version_id, status) VALUES ($1, $2, $3, 'clarified')`,
			[]any{reqID, versionID, reqVersionID}},
		{`INSERT INTO requirement_version (id, requirement_id, revision, title, priority, sut_description,
		                                   acceptance_criteria, content_hash)
		  VALUES ($1, $2, 1, '下单', $3, $4, $5::jsonb, $6)`,
			[]any{reqVersionID, reqID, priority, sutDescription, criteriaJSON, contentHash}},
	}
	for _, stmt := range stmts {
		if _, err := pool.Exec(ctx, stmt.query, stmt.args...); err != nil {
			t.Fatalf("seed (%s): %v", stmt.query[:40], err)
		}
	}
}
