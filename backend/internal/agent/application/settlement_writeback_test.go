package agentapp_test

// v0.22 W0 A-14 settlement writeback (RED-first): a run that ends on the
// turn/time settlement turn without a verified completion must write the
// resolved plan item status/decision and a progress fact back to the version,
// so the next run's briefing reflects the real state (R-A8-3, A §6.12 rule 3).
// A verified completion still wins and is never written twice.

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// settlementStoreFake is the in-memory CompletionStore the settlement writeback
// path commits through. It records the full CompletionWrite so the test can
// assert the plan item and fact payload that would reach PostgreSQL.
type settlementStoreFake struct {
	mu     sync.Mutex
	writes []agentapp.CompletionWrite
	err    error
}

func (f *settlementStoreFake) CommitCompletion(_ context.Context, write agentapp.CompletionWrite) (agentapp.CompletionWriteResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return agentapp.CompletionWriteResult{}, f.err
	}
	f.writes = append(f.writes, write)
	return agentapp.CompletionWriteResult{FactID: 7001, PlanItemRevision: 4}, nil
}

func (f *settlementStoreFake) recorded() []agentapp.CompletionWrite {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]agentapp.CompletionWrite(nil), f.writes...)
}

// newSettlementFixture wires the runner with a settlement builder bound to the
// given store and target plan item, exactly like the composition root binds the
// resolver output.
func newSettlementFixture(t *testing.T, store agentapp.CompletionStore, planItemID int64) *wiringFixture {
	t.Helper()
	return newWiringFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.Settlement = func(context.Context, agentapp.CompletionContext) (agentapp.SettlementRecorder, error) {
			return agentapp.NewSettlementService(store, planItemID), nil
		}
	})
}

// forceTurnSettlement drives a two-turn run whose second turn is the tool-less
// settlement turn (the fuse is one below the loop cap).
func forceTurnSettlement(fixture *wiringFixture) {
	fixture.settings.ints["agent.max_turns"] = 2
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "回合上限已到，以下为收尾回复。"},
	}
}

func TestRunnerSettlementWritebackOnTurnBudget(t *testing.T) {
	store := &settlementStoreFake{}
	fixture := newSettlementFixture(t, store, 77)
	forceTurnSettlement(fixture)

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.SettlementCause != agentapp.BudgetCauseTurns {
		t.Fatalf("result = %#v, want a done run settled by the turn budget", result)
	}
	if result.TerminalReason != agentapp.BudgetCauseTurns {
		t.Fatalf("terminal reason = %q, want %q", result.TerminalReason, agentapp.BudgetCauseTurns)
	}

	writes := store.recorded()
	if len(writes) != 1 {
		t.Fatalf("settlement writes = %d, want exactly one", len(writes))
	}
	write := writes[0]
	if write.VersionID != 10 || write.AgentRunID != result.AgentRunID || write.PlanItemID != 77 {
		t.Fatalf("write scope = version=%d run=%d item=%d (want 10/%d/77)",
			write.VersionID, write.AgentRunID, write.PlanItemID, result.AgentRunID)
	}
	if write.PlanStatus != "in_progress" {
		t.Fatalf("plan status = %q, want in_progress", write.PlanStatus)
	}
	if write.Decision != "settled by turn budget" {
		t.Fatalf("decision = %q, want the documented settlement decision", write.Decision)
	}
	if write.FactKind != "progress" || write.FactKey == "" || write.Source == "" {
		t.Fatalf("fact = kind=%q key=%q source=%q", write.FactKind, write.FactKey, write.Source)
	}
	if wantKey := "run.settle:" + strconv.FormatInt(result.AgentRunID, 10); write.FactKey != wantKey {
		t.Fatalf("fact key = %q, want %q", write.FactKey, wantKey)
	}
	payload := write.FactPayload
	if payload["run_id"] != strconv.FormatInt(result.AgentRunID, 10) {
		t.Fatalf("payload run_id = %#v", payload["run_id"])
	}
	if payload["reason"] != agentapp.BudgetCauseTurns {
		t.Fatalf("payload reason = %#v, want %q", payload["reason"], agentapp.BudgetCauseTurns)
	}
	if payload["plan_item_id"] != "77" {
		t.Fatalf("payload plan_item_id = %#v, want 77", payload["plan_item_id"])
	}
	if payload["status"] != "in_progress" {
		t.Fatalf("payload status = %#v, want in_progress", payload["status"])
	}
}

