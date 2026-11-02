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

// DesignReconciler is the P2 design control-plane surface run by the agent
// reconciler on every pass (designapp.Planner implements it). It must be
// idempotent: repeated scans never add duplicate scopes/rounds/tasks.
type DesignReconciler interface {
	Reconcile(ctx context.Context) error
}

// P7Ensurer is the P7 control-plane surface (report PDF backfill and other
// P7 ensure rules) run by the reconciler after the P4 rules. It must be
// idempotent: repeated scans never add duplicate work items.
type P7Ensurer interface {
	ReconcileP7(ctx context.Context) error
}

// ReconcilerDeps wires the P1+P2+P4 reconciler. It only ensures work items and
// never executes business actions.
type ReconcilerDeps struct {
	Versions      VersionProjector
	Conversations ConversationReader
	Attachments   PendingAttachmentReader
	Design        DesignReconciler
	Queue         WorkQueueEnsurer
	// P4 is the P4 delivery/steward ensure surface. Nil keeps the P4 rules
	// disabled for P1-P3 wiring (and in unit tests of the earlier rules).
	P4 P4Ensurer
	// RunningVersions lists the running versions the P4 rules reconcile. Nil
	// keeps the P4 rules disabled.
	RunningVersions P4VersionLister
	// P7 is the P7 ensure surface (report PDF backfill). Nil keeps the P7
	// rules disabled for P1-P6 wiring and earlier-rule unit tests.
	P7     P7Ensurer
	Events eventingapp.Publisher
	Clock  clock.Clock
	Logger *slog.Logger
}

// Reconciler periodically repairs the control plane: version projection,
// conversation opening/reply/curate tasks, attachment ingest tasks, the P2
// design planner scan and the P4 delivery/steward ensure rules. Every ensure
// uses a dedupe key, so repeated scans never add duplicate work.
type Reconciler struct {
	versions        VersionProjector
	conversations   ConversationReader
	attachments     PendingAttachmentReader
	design          DesignReconciler
	queue           WorkQueueEnsurer
	p4              P4Ensurer
	runningVersions P4VersionLister
	p7              P7Ensurer
	events          eventingapp.Publisher
	clock           clock.Clock
	logger          *slog.Logger
	limit           int
}

// NewReconciler wires the reconciler.
func NewReconciler(deps ReconcilerDeps) *Reconciler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Reconciler{
		versions:        deps.Versions,
		conversations:   deps.Conversations,
		attachments:     deps.Attachments,
		design:          deps.Design,
		queue:           deps.Queue,
		p4:              deps.P4,
		runningVersions: deps.RunningVersions,
		p7:              deps.P7,
		events:          deps.Events,
		clock:           deps.Clock,
		logger:          logger,
		limit:           reconcilerLimit,
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
	if err := r.reconcileDesign(ctx); err != nil {
		errs = append(errs, err)
	}
	// P4 runs last among the pre-P7 rules: its failures are aggregated, never
	// hiding or skipping the P1-P3 rules above.
	if err := r.reconcileP4(ctx); err != nil {
		errs = append(errs, err)
	}
	// P7 runs after P4: report PDF backfill depends on the P4 report rules and
	// must never hide a P4 failure. Nil keeps the hook disabled.
	if err := r.reconcileP7(ctx); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// reconcileP7 runs the P7 ensure surface (report PDF backfill). It is optional
// so P1-P6 wiring keeps starting, and its error is aggregated with every other
// rule by Reconcile.
func (r *Reconciler) reconcileP7(ctx context.Context) error {
	if r.p7 == nil {
		return nil
	}
	if err := r.p7.ReconcileP7(ctx); err != nil {
		return fmt.Errorf("p7 reconcile: %w", err)
	}
	return nil
}

// reconcileDesign runs the design Planner scan (auto-design, pending scopes,
// critic/data-plan derivation and the stale safety net). The design reconciler
// is optional so P1-only wiring keeps starting.
func (r *Reconciler) reconcileDesign(ctx context.Context) error {
	if r.design == nil {
		return nil
	}
	if err := r.design.Reconcile(ctx); err != nil {
		return fmt.Errorf("design reconcile: %w", err)
	}
	return nil
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
