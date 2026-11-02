package agentapp_test

import (
	"context"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// The human-takeover additions of the work queue (P3 §6.8). The fake repo
// methods live here so the existing P1 workqueue tests remain untouched.

func (r *fakeWorkItemRepo) MarkWaitingHuman(_ context.Context, id, epoch int64) error {
	r.waitingHuman = append(r.waitingHuman, [2]int64{id, epoch})
	return r.waitingHumanErr
}

func (r *fakeWorkItemRepo) Finish(_ context.Context, id int64, reason string) error {
	r.finished = append(r.finished, finishCall{id: id, reason: reason})
	return r.finishErr
}

// fakeWorkerQueue (worker_test.go) must satisfy the extended WorkerQueue port.
func (q *fakeWorkerQueue) MarkWaitingHuman(context.Context, int64, int64) error { return nil }
func (q *fakeWorkerQueue) Finish(context.Context, int64, string) error          { return nil }

type finishCall struct {
	id     int64
	reason string
}

func TestWorkQueueMarkWaitingHuman(t *testing.T) {
	repo := &fakeWorkItemRepo{}
	queue := newWorkQueue(repo)
	if err := queue.MarkWaitingHuman(context.Background(), 77, 3); err != nil {
		t.Fatalf("MarkWaitingHuman: %v", err)
	}
	if len(repo.waitingHuman) != 1 || repo.waitingHuman[0] != [2]int64{77, 3} {
		t.Fatalf("waiting human calls = %v", repo.waitingHuman)
	}
}

func TestWorkQueueMarkWaitingHumanLeaseLost(t *testing.T) {
	repo := &fakeWorkItemRepo{waitingHumanErr: agent.ErrLeaseLost}
	queue := newWorkQueue(repo)
	err := queue.MarkWaitingHuman(context.Background(), 77, 3)
	if !agentapp.IsLeaseLost(err) {
		t.Fatalf("error = %v, want lease_lost", err)
	}
}

func TestWorkQueueFinishWaitingHumanItem(t *testing.T) {
	repo := &fakeWorkItemRepo{}
	queue := newWorkQueue(repo)
	if err := queue.Finish(context.Background(), 77, "waiting_human_resumed"); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(repo.finished) != 1 || repo.finished[0].id != 77 || repo.finished[0].reason != "waiting_human_resumed" {
		t.Fatalf("finish calls = %+v", repo.finished)
	}
}
