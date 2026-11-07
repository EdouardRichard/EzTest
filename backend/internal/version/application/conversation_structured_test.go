package versionapp_test

// v0.22 W0 fix round 1 (Task 36 review Critical 1): the structured-message
// write API of the conversation service. The question.ask card used to be a
// direct SQL INSERT of a `question_card` part that the version domain rejected
// on read. These tests drive the production service path instead: writer lease
// + author, idempotent client_message_id, and a domain JSON round trip that
// simulates the persisted snapshot read.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func structuredQuestionPayload() json.RawMessage {
	return json.RawMessage(`{"question_id":"9001","batch_id":"qb-9001","questions":[{"id":"q1","text":"Which login method?","options":[{"id":"o1","label":"SMS"}],"allow_free_text":true}],"expires_at":"2026-11-06T08:00:00Z"}`)
}

// TestAppendStructuredQuestionRoundTripsThroughWriterLease is the end-to-end
// Critical 1 proof: the Host writer lease authorizes the card, the stored
// message is kind=question/role=assistant, a retried write is idempotent and a
// JSON round trip of the stored parts still validates as an assistant message
// (the version domain read path).
func TestAppendStructuredQuestionRoundTripsThroughWriterLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	const runID = int64(701)
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", runID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	part := version.MessagePart{Type: version.MessagePartQuestion, Question: structuredQuestionPayload()}
	input := versionapp.StructuredMessageInput{
		VersionID:       env.versionID,
		ConversationID:  conversation.ID,
		Kind:            version.MessageKindQuestion,
		Parts:           []version.MessagePart{part},
		ClientMessageID: "question-ask:9001",
		AgentRunID:      runID,
	}
	view, created, err := env.svc.AppendStructuredMessage(requestContext(), input)
	if err != nil {
		t.Fatalf("AppendStructuredMessage: %v", err)
	}
	if !created || view.ID <= 0 {
		t.Fatalf("created/view = %v/%#v", created, view)
	}
	if view.Role != version.MessageRoleAssistant {
		t.Fatalf("role = %q, want assistant", view.Role)
	}
	if view.ConversationID != conversation.ID {
		t.Fatalf("conversation = %d", view.ConversationID)
	}

	stored := env.repo.messages[conversation.ID]
	if len(stored) != 1 {
		t.Fatalf("stored messages = %d, want 1", len(stored))
	}
	message := stored[0]
	if message.Kind != version.MessageKindQuestion {
		t.Fatalf("kind = %q, want %q", message.Kind, version.MessageKindQuestion)
	}
	if message.AuthorAgent != "clarification_host" {
		t.Fatalf("author agent = %q, want the lease holder", message.AuthorAgent)
	}
	if message.ClientMessageID == nil || *message.ClientMessageID != "question-ask:9001" {
		t.Fatalf("client message id = %#v", message.ClientMessageID)
	}
	if message.AgentRunID == nil || *message.AgentRunID != runID {
		t.Fatalf("agent run = %#v", message.AgentRunID)
	}
	if len(message.Parts) != 1 || message.Parts[0].Type != version.MessagePartQuestion {
		t.Fatalf("stored parts = %#v", message.Parts)
	}

	// The stored snapshot is JSON in PostgreSQL; a read must unmarshal through
	// the version domain, which is exactly where the old question_card crashed.
	snapshot, err := json.Marshal(message.Parts)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	var reloaded []version.MessagePart
	if err := json.Unmarshal(snapshot, &reloaded); err != nil {
		t.Fatalf("read stored parts through the domain: %v", err)
	}
	if len(reloaded) != 1 || reloaded[0].Type != version.MessagePartQuestion {
		t.Fatalf("reloaded parts = %#v", reloaded)
	}
	if err := version.ValidateAssistantMessageParts(reloaded); err != nil {
		t.Fatalf("reloaded question card rejected: %v", err)
	}
	if !strings.Contains(string(reloaded[0].Question), `"question_id":"9001"`) {
		t.Fatalf("reloaded payload = %s", reloaded[0].Question)
	}

	// Retry of the same logical question is a no-op: same stored id, no second
	// row, no second write.
	retried, createdAgain, err := env.svc.AppendStructuredMessage(requestContext(), input)
	if err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	if createdAgain {
		t.Fatal("retried question card created a second message")
	}
	if retried.ID != view.ID {
		t.Fatalf("retry id = %d, want %d", retried.ID, view.ID)
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("retry duplicated the stored message: %d", len(env.repo.messages[conversation.ID]))
	}
}

