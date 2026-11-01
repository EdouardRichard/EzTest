//go:build integration

package versionpg_test

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

// versionCloseFixture creates one running version (with its bindings) for the
// close/reopen repository tests.
type versionCloseFixture struct {
	pool            *pgxpool.Pool
	ids             *id.Snowflake
	versionRepo     *versionpg.Repository
	requirementRepo *versionpg.RequirementRepository
	version         version.Version
}

func newVersionCloseFixture(t *testing.T) *versionCloseFixture {
	t.Helper()
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.New(pool, ids)
	ctx := context.Background()
	v := sampleVersion(ids, 42)
	v.Status = version.StatusRunning
	if err := repo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create running version: %v", err)
	}
	return &versionCloseFixture{
		pool: pool, ids: ids, versionRepo: repo,
		requirementRepo: versionpg.NewRequirementRepository(pool), version: v,
	}
}

func (f *versionCloseFixture) readVersion(t *testing.T) (status string, closedAt *time.Time, closeReason string, closeRunID *int64) {
	t.Helper()
	if err := f.pool.QueryRow(context.Background(), `
		SELECT status, closed_at, close_reason, close_run_id FROM version WHERE id = $1
	`, f.version.ID).Scan(&status, &closedAt, &closeReason, &closeRunID); err != nil {
		t.Fatalf("read version: %v", err)
	}
	return status, closedAt, closeReason, closeRunID
}

func (f *versionCloseFixture) insertIntent(t *testing.T, dedupeKey string) int64 {
	t.Helper()
	intentID := f.ids.Next()
	if _, err := f.pool.Exec(context.Background(), `
		INSERT INTO execution_intent (
			id, version_id, case_version_id, environment_fingerprint,
			code_baseline, purpose, status, dedupe_key
		) VALUES ($1, $2, $3, $4, '', 'initial', 'open', $5)
	`, intentID, f.version.ID, f.ids.Next(), strings.Repeat("a", 64), dedupeKey); err != nil {
		t.Fatalf("insert execution intent: %v", err)
	}
	return intentID
}

func (f *versionCloseFixture) countActiveIntents(t *testing.T) int {
	t.Helper()
	var count int
	if err := f.pool.QueryRow(context.Background(), `
		SELECT count(*) FROM execution_intent
		WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
	`, f.version.ID).Scan(&count); err != nil {
		t.Fatalf("count active intents: %v", err)
	}
	return count
}

func TestVersionCloseRepoCloseWritesFields(t *testing.T) {
	fixture := newVersionCloseFixture(t)
	ctx := context.Background()
	closedAt := testTime.Add(time.Hour)
	runID := int64(9001)

	recheckCalls := 0
	lockedStatus := ""
	closed, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
		ClosedAt:    closedAt,
		CloseReason: "all requirements delivered",
		CloseRunID:  &runID,
	}, func(_ context.Context, locked version.Version) error {
		recheckCalls++
		lockedStatus = locked.Status
		if locked.ClosedAt != nil || locked.CloseReason != "" {
			t.Errorf("locked version already carries close fields: %#v", locked)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Close: %v", err)
	}
	if lockedStatus != version.StatusRunning {
		t.Fatalf("recheck locked status = %q", lockedStatus)
	}
	if closed.ID != fixture.version.ID || closed.Status != version.StatusCompleted {
		t.Fatalf("closed version = %#v", closed)
	}
	if closed.ClosedAt == nil || !closed.ClosedAt.Equal(closedAt) ||
		closed.CloseReason != "all requirements delivered" ||
		closed.CloseRunID == nil || *closed.CloseRunID != runID {
		t.Fatalf("closed projection = %#v", closed)
	}

	status, storedClosedAt, storedReason, storedRunID := fixture.readVersion(t)
	if status != string(version.StatusCompleted) || storedClosedAt == nil || !storedClosedAt.Equal(closedAt) {
		t.Fatalf("stored version = %q/%v", status, storedClosedAt)
	}
	if storedReason != "all requirements delivered" || storedRunID == nil || *storedRunID != runID {
		t.Fatalf("stored close fields = %q/%v", storedReason, storedRunID)
	}

	// A second close is a state conflict; the recheck never runs again.
	if _, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
		ClosedAt: closedAt, CloseReason: "again",
	}, func(context.Context, version.Version) error {
		recheckCalls++
		return nil
	}); !errors.Is(err, version.ErrStateConflict) {
		t.Fatalf("second Close error = %v, want ErrStateConflict", err)
	}
	if recheckCalls != 1 {
		t.Fatalf("recheck calls after conflict = %d", recheckCalls)
	}

	// A recheck error rolls the whole transaction back.
	wantErr := errors.New("predicate failed inside the transaction")
	reopenedAt := closedAt.Add(time.Minute)
	if _, changed, err := fixture.versionRepo.ReopenIfCompleted(ctx, fixture.version.ID, reopenedAt); err != nil || !changed {
		t.Fatalf("ReopenIfCompleted = %v/%v", changed, err)
	}
	if _, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
		ClosedAt: reopenedAt.Add(time.Minute), CloseReason: "should roll back",
	}, func(context.Context, version.Version) error {
		return wantErr
	}); !errors.Is(err, wantErr) {
		t.Fatalf("Close recheck error = %v, want %v", err, wantErr)
	}
	status, storedClosedAt, storedReason, storedRunID = fixture.readVersion(t)
	if status != string(version.StatusRunning) || storedClosedAt != nil || storedReason != "" || storedRunID != nil {
		t.Fatalf("failed close left projection = %q/%v/%q/%v", status, storedClosedAt, storedReason, storedRunID)
	}
	if _, err := fixture.versionRepo.Close(ctx, 999999, version.VersionCloseFields{ClosedAt: closedAt}, nil); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing version Close error = %v, want ErrNotFound", err)
	}
}

