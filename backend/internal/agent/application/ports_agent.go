package agentapp

import (
	"context"
	"strings"
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
	Kind        string
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
	ConversationKind(ctx context.Context, versionID, conversationID int64) (string, error)
	// ModelOverride returns the conversation's stored 0.2.1 model_override
	// identifier ("" when unset). The runner reads it before resolving the run
	// model so a user-initiated conversation switch takes effect on the next
	// run; the value is validated by model resolution, never by a fallback.
	ModelOverride(ctx context.Context, versionID, conversationID int64) (string, error)
	GetMessage(ctx context.Context, versionID, conversationID, messageID int64) (ConversationMessage, error)
	AppendAssistantMessage(ctx context.Context, in AssistantMessageInput) (ConversationMessage, error)
	// UpsertJobCard stores or refreshes the single job_card part of one
	// background job in the conversation. The first projection appends the card
	// message; later start/progress/terminal projections rewrite that same card
	// (one card per job). The part must be a job_card ConversationPart.
	UpsertJobCard(ctx context.Context, versionID, conversationID int64, part ConversationPart) (ConversationMessage, error)
	UpdateSummary(ctx context.Context, versionID, conversationID int64, summary string, pinnedFacts []string) (ConversationSummary, error)
}

// VersionReader builds the version/task package rendered into the context.
type VersionReader interface {
	VersionPack(ctx context.Context, versionID int64) (VersionPack, error)
}

// AttachmentImageTokenBudget is the R-E3-4 prompt charge of one embedded image
// reference in the attachment task pack. It matches the version domain's
// AttachmentImageTokenBudget and is charged once per image on top of the
// extracted text.
const AttachmentImageTokenBudget = 1500

// AttachmentInfo is the parsed attachment view used by the ingest/vision task
// pack. It carries the extracted text, its total character count and the
// embedded image references with captions (R-E3-4/R-E3-5); ImageTokens is the
// per-image prompt charge (1500 tokens each) the task pack budgets.
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
	Images         []AttachmentImageInfo
	ImageTokens    int
}

// AttachmentImageInfo is the compact image reference/caption pair exposed to the
// agent runtime. The bytes stay in the content ledger; SHA256 is the read
// reference. Status/Error make a skipped/failed image visible instead of
// silently dropping it.
type AttachmentImageInfo struct {
	ID         int64
	Seq        int
	SHA256     string
	MIME       string
	Width      int
	Height     int
	SourceKind string
	Status     string
	Caption    string
	Error      string
}

// AttachmentReader loads one attachment with its extracted text for ingest runs.
type AttachmentReader interface {
	GetAttachment(ctx context.Context, versionID, attachmentID int64) (AttachmentInfo, error)
}

// ImageData is one image resolved from an image part data_ref.
type ImageData struct {
	MIME string
	Data []byte
}

