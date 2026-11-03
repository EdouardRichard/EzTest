package agent

import (
	"context"
	"encoding/json"
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

// WorkItemContractRepository is the additive 0.2.1 work_item surface used by
// the dispatcher and executor runtime. It is intentionally separate from
// WorkItemRepository so the frozen P1–P8 workers and their fakes keep
// compiling. Implementations must fence every epoch-bearing write on
// (id, lease_epoch) and report ErrLeaseLost when zero rows match.
type WorkItemContractRepository interface {
	// CreateWorkItem inserts an open item, or returns the existing row for
	// (version_id, kind, idempotency_key). An empty idempotency key is stored
	// as NULL and every call inserts a new row.
	CreateWorkItem(ctx context.Context, in CreateWorkItemParams) (WorkItem, error)
	// ClaimWorkItem leases the next runnable item to owner as of now. Runnable
	// means open, retryable failed, or a claimed/running item whose lease
	// expired; all depends_on items must be done (NULL/{} means no deps). It
	// increments lease_epoch, attempts and inserts a claimed attempt row. A nil
	// item with a nil error means nothing is runnable.
	ClaimWorkItem(ctx context.Context, owner string, now time.Time) (*WorkItem, error)
	// ClaimWorkItemForTeams is ClaimWorkItem restricted to the given teams. The
	// agent worker uses it so a 0.2.1 stage-entry item is only taken by the
	// executor that owns its team. An empty team set claims nothing and returns
	// (nil, nil); team validity is the caller's business (WorkQueue validates).
	ClaimWorkItemForTeams(ctx context.Context, owner string, teams []Team, now time.Time) (*WorkItem, error)
	// RenewLease extends the lease to the absolute until. A stale epoch is
	// ErrLeaseLost.
	RenewLease(ctx context.Context, id, epoch int64, until time.Time) error
	// CompleteWorkItem marks a claimed/running item done and stores result.
	CompleteWorkItem(ctx context.Context, id, epoch int64, result json.RawMessage) error
	// FailWorkItem records a failure. Failed items remain retryable by
	// ClaimWorkItem until attempts reach max_attempts.
	FailWorkItem(ctx context.Context, id, epoch int64, reason string) error
	// FailWorkItemTerminal records a failure that must never be retried: the
	// row is stored failed with attempts=max_attempts (the frozen schema has no
	// 'dead' status in 0.2.1), so ClaimWorkItem can never select it again while
	// the job-control API still projects the truthful failed state.
	FailWorkItemTerminal(ctx context.Context, id, epoch int64, reason string) error
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
	// CreateRun inserts one agent_run row with the status it carries: running
	// for a dispatch-ready attempt, failed with a terminal_reason/finished_at
	// for an attempt that stopped before dispatch.
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
	// UpdateContextPacketCacheStats overwrites the provider-reported prompt
	// cache bookkeeping of a persisted packet (contracts §18.2.6). The packet
	// content is immutable; only token_counts changes after the first model
	// response. It returns ErrNotFound when the packet does not exist.
	UpdateContextPacketCacheStats(ctx context.Context, id int64, cachedTokens int, cacheHit bool) error
}
