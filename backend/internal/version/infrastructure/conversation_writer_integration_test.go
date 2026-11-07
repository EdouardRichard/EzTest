//go:build integration

package versionpg_test

// PostgreSQL proof of the single-writer lease SQL (design §4.10 R-B2-1..R-B2-5,
// B §5.1). The application-level tests use a fake repository; this test pins the
// real compare-and-set/heartbeat/append transaction against the DB constraints.

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

func TestConversationWriterLeaseIntegration(t *testing.T) {
	pool := testutil.NewDB(t)
	ids := newIDs(t)
	repo := versionpg.NewConversationRepository(pool, ids)
	ctx := context.Background()

	// Insert the version row directly: the version-context Create SQL still
	// references columns dropped by the W0 schema tasks (hazard H1, owned by
	// Task 29), which is unrelated to the conversation lease under test.
	v := sampleVersion(ids, 42)
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
	`, v.ID, "Writer lease", int64(42), testTime, testTime); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}

	base := testTime
	now := base
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	stored, _, err := repo.Get(ctx, v.ID, conversation.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.ActiveTurnID == nil || *stored.ActiveTurnID != 101 ||
		stored.ActiveAgent == nil || *stored.ActiveAgent != "clarification_host" ||
		stored.LeaseUntil == nil || !stored.LeaseUntil.Equal(leaseUntil) {
		t.Fatalf("stored lease = %#v", stored)
	}

	// A second live writer is rejected by the atomic UPDATE.
	if err := repo.AcquireWriter(ctx, conversation.ID, 202, "design_host", now, leaseUntil); !errors.Is(err, version.ErrConversationBusy) {
		t.Fatalf("second AcquireWriter = %v, want ErrConversationBusy", err)
	}

	// The append transaction rejects the same second writer, and stores nothing.
	writeMessage := func(turnID int64, agent, text string, at time.Time) version.Message {
		turnKey := version.HostTurnClientMessageID(turnID)
		return version.Message{
			ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleAssistant,
			Parts:           []version.MessagePart{{Type: version.MessagePartText, Text: text}},
			StreamParts:     []version.ConversationMessagePart{{Seq: 1, Type: version.MessagePartText, Payload: json.RawMessage(`{"type":"text","text":"` + text + `"}`), CreatedAt: at}},
			Refs:            []version.MessageRef{},
			TokenUsage:      map[string]any{},
			ClientMessageID: &turnKey,
			AuthorAgent:     agent,
			Kind:            version.MessageKindText,
			CreatedAt:       at,
		}
	}
	_, created, err := repo.AppendAssistantMessageAsWriter(ctx, conversation.ID,
		writeMessage(202, "design_host", "stolen", now), 202, "design_host", now, leaseUntil)
	if !errors.Is(err, version.ErrConversationBusy) || created {
		t.Fatalf("concurrent append = created:%v err:%v, want busy", created, err)
	}
	if messages, err := repo.Messages(ctx, conversation.ID, 0, 10); err != nil || len(messages) != 0 {
		t.Fatalf("messages after blocked append = %#v, %v", messages, err)
	}

	// Crash recovery: once the 90s lease expired the next Host takes it over and
	// appends in the same transaction.
	recoveredAt := base.Add(91 * time.Second)
	recoveredLease := recoveredAt.Add(version.ConversationWriterLeaseDuration)
	first := writeMessage(202, "design_host", "recovered", recoveredAt)
	storedMessage, created, err := repo.AppendAssistantMessageAsWriter(ctx, conversation.ID,
		first, 202, "design_host", recoveredAt, recoveredLease)
	if err != nil || !created || storedMessage != nil {
		t.Fatalf("append after expiry = %#v/%v/%v", storedMessage, created, err)
	}
	stored, _, err = repo.Get(ctx, v.ID, conversation.ID)
	if err != nil {
		t.Fatalf("Get after takeover: %v", err)
	}
	if stored.ActiveTurnID == nil || *stored.ActiveTurnID != 202 ||
		stored.ActiveAgent == nil || *stored.ActiveAgent != "design_host" ||
		stored.LeaseUntil == nil || !stored.LeaseUntil.Equal(recoveredLease) {
		t.Fatalf("takeover lease = %#v", stored)
	}
	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
	if messages[0].AuthorAgent != "design_host" || messages[0].Kind != version.MessageKindText {
		t.Fatalf("stored author/kind = %q/%q", messages[0].AuthorAgent, messages[0].Kind)
	}
	if messages[0].ClientMessageID == nil || *messages[0].ClientMessageID != version.HostTurnClientMessageID(202) {
		t.Fatalf("stored idempotency key = %#v", messages[0].ClientMessageID)
	}
	parts, err := repo.MessageParts(ctx, v.ID, conversation.ID, messages[0].ID)
	if err != nil || len(parts) != 1 || parts[0].Type != version.MessagePartText {
		t.Fatalf("stream parts = %#v, %v", parts, err)
	}

	// Idempotent retry of the same turn returns the stored message.
	retry := writeMessage(202, "design_host", "recovered", recoveredAt)
	existing, created, err := repo.AppendAssistantMessageAsWriter(ctx, conversation.ID,
		retry, 202, "design_host", recoveredAt, recoveredLease)
	if err != nil || created || existing == nil || existing.ID != messages[0].ID {
		t.Fatalf("idempotent retry = %#v/%v/%v", existing, created, err)
	}
	if all, err := repo.Messages(ctx, conversation.ID, 0, 10); err != nil || len(all) != 1 {
		t.Fatalf("retry duplicated the message: %#v, %v", all, err)
	}

	// Heartbeat renews the same owner and fences a foreign turn.
	renewedAt := recoveredAt.Add(60 * time.Second)
	renewedLease := renewedAt.Add(version.ConversationWriterLeaseDuration)
	if err := repo.RenewWriterLease(ctx, conversation.ID, 202, "design_host", renewedAt, renewedLease); err != nil {
		t.Fatalf("RenewWriterLease: %v", err)
	}
	if err := repo.RenewWriterLease(ctx, conversation.ID, 101, "clarification_host", renewedAt, renewedLease); !errors.Is(err, version.ErrNotConversationWriter) {
		t.Fatalf("foreign heartbeat = %v, want ErrNotConversationWriter", err)
	}

	// Release clears the lease; the next Host can acquire it again.
	if err := repo.ReleaseWriter(ctx, conversation.ID, 202, "design_host"); err != nil {
		t.Fatalf("ReleaseWriter: %v", err)
	}
	stored, _, err = repo.Get(ctx, v.ID, conversation.ID)
	if err != nil || stored.ActiveTurnID != nil || stored.ActiveAgent != nil || stored.LeaseUntil != nil {
		t.Fatalf("lease after release = %#v, %v", stored, err)
	}
	if err := repo.ReleaseWriter(ctx, conversation.ID, 202, "design_host"); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	if err := repo.AcquireWriter(ctx, conversation.ID, 303, "clarification_host", renewedAt, renewedLease); err != nil {
		t.Fatalf("AcquireWriter after release: %v", err)
	}

	// The legacy user/background insert paths keep working with the new
	// author_agent/kind columns (normalized to text when unset).
	userKey := "client-writer-1"
	userMessage := version.Message{
		ID: ids.Next(), ConversationID: conversation.ID, Role: version.MessageRoleUser,
		Parts: []version.MessagePart{{Type: version.MessagePartText, Text: "queued"}},
		Refs:  []version.MessageRef{}, TokenUsage: map[string]any{},
		ClientMessageID: &userKey, CreatedAt: renewedAt.Add(time.Minute),
	}
	if _, created, err := repo.PostUserMessage(ctx, conversation.ID, userMessage); err != nil || !created {
		t.Fatalf("PostUserMessage = %v/%v", created, err)
	}
	legacyAssistant := writeMessage(303, "", "server card", renewedAt.Add(2*time.Minute))
	legacyAssistant.Kind = version.MessageKindArtifact
	if err := repo.AppendAssistantMessage(ctx, conversation.ID, legacyAssistant); err != nil {
		t.Fatalf("AppendAssistantMessage: %v", err)
	}
	all, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(all) != 3 {
		t.Fatalf("messages after legacy inserts = %#v, %v", all, err)
	}
	if all[1].Kind != version.MessageKindText || all[2].Kind != version.MessageKindArtifact {
		t.Fatalf("legacy kinds = %q/%q", all[1].Kind, all[2].Kind)
	}

	// Missing conversations are typed 404s, never a silent lease row.
	if err := repo.AcquireWriter(ctx, 999999, 101, "clarification_host", now, leaseUntil); !errors.Is(err, version.ErrConversationNotFound) {
		t.Fatalf("missing conversation acquire = %v", err)
	}
	if err := repo.RenewWriterLease(ctx, 999999, 101, "clarification_host", now, leaseUntil); !errors.Is(err, version.ErrConversationNotFound) {
		t.Fatalf("missing conversation renew = %v", err)
	}
}
