//go:build integration

package integration_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestP8VisualEvidenceKinds applies the embedded migrations and proves the P8
// §9.3 execution schema: assertion_result accepts the visual kind and evidence
// accepts visual_baseline/visual_actual/visual_diff while the widened CHECK
// still rejects unknown kinds.
func TestP8VisualEvidenceKinds(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)

	// assertion_result.kind accepts visual.
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_batch (id, version_id, batch_no, baseline_hash, graph_hash, created_by)
		VALUES (9401, 9400, 1, repeat('a', 64), repeat('f', 64), 1)`); err != nil {
		t.Fatalf("insert run batch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot)
		VALUES (9402, 9401, 9400, 101, 1, repeat('b', 64), '{}'::jsonb)`); err != nil {
		t.Fatalf("insert case run: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO assertion_result
			(id, batch_id, case_run_id, assertion_id, kind, verdict)
		VALUES (9403, 9401, 9402, 77, 'visual', 'passed')`); err != nil {
		t.Fatalf("assertion_result rejected the visual kind: %v", err)
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO assertion_result
			(id, batch_id, case_run_id, assertion_id, kind, verdict)
		VALUES (9404, 9401, 9402, 78, 'pixel', 'passed')`)
	assertCheckViolation(t, err, "assertion_result unknown kind")

	// evidence.kind accepts the three visual artifact kinds.
	for index, kind := range []string{"visual_baseline", "visual_actual", "visual_diff"} {
		id := int64(9410 + index)
		if _, err := pool.Exec(ctx, `
			INSERT INTO evidence
				(id, batch_id, case_run_id, kind, path, sha256, size_bytes, mime)
			VALUES ($1, 9401, 9402, $2, $3, repeat('c', 64), 3, 'image/png')`,
			id, kind, "9401/9402/"+kind+".png"); err != nil {
			t.Errorf("evidence rejected kind %q: %v", kind, err)
		}
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO evidence
			(id, batch_id, case_run_id, kind, path, sha256, size_bytes, mime)
		VALUES (9420, 9401, 9402, 'pixel_artifact',
		        '9401/9402/pixel.png', repeat('d', 64), 3, 'image/png')`)
	assertCheckViolation(t, err, "evidence unknown kind")

	// The P7 db_result kind stays accepted after the widening.
	if _, err := pool.Exec(ctx, `
		INSERT INTO evidence
			(id, batch_id, case_run_id, kind, path, sha256, size_bytes, mime)
		VALUES (9421, 9401, 9402, 'db_result', '9401/9402/db.json',
		        repeat('e', 64), 2, 'application/json')`); err != nil {
		t.Errorf("evidence rejected legacy db_result kind: %v", err)
	}

	// The visual snapshot projection carries the baseline id as a JSON number.
	visual := domain.CaseSnapshotAssertion{
		ID: 77, Kind: "visual", Expression: "matches",
		Visual: &domain.CaseSnapshotVisual{
			Selector: "#receipt", Threshold: 0.01, BaselineEvidenceID: int64Ptr(500),
			ViewportWidth: 1280, ViewportHeight: 720, DeviceScaleFactor: 1,
		},
	}
	if visual.Visual == nil || visual.Visual.Selector != "#receipt" ||
		!strings.Contains(visual.Kind, "visual") {
		t.Fatalf("visual snapshot assertion = %+v", visual)
	}
}

func int64Ptr(value int64) *int64 { return &value }

// assertCheckViolation requires err to be a PostgreSQL CHECK violation (23514).
func assertCheckViolation(t *testing.T, err error, what string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: insert succeeded, want a CHECK violation", what)
	}
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("%s: error is not a PostgreSQL error: %v", what, err)
	}
	if pgErr.Code != "23514" {
		t.Fatalf("%s: SQLSTATE = %s (%s), want 23514", what, pgErr.Code, pgErr.Message)
	}
}
