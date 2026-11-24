package versionapp_test

// v0.2.4-D T6: crash recovery of orphaned incremental Host heads (SC-D4).
// A process that dies mid-stream can never seal its own head. The next turn
// acquires the conversation's single-writer lease (taking over the dead turn),
// which makes every other streaming head an orphan by construction; the lease
// holder seals them interrupted with an error part, preserving the accumulated
// content. Expectations come from the SC-D4 contract, not observed output.

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// InterruptOrphanStreams mirrors the repository's lease-fenced orphan sweep,
// including its H-E2 per-head tolerance.
func (f *fakeConversationRepo) InterruptOrphanStreams(_ context.Context, conversationID, turnID int64, agent string, errorPart version.MessagePart, now time.Time) ([]version.Message, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return nil, version.ErrConversationNotFound
	}
	live := conversation.ActiveTurnID != nil && conversation.ActiveAgent != nil &&
		conversation.LeaseUntil != nil && !conversation.LeaseUntil.Before(now)
	if !live {
		return nil, version.ErrNotConversationWriter
	}
	if *conversation.ActiveTurnID != turnID || *conversation.ActiveAgent != agent {
		return nil, version.ErrConversationBusy
	}
	turnKey := version.HostTurnClientMessageID(turnID)
	return f.sealOrphanHeads(conversationID, errorPart, now, func(message version.Message) bool {
		return message.ClientMessageID == nil || *message.ClientMessageID != turnKey
	})
}

// InterruptRunOrphanStreams mirrors the run-scoped recovery: no lease is
// required, and only heads bound to the given run are selected.
func (f *fakeConversationRepo) InterruptRunOrphanStreams(_ context.Context, conversationID, agentRunID int64, errorPart version.MessagePart, now time.Time) ([]version.Message, error) {
	if _, ok := f.conversations[conversationID]; !ok {
		return nil, version.ErrConversationNotFound
	}
	return f.sealOrphanHeads(conversationID, errorPart, now, func(message version.Message) bool {
		return message.AgentRunID != nil && *message.AgentRunID == agentRunID
	})
}

// sealOrphanHeads is the fake's ONE seal implementation, shared by both recovery
// entries exactly as the repository shares sealOrphanHeads. A head the validator
// refuses is left streaming and reported.
func (f *fakeConversationRepo) sealOrphanHeads(conversationID int64, errorPart version.MessagePart, now time.Time, selected func(version.Message) bool) ([]version.Message, error) {
	var sealed []version.Message
	failures := make([]version.OrphanSealFailure, 0)
	for index := range f.messages[conversationID] {
		message := &f.messages[conversationID][index]
		if message.Role != version.MessageRoleAssistant || message.StreamState != version.MessageStreamStateStreaming {
			continue
		}
		if !selected(*message) {
			continue
		}
		parts := append(append([]version.MessagePart(nil), message.Parts...), errorPart)
		if err := version.ValidateAssistantMessageParts(parts); err != nil {
			failures = append(failures, version.OrphanSealFailure{MessageID: message.ID, Err: err})
			continue
		}
		seq := 0
		for _, part := range message.Parts {
			if version.IsStreamingMessagePartType(part.Type) {
				seq++
			}
		}
		message.Parts = parts
		message.StreamState = version.MessageStreamStateInterrupted
		payload, err := json.Marshal(errorPart)
		if err != nil {
			return nil, err
		}
		f.messageParts[message.ID] = append(f.messageParts[message.ID],
			version.ConversationMessagePart{Seq: seq + 1, Type: errorPart.Type, Payload: payload, CreatedAt: now})
		sealed = append(sealed, *message)
	}
	if len(failures) > 0 {
		return sealed, &version.OrphanSealError{Failures: failures}
	}
	return sealed, nil
}

func recoveryErrorPart() version.MessagePart {
	return version.MessagePart{
		Type: version.MessagePartError, Code: "stream_interrupted",
		Message: "连接中断，本条回复未完成。", Retryable: false,
	}
}

