package agentapp

// Task 46 (P2-T6): the user-facing job control API. A 0.2.1 work item is the
// UI's "job"; this service owns the two user actions on it:
//
//   - Cancel: open/claimed/running jobs only. The lease is released, the active
//     attempt is closed and the row is stored terminal. The frozen work_item
//     schema has no 'cancelled' status, so a canceled job is persisted as
//     failed with the attempt budget exhausted (the same terminal shape
//     ClaimWorkItem already uses at the attempt cap) and LastError set to
//     JobCanceledReason. The API projection reports state=cancelled and the
//     truthful pre-cancel attempts (not the exhausted storage marker), and the
//     service publishes the contract terminal event job.finished{status:
//     cancelled} exactly once after the actor-facing job.canceled event.
//   - Retry: failed jobs below max_attempts only. The row returns to open with
//     available_at=now and the lease cleared, and attempts is reset to 0: a
//     user-initiated retry grants a fresh attempt budget while keeping
//     max_attempts. This takes precedence over the automatic dispatcher retry
//     (ClaimWorkItem), which consumes attempts; the explicit user action starts
//     a new budget. The model/inputs/budget are never touched (no automatic
//     model switch).
//
// Every accepted action records one audit entry and publishes its
// version-channel event (job.canceled / job.requeued, plus the terminal
// job.finished for a cancel). They are best effort: a failed audit or publish
// never rolls back the lifecycle write.
//
// Ownership is version-scoped: a caller that addresses a job through a version
// gets not_found for a row owned by another version, and the service re-checks
// the version itself in addition to the repository's scope.

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/requestid"
)

// JobStateCancelled is the API lifecycle value of a canceled job (spec §5.4).
// The frozen work_item schema stores 'failed' and this service projects it.
const JobStateCancelled = "cancelled"

// JobCanceledReason is the work_item.last_error marker of a user-canceled job.
// It is also how the retry guard recognizes a canceled row despite the frozen
// schema storing it as failed.
const JobCanceledReason = "cancelled by user"

// Audit actions of the two job control operations.
const (
	ActionJobCanceled = "job.canceled"
	ActionJobRequeued = "job.requeued"
)

// Version-channel event types of the two job control operations
// (contracts/events.md §18; job_id is work_item.id).
const (
	EventJobCanceled = "job.canceled"
	EventJobRequeued = "job.requeued"
)

// JobControlRepository is the narrow persistence surface of the job control
// API. *agentpg.WorkItemRepository satisfies it. Implementations must:
//   - scope every read/write by versionID when it is positive (version 0 means
//     the caller addressed the job directly, so the job's own version owns it);
//   - return agent.ErrNotFound for a missing or foreign row;
//   - return agent.ErrConflict when the lease epoch or the guarded state no
//     longer matches (a concurrent claim/complete won the race).
type JobControlRepository interface {
	// GetJob loads one work item, scoped to versionID when positive.
	GetJob(ctx context.Context, versionID, id int64) (agent.WorkItem, error)
	// CancelJob atomically turns an open/claimed/running item terminal:
	// status failed, lease cleared, lease_epoch incremented (fencing the old
	// owner), attempts set to max_attempts, available_at/updated_at set to now,
	// last_error set to reason, and the active attempt closed. leaseEpoch is
	// the epoch read by GetJob. The exhausted attempts value is the
	// no-migration marker of a canceled row: retry-rate and todo queries must
	// exclude it (see the PostgreSQL QueryReader's cancel-marker predicate).
	// The returned row therefore carries that marker; the service projects
	// the truthful pre-cancel Attempts value itself.
	CancelJob(ctx context.Context, versionID, id, leaseEpoch int64, reason string, now time.Time) (agent.WorkItem, error)
	// RetryJob atomically re-opens a failed item below max_attempts:
	// status open, lease cleared, last_error cleared, available_at and
	// updated_at set to now, and attempts reset to 0 (a user-initiated retry
	// grants a fresh budget). max_attempts and the contract inputs are
	// preserved.
	RetryJob(ctx context.Context, versionID, id int64, now time.Time) (agent.WorkItem, error)
}

// JobCardUpdater is the narrow persisted-conversation-card surface of the job
// control API (Task 63). CancelJobCard rewrites the one job_card message of a
// canceled job to the terminal cancelled snapshot; ReopenJobCard returns a
// retried job's card to the non-terminal open snapshot (the explicit bypass of
// the sticky cancelled card, so the new attempt can reuse it). The version
// context owns the lookup, the one-card-per-job rule and the SSE republication;
// the agent side only names the job. A job without a card (legacy or
// non-conversation job) is a no-op. *versionapp.ConversationService satisfies
// it.
type JobCardUpdater interface {
	CancelJobCard(ctx context.Context, versionID, conversationID, jobID int64) error
	ReopenJobCard(ctx context.Context, versionID, conversationID, jobID int64) error
}

