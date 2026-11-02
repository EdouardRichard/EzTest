package agentapp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// reconcilerLimit bounds how many rows each reconciliation rule scans per pass.
const reconcilerLimit = 100

// stageJobGrace is the clock-skew allowance subtracted from StageEnteredAt when
// R49 decides whether a stage-entry job belongs to the current stage visit. A
// job created a little before the recorded transition (transaction latency,
// replica clock skew) still counts as present; an older one is from a previous
// visit and must not mask a lost job.
const stageJobGrace = 30 * time.Second

// StageJobPresence answers R49's "does the current stage visit already own a
// job of this kind?" question. It must query by kind and creation floor instead
// of trusting a bounded newest-first page: a long-lived version can push the
// legitimate job outside the page, and a job from a previous visit to the same
// stage must not hide a later-cycle loss. *agentpg.WorkItemRepository
// implements it with a SQL EXISTS; nil disables the R49 rule.
type StageJobPresence interface {
	HasWorkItemKindSince(ctx context.Context, versionID int64, kind string, since time.Time) (bool, error)
}

// DefaultReconcileDebounce is the event-driven wake window (spec §5.7:
// 250–500ms). A burst of Notify calls inside the window coalesces into one
// reconciliation pass; the window is anchored at the first notification so a
// continuous event stream cannot starve the reconciler.
const DefaultReconcileDebounce = 300 * time.Millisecond

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
	P7 P7Ensurer
	// Stages reads the version stage for the R49 stage-entry-job recovery.
	// Nil disables the rule (earlier-rule unit tests and degraded wiring).
	Stages VersionStageReader
	// StageJobs re-enqueues a missing stage-entry job (R49). *Dispatcher
	// satisfies it; nil disables the rule.
	StageJobs versionapp.StageJobEnqueuer
	// StageJobPresence checks the current stage visit for an existing job. It
	// is queried by kind and created_at floor, never by a bounded page.
	// *agentpg.WorkItemRepository satisfies it; nil disables the rule.
	StageJobPresence StageJobPresence
	// Debounce overrides the event-driven wake window. Zero uses
	// DefaultReconcileDebounce; tests may shorten it.
	Debounce time.Duration
	// PollInterval re-enables periodic reconciliation as a compatibility switch
	// until P10 acceptance. Zero (the default) is fully event-driven. The
	// composition root sets it from an environment override.
	PollInterval time.Duration
	Events       eventingapp.Publisher
	Clock        clock.Clock
	Logger       *slog.Logger
}

// Reconciler repairs the control plane: version projection, conversation
// opening/reply/curate tasks, attachment ingest tasks, the P2 design planner
// scan, the P4 delivery/steward ensure rules, the R49 stage-entry-job recovery
// and the P7 backfill. Reconcile runs one pass (startup and each debounced
// Notify wake); every ensure uses a dedupe key, so repeated passes never add
// duplicate work.
type Reconciler struct {
	versions        VersionProjector
	conversations   ConversationReader
	attachments     PendingAttachmentReader
	design          DesignReconciler
	queue           WorkQueueEnsurer
	p4              P4Ensurer
	runningVersions P4VersionLister
	p7              P7Ensurer
	stages          VersionStageReader
	stageJobs       versionapp.StageJobEnqueuer
	stageJobLookup  StageJobPresence
	events          eventingapp.Publisher
	clock           clock.Clock
	logger          *slog.Logger
	limit           int
	debounce        time.Duration
	pollInterval    time.Duration

	mu              sync.Mutex
	lastWakeKind    string
	lastWakeVersion int64
	// wake is a one-slot coalescing signal: Notify never blocks and a burst of
	// events while a pass is running schedules exactly one follow-up pass.
	wake chan struct{}
}

// NewReconciler wires the reconciler.
func NewReconciler(deps ReconcilerDeps) *Reconciler {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default()
	}
	debounce := deps.Debounce
	if debounce <= 0 {
		debounce = DefaultReconcileDebounce
	}
	pollInterval := deps.PollInterval
	if pollInterval < 0 {
		pollInterval = 0
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
		stages:          deps.Stages,
		stageJobs:       deps.StageJobs,
		stageJobLookup:  deps.StageJobPresence,
		events:          deps.Events,
		clock:           deps.Clock,
		logger:          logger,
		limit:           reconcilerLimit,
		debounce:        debounce,
		pollInterval:    pollInterval,
		wake:            make(chan struct{}, 1),
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
	// R49: repair a stage-entry job lost when StageMachine.apply discarded the
	// enqueue error. It runs after the design scan (which may have just moved
	// the version or generated cases) and before P4, whose report rules are also
	// idempotent for the reporting stage.
	if err := r.reconcileStageJobs(ctx); err != nil {
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

// Notify signals that control-plane state changed (an event was published, a
// stage moved, a job finished). It never blocks: notifications arriving inside
// the debounce window or while a pass is running coalesce into the next single
// pass, so an event-driven loop cannot lose a wakeup.
//
// kind and versionID are diagnostic context for the pass; the reconciler still
// scans all rules because a single event may affect more than its version.
func (r *Reconciler) Notify(kind string, versionID int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.lastWakeKind = kind
	r.lastWakeVersion = versionID
	r.mu.Unlock()
	r.signal()
}

// Run is the event-driven reconciliation loop (spec §5.7: 250–500ms debounce,
// replacing the fixed 3s poll). It waits for Notify signals, coalesces each
// burst for one debounce window and then runs one pass. A positive
// PollInterval additionally runs a compatibility ticker until P10 acceptance.
//
// shouldRun gates each pass (nil runs on every wake); the composition root
// passes the leader gate so only one replica reconciles. Startup is the
// caller's responsibility (the main loop Notifies a "startup" wake before
// calling Run). A gate-closed pass is never dropped: without the compatibility
// poll the pending wake is re-armed, so a pass that arrives before leadership
// is acquired runs as soon as the gate opens.
func (r *Reconciler) Run(ctx context.Context, shouldRun func() bool) error {
	if r == nil {
		return nil
	}
	var poll <-chan time.Time
	if r.pollInterval > 0 {
		ticker := time.NewTicker(r.pollInterval)
		defer ticker.Stop()
		poll = ticker.C
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-r.wake:
			if !r.waitDebounce(ctx) {
				return nil
			}
			if !r.runPass(ctx, shouldRun) && r.pollInterval <= 0 && ctx.Err() == nil {
				// Leadership is acquired asynchronously. Keep the pending pass
				// armed instead of silently consuming the wake; the re-arm uses
				// the normal debounce window so a follower does not spin.
				r.signal()
			}
		case <-poll:
			r.runPass(ctx, shouldRun)
		}
	}
}

// signal re-arms the one-slot wake channel without changing the diagnostic
// event context of the pending pass.
func (r *Reconciler) signal() {
	select {
	case r.wake <- struct{}{}:
	default:
		// A pass is already pending.
	}
}

// waitDebounce coalesces a Notify burst. The window is anchored at the first
// notification (not reset per event), so a continuous stream cannot postpone
// reconciliation forever; it returns false when ctx is cancelled.
func (r *Reconciler) waitDebounce(ctx context.Context) bool {
	timer := time.NewTimer(r.debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-r.wake:
			// Another event in the same burst: keep waiting for the window.
		case <-timer.C:
			return true
		}
	}
}

