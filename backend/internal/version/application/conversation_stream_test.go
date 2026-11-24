package versionapp_test

// v0.2.4-D T5: version conversation incremental stream pipeline unit tests
// (SC-D2, R-D2). The service exposes BeginAssistantStream (placeholder head via
// the single-writer lease), UpdateStreamPart (seq in-place full-snapshot update +
// conversation.message.delta increment event) and FinishAssistantStream /
// InterruptAssistantStream (terminal stream_state + full conversation.message
// frame). The fake repository below mirrors the PostgreSQL lease/row-lock
// contract; the real SQL is pinned in the infrastructure integration test.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fake repository: incremental stream methods ----------

func (f *fakeConversationRepo) BeginAssistantStream(_ context.Context, conversationID int64, message version.Message, turnID int64, agent string, now, leaseUntil time.Time) (*version.Message, bool, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return nil, false, version.ErrConversationNotFound
	}
	live := conversation.ActiveTurnID != nil && conversation.ActiveAgent != nil &&
		conversation.LeaseUntil != nil && !conversation.LeaseUntil.Before(now)
	switch {
	case live:
		if *conversation.ActiveTurnID != turnID || *conversation.ActiveAgent != agent {
			return nil, false, version.ErrConversationBusy
		}
	case conversation.ActiveTurnID != nil && conversation.LeaseUntil != nil && conversation.LeaseUntil.Before(now):
		conversation.ActiveTurnID = &turnID
		activeAgent := agent
		conversation.ActiveAgent = &activeAgent
		conversation.LeaseUntil = &leaseUntil
		f.conversations[conversationID] = conversation
	default:
		return nil, false, version.ErrNotConversationWriter
	}
	if message.ClientMessageID != nil {
		for _, existing := range f.messages[conversationID] {
			if existing.ClientMessageID != nil && *existing.ClientMessageID == *message.ClientMessageID {
				copied := existing
				return &copied, false, nil
			}
		}
	}
	message.StreamState = version.NormalizeMessageStreamState(message.StreamState)
	f.messages[conversationID] = append(f.messages[conversationID], message)
	if len(message.StreamParts) > 0 {
		f.messageParts[message.ID] = append([]version.ConversationMessagePart(nil), message.StreamParts...)
	}
	conversation = f.conversations[conversationID]
	conversation.MessageCount = len(f.messages[conversationID])
	conversation.LastMessageAt = &message.CreatedAt
	conversation.UpdatedAt = message.CreatedAt
	f.conversations[conversationID] = conversation
	return nil, true, nil
}

// streamLeaseCheck mirrors the repository's row-locked lease re-check for an
// in-place stream write: the live lease must belong to the message's owning
// Host turn (client_message_id == HostTurnClientMessageID(active_turn_id),
// author_agent == active_agent).
func (f *fakeConversationRepo) streamLeaseCheck(conversationID, messageID int64, now time.Time) (*version.Message, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return nil, version.ErrConversationNotFound
	}
	var message *version.Message
	for index := range f.messages[conversationID] {
		if f.messages[conversationID][index].ID == messageID {
			message = &f.messages[conversationID][index]
			break
		}
	}
	if message == nil || message.Role != version.MessageRoleAssistant {
		return nil, version.ErrMessageNotFound
	}
	live := conversation.ActiveTurnID != nil && conversation.ActiveAgent != nil &&
		conversation.LeaseUntil != nil && !conversation.LeaseUntil.Before(now)
	if !live {
		return nil, version.ErrNotConversationWriter
	}
	turnKey := version.HostTurnClientMessageID(*conversation.ActiveTurnID)
	if message.ClientMessageID == nil || *message.ClientMessageID != turnKey ||
		message.AuthorAgent != *conversation.ActiveAgent {
		return nil, version.ErrConversationBusy
	}
	return message, nil
}

