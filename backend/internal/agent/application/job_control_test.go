package agentapp_test

// Task 46 (P2-T6): the job cancel/retry control API. These tests pin the
// application semantics: only active jobs can be canceled, only failed jobs
// below the attempt cap can be requeued, ownership is scoped to the job's
// version, and every accepted mutation writes exactly one audit entry plus one
// version-channel job event. The retry path must not rewrite the job inputs
// (there is deliberately no automatic model switch).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

// ---------- Task 63: persisted job card updater ----------

// jobCardUpdateCall is one best-effort conversation-card write-back requested by
// a cancel or a retry.
type jobCardUpdateCall struct {
	VersionID      int64
	ConversationID int64
	JobID          int64
}

// fakeJobCardUpdater records the cancel/retry card write-backs. Its error models
// the card store being unavailable: the job lifecycle action must still succeed.
type fakeJobCardUpdater struct {
	mu          sync.Mutex
	cancelCalls []jobCardUpdateCall
	reopenCalls []jobCardUpdateCall
	err         error
}

func (u *fakeJobCardUpdater) CancelJobCard(_ context.Context, versionID, conversationID, jobID int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cancelCalls = append(u.cancelCalls, jobCardUpdateCall{
		VersionID: versionID, ConversationID: conversationID, JobID: jobID,
	})
	return u.err
}

func (u *fakeJobCardUpdater) ReopenJobCard(_ context.Context, versionID, conversationID, jobID int64) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.reopenCalls = append(u.reopenCalls, jobCardUpdateCall{
		VersionID: versionID, ConversationID: conversationID, JobID: jobID,
	})
	return u.err
}

func (u *fakeJobCardUpdater) recorded() []jobCardUpdateCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	calls := make([]jobCardUpdateCall, len(u.cancelCalls))
	copy(calls, u.cancelCalls)
	return calls
}

func (u *fakeJobCardUpdater) reopened() []jobCardUpdateCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	calls := make([]jobCardUpdateCall, len(u.reopenCalls))
	copy(calls, u.reopenCalls)
	return calls
}

// ---------- fake job control repository ----------

type jobCancelCall struct {
	VersionID int64
	ID        int64
	Epoch     int64
	Reason    string
	Now       time.Time
}

type jobRetryCall struct {
	VersionID int64
	ID        int64
	Now       time.Time
}

// fakeJobControlRepo is an in-memory JobControlRepository. It applies the
// lifecycle transitions the PostgreSQL adapter performs, so the service tests
// can assert the post-write state without a database.
type fakeJobControlRepo struct {
	mu    sync.Mutex
	items map[int64]agent.WorkItem
	// ignoreVersion models a repository that fails to scope its read; the
	// service must still reject a foreign row.
	ignoreVersion bool

	getCalls    int
	getVersions []int64
	cancelCalls int
	retryCalls  int
	lastCancel  jobCancelCall
	lastRetry   jobRetryCall
}

func newFakeJobControlRepo() *fakeJobControlRepo {
	return &fakeJobControlRepo{items: map[int64]agent.WorkItem{}}
}

func (r *fakeJobControlRepo) add(item agent.WorkItem) agent.WorkItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.items[item.ID] = item
	return item
}

func (r *fakeJobControlRepo) get(id int64) agent.WorkItem {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.items[id]
	if !ok {
		return agent.WorkItem{}
	}
	return item
}

func (r *fakeJobControlRepo) GetJob(_ context.Context, versionID, id int64) (agent.WorkItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.getCalls++
	r.getVersions = append(r.getVersions, versionID)
	item, ok := r.items[id]
	if !ok {
		return agent.WorkItem{}, agent.ErrNotFound
	}
	if versionID > 0 && item.VersionID != versionID && !r.ignoreVersion {
		return agent.WorkItem{}, agent.ErrNotFound
	}
	return item, nil
}

func (r *fakeJobControlRepo) CancelJob(_ context.Context, versionID, id, epoch int64, reason string, now time.Time) (agent.WorkItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancelCalls++
	r.lastCancel = jobCancelCall{VersionID: versionID, ID: id, Epoch: epoch, Reason: reason, Now: now}

	item, ok := r.items[id]
	if !ok || (versionID > 0 && item.VersionID != versionID) {
		return agent.WorkItem{}, agent.ErrNotFound
	}
	if item.LeaseEpoch != epoch || !jobIsCancellable(item.Status) {
		return agent.WorkItem{}, agent.ErrConflict
	}

	item.Status = agent.WorkItemFailed
	item.ClaimedBy = nil
	item.LeaseUntil = nil
	item.LeaseEpoch++
	item.LastError = jobStringPtr(reason)
	// A canceled job is terminal: the attempt budget is exhausted so
	// ClaimWorkItem can never pick it up again.
	if item.Attempts < item.MaxAttempts {
		item.Attempts = item.MaxAttempts
	}
	item.AvailableAt = now
	item.UpdatedAt = now
	r.items[id] = item
	return item, nil
}

