package agentapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
)

// reconcilerLimit bounds how many rows each reconciliation rule scans per pass.
const reconcilerLimit = 100

// ReconcilerDeps wires the P1 reconciler. It only ensures work items and never
// executes business actions.
type ReconcilerDeps struct {
	Versions      VersionProjector
	Conversations ConversationReader
	Attachments   PendingAttachmentReader
	Queue         WorkQueueEnsurer
	Events        eventingapp.Publisher
	Clock         clock.Clock
	Logger        *slog.Logger
}

// Reconciler periodically repairs the P1 control plane: version projection,
// conversation opening/reply/curate tasks and attachment ingest tasks. Every
// ensure uses a dedupe key, so repeated scans never add duplicate work.
type Reconciler struct {
	versions      VersionProjector
	conversations ConversationReader
	attachments   PendingAttachmentReader
	queue         WorkQueueEnsurer
	events        eventingapp.Publisher
	clock         clock.Clock
	logger        *slog.Logger
	limit         int
}

// NewReconciler wires the reconciler.
func NewReconciler(deps ReconcilerDeps) *Reconciler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		versions:      deps.Versions,
		conversations: deps.Conversations,
		attachments:   deps.Attachments,
		queue:         deps.Queue,
		events:        deps.Events,
		clock:         deps.Clock,
		logger:        logger,
		limit:         reconcilerLimit,
	}
}

