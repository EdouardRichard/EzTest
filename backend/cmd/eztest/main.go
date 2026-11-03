// Command eztest is the single composition root of the EzTest API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentcontext "github.com/EdouardRichard/EzTest/backend/internal/agent/application/context"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/designadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/knowledgeadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/p4"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	agentapi "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
	chainproviders "github.com/EdouardRichard/EzTest/backend/internal/chain/infrastructure/providers"
	chainapi "github.com/EdouardRichard/EzTest/backend/internal/chain/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	deliveryadapters "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/adapters"
	deliverycontent "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	deliverypg "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/postgres"
	deliveryrender "github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/render"
	deliveryapi "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/storageexport"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventingdomain "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionadapters "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/adapters"
	executioninfra "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	executionpg "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/load"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	versionadapters "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/adapters"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/contentstore"
)

var version = "0.2.0-p8"

func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "eztest"
	}
	return name
}

// leaderAcquisitionAware is implemented by leader gates that can report every
// acquisition of the advisory lock (*orchestrationpg.Leader). runReconciler
// registers a wake on it so a failover reconciles immediately instead of
// waiting for the next unrelated event; the reconciler's own re-arm covers a
// gate implementation without this hook.
type leaderAcquisitionAware interface {
	OnAcquired(func())
}

// runReconciler starts the event-driven reconciliation loop (spec §5.7: a
// 250–500ms debounce replaces the old 3s poll). Every persisted event is a hint
// that control-plane state may need repair; the reconciler coalesces bursts
// into one pass. Startup is a normal (debounced) wake, so a replica that is not
// leader yet keeps the pass armed instead of losing it. Leadership acquisition
// is a second wake, so a failover reconciles without an unrelated event. The
// EZTEST_RECONCILER_POLL_INTERVAL compatibility switch can restore periodic
// polling until the P10 acceptance run retires it.
func runReconciler(ctx context.Context, gate orchestrationapp.LeaderGate, reconciler *agentapp.Reconciler) {
	if reconciler == nil {
		return
	}
	reconciler.Notify("startup", 0)
	if aware, ok := gate.(leaderAcquisitionAware); ok {
		aware.OnAcquired(func() {
			reconciler.Notify("leader_acquired", 0)
		})
	}
	shouldRun := func() bool { return gate != nil && gate.IsLeader() }
	if err := reconciler.Run(ctx, shouldRun); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("eztest: reconciler loop stopped: %v", err)
	}
}

// reconcilerPollInterval is the P2-T5 compatibility switch. Unset/0 (the
// default) keeps the reconciler fully event-driven; a positive duration, for
// example "3s", restores the pre-0.2.1 periodic scan until P10 acceptance.
func reconcilerPollInterval() time.Duration {
	raw := strings.TrimSpace(os.Getenv("EZTEST_RECONCILER_POLL_INTERVAL"))
	if raw == "" || raw == "0" || strings.EqualFold(raw, "off") {
		return 0
	}
	if interval, err := time.ParseDuration(raw); err == nil {
		// "0s" (and any non-positive duration) is the same disabled switch as
		// the bare "0"/"off" spellings, not an invalid value.
		if interval <= 0 {
			return 0
		}
		return interval
	}
	log.Printf("eztest: invalid EZTEST_RECONCILER_POLL_INTERVAL %q; keeping the 3s compatibility poll", raw)
	return 3 * time.Second
}

// reconcilerEventVersionID extracts the version scope of a published event so
// the wake carries diagnostic context. Zero is valid: the pass scans every rule
// and version anyway.
func reconcilerEventVersionID(event eventingdomain.Event) int64 {
	if raw, ok := event.Scope["version_id"]; ok {
		switch value := raw.(type) {
		case string:
			if parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil && parsed > 0 {
				return parsed
			}
		case float64:
			return int64(value)
		case int64:
			return value
		case json.Number:
			if parsed, err := value.Int64(); err == nil {
				return parsed
			}
		}
	}
	const prefix = "version:"
	if strings.HasPrefix(event.Channel, prefix) {
		if parsed, err := strconv.ParseInt(strings.TrimPrefix(event.Channel, prefix), 10, 64); err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

// versionTaskEnqueuer adapts the version context's local TaskEnqueuer port to
// the agent work queue. The composition root is the only place that knows both
// bounded contexts, so the adapter stays in cmd/eztest.
type versionTaskEnqueuer struct {
	queue *agentapp.WorkQueue
}

func (e versionTaskEnqueuer) Ensure(ctx context.Context, in versionapp.TaskInput) error {
	if e.queue == nil {
		return nil
	}
	_, _, err := e.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: in.VersionID,
		Team:      in.Team,
		Type:      in.Type,
		Priority:  in.Priority,
		DedupeKey: in.DedupeKey,
		Payload:   in.Payload,
	})
	return err
}

// designTaskEnqueuer adapts the design planner's work-queue port to the agent
// task board. Composition-root only.
type designTaskEnqueuer struct {
	queue *agentapp.WorkQueue
}

func (e designTaskEnqueuer) Ensure(ctx context.Context, in designapp.TaskInput) (bool, error) {
	if e.queue == nil {
		return false, nil
	}
	_, created, err := e.queue.Ensure(ctx, agent.NewWorkItem{
		VersionID: in.VersionID,
		Team:      in.Team,
		Type:      in.Type,
		Priority:  in.Priority,
		DedupeKey: in.DedupeKey,
		Payload:   in.Payload,
	})
	return created, err
}

// designConversationCreator creates the auto-design conversation through the
// version conversation service.
type designConversationCreator struct {
	svc *versionapp.ConversationService
}

func (c designConversationCreator) CreateDesign(ctx context.Context, versionID int64, title string) (int64, error) {
	if c.svc == nil {
		return 0, errors.New("design conversation creator has no conversation service")
	}
	view, err := c.svc.Create(ctx, versionID, "design", title, 0)
	if err != nil {
		return 0, err
	}
	return view.ID, nil
}

// conversationModelCatalog adapts the LLM model catalog to the version
// conversation model-override port. It resolves an explicit override (or the
// configuration default when empty) and rejects models outside the selectable
// catalog, so a conversation can never pin a model the runtime would refuse.
type conversationModelCatalog struct {
	svc *llmapp.Service
}

func (c conversationModelCatalog) ResolveConversationModel(ctx context.Context, modelOverride string) (string, error) {
	if c.svc == nil {
		return "", errors.New("conversation model catalog has no llm service")
	}
	model, err := c.svc.ResolveSelectableModel(ctx, modelOverride)
	if err != nil {
		return "", err
	}
	return model.Model, nil
}

// designVersionStarter projects a version to running for design activation.
type designVersionStarter struct {
	svc *versionapp.Service
}

func (s designVersionStarter) MarkRunning(ctx context.Context, versionID int64) error {
	if s.svc == nil {
		return nil
	}
	return s.svc.MarkRunning(ctx, versionID)
}

// designStarterAdapter adapts the planner StartRound result to versionapp's
// error-only DesignStarter port.
type designStarterAdapter struct {
	planner *designapp.Planner
}

func (a designStarterAdapter) StartRound(ctx context.Context, versionID, conversationID, actorID int64) error {
	if a.planner == nil {
		return errors.New("design starter has no planner")
	}
	_, err := a.planner.StartRound(ctx, versionID, conversationID, actorID)
	return err
}

// designMetricsAdapter maps designapp.CaseCounts to the version read model.
type designMetricsAdapter struct {
	planner *designapp.Planner
}