// JobControlDeps wires the job control service. Jobs is required; Audit,
// Events and JobCards are best-effort and Clock defaults to the system clock.
type JobControlDeps struct {
	Jobs     JobControlRepository
	Audit    auditapp.Recorder
	Events   eventingapp.Publisher
	JobCards JobCardUpdater
	Clock    clock.Clock
}

// JobControl implements the cancel/retry use cases.
type JobControl struct {
	jobs     JobControlRepository
	audit    auditapp.Recorder
	events   eventingapp.Publisher
	jobCards JobCardUpdater
	clock    clock.Clock
}

// NewJobControl wires the service.
func NewJobControl(deps JobControlDeps) *JobControl {
	clk := deps.Clock
	if clk == nil {
		clk = clock.System{}
	}
	return &JobControl{
		jobs: deps.Jobs, audit: deps.Audit, events: deps.Events,
		jobCards: deps.JobCards, clock: clk,
	}
}

// JobView is the API projection of one job after a control action.
type JobView struct {
	JobID       int64
	VersionID   int64
	Kind        string
	State       string
	Attempts    int
	MaxAttempts int
	UpdatedAt   time.Time
}

// Cancel cancels one pending/claimed/running job. actorID is the authenticated
// user; versionID is 0 when the caller used the direct job route, otherwise it
// is the version the caller addressed.
func (s *JobControl) Cancel(ctx context.Context, actorID, versionID, jobID int64) (JobView, error) {
	if err := s.requireConfigured(); err != nil {
		return JobView{}, err
	}
	if err := validateJobControlIDs(versionID, jobID); err != nil {
		return JobView{}, err
	}
	item, err := s.jobs.GetJob(ctx, versionID, jobID)
	if err != nil {
		return JobView{}, mapJobControlReadError(err)
	}
	if versionID > 0 && item.VersionID != versionID {
		return JobView{}, platform.NotFound("job")
	}
	if !isControllableJob(item) {
		return JobView{}, platform.NotFound("job")
	}
	if !cancellableJobStatus(item.Status) {
		return JobView{}, platform.StateConflict(fmt.Sprintf(
			"job %d cannot be canceled from status %s", jobID, item.Status,
		))
	}

	updated, err := s.jobs.CancelJob(ctx, versionID, jobID, item.LeaseEpoch, JobCanceledReason, s.now())
	if err != nil {
		return JobView{}, mapJobControlWriteError(err)
	}
	s.record(ctx, ActionJobCanceled, actorID, item, updated)
	s.publish(ctx, EventJobCanceled, actorID, updated, JobStateCancelled)
	// The terminal contract event follows the actor-facing cancel event. The
	// stored status is failed (no cancelled value in the frozen schema), so the
	// projected cancelled state is passed explicitly.
	s.publishFinished(ctx, updated)
	// Task 63 (P2-T10): the live stream now says cancelled, but the persisted
	// conversation card still snapshots the job as running. Rewrite that same
	// card to the terminal cancelled state so a reload agrees with the live UI.
	// It runs after the state/audit/event writes and is best effort: a job
	// without a card is a no-op, and a failed card write never rolls back the
	// cancel.
	s.cancelJobCard(ctx, updated)
	// The no-migration persistence compromise stores the canceled row at
	// attempts=max_attempts so the dispatcher can never claim it again. That
	// terminal marker is storage bookkeeping, not the job's attempt position:
	// report the truthful pre-cancel attempts captured by the read above. The
	// lease-epoch fence in CancelJob guarantees the row did not move since
	// then.
	view := jobControlView(updated)
	view.Attempts = item.Attempts
	return view, nil
}

