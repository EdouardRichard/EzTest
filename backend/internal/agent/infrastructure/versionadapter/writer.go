package versionadapter

// v0.22 single-writer Host replies (spec R-B1-2, Task 21/36).
//
// The version conversation context owns the one active writer lease of a
// conversation. The runtime adapter exposes that lease to the agent runner
// through a narrow, type-asserted surface: the runner acquires the lease as the
// Host profile, writes assistant text as that writer, renews on its heartbeat
// and releases when the run ends. The legacy writer-less append stays available
// for deployments without the lease tables.

import (
	"context"
	"fmt"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// conversationLeaseService is the version conversation use-case surface behind
// the writer lease. *versionapp.ConversationService satisfies it.
type conversationLeaseService interface {
	Acquire(ctx context.Context, conversationID int64, agent string, turnID int64) (versionapp.WriterLease, error)
	RenewLease(ctx context.Context, conversationID int64, agent string, turnID int64) (versionapp.WriterLease, error)
	Release(ctx context.Context, conversationID int64, agent string, turnID int64) error
	AppendAssistant(ctx context.Context, conversationID int64, writer versionapp.Writer, parts []version.MessagePart, runID int64, replyToMessageID *int64) (versionapp.MessageView, error)
}

var _ conversationLeaseService = (*versionapp.ConversationService)(nil)

// writerLease returns the lease-capable conversation service, or false when the
// adapter was built without it (unit tests / pre-v0.22 assemblies).
func (a *Adapter) writerLease() (conversationLeaseService, bool) {
	if a == nil || a.conversations == nil {
		return nil, false
	}
	service, ok := a.conversations.(conversationLeaseService)
	return service, ok
}

// AcquireWriter implements the runner's Host single-writer lease.
func (a *Adapter) AcquireWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error {
	service, ok := a.writerLease()
	if !ok {
		return fmt.Errorf("versionadapter: conversation service does not expose the writer lease")
	}
	_, err := service.Acquire(ctx, conversationID, agent, turnID)
	return err
}

// RenewWriter renews the runner's Host writer lease.
func (a *Adapter) RenewWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error {
	service, ok := a.writerLease()
	if !ok {
		return fmt.Errorf("versionadapter: conversation service does not expose the writer lease")
	}
	_, err := service.RenewLease(ctx, conversationID, agent, turnID)
	return err
}

// ReleaseWriter releases the runner's Host writer lease.
func (a *Adapter) ReleaseWriter(ctx context.Context, conversationID int64, agent string, turnID int64) error {
	service, ok := a.writerLease()
	if !ok {
		return fmt.Errorf("versionadapter: conversation service does not expose the writer lease")
	}
	return service.Release(ctx, conversationID, agent, turnID)
}

// AppendWriterAssistant writes one Host assistant message as the leased writer.
func (a *Adapter) AppendWriterAssistant(ctx context.Context, conversationID int64, agent string, turnID int64, runID int64, replyTo *int64, parts []agentapp.ConversationPart) (agentapp.ConversationMessage, error) {
	service, ok := a.writerLease()
	if !ok {
		return agentapp.ConversationMessage{}, fmt.Errorf("versionadapter: conversation service does not expose the writer lease")
	}
	converted, err := versionParts(parts)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	view, err := service.AppendAssistant(ctx, conversationID,
		versionapp.Writer{TurnID: turnID, Agent: agent}, converted, runID, replyTo)
	if err != nil {
		return agentapp.ConversationMessage{}, err
	}
	return messageViewToRuntime(view), nil
}

// conversationStreamService is the version conversation incremental stream
// surface (v0.2.4-D T5) behind the same writer lease.
// *versionapp.ConversationService satisfies it.
type conversationStreamService interface {
	BeginAssistantStream(ctx context.Context, conversationID int64, writer versionapp.Writer, runID int64) (int64, error)
	UpdateStreamPart(ctx context.Context, conversationID, messageID int64, seq int, part version.MessagePart) error
	FinishAssistantStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart) error
	InterruptAssistantStream(ctx context.Context, conversationID, messageID int64, finalParts []version.MessagePart) error
	InterruptOrphanStreams(ctx context.Context, conversationID int64, writer versionapp.Writer, errorPart version.MessagePart) (int, error)
}

var _ conversationStreamService = (*versionapp.ConversationService)(nil)

// writerStream returns the incremental-stream conversation service, or false
// when the adapter was built without it (unit tests / pre-T5 assemblies). The
// runner then keeps the batch Host reply, exactly as it does without the lease.
func (a *Adapter) writerStream() (conversationStreamService, bool) {
	if a == nil || a.conversations == nil {
		return nil, false
	}
	service, ok := a.conversations.(conversationStreamService)
	return service, ok
}

// BeginAssistantStream opens the placeholder head of one incremental Host reply
// as the leased writer.
func (a *Adapter) BeginAssistantStream(ctx context.Context, conversationID int64, agent string, turnID, runID int64) (int64, error) {
	service, ok := a.writerStream()
	if !ok {
		return 0, fmt.Errorf("versionadapter: conversation service does not expose the incremental stream")
	}
	return service.BeginAssistantStream(ctx, conversationID, versionapp.Writer{TurnID: turnID, Agent: agent}, runID)
}

// UpdateStreamPart rewrites the full snapshot of one streaming part (seq).
func (a *Adapter) UpdateStreamPart(ctx context.Context, conversationID, messageID int64, seq int, part agentapp.ConversationPart) error {
	service, ok := a.writerStream()
	if !ok {
		return fmt.Errorf("versionadapter: conversation service does not expose the incremental stream")
	}
	converted, err := versionPart(part)
	if err != nil {
		return err
	}
	return service.UpdateStreamPart(ctx, conversationID, messageID, seq, converted)
}

