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

// TestVersionWaiverProjectionRoundTrip pins the T2→T6 column wiring at the
// repository level: Create defaults closure_kind/unverified to normal/false,
// the Close terminal entry persists the waived projection, and ReopenIfCompleted
// clears BOTH the version projection and every requirement waiver of the
// version atomically (R-G8 lifecycle).
func TestVersionWaiverProjectionRoundTrip(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := repo.Create(ctx, v, sampleAccounts()); err != nil {
		t.Fatalf("Create: %v", err)
	}
	stored, _, err := repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ClosureKind != version.ClosureKindNormal || stored.Unverified {
		t.Fatalf("fresh version projection = %q/%v, want normal/false", stored.ClosureKind, stored.Unverified)
	}

	// A waived requirement of the version (fixture: the waive write path is
	// exercised end-to-end in the server integration suite).
	requirementID := ids.Next()
	if _, err := pool.Exec(ctx, `
		INSERT INTO requirement (id, version_id, status, waived)
		VALUES ($1, $2, 'clarified', true)`, requirementID, v.ID); err != nil {
		t.Fatalf("seed waived requirement: %v", err)
	}

	if _, err := repo.MarkRunning(ctx, v.ID, testTime); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	closed, err := repo.Close(ctx, v.ID, &version.VersionCloseFields{
		ClosedAt:    testTime,
		CloseReason: "waived by owner",
		ClosureKind: version.ClosureKindWaived,
		Unverified:  true,
	}, nil)
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if closed.ClosureKind != version.ClosureKindWaived || !closed.Unverified {
		t.Fatalf("closed projection = %q/%v, want waived/true", closed.ClosureKind, closed.Unverified)
	}
	stored, _, err = repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get after close: %v", err)
	}
	if stored.ClosureKind != version.ClosureKindWaived || !stored.Unverified {
		t.Fatalf("persisted projection = %q/%v, want waived/true", stored.ClosureKind, stored.Unverified)
	}

	reopened, changed, err := repo.ReopenIfCompleted(ctx, v.ID, testTime.Add(time.Hour))
	if err != nil || !changed {
		t.Fatalf("ReopenIfCompleted: %v changed=%v", err, changed)
	}
	if reopened.ClosureKind != version.ClosureKindNormal || reopened.Unverified {
		t.Fatalf("reopened projection = %q/%v, want normal/false", reopened.ClosureKind, reopened.Unverified)
	}
	var waived bool
	if err := pool.QueryRow(ctx, `SELECT waived FROM requirement WHERE id = $1`, requirementID).Scan(&waived); err != nil {
		t.Fatalf("read requirement waived: %v", err)
	}
	if waived {
		t.Fatal("requirement waiver survived the version reopen")
	}

	// A normal close after reopen writes the normal projection explicitly.
	if _, err := repo.Close(ctx, v.ID, &version.VersionCloseFields{
		ClosedAt:    testTime.Add(2 * time.Hour),
		CloseReason: "all checks pass",
		ClosureKind: version.ClosureKindNormal,
	}, nil); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	stored, _, err = repo.Get(ctx, v.ID)
	if err != nil {
		t.Fatalf("Get after second close: %v", err)
	}
	if stored.ClosureKind != version.ClosureKindNormal || stored.Unverified {
		t.Fatalf("normal close projection = %q/%v, want normal/false", stored.ClosureKind, stored.Unverified)
	}
}
