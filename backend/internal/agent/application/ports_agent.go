package agentapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// ConversationPart is one UI part written into a conversation_message. Data is
// the structured payload for requirement_card/rollback_proposal parts.
type ConversationPart struct {
	Type string
	Text string
	Data map[string]any
}

// ConversationMessage is the version context's UI-thread message shape.
type ConversationMessage struct {
	ID               int64
	VersionID        int64
	ConversationID   int64
	Role             string
	Text             string
	Parts            []ConversationPart
	ReplyToMessageID *int64
	AgentRunID       *int64
	CreatedAt        time.Time
}

// ConversationSnapshot is the session memory plus recent messages loaded for a
// run context.
type ConversationSnapshot struct {
	ID          int64
	VersionID   int64
	Summary     string
	PinnedFacts []string
	Messages    []ConversationMessage
}

// ConversationSummary is one persisted summary revision.
type ConversationSummary struct {
	Version     int
	Summary     string
	PinnedFacts []string
	UpdatedAt   time.Time
}

// AssistantMessageInput writes one assistant conversation_message (text and/or
// structured parts) attributed to an agent run.
type AssistantMessageInput struct {
	VersionID        int64
	ConversationID   int64
	AgentRunID       int64
	ReplyToMessageID *int64
	Parts            []ConversationPart
}

// ConversationPort is the version context's conversation/thread surface used by
// the agent runtime. Adapters enforce version/conversation ownership and return
// *tools.Error or *platform.Error for business failures.
type ConversationPort interface {
	Snapshot(ctx context.Context, versionID, conversationID int64, keepRecent int) (ConversationSnapshot, error)
	GetMessage(ctx context.Context, versionID, conversationID, messageID int64) (ConversationMessage, error)
	AppendAssistantMessage(ctx context.Context, in AssistantMessageInput) (ConversationMessage, error)
	UpdateSummary(ctx context.Context, versionID, conversationID int64, summary string, pinnedFacts []string) (ConversationSummary, error)
}

// VersionReader builds the version/task package rendered into the context.
type VersionReader interface {
	VersionPack(ctx context.Context, versionID int64) (VersionPack, error)
}

// AttachmentInfo is the parsed attachment view used by the Scout run.
type AttachmentInfo struct {
	ID             int64
	ConversationID int64
	RequirementID  int64
	Name           string
	MIME           string
	Status         string
	Error          string
	Text           string
	TotalChars     int
}

// AttachmentReader loads one attachment with its extracted text for ingest runs.
type AttachmentReader interface {
	GetAttachment(ctx context.Context, versionID, attachmentID int64) (AttachmentInfo, error)
}

// TeamRoomPort is the agent context's team-room surface. *TeamRoom satisfies it.
type TeamRoomPort interface {
	Context(ctx context.Context, versionID int64, team string, maxPosts int) (TeamRoomContext, error)
	Post(ctx context.Context, post agent.TeamRoomPost) (agent.TeamRoomPost, error)
}

// ContentRef is the stored reference for an oversized tool result.
type ContentRef struct {
	SHA256 string
	Ref    string
}

// ContentWriter spills oversized tool results to text content storage.
type ContentWriter interface {
	PutText(ctx context.Context, owner string, data []byte) (ContentRef, error)
}

// ProfileKeyForWorkType maps a P1 work item type to its profile (spec §4.2).
func ProfileKeyForWorkType(workType string) (string, bool) {
	switch workType {
	case WorkTypeChatTurn:
		return ProfileKeyClarificationHost, true
	case WorkTypeRequirementCurate:
		return ProfileKeyRequirementCurator, true
	case WorkTypeAttachmentIngest:
		return ProfileKeyContextScout, true
	default:
		return "", false
	}
}

// P1 work item types (contracts/task-types.md).
const (
	WorkTypeChatTurn          = "chat.turn"
	WorkTypeRequirementCurate = "requirement.curate"
	WorkTypeAttachmentIngest  = "attachment.ingest"
)

// WorkItemPayload is the small reference payload carried by P1 work items.
// IDs are parsed from decimal strings or JSON numbers for P0 payload
// compatibility.
type WorkItemPayload struct {
	ConversationID int64
	MessageID      int64
	AttachmentID   int64
	Trigger        string
}

// ToolDeps is the adapter wiring for the P1 tools. The runner adds the
// per-run context closures before creating the per-run registry.
type ToolDeps = tools.Deps

// RunExecutor is the run-loop surface consumed by the worker (satisfied by
// *Runner; kept as an interface so the worker can be tested in isolation).
type RunExecutor interface {
	Run(ctx context.Context, item agent.WorkItem) (RunResult, error)
}

// WorkerQueue is the work-queue surface used by the worker (satisfied by
// *WorkQueue).
type WorkerQueue interface {
	Claim(ctx context.Context, claimant string) (agent.WorkItem, bool, error)
	MarkRunning(ctx context.Context, id, epoch int64) error
	Heartbeat(ctx context.Context, id, epoch int64) error
	Complete(ctx context.Context, id, epoch int64) error
	Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error
	RecoverExpired(ctx context.Context) (int64, error)
	ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error)
	Lease() time.Duration
}

// VersionProjector is the version-context surface used by the reconciler to
// move versions from initial to running when content exists. versionapp.Service
// satisfies it directly; its MarkRunning is idempotent and publishes
// version.started/version.status_changed on the actual transition.
type VersionProjector interface {
	ListInitialVersionsWithContent(ctx context.Context, limit int) ([]int64, error)
	MarkRunning(ctx context.Context, versionID int64) error
}

// ReconcilerMessage is one conversation message row relevant to reconciliation.
// AgentProfileKey is set for assistant messages produced by an agent run.
type ReconcilerMessage struct {
	ID              int64
	Role            string
	AgentProfileKey string
}

// ReconcilerConversation is one conversation with its messages in ID order.
type ReconcilerConversation struct {
	ID        int64
	VersionID int64
	Messages  []ReconcilerMessage
}

// ConversationReader lists conversations that may need opening/reply/curate
// work items.
type ConversationReader interface {
	ListConversations(ctx context.Context, limit int) ([]ReconcilerConversation, error)
}

// PendingAttachment is one attachment awaiting text extraction.
type PendingAttachment struct {
	ID             int64
	VersionID      int64
	ConversationID int64
	MessageID      int64
}

// PendingAttachmentReader lists attachments whose extraction is still pending.
type PendingAttachmentReader interface {
	ListPendingAttachments(ctx context.Context, limit int) ([]PendingAttachment, error)
}

// WorkQueueEnsurer is the idempotent ensure surface used by the reconciler
// (satisfied by *WorkQueue).
type WorkQueueEnsurer interface {
	Ensure(ctx context.Context, in agent.NewWorkItem) (agent.WorkItem, bool, error)
	ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error)
}
