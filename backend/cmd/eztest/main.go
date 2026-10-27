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
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/gitexec"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/httpcheck"
	resourcepostgres "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	resourcehttp "github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/localfs"
	storagepostgres "github.com/EdouardRichard/EzTest/backend/internal/storage/infrastructure/postgres"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionagent "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/agent"
	versioneventing "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/eventing"
	versionfilestore "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/filestore"
	versiongitread "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/gitread"
	versionpostgres "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	versionresource "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/resource"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
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
	// versionAttachmentDirPerm is the least-privilege mode of the P3 attachment
	// root (design spec §6.7). It is enforced at startup and by the filestore;
	// Windows cannot represent the Unix permission bits and ignores them.
	versionAttachmentDirPerm os.FileMode = 0o700
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

	// Resource context (P2): the catalog repository backs the company, system,
	// environment and app-model ports; the access, session and code-repository
	// repositories back their own aggregates. Requests never carry a company id:
	// the services resolve the seeded default company through the catalog
	// repository. The clock and the read-only login_check/Git adapters are the
	// production implementations of the application ports.
	resourceCatalogRepo := resourcepostgres.NewCatalogRepository(database.Pool)
	resourceAccessRepo := resourcepostgres.NewAccessRepository(database.Pool)
	resourceSessionRepo := resourcepostgres.NewSessionRepository(database.Pool)
	resourceCodeRepo := resourcepostgres.NewCodeRepository(database.Pool)
	catalogService := resourceapp.NewCatalogService(
		resourceCatalogRepo, resourceCatalogRepo, resourceCatalogRepo, resourceCatalogRepo, wallClock{})
	accessService := resourceapp.NewAccessService(
		resourceCatalogRepo, resourceAccessRepo, resourceAccessRepo, resourceCatalogRepo)
	sessionService := resourceapp.NewSessionService(
		resourceSessionRepo, resourceAccessRepo, resourceCatalogRepo, resourceCatalogRepo,
		httpcheck.NewChecker(), wallClock{})
	codeRepoService := resourceapp.NewCodeRepoService(
		resourceCodeRepo, gitexec.NewValidator(), wallClock{})
	resourceHandlers := &resourcehttp.Handlers{
		Catalog:  resourcehttp.NewCatalogHandler(catalogService),
		Access:   resourcehttp.NewAccessHandler(accessService),
		Session:  resourcehttp.NewSessionHandler(sessionService),
		CodeRepo: resourcehttp.NewCodeRepoHandler(codeRepoService),
	}

	// P3 version context: the PostgreSQL repositories, the cross-context
	// adapters (P2 catalog, read-only Git, P0 eventing, P1 agent sessions,
	// filesystem attachments) and one shared Snowflake generator. Every P3
	// service receives the same generator through its With*IDGenerator option,
	// so version, requirement, relation, clarification and attachment ids share
	// one node/sequence and can never collide (D29/INV-15).
	versionRepo := versionpostgres.NewVersionRepository(database.Pool)
	requirementRepo := versionpostgres.NewRequirementRepository(database.Pool)
	clarificationRepo := versionpostgres.NewClarificationRepository(database.Pool)
	versionEvents := versioneventing.NewEventPublisher(eventService)
	versionCatalog := versionresource.NewCatalog(catalogService, catalogService, codeRepoService)
	versionGit := versiongitread.NewGitReader(0)
	snowflake, err := id.NewSnowflake(cfg.SnowflakeNodeID)
	if err != nil {
		return fmt.Errorf("create snowflake id generator (EZTEST_SNOWFLAKE_NODE_ID=%d): %w",
			cfg.SnowflakeNodeID, err)
	}
	// The attachment root is created eagerly with least privilege. The filestore
	// also creates/tightens it on first use, which covers a root removed while
	// the process is running; Windows cannot represent the Unix mode bits.
	if err := os.MkdirAll(cfg.VersionAttachmentDir, versionAttachmentDirPerm); err != nil {
		return fmt.Errorf("create version attachment directory %q: %w", cfg.VersionAttachmentDir, err)
	}
	if err := os.Chmod(cfg.VersionAttachmentDir, versionAttachmentDirPerm); err != nil {
		return fmt.Errorf("set version attachment directory mode on %q: %w", cfg.VersionAttachmentDir, err)
	}
	attachmentStore := versionfilestore.New(cfg.VersionAttachmentDir)
	versionService := versionapp.NewVersionService(
		versionRepo, versionCatalog, versionGit, versionEvents,
		versionapp.WithIDGenerator(snowflake))
	requirementService := versionapp.NewRequirementService(
		requirementRepo, versionRepo, versionEvents,
		versionapp.WithRequirementIDGenerator(snowflake))
	relationService := versionapp.NewRelationService(
		requirementRepo,
		versionapp.WithRelationIDGenerator(snowflake),
		versionapp.WithRelationEventPublisher(versionEvents))
	attachmentService := versionapp.NewAttachmentService(
		requirementRepo, attachmentStore, versionapp.DefaultLimits(), versionEvents,
		versionapp.WithAttachmentIDGenerator(snowflake))
	imageResolver := versionagent.NewImageResolver(attachmentService)

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
	agentProfileService := agentapp.NewProfileService(agentProfiles, agentAuditSink)
	// The P1 gateway resolves the P3 attachment image references
	// (attachment:<id>) through the attachment service; the data URL it returns
	// is transient and never persisted (D30).
	agentGateway := agentllm.NewGateway(llmConfigs, llmClientFactory, imageResolver)

	// The P3 clarification service needs the P1 session/run services, while the
	// P1 run service's tool registry must carry the P3 clarification tools: the
	// graph is cyclic at construction time. This holder breaks the cycle; it is
	// bound to the final P1 services below, before the HTTP server starts.
	p1Agents := &p1AgentServices{}
	agentSessionsAdapter := versionagent.NewAgentSessions(p1Agents, p1Agents, agentProfileService)
	clarificationService := versionapp.NewClarificationService(
		clarificationRepo, requirementRepo, versionRepo, agentSessionsAdapter, versionEvents,
		versionapp.WithClarificationIDGenerator(snowflake))
	agentToolService := versionapp.NewAgentToolService(versionapp.AgentToolServiceDeps{
		Versions:             versionRepo,
		Requirements:         requirementRepo,
		Clarifications:       clarificationRepo,
		RequirementService:   requirementService,
		RelationService:      relationService,
		ClarificationService: clarificationService,
		AttachmentService:    attachmentService,
		Resources:            versionCatalog,
		Git:                  versionGit,
	})

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
	// The 11 P3 tools are appended BEFORE the registry is built so the
	// startup profile validation below checks the real registry the runs use.
	agentToolList = append(agentToolList, agenttools.NewClarificationTools(agentToolService)...)
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
	agentSessionService := agentapp.NewSessionService(
		agentProfiles, agentSessions, agentRuns, agentMessages, runService, agentAuditSink)
	agentToolCatalog := agentapp.NewToolCatalog(agentRegistry)
	// Both P1 services exist now; bind them into the P3 adapter before the HTTP
	// server can accept a clarification request.
	p1Agents.bind(agentSessionService, runService)

	// Startup seeds and cleanup are idempotent: the builtin profile is created
	// only when its key is absent (an existing, possibly edited row is never
	// overwritten) and runs left running by a previous process are marked
	// interrupted.
	if created, err := agentProfiles.EnsureBuiltin(startupCtx, agentdomain.BuiltinProfile()); err != nil {
		return fmt.Errorf("seed builtin agent profile: %w", err)
	} else if created {
		logger.Info("seeded builtin agent profile", "key", agentdomain.BuiltinProfileKey)
	}
	if err := ensureP3Profiles(startupCtx, agentProfiles, agentRegistry, logger); err != nil {
		return err
	}
	if recovered, err := runService.RecoverInterrupted(startupCtx); err != nil {
		return fmt.Errorf("recover interrupted agent runs: %w", err)
	} else if recovered > 0 {
		logger.Info("marked interrupted agent runs", "count", recovered)
	}

	// The P3 HTTP handlers consume the version services, the two aggregate
	// readers implemented by the repositories, the P2 catalog adapter, the P1
	// agent-sessions adapter (clarification messages) and the eventing
	// snapshot/stream services.
	versionHandlers := versionhttp.NewHandlers(versionhttp.HandlersDeps{
		Versions:              versionService,
		VersionCounts:         versionRepo,
		ResourceCatalog:       versionCatalog,
		Requirements:          requirementService,
		RequirementStats:      requirementRepo,
		Relations:             relationService,
		Attachments:           attachmentService,
		Clarification:         clarificationService,
		ClarificationMessages: agentSessionsAdapter,
		// The readiness probe refuses the clarification endpoints that start a
		// run while the default LLM configuration is missing or cannot serve
		// tools (503 ai_unavailable, spec §8.6). Capacity is enforced atomically
		// by RunService.Start, whose ErrCapacity maps to the same 503.
		AIReady: func(ctx context.Context) error {
			_, err := agentGateway.Prepare(ctx, "")
			return err
		},
		Events:          eventService,
		EventReader:     eventStore,
		EventSubscriber: eventHub,
	})
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
			Resources: resourceHandlers,
			Version:   versionHandlers,
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

