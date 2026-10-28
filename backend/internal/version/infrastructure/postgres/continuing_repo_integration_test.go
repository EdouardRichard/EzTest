//go:build integration

package postgres_test

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// seedContinuePhase writes the P7 lifecycle columns of a version created by
// insertVersion. VersionRepository.Create only inserts the P3 columns because
// the lifecycle writes own continue_phase/continued_at, so a test that needs a
// directly seeded continuing row sets them explicitly.
func seedContinuePhase(t *testing.T, pool *pgxpool.Pool, versionID int64, phase string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE version
		SET continue_phase = $2, continued_at = COALESCE(continued_at, now())
		WHERE id = $1`, versionID, phase); err != nil {
		t.Fatalf("seed continue phase of version %d: %v", versionID, err)
	}
}

// newContinuingRequirement builds a draft requirement of versionID.
func newContinuingRequirement(id, versionID int64) domain.Requirement {
	return domain.Requirement{
		ID: id, VersionID: versionID, Title: "Coupons", Priority: domain.PriorityShould,
		Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
}

// TestVersionRepositoryContinuingLifecycle covers the completed -> continuing ->
// completed lifecycle writes of spec §5.1/§4.2.5.
func TestVersionRepositoryContinuingLifecycle(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	completed := insertVersion(t, repo, domain.Version{
		ID: 2001, Name: "Order v2", Status: domain.VersionCompleted, CreatedBy: 7, UpdatedBy: 7,
	})

	moved, transitioned, err := repo.MarkContinuing(ctx, completed.ID, 42)
	if err != nil {
		t.Fatalf("MarkContinuing: %v", err)
	}
	if !transitioned {
		t.Fatal("MarkContinuing did not report the transition")
	}
	if moved.Status != domain.VersionContinuing || moved.ContinuePhase != domain.ContinuePhaseClarifying {
		t.Fatalf("moved status/phase = %s/%s, want continuing/clarifying", moved.Status, moved.ContinuePhase)
	}
	if moved.ContinuedAt == nil {
		t.Fatal("continued_at was not stamped")
	}
	if moved.Revision != completed.Revision+1 || moved.UpdatedBy != 42 {
		t.Fatalf("moved revision/updatedBy = %d/%d", moved.Revision, moved.UpdatedBy)
	}

	again, transitioned, err := repo.MarkContinuing(ctx, completed.ID, 43)
	if err != nil {
		t.Fatalf("idempotent MarkContinuing: %v", err)
	}
	if transitioned || again.Status != domain.VersionContinuing || again.Revision != moved.Revision {
		t.Fatalf("idempotent MarkContinuing = transitioned %v revision %d", transitioned, again.Revision)
	}

	draft := insertVersion(t, repo, domain.Version{ID: 2002, Name: "Draft", CreatedBy: 7, UpdatedBy: 7})
	assertVersionKind(t, errFromMarkContinuing(repo, draft.ID), domain.KindStateConflict, "version_not_continuable")

	phase, err := repo.SetContinuePhase(ctx, completed.ID, domain.ContinuePhaseDesigning, 42)
	if err != nil {
		t.Fatalf("SetContinuePhase: %v", err)
	}
	if phase.ContinuePhase != domain.ContinuePhaseDesigning {
		t.Fatalf("phase = %q, want designing", phase.ContinuePhase)
	}
	if _, err := repo.SetContinuePhase(ctx, draft.ID, domain.ContinuePhaseDesigning, 42); err == nil {
		t.Fatal("SetContinuePhase accepted a non-continuing version")
	}

	done, transitioned, err := repo.CompleteContinuing(ctx, completed.ID, 42)
	if err != nil {
		t.Fatalf("CompleteContinuing: %v", err)
	}
	if !transitioned || done.Status != domain.VersionCompleted || done.ContinuePhase != "" {
		t.Fatalf("completed row = %+v transitioned=%v", done, transitioned)
	}
	if _, transitioned, err := repo.CompleteContinuing(ctx, completed.ID, 42); err != nil || transitioned {
		t.Fatalf("idempotent CompleteContinuing = %v/%v", transitioned, err)
	}
}

// errFromMarkContinuing returns the error of a MarkContinuing call.
func errFromMarkContinuing(repo *postgres.VersionRepository, id int64) error {
	_, _, err := repo.MarkContinuing(context.Background(), id, 42)
	return err
}

// TestVersionRepositoryRefreshContinuePhase covers the atomic phase derivation
// of spec §5.1: any draft keeps/moves the phase to clarifying, all non-draft
// moves it to designing, and an executing phase is never interrupted.
func TestVersionRepositoryRefreshContinuePhase(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)
	requirements := postgres.NewRequirementRepository(pool)

	version := insertVersion(t, repo, domain.Version{
		ID: 2025, Name: "Order v2", Status: domain.VersionContinuing,
		ContinuePhase: domain.ContinuePhaseClarifying, CreatedBy: 7, UpdatedBy: 7,
	})
	seedContinuePhase(t, pool, version.ID, domain.ContinuePhaseClarifying)
	first := insertRequirement(t, requirements, newContinuingRequirement(3051, version.ID))
	second := insertRequirement(t, requirements, newContinuingRequirement(3052, version.ID))

	if _, changed, err := repo.RefreshContinuePhase(ctx, version.ID, 42); err != nil || changed {
		t.Fatalf("all-draft refresh = %v/%v, want unchanged", changed, err)
	}

	if _, err := repo.SetContinuePhase(ctx, version.ID, domain.ContinuePhaseDesigning, 42); err != nil {
		t.Fatalf("SetContinuePhase designing: %v", err)
	}
	updated, changed, err := repo.RefreshContinuePhase(ctx, version.ID, 42)
	if err != nil || !changed || updated.ContinuePhase != domain.ContinuePhaseClarifying {
		t.Fatalf("draft refresh = %+v/%v/%v, want clarifying/changed", updated, changed, err)
	}

	mustUpdateStatus(requirements, ctx, first.ID, domain.RequirementDraft, domain.RequirementClarified, 42)
	mustUpdateStatus(requirements, ctx, second.ID, domain.RequirementDraft, domain.RequirementClarified, 42)
	updated, changed, err = repo.RefreshContinuePhase(ctx, version.ID, 42)
	if err != nil || !changed || updated.ContinuePhase != domain.ContinuePhaseDesigning {
		t.Fatalf("confirmed refresh = %+v/%v/%v, want designing/changed", updated, changed, err)
	}

	if _, err := repo.SetContinuePhase(ctx, version.ID, domain.ContinuePhaseExecuting, 42); err != nil {
		t.Fatalf("SetContinuePhase executing: %v", err)
	}
	updated, changed, err = repo.RefreshContinuePhase(ctx, version.ID, 42)
	if err != nil || changed || updated != nil {
		t.Fatalf("executing refresh = %+v/%v/%v, want untouched", updated, changed, err)
	}

	// A new draft requirement inserted while phase=executing must not interrupt
	// the running incremental batch either.
	insertRequirement(t, requirements, newContinuingRequirement(3053, version.ID))
	if _, changed, err := repo.RefreshContinuePhase(ctx, version.ID, 42); err != nil || changed {
		t.Fatalf("executing refresh after a draft insert = %v/%v, want untouched", changed, err)
	}
	stored, err := repo.Get(ctx, version.ID)
	if err != nil || stored.ContinuePhase != domain.ContinuePhaseExecuting {
		t.Fatalf("phase after a draft insert = %+v/%v, want executing", stored, err)
	}
}

// TestVersionRepositoryFinalize covers the finalize CAS, including the paused
// guard repeated in SQL.
func TestVersionRepositoryFinalize(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	executing := insertVersion(t, repo, domain.Version{
		ID: 2011, Name: "Executing", Status: domain.VersionExecuting,
		PauseMode: domain.PauseModeGraceful, CreatedBy: 7, UpdatedBy: 7,
	})
	if _, err := repo.SetPaused(ctx, executing.ID, true, domain.PauseModeGraceful, 7); err != nil {
		t.Fatalf("SetPaused: %v", err)
	}
	// The repository documents a paused finalize as a version_state_conflict (the
	// SQL guard refuses the CAS); the service's paused short-circuit returns
	// false,nil before calling the repository, so this is the repo contract.
	if _, transitioned, err := repo.Finalize(ctx, executing.ID, 0); transitioned {
		t.Fatal("paused finalize reported a transition")
	} else {
		assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	}
	if _, err := repo.SetPaused(ctx, executing.ID, false, domain.PauseModeGraceful, 7); err != nil {
		t.Fatalf("resume: %v", err)
	}
	finalized, transitioned, err := repo.Finalize(ctx, executing.ID, 0)
	if err != nil || !transitioned {
		t.Fatalf("finalize = %v/%v, want true/nil", transitioned, err)
	}
	if finalized.Status != domain.VersionCompleted {
		t.Fatalf("status = %s, want completed", finalized.Status)
	}
	if _, transitioned, err := repo.Finalize(ctx, executing.ID, 0); err != nil || transitioned {
		t.Fatalf("idempotent finalize = %v/%v", transitioned, err)
	}
}

// TestVersionRepositoryCreateRequirementContinuing covers the D43 same-
// transaction trigger and its concurrency rule.
func TestVersionRepositoryCreateRequirementContinuing(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)
	requirements := postgres.NewRequirementRepository(pool)

	completed := insertVersion(t, repo, domain.Version{
		ID: 2021, Name: "Order v2", Status: domain.VersionCompleted, CreatedBy: 7, UpdatedBy: 7,
	})
	first := newContinuingRequirement(3001, completed.ID)
	transitioned, err := repo.CreateRequirementContinuing(ctx, &first, 42)
	if err != nil {
		t.Fatalf("CreateRequirementContinuing: %v", err)
	}
	if !transitioned {
		t.Fatal("first create did not report the transition")
	}
	version, err := repo.Get(ctx, completed.ID)
	if err != nil {
		t.Fatalf("Get version: %v", err)
	}
	if version.Status != domain.VersionContinuing || version.ContinuePhase != domain.ContinuePhaseClarifying {
		t.Fatalf("version status/phase = %s/%s", version.Status, version.ContinuePhase)
	}
	if version.ContinuedAt == nil || version.Revision != completed.Revision+1 {
		t.Fatalf("version continuedAt/revision = %v/%d", version.ContinuedAt, version.Revision)
	}
	if stored, err := requirements.Get(ctx, first.ID); err != nil || stored.Title != "Coupons" {
		t.Fatalf("stored requirement = %+v/%v", stored, err)
	}

	second := newContinuingRequirement(3002, completed.ID)
	transitioned, err = repo.CreateRequirementContinuing(ctx, &second, 42)
	if err != nil {
		t.Fatalf("second CreateRequirementContinuing: %v", err)
	}
	if transitioned {
		t.Fatal("second create reported a second transition")
	}

	archived := insertVersion(t, repo, domain.Version{
		ID: 2022, Name: "Archived", Status: domain.VersionArchived, CreatedBy: 7, UpdatedBy: 7,
	})
	blocked := newContinuingRequirement(3003, archived.ID)
	_, err = repo.CreateRequirementContinuing(ctx, &blocked, 42)
	assertVersionKind(t, err, domain.KindStateConflict, "version_not_continuable")
	if _, err := requirements.Get(ctx, blocked.ID); err == nil {
		t.Fatal("blocked continuing create inserted the requirement")
	}
}

// TestVersionRepositoryCreateRequirementContinuingConcurrent proves two
// concurrent creates on a completed version transition it exactly once.
func TestVersionRepositoryCreateRequirementContinuingConcurrent(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	completed := insertVersion(t, repo, domain.Version{
		ID: 2031, Name: "Order v2", Status: domain.VersionCompleted, CreatedBy: 7, UpdatedBy: 7,
	})

	var (
		wg        sync.WaitGroup
		mu        sync.Mutex
		transited int
	)
	for _, id := range []int64{3101, 3102} {
		wg.Add(1)
		go func(requirementID int64) {
			defer wg.Done()
			requirement := newContinuingRequirement(requirementID, completed.ID)
			transitioned, err := repo.CreateRequirementContinuing(ctx, &requirement, 42)
			if err != nil {
				t.Errorf("concurrent create %d: %v", requirementID, err)
				return
			}
			if transitioned {
				mu.Lock()
				transited++
				mu.Unlock()
			}
		}(id)
	}
	wg.Wait()
	if transited != 1 {
		t.Fatalf("transitions = %d, want exactly 1", transited)
	}
	if got, err := repo.Get(ctx, completed.ID); err != nil || got.Status != domain.VersionContinuing {
		t.Fatalf("version = %+v/%v", got, err)
	}
}

// TestVersionRepositoryFinalizeBatchState covers the read-only finalize
// projection over run_batch (spec §5.1).
func TestVersionRepositoryFinalizeBatchState(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	version := insertVersion(t, repo, domain.Version{
		ID: 2051, Name: "Order v2", Status: domain.VersionContinuing,
		ContinuePhase: domain.ContinuePhaseExecuting, CreatedBy: 7, UpdatedBy: 7,
	})
	seedContinuePhase(t, pool, version.ID, domain.ContinuePhaseExecuting)
	hash := strings.Repeat("a", 64)
	batches := []struct {
		id           int64
		batchNo      int
		status       string
		kind         string
		autonomyDone bool
		parentID     *int64
	}{
		{id: 3601, batchNo: 1, status: "planned", kind: "baseline"},
		{id: 3602, batchNo: 2, status: "done", kind: "baseline"},
		{id: 3603, batchNo: 3, status: "done", kind: "supplement", autonomyDone: true, parentID: ptrInt64(3602)},
		{id: 3604, batchNo: 4, status: "done", kind: "incremental", autonomyDone: true},
	}
	for _, batch := range batches {
		if _, err := pool.Exec(ctx, `
			INSERT INTO run_batch (id, version_id, batch_no, status, kind, baseline_hash, graph_hash,
			                       parent_batch_id, autonomy_done, created_by)
			VALUES ($1, $2, $3, $4, $5, $6, $6, $7, $8, 7)`,
			batch.id, version.ID, batch.batchNo, batch.status, batch.kind, hash,
			batch.parentID, batch.autonomyDone); err != nil {
			t.Fatalf("insert run_batch %d: %v", batch.id, err)
		}
	}

	state, err := repo.FinalizeBatchState(ctx, version.ID)
	if err != nil {
		t.Fatalf("FinalizeBatchState: %v", err)
	}
	if state.TotalBatches != 4 || state.ActiveBatches != 1 ||
		state.UnsettledBatches != 1 || state.IncrementalBatches != 1 {
		t.Fatalf("state = %+v, want total 4 active 1 unsettled 1 incremental 1", state)
	}
}

// TestVersionRepositoryChangesDerivation covers the D50 read-time projection:
// the four source kinds are merged newest first.
func TestVersionRepositoryChangesDerivation(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)
	requirements := postgres.NewRequirementRepository(pool)

	version := insertVersion(t, repo, domain.Version{
		ID: 2041, Name: "Order v2", Status: domain.VersionContinuing,
		ContinuePhase: domain.ContinuePhaseExecuting, CreatedBy: 7, UpdatedBy: 7,
	})
	seedContinuePhase(t, pool, version.ID, domain.ContinuePhaseExecuting)
	requirement := insertRequirement(t, requirements, newContinuingRequirement(3201, version.ID))

	base := time.Now().UTC().Truncate(time.Second)
	if _, err := pool.Exec(ctx, `
		INSERT INTO run_batch (id, version_id, batch_no, status, baseline_hash, graph_hash, created_by, finished_at)
		VALUES ($1, $2, 1, 'done', $3, $3, 7, $4)`,
		3301, version.ID, strings.Repeat("a", 64), base.Add(time.Minute)); err != nil {
		t.Fatalf("insert run_batch: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO report (id, version_id, batch_id, revision, status, title, content,
		                    input_fingerprint, published_at)
		VALUES ($1, $2, $3, 1, 'published', 'report', '{}'::jsonb, $4, $5)`,
		3401, version.ID, 3301, strings.Repeat("b", 64), base.Add(2*time.Minute)); err != nil {
		t.Fatalf("insert report: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO issue (id, version_id, source, severity, title, created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, 'human', 'major', 'issue', 7, 7, $3, $3)`,
		3501, version.ID, base.Add(3*time.Minute)); err != nil {
		t.Fatalf("insert issue: %v", err)
	}

	changes, err := repo.Changes(ctx, version.ID)
	if err != nil {
		t.Fatalf("Changes: %v", err)
	}
	if len(changes) != 4 {
		t.Fatalf("changes = %d, want 4 (%+v)", len(changes), changes)
	}
	wantKinds := []string{
		domain.VersionChangeIssueCreated,
		domain.VersionChangeReportPublished,
		domain.VersionChangeBatchFinished,
		domain.VersionChangeRequirementAdded,
	}
	wantRefs := []string{"3501", "3401", "3301", "3201"}
	for i := range wantKinds {
		if changes[i].Kind != wantKinds[i] || changes[i].RefID != wantRefs[i] {
			t.Fatalf("change[%d] = %s/%s, want %s/%s",
				i, changes[i].Kind, changes[i].RefID, wantKinds[i], wantRefs[i])
		}
	}
	if changes[3].RefID != strconv.FormatInt(requirement.ID, 10) {
		t.Fatalf("requirement change ref = %s, want %d", changes[3].RefID, requirement.ID)
	}
}
