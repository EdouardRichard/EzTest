//go:build integration

package integration_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// visualRepoVersion is the version of the visual-repository scenarios.
const visualRepoVersion = int64(7701)

// visualRepoFixture seeds one case node with one assertion of kind.
func visualRepoFixture(t *testing.T, pool *pgxpool.Pool, versionID, nodeID, assertionID int64, kind string) (domain.CaseNode, domain.CaseAssertion) {
	t.Helper()
	nodeRepo := postgres.NewNodeRepository(pool)
	assertionRepo := postgres.NewAssertionRepository(pool)
	node := mustCreateNode(t, nodeRepo, newCaseNode(nodeID, versionID, nil, domain.NodeKindCase, "visual assertion case"))
	created, err := assertionRepo.Create(context.Background(), newCaseAssertion(assertionID, node.ID, nil, kind))
	requireNoError(t, err, "create assertion")
	return node, created
}

// visualRepoParams is a fully populated comparison configuration.
func visualRepoParams(selector string, threshold float64) domain.VisualAssertionParams {
	return domain.VisualAssertionParams{
		Selector:      selector,
		Region:        &domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3},
		Threshold:     threshold,
		ViewportWidth: 1440, ViewportHeight: 900, DeviceScaleFactor: 2,
	}
}

// TestAssertionListByVersionAttachesVisualParams is the regression test of fix
// round 1 finding 1: ListByVersion joins the optional visual projection and must
// scan the base assertion and the LEFT JOINed visual columns in one Scan. A
// non-visual assertion of the same version keeps a nil Visual.
func TestAssertionListByVersionAttachesVisualParams(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	assertionRepo := postgres.NewAssertionRepository(pool)
	visualRepo := postgres.NewVisualAssertionRepository(pool)
	node, assertion := visualRepoFixture(t, pool, visualRepoVersion+5, 7771, 7772, "visual")
	_, err := assertionRepo.Create(ctx, newCaseAssertion(7773, node.ID, nil, "ui"))
	requireNoError(t, err, "create non-visual assertion")
	_, err = visualRepo.Save(ctx, visualRepoVersion+5, assertion.ID, node.Revision,
		visualRepoParams("#receipt", 0.01), 42, testTime)
	requireNoError(t, err, "save visual params")

	assertions, err := assertionRepo.ListByVersion(ctx, visualRepoVersion+5)
	requireNoError(t, err, "list assertions by version")
	if len(assertions) != 2 {
		t.Fatalf("assertions = %d, want 2", len(assertions))
	}
	seen := make(map[int64]domain.CaseAssertion, len(assertions))
	for _, listed := range assertions {
		seen[listed.ID] = listed
	}
	visual, ok := seen[assertion.ID]
	if !ok {
		t.Fatalf("visual assertion %d missing from ListByVersion: %+v", assertion.ID, assertions)
	}
	if visual.Visual == nil {
		t.Fatalf("visual assertion has no attached parameters: %+v", visual)
	}
	if visual.Visual.Selector != "#receipt" || visual.Visual.Threshold != 0.01 ||
		visual.Visual.ViewportWidth != 1440 || visual.Visual.ViewportHeight != 900 ||
		visual.Visual.DeviceScaleFactor != 2 || visual.Visual.Revision != 1 {
		t.Fatalf("attached visual params = %+v", visual.Visual)
	}
	if visual.Visual.Region == nil || *visual.Visual.Region != (domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3}) {
		t.Fatalf("attached visual region = %+v", visual.Visual.Region)
	}
	plain, ok := seen[7773]
	if !ok {
		t.Fatalf("non-visual assertion missing from ListByVersion")
	}
	if plain.Kind != "ui" || plain.Visual != nil {
		t.Fatalf("non-visual assertion = %+v", plain)
	}
}