// p1AgentServices late-binds the P1 agent session and run services into the P3
// agent-sessions adapter. The dependency graph is cyclic at construction time:
// the P1 run service consumes the tool registry, which must already contain the
// P3 clarification tools, while the P3 clarification service consumes the P1
// session and run services. The composition root passes this holder to
// versionagent.NewAgentSessions and binds the final services immediately after
// both exist; no HTTP request can arrive before the server starts.
type p1AgentServices struct {
	sessions *agentapp.SessionService
	runs     *agentapp.RunService
}

// bind fills the late-bound P1 services. It is called once, before the HTTP
// server starts.
func (s *p1AgentServices) bind(sessions *agentapp.SessionService, runs *agentapp.RunService) {
	s.sessions = sessions
	s.runs = runs
}

// errUnbound reports a request that raced the bind, which is a startup bug.
func (s *p1AgentServices) errUnbound(service string) error {
	return fmt.Errorf("eztest: P1 %s service is not bound; the composition root did not finish wiring", service)
}

// CreateWithOptions implements versionagent.SessionStore.
func (s *p1AgentServices) CreateWithOptions(ctx context.Context, profileID int64, title string, actor *int64, opts agentapp.SessionOptions) (agentdomain.Session, error) {
	if s.sessions == nil {
		return agentdomain.Session{}, s.errUnbound("session")
	}
	return s.sessions.CreateWithOptions(ctx, profileID, title, actor, opts)
}

