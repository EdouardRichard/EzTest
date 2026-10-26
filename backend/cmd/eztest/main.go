// Command eztest is the single composition root of the EzTest backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/deepseek"
	llmpostgres "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/crypto"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/localfs"
	storagepostgres "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/postgres"
)

const (
	// startupTimeout bounds database connection, migration and admin seeding
	// work at boot.
	startupTimeout = 30 * time.Second
	// bcryptCost is the documented password hashing cost.
	bcryptCost = 12
)

func main() {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		fmt.Fprintln(os.Stderr, "eztest:", err)
		os.Exit(1)
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	if err := serve(cfg, logger); err != nil {
		logger.Error("eztest stopped with error", "error", err)
		os.Exit(1)
	}
}

// serve opens the database, applies pending migrations, then starts the HTTP
// server and shuts it down gracefully on SIGINT or SIGTERM.
func serve(cfg config.Config, logger *slog.Logger) error {
	startupCtx, cancelStartup := context.WithTimeout(context.Background(), startupTimeout)
	defer cancelStartup()

	database, err := db.Open(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer database.Close()
	if err := db.MigrateEmbedded(startupCtx, database.Pool); err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}

	userRepo := identitypostgres.NewUserRepository(database.Pool)
	tokenRepo := identitypostgres.NewTokenRepository(database.Pool)
	hasher := password.NewHasher(bcryptCost)
	auditor := auditapp.NewService(auditpostgres.NewRepository(database.Pool))
	userService := identityapp.NewUserService(userRepo, hasher, auditor, tokenRepo)
	if err := userService.EnsureAdmin(startupCtx, cfg.AdminUsername, cfg.AdminPassword); err != nil {
		return fmt.Errorf("seed admin (set EZTEST_ADMIN_USERNAME and EZTEST_ADMIN_PASSWORD when no admin exists): %w", err)
	}
	authService := identityapp.NewAuthService(
		userRepo,
		tokenRepo,
		hasher,
		time.Now,
		auditor,
	)
	keyring, err := crypto.NewKeyring(cfg.SecretKeys, cfg.SecretKeyActive)
	if err != nil {
		return fmt.Errorf("create secret keyring: %w", err)
	}
	llmRepo := llmpostgres.NewConfigRepository(database.Pool, keyring)
	llmConfigs := llmapp.NewConfigService(llmRepo, auditor)
	seededLLM, err := llmConfigs.SeedFromEnv(startupCtx, cfg.LLMSeed.BaseURL, cfg.LLMSeed.Key, cfg.LLMSeed.Model)
	if err != nil {
		return fmt.Errorf("seed default LLM configuration: %w", err)
	}
	if seededLLM {
		logger.Info("seeded default LLM configuration from environment", "name", "default", "protocol", "chat_completions")
	}
	// The client factory applies each stored configuration's protocol, retry,
	// interval and timeout policy to the upstream adapter.
	llmClientFactory := func(llmCfg llm.Config) llmapp.Client {
		timeout := time.Duration(llmCfg.TimeoutMS) * time.Millisecond
		if timeout <= 0 {
			timeout = time.Duration(llm.DefaultTimeoutMS) * time.Millisecond
		}
		interval := time.Duration(llmCfg.RetryIntervalMS) * time.Millisecond
		base := deepseek.NewDeepSeekClient(llmCfg, &http.Client{Timeout: timeout})
		return llmapp.NewRetryClient(base, llmCfg.RetryCount, interval, timeout)
	}
	idempotencyStore := idempotency.NewPostgresStore(database.Pool)
	eventStore := eventpostgres.NewEventStore(database.Pool)
	eventHub := eventinginfra.NewHub()
	eventService := eventingapp.NewService(eventStore, eventHub)
	// Content-addressed objects live below EZTEST_STORAGE_DIR; the directory is
	// created lazily on the first upload. The durable metadata (size, content
	// type, relative path) lives in the storage_objects table and is read back
	// on Open/Stat so downloads carry the stored media type.
	storageService := storageapp.NewService(
		localfs.New(cfg.StorageDir),
		storagepostgres.NewMetadataRepository(database.Pool),
		auditor,
	)
	cancelStartup()

	srv := &http.Server{
		Addr: cfg.HTTPAddr,
		Handler: server.New(cfg, logger, server.Dependencies{
			Pinger:           database,
			Auth:             authService,
			Users:            userService,
			Audit:            auditor,
			Idempotency:      idempotencyStore,
			Events:           eventService,
			EventReader:      eventStore,
			EventHub:         eventHub,
			LLMConfigs:       llmConfigs,
			LLMClientFactory: llmClientFactory,
			Storage:          storageService,
		}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("http server listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		stop()
		logger.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	}
}
