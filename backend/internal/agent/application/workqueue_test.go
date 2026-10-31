package agentapp_test

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

type fakeClock struct{ now time.Time }

func (c *fakeClock) Now() time.Time { return c.now }

type failCall struct {
	id        int64
	epoch     int64
	message   string
	retryable bool
	backoff   time.Duration
}

type fakeWorkItemRepo struct {
	item      agent.WorkItem
	created   bool
	claimOK   bool
	recovered int64
	items     []agent.WorkItem

	ensureErr   error
	claimErr    error
	getErr      error
	markErr     error
	renewErr    error
	completeErr error
	failErr     error
	recoverErr  error
	listErr     error

	ensured       []agent.NewWorkItem
	claimants     []string
	claimLease    []time.Duration
	marked        [][2]int64
	renewed       [][2]int64
	renewLease    []time.Duration
	completed     [][2]int64
	failed        []failCall
	listVersionID int64
	listLimit     int
}

func (r *fakeWorkItemRepo) Ensure(_ context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error) {
	r.ensured = append(r.ensured, in)
	return r.item, r.created, r.ensureErr
}

func (r *fakeWorkItemRepo) Claim(_ context.Context, claimant string, lease time.Duration) (agent.WorkItem, bool, error) {
	r.claimants = append(r.claimants, claimant)
	r.claimLease = append(r.claimLease, lease)
	return r.item, r.claimOK, r.claimErr
}

func (r *fakeWorkItemRepo) Get(_ context.Context, _ int64) (agent.WorkItem, error) {
	return r.item, r.getErr
}

func (r *fakeWorkItemRepo) MarkRunning(_ context.Context, id, epoch int64) error {
	r.marked = append(r.marked, [2]int64{id, epoch})
	return r.markErr
}

func (r *fakeWorkItemRepo) Renew(_ context.Context, id, epoch int64, lease time.Duration) error {
	r.renewed = append(r.renewed, [2]int64{id, epoch})
	r.renewLease = append(r.renewLease, lease)
	return r.renewErr
}

func (r *fakeWorkItemRepo) Complete(_ context.Context, id, epoch int64) error {
	r.completed = append(r.completed, [2]int64{id, epoch})
	return r.completeErr
}

func (r *fakeWorkItemRepo) Fail(_ context.Context, id, epoch int64, message string, retryable bool, backoff time.Duration) error {
	r.failed = append(r.failed, failCall{id: id, epoch: epoch, message: message, retryable: retryable, backoff: backoff})
	return r.failErr
}

func (r *fakeWorkItemRepo) RecoverExpired(context.Context) (int64, error) {
	return r.recovered, r.recoverErr
}

func (r *fakeWorkItemRepo) ListByVersion(_ context.Context, versionID int64, limit int) ([]agent.WorkItem, error) {
	r.listVersionID = versionID
	r.listLimit = limit
	return r.items, r.listErr
}

func (r *fakeWorkItemRepo) ListAttempts(context.Context, int64) ([]agent.WorkItemAttempt, error) {
	return nil, nil
}

var _ agent.WorkItemRepository = (*fakeWorkItemRepo)(nil)

func newWorkQueue(repo *fakeWorkItemRepo) *agentapp.WorkQueue {
	return agentapp.NewWorkQueue(repo, &fakeClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		agentapp.DefaultLease, agentapp.DefaultMaxAttempts)
}

func assertAPIError(t *testing.T, err error, status int, code string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s", code)
	}
	apiErr := platform.AsError(err)
	if apiErr.Status != status || apiErr.Code != code {
		t.Fatalf("error = %#v, want status %d code %s", apiErr, status, code)
	}
}

func TestWorkQueueEnsurePublishesCreatedOnlyWhenNew(t *testing.T) {
	repo := &fakeWorkItemRepo{
		item: agent.WorkItem{
			ID: 77, VersionID: 10, Team: agent.TeamClarification,
			Type: "chat.turn", DedupeKey: "opening:1",
		},
		created: true,
	}
	publisher := &fakePublisher{}
	queue := newWorkQueue(repo)
	queue.SetEvents(publisher)
	ctx := context.Background()

	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10, Team: "clarification", Type: "chat.turn", DedupeKey: "opening:1",
	}); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("events = %#v", publisher.events)
	}
	event := publisher.events[0]
	if event.Type != "work_item.created" || event.Channel != "version:10" {
		t.Fatalf("event = %#v", event)
	}
	if event.Scope["version_id"] != "10" {
		t.Fatalf("scope = %#v", event.Scope)
	}
	if event.Payload["work_item_id"] != "77" || event.Payload["type"] != "chat.turn" ||
		event.Payload["team"] != "clarification" || event.Payload["dedupe_key"] != "opening:1" {
		t.Fatalf("payload = %#v", event.Payload)
	}

	repo.created = false
	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10, Team: "clarification", Type: "chat.turn", DedupeKey: "opening:1",
	}); err != nil {
		t.Fatalf("Ensure duplicate: %v", err)
	}
	if len(publisher.events) != 1 {
		t.Fatalf("duplicate ensure published: %#v", publisher.events)
	}
}

