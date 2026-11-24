//go:build integration

package agentpg_test

// v0.2.4-D T8 (SC-D3): agent_message becomes an incrementally updated transcript
// row while a run streams, and freezes at its terminal state. The expectations
// come from the stream-state contract shared with the conversation pipeline
// (streaming -> complete | interrupted, terminal rows immutable) and from the
// migration's derived CHECK — not from observed repository output.

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func streamingRun(t *testing.T, repo *agentpg.RuntimeRepository) agent.AgentRun {
	t.Helper()
	run, err := repo.CreateRun(context.Background(), agent.AgentRun{
		ProfileKey:    "case_executor",
		Status:        agent.AgentRunRunning,
		OwnerInstance: "instance-1",
		LeaseEpoch:    1,
		Usage:         map[string]int{},
		StartedAt:     testTime,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	return run
}

// TestUpdateMessageRewritesTheStreamingRowInPlace pins the core T8 write: one
// row per assistant turn, rewritten in place (no row growth) with the full
// accumulated snapshot, and readable through the transcript read.
func TestUpdateMessageRewritesTheStreamingRowInPlace(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()
	run := streamingRun(t, repo)

	head, err := repo.AppendMessage(ctx, agent.AgentMessage{
		RunID:       ptr(run.ID),
		Role:        agent.MessageRoleAssistant,
		Parts:       []agent.MessagePart{},
		StreamState: agent.MessageStreamStateStreaming,
		CreatedAt:   testTime,
	})
	if err != nil {
		t.Fatalf("AppendMessage(streaming): %v", err)
	}
	if head.StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("stored stream_state = %q, want streaming", head.StreamState)
	}

	head.Parts = []agent.MessagePart{{Type: "text", Text: "第一段"}}
	if err := repo.UpdateMessage(ctx, head); err != nil {
		t.Fatalf("UpdateMessage first: %v", err)
	}
	head.Parts = []agent.MessagePart{{Type: "text", Text: "第一段第二段"}}
	head.ToolCalls = []agent.ToolCall{{ID: "call_1", Name: "tool_result.read", Arguments: `{"id":"1"}`}}
	head.Usage = map[string]int{"output_tokens": 12}
	if err := repo.UpdateMessage(ctx, head); err != nil {
		t.Fatalf("UpdateMessage second: %v", err)
	}

	messages, err := repo.ListRunMessages(ctx, run.ID, 0)
	if err != nil {
		t.Fatalf("ListRunMessages: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("transcript rows = %d, want 1 (in-place update, never a new row)", len(messages))
	}
	stored := messages[0]
	if stored.ID != head.ID {
		t.Fatalf("row id = %d, want the head %d", stored.ID, head.ID)
	}
	if len(stored.Parts) != 1 || stored.Parts[0].Text != "第一段第二段" {
		t.Fatalf("stored parts = %#v, want the full accumulated snapshot", stored.Parts)
	}
	if len(stored.ToolCalls) != 1 || stored.ToolCalls[0].ID != "call_1" {
		t.Fatalf("stored tool calls = %#v", stored.ToolCalls)
	}
	if stored.Usage["output_tokens"] != 12 {
		t.Fatalf("stored usage = %#v", stored.Usage)
	}
	if stored.StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("stream_state = %q, want streaming until sealed", stored.StreamState)
	}
}

// TestUpdateMessageSealsAndFreezes pins the terminal freeze: sealing writes the
// terminal state, and every later update of that row is refused — for both
// terminal states, and for a batch row that was never streaming.
func TestUpdateMessageSealsAndFreezes(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	ctx := context.Background()

	for _, terminal := range []string{agent.MessageStreamStateComplete, agent.MessageStreamStateInterrupted} {
		run := streamingRun(t, repo)
		head, err := repo.AppendMessage(ctx, agent.AgentMessage{
			RunID:       ptr(run.ID),
			Role:        agent.MessageRoleAssistant,
			Parts:       []agent.MessagePart{{Type: "text", Text: "部分回复"}},
			StreamState: agent.MessageStreamStateStreaming,
			CreatedAt:   testTime,
		})
		if err != nil {
			t.Fatalf("AppendMessage: %v", err)
		}
		head.StreamState = terminal
		head.Parts = []agent.MessagePart{{Type: "text", Text: "部分回复（中断）"}}
		if err := repo.UpdateMessage(ctx, head); err != nil {
			t.Fatalf("seal %s: %v", terminal, err)
		}

		head.Parts = []agent.MessagePart{{Type: "text", Text: "late write"}}
		err = repo.UpdateMessage(ctx, head)
		if !errors.Is(err, agent.ErrMessageStreamClosed) {
			t.Fatalf("late update after %s: err = %v, want ErrMessageStreamClosed", terminal, err)
		}
		messages, err := repo.ListRunMessages(ctx, run.ID, 0)
		if err != nil || len(messages) != 1 {
			t.Fatalf("ListRunMessages = %#v, %v", messages, err)
		}
		if messages[0].StreamState != terminal {
			t.Fatalf("stream_state = %q, want %q", messages[0].StreamState, terminal)
		}
		if messages[0].Parts[0].Text != "部分回复（中断）" {
			t.Fatalf("frozen parts were rewritten: %#v", messages[0].Parts)
		}
	}

	// A legacy/batch row defaults to complete and is therefore frozen too.
	run := streamingRun(t, repo)
	batch, err := repo.AppendMessage(ctx, agent.AgentMessage{
		RunID:     ptr(run.ID),
		Role:      agent.MessageRoleAssistant,
		Parts:     []agent.MessagePart{{Type: "text", Text: "整轮回复"}},
		CreatedAt: testTime,
	})
	if err != nil {
		t.Fatalf("AppendMessage batch: %v", err)
	}
	if batch.StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("batch row stream_state = %q, want the complete default", batch.StreamState)
	}
	batch.Parts = []agent.MessagePart{{Type: "text", Text: "rewritten"}}
	if err := repo.UpdateMessage(ctx, batch); !errors.Is(err, agent.ErrMessageStreamClosed) {
		t.Fatalf("update of a batch row: err = %v, want ErrMessageStreamClosed", err)
	}
}

