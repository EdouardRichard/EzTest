package versionapp_test

// Single-writer lease tests (design §4.10 R-B2-1..R-B2-5, B §5.1).
//
// The writer tests live in their own file so they can inject a mutable clock
// (lease expiry/heartbeat need to move time forward) without changing the
// existing conversation fixtures. The fake repository methods that mirror the
// DB compare-and-set/lease SQL also live here.

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// ---------- fake writer repository (mirrors the PostgreSQL lease SQL) ----------

func (f *fakeConversationRepo) AcquireWriter(_ context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return version.ErrConversationNotFound
	}
	if conversation.ActiveTurnID != nil && conversation.LeaseUntil != nil && !conversation.LeaseUntil.Before(now) {
		sameOwner := conversation.ActiveAgent != nil && *conversation.ActiveAgent == agent &&
			*conversation.ActiveTurnID == turnID
		if !sameOwner {
			return version.ErrConversationBusy
		}
	}
	conversation.ActiveTurnID = &turnID
	activeAgent := agent
	conversation.ActiveAgent = &activeAgent
	conversation.LeaseUntil = &leaseUntil
	f.conversations[conversationID] = conversation
	return nil
}

func (f *fakeConversationRepo) RenewWriterLease(_ context.Context, conversationID, turnID int64, agent string, now, leaseUntil time.Time) error {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return version.ErrConversationNotFound
	}
	if conversation.ActiveTurnID == nil || conversation.ActiveAgent == nil ||
		*conversation.ActiveTurnID != turnID || *conversation.ActiveAgent != agent ||
		conversation.LeaseUntil == nil || conversation.LeaseUntil.Before(now) {
		return version.ErrNotConversationWriter
	}
	conversation.LeaseUntil = &leaseUntil
	f.conversations[conversationID] = conversation
	return nil
}

func (f *fakeConversationRepo) ReleaseWriter(_ context.Context, conversationID, turnID int64, agent string) error {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return version.ErrConversationNotFound
	}
	if conversation.ActiveTurnID == nil {
		return nil
	}
	if *conversation.ActiveTurnID != turnID || conversation.ActiveAgent == nil || *conversation.ActiveAgent != agent {
		return version.ErrNotConversationWriter
	}
	conversation.ActiveTurnID = nil
	conversation.ActiveAgent = nil
	conversation.LeaseUntil = nil
	f.conversations[conversationID] = conversation
	return nil
}

func (f *fakeConversationRepo) AppendAssistantMessageAsWriter(_ context.Context, conversationID int64, message version.Message, turnID int64, agent string, now, leaseUntil time.Time) (*version.Message, bool, error) {
	conversation, ok := f.conversations[conversationID]
	if !ok {
		return nil, false, version.ErrConversationNotFound
	}
	if conversation.ActiveTurnID != nil && conversation.ActiveAgent != nil &&
		conversation.LeaseUntil != nil && !conversation.LeaseUntil.Before(now) {
		if *conversation.ActiveTurnID != turnID || *conversation.ActiveAgent != agent {
			return nil, false, version.ErrConversationBusy
		}
	} else {
		conversation.ActiveTurnID = &turnID
		activeAgent := agent
		conversation.ActiveAgent = &activeAgent
		conversation.LeaseUntil = &leaseUntil
		f.conversations[conversationID] = conversation
	}
	if message.ClientMessageID != nil {
		for _, existing := range f.messages[conversationID] {
			if existing.ClientMessageID != nil && *existing.ClientMessageID == *message.ClientMessageID {
				copied := existing
				return &copied, false, nil
			}
		}
	}
	f.messages[conversationID] = append(f.messages[conversationID], message)
	conversation = f.conversations[conversationID]
	conversation.MessageCount = len(f.messages[conversationID])
	conversation.LastMessageAt = &message.CreatedAt
	conversation.UpdatedAt = message.CreatedAt
	f.conversations[conversationID] = conversation
	return nil, true, nil
}

// ---------- mutable-clock conversation environment ----------

// leaseClock is a mutable test clock: lease tests advance it to model a Host
// that stopped heartbeating (crash recovery) or kept heartbeating.
type leaseClock struct{ at time.Time }

