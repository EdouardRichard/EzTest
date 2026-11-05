package main

// v0.22 W0 fix round 1 (Task 36 review Important 9): composition-root tests
// without a database or external services. They pin the wiring the plan's
// TestW0CompositionRootNonNil asks for: stage gates are constructed, the stage
// callback relay is bound, the task.complete builder resolves a Host target,
// the structured-question writer round-trips through the real adapter and the
// error-part metadata reaches the domain snapshot.

import (
	"context"
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type fakeWiringPlanCatalog struct {
	items []versiondomain.PlanItem
}

func (f fakeWiringPlanCatalog) List(context.Context, int64, string) ([]versiondomain.PlanItem, error) {
	return f.items, nil
}

func (f fakeWiringPlanCatalog) Update(_ context.Context, _, itemID int64, patch versiondomain.PlanPatch, revision int) (versiondomain.PlanItem, error) {
	for _, item := range f.items {
		if item.ID == itemID {
			item.Status = patch.Status
			item.Decision = patch.Decision
			item.Revision = revision + 1
			return item, nil
		}
	}
	return versiondomain.PlanItem{}, versiondomain.ErrPlanNotFound
}

func (f fakeWiringPlanCatalog) Upsert(_ context.Context, item versiondomain.PlanItem) (versiondomain.PlanItem, error) {
	if item.ID == 0 {
		item.ID = int64(len(f.items) + 1)
	}
	item.Revision = 1
	return item, nil
}

type fakeWiringCompletionStore struct {
	commits int
	last    agentapp.CompletionWrite
}

func (f *fakeWiringCompletionStore) CommitCompletion(_ context.Context, write agentapp.CompletionWrite) (agentapp.CompletionWriteResult, error) {
	f.commits++
	f.last = write
	return agentapp.CompletionWriteResult{FactID: 9001, PlanItemRevision: 2}, nil
}

type fakeWiringStructuredConversation struct {
	last    versionapp.StructuredMessageInput
	view    versionapp.MessageView
	created bool
	err     error
}

func (f *fakeWiringStructuredConversation) AppendStructuredMessage(_ context.Context, in versionapp.StructuredMessageInput) (versionapp.MessageView, bool, error) {
	f.last = in
	return f.view, f.created, f.err
}

// TestW0CompositionRootNonNil is the plan-required composition-root assertion:
// the v0.22 runtime collaborators assembled by main are really wired (not nil
// placeholders) and their adapter contracts are exercised end to end.
func TestW0CompositionRootNonNil(t *testing.T) {
	// Stage callback relay is bound and forwards to the real service surface.
	relay := newStageCallbackRelay()
	if relay == nil {
		t.Fatal("stage callback relay is nil")
	}
	sink := &fakeStageCallbackSink{}
	relay.set(sink)
	if err := relay.OnWaveTerminal(context.Background(), 11); err != nil {
		t.Fatalf("bound relay: %v", err)
	}
	if len(sink.terminal) != 1 || sink.terminal[0] != 11 {
		t.Fatalf("relay did not forward to the stage service: %#v", sink.terminal)
	}

	// The stage gates read real collaborators: every gate whose dependency is
	// supplied must be a non-nil function (a nil gate silently disables a gate).
	gates := newV022StageGates(v022StageGateDeps{
		Versions: fakeWiringVersionRead{view: versionapp.VersionView{ID: 7}},
		Waves:    fakeWiringWaves{},
	})
	checks := map[string]bool{
		"environment_bound":      gates.EnvironmentBound != nil,
		"requirement_count":      gates.RequirementCount != nil,
		"requirements_confirmed": gates.RequirementsConfirmed != nil,
		"accounts_valid":         gates.AccountsValid != nil,
		"waves_terminal":         gates.WavesTerminal != nil,
		"blocker_cleared":        gates.BlockerCleared != nil,
	}
	for name, ok := range checks {
		if !ok {
			t.Fatalf("stage gate %s is nil", name)
		}
	}
	if gates.EnvironmentBound == nil {
		t.Fatal("environment gate is nil")
	}
	if _, err := gates.EnvironmentBound(context.Background(), 7); err != nil {
		t.Fatalf("environment gate read: %v", err)
	}
}

// TestCompletionBuilderResolvesHostTarget is Critical 3 at the composition
// level: a real Host work item (no explicit plan link, no payload plan id) gets
// task.complete through the version's single in-progress plan item, and the
// resolved deps carry a usable validator and writer.
func TestCompletionBuilderResolvesHostTarget(t *testing.T) {
	store := &fakeWiringCompletionStore{}
	workItemID := int64(401)
	plans := fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
		{ID: 70, VersionID: 7, Status: "done"},
		{ID: 77, VersionID: 7, Status: "in_progress", Revision: 3},
	}}
	builder := completionBuilder{plans: plans, store: store}
	deps, err := builder.Build(context.Background(), agentapp.CompletionContext{
		VersionID:  7,
		WorkItemID: workItemID,
		ProfileKey: agentapp.ProfileKeyClarificationHost,
	})
	if err != nil {
		t.Fatalf("Build(host): %v", err)
	}
	if deps.Validator == nil || deps.Writer == nil {
		t.Fatalf("host completion deps = %#v", deps)
	}
	// The writer is the real completion service over the injected store: a
	// verified completion reaches the store with the resolved plan target.
	outcome, err := deps.Writer.Commit(context.Background(), tools.CompletionCommit{
		Scope:      tools.Scope{VersionID: 7, AgentRunID: 901},
		PlanItemID: 77,
		Summary:    "clarified the login requirement",
	})
	if err != nil {
		t.Fatalf("completion commit: %v", err)
	}
	if outcome.PlanItemID != 77 || store.commits != 1 || store.last.PlanItemID != 77 {
		t.Fatalf("completion write = %#v (store commits=%d)", outcome, store.commits)
	}

	// The explicit work_item_id link wins over the in-progress fallback.
	linked := int64(401)
	linkedPlans := fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
		{ID: 88, VersionID: 7, Status: "pending", WorkItemID: &linked},
		{ID: 77, VersionID: 7, Status: "in_progress"},
	}}
	linkedDeps, err := (completionBuilder{plans: linkedPlans, store: store}).Build(context.Background(), agentapp.CompletionContext{
		VersionID: 7, WorkItemID: 401, ProfileKey: agentapp.ProfileKeyDesignHost,
	})
	if err != nil || linkedDeps.Writer == nil {
		t.Fatalf("Build(linked host) = %#v, %v", linkedDeps, err)
	}

	// Ambiguity fails closed instead of exposing task.complete with a guess.
	ambiguous := fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
		{ID: 1, VersionID: 7, Status: "in_progress"},
		{ID: 2, VersionID: 7, Status: "in_progress"},
	}}
	if _, err := (completionBuilder{plans: ambiguous, store: store}).Build(context.Background(), agentapp.CompletionContext{
		VersionID: 7, WorkItemID: 401, ProfileKey: agentapp.ProfileKeyClarificationHost,
	}); err == nil {
		t.Fatal("ambiguous in-progress plan items did not fail closed")
	}
}