// TestVisualAssertionRepositorySaveReadApprove covers the real-PostgreSQL
// Get/Save/ApproveBaseline round trips, the node-revision CAS, the transaction
// rollback of a stale Save, the no-revision-bump approval and the
// version/assertion ownership 404s.
func TestVisualAssertionRepositorySaveReadApprove(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewVisualAssertionRepository(pool)
	node, assertion := visualRepoFixture(t, pool, visualRepoVersion, 7711, 7712, "visual")
	now := testTime.Add(time.Hour)

	target, err := repo.Get(ctx, visualRepoVersion, assertion.ID)
	requireNoError(t, err, "get visual assertion before save")
	if target.NodeID != node.ID || target.NodeRevision != 1 || target.Kind != "visual" || target.Visual != nil {
		t.Fatalf("initial target = %+v", target)
	}

	stored, err := repo.Save(ctx, visualRepoVersion, assertion.ID, target.NodeRevision,
		visualRepoParams("#chart", 0.02), 42, now)
	requireNoError(t, err, "save visual params")
	if stored.AssertionID != assertion.ID || stored.Selector != "#chart" ||
		stored.Threshold != 0.02 || stored.ViewportWidth != 1440 ||
		stored.ViewportHeight != 900 || stored.DeviceScaleFactor != 2 ||
		stored.Revision != 1 || stored.HasBaseline() || stored.ApprovedBy != nil {
		t.Fatalf("stored visual = %+v", stored)
	}
	if stored.Region == nil || *stored.Region != (domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3}) {
		t.Fatalf("stored region = %+v", stored.Region)
	}

	loaded, err := repo.Get(ctx, visualRepoVersion, assertion.ID)
	requireNoError(t, err, "get visual assertion after save")
	if loaded.NodeRevision != 2 || loaded.Visual == nil {
		t.Fatalf("target after save = %+v", loaded)
	}
	if loaded.Visual.Threshold != 0.02 || loaded.Visual.Revision != 1 || loaded.Visual.Selector != "#chart" {
		t.Fatalf("stored visual after save = %+v", loaded.Visual)
	}

	// A stale CAS must fail and roll the upsert back: the row keeps the previous
	// parameters and revision.
	_, err = repo.Save(ctx, visualRepoVersion, assertion.ID, 1, visualRepoParams("#other", 0.09), 42, now.Add(time.Minute))
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	rolledBack, err := repo.Get(ctx, visualRepoVersion, assertion.ID)
	requireNoError(t, err, "get after stale save")
	if rolledBack.Visual == nil || rolledBack.Visual.Selector != "#chart" ||
		rolledBack.Visual.Threshold != 0.02 || rolledBack.Visual.Revision != 1 {
		t.Fatalf("stale Save was not rolled back: %+v", rolledBack.Visual)
	}

	// A fresh revision succeeds and bumps both the visual and node revisions.
	stored, err = repo.Save(ctx, visualRepoVersion, assertion.ID, 2, visualRepoParams("#chart", 0.03), 42, now.Add(2*time.Minute))
	requireNoError(t, err, "save visual params with current revision")
	if stored.Revision != 2 || stored.Threshold != 0.03 {
		t.Fatalf("second save = %+v", stored)
	}

	approvedAt := now.Add(3 * time.Minute)
	approved, err := repo.ApproveBaseline(ctx, visualRepoVersion, assertion.ID, 901, 43, approvedAt)
	requireNoError(t, err, "approve baseline")
	if approved.BaselineEvidenceID == nil || *approved.BaselineEvidenceID != 901 ||
		approved.ApprovedBy == nil || *approved.ApprovedBy != 43 ||
		approved.ApprovedAt == nil || !approved.ApprovedAt.Equal(approvedAt) {
		t.Fatalf("approved visual = %+v", approved)
	}
	afterApproval, err := repo.Get(ctx, visualRepoVersion, assertion.ID)
	requireNoError(t, err, "get after approval")
	if afterApproval.NodeRevision != 3 {
		t.Fatalf("approval bumped the node revision: %d, want 3", afterApproval.NodeRevision)
	}
	if afterApproval.Visual == nil || afterApproval.Visual.Revision != 2 {
		t.Fatalf("approval bumped the visual revision: %+v", afterApproval.Visual)
	}

	// A repeated approval replaces the baseline and the approver without any
	// revision change.
	reapprovedAt := approvedAt.Add(time.Minute)
	reapproved, err := repo.ApproveBaseline(ctx, visualRepoVersion, assertion.ID, 902, 44, reapprovedAt)
	requireNoError(t, err, "re-approve baseline")
	if reapproved.BaselineEvidenceID == nil || *reapproved.BaselineEvidenceID != 902 ||
		reapproved.ApprovedBy == nil || *reapproved.ApprovedBy != 44 ||
		reapproved.Revision != 2 {
		t.Fatalf("re-approved visual = %+v", reapproved)
	}

	// Version and assertion ownership are enforced on every method.
	requireDomainError(t,
		func() error { _, err := repo.Get(ctx, visualRepoVersion+1, assertion.ID); return err }(),
		domain.KindNotFound, "assertion_not_found")
	requireDomainError(t,
		func() error { _, err := repo.Get(ctx, visualRepoVersion, assertion.ID+1000); return err }(),
		domain.KindNotFound, "assertion_not_found")
	requireDomainError(t,
		func() error {
			_, err := repo.Save(ctx, visualRepoVersion+1, assertion.ID, 3, visualRepoParams("#chart", 0.02), 42, now)
			return err
		}(),
		domain.KindNotFound, "assertion_not_found")
	requireDomainError(t,
		func() error {
			_, err := repo.ApproveBaseline(ctx, visualRepoVersion+1, assertion.ID, 901, 43, now)
			return err
		}(),
		domain.KindNotFound, "assertion_not_found")
}

