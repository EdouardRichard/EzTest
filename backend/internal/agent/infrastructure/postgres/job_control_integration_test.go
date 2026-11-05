//go:build integration

package agentpg_test

// Task 46 fix round 1: integration coverage of the job cancel/retry control
// SQL. The application tests use an in-memory repository; these tests execute
// the real statements against an isolated PostgreSQL schema
// (testutil.NewDB), so the lease/attempt/epoch transitions and the
// no-migration cancel marker are pinned where they are written.
//
// Covered:
//   - cancel of open/claimed/running: lease cleared, epoch bumped (stale owner
//     fenced), attempts exhausted, active attempt closed, row unclaimable;
//   - cancel/retry cross-version and missing rows: agent.ErrNotFound;
//   - retry guard: failed below the cap only, missing/foreign/capped → errors;
//   - a user-initiated retry resets attempts to 0 (fresh budget) while keeping
//     max_attempts and the contract payloads (no automatic model switch);
//   - the Task 46 cancel marker is excluded from the P6 retry rate and the
//     dead-work-item todo feed.

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// jobControlSeed is one work_item fixture row. The zero MaxAttempts defaults to
// 3 like the 0.2.1 contract default. Legacy seeds omit the 0.2.1 kind so the
// row looks like a pre-0.2.1 work item (empty kind).
type jobControlSeed struct {
	ID          int64
	VersionID   int64
	Status      string
	Attempts    int
	MaxAttempts int
	LeaseEpoch  int64
	Priority    int
	LastError   string
	Payload     string
	Claimed     bool // claimed_by/lease_until are set (claimed/running rows)
	Legacy      bool // kind stays '' (pre-0.2.1 row, legacy claim path)
	AvailableAt time.Time
}

func seedJobControlItem(t *testing.T, pool *pgxpool.Pool, seed jobControlSeed) {
	t.Helper()
	if seed.MaxAttempts == 0 {
		seed.MaxAttempts = 3
	}
	payload := seed.Payload
	if payload == "" {
		payload = `{"model":"deepseek-chat"}`
	}
	availableAt := seed.AvailableAt
	if availableAt.IsZero() {
		availableAt = time.Date(2026, 11, 3, 8, 0, 0, 0, time.UTC)
	}
	kind := "execute.batch"
	if seed.Legacy {
		kind = ""
	}
	var lastError any
	if seed.LastError != "" {
		lastError = seed.LastError
	}
	var claimedBy, leaseUntil any
	if seed.Claimed {
		claimedBy = "worker-7"
		leaseUntil = availableAt.Add(2 * time.Hour)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item (id, version_id, team, type, kind, goal, status, priority,
			dedupe_key, payload, created_by, attempts, max_attempts, lease_epoch,
			claimed_by, lease_until, last_error, available_at, created_at, updated_at)
		VALUES ($1, $2, 'execution', 'job.control.test', $14,
		        'job control integration test', $3, $4, $5, $6::jsonb, 'state_machine',
		        $7, $8, $9, $10, $11, $12, $13, $13, $13)
	`, seed.ID, seed.VersionID, seed.Status, seed.Priority,
		fmt.Sprintf("job-control-%d", seed.ID), payload, seed.Attempts, seed.MaxAttempts,
		seed.LeaseEpoch, claimedBy, leaseUntil, lastError, availableAt, kind); err != nil {
		t.Fatalf("seed work item %d: %v", seed.ID, err)
	}
}

func seedJobControlAttempt(t *testing.T, pool *pgxpool.Pool, itemID int64, attempt int, status string) {
	t.Helper()
	// Failed/done/dead/recovered attempts are historical: they are already
	// closed. claimed/running are the active attempt of a leased row.
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO work_item_attempt (id, work_item_id, attempt, status, started_at, finished_at)
		VALUES ($1, $2, $3, $4, now(),
		        CASE WHEN $4 IN ('done', 'failed', 'dead', 'recovered') THEN now() ELSE NULL END)
	`, itemID*100+int64(attempt), itemID, attempt, status); err != nil {
		t.Fatalf("seed work item attempt %d/%d: %v", itemID, attempt, err)
	}
}