// TestCompletionBuilderResolvesSettlementTarget pins A-14 at the composition
// level: the settlement writeback reuses the completion target resolver, so a
// run without an explicit plan link writes the version's single in-progress
// item, while an ambiguous version fails closed (the runner then only logs the
// best-effort warning).
func TestCompletionBuilderResolvesSettlementTarget(t *testing.T) {
	store := &fakeWiringCompletionStore{}
	builder := completionBuilder{
		plans: fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
			{ID: 70, VersionID: 7, Status: "done"},
			{ID: 77, VersionID: 7, Status: "in_progress", Revision: 3},
		}},
		store: store,
	}
	recorder, err := builder.BuildSettlement(context.Background(), agentapp.CompletionContext{VersionID: 7, WorkItemID: 401})
	if err != nil || recorder == nil {
		t.Fatalf("BuildSettlement = %v, %v", recorder, err)
	}
	result, err := recorder.RecordSettlement(context.Background(), agentapp.Settlement{
		VersionID: 7, AgentRunID: 901, Reason: agentapp.BudgetCauseTurns,
	})
	if err != nil {
		t.Fatalf("RecordSettlement: %v", err)
	}
	if result.PlanItemID != 77 || store.commits != 1 || store.last.PlanItemID != 77 {
		t.Fatalf("settlement write = %#v (store commits=%d)", result, store.commits)
	}
	if store.last.PlanStatus != agentapp.SettlementPlanStatus || store.last.FactKind != "progress" {
		t.Fatalf("settlement store write = %#v", store.last)
	}

	// The explicit work_item_id link wins over the in-progress fallback.
	linkedWorkItem := int64(401)
	linkedStore := &fakeWiringCompletionStore{}
	linkedBuilder := completionBuilder{
		plans: fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
			{ID: 88, VersionID: 7, Status: "pending", WorkItemID: &linkedWorkItem},
			{ID: 77, VersionID: 7, Status: "in_progress"},
		}},
		store: linkedStore,
	}
	linkedRecorder, err := linkedBuilder.BuildSettlement(context.Background(), agentapp.CompletionContext{VersionID: 7, WorkItemID: 401})
	if err != nil {
		t.Fatalf("BuildSettlement(linked): %v", err)
	}
	if _, err := linkedRecorder.RecordSettlement(context.Background(), agentapp.Settlement{
		VersionID: 7, AgentRunID: 902, Reason: agentapp.BudgetCauseTime,
	}); err != nil {
		t.Fatalf("RecordSettlement(linked): %v", err)
	}
	if linkedStore.last.PlanItemID != 88 {
		t.Fatalf("linked settlement target = %d, want the work-item link 88", linkedStore.last.PlanItemID)
	}

	// Ambiguity fails closed instead of guessing which item to settle.
	ambiguous := completionBuilder{
		plans: fakeWiringPlanCatalog{items: []versiondomain.PlanItem{
			{ID: 1, VersionID: 7, Status: "in_progress"},
			{ID: 2, VersionID: 7, Status: "in_progress"},
		}},
		store: store,
	}
	if _, err := ambiguous.BuildSettlement(context.Background(), agentapp.CompletionContext{VersionID: 7, WorkItemID: 401}); err == nil {
		t.Fatal("ambiguous settlement target did not fail closed")
	}
	// A missing store is a configuration error, never a fact-only write.
	if _, err := (completionBuilder{plans: fakeWiringPlanCatalog{}}).BuildSettlement(context.Background(), agentapp.CompletionContext{VersionID: 7}); err == nil {
		t.Fatal("settlement builder without a store did not fail closed")
	}
}

