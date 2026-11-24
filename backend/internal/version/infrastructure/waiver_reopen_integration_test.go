//go:build integration

package versionpg_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestReopenClearsPersistedWaiverState pins the R-G8 reopen lifecycle from a
// persisted waived state (seeded directly, independent of the waive write
// path): ReopenIfCompleted must reset closure_kind/unverified AND clear every
// requirement waiver of the version, so the reconciler cannot re-close through
// a stale waiver.
func TestReopenClearsPersistedWaiverState(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := repo.Create(ctx, v, sampleAccounts()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Seed the state a waive close produces: completed + waived + unverified,
	// with one waived requirement.
	if _, err := pool.Exec(ctx, `
		UPDATE version SET status = 'completed', closure_kind = 'waived', unverified = true,
			closed_at = $2, close_reason = 'waived by owner'
		WHERE id = $1`, v.ID, testTime); err != nil {
		t.Fatalf("seed waived close state: %v", err)
	}
	requirementID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, status, waived)
		VALUES ($1, $2, 'clarified', true)`, requirementID, v.ID); err != nil {
		t.Fatalf("seed waived requirement: %v", err)
	}

	reopened, changed, err := repo.ReopenIfCompleted(ctx, v.ID, testTime.Add(time.Hour))
	if err != nil || !changed {
		t.Fatalf("ReopenIfCompleted: %v changed=%v", err, changed)
	}
	if reopened.Status != version.StatusRunning {
		t.Fatalf("reopened status = %q, want running", reopened.Status)
	}
	if reopened.ClosureKind != version.ClosureKindNormal || reopened.Unverified {
		t.Fatalf("reopened projection = %q/%v, want normal/false (reopen must clear the waiver)", reopened.ClosureKind, reopened.Unverified)
	}
	var waived bool
	if err := pool.QueryRow(ctx, `SELECT waived FROM requirement WHERE id = $1`, requirementID).Scan(&waived); err != nil {
		t.Fatalf("read requirement waived: %v", err)
	}
	if waived {
		t.Fatal("requirement waiver survived the version reopen (stale waiver would re-close the version)")
	}
}
