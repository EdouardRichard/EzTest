package agentapp_test

import (
	"errors"
	"fmt"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

// TestOverflowRetriesFromSettings pins the W1-2 wiring of
// compaction.overflow_retries: the runner reads the setting instead of the old
// hard-coded rescue budget. Zero disables the forced-compaction rescue, so the
// first provider context-overflow is terminal and no summary call is made.
func TestOverflowRetriesFromSettings(t *testing.T) {
	fixture := newRunnerFixture(t)
	fixture.settings.ints["compaction.overflow_retries"] = 0
	fixture.conv.snapshot.Summary = ""
	fixture.conv.snapshot.PinnedFacts = nil
	fixture.conv.snapshot.Messages = []agentapp.ConversationMessage{
		{ID: 671, Role: "user", Text: "较早需求一"},
		{ID: 672, Role: "user", Text: "较早需求二"},
		{ID: 673, Role: "user", Text: "较早需求三"},
		{ID: 674, Role: "user", Text: "最新需求"},
	}
	fixture.chat.errors = []error{fmt.Errorf("upstream: %w", llmapp.ErrContextOverflow)}
	fixture.chat.responses = []llmapp.ChatResponse{{Content: "不应被采用的回复"}}

	_, err := mustRun(t, fixture, hostItem())
	if err == nil {
		t.Fatal("Run err = nil, want context_overflow_unrecoverable with overflow_retries=0")
	}
	var runErr *agentapp.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("Run err = %#v, want *RunError", err)
	}
	if runErr.Reason != "context_overflow_unrecoverable" {
		t.Fatalf("Run err reason = %q, want context_overflow_unrecoverable", runErr.Reason)
	}
	if calls := fixture.chat.requestCount(); calls != 1 {
		t.Fatalf("chat calls = %d, want 1 (no compaction summary after retries=0)", calls)
	}
	if containsEvent(fixture.events, "context.compacted") {
		t.Fatalf("events = %v, want no context.compacted with retries=0", eventTypes(fixture.events))
	}
}
