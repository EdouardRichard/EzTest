//go:build integration

package versionpg_test

// v0.2.4-H T12 (H-E2): the typed partial-recovery report and the RUN-SCOPED
// recovery entry, against real Postgres.
//
// A dead work item whose conversation never gets another turn has no lease holder
// to run InterruptOrphanStreams, so its head stays streaming forever.
// InterruptRunOrphanStreams seals the heads bound to one terminal agent run
// without requiring a live lease, and never touches a head of another run.
//
// Expectations come from the stream-state contract (streaming -> interrupted,
// accumulated content preserved, error part at the next contiguous seq) and from
// the domain's OrphanSealError report — never from observed repository output.

import (
	"context"
	"errors"
	"testing"
	"time"

	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

func TestConversationStreamOrphanSweepReportsUnsealableHeadIntegration(t *testing.T) {
	pool, repo, conversationID, badID, goodID, takeover := twoCrashedHeads(t)
	ctx := context.Background()
	sealed, err := repo.InterruptOrphanStreams(ctx, conversationID, 203, "clarification_host", recoveryErrorPart(), takeover)
	if err == nil {
		t.Fatal("the sweep hid the head it could not seal; a partial recovery must be reported")
	}
	if len(sealed) != 1 || sealed[0].ID != goodID {
		t.Fatalf("sealed heads = %#v (%v), want the good head alongside the reported failure", sealed, err)
	}
	var sealErr *version.OrphanSealError
	if !errors.As(err, &sealErr) {
		t.Fatalf("sweep error = %T (%v), want *version.OrphanSealError", err, err)
	}
	if len(sealErr.Failures) != 1 || sealErr.Failures[0].MessageID != badID || sealErr.Failures[0].Err == nil {
		t.Fatalf("seal failures = %#v, want one entry for head %d carrying its cause", sealErr.Failures, badID)
	}
	if ids := sealErr.MessageIDs(); len(ids) != 1 || ids[0] != badID {
		t.Fatalf("reported message ids = %#v, want [%d]", ids, badID)
	}
	if state, _ := readStreamHead(t, pool, badID); state != version.MessageStreamStateStreaming {
		t.Fatalf("reported head stream_state = %q, want streaming", state)
	}
}

func TestConversationStreamInterruptRunOrphansIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	errorPart := recoveryErrorPart()

	// One head of the dead run 101 and one of another run 202. No lease is ever
	// taken over after the crash: the conversation has no further turn, which is
	// exactly the case the lease-fenced sweep cannot reach.
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	dead := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, dead, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin(dead): %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, dead.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "崩溃前已显示的内容"}, now); err != nil {
		t.Fatalf("UpdateStreamPart(dead): %v", err)
	}
	second := leaseUntil.Add(time.Second)
	secondUntil := second.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 202, "clarification_host", second, secondUntil); err != nil {
		t.Fatalf("second AcquireWriter: %v", err)
	}
	other := streamHeadMessage(ids, conversation.ID, 202, "clarification_host", second)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, other, 202, "clarification_host", second, secondUntil); err != nil {
		t.Fatalf("Begin(other): %v", err)
	}

	// The lease has expired and nobody took it over. The run-scoped recovery does
	// not need a lease: the agent context owns the run's terminal truth.
	expired := secondUntil.Add(time.Second)
	sealed, err := repo.InterruptRunOrphanStreams(ctx, conversation.ID, 101, errorPart, expired)
	if err != nil || len(sealed) != 1 || sealed[0].ID != dead.ID {
		t.Fatalf("run-scoped sweep = %#v (%v), want the dead run's head", sealed, err)
	}
	state, parts := readStreamHead(t, pool, dead.ID)
	if state != version.MessageStreamStateInterrupted {
		t.Fatalf("dead head stream_state = %q, want interrupted", state)
	}
	if len(parts) != 2 || parts[0].Text != "崩溃前已显示的内容" || parts[1].Code != "stream_interrupted" {
		t.Fatalf("dead head parts = %#v, want preserved text + appended error part", parts)
	}
	if count := countStreamPartRows(t, pool, dead.ID); count != 2 {
		t.Fatalf("dead head part rows = %d, want 2", count)
	}
	if state, _ := readStreamHead(t, pool, other.ID); state != version.MessageStreamStateStreaming {
		t.Fatalf("another run's head stream_state = %q, want streaming (never swept)", state)
	}

	// Idempotent: a repeated recovery seals nothing.
	again, err := repo.InterruptRunOrphanStreams(ctx, conversation.ID, 101, errorPart, expired)
	if err != nil || len(again) != 0 {
		t.Fatalf("repeated run-scoped sweep = %d heads (%v), want 0", len(again), err)
	}

	// A run with no heads is an empty recovery, not an error.
	empty, err := repo.InterruptRunOrphanStreams(ctx, conversation.ID, 999, errorPart, expired)
	if err != nil || len(empty) != 0 {
		t.Fatalf("unknown run sweep = %d heads (%v), want 0", len(empty), err)
	}

	// A non-error recovery part is refused before any write.
	if _, err := repo.InterruptRunOrphanStreams(ctx, conversation.ID, 202,
		version.MessagePart{Type: version.MessagePartText, Text: "伪造成功"}, expired); err == nil {
		t.Fatal("a non-error recovery part was accepted")
	}
	if state, _ := readStreamHead(t, pool, other.ID); state != version.MessageStreamStateStreaming {
		t.Fatalf("refused recovery wrote the head: stream_state = %q", state)
	}

	// An unknown conversation is the domain's not-found, never a silent no-op.
	if _, err := repo.InterruptRunOrphanStreams(ctx, 999999, 101, errorPart, expired); !errors.Is(err, version.ErrConversationNotFound) {
		t.Fatalf("unknown conversation err = %v, want ErrConversationNotFound", err)
	}
}

