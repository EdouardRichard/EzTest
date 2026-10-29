//go:build integration

package chain_test

// This file exercises the execution-side chain provider against the real
// PostgreSQL schema: one DISTINCT ON statement selects the newest attempt of
// every case node, its evidence count and the optional batch scope (spec §5.2
// query 6). It is skipped unless EZTEST_TEST_DATABASE_URL points at a dedicated
// *_test database (testutil.OpenTestDB enforces that).

import (
	"context"
	"reflect"
	"strings"
	"testing"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	executionchain "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/chain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// executionChainSeedVersion inserts one version row.
func executionChainSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, name, status, revision, created_by, updated_by)
		VALUES ($1, 'execution-chain-fixture', 'executing', 1, 1, 1)`, id)
	if err != nil {
		t.Fatalf("seed version %d: %v", id, err)
	}
}

// executionChainSeedBatch inserts one terminal batch with a valid hash pair.
func executionChainSeedBatch(t *testing.T, pool *pgxpool.Pool, id, versionID int64, batchNo int) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_batch
			(id, version_id, batch_no, status, baseline_hash, graph_hash, kind, created_by)
		VALUES ($1, $2, $3, 'done', $4, $4, 'baseline', 1)`,
		id, versionID, batchNo, hash)
	if err != nil {
		t.Fatalf("seed batch %d: %v", id, err)
	}
}

// executionChainSeedRun inserts one case run; an empty failureClass means NULL.
func executionChainSeedRun(t *testing.T, pool *pgxpool.Pool, id, batchID, versionID, caseNodeID int64, attempt int, status, failureClass, summary string, durationMs int64) {
	t.Helper()
	var class any
	if failureClass != "" {
		class = failureClass
	}
	hash := strings.Repeat("b", 64)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash,
			 case_snapshot, attempt, status, failure_class, summary, duration_ms)
		VALUES ($1, $2, $3, $4, 1, $5, '{"node":{"title":"case"}}'::jsonb, $6, $7, $8, $9, $10)`,
		id, batchID, versionID, caseNodeID, hash, attempt, status, class, summary, durationMs)
	if err != nil {
		t.Fatalf("seed case run %d: %v", id, err)
	}
}

// executionChainSeedEvidence inserts one evidence row linked to a case run.
func executionChainSeedEvidence(t *testing.T, pool *pgxpool.Pool, id, batchID, caseRunID int64, name string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO evidence (id, batch_id, case_run_id, kind, path, sha256, size_bytes)
		VALUES ($1, $2, $3, 'screenshot', $4, $5, 10)`,
		id, batchID, caseRunID, name, strings.Repeat("c", 64))
	if err != nil {
		t.Fatalf("seed evidence %d: %v", id, err)
	}
}

// TestExecutionChainReadsLatestResults pins the latest-attempt contract: the
// newest (attempt, id) row of each case node wins, the batch scope excludes the
// other batches, the evidence count belongs to the selected attempt and the
// result fields are projected as-is.
func TestExecutionChainReadsLatestResults(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID    = int64(401)
		otherVersion = int64(402)
		oldBatchID   = int64(4101)
		newBatchID   = int64(4102)
		caseA        = int64(4201)
		caseB        = int64(4202)
		caseC        = int64(4203)
		caseOldOnly  = int64(4204)
		caseForeign  = int64(4209)
	)
	executionChainSeedVersion(t, pool, versionID)
	executionChainSeedVersion(t, pool, otherVersion)
	executionChainSeedBatch(t, pool, oldBatchID, versionID, 1)
	executionChainSeedBatch(t, pool, newBatchID, versionID, 2)

	// Case A runs in both batches: the two attempts of the selected batch must
	// win over the old batch's attempt, and attempt 2 must win over attempt 1.
	executionChainSeedRun(t, pool, 4301, oldBatchID, versionID, caseA, 1, "passed", "", "old batch", 10)
	executionChainSeedRun(t, pool, 4311, newBatchID, versionID, caseA, 1, "failed", "application", "first try", 100)
	executionChainSeedRun(t, pool, 4312, newBatchID, versionID, caseA, 2, "passed", "", "latest try", 1500)
	executionChainSeedRun(t, pool, 4321, newBatchID, versionID, caseB, 1, "blocked", "environment", "blocked", 30)
	executionChainSeedRun(t, pool, 4331, newBatchID, versionID, caseC, 1, "interrupted", "", "interrupted", 0)
	executionChainSeedRun(t, pool, 4341, oldBatchID, versionID, caseOldOnly, 1, "passed", "", "old batch only", 5)
	executionChainSeedRun(t, pool, 4391, newBatchID, otherVersion, caseForeign, 1, "passed", "", "foreign", 1)

	// Evidence 1+2 belong to the losing first attempt, 3 to the selected one.
	executionChainSeedEvidence(t, pool, 4401, newBatchID, 4311, "shots/losing-1.png")
	executionChainSeedEvidence(t, pool, 4402, newBatchID, 4311, "shots/losing-2.png")
	executionChainSeedEvidence(t, pool, 4403, newBatchID, 4312, "shots/latest.png")

	reads := executionchain.New(pool)

	batchScope := newBatchID
	results, err := reads.LatestResults(ctx, versionID, &batchScope)
	if err != nil {
		t.Fatalf("LatestResults(batch): %v", err)
	}
	want := []chainapp.ResultRow{
		{ID: 4312, CaseNodeID: caseA, BatchID: newBatchID, Attempt: 2, Status: "passed",
			DurationMs: 1500, FailureClass: "", Summary: "latest try", EvidenceCount: 1},
		{ID: 4321, CaseNodeID: caseB, BatchID: newBatchID, Attempt: 1, Status: "blocked",
			DurationMs: 30, FailureClass: "environment", Summary: "blocked", EvidenceCount: 0},
		{ID: 4331, CaseNodeID: caseC, BatchID: newBatchID, Attempt: 1, Status: "interrupted",
			DurationMs: 0, FailureClass: "", Summary: "interrupted", EvidenceCount: 0},
	}
	if !reflect.DeepEqual(results, want) {
		t.Fatalf("batch results = %+v, want %+v", results, want)
	}

	// A nil batch means every batch of the version: the old batch's case must
	// appear, while the foreign version stays excluded.
	allResults, err := reads.LatestResults(ctx, versionID, nil)
	if err != nil {
		t.Fatalf("LatestResults(all): %v", err)
	}
	wantAll := append(append([]chainapp.ResultRow{}, want...),
		chainapp.ResultRow{ID: 4341, CaseNodeID: caseOldOnly, BatchID: oldBatchID, Attempt: 1,
			Status: "passed", DurationMs: 5, FailureClass: "", Summary: "old batch only", EvidenceCount: 0})
	if !reflect.DeepEqual(allResults, wantAll) {
		t.Fatalf("all-batch results = %+v, want %+v", allResults, wantAll)
	}

	unknownBatch := int64(9999)
	empty, err := reads.LatestResults(ctx, versionID, &unknownBatch)
	if err != nil {
		t.Fatalf("LatestResults(unknown batch): %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("unknown batch results = %d rows, want none", len(empty))
	}
}
