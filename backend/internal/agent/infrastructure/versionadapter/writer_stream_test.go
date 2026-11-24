package versionadapter_test

// v0.2.4-D T6: the adapter's incremental Host stream surface. The runner speaks
// its own ConversationPart shape; the adapter must hand the version service the
// contract MessagePart fields, pass the lease identity as Writer, and map the
// interrupted seal onto InterruptAssistantStream (FinishAssistantStream is
// complete-only). Expectations come from the T5 service contract.

import (
	"context"
	"encoding/json"
	"testing"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionadapter "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

type streamCall struct {
	writer     versionapp.Writer
	runID      int64
	messageID  int64
	seq        int
	parts      []version.MessagePart
	part       version.MessagePart
	interrupt  bool
	finishSeen bool
}

type fakeStreamConversations struct {
	*fakeConversations
	beginCalls     []streamCall
	updateCalls    []streamCall
	finishCalls    []streamCall
	interruptCalls []streamCall
	recoverCalls   []streamCall
	headID         int64
	orphans        int
}

func (f *fakeStreamConversations) BeginAssistantStream(_ context.Context, _ int64, writer versionapp.Writer, runID int64) (int64, error) {
	f.beginCalls = append(f.beginCalls, streamCall{writer: writer, runID: runID})
	return f.headID, nil
}

func (f *fakeStreamConversations) UpdateStreamPart(_ context.Context, _, messageID int64, seq int, part version.MessagePart) error {
	f.updateCalls = append(f.updateCalls, streamCall{messageID: messageID, seq: seq, part: part})
	return nil
}

func (f *fakeStreamConversations) FinishAssistantStream(_ context.Context, _, messageID int64, parts []version.MessagePart) error {
	f.finishCalls = append(f.finishCalls, streamCall{messageID: messageID, parts: parts})
	return nil
}

func (f *fakeStreamConversations) InterruptAssistantStream(_ context.Context, _, messageID int64, parts []version.MessagePart) error {
	f.interruptCalls = append(f.interruptCalls, streamCall{messageID: messageID, parts: parts, interrupt: true})
	return nil
}

func (f *fakeStreamConversations) InterruptOrphanStreams(_ context.Context, _ int64, writer versionapp.Writer, errorPart version.MessagePart) (int, error) {
	f.recoverCalls = append(f.recoverCalls, streamCall{writer: writer, part: errorPart})
	return f.orphans, nil
}

func TestAdapterHostStreamSurface(t *testing.T) {
	ctx := context.Background()
	fake := &fakeStreamConversations{fakeConversations: &fakeConversations{}, headID: 9101, orphans: 2}
	adapter := versionadapter.New(nil, fake, nil, nil, nil)

	headID, err := adapter.BeginAssistantStream(ctx, 20, "clarification_host", 40, 40)
	if err != nil || headID != 9101 {
		t.Fatalf("BeginAssistantStream = %d (%v), want the head id", headID, err)
	}
	if len(fake.beginCalls) != 1 || fake.beginCalls[0].writer != (versionapp.Writer{TurnID: 40, Agent: "clarification_host"}) || fake.beginCalls[0].runID != 40 {
		t.Fatalf("begin call = %#v, want Writer{40, clarification_host} + runID 40", fake.beginCalls)
	}

	if err := adapter.UpdateStreamPart(ctx, 20, headID, 2, agentapp.ConversationPart{
		Type: "thinking", Text: "核对需求", Data: map[string]any{"duration_ms": int64(1200)},
	}); err != nil {
		t.Fatalf("UpdateStreamPart: %v", err)
	}
	if err := adapter.UpdateStreamPart(ctx, 20, headID, 1, agentapp.ConversationPart{
		Type: "tool_call", Data: map[string]any{
			"call_id": "call_1", "name": "context.version_pack", "status": "running",
			"arguments": json.RawMessage(`{}`), "duration_ms": int64(0),
		},
	}); err != nil {
		t.Fatalf("UpdateStreamPart tool_call: %v", err)
	}
	if len(fake.updateCalls) != 2 {
		t.Fatalf("update calls = %#v", fake.updateCalls)
	}
	thinking := fake.updateCalls[0]
	if thinking.messageID != headID || thinking.seq != 2 || thinking.part.Type != version.MessagePartThinking ||
		thinking.part.Text != "核对需求" || thinking.part.DurationMS != 1200 {
		t.Fatalf("thinking update = %#v, want the contract thinking fields", thinking)
	}
	call := fake.updateCalls[1].part
	if call.Type != version.MessagePartToolCall || call.CallID != "call_1" ||
		call.Name != "context.version_pack" || call.Status != "running" || string(call.Arguments) != "{}" {
		t.Fatalf("tool_call update = %#v, want the contract tool_call fields", call)
	}

	parts := []agentapp.ConversationPart{
		{Type: "text", Text: "已生成的内容"},
		{Type: "error", Data: map[string]any{"code": "stream_interrupted", "message": "连接中断", "retryable": false}},
	}
	if err := adapter.FinishAssistantStream(ctx, 20, headID, parts); err != nil {
		t.Fatalf("FinishAssistantStream: %v", err)
	}
	if err := adapter.InterruptAssistantStream(ctx, 20, headID, parts); err != nil {
		t.Fatalf("InterruptAssistantStream: %v", err)
	}
	if len(fake.finishCalls) != 1 || len(fake.interruptCalls) != 1 {
		t.Fatalf("seals: finish=%d interrupt=%d, want one each (interrupted must not use the complete-only path)",
			len(fake.finishCalls), len(fake.interruptCalls))
	}
	for _, seal := range [][]streamCall{fake.finishCalls, fake.interruptCalls} {
		if seal[0].messageID != headID || len(seal[0].parts) != 2 ||
			seal[0].parts[0].Text != "已生成的内容" || seal[0].parts[1].Code != "stream_interrupted" {
			t.Fatalf("seal parts = %#v, want the converted accumulated parts", seal[0].parts)
		}
	}

	sealed, err := adapter.InterruptOrphanStreams(ctx, 20, "clarification_host", 41, parts[1])
	if err != nil || sealed != 2 {
		t.Fatalf("InterruptOrphanStreams = %d (%v), want the service count", sealed, err)
	}
	if len(fake.recoverCalls) != 1 ||
		fake.recoverCalls[0].writer != (versionapp.Writer{TurnID: 41, Agent: "clarification_host"}) ||
		fake.recoverCalls[0].part.Type != version.MessagePartError ||
		fake.recoverCalls[0].part.Message != "连接中断" {
		t.Fatalf("recovery call = %#v, want the lease identity + converted error part", fake.recoverCalls)
	}
}

// TestAdapterWithoutStreamSurfaceKeepsBatchLease pins the graceful shape: an
// adapter built on a conversation service without the incremental pipeline
// still serves the batch writer lease, and the stream calls fail explicitly
// rather than silently pretending to have written.
func TestAdapterWithoutStreamSurfaceKeepsBatchLease(t *testing.T) {
	ctx := context.Background()
	adapter := versionadapter.New(nil, &fakeConversations{}, nil, nil, nil)
	if _, err := adapter.BeginAssistantStream(ctx, 20, "clarification_host", 40, 40); err == nil {
		t.Fatal("BeginAssistantStream succeeded without the incremental service")
	}
	if err := adapter.UpdateStreamPart(ctx, 20, 9101, 1, agentapp.ConversationPart{Type: "text", Text: "x"}); err == nil {
		t.Fatal("UpdateStreamPart succeeded without the incremental service")
	}
	if err := adapter.FinishAssistantStream(ctx, 20, 9101, []agentapp.ConversationPart{{Type: "text", Text: "x"}}); err == nil {
		t.Fatal("FinishAssistantStream succeeded without the incremental service")
	}
	if err := adapter.InterruptAssistantStream(ctx, 20, 9101, []agentapp.ConversationPart{{Type: "text", Text: "x"}}); err == nil {
		t.Fatal("InterruptAssistantStream succeeded without the incremental service")
	}
	if _, err := adapter.InterruptOrphanStreams(ctx, 20, "clarification_host", 40,
		agentapp.ConversationPart{Type: "error", Data: map[string]any{"code": "stream_interrupted", "message": "连接中断"}}); err == nil {
		t.Fatal("InterruptOrphanStreams succeeded without the incremental service")
	}
}