func (r *fakeJobControlRepo) RetryJob(_ context.Context, versionID, id int64, now time.Time) (agent.WorkItem, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.retryCalls++
	r.lastRetry = jobRetryCall{VersionID: versionID, ID: id, Now: now}

	item, ok := r.items[id]
	if !ok || (versionID > 0 && item.VersionID != versionID) {
		return agent.WorkItem{}, agent.ErrNotFound
	}
	if !jobIsRetryable(item) {
		return agent.WorkItem{}, agent.ErrConflict
	}

	item.Status = agent.WorkItemOpen
	item.ClaimedBy = nil
	item.LeaseUntil = nil
	item.LastError = nil
	// A user-initiated retry grants a fresh attempt budget: attempts goes back
	// to 0 while max_attempts is preserved. This mirrors the PostgreSQL adapter.
	item.Attempts = 0
	item.AvailableAt = now
	item.UpdatedAt = now
	r.items[id] = item
	return item, nil
}

var _ agentapp.JobControlRepository = (*fakeJobControlRepo)(nil)

func jobIsCancellable(status agent.WorkItemStatus) bool {
	switch status {
	case agent.WorkItemOpen, agent.WorkItemClaimed, agent.WorkItemRunning:
		return true
	default:
		return false
	}
}

func jobIsRetryable(item agent.WorkItem) bool {
	if item.Status != agent.WorkItemFailed || item.Attempts >= item.MaxAttempts {
		return false
	}
	return item.LastError == nil || *item.LastError != agentapp.JobCanceledReason
}

func jobStringPtr(value string) *string { return &value }

// ---------- fixture ----------

type jobControlFixture struct {
	service  *agentapp.JobControl
	repo     *fakeJobControlRepo
	audit    *fakeAuditRecorder
	events   *fakePublisher
	jobCards *fakeJobCardUpdater
	clock    *fakeClock
}

func newJobControlFixture(t *testing.T, now time.Time) *jobControlFixture {
	t.Helper()
	fixture := &jobControlFixture{
		repo:     newFakeJobControlRepo(),
		audit:    &fakeAuditRecorder{},
		events:   &fakePublisher{},
		jobCards: &fakeJobCardUpdater{},
		clock:    &fakeClock{now: now},
	}
	fixture.service = agentapp.NewJobControl(agentapp.JobControlDeps{
		Jobs:     fixture.repo,
		Audit:    fixture.audit,
		Events:   fixture.events,
		JobCards: fixture.jobCards,
		Clock:    fixture.clock,
	})
	return fixture
}

var jobTestNow = time.Date(2026, 11, 3, 7, 30, 0, 0, time.UTC)

func jobControlItem(id, versionID int64, status agent.WorkItemStatus, attempts, maxAttempts int) agent.WorkItem {
	item := agent.WorkItem{
		ID:          id,
		VersionID:   versionID,
		Team:        agent.TeamOps,
		Type:        agent.WorkItemKindExecuteBatch,
		Kind:        agent.WorkItemKindExecuteBatch,
		Goal:        "execute the confirmed cases",
		Target:      json.RawMessage(`{"sut_id":9}`),
		Inputs:      json.RawMessage(`{"case_ids":[11,12]}`),
		Expected:    json.RawMessage(`{"artifact":"execution_result"}`),
		Budget:      json.RawMessage(`{"max_tokens":20000}`),
		Payload:     json.RawMessage(`{"model":"deepseek-chat"}`),
		Status:      status,
		Attempts:    attempts,
		MaxAttempts: maxAttempts,
		CreatedBy:   agent.WorkItemCreatedByStateMachine,
		AvailableAt: jobTestNow.Add(-time.Hour),
		CreatedAt:   jobTestNow.Add(-time.Hour),
		UpdatedAt:   jobTestNow.Add(-time.Hour),
	}
	if status == agent.WorkItemClaimed || status == agent.WorkItemRunning {
		claimedBy := "worker-7"
		leaseUntil := jobTestNow.Add(4 * time.Minute)
		item.ClaimedBy = &claimedBy
		item.LeaseUntil = &leaseUntil
		item.LeaseEpoch = int64(attempts) + 2
	}
	if status == agent.WorkItemFailed {
		reason := "assertion failed"
		item.LastError = &reason
	}
	return item
}

func (f *jobControlFixture) auditEvents() []auditEventView {
	// fakeAuditRecorder is shared by the application tests; copy under its mutex.
	f.audit.mu.Lock()
	defer f.audit.mu.Unlock()
	events := make([]auditEventView, 0, len(f.audit.events))
	for _, event := range f.audit.events {
		events = append(events, auditEventView{
			Action:     event.Action,
			TargetKind: event.TargetKind,
			TargetID:   event.TargetID,
			ActorID:    event.ActorID,
			Metadata:   event.Metadata,
		})
	}
	return events
}

// auditEventView is the subset of auditapp.Event these tests assert.
type auditEventView struct {
	Action     string
	TargetKind string
	TargetID   string
	ActorID    *int64
	Metadata   map[string]any
}

func (f *jobControlFixture) publishedEvents() []eventingapp.Event {
	events := make([]eventingapp.Event, len(f.events.events))
	copy(events, f.events.events)
	return events
}

// ---------- cancel ----------

