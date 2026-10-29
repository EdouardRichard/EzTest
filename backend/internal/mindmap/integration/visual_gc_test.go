//go:build integration

package integration_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmappostgres "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// visualGCVersion is the version of the GC protection scenario. It is separate
// from the other tests' versions so the seeded batches do not collide.
const visualGCVersion = int64(7501)

// mustExec runs one seeding statement and fails the test on error.
func mustExec(t *testing.T, pool *pgxpool.Pool, statement string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), statement, args...); err != nil {
		t.Fatalf("seed statement failed: %v\n%s", err, statement)
	}
}

// seedVisualGCWorld creates one old batch of the version (batch_no 1) and a
// newer empty batch (batch_no 2) so the old batch is outside the latest-N
// window with batchKeep=1. The returned batch id owns the old evidence rows.
func seedVisualGCWorld(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	old := time.Now().Add(-60 * 24 * time.Hour)
	hash := strings.Repeat("a", 64)
	mustExec(t, pool, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, created_at, updated_at)
		VALUES (7601, $1, 1, 'done', $2, $2, 1, $3, $3)`, visualGCVersion, hash, old)
	mustExec(t, pool, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, created_at, updated_at)
		VALUES (7602, $1, 2, 'done', $2, $2, 1, now(), now())`, visualGCVersion, hash)
	return 7601
}

// seedVisualGCEvidence inserts one old evidence row of kind in batchID.
func seedVisualGCEvidence(t *testing.T, pool *pgxpool.Pool, id, batchID int64, kind string) {
	t.Helper()
	old := time.Now().Add(-60 * 24 * time.Hour)
	mustExec(t, pool, `
		INSERT INTO evidence (id, batch_id, kind, path, sha256, size_bytes, mime, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, 10, 'image/png', $6, $6)`,
		id, batchID, kind, "baseline/"+kind+".png", strings.Repeat("b", 64), old)
}

// TestGCProtectsApprovedVisualBaselineOfScreenshotEvidence is fix round 1
// finding 4: the baseline approval path accepts a plain `screenshot` evidence
// artifact (design spec §9.3), so GC must protect the evidence referenced by
// case_visual_assertion.baseline_evidence_id even though its kind is not
// visual_baseline. Both ListCandidates and the pre-delete IsProtected re-check
// must honour the reference.
func TestGCProtectsApprovedVisualBaselineOfScreenshotEvidence(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	oldBatch := seedVisualGCWorld(t, pool)

	// The approved baseline is a plain screenshot artifact, as produced by
	// browser.screenshot and accepted by the admin approval endpoint.
	seedVisualGCEvidence(t, pool, 7611, oldBatch, "screenshot")
	// An unreferenced old screenshot is a genuine GC candidate.
	seedVisualGCEvidence(t, pool, 7612, oldBatch, "screenshot")
	// A dedicated kind=visual_baseline artifact stays protected by kind.
	seedVisualGCEvidence(t, pool, 7613, oldBatch, "visual_baseline")

	// The real approval write path stores the reference GC must follow.
	nodeRepo := mindmappostgres.NewNodeRepository(pool)
	assertionRepo := mindmappostgres.NewAssertionRepository(pool)
	visualRepo := mindmappostgres.NewVisualAssertionRepository(pool)
	node := mustCreateNode(t, nodeRepo, newCaseNode(7620, visualGCVersion, nil, "case", "visual baseline case"))
	assertion := newCaseAssertion(7621, node.ID, nil, "visual")
	assertion.Expression = "页面与基线一致"
	if _, err := assertionRepo.Create(ctx, assertion); err != nil {
		t.Fatalf("create visual assertion: %v", err)
	}
	if _, err := visualRepo.Save(ctx, visualGCVersion, assertion.ID, node.Revision,
		visualGCParams("#chart"), 1, time.Now()); err != nil {
		t.Fatalf("save visual params: %v", err)
	}
	if _, err := visualRepo.ApproveBaseline(ctx, visualGCVersion, assertion.ID, 7611, 1, time.Now()); err != nil {
		t.Fatalf("approve baseline: %v", err)
	}

	gc := executionpostgres.NewGCRepository(pool)
	protected, err := gc.IsProtected(ctx, 7611, 1)
	if err != nil {
		t.Fatalf("IsProtected(approved baseline): %v", err)
	}
	if !protected {
		t.Fatal("IsProtected(approved baseline screenshot) = false, want true")
	}
	protected, err = gc.IsProtected(ctx, 7612, 1)
	if err != nil {
		t.Fatalf("IsProtected(unreferenced screenshot): %v", err)
	}
	if protected {
		t.Fatal("IsProtected(unreferenced screenshot) = true, want false")
	}

	candidates, err := gc.ListCandidates(ctx, 30, 1)
	if err != nil {
		t.Fatalf("ListCandidates: %v", err)
	}
	candidateIDs := make(map[int64]bool, len(candidates))
	for _, candidate := range candidates {
		candidateIDs[candidate.ID] = true
	}
	if candidateIDs[7611] {
		t.Fatal("an approved baseline (screenshot evidence) is a GC candidate")
	}
	if !candidateIDs[7612] {
		t.Fatalf("the unreferenced screenshot is not a GC candidate: %+v", candidates)
	}
	if candidateIDs[7613] {
		t.Fatal("a visual_baseline evidence row is a GC candidate")
	}

	count, err := gc.CountProtected(ctx, 1)
	if err != nil {
		t.Fatalf("CountProtected: %v", err)
	}
	if count != 2 {
		t.Fatalf("protected count = %d, want 2 (approved baseline + visual_baseline kind)", count)
	}
}