func TestWorkQueueEnsureValidatesAndDefaults(t *testing.T) {
	repo := &fakeWorkItemRepo{item: agent.WorkItem{ID: 5}, created: true}
	queue := newWorkQueue(repo)
	ctx := context.Background()

	item, created, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10,
		Team:      "clarification",
		Type:      "chat.turn",
		DedupeKey: "opening:1",
	})
	if err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	if !created || item.ID != 5 {
		t.Fatalf("item=%#v created=%v", item, created)
	}
	if len(repo.ensured) != 1 || repo.ensured[0].MaxAttempts != agentapp.DefaultMaxAttempts {
		t.Fatalf("ensured = %#v", repo.ensured)
	}

	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: 10,
		Team:      "clarification",
		Type:      "",
		DedupeKey: "x",
	}); err == nil {
		t.Fatal("invalid input was accepted")
	}
	if len(repo.ensured) != 1 {
		t.Fatalf("repository called for invalid input: %#v", repo.ensured)
	}

	if _, _, err := queue.Ensure(ctx, agent.NewWorkItem{
		VersionID:   10,
		Team:        "clarification",
		Type:        "chat.turn",
		DedupeKey:   "explicit",
		MaxAttempts: 5,
	}); err != nil {
		t.Fatalf("Ensure explicit: %v", err)
	}
	if repo.ensured[1].MaxAttempts != 5 {
		t.Fatalf("explicit max attempts = %d", repo.ensured[1].MaxAttempts)
	}
}

func TestWorkQueueClaimTrimsClaimantAndUsesLease(t *testing.T) {
	repo := &fakeWorkItemRepo{item: agent.WorkItem{ID: 9}, claimOK: true}
	queue := newWorkQueue(repo)
	ctx := context.Background()

	item, ok, err := queue.Claim(ctx, "  worker-1  ")
	if err != nil || !ok || item.ID != 9 {
		t.Fatalf("Claim = %#v, %v, %v", item, ok, err)
	}
	if len(repo.claimants) != 1 || repo.claimants[0] != "worker-1" {
		t.Fatalf("claimants = %#v", repo.claimants)
	}
	if repo.claimLease[0] != agentapp.DefaultLease {
		t.Fatalf("lease = %v", repo.claimLease[0])
	}

	if _, _, err := queue.Claim(ctx, "   "); err == nil {
		t.Fatal("empty claimant accepted")
	} else {
		assertAPIError(t, err, 400, "invalid_argument")
	}
	if len(repo.claimants) != 1 {
		t.Fatalf("claim called with empty claimant: %#v", repo.claimants)
	}

	repo.claimErr = errors.New("boom")
	if _, _, err := queue.Claim(ctx, "worker-2"); err == nil {
		t.Fatal("claim error not propagated")
	}
}

func TestWorkQueueFencingErrorsMapToLeaseLost(t *testing.T) {
	ctx := context.Background()

	t.Run("mark running", func(t *testing.T) {
		repo := &fakeWorkItemRepo{markErr: agent.ErrLeaseLost}
		queue := newWorkQueue(repo)
		assertAPIError(t, queue.MarkRunning(ctx, 7, 2), 409, "lease_lost")
		if len(repo.marked) != 1 || repo.marked[0] != [2]int64{7, 2} {
			t.Fatalf("marked = %#v", repo.marked)
		}
	})
	t.Run("heartbeat", func(t *testing.T) {
		repo := &fakeWorkItemRepo{renewErr: agent.ErrLeaseLost}
		queue := newWorkQueue(repo)
		assertAPIError(t, queue.Heartbeat(ctx, 7, 2), 409, "lease_lost")
		if len(repo.renewed) != 1 || repo.renewed[0] != [2]int64{7, 2} {
			t.Fatalf("renewed = %#v", repo.renewed)
		}
		if repo.renewLease[0] != agentapp.DefaultLease {
			t.Fatalf("renew lease = %v", repo.renewLease[0])
		}
	})
	t.Run("complete", func(t *testing.T) {
		repo := &fakeWorkItemRepo{completeErr: agent.ErrLeaseLost}
		queue := newWorkQueue(repo)
		assertAPIError(t, queue.Complete(ctx, 7, 2), 409, "lease_lost")
	})
	t.Run("unknown errors untouched", func(t *testing.T) {
		repo := &fakeWorkItemRepo{completeErr: errors.New("db down")}
		queue := newWorkQueue(repo)
		if err := queue.Complete(ctx, 7, 2); err == nil {
			t.Fatal("unknown error not propagated")
		} else if platform.AsError(err).Code == "lease_lost" {
			t.Fatalf("unknown error mapped to lease_lost: %v", err)
		}
	})
}