// readJobControlAttempt returns the status/error of one attempt log row and
// whether it is closed (finished_at set).
func readJobControlAttempt(t *testing.T, pool *pgxpool.Pool, itemID int64, attempt int) (status, errorText string, finished bool) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT status, COALESCE(error, ''), finished_at IS NOT NULL
		FROM work_item_attempt
		WHERE work_item_id = $1 AND attempt = $2
	`, itemID, attempt).Scan(&status, &errorText, &finished); err != nil {
		t.Fatalf("read attempt %d of work item %d: %v", attempt, itemID, err)
	}
	return status, errorText, finished
}

func TestJobCancelControlSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	const versionID = int64(7101)

	for index, status := range []string{"open", "claimed", "running"} {
		index, status := index, status
		t.Run("cancels "+status, func(t *testing.T) {
			id := int64(9101 + index)
			seed := jobControlSeed{
				ID:          id,
				VersionID:   versionID,
				Status:      status,
				Attempts:    1,
				MaxAttempts: 3,
				AvailableAt: now.Add(-time.Hour),
			}
			if status == "open" {
				seed.LeaseEpoch = 0
			} else {
				seed.LeaseEpoch = 2
				seed.Claimed = true
			}
			seedJobControlItem(t, pool, seed)
			if status != "open" {
				seedJobControlAttempt(t, pool, id, 1, status)
			}

			before, err := repo.GetJob(ctx, versionID, id)
			if err != nil {
				t.Fatalf("GetJob: %v", err)
			}
			if before.Status != agent.WorkItemStatus(status) || before.Attempts != 1 {
				t.Fatalf("seeded row = %#v", before)
			}

			updated, err := repo.CancelJob(ctx, versionID, id, before.LeaseEpoch, agentapp.JobCanceledReason, now)
			if err != nil {
				t.Fatalf("CancelJob(%s): %v", status, err)
			}

			// The frozen schema has no cancelled status: a canceled row is
			// failed at the attempt cap with the marker in last_error.
			if updated.Status != agent.WorkItemFailed {
				t.Fatalf("status = %q, want failed", updated.Status)
			}
			if updated.Attempts != seed.MaxAttempts || updated.MaxAttempts != seed.MaxAttempts {
				t.Fatalf("attempts = %d/%d, want %d/%d", updated.Attempts, updated.MaxAttempts, seed.MaxAttempts, seed.MaxAttempts)
			}
			if updated.LastError == nil || *updated.LastError != agentapp.JobCanceledReason {
				t.Fatalf("last_error = %v, want %q", updated.LastError, agentapp.JobCanceledReason)
			}
			if updated.ClaimedBy != nil || updated.LeaseUntil != nil {
				t.Fatalf("lease was not cleared: claimed_by=%v lease_until=%v", updated.ClaimedBy, updated.LeaseUntil)
			}
			if updated.LeaseEpoch != before.LeaseEpoch+1 {
				t.Fatalf("lease_epoch = %d, want %d (stale owner fenced)", updated.LeaseEpoch, before.LeaseEpoch+1)
			}
			if !updated.AvailableAt.Equal(now) || !updated.UpdatedAt.Equal(now) {
				t.Fatalf("timestamps = available %s updated %s, want %s",
					updated.AvailableAt, updated.UpdatedAt, now)
			}

			if status != "open" {
				var attemptStatus string
				var attemptError *string
				var finishedAt *time.Time
				if err := pool.QueryRow(ctx, `
					SELECT status, error, finished_at
					FROM work_item_attempt
					WHERE work_item_id = $1 AND attempt = 1
				`, id).Scan(&attemptStatus, &attemptError, &finishedAt); err != nil {
					t.Fatalf("read attempt: %v", err)
				}
				if attemptStatus != string(agent.AttemptFailed) {
					t.Fatalf("attempt status = %q, want failed", attemptStatus)
				}
				if attemptError == nil || *attemptError != agentapp.JobCanceledReason {
					t.Fatalf("attempt error = %v, want %q", attemptError, agentapp.JobCanceledReason)
				}
				if finishedAt == nil {
					t.Fatal("attempt was not closed (finished_at is NULL)")
				}
			}

			// The canceled row is terminal: the dispatcher can never claim it,
			// even though the attempt budget is the only fencing mechanism.
			claimed, err := repo.ClaimWorkItem(ctx, "worker-cancel-test", now)
			if err != nil {
				t.Fatalf("ClaimWorkItem: %v", err)
			}
			if claimed != nil {
				t.Fatalf("canceled job was claimed: %#v", claimed)
			}

			// A stale epoch is a conflict and changes nothing.
			if _, err := repo.CancelJob(ctx, versionID, id, before.LeaseEpoch, agentapp.JobCanceledReason, now); !errors.Is(err, agent.ErrConflict) {
				t.Fatalf("stale-epoch cancel error = %v, want agent.ErrConflict", err)
			}

			// Cross-version addressing is not found, for reads and writes.
			if _, err := repo.GetJob(ctx, versionID+1, id); !errors.Is(err, agent.ErrNotFound) {
				t.Fatalf("foreign GetJob error = %v, want agent.ErrNotFound", err)
			}
			if _, err := repo.CancelJob(ctx, versionID+1, id, before.LeaseEpoch, agentapp.JobCanceledReason, now); !errors.Is(err, agent.ErrNotFound) {
				t.Fatalf("foreign CancelJob error = %v, want agent.ErrNotFound", err)
			}
		})
	}

	if _, err := repo.CancelJob(ctx, versionID, 999999, 0, agentapp.JobCanceledReason, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing CancelJob error = %v, want agent.ErrNotFound", err)
	}
}

func TestJobRetryControlSQL(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	const versionID = int64(7102)
	const foreignVersionID = int64(7103)

	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9201, VersionID: versionID, Status: "failed", Attempts: 2, MaxAttempts: 3,
		LastError: "assertion failed", Payload: `{"model":"deepseek-chat"}`,
		AvailableAt: now.Add(-time.Hour),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9202, VersionID: versionID, Status: "failed", Attempts: 3, MaxAttempts: 3,
		LastError: "assertion failed", AvailableAt: now.Add(-time.Hour),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9203, VersionID: versionID, Status: "open", Attempts: 0, MaxAttempts: 3,
		AvailableAt: now.Add(-time.Minute),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9204, VersionID: foreignVersionID, Status: "failed", Attempts: 1, MaxAttempts: 3,
		LastError: "assertion failed", AvailableAt: now.Add(-time.Hour),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9205, VersionID: versionID, Status: "failed", Attempts: 1, MaxAttempts: 3,
		LastError: "assertion failed", AvailableAt: now.Add(-time.Hour),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9206, VersionID: versionID, Status: "failed", Attempts: 3, MaxAttempts: 3,
		LastError: agentapp.JobCanceledReason, AvailableAt: now.Add(-time.Hour),
	})

	before, err := repo.GetJob(ctx, versionID, 9201)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}

	updated, err := repo.RetryJob(ctx, versionID, 9201, now)
	if err != nil {
		t.Fatalf("RetryJob: %v", err)
	}
	// Task 46 fix round 1: a user-initiated retry grants a fresh budget —
	// attempts goes back to 0 while max_attempts is preserved.
	if updated.Status != agent.WorkItemOpen {
		t.Fatalf("status = %q, want open", updated.Status)
	}
	if updated.Attempts != 0 {
		t.Fatalf("attempts = %d, want 0 (user retry resets to a fresh budget)", updated.Attempts)
	}
	if updated.MaxAttempts != before.MaxAttempts {
		t.Fatalf("max_attempts = %d, want %d preserved", updated.MaxAttempts, before.MaxAttempts)
	}
	if updated.LastError != nil {
		t.Fatalf("last_error = %v, want nil", updated.LastError)
	}
	if updated.ClaimedBy != nil || updated.LeaseUntil != nil {
		t.Fatalf("lease not cleared: claimed_by=%v lease_until=%v", updated.ClaimedBy, updated.LeaseUntil)
	}
	if !updated.AvailableAt.Equal(now) || !updated.UpdatedAt.Equal(now) {
		t.Fatalf("timestamps = available %s updated %s, want %s",
			updated.AvailableAt, updated.UpdatedAt, now)
	}
	if string(updated.Payload) != string(before.Payload) {
		t.Fatalf("payload = %s, want unchanged %s (no automatic model switch)", updated.Payload, before.Payload)
	}

	// The fresh budget is real: the dispatcher claims the item as attempt 1.
	claimed, err := repo.ClaimWorkItem(ctx, "worker-retry-test", now)
	if err != nil {
		t.Fatalf("ClaimWorkItem: %v", err)
	}
	if claimed == nil || claimed.ID != 9201 {
		t.Fatalf("claimed = %#v, want item 9201", claimed)
	}
	if claimed.Attempts != 1 {
		t.Fatalf("claimed attempts = %d, want 1 (fresh budget restarted at 0)", claimed.Attempts)
	}

	// Wrong state, attempt cap and a canceled marker are conflicts.
	for _, id := range []int64{9202, 9203, 9206} {
		id := id
		t.Run(fmt.Sprintf("rejects item %d", id), func(t *testing.T) {
			if _, err := repo.RetryJob(ctx, versionID, id, now); !errors.Is(err, agent.ErrConflict) {
				t.Fatalf("RetryJob(%d) error = %v, want agent.ErrConflict", id, err)
			}
		})
	}

	// Cross-version retry is not found; the owning version can retry it.
	if _, err := repo.RetryJob(ctx, versionID, 9204, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("foreign RetryJob error = %v, want agent.ErrNotFound", err)
	}
	foreign, err := repo.RetryJob(ctx, foreignVersionID, 9204, now)
	if err != nil {
		t.Fatalf("RetryJob other version: %v", err)
	}
	if foreign.Attempts != 0 || foreign.Status != agent.WorkItemOpen {
		t.Fatalf("other-version retry = %#v", foreign)
	}

	// The direct route (versionID 0) is owned by the job's own version.
	direct, err := repo.RetryJob(ctx, 0, 9205, now)
	if err != nil {
		t.Fatalf("direct RetryJob: %v", err)
	}
	if direct.VersionID != versionID || direct.Attempts != 0 {
		t.Fatalf("direct retry = %#v", direct)
	}

	if _, err := repo.RetryJob(ctx, versionID, 999999, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("missing RetryJob error = %v, want agent.ErrNotFound", err)
	}
}

// TestJobRetryRejectsLegacyKind pins the Task 60 legacy fence at the SQL layer:
// a pre-0.2.1 work_item row (empty kind) is dispatched by the legacy Claim* path,
// whose work_item_attempt number is the attempts budget counter. The 0.2.1 job
// control adapter must not read, cancel or retry such a row by raw ID — both
// scoped reads and both writes report agent.ErrNotFound and change nothing.
func TestJobRetryRejectsLegacyKind(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	const versionID = int64(7108)

	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9501, VersionID: versionID, Status: "failed", Attempts: 1, MaxAttempts: 3,
		LastError: "legacy boom", Legacy: true, AvailableAt: now.Add(-time.Hour),
	})
	seedJobControlItem(t, pool, jobControlSeed{
		ID: 9502, VersionID: versionID, Status: "open", Attempts: 0, MaxAttempts: 3,
		Legacy: true, AvailableAt: now.Add(-time.Minute),
	})

	for _, id := range []int64{9501, 9502} {
		if _, err := repo.GetJob(ctx, versionID, id); !errors.Is(err, agent.ErrNotFound) {
			t.Fatalf("GetJob(%d) error = %v, want agent.ErrNotFound (legacy kind fence)", id, err)
		}
		if _, err := repo.GetJob(ctx, 0, id); !errors.Is(err, agent.ErrNotFound) {
			t.Fatalf("direct GetJob(%d) error = %v, want agent.ErrNotFound", id, err)
		}
	}

	if _, err := repo.RetryJob(ctx, versionID, 9501, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("RetryJob(legacy failed) error = %v, want agent.ErrNotFound", err)
	}
	if _, err := repo.RetryJob(ctx, 0, 9501, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("direct RetryJob(legacy failed) error = %v, want agent.ErrNotFound", err)
	}
	if _, err := repo.CancelJob(ctx, versionID, 9502, 0, agentapp.JobCanceledReason, now); !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("CancelJob(legacy open) error = %v, want agent.ErrNotFound", err)
	}

	// The fence must not mutate the legacy row: no status/attempt/last_error
	// change, so the legacy claim path keeps its own budget-coupled numbering.
	var status string
	var attempts int
	var lastError *string
	if err := pool.QueryRow(ctx, `
		SELECT status, attempts, last_error FROM work_item WHERE id = 9501
	`).Scan(&status, &attempts, &lastError); err != nil {
		t.Fatalf("read legacy row: %v", err)
	}
	if status != "failed" || attempts != 1 || lastError == nil || *lastError != "legacy boom" {
		t.Fatalf("legacy row = %q/%d/%v, want untouched failed/1/legacy boom", status, attempts, lastError)
	}
}

// TestJobRetryClaimAfterHistoricalAttempts pins the Task 46 fix round 2
// regression: RetryJob resets work_item.attempts to 0 (a fresh budget), while
// the attempt log is append-only. When ClaimWorkItem still derived the log
// number from attempts it re-used attempt 1 for an item that already had
// attempts 1..2, so UNIQUE (work_item_id, attempt) rejected the INSERT (23505),
// the claim rolled back and the still-open item blocked the dispatcher loop.
// The log number is now MAX(attempt)+1 inside the claim transaction; attempts
// stays the budget counter and the write-backs close the current open row.
func TestJobRetryClaimAfterHistoricalAttempts(t *testing.T) {
	t.Run("historical attempts survive a manual retry", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		const versionID = int64(7106)

		seedJobControlItem(t, pool, jobControlSeed{
			ID: 9401, VersionID: versionID, Status: "failed", Attempts: 2, MaxAttempts: 3,
			LastError: "assertion failed", AvailableAt: now.Add(-time.Hour),
		})
		seedJobControlAttempt(t, pool, 9401, 1, "failed")
		seedJobControlAttempt(t, pool, 9401, 2, "failed")

		retried, err := repo.RetryJob(ctx, versionID, 9401, now)
		if err != nil {
			t.Fatalf("RetryJob: %v", err)
		}
		if retried.Status != agent.WorkItemOpen || retried.Attempts != 0 {
			t.Fatalf("retried = %#v, want open with a fresh budget", retried)
		}

		// RED before fix round 2: this claim reused attempt 1 and failed with a
		// duplicate-key error (SQLSTATE 23505).
		claimed, err := repo.ClaimWorkItem(ctx, "worker-history", now)
		if err != nil {
			t.Fatalf("ClaimWorkItem after a manual retry: %v", err)
		}
		if claimed == nil || claimed.ID != 9401 {
			t.Fatalf("claimed = %#v, want work item 9401", claimed)
		}
		if claimed.Attempts != 1 {
			t.Fatalf("claimed attempts = %d, want the fresh-budget 1", claimed.Attempts)
		}
		if status, errorText, finished := readJobControlAttempt(t, pool, 9401, 3); status != "claimed" || errorText != "" || finished {
			t.Fatalf("attempt 3 = %q/%q/finished=%v, want a new open claimed row", status, errorText, finished)
		}
		for _, attempt := range []int{1, 2} {
			if status, _, finished := readJobControlAttempt(t, pool, 9401, attempt); status != "failed" || !finished {
				t.Fatalf("historical attempt %d = %q/finished=%v, want failed", attempt, status, finished)
			}
		}

		// The write-backs must close the current log row, not the row numbered
		// like the budget counter (attempt 1 already failed).
		if err := repo.MarkRunning(ctx, 9401, claimed.LeaseEpoch); err != nil {
			t.Fatalf("MarkRunning: %v", err)
		}
		if status, _, _ := readJobControlAttempt(t, pool, 9401, 3); status != "running" {
			t.Fatalf("attempt 3 status after MarkRunning = %q, want running", status)
		}
		if err := repo.FailWorkItem(ctx, 9401, claimed.LeaseEpoch, "assertion failed again"); err != nil {
			t.Fatalf("FailWorkItem: %v", err)
		}
		if status, errorText, finished := readJobControlAttempt(t, pool, 9401, 3); status != "failed" || errorText != "assertion failed again" || !finished {
			t.Fatalf("attempt 3 after FailWorkItem = %q/%q/finished=%v", status, errorText, finished)
		}

		// The next automatic retry keeps the log monotonic (4) while the budget
		// moves 1 -> 2.
		// FailWorkItem stamps available_at with the database clock, so the
		// next claim must use a clock at least as new as that write.
		now = time.Now().UTC().Add(2 * time.Second)
		reclaimed, err := repo.ClaimWorkItem(ctx, "worker-history-2", now)
		if err != nil {
			t.Fatalf("ClaimWorkItem again: %v", err)
		}
		if reclaimed == nil || reclaimed.ID != 9401 || reclaimed.Attempts != 2 {
			t.Fatalf("reclaimed = %#v, want work item 9401 with attempts 2", reclaimed)
		}
		if status, _, finished := readJobControlAttempt(t, pool, 9401, 4); status != "claimed" || finished {
			t.Fatalf("attempt 4 = %q/finished=%v, want a new open claimed row", status, finished)
		}

		// Cancel closes the current log row too (attempt 4, not attempt 2).
		beforeCancel, err := repo.GetJob(ctx, versionID, 9401)
		if err != nil {
			t.Fatalf("GetJob: %v", err)
		}
		if _, err := repo.CancelJob(ctx, versionID, 9401, beforeCancel.LeaseEpoch, agentapp.JobCanceledReason, now); err != nil {
			t.Fatalf("CancelJob: %v", err)
		}
		if status, errorText, finished := readJobControlAttempt(t, pool, 9401, 4); status != "failed" || errorText != agentapp.JobCanceledReason || !finished {
			t.Fatalf("attempt 4 after CancelJob = %q/%q/finished=%v", status, errorText, finished)
		}
	})

	t.Run("no history starts at the first attempt", func(t *testing.T) {
		pool := testutil.NewDB(t)
		repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
		ctx := context.Background()
		now := time.Now().UTC().Truncate(time.Millisecond)
		const versionID = int64(7107)

		seedJobControlItem(t, pool, jobControlSeed{
			ID: 9402, VersionID: versionID, Status: "failed", Attempts: 1, MaxAttempts: 3,
			LastError: "assertion failed", AvailableAt: now.Add(-time.Hour),
		})

		if _, err := repo.RetryJob(ctx, versionID, 9402, now); err != nil {
			t.Fatalf("RetryJob: %v", err)
		}
		claimed, err := repo.ClaimWorkItem(ctx, "worker-no-history", now)
		if err != nil {
			t.Fatalf("ClaimWorkItem after a manual retry: %v", err)
		}
		if claimed == nil || claimed.ID != 9402 || claimed.Attempts != 1 {
			t.Fatalf("claimed = %#v, want work item 9402 with attempts 1", claimed)
		}
		if status, _, finished := readJobControlAttempt(t, pool, 9402, 1); status != "claimed" || finished {
			t.Fatalf("attempt 1 = %q/finished=%v, want a new open claimed row", status, finished)
		}
	})
}

func TestJobCancelMarkerExcludedFromRetryMetric(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIDs(t))
	reader, err := agentpg.NewQueryReader(pool)
	if err != nil {
		t.Fatalf("NewQueryReader: %v", err)
	}
	ctx := context.Background()
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	const versionID = int64(7104)
	const otherVersionID = int64(7105)
	versionPtr := versionID

	seedJobControlItem(t, pool, jobControlSeed{ID: 9301, VersionID: versionID, Status: "done", Attempts: 1, MaxAttempts: 3, AvailableAt: now.Add(-time.Hour)})
	seedJobControlItem(t, pool, jobControlSeed{ID: 9302, VersionID: versionID, Status: "failed", Attempts: 2, MaxAttempts: 3, LastError: "assertion failed", AvailableAt: now.Add(-time.Hour)})
	seedJobControlItem(t, pool, jobControlSeed{ID: 9303, VersionID: versionID, Status: "running", Attempts: 1, MaxAttempts: 3, LeaseEpoch: 1, Claimed: true, AvailableAt: now.Add(-time.Hour)})
	seedJobControlItem(t, pool, jobControlSeed{ID: 9304, VersionID: versionID, Status: "open", Attempts: 0, MaxAttempts: 3, AvailableAt: now.Add(-time.Minute)})
	seedJobControlItem(t, pool, jobControlSeed{ID: 9305, VersionID: versionID, Status: "dead", Attempts: 3, MaxAttempts: 3, LastError: "lease expired", AvailableAt: now.Add(-2 * time.Hour)})
	// A canceled row that a future reconcile moved to dead: the marker, not the
	// status, is what keeps it out of the metric and the todo feed.
	seedJobControlItem(t, pool, jobControlSeed{ID: 9306, VersionID: versionID, Status: "dead", Attempts: 3, MaxAttempts: 3, LastError: agentapp.JobCanceledReason, AvailableAt: now.Add(-2 * time.Hour)})
	seedJobControlItem(t, pool, jobControlSeed{ID: 9307, VersionID: otherVersionID, Status: "done", Attempts: 2, MaxAttempts: 3, AvailableAt: now.Add(-time.Hour)})
	seedJobControlAttempt(t, pool, 9303, 1, "running")

	// Before the cancel the running row only contributes to the denominator
	// (and the already-marked dead row 9306 is excluded).
	num, denom, err := reader.CountTaskRetries(ctx, nil)
	if err != nil {
		t.Fatalf("CountTaskRetries before cancel: %v", err)
	}
	if num != 3 || denom != 5 {
		t.Fatalf("before cancel counts = %d/%d, want 3/5", num, denom)
	}

	before, err := repo.GetJob(ctx, versionID, 9303)
	if err != nil {
		t.Fatalf("GetJob: %v", err)
	}
	if _, err := repo.CancelJob(ctx, versionID, 9303, before.LeaseEpoch, agentapp.JobCanceledReason, now); err != nil {
		t.Fatalf("CancelJob: %v", err)
	}

	// Cancel stores attempts=max_attempts (the no-migration compromise). The
	// marker must keep that bookkeeping out of the P6 retry rate: otherwise
	// this cancel would turn 3/4 into 4/5.
	num, denom, err = reader.CountTaskRetries(ctx, nil)
	if err != nil {
		t.Fatalf("CountTaskRetries after cancel: %v", err)
	}
	if num != 3 || denom != 4 {
		t.Fatalf("after cancel counts = %d/%d, want 3/4 (canceled marker excluded)", num, denom)
	}

	num, denom, err = reader.CountTaskRetries(ctx, &versionPtr)
	if err != nil {
		t.Fatalf("CountTaskRetries version: %v", err)
	}
	if num != 2 || denom != 3 {
		t.Fatalf("version counts = %d/%d, want 2/3", num, denom)
	}

	// The todo feed likewise never surfaces a canceled row as retry-exhausted
	// work: only the genuine dead item 9305 remains.
	todos, err := reader.ListDeadWorkItems(ctx, 10)
	if err != nil {
		t.Fatalf("ListDeadWorkItems: %v", err)
	}
	if len(todos) != 1 || todos[0].ID != 9305 {
		t.Fatalf("todos = %#v, want only work item 9305", todos)
	}
}
