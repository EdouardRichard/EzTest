package application

// This file pins the baseline half of D49/spec §6.8: a baseline batch pins the
// version's fixed-branch head at creation time, and a version without usable
// code history still creates the batch with head_commit=NULL instead of failing
// (the Task 17d wiring supplies the production CodeHistoryPort).

import (
	"context"
	"errors"
	"testing"
)

// TestCreateBaselinePinsCurrentHead proves a baseline batch records the head
// returned by the wired CodeHistoryPort. Before Task 17d this field was always
// nil because the composition root registered no port, which in turn left the
// autonomy worker without an analysable base.
func TestCreateBaselinePinsCurrentHead(t *testing.T) {
	service, _ := newBatchServiceTest(t)
	service.deps.CodeHistory = &fakeCodeHistory{head: testHead}

	batch := createBatch(t, service, CreateBatchCommand{})

	if batch.HeadCommit == nil || *batch.HeadCommit != testHead {
		t.Fatalf("head = %v, want the pinned current head %q", batch.HeadCommit, testHead)
	}
}

// TestCreateBaselineDegradesWhenHeadUnavailable proves the no-code-repository
// degradation: a HeadCommit failure logs and leaves head_commit NULL while the
// batch is still created (spec §6.1 "无代码仓" and the optional-port contract).
func TestCreateBaselineDegradesWhenHeadUnavailable(t *testing.T) {
	service, _ := newBatchServiceTest(t)
	service.deps.CodeHistory = &fakeCodeHistory{headErr: errors.New("version has no code repository binding")}

	batch, err := service.Create(context.Background(), 7, 1, CreateBatchCommand{})
	if err != nil {
		t.Fatalf("Create must degrade, not fail: %v", err)
	}
	if batch.HeadCommit != nil {
		t.Fatalf("head = %v, want nil when code history is unavailable", batch.HeadCommit)
	}
}
