// Command eztest is the single composition root of the EzTest backend.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	agentaudit "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/audit"
	agentevent "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/eventing"
	agentllm "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/llm"
	agentpostgres "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/postgres"
	agenttools "github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/tools"
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
	// shutdownTimeout is the grace period for in-flight requests after the
	// shutdown signal. Open SSE connections are canceled through BaseContext
	// before Shutdown is called, so only genuinely stuck handlers hit it.
	shutdownTimeout = 10 * time.Second
	// agentShutdownTimeout is how long the Agent run service gets to cancel and
	// finish the in-flight runs before the HTTP server starts draining.
	agentShutdownTimeout = 5 * time.Second
	// agentHardTimeout is the fallback hard-stop budget the run service adds to
	// a profile's run_seconds when the profile leaves hard_timeout_seconds
	// unset (design spec §4.1).
	agentHardTimeout = 60 * time.Second
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

	// Agent runtime: the four postgres repositories, the cross-context adapters
	// (LLM gateway, event sink, audit sink) and the tool registry. The same
	// registry instance backs both the run service and the HTTP tool catalog, so
	// the profile allowlist validation and the live tool list never disagree.
	agentProfiles := agentpostgres.NewProfileRepository(database.Pool)
	agentSessions := agentpostgres.NewSessionRepository(database.Pool)
	agentRuns := agentpostgres.NewRunRepository(database.Pool)
	agentMessages := agentpostgres.NewMessageRepository(database.Pool)

	agentAuditSink := agentaudit.NewSink(auditor)
	agentEventSink := agentevent.NewSink(eventService)
	agentGateway := agentllm.NewGateway(llmConfigs, llmClientFactory)

	// http.get keeps its documented 5 s request bound (a zero duration selects
	// it); the tool executor applies the global EZTEST_AGENT_TOOL_TIMEOUT_MS
	// budget on top. The file tools are only registered when a read-only root is
	// configured: an invalid root is a startup failure, never a silent skip.
	agentToolList := []agentdomain.Tool{
		agenttools.NewTestEcho(),
		agenttools.NewTestFail(),
		agenttools.NewHTTPGet(cfg.AgentHTTPAllowlist, 0),
	}
	if cfg.AgentFileRoot != "" {
		fileTools, err := agenttools.NewFileTools(cfg.AgentFileRoot)
		if err != nil {
			return fmt.Errorf("configure agent file tools (EZTEST_AGENT_FILE_ROOT=%q): %w", cfg.AgentFileRoot, err)
		}
		agentToolList = append(agentToolList, fileTools...)
	}
	agentRegistry := agentapp.NewToolRegistry(agentToolList...)
	agentExecutor := agentapp.NewToolExecutor(
		agentRegistry, agentAuditSink, time.Duration(cfg.AgentToolTimeoutMS)*time.Millisecond)
	runService := agentapp.NewRunService(agentapp.RunServiceDeps{
		Profiles: agentProfiles,
		Sessions: agentSessions,
		Runs:     agentRuns,
		Messages: agentMessages,

		LLM:      agentGateway,
		Tools:    agentRegistry,
		Executor: agentExecutor,

		Events: agentEventSink,
		Audit:  agentAuditSink,

		MaxConcurrentRuns: cfg.AgentMaxConcurrentRuns,
		HardTimeout:       agentHardTimeout,
	})
	agentProfileService := agentapp.NewProfileService(agentProfiles, agentAuditSink)
	agentSessionService := agentapp.NewSessionService(
		agentProfiles, agentSessions, agentRuns, agentMessages, runService, agentAuditSink)
	agentToolCatalog := agentapp.NewToolCatalog(agentRegistry)

	// Startup seeds and cleanup are idempotent: the builtin profile is created
	// only when its key is absent (an existing, possibly edited row is never
	// overwritten) and runs left running by a previous process are marked
	// interrupted.
	if created, err := agentProfiles.EnsureBuiltin(startupCtx, agentdomain.BuiltinProfile()); err != nil {
		return fmt.Errorf("seed builtin agent profile: %w", err)
	} else if created {
		logger.Info("seeded builtin agent profile", "key", agentdomain.BuiltinProfileKey)
	}
	if recovered, err := runService.RecoverInterrupted(startupCtx); err != nil {
		return fmt.Errorf("recover interrupted agent runs: %w", err)
	} else if recovered > 0 {
		logger.Info("marked interrupted agent runs", "count", recovered)
	}
	cancelStartup()

	// Every request context descends from baseCtx. Canceling it on shutdown
	// wakes long-lived SSE handlers, which only exit on r.Context().Done().
	baseCtx, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

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
			Agent: &server.AgentDependencies{
				Profiles: agentProfileService,
				Sessions: agentSessionService,
				Runs:     runService,
				Tools:    agentToolCatalog,
			},
		}),
		BaseContext:       baseContext(baseCtx),
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
		// Cancel request contexts before Shutdown: SSE handlers block on
		// r.Context().Done() and would otherwise keep Shutdown waiting until
		// the grace period expires.
		cancelBase()
		// Cancel and drain the Agent runs before the HTTP server: their runners
		// write terminal rows and activity events with a detached context, so a
		// short bounded wait lets them finish cleanly.
		agentShutdownCtx, cancelAgent := context.WithTimeout(context.Background(), agentShutdownTimeout)
		runService.Shutdown(agentShutdownCtx)
		cancelAgent()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			// The shutdown was requested intentionally, so a grace-period
			// timeout is not a process failure: force-close the remaining
			// connections and exit cleanly.
			logger.Warn("graceful shutdown did not finish; forcing connection close", "error", err)
			if closeErr := srv.Close(); closeErr != nil {
				logger.Warn("force close after shutdown timeout failed", "error", closeErr)
			}
		}
		return nil
	}
}

// baseContext returns the http.Server.BaseContext callback that derives every
// request context from ctx. Canceling ctx on SIGINT/SIGTERM makes handlers that
// only observe their request context (the SSE streams) return, so Shutdown can
// drain instead of waiting for client disconnects.
func baseContext(ctx context.Context) func(net.Listener) context.Context {
	return func(net.Listener) context.Context { return ctx }
}
