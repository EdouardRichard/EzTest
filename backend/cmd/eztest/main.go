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
	agentpg "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
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

	// P1 control plane and clarification services.
	requirementSvc := versionapp.NewRequirementService(
		versionpg.NewRequirementRepository(pool), versionSvc, resourceSvc,
		versionapp.NoopCaseStaleHook{}, auditSvc, eventSvc, ids, clk)
	workItems := agentpg.NewWorkItemRepository(pool, ids)
	workQueue := agentapp.NewWorkQueue(workItems, clk, 30*time.Second, 3)
	teamRoom := agentapp.NewTeamRoom(agentpg.NewTeamRoomRepository(pool, ids), clk, ids, settingsSvc, eventSvc)
	enqueuer := versionTaskEnqueuer{queue: workQueue}
	attachmentSvc := versionapp.NewAttachmentService(
		versionpg.NewAttachmentRepository(pool), contentstore.New(storageSvc), enqueuer,
		attachments.NewParser(), eventSvc, ids, clk)
	conversationSvc := versionapp.NewConversationService(
		versionpg.NewConversationRepository(pool, ids), enqueuer, attachmentSvc, versionSvc, clk, ids, eventSvc)
	ports := versionadapter.New(requirementSvc, conversationSvc, versionSvc, attachmentSvc, storageSvc)

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
		ToolDeps:     tools.Deps{Requirement: ports, Attachment: ports, Resource: ports},
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