// ImageLoader resolves an image part's content data_ref (the content-ledger
// SHA-256) into bytes for a vision model. The version attachment adapter
// implements it; the agent application never opens content storage itself.
type ImageLoader interface {
	LoadImage(ctx context.Context, dataRef string) (ImageData, error)
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

// ProfileKeyForWorkType maps a work item type to its profile (P1 spec §4.2 /
// P2 spec §5.1; the P3/P4 increments reuse the same switch). The 0.2.1
// work-item kinds are accepted as well (Task 65): they are the authoritative
// kind of a contract row, and the two stage-entry kinds additionally have a
// deterministic runner path before this profile is resolved.
//
// v0.22 D-B1/R-W1-5 removed the requirement.curate, attachment.ingest,
// design.round, design.data_plan and explore.* mappings together with their
// profiles: those capabilities moved to the deterministic server pipeline or
// Host tools, so an item of one of those legacy types fails closed here instead
// of resolving a deleted profile.
func ProfileKeyForWorkType(workType string) (string, bool) {
	switch workType {
	case WorkTypeChatTurn:
		return ProfileKeyClarificationHost, true
	case WorkTypeDesignRequirement, WorkTypeDesignGap, agent.WorkItemKindDesignCases:
		return ProfileKeyDesignAuthor, true
	case WorkTypeDesignCritic, agent.WorkItemKindReviewCases:
		return ProfileKeyDesignCritic, true
	case WorkTypeExecuteCase, agent.WorkItemKindExecuteBatch:
		return ProfileKeyCaseExecutor, true
	case WorkTypeAnalyzeFailure:
		return ProfileKeyFailureAnalyst, true
	case WorkTypeAnalyzeCoverage:
		return ProfileKeyCoverageAnalyst, true
	case WorkTypeIssueTriage:
		return ProfileKeyIssueTriager, true
	case WorkTypeReportWave, WorkTypeReportVersion:
		return ProfileKeyReportWriter, true
	case WorkTypeRequirementClose, WorkTypeVersionClose:
		return ProfileKeyVersionSteward, true
	case agent.WorkItemKindKnowledgePropose:
		return ProfileKeyKnowledgeCurator, true
	default:
		return "", false
	}
}

// WorkTypeForItem resolves the effective work type of a stored item. The 0.2.1
// Kind column is authoritative when present (contract rows); legacy P1-P8 rows
// carry no Kind and keep their frozen Type. The runner uses this everywhere it
// previously switched on item.Type.
func WorkTypeForItem(item agent.WorkItem) string {
	if kind := strings.TrimSpace(item.Kind); kind != "" {
		return kind
	}
	return strings.TrimSpace(item.Type)
}

// IsStageEntryWorkType reports whether the effective work type is consumed by
// the runner's deterministic stage-entry path (design round / execution batch)
// instead of an LLM profile run. Both are still 0.2.1 work items created by the
// dispatcher; the path runs the real domain action and projects the job card.
func IsStageEntryWorkType(workType string) bool {
	switch workType {
	case agent.WorkItemKindDesignCases, agent.WorkItemKindExecuteBatch:
		return true
	default:
		return false
	}
}

// ProfileKeyForConversationKind selects the Host profile for chat.turn runs:
// clarification conversations keep the P1 Host, design conversations use the
// design Host (spec §5.1 semantic increment; no new work type).
func ProfileKeyForConversationKind(kind string) (string, bool) {
	switch kind {
	case "clarification":
		return ProfileKeyClarificationHost, true
	case "design":
		return ProfileKeyDesignHost, true
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

// P2 design work item types (contracts/task-types.md P2 increment).
const (
	WorkTypeDesignRound       = "design.round"
	WorkTypeDesignRequirement = "design.requirement"
	WorkTypeDesignGap         = "design.gap"
	WorkTypeDesignCritic      = "design.critic"
	WorkTypeDesignDataPlan    = "design.data_plan"
)

// P3 execution work item types (contracts/task-types.md P3 increment).
const (
	WorkTypeExecuteCase     = "execute.case"
	WorkTypeAnalyzeFailure  = "analyze.failure"
	WorkTypeAnalyzeCoverage = "analyze.coverage"
)

// P4 delivery/closure work item types (contracts/task-types.md P4 increment).
const (
	WorkTypeIssueTriage      = "issue.triage"
	WorkTypeReportWave       = "report.wave"
	WorkTypeReportVersion    = "report.version"
	WorkTypeRequirementClose = "requirement.close"
	WorkTypeVersionClose     = "version.close"
)

// P5 codeintel work item types (contracts/task-types.md P5 increment). They are
// claimed by the dedicated codeintel worker and never mapped to an agent
// profile.
const (
	WorkTypeIndexRepo   = "index.repo"
	WorkTypeCodeAnalyze = "code.analyze"
)

// WorkItemPayload is the small reference payload carried by work items. IDs
// are parsed from decimal strings or JSON numbers for P0 payload
// compatibility.
type WorkItemPayload struct {
	ConversationID int64
	MessageID      int64
	AttachmentID   int64
	// PlanItemID is the v0.22 task.complete target of a work item created by a
	// plan-driven path (R-A3-4/R-A8-3). The runner falls back to the version's
	// single in-progress plan item when the work item carries no explicit link,
	// so a real Host run still registers task.complete.
	PlanItemID           int64
	RequirementID        int64
	RequirementVersionID int64
	CaseID               int64
	CaseVersionID        int64
	RoundID              int64
	Trigger              string
	// P3 execution payload (contracts/task-types.md §5). DispatchEpoch/Token
	// fence every case_run result write; the token stays opaque.
	IntentID      int64
	WaveID        int64
	CaseRunID     int64
	DispatchEpoch int64
	DispatchToken string
	// P4 delivery/steward payload (contracts/task-types.md §6). IDs are
	// decimal strings in JSON, parsed by ParseWorkItemPayload;
	// RequirementID/RequirementVersionID above are reused by requirement.close.
	IssueID              int64
	ReportID             int64
	ReportRevision       int32
	CloseRound           int32
	ReportRevisionID     int64
	PredicateFingerprint string
	// P5 codeintel payload (contracts/task-types.md §7). IDs follow the same
	// decimal-string/number convention; commit SHAs are 40 hex strings.
	RepoID        int64
	IndexRunID    int64
	AnalysisID    int64
	BaseCommitSHA string
	HeadCommitSHA string
}

// ToolDeps is the adapter wiring for the P1 tools. The runner adds the
// per-run context closures before creating the per-run registry.
type ToolDeps = tools.Deps

// RunExecutor is the run-loop surface consumed by the worker (satisfied by
// *Runner; kept as an interface so the worker can be tested in isolation).
type RunExecutor interface {
	Run(ctx context.Context, item agent.WorkItem) (RunResult, error)
}

// DesignRoundStarter is the deterministic consumer of the design.cases
// stage-entry work item (Task 65). It opens/repairs the design round for the
// requirements confirmed by the requirement hard gate and returns the design
// conversation the job card is projected into. It never depends on the legacy
// auto_design_new_requirements toggle: the stage transition already selected
// the scope.
type DesignRoundStarter interface {
	StartDesignRound(ctx context.Context, versionID int64, requirementIDs []int64) (conversationID int64, err error)
}

// BatchStarter is the deterministic consumer of the execute.batch stage-entry
// work item. It triggers the execution planner for the confirmed case batch
// (the scheduler then dispatches the per-case execute.case runs); the agent
// runner only owns the stage-entry job, never browser execution.
type BatchStarter interface {
	StartBatch(ctx context.Context, versionID int64, caseIDs []int64) error
}

// WorkerQueue is the work-queue surface used by the worker (satisfied by
// *WorkQueue).
type WorkerQueue interface {
	Claim(ctx context.Context, claimant string) (agent.WorkItem, bool, error)
	// ClaimForTeams leases the next available legacy (kind='') item whose team
	// is in teams.
	ClaimForTeams(ctx context.Context, claimant string, teams []agent.Team) (agent.WorkItem, bool, error)
	// ClaimContract leases the next runnable 0.2.1 contract item (kind != '')
	// whose team is in teams. A nil item with ok=false means nothing is ready.
	ClaimContract(ctx context.Context, claimant string, teams []agent.Team) (*agent.WorkItem, bool, error)
	MarkRunning(ctx context.Context, id, epoch int64) error
	// Heartbeat renews the lease. The caller passes the lease it claimed with;
	// zero keeps the queue default (the P1 worker always uses the default).
	Heartbeat(ctx context.Context, id, epoch int64, lease time.Duration) error
	Complete(ctx context.Context, id, epoch int64) error
	// MarkWaitingHuman parks a running work item for a human takeover.
	MarkWaitingHuman(ctx context.Context, id, epoch int64) error
	// Finish closes a waiting_human/pending work item without a failure.
	Finish(ctx context.Context, id int64, reason string) error
	Fail(ctx context.Context, id, epoch int64, cause error, retryable bool) error
	// FailContract records a failure with the 0.2.1 retry policy (failed until
	// attempts reach max_attempts; a non-retryable failure exhausts them).
	FailContract(ctx context.Context, id, epoch int64, cause error, retryable bool) error
	RecoverExpired(ctx context.Context) (int64, error)
	ListByVersion(ctx context.Context, versionID int64, limit int) ([]agent.WorkItem, error)
	Lease() time.Duration
}

// RunCanceller registers the cancel function of an in-flight P3 execution run
// so pause/cancel/human takeover can interrupt it. It is implemented by the
// execution CaseRunner (application.RunCanceller).
type RunCanceller interface {
	RegisterRun(caseRunID int64, cancel context.CancelFunc)
	ClearRun(caseRunID int64)
}

// VersionProjector is the version-context surface used by the reconciler to// move versions from initial to running when content exists. versionapp.Service
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
	Kind      string
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