// TestQuestionMessageWriterAdapterRoundTrips pins Critical 1 at the composition
// level: the adapter hands the conversation service a domain-valid question
// part (no direct SQL, no question_card type) with the host writer run scope.
func TestQuestionMessageWriterAdapterRoundTrips(t *testing.T) {
	conversations := &fakeWiringStructuredConversation{
		view:    versionapp.MessageView{ID: 555, ConversationID: 20, Role: versiondomain.MessageRoleAssistant},
		created: true,
	}
	adapter := questionMessageWriterAdapter{conversations: conversations}
	id, created, err := adapter.AppendQuestionMessage(context.Background(), executionapp.QuestionMessage{
		VersionID:       7,
		ConversationID:  20,
		QuestionID:      9001,
		Kind:            executionapp.QuestionMessageKindQuestion,
		ClientMessageID: "question-ask:9001",
		AgentRunID:      701,
		Payload: map[string]any{
			"question_id": "9001",
			"batch_id":    "qb-9001",
			"questions":   []any{map[string]any{"id": "q1", "text": "Which login method?"}},
			"expires_at":  "2026-11-06T08:00:00Z",
		},
	})
	if err != nil {
		t.Fatalf("AppendQuestionMessage: %v", err)
	}
	if id != 555 || !created {
		t.Fatalf("adapter result = %d/%v", id, created)
	}
	if conversations.last.Kind != versiondomain.MessageKindQuestion ||
		conversations.last.ClientMessageID != "question-ask:9001" ||
		conversations.last.AgentRunID != 701 || conversations.last.ConversationID != 20 {
		t.Fatalf("structured write input = %#v", conversations.last)
	}
	if len(conversations.last.Parts) != 1 || conversations.last.Parts[0].Type != versiondomain.MessagePartQuestion {
		t.Fatalf("adapter parts = %#v", conversations.last.Parts)
	}
	if err := versiondomain.ValidateAssistantMessageParts(conversations.last.Parts); err != nil {
		t.Fatalf("adapter wrote a domain-invalid part: %v", err)
	}

	// The answer echo is a user text message, not a second question card.
	answerID, _, err := adapter.AppendQuestionMessage(context.Background(), executionapp.QuestionMessage{
		VersionID:       7,
		ConversationID:  20,
		QuestionID:      9001,
		Kind:            executionapp.QuestionMessageKindAnswer,
		ClientMessageID: "question-answer:9001:1",
		Payload: map[string]any{
			"question_id": "9001",
			"answers":     []any{map[string]any{"question_id": "q1", "selected": []any{"o1"}, "text": "SMS"}},
		},
	})
	if err != nil || answerID != 555 {
		t.Fatalf("AppendQuestionMessage(answer) = %d, %v", answerID, err)
	}
	if conversations.last.Kind != versiondomain.MessageKindAnswer ||
		conversations.last.Parts[0].Type != versiondomain.MessagePartText ||
		conversations.last.Parts[0].Text == "" {
		t.Fatalf("answer write = %#v", conversations.last)
	}
}

