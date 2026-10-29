//go:build integration

package chain_test

// This file exercises the version-side chain provider against the real
// PostgreSQL schema: batch resolution (latest, explicit, foreign, missing), the
// complete requirement layer and the aggregated open-question counts (spec §5.2
// queries 1–2). It is skipped unless EZTEST_TEST_DATABASE_URL points at a
// dedicated *_test database (testutil.OpenTestDB enforces that).

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionchain "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/chain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionChainSeedVersion inserts one version row.
func versionChainSeedVersion(t *testing.T, pool *pgxpool.Pool, id int64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO version (id, name, status, revision, created_by, updated_by)
		VALUES ($1, $2, 'clarifying', 1, 1, 1)`,
		id, "version-chain-fixture")
	if err != nil {
		t.Fatalf("seed version %d: %v", id, err)
	}
}

// versionChainSeedBatch inserts one terminal batch with a valid hash pair.
func versionChainSeedBatch(t *testing.T, pool *pgxpool.Pool, id, versionID int64, batchNo int, kind string, createdAt time.Time) {
	t.Helper()
	hash := strings.Repeat("a", 64)
	_, err := pool.Exec(context.Background(), `
		INSERT INTO run_batch
			(id, version_id, batch_no, status, baseline_hash, graph_hash, kind, created_by, created_at)
		VALUES ($1, $2, $3, 'done', $4, $4, $5, 1, $6)`,
		id, versionID, batchNo, hash, kind, createdAt)
	if err != nil {
		t.Fatalf("seed batch %d: %v", id, err)
	}
}

// versionChainSeedRequirement inserts one requirement with an explicit status
// and declaration timestamp.
func versionChainSeedRequirement(t *testing.T, pool *pgxpool.Pool, id, versionID int64, title, status string, createdAt time.Time) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO requirement (id, version_id, title, priority, status, created_by, updated_by, created_at)
		VALUES ($1, $2, $3, 'must', $4, 1, 1, $5)`,
		id, versionID, title, status, createdAt)
	if err != nil {
		t.Fatalf("seed requirement %d: %v", id, err)
	}
}

// versionChainSeedQuestion inserts one clarification question. An "answered"
// status needs a non-empty answer because of the schema check.
func versionChainSeedQuestion(t *testing.T, pool *pgxpool.Pool, id, versionID int64, requirementID *int64, status string) {
	t.Helper()
	answer := ""
	if status == "answered" {
		answer = "resolved"
	}
	_, err := pool.Exec(context.Background(), `
		INSERT INTO clarification_question
			(id, version_id, requirement_id, question, status, answer, created_by)
		VALUES ($1, $2, $3, 'question', $4, $5, 1)`,
		id, versionID, requirementID, status, answer)
	if err != nil {
		t.Fatalf("seed clarification question %d: %v", id, err)
	}
}

// TestVersionChainReadsBatchResolution covers the batch selection contract: the
// latest batch by default, the explicit batch when it belongs to the version,
// and the two distinct 404 resources (version vs batch).
func TestVersionChainReadsBatchResolution(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID    = int64(101)
		otherVersion = int64(102)
		emptyVersion = int64(103)
		oldBatchID   = int64(1101)
		newBatchID   = int64(1102)
		foreignBatch = int64(1103)
	)
	versionChainSeedVersion(t, pool, versionID)
	versionChainSeedVersion(t, pool, otherVersion)
	versionChainSeedVersion(t, pool, emptyVersion)
	versionChainSeedBatch(t, pool, oldBatchID, versionID, 1, "baseline",
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	versionChainSeedBatch(t, pool, newBatchID, versionID, 2, "incremental",
		time.Date(2026, 10, 2, 0, 0, 0, 0, time.UTC))
	versionChainSeedBatch(t, pool, foreignBatch, otherVersion, 1, "baseline",
		time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC))

	reads := versionchain.New(pool)

	latest, err := reads.GetBatch(ctx, versionID, nil)
	if err != nil {
		t.Fatalf("GetBatch(latest): %v", err)
	}
	if latest.ID != newBatchID || latest.BatchNo != 2 || latest.Kind != "incremental" || latest.Status != "done" {
		t.Fatalf("latest batch = %+v, want %d/2/incremental/done", latest, newBatchID)
	}

	explicit, err := reads.GetBatch(ctx, versionID, &[]int64{oldBatchID}[0])
	if err != nil {
		t.Fatalf("GetBatch(explicit): %v", err)
	}
	if explicit.ID != oldBatchID || explicit.BatchNo != 1 || explicit.Kind != "baseline" {
		t.Fatalf("explicit batch = %+v, want %d/1/baseline", explicit, oldBatchID)
	}

	_, err = reads.GetBatch(ctx, versionID, &[]int64{foreignBatch}[0])
	var batchNotFound *chainapp.ErrNotFound
	if !errors.As(err, &batchNotFound) || batchNotFound.Resource != "batch" || batchNotFound.ID != foreignBatch {
		t.Fatalf("foreign batch error = %v, want ErrNotFound{batch,%d}", err, foreignBatch)
	}

	_, err = reads.GetBatch(ctx, 999, nil)
	var versionNotFound *chainapp.ErrNotFound
	if !errors.As(err, &versionNotFound) || versionNotFound.Resource != "version" || versionNotFound.ID != 999 {
		t.Fatalf("missing version error = %v, want ErrNotFound{version,999}", err)
	}

	empty, err := reads.GetBatch(ctx, emptyVersion, nil)
	if err != nil {
		t.Fatalf("GetBatch(no batch): %v", err)
	}
	if empty.ID != 0 || empty.BatchNo != 0 || empty.Kind != "" || empty.Status != "" {
		t.Fatalf("version without batch = %+v, want zero row", empty)
	}
}

