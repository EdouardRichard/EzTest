//go:build integration

package db_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// TestGMigrationSchema pins the v0.2.4-G (T2) foundation schema: the delivery
// issue structured failure elements (SC-G3 / P21) and the two-level admin waiver
// columns (SC-G11 / GD-06 / P23). The closure_kind CHECK set is DERIVED from the
// Go domain authority version.ClosureKinds() (constitution 五.1), never
// hand-copied, so a domain change fails here until the migration is updated. It
// also verifies the behavioral defaults, the fail-closed enum, and that an
// already-migrated database re-applies nothing (append-only idempotency).
func TestGMigrationSchema(t *testing.T) {
	pool := integrationDB(t)
	ctx := context.Background()

	// --- delivery_issue structured failure elements (SC-G3): Summary stays. ---
	requireTable(t, pool, "delivery_issue")
	for _, col := range []string{"failed_expected", "failed_actual", "failed_reason"} {
		requireColumn(t, pool, "delivery_issue", col)
		requireColumnNotNull(t, pool, "delivery_issue", col)
		requireColumnDefault(t, pool, "delivery_issue", col, "''::text")
	}

	// --- version closure_kind / unverified (SC-G11). ---
	requireColumn(t, pool, "version", "closure_kind")
	requireColumnNotNull(t, pool, "version", "closure_kind")
	requireColumnDefault(t, pool, "version", "closure_kind", "'normal'::text")
	// The CHECK enum mirrors the domain authority (derived, not hand-copied).
	requireCheckSet(t, pool, "chk_version_closure_kind", version.ClosureKinds(), nil)
	requireColumn(t, pool, "version", "unverified")
	requireColumnNotNull(t, pool, "version", "unverified")
	requireColumnDefault(t, pool, "version", "unverified", "false")

	// --- requirement waived (SC-G11): a real persisted column, default false. ---
	requireColumn(t, pool, "requirement", "waived")
	requireColumnNotNull(t, pool, "requirement", "waived")
	requireColumnDefault(t, pool, "requirement", "waived", "false")

	// --- behavioral defaults + fail-closed closure_kind enum. ---
	// environment_id / created_by are logical BIGINT references (no physical FK).
	if _, err := pool.Exec(ctx, `INSERT INTO version (id, title, environment_id, created_by) VALUES (1, 'g', 1, 1)`); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	var closureKind string
	var unverified bool
	if err := pool.QueryRow(ctx, `SELECT closure_kind, unverified FROM version WHERE id = 1`).Scan(&closureKind, &unverified); err != nil {
		t.Fatalf("read version waiver defaults: %v", err)
	}
	if closureKind != version.ClosureKindNormal || unverified {
		t.Fatalf("version defaults = (%q, %v), want (normal, false)", closureKind, unverified)
	}
	if _, err := pool.Exec(ctx, `UPDATE version SET closure_kind = 'waived', unverified = true WHERE id = 1`); err != nil {
		t.Fatalf("waived closure_kind rejected: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE version SET closure_kind = 'bogus' WHERE id = 1`); err == nil {
		t.Fatal("closure_kind CHECK accepted a non-enum value")
	}

	if _, err := pool.Exec(ctx, `INSERT INTO requirement (id, version_id) VALUES (1, 1)`); err != nil {
		t.Fatalf("insert requirement: %v", err)
	}
	var waived bool
	if err := pool.QueryRow(ctx, `SELECT waived FROM requirement WHERE id = 1`).Scan(&waived); err != nil {
		t.Fatalf("read requirement waived default: %v", err)
	}
	if waived {
		t.Fatal("requirement.waived default = true, want false")
	}

	// dedupe_key is 32 hex chars (delivery_0001 CHECK); version_id is logical.
	if _, err := pool.Exec(ctx, `INSERT INTO delivery_issue (id, version_id, title, category, severity, dedupe_key) VALUES (1, 1, 'g', 'ui', 'major', 'aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa')`); err != nil {
		t.Fatalf("insert delivery_issue: %v", err)
	}
	var expected, actual, reason string
	if err := pool.QueryRow(ctx, `SELECT failed_expected, failed_actual, failed_reason FROM delivery_issue WHERE id = 1`).Scan(&expected, &actual, &reason); err != nil {
		t.Fatalf("read issue structured defaults: %v", err)
	}
	if expected != "" || actual != "" || reason != "" {
		t.Fatalf("issue structured defaults = (%q,%q,%q), want empty", expected, actual, reason)
	}

	// --- already-migrated-DB startup is a no-op (append-only idempotency). ---
	again, err := db.Migrate(ctx, pool)
	if err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if len(again) != 0 {
		t.Fatalf("second Migrate applied %v, want none", again)
	}
}