func (c *leaseClock) Now() time.Time { return c.at }

type leaseConversationEnv struct {
	*conversationEnv
	clock *leaseClock
}

func newLeaseConversationEnv(t *testing.T) *leaseConversationEnv {
	t.Helper()
	base := newTestEnv(t)
	created := createVersion(t, base, "Conversation version")
	repo := newFakeConversationRepo()
	queue := &fakeQueue{}
	registrar := &fakeRegistrar{}
	events := &fakePublisher{}
	clk := &leaseClock{at: testTime}
	svc := versionapp.NewConversationService(
		repo, queue, registrar, base.svc, clk, &seqIDs{}, events,
	)
	return &leaseConversationEnv{
		conversationEnv: &conversationEnv{
			svc: svc, repo: repo, queue: queue, registrar: registrar,
			versionEnv: base, versionID: created.ID, events: events,
		},
		clock: clk,
	}
}

func (e *leaseConversationEnv) advanceClock(duration time.Duration) {
	e.clock.at = e.clock.at.Add(duration)
}

func (e *leaseConversationEnv) createConversation(t *testing.T) versionapp.ConversationView {
	t.Helper()
	conversation, err := e.svc.Create(requestContext(), e.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return conversation
}

func textParts(text string) []version.MessagePart {
	return []version.MessagePart{{Type: version.MessagePartText, Text: text}}
}

// TestConcurrentHostRun409 pins R-B2-3/B-1: while one Host turn holds the
// conversation lease a second Host cannot acquire it and cannot append, and the
// failure is the typed 409 conversation_busy rather than a duplicate message.
func TestConcurrentHostRun409(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)

	lease, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 101)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if lease.TurnID != 101 || lease.Agent != "clarification_host" ||
		!lease.ExpiresAt.Equal(testTime.Add(90*time.Second)) {
		t.Fatalf("lease = %#v, want 90s from the injected clock", lease)
	}

	_, err = env.svc.Acquire(requestContext(), conversation.ID, "design_host", 202)
	var busy *versionapp.BusyError
	if !errors.As(err, &busy) {
		t.Fatalf("second Acquire err = %v, want *BusyError", err)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusConflict || apiErr.Code != versionapp.CodeConversationBusy {
		t.Fatalf("second Acquire = %#v, want 409 conversation_busy", apiErr)
	}
	if busy.ActiveTurnID == nil || *busy.ActiveTurnID != 101 ||
		busy.ActiveAgent == nil || *busy.ActiveAgent != "clarification_host" {
		t.Fatalf("busy holder = %#v", busy)
	}

	// The losing Host run cannot append either, even with a reply pointer.
	replyTo := int64(7)
	_, err = env.svc.AppendAssistant(requestContext(), conversation.ID,
		versionapp.Writer{TurnID: 202, Agent: "design_host"}, textParts("hi"), 555, &replyTo)
	busy = nil
	if !errors.As(err, &busy) {
		t.Fatalf("concurrent append err = %v, want *BusyError", err)
	}
	if apiErr := apiError(t, err); apiErr.Status != http.StatusConflict || apiErr.Code != versionapp.CodeConversationBusy {
		t.Fatalf("concurrent append = %#v, want 409 conversation_busy", apiErr)
	}
	if len(env.repo.messages[conversation.ID]) != 0 {
		t.Fatalf("concurrent append stored a message: %#v", env.repo.messages[conversation.ID])
	}
}

// TestLeaseExpiryRecovers pins R-B2-5 crash recovery: after the previous Host's
// 90s lease expires the next Host may append and takes the lease over instead of
// leaving the conversation wedged forever.
func TestLeaseExpiryRecovers(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)

	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 101); err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	env.advanceClock(91 * time.Second)

	message, err := env.svc.AppendAssistant(requestContext(), conversation.ID,
		versionapp.Writer{TurnID: 202, Agent: "design_host"}, textParts("recovered"), 556, nil)
	if err != nil {
		t.Fatalf("append after lease expiry: %v", err)
	}
	if message.Role != version.MessageRoleAssistant {
		t.Fatalf("message = %#v", message)
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("messages = %#v", env.repo.messages[conversation.ID])
	}
	stored := env.repo.conversations[conversation.ID]
	if stored.ActiveTurnID == nil || *stored.ActiveTurnID != 202 ||
		stored.ActiveAgent == nil || *stored.ActiveAgent != "design_host" {
		t.Fatalf("lease was not taken over after expiry: %#v", stored)
	}
	if stored.LeaseUntil == nil || !stored.LeaseUntil.Equal(testTime.Add(91*time.Second+90*time.Second)) {
		t.Fatalf("lease_until = %#v, want now+90s", stored.LeaseUntil)
	}
}