func (f *fakeConversationRepo) UpdateStreamPart(_ context.Context, conversationID, messageID int64, seq int, part version.MessagePart, now time.Time) (json.RawMessage, error) {
	message, err := f.streamLeaseCheck(conversationID, messageID, now)
	if err != nil {
		return nil, err
	}
	if message.StreamState != version.MessageStreamStateStreaming {
		return nil, version.ErrMessageStreamClosed
	}
	rows := f.messageParts[messageID]
	var previous json.RawMessage
	rowIndex := -1
	for index := range rows {
		if rows[index].Seq == seq {
			previous = rows[index].Payload
			rowIndex = index
			break
		}
	}
	payload, err := json.Marshal(part)
	if err != nil {
		return nil, err
	}
	if rowIndex >= 0 {
		rows[rowIndex].Payload = payload
		rows[rowIndex].Type = part.Type
	} else {
		rows = append(rows, version.ConversationMessagePart{Seq: seq, Type: part.Type, Payload: payload, CreatedAt: now})
	}
	f.messageParts[messageID] = rows
	message.Parts = replaceStreamPartBySeq(message.Parts, seq, part)
	return previous, nil
}

func (f *fakeConversationRepo) FinishAssistantStream(_ context.Context, conversationID, messageID int64, finalParts []version.MessagePart, streamState string, now time.Time) (version.Message, bool, error) {
	message, err := f.streamLeaseCheck(conversationID, messageID, now)
	if err != nil {
		return version.Message{}, false, err
	}
	if message.StreamState != version.MessageStreamStateStreaming {
		// Terminal heads are sticky; a retried finish is an idempotent no-op.
		return *message, false, nil
	}
	message.Parts = append([]version.MessagePart(nil), finalParts...)
	message.StreamState = streamState
	rows := make([]version.ConversationMessagePart, 0, len(finalParts))
	seq := 0
	for _, part := range finalParts {
		if !version.IsStreamingMessagePartType(part.Type) {
			continue
		}
		payload, err := json.Marshal(part)
		if err != nil {
			return version.Message{}, false, err
		}
		seq++
		rows = append(rows, version.ConversationMessagePart{Seq: seq, Type: part.Type, Payload: payload, CreatedAt: now})
	}
	f.messageParts[messageID] = rows
	return *message, true, nil
}

// replaceStreamPartBySeq mirrors the head-snapshot seq mapping: seq addresses the
// seq-th streaming-type part (1-based); a new seq appends.
func replaceStreamPartBySeq(parts []version.MessagePart, seq int, part version.MessagePart) []version.MessagePart {
	updated := append([]version.MessagePart(nil), parts...)
	streaming := 0
	for index := range updated {
		if !version.IsStreamingMessagePartType(updated[index].Type) {
			continue
		}
		streaming++
		if streaming == seq {
			updated[index] = part
			return updated
		}
	}
	return append(updated, part)
}

// ---------- helpers ----------

func beginStream(t *testing.T, env *leaseConversationEnv, conversationID int64, writer versionapp.Writer, runID int64) int64 {
	t.Helper()
	messageID, err := env.svc.BeginAssistantStream(requestContext(), conversationID, writer, runID)
	if err != nil {
		t.Fatalf("BeginAssistantStream: %v", err)
	}
	return messageID
}

func deltaEvents(env *leaseConversationEnv) []eventingapp.Event {
	var out []eventingapp.Event
	for _, event := range env.events.events {
		if event.Type == "conversation.message.delta" {
			out = append(out, event)
		}
	}
	return out
}

func decodeDelta(t *testing.T, event eventingapp.Event) versionapp.MessageDelta {
	t.Helper()
	raw, err := json.Marshal(event.Payload)
	if err != nil {
		t.Fatalf("marshal delta payload: %v", err)
	}
	var delta versionapp.MessageDelta
	if err := json.Unmarshal(raw, &delta); err != nil {
		t.Fatalf("decode delta payload %s: %v", raw, err)
	}
	return delta
}

func fullFrameFor(t *testing.T, env *leaseConversationEnv, messageID int64) eventingapp.Event {
	t.Helper()
	want := idString(messageID)
	var found *eventingapp.Event
	for index := range env.events.events {
		event := env.events.events[index]
		if event.Type == "conversation.message" && event.Payload["id"] == want {
			found = &env.events.events[index]
		}
	}
	if found == nil {
		t.Fatalf("conversation.message frame for %s missing: %#v", want, env.events.events)
	}
	return *found
}