// TestUpdateMessageUnknownRowIsNotFound keeps the two refusals distinguishable:
// a missing row is not a frozen one.
func TestUpdateMessageUnknownRowIsNotFound(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	err := repo.UpdateMessage(context.Background(), agent.AgentMessage{
		ID:          newIDs(t).Next(),
		Role:        agent.MessageRoleAssistant,
		Parts:       []agent.MessagePart{{Type: "text", Text: "x"}},
		StreamState: agent.MessageStreamStateComplete,
	})
	if !errors.Is(err, agent.ErrNotFound) {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// TestAgentMessageStreamStateCheckIsApplied pins the persisted projection: the
// live DB CHECK accepts exactly the domain authority's set and its default is
// the authority's complete value.
func TestAgentMessageStreamStateCheckIsApplied(t *testing.T) {
	pool := testutil.NewDB(t)
	ctx := context.Background()
	var definition string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid)
		FROM pg_constraint
		WHERE conname = 'chk_agent_message_stream_state'
	`).Scan(&definition); err != nil {
		t.Fatalf("read chk_agent_message_stream_state: %v", err)
	}
	var got []string
	for _, field := range strings.FieldsFunc(definition, func(r rune) bool { return r == '\'' }) {
		switch field {
		case version.MessageStreamStateStreaming, version.MessageStreamStateComplete, version.MessageStreamStateInterrupted:
			got = append(got, field)
		}
	}
	want := append([]string(nil), version.MessageStreamStates()...)
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("applied CHECK %q carries %v, want the authority %v", definition, got, want)
	}

	repo := agentpg.NewRuntimeRepository(pool, newIDs(t))
	run := streamingRun(t, repo)
	_, err := repo.AppendMessage(ctx, agent.AgentMessage{
		RunID:       ptr(run.ID),
		Role:        agent.MessageRoleAssistant,
		StreamState: "not-a-state",
		CreatedAt:   testTime,
	})
	if err == nil {
		t.Fatal("an out-of-enum stream_state was accepted")
	}
}