// TestVersionChainReadsRequirements covers the complete requirement layer: every
// status is returned (no SQL-side request filtering), declaration order is
// preserved and the open-question count is aggregated per requirement (answered
// questions, NULL requirement questions and other-version questions excluded).
func TestVersionChainReadsRequirements(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	const (
		versionID    = int64(201)
		otherVersion = int64(202)
	)
	versionChainSeedVersion(t, pool, versionID)
	versionChainSeedVersion(t, pool, otherVersion)
	base := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	versionChainSeedRequirement(t, pool, 2201, versionID, "first", "clarified", base)
	versionChainSeedRequirement(t, pool, 2202, versionID, "second", "draft", base.Add(time.Minute))
	versionChainSeedRequirement(t, pool, 2203, versionID, "third", "designed", base.Add(2*time.Minute))
	versionChainSeedRequirement(t, pool, 2299, otherVersion, "foreign", "clarified", base.Add(3*time.Minute))

	req1 := int64(2201)
	req2 := int64(2202)
	versionChainSeedQuestion(t, pool, 2301, versionID, &req1, "open")
	versionChainSeedQuestion(t, pool, 2302, versionID, &req1, "open")
	versionChainSeedQuestion(t, pool, 2303, versionID, &req1, "answered")
	versionChainSeedQuestion(t, pool, 2304, versionID, &req2, "open")
	// A NULL requirement and another version's question must never leak into a
	// requirement's open count.
	versionChainSeedQuestion(t, pool, 2305, versionID, nil, "open")
	foreignReq := int64(2299)
	versionChainSeedQuestion(t, pool, 2306, otherVersion, &foreignReq, "open")

	rows, err := versionchain.New(pool).Requirements(ctx, versionID)
	if err != nil {
		t.Fatalf("Requirements: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("requirements = %d rows, want the complete layer of 3", len(rows))
	}
	wantIDs := []int64{2201, 2202, 2203}
	wantTitles := []string{"first", "second", "third"}
	wantStatuses := []string{"clarified", "draft", "designed"}
	wantOpen := []int{2, 1, 0}
	for index, row := range rows {
		if row.ID != wantIDs[index] || row.Title != wantTitles[index] || row.Status != wantStatuses[index] {
			t.Fatalf("row %d = %+v, want id/title/status %d/%q/%q",
				index, row, wantIDs[index], wantTitles[index], wantStatuses[index])
		}
		if row.SortOrder != index {
			t.Fatalf("row %d sortOrder = %d, want %d", index, row.SortOrder, index)
		}
		if row.OpenQuestionCount != wantOpen[index] {
			t.Fatalf("row %d openQuestionCount = %d, want %d", index, row.OpenQuestionCount, wantOpen[index])
		}
		if row.Priority != "must" {
			t.Fatalf("row %d priority = %q, want must", index, row.Priority)
		}
	}
}