// TestInterruptOrphanStreamsPreservesContentAndSealsInterrupted pins the crash
// path: the new lease holder seals the dead turn's head, keeps its accumulated
// parts, appends the error part and publishes the terminal interrupted frame.
func TestInterruptOrphanStreamsPreservesContentAndSealsInterrupted(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	dead := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, dead.Agent, dead.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	orphanID := beginStream(t, env, conversation.ID, dead, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, orphanID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "崩溃前已显示的内容"}); err != nil {
		t.Fatalf("update: %v", err)
	}

	// The crashed turn stopped heartbeating; the next turn takes the lease over.
	env.advanceClock(version.ConversationWriterLeaseDuration + time.Second)
	next := versionapp.Writer{TurnID: 202, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, next.Agent, next.TurnID); err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	sealed, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, next, recoveryErrorPart())
	if err != nil {
		t.Fatalf("InterruptOrphanStreams: %v", err)
	}
	if sealed != 1 {
		t.Fatalf("sealed heads = %d, want 1", sealed)
	}
	var head *version.Message
	for index := range env.repo.messages[conversation.ID] {
		if env.repo.messages[conversation.ID][index].ID == orphanID {
			head = &env.repo.messages[conversation.ID][index]
		}
	}
	if head == nil {
		t.Fatal("orphan head disappeared")
	}
	if head.StreamState != version.MessageStreamStateInterrupted {
		t.Fatalf("orphan stream_state = %q, want interrupted (never complete)", head.StreamState)
	}
	if len(head.Parts) != 2 || head.Parts[0].Type != version.MessagePartText ||
		head.Parts[0].Text != "崩溃前已显示的内容" || head.Parts[1].Type != version.MessagePartError {
		t.Fatalf("orphan parts = %#v, want preserved text + appended error part", head.Parts)
	}
	rows := env.repo.messageParts[orphanID]
	if len(rows) != 2 || rows[1].Seq != 2 || rows[1].Type != version.MessagePartError {
		t.Fatalf("orphan part rows = %#v, want the error part at the next contiguous seq", rows)
	}
	frame := fullFrameFor(t, env, orphanID)
	if frame.Payload["stream_state"] != version.MessageStreamStateInterrupted {
		t.Fatalf("terminal frame stream_state = %#v, want interrupted", frame.Payload["stream_state"])
	}

	// Idempotent: a second sweep finds nothing and republishes nothing.
	again, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, next, recoveryErrorPart())
	if err != nil || again != 0 {
		t.Fatalf("second sweep = %d (%v), want 0", again, err)
	}
}

// TestInterruptOrphanStreamsLeavesLiveTurnHeadAlone pins that the sweep never
// touches the head of the turn that currently owns the lease.
func TestInterruptOrphanStreamsLeavesLiveTurnHeadAlone(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	liveID := beginStream(t, env, conversation.ID, writer, 101)
	sealed, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, writer, recoveryErrorPart())
	if err != nil || sealed != 0 {
		t.Fatalf("sweep with only the live head = %d (%v), want 0", sealed, err)
	}
	head := env.repo.messages[conversation.ID][0]
	if head.ID != liveID || head.StreamState != version.MessageStreamStateStreaming || len(head.Parts) != 0 {
		t.Fatalf("the live turn's head was modified: %#v", head)
	}
}

// TestInterruptOrphanStreamsRequiresLiveLease pins the fence: an idle/expired
// conversation or a foreign turn can never seal another writer's head.
func TestInterruptOrphanStreamsRequiresLiveLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}

	// No lease at all.
	if _, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, writer, recoveryErrorPart()); err == nil {
		t.Fatal("sweep without a lease was accepted")
	}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	beginStream(t, env, conversation.ID, writer, 101)

	// A different turn that does not hold the live lease.
	foreign := versionapp.Writer{TurnID: 202, Agent: "design_host"}
	if _, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, foreign, recoveryErrorPart()); err == nil {
		t.Fatal("a foreign turn sealed the live writer's head")
	}

	// A non-error part is rejected before any write.
	if _, err := env.svc.InterruptOrphanStreams(requestContext(), conversation.ID, writer,
		version.MessagePart{Type: version.MessagePartText, Text: "伪造成功"}); err == nil {
		t.Fatal("a non-error recovery part was accepted")
	}
	head := env.repo.messages[conversation.ID][0]
	if head.StreamState != version.MessageStreamStateStreaming {
		t.Fatalf("a rejected sweep changed the head: %#v", head)
	}
}