func TestJobCancelPendingRunning(t *testing.T) {
	for _, testCase := range []struct {
		status   agent.WorkItemStatus
		attempts int
	}{
		// The pre-cancel budget position varies so the projection cannot be
		// confused with the stored terminal attempts=max_attempts marker.
		{agent.WorkItemOpen, 0},
		{agent.WorkItemClaimed, 1},
		{agent.WorkItemRunning, 2},
	} {
		status := testCase.status
		t.Run(string(status), func(t *testing.T) {
			fixture := newJobControlFixture(t, jobTestNow)
			before := fixture.repo.add(jobControlItem(501, 10, status, testCase.attempts, 3))

			view, err := fixture.service.Cancel(context.Background(), 71, before.VersionID, before.ID)
			if err != nil {
				t.Fatalf("Cancel(%s): %v", status, err)
			}

			if view.State != agentapp.JobStateCancelled {
				t.Fatalf("view state = %q, want %q", view.State, agentapp.JobStateCancelled)
			}
			if view.JobID != before.ID || view.VersionID != before.VersionID || view.Kind != before.Kind {
				t.Fatalf("view = %#v, want job %d version %d kind %s", view, before.ID, before.VersionID, before.Kind)
			}
			// Task 46 fix round 2: the frozen-schema compromise persists
			// attempts=max_attempts (so the dispatcher cannot claim the row),
			// but the API must report the truthful pre-cancel attempt
			// position, not that storage marker.
			if view.Attempts != before.Attempts || view.MaxAttempts != before.MaxAttempts {
				t.Fatalf("view attempts = %d/%d, want the pre-cancel %d/%d",
					view.Attempts, view.MaxAttempts, before.Attempts, before.MaxAttempts)
			}
			if !view.UpdatedAt.Equal(jobTestNow) {
				t.Fatalf("view updated_at = %s, want %s", view.UpdatedAt, jobTestNow)
			}

			stored := fixture.repo.get(before.ID)
			if stored.Status != agent.WorkItemFailed {
				t.Fatalf("stored status = %q, want failed (no cancelled status exists in the frozen schema)", stored.Status)
			}
			if stored.ClaimedBy != nil || stored.LeaseUntil != nil {
				t.Fatalf("lease was not cleared: claimed_by=%v lease_until=%v", stored.ClaimedBy, stored.LeaseUntil)
			}
			if stored.Attempts != before.MaxAttempts {
				t.Fatalf("stored attempts = %d, want max_attempts %d", stored.Attempts, before.MaxAttempts)
			}
			if stored.LastError == nil || *stored.LastError != agentapp.JobCanceledReason {
				t.Fatalf("stored last_error = %v, want %q", stored.LastError, agentapp.JobCanceledReason)
			}
			if stored.LeaseEpoch <= before.LeaseEpoch {
				t.Fatalf("lease_epoch = %d, want > %d (stale worker must be fenced)", stored.LeaseEpoch, before.LeaseEpoch)
			}
			if !stored.AvailableAt.Equal(jobTestNow) || !stored.UpdatedAt.Equal(jobTestNow) {
				t.Fatalf("stored timestamps = available %s updated %s, want %s", stored.AvailableAt, stored.UpdatedAt, jobTestNow)
			}

			if fixture.repo.cancelCalls != 1 {
				t.Fatalf("cancel calls = %d, want 1", fixture.repo.cancelCalls)
			}
			call := fixture.repo.lastCancel
			if call.VersionID != before.VersionID || call.ID != before.ID ||
				call.Epoch != before.LeaseEpoch || call.Reason != agentapp.JobCanceledReason ||
				!call.Now.Equal(jobTestNow) {
				t.Fatalf("cancel call = %#v", call)
			}

			audits := fixture.auditEvents()
			if len(audits) != 1 {
				t.Fatalf("audit events = %d, want 1: %#v", len(audits), audits)
			}
			audit := audits[0]
			if audit.Action != "job.canceled" || audit.TargetKind != "work_item" || audit.TargetID != "501" {
				t.Fatalf("audit = %#v", audit)
			}
			if audit.ActorID == nil || *audit.ActorID != 71 {
				t.Fatalf("audit actor = %v, want 71", audit.ActorID)
			}
			if audit.Metadata["job_id"] != "501" || audit.Metadata["kind"] != before.Kind ||
				audit.Metadata["from_status"] != string(status) {
				t.Fatalf("audit metadata = %#v", audit.Metadata)
			}
			if got, ok := audit.Metadata["attempts"].(int); !ok || got != before.Attempts {
				t.Fatalf("audit attempts = %#v, want %d", audit.Metadata["attempts"], before.Attempts)
			}
			if got, ok := audit.Metadata["max_attempts"].(int); !ok || got != before.MaxAttempts {
				t.Fatalf("audit max_attempts = %#v, want %d", audit.Metadata["max_attempts"], before.MaxAttempts)
			}

			events := fixture.publishedEvents()
			if len(events) != 2 {
				t.Fatalf("events = %d, want 2 (job.canceled + exactly one job.finished): %#v", len(events), events)
			}
			event := events[0]
			if event.Type != "job.canceled" || event.Channel != "version:10" {
				t.Fatalf("event type/channel = %q/%q", event.Type, event.Channel)
			}
			if event.Scope["version_id"] != "10" || event.Scope["job_id"] != "501" {
				t.Fatalf("event scope = %#v", event.Scope)
			}
			if event.Payload["job_id"] != "501" || event.Payload["kind"] != before.Kind ||
				event.Payload["status"] != agentapp.JobStateCancelled {
				t.Fatalf("event payload = %#v", event.Payload)
			}
			if event.ActorID == nil || *event.ActorID != 71 {
				t.Fatalf("event actor = %v, want 71", event.ActorID)
			}

			// Task 46 fix round 1: the contract terminal event is
			// job.finished{status:"cancelled"}, emitted once after the
			// actor-facing job.canceled event.
			finished := events[1]
			if finished.Type != eventingapp.EventJobFinished || finished.Channel != "version:10" {
				t.Fatalf("terminal event = %#v, want job.finished on version:10", finished)
			}
			if finished.Scope["version_id"] != "10" || finished.Scope["job_id"] != "501" {
				t.Fatalf("terminal scope = %#v", finished.Scope)
			}
			wantFinished := map[string]any{
				"job_id":     "501",
				"kind":       before.Kind,
				"status":     eventingapp.JobStatusCancelled,
				"result_ref": "",
			}
			for field, want := range wantFinished {
				if got := finished.Payload[field]; got != want {
					t.Fatalf("terminal payload[%q] = %#v, want %#v (payload %#v)",
						field, got, want, finished.Payload)
				}
			}
			if _, present := finished.Payload["error"]; present {
				t.Fatalf("cancel terminal event carries an error: %#v", finished.Payload)
			}

			// Exactly once: a repeated cancel is rejected and must not emit a
			// second terminal event.
			if _, err := fixture.service.Cancel(context.Background(), 71, before.VersionID, before.ID); err == nil {
				t.Fatal("second Cancel succeeded, want a state conflict")
			}
			finishedCount := 0
			for _, published := range fixture.publishedEvents() {
				if published.Type == eventingapp.EventJobFinished {
					finishedCount++
				}
			}
			if finishedCount != 1 {
				t.Fatalf("job.finished count after repeated cancel = %d, want exactly 1", finishedCount)
			}
		})
	}
}

