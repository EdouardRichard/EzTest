package main

// v0.2.4-H T10 (H-A16, defect 012 third site): the codeintel work-queue adapter
// used to DISCARD the lease its caller passed to Renew and always renew with the
// queue default. The discard was silent (a contract parameter with no effect);
// the render worker's equivalent site was fixed by honoring the claimed lease
// (orchestration_wiring.go renderQueueAdapter.Heartbeat).
//
// The adapter now honors the requested lease with a FLOOR at the lease the queue
// actually grants (WorkQueue.Lease, the same value ClaimForTeams claims with): a
// caller may extend the lease but can never shrink it below the claim. The
// codeintel worker passes its heartbeat INTERVAL, which is shorter than the
// claim — flooring keeps the renewal correct instead of silently shortening the
// lease under a running job.

import (
	"context"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestCodeIntelQueueRenewHonorsLeaseWithClaimFloor(t *testing.T) {
	const claimed = 30 * time.Second
	tests := []struct {
		name      string
		requested time.Duration
		want      time.Duration
	}{
		{"heartbeat interval never shrinks the claimed lease", 10 * time.Second, claimed},
		{"zero keeps the claimed lease", 0, claimed},
		{"a longer lease is honored, not discarded", 5 * time.Minute, 5 * time.Minute},
		{"the exact claimed lease is honored", claimed, claimed},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &fakeCodeIntelQueue{lease: claimed}
			queue := newCodeIntelQueue(fake)
			if err := queue.Renew(context.Background(), 44, 5, test.requested); err != nil {
				t.Fatalf("Renew: %v", err)
			}
			if !fake.heartbeat {
				t.Fatal("Renew did not reach the queue heartbeat")
			}
			if fake.heartbeatLease != test.want {
				t.Fatalf("renewed lease = %v, want %v (requested %v, claimed %v)",
					fake.heartbeatLease, test.want, test.requested, claimed)
			}
		})
	}
}

// TestCodeIntelQueueRenewFloorsAtTheQueueDefault pins the boundary where the
// queue reports no lease of its own: the floor is the queue's default authority,
// never "no floor" (which would let a short heartbeat interval shrink the lease).
func TestCodeIntelQueueRenewFloorsAtTheQueueDefault(t *testing.T) {
	fake := &fakeCodeIntelQueue{}
	queue := newCodeIntelQueue(fake)
	if err := queue.Renew(context.Background(), 44, 5, time.Second); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if fake.heartbeatLease != agentapp.DefaultLease {
		t.Fatalf("renewed lease = %v, want the queue default %v", fake.heartbeatLease, agentapp.DefaultLease)
	}
}

// TestCodeIntelQueueRenewUnwiredFailsClosed pins review M4: the nil guard used to
// cover only the Lease() read, so an unwired queue still panicked on Heartbeat.
// Renew now fails closed like Ensure/ClaimForTeams instead of half-guarding.
func TestCodeIntelQueueRenewUnwiredFailsClosed(t *testing.T) {
	queue := newCodeIntelQueue(nil)
	err := queue.Renew(context.Background(), 44, 5, time.Minute)
	if err == nil {
		t.Fatal("Renew on an unwired queue must return an error, not panic")
	}
	apiErr := platform.AsError(err)
	if apiErr == nil || apiErr.Status != 503 {
		t.Fatalf("unwired Renew error = %v, want a 503 unavailable", err)
	}
}

// TestCodeIntelQueueRenewFloorFollowsTheClaimedLease is the discriminating leg:
// the floor is READ from the queue, not a second hardcoded copy of the default.
// A queue that claims with a longer lease must renew with that longer lease even
// when the caller asks for the platform default.
func TestCodeIntelQueueRenewFloorFollowsTheClaimedLease(t *testing.T) {
	fake := &fakeCodeIntelQueue{lease: 5 * time.Minute}
	queue := newCodeIntelQueue(fake)
	if err := queue.Renew(context.Background(), 44, 5, agentapp.DefaultLease); err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if fake.heartbeatLease != 5*time.Minute {
		t.Fatalf("renewed lease = %v, want the claimed 5m (the floor must follow the queue)", fake.heartbeatLease)
	}
}
