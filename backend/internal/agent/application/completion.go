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
