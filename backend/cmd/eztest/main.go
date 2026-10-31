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

var version = "0.2.0-p1"

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
		},
		InstanceID:   instanceID,
	})
	worker := agentapp.NewWorker(workQueue, runner, settingsSvc, slog.Default(), instanceID, agentapp.WithWorkerEvents(eventSvc))
	go func() {
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
	go runReconciler(ctx, reconciler)

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
}
