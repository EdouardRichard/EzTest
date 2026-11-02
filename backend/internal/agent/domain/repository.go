package agent

import (
	"context"
	"errors"
	"time"
)

// Persistence errors raised by the agent repositories.
var (
	// ErrNotFound means the requested row does not exist.
	ErrNotFound = errors.New("agent: not found")
	// ErrLeaseLost means the write was fenced by a newer lease_epoch.
	ErrLeaseLost = errors.New("agent: lease lost")
	// ErrConflict means a uniqueness constraint rejected the write.
	ErrConflict = errors.New("agent: conflict")
)

// WorkItemRepository is the persistence port for the single execution queue.
// Every mutation that carries an epoch must fence on (id, lease_epoch) and
// report ErrLeaseLost when zero rows match.
type WorkItemRepository interface {
	// Ensure inserts an open item or returns the existing row for
	// (version_id, type, dedupe_key); created distinguishes both cases.
	Ensure(ctx context.Context, in NewWorkItem) (item WorkItem, created bool, err error)
	// Claim picks the highest-priority available open item with
	// FOR UPDATE SKIP LOCKED and leases it to claimant.
	Claim(ctx context.Context, claimant string, lease time.Duration) (item WorkItem, ok bool, err error)
	// ClaimForTeams is Claim restricted to the given teams. An empty team set
	// claims nothing.
	ClaimForTeams(ctx context.Context, claimant string, teams []Team, lease time.Duration) (item WorkItem, ok bool, err error)
	// ClaimForTypes is Claim restricted to the given work item types. Unlike
	// Claim it also takes over claimed/running items whose lease expired, so a
	// crashed render/worker instance cannot strand its item until recovery.
	// An empty type set claims nothing.
	ClaimForTypes(ctx context.Context, claimant string, types []string, lease time.Duration) (item WorkItem, ok bool, err error)
	// Get loads one work item, or ErrNotFound.
	Get(ctx context.Context, id int64) (WorkItem, error)
	// MarkRunning moves a claimed item to running.
	MarkRunning(ctx context.Context, id, epoch int64) error
	// Renew extends the lease of a claimed/running item.
	Renew(ctx context.Context, id, epoch int64, lease time.Duration) error
	// Complete finishes an item.
	Complete(ctx context.Context, id, epoch int64) error
	// MarkWaitingHuman parks a running item for a human takeover. It fences on
	// (id, lease_epoch) and reports ErrLeaseLost when zero rows match.
	MarkWaitingHuman(ctx context.Context, id, epoch int64) error
	// Finish closes a waiting_human/pending item without a failure, recording
	// the reason in last_error. It is idempotent for an already-done item.
	Finish(ctx context.Context, id int64, reason string) error
	// Fail records a failure. The repository decides between open (retry with
	// the supplied backoff) and dead based on retryable/attempts/max_attempts.
	Fail(ctx context.Context, id, epoch int64, message string, retryable bool, backoff time.Duration) error
	// RecoverExpired re-opens claimed/running items whose lease expired and
	// marks them dead when attempts already reached max_attempts. It returns
	// the number of affected items.
	RecoverExpired(ctx context.Context) (recovered int64, err error)
	// ListByVersion lists a version's items newest first.
	ListByVersion(ctx context.Context, versionID int64, limit int) ([]WorkItem, error)
	// ListAttempts returns the attempt log for one work item, oldest first.
	ListAttempts(ctx context.Context, workItemID int64) ([]WorkItemAttempt, error)
}

// TeamRoomRepository persists team-room posts and bounded digest versions.
type TeamRoomRepository interface {
	// EnsurePost inserts a post, returning the stored row. Re-inserting the
	// same ID is a no-op that returns the existing row.
	EnsurePost(ctx context.Context, post TeamRoomPost) (TeamRoomPost, error)
	// ListPosts returns the newest posts for a version/team pair, ID descending.
	ListPosts(ctx context.Context, versionID int64, team Team, limit int) ([]TeamRoomPost, error)
	// ListPostsSince returns posts created strictly after since, oldest first.
	ListPostsSince(ctx context.Context, versionID int64, team Team, since time.Time, limit int) ([]TeamRoomPost, error)
	// CountPosts counts posts for a version/team pair.
	CountPosts(ctx context.Context, versionID int64, team Team) (int, error)
	// CountPostsSince counts posts created strictly after since.
	CountPostsSince(ctx context.Context, versionID int64, team Team, since time.Time) (int, error)
	// LatestDigest returns the newest digest, or ErrNotFound.
	LatestDigest(ctx context.Context, versionID int64, team Team) (TeamRoomDigest, error)
	// SaveDigest inserts one digest version; a duplicate version is
	// ErrConflict.
	SaveDigest(ctx context.Context, digest TeamRoomDigest) (TeamRoomDigest, error)
	// PruneDigests keeps only the newest keep digest versions and deletes the
	// rest.
	PruneDigests(ctx context.Context, versionID int64, team Team, keep int) error
}

// RuntimeRepository persists profiles, runs, LLM transcripts and context
// packets.
type RuntimeRepository interface {
	// EnsureProfile inserts a profile by key when missing. An existing profile
	// is returned unchanged (created=false) so admin edits are never
	// overwritten by the startup seed.
	EnsureProfile(ctx context.Context, profile AgentProfile) (stored AgentProfile, created bool, err error)
	// GetProfile loads a profile by key, or ErrNotFound.
	GetProfile(ctx context.Context, key string) (AgentProfile, error)
	// ListProfiles returns every profile ordered by key.
	ListProfiles(ctx context.Context) ([]AgentProfile, error)
	// CreateRun inserts a running agent_run row.
	CreateRun(ctx context.Context, run AgentRun) (AgentRun, error)
	// FinishRun persists the terminal status/counters. It fences on
	// (id, lease_epoch) and returns ErrLeaseLost when the run was taken over.
	FinishRun(ctx context.Context, run AgentRun) error
	// AppendMessage inserts one transcript row.
	AppendMessage(ctx context.Context, message AgentMessage) (AgentMessage, error)
	// ListRunMessages returns a run's transcript in ID order.
	ListRunMessages(ctx context.Context, runID int64, limit int) ([]AgentMessage, error)
	// SaveContextPacket inserts a packet and points its run at it.
	SaveContextPacket(ctx context.Context, packet ContextPacket) (ContextPacket, error)
	// GetContextPacket loads one packet, or ErrNotFound.
	GetContextPacket(ctx context.Context, id int64) (ContextPacket, error)
}
