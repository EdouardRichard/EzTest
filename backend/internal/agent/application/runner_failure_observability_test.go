package agentapp_test

import (
	"context"
	"strings"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestConversationRunsVisibleWithReason covers Task 75 / admin B1: a
// conversation chat.turn that fails during model resolution used to leave no
// agent_run row, so Agent 中心 运行观察 filtered by the version returned [] even
// though the thread showed failed jobs. The failed attempt must be recorded with
// the typed reason, and the stored job card must carry the same reason for the
// thread UI.
func TestConversationRunsVisibleWithReason(t *testing.T) {
	fixture := newRunnerFixture(t)
	item := hostItem()
	fixture.chat.modelErr = &platform.Error{
		Status: 400,
		Code:   llmapp.CodeLLMCapabilityMismatch,
		Message: `llm model "text-only" does not support vision required by this role; ` +
			`choose a model with that capability`,
		Details: map[string]any{
			"model":                "text-only",
			"missing_capabilities": []string{"vision"},
		},
	}

	if _, err := mustRun(t, fixture, item); err == nil {
		t.Fatal("Run succeeded on a non-retryable capability mismatch")
	}

	// The pre-dispatch failure is still an agent_run: without it the
	// version-scoped Agent center runs query has nothing to list.
	fixture.repo.mu.Lock()
	runs := make([]agent.AgentRun, 0, len(fixture.repo.runs))
	for _, run := range fixture.repo.runs {
		runs = append(runs, run)
	}
	fixture.repo.mu.Unlock()
	if len(runs) != 1 {
		t.Fatalf("recorded runs = %#v, want one failed conversation run", runs)
	}
	run := runs[0]
	if run.Status != agent.AgentRunFailed {
		t.Fatalf("run status = %q, want failed", run.Status)
	}
	if run.TerminalReason == nil || *run.TerminalReason != llmapp.CodeLLMCapabilityMismatch {
		t.Fatalf("terminal_reason = %#v, want the typed %s", run.TerminalReason, llmapp.CodeLLMCapabilityMismatch)
	}
	if run.WorkItemID == nil || *run.WorkItemID != item.ID {
		t.Fatalf("work_item_id = %#v, want %d", run.WorkItemID, item.ID)
	}
	if run.ConversationID == nil || *run.ConversationID != 20 {
		t.Fatalf("conversation_id = %#v, want 20", run.ConversationID)
	}
	if run.ProfileKey != agentapp.ProfileKeyClarificationHost {
		t.Fatalf("profile_key = %q, want clarification_host", run.ProfileKey)
	}
	if run.FinishedAt == nil || run.FinishedAt.Before(run.StartedAt) {
		t.Fatalf("run started/finished = %v/%v, want a bounded failed attempt", run.StartedAt, run.FinishedAt)
	}

	// The thread-side card explains the failure (code + reason), not just the
	// bare 失败 status the admin trial saw.
	if len(fixture.conv.jobCards) != 1 {
		t.Fatalf("job cards = %#v, want one failed card", fixture.conv.jobCards)
	}
	card := fixture.conv.jobCards[0]
	if card.Data["status"] != "failed" {
		t.Fatalf("card status = %#v, want failed", card.Data["status"])
	}
	if card.Data["code"] != llmapp.CodeLLMCapabilityMismatch {
		t.Fatalf("card code = %#v, want %s", card.Data["code"], llmapp.CodeLLMCapabilityMismatch)
	}
	reason, _ := card.Data["reason"].(string)
	if !strings.Contains(reason, "vision") {
		t.Fatalf("card reason = %q, want the capability explanation", reason)
	}

	// The Agent center run view for the version lists the conversation job and
	// exposes its typed terminal reason.
	versionID := item.VersionID
	workType := agentapp.WorkTypeChatTurn
	reader := &fakeAgentCenterReader{runs: []agentapp.RunRow{{
		ID:             run.ID,
		ProfileKey:     run.ProfileKey,
		WorkItemID:     run.WorkItemID,
		WorkType:       &workType,
		VersionID:      &versionID,
		ConversationID: run.ConversationID,
		Status:         string(run.Status),
		TerminalReason: run.TerminalReason,
		Turns:          run.Turns,
		Usage:          run.Usage,
		Model:          run.Model,
		Tokens:         run.Tokens,
		Cache:          run.Cache,
		StartedAt:      &run.StartedAt,
		FinishedAt:     run.FinishedAt,
	}}}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	views, _, err := service.Runs(context.Background(), agentapp.RunFilter{VersionID: &versionID})
	if err != nil {
		t.Fatalf("Runs: %v", err)
	}
	if reader.lastQuery.VersionID == nil || *reader.lastQuery.VersionID != versionID {
		t.Fatalf("run query version = %#v, want %d", reader.lastQuery.VersionID, versionID)
	}
	if len(views) != 1 {
		t.Fatalf("run views = %#v, want the failed conversation run", views)
	}
	view := views[0]
	if view.WorkType == nil || *view.WorkType != agentapp.WorkTypeChatTurn {
		t.Fatalf("view work_type = %#v, want chat.turn", view.WorkType)
	}
	if view.TerminalReason == nil || *view.TerminalReason != llmapp.CodeLLMCapabilityMismatch {
		t.Fatalf("view terminal_reason = %#v, want %s", view.TerminalReason, llmapp.CodeLLMCapabilityMismatch)
	}
}