// TestAppendStructuredQuestionRequiresWriterLease pins the write fence: only
// the live conversation writer (the run that owns the lease) can persist a
// question card.
func TestAppendStructuredQuestionRequiresWriterLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)

	part := version.MessagePart{Type: version.MessagePartQuestion, Question: structuredQuestionPayload()}
	input := versionapp.StructuredMessageInput{
		VersionID:       env.versionID,
		ConversationID:  conversation.ID,
		Kind:            version.MessageKindQuestion,
		Parts:           []version.MessagePart{part},
		ClientMessageID: "question-ask:9001",
		AgentRunID:      999,
	}
	if _, _, err := env.svc.AppendStructuredMessage(requestContext(), input); err == nil {
		t.Fatal("question card was written without a writer lease")
	}

	// A different live writer cannot write another run's question card.
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 701); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if _, _, err := env.svc.AppendStructuredMessage(requestContext(), input); err == nil {
		t.Fatal("a foreign agent run wrote the question card")
	}
}

// TestAppendStructuredAnswerUsesUserMessageAndDedupes pins the answer echo: it
// is a user/kind=answer message, idempotent by client_message_id, and never
// creates another chat.turn task (the answer is not a user prompt).
func TestAppendStructuredAnswerUsesUserMessageAndDedupes(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	env.queue.tasks = nil

	input := versionapp.StructuredMessageInput{
		VersionID:       env.versionID,
		ConversationID:  conversation.ID,
		Kind:            version.MessageKindAnswer,
		Parts:           []version.MessagePart{{Type: version.MessagePartText, Text: "SMS code, please."}},
		ClientMessageID: "question-answer:9001:1",
		ActorID:         42,
	}
	view, created, err := env.svc.AppendStructuredMessage(requestContext(), input)
	if err != nil {
		t.Fatalf("AppendStructuredMessage: %v", err)
	}
	if !created || view.ID <= 0 || view.Role != version.MessageRoleUser {
		t.Fatalf("answer created/view = %v/%#v", created, view)
	}
	stored := env.repo.messages[conversation.ID]
	if len(stored) != 1 || stored[0].Kind != version.MessageKindAnswer {
		t.Fatalf("stored answer = %#v", stored)
	}
	if len(env.queue.tasks) != 0 {
		t.Fatalf("answer echo enqueued tasks: %#v", env.queue.tasks)
	}

	_, createdAgain, err := env.svc.AppendStructuredMessage(requestContext(), input)
	if err != nil {
		t.Fatalf("answer retry: %v", err)
	}
	if createdAgain {
		t.Fatal("answer retry was not idempotent")
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("answer retry duplicated the message: %d", len(env.repo.messages[conversation.ID]))
	}
}

// TestAppendStructuredMessageRejectsUnknownKind keeps the structured API
// narrow: it only writes the two v0.22 question kinds.
func TestAppendStructuredMessageRejectsUnknownKind(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	_, _, err := env.svc.AppendStructuredMessage(requestContext(), versionapp.StructuredMessageInput{
		VersionID:      env.versionID,
		ConversationID: conversation.ID,
		Kind:           "system",
		Parts:          []version.MessagePart{{Type: version.MessagePartText, Text: "x"}},
	})
	if err == nil {
		t.Fatal("unsupported structured message kind accepted")
	}
	var apiErr interface{ Error() string }
	if !errors.As(err, &apiErr) {
		t.Fatalf("error = %v", err)
	}
}

var _ = context.Background