func TestVersionCloseRepoReopenIfCompleted(t *testing.T) {
	fixture := newVersionCloseFixture(t)
	ctx := context.Background()
	closedAt := testTime.Add(time.Hour)
	runID := int64(9001)
	if _, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
		ClosedAt: closedAt, CloseReason: "done", CloseRunID: &runID,
	}, nil); err != nil {
		t.Fatalf("Close: %v", err)
	}

	reopenedAt := closedAt.Add(time.Minute)
	reopened, changed, err := fixture.versionRepo.ReopenIfCompleted(ctx, fixture.version.ID, reopenedAt)
	if err != nil {
		t.Fatalf("ReopenIfCompleted: %v", err)
	}
	if !changed || reopened.Status != version.StatusRunning ||
		reopened.ClosedAt != nil || reopened.CloseReason != "" || reopened.CloseRunID != nil {
		t.Fatalf("reopened = %v/%#v", changed, reopened)
	}
	if !reopened.UpdatedAt.Equal(reopenedAt) {
		t.Fatalf("reopened updated_at = %v", reopened.UpdatedAt)
	}
	status, closedAtValue, reason, runIDValue := fixture.readVersion(t)
	if status != string(version.StatusRunning) || closedAtValue != nil || reason != "" || runIDValue != nil {
		t.Fatalf("stored reopen projection = %q/%v/%q/%v", status, closedAtValue, reason, runIDValue)
	}

	// A version that is not completed is a no-op, not an error.
	again, changed, err := fixture.versionRepo.ReopenIfCompleted(ctx, fixture.version.ID, reopenedAt.Add(time.Minute))
	if err != nil || changed || again.Status != version.StatusRunning {
		t.Fatalf("second ReopenIfCompleted = %#v/%v/%v", again, changed, err)
	}
	if _, _, err := fixture.versionRepo.ReopenIfCompleted(ctx, 999999, reopenedAt); !errors.Is(err, version.ErrNotFound) {
		t.Fatalf("missing version reopen error = %v, want ErrNotFound", err)
	}
}