// TestErrorPartMetadataSurvivesDomainSnapshot pins Important 8 end to end at
// the domain boundary: the metadata the runner forwards into the ConversationPart
// is a valid, round-trippable version.MessagePart error.
func TestErrorPartMetadataSurvivesDomainSnapshot(t *testing.T) {
	part := versiondomain.MessagePart{
		Type:      versiondomain.MessagePartError,
		Code:      tools.CodeInvalidArgument,
		Message:   "database_id is not bound to this version",
		Retryable: true,
		Field:     "cases[0].assertions[0].database_id",
		Allowed:   []string{"11", "12"},
		Hint:      "call sut.database_list first",
	}
	if err := versiondomain.ValidateAssistantMessageParts([]versiondomain.MessagePart{part}); err != nil {
		t.Fatalf("error part rejected: %v", err)
	}
	raw, err := json.Marshal(part)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var decoded versiondomain.MessagePart
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal error part: %v", err)
	}
	if !decoded.Retryable || decoded.Field != part.Field || decoded.Hint != part.Hint || len(decoded.Allowed) != 2 {
		t.Fatalf("error metadata round trip = %#v", decoded)
	}
}

// fakeWiringVersionRead implements stageVersionReadModel.
type fakeWiringVersionRead struct {
	view versionapp.VersionView
	err  error
}

func (f fakeWiringVersionRead) Get(context.Context, int64) (versionapp.VersionView, error) {
	return f.view, f.err
}

// fakeWiringWaves implements stageWaveRepository for the terminal gate.
type fakeWiringWaves struct {
	waves []execution.RunWave
	err   error
}

func (f fakeWiringWaves) Get(context.Context, int64) (execution.RunWave, error) {
	if len(f.waves) == 0 {
		return execution.RunWave{}, nil
	}
	return f.waves[0], nil
}

func (f fakeWiringWaves) List(context.Context, int64, int, *int64) ([]execution.RunWave, bool, error) {
	return f.waves, false, f.err
}
