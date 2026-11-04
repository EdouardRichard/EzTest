package agentapp_test

import (
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestJobCardWiredForConversation pins the Task 61 conversation wiring of
// background job cards:
//
//   - a job that runs for a conversation appends exactly one job_card message to
//     that conversation (job_id/kind/status/current/total);
//   - the start, every completed-turn progress snapshot and the terminal run
//     outcome rewrite that same card instead of appending new ones;
//   - the final assistant reply carries no second card (one job = one card);
//   - a retried run of the same work item reuses the existing card message.
func TestJobCardWiredForConversation(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.chat.responses = []llmapp.ChatResponse{
		{ToolCalls: []llmapp.ToolCall{{
			ID: "call_1", Name: tools.ToolContextSummaryUpdate, Arguments: `{"summary":"登录方式已记录"}`,
		}}},
		{Content: "本轮澄清完成。"},
	}
	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(fixture.conv.jobCards) != 1 {
		t.Fatalf("job cards = %#v, want exactly one card for the job", fixture.conv.jobCards)
	}
	card := fixture.conv.jobCards[0]
	if card.VersionID != 10 || card.ConversationID != 20 {
		t.Fatalf("job card scope = %d/%d, want 10/20", card.VersionID, card.ConversationID)
	}
	if card.Data["job_id"] != "1001" {
		t.Fatalf("job_id = %#v, want the work item id", card.Data["job_id"])
	}
	if card.Data["kind"] != "chat.turn" {
		t.Fatalf("kind = %#v, want the job kind", card.Data["kind"])
	}
	if card.Data["status"] != "done" {
		t.Fatalf("status = %#v, want the terminal done snapshot", card.Data["status"])
	}
	if current, ok := card.Data["current"].(int64); !ok || current != 2 {
		t.Fatalf("current = %#v, want the completed turn count 2", card.Data["current"])
	}
	if total, ok := card.Data["total"].(int64); !ok || total != 0 {
		t.Fatalf("total = %#v, want 0 (unknown)", card.Data["total"])
	}
	if cancelable, ok := card.Data["cancelable"].(bool); !ok || cancelable {
		t.Fatalf("cancelable = %#v, want false on a terminal card", card.Data["cancelable"])
	}

	// Start and per-turn progress rewrote the same card: the history is the
	// coherent snapshot sequence, not a set of appended duplicate cards.
	wantHistory := []struct {
		status  string
		current int64
	}{
		{"running", 0}, {"running", 1}, {"running", 2}, {"done", 2},
	}
	if len(card.History) != len(wantHistory) {
		t.Fatalf("card updates = %#v, want %d snapshots", card.History, len(wantHistory))
	}
	for index, want := range wantHistory {
		got := card.History[index]
		if got["status"] != want.status || got["current"] != want.current {
			t.Fatalf("card update[%d] = %#v, want status %q current %d", index, got, want.status, want.current)
		}
	}

	// The final assistant reply is text only: the card lives in its own assistant
	// message, so the job is never rendered twice.
	if _, ok := partOfType(assistantParts(t, fixture), "job_card"); ok {
		t.Fatal("job_card leaked into the final assistant message")
	}

	// A retry/restart of the same work item updates the existing card instead of
	// appending a second one.
	fixture.chat.responses = append(fixture.chat.responses, llmapp.ChatResponse{Content: "重试完成。"})
	if _, err := mustRun(t, fixture, hostItem()); err != nil {
		t.Fatalf("retry Run: %v", err)
	}
	if len(fixture.conv.jobCards) != 1 {
		t.Fatalf("job cards after retry = %d, want one", len(fixture.conv.jobCards))
	}
	retried := fixture.conv.jobCards[0]
	if retried.MessageID != card.MessageID {
		t.Fatalf("retried job card message = %d, want the original %d", retried.MessageID, card.MessageID)
	}
	if retried.Data["status"] != "done" {
		t.Fatalf("retried card status = %#v, want done", retried.Data["status"])
	}
}