// TestVisualAssertionRepositoryRejectsNonVisualAssertion proves the repository
// never creates comparison parameters for an assertion of another kind.
func TestVisualAssertionRepositoryRejectsNonVisualAssertion(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewVisualAssertionRepository(pool)
	node, assertion := visualRepoFixture(t, pool, visualRepoVersion+1, 7721, 7722, "ui")

	_, err := repo.Save(ctx, visualRepoVersion+1, assertion.ID, node.Revision, visualRepoParams("#chart", 0.02), 42, testTime)
	requireDomainError(t, err, domain.KindInvalid, "invalid_assertion_kind")
	_, err = repo.ApproveBaseline(ctx, visualRepoVersion+1, assertion.ID, 901, 43, testTime)
	requireDomainError(t, err, domain.KindInvalid, "invalid_assertion_kind")

	target, err := repo.Get(ctx, visualRepoVersion+1, assertion.ID)
	requireNoError(t, err, "get non-visual assertion")
	if target.Kind != "ui" || target.Visual != nil {
		t.Fatalf("non-visual target = %+v", target)
	}
}

// TestVisualAssertionRepositoryConcurrentCAS proves exactly one of two
// concurrent writes at the same expected node revision wins; the loser gets
// case_revision_conflict and neither the node nor the visual revision is
// corrupted.
func TestVisualAssertionRepositoryConcurrentCAS(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	repo := postgres.NewVisualAssertionRepository(pool)
	_, assertion := visualRepoFixture(t, pool, visualRepoVersion+2, 7731, 7732, "visual")

	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, selector := range []string{"#left", "#right"} {
		selector := selector
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Save(ctx, visualRepoVersion+2, assertion.ID, 1,
				visualRepoParams(selector, 0.02), 42, testTime)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)

	successes, conflicts := 0, 0
	for err := range errs {
		if err == nil {
			successes++
			continue
		}
		var domainErr *domain.Error
		if errors.As(err, &domainErr) && domainErr.Kind == domain.KindStateConflict &&
			domainErr.Reason == "case_revision_conflict" {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent Save error: %v", err)
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("concurrent Save = %d successes / %d conflicts, want 1/1", successes, conflicts)
	}

	target, err := repo.Get(ctx, visualRepoVersion+2, assertion.ID)
	requireNoError(t, err, "get after concurrent save")
	if target.NodeRevision != 2 {
		t.Fatalf("node revision after concurrent save = %d, want 2", target.NodeRevision)
	}
	if target.Visual == nil || target.Visual.Revision != 1 {
		t.Fatalf("visual after concurrent save = %+v", target.Visual)
	}
}

// TestVisualBaselineEvidenceRepositoryOwnership proves the baseline reader only
// returns evidence whose batch belongs to the requested version and whose case
// run belongs to the requested case node; every foreign id is a not-found.
func TestVisualBaselineEvidenceRepositoryOwnership(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	node, _ := visualRepoFixture(t, pool, visualRepoVersion+3, 7741, 7742, "visual")

	hash := repeatHex('c', 64)
	mustExec(t, pool, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by)
		VALUES (7751, $1, 1, 'done', $2, $2, 1)`, visualRepoVersion+3, hash)
	mustExec(t, pool, `
		INSERT INTO case_run (id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot, status)
		VALUES (7752, 7751, $1, $2, 1, $3, '{}'::jsonb, 'running')`, visualRepoVersion+3, node.ID, hash)
	// A foreign batch/version owns a second evidence row.
	mustExec(t, pool, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by)
		VALUES (7753, $1, 1, 'done', $2, $2, 1)`, visualRepoVersion+4, hash)
	mustExec(t, pool, `
		INSERT INTO case_run (id, batch_id, version_id, case_node_id, case_revision, case_hash, case_snapshot, status)
		VALUES (7754, 7753, $1, $2, 1, $3, '{}'::jsonb, 'running')`, visualRepoVersion+4, node.ID, hash)

	store, err := filestore.New(t.TempDir())
	requireNoError(t, err, "new filestore")
	data := []byte("\x89PNG\r\n\x1a\nbaseline-bytes")
	storedPath, sum, size, err := store.Write(ctx, "7751/7752/7761.png", data)
	requireNoError(t, err, "write baseline artifact")
	mustExec(t, pool, `
		INSERT INTO evidence (id, batch_id, case_run_id, kind, path, sha256, size_bytes, mime)
		VALUES (7761, 7751, 7752, 'screenshot', $1, $2, $3, 'image/png')`, storedPath, sum, size)
	foreignPath, foreignSum, foreignSize, err := store.Write(ctx, "7753/7754/7762.png", data)
	requireNoError(t, err, "write foreign baseline artifact")
	mustExec(t, pool, `
		INSERT INTO evidence (id, batch_id, case_run_id, kind, path, sha256, size_bytes, mime)
		VALUES (7762, 7753, 7754, 'screenshot', $1, $2, $3, 'image/png')`, foreignPath, foreignSum, foreignSize)

	reader := postgres.NewVisualBaselineEvidenceRepository(pool, store)
	evidence, err := reader.ReadForBaseline(ctx, visualRepoVersion+3, node.ID, 7761)
	requireNoError(t, err, "read owned baseline")
	if string(evidence.Data) != string(data) || evidence.SizeBytes != int64(len(data)) || evidence.Kind != "screenshot" {
		t.Fatalf("baseline evidence = %+v", evidence)
	}

	requireDomainError(t,
		func() error {
			_, err := reader.ReadForBaseline(ctx, visualRepoVersion+4, node.ID, 7761)
			return err
		}(),
		domain.KindNotFound, "evidence_not_found")
	requireDomainError(t,
		func() error {
			_, err := reader.ReadForBaseline(ctx, visualRepoVersion+3, node.ID+1, 7761)
			return err
		}(),
		domain.KindNotFound, "evidence_not_found")
	requireDomainError(t,
		func() error {
			_, err := reader.ReadForBaseline(ctx, visualRepoVersion+3, node.ID, 7762)
			return err
		}(),
		domain.KindNotFound, "evidence_not_found")
	requireDomainError(t,
		func() error {
			_, err := reader.ReadForBaseline(ctx, visualRepoVersion+3, node.ID, 999999)
			return err
		}(),
		domain.KindNotFound, "evidence_not_found")
}

// repeatHex returns a hex string of length n filled with digit.
func repeatHex(digit byte, n int) string {
	buffer := make([]byte, n)
	for index := range buffer {
		buffer[index] = digit
	}
	return string(buffer)
}