// TestJobCancelRejectsInactiveStates is folded into the required
// TestJobCancelPendingRunning surface by Go's -run regexp; it pins the
// read-only boundary: waiting_human and every terminal state are 409 conflicts
// and never reach the repository.
func TestJobCancelRejectsInactiveStates(t *testing.T) {
	for _, status := range []agent.WorkItemStatus{
		agent.WorkItemWaitingHuman,
		agent.WorkItemDone,
		agent.WorkItemFailed,
		agent.WorkItemDead,
	} {
		status := status
		t.Run(string(status), func(t *testing.T) {
			fixture := newJobControlFixture(t, jobTestNow)
			item := fixture.repo.add(jobControlItem(601, 10, status, 2, 3))

			_, err := fixture.service.Cancel(context.Background(), 71, item.VersionID, item.ID)
			assertAPIError(t, err, http.StatusConflict, "state_conflict")
			if fixture.repo.cancelCalls != 0 {
				t.Fatalf("cancel calls = %d, want 0 for status %s", fixture.repo.cancelCalls, status)
			}
			if len(fixture.auditEvents()) != 0 || len(fixture.publishedEvents()) != 0 {
				t.Fatalf("rejected cancel wrote audit/events")
			}
			if stored := fixture.repo.get(item.ID); stored.Status != status {
				t.Fatalf("stored status = %q, want %q", stored.Status, status)
			}
		})
	}
}

// ---------- retry ----------

