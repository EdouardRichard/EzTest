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
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelaudit "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/audit"
	codeinteleventing "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/eventing"
	codeintelgitx "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelllm "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/llm"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	codeintelversion "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/version"
	codeintelhttp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	"github.com/EdouardRichard/EzTest/backend/internal/db"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executionagenttool "github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executionagent "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/agent"
	executionaudit "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/audit"
	executionbrowser "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/browser"
	executioneventing "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/eventing"
	executionfilestore "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/filestore"
	executionmindmap "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/mindmap"
	executionpostgres "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/postgres"
	executionresource "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/resource"
	executionversion "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/version"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	"github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/password"
	identitypostgres "github.com/EdouardRichard/EzTest/backend/internal/identity/infrastructure/postgres"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/deepseek"
	llmpostgres "github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/postgres"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmapagent "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/agent"
	mindmapagenttools "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/agenttools"
	mindmapaudit "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/audit"
	mindmapcodeintel "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/codeintel"
	mindmapeventing "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/eventing"
	mindmapfilestore "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/filestore"
	mindmaphttpclient "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/httpclient"
	mindmappostgres "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/postgres"
	mindmapresource "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/resource"
	mindmapversion "github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/version"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
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
	// mindmapExportDirPerm is the least-privilege mode of the P5 export root
	// (D30). The export filestore creates/tightens it as well; creating it at
	// startup turns a misconfigured EZTEST_EXPORT_DIR into a boot failure.
	mindmapExportDirPerm os.FileMode = 0o700
	// executionEvidenceDirPerm is the least-privilege mode of the P6 evidence
	// root (spec §5.7/D30). The filestore creates/tightens it on first use;
	// creating it eagerly turns a misconfigured EZTEST_EVIDENCE_DIR into a boot
	// failure. Windows cannot represent the Unix permission bits.
	executionEvidenceDirPerm os.FileMode = 0o700
	// executorHealthTimeout bounds the boot-time executor health warning.
	executorHealthTimeout = 5 * time.Second
	// codeIntelWorkerQueueSize is the bounded async-run queue of design spec
	// §6.8 (16 jobs; a full queue answers 429 analysis_queue_full).
	codeIntelWorkerQueueSize = 16
	// codeIntelImpactMaxFacts and codeIntelImpactMaxPromptBytes are the §6.6
	// AI impact prompt budgets (R15): at most 200 facts and 64 KiB of prompt
	// text per planning call.
	codeIntelImpactMaxFacts       = 200
	codeIntelImpactMaxPromptBytes = 64 << 10
	// codeIntelShutdownBuffer is the extra grace added to the configured
	// analysis timeout for the worker's graceful stop (design spec §6.8): a run
	// already executing when shutdown starts may still need its whole
	// analysis_timeout_ms to persist its terminal state.
	codeIntelShutdownBuffer = 10 * time.Second
	// codeIntelDefaultAnalysisTimeoutMS and codeIntelMaxAnalysisTimeoutMS mirror
	// the EZTEST_CODEINTEL_ANALYSIS_TIMEOUT_MS bounds owned by internal/config.
	// They keep the shutdown budget positive and bounded even for a defensively
	// built Config that never went through config.Load.
	codeIntelDefaultAnalysisTimeoutMS = 180000
	codeIntelMaxAnalysisTimeoutMS     = 600000
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
	// type, relative path) lives in the storage_object table and is read back
	// on Open/Stat so downloads carry the stored media type.
	storageService := storageapp.NewService(
		localfs.New(cfg.StorageDir),
		storagepostgres.NewMetadataRepository(database.Pool),
		auditor,
	)
	// One shared Snowflake generator backs every context so ids can never
	// collide across contexts or services (D29/INV-15). It is created before the
	// P2 resource context because the auth-flow repository mints its ids too.
	snowflake, err := id.NewSnowflake(cfg.SnowflakeNodeID)
	if err != nil {
		return fmt.Errorf("create snowflake id generator (EZTEST_SNOWFLAKE_NODE_ID=%d): %w",
			cfg.SnowflakeNodeID, err)
	}

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
	resourceAuthFlows := resourcepostgres.NewAuthFlowRepository(database.Pool, snowflake)
	catalogService := resourceapp.NewCatalogService(
		resourceCatalogRepo, resourceCatalogRepo, resourceCatalogRepo, resourceCatalogRepo, wallClock{})
	accessService := resourceapp.NewAccessService(
		resourceCatalogRepo, resourceAccessRepo, resourceAccessRepo, resourceCatalogRepo, resourceAuthFlows)
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
		AuthFlow: resourcehttp.NewAuthFlowHandler(
			resourceapp.NewAuthFlowService(resourceAuthFlows, resourceCatalogRepo)),
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

	// Code intelligence context (P4, D32/D33): the nine PostgreSQL repositories,
	// the read-only adapters over the P2/P3/P0 contexts, the codeintel LLM
	// gateway and the bounded async worker. A missing or unusable default LLM
	// configuration is not a startup failure: the completer stays nil and
	// analyses degrade to deterministic T0 results (design spec §6.2).
	codeIntelGit := codeintelgitx.NewGitReader(codeintelgitx.GitConfig{})
	codeIntelLLMGateway, codeIntelLLMErr := codeintelllm.NewLLMGateway(codeintelllm.LLMGatewayConfig{
		ConfigService: llmConfigs,
		ClientFactory: llmClientFactory,
	})
	// A missing or unusable default LLM configuration is an intentional
	// degraded mode, never a startup failure: the helper logs once (the error
	// carries only the stable "not configured" reason, never a prompt, response
	// or secret) and returns a nil completer, so the process keeps serving and
	// analyses fall back to deterministic T0 results (design spec §6.2).
	codeIntelLLM := codeIntelLLMCompleter(codeIntelLLMGateway, codeIntelLLMErr, logger)

	// The nine codeintel PostgreSQL repositories (design spec §5).
	codeIntelIndexRuns := codeintelpg.NewIndexRunRepository(database.Pool)
	codeIntelEntities := codeintelpg.NewEntityRepository(database.Pool)
	codeIntelChangeSets := codeintelpg.NewChangeSetRepository(database.Pool)
	codeIntelAnalyses := codeintelpg.NewAnalysisRepository(database.Pool)
	codeIntelCandidates := codeintelpg.NewCandidateRepository(database.Pool)
	codeIntelFacts := codeintelpg.NewFactVersionRepository(database.Pool)
	codeIntelCache := codeintelpg.NewCacheRepository(database.Pool)
	codeIntelEdges := codeintelpg.NewEdgeRepository(database.Pool)
	codeIntelAnchors := codeintelpg.NewAnchorRepository(database.Pool)

	// Cross-context adapters (design spec §3.5): the P2 repository catalog (repo
	// and app-model reads), the P2 index-state writer, the P3 read-only version
	// catalog, the P0 version-scoped event publisher and the audit recorder.
	codeIntelRepos := codeintelresource.NewCatalog(codeRepoService, catalogService)
	codeIntelIndexState := codeintelresource.NewIndexStateWriter(codeRepoService)
	codeIntelVersions := codeintelversion.NewCatalog(versionService, requirementService, relationService)
	codeIntelEvents := codeinteleventing.NewPublisher(eventService)
	codeIntelAudit := codeintelaudit.NewRecorder(auditor)

	// The worker is built first and receives no runner until both async
	// services exist: the services depend on its Enqueuer port, so the graph is
	// cyclic at construction time (design spec §3.6).
	codeIntelWorker := codeintelapp.NewWorker(codeintelapp.WorkerConfig{
		QueueSize:   codeIntelWorkerQueueSize,
		Concurrency: cfg.CodeIntelMaxConcurrentAnalyses,
		Timeout:     time.Duration(cfg.CodeIntelAnalysisTimeoutMS) * time.Millisecond,
		Logger:      logger,
	})

	codeIntelImpact := codeintelapp.NewImpactService(codeintelapp.ImpactServiceDeps{
		LLM:       codeIntelLLM,
		Versions:  codeIntelVersions,
		AppModels: codeIntelRepos,
		Anchors:   codeIntelAnchors,
		Edges:     codeIntelEdges,
		Git:       codeIntelGit,
		// Repos resolves the checkout the AI candidate evidence is verified
		// against (design spec §6.3/§6.6): without it the impact service cannot
		// read the commit content and every AI candidate would fail its check.
		Repos:           codeIntelRepos,
		IDs:             snowflake,
		MaxRequirements: codeinteldomain.MaxRequirementsInPrompt,
		MaxFacts:        codeIntelImpactMaxFacts,
		MaxPromptBytes:  codeIntelImpactMaxPromptBytes,
	})
	codeIntelIndex := codeintelapp.NewIndexService(codeintelapp.IndexServiceDeps{
		Git:           codeIntelGit,
		Repos:         codeIntelRepos,
		IndexRuns:     codeIntelIndexRuns,
		Entities:      codeIntelEntities,
		ChangeSets:    codeIntelChangeSets,
		State:         codeIntelIndexState,
		Queue:         codeIntelWorker,
		Audit:         codeIntelAudit,
		IDs:           snowflake,
		Clock:         wallClock{},
		MaxIndexFiles: cfg.CodeIntelMaxIndexFiles,
	})
	codeIntelAnalysis := codeintelapp.NewAnalysisService(codeintelapp.AnalysisServiceDeps{
		Git:        codeIntelGit,
		Repos:      codeIntelRepos,
		Versions:   codeIntelVersions,
		ChangeSets: codeIntelChangeSets,
		Entities:   codeIntelEntities,
		Edges:      codeIntelEdges,
		Facts:      codeIntelFacts,
		Cache:      codeIntelCache,
		Analyses:   codeIntelAnalyses,
		Candidates: codeIntelCandidates,
		Anchors:    codeIntelAnchors,
		Queue:      codeIntelWorker,
		LLM:        codeIntelLLM,
		Planner:    codeIntelImpact,
		Notifier:   codeIntelEvents,
		Audit:      codeIntelAudit,
		IDs:        snowflake,
		Clock:      wallClock{},
		Config: codeintelapp.AnalysisConfig{
			MaxChangedFiles: cfg.CodeIntelMaxChangedFiles,
			MaxFileBytes:    cfg.CodeIntelMaxFileBytes,
			MaxExcerptLines: cfg.CodeIntelMaxExcerptLines,
			MaxIndexFiles:   cfg.CodeIntelMaxIndexFiles,
			PromptVersion:   cfg.CodeIntelPromptVersion,
			// Timeout is left zero: the worker applies the configured
			// analysis_timeout_ms to every run (design spec §3.6).
		},
	})
	codeIntelQuery := codeintelapp.NewQueryService(codeintelapp.QueryServiceDeps{
		Git:        codeIntelGit,
		Repos:      codeIntelRepos,
		Entities:   codeIntelEntities,
		ChangeSets: codeIntelChangeSets,
		Versions:   codeIntelVersions,
		Audit:      codeIntelAudit,
		Config: codeintelapp.QueryConfig{
			MaxTreeEntries:  codeinteldomain.MaxTreeEntriesPerPage,
			MaxReadLines:    codeinteldomain.MaxReadLines,
			MaxDiffBytes:    cfg.CodeIntelMaxDiffBytes,
			MaxChangedFiles: codeinteldomain.MaxChangedFilesStored,
		},
	})
	codeIntelAnchor := codeintelapp.NewAnchorService(codeintelapp.AnchorServiceDeps{
		Repos:     codeIntelRepos,
		Versions:  codeIntelVersions,
		AppModels: codeIntelRepos,
		Git:       codeIntelGit,
		Anchors:   codeIntelAnchors,
		IDs:       snowflake,
		Clock:     wallClock{},
	})

	// Both async services exist now, so the worker can execute them and recover
	// the previous process's leftover queued/running runs before serving its
	// first job (design spec §6.8). The deferred stop runs after the HTTP
	// server drained and before database.Close (that defer was registered
	// first, so it runs last).
	codeIntelWorker.SetRunner(codeIntelRunner{index: codeIntelIndex, analyses: codeIntelAnalysis})
	codeIntelWorker.SetRecoverer(codeintelapp.StaleRunRecoverer{
		IndexRuns: codeIntelIndexRuns,
		Analyses:  codeIntelAnalyses,
	})
	codeIntelWorker.Start()
	defer func() {
		// The stop budget tracks the configured analysis timeout, not a fixed
		// 10 s: design spec §6.8 requires the graceful shutdown to wait for the
		// current run up to its own timeout. database.Close was deferred before
		// this block, so it always runs after the worker stopped, even when the
		// configured timeout is zero or invalid (the helper keeps the budget
		// positive and capped at the documented maximum).
		budget := codeintelShutdownTimeout(cfg)
		logger.Info("stopping codeintel worker", "timeout", budget)
		stopCtx, cancelStop := context.WithTimeout(context.Background(), budget)
		defer cancelStop()
		codeIntelWorker.Stop(stopCtx)
	}()

	// The handler group is assembled here, in the composition root; no service
	// is constructed inside the HTTP layer (design spec §3.6, Agent.md §6.2).
	codeIntelHandler := codeintelhttp.NewHandler(codeintelhttp.HandlerDeps{
		Index:    codeIntelIndex,
		Query:    codeIntelQuery,
		Analyses: codeIntelAnalysis,
		Anchors:  codeIntelAnchor,
		Repos:    codeIntelRepos,
	})

	// P5 mindmap context: the nine repositories, the cross-context adapters (P3
	// version/requirements, P2 catalog/session, P4 code facts and case anchors,
	// P0 eventing/audit, the read-only HTTP prober and the export filesystem)
	// and the manual graph service. The graph service is built before the agent
	// tool registry because the six case_* tools consume it; the run coordinator
	// needs the P1 session/run services, so it is built after p1Agents.bind.
	mindmapGraphTx := mindmappostgres.NewGraphTx(database.Pool)
	mindmapNodes := mindmappostgres.NewNodeRepository(database.Pool)
	mindmapSteps := mindmappostgres.NewStepRepository(database.Pool)
	mindmapAssertions := mindmappostgres.NewAssertionRepository(database.Pool)
	mindmapSources := mindmappostgres.NewSourceRepository(database.Pool)
	mindmapReviews := mindmappostgres.NewReviewRepository(database.Pool)
	mindmapFindings := mindmappostgres.NewFindingRepository(database.Pool)
	mindmapBaselines := mindmappostgres.NewBaselineRepository(database.Pool)
	mindmapRuns := mindmappostgres.NewRunRepository(database.Pool)
	mindmapExports := mindmappostgres.NewExportRepository(database.Pool)

	mindmapLifecycle := mindmapversion.NewLifecycleAdapter(versionService)
	mindmapRequirements := mindmapversion.NewRequirementReader(
		requirementService, relationService, attachmentService)
	mindmapCatalog := mindmapresource.NewCatalogAdapter(catalogService, catalogService)
	mindmapSessions := mindmapresource.NewSessionAdapter(sessionService, wallClock{})
	mindmapCodeFacts := mindmapcodeintel.NewCodeFactsAdapter(codeIntelAnalysis, codeIntelQuery, versionService)
	mindmapAnchors := mindmapcodeintel.NewAnchorAdapter(codeIntelAnchor)
	mindmapEvents := mindmapeventing.NewEventPublisher(eventService)
	mindmapAudit := mindmapaudit.NewAuditRecorder(auditor)
	// The prober's configured allowlist is deliberately empty: every probe
	// carries the environment base host + allowed_hosts explicitly (the
	// application.Prober contract), so one shared client serves every
	// environment without weakening the SSRF host check.
	mindmapProber := mindmaphttpclient.New(mindmaphttpclient.Options{})
	// Export files live on the filesystem (D30); the store confines every path
	// to this root and writes 0600 files below 0700 directories. The root is
	// created eagerly so a missing EZTEST_EXPORT_DIR fails at startup instead of
	// on the first export request.
	if err := os.MkdirAll(cfg.ExportDir, mindmapExportDirPerm); err != nil {
		return fmt.Errorf("create mindmap export directory %q: %w", cfg.ExportDir, err)
	}
	mindmapExportStore, err := mindmapfilestore.New(cfg.ExportDir)
	if err != nil {
		return fmt.Errorf("configure mindmap export store (EZTEST_EXPORT_DIR=%q): %w", cfg.ExportDir, err)
	}

	mindmapGraph := mindmapapp.NewGraphService(mindmapapp.GraphDeps{
		Graph:        mindmapGraphTx,
		Nodes:        mindmapNodes,
		Steps:        mindmapSteps,
		Assertions:   mindmapAssertions,
		Sources:      mindmapSources,
		Baselines:    mindmapBaselines,
		Versions:     mindmapLifecycle,
		Requirements: mindmapRequirements,
		Anchors:      mindmapAnchors,
		Events:       mindmapEvents,
		IDs:          snowflake,
		Clock:        wallClock{},
		Logger:       logger,
	})
	// The P6 execution context reads the frozen baseline/snapshots and marks
	// executed cases through this narrow projection (spec §4.2).
	mindmapExecution := mindmapapp.NewExecutionService(mindmapapp.ExecutionDeps{
		Graph:      mindmapGraphTx,
		Nodes:      mindmapNodes,
		Steps:      mindmapSteps,
		Assertions: mindmapAssertions,
		Sources:    mindmapSources,
		Baselines:  mindmapBaselines,
		Events:     mindmapEvents,
		Clock:      wallClock{},
	})

	// P6 execution context: repositories, cross-context adapters, the evidence
	// filesystem store, the executor client and the shared tool set. The tool
	// set is built before the P1 registry so the execution-agent profile seed
	// validates its allowlist against the live registry (spec §7.1). The agent
	// runtime adapter does not exist until the P1 services are bound, so the
	// tools resolve their per-case session through a late-bound closure.
	executionBatches := executionpostgres.NewBatchRepository(database.Pool)
	executionCaseRuns := executionpostgres.NewCaseRunRepository(database.Pool)
	executionSteps := executionpostgres.NewStepRepository(database.Pool)
	executionAssertions := executionpostgres.NewAssertionRepository(database.Pool)
	executionEvidence := executionpostgres.NewEvidenceRepository(database.Pool)
	executionEvidenceRefs := executionpostgres.NewEvidenceRefRepository(database.Pool)
	executionQueries := executionpostgres.NewExecutionQueries(database.Pool)
	executionVersionPort := executionversion.New(versionService)
	executionMindmapPort := executionmindmap.New(mindmapExecution)
	executionSessionPort := executionresource.New(sessionService, catalogService)
	executionEvents := executioneventing.New(eventService)
	executionAudit := executionaudit.NewSink(auditor)

	if err := os.MkdirAll(cfg.EvidenceDir, executionEvidenceDirPerm); err != nil {
		return fmt.Errorf("create evidence directory %q: %w", cfg.EvidenceDir, err)
	}
	if err := os.Chmod(cfg.EvidenceDir, executionEvidenceDirPerm); err != nil {
		return fmt.Errorf("set evidence directory mode on %q: %w", cfg.EvidenceDir, err)
	}
	evidenceStore, err := executionfilestore.New(cfg.EvidenceDir,
		executionfilestore.WithMaxBytes(int64(cfg.ExecEvidenceMaxBytes)))
	if err != nil {
		return fmt.Errorf("configure evidence store (EZTEST_EVIDENCE_DIR=%q): %w", cfg.EvidenceDir, err)
	}
	evidenceService := executionapp.NewEvidenceService(
		executionEvidence, executionEvidenceRefs, executionBatches, evidenceStore, snowflake,
		executionapp.WithEvidenceMaxBytes(int64(cfg.ExecEvidenceMaxBytes)),
		executionapp.WithEvidenceMaxPerCaseRun(cfg.ExecEvidencePerCase),
	)
	executorClient := executionbrowser.New(cfg.ExecutorURL, cfg.ExecutorToken, 0)
	// Auto-login acquisition drives the stored form recipe through the same
	// executor client used for case execution and captures the storage_state the
	// resource service leases to execution (spec §5.9). Without this wiring the
	// acquisition path fails closed with no_valid_session.
	sessionService.ConfigureAcquisition(resourceapp.AcquisitionDeps{
		Flows:    resourceAuthFlows,
		Login:    executionbrowser.NewLoginBrowser(executorClient),
		Accounts: resourceAccessRepo,
	})
	if cfg.ExecutorToken == "" {
		logger.Warn("EZTEST_EXECUTOR_TOKEN is empty; executor calls will be rejected until the API and executor share a token")
	}
	// A dead executor is not a startup failure: the executor is an external
	// service and the batch-create gate answers 503 executor_unavailable until
	// it is reachable. The boot probe only warns (spec §5.6, §8.4).
	executorHealthCtx, cancelExecutorHealth := context.WithTimeout(startupCtx, executorHealthTimeout)
	executorHealthErr := executorClient.Health(executorHealthCtx)
	cancelExecutorHealth()
	if executorHealthErr != nil {
		logger.Warn("executor health probe failed; batch creation will answer 503 executor_unavailable until it is reachable",
			"url", cfg.ExecutorURL, "error", executorHealthErr)
	} else {
		logger.Info("executor health probe succeeded", "url", cfg.ExecutorURL)
	}
	executionRecorder := executionapp.NewRecorder(executionapp.RecorderDeps{
		Locator:    executionQueries,
		CaseRuns:   executionCaseRuns,
		Steps:      executionSteps,
		Assertions: executionAssertions,
		Evidence:   executionEvidence,
		Refs:       executionEvidenceRefs,
		Batches:    executionBatches,
		Versions:   executionVersionPort,
		Sessions:   executionSessionPort,
		Events:     executionEvents,
		Clock:      wallClock{},
		IDs:        snowflake,
	})
	var executionAgentRuntime *executionagent.AgentRuntime
	executionToolSet := executionagenttool.NewToolSet(
		executionRecorder, executorClient, evidenceService, nil,
		executionagenttool.Limits{
			MaxArtifactBytes: int64(cfg.ExecEvidenceMaxBytes),
			Session: func(ctx context.Context, caseRunID int64) (executionapp.SessionRef, error) {
				if executionAgentRuntime == nil {
					return executionapp.SessionRef{}, errors.New("execution: agent runtime is not bound yet")
				}
				return executionAgentRuntime.SessionForCaseRun(ctx, caseRunID)
			},
		},
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
	// The nine P4 codeintel tools are appended the same way (design spec §8.1).
	// The P3 profile allowlists stay unchanged: registration only makes the
	// tools available; P5 configures which profiles may call them. The
	// repository catalog is adapted because the tools package depends only on
	// its own local projection, never on the codeintel application layer.
	agentToolList = append(agentToolList, codeIntelTools(agenttools.CodeIntelToolDeps{
		Repos:    codeIntelRepoCatalog{repos: codeIntelRepos},
		Indexes:  codeIntelIndex,
		Queries:  codeIntelQuery,
		Analyses: codeIntelAnalysis,
		Anchors:  codeIntelAnchor,
	})...)
	// The six P5 mindmap tools are appended the same way, before NewToolRegistry,
	// so ensureP5Profiles below validates their allowlists against the live
	// registry the P5 runs use (design spec §8.1/§8.2). NewTools fails closed
	// (returns nil) when a required port is missing, which the startup profile
	// validation then reports as an unregistered tool.
	agentToolList = append(agentToolList, mindmapagenttools.NewTools(mindmapagenttools.ToolDeps{
		Graphs:   mindmapGraph,
		Runs:     mindmapRuns,
		Catalog:  mindmapCatalog,
		Prober:   mindmapProber,
		Anchors:  mindmapAnchors,
		Findings: mindmapFindings,
		Audit:    mindmapAudit,
	})...)
	// The 15 P6 execution tools are appended before NewToolRegistry so the
	// execution-agent profile allowlist is validated against the live registry.
	agentToolList = append(agentToolList, executionToolSet.Tools()...)
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

	// P6 execution engine. The agent runtime adapter needs the bound P1
	// session/run services; its profile seed is validated against the live
	// registry before it is written, exactly like the P3/P5 seeds. The scheduler
	// is the sole dispatcher and is started once below; boot recovery settles
	// anything a previous process left running before the loop begins.
	executionAgentRuntime = executionagent.NewAgentRuntime(
		agentProfileService, agentProfiles, agentSessionService, runService)
	// The recorder is the live case read model, so the resumed case run's prompt
	// starts with the completed steps' actuals/evidence counts and the remaining
	// steps (spec §5.5, T16); both dependencies exist only here.
	executionAgentRuntime.SetCaseContextSource(executionRecorder)
	if err := ensureExecutionProfile(startupCtx, executionAgentRuntime, agentRegistry, logger); err != nil {
		return err
	}
	executionRunner := executionapp.NewCaseRunner(executionapp.CaseRunnerDeps{
		CaseRuns: executionCaseRuns,
		Steps:    executionSteps,
		Batches:  executionBatches,
		Sessions: executionSessionPort,
		Runtime:  executionAgentRuntime,
		Mindmap:  executionMindmapPort,
		Events:   executionEvents,
		Clock:    wallClock{},
		// The tools own the per-case executor context; the runner only cancels
		// and closes that context through this controller.
		Browser: executionToolSet,
		Timeout: time.Duration(cfg.ExecCaseTimeoutMS) * time.Millisecond,
	})
	executionScheduler := executionapp.NewScheduler(executionapp.SchedulerDeps{
		Queue:         executionQueries,
		Batches:       executionBatches,
		CaseRuns:      executionCaseRuns,
		Versions:      executionVersionPort,
		Sessions:      executionSessionPort,
		Mindmap:       executionMindmapPort,
		Events:        executionEvents,
		Clock:         wallClock{},
		IDs:           snowflake,
		Runner:        executionRunner,
		Browser:       executionToolSet,
		MaxConcurrent: cfg.ExecMaxConcurrentCases,
		Tick:          time.Duration(cfg.ExecWorkerIntervalMS) * time.Millisecond,
	})
	executionRecovery := executionapp.NewRecovery(executionapp.RecoveryDeps{
		Queue:    executionQueries,
		Batches:  executionBatches,
		CaseRuns: executionCaseRuns,
		Steps:    executionSteps,
		Versions: executionVersionPort,
		Sessions: executionSessionPort,
		Events:   executionEvents,
		Clock:    wallClock{},
		// The queue only sees active batches; this scan reaches a terminal batch
		// whose finished runs lost their lease release, so the periodic sweep
		// releases those stale sessions too (spec §5.4).
		LeaseScan: executionQueries,
	})
	if err := executionRecovery.Recover(startupCtx); err != nil {
		return fmt.Errorf("recover execution batches: %w", err)
	}
	// Recover settles the runs of a previous process, but it is scoped to the
	// active batches: a pending/parked case run (or a terminal batch) can keep a
	// session lease whose release write was lost. Sweep releases those residual
	// leases before the scheduler starts, so no lease survives a restart
	// (spec §5.4). It is idempotent, so a boot that finds nothing is a no-op.
	if err := executionRecovery.Sweep(startupCtx); err != nil {
		return fmt.Errorf("sweep execution session leases: %w", err)
	}
	executionBatchService := executionapp.NewBatchService(executionapp.BatchServiceDeps{
		Versions: executionVersionPort,
		Mindmap:  executionMindmapPort,
		Batches:  executionBatches,
		CaseRuns: executionCaseRuns,
		Events:   executionEvents,
		Clock:    wallClock{},
		IDs:      snowflake,
		MaxCases: cfg.ExecMaxCasesPerBatch,
	})
	executionReportService := executionapp.NewReportService(
		executionBatches, executionCaseRuns, executionSteps, executionAssertions,
		executionEvidence,
		executionresource.NewReportEnvironmentSource(executionVersionPort, catalogService),
		time.Now,
	)
	// One in-process scheduler loop (spec §5.3). The deferred stop runs before
	// database.Close (registered earlier) and waits briefly for the in-flight
	// case runners, which leave the run for boot recovery on shutdown.
	executionWorkerCtx, cancelExecutionWorker := context.WithCancel(context.Background())
	go executionScheduler.Start(executionWorkerCtx)
	defer func() {
		cancelExecutionWorker()
		done := make(chan struct{})
		go func() {
			executionScheduler.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(shutdownTimeout):
			logger.Warn("execution scheduler did not drain before shutdown")
		}
	}()

	// The remaining P5 services. The run coordinator polls the P1 agent runtime
	// through the mindmap adapter, so it is built once both P1 services exist;
	// the review, baseline and export services then consume the run coordinator
	// and the graph repositories.
	mindmapAgentRuntime := mindmapagent.NewAgentRuntimeAdapter(
		agentProfileService, agentProfiles, agentSessionService, runService)
	mindmapRun := mindmapapp.NewRunService(mindmapapp.RunDeps{
		Runs:         mindmapRuns,
		Graph:        mindmapGraphTx,
		Baselines:    mindmapBaselines,
		Nodes:        mindmapNodes,
		Steps:        mindmapSteps,
		Assertions:   mindmapAssertions,
		Sources:      mindmapSources,
		Versions:     mindmapLifecycle,
		Requirements: mindmapRequirements,
		Catalog:      mindmapCatalog,
		CodeFacts:    mindmapCodeFacts,
		Anchors:      mindmapAnchors,
		Agents:       mindmapAgentRuntime,
		Events:       mindmapEvents,
		IDs:          snowflake,
		Clock:        wallClock{},
	})
	mindmapBaseline := mindmapapp.NewBaselineService(mindmapapp.BaselineDeps{
		Graph:      mindmapGraphTx,
		Nodes:      mindmapNodes,
		Steps:      mindmapSteps,
		Assertions: mindmapAssertions,
		Sources:    mindmapSources,
		Reviews:    mindmapReviews,
		Baselines:  mindmapBaselines,
		Versions:   mindmapLifecycle,
		IDs:        snowflake,
		Clock:      wallClock{},
	})
	mindmapReview := mindmapapp.NewReviewService(mindmapapp.ReviewDeps{
		Runs:      mindmapRun,
		Reviews:   mindmapReviews,
		Findings:  mindmapFindings,
		Baselines: mindmapBaselines,
		Versions:  mindmapLifecycle,
		Events:    mindmapEvents,
		Baseline:  mindmapBaseline,
		IDs:       snowflake,
		Clock:     wallClock{},
	})
	mindmapExport := mindmapapp.NewExportService(mindmapapp.ExportDeps{
		Versions:     mindmapLifecycle,
		Requirements: mindmapRequirements,
		Nodes:        mindmapNodes,
		Steps:        mindmapSteps,
		Assertions:   mindmapAssertions,
		Sources:      mindmapSources,
		Findings:     mindmapFindings,
		Baselines:    mindmapBaselines,
		Exports:      mindmapExports,
		Anchors:      mindmapAnchors,
		Store:        mindmapExportStore,
		Events:       mindmapEvents,
		Audit:        mindmapAudit,
		IDs:          snowflake,
		Clock:        wallClock{},
		Logger:       logger,
	})
	mindmapExplore := mindmapapp.NewExploreService(mindmapapp.ExploreDeps{
		Versions: mindmapLifecycle,
		Catalog:  mindmapCatalog,
		Sessions: mindmapSessions,
		Prober:   mindmapProber,
		Runs:     mindmapRuns,
		Events:   mindmapEvents,
		IDs:      snowflake,
		Clock:    wallClock{},
	})

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
	if err := ensureP5Profiles(startupCtx, mindmapAgentRuntime, agentRegistry, logger); err != nil {
		return err
	}
	if recovered, err := runService.RecoverInterrupted(startupCtx); err != nil {
		return fmt.Errorf("recover interrupted agent runs: %w", err)
	} else if recovered > 0 {
		logger.Info("marked interrupted agent runs", "count", recovered)
	}

	// The P5 run coordinator is a single in-process worker: it polls the agent
	// sessions every 2 s (spec §8.4) and first fails the runs a previous process
	// left queued/running (reason worker_restart) so the per-kind single-flight
	// slot is never stuck. The deferred stop is registered after database.Close
	// and therefore runs before it.
	if recovered, err := mindmapRun.RecoverStale(startupCtx); err != nil {
		return fmt.Errorf("recover stale mindmap runs: %w", err)
	} else if recovered > 0 {
		logger.Info("marked stale mindmap runs failed", "count", recovered)
	}
	mindmapWorkerCtx, cancelMindmapWorker := context.WithCancel(context.Background())
	mindmapRun.Worker(mindmapWorkerCtx)
	defer func() {
		cancelMindmapWorker()
		mindmapRun.Stop()
	}()

	// The P5 handler group consumes the five services plus the read-only readers
	// the handlers need next to them: node revisions (the create-step/assertion
	// bodies carry no token), run pages, findings, case anchors, the frozen
	// baseline, the export metadata lookup and the version lifecycle.
	mindmapHandler := mindmaphttp.NewHandler(mindmaphttp.Deps{
		Graph:     mindmapGraph,
		Runs:      mindmapRun,
		Explore:   mindmapExplore,
		Reviews:   mindmapReview,
		Exports:   mindmapExport,
		Nodes:     mindmapNodes,
		RunStore:  mindmapRuns,
		Findings:  mindmapFindings,
		Anchors:   mindmapAnchors,
		CodeFacts: mindmapCodeFacts,
		Baselines: mindmapBaselines,
		ExportIdx: mindmapExports,
		Versions:  mindmapLifecycle,
		Events:    mindmapEvents,
		Logger:    logger,
	})

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
	// The P6 handler group consumes the batch/case-run services, the evidence
	// and report services, the tool-controller-free session projection and the
	// executor health probe. All ids cross this boundary as decimal strings.
	executionHandlers := executionhttp.NewHandlers(executionhttp.HandlerDeps{
		Batches:         executionBatchService,
		CaseRuns:        executionCaseRuns,
		Steps:           executionSteps,
		Assertions:      executionAssertions,
		Evidence:        executionEvidence,
		EvidenceService: evidenceService,
		EvidenceRefs:    executionQueries,
		Reports:         executionReportService,
		Sessions:        executionSessionPort,
		AgentSessions:   executionAgentRuntime,
		Executor:        executorClient,
		Audit:           executionAudit,
		SessionInfo:     executionSessionInfo{sessions: sessionService, accounts: accessService},
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
			CodeIntel: codeIntelHandler,
			Mindmap:   mindmapHandler,
			Execution: executionHandlers,
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

// ensureP5Profiles seeds the four P5 mindmap agent profiles (spec §8.1) and
// validates every allowed tool against the live registry before the first seed,
// exactly like ensureP3Profiles: a profile that allows an unregistered tool would
// silently offer fewer tools than it declares, and the case_* tool names must
// match the six tools registered above. EnsureProfile delegates to the P1 seed
// writer, which only inserts a missing key: an admin-edited row is never
// overwritten, so restarts keep the edited prompts and budgets.
func ensureP5Profiles(ctx context.Context, runtime mindmapapp.AgentRuntime, registry *agentapp.ToolRegistry, logger *slog.Logger) error {
	seeds := mindmapdomain.P5Profiles()
	for _, profile := range seeds {
		for _, name := range profile.AllowedTools {
			if tool, ok := registry.Get(name); !ok || tool == nil {
				return fmt.Errorf("P5 agent profile %q allows tool %q, which is not registered", profile.Key, name)
			}
		}
	}
	for _, profile := range seeds {
		if err := runtime.EnsureProfile(ctx, profile); err != nil {
			return fmt.Errorf("seed P5 agent profile %q: %w", profile.Key, err)
		}
	}
	logger.Debug("ensured P5 agent profiles", "count", len(seeds))
	return nil
}

// ensureExecutionProfile seeds the P6 execution-agent profile (spec §7.1) and
// validates every allowed tool against the live registry before the seed is
// written. A profile that allows an unregistered tool would silently offer
// fewer tools than it declares. The seed writer only inserts a missing key, so
// an administrator-edited row is never overwritten on restart.
func ensureExecutionProfile(ctx context.Context, runtime executionapp.AgentRuntimePort, registry *agentapp.ToolRegistry, logger *slog.Logger) error {
	seed := executiondomain.ExecutionProfile()
	if registry == nil {
		return fmt.Errorf("P6 execution profile %q cannot be validated: the tool registry is not configured", seed.Key)
	}
	if err := executionagenttool.ValidateTools(registry); err != nil {
		return fmt.Errorf("P6 execution profile %q: %w", seed.Key, err)
	}
	if err := runtime.EnsureProfile(ctx); err != nil {
		return fmt.Errorf("seed P6 execution profile %q: %w", seed.Key, err)
	}
	logger.Debug("ensured P6 execution profile", "key", seed.Key)
	return nil
}

// executionSessionInfo projects the resource session of a case run without ever
// exposing its storage state or credentials. The account label is best-effort:
// a deleted account leaves it empty instead of failing the case-run detail.
type executionSessionInfo struct {
	sessions *resourceapp.SessionService
	accounts *resourceapp.AccessService
}

// CaseRunSession implements executionhttp.SessionInfoReader.
func (s executionSessionInfo) CaseRunSession(ctx context.Context, sessionID int64) (executionhttp.CaseRunSession, error) {
	if s.sessions == nil {
		return executionhttp.CaseRunSession{}, errors.New("execution: session service is not configured")
	}
	session, _, _, _, err := s.sessions.Get(ctx, sessionID)
	if err != nil {
		return executionhttp.CaseRunSession{}, err
	}
	account := ""
	if s.accounts != nil {
		if resolved, accountErr := s.accounts.GetAccount(ctx, session.AccountID); accountErr == nil {
			account = resolved.Label
		}
	}
	return executionhttp.CaseRunSession{ID: session.ID, Account: account, State: session.State}, nil
}

// codeIntelRunner adapts the two codeintel async run services to the worker's
// Runner port. The worker only knows run ids; the services own the state
// machine and persist every terminal transition.
type codeIntelRunner struct {
	index    *codeintelapp.IndexService
	analyses *codeintelapp.AnalysisService
}

// RunIndex executes one queued T0 repository index run.
func (r codeIntelRunner) RunIndex(ctx context.Context, runID int64) error {
	return r.index.Run(ctx, runID)
}

// RunAnalysis executes one queued version impact analysis run.
func (r codeIntelRunner) RunAnalysis(ctx context.Context, analysisID int64) error {
	return r.analyses.Run(ctx, analysisID)
}

var _ codeintelapp.Runner = codeIntelRunner{}

// codeIntelRepoCatalog adapts the codeintel application RepoCatalog to the
// agent tools' local ListRepos projection. The tools package deliberately
// depends only on its own CodeIntelRepo type, so the composition root performs
// the projection (design spec §8.1).
type codeIntelRepoCatalog struct {
	repos codeintelapp.RepoCatalog
}

var _ agenttools.CodeIntelRepoCatalog = codeIntelRepoCatalog{}

// ListRepos implements agent tools.CodeIntelRepoCatalog over the P2 repository
// catalog.
func (c codeIntelRepoCatalog) ListRepos(ctx context.Context) ([]agenttools.CodeIntelRepo, error) {
	infos, err := c.repos.List(ctx)
	if err != nil {
		return nil, err
	}
	repos := make([]agenttools.CodeIntelRepo, 0, len(infos))
	for _, info := range infos {
		repos = append(repos, agenttools.CodeIntelRepo{
			ID:            info.ID,
			Key:           info.Key,
			Name:          info.Name,
			Status:        info.Status,
			FixedBranch:   info.FixedBranch,
			LastIndexedAt: info.LastIndexedAt,
		})
	}
	return repos, nil
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

// codeIntelTools builds the nine P4 codeintel agent tools (design spec §8.1).
// The composition root appends them to the single tool registry before
// NewToolRegistry is called; keeping that append behind this helper makes the
// registration path directly testable from package main.
func codeIntelTools(deps agenttools.CodeIntelToolDeps) []agentdomain.Tool {
	return agenttools.NewCodeIntelTools(deps)
}

// codeintelShutdownTimeout returns the graceful codeintel worker-stop budget:
// the configured analysis timeout plus codeIntelShutdownBuffer (design spec
// §6.8). The worker applies analysis_timeout_ms per job, so the budget covers a
// run that was already executing when the shutdown started. A zero or negative
// timeout falls back to the documented default (NewWorker applies the same
// default) and an above-max value is capped, keeping the budget positive and
// bounded even for a Config that bypassed config.Load. A positive budget also
// guarantees the deferred worker stop completes before database.Close, which
// was deferred earlier and therefore runs last.
func codeintelShutdownTimeout(cfg config.Config) time.Duration {
	timeoutMS := cfg.CodeIntelAnalysisTimeoutMS
	switch {
	case timeoutMS <= 0:
		timeoutMS = codeIntelDefaultAnalysisTimeoutMS
	case timeoutMS > codeIntelMaxAnalysisTimeoutMS:
		timeoutMS = codeIntelMaxAnalysisTimeoutMS
	}
	return time.Duration(timeoutMS)*time.Millisecond + codeIntelShutdownBuffer
}

// codeIntelLLMCompleter selects the codeintel completer. A gateway construction
// failure is the intentional degraded mode of design spec §6.2, not a startup
// failure: it is logged once and reported as a nil completer, so the process
// keeps serving and analyses fall back to deterministic T0 results. The error
// carries only the stable "not configured" reason: never a prompt, response or
// secret.
func codeIntelLLMCompleter(gateway codeintelapp.LLMCompleter, err error, logger *slog.Logger) codeintelapp.LLMCompleter {
	if err != nil {
		logger.Warn("code intelligence LLM is unavailable; analyses will degrade to T0 results",
			"reason", err)
		return nil
	}
	return gateway
}