// ---------- Begin ----------

func TestBeginAssistantStreamPlaceholderViaLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	messageID := beginStream(t, env, conversation.ID, writer, 101)
	stored := env.repo.messages[conversation.ID]
	if len(stored) != 1 || stored[0].ID != messageID {
		t.Fatalf("placeholder head = %#v, want one message %d", stored, messageID)
	}
	head := stored[0]
	if head.StreamState != version.MessageStreamStateStreaming {
		t.Fatalf("head stream_state = %q, want streaming", head.StreamState)
	}
	if head.Role != version.MessageRoleAssistant || head.AuthorAgent != writer.Agent || head.Kind != version.MessageKindText {
		t.Fatalf("head = %#v", head)
	}
	if len(head.Parts) != 0 {
		t.Fatalf("placeholder parts = %#v, want empty", head.Parts)
	}
	if head.ClientMessageID == nil || *head.ClientMessageID != version.HostTurnClientMessageID(writer.TurnID) {
		t.Fatalf("idempotency key = %#v", head.ClientMessageID)
	}
	if head.AgentRunID == nil || *head.AgentRunID != 101 {
		t.Fatalf("agent_run_id = %#v, want the lease turn 101", head.AgentRunID)
	}

	// The placeholder publishes a full frame so the existing frontend renders an
	// empty streaming bubble; stream_state marks it unfinished.
	frame := fullFrameFor(t, env, messageID)
	if frame.Channel != "version:"+idString(env.versionID) {
		t.Fatalf("frame channel = %q", frame.Channel)
	}
	if frame.Payload["stream_state"] != version.MessageStreamStateStreaming {
		t.Fatalf("frame stream_state = %#v, want streaming", frame.Payload["stream_state"])
	}
	if parts, ok := frame.Payload["parts"].([]any); !ok || len(parts) != 0 {
		t.Fatalf("frame parts = %#v, want empty", frame.Payload["parts"])
	}
}

func TestBeginAssistantStreamRequiresLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}

	// Idle conversation: Begin must not silently claim the lease.
	_, err := env.svc.BeginAssistantStream(requestContext(), conversation.ID, writer, 101)
	var notWriter *versionapp.NotWriterError
	if !errors.As(err, &notWriter) {
		t.Fatalf("idle Begin err = %v, want *NotWriterError", err)
	}
	if apiErr := apiError(t, err); apiErr.Status != http.StatusForbidden || apiErr.Code != versionapp.CodeNotWriter {
		t.Fatalf("idle Begin = %#v, want 403 not_writer", apiErr)
	}
	if len(env.repo.messages[conversation.ID]) != 0 {
		t.Fatalf("idle Begin stored a head: %#v", env.repo.messages[conversation.ID])
	}

	// A different live turn holds the lease: typed 409 busy.
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "design_host", 202); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	_, err = env.svc.BeginAssistantStream(requestContext(), conversation.ID, writer, 101)
	var busy *versionapp.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("foreign Begin err = %v, want *BusyError", err)
	}
	if apiErr := apiError(t, err); apiErr.Status != http.StatusConflict || apiErr.Code != versionapp.CodeConversationBusy {
		t.Fatalf("foreign Begin = %#v, want 409 conversation_busy", apiErr)
	}

	// A writer-less call is rejected before any repository write.
	_, err = env.svc.BeginAssistantStream(requestContext(), conversation.ID, versionapp.Writer{Agent: "clarification_host"}, 0)
	notWriter = nil
	if !errors.As(err, &notWriter) || notWriter.Reason == "" {
		t.Fatalf("lease-less Begin err = %v, want a reasoned *NotWriterError", err)
	}
}