func TestJobRetryFailedOnly(t *testing.T) {
	t.Run("requeues a failed job below the cap", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		before := fixture.repo.add(jobControlItem(502, 10, agent.WorkItemFailed, 2, 3))

		view, err := fixture.service.Retry(context.Background(), 71, before.VersionID, before.ID)
		if err != nil {
			t.Fatalf("Retry: %v", err)
		}
		if view.State != string(agent.WorkItemOpen) {
			t.Fatalf("view state = %q, want open", view.State)
		}
		if view.JobID != before.ID || view.VersionID != before.VersionID || view.Kind != before.Kind {
			t.Fatalf("view = %#v", view)
		}
		if view.Attempts != 0 || view.MaxAttempts != before.MaxAttempts {
			t.Fatalf("view attempts = %d/%d, want a fresh budget 0/%d", view.Attempts, view.MaxAttempts, before.MaxAttempts)
		}
		if !view.UpdatedAt.Equal(jobTestNow) {
			t.Fatalf("view updated_at = %s, want %s", view.UpdatedAt, jobTestNow)
		}

		stored := fixture.repo.get(before.ID)
		if stored.Status != agent.WorkItemOpen {
			t.Fatalf("stored status = %q, want open", stored.Status)
		}
		if stored.ClaimedBy != nil || stored.LeaseUntil != nil {
			t.Fatalf("lease was not cleared: claimed_by=%v lease_until=%v", stored.ClaimedBy, stored.LeaseUntil)
		}
		if stored.LastError != nil {
			t.Fatalf("stored last_error = %v, want nil", stored.LastError)
		}
		if !stored.AvailableAt.Equal(jobTestNow) || !stored.UpdatedAt.Equal(jobTestNow) {
			t.Fatalf("stored timestamps = available %s updated %s, want %s", stored.AvailableAt, stored.UpdatedAt, jobTestNow)
		}
		// Task 46 fix round 1: a user-initiated retry resets attempts to 0 (a
		// fresh budget) while keeping max_attempts. The automatic dispatcher
		// retry consumes attempts; this explicit user action takes precedence.
		if stored.Attempts != 0 {
			t.Fatalf("stored attempts = %d, want 0 (user retry resets to a fresh budget)", stored.Attempts)
		}
		if stored.MaxAttempts != before.MaxAttempts {
			t.Fatalf("stored max_attempts = %d, want %d preserved", stored.MaxAttempts, before.MaxAttempts)
		}

		// No automatic model switch: the retry only touches lifecycle columns,
		// so every contract payload is byte-identical.
		for _, field := range []struct {
			name   string
			before json.RawMessage
			after  json.RawMessage
		}{
			{"target", before.Target, stored.Target},
			{"inputs", before.Inputs, stored.Inputs},
			{"expected", before.Expected, stored.Expected},
			{"budget", before.Budget, stored.Budget},
			{"payload", before.Payload, stored.Payload},
		} {
			if string(field.before) != string(field.after) {
				t.Fatalf("retry rewrote %s (%s -> %s): no automatic model switch", field.name, field.before, field.after)
			}
		}

		if fixture.repo.retryCalls != 1 {
			t.Fatalf("retry calls = %d, want 1", fixture.repo.retryCalls)
		}
		call := fixture.repo.lastRetry
		if call.VersionID != before.VersionID || call.ID != before.ID || !call.Now.Equal(jobTestNow) {
			t.Fatalf("retry call = %#v", call)
		}

		audits := fixture.auditEvents()
		if len(audits) != 1 {
			t.Fatalf("audit events = %d, want 1: %#v", len(audits), audits)
		}
		audit := audits[0]
		if audit.Action != "job.requeued" || audit.TargetKind != "work_item" || audit.TargetID != "502" {
			t.Fatalf("audit = %#v", audit)
		}
		if audit.ActorID == nil || *audit.ActorID != 71 {
			t.Fatalf("audit actor = %v, want 71", audit.ActorID)
		}
		if audit.Metadata["job_id"] != "502" || audit.Metadata["kind"] != before.Kind ||
			audit.Metadata["from_status"] != string(agent.WorkItemFailed) {
			t.Fatalf("audit metadata = %#v", audit.Metadata)
		}
		if got, ok := audit.Metadata["attempts"].(int); !ok || got != before.Attempts {
			t.Fatalf("audit attempts = %#v, want %d", audit.Metadata["attempts"], before.Attempts)
		}

		events := fixture.publishedEvents()
		if len(events) != 1 {
			t.Fatalf("events = %d, want 1: %#v", len(events), events)
		}
		event := events[0]
		if event.Type != "job.requeued" || event.Channel != "version:10" {
			t.Fatalf("event type/channel = %q/%q", event.Type, event.Channel)
		}
		if event.Scope["version_id"] != "10" || event.Scope["job_id"] != "502" {
			t.Fatalf("event scope = %#v", event.Scope)
		}
		if event.Payload["job_id"] != "502" || event.Payload["kind"] != before.Kind ||
			event.Payload["status"] != string(agent.WorkItemOpen) {
			t.Fatalf("event payload = %#v", event.Payload)
		}
		if event.ActorID == nil || *event.ActorID != 71 {
			t.Fatalf("event actor = %v, want 71", event.ActorID)
		}
	})

	t.Run("rejects a failed job at the attempt cap", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(503, 10, agent.WorkItemFailed, 3, 3))

		_, err := fixture.service.Retry(context.Background(), 71, item.VersionID, item.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
		if fixture.repo.retryCalls != 0 {
			t.Fatalf("retry calls = %d, want 0 at the attempt cap", fixture.repo.retryCalls)
		}
		if len(fixture.auditEvents()) != 0 || len(fixture.publishedEvents()) != 0 {
			t.Fatalf("rejected retry wrote audit/events")
		}
	})

	t.Run("rejects a canceled job", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := jobControlItem(504, 10, agent.WorkItemFailed, 1, 3)
		item.LastError = jobStringPtr(agentapp.JobCanceledReason)
		item = fixture.repo.add(item)

		_, err := fixture.service.Retry(context.Background(), 71, item.VersionID, item.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
		if fixture.repo.retryCalls != 0 {
			t.Fatalf("retry calls = %d, want 0 for a canceled job", fixture.repo.retryCalls)
		}
	})

	for _, status := range []agent.WorkItemStatus{
		agent.WorkItemOpen,
		agent.WorkItemClaimed,
		agent.WorkItemRunning,
		agent.WorkItemWaitingHuman,
		agent.WorkItemDone,
		agent.WorkItemDead,
	} {
		status := status
		t.Run("rejects "+string(status), func(t *testing.T) {
			fixture := newJobControlFixture(t, jobTestNow)
			item := fixture.repo.add(jobControlItem(505, 10, status, 0, 3))

			_, err := fixture.service.Retry(context.Background(), 71, item.VersionID, item.ID)
			assertAPIError(t, err, http.StatusConflict, "state_conflict")
			if fixture.repo.retryCalls != 0 {
				t.Fatalf("retry calls = %d, want 0 for status %s", fixture.repo.retryCalls, status)
			}
			if len(fixture.auditEvents()) != 0 || len(fixture.publishedEvents()) != 0 {
				t.Fatalf("rejected retry wrote audit/events")
			}
		})
	}
}

