//go:build integration

package versionpg_test

// v0.2.4-D T5: real-Postgres proof of the version conversation incremental
// stream pipeline (SC-D2, R-D2). The placeholder head is created under the
// single-writer lease; UpdateStreamPart rewrites one (message_id, seq) part row
// and the head snapshot in place under FOR UPDATE (no row growth, concurrent
// same-seq updates idempotent); Finish/Interrupt set the terminal stream_state
// and reconcile the part rows. The stream_state CHECK is pinned against the Go
// domain authority (derived, never hand-copied).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

func streamHeadMessage(ids *id.Snowflake, conversationID, turnID int64, agent string, at time.Time) version.Message {
	turnKey := version.HostTurnClientMessageID(turnID)
	runID := turnID
	return version.Message{
		ID:              ids.Next(),
		ConversationID:  conversationID,
		Role:            version.MessageRoleAssistant,
		Parts:           []version.MessagePart{},
		Refs:            []version.MessageRef{},
		TokenUsage:      map[string]any{},
		ClientMessageID: &turnKey,
		AuthorAgent:     agent,
		AgentRunID:      &runID,
		Kind:            version.MessageKindText,
		StreamState:     version.MessageStreamStateStreaming,
		CreatedAt:       at,
	}
}

func countStreamPartRows(t *testing.T, pool *pgxpool.Pool, messageID int64) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM conversation_message_part WHERE message_id = $1`, messageID).Scan(&count); err != nil {
		t.Fatalf("count part rows: %v", err)
	}
	return count
}

func readStreamHead(t *testing.T, pool *pgxpool.Pool, messageID int64) (string, []version.MessagePart) {
	t.Helper()
	var state string
	var raw []byte
	if err := pool.QueryRow(context.Background(),
		`SELECT stream_state, parts FROM conversation_message WHERE id = $1`, messageID).Scan(&state, &raw); err != nil {
		t.Fatalf("read stream head: %v", err)
	}
	var parts []version.MessagePart
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &parts); err != nil {
			t.Fatalf("decode head parts: %v", err)
		}
	}
	return state, parts
}

func TestConversationStreamBeginPlaceholderIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)

	// Idle conversation: Begin must not silently claim the lease.
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, created, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); !errors.Is(err, version.ErrNotConversationWriter) || created {
		t.Fatalf("idle Begin = created:%v err:%v, want ErrNotConversationWriter", created, err)
	}

	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	_, created, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil)
	if err != nil || !created {
		t.Fatalf("Begin after Acquire = created:%v err:%v", created, err)
	}
	state, parts := readStreamHead(t, pool, head.ID)
	if state != version.MessageStreamStateStreaming || len(parts) != 0 {
		t.Fatalf("placeholder head = %q parts:%#v, want streaming/empty", state, parts)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 0 {
		t.Fatalf("placeholder part rows = %d, want 0", count)
	}

	// Idempotent replay: same turn key returns the stored head, created=false.
	replay := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	existing, created, err := repo.BeginAssistantStream(ctx, conversation.ID, replay, 101, "clarification_host", now, leaseUntil)
	if err != nil || created || existing == nil || existing.ID != head.ID {
		t.Fatalf("replay Begin = existing:%#v created:%v err:%v, want stored head %d", existing, created, err, head.ID)
	}

	// A different live turn is busy.
	foreign := streamHeadMessage(ids, conversation.ID, 202, "design_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, foreign, 202, "design_host", now, leaseUntil); !errors.Is(err, version.ErrConversationBusy) {
		t.Fatalf("foreign Begin err = %v, want ErrConversationBusy", err)
	}
}

func TestConversationStreamUpdateInPlaceIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// First update for seq 1 inserts the row; previous payload is absent.
	prev, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hel"}, now)
	if err != nil || len(prev) != 0 {
		t.Fatalf("first update prev = %s err = %v, want absent", prev, err)
	}
	// Second update for seq 1 rewrites in place; previous is the prior snapshot.
	prev, err = repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hello"}, now)
	if err != nil {
		t.Fatalf("second update: %v", err)
	}
	var prevPart version.MessagePart
	if err := json.Unmarshal(prev, &prevPart); err != nil || prevPart.Text != "Hel" {
		t.Fatalf("previous snapshot = %s (%v), want Hel", prev, err)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 1 {
		t.Fatalf("part rows after same-seq updates = %d, want 1 (no growth)", count)
	}
	var payload []byte
	if err := pool.QueryRow(ctx,
		`SELECT payload FROM conversation_message_part WHERE message_id = $1 AND seq = 1`, head.ID).Scan(&payload); err != nil {
		t.Fatalf("read part payload: %v", err)
	}
	var stored version.MessagePart
	if err := json.Unmarshal(payload, &stored); err != nil || stored.Text != "Hello" {
		t.Fatalf("stored snapshot = %s, want full Hello", payload)
	}
	state, parts := readStreamHead(t, pool, head.ID)
	if state != version.MessageStreamStateStreaming || len(parts) != 1 || parts[0].Text != "Hello" {
		t.Fatalf("head = %q parts:%#v, want streaming + full Hello", state, parts)
	}

	// A new seq appends exactly one more row.
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 2,
		version.MessagePart{Type: version.MessagePartThinking, Text: "核对", DurationMS: 50}, now); err != nil {
		t.Fatalf("seq 2 update: %v", err)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 2 {
		t.Fatalf("part rows after seq 2 = %d, want 2", count)
	}
}

// TestConversationStreamUpdateSeqContiguityIntegration pins the contiguity guard
// (review fix #2, DoD failure-path coverage): a seq that skips past the next
// contiguous slot is the typed 400 invalid_argument and writes no row; the
// contiguous seq still succeeds.
func TestConversationStreamUpdateSeqContiguityIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "a"}, now); err != nil {
		t.Fatalf("seq 1 update: %v", err)
	}
	// seq 3 skips seq 2 (streamingCount=1, so only seq 1 or 2 is legal).
	_, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 3,
		version.MessagePart{Type: version.MessagePartText, Text: "c"}, now)
	apiErr := platform.AsError(err)
	if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" {
		t.Fatalf("gap seq 3 = %#v, want typed 400 invalid_argument", apiErr)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 1 {
		t.Fatalf("part rows after rejected gap = %d, want 1 (no row written)", count)
	}
	// The contiguous seq 2 succeeds.
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 2,
		version.MessagePart{Type: version.MessagePartText, Text: "b"}, now); err != nil {
		t.Fatalf("contiguous seq 2 update: %v", err)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 2 {
		t.Fatalf("part rows after seq 2 = %d, want 2", count)
	}
}

func TestConversationStreamUpdateLeaseAndClosedIntegration(t *testing.T) {
	ids := newIDs(t)
	_, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// A foreign live turn cannot update the stream.
	if err := repo.AcquireWriter(ctx, conversation.ID, 202, "design_host", now, leaseUntil); !errors.Is(err, version.ErrConversationBusy) {
		t.Fatalf("foreign Acquire err = %v, want busy (lease still held)", err)
	}
	// Take over after expiry, then the original turn's update is fenced.
	after := now.Add(91 * time.Second)
	if err := repo.AcquireWriter(ctx, conversation.ID, 202, "design_host", after, after.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("takeover Acquire: %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "stolen"}, after); !errors.Is(err, version.ErrConversationBusy) {
		t.Fatalf("foreign update err = %v, want ErrConversationBusy", err)
	}

	// Released lease: no live writer, update rejected.
	if err := repo.ReleaseWriter(ctx, conversation.ID, 202, "design_host"); err != nil {
		t.Fatalf("ReleaseWriter: %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "late"}, after); !errors.Is(err, version.ErrNotConversationWriter) {
		t.Fatalf("update after release err = %v, want ErrNotConversationWriter", err)
	}

	// A complete head rejects late deltas (terminal sticky). Re-acquire as the
	// original head's owning turn (101) and finish that head complete.
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", after, after.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if _, _, err := repo.FinishAssistantStream(ctx, conversation.ID, head.ID,
		[]version.MessagePart{{Type: version.MessagePartText, Text: "done"}}, version.MessageStreamStateComplete, after); err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "late"}, after); !errors.Is(err, version.ErrMessageStreamClosed) {
		t.Fatalf("update on complete head err = %v, want ErrMessageStreamClosed", err)
	}
}

func TestConversationStreamConcurrentSameSeqIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}

	// Two connections race the same (message_id, seq) with the identical snapshot:
	// the row lock + unique index serialize them into one row (idempotent), never
	// a duplicate or a lost update.
	snapshot := version.MessagePart{Type: version.MessagePartText, Text: "Hello"}
	const writers = 2
	errs := make([]error, writers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = repo.UpdateStreamPart(ctx, conversation.ID, head.ID, 1, snapshot, now)
		}(i)
	}
	close(start)
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent update %d: %v", i, err)
		}
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 1 {
		t.Fatalf("part rows after concurrent same-seq updates = %d, want 1", count)
	}
	_, parts := readStreamHead(t, pool, head.ID)
	if len(parts) != 1 || parts[0].Text != "Hello" {
		t.Fatalf("head parts = %#v, want one Hello", parts)
	}
}

func TestConversationStreamFinishReconcilesIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	head := streamHeadMessage(ids, conversation.ID, 101, "clarification_host", now)
	if _, _, err := repo.BeginAssistantStream(ctx, conversation.ID, head, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("Begin: %v", err)
	}
	// Stream three parts, then finish with a different final snapshot: the part
	// rows must reconcile to finalParts (extras removed, payloads replaced).
	for seq, part := range []version.MessagePart{
		{Type: version.MessagePartText, Text: "partial"},
		{Type: version.MessagePartThinking, Text: "核对", DurationMS: 10},
		{Type: version.MessagePartToolCall, CallID: "c1", Name: "context.task_pack", Status: "running"},
	} {
		if _, err := repo.UpdateStreamPart(ctx, conversation.ID, head.ID, seq+1, part, now); err != nil {
			t.Fatalf("update seq %d: %v", seq+1, err)
		}
	}
	finalParts := []version.MessagePart{
		{Type: version.MessagePartText, Text: "partial"},
		{Type: version.MessagePartError, Code: "interrupted", Message: "连接中断"},
	}
	if _, _, err := repo.FinishAssistantStream(ctx, conversation.ID, head.ID, finalParts, version.MessageStreamStateInterrupted, now); err != nil {
		t.Fatalf("Interrupt finish: %v", err)
	}
	state, parts := readStreamHead(t, pool, head.ID)
	if state != version.MessageStreamStateInterrupted || len(parts) != 2 {
		t.Fatalf("head = %q parts:%#v, want interrupted + 2 final parts", state, parts)
	}
	if count := countStreamPartRows(t, pool, head.ID); count != 2 {
		t.Fatalf("part rows after finish = %d, want 2 (reconciled to finalParts)", count)
	}
	rows, err := repo.MessageParts(ctx, conversation.VersionID, conversation.ID, head.ID)
	if err != nil || len(rows) != 2 || rows[0].Type != version.MessagePartText || rows[1].Type != version.MessagePartError {
		t.Fatalf("reconciled rows = %#v (%v), want text/error seq 1/2", rows, err)
	}
	// Retried finish is an idempotent no-op (terminal sticky, finished=false).
	if _, finished, err := repo.FinishAssistantStream(ctx, conversation.ID, head.ID, finalParts, version.MessageStreamStateInterrupted, now); err != nil || finished {
		t.Fatalf("retried finish = finished:%v err:%v, want false/nil", finished, err)
	}
}

// TestConversationStreamStateCheckConstraint pins the applied DB CHECK against
// the Go domain authority (derived set equality), the complete default for
// existing/batch rows, and fail-closed rejection of a non-enum value.
func TestConversationStreamStateCheckConstraint(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()

	var def string
	if err := pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conname = 'chk_conversation_message_stream_state'
		  AND contype = 'c' AND connamespace = current_schema()::regnamespace
	`).Scan(&def); err != nil {
		t.Fatalf("read chk_conversation_message_stream_state: %v", err)
	}
	var got []string
	for _, m := range quotedLiterals(def) {
		got = append(got, m)
	}
	want := version.MessageStreamStates()
	sort.Strings(got)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if strings.Join(got, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("stream_state CHECK set = %v, want domain authority %v (def %s)", got, wantSorted, def)
	}

	var columnDefault string
	if err := pool.QueryRow(ctx, `
		SELECT column_default FROM information_schema.columns
		WHERE table_schema = current_schema() AND table_name = 'conversation_message' AND column_name = 'stream_state'
	`).Scan(&columnDefault); err != nil {
		t.Fatalf("read stream_state default: %v", err)
	}
	if !strings.Contains(columnDefault, "'"+version.MessageStreamStateComplete+"'") {
		t.Fatalf("stream_state default = %q, want complete", columnDefault)
	}

	// A batch-path message (no explicit stream_state) defaults to complete.
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	batch := hostTurnMessage(ids, conversation.ID, 101, "clarification_host", "batch", now)
	if _, created, err := repo.AppendAssistantMessageAsWriter(ctx, conversation.ID, batch, 101, "clarification_host", now, leaseUntil); err != nil || !created {
		t.Fatalf("batch append = created:%v err:%v", created, err)
	}
	if state, _ := readStreamHead(t, pool, batch.ID); state != version.MessageStreamStateComplete {
		t.Fatalf("batch message stream_state = %q, want complete", state)
	}

	// A non-enum value is rejected by the CHECK (fail-closed).
	if _, err := pool.Exec(ctx, `UPDATE conversation_message SET stream_state = 'bogus' WHERE id = $1`, batch.ID); err == nil {
		t.Fatal("stream_state CHECK accepted a non-enum value")
	}
}