func (a designMetricsAdapter) CountPendingDesign(ctx context.Context, versionID int64) (int, error) {
	if a.planner == nil {
		return 0, errors.New("design metrics has no planner")
	}
	return a.planner.CountPendingDesign(ctx, versionID)
}

func (a designMetricsAdapter) CaseCounts(ctx context.Context, versionID int64) (versionapp.CaseCounts, error) {
	if a.planner == nil {
		return versionapp.CaseCounts{}, nil
	}
	counts, err := a.planner.CaseCounts(ctx, versionID)
	if err != nil {
		return versionapp.CaseCounts{}, err
	}
	return versionapp.CaseCounts{Draft: counts.Draft, Confirmed: counts.Confirmed, Stale: counts.Stale}, nil
}

// designCaseGateConfirmer adapts the design context's case confirmation use
// case to the version stage machine's narrow case-gate port. The composition
// root is the only place that knows both bounded contexts.
type designCaseGateConfirmer struct {
	svc *designapp.GraphService
}

func (c designCaseGateConfirmer) ConfirmCases(ctx context.Context, versionID int64, caseIDs []int64) error {
	if c.svc == nil {
		return errors.New("case gate confirmer is not configured")
	}
	return c.svc.ConfirmCasesByIDs(ctx, versionID, caseIDs)
}

// designTeamRoomAdapter maps the design context's post port to the agent team
// room service, keeping both contexts independent.
type designTeamRoomAdapter struct {
	room *agentapp.TeamRoom
}

func (a designTeamRoomAdapter) Post(ctx context.Context, post designapp.TeamPost) error {
	if a.room == nil {
		return nil
	}
	refs := make([]agent.PostRef, 0, len(post.Refs))
	for _, ref := range post.Refs {
		refs = append(refs, agent.PostRef{Kind: ref.Kind, ID: ref.ID})
	}
	_, err := a.room.Post(ctx, agent.TeamRoomPost{
		VersionID:  post.VersionID,
		Team:       agent.Team(post.Team),
		Kind:       agent.PostKind(post.Kind),
		Content:    post.Content,
		Refs:       refs,
		AgentRunID: post.AgentRunID,
		CreatedBy:  post.CreatedBy,
	})
	return err
}

// designCaseRunStatuses adapts the execution case_run read model onto the
// design graph's latest-verdict port (spec §9.3). The composition root is the
// only place that knows both contexts: design reads execution through this
// port, never through the execution tables (spec §2.1).
type designCaseRunStatuses struct {
	runs *executionpg.CaseRunRepository
}

func (a designCaseRunStatuses) LatestCaseRunStatuses(ctx context.Context, versionID int64, caseVersionIDs []int64) (map[int64]string, error) {
	if a.runs == nil {
		return nil, errors.New("design case run status reader is not configured")
	}
	latest, err := a.runs.LatestByCaseVersions(ctx, versionID, caseVersionIDs)
	if err != nil {
		return nil, err
	}
	statuses := make(map[int64]string, len(latest))
	for caseVersionID, run := range latest {
		statuses[caseVersionID] = string(run.Status)
	}
	return statuses, nil
}

// caseRunCanceller breaks the construction cycle between the execution
// CaseRunner (which needs the human service to open takeovers) and the human /
// pause services (which cancel in-flight case executor runs). The runner pointer
// is assigned once right after it is built; calls only happen at runtime.
type caseRunCanceller struct {
	runner *executionapp.CaseRunner
}

func (c *caseRunCanceller) RegisterRun(caseRunID int64, cancel context.CancelFunc) {
	if c.runner != nil {
		c.runner.RegisterRun(caseRunID, cancel)
	}
}

func (c *caseRunCanceller) CancelRun(caseRunID int64) {
	if c.runner != nil {
		c.runner.CancelRun(caseRunID)
	}
}

func (c *caseRunCanceller) ClearRun(caseRunID int64) {
	if c.runner != nil {
		c.runner.ClearRun(caseRunID)
	}
}

var _ executionapp.RunCanceller = (*caseRunCanceller)(nil)

// executionCoverageAdapter projects the version requirement read model onto the
// execution coverage port. The execution context consumes only current
// clarified/closed requirements (P3 §8.1); no P2 semantics change.
type executionCoverageAdapter struct {
	requirements *versionapp.RequirementService
}

func (a executionCoverageAdapter) ListRequirements(ctx context.Context, versionID int64) ([]executionapp.RequirementRef, error) {
	if a.requirements == nil {
		return nil, nil
	}
	refs := make([]executionapp.RequirementRef, 0)
	for _, status := range []string{"clarified", "closed"} {
		var beforeID *int64
		for {
			items, hasMore, err := a.requirements.List(ctx, versionID, status, 100, beforeID)
			if err != nil {
				return nil, err
			}
			for _, item := range items {
				refs = append(refs, executionapp.RequirementRef{
					RequirementID:        item.ID,
					RequirementVersionID: item.CurrentVersionID,
					Status:               item.Status,
					Title:                item.Title,
				})
			}
			if !hasMore || len(items) == 0 {
				break
			}
			last := items[len(items)-1].ID
			beforeID = &last
		}
	}
	return refs, nil
}

var _ executionapp.CoverageReader = executionCoverageAdapter{}

// executionQuotaReader projects the running case_runs into the frozen quota
// accounting shape (P3-D-11): each running run counts once globally plus once
// per version_sut it uses, and a multi-SUT run counts as cross-system.
type executionQuotaReader struct {
	caseRuns executionapp.CaseRunRepository
	intents  executionapp.IntentRepository
}

func (q executionQuotaReader) RunningCases(ctx context.Context) ([]execution.RunningCase, error) {
	runs, err := q.caseRuns.ListRunning(ctx)
	if err != nil {
		return nil, err
	}
	usage := make([]execution.RunningCase, 0, len(runs))
	for _, run := range runs {
		versionSUTIDs := []int64{}
		if q.intents != nil {
			if intent, err := q.intents.Get(ctx, run.IntentID); err == nil {
				versionSUTIDs = intent.VersionSUTIDs
			}
		}
		usage = append(usage, execution.RunningCase{
			CaseRunID:     run.ID,
			VersionSUTIDs: versionSUTIDs,
			CrossSystem:   len(versionSUTIDs) > 1,
		})
	}
	return usage, nil
}

var _ executionapp.QuotaReader = executionQuotaReader{}

// executionSUTContexts adapts the postgres store's CreateSUTContext verb to the
// application SUTContextRepository port (the store predates the narrow port
// name). It is composition-root-only wiring, no logic.
type executionSUTContexts struct {
	repo *executionpg.SUTContextRepository
}

func (a executionSUTContexts) Create(ctx context.Context, item execution.CaseRunSUTContext) error {
	return a.repo.CreateSUTContext(ctx, item)
}

func (a executionSUTContexts) ListByCaseRun(ctx context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListByCaseRun(ctx, caseRunID)
}

func (a executionSUTContexts) UpdateCAS(ctx context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error) {
	return a.repo.UpdateCAS(ctx, id, mutate)
}

func (a executionSUTContexts) ListStale(ctx context.Context, createdBefore time.Time, limit int) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListStale(ctx, createdBefore, limit)
}

func (a executionSUTContexts) ListAll(ctx context.Context) ([]execution.CaseRunSUTContext, error) {
	return a.repo.ListAll(ctx)
}

var _ executionapp.SUTContextRepository = executionSUTContexts{}

// versionCaseBindingSource is the version read-model surface consumed by the
// knowledge success-hook binding adapter (*versionapp.Service satisfies it).
type versionCaseBindingSource interface {
	Get(ctx context.Context, id int64) (versionapp.VersionView, error)
}

