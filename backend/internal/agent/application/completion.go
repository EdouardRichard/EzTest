package agentapp

// Completion service (v0.22 spec R-A3-4/R-A8-3, design §4.3; A §5.2 D-A14).
//
// The task.complete tool validates the declaration through the injected
// validator, then hands it to a tools.CompletionWriter. This file provides the
// application service behind that port: it turns a verified completion into one
// write set — plan item status/decision plus a progress fact — and delegates the
// persistence to a CompletionStore that owns the transaction/outbox boundary.
//
// The Postgres store adapter (one transaction over version_plan_item and
// version_fact) and the per-run validator selection are Task 36 wiring, because
// they need the version-context services; tests here pin the write set with an
// in-memory store fake.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Completion write contract constants.
const (
	// CompletionPlanStatusDone is the terminal plan status written when a
	// completion targets a plan item (R-A3-4).
	CompletionPlanStatusDone = "done"
	// CompletionFactKindProgress is the version_fact kind of the progress fact
	// written on completion (R-A8-3).
	CompletionFactKindProgress = "progress"
	// CompletionFactSource is the version_fact.source recorded for completion
	// progress facts.
	CompletionFactSource = "task.complete"
)

// CompletionWrite is the atomic write set of one verified completion: the
// target plan item and the progress fact. A store must write both or neither
// (single transaction or outbox entry), so a crash can never leave the plan done
// without the recorded fact or vice versa. PlanItemID is always > 0: a
// fact-only completion is rejected before this write set is built.
type CompletionWrite struct {
	VersionID  int64
	AgentRunID int64
	// PlanItemID is the verified target plan item; always > 0.
	PlanItemID int64
	// PlanStatus and Decision are the plan item update; always set.
	PlanStatus  string
	Decision    string
	Summary     string
	Artifacts   []string
	Status      string
	FactKind    string
	FactKey     string
	FactPayload map[string]any
	Source      string
	SourceRef   string
}

// CompletionWriteResult is what a CompletionStore persisted.
type CompletionWriteResult struct {
	// FactID is the appended (or idempotently matched) progress fact ID.
	FactID int64
	// PlanItemRevision is the new optimistic-lock revision of the written plan
	// item; the completion port propagates it to the caller.
	PlanItemRevision int
}

// CompletionStore persists one CompletionWrite atomically. The production
// adapter is Task 36 wiring; tests use an in-memory fake.
type CompletionStore interface {
	CommitCompletion(ctx context.Context, write CompletionWrite) (CompletionWriteResult, error)
}

// CompletionService is the tools.CompletionWriter implementation that turns the
// verified completion target into the atomic plan+fact write set. The target
// comes only from tools.CompletionCommit, which the tool copies unchanged from
// the validator's CompletionVerification, so there is no second target the
// service could write by mistake.
type CompletionService struct {
	store CompletionStore
}

// NewCompletionService builds the completion writer for one run. The target plan
// item is carried by each tools.CompletionCommit; a zero target is rejected.
func NewCompletionService(store CompletionStore) *CompletionService {
	return &CompletionService{store: store}
}

var _ tools.CompletionWriter = (*CompletionService)(nil)