// ListMessages implements versionagent.SessionStore.
func (s *p1AgentServices) ListMessages(ctx context.Context, sessionID, beforeID int64, limit int) ([]agentdomain.Message, bool, error) {
	if s.sessions == nil {
		return nil, false, s.errUnbound("session")
	}
	return s.sessions.ListMessages(ctx, sessionID, beforeID, limit)
}

// Start implements versionagent.RunStarter.
func (s *p1AgentServices) Start(ctx context.Context, sessionID int64, message string, actor *int64) (agentdomain.StartResult, error) {
	if s.runs == nil {
		return agentdomain.StartResult{}, s.errUnbound("run")
	}
	return s.runs.Start(ctx, sessionID, message, actor)
}

// StartWithParts implements versionagent.RunStarter.
func (s *p1AgentServices) StartWithParts(ctx context.Context, sessionID int64, parts []agentdomain.Part, actor *int64) (agentdomain.StartResult, error) {
	if s.runs == nil {
		return agentdomain.StartResult{}, s.errUnbound("run")
	}
	return s.runs.StartWithParts(ctx, sessionID, parts, actor)
}

// HasRunningByChannel implements versionagent.RunStarter.
func (s *p1AgentServices) HasRunningByChannel(ctx context.Context, channel string) (bool, error) {
	if s.runs == nil {
		return false, s.errUnbound("run")
	}
	return s.runs.HasRunningByChannel(ctx, channel)
}

// ensureP3Profiles seeds the three P3 agent profiles and validates every
// allowed tool against the real tool registry. The validation runs before any
// insert and fails the startup loudly on a mismatch: a profile that allows an
// unregistered tool would silently offer fewer tools than it declares (spec
// §8.1). EnsureBuiltin is idempotent and never overwrites an existing row, so a
// later startup keeps the edited prompts and budgets.
func ensureP3Profiles(ctx context.Context, profiles *agentpostgres.ProfileRepository, registry *agentapp.ToolRegistry, logger *slog.Logger) error {
	seeds := agentdomain.ClarificationProfiles()
	for _, profile := range seeds {
		if err := profile.Validate(); err != nil {
			return fmt.Errorf("validate P3 agent profile %q: %w", profile.Key, err)
		}
		for _, name := range profile.AllowedTools {
			if tool, ok := registry.Get(name); !ok || tool == nil {
				return fmt.Errorf("P3 agent profile %q allows tool %q, which is not registered", profile.Key, name)
			}
		}
	}
	for _, profile := range seeds {
		created, err := profiles.EnsureBuiltin(ctx, profile)
		if err != nil {
			return fmt.Errorf("seed P3 agent profile %q: %w", profile.Key, err)
		}
		if created {
			logger.Info("seeded P3 agent profile", "key", profile.Key)
		}
	}
	return nil
}

// wallClock is the production resource application.Clock. The services convert
// the returned instant to UTC where the database stores timestamptz.
type wallClock struct{}

// Now returns the current time.
func (wallClock) Now() time.Time { return time.Now() }

// baseContext returns the http.Server.BaseContext callback that derives every
// request context from ctx. Canceling ctx on SIGINT/SIGTERM makes handlers that
// only observe their request context (the SSE streams) return, so Shutdown can
// drain instead of waiting for client disconnects.
func baseContext(ctx context.Context) func(net.Listener) context.Context {
	return func(net.Listener) context.Context { return ctx }
}
