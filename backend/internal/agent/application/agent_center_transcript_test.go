package agentapp_test

// v0.2.4-D T8 (SC-D3): AgentCenterService.RunTranscript is the read side of the
// admin-only live transcript. It returns the run's CURRENT snapshot including
// the streaming state, so an admin opening the drawer mid-run sees the same rows
// the run:{id} deltas then extend.

import (
	"context"
	"errors"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

func TestRunTranscriptReturnsTheCurrentSnapshot(t *testing.T) {
	runID := int64(4242)
	reader := &fakeAgentCenterReader{transcript: []agent.AgentMessage{
		{
			ID: 1, RunID: &runID, Role: agent.MessageRoleUser,
			Parts:       []agent.MessagePart{{Type: "text", Text: "任务"}},
			StreamState: agent.MessageStreamStateComplete,
			CreatedAt:   time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC),
		},
		{
			ID: 2, RunID: &runID, Role: agent.MessageRoleAssistant,
			Parts:       []agent.MessagePart{{Type: "text", Text: "正在生成"}},
			StreamState: agent.MessageStreamStateStreaming,
			CreatedAt:   time.Date(2026, 11, 17, 10, 0, 1, 0, time.UTC),
		},
	}}
	service := agentapp.NewAgentCenterService(reader, nil, nil)

	messages, err := service.RunTranscript(context.Background(), runID)
	if err != nil {
		t.Fatalf("RunTranscript: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages = %d, want the whole transcript (no hidden truncation)", len(messages))
	}
	if messages[1].StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("stream_state = %q, want the live streaming row", messages[1].StreamState)
	}
	if reader.transcriptCalls != 1 || reader.lastTranscriptRunID != runID {
		t.Fatalf("reader calls = %d for run %d, want 1 for %d",
			reader.transcriptCalls, reader.lastTranscriptRunID, runID)
	}
}

func TestRunTranscriptValidatesTheRunID(t *testing.T) {
	reader := &fakeAgentCenterReader{}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	for _, runID := range []int64{0, -1} {
		_, err := service.RunTranscript(context.Background(), runID)
		var typed *platform.Error
		if !errors.As(err, &typed) || typed.Status != 400 {
			t.Fatalf("RunTranscript(%d) err = %v, want a 400 field error", runID, err)
		}
	}
	if reader.transcriptCalls != 0 {
		t.Fatalf("an invalid run id reached the reader %d times", reader.transcriptCalls)
	}
}

func TestRunTranscriptWithoutReaderIsUnavailable(t *testing.T) {
	service := agentapp.NewAgentCenterService(nil, nil, nil)
	_, err := service.RunTranscript(context.Background(), 42)
	var typed *platform.Error
	if !errors.As(err, &typed) || typed.Status != 503 {
		t.Fatalf("err = %v, want 503 when the reader is not wired", err)
	}
}

func TestRunTranscriptPropagatesReadErrors(t *testing.T) {
	wantErr := errors.New("store down")
	reader := &fakeAgentCenterReader{transcriptErr: wantErr}
	service := agentapp.NewAgentCenterService(reader, nil, nil)
	if _, err := service.RunTranscript(context.Background(), 42); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want the reader error", err)
	}
}

// TestTranscriptMessageIsTheWireRow pins the ONE wire projection shared by the
// snapshot endpoint and the delta's full-snapshot mode: decimal string ids, the
// parts/tool_calls arrays, and the stream state.
func TestTranscriptMessageIsTheWireRow(t *testing.T) {
	runID := int64(42)
	conversationID := int64(20)
	toolCallID := "call_1"
	created := time.Date(2026, 11, 17, 10, 0, 0, 0, time.UTC)
	row := agentapp.NewTranscriptMessage(agent.AgentMessage{
		ID: 9101, RunID: &runID, ConversationID: &conversationID,
		Role:        agent.MessageRoleAssistant,
		Parts:       []agent.MessagePart{{Type: "text", Text: "回复"}},
		ToolCalls:   []agent.ToolCall{{ID: "call_1", Name: "requirement.read", Arguments: "{}"}},
		ToolCallID:  &toolCallID,
		Usage:       map[string]int{"output_tokens": 3},
		StreamState: agent.MessageStreamStateStreaming,
		CreatedAt:   created,
	})
	if row.ID != "9101" || row.RunID != "42" || row.ConversationID != "20" {
		t.Fatalf("wire ids = %#v, want decimal strings", row)
	}
	if row.Role != "assistant" || row.StreamState != agent.MessageStreamStateStreaming {
		t.Fatalf("wire role/state = %q/%q", row.Role, row.StreamState)
	}
	if len(row.Parts) != 1 || row.Parts[0].Text != "回复" || len(row.ToolCalls) != 1 {
		t.Fatalf("wire content = %#v", row)
	}
	if row.ToolCallID == nil || *row.ToolCallID != "call_1" || !row.CreatedAt.Equal(created) {
		t.Fatalf("wire tool_call_id/created_at = %#v", row)
	}

	// A batch row without optional ids omits them instead of emitting "0".
	bare := agentapp.NewTranscriptMessage(agent.AgentMessage{ID: 5, Role: agent.MessageRoleUser})
	if bare.RunID != "" || bare.ConversationID != "" || bare.ToolCallID != nil {
		t.Fatalf("bare wire row = %#v, want the optional ids omitted", bare)
	}
	if bare.StreamState != agent.MessageStreamStateComplete {
		t.Fatalf("bare stream_state = %q, want the complete default", bare.StreamState)
	}
	if bare.Parts == nil || bare.ToolCalls == nil || bare.Usage == nil {
		t.Fatalf("bare collections = %#v, want empty arrays/objects, never null", bare)
	}
}
