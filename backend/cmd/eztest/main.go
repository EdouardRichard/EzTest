// Command eztest is the single composition root of the EzTest API.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
)

var version = "0.2.0-p0"

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

	llmSvc := llmapp.New(llminfra.NewRepository(pool), llminfra.NewClient(), ids, clk, auditSvc, eventSvc)
	storageSvc := storageapp.New(storagepg.New(pool), storagepg.NewFileStore(), settingsSvc, ids, auditSvc, eventSvc)
	resourceSvc := resourceapp.New(resourcepg.New(pool), ids, clk, nil, auditSvc, eventSvc)
	versionSvc := versionapp.New(versionpg.New(pool, ids), resourceSvc, ids, clk, auditSvc, eventSvc)
	resourceSvc.SetUsageChecker(versionSvc)

	handler := server.New(server.Deps{
		Config:   cfg,
		Pool:     pool,
		Version:  version,
		Settings: settingsSvc,
		Identity: identitySvc,
		Audit:    auditSvc,
		Events:   eventSvc,
		Storage:  storageSvc,
		LLM:      llmSvc,
		Resource: resourceSvc,
		Versions: versionSvc,
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