// TestHeartbeatRenewsWriterLease pins R-B2-5: a running Host renews its lease so
// the turn survives past the original 90s; a non-owner can never renew it.
func TestHeartbeatRenewsWriterLease(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 101); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	env.advanceClock(60 * time.Second)
	lease, err := env.svc.RenewLease(requestContext(), conversation.ID, "clarification_host", 101)
	if err != nil {
		t.Fatalf("RenewLease: %v", err)
	}
	if !lease.ExpiresAt.Equal(testTime.Add(150 * time.Second)) {
		t.Fatalf("renewed lease = %v, want heartbeat+90s", lease.ExpiresAt)
	}

	// 120s after acquisition: the original lease is long gone, the renewed one
	// still owns the conversation.
	env.advanceClock(60 * time.Second)
	if _, err := env.svc.AppendAssistant(requestContext(), conversation.ID,
		versionapp.Writer{TurnID: 101, Agent: "clarification_host"}, textParts("still mine"), 555, nil); err != nil {
		t.Fatalf("append inside the renewed lease: %v", err)
	}

	_, err = env.svc.RenewLease(requestContext(), conversation.ID, "design_host", 202)
	var notWriter *versionapp.NotWriterError
	if !errors.As(err, &notWriter) {
		t.Fatalf("foreign heartbeat err = %v, want *NotWriterError", err)
	}
	if apiErr := apiError(t, err); apiErr.Status != http.StatusForbidden || apiErr.Code != versionapp.CodeNotWriter {
		t.Fatalf("foreign heartbeat = %#v, want 403 not_writer", apiErr)
	}
}

// TestBackgroundJobCannotAppendAssistant pins R-B2-4: background work has no
// writer lease and can never append assistant conversation text, while its
// server-generated job cards remain writable during an active Host turn.
func TestBackgroundJobCannotAppendAssistant(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 101); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// The legacy background path (no Host writer identity) is fenced while a
	// Host owns the conversation.
	_, err := env.svc.AppendAssistantMessage(requestContext(), conversation.ID, textParts("background"), 9001)
	var notWriter *versionapp.NotWriterError
	if !errors.As(err, &notWriter) {
		t.Fatalf("background append err = %v, want *NotWriterError", err)
	}
	apiErr := apiError(t, err)
	if apiErr.Status != http.StatusForbidden || apiErr.Code != versionapp.CodeNotWriter {
		t.Fatalf("background append = %#v, want 403 not_writer", apiErr)
	}
	if len(env.repo.messages[conversation.ID]) != 0 {
		t.Fatalf("background append stored a message: %#v", env.repo.messages[conversation.ID])
	}

	// The writer entry point also rejects a call without a turn id.
	_, err = env.svc.AppendAssistant(requestContext(), conversation.ID,
		versionapp.Writer{Agent: "requirement_curator"}, textParts("no turn"), 9002, nil)
	notWriter = nil
	if !errors.As(err, &notWriter) || notWriter.Reason == "" {
		t.Fatalf("lease-less append err = %v, want a reasoned *NotWriterError", err)
	}

	// A job card is a server-generated artifact, not a Host reply: it stays
	// appendable while the Host turn is active and is stored as kind=artifact.
	card := version.MessagePart{
		Type: version.MessagePartJobCard, JobID: 7001, JobKind: "chat.turn",
		Status: "open", Current: 0, Total: 0,
	}
	view, created, err := env.svc.UpsertJobCard(requestContext(), env.versionID, conversation.ID, card)
	if err != nil || !created {
		t.Fatalf("UpsertJobCard = %#v/%v/%v", view, created, err)
	}
	jobCard := env.repo.messages[conversation.ID][0]
	if jobCard.Kind != version.MessageKindArtifact || jobCard.AuthorAgent != "" {
		t.Fatalf("job card kind/author = %q/%q, want artifact/server", jobCard.Kind, jobCard.AuthorAgent)
	}
}