func TestBeginAssistantStreamIdempotentPerTurn(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	first := beginStream(t, env, conversation.ID, writer, 101)
	second := beginStream(t, env, conversation.ID, writer, 101)
	if first != second {
		t.Fatalf("retried Begin created a second head: %d vs %d", first, second)
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("heads = %d, want one idempotent placeholder", len(env.repo.messages[conversation.ID]))
	}
	frames := 0
	for _, event := range env.events.events {
		if event.Type == "conversation.message" {
			frames++
		}
	}
	if frames != 1 {
		t.Fatalf("full frames = %d, want one (idempotent replay must not republish)", frames)
	}
}

// ---------- UpdateStreamPart ----------

func TestUpdateStreamPartTextIncrement(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hel"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hello"}); err != nil {
		t.Fatalf("second update: %v", err)
	}

	deltas := deltaEvents(env)
	if len(deltas) != 2 {
		t.Fatalf("delta events = %d, want 2: %#v", len(deltas), deltas)
	}
	first := decodeDelta(t, deltas[0])
	if first.MessageID != idString(messageID) || first.ConversationID != idString(conversation.ID) ||
		first.Seq != 1 || first.PartType != version.MessagePartText {
		t.Fatalf("first delta = %#v", first)
	}
	if first.Increment != "Hel" || first.Offset != 0 || len(first.Part) != 0 {
		t.Fatalf("first delta increment/offset/part = %q/%d/%s, want Hel/0/absent", first.Increment, first.Offset, first.Part)
	}
	second := decodeDelta(t, deltas[1])
	if second.Increment != "lo" || second.Offset != 3 || len(second.Part) != 0 {
		t.Fatalf("second delta = %#v, want increment lo offset 3", second)
	}
	if deltas[0].Channel != "version:"+idString(env.versionID) {
		t.Fatalf("delta channel = %q", deltas[0].Channel)
	}

	// Same seq updated in place: exactly one part row, head snapshot is the full
	// accumulated text.
	rows := env.repo.messageParts[messageID]
	if len(rows) != 1 || rows[0].Seq != 1 {
		t.Fatalf("part rows = %#v, want one row at seq 1 (no growth)", rows)
	}
	var rowPayload map[string]any
	if err := json.Unmarshal(rows[0].Payload, &rowPayload); err != nil {
		t.Fatalf("decode row payload: %v", err)
	}
	if rowPayload["text"] != "Hello" {
		t.Fatalf("row snapshot = %#v, want full Hello", rowPayload)
	}
	head := env.repo.messages[conversation.ID][0]
	if len(head.Parts) != 1 || head.Parts[0].Text != "Hello" {
		t.Fatalf("head parts = %#v, want one full-snapshot text part", head.Parts)
	}
}

func TestUpdateStreamPartUTF16Offset(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	// "你好" is two BMP runes (2 UTF-16 code units, 6 bytes); the offset must be
	// the UTF-16 length so the frontend's String.length dedup matches.
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "你好"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "你好世界"}); err != nil {
		t.Fatalf("second update: %v", err)
	}
	deltas := deltaEvents(env)
	second := decodeDelta(t, deltas[1])
	if second.Offset != 2 || second.Increment != "世界" {
		t.Fatalf("delta = %#v, want offset 2 (UTF-16 units of 你好) increment 世界", second)
	}
}

// TestUpdateStreamPartUTF16OffsetAstralPlane is the T14 astral discriminator: an
// astral rune in the prefix contributes 2 UTF-16 code units to the continuation's
// offset — never the rune count (1) nor the byte count (4). It pins the offset
// SEMANTICS through the real call site (textunit.UTF16Length), so it stays green
// under an m1-type duplicate regression and only reds if the meaning drifts.
func TestUpdateStreamPartUTF16OffsetAstralPlane(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	// "😀" is one astral rune: 2 UTF-16 units, 1 rune, 4 bytes.
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "😀"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "😀尾巴"}); err != nil {
		t.Fatalf("second update: %v", err)
	}
	deltas := deltaEvents(env)
	second := decodeDelta(t, deltas[1])
	if second.Offset != 2 || second.Increment != "尾巴" {
		t.Fatalf("delta = %#v, want offset 2 (UTF-16 units of 😀, not 1 rune / 4 bytes) increment 尾巴", second)
	}
}