// p7VersionCaseBindings resolves the page/API/account binding keys of one
// version for the knowledge success hook (Task 51). The version read model
// already exposes the SUT family, environment IDs, route templates, API
// method/path templates and account roles; this adapter only projects them, so
// the agent knowledge adapter never imports the version context.
type p7VersionCaseBindings struct {
	versions versionCaseBindingSource
}

// VersionBindings implements knowledgeadapter.VersionBindingReader.
func (b p7VersionCaseBindings) VersionBindings(ctx context.Context, versionID int64) ([]knowledgeadapter.VersionBinding, error) {
	if b.versions == nil {
		return nil, errors.New("knowledge case bindings: version read model is not configured")
	}
	if versionID <= 0 {
		return nil, errors.New("knowledge case bindings: version id must be positive")
	}
	view, err := b.versions.Get(ctx, versionID)
	if err != nil {
		return nil, err
	}
	bindings := make([]knowledgeadapter.VersionBinding, 0, len(view.Bindings))
	for _, item := range view.Bindings {
		binding := knowledgeadapter.VersionBinding{
			SystemID:      parseCaseBindingID(item.SUT.ID),
			EnvironmentID: parseCaseBindingID(item.Environment.ID),
		}
		if item.SUT.Family != nil {
			binding.SystemKey = strings.TrimSpace(*item.SUT.Family)
		}
		for _, page := range item.Pages {
			// The page binding key is the route template only (spec §10.2,
			// R97): the briefing reader and MarkBindingsStale name knowledge
			// page keys as route templates, so a page without one is omitted
			// instead of sedimenting a URL-shaped key that can never match.
			key := strings.TrimSpace(page.RouteTemplate)
			id := parseCaseBindingID(page.ID)
			if id <= 0 || key == "" {
				continue
			}
			binding.Pages = append(binding.Pages, knowledgeadapter.PageBinding{ID: id, Key: key})
		}
		for _, api := range item.APIs {
			key := caseBindingAPIKey(api.Method, api.PathTemplate)
			id := parseCaseBindingID(api.ID)
			if id <= 0 || key == "" {
				continue
			}
			binding.APIs = append(binding.APIs, knowledgeadapter.APIBinding{ID: id, Key: key})
		}
		seenRoles := make(map[string]struct{}, len(item.Accounts))
		for _, account := range item.Accounts {
			for _, role := range account.Roles {
				role = strings.TrimSpace(role)
				if role == "" {
					continue
				}
				if _, duplicate := seenRoles[role]; duplicate {
					continue
				}
				seenRoles[role] = struct{}{}
				binding.AccountRoles = append(binding.AccountRoles, role)
			}
		}
		bindings = append(bindings, binding)
	}
	return bindings, nil
}

// caseBindingAPIKey is the structured api_key of a version SUT API: the
// uppercase method plus the route template (the same shape the execution
// version adapter uses for http.request).
func caseBindingAPIKey(method, pathTemplate string) string {
	path := strings.TrimSpace(pathTemplate)
	if path == "" {
		return ""
	}
	method = strings.ToUpper(strings.TrimSpace(method))
	if method == "" {
		method = "GET"
	}
	return method + " " + path
}

// parseCaseBindingID parses a decimal resource-view ID. Invalid IDs degrade to
// zero so the knowledge adapter leaves that binding dimension unset instead of
// writing a zero ID.
func parseCaseBindingID(value string) int64 {
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed <= 0 {
		return 0
	}
	return parsed
}

// runExecutionScheduler drives the event-driven intent scan, wave planner and
// recovery sweep. Case confirmations wake the intent scan through the eventing
// observer seam (design.case_confirmed); a positive scanInterval additionally
// runs the old periodic full scan as a compatibility switch until P10
// acceptance. The scan is idempotent (P3 §5.1). Errors are logged and retried;
// the loop exits with the process context. The whole tick is leader-gated
// (P7 §4.5: scheduler+recovery run on the leader only; in-flight work stays
// protected by leases).
func runExecutionScheduler(
	ctx context.Context,
	gate orchestrationapp.LeaderGate,
	intents intentEnsurer,
	versions versionSummaryLister,
	scheduler *executionapp.Scheduler,
	recovery *executionapp.Recovery,
	wake *executionIntentWake,
	scanInterval time.Duration,
) {
	if scheduler == nil && recovery == nil && intents == nil {
		return
	}
	ensureIntents := func(versionID int64) {
		if intents == nil || versionID <= 0 {
			return
		}
		if _, err := intents.EnsureInitialIntents(ctx, versionID); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("eztest: execution intent scan: version %d: %v", versionID, err)
		}
	}
	scanIntents := func() {
		if intents == nil || versions == nil {
			return
		}
		summaries, _, err := versions.List(ctx, 100, nil)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				log.Printf("eztest: execution intent scan: list versions: %v", err)
			}
			return
		}
		for _, summary := range summaries {
			ensureIntents(summary.ID)
		}
	}
	// flushIntents only consumes the pending wake while this replica is the
	// leader, so a follower keeps the request armed until it acquires
	// leadership (the 1s tick retries the flush).
	flushIntents := func() {
		if wake == nil {
			return
		}
		p7RunGated(gate, func() {
			versionIDs, scanAll := wake.take()
			if scanAll {
				scanIntents()
			}
			for _, versionID := range versionIDs {
				ensureIntents(versionID)
			}
		})
	}
	tick := func() {
		p7RunGated(gate, func() {
			if scheduler != nil {
				if err := scheduler.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("eztest: execution scheduler: %v", err)
				}
			}
			if recovery != nil {
				if err := recovery.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
					log.Printf("eztest: execution recovery: %v", err)
				}
			}
		})
	}
	tick()
	if wake != nil {
		wake.NotifyScanAll()
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	var scanTicker *time.Ticker
	var scan <-chan time.Time
	if scanInterval > 0 {
		scanTicker = time.NewTicker(scanInterval)
		defer scanTicker.Stop()
		scan = scanTicker.C
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
			flushIntents()
		case <-scan:
			p7RunGated(gate, scanIntents)
		case <-wake.events():
			flushIntents()
		}
	}
}

