//go:build integration

package designpg_test

// M-8 pin (v0.2.4-E T20, spec SC-E11): the case_step.target_description CHECK
// added by design_0004_v024_case_contract.sql:80-81 is LIVE and enforced by the
// database. The registered debt was "CHECK is live, only the test pin is
// missing" (19 号 §4.1 M-8).
//
// Code-fact correction to the brief/spec wording ("非空接受/空拒"): the actual
// constraint is an UPPER BOUND -- CHECK (char_length(target_description) <=
// 2000) on a NOT NULL DEFAULT '' column. Empty is ACCEPTED (target_description
// is optional per design §5.2); over-length is rejected. This test pins the
// real constraint semantics; asserting "empty rejected" would pin a fiction
// and block the correct contract (constitution 五.12 / 方法纪律 ⑩).
//
// Mutation verification is built into the test: inside a rolled-back
// transaction the constraint is dropped and the over-length insert then
// SUCCEEDS -- proving the rejection is caused by this specific CHECK, so
// dropping the CHECK in production makes this test fail.

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

const targetDescriptionCheckName = "case_step_target_description_check"

func TestTargetDescriptionCheckPin(t *testing.T) {
	_, pool := newRepo(t)
	seedVersion(t, pool, testVersionID, true)
	seedRequirement(t, pool, testVersionID, testReqID, testReqVersion, "clarified", "目标描述约束", 1)
	seedScenario(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, "M-8", 1)
	seedCase(t, pool, testVersionID, testReqID, testReqVersion, testScenarioID, testCaseID, testCaseVersion, 1, "draft", "M-8 pin")

	ctx := context.Background()
	insertStep := func(t *testing.T, q interface {
		Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	}, stepID int64, seq int, target string) error {
		t.Helper()
		_, err := q.Exec(ctx, `
			INSERT INTO case_step (id, case_version_id, seq, action, expected, target_description, data)
			VALUES ($1, $2, $3, '打开页面', '页面可见', $4, '{}'::jsonb)
		`, stepID, testCaseVersion, seq, target)
		return err
	}

	// Empty is accepted: the column is NOT NULL DEFAULT '' and the CHECK is an
	// upper bound, not a non-empty requirement (§5.2 target_description 可选).
	if err := insertStep(t, pool, testCaseVersion+200001, 2, ""); err != nil {
		t.Fatalf("empty target_description must be accepted (optional per §5.2): %v", err)
	}
	// Exactly 2000 characters is the inclusive boundary.
	if err := insertStep(t, pool, testCaseVersion+200002, 3, strings.Repeat("x", 2000)); err != nil {
		t.Fatalf("2000-char target_description must be accepted: %v", err)
	}
	// The bound is char_length, not octet_length: 2000 multibyte characters
	// (6000 bytes) are still accepted.
	if err := insertStep(t, pool, testCaseVersion+200003, 4, strings.Repeat("登", 2000)); err != nil {
		t.Fatalf("2000 multibyte chars must be accepted (char_length semantics): %v", err)
	}
	// 2001 characters is rejected by the named CHECK.
	err := insertStep(t, pool, testCaseVersion+200004, 5, strings.Repeat("x", 2001))
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("2001-char target_description must be rejected by the CHECK, got %v", err)
	}
	if pgErr.Code != "23514" || pgErr.ConstraintName != targetDescriptionCheckName {
		t.Fatalf("rejection = code %q constraint %q, want 23514 %s", pgErr.Code, pgErr.ConstraintName, targetDescriptionCheckName)
	}

	// Mutation (rolled back): without the CHECK the same over-length insert
	// succeeds. This is the discriminating proof that the assertion above is
	// pinned to THIS constraint -- drop the CHECK and the test goes red.
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin mutation tx: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, "ALTER TABLE case_step DROP CONSTRAINT "+targetDescriptionCheckName); err != nil {
		t.Fatalf("drop CHECK inside mutation tx: %v", err)
	}
	if err := insertStep(t, tx, testCaseVersion+200005, 6, strings.Repeat("x", 2001)); err != nil {
		t.Fatalf("with the CHECK dropped the over-length insert must succeed (mutation proof): %v", err)
	}
	if err := tx.Rollback(ctx); err != nil && !errors.Is(err, pgx.ErrTxClosed) {
		t.Fatalf("rollback mutation tx: %v", err)
	}
	// The constraint is back after rollback: the rejection holds again.
	if err := insertStep(t, pool, testCaseVersion+200006, 7, strings.Repeat("x", 2001)); err == nil {
		t.Fatal("after rollback the CHECK must reject over-length again")
	}
}
