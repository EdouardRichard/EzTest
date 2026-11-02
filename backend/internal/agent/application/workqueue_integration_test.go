//go:build integration

package agentapp_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func newIntegrationIDs(t *testing.T) platformid.Generator {
	t.Helper()
	generator, err := platformid.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func TestWorkQueueConcurrentClaim(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIntegrationIDs(t))
	queue := agentapp.NewWorkQueue(repo, clock.System{}, agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
	ctx := context.Background()

	const total = 50
	for index := 0; index < total; index++ {
		if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
			VersionID: 10,
			Team:      "clarification",
			Type:      "chat.turn",
			DedupeKey: fmt.Sprintf("item:%d", index),
		}); err != nil {
			t.Fatalf("Ensure(%d): %v", index, err)
		}
	}

	const workers = 20
	claimed := make(chan int64, total)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for {
				item, ok, err := queue.Claim(ctx, fmt.Sprintf("worker-%d", worker))
				if err != nil {
					errs <- err
					return
				}
				if !ok {
					return
				}
				claimed <- item.ID
			}
		}(worker)
	}
	wg.Wait()
	close(claimed)
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent claim: %v", err)
	}

	seen := make(map[int64]int, total)
	for id := range claimed {
		seen[id]++
	}
	if len(seen) != total {
		t.Fatalf("claimed %d distinct items, want %d", len(seen), total)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("item %d claimed %d times", id, count)
		}
	}
}

func TestWorkQueueLeaseRecoveryAndStaleWrite(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIntegrationIDs(t))
	queue := agentapp.NewWorkQueue(repo, clock.System{}, 250*time.Millisecond, agentapp.DefaultMaxAttempts)
	ctx := context.Background()

	stored, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10,
		Team:      "clarification",
		Type:      "chat.turn",
		DedupeKey: "lease-recovery",
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}

	first, ok, err := queue.Claim(ctx, "worker-a")
	if err != nil || !ok || first.ID != stored.ID {
		t.Fatalf("first claim = %#v, %v, %v", first, ok, err)
	}
	if first.LeaseEpoch != 1 || first.Attempts != 1 {
		t.Fatalf("first claim state = %#v", first)
	}

	time.Sleep(350 * time.Millisecond)
	recovered, err := queue.RecoverExpired(ctx)
	if err != nil {
		t.Fatalf("RecoverExpired: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}

	second, ok, err := queue.Claim(ctx, "worker-b")
	if err != nil || !ok || second.ID != stored.ID {
		t.Fatalf("second claim = %#v, %v, %v", second, ok, err)
	}
	if second.LeaseEpoch != 2 || second.Attempts != 2 {
		t.Fatalf("second claim state = %#v", second)
	}

	assertAPIError(t, queue.Complete(ctx, first.ID, first.LeaseEpoch), 409, "lease_lost")
	assertAPIError(t, queue.Heartbeat(ctx, first.ID, first.LeaseEpoch, 0), 409, "lease_lost")
	if err := queue.Complete(ctx, second.ID, second.LeaseEpoch); err != nil {
		t.Fatalf("second complete: %v", err)
	}
}

func TestWorkQueueFailReopensWithBackoff(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewWorkItemRepository(pool, newIntegrationIDs(t))
	queue := agentapp.NewWorkQueue(repo, clock.System{}, agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
	ctx := context.Background()

	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10,
		Team:      "clarification",
		Type:      "requirement.curate",
		DedupeKey: "fail-retry",
	}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	claimed, ok, err := queue.Claim(ctx, "worker-a")
	if err != nil || !ok {
		t.Fatalf("claim = %#v, %v, %v", claimed, ok, err)
	}
	if err := queue.MarkRunning(ctx, claimed.ID, claimed.LeaseEpoch); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	if err := queue.Fail(ctx, claimed.ID, claimed.LeaseEpoch, fmt.Errorf("model unavailable"), true); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	stored, err := repo.Get(ctx, claimed.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != agent.WorkItemOpen {
		t.Fatalf("status = %q, want open", stored.Status)
	}
	if stored.LastError == nil || *stored.LastError != "model unavailable" {
		t.Fatalf("last_error = %#v", stored.LastError)
	}
	if !stored.AvailableAt.After(time.Now()) {
		t.Fatalf("available_at = %v, want future backoff", stored.AvailableAt)
	}
	if item, ok, err := queue.Claim(ctx, "worker-b"); err != nil || ok || item.ID != 0 {
		t.Fatalf("claim during backoff = %#v, %v, %v", item, ok, err)
	}

	// A stale owner can never record a failure against the new lease.
	assertAPIError(t, queue.Fail(ctx, claimed.ID, claimed.LeaseEpoch+1, fmt.Errorf("stale"), true), 409, "lease_lost")
}

var _ = platform.AsError
