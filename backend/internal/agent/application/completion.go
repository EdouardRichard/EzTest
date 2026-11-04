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
// target plan item (when PlanItemID > 0) and the progress fact. A store must
// write both or neither (single transaction or outbox entry), so a crash can
// never leave the plan done without the recorded fact or vice versa.
type CompletionWrite struct {
	VersionID  int64
	AgentRunID int64
	// PlanItemID is the target plan item, or 0 for a fact-only completion.
	PlanItemID int64
	// PlanStatus and Decision are empty when PlanItemID is 0.
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
	// PlanItemRevision is the new optimistic-lock revision of the plan item; 0
	// for a fact-only completion.
	PlanItemRevision int
}

// CompletionStore persists one CompletionWrite atomically. The production
// adapter is Task 36 wiring; tests use an in-memory fake.
type CompletionStore interface {
	CommitCompletion(ctx context.Context, write CompletionWrite) (CompletionWriteResult, error)
}

// CompletionService is the tools.CompletionWriter implementation that binds a
// completed run to its task target (the plan item) and builds the atomic write
// set.
type CompletionService struct {
	store      CompletionStore
	planItemID int64
}

// NewCompletionService builds the completion writer for one run. planItemID is
// the target plan item (0 = no plan target, write the progress fact only).
func NewCompletionService(store CompletionStore, planItemID int64) *CompletionService {
	return &CompletionService{store: store, planItemID: planItemID}
}

var _ tools.CompletionWriter = (*CompletionService)(nil)

// Commit implements tools.CompletionWriter. The declaration was already
// server-verified; this method only validates the internal invariants, builds
// the plan/fact write set and delegates the atomic persistence.
func (s *CompletionService) Commit(ctx context.Context, commit tools.CompletionCommit) (tools.CompletionOutcome, error) {
	if s == nil || s.store == nil {
		return tools.CompletionOutcome{}, errors.New("completion service has no completion store")
	}
	if commit.Scope.VersionID <= 0 {
		return tools.CompletionOutcome{}, errors.New("completion service requires the injected version scope")
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

	planStatus := ""
	decision := ""
	if s.planItemID > 0 {
		planStatus = CompletionPlanStatusDone
		decision = summary
	}
	factKey := completionFactKey(commit.Scope, s.planItemID)
	payload := map[string]any{
		"summary":   summary,
		"status":    status,
		"artifacts": artifacts,
	}
	if s.planItemID > 0 {
		payload["plan_item_id"] = platformid.Format(s.planItemID)
		payload["decision"] = summary
	}
	if commit.Scope.AgentRunID > 0 {
		payload["agent_run_id"] = platformid.Format(commit.Scope.AgentRunID)
	}

	write := CompletionWrite{
		VersionID:   commit.Scope.VersionID,
		AgentRunID:  commit.Scope.AgentRunID,
		PlanItemID:  s.planItemID,
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
		PlanItemID: s.planItemID,
		PlanStatus: planStatus,
		Decision:   decision,
		FactID:     result.FactID,
		FactKey:    factKey,
	}, nil
}

// completionFactKey names the completion progress fact. Binding it to the run
// makes a repeated task.complete call idempotent (the version_fact store returns
// the existing row for the same kind/key/content hash) while distinct runs and
// plan items stay distinguishable.
func completionFactKey(scope tools.Scope, planItemID int64) string {
	switch {
	case scope.AgentRunID > 0:
		return "task.complete:run:" + platformid.Format(scope.AgentRunID)
	case planItemID > 0:
		return "task.complete:plan_item:" + platformid.Format(planItemID)
	default:
		return "task.complete:version:" + platformid.Format(scope.VersionID)
	}
}
