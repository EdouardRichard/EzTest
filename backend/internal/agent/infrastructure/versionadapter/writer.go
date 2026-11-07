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
} = (*Adapter)(nil)

// version.MessagePart is the converted append payload; keep the import in the
// compile surface documented.
var _ version.MessagePart
