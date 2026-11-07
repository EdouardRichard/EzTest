package agentapp_test

// Task 11 (v0.22 R-A3-4/R-A8-3): the completion service turns a server-verified
// task.complete declaration into one atomic write set: plan item status/decision
// plus a progress fact. The production adapter is Task 36 wiring; these tests
// pin the write set with an in-memory store fake.

import (
	"context"
	"errors"
	"reflect"
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

// TestSettlementServiceWritesInProgressPlanAndProgressFact pins the A-14
// writeback set: the plan item is NOT marked done (the work is incomplete), the
// decision names the budget that ended the run, and the fact is the existing
// progress kind with a run-bound idempotency key.
func TestSettlementServiceWritesInProgressPlanAndProgressFact(t *testing.T) {
	store := &fakeCompletionStore{result: agentapp.CompletionWriteResult{FactID: 88, PlanItemRevision: 5}}
	service := agentapp.NewSettlementService(store, 9)

	result, err := service.RecordSettlement(context.Background(), agentapp.Settlement{
		VersionID:  7,
		AgentRunID: 42,
		Reason:     agentapp.BudgetCauseTurns,
	})
	if err != nil {
		t.Fatalf("RecordSettlement: %v", err)
	}
	if len(store.writes) != 1 {
		t.Fatalf("store writes = %d, want one atomic write set", len(store.writes))
	}
	write := store.writes[0]
	if write.VersionID != 7 || write.AgentRunID != 42 || write.PlanItemID != 9 {
		t.Fatalf("write scope = version=%d run=%d item=%d", write.VersionID, write.AgentRunID, write.PlanItemID)
	}
	if write.PlanStatus != agentapp.SettlementPlanStatus || write.PlanStatus == agentapp.CompletionPlanStatusDone {
		t.Fatalf("plan status = %q, want the non-success settlement status", write.PlanStatus)
	}
	if write.Decision != "settled by turn budget" {
		t.Fatalf("decision = %q", write.Decision)
	}
	if write.FactKind != agentapp.CompletionFactKindProgress {
		t.Fatalf("fact kind = %q, want the existing progress kind", write.FactKind)
	}
	if write.FactKey != "run.settle:42" || write.Source != agentapp.SettlementFactSource {
		t.Fatalf("fact identity = key=%q source=%q", write.FactKey, write.Source)
	}
	if write.FactPayload["run_id"] != "42" || write.FactPayload["reason"] != agentapp.BudgetCauseTurns ||
		write.FactPayload["plan_item_id"] != "9" || write.FactPayload["status"] != agentapp.SettlementPlanStatus {
		t.Fatalf("fact payload = %#v", write.FactPayload)
	}
	if result.PlanItemID != 9 || result.PlanStatus != agentapp.SettlementPlanStatus ||
		result.Decision != "settled by turn budget" || result.FactID != 88 || result.PlanItemRevision != 5 {
		t.Fatalf("result = %+v", result)
	}
}

// TestSettlementServiceReplayIsIdempotent pins the outbox/replay contract: the
// same run/cause always builds a byte-identical write set, so the version_fact
// unique key (version_id, kind, key, content_hash) matches the stored row and a
// retried writeback does not append a duplicate fact.
func TestSettlementServiceReplayIsIdempotent(t *testing.T) {
	store := &fakeCompletionStore{result: agentapp.CompletionWriteResult{FactID: 77, PlanItemRevision: 3}}
	service := agentapp.NewSettlementService(store, 9)
	settlement := agentapp.Settlement{VersionID: 7, AgentRunID: 42, Reason: agentapp.BudgetCauseTime}

	if _, err := service.RecordSettlement(context.Background(), settlement); err != nil {
		t.Fatalf("first RecordSettlement: %v", err)
	}
	if _, err := service.RecordSettlement(context.Background(), settlement); err != nil {
		t.Fatalf("replayed RecordSettlement: %v", err)
	}
	if len(store.writes) != 2 {
		t.Fatalf("store writes = %d, want the replay attempted", len(store.writes))
	}
	first, second := store.writes[0], store.writes[1]
	if first.FactKey != second.FactKey || first.FactKind != second.FactKind ||
		first.PlanStatus != second.PlanStatus || first.Decision != second.Decision {
		t.Fatalf("replay write set differs: first=%+v second=%+v", first, second)
	}
	if !reflect.DeepEqual(first.FactPayload, second.FactPayload) {
		t.Fatalf("replay fact payload differs: %#v vs %#v", first.FactPayload, second.FactPayload)
	}
}

// TestSettlementServiceRejectsInvalidWrites pins that a settlement can never be
// written without the server-resolved scope: no target, version, run or a
// non-budget cause is rejected before the store is touched.
func TestSettlementServiceRejectsInvalidWrites(t *testing.T) {
	cases := []struct {
		name       string
		planItemID int64
		settlement agentapp.Settlement
	}{
		{"no target", 0, agentapp.Settlement{VersionID: 7, AgentRunID: 42, Reason: agentapp.BudgetCauseTurns}},
		{"no version", 9, agentapp.Settlement{AgentRunID: 42, Reason: agentapp.BudgetCauseTurns}},
		{"no run", 9, agentapp.Settlement{VersionID: 7, Reason: agentapp.BudgetCauseTurns}},
		{"non-budget cause", 9, agentapp.Settlement{VersionID: 7, AgentRunID: 42, Reason: agentapp.BudgetCauseTokens}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeCompletionStore{}
			service := agentapp.NewSettlementService(store, tc.planItemID)
			if _, err := service.RecordSettlement(context.Background(), tc.settlement); err == nil {
				t.Fatal("invalid settlement write was accepted")
			}
			if len(store.writes) != 0 {
				t.Fatalf("store writes = %d, want none", len(store.writes))
			}
		})
	}
}

// assertNoPartialWrite documents the atomic/outbox contract: the service itself
// never decomposes the write set, so a store failure cannot leave a half write.
func assertNoPartialWrite(t *testing.T, store *fakeCompletionStore) {
	t.Helper()
	if len(store.writes) != 1 {
		t.Fatalf("store calls = %d, want exactly one attempted write set", len(store.writes))
	}
}
