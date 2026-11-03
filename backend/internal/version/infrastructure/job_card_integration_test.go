//go:build integration

package versionpg_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
)

// TestJobCardUpsertIntegration pins the mutable-card SQL against a real
// Postgres: the job id lookup scopes the card to its version/conversation, and a
// progress/terminal snapshot rewrites the message's parts snapshot and its
// conversation_message_part payload in place (same message, same seq) instead of
// appending a second card.
func TestJobCardUpsertIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	versionRepo := versionpg.New(pool, ids)
	ctx := context.Background()

	v := sampleVersion(ids, 43)
	if err := versionRepo.Create(ctx, v, sampleBindings(ids, v.ID)); err != nil {
		t.Fatalf("create version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Job cards",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create conversation: %v", err)
	}

	cancelable := true
	start := version.MessagePart{
		Type: "job_card", JobID: 501, JobKind: "chat.turn",
		Status: "running", Current: 0, Total: 0, Cancelable: &cancelable,
	}
	message := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
		Parts:       []version.MessagePart{start},
		StreamParts: buildStreamParts(t, []version.MessagePart{start}, testTime),
		Refs:        []version.MessageRef{},
		TokenUsage:  map[string]any{},
		CreatedAt:   testTime,
	}
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, message); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}

	found, err := repo.FindJobCardMessage(ctx, v.ID, conversation.ID, 501)
	if err != nil {
		t.Fatalf("FindJobCardMessage: %v", err)
	}
	if found.ID != message.ID {
		t.Fatalf("found message = %d, want %d", found.ID, message.ID)
	}
	if _, err := repo.FindJobCardMessage(ctx, v.ID, conversation.ID, 502); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("missing card error = %v, want ErrMessageNotFound", err)
	}
	if _, err := repo.FindJobCardMessage(ctx, v.ID+1, conversation.ID, 501); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("foreign-version card error = %v, want ErrMessageNotFound", err)
	}

	progress := start
	progress.Status = "running"
	progress.Current = 3
	if err := repo.UpdateJobCardMessage(ctx, v.ID, conversation.ID, message.ID, 501, progress); err != nil {
		t.Fatalf("UpdateJobCardMessage(progress): %v", err)
	}
	stored, err := repo.MessageParts(ctx, v.ID, conversation.ID, message.ID)
	if err != nil {
		t.Fatalf("MessageParts: %v", err)
	}
	if len(stored) != 1 || stored[0].Seq != 1 || stored[0].Type != "job_card" {
		t.Fatalf("stream rows = %#v, want one seq=1 job_card row", stored)
	}
	var payload map[string]any
	if err := json.Unmarshal(stored[0].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload["status"] != "running" || payload["current"] != float64(3) {
		t.Fatalf("card payload = %#v, want the progress snapshot", payload)
	}
	// The message snapshot (the UI's history read path) is rewritten too.
	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(messages) != 1 || len(messages[0].Parts) != 1 || messages[0].Parts[0].Current != 3 {
		t.Fatalf("message snapshot = %#v, want the updated card", messages)
	}

	// The terminal snapshot replaces the same card; no new message appears.
	done := progress
	done.Status = "done"
	done.Current = 4
	done.Cancelable = nil
	if err := repo.UpdateJobCardMessage(ctx, v.ID, conversation.ID, message.ID, 501, done); err != nil {
		t.Fatalf("UpdateJobCardMessage(done): %v", err)
	}
	messages, err = repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("Messages after done: %v", err)
	}
	if len(messages) != 1 || messages[0].Parts[0].Status != "done" || messages[0].Parts[0].Current != 4 {
		t.Fatalf("terminal message snapshot = %#v", messages)
	}
	if messages[0].Parts[0].Cancelable != nil && *messages[0].Parts[0].Cancelable {
		t.Fatalf("terminal card is still cancelable: %#v", messages[0].Parts[0])
	}

	// A missing card id and a foreign version never touch the stored card.
	if err := repo.UpdateJobCardMessage(ctx, v.ID, conversation.ID, message.ID, 502, progress); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("missing-card update error = %v, want ErrMessageNotFound", err)
	}
	if err := repo.UpdateJobCardMessage(ctx, v.ID+1, conversation.ID, message.ID, 501, progress); !errors.Is(err, version.ErrMessageNotFound) {
		t.Fatalf("foreign-version update error = %v, want ErrMessageNotFound", err)
	}
}
