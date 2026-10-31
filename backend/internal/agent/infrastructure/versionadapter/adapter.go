// Package versionadapter adapts the version bounded context's application
// services to the agent runtime ports. It is the only bridge between
// agent/** and the version context: the agent application and tools packages
// never import the version context directly.
//
// Wiring is owned by cmd/eztest (Lead T13):
//
//	adapter := versionadapter.New(requirementService, conversationService, versionService, attachmentService, storageService)
//	tools.NewP1Registry(tools.Deps{Requirement: adapter, Resource: adapter, ...})
//	agentapp.NewRunner(agentapp.RunnerDeps{Conversations: adapter, Versions: adapter, ...})
//
// The constructor accepts the concrete version/storage application services via
// the narrow interfaces below; tests use fakes. Business failures are returned
// as *platform.Error (or nil); the tools package maps them to the model-visible
// {"error":{...}} payload.
package versionadapter

import (
	"context"
	"fmt"
	"io"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// RequirementService is the version application surface the requirement tools
// need. *versionapp.RequirementService satisfies it.
type RequirementService interface {
	List(ctx context.Context, versionID int64, status string, limit int, beforeID *int64) ([]versionapp.RequirementView, bool, error)
	Get(ctx context.Context, versionID, requirementID int64) (versionapp.RequirementDetail, error)
	Create(ctx context.Context, versionID int64, draft versionapp.RequirementDraft, actorID int64) (versionapp.RequirementDetail, error)
	Update(ctx context.Context, versionID, requirementID int64, draft versionapp.RequirementDraft, reason string, actorID int64) (versionapp.RequirementDetail, error)
	CreateRelation(ctx context.Context, versionID int64, in versionapp.RelationInput, actorID int64) (versionapp.RelationView, error)
}

// ConversationService is the version application surface the runtime
// conversation port needs. *versionapp.ConversationService satisfies it.
type ConversationService interface {
	Get(ctx context.Context, versionID, conversationID int64) (versionapp.ConversationView, error)
	Messages(ctx context.Context, versionID, conversationID int64, after int64, limit int) ([]versionapp.MessageView, bool, error)
	Summary(ctx context.Context, conversationID int64) (versionapp.SummaryView, error)
	AppendAssistantMessageWithReply(ctx context.Context, conversationID int64, parts []version.MessagePart, runID int64, replyToMessageID *int64) (versionapp.MessageView, error)
	UpdateSummary(ctx context.Context, conversationID int64, summary string, pinned []string) error
	// ListReconcilable feeds the P1 reconciler (conversation opening/reply/curate).
	ListReconcilable(ctx context.Context, limit int) ([]versionapp.ReconcilerConversation, error)
}

// VersionService is the version read surface (context version pack, SUT tools).
// *versionapp.Service satisfies it.
type VersionService interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// AttachmentService is the version application surface for attachment ingest
// and the attachment tools. *versionapp.AttachmentService satisfies it.
type AttachmentService interface {
	GetAttachmentInfo(ctx context.Context, versionID, attachmentID int64) (versionapp.AttachmentInfo, error)
	ListAttachments(ctx context.Context, versionID, conversationID int64) ([]versionapp.AttachmentMeta, error)
	ReadAttachmentText(ctx context.Context, versionID, conversationID, attachmentID int64, offset, maxChars int) (string, int, error)
	AttachmentImageRef(ctx context.Context, versionID, conversationID, attachmentID int64) (versionapp.AttachmentImageRef, error)
	// ListPendingForIngest feeds the P1 reconciler (attachment.ingest).
	ListPendingForIngest(ctx context.Context, limit int) ([]versionapp.PendingAttachment, error)
}

// ContentService spills oversized tool results into the content ledger.
// *storageapp.Service satisfies it.
type ContentService interface {
	Upload(ctx context.Context, filename, declaredMIME string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error)
}

// Adapter implements the P1 agent ports on top of the version application
// services. All dependencies are application services; the adapter only
// reads/writes through their public use-case methods.
type Adapter struct {
	requirements  RequirementService
	conversations ConversationService
	versions      VersionService
	attachments   AttachmentService
	storage       ContentService
}

// New wires the version adapter. Concrete *versionapp/*storageapp services can
// be passed directly.
func New(
	requirements RequirementService,
	conversations ConversationService,
	versions VersionService,
	attachments AttachmentService,
	storage ContentService,
) *Adapter {
	return &Adapter{
		requirements:  requirements,
		conversations: conversations,
		versions:      versions,
		attachments:   attachments,
		storage:       storage,
	}
}

var (
	_ tools.RequirementPort            = (*Adapter)(nil)
	_ tools.ResourcePort               = (*Adapter)(nil)
	_ tools.AttachmentPort             = (*Adapter)(nil)
	_ agentapp.ConversationPort        = (*Adapter)(nil)
	_ agentapp.VersionReader           = (*Adapter)(nil)
	_ agentapp.AttachmentReader        = (*Adapter)(nil)
	_ agentapp.ContentWriter           = (*Adapter)(nil)
	_ agentapp.ConversationReader      = (*Adapter)(nil)
	_ agentapp.PendingAttachmentReader = (*Adapter)(nil)
)

func (a *Adapter) missing(name string) error {
	return fmt.Errorf("versionadapter: dependency %q is not configured", name)
}

// conversationOwned verifies that conversationID belongs to versionID before a
// scoped read/write, so cross-version IDs behave like not-found instead of
// leaking another version's conversation.
func (a *Adapter) conversationOwned(ctx context.Context, versionID, conversationID int64) error {
	if a.conversations == nil {
		return a.missing("conversations")
	}
	_, err := a.conversations.Get(ctx, versionID, conversationID)
	return err
}

// The concrete version/storage services are asserted against the interfaces at
// compile time by the assembly root (cmd/eztest); keeping the assertion here
// documents the expected implementations without importing them into tests.
var (
	_ RequirementService  = (*versionapp.RequirementService)(nil)
	_ ConversationService = (*versionapp.ConversationService)(nil)
	_ VersionService      = (*versionapp.Service)(nil)
	_ AttachmentService   = (*versionapp.AttachmentService)(nil)
)
