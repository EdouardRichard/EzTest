//go:build integration

package versionpg_test

// v0.2.4-H T12 (H-E2) PER-HEAD tolerance, real Postgres.
//
// InterruptOrphanStreams used to validate every orphan head before writing any of
// them, so ONE head the validator refuses aborted the whole transaction and the
// conversation kept EVERY unfinished bubble. The bad head is injected directly
// into the stored row (MaxMessageStreamParts parts, so the appended error part
// exceeds the limit): that is the exact shape the cap relation pin keeps
// unreachable from the runner, and the only way to exercise the tolerance branch
// against the real SQL.
//
// Expectations come from the stream-state contract (streaming -> interrupted,
// accumulated content preserved, error part at the next contiguous seq) and from
// version.MaxMessageStreamParts — never from observed repository output.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

func recoveryErrorPart() version.MessagePart {
	return version.MessagePart{
		Type: version.MessagePartError, Code: "stream_interrupted",
		Message: "连接中断，本条回复未完成。", Retryable: false,
	}
}

// injectAtLimitHead writes a stored head snapshot directly, bypassing the
// streaming write path. It is how a head the producer can no longer create (the
// validator's own part limit) is put back into the database so the recovery
// tolerance branch is exercised against real rows.
func injectAtLimitHead(t *testing.T, pool *pgxpool.Pool, messageID int64) {
	t.Helper()
	parts := make([]version.MessagePart, 0, version.MaxMessageStreamParts)
	for index := 0; index < version.MaxMessageStreamParts; index++ {
		parts = append(parts, version.MessagePart{Type: version.MessagePartText, Text: fmt.Sprintf("第%d段", index+1)})
	}
	encoded, err := json.Marshal(parts)
	if err != nil {
		t.Fatalf("encode at-limit parts: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`UPDATE conversation_message SET parts = $2::jsonb WHERE id = $1`, messageID, string(encoded)); err != nil {
		t.Fatalf("inject at-limit head: %v", err)
	}
	// The bad sample is proven bad against the domain authority, not assumed.
	if err := version.ValidateAssistantMessageParts(append(append([]version.MessagePart(nil), parts...), recoveryErrorPart())); err == nil {
		t.Fatal("the injected head plus the recovery part is accepted by the validator; the sample is not bad")
	}
}

// twoCrashedHeads opens one at-limit (bad) head under turn 101 and one normal
// (good) head under turn 102 — one head per Host turn, the turn key being the
// head's idempotency key — then hands the lease to turn 203 so BOTH are orphans.
// The bad head is created FIRST (lower id), so an all-or-nothing sweep fails
// before it ever reaches the good head.
func twoCrashedHeads(t *testing.T) (*pgxpool.Pool, *versionpg.ConversationRepository, int64, int64, int64, time.Time) {
	t.Helper()
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	bad := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, bad, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin(bad): %v", err)
	}
	injectAtLimitHead(t, pool, bad.ID)

	// Turn 101 crashed; turn 102 takes the lease over, streams, and crashes too.
	second := leaseUntil.Add(time.Second)
	secondUntil := second.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 102, "clarification_host", second, secondUntil); err != nil {
		t.Fatalf("second AcquireWriter: %v", err)
	}
	good := streamHeadMessage(ids, conversation.ID, 102, "clarification_host", second)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, good, 102, "clarification_host", second, secondUntil); err != nil {
		t.Fatalf("Begin(good): %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, good.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "崩溃前已显示的内容"}, second); err != nil {
		t.Fatalf("UpdateStreamPart: %v", err)
	}
	takeover := secondUntil.Add(time.Second)
	if err := repo.AcquireWriter(ctx, conversation.ID, 203, "clarification_host", takeover, takeover.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("takeover AcquireWriter: %v", err)
	}
	return pool, repo, conversation.ID, bad.ID, good.ID, takeover
}

func TestConversationStreamOrphanSweepPerHeadTolerantIntegration(t *testing.T) {
	pool, repo, conversationID, badID, goodID, takeover := twoCrashedHeads(t)
	ctx := context.Background()
	sealed, err := repo.InterruptOrphanStreams(ctx, conversationID, 203, "clarification_host", recoveryErrorPart(), takeover)
	if len(sealed) != 1 || sealed[0].ID != goodID {
		t.Fatalf("sealed heads = %#v (%v), want only the good head %d", sealed, err, goodID)
	}
	state, parts := readStreamHead(t, pool, goodID)
	if state != version.MessageStreamStateInterrupted {
		t.Fatalf("good head stream_state = %q, want interrupted (one bad head must not block it)", state)
	}
	if len(parts) != 2 || parts[0].Text != "崩溃前已显示的内容" || parts[1].Code != "stream_interrupted" {
		t.Fatalf("good head parts = %#v, want preserved text + appended error part", parts)
	}
	if count := countStreamPartRows(t, pool, goodID); count != 2 {
		t.Fatalf("good head part rows = %d, want 2", count)
	}
	badState, badParts := readStreamHead(t, pool, badID)
	if badState != version.MessageStreamStateStreaming {
		t.Fatalf("bad head stream_state = %q, want streaming (never a fabricated seal)", badState)
	}
	if len(badParts) != version.MaxMessageStreamParts {
		t.Fatalf("bad head parts = %d, want the %d it already stored (content never dropped)",
			len(badParts), version.MaxMessageStreamParts)
	}
	// A repeated sweep is still tolerant: the good head is terminal now, so
	// nothing new is sealed.
	again, _ := repo.InterruptOrphanStreams(ctx, conversationID, 203, "clarification_host", recoveryErrorPart(), takeover)
	if len(again) != 0 {
		t.Fatalf("repeated sweep sealed %d heads, want 0", len(again))
	}
}