// Commit implements tools.CompletionWriter. The declaration was already
// server-verified; this method only validates the internal invariants, builds
// the plan/fact write set for the verified target and delegates the atomic
// persistence.
func (s *CompletionService) Commit(ctx context.Context, commit tools.CompletionCommit) (tools.CompletionOutcome, error) {
	if s == nil || s.store == nil {
		return tools.CompletionOutcome{}, errors.New("completion service has no completion store")
	}
	if commit.Scope.VersionID <= 0 {
		return tools.CompletionOutcome{}, errors.New("completion service requires the injected version scope")
	}
	if commit.PlanItemID <= 0 {
		return tools.CompletionOutcome{}, fmt.Errorf("completion service requires a positive target plan item ID (got %d); fact-only completions are not allowed", commit.PlanItemID)
	}
	summary := strings.TrimSpace(commit.Summary)
	if summary == "" {
		return tools.CompletionOutcome{}, errors.New("completion service requires a non-empty completion summary")
	}
	status := strings.TrimSpace(commit.Status)
	if status == "" {
		status = tools.CompletionStatusCompleted
	}
	artifacts := append([]string{}, commit.Artifacts...)

	planStatus := CompletionPlanStatusDone
	decision := summary
	factKey := completionFactKey(commit.Scope, commit.PlanItemID)
	payload := map[string]any{
		"summary":      summary,
		"status":       status,
		"artifacts":    artifacts,
		"plan_item_id": platformid.Format(commit.PlanItemID),
		"decision":     summary,
	}
	if commit.Scope.AgentRunID > 0 {
		payload["agent_run_id"] = platformid.Format(commit.Scope.AgentRunID)
	}

	write := CompletionWrite{
		VersionID:   commit.Scope.VersionID,
		AgentRunID:  commit.Scope.AgentRunID,
		PlanItemID:  commit.PlanItemID,
		PlanStatus:  planStatus,
		Decision:    decision,
		Summary:     summary,
		Artifacts:   artifacts,
		Status:      status,
		FactKind:    CompletionFactKindProgress,
		FactKey:     factKey,
		FactPayload: payload,
		Source:      CompletionFactSource,
		SourceRef:   factKey,
	}
	result, err := s.store.CommitCompletion(ctx, write)
	if err != nil {
		return tools.CompletionOutcome{}, fmt.Errorf("commit completion: %w", err)
	}
	return tools.CompletionOutcome{
		PlanItemID:       commit.PlanItemID,
		PlanStatus:       planStatus,
		Decision:         decision,
		PlanItemRevision: result.PlanItemRevision,
		FactID:           result.FactID,
		FactKey:          factKey,
	}, nil
}

// completionFactKey names the completion progress fact. Binding it to the run
// makes a repeated task.complete call deterministic (falling back to the target
// plan item when the run ID is absent). Idempotency is the version_fact unique
// key (version_id, kind, key, content_hash): replaying the same completion with
// identical content matches the existing row, while changed content appends a
// new fact (facts are append-only and never overwritten), so the key alone is
// not the dedupe condition.
func completionFactKey(scope tools.Scope, planItemID int64) string {
	if scope.AgentRunID > 0 {
		return "task.complete:run:" + platformid.Format(scope.AgentRunID)
	}
	return "task.complete:plan_item:" + platformid.Format(planItemID)
}

// Settlement writeback contract (v0.22 R-A8-3, A §6.12 rule 3; acceptance
// A-14). A run that ends on the turn/time settlement turn without a verified
// completion did not finish its plan item, so the next run's briefing must not
// keep showing the stale pre-run state.
const (
	// SettlementPlanStatus is the plan status written on settlement. The work
	// was not completed, so the item stays in progress; the decision records
	// why the run stopped. "blocked" is intentionally not used: the item is
	// still actionable and a later run/attempt can continue it.
	SettlementPlanStatus = "in_progress"
	// SettlementFactKind is the existing progress fact kind ("progress");
	// v0.22 adds no new fact type for settlement (A §6.12 calls for a
	// progress/decision fact).
	SettlementFactKind = CompletionFactKindProgress
	// SettlementFactSource is the version_fact.source of a settlement fact,
	// distinct from the verified-completion source task.complete.
	SettlementFactSource = "run.settlement"
)

// Settlement is one settled-run writeback request. The target plan item and the
// version/run scope are server-resolved; the model never supplies them.
type Settlement struct {
	VersionID  int64
	AgentRunID int64
	// Reason is the stable budget cause that opened the settlement turn:
	// BudgetCauseTurns or BudgetCauseTime (tokens are telemetry in v0.22).
	Reason string
}

// SettlementResult is what a settlement writeback persisted.
type SettlementResult struct {
	PlanItemID       int64
	PlanStatus       string
	Decision         string
	FactID           int64
	PlanItemRevision int
}

