//go:build integration

package versionpg_test

// True-concurrency proof of the single-writer invariant (design §4.10
// R-B2-1..R-B2-5, B §5.1). The application tests use a fake repository and the
// lease integration test exercises the SQL sequentially; here two goroutines on
// separate pool connections race the real FOR UPDATE transactions and the test
// asserts the invariant on the rows that survive:
//   - one live lease owner appending while a foreign turn also appends yields
//     exactly one message and one typed busy/not-writer rejection;
//   - two turns taking over the same expired lease yield exactly one message (no
//     duplicate) and one busy rejection;
//   - background assistant text racing a Host acquire can never commit while the
//     lease is live (the locked write re-checks it after the acquire commits);
//   - AppendAssistant never becomes the writer of an idle conversation.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/jackc/pgx/v5/pgxpool"
)

// newWriterConcurrencyConversation creates a migrated test schema, one version
// row and one active conversation. The version row is inserted directly because
// the version-context Create SQL still references a column dropped by the W0
// schema tasks (hazard H1, owned by Task 29), which is unrelated to the lease
// under test.
func newWriterConcurrencyConversation(t *testing.T, ids *id.Snowflake) (*pgxpool.Pool, *versionpg.ConversationRepository, version.Conversation) {
	t.Helper()
	ctx := context.Background()
	pool := testutil.NewDB(t)
	repo := versionpg.NewConversationRepository(pool, ids)

	v := sampleVersion(ids, 42)
	if _, err := pool.Exec(ctx, `
		INSERT INTO version (id, title, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5)
	`, v.ID, "Writer lease concurrency", int64(42), testTime, testTime); err != nil {
		t.Fatalf("insert version: %v", err)
	}
	conversation := version.Conversation{
		ID: ids.Next(), VersionID: v.ID, Kind: "clarification", Title: "Chat",
		Status: version.ConversationActive, CreatedAt: testTime, UpdatedAt: testTime,
	}
	if err := repo.Create(ctx, conversation); err != nil {
		t.Fatalf("Create: %v", err)
	}
	return pool, repo, conversation
}

// textStreamPart builds the append-only part row that mirrors one text part.
func textStreamPart(text string, at time.Time) version.ConversationMessagePart {
	payload, err := json.Marshal(version.MessagePart{Type: version.MessagePartText, Text: text})
	if err != nil {
		panic(err)
	}
	return version.ConversationMessagePart{
		Seq: 1, Type: version.MessagePartText, Payload: payload, CreatedAt: at,
	}
}

// hostTurnMessage builds one Host turn assistant-text write with its reserved
// turn idempotency key.
func hostTurnMessage(ids *id.Snowflake, conversationID, turnID int64, agent, text string, at time.Time) version.Message {
	turnKey := version.HostTurnClientMessageID(turnID)
	return version.Message{
		ID:              ids.Next(),
		ConversationID:  conversationID,
		Role:            version.MessageRoleAssistant,
		Parts:           []version.MessagePart{{Type: version.MessagePartText, Text: text}},
		StreamParts:     []version.ConversationMessagePart{textStreamPart(text, at)},
		Refs:            []version.MessageRef{},
		TokenUsage:      map[string]any{},
		ClientMessageID: &turnKey,
		AuthorAgent:     agent,
		Kind:            version.MessageKindText,
		CreatedAt:       at,
	}
}

// backgroundMessage builds one background/server assistant-text write: no
// writer identity and no Host turn idempotency key.
func backgroundMessage(ids *id.Snowflake, conversationID int64, text string, at time.Time) version.Message {
	return version.Message{
		ID:             ids.Next(),
		ConversationID: conversationID,
		Role:           version.MessageRoleAssistant,
		Parts:          []version.MessagePart{{Type: version.MessagePartText, Text: text}},
		StreamParts:    []version.ConversationMessagePart{textStreamPart(text, at)},
		Refs:           []version.MessageRef{},
		TokenUsage:     map[string]any{},
		Kind:           version.MessageKindText,
		CreatedAt:      at,
	}
}

