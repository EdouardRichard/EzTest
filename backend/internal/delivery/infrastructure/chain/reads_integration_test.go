//go:build integration

package chain_test

// This file exercises the delivery-side chain provider against the real
// PostgreSQL schema: the issue layer, the issue_ref evidence/code-anchor counts
// and the case connection (spec §5.2 query 8). It also pins the Task 6 edge
// case: an issue that only carries case_run_id (case_node_id is NULL) must
// still receive its case node from the case_run join, so the chain service can
// degrade it to a case_issue edge. It is skipped unless
// EZTEST_TEST_DATABASE_URL points at a dedicated *_test database
// (testutil.OpenTestDB enforces that).

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	deliverychain "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/chain"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// deliveryChainSeedVersion inserts one version row.
func deliveryChainSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, name, status, revision, created_by, updated_by)
		VALUES ($1, 'delivery-chain-fixture', 'executing', 1, 1, 1)`, id)
	if err != nil {
		t.Fatalf("seed version %d: %v", id, err)
	}
}

// deliveryChainSeedBatch inserts one terminal batch with a valid hash pair.
func deliveryChainSeedBatch(t *testing.T, pool *pgxpool.Pool, id, versionID int64, batchNo int) {
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

// deliveryChainSeedCaseRun inserts one case run.
func deliveryChainSeedCaseRun(t *testing.T, pool *pgxpool.Pool, id, batchID, versionID, caseNodeID int64) {
	t.Helper()
	hash := strings.Repeat("b", 64)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO case_run
			(id, batch_id, version_id, case_node_id, case_revision, case_hash,
			 case_snapshot, attempt, status)
		VALUES ($1, $2, $3, $4, 1, $5, '{"node":{"title":"case"}}'::jsonb, 1, 'failed')`,
		id, batchID, versionID, caseNodeID, hash)
	if err != nil {
		t.Fatalf("seed case run %d: %v", id, err)
	}
}

// deliveryChainSeedIssue inserts one human issue. A nil caseRunID/caseNodeID
// stays NULL, which is how requirement-gap and unlinked issues are stored. A
// dismissed status needs a non-empty reason because of the schema check.
func deliveryChainSeedIssue(t *testing.T, pool *pgxpool.Pool, id, versionID int64, batchID *int64, caseRunID, caseNodeID *int64, severity, status string, createdAt time.Time) {
	t.Helper()
	dismissedReason := ""
	if status == "dismissed" {
		dismissedReason = "duplicate"
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO issue
			(id, version_id, batch_id, case_run_id, case_node_id, source, category,
			 severity, status, confidence, title, dismissed_reason, created_by, updated_by, created_at)
		VALUES ($1, $2, $3, $4, $5, 'human', 'defect', $6, $7, 'high', $8, $9, 1, 1, $10)`,
		id, versionID, batchID, caseRunID, caseNodeID, severity, status, "issue", dismissedReason, createdAt)
	if err != nil {
		t.Fatalf("seed issue %d: %v", id, err)
	}
}

// deliveryChainSeedIssueRef inserts one issue_ref row.
func deliveryChainSeedIssueRef(t *testing.T, pool *pgxpool.Pool, issueID int64, refKind, refID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO issue_ref (issue_id, ref_kind, ref_id) VALUES ($1, $2, $3)`,
		issueID, refKind, refID)
	if err != nil {
		t.Fatalf("seed issue ref %d/%s: %v", issueID, refKind, err)
	}
}