// TestJobRetryRejectsLegacyKind pins the Task 60 legacy fence. Pre-0.2.1
// work_item rows carry an empty kind (agent_0007_work_item021 defaults the column to
// an empty string) and are dispatched by the legacy Claim/ClaimForTeams/
// ClaimForTypes path, whose work_item_attempt numbering is coupled to the
// attempts budget counter. Re-opening such a row through the 0.2.1 control API
// would let the two attempt-numbering schemes collide, so both control actions
// must answer a typed not-found and never reach the repository write path.
func TestJobRetryRejectsLegacyKind(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		status   agent.WorkItemStatus
		attempts int
		call     func(*agentapp.JobControl, agent.WorkItem) error
	}{
		{"retry", agent.WorkItemFailed, 1, func(service *agentapp.JobControl, item agent.WorkItem) error {
			_, err := service.Retry(context.Background(), 71, item.VersionID, item.ID)
			return err
		}},
		{"cancel", agent.WorkItemOpen, 0, func(service *agentapp.JobControl, item agent.WorkItem) error {
			_, err := service.Cancel(context.Background(), 71, item.VersionID, item.ID)
			return err
		}},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newJobControlFixture(t, jobTestNow)
			legacy := jobControlItem(512, 10, testCase.status, testCase.attempts, 3)
			legacy.Kind = "" // pre-0.2.1 row: no 0.2.1 job kind
			legacy = fixture.repo.add(legacy)

			assertAPIError(t, testCase.call(fixture.service, legacy), http.StatusNotFound, "not_found")

			if fixture.repo.cancelCalls != 0 || fixture.repo.retryCalls != 0 {
				t.Fatalf("legacy row reached the write path: cancel=%d retry=%d",
					fixture.repo.cancelCalls, fixture.repo.retryCalls)
			}
			if len(fixture.auditEvents()) != 0 || len(fixture.publishedEvents()) != 0 {
				t.Fatalf("rejected legacy job action wrote audit/events")
			}
			stored := fixture.repo.get(legacy.ID)
			if stored.Status != testCase.status || stored.Attempts != testCase.attempts {
				t.Fatalf("legacy row changed: %#v", stored)
			}
		})
	}
}

// ---------- ownership / typed errors ----------

func TestJobControlOwnership(t *testing.T) {
	t.Run("cross-version cancel is not found", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(506, 10, agent.WorkItemRunning, 1, 3))

		_, err := fixture.service.Cancel(context.Background(), 71, 11, item.ID)
		assertAPIError(t, err, http.StatusNotFound, "not_found")
		if fixture.repo.cancelCalls != 0 {
			t.Fatalf("cancel calls = %d, want 0 for a foreign version", fixture.repo.cancelCalls)
		}
		if len(fixture.auditEvents()) != 0 || len(fixture.publishedEvents()) != 0 {
			t.Fatalf("foreign cancel wrote audit/events")
		}
		if stored := fixture.repo.get(item.ID); stored.Status != agent.WorkItemRunning {
			t.Fatalf("foreign cancel changed the job: %#v", stored)
		}
	})

	t.Run("cross-version retry is not found", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(507, 10, agent.WorkItemFailed, 1, 3))

		_, err := fixture.service.Retry(context.Background(), 71, 11, item.ID)
		assertAPIError(t, err, http.StatusNotFound, "not_found")
		if fixture.repo.retryCalls != 0 {
			t.Fatalf("retry calls = %d, want 0 for a foreign version", fixture.repo.retryCalls)
		}
	})

	t.Run("a repository that ignores the version scope is still rejected", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		fixture.repo.ignoreVersion = true
		item := fixture.repo.add(jobControlItem(508, 10, agent.WorkItemRunning, 1, 3))

		_, err := fixture.service.Cancel(context.Background(), 71, 11, item.ID)
		assertAPIError(t, err, http.StatusNotFound, "not_found")
		if fixture.repo.cancelCalls != 0 {
			t.Fatalf("cancel calls = %d, want 0 (service must verify version ownership)", fixture.repo.cancelCalls)
		}
	})

	t.Run("missing job is not found", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		_, err := fixture.service.Cancel(context.Background(), 71, 10, 999)
		assertAPIError(t, err, http.StatusNotFound, "not_found")
		_, err = fixture.service.Retry(context.Background(), 71, 10, 999)
		assertAPIError(t, err, http.StatusNotFound, "not_found")
	})

	t.Run("invalid IDs are rejected before the repository", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		if _, err := fixture.service.Cancel(context.Background(), 71, 10, 0); err == nil {
			t.Fatal("Cancel accepted a zero job id")
		} else {
			assertAPIError(t, err, http.StatusBadRequest, "invalid_argument")
		}
		if _, err := fixture.service.Retry(context.Background(), 71, -1, 501); err == nil {
			t.Fatal("Retry accepted a negative version id")
		} else {
			assertAPIError(t, err, http.StatusBadRequest, "invalid_argument")
		}
	})

	t.Run("wrong-state cancel is a conflict", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(509, 10, agent.WorkItemDone, 1, 3))
		_, err := fixture.service.Cancel(context.Background(), 71, item.VersionID, item.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
	})

	t.Run("wrong-state retry is a conflict", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(510, 10, agent.WorkItemRunning, 1, 3))
		_, err := fixture.service.Retry(context.Background(), 71, item.VersionID, item.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
	})

	t.Run("retry at the attempt cap is a conflict", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := fixture.repo.add(jobControlItem(511, 10, agent.WorkItemFailed, 3, 3))
		_, err := fixture.service.Retry(context.Background(), 71, item.VersionID, item.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
	})

	t.Run("unconfigured service is unavailable", func(t *testing.T) {
		service := agentapp.NewJobControl(agentapp.JobControlDeps{})
		_, err := service.Cancel(context.Background(), 71, 10, 501)
		assertAPIError(t, err, http.StatusServiceUnavailable, "unavailable")
	})
}