// FinishAssistantStream seals the incremental reply as complete.
func (a *Adapter) FinishAssistantStream(ctx context.Context, conversationID, messageID int64, parts []agentapp.ConversationPart) error {
	return a.sealStream(ctx, conversationID, messageID, parts, false)
}

// InterruptAssistantStream seals the incremental reply as interrupted, keeping
// the accumulated parts the caller passes (never deleting what was shown).
func (a *Adapter) InterruptAssistantStream(ctx context.Context, conversationID, messageID int64, parts []agentapp.ConversationPart) error {
	return a.sealStream(ctx, conversationID, messageID, parts, true)
}

func (a *Adapter) sealStream(ctx context.Context, conversationID, messageID int64, parts []agentapp.ConversationPart, interrupted bool) error {
	service, ok := a.writerStream()
	if !ok {
		return fmt.Errorf("versionadapter: conversation service does not expose the incremental stream")
	}
	converted, err := versionParts(parts)
	if err != nil {
		return err
	}
	if interrupted {
		return service.InterruptAssistantStream(ctx, conversationID, messageID, converted)
	}
	return service.FinishAssistantStream(ctx, conversationID, messageID, converted)
}

// InterruptOrphanStreams seals the heads a crashed Host turn left streaming in
// this conversation, under the lease this turn just acquired.
func (a *Adapter) InterruptOrphanStreams(ctx context.Context, conversationID int64, agent string, turnID int64, errorPart agentapp.ConversationPart) (int, error) {
	service, ok := a.writerStream()
	if !ok {
		return 0, fmt.Errorf("versionadapter: conversation service does not expose the incremental stream")
	}
	converted, err := versionPart(errorPart)
	if err != nil {
		return 0, err
	}
	return service.InterruptOrphanStreams(ctx, conversationID, versionapp.Writer{TurnID: turnID, Agent: agent}, converted)
}

// conversationRunRecoveryService is the run-scoped crash recovery surface
// (v0.2.4-H T12, H-E2). It is asserted separately from conversationStreamService
// so an adapter built against a pre-T12 conversation service keeps every existing
// stream call working; only the crash sweep's Host leg is unavailable then.
type conversationRunRecoveryService interface {
	InterruptRunOrphanStreams(ctx context.Context, conversationID, agentRunID int64, errorPart version.MessagePart) (int, error)
}

var _ conversationRunRecoveryService = (*versionapp.ConversationService)(nil)

// InterruptRunOrphanStreams seals the Host conversation heads of ONE terminal
// agent run. Unlike InterruptOrphanStreams it needs no writer lease: the crash
// sweep calls it for a run whose work item is settled or taken over, in a
// conversation that may never get another turn. A partial recovery (some heads
// left streaming) is returned as the version domain's OrphanSealError together
// with the count of heads that WERE sealed, so the caller can report it.
func (a *Adapter) InterruptRunOrphanStreams(ctx context.Context, conversationID, runID int64, errorPart agentapp.ConversationPart) (int, error) {
	if a == nil || a.conversations == nil {
		return 0, fmt.Errorf("versionadapter: conversation service is not configured")
	}
	service, ok := a.conversations.(conversationRunRecoveryService)
	if !ok {
		return 0, fmt.Errorf("versionadapter: conversation service does not expose run-scoped stream recovery")
	}
	converted, err := versionPart(errorPart)
	if err != nil {
		return 0, err
	}
	return service.InterruptRunOrphanStreams(ctx, conversationID, runID, converted)
}

// messageViewToRuntime projects one version message view onto the runtime's
// conversation message shape. The Host reply only needs the identity; the full
// rendered message is read through the conversation snapshot.
func messageViewToRuntime(view versionapp.MessageView) agentapp.ConversationMessage {
	return agentapp.ConversationMessage{
		ID:             view.ID,
		ConversationID: view.ConversationID,
		Role:           view.Role,
		CreatedAt:      view.CreatedAt,
	}
}

// Compile-time guarantee: the production adapter implements the runner's
// writer-lease port through the same method set. The assertion lives here (not
// in the agent application) so the agent package never imports the version
// context.
var _ interface {
	AcquireWriter(context.Context, int64, string, int64) error
	RenewWriter(context.Context, int64, string, int64) error
	ReleaseWriter(context.Context, int64, string, int64) error
	AppendWriterAssistant(context.Context, int64, string, int64, int64, *int64, []agentapp.ConversationPart) (agentapp.ConversationMessage, error)
	BeginAssistantStream(context.Context, int64, string, int64, int64) (int64, error)
	UpdateStreamPart(context.Context, int64, int64, int, agentapp.ConversationPart) error
	FinishAssistantStream(context.Context, int64, int64, []agentapp.ConversationPart) error
	InterruptAssistantStream(context.Context, int64, int64, []agentapp.ConversationPart) error
	InterruptOrphanStreams(context.Context, int64, string, int64, agentapp.ConversationPart) (int, error)
} = (*Adapter)(nil)

// Compile-time guarantee for the crash sweep's Host leg (H-E2): the production
// adapter is the conversation port the run crash sweeper is wired with.
var _ agentapp.CrashSweepConversations = (*Adapter)(nil)

// version.MessagePart is the converted append payload; keep the import in the
// compile surface documented.
var _ version.MessagePart