// runPass runs one gated pass and reports whether the pass actually ran. A
// false result means the caller still owns a pending wake (gate closed) or the
// context was cancelled.
func (r *Reconciler) runPass(ctx context.Context, shouldRun func() bool) bool {
	if ctx.Err() != nil {
		return false
	}
	if shouldRun != nil && !shouldRun() {
		r.logger.Debug("agent reconciler pass deferred: not the leader")
		return false
	}
	r.mu.Lock()
	kind, versionID := r.lastWakeKind, r.lastWakeVersion
	r.mu.Unlock()
	if kind != "" {
		r.logger.Debug("agent reconciler wake", "event_type", kind, "version_id", versionID)
	}
	if err := r.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		r.logger.Warn("agent reconciler pass failed", "error", err)
	}
	return true
}

// reconcileStageJobs implements R49: scan running versions in a stage that owns
// a stage-entry job and re-enqueue the job when it is missing. StageMachine.apply
// intentionally discards the enqueue error (the DB stage row is the truth), so
// this rule is the repair path for that silent loss. It only ensures work; it
// never executes or writes the stage.
func (r *Reconciler) reconcileStageJobs(ctx context.Context) error {
	if r.stageJobs == nil || r.stages == nil || r.runningVersions == nil || r.stageJobLookup == nil {
		return nil
	}
	versionIDs, err := r.runningVersions.ListRunningVersions(ctx, r.limit)
	if err != nil {
		return fmt.Errorf("list running versions for stage recovery: %w", err)
	}
	var errs []error
	for _, versionID := range versionIDs {
		if versionID <= 0 {
			continue
		}
		if ctx.Err() != nil {
			errs = append(errs, ctx.Err())
			break
		}
		state, err := r.stages.GetStage(ctx, versionID)
		if err != nil {
			if errors.Is(err, version.ErrNotFound) {
				continue
			}
			errs = append(errs, fmt.Errorf("read stage for version %d: %w", versionID, err))
			continue
		}
		kind, ok := stageEntryJobKind(state.Stage)
		if !ok {
			continue
		}
		missing, err := r.missingStageJob(ctx, versionID, kind, state.StageEnteredAt)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if !missing {
			continue
		}
		if err := r.stageJobs.EnqueueStageJob(ctx, versionapp.StageJob{
			VersionID: versionID,
			Kind:      kind,
		}); err != nil {
			errs = append(errs, fmt.Errorf("re-enqueue %s for version %d: %w", kind, versionID, err))
		}
	}
	return errors.Join(errs...)
}

// stageEntryJobKind maps the stage to the entry job the stage machine owns
// (spec §5.3). case_review is deliberately not mapped: it is the gate-managed
// review stage, the design.cases job can only be dispatched while the version
// is still designing (the dispatcher enforces stage ownership, so re-enqueueing
// from case_review would be a typed 409), and case_review has no entry job of
// its own (Task 4/7).
func stageEntryJobKind(stage version.Stage) (string, bool) {
	switch stage {
	case version.StageDesigning:
		return versionapp.StageJobDesignCases, true
	case version.StageExecuting:
		return versionapp.StageJobExecuteBatch, true
	case version.StageReporting:
		return versionapp.StageJobReportVersion, true
	default:
		return "", false
	}
}

// missingStageJob reports whether the current stage visit has no work item of
// the expected stage-entry kind. The visit starts at StageEnteredAt (minus the
// clock-skew grace), so a done/cancelled job from an earlier visit to the same
// stage cannot mask a lost later-cycle job. Any status within the visit counts
// as present: a failed stage job is retried by the claim/attempt policy, and a
// done one already ran. Matching by kind (not by a recomputed idempotency key)
// also avoids adding a second row for a partial gate selection, which has a
// different key but the same kind.
func (r *Reconciler) missingStageJob(ctx context.Context, versionID int64, kind string, enteredAt time.Time) (bool, error) {
	since := enteredAt.Add(-stageJobGrace)
	present, err := r.stageJobLookup.HasWorkItemKindSince(ctx, versionID, kind, since)
	if err != nil {
		return false, fmt.Errorf("query %s work item for version %d: %w", kind, versionID, err)
	}
	return !present, nil
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
