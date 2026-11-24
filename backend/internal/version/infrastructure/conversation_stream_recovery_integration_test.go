//go:build integration

package versionpg_test

// v0.2.4-D T6: real-Postgres proof of the orphaned-stream recovery sweep
// (SC-D4 crash path). The lease holder seals the heads a dead Host turn left
// streaming, preserving their accumulated parts and appending the error part at
// the next contiguous seq; the fence, the idempotency and the "never touch the
// live turn's head" rule are pinned against the real SQL.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestConversationStreamInterruptOrphansIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	errorPart := version.MessagePart{
		Type: version.MessagePartError, Code: "stream_interrupted",
		Message: "连接中断，本条回复未完成。", Retryable: false,
	}

	// No lease: the sweep is refused and writes nothing.
	if _, err := repo.InterruptOrphanStreams(ctx, conversation.ID, 101, "clarification_host", errorPart, now); !errors.Is(err, version.ErrNotConversationWriter) {
		t.Fatalf("lease-less sweep err = %v, want ErrNotConversationWriter", err)
	}

	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	orphan := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, orphan, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, orphan.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "崩溃前已显示的内容"}, now); err != nil {
		t.Fatalf("UpdateStreamPart: %v", err)
	}

	// A foreign turn cannot seal the live writer's head.
	if _, err := repo.InterruptOrphanStreams(ctx, conversation.ID, 202, "design_host", errorPart, now); !errors.Is(err, version.ErrConversationBusy) {
		t.Fatalf("foreign sweep err = %v, want ErrConversationBusy", err)
	}
	// The live turn's own head is never an orphan.
	sealed, err := repo.InterruptOrphanStreams(ctx, conversation.ID, 101, "clarification_host", errorPart, now)
	if err != nil || len(sealed) != 0 {
		t.Fatalf("self sweep = %d (%v), want 0", len(sealed), err)
	}

	// The crashed turn stopped heartbeating; the next turn takes the lease over.
	takeover := leaseUntil.Add(time.Second)
	if err := repo.AcquireWriter(ctx, conversation.ID, 202, "clarification_host", takeover, takeover.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("takeover AcquireWriter: %v", err)
	}
	sealed, err = repo.InterruptOrphanStreams(ctx, conversation.ID, 202, "clarification_host", errorPart, takeover)
	if err != nil || len(sealed) != 1 || sealed[0].ID != orphan.ID {
		t.Fatalf("orphan sweep = %#v (%v), want the dead turn's head", sealed, err)
	}
	state, parts := readStreamHead(t, pool, orphan.ID)
	if state != version.MessageStreamStateInterrupted {
		t.Fatalf("orphan stream_state = %q, want interrupted", state)
	}
	if len(parts) != 2 || parts[0].Type != version.MessagePartText || parts[0].Text != "崩溃前已显示的内容" ||
		parts[1].Type != version.MessagePartError || parts[1].Code != "stream_interrupted" {
		t.Fatalf("orphan head parts = %#v, want preserved text + appended error part", parts)
	}
	if count := countStreamPartRows(t, pool, orphan.ID); count != 2 {
		t.Fatalf("orphan part rows = %d, want 2", count)
	}
	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM conversation_message_part WHERE message_id = $1 AND seq = 2`, orphan.ID).Scan(&payload); err != nil {
		t.Fatalf("read recovery error part row: %v", err)
	}
	var stored version.MessagePart
	if err := json.Unmarshal(payload, &stored); err != nil || stored.Code != "stream_interrupted" {
		t.Fatalf("recovery part row = %s (%v), want the stream_interrupted error code", payload, err)
	}

	// Idempotent: a repeated sweep seals nothing.
	sealed, err = repo.InterruptOrphanStreams(ctx, conversation.ID, 202, "clarification_host", errorPart, takeover)
	if err != nil || len(sealed) != 0 {
		t.Fatalf("repeated sweep = %d (%v), want 0", len(sealed), err)
	}

	// The new turn's own head stays streaming through a sweep.
	live := streamHeadMessage(ids, conversation.ID, 202, "clarification_host", takeover)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, live, 202, "clarification_host", takeover, takeover.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("live Begin: %v", err)
	}
	sealed, err = repo.InterruptOrphanStreams(ctx, conversation.ID, 202, "clarification_host", errorPart, takeover)
	if err != nil || len(sealed) != 0 {
		t.Fatalf("sweep with the live head = %d (%v), want 0", len(sealed), err)
	}
	if state, _ := readStreamHead(t, pool, live.ID); state != version.MessageStreamStateStreaming {
		t.Fatalf("live head stream_state = %q, want streaming", state)
	}

	// A non-error recovery part is refused before any write.
	if _, err := repo.InterruptOrphanStreams(ctx, conversation.ID, 202, "clarification_host",
		version.MessagePart{Type: version.MessagePartText, Text: "伪造成功"}, takeover); err == nil {
		t.Fatal("a non-error recovery part was accepted")
	}
}