// Retry re-opens one failed job below its attempt cap.
func (s *JobControl) Retry(ctx context.Context, actorID, versionID, jobID int64) (JobView, error) {
	if err := s.requireConfigured(); err != nil {
		return JobView{}, err
	}
	if err := validateJobControlIDs(versionID, jobID); err != nil {
		return JobView{}, err
	}
	item, err := s.jobs.GetJob(ctx, versionID, jobID)
	if err != nil {
		return JobView{}, mapJobControlReadError(err)
	}
	if versionID > 0 && item.VersionID != versionID {
		return JobView{}, platform.NotFound("job")
	}
	if !isControllableJob(item) {
		return JobView{}, platform.NotFound("job")
	}
	if isCanceledJob(item) {
		return JobView{}, platform.StateConflict(fmt.Sprintf(
			"job %d was canceled and cannot be retried", jobID,
		))
	}
	if item.Status != agent.WorkItemFailed {
		return JobView{}, platform.StateConflict(fmt.Sprintf(
			"job %d cannot be retried from status %s", jobID, item.Status,
		))
	}
	if item.Attempts >= item.MaxAttempts {
		return JobView{}, platform.StateConflict(fmt.Sprintf(
			"job %d has exhausted its attempt budget (%d/%d)", jobID, item.Attempts, item.MaxAttempts,
		))
	}

	updated, err := s.jobs.RetryJob(ctx, versionID, jobID, s.now())
	if err != nil {
		return JobView{}, mapJobControlWriteError(err)
	}
	s.record(ctx, ActionJobRequeued, actorID, item, updated)
	s.publish(ctx, EventJobRequeued, actorID, updated, string(agent.WorkItemOpen))
	// Task 63 fix round 2: the retried job starts a new attempt, so a card left
	// cancelled (by a cancel racing the retry, or by an earlier projection) must
	// be reopened to the non-terminal open snapshot. Otherwise the sticky
	// cancelled card would ignore every projection of the new attempt. Best
	// effort like every card write-back: a failed reopen never rolls back the
	// committed retry.
	s.reopenJobCard(ctx, updated)
	return jobControlView(updated), nil
}

// ---------- internals ----------

func (s *JobControl) requireConfigured() error {
	if s == nil || s.jobs == nil {
		return platform.Unavailable("job control service is not configured")
	}
	return nil
}

func (s *JobControl) now() time.Time {
	if s != nil && s.clock != nil {
		return s.clock.Now().UTC()
	}
	return time.Now().UTC()
}

func validateJobControlIDs(versionID, jobID int64) error {
	if versionID < 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if jobID <= 0 {
		return platform.Invalid("id", "must be a positive decimal ID")
	}
	return nil
}

func cancellableJobStatus(status agent.WorkItemStatus) bool {
	switch status {
	case agent.WorkItemOpen, agent.WorkItemClaimed, agent.WorkItemRunning:
		return true
	default:
		return false
	}
}

// isControllableJob fences the pre-0.2.1 work items out of the control API.
// Rows created before the 0.2.1 contract have an empty kind (agent_0007_work_item021
// adds the column with that default) and are still dispatched by the legacy
// Claim/ClaimForTeams/ClaimForTypes path. That path derives the
// work_item_attempt number from the attempts budget counter, unlike
// ClaimWorkItem, which uses MAX(attempt)+1. A raw-ID retry of a legacy row would
// re-open it under the 0.2.1 rules and let the two numbering schemes collide, so
// the control API reports it as not found instead of touching it.
func isControllableJob(item agent.WorkItem) bool {
	return strings.TrimSpace(item.Kind) != ""
}

func isCanceledJob(item agent.WorkItem) bool {
	return item.Status == agent.WorkItemFailed &&
		item.LastError != nil && *item.LastError == JobCanceledReason
}

func jobControlView(item agent.WorkItem) JobView {
	state := string(item.Status)
	if isCanceledJob(item) {
		state = JobStateCancelled
	}
	return JobView{
		JobID:       item.ID,
		VersionID:   item.VersionID,
		Kind:        item.Kind,
		State:       state,
		Attempts:    item.Attempts,
		MaxAttempts: item.MaxAttempts,
		UpdatedAt:   item.UpdatedAt,
	}
}

func mapJobControlReadError(err error) error {
	if errors.Is(err, agent.ErrNotFound) {
		return platform.NotFound("job")
	}
	return err
}

func mapJobControlWriteError(err error) error {
	switch {
	case errors.Is(err, agent.ErrNotFound):
		return platform.NotFound("job")
	case errors.Is(err, agent.ErrConflict), errors.Is(err, agent.ErrLeaseLost):
		return platform.StateConflict("job state changed before the request could be applied; refresh and try again")
	default:
		return err
	}
}