func quotedLiterals(def string) []string {
	var out []string
	rest := def
	for {
		open := strings.Index(rest, "'")
		if open < 0 {
			break
		}
		rest = rest[open+1:]
		closeIdx := strings.Index(rest, "'")
		if closeIdx < 0 {
			break
		}
		out = append(out, rest[:closeIdx])
		rest = rest[closeIdx+1:]
	}
	return out
}

// ---------- service + real repository delta pipeline ----------

type streamCapturePublisher struct{ events []eventingapp.Event }

func (p *streamCapturePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

type streamVersionReader struct{}

func (streamVersionReader) Get(_ context.Context, _ int64) (versionapp.VersionView, error) {
	return versionapp.VersionView{}, nil
}
func (streamVersionReader) MarkRunning(_ context.Context, _ int64) error { return nil }

type streamFixedClock struct{ at time.Time }

func (c streamFixedClock) Now() time.Time { return c.at }

func TestConversationStreamServiceDeltaIntegration(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	events := &streamCapturePublisher{}
	svc := versionapp.NewConversationService(repo, nil, nil, streamVersionReader{},
		streamFixedClock{at: testTime}, ids, events)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := svc.Acquire(ctx, conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	messageID, err := svc.BeginAssistantStream(ctx, conversation.ID, writer, 101)
	if err != nil {
		t.Fatalf("BeginAssistantStream: %v", err)
	}
	if err := svc.UpdateStreamPart(ctx, conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hel"}); err != nil {
		t.Fatalf("update 1: %v", err)
	}
	if err := svc.UpdateStreamPart(ctx, conversation.ID, messageID, 1,
		version.MessagePart{Type: version.MessagePartText, Text: "Hello"}); err != nil {
		t.Fatalf("update 2: %v", err)
	}
	if err := svc.FinishAssistantStream(ctx, conversation.ID, messageID,
		[]version.MessagePart{{Type: version.MessagePartText, Text: "Hello"}}); err != nil {
		t.Fatalf("Finish: %v", err)
	}

	var deltas []versionapp.MessageDelta
	var frames []eventingapp.Event
	for _, event := range events.events {
		switch event.Type {
		case "conversation.message.delta":
			raw, err := json.Marshal(event.Payload)
			if err != nil {
				t.Fatalf("marshal delta: %v", err)
			}
			var delta versionapp.MessageDelta
			if err := json.Unmarshal(raw, &delta); err != nil {
				t.Fatalf("decode delta: %v", err)
			}
			deltas = append(deltas, delta)
		case "conversation.message":
			frames = append(frames, event)
		}
	}
	if len(deltas) != 2 || deltas[0].Increment != "Hel" || deltas[1].Increment != "lo" || deltas[1].Offset != 3 {
		t.Fatalf("deltas = %#v, want Hel then lo@3", deltas)
	}
	if len(frames) != 2 {
		t.Fatalf("full frames = %d, want placeholder + terminal", len(frames))
	}
	if frames[0].Payload["stream_state"] != version.MessageStreamStateStreaming ||
		frames[1].Payload["stream_state"] != version.MessageStreamStateComplete {
		t.Fatalf("frame states = %#v / %#v", frames[0].Payload["stream_state"], frames[1].Payload["stream_state"])
	}
	if count := countStreamPartRows(t, pool, messageID); count != 1 {
		t.Fatalf("part rows = %d, want 1", count)
	}
}