// TestWriterAppendIsIdempotentPerTurn pins R-B2-5 message idempotency: a retried
// append of the same Host turn returns the stored message instead of writing a
// duplicate or repeating side effects.
func TestWriterAppendIsIdempotentPerTurn(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	writer := versionapp.Writer{TurnID: 101, Agent: "clarification_host"}
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, writer.Agent, writer.TurnID); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	first, err := env.svc.AppendAssistant(requestContext(), conversation.ID, writer, textParts("hello"), 555, nil)
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	second, err := env.svc.AppendAssistant(requestContext(), conversation.ID, writer, textParts("hello"), 555, nil)
	if err != nil {
		t.Fatalf("retried append: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("retry wrote a second message: first=%d second=%d", first.ID, second.ID)
	}
	stored := env.repo.messages[conversation.ID]
	if len(stored) != 1 {
		t.Fatalf("messages = %d, want one idempotent message", len(stored))
	}
	if stored[0].AuthorAgent != writer.Agent || stored[0].Kind != version.MessageKindText {
		t.Fatalf("stored author/kind = %q/%q", stored[0].AuthorAgent, stored[0].Kind)
	}
	if stored[0].ClientMessageID == nil || *stored[0].ClientMessageID != version.HostTurnClientMessageID(writer.TurnID) {
		t.Fatalf("idempotency key = %#v", stored[0].ClientMessageID)
	}
	messageEvents := 0
	for _, event := range env.events.events {
		if event.Type == "conversation.message" {
			messageEvents++
		}
	}
	if messageEvents != 1 {
		t.Fatalf("message events = %d, want one (idempotent retry must not republish)", messageEvents)
	}
}

// TestUserMessageQueuedWhileHostRunActive pins B §5.1: users may keep posting
// while a Host turn owns the lease (the inbox queues them); only other Host
// writers are rejected.
func TestUserMessageQueuedWhileHostRunActive(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)
	if _, err := env.svc.Acquire(requestContext(), conversation.ID, "clarification_host", 101); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	message, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID,
		textParts("queued while the host answers"), "queued-1", 42)
	if err != nil {
		t.Fatalf("PostUserMessage during an active Host turn: %v", err)
	}
	if message.Role != version.MessageRoleUser {
		t.Fatalf("message = %#v", message)
	}
	if len(env.repo.messages[conversation.ID]) != 1 {
		t.Fatalf("user message was blocked: %#v", env.repo.messages[conversation.ID])
	}
	stored := env.repo.conversations[conversation.ID]
	if stored.ActiveAgent == nil || *stored.ActiveAgent != "clarification_host" {
		t.Fatalf("user message disturbed the lease: %#v", stored)
	}

	// The queued user turn does not let a second Host append.
	if _, err := env.svc.AppendAssistant(requestContext(), conversation.ID,
		versionapp.Writer{TurnID: 202, Agent: "design_host"}, textParts("stolen"), 556, nil); err == nil {
		t.Fatal("second host appended while the lease was held")
	}
}

// TestReservedHostTurnClientIDRejected documents the reserved idempotency-key
// namespace: a client-supplied id may not collide with a Host turn key.
func TestReservedHostTurnClientIDRejected(t *testing.T) {
	env := newLeaseConversationEnv(t)
	conversation := env.createConversation(t)

	_, err := env.svc.PostUserMessage(requestContext(), env.versionID, conversation.ID,
		textParts("collision"), version.HostTurnClientMessageID(101), 42)
	if err == nil {
		t.Fatal("reserved host-turn client_message_id accepted")
	}
	if apiErr := apiError(t, err); apiErr.Status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", apiErr.Status)
	}
	if len(env.repo.messages[conversation.ID]) != 0 {
		t.Fatalf("reserved message stored: %#v", env.repo.messages[conversation.ID])
	}
}