func TestVersionCloseRepoConcurrentNewIntent(t *testing.T) {
	t.Run("intent committed while close holds the lock aborts the close", func(t *testing.T) {
		fixture := newVersionCloseFixture(t)
		ctx := context.Background()

		locked := make(chan struct{})
		intentCommitted := make(chan struct{})
		closeErr := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
				ClosedAt: testTime.Add(time.Hour), CloseReason: "racing close",
			}, func(ctx context.Context, _ version.Version) error {
				close(locked)
				<-intentCommitted
				// Re-evaluate the execution_intent condition through a second
				// connection while the version row lock is still held. This
				// callback runs on the test goroutine, so it must not call
				// t.Fatalf.
				var active int
				if err := fixture.pool.QueryRow(ctx, `
					SELECT count(*) FROM execution_intent
					WHERE version_id = $1 AND status IN ('open', 'claimed', 'running')
				`, fixture.version.ID).Scan(&active); err != nil {
					return err
				}
				if active > 0 {
					return version.ErrStateConflict
				}
				return nil
			})
			closeErr <- err
		}()

		<-locked
		fixture.insertIntent(t, "race-intent-1")
		close(intentCommitted)
		wg.Wait()
		err := <-closeErr
		if !errors.Is(err, version.ErrStateConflict) {
			t.Fatalf("concurrent Close error = %v, want state conflict", err)
		}

		status, closedAt, reason, runID := fixture.readVersion(t)
		if status != string(version.StatusRunning) || closedAt != nil || reason != "" || runID != nil {
			t.Fatalf("losing close changed the version: %q/%v/%q/%v", status, closedAt, reason, runID)
		}
		if active := fixture.countActiveIntents(t); active != 1 {
			t.Fatalf("active intents = %d, want 1", active)
		}
	})

	t.Run("close commits first and the intent path reopens", func(t *testing.T) {
		fixture := newVersionCloseFixture(t)
		ctx := context.Background()
		closedAt := testTime.Add(time.Hour)
		if _, err := fixture.versionRepo.Close(ctx, fixture.version.ID, version.VersionCloseFields{
			ClosedAt: closedAt, CloseReason: "close won",
		}, func(context.Context, version.Version) error {
			if fixture.countActiveIntents(t) != 0 {
				return version.ErrStateConflict
			}
			return nil
		}); err != nil {
			t.Fatalf("Close: %v", err)
		}
		if status, _, _, _ := fixture.readVersion(t); status != string(version.StatusCompleted) {
			t.Fatalf("status after close = %q", status)
		}

		fixture.insertIntent(t, "race-intent-2")
		reopenedAt := closedAt.Add(time.Minute)
		reopened, changed, err := fixture.versionRepo.ReopenIfCompleted(ctx, fixture.version.ID, reopenedAt)
		if err != nil || !changed || reopened.Status != version.StatusRunning {
			t.Fatalf("intent reopen = %#v/%v/%v", reopened, changed, err)
		}
		status, closedAtValue, reason, runID := fixture.readVersion(t)
		if status != string(version.StatusRunning) || closedAtValue != nil || reason != "" || runID != nil {
			t.Fatalf("final state = %q/%v/%q/%v, want running with the intent", status, closedAtValue, reason, runID)
		}
		if active := fixture.countActiveIntents(t); active != 1 {
			t.Fatalf("active intents = %d, want 1", active)
		}
	})
}

func TestVersionCloseRepoRequirementReader(t *testing.T) {
	fixture := newVersionCloseFixture(t)
	ctx := context.Background()

	closedRequirement, closedRevision := sampleRequirement(fixture.ids, fixture.version.ID)
	closedRequirement.Status = version.RequirementClosed
	closedClosedAt := testTime
	closedRequirement.ClosedAt = &closedClosedAt
	if err := fixture.requirementRepo.Create(ctx, closedRequirement, closedRevision); err != nil {
		t.Fatalf("create closed requirement: %v", err)
	}
	obsoleteRequirement, obsoleteRevision := sampleRequirement(fixture.ids, fixture.version.ID)
	obsoleteRequirement.Status = version.RequirementObsolete
	if err := fixture.requirementRepo.Create(ctx, obsoleteRequirement, obsoleteRevision); err != nil {
		t.Fatalf("create obsolete requirement: %v", err)
	}

	requirements, err := fixture.requirementRepo.ListByVersion(ctx, fixture.version.ID)
	if err != nil {
		t.Fatalf("ListByVersion: %v", err)
	}
	if len(requirements) != 2 {
		t.Fatalf("requirements = %#v", requirements)
	}
	statuses := map[int64]version.RequirementStatus{}
	for _, requirement := range requirements {
		statuses[requirement.ID] = requirement.Status
	}
	if statuses[closedRequirement.ID] != version.RequirementClosed ||
		statuses[obsoleteRequirement.ID] != version.RequirementObsolete {
		t.Fatalf("statuses = %#v", statuses)
	}
	if requirements[0].ID > requirements[1].ID {
		t.Fatalf("ListByVersion is not ID ascending: %#v", requirements)
	}
	if empty, err := fixture.requirementRepo.ListByVersion(ctx, 999999); err != nil || len(empty) != 0 {
		t.Fatalf("missing version ListByVersion = %#v/%v", empty, err)
	}
}