// waitForConversationRowWaiter blocks until the fenced background write is
// waiting on a row lock. It makes the concurrency test deterministic: the
// background append has provably reached its FOR UPDATE call before the lock
// holder commits. Row-level lock waits surface as transactionid waits (with no
// relation OID), so the probe looks at the waiting backend, not pg_locks.relation.
func waitForConversationRowWaiter(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
			SELECT EXISTS (
				SELECT 1 FROM pg_stat_activity
				WHERE datname = current_database()
				  AND state = 'active'
				  AND wait_event_type = 'Lock'
				  AND query ILIKE '%FROM conversation WHERE id = $1%'
			)
		`).Scan(&waiting); err != nil {
			t.Fatalf("poll conversation row lock: %v", err)
		}
		if waiting {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background append never blocked on the conversation row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestConversationWriterConcurrentAppendWhileLeaseLive pins the live-lease
// invariant under real concurrency: the owner turn and a foreign turn append at
// the same instant on two connections; exactly one message is created (the
// owner's) and the foreign writer receives the typed 409 conversation_busy.
func TestConversationWriterConcurrentAppendWhileLeaseLive(t *testing.T) {
	ids := newIDs(t)
	_, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()

	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}

	calls := []struct {
		message version.Message
		turnID  int64
		agent   string
	}{
		{hostTurnMessage(ids, conversation.ID, 101, "clarification_host", "owner reply", now), 101, "clarification_host"},
		{hostTurnMessage(ids, conversation.ID, 202, "design_host", "stolen reply", now), 202, "design_host"},
	}
	type outcome struct {
		created bool
		err     error
	}
	results := make([]outcome, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, call := range calls {
		wg.Add(1)
		go func(index int, call struct {
			message version.Message
			turnID  int64
			agent   string
		}) {
			defer wg.Done()
			<-start
			_, created, err := repo.AppendAssistantMessageAsWriter(
				ctx, conversation.ID, call.message, call.turnID, call.agent, now, leaseUntil)
			results[index] = outcome{created: created, err: err}
		}(index, call)
	}
	close(start)
	wg.Wait()

	var succeeded, busy int
	for index, result := range results {
		switch {
		case result.err == nil && result.created:
			succeeded++
		case errors.Is(result.err, version.ErrConversationBusy):
			busy++
		default:
			t.Fatalf("concurrent append %d = created:%v err:%v, want one success and one busy",
				index, result.created, result.err)
		}
	}
	if succeeded != 1 || busy != 1 {
		t.Fatalf("concurrent appends = %d success / %d busy, want exactly 1/1", succeeded, busy)
	}

	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %#v, %v, want exactly one", messages, err)
	}
	if messages[0].AuthorAgent != "clarification_host" {
		t.Fatalf("stored author = %q, want the lease owner", messages[0].AuthorAgent)
	}
}

// TestConversationWriterConcurrentTakeoverAfterExpiry pins crash recovery under
// real concurrency: two turns race to take over the same expired lease; the
// row lock serializes them, so exactly one message is created and the loser gets
// the typed busy rejection instead of a duplicate write.
func TestConversationWriterConcurrentTakeoverAfterExpiry(t *testing.T) {
	ids := newIDs(t)
	_, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()

	base := testTime
	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host",
		base, base.Add(version.ConversationWriterLeaseDuration)); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}

	after := base.Add(91 * time.Second)
	leaseUntil := after.Add(version.ConversationWriterLeaseDuration)
	calls := []struct {
		message version.Message
		turnID  int64
		agent   string
	}{
		{hostTurnMessage(ids, conversation.ID, 202, "design_host", "takeover design", after), 202, "design_host"},
		{hostTurnMessage(ids, conversation.ID, 303, "clarification_host", "takeover clarification", after), 303, "clarification_host"},
	}
	type outcome struct {
		created bool
		err     error
	}
	results := make([]outcome, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for index, call := range calls {
		wg.Add(1)
		go func(index int, call struct {
			message version.Message
			turnID  int64
			agent   string
		}) {
			defer wg.Done()
			<-start
			_, created, err := repo.AppendAssistantMessageAsWriter(
				ctx, conversation.ID, call.message, call.turnID, call.agent, after, leaseUntil)
			results[index] = outcome{created: created, err: err}
		}(index, call)
	}
	close(start)
	wg.Wait()

	var succeeded, busy int
	for index, result := range results {
		switch {
		case result.err == nil && result.created:
			succeeded++
		case errors.Is(result.err, version.ErrConversationBusy):
			busy++
		default:
			t.Fatalf("takeover append %d = created:%v err:%v, want one success and one busy",
				index, result.created, result.err)
		}
	}
	if succeeded != 1 || busy != 1 {
		t.Fatalf("concurrent takeovers = %d success / %d busy, want exactly 1/1", succeeded, busy)
	}

	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 1 {
		t.Fatalf("messages = %#v, %v, want exactly one (no duplicate takeover write)", messages, err)
	}
	stored, err := repo.GetByID(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.ActiveTurnID == nil || (*stored.ActiveTurnID != 202 && *stored.ActiveTurnID != 303) {
		t.Fatalf("stored lease = %#v, want the winning turn", stored)
	}
	if stored.ActiveAgent == nil || *stored.ActiveAgent != messages[0].AuthorAgent {
		t.Fatalf("stored author %q does not match the lease owner %v", messages[0].AuthorAgent, stored.ActiveAgent)
	}
	if stored.LeaseUntil == nil || !stored.LeaseUntil.Equal(leaseUntil) {
		t.Fatalf("stored lease_until = %#v, want the takeover expiry", stored.LeaseUntil)
	}
}

// TestConversationWriterBackgroundAppendFenceConcurrency pins the atomic
// background fence against a real race: a Host acquire holds the conversation
// row lock while the background write starts (so an application pre-read would
// still see an idle conversation); the background write blocks on FOR UPDATE,
// then re-checks the now-live lease after the acquire commits and is rejected
// with the typed not_writer. Nothing is written and the Host lease is intact.
// Once that lease expires, background writing is allowed again.
func TestConversationWriterBackgroundAppendFenceConcurrency(t *testing.T) {
	ids := newIDs(t)
	pool, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()

	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin acquire: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	// Hold the row lock while installing the same lease AcquireWriter would
	// write: the background append below starts before this commits.
	if _, err := tx.Exec(ctx, `
		UPDATE conversation
		SET active_turn_id = $2, active_agent = $3, lease_until = $4
		WHERE id = $1
	`, conversation.ID, int64(101), "clarification_host", leaseUntil); err != nil {
		t.Fatalf("hold lease: %v", err)
	}

	blocked := backgroundMessage(ids, conversation.ID, "background text", now)
	result := make(chan error, 1)
	started := make(chan struct{})
	go func() {
		close(started)
		result <- repo.AppendAssistantMessageAsBackground(ctx, conversation.ID, blocked, now)
	}()
	<-started
	waitForConversationRowWaiter(t, pool)

	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit acquire: %v", err)
	}
	if err := <-result; !errors.Is(err, version.ErrNotConversationWriter) {
		t.Fatalf("background append against a live lease = %v, want ErrNotConversationWriter", err)
	}
	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("background append stored a message while the lease was live: %#v, %v", messages, err)
	}
	stored, err := repo.GetByID(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.ActiveTurnID == nil || *stored.ActiveTurnID != 101 ||
		stored.LeaseUntil == nil || !stored.LeaseUntil.Equal(leaseUntil) {
		t.Fatalf("background fence disturbed the Host lease: %#v", stored)
	}

	// No live writer any more: the fenced background path is writable and still
	// leaves the (expired) lease row to the Host lifecycle.
	later := now.Add(91 * time.Second)
	allowed := backgroundMessage(ids, conversation.ID, "after expiry", later)
	if err := repo.AppendAssistantMessageAsBackground(ctx, conversation.ID, allowed, later); err != nil {
		t.Fatalf("background append after lease expiry: %v", err)
	}
	messages, err = repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].AuthorAgent != "" {
		t.Fatalf("messages after expiry = %#v, %v", messages, err)
	}
	stored, err = repo.GetByID(ctx, conversation.ID)
	if err != nil || stored.ActiveTurnID == nil || *stored.ActiveTurnID != 101 {
		t.Fatalf("background write touched the lease row: %#v, %v", stored, err)
	}
}

// TestConversationWriterAppendRequiresExplicitLease pins the repository rule
// against the real rows: an idle conversation is not silently claimed by an
// append; the explicit Acquire is required first.
func TestConversationWriterAppendRequiresExplicitLease(t *testing.T) {
	ids := newIDs(t)
	_, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()

	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)
	message := hostTurnMessage(ids, conversation.ID, 101, "clarification_host", "no lease", now)
	_, created, err := repo.AppendAssistantMessageAsWriter(
		ctx, conversation.ID, message, 101, "clarification_host", now, leaseUntil)
	if !errors.Is(err, version.ErrNotConversationWriter) || created {
		t.Fatalf("idle append = created:%v err:%v, want ErrNotConversationWriter", created, err)
	}
	if messages, err := repo.Messages(ctx, conversation.ID, 0, 10); err != nil || len(messages) != 0 {
		t.Fatalf("idle append stored a message: %#v, %v", messages, err)
	}
	stored, err := repo.GetByID(ctx, conversation.ID)
	if err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if stored.ActiveTurnID != nil || stored.ActiveAgent != nil || stored.LeaseUntil != nil {
		t.Fatalf("idle append silently claimed the lease: %#v", stored)
	}

	if err := repo.AcquireWriter(ctx, conversation.ID, 101, "clarification_host", now, leaseUntil); err != nil {
		t.Fatalf("AcquireWriter: %v", err)
	}
	if _, created, err := repo.AppendAssistantMessageAsWriter(
		ctx, conversation.ID, message, 101, "clarification_host", now, leaseUntil); err != nil || !created {
		t.Fatalf("append after explicit Acquire = created:%v err:%v", created, err)
	}
	if messages, err := repo.Messages(ctx, conversation.ID, 0, 10); err != nil || len(messages) != 1 {
		t.Fatalf("messages = %#v, %v", messages, err)
	}
}

// TestConversationWriterMessageKindValidation pins the write-path kind guard:
// an invalid kind is the typed 400 invalid_argument before any SQL, not a
// database CHECK violation surfacing as an internal 500.
func TestConversationWriterMessageKindValidation(t *testing.T) {
	ids := newIDs(t)
	_, repo, conversation := newWriterConcurrencyConversation(t, ids)
	ctx := context.Background()
	now := testTime
	leaseUntil := now.Add(version.ConversationWriterLeaseDuration)

	assertInvalid := func(label string, err error) {
		t.Helper()
		apiErr := platform.AsError(err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" {
			t.Fatalf("%s error = %#v, want typed 400 invalid_argument (not a DB 500)", label, apiErr)
		}
	}

	unconditional := hostTurnMessage(ids, conversation.ID, 0, "", "bad kind", now)
	unconditional.Kind = "bogus"
	assertInvalid("unconditional append", repo.AppendAssistantMessage(ctx, conversation.ID, unconditional))

	writerMessage := hostTurnMessage(ids, conversation.ID, 101, "clarification_host", "bad kind", now)
	writerMessage.Kind = "bogus"
	_, created, err := repo.AppendAssistantMessageAsWriter(
		ctx, conversation.ID, writerMessage, 101, "clarification_host", now, leaseUntil)
	if created {
		t.Fatal("invalid writer kind was stored")
	}
	assertInvalid("writer append", err)

	fenced := backgroundMessage(ids, conversation.ID, "bad kind", now)
	fenced.Kind = "bogus"
	assertInvalid("background append", repo.AppendAssistantMessageAsBackground(ctx, conversation.ID, fenced, now))

	if messages, err := repo.Messages(ctx, conversation.ID, 0, 10); err != nil || len(messages) != 0 {
		t.Fatalf("invalid kind stored a message: %#v, %v", messages, err)
	}

	// Empty is the legacy default and still normalizes to text.
	legacy := backgroundMessage(ids, conversation.ID, "legacy default", now)
	legacy.Kind = ""
	if err := repo.AppendAssistantMessageAsBackground(ctx, conversation.ID, legacy, now); err != nil {
		t.Fatalf("legacy empty kind append: %v", err)
	}
	messages, err := repo.Messages(ctx, conversation.ID, 0, 10)
	if err != nil || len(messages) != 1 || messages[0].Kind != version.MessageKindText {
		t.Fatalf("legacy kind = %#v, %v, want normalized text", messages, err)
	}
}