func TestUpdateStreamPartThinkingToolCallError(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	// thinking grows by text increment (seq 1).
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartThinking, Text: "核对", DurationMS: 100}); err != nil {
		t.Fatalf("thinking update: %v", err)
	}
	// tool_call (seq 2) is a discrete snapshot replacement, never raw JSON fragments.
	toolCall := version.MessagePart{Type: version.MessagePartToolCall, CallID: "call_1",
		Name: "context.task_pack", Status: "running", Arguments: json.RawMessage(`{"a":1}`)}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 2, toolCall); err != nil {
		t.Fatalf("tool_call update: %v", err)
	}
	// error (seq 3) is a snapshot replacement.
	errPart := version.MessagePart{Type: version.MessagePartError, Code: "model_error",
		Message: "模型中断", Retryable: false}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 3, errPart); err != nil {
		t.Fatalf("error update: %v", err)
	}

	deltas := deltaEvents(env)
	if len(deltas) != 3 {
		t.Fatalf("deltas = %d, want 3", len(deltas))
	}
	thinking := decodeDelta(t, deltas[0])
	if thinking.PartType != version.MessagePartThinking || thinking.Increment != "核对" || thinking.Offset != 0 {
		t.Fatalf("thinking delta = %#v", thinking)
	}
	tool := decodeDelta(t, deltas[1])
	if tool.PartType != version.MessagePartToolCall || tool.Increment != "" || len(tool.Part) == 0 {
		t.Fatalf("tool_call delta = %#v, want part snapshot replacement", tool)
	}
	var toolSnapshot map[string]any
	if err := json.Unmarshal(tool.Part, &toolSnapshot); err != nil {
		t.Fatalf("decode tool part: %v", err)
	}
	if toolSnapshot["call_id"] != "call_1" || toolSnapshot["status"] != "running" {
		t.Fatalf("tool snapshot = %#v", toolSnapshot)
	}
	errDelta := decodeDelta(t, deltas[2])
	if errDelta.PartType != version.MessagePartError || len(errDelta.Part) == 0 {
		t.Fatalf("error delta = %#v, want part snapshot", errDelta)
	}
	var errSnapshot map[string]any
	if err := json.Unmarshal(errDelta.Part, &errSnapshot); err != nil {
		t.Fatalf("decode error part: %v", err)
	}
	if errSnapshot["code"] != "model_error" || errSnapshot["message"] != "模型中断" {
		t.Fatalf("error snapshot = %#v", errSnapshot)
	}
	if rows := env.repo.messageParts[messageID]; len(rows) != 3 {
		t.Fatalf("part rows = %#v, want three (seq 1/2/3)", rows)
	}
}

func TestUpdateStreamPartNonPrefixRewriteIsReplacement(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hello"}); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// A rewrite that is not an extension cannot be an append increment; it must
	// be an explicit full-snapshot replacement (no silent corruption).
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Goodbye"}); err != nil {
		t.Fatalf("rewrite update: %v", err)
	}
	deltas := deltaEvents(env)
	rewrite := decodeDelta(t, deltas[1])
	if rewrite.Increment != "" || len(rewrite.Part) == 0 {
		t.Fatalf("rewrite delta = %#v, want part replacement", rewrite)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(rewrite.Part, &snapshot); err != nil {
		t.Fatalf("decode rewrite part: %v", err)
	}
	if snapshot["text"] != "Goodbye" {
		t.Fatalf("rewrite snapshot = %#v", snapshot)
	}
}