// TestRunnerSettlementWritebackOnTimeBudget pins the wall-clock branch: the run
// ends on the settlement turn opened by the time budget, not the turn fuse.
func TestRunnerSettlementWritebackOnTimeBudget(t *testing.T) {
	store := &settlementStoreFake{}
	fixture := newSettlementFixture(t, store, 77)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		{Content: "时间预算已到，以下为收尾回复。"},
	}
	// Advance past the 1s wall-clock budget after the first model turn so the
	// next loop iteration decides BudgetSettle with cause=time_budget.
	fixture.chat.onChat = func(index int, _ llmapp.ChatRequest) {
		if index == 0 {
			fixture.clock.Advance(2 * time.Second)
		}
	}
	item := hostItem()
	item.Budget = json.RawMessage(`{"run_seconds":1,"max_turns_fuse":12}`)

	result, err := fixture.runner.Run(context.Background(), item)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.SettlementCause != agentapp.BudgetCauseTime {
		t.Fatalf("result = %#v, want a done run settled by the time budget", result)
	}
	writes := store.recorded()
	if len(writes) != 1 {
		t.Fatalf("settlement writes = %d, want exactly one", len(writes))
	}
	if writes[0].PlanStatus != "in_progress" {
		t.Fatalf("plan status = %q, want in_progress", writes[0].PlanStatus)
	}
	if writes[0].Decision != "settled by time budget" {
		t.Fatalf("decision = %q", writes[0].Decision)
	}
	if writes[0].FactPayload["reason"] != agentapp.BudgetCauseTime {
		t.Fatalf("payload reason = %#v", writes[0].FactPayload["reason"])
	}
}

// TestRunnerSettlementSkippedWhenCompletionWins pins that a verified completion
// is the terminal write: the completion writer runs once and the settlement
// path writes nothing (no double write of the plan item or a second fact).
func TestRunnerSettlementSkippedWhenCompletionWins(t *testing.T) {
	store := &settlementStoreFake{}
	fixture := newSettlementFixture(t, store, 77)
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolTaskComplete, `{"summary":"done","artifacts":["design:1"]}`),
	}

	result := runWiring(t, fixture, hostItem())
	if result.Status != agent.AgentRunDone || result.TerminalReason != "completed" {
		t.Fatalf("result = %#v, want the verified-completion terminal reason", result)
	}
	if fixture.writer.commits != 1 {
		t.Fatalf("completion commits = %d, want exactly one", fixture.writer.commits)
	}
	if writes := store.recorded(); len(writes) != 0 {
		t.Fatalf("settlement writes after a verified completion = %d, want none", len(writes))
	}
}

// TestRunnerSettlementResolutionFailureDoesNotFailRun pins the best-effort
// contract: an unresolvable plan item only logs a warning; the run verdict and
// the persisted run finish are unchanged.
func TestRunnerSettlementResolutionFailureDoesNotFailRun(t *testing.T) {
	fixture := newWiringFixture(t, func(deps *agentapp.RunnerDeps) {
		deps.Settlement = func(context.Context, agentapp.CompletionContext) (agentapp.SettlementRecorder, error) {
			return nil, errors.New("no plan item target")
		}
	})
	forceTurnSettlement(fixture)

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run with an unresolvable settlement target must not fail: %v", err)
	}
	if result.Status != agent.AgentRunDone || result.SettlementCause != agentapp.BudgetCauseTurns {
		t.Fatalf("result = %#v", result)
	}
}

// TestRunnerSettlementWritebackOnBudgetExhaustion pins the second terminal
// shape: the settlement turn itself consumed the last budget (a tool-call-only
// reply fails the run as budget), yet the plan item is still written back as
// incomplete so the next run does not see the stale pre-run state.
func TestRunnerSettlementWritebackOnBudgetExhaustion(t *testing.T) {
	store := &settlementStoreFake{}
	fixture := newSettlementFixture(t, store, 77)
	fixture.settings.ints["agent.max_turns"] = 2
	fixture.chat.responses = []llmapp.ChatResponse{
		toolCall(tools.ToolContextVersionPack, `{}`),
		// The frozen tool-less settlement turn got a tool call and no text.
		toolCall(tools.ToolContextTaskPack, `{}`),
	}

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err == nil {
		t.Fatal("expected a budget exhaustion failure")
	}
	if result.Status != agent.AgentRunFailed || result.TerminalReason != "budget" {
		t.Fatalf("result = %#v, want the budget failure", result)
	}
	writes := store.recorded()
	if len(writes) != 1 {
		t.Fatalf("settlement writes after budget exhaustion = %d, want exactly one", len(writes))
	}
	if writes[0].PlanStatus != "in_progress" || writes[0].Decision != "settled by turn budget" {
		t.Fatalf("settlement write = %#v", writes[0])
	}
}

// TestRunnerSettlementStoreFailureDoesNotFailRun pins the same best-effort
// contract for a store error: the plan writeback is lost, never the run.
func TestRunnerSettlementStoreFailureDoesNotFailRun(t *testing.T) {
	store := &settlementStoreFake{err: errors.New("database down")}
	fixture := newSettlementFixture(t, store, 77)
	forceTurnSettlement(fixture)

	result, err := fixture.runner.Run(context.Background(), hostItem())
	if err != nil {
		t.Fatalf("Run with a failing settlement store must not fail: %v", err)
	}
	if result.Status != agent.AgentRunDone {
		t.Fatalf("result = %#v", result)
	}
}
