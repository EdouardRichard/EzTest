package agentapp_test

// Task 11 (v0.22 R-A3-4/R-A8-3): the completion service turns a server-verified
// task.complete declaration into one atomic write set: plan item status/decision
// plus a progress fact. The production adapter is Task 36 wiring; these tests
// pin the write set with an in-memory store fake.

import (
	"context"
	"errors"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

type fakeCompletionStore struct {
	writes []agentapp.CompletionWrite
	result agentapp.CompletionWriteResult
	err    error
}

func (f *fakeCompletionStore) CommitCompletion(_ context.Context, write agentapp.CompletionWrite) (agentapp.CompletionWriteResult, error) {
	f.writes = append(f.writes, write)
	if f.err != nil {
		return agentapp.CompletionWriteResult{}, f.err
	}
	return f.result, nil
}

func TestCompletionServiceCommitsPlanAndProgressFact(t *testing.T) {
	store := &fakeCompletionStore{result: agentapp.CompletionWriteResult{FactID: 77, PlanItemRevision: 3}}
	service := agentapp.NewCompletionService(store)

	outcome, err := service.Commit(context.Background(), tools.CompletionCommit{
		Scope:      tools.Scope{VersionID: 7, AgentRunID: 42},
		Summary:    "58/58 extracted",
		Artifacts:  []string{"extraction:5"},
		Status:     "completed",
		PlanItemID: 9,
	})
	if err != nil {
		t.Fatalf("completion commit failed: %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("store writes = %d, want 1 atomic write", len(store.writes))
	}
	write := store.writes[0]
	if write.VersionID != 7 || write.AgentRunID != 42 || write.PlanItemID != 9 {
		t.Fatalf("write scope = version=%d run=%d item=%d", write.VersionID, write.AgentRunID, write.PlanItemID)
	}
	if write.PlanStatus != agentapp.CompletionPlanStatusDone {
		t.Fatalf("plan status = %q, want %q", write.PlanStatus, agentapp.CompletionPlanStatusDone)
	}
	if write.Decision != "58/58 extracted" {
		t.Fatalf("plan decision = %q, want the completion summary", write.Decision)
	}
	if write.Status != "completed" || write.Summary != "58/58 extracted" {
		t.Fatalf("write summary/status = %q/%q", write.Summary, write.Status)
	}
	if write.FactKind != agentapp.CompletionFactKindProgress || strings.TrimSpace(write.FactKey) == "" {
		t.Fatalf("progress fact = kind=%q key=%q", write.FactKind, write.FactKey)
	}
	// Deterministic per-run idempotency key. version_fact is unique on
	// (version_id, kind, key, content_hash), so identical replays match the
	// existing row while changed content appends a new fact (facts never
	// overwrite). The key alone is therefore not the dedupe condition.
	if write.FactKey != "task.complete:run:42" {
		t.Fatalf("fact key = %q, want task.complete:run:42", write.FactKey)
	}
	if got := write.FactPayload["summary"]; got != "58/58 extracted" {
		t.Fatalf("fact payload summary = %#v", got)
	}
	if got := write.FactPayload["status"]; got != "completed" {
		t.Fatalf("fact payload status = %#v", got)
	}
	if got := write.FactPayload["plan_item_id"]; got != "9" {
		t.Fatalf("fact payload plan_item_id = %#v, want 9", got)
	}
	artifacts, ok := write.FactPayload["artifacts"].([]string)
	if !ok || len(artifacts) != 1 || artifacts[0] != "extraction:5" {
		t.Fatalf("fact payload artifacts = %#v", write.FactPayload["artifacts"])
	}
	if outcome.PlanItemID != 9 || outcome.PlanStatus != agentapp.CompletionPlanStatusDone {
		t.Fatalf("outcome plan = %+v", outcome)
	}
	if outcome.Decision != "58/58 extracted" || outcome.FactID != 77 {
		t.Fatalf("outcome = %+v", outcome)
	}
	if outcome.FactKey != "task.complete:run:42" {
		t.Fatalf("outcome fact key = %q, want task.complete:run:42", outcome.FactKey)
	}
	if outcome.PlanItemRevision != 3 {
		t.Fatalf("outcome plan revision = %d, want the store revision 3", outcome.PlanItemRevision)
	}
	t.Logf("completion write: plan_item=%d plan_status=%q decision=%q revision=%d fact=%s/%s summary=%q artifacts=%v",
		write.PlanItemID, write.PlanStatus, write.Decision, outcome.PlanItemRevision, write.FactKind, write.FactKey, write.Summary, write.Artifacts)
}

// TestCompletionServiceRejectsFactOnlyCommit pins the removed fact-only write
// path: every completion writes plan status/decision plus the progress fact for
// a positive target plan item, so a zero target is an internal error and never
// reaches the store.
func TestCompletionServiceRejectsFactOnlyCommit(t *testing.T) {
	store := &fakeCompletionStore{}
	service := agentapp.NewCompletionService(store)

	_, err := service.Commit(context.Background(), tools.CompletionCommit{
		Scope:     tools.Scope{VersionID: 7},
		Summary:   "requirements drafted",
		Artifacts: []string{"draft:5"},
		Status:    "completed",
	})
	if err == nil {
		t.Fatal("fact-only completion (plan item 0) was accepted")
	}
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		t.Fatalf("fact-only completion must be an internal (non-retryable) error, got %+v", toolErr)
	}
	if len(store.writes) != 0 {
		t.Fatalf("store writes on fact-only completion = %d, want 0", len(store.writes))
	}
}

func TestCompletionServicePropagatesStoreFailure(t *testing.T) {
	store := &fakeCompletionStore{err: errors.New("store down")}
	service := agentapp.NewCompletionService(store)

	_, err := service.Commit(context.Background(), tools.CompletionCommit{
		Scope:      tools.Scope{VersionID: 7},
		Summary:    "done",
		Status:     "completed",
		PlanItemID: 9,
	})
	if err == nil || !strings.Contains(err.Error(), "store down") {
		t.Fatalf("error = %v, want the store failure", err)
	}
	assertNoPartialWrite(t, store)
}

// assertNoPartialWrite documents the atomic/outbox contract: the service itself
// never decomposes the write set, so a store failure cannot leave a half write.
func assertNoPartialWrite(t *testing.T, store *fakeCompletionStore) {
	t.Helper()
	if len(store.writes) != 1 {
		t.Fatalf("store calls = %d, want exactly one attempted write set", len(store.writes))
	}
}