// ---------- Task 63: cancel persists the conversation card ----------

// TestJobCardCancelUpdaterWiring pins the Task 63 wiring: a successful cancel
// names the job's conversation card (version/conversation/job) so the version
// context can rewrite the persisted card to cancelled. The live SSE job.finished
// and the reloaded card must agree. The card write is best effort: a card-store
// failure never rolls back the committed cancel or its events.
func TestJobCardCancelUpdaterWiring(t *testing.T) {
	t.Run("rewrites the canceled job's card", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := jobControlItem(520, 10, agent.WorkItemRunning, 1, 3)
		// Task 61 stores the conversation association in the frozen payload
		// column; the card lookup must use it.
		item.Payload = json.RawMessage(`{"conversation_id":"77","model":"deepseek-chat"}`)
		before := fixture.repo.add(item)

		view, err := fixture.service.Cancel(context.Background(), 71, before.VersionID, before.ID)
		if err != nil {
			t.Fatalf("Cancel: %v", err)
		}
		if view.State != agentapp.JobStateCancelled {
			t.Fatalf("view state = %q, want %q", view.State, agentapp.JobStateCancelled)
		}

		calls := fixture.jobCards.recorded()
		if len(calls) != 1 {
			t.Fatalf("card updates = %d, want 1: %#v", len(calls), calls)
		}
		want := jobCardUpdateCall{VersionID: before.VersionID, ConversationID: 77, JobID: before.ID}
		if calls[0] != want {
			t.Fatalf("card update = %#v, want %#v", calls[0], want)
		}

		// The cancel state and both contract events stay intact: the card
		// write-back is an after-the-fact projection, not part of the cancel.
		stored := fixture.repo.get(before.ID)
		if stored.Status != agent.WorkItemFailed || stored.LastError == nil ||
			*stored.LastError != agentapp.JobCanceledReason {
			t.Fatalf("stored canceled row = %#v", stored)
		}
		events := fixture.publishedEvents()
		if len(events) != 2 || events[0].Type != "job.canceled" ||
			events[1].Type != eventingapp.EventJobFinished {
			t.Fatalf("events = %#v, want job.canceled + job.finished", events)
		}
	})

	t.Run("a card failure does not roll back the cancel", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		fixture.jobCards.err = errors.New("conversation card store unavailable")
		item := jobControlItem(521, 10, agent.WorkItemRunning, 1, 3)
		item.Payload = json.RawMessage(`{"conversation_id":"78"}`)
		before := fixture.repo.add(item)

		view, err := fixture.service.Cancel(context.Background(), 71, before.VersionID, before.ID)
		if err != nil {
			t.Fatalf("Cancel with a failing card update: %v", err)
		}
		if view.State != agentapp.JobStateCancelled {
			t.Fatalf("view state = %q, want %q", view.State, agentapp.JobStateCancelled)
		}
		if stored := fixture.repo.get(before.ID); stored.Status != agent.WorkItemFailed {
			t.Fatalf("stored status = %q, want the canceled (failed) row", stored.Status)
		}
		if calls := fixture.jobCards.recorded(); len(calls) != 1 {
			t.Fatalf("card updates = %d, want the one attempted best-effort write", len(calls))
		}
	})
}

// TestJobCardCancelUpdaterNoCardNoop pins the no-card path: a 0.2.1 job that
// was never associated with a conversation never projected a card, so cancel
// must not call the card updater and must still succeed. A malformed frozen
// payload is treated the same way instead of failing the cancel.
func TestJobCardCancelUpdaterNoCardNoop(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		payload json.RawMessage
	}{
		{"no conversation association", json.RawMessage(`{"model":"deepseek-chat"}`)},
		{"empty payload", nil},
		{"malformed conversation id", json.RawMessage(`{"conversation_id":"not-a-number"}`)},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newJobControlFixture(t, jobTestNow)
			item := jobControlItem(522, 10, agent.WorkItemOpen, 0, 3)
			item.Payload = testCase.payload
			before := fixture.repo.add(item)

			view, err := fixture.service.Cancel(context.Background(), 71, before.VersionID, before.ID)
			if err != nil {
				t.Fatalf("Cancel: %v", err)
			}
			if view.State != agentapp.JobStateCancelled {
				t.Fatalf("view state = %q, want %q", view.State, agentapp.JobStateCancelled)
			}
			if calls := fixture.jobCards.recorded(); len(calls) != 0 {
				t.Fatalf("card updates = %#v, want no card update", calls)
			}
			if stored := fixture.repo.get(before.ID); stored.Status != agent.WorkItemFailed {
				t.Fatalf("stored status = %q, want the canceled (failed) row", stored.Status)
			}
		})
	}
}