// record writes the best-effort audit entry of one accepted control action. The
// metadata captures the pre-action lifecycle values plus the updated ones.
func (s *JobControl) record(ctx context.Context, action string, actorID int64, before, after agent.WorkItem) {
	if s == nil || s.audit == nil {
		return
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	job := platformid.Format(after.ID)
	_ = s.audit.Record(ctx, auditapp.Event{
		ActorID:    actor,
		Action:     action,
		TargetKind: "work_item",
		TargetID:   job,
		RequestID:  requestid.From(ctx),
		Metadata: map[string]any{
			"job_id":       job,
			"kind":         after.Kind,
			"from_status":  string(before.Status),
			"to_status":    string(after.Status),
			"attempts":     before.Attempts,
			"max_attempts": before.MaxAttempts,
		},
	})
}

// publish emits one version-channel job event. It follows the job.progress /
// job.finished shape (job_id/kind/status, decimal string IDs) so the UI can
// consume cancel/requeue on the same stream.
func (s *JobControl) publish(ctx context.Context, eventType string, actorID int64, item agent.WorkItem, status string) {
	if s == nil || s.events == nil || item.VersionID <= 0 || item.ID <= 0 {
		return
	}
	var actor *int64
	if actorID > 0 {
		actor = &actorID
	}
	version := platformid.Format(item.VersionID)
	job := platformid.Format(item.ID)
	_ = s.events.Publish(ctx, eventingapp.Event{
		Channel: "version:" + version,
		Type:    eventType,
		Scope:   map[string]any{"version_id": version, "job_id": job},
		Payload: map[string]any{
			"job_id": job,
			"kind":   item.Kind,
			"status": status,
		},
		ActorID:   actor,
		RequestID: requestid.From(ctx),
	})
}

// publishFinished emits the one-time contract terminal event of a cancel
// (job.finished{status:"cancelled"}, events.md §18.1/§18.2 #4). It goes through
// eventingapp.PublishJobFinished — the single place that enforces the
// job.finished payload shape (job_id/kind/status/result_ref) — so the UI's
// terminal state comes from the same producer task as a normally finished job.
// A canceled row is stored failed for the no-migration compromise, so the raw
// column value must never be used here. Best effort like every job event: a
// failed publish never rolls back the cancel.
func (s *JobControl) publishFinished(ctx context.Context, item agent.WorkItem) {
	if s == nil || s.events == nil || item.VersionID <= 0 || item.ID <= 0 {
		return
	}
	_ = eventingapp.PublishJobFinished(ctx, s.events, item.VersionID, eventingapp.JobFinished{
		JobID:  item.ID,
		Kind:   item.Kind,
		Status: eventingapp.JobStatusCancelled,
	})
}

// jobCardUpdateTimeout bounds the best-effort persisted-card write-back. The
// cancel/retry itself is already committed and published; a stuck card store
// must not hold the HTTP response open indefinitely.
const jobCardUpdateTimeout = 5 * time.Second

// updateJobCard runs one best-effort card write-back for item's conversation.
// The conversation association is the only card linkage (Task 61 stores it in
// the frozen work_item payload column), so a job without one never had a card
// and is a no-op — that covers both legacy rows and non-conversation jobs. The
// error of the write is deliberately dropped: the committed work item and its
// events are the truth, never the card.
func (s *JobControl) updateJobCard(ctx context.Context, item agent.WorkItem, write func(context.Context, int64, int64, int64) error) {
	if s == nil || s.jobCards == nil || item.VersionID <= 0 || item.ID <= 0 || write == nil {
		return
	}
	payload, err := ParseWorkItemPayload(item.Payload)
	if err != nil || payload.ConversationID <= 0 {
		return
	}
	cardCtx, cancel := context.WithTimeout(ctx, jobCardUpdateTimeout)
	defer cancel()
	_ = write(cardCtx, item.VersionID, payload.ConversationID, item.ID)
}

// cancelJobCard asks the version context to rewrite the conversation card of a
// canceled job to the terminal cancelled snapshot.
func (s *JobControl) cancelJobCard(ctx context.Context, item agent.WorkItem) {
	if s == nil || s.jobCards == nil {
		return
	}
	s.updateJobCard(ctx, item, s.jobCards.CancelJobCard)
}

// reopenJobCard asks the version context to return a retried job's card to the
// non-terminal open snapshot — the explicit bypass of the sticky cancelled card,
// so the new attempt can update the same card again. Best effort like the cancel
// write-back: a failed reopen never rolls back the retry.
func (s *JobControl) reopenJobCard(ctx context.Context, item agent.WorkItem) {
	if s == nil || s.jobCards == nil {
		return
	}
	s.updateJobCard(ctx, item, s.jobCards.ReopenJobCard)
}