func TestWorkQueueFailComputesBackoff(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name      string
		attempts  int
		retryable bool
		want      time.Duration
	}{
		{"first retry", 1, true, 2 * time.Second},
		{"second retry", 2, true, 4 * time.Second},
		{"capped", 5, true, agentapp.MaxBackoff},
		{"huge attempts", 20, true, agentapp.MaxBackoff},
		{"not retryable", 1, false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := &fakeWorkItemRepo{item: agent.WorkItem{ID: 7, Attempts: tc.attempts, MaxAttempts: 3}}
			queue := newWorkQueue(repo)
			if err := queue.Fail(ctx, 7, 2, errors.New("model down"), tc.retryable); err != nil {
				t.Fatalf("Fail: %v", err)
			}
			if len(repo.failed) != 1 {
				t.Fatalf("failed calls = %#v", repo.failed)
			}
			call := repo.failed[0]
			if call.message != "model down" || call.retryable != tc.retryable || call.backoff != tc.want {
				t.Fatalf("fail call = %#v, want backoff %v", call, tc.want)
			}
		})
	}

	t.Run("nil cause", func(t *testing.T) {
		repo := &fakeWorkItemRepo{item: agent.WorkItem{ID: 7, Attempts: 1}}
		queue := newWorkQueue(repo)
		if err := queue.Fail(ctx, 7, 2, nil, true); err != nil {
			t.Fatalf("Fail: %v", err)
		}
		if repo.failed[0].message != "" {
			t.Fatalf("message = %q", repo.failed[0].message)
		}
	})

	t.Run("lease lost", func(t *testing.T) {
		repo := &fakeWorkItemRepo{item: agent.WorkItem{ID: 7, Attempts: 1}, failErr: agent.ErrLeaseLost}
		queue := newWorkQueue(repo)
		assertAPIError(t, queue.Fail(ctx, 7, 2, errors.New("x"), true), 409, "lease_lost")
	})

	t.Run("get error", func(t *testing.T) {
		repo := &fakeWorkItemRepo{getErr: agent.ErrNotFound}
		queue := newWorkQueue(repo)
		if err := queue.Fail(ctx, 7, 2, errors.New("x"), true); !errors.Is(err, agent.ErrNotFound) {
			t.Fatalf("Fail = %v, want get error", err)
		}
		if len(repo.failed) != 0 {
			t.Fatalf("Fail called after Get error: %#v", repo.failed)
		}
	})
}

func TestWorkQueueRecoverExpiredAndList(t *testing.T) {
	ctx := context.Background()
	repo := &fakeWorkItemRepo{
		recovered: 4,
		items:     []agent.WorkItem{{ID: 1}, {ID: 2}},
	}
	queue := newWorkQueue(repo)

	count, err := queue.RecoverExpired(ctx)
	if err != nil || count != 4 {
		t.Fatalf("RecoverExpired = %d, %v", count, err)
	}

	items, err := queue.ListByVersion(ctx, 42, 0)
	if err != nil || len(items) != 2 {
		t.Fatalf("ListByVersion = %#v, %v", items, err)
	}
	if repo.listVersionID != 42 || repo.listLimit != agentapp.DefaultListLimit {
		t.Fatalf("list args = %d/%d", repo.listVersionID, repo.listLimit)
	}
	if _, err := queue.ListByVersion(ctx, 42, 1000); err != nil {
		t.Fatalf("ListByVersion capped: %v", err)
	}
	if repo.listLimit != agentapp.MaxListLimit {
		t.Fatalf("capped limit = %d", repo.listLimit)
	}

	repo.recoverErr = errors.New("recover failed")
	if _, err := queue.RecoverExpired(ctx); err == nil {
		t.Fatal("RecoverExpired error not propagated")
	}
}