// runExecutorPoller pulls the executor /healthz every
// execution.executor_heartbeat_seconds (P3 §6.2, P3-D-01). The interval is
// re-read from the hot settings cache each cycle so an admin change applies
// without a restart. Each poll is leader-gated (P7 §4.5).
func runExecutorPoller(ctx context.Context, gate orchestrationapp.LeaderGate, registry *executionapp.Registry, settings *settingsapp.Service) {
	if registry == nil {
		return
	}
	poll := func() {
		p7RunGated(gate, func() {
			if err := registry.PollOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("eztest: executor poll: %v", err)
			}
		})
	}
	poll()
	for {
		interval := time.Duration(execution.DefaultExecutorHeartbeatSeconds) * time.Second
		if settings != nil {
			if seconds, err := settings.Int(ctx, executionapp.SettingExecutorHeartbeatSeconds); err == nil && seconds >= 1 && seconds <= 60 {
				interval = time.Duration(seconds) * time.Second
			}
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
			poll()
		}
	}
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("eztest: invalid startup configuration: %v", err)
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("eztest: open database: %v", err)
	}
	defer pool.Close()

	if applied, err := db.Migrate(ctx, pool); err != nil {
		log.Fatalf("eztest: migrate database: %v", err)
	} else if len(applied) > 0 {
		log.Printf("eztest: applied %d migrations", len(applied))
	}

	settingsRepo := settingspg.New(pool)
	definitions := settingsapp.Definitions(cfg.ExecutorURL())
	if err := settingsRepo.Seed(ctx, definitions); err != nil {
		log.Fatalf("eztest: seed settings: %v", err)
	}

	// P7 boot ordering: settings are seeded above, then the orchestration
	// registry allocates this replica's snowflake node, and only then is the
	// real ID generator built for every service (S4 report, Task 19). The
	// bootstrap generator is used solely for the api_instance row; reusing a
	// shared settings node for business IDs would collide across replicas.
	nodeID := int64(1)
	nodeSetting, err := settingsRepo.Get(ctx, "runtime.snowflake_node_id")
	if err != nil {
		log.Fatalf("eztest: read snowflake node: %v", err)
	}
	if err := json.Unmarshal(nodeSetting.Value, &nodeID); err != nil {
		log.Fatalf("eztest: parse snowflake node: %v", err)
	}
	bootstrapIDs, err := id.NewSnowflake(nodeID)
	if err != nil {
		log.Fatalf("eztest: configure bootstrap snowflake: %v", err)
	}

	clk := clock.System{}
	// One eventing service per replica, including the cross-instance NOTIFY
	// notifier; the local hub is shared with the P7 SSE listener below.
	eventHub := eventingapp.NewHub(eventingapp.DefaultHubBuffer)
	eventSvc := eventingapp.New(eventingpg.New(pool), eventHub, eventingpg.NewNotifier(pool))

	instanceKey := p7InstanceKey()
	p7OrchestrationRuntime, err := startP7Orchestration(ctx, p7OrchestrationDeps{
		Pool:         pool,
		SettingsRepo: settingsRepo,
		BootstrapIDs: bootstrapIDs,
		Clock:        clk,
		Events:       eventSvc,
		Hostname:     hostName(),
		PID:          os.Getpid(),
		APIPort:      cfg.APIPort,
		Version:      version,
		InstanceKey:  instanceKey,
	})
	if err != nil {
		log.Fatalf("eztest: register api instance: %v", err)
	}
	defer func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), p7StopTimeout)
		defer cancel()
		if err := p7OrchestrationRuntime.Service.Stop(stopCtx); err != nil {
			log.Printf("eztest: stop api instance: %v", err)
		}
	}()

	ids, err := id.NewSnowflake(p7OrchestrationRuntime.Service.NodeID())
	if err != nil {
		log.Fatalf("eztest: configure snowflake: %v", err)
	}

	auditSvc := auditapp.New(auditpg.New(pool), ids, clk)
	// The registry is constructed before auditSvc exists; attach the real
	// recorder now that the allocated node seeded the generator.
	p7OrchestrationRuntime.LateAudit.Set(auditSvc)
	settingsSvc := settingsapp.New(settingsRepo, definitions, ids, clk, auditSvc, eventSvc)
	if err := settingsSvc.Reload(ctx); err != nil {
		log.Fatalf("eztest: load settings: %v", err)
	}

	identitySvc := identityapp.New(identityinfra.NewRepository(pool), identityinfra.NewHasher(identityinfra.DefaultBcryptCost), ids, clk, auditSvc)
	if _, err := identitySvc.EnsureSeedAdmin(ctx); err != nil {
		log.Fatalf("eztest: seed admin: %v", err)
	}

	llmSvc := llmapp.New(llminfra.NewRepository(pool), llminfra.NewClient(), ids, clk, auditSvc, eventSvc,
		llmapp.WithChatClient(llminfra.NewClient()), llmapp.WithSettingsReader(settingsSvc))
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settingsSvc, ids, auditSvc, eventSvc)
	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clk, nil, auditSvc, eventSvc)
	// P5 code baseline resolution: the version context resolves a bound
	// repository's HEAD through the read-only codeintel git layer. The adapter
	// is constructed before the service and completed right after it.
	p5Git := newP5GitClient()
	p5Bindings := newP5CodeBindings(p5Git, resourceSvc)
	versionRepo := versionpg.New(pool, ids)
	versionSvc := versionapp.New(versionRepo, resourceSvc, ids, clk, auditSvc, eventSvc, p5Bindings)
	p5Bindings.SetVersionService(versionSvc, versionRepo)
	resourceSvc.SetUsageChecker(versionSvc)
	designRepo := designpg.New(pool)
	designStale := designapp.NewStaleService(designRepo, clk, auditSvc, eventSvc)

	// P1 control plane and clarification services.
	requirementRepo := versionpg.NewRequirementRepository(pool)
	requirementSvc := versionapp.NewRequirementService(
		requirementRepo, versionSvc, resourceSvc,
		designStale, auditSvc, eventSvc, ids, clk)
	workItems := agentpg.NewWorkItemRepository(pool, ids)
	workQueue := agentapp.NewWorkQueue(workItems, clk, 30*time.Second, 3)
	workQueue.SetEvents(eventSvc)
	teamRoom := agentapp.NewTeamRoom(agentpg.NewTeamRoomRepository(pool, ids), clk, ids, settingsSvc, eventSvc)
	enqueuer := versionTaskEnqueuer{queue: workQueue}
	attachmentSvc := versionapp.NewAttachmentService(
		versionpg.NewAttachmentRepository(pool), contentstore.New(storageSvc), enqueuer,
		attachments.NewParser(), attachments.NewImageProber(), eventSvc, ids, clk)
	conversationSvc := versionapp.NewConversationService(
		versionpg.NewConversationRepository(pool, ids), enqueuer, attachmentSvc, versionSvc, clk, ids, eventSvc)
	conversationSvc.SetModelCatalog(conversationModelCatalog{svc: llmSvc})
	ports := versionadapter.New(requirementSvc, conversationSvc, versionSvc, attachmentSvc, storageSvc)
	// 0.2.1 job control (cancel/retry) is mounted by the HTTP layer only when
	// server.Deps.Jobs is set. The repository is the postgres job-control
	// adapter, audit and events are best-effort collaborators, and the service
	// fails closed (503) rather than panicking if it were ever built without a
	// repository. The version conversation service owns the persisted job card
	// write-back (Task 63), so the handler is built after it.
	jobControlHandler := agentapi.NewJobControlHandler(agentapp.NewJobControl(agentapp.JobControlDeps{
		Jobs:     workItems,
		Audit:    auditSvc,
		Events:   eventSvc,
		Clock:    clk,
		JobCards: conversationSvc, // Task 63: persisted job card write-back
	}))

	// P3-T5 production version-briefing read path. The reader is a read-only,
	// deterministic SQL source over the version/design/execution/knowledge
	// tables. Reader and builder are constructed here at the assembly root (not
	// a package singleton) so the future cache-first context assembler can take
	// the builder as a dependency. That consumer is not wired yet (Task 13/14/33
	// owns it), so the builder is held with a blank assignment instead of being
	// added to the runner dependency set ahead of its consumer.
	briefingReader, err := versionadapter.NewBriefingReader(pool)
	if err != nil {
		log.Fatalf("eztest: wire version briefing reader: %v", err)
	}
	briefingBuilder := agentcontext.NewBriefingBuilder(briefingReader)
	_ = briefingBuilder

	// P2 design services and cross-context adapters.
	designTeamRoom := designTeamRoomAdapter{room: teamRoom}
	designPlanner := designapp.NewPlanner(
		designRepo, ids, clk, eventSvc,
		designTaskEnqueuer{queue: workQueue},
		designTeamRoom,
		designConversationCreator{svc: conversationSvc},
		designVersionStarter{svc: versionSvc},
		designStale,
	)
	designGraph := designapp.NewGraphService(designRepo, ids, clk, auditSvc, eventSvc, designTeamRoom)
	storageExportSvc := storageapp.NewExportService(storagepg.NewExportRepository(pool), storageSvc, ids, clk)
	designExports := designapp.NewExportService(
		designRepo, storageexport.New(storageExportSvc), ids, clk, eventSvc, auditSvc)
	versionSvc.SetDesignMetrics(designMetricsAdapter{planner: designPlanner})

	instanceID := fmt.Sprintf("%s-%d", hostName(), os.Getpid())

	// P3 execution context: repositories, cross-context adapters and services.
	executionRepo := executionpg.NewRepository(pool, ids)
	// P7 design graph result colouring (spec §9.3): each case card carries the
	// latest case_run verdict of its current version.
	designGraph.SetCaseRunStatusReader(designCaseRunStatuses{runs: executionRepo.CaseRuns})
	executionSUTContextRepo := executionSUTContexts{repo: executionRepo.SUTContexts}
	executionVersionAdapter := executionadapters.NewVersionAdapter(versionSvc)
	executionCases := executionadapters.NewDesignCaseReader(designGraph)
	executionContent := executionadapters.NewContentStore(storageSvc)
	executionCredentials := executionadapters.NewCredentialAdapter(versionSvc, resourceSvc, resourceSvc)
	executionHTTPClient := executionadapters.NewHTTPClient()
	executionDatabase := executionadapters.NewDatabaseAdapter()
	executionQueue := executionadapters.NewWorkQueueAdapter(workQueue)
	executionGaps := executionadapters.NewGapEnsurer(workQueue, requirementSvc)
	executionTeamRoom := executionadapters.NewTeamRoomAdapter(teamRoom)
	executionCoverage := executionCoverageAdapter{requirements: requirementSvc}
	// The delivery issue repository is created before the P5 contexts because
	// code anchors validate issue subjects through a read-only adapter.
	issueRepo := deliverypg.NewIssueRepository(pool, ids)

	// P5 codeintel + visual contexts. Both are constructed here (after the P3
	// repositories they read, before the P3 case runner that consumes the
	// visual assert port) and started later with the process.
	p5 := newP5Runtime(ctx, p5RuntimeDeps{
		Pool: pool, LLM: llmSvc, IDs: ids, Clock: clk, Audit: auditSvc, Events: eventSvc,
		Settings: settingsSvc, Resource: resourceSvc, Versions: versionSvc,
		VersionRepo: versionRepo, Requirements: requirementSvc, Design: designRepo,
		Issues: issueRepo, Execution: executionRepo, Content: executionContent,
		Queue: workQueue, Bindings: p5Bindings, InstanceID: instanceID,
		// P7 §4.5: the codeintel worker stops claiming while this replica drains.
		DrainGate: p7OrchestrationRuntime.Service.IsDraining,
	})

	executorURL, _ := settingsSvc.String(ctx, executionapp.SettingExecutorURL)
	executorToken, _ := settingsSvc.String(ctx, executionapp.SettingExecutorToken)
	executorClient := executioninfra.NewClient(executorURL, executorToken, executioninfra.DefaultTimeout)
	executorResolver := executioninfra.NewResolver(executorToken, executioninfra.DefaultTimeout)
	// P9 explore runtime: the real HTTP explore-action executor, the bounded
	// version-scoped session cache and the version fact recorder are constructed
	// once and shared by every exploration caller (Task 36/37). Reuse
	// verification closes its throwaway page through DELETE /v1/explore, so the
	// 8-page cap cannot be exhausted by repeated logins.
	p9rt, err := newP9Runtime(p9RuntimeDeps{
		Pool:          pool,
		IDs:           ids,
		Events:        eventSvc,
		Accounts:      resourceSvc,
		Versions:      versionSvc,
		Requirements:  requirementSvc,
		Design:        designGraph,
		ExecutorURL:   executorURL,
		ExecutorToken: executorToken,
		Now:           clk.Now,
	})
	if err != nil {
		log.Fatalf("eztest: wire P9 explore runtime: %v", err)
	}
	p9ExploreRuntime = p9rt
	// The case author reads the exploration facts (page/API/gap/login) through
	// its design graph read: the design graph holds the same process-wide fact
	// store the explore runtime writes (Task 37).
	designGraph.SetDiscoveryFacts(p9FactRecords{facts: p9rt.Facts})
	executorRegistry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo:     executionRepo.Executors,
		Probe:    executionadapters.NewExecutorProbe(executorClient),
		Settings: settingsSvc,
		Clock:    clk,
		IDs:      ids,
		// P7 multi-endpoint pull: each execution.executor_urls entry gets its
		// own client/probe so one unreachable endpoint cannot hide the others.
		ProbeFactory: func(endpoint string) executionapp.ExecutorProbe {
			return executionadapters.NewExecutorProbe(
				executioninfra.NewClient(endpoint, executorToken, executioninfra.DefaultTimeout))
		},
	})
	// P8 per-executor routing: browser contexts and control calls are sent to
	// the endpoint that owns them; executorID 0 keeps the P3 single-endpoint
	// fallback for historical rows.
	executorRouter := executionadapters.NewExecutorRouter(executorResolver, executorRegistry, executorURL)
	executionCanceller := &caseRunCanceller{}
	executionHumans := executionapp.NewHumanService(executionapp.HumanServiceDeps{
		Humans:    executionRepo.Humans,
		CaseRuns:  executionRepo.CaseRuns,
		Waves:     executionRepo.Waves,
		Canceller: executionCanceller,
		WorkQueue: executionQueue,
		Settings:  settingsSvc,
		IDs:       ids,
		Clock:     clk,
		Events:    eventSvc,
		Audit:     auditSvc,
		Timeout:   1800 * time.Second,
	})
	executionIntents := executionapp.NewIntentService(executionapp.IntentServiceDeps{
		Intents:  executionRepo.Intents,
		Cases:    executionCases,
		Versions: executionVersionAdapter,
		IDs:      ids,
		Clock:    clk,
		Events:   eventSvc,
		Audit:    auditSvc,
	})
	executionAnalysis := executionapp.NewAnalysisService(executionapp.AnalysisServiceDeps{
		Waves:        executionRepo.Waves,
		CaseRuns:     executionRepo.CaseRuns,
		Results:      executionRepo.Results,
		Cases:        executionCases,
		Requirements: executionCoverage,
		Gaps:         executionGaps,
		TeamRoom:     executionTeamRoom,
		WorkQueue:    executionQueue,
		Settings:     settingsSvc,
		Clock:        clk,
		Events:       eventSvc,
		Audit:        auditSvc,
	})
	executionPause := executionapp.NewPauseService(executionapp.PauseServiceDeps{
		Intents:   executionRepo.Intents,
		Waves:     executionRepo.Waves,
		CaseRuns:  executionRepo.CaseRuns,
		Humans:    executionRepo.Humans,
		Contexts:  executionSUTContextRepo,
		Canceller: executionCanceller,
		Executor:  executionadapters.NewExecutorControlWithRouter(executorRouter),
		Clock:     clk,
		Events:    eventSvc,
		Audit:     auditSvc,
	})
	executionScheduler := executionapp.NewScheduler(executionapp.SchedulerDeps{
		Intents:  executionRepo.Intents,
		Waves:    executionRepo.Waves,
		CaseRuns: executionRepo.CaseRuns,
		Cases:    executionCases,
		Versions: executionVersionAdapter,
		// Dispatch restriction (Task 19, S5 report §3): the runner's browser
		// adapter is bound to the primary executor client, so Select never
		// returns a secondary/push-registered endpoint. Multi-endpoint rows are
		// still polled, heartbeated, registered and admin-listed; per-executor
		// browser routing stays a P8 item.
		Executors:   executorRegistry,
		WorkQueue:   executionQueue,
		Quota:       execution.QuotaFromSettings(3, 2, 1),
		QuotaReader: executionQuotaReader{caseRuns: executionRepo.CaseRuns, intents: executionRepo.Intents},
		Analyzer:    executionAnalysis,
		IDs:         ids,
		Clock:       clk,
		Events:      eventSvc,
		Instance:    instanceID,
		// Same lease the CaseRunner stamps in Prepare (the scheduler also
		// enforces this as a minimum): a queued work item can wait longer than
		// 30s before a worker claims it.
		LeaseTTL: executionapp.DefaultCaseLease,
	})
	executionRecovery := executionapp.NewRecovery(executionapp.RecoveryDeps{
		Intents:     executionRepo.Intents,
		Waves:       executionRepo.Waves,
		CaseRuns:    executionRepo.CaseRuns,
		Humans:      executionHumans,
		Executors:   executionRepo.Executors,
		Contexts:    executionSUTContextRepo,
		Cases:       executionCases,
		WorkQueue:   executionQueue,
		Canceller:   executionCanceller,
		Executor:    executionadapters.NewExecutorControlWithRouter(executorRouter),
		Clock:       clk,
		Events:      eventSvc,
		Audit:       auditSvc,
		MaxAttempts: execution.DefaultMaxDispatchAttempts,
		ContextTTL:  300 * time.Second,
	})
	executionHandler := executionhttp.New(executionhttp.Deps{
		Intents:       executionRepo.Intents,
		Waves:         executionRepo.Waves,
		CaseRuns:      executionRepo.CaseRuns,
		Results:       executionRepo.Results,
		Evidence:      executionRepo.Evidence,
		Humans:        executionRepo.Humans,
		Executors:     executionRepo.Executors,
		Cases:         executionCases,
		Versions:      executionVersionAdapter,
		Content:       executionContent,
		Settings:      settingsSvc,
		IntentService: executionIntents,
		PauseService:  executionPause,
		HumanService:  executionHumans,
		Registry:      executorRegistry,
	})

	// P4 delivery/chain/steward composition. The composition root is the only
	// place that knows every context; the cross-context reads go through the
	// application adapters, never through another context's tables.
	analysisRepo := deliverypg.NewAnalysisRepository(pool, ids)
	reportRepo := deliverypg.NewReportRepository(pool, ids)
	closureRepo := versionpg.NewClosureRepository(pool)
	deliveryContent := deliverycontent.New(storageSvc, deliverypg.NewContentRefReader(pool))
	templateVersion, err := settingsSvc.String(ctx, "report.template_version")
	if err != nil || templateVersion == "" {
		templateVersion = "1"
	}
	reportRenderer, err := deliveryrender.NewHTMLRenderer(templateVersion)
	if err != nil {
		log.Fatalf("eztest: configure report renderer: %v", err)
	}
	p4Tasks := p4.NewTaskEnsurer(workQueue, reportRepo, requirementVersionLookup{repo: requirementRepo})

	readDeps := deliveryadapters.Deps{
		Versions:     versionSvc,
		Requirements: requirementSvc,
		Closures:     closureRepo,
		Design:       designGraph,
		Waves:        executionRepo.Waves,
		CaseRuns:     executionRepo.CaseRuns,
		Results:      executionRepo.Results,
		Evidence:     executionRepo.Evidence,
		SUTContexts:  executionSUTContextRepo,
		Intents:      executionRepo.Intents,
		Humans:       executionRepo.Humans,
		Issues:       issueRepo,
	}
	deliveryEvents := deliveryadapters.NewEventPublisher(eventSvc)
	deliveryAudit := deliveryadapters.NewAuditSink(auditSvc)
	issueSvc := deliveryapp.NewIssueService(
		issueRepo, deliveryadapters.NewExecutionReader(readDeps), deliveryEvents, deliveryAudit, clk, p4Tasks)
	issueSvc.WithTriageReader(analysisRepo)
	analysisSvc := deliveryapp.NewAnalysisService(analysisRepo, issueSvc, clk)
	analysisSvc.WithTriagePoster(deliveryTriagePoster{room: teamRoom})
	pdfEnabled, pdfAttempts := p7RenderSettings(ctx, settingsSvc)
	reportSvc := deliveryapp.NewReportService(
		reportRepo, reportRenderer, deliveryContent, deliveryadapters.NewReportDataReader(readDeps),
		deliveryEvents, deliveryAudit, clk, p4Tasks, p4SectionOptions(ctx, settingsSvc),
	).WithRenderJobs(reportRepo, pdfEnabled, pdfAttempts)

	// Four-way work-item dispatcher (0.2.1 D21): the only writer of 0.2.1
	// work_item rows. It enforces the creator-kind matrix, the version-stage
	// ownership of stage-entry kinds and the idempotency key, then delegates to
	// the Task 6 contract repository. It is also the stage machine's
	// StageJobEnqueuer (R35, so stage-entry jobs are actually dispatched in
	// production) and routes report.version through the §5.3 report gate (R43).
	dispatcher := agentapp.NewDispatcher(agentapp.DispatcherDeps{
		WorkItems: workItems,
		Stages:    versionRepo,
		Reports:   reportSvc,
	})
	// Deterministic stage machine (0.2.1 §5.3/§5.5). Both hard gates confirm
	// their selected entities before advancing; after the stage write commits
	// the dispatcher enqueues design.cases/execute.batch and routes the reporting
	// entry through EnsureVersionReport instead of a duplicate stage job.
	stageMachine := versionapp.NewStageMachine(versionRepo, dispatcher, auditSvc, eventSvc, clk,
		versionapp.WithRequirementGateConfirmer(versionapp.NewRequirementGateConfirmer(requirementSvc)),
		versionapp.WithCaseGateConfirmer(designCaseGateConfirmer{svc: designGraph}),
	)

	closureReaders := versionadapters.NewClosureReaders(versionadapters.Deps{
		Requirements: requirementRepo,
		Design:       designGraph,
		CaseRuns:     executionRepo.CaseRuns,
		Waves:        executionRepo.Waves,
		Intents:      executionRepo.Intents,
		Humans:       executionRepo.Humans,
		WorkItems:    workQueue,
		Issues:       issueRepo,
	})
	versionCloseSvc := versionapp.NewVersionCloseService(versionapp.VersionCloseServiceDeps{
		Versions:      versionRepo,
		WorkItems:     closureReaders,
		Execution:     versionadapters.NewVersionCloseExecution(closureReaders),
		HumanRequests: closureReaders,
		Requirements:  requirementRepo,
		Reports:       versionadapters.NewReportStatusReader(reportSvc),
		ReportStaler:  reportSvc,
		Tasks:         p4Tasks,
		Audit:         auditSvc,
		Events:        eventSvc,
		Clock:         clk,
	})
	closureSvc := versionapp.NewClosureService(versionapp.ClosureServiceDeps{
		Closures:     closureRepo,
		Requirements: requirementRepo,
		Cases:        closureReaders,
		Runs:         closureReaders,
		Coverage:     closureReaders,
		Issues:       closureReaders,
		WorkItems:    closureReaders,
		Versions:     versionCloseSvc,
		Tasks:        p4Tasks,
		Audit:        auditSvc,
		Events:       eventSvc,
		IDs:          ids,
		Clock:        clk,
	})
	// P4 reopen hooks: new requirement drafts and confirmed design cases clear
	// the completed projection. Both are best effort; the reconciler retries.
	// The execution intent path uses the same P4 reopen service so a new intent
	// emits version.reopened (trigger=execution_intent) and marks the version
	// report stale instead of the P3 status_changed projection only.
	requirementSvc.SetVersionReopener(versionCloseSvc)
	designGraph.SetVersionReopener(versionCloseSvc)
	executionVersionAdapter.SetVersionReopener(versionCloseSvc)

	chainVersionProvider := chainproviders.NewVersionProvider(requirementSvc, versionSvc)
	chainDesignProvider := chainproviders.NewDesignProvider(designGraph)
	chainExecutionProvider := chainproviders.NewExecutionProvider(
		executionRepo.Waves, executionRepo.CaseRuns, executionSUTContextRepo)
	chainDeliveryProvider := chainproviders.NewDeliveryProvider(issueRepo, reportRepo, requirementSvc, closureSvc)
	chainResourceProvider := chainproviders.NewResourceProvider(versionSvc, resourceSvc)
	chainCursorProvider := chainproviders.NewEventCursorProvider(eventSvc)
	chainSvc := chainapp.NewChainService(
		chainVersionProvider, chainDesignProvider, chainExecutionProvider,
		chainDeliveryProvider, chainResourceProvider, chainCursorProvider, chainCursorProvider,
		clk, p4ChainBudget(ctx, settingsSvc)).WithSettings(settingsSvc)
	deliveryHandler := deliveryapi.New(issueSvc, deliveryapi.WithReports(reportSvc))
	chainHandler := chainapi.New(chainSvc)

	p6Read, err := newP6ReadRuntime(p6ReadDeps{
		Pool:     pool,
		IDs:      ids,
		Clock:    clk,
		Settings: settingsSvc,
		Audit:    auditSvc,
		Issues:   issueSvc,
		Reports:  reportSvc,
	})
	if err != nil {
		log.Fatalf("eztest: wire P6 read models: %v", err)
	}

	registerP4Plugins(issueSvc, analysisSvc, reportSvc, closureSvc, versionCloseSvc)

	runtimeRepo := agentpg.NewRuntimeRepository(pool, ids)
	if _, err := agentapp.EnsureProfiles(ctx, runtimeRepo); err != nil {
		log.Fatalf("eztest: seed agent profiles: %v", err)
	}
	if err := ensurePromptRevisions(ctx, runtimeRepo); err != nil {
		log.Fatalf("eztest: seed agent prompt revisions: %v", err)
	}
	p6Agent, err := newP6AgentRuntime(pool, runtimeRepo, p6Read.VersionQuery, llmSvc, auditSvc)
	if err != nil {
		log.Fatalf("eztest: wire P6 agent centre: %v", err)
	}
	p6Read.Handlers.AgentCenter = p6Agent.AgentCenter
	p6Read.Handlers.Profiles = p6Agent.Profiles

	// P7 services: knowledge (context + agent/tool bridge), storage GC and the
	// report PDF worker/ensurer. Constructed before the agent runner so the
	// runner and tool registry receive the knowledge adapter.
	p7rt, err := newP7Runtime(ctx, p7RuntimeDeps{
		Pool:           pool,
		Settings:       settingsSvc,
		IDs:            ids,
		Clock:          clk,
		Audit:          auditSvc,
		Events:         eventSvc,
		Hub:            eventHub,
		Queue:          workQueue,
		ReportRepo:     reportRepo,
		ReportContent:  deliveryContent,
		DeliveryEvents: deliveryEvents,
		DeliveryAudit:  deliveryAudit,
		Orchestrator:   p7OrchestrationRuntime.Service,
		Leader:         p7OrchestrationRuntime.Leader,
		InstanceKey:    instanceKey,
		InstanceID:     instanceID,
		Logger:         slog.Default(),
	})
	if err != nil {
		log.Fatalf("eztest: wire P7 runtime: %v", err)
	}

	// P8 knowledge success hook: the adapter maps a passed case run's page/API
	// IDs to the version's binding keys through the version read model, then
	// sediments page_locator/api_auth/login_recipe drafts (auto-approved with
	// audit) through the real knowledge service. The binding resolver is a
	// composition-root adapter so the knowledge bridge never imports version.
	p7rt.KnowledgeAdapter.SetVersionBindings(p7VersionCaseBindings{versions: versionSvc})
	// P8 knowledge lifecycle: a resource page-route / account-role change marks
	// the approved knowledge bound to the old value stale. The resource context
	// only emits the neutral change; the knowledge service owns the scan/write.
	resourceSvc.SetKnowledgeInvalidator(p7rt.KnowledgeInvalidator)

	// The case runner is constructed after the P7 runtime so the knowledge
	// success hook is non-nil (the hook is best effort at the Finish boundary:
	// a failure is logged and never blocks or changes the case_run verdict).
	executionRunner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:       executionRepo.Intents,
		CaseRuns:      executionRepo.CaseRuns,
		SUTContexts:   executionSUTContextRepo,
		Results:       executionRepo.Results,
		Evidence:      executionRepo.Evidence,
		Humans:        executionRepo.Humans,
		Cases:         executionCases,
		Bindings:      executionVersionAdapter,
		APIs:          executionVersionAdapter,
		Browser:       executionadapters.NewBrowserAdapterWithRouter(executorRouter),
		Credentials:   executionCredentials,
		Content:       executionContent,
		HTTPClient:    executionHTTPClient,
		Database:      executionDatabase,
		Coverage:      executionCoverage,
		Gaps:          executionGaps,
		TeamRoom:      executionTeamRoom,
		Human:         executionHumans,
		Visual:        p5.VisualPort,
		Settings:      settingsSvc,
		Clock:         clk,
		IDs:           ids,
		Events:        eventSvc,
		Audit:         auditSvc,
		InstanceID:    instanceID,
		KnowledgeHook: p7rt.KnowledgeAdapter,
		// The failure hook moves the knowledge reused by a failed case_run to
		// suspect; the success hook stamps last_verified_at on a passed run.
		KnowledgeFailureHook: p7rt.KnowledgeAdapter,
	})
	executionCanceller.runner = executionRunner

	// P8 in-flight counters: HTTP requests and agent runs. The orchestration
	// heartbeat copies them into api_instance for drain/rolling-upgrade
	// observation; they never participate in scheduling.
	requestLoad := &load.Counter{}
	workLoad := &load.Counter{}
	p7OrchestrationRuntime.Service.SetLoadTracker(p8LoadTracker{requests: requestLoad, work: workLoad})

	// The P5 tool deps bind the constructed services to this process; the
	// execution.visual_assert group resolves its port from the case runner.
	p5Tools := p5.Tools
	p5Tools.VisualAssert = executionRunner
	runner := agentapp.NewRunner(agentapp.RunnerDeps{
		Runtime:      runtimeRepo,
		Chat:         llmSvc,
		Settings:     settingsSvc,
		Clock:        clk,
		IDs:          ids,
		Events:       eventSvc,
		Audit:        auditSvc,
		Conversation: ports,
		Versions:     ports,
		Attachments:  ports,
		Images:       ports,
		TeamRoom:     teamRoom,
		Content:      ports,
		// P7 knowledge injection + usage accounting (S6 report §5).
		Knowledge:      p7rt.KnowledgeAdapter,
		KnowledgeUsage: p7rt.KnowledgeAdapter,
		ToolDeps: tools.Deps{
			Requirement: ports,
			Attachment:  ports,
			Resource:    ports,
			Design:      designadapter.New(designGraph),
			Execution:   executionRunner,
			P5:          p5Tools,
			P7: tools.P7Deps{
				Knowledge: p7rt.KnowledgeAdapter,
				Retrieval: p7rt.KnowledgeAdapter,
			},
		},
		TaskProvider:         executionadapters.NewTaskProvider(executionRunner, p5.VisualConfigs),
		RunCanceller:         executionRunner,
		ReportFinalizer:      p4ReportFinalizer{service: reportSvc},
		RequirementFinalizer: versionapp.NewClosureFinalizer(closureSvc),
		InstanceID:           instanceID,
		Load:                 workLoad,
	})
	worker := agentapp.NewWorker(workQueue, runner, settingsSvc, slog.Default(), instanceID,
		agentapp.WithWorkerEvents(eventSvc),
		agentapp.WithWorkerDrainGate(p7OrchestrationRuntime.Service.IsDraining),
		agentapp.WithWorkerRunSink(executionadapters.NewRunSink(executionRunner)))
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		if err := p5.Worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("eztest: codeintel worker stopped: %v", err)
		}
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("eztest: agent worker stopped: %v", err)
		}
	}()
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:        versionSvc,
		Conversations:   ports,
		Attachments:     ports,
		Queue:           workQueue,
		Events:          eventSvc,
		Clock:           clk,
		Logger:          slog.Default(),
		Design:          designPlanner,
		P4:              p4.NewEnsurer(issueSvc, reportSvc, closureSvc, versionCloseSvc),
		RunningVersions: versionSvc,
		// R49 stage-entry-job recovery: read the stage and re-enqueue through
		// the same idempotent dispatcher the stage machine uses. The presence
		// query uses kind + stage-entered floor, not a newest-page scan, and the
		// transition reason proves the visit was created by a transition that
		// owns the entry job (a resume/reject re-entry must not be repaired).
		Stages:           versionRepo,
		StageJobs:        dispatcher,
		StageJobPresence: workItems,
		StageTransitions: eventingpg.NewStageTransitionReader(pool),
		// The event-driven loop is the default; the env switch restores the old
		// periodic scan until P10 acceptance.
		PollInterval: reconcilerPollInterval(),
		// P7 report PDF backfill runs after the P4 rules (S1 report §5.5).
		P7: p7rt.RenderEnsurer,
	})
	// Event-driven wake (spec §5.7): every persisted event is a hint that the
	// control plane may need a repair pass. Notify only signals a coalescing
	// channel, so a slow observer never delays SSE delivery. Both delivery
	// paths are wired: local publishes (this replica) and cross-instance events
	// loaded by the P7 listener, so a worker on any replica wakes the leader.
	// Case confirmation additionally wakes the execution intent scan; the
	// observer only records the version and the leader-gated scheduler loop
	// performs the database work.
	intentWake := newExecutionIntentWake()
	wake := func(_ context.Context, event eventingdomain.Event) error {
		versionID := reconcilerEventVersionID(event)
		reconciler.Notify(event.Type, versionID)
		if event.Type == designCaseConfirmedEvent {
			intentWake.Notify(versionID)
		}
		// P9 explore analysis (Task 37): every version-scoped event schedules a
		// coalesced, leader-gated gap/coverage refresh. The analysis performs
		// its page probes through the shared explore runtime (Login first,
		// serialized, page-scoped close).
		if versionID > 0 && p9rt != nil && p9rt.Analysis != nil {
			p9rt.Analysis.Notify(versionID)
		}
		return nil
	}
	eventSvc.Observe(wake)
	if p7rt.Listener != nil {
		p7rt.Listener.Observe(wake)
	}
	background.Add(1)
	go func() {
		defer background.Done()
		runReconciler(ctx, p7OrchestrationRuntime.Leader, reconciler)
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		runExecutionScheduler(ctx, p7OrchestrationRuntime.Leader, executionIntents, versionSvc,
			executionScheduler, executionRecovery, intentWake, intentScanIntervalFromEnv())
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		runExecutorPoller(ctx, p7OrchestrationRuntime.Leader, executorRegistry, settingsSvc)
	}()
	// P9 explore analysis consumer (Task 37, R93): the leader-gated refresh loop
	// that drives GapAnalysis/Coverage/CaseAugment over the shared explore
	// runtime. Only the leader probes/writes, and a per-version debounce keeps
	// an event storm to one refresh per interval.
	background.Add(1)
	go func() {
		defer background.Done()
		p9rt.Analysis.Run(ctx, func() bool {
			return p7OrchestrationRuntime.Leader != nil && p7OrchestrationRuntime.Leader.IsLeader()
		})
	}()

	// P7 background loops: leader election, SSE listener, instance maintenance,
	// per-replica report PDF worker and the leader-gated GC worker.
	p7rt.StartWorkers(ctx, &background, settingsSvc)

	// P8 leader-gated event/audit retention. The cutoffs are read on every tick
	// from the settings center; the batch size reuses retention.gc_batch_size.
	retentionService := auditapp.NewRetentionService(
		auditpg.New(pool),
		eventingpg.New(pool),
		auditSvc,
		p8RetentionBatch(ctx, settingsSvc),
	)
	auditDays := func(ctx context.Context) int {
		return p8IntSetting(ctx, settingsSvc, "security.audit_retention_days", 90)
	}
	eventDays := func(ctx context.Context) int {
		return p8IntSetting(ctx, settingsSvc, "retention.event_days", 90)
	}
	background.Add(1)
	go func() {
		defer background.Done()
		runP8RetentionWorker(ctx, p7rt.leader, retentionService,
			func(ctx context.Context) time.Duration { return p8RetentionInterval(ctx, settingsSvc) },
			auditDays, eventDays, p8Logf)
	}()

	handler := server.New(server.Deps{
		Config:        cfg,
		Pool:          pool,
		Version:       version,
		Settings:      settingsSvc,
		Identity:      identitySvc,
		Audit:         auditSvc,
		Events:        eventSvc,
		Storage:       storageSvc,
		LLM:           llmSvc,
		Resource:      resourceSvc,
		Versions:      versionSvc,
		Requirements:  requirementSvc,
		Conversations: conversationSvc,
		Stage:         stageMachine,
		TeamRoom:      teamRoom,
		DesignGraph:   designGraph,
		DesignExports: designExports,
		DesignStarter: designStarterAdapter{planner: designPlanner},
		Execution:     executionHandler,
		Delivery:      deliveryHandler,
		Chain:         chainHandler,
		Closure:       closureSvc,
		VersionClose:  versionCloseSvc,
		CodeIntel:     p5.CodeIntel,
		Visual:        p5.Visual,
		P6:            &p6Read.Handlers,
		P7:            p7rt.p7ServerHandlers(),
		Jobs:          jobControlHandler,
		Load:          requestLoad,
	})

	srv := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.APIPort),
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Printf("eztest api %s listening on :%d", version, cfg.APIPort)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		log.Fatalf("eztest: server failed: %v", err)
	case <-ctx.Done():
		log.Printf("eztest: shutting down")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("eztest: graceful shutdown failed: %v", err)
	}
	background.Wait()
	// Graceful registry shutdown marks this api_instance stopped and releases
	// its node ID for the next replica (the deferred Stop is idempotent).
	if err := p7OrchestrationRuntime.Service.Stop(shutdownCtx); err != nil {
		log.Printf("eztest: stop api instance: %v", err)
	}
	log.Printf("eztest: stopped")
}