// TestDeliveryChainReadsIssuesAndCaseRunJoin pins the issue layer: every status
// and severity is returned (the service filters), the batch scope selects the
// requested batch or all batches, issue_ref counts are aggregated per issue and
// case_run_id alone still resolves the case node. The latter is the Task 6
// deferred edge case that lets an older-attempt issue render as case_issue.
func TestDeliveryChainReadsIssuesAndCaseRunJoin(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID    = int64(501)
		otherVersion = int64(502)
		oldBatchID   = int64(5101)
		newBatchID   = int64(5102)
		caseNodeA    = int64(5301)
		caseNodeB    = int64(5302)
		runID        = int64(5201)
	)
	deliveryChainSeedVersion(t, pool, versionID)
	deliveryChainSeedVersion(t, pool, otherVersion)
	deliveryChainSeedBatch(t, pool, oldBatchID, versionID, 1)
	deliveryChainSeedBatch(t, pool, newBatchID, versionID, 2)
	deliveryChainSeedCaseRun(t, pool, runID, newBatchID, versionID, caseNodeA)

	created := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	newBatch := newBatchID
	oldBatch := oldBatchID
	// Issue 5401: case_run_id only — case_node_id is NULL on purpose.
	deliveryChainSeedIssue(t, pool, 5401, versionID, &newBatch,
		&[]int64{runID}[0], nil, "major", "open", created)
	// Issue 5402: explicit case node, no case run.
	deliveryChainSeedIssue(t, pool, 5402, versionID, &newBatch,
		nil, &[]int64{caseNodeB}[0], "blocker", "confirmed", created.Add(time.Minute))
	// Issue 5403: unlinked requirement gap.
	deliveryChainSeedIssue(t, pool, 5403, versionID, &newBatch,
		nil, nil, "info", "dismissed", created.Add(2*time.Minute))
	// Issue 5404: belongs to the previous batch only.
	deliveryChainSeedIssue(t, pool, 5404, versionID, &oldBatch,
		nil, nil, "minor", "open", created.Add(3*time.Minute))
	// Issue 5405: another version must never appear.
	deliveryChainSeedIssue(t, pool, 5405, otherVersion, &newBatch,
		nil, nil, "major", "open", created.Add(4*time.Minute))

	deliveryChainSeedIssueRef(t, pool, 5401, "evidence", "80001")
	deliveryChainSeedIssueRef(t, pool, 5401, "evidence", "80002")
	deliveryChainSeedIssueRef(t, pool, 5401, "code_anchor", "41")
	deliveryChainSeedIssueRef(t, pool, 5402, "code_anchor", "42")
	deliveryChainSeedIssueRef(t, pool, 5402, "code_anchor", "43")
	deliveryChainSeedIssueRef(t, pool, 5403, "requirement", "10011")

	reads := deliverychain.New(pool)

	issues, err := reads.Issues(ctx, versionID, &newBatch)
	if err != nil {
		t.Fatalf("Issues(batch): %v", err)
	}
	want := []chainapp.IssueRow{
		{ID: 5401, Title: "issue", Severity: "major", Status: "open", Category: "defect",
			Confidence: "high", CaseRunID: &[]int64{runID}[0], CaseNodeID: &[]int64{caseNodeA}[0],
			EvidenceCount: 2, CodeRefCount: 1, CreatedAt: created},
		{ID: 5402, Title: "issue", Severity: "blocker", Status: "confirmed", Category: "defect",
			Confidence: "high", CaseNodeID: &[]int64{caseNodeB}[0],
			EvidenceCount: 0, CodeRefCount: 2, CreatedAt: created.Add(time.Minute)},
		{ID: 5403, Title: "issue", Severity: "info", Status: "dismissed", Category: "defect",
			Confidence: "high", EvidenceCount: 0, CodeRefCount: 0, CreatedAt: created.Add(2 * time.Minute)},
	}
	if len(issues) != len(want) {
		t.Fatalf("batch issues = %+v, want %+v", issues, want)
	}
	// timestamptz round-trips may carry a different location object; compare
	// instants, then normalize so the value comparison stays exact.
	for index := range issues {
		if !issues[index].CreatedAt.Equal(want[index].CreatedAt) {
			t.Fatalf("issue %d createdAt = %s, want %s", index, issues[index].CreatedAt, want[index].CreatedAt)
		}
		issues[index].CreatedAt = want[index].CreatedAt
	}
	if !reflect.DeepEqual(issues, want) {
		t.Fatalf("batch issues = %+v, want %+v", issues, want)
	}
	if issues[0].CaseNodeID == nil || *issues[0].CaseNodeID != caseNodeA {
		t.Fatalf("case_run-only issue caseNodeId = %v, want %d from the case_run join", issues[0].CaseNodeID, caseNodeA)
	}
	if issues[2].CaseRunID != nil || issues[2].CaseNodeID != nil {
		t.Fatalf("unlinked issue = %+v, want nil case links", issues[2])
	}

	allIssues, err := reads.Issues(ctx, versionID, nil)
	if err != nil {
		t.Fatalf("Issues(all): %v", err)
	}
	if len(allIssues) != 4 || allIssues[3].ID != 5404 {
		t.Fatalf("all-batch issues = %+v, want 5401..5404", allIssues)
	}

	oldIssues, err := reads.Issues(ctx, versionID, &oldBatch)
	if err != nil {
		t.Fatalf("Issues(old batch): %v", err)
	}
	if len(oldIssues) != 1 || oldIssues[0].ID != 5404 {
		t.Fatalf("old-batch issues = %+v, want only 5404", oldIssues)
	}
}