// TestJobCardCancelUpdaterLegacyNoop pins the legacy fence: pre-0.2.1 work
// items (empty kind) never had a job card and remain not-found for the control
// API, so the card updater must never run for them.
func TestJobCardCancelUpdaterLegacyNoop(t *testing.T) {
	fixture := newJobControlFixture(t, jobTestNow)
	legacy := jobControlItem(523, 10, agent.WorkItemRunning, 1, 3)
	legacy.Kind = ""
	legacy.Payload = json.RawMessage(`{"conversation_id":"79"}`)
	legacy = fixture.repo.add(legacy)

	_, err := fixture.service.Cancel(context.Background(), 71, legacy.VersionID, legacy.ID)
	assertAPIError(t, err, http.StatusNotFound, "not_found")

	if fixture.repo.cancelCalls != 0 {
		t.Fatalf("cancel calls = %d, want 0 for a legacy row", fixture.repo.cancelCalls)
	}
	if calls := fixture.jobCards.recorded(); len(calls) != 0 {
		t.Fatalf("card updates = %#v, want none for a legacy row", calls)
	}
	if stored := fixture.repo.get(legacy.ID); stored.Status != agent.WorkItemRunning {
		t.Fatalf("legacy row changed: %#v", stored)
	}
}

// TestJobCardRetryReopenWiring pins the retry-side card wiring: after a
// successful retry reset the job control service best-effort reopens the
// persisted card of the retried job (same version/conversation/job), so a card
// that a cancel/race had left cancelled can be reused by the new attempt. Retry
// semantics are unchanged: one job.requeued event, no terminal event, and a card
// failure never rolls back the re-opened row. A canceled row is still rejected
// by Task 46 and never reopens anything.
func TestJobCardRetryReopenWiring(t *testing.T) {
	t.Run("reopens the retried job's card", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := jobControlItem(530, 10, agent.WorkItemFailed, 1, 3)
		item.Payload = json.RawMessage(`{"conversation_id":"77","model":"deepseek-chat"}`)
		before := fixture.repo.add(item)

		view, err := fixture.service.Retry(context.Background(), 71, before.VersionID, before.ID)
		if err != nil {
			t.Fatalf("Retry: %v", err)
		}
		if view.State != string(agent.WorkItemOpen) {
			t.Fatalf("view state = %q, want open", view.State)
		}

		calls := fixture.jobCards.reopened()
		if len(calls) != 1 {
			t.Fatalf("reopen calls = %d, want 1: %#v", len(calls), calls)
		}
		want := jobCardUpdateCall{VersionID: before.VersionID, ConversationID: 77, JobID: before.ID}
		if calls[0] != want {
			t.Fatalf("reopen call = %#v, want %#v", calls[0], want)
		}
		if cancelCalls := fixture.jobCards.recorded(); len(cancelCalls) != 0 {
			t.Fatalf("cancel card calls = %#v, want none on a retry", cancelCalls)
		}

		// Retry semantics are untouched: one requeue event, no terminal event.
		events := fixture.publishedEvents()
		if len(events) != 1 || events[0].Type != "job.requeued" {
			t.Fatalf("events = %#v, want exactly one job.requeued", events)
		}
		if stored := fixture.repo.get(before.ID); stored.Status != agent.WorkItemOpen || stored.Attempts != 0 {
			t.Fatalf("stored retried row = %#v", stored)
		}
	})

	t.Run("a card failure does not roll back the retry", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		fixture.jobCards.err = errors.New("conversation card store unavailable")
		item := jobControlItem(531, 10, agent.WorkItemFailed, 1, 3)
		item.Payload = json.RawMessage(`{"conversation_id":"78"}`)
		before := fixture.repo.add(item)

		if _, err := fixture.service.Retry(context.Background(), 71, before.VersionID, before.ID); err != nil {
			t.Fatalf("Retry with a failing card update: %v", err)
		}
		if calls := fixture.jobCards.reopened(); len(calls) != 1 {
			t.Fatalf("reopen calls = %d, want the one attempted best-effort write", len(calls))
		}
		if stored := fixture.repo.get(before.ID); stored.Status != agent.WorkItemOpen {
			t.Fatalf("stored status = %q, want the re-opened row", stored.Status)
		}
	})

	t.Run("no conversation association is a no-op", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := jobControlItem(532, 10, agent.WorkItemFailed, 1, 3)
		before := fixture.repo.add(item)

		if _, err := fixture.service.Retry(context.Background(), 71, before.VersionID, before.ID); err != nil {
			t.Fatalf("Retry: %v", err)
		}
		if calls := fixture.jobCards.reopened(); len(calls) != 0 {
			t.Fatalf("reopen calls = %#v, want none without a conversation association", calls)
		}
	})

	t.Run("a canceled job is rejected without reopening its card", func(t *testing.T) {
		fixture := newJobControlFixture(t, jobTestNow)
		item := jobControlItem(533, 10, agent.WorkItemFailed, 1, 3)
		item.LastError = jobStringPtr(agentapp.JobCanceledReason)
		item.Payload = json.RawMessage(`{"conversation_id":"79"}`)
		before := fixture.repo.add(item)

		_, err := fixture.service.Retry(context.Background(), 71, before.VersionID, before.ID)
		assertAPIError(t, err, http.StatusConflict, "state_conflict")
		if calls := fixture.jobCards.reopened(); len(calls) != 0 {
			t.Fatalf("reopen calls = %#v, want none for a canceled row", calls)
		}
	})
}