// TestConversationStreamRunRecoveryIsPerHeadTolerantIntegration pins that the
// run-scoped entry shares the tolerance: a bad head of the run is reported while
// its good head is still sealed.
func TestConversationStreamRunRecoveryIsPerHeadTolerantIntegration(t *testing.T) {
	pool, repo, conversationID, badID, goodID, takeover := twoCrashedHeads(t)
	ctx := context.Background()
	// Both heads were opened by different turns but attributed to their own runs
	// by streamHeadMessage; re-attribute the good head to the bad head's run so
	// one run owns both a bad and a good head.
	var badRunID int64
	if err := pool.QueryRow(ctx, `SELECT agent_run_id FROM conversation_message WHERE id = $1`, badID).Scan(&badRunID); err != nil {
		t.Fatalf("read bad head run: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE conversation_message SET agent_run_id = $2 WHERE id = $1`, goodID, badRunID); err != nil {
		t.Fatalf("re-attribute good head: %v", err)
	}
	sealed, err := repo.InterruptRunOrphanStreams(ctx, conversationID, badRunID, recoveryErrorPart(), takeover)
	if len(sealed) != 1 || sealed[0].ID != goodID {
		t.Fatalf("run-scoped sealed heads = %#v (%v), want only the good head", sealed, err)
	}
	var sealErr *version.OrphanSealError
	if !errors.As(err, &sealErr) || len(sealErr.Failures) != 1 || sealErr.Failures[0].MessageID != badID {
		t.Fatalf("run-scoped error = %v, want an OrphanSealError naming head %d", err, badID)
	}
	if state, _ := readStreamHead(t, pool, goodID); state != version.MessageStreamStateInterrupted {
		t.Fatalf("good head stream_state = %q, want interrupted", state)
	}
	if state, _ := readStreamHead(t, pool, badID); state != version.MessageStreamStateStreaming {
		t.Fatalf("bad head stream_state = %q, want streaming", state)
	}
}