// Reconcile runs one best-effort scan. Rule failures are aggregated; the next
// tick retries them (all rules are idempotent).
func (r *Reconciler) Reconcile(ctx context.Context) error {
	var errs []error
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err := r.projectVersions(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.reconcileConversations(ctx); err != nil {
		errs = append(errs, err)
	}
	if err := r.reconcileAttachments(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// projectVersions moves versions with content from initial to running. The
// version service also does this on first content; the reconciler is the
// fallback repair path. MarkRunning is idempotent and owns the
// version.started/version.status_changed events, so the reconciler does not
// publish status events itself.
func (r *Reconciler) projectVersions(ctx context.Context) error {
	if r.versions == nil {
		return nil
	}
	versionIDs, err := r.versions.ListInitialVersionsWithContent(ctx, r.limit)
	if err != nil {
		return fmt.Errorf("list initial versions: %w", err)
	}
	var errs []error
	for _, versionID := range versionIDs {
		if err := r.versions.MarkRunning(ctx, versionID); err != nil {
			errs = append(errs, fmt.Errorf("mark version %d running: %w", versionID, err))
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) reconcileConversations(ctx context.Context) error {
	if r.conversations == nil || r.queue == nil {
		return nil
	}
	conversations, err := r.conversations.ListConversations(ctx, r.limit)
	if err != nil {
		return fmt.Errorf("list conversations: %w", err)
	}
	tasks := map[int64][]agent.WorkItem{}
	loadTasks := func(versionID int64) ([]agent.WorkItem, error) {
		if cached, ok := tasks[versionID]; ok {
			return cached, nil
		}
		items, err := r.queue.ListByVersion(ctx, versionID, r.limit)
		if err != nil {
			return nil, err
		}
		tasks[versionID] = items
		return items, nil
	}

	var errs []error
	for _, conversation := range conversations {
		items, err := loadTasks(conversation.VersionID)
		if err != nil {
			errs = append(errs, fmt.Errorf("list work items for version %d: %w", conversation.VersionID, err))
			continue
		}
		team := agent.TeamClarification
		design := conversation.Kind == "design"
		if design {
			team = agent.TeamDesign
		}

		// Rule 2: conversation opening.
		if len(conversation.Messages) == 0 {
			dedupeKey := fmt.Sprintf("opening:%d", conversation.ID)
			if !hasBlockingTask(items, WorkTypeChatTurn, dedupeKey) {
				created, err := r.ensure(ctx, conversation.VersionID, team, WorkTypeChatTurn, dedupeKey, map[string]any{
					"conversation_id": idText(conversation.ID),
					"trigger":         "opening",
				})
				if err != nil {
					errs = append(errs, err)
				} else if created {
					items = append(items, agent.WorkItem{VersionID: conversation.VersionID, Type: WorkTypeChatTurn, DedupeKey: dedupeKey, Status: agent.WorkItemOpen})
				}
			}
		}

		// Rules 3 and 4: reply and curation per user message. Design
		// conversations keep the reply Host but never run requirement.curate.
		for index, message := range conversation.Messages {
			if message.Role != "user" {
				continue
			}
			if !hasAssistantAfter(conversation.Messages, index) {
				dedupeKey := fmt.Sprintf("reply:%d", message.ID)
				if !hasBlockingTask(items, WorkTypeChatTurn, dedupeKey) {
					created, err := r.ensure(ctx, conversation.VersionID, team, WorkTypeChatTurn, dedupeKey, map[string]any{
						"conversation_id": idText(conversation.ID),
						"message_id":      idText(message.ID),
						"trigger":         "user_message",
					})
					if err != nil {
						errs = append(errs, err)
					} else if created {
						items = append(items, agent.WorkItem{VersionID: conversation.VersionID, Type: WorkTypeChatTurn, DedupeKey: dedupeKey, Status: agent.WorkItemOpen})
					}
				}
			}
			if design {
				continue
			}
			if !hasCuratorAfter(conversation.Messages, index) {
				dedupeKey := fmt.Sprintf("curate:%d", message.ID)
				if !hasBlockingTask(items, WorkTypeRequirementCurate, dedupeKey) {
					created, err := r.ensure(ctx, conversation.VersionID, team, WorkTypeRequirementCurate, dedupeKey, map[string]any{
						"conversation_id": idText(conversation.ID),
						"message_id":      idText(message.ID),
					})
					if err != nil {
						errs = append(errs, err)
					} else if created {
						items = append(items, agent.WorkItem{VersionID: conversation.VersionID, Type: WorkTypeRequirementCurate, DedupeKey: dedupeKey, Status: agent.WorkItemOpen})
					}
				}
			}
		}
		tasks[conversation.VersionID] = items
	}
	return errors.Join(errs...)
}

func (r *Reconciler) reconcileAttachments(ctx context.Context) error {
	if r.attachments == nil || r.queue == nil {
		return nil
	}
	attachments, err := r.attachments.ListPendingAttachments(ctx, r.limit)
	if err != nil {
		return fmt.Errorf("list pending attachments: %w", err)
	}
	var errs []error
	for _, attachment := range attachments {
		dedupeKey := fmt.Sprintf("attachment:%d", attachment.ID)
		items, err := r.queue.ListByVersion(ctx, attachment.VersionID, r.limit)
		if err != nil {
			errs = append(errs, fmt.Errorf("list work items for version %d: %w", attachment.VersionID, err))
			continue
		}
		if hasBlockingTask(items, WorkTypeAttachmentIngest, dedupeKey) {
			continue
		}
		payload := map[string]any{
			"attachment_id": idText(attachment.ID),
			"version_id":    idText(attachment.VersionID),
		}
		if attachment.ConversationID > 0 {
			payload["conversation_id"] = idText(attachment.ConversationID)
		}
		if attachment.MessageID > 0 {
			payload["message_id"] = idText(attachment.MessageID)
		}
		if _, err := r.ensure(ctx, attachment.VersionID, agent.TeamClarification, WorkTypeAttachmentIngest, dedupeKey, payload); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (r *Reconciler) ensure(ctx context.Context, versionID int64, team agent.Team, workType, dedupeKey string, payload map[string]any) (bool, error) {
	_, created, err := r.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: versionID,
		Team:      string(team),
		Type:      workType,
		DedupeKey: dedupeKey,
		Payload:   payload,
	})
	if err != nil {
		return false, fmt.Errorf("ensure %s %s: %w", workType, dedupeKey, err)
	}
	return created, nil
}

func hasBlockingTask(items []agent.WorkItem, workType, dedupeKey string) bool {
	for _, item := range items {
		if item.Type != workType || item.DedupeKey != dedupeKey {
			continue
		}
		switch item.Status {
		case agent.WorkItemOpen, agent.WorkItemClaimed, agent.WorkItemRunning, agent.WorkItemDone:
			return true
		}
	}
	return false
}

func hasAssistantAfter(messages []ReconcilerMessage, index int) bool {
	for i := index + 1; i < len(messages); i++ {
		if messages[i].Role == "assistant" {
			return true
		}
	}
	return false
}

func hasCuratorAfter(messages []ReconcilerMessage, index int) bool {
	for i := index + 1; i < len(messages); i++ {
		if messages[i].Role == "assistant" && messages[i].AgentProfileKey == ProfileKeyRequirementCurator {
			return true
		}
	}
	return false
}
