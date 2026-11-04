//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestConversationPartWriterIntegration pins the SQL writer against a real
// Postgres: the message head and its conversation_message_part rows commit in
// one transaction with contiguous 1-based seq, and a duplicate (message_id, seq)
// rolls the whole append back (uk_conversation_message_part_message_seq).
func TestConversationPartWriterIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 42)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Parts",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create conversation: %v", err)
	}

	parts := []version.MessagePart{
		{Type: "thinking", Text: "核对退款时效", DurationMS: 1200},
		{Type: "tool_call", CallID: "call_1", Name: "context.task_pack", Status: "done",
			Arguments: json.RawMessage(`{"a":1}`), Result: json.RawMessage(`{"ok":true}`)},
		{Type: "text", Text: "已核对退款规则。"},
	}
	message := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts:       parts,
		StreamParts: buildStreamParts(t, parts, testTime),
		Refs:        []version.MessageRef{},
		TokenUsage:  map[string]any{},
		CreatedAt:   testTime,
	}
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, message); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}

	stored, err := repo.MessageParts(ctx, v.ID, conversation.ID, message.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(stored) != 3 {
		t.Fatalf("stored parts = %#v", stored)
	}
	for index, part := range stored {
		if part.Seq != index+1 || part.Type != parts[index].Type {
			t.Fatalf("stored[%d] = %#v, want seq %d type %q", index, part, index+1, parts[index].Type)
		}
	}
	var payload map[string]any
	if err := json.Unmarshal(stored[1].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["call_id"] != "call_1" || payload["status"] != "done" {
		t.Fatalf("tool payload = %#v", payload)
	}

	// A conflicting batch must leave neither the head nor any part row behind.
	conflicting := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts:      []version.MessagePart{{Type: "text", Text: "conflict"}},
		StreamParts: []version.ConversationMessagePart{
			{Seq: 1, Type: "text", Payload: json.RawMessage(`{"type":"text","text":"a"}`), CreatedAt: testTime},
			{Seq: 1, Type: "text", Payload: json.RawMessage(`{"type":"text","text":"b"}`), CreatedAt: testTime},
		},
		Refs:       []version.MessageRef{},
		TokenUsage: map[string]any{},
		CreatedAt:  testTime,
	}
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, conflicting); err == nil {
		t.Fatal("duplicate (message_id, seq) was accepted")
	}
	if _, err := repo.MessageParts(ctx, v.ID, conversation.ID, conflicting.ID); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("conflicting message head survived the rollback: %v", err)
	}
}

func buildStreamParts(t *testing.T, parts []version.MessagePart, createdAt time.Time) []version.ConversationMessagePart {
	t.Helper()
	rows := make([]version.ConversationMessagePart, 0, len(parts))
	for index, part := range parts {
		payload, err := json.Marshal(part)
		if err != nil {
			t.Fatalf("encode part: %v", err)
		}
		rows = append(rows, version.ConversationMessagePart{
			Seq: index + 1, Type: part.Type, Payload: payload, CreatedAt: createdAt,
		})
	}
	return rows
}
