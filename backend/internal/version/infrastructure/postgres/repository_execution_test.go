//go:build integration

package postgres_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// TestVersionRepositoryExecutionState covers the P6 execution columns:
// scanner/round-trip, the defaults of a freshly created version, the
// paused/pause_mode/batch_no write, the executing|continuing status guard and
// the paused partial index (spec §3.3/§5.4).
func TestVersionRepositoryExecutionState(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewVersionRepository(pool)

	stored := insertVersion(t, repo, domain.Version{ID: 6001, Name: "Execution state", CreatedBy: 7, UpdatedBy: 7})
	if stored.Paused || stored.PauseMode != domain.PauseModeGraceful || stored.BatchNo != 0 {
		t.Fatalf("new version execution defaults = paused=%v mode=%q batch=%d, want false/graceful/0",
			stored.Paused, stored.PauseMode, stored.BatchNo)
	}

	// The fresh scanner path (Get) sees the same defaults.
	loaded, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get fresh: %v", err)
	}
	if loaded.Paused || loaded.PauseMode != domain.PauseModeGraceful || loaded.BatchNo != 0 {
		t.Fatalf("loaded execution defaults = %+v", loaded)
	}

	// Execution-state writes are only legal while executing (or continuing).
	// Move the seeded draft row straight to executing through SQL: UpdateStatus
	// only models the P3/P5 transitions.
	if _, err := pool.Exec(ctx, `UPDATE version SET status = 'executing' WHERE id = $1`, stored.ID); err != nil {
		t.Fatalf("set executing: %v", err)
	}

	paused, err := repo.UpdateExecutionState(ctx, stored.ID, true, domain.PauseModeImmediate, 0, 42)
	if err != nil {
		t.Fatalf("UpdateExecutionState pause: %v", err)
	}
	if !paused.Paused || paused.PauseMode != domain.PauseModeImmediate || paused.BatchNo != 0 ||
		paused.Status != domain.VersionExecuting || paused.Revision != 2 || paused.UpdatedBy != 42 {
		t.Fatalf("paused row = %+v", paused)
	}

	withBatch, err := repo.UpdateExecutionState(ctx, stored.ID, true, domain.PauseModeImmediate, 4, 43)
	if err != nil {
		t.Fatalf("UpdateExecutionState batch: %v", err)
	}
	if withBatch.BatchNo != 4 || withBatch.Revision != 3 || withBatch.UpdatedBy != 43 {
		t.Fatalf("batch row = %+v", withBatch)
	}

	resumed, err := repo.UpdateExecutionState(ctx, stored.ID, false, domain.PauseModeImmediate, 4, 43)
	if err != nil {
		t.Fatalf("UpdateExecutionState resume: %v", err)
	}
	if resumed.Paused || resumed.PauseMode != domain.PauseModeImmediate || resumed.BatchNo != 4 {
		t.Fatalf("resumed row = %+v", resumed)
	}

	// The paused partial index exists on the renamed singular table.
	var indexName *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.idx_versions_paused')::text`).Scan(&indexName); err != nil {
		t.Fatalf("read paused index: %v", err)
	}
	if indexName == nil || *indexName != "idx_versions_paused" {
		t.Fatalf("paused index = %v, want idx_versions_paused", indexName)
	}

	// completing the version closes the execution-state write path.
	completed, err := repo.UpdateStatus(ctx, stored.ID, domain.VersionExecuting, domain.VersionCompleted, 44)
	if err != nil {
		t.Fatalf("UpdateStatus completed: %v", err)
	}
	if completed.Status != domain.VersionCompleted {
		t.Fatalf("completed status = %s", completed.Status)
	}
	_, err = repo.UpdateExecutionState(ctx, stored.ID, true, domain.PauseModeGraceful, 5, 44)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
	afterBlocked, err := repo.Get(ctx, stored.ID)
	if err != nil {
		t.Fatalf("Get after blocked execution state: %v", err)
	}
	if afterBlocked.Status != domain.VersionCompleted || afterBlocked.Paused ||
		afterBlocked.PauseMode != domain.PauseModeImmediate || afterBlocked.BatchNo != 4 ||
		afterBlocked.Revision != completed.Revision {
		t.Fatalf("blocked execution-state write changed the row: %+v", afterBlocked)
	}

	// Missing rows are the same state conflict, matching the port contract.
	_, err = repo.UpdateExecutionState(ctx, 999999, false, domain.PauseModeGraceful, 0, 44)
	assertVersionKind(t, err, domain.KindStateConflict, "version_state_conflict")
}
