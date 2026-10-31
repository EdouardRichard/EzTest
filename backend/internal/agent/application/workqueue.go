package agentapp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// P1 lease constants (spec P1-D4). They are intentionally not settings keys:
// the design fixed the range (lease 30s, heartbeat 10s) and P1 avoids growing
// the settings catalog.
const (
	DefaultLease       = 30 * time.Second
	DefaultHeartbeat   = 10 * time.Second
	DefaultMaxAttempts = agent.DefaultMaxAttempts
	MaxBackoff         = 30 * time.Second
	DefaultListLimit   = 50
	MaxListLimit       = 500
)

// WorkQueue is the lease-based facade over the work_item repository. It owns
// the P1 retry policy: retryable failures reopen the item with an exponential
// backoff capped at MaxBackoff; exhausted or non-retryable failures go dead.
type WorkQueue struct {
	repo        agent.WorkItemRepository
	clock       clock.Clock
	lease       time.Duration
	maxAttempts int
	events      eventingapp.Publisher
}

// NewWorkQueue wires the work queue. Zero/invalid tuning values fall back to
// the P1 defaults so the assembly root can pass settings-derived values later.
func NewWorkQueue(repo agent.WorkItemRepository, clk clock.Clock, lease time.Duration, maxAttempts int) *WorkQueue {
	if clk == nil {
		clk = clock.System{}
	}
	if lease <= 0 {
		lease = DefaultLease
	}
	if maxAttempts < agent.MinMaxAttempts || maxAttempts > agent.MaxMaxAttempts {
		maxAttempts = DefaultMaxAttempts
	}
	return &WorkQueue{repo: repo, clock: clk, lease: lease, maxAttempts: maxAttempts}
}

// SetEvents attaches the event publisher. Work item creation events are
// best-effort: a publish failure never rolls back the ensure.
func (q *WorkQueue) SetEvents(publisher eventingapp.Publisher) {
	q.events = publisher
}

// Ensure inserts a deduplicated open work item. When the repository actually
// creates a new row, it publishes work_item.created exactly once.
func (q *WorkQueue) Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	if in.MaxAttempts == 0 {
		in.MaxAttempts = q.maxAttempts
	}
	if err := in.Validate(); err != nil {
		return agent.WorkItem{}, false, err
	}
	item, created, err := q.repo.Ensure(ctx, in)
	if err != nil {
		return agent.WorkItem{}, false, err
	}
	if created && q.events != nil {
		_ = q.events.Publish(ctx, eventingapp.Event{
			Channel: "version:" + platformid.Format(item.VersionID),
			Type:    "work_item.created",
			Scope:   map[string]any{"version_id": platformid.Format(item.VersionID)},
			Payload: map[string]any{
				"work_item_id": platformid.Format(item.ID),
				"type":         string(item.Type),
				"team":         string(item.Team),
				"dedupe_key":   item.DedupeKey,
			},
			RequestID: requestid.From(ctx),
		})
	}
	return item, created, nil
}

// Claim leases the next available item to claimant.
func (q *WorkQueue) Claim(ctx context.Context, claimant string) (agent.WorkItem, bool, error) {
	claimant = strings.TrimSpace(claimant)
	if claimant == "" {
		return agent.WorkItem{}, false, platform.Invalid("claimant", "must not be empty")
	}
	return q.repo.Claim(ctx, claimant, q.lease)
}

// MarkRunning moves a freshly claimed item to running.
func (q *WorkQueue) MarkRunning(ctx context.Context, id, epoch int64) error {
	return mapLeaseLost(q.repo.MarkRunning(ctx, id, epoch))
}

// Heartbeat extends the current lease without changing the epoch.
func (q *WorkQueue) Heartbeat(ctx context.Context, id, epoch int64) error {
	return mapLeaseLost(q.repo.Renew(ctx, id, epoch, q.lease))
}

// Complete finishes a work item.
func (q *WorkQueue) Complete(ctx context.Context, id, epoch int64) error {
	return mapLeaseLost(q.repo.Complete(ctx, id, epoch))
}

// MarkWaitingHuman parks a running work item for a human takeover (P3 §6.8).
func (q *WorkQueue) MarkWaitingHuman(ctx context.Context, id, epoch int64) error {
	return mapLeaseLost(q.repo.MarkWaitingHuman(ctx, id, epoch))
}

// Finish closes a waiting_human/pending work item without a failure (the
// resolved human takeover creates a fresh execute.case item).
func (q *WorkQueue) Finish(ctx context.Context, id int64, reason string) error {
	return q.repo.Finish(ctx, id, reason)
}

// Fail records a failure. Retryable failures reopen the item after a
// min(2^attempts, 30s) backoff; other failures are marked dead by the
// repository once attempts reach max_attempts.
func (q *WorkQueue) Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error {
	item, err := q.repo.Get(ctx, id)
	if err != nil {
		return err
	}
	backoff := time.Duration(0)
	if retryable {
		backoff = backoffForAttempts(item.Attempts)
	}
	message := ""
	if cause != nil {
		message = cause.Error()
	}
	return mapLeaseLost(q.repo.Fail(ctx, id, epoch, message, retryable, backoff))
}

// RecoverExpired re-opens claimed/running items whose lease expired. It is
// called once at startup and then periodically by the worker; it never
// interrupts a live run.
func (q *WorkQueue) RecoverExpired(ctx context.Context) (int64, error) {
	return q.repo.RecoverExpired(ctx)
}

// ListByVersion lists a version's work items newest first.
func (q *WorkQueue) ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error) {
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}
	return q.repo.ListByVersion(ctx, versionID, limit)
}

// Lease returns the configured lease duration.
func (q *WorkQueue) Lease() time.Duration { return q.lease }

// MaxAttempts returns the configured default attempt budget.
func (q *WorkQueue) MaxAttempts() int { return q.maxAttempts }

// backoffForAttempts implements min(2^attempts, 30) seconds. Attempts is the
// value after the claim increment, so the first retry waits 2s.
func backoffForAttempts(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	if attempts >= 5 {
		return MaxBackoff
	}
	seconds := 1 << uint(attempts)
	if seconds > int(MaxBackoff/time.Second) {
		return MaxBackoff
	}
	return time.Duration(seconds) * time.Second
}

// mapLeaseLost turns a repository fencing failure into the stable 409 error.
func mapLeaseLost(err error) error {
	if errors.Is(err, agent.ErrLeaseLost) {
		return platform.NewError(http.StatusConflict, "lease_lost", "work item lease was taken over by another claimant")
	}
	return err
}