// TestUpdateStreamPartSameSeqTypeChangeIsReplacement pins review fix #1 end to
// end: the repository accepts a same-seq part-type change, so the delta must be a
// full-snapshot replacement (never an append-from-empty that the frontend would
// skip or mis-apply against the stale different-type text).
func TestUpdateStreamPartSameSeqTypeChangeIsReplacement(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartThinking, Text: "核对退款时效", DurationMS: 100}); err != nil {
		t.Fatalf("thinking update: %v", err)
	}
	// Same seq, different type (thinking -> text). The new text is shorter than the
	// stale thinking text; an append frame would be skipped by the frontend.
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hi"}); err != nil {
		t.Fatalf("text update: %v", err)
	}
	deltas := deltaEvents(env)
	if len(deltas) != 2 {
		t.Fatalf("deltas = %d, want 2", len(deltas))
	}
	changed := decodeDelta(t, deltas[1])
	if changed.PartType != version.MessagePartText || changed.Increment != "" || len(changed.Part) == 0 {
		t.Fatalf("type-change delta = %#v, want replace mode (part snapshot, no increment)", changed)
	}
	var snapshot map[string]any
	if err := json.Unmarshal(changed.Part, &snapshot); err != nil {
		t.Fatalf("decode replace part: %v", err)
	}
	if snapshot["type"] != "text" || snapshot["text"] != "Hi" {
		t.Fatalf("replace snapshot = %#v, want full text Hi", snapshot)
	}
	// The stored row reflects the type change (DB truth), still one row.
	rows := env.repo.messageParts[messageID]
	if len(rows) != 1 || rows[0].Type != version.MessagePartText {
		t.Fatalf("rows = %#v, want one text row at seq 1", rows)
	}
}

func TestUpdateStreamPartSameSnapshotIdempotent(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	part := version.MessagePart{Type: version.MessagePartText, Text: "Hello"}
	for i := 0; i < 3; i++ {
		if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1, part); err != nil {
			t.Fatalf("update %d: %v", i, err)
		}
	}
	if rows := env.repo.messageParts[messageID]; len(rows) != 1 {
		t.Fatalf("part rows = %#v, want one (same-seq replay never grows rows)", rows)
	}
	// Replaying the identical snapshot yields an empty increment (no growth); the
	// delta is a replacement of the same value, never a duplicated suffix.
	deltas := deltaEvents(env)
	for _, event := range deltas[1:] {
		delta := decodeDelta(t, event)
		if delta.Increment != "" {
			t.Fatalf("replay delta carried a non-empty increment: %#v", delta)
		}
	}
}

func TestUpdateStreamPartRejectsNonWriterAndClosedHead(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	// A foreign live turn cannot update another turn's stream.
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "design_host", 202); err == nil {
		t.Fatal("second Acquire should have been busy")
	}
	// Force the lease to a different owner to model a takeover.
	env.advanceClock(91 * time.Second)
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "design_host", 202); err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "stolen"})
	if !errors.Is(err, version.ErrConversationBusy) && !errors.Is(err, version.ErrNotConversationWriter) {
		t.Fatalf("foreign update err = %v, want busy/not-writer", err)
	}

	// A closed (complete) head rejects late deltas.
	env2 := newLeaseConversationEnv(t)
	conv2 := env2.createConversation(t)
	w2 := versionapp.Writer{TurnID: 303, Agent: "clarification_host"}
	if _, err := env2.svc.Acquire(requestContext(), conv2.ID, w2.Agent, w2.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	mid2 := beginStream(t, env2, conv2.ID, w2, 303)
	if err := env2.svc.FinishAssistantStream(requestContext(), conv2.ID, mid2,
		[]version.MessagePart{{Type: version.MessagePartText, Text: "done"}}); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	err = env2.svc.UpdateStreamPart(requestContext(), conv2.ID, mid2, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "late"})
	// The service maps the closed-head sentinel to the typed 409 (the domain
	// sentinel itself is the repository-level signal, pinned in the integration test).
	if apiErr := apiError(t, err); apiErr.Status != http.StatusConflict || apiErr.Code != versionapp.CodeMessageStreamClosed {
		t.Fatalf("late update on complete head = %#v, want 409 message_stream_closed", apiErr)
	}
}

func TestUpdateStreamPartValidatesPartAndSeq(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)

	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 0,
		version.MessagePart{Type: version.MessagePartText, Text: "x"}); err == nil {
		t.Fatal("seq 0 accepted")
	}
	// image is snapshot-only (no conversation_message_part row).
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartImage, MIME: "image/png", DataRef: "x"}); err == nil {
		t.Fatal("non-streaming part type accepted")
	}
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: ""}); err == nil {
		t.Fatal("empty text part accepted")
	}
	if len(env.repo.messageParts[messageID]) != 0 {
		t.Fatalf("invalid updates stored rows: %#v", env.repo.messageParts[messageID])
	}
}