// SettlementRecorder persists the settlement write set. The target plan item is
// bound by the builder that resolved it (the same ordered resolver task.complete
// uses), so the runner cannot write an unprepared target.
type SettlementRecorder interface {
	RecordSettlement(ctx context.Context, settlement Settlement) (SettlementResult, error)
}

// SettlementService is the SettlementRecorder implementation. It reuses the
// CompletionStore write path (one transaction/outbox entry over
// version_plan_item + version_fact) but does not go through
// CompletionService.Commit: that is the success-only path that hardcodes the
// done status and requires a completion summary.
type SettlementService struct {
	store      CompletionStore
	planItemID int64
}

// NewSettlementService binds a settlement writeback to one resolved plan item.
func NewSettlementService(store CompletionStore, planItemID int64) *SettlementService {
	return &SettlementService{store: store, planItemID: planItemID}
}

var _ SettlementRecorder = (*SettlementService)(nil)

// RecordSettlement builds the plan+fact write set of one settled run and
// delegates the atomic persistence to the shared CompletionStore. The fact key
// is bound to the run and the payload is byte-identical on replay, so the
// version_fact unique key (version_id, kind, key, content_hash) makes a retried
// writeback converge to the existing row; the store's plan update is a no-op
// when the status/decision already match, so a replay does not bump the
// revision twice.
func (s *SettlementService) RecordSettlement(ctx context.Context, settlement Settlement) (SettlementResult, error) {
	if s == nil || s.store == nil {
		return SettlementResult{}, errors.New("settlement service has no completion store")
	}
	if s.planItemID <= 0 {
		return SettlementResult{}, fmt.Errorf("settlement service requires a positive target plan item ID (got %d)", s.planItemID)
	}
	if settlement.VersionID <= 0 {
		return SettlementResult{}, errors.New("settlement service requires the injected version scope")
	}
	if settlement.AgentRunID <= 0 {
		return SettlementResult{}, errors.New("settlement service requires the run ID for an idempotent fact key")
	}
	reason := strings.TrimSpace(settlement.Reason)
	if reason != BudgetCauseTurns && reason != BudgetCauseTime {
		return SettlementResult{}, fmt.Errorf("settlement service does not accept budget cause %q", settlement.Reason)
	}
	decision := settlementDecision(reason)
	factKey := "run.settle:" + platformid.Format(settlement.AgentRunID)
	payload := map[string]any{
		"run_id":       platformid.Format(settlement.AgentRunID),
		"reason":       reason,
		"plan_item_id": platformid.Format(s.planItemID),
		"status":       SettlementPlanStatus,
		"decision":     decision,
	}
	write := CompletionWrite{
		VersionID:   settlement.VersionID,
		AgentRunID:  settlement.AgentRunID,
		PlanItemID:  s.planItemID,
		PlanStatus:  SettlementPlanStatus,
		Decision:    decision,
		Summary:     decision,
		Status:      SettlementPlanStatus,
		FactKind:    SettlementFactKind,
		FactKey:     factKey,
		FactPayload: payload,
		Source:      SettlementFactSource,
		SourceRef:   factKey,
	}
	result, err := s.store.CommitCompletion(ctx, write)
	if err != nil {
		return SettlementResult{}, fmt.Errorf("record settlement: %w", err)
	}
	return SettlementResult{
		PlanItemID:       s.planItemID,
		PlanStatus:       SettlementPlanStatus,
		Decision:         decision,
		FactID:           result.FactID,
		PlanItemRevision: result.PlanItemRevision,
	}, nil
}

// settlementDecision renders the stable plan decision of a settled run. It is
// deterministic for a cause, so an idempotent replay produces the same content
// hash.
func settlementDecision(reason string) string {
	switch reason {
	case BudgetCauseTurns:
		return "settled by turn budget"
	case BudgetCauseTime:
		return "settled by time budget"
	default:
		return "settled by resource budget"
	}
}
