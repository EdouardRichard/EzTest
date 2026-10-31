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
	"sync"
	"syscall"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/designadapter"
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	designpg "github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/design/infrastructure/storageexport"
	"github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
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
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/application"
	"github.com/EdouardRichard/EzTest/backend/internal/settings/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/attachments"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/contentstore"
)

var version = "0.2.0-p3"

func hostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "eztest"
	}
	return name
}

// runReconciler scans the P1 control plane every three seconds and once at
// startup. It only ensures work items; business actions remain in workers.
func runReconciler(ctx context.Context, reconciler *agentapp.Reconciler) {
	if reconciler == nil {
		return
	}
	if err := reconciler.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("eztest: reconcile: %v", err)
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := reconciler.Reconcile(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Printf("eztest: reconcile: %v", err)
			}
		}
	}
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

// runExecutionScheduler drives the deterministic wave planner and the recovery
// sweep every second (P3 §5.2/§5.5). Errors are logged and retried; the loops
// exit with the process context.
func runExecutionScheduler(ctx context.Context, scheduler *executionapp.Scheduler, recovery *executionapp.Recovery) {
	if scheduler == nil && recovery == nil {
		return
	}
	tick := func() {
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
	}
	tick()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tick()
		}
	}
}

// runExecutorPoller pulls the executor /healthz every
// execution.executor_heartbeat_seconds (P3 §6.2, P3-D-01). The interval is
// re-read from the hot settings cache each cycle so an admin change applies
// without a restart.
func runExecutorPoller(ctx context.Context, registry *executionapp.Registry, settings *settingsapp.Service) {
	if registry == nil {
		return
	}
	poll := func() {
		if err := registry.PollOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("eztest: executor poll: %v", err)
		}
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

	nodeID := int64(1)
	nodeSetting, err := settingsRepo.Get(ctx, "runtime.snowflake_node_id")
	if err != nil {
		log.Fatalf("eztest: read snowflake node: %v", err)
	}
	if err := json.Unmarshal(nodeSetting.Value, &nodeID); err != nil {
		log.Fatalf("eztest: parse snowflake node: %v", err)
	}
	ids, err := id.NewSnowflake(nodeID)
	if err != nil {
		log.Fatalf("eztest: configure snowflake: %v", err)
	}

	clk := clock.System{}
	auditSvc := auditapp.New(auditpg.New(pool), ids, clk)
	eventSvc := eventingapp.New(eventingpg.New(pool), eventingapp.NewHub(eventingapp.DefaultHubBuffer))
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
	versionSvc := versionapp.New(versionpg.New(pool, ids), resourceSvc, ids, clk, auditSvc, eventSvc)
	resourceSvc.SetUsageChecker(versionSvc)
	designRepo := designpg.New(pool)
	designStale := designapp.NewStaleService(designRepo, clk, auditSvc, eventSvc)

	// P1 control plane and clarification services.
	requirementSvc := versionapp.NewRequirementService(
		versionpg.NewRequirementRepository(pool), versionSvc, resourceSvc,
		designStale, auditSvc, eventSvc, ids, clk)
	workItems := agentpg.NewWorkItemRepository(pool, ids)
	workQueue := agentapp.NewWorkQueue(workItems, clk, 30*time.Second, 3)
	workQueue.SetEvents(eventSvc)
	teamRoom := agentapp.NewTeamRoom(agentpg.NewTeamRoomRepository(pool, ids), clk, ids, settingsSvc, eventSvc)
	enqueuer := versionTaskEnqueuer{queue: workQueue}
	attachmentSvc := versionapp.NewAttachmentService(
		versionpg.NewAttachmentRepository(pool), contentstore.New(storageSvc), enqueuer,
		attachments.NewParser(), eventSvc, ids, clk)
	conversationSvc := versionapp.NewConversationService(
		versionpg.NewConversationRepository(pool, ids), enqueuer, attachmentSvc, versionSvc, clk, ids, eventSvc)
	ports := versionadapter.New(requirementSvc, conversationSvc, versionSvc, attachmentSvc, storageSvc)

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

	executorURL, _ := settingsSvc.String(ctx, executionapp.SettingExecutorURL)
	executorToken, _ := settingsSvc.String(ctx, executionapp.SettingExecutorToken)
	executorClient := executioninfra.NewClient(executorURL, executorToken, executioninfra.DefaultTimeout)
	executorRegistry := executionapp.NewRegistry(executionapp.RegistryDeps{
		Repo:     executionRepo.Executors,
		Probe:    executionadapters.NewExecutorProbe(executorClient),
		Settings: settingsSvc,
		Clock:    clk,
		IDs:      ids,
	})
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
	executionRunner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		Intents:     executionRepo.Intents,
		CaseRuns:    executionRepo.CaseRuns,
		SUTContexts: executionSUTContextRepo,
		Results:     executionRepo.Results,
		Evidence:    executionRepo.Evidence,
		Humans:      executionRepo.Humans,
		Cases:       executionCases,
		Bindings:    executionVersionAdapter,
		APIs:        executionVersionAdapter,
		Browser:     executionadapters.NewBrowserAdapter(executorClient),
		Credentials: executionCredentials,
		Content:     executionContent,
		HTTPClient:  executionHTTPClient,
		Database:    executionDatabase,
		Coverage:    executionCoverage,
		Gaps:        executionGaps,
		TeamRoom:    executionTeamRoom,
		Human:       executionHumans,
		Settings:    settingsSvc,
		Clock:       clk,
		IDs:         ids,
		Events:      eventSvc,
		Audit:       auditSvc,
		InstanceID:  instanceID,
	})
	executionCanceller.runner = executionRunner
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
		Executor:  executionadapters.NewExecutorControl(executorClient),
		Clock:     clk,
		Events:    eventSvc,
		Audit:     auditSvc,
	})
	executionScheduler := executionapp.NewScheduler(executionapp.SchedulerDeps{
		Intents:     executionRepo.Intents,
		Waves:       executionRepo.Waves,
		CaseRuns:    executionRepo.CaseRuns,
		Cases:       executionCases,
		Versions:    executionVersionAdapter,
		Executors:   executorRegistry,
		WorkQueue:   executionQueue,
		Quota:       execution.QuotaFromSettings(3, 2, 1),
		QuotaReader: executionQuotaReader{caseRuns: executionRepo.CaseRuns, intents: executionRepo.Intents},
		Analyzer:    executionAnalysis,
		IDs:         ids,
		Clock:       clk,
		Events:      eventSvc,
		Instance:    instanceID,
		LeaseTTL:    30 * time.Second,
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
		Executor:    executionadapters.NewExecutorControl(executorClient),
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

	runtimeRepo := agentpg.NewRuntimeRepository(pool, ids)
	if _, err := agentapp.EnsureProfiles(ctx, runtimeRepo); err != nil {
		log.Fatalf("eztest: seed agent profiles: %v", err)
	}
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
		TeamRoom:     teamRoom,
		Content:      ports,
		ToolDeps: tools.Deps{
			Requirement: ports,
			Attachment:  ports,
			Resource:    ports,
			Design:      designadapter.New(designGraph),
			Execution:   executionRunner,
		},
		TaskProvider: executionadapters.NewTaskProvider(executionRunner),
		RunCanceller: executionRunner,
		InstanceID:   instanceID,
	})
	worker := agentapp.NewWorker(workQueue, runner, settingsSvc, slog.Default(), instanceID,
		agentapp.WithWorkerEvents(eventSvc),
		agentapp.WithWorkerRunSink(executionadapters.NewRunSink(executionRunner)))
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		if err := worker.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("eztest: agent worker stopped: %v", err)
		}
	}()
	reconciler := agentapp.NewReconciler(agentapp.ReconcilerDeps{
		Versions:      versionSvc,
		Conversations: ports,
		Attachments:   ports,
		Queue:         workQueue,
		Events:        eventSvc,
		Clock:         clk,
		Logger:        slog.Default(),
		Design:        designPlanner,
	})
	background.Add(1)
	go func() {
		defer background.Done()
		runReconciler(ctx, reconciler)
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		runExecutionScheduler(ctx, executionScheduler, executionRecovery)
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		runExecutorPoller(ctx, executorRegistry, settingsSvc)
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
		TeamRoom:      teamRoom,
		DesignGraph:   designGraph,
		DesignExports: designExports,
		DesignStarter: designStarterAdapter{planner: designPlanner},
		Execution:     executionHandler,
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
	log.Printf("eztest: stopped")
}