// TestGCProtectionSurvivesRepeatedApproval proves the protection follows the
// current approved baseline after a second approval replaces the first one: the
// old reference stops being protected and the new one is protected.
func TestGCProtectionSurvivesRepeatedApproval(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	oldBatch := seedVisualGCWorld(t, pool)
	seedVisualGCEvidence(t, pool, 7621, oldBatch, "screenshot")
	seedVisualGCEvidence(t, pool, 7622, oldBatch, "screenshot")

	nodeRepo := mindmappostgres.NewNodeRepository(pool)
	assertionRepo := mindmappostgres.NewAssertionRepository(pool)
	visualRepo := mindmappostgres.NewVisualAssertionRepository(pool)
	node := mustCreateNode(t, nodeRepo, newCaseNode(7630, visualGCVersion+1, nil, "case", "re-approval case"))
	assertion := newCaseAssertion(7631, node.ID, nil, "visual")
	if _, err := assertionRepo.Create(ctx, assertion); err != nil {
		t.Fatalf("create visual assertion: %v", err)
	}
	if _, err := visualRepo.Save(ctx, visualGCVersion+1, assertion.ID, node.Revision,
		visualGCParams("#chart"), 1, time.Now()); err != nil {
		t.Fatalf("save visual params: %v", err)
	}
	if _, err := visualRepo.ApproveBaseline(ctx, visualGCVersion+1, assertion.ID, 7621, 1, time.Now()); err != nil {
		t.Fatalf("first approve: %v", err)
	}
	if _, err := visualRepo.ApproveBaseline(ctx, visualGCVersion+1, assertion.ID, 7622, 1, time.Now()); err != nil {
		t.Fatalf("second approve: %v", err)
	}

	gc := executionpostgres.NewGCRepository(pool)
	for evidenceID, want := range map[int64]bool{7621: false, 7622: true} {
		protected, err := gc.IsProtected(ctx, evidenceID, 1)
		if err != nil {
			t.Fatalf("IsProtected(%d): %v", evidenceID, err)
		}
		if protected != want {
			t.Fatalf("IsProtected(%d) = %v, want %v", evidenceID, protected, want)
		}
	}
}

// visualGCParams is the comparison configuration used by the GC scenarios.
func visualGCParams(selector string) domain.VisualAssertionParams {
	return domain.VisualAssertionParams{
		Selector: selector, Threshold: 0.01,
		ViewportWidth: 1440, ViewportHeight: 900, DeviceScaleFactor: 2,
	}
}