// ---------- Finish / Interrupt ----------

func TestFinishAssistantStreamCompleteFullFrame(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hel"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	finalParts := []version.MessagePart{
		{Type: version.MessagePartThinking, Text: "核对", DurationMS: 1200},
		{Type: version.MessagePartText, Text: "Hello"},
	}
	if err := env.svc.FinishAssistantStream(requestContext(), conversation.ID, messageID, finalParts); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	head := env.repo.messages[conversation.ID][0]
	if head.StreamState != version.MessageStreamStateComplete {
		t.Fatalf("head stream_state = %q, want complete", head.StreamState)
	}
	if len(head.Parts) != 2 || head.Parts[1].Text != "Hello" {
		t.Fatalf("head parts = %#v, want finalParts", head.Parts)
	}
	rows := env.repo.messageParts[messageID]
	if len(rows) != 2 || rows[0].Seq != 1 || rows[1].Seq != 2 {
		t.Fatalf("part rows = %#v, want reconciled seq 1/2", rows)
	}

	// The terminal full frame is the existing conversation.message contract plus
	// stream_state, so the current frontend renders the final message unchanged.
	frame := fullFrameFor(t, env, messageID)
	if frame.Payload["stream_state"] != version.MessageStreamStateComplete {
		t.Fatalf("frame stream_state = %#v, want complete", frame.Payload["stream_state"])
	}
	wire := eventParts(t, frame)
	if len(wire) != 2 || wire[0]["type"] != "thinking" || wire[1]["text"] != "Hello" {
		t.Fatalf("frame parts = %#v", wire)
	}
	if wire[1]["seq"] != 2 {
		t.Fatalf("frame text seq = %#v, want 2", wire[1]["seq"])
	}
}

func TestInterruptAssistantStream(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	if err := env.svc.UpdateStreamPart(requestContext(), conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "partial"}); err != nil {
		t.Fatalf("update: %v", err)
	}
	finalParts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "partial"},
		{Type: version.MessagePartError, Code: "interrupted", Message: "连接中断", Retryable: false},
	}
	if err := env.svc.InterruptAssistantStream(requestContext(), conversation.ID, messageID, finalParts); err != nil {
		t.Fatalf("Interrupt: %v", err)
	}
	head := env.repo.messages[conversation.ID][0]
	if head.StreamState != version.MessageStreamStateInterrupted {
		t.Fatalf("head stream_state = %q, want interrupted", head.StreamState)
	}
	if len(head.Parts) != 2 || head.Parts[0].Text != "partial" {
		t.Fatalf("interrupted head dropped accumulated content: %#v", head.Parts)
	}
	frame := fullFrameFor(t, env, messageID)
	if frame.Payload["stream_state"] != version.MessageStreamStateInterrupted {
		t.Fatalf("frame stream_state = %#v, want interrupted", frame.Payload["stream_state"])
	}
}

func TestFinishAssistantStreamIdempotent(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID := beginStream(t, env, conversation.ID, writer, 101)
	finalParts := []version.MessagePart{{Type: version.MessagePartText, Text: "done"}}
	if err := env.svc.FinishAssistantStream(requestContext(), conversation.ID, messageID, finalParts); err != nil {
		t.Fatalf("first Finish: %v", err)
	}
	if err := env.svc.FinishAssistantStream(requestContext(), conversation.ID, messageID, finalParts); err != nil {
		t.Fatalf("retried Finish: %v", err)
	}
	frames := 0
	for _, event := range env.events.events {
		if event.Type == "conversation.message" && event.Payload["id"] == idString(messageID) {
			frames++
		}
	}
	// Begin placeholder + one terminal frame; the idempotent retry republishes nothing.
	if frames != 2 {
		t.Fatalf("full frames for message = %d, want 2 (placeholder + terminal, no retry republish)", frames)
	}
}
