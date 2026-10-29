// Package server assembles the HTTP routes and the middleware chain. It
// contains no business logic.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	agenthttp "github.com/EdouardRichard/EzTest/backend/internal/agent/interfaces/httpapi"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	audithttp "github.com/EdouardRichard/EzTest/backend/internal/audit/interfaces/httpapi"
	chainhttp "github.com/EdouardRichard/EzTest/backend/internal/chain/interfaces/httpapi"
	codeintelhttp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	deliveryhttp "github.com/EdouardRichard/EzTest/backend/internal/delivery/interfaces/httpapi"
	eventinghttp "github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llmhttp "github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
	qualityhttp "github.com/EdouardRichard/EzTest/backend/internal/quality/interfaces/httpapi"
	resourcehttp "github.com/EdouardRichard/EzTest/backend/internal/resource/interfaces/httpapi"
	searchhttp "github.com/EdouardRichard/EzTest/backend/internal/search/interfaces/httpapi"
	storagehttp "github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
	versionhttp "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
	workspacehttp "github.com/EdouardRichard/EzTest/backend/internal/workspace/interfaces/httpapi"
)

// Pinger reports whether a backing dependency (for example the database) is
// reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// EventService is the eventing use case the server mounts: the snapshot query
// and the admin test publisher. The eventing application Service satisfies it.
type EventService interface {
	eventinghttp.Snapshotter
	eventinghttp.Publisher
}

// GCService is the P8 evidence GC use case mounted at POST /api/admin/gc
// (spec §10.2). The execution application GCService satisfies it.
type GCService interface {
	Run(ctx context.Context, req executionapp.GCRequest) (executionapp.GCReport, error)
}

// AgentDependencies holds the Agent use cases the server mounts. They are
// grouped so a process without an Agent runtime (and most unit tests) can leave
// the whole /api/agents surface unmounted by leaving Dependencies.Agent nil.
type AgentDependencies struct {
	Profiles agenthttp.ProfileService
	Sessions agenthttp.SessionService
	Runs     agenthttp.RunControl
	Tools    agenthttp.ToolCatalog
	// Center is the read-only P8 Agent-centre use case (13-role catalogue, run
	// observation, latest quality metrics). Unlike the P1 /api/agents surface it
	// is readable by every logged-in user, so the server mounts
	// /api/agent-center/* without the per-route admin guard (spec §8.1/D60).
	Center agenthttp.CenterReader
}

// Dependencies holds the collaborators wired into the HTTP layer. Later tasks
// extend this struct.
type Dependencies struct {
	Pinger Pinger
	// Auth is the identity auth use-case port. When set, the composition root
	// has wired authentication, so the /api/auth routes are mounted and the
	// protected /api routes require a bearer token.
	Auth identityhttp.AuthService
	// Users is the identity user-management use case. When set, the admin user
	// routes are mounted. They rely on the global bearer-token middleware and
	// apply the per-route admin guard.
	Users identityhttp.UserService
	// Audit is the audit recorder. When set, the admin audit query endpoint is
	// mounted and the auth handlers record the login rejections that happen
	// before the auth service (invalid username, limiter lockout).
	Audit auditapp.Recorder
	// Idempotency stores replayable write responses. A nil store disables
	// idempotent replay (for example in unit tests that do not exercise it).
	Idempotency idempotency.Store
	// Events is the eventing use case. When set, GET /api/stream/events is
	// mounted and, when test endpoints are enabled, the admin test publisher.
	Events EventService
	// EventReader replays persisted events for GET /api/stream. The stream route
	// is mounted only when EventReader and EventHub are both set.
	EventReader eventinghttp.EventReader
	// EventHub delivers live events to GET /api/stream.
	EventHub eventinghttp.EventSubscriber
	// LLMConfigs is the LLM configuration use case. When both LLMConfigs and
	// LLMClientFactory are set, the admin LLM routes are mounted behind the
	// global bearer auth and the per-route admin guard.
	LLMConfigs llmhttp.ConfigService
	// LLMClientFactory builds a protocol client from a stored configuration.
	// It applies the configuration's retry, interval and timeout policy.
	LLMClientFactory llmapp.ClientFactory
	// Storage is the object-storage use case. When set, the admin storage
	// routes are mounted behind the global bearer auth and the per-route admin
	// guard. Reads expose the content type persisted by the metadata
	// repository wired into the use case by the composition root.
	Storage storagehttp.ObjectService
	// Agent holds the Agent use cases. When set, the admin /api/agents routes
	// are mounted behind the global bearer auth and the per-route admin guard.
	Agent *AgentDependencies
	// Resources holds the resource context's HTTP handlers (P2). When set, the
	// /api/resources routes are mounted; reads require only the global bearer
	// auth, while writes and actions apply the per-route admin guard.
	Resources *resourcehttp.Handlers
	// Version holds the version context's HTTP handlers (P3). When set, the
	// /api/versions routes are mounted. They rely on the global bearer
	// middleware and add no per-route guard: the P3 contract makes every route
	// available to any authenticated user.
	Version *versionhttp.Handlers
	// CodeIntel holds the code-intelligence context's HTTP handlers (P4). When
	// set, the /api/code and /api/versions/{id}/code routes are mounted. They
	// rely on the global bearer auth; only the T0 index trigger adds the
	// per-route admin guard (design spec §6.10).
	CodeIntel *codeintelhttp.Handler
	// Mindmap holds the case-mindmap HTTP handler group (P5). When set, the
	// /api/versions/{versionId}/mindmap routes are mounted. They rely on the
	// global bearer auth; the exploration run kind and the anchor-delete
	// ownership rule enforce their own authorization in the handler (design
	// spec §7.1/§7.2), so no per-route guard is added here.
	Mindmap *mindmaphttp.Handler
	// Execution holds the execution context's HTTP handlers (P6). When set, the
	// /api/versions/{id}/batches|evidence|execution routes are mounted. They
	// rely on the global bearer auth: every logged-in user may create and
	// control batches and read execution data (design spec §5.11), so no
	// per-route guard is added here.
	Execution *executionhttp.Handlers
	// Delivery holds the delivery context's HTTP handlers (P7): issues, reports,
	// analysis runs and the global issue/report centres. When set, the P7
	// delivery routes are mounted under /api/versions/{id} and /api. They rely
	// on the global bearer auth and add no per-route guard (spec §11.1).
	Delivery *deliveryhttp.Handlers
	// Chain holds the P8 exploration-chain read-model handlers. When set,
	// GET /api/versions/{versionId}/exploration-chain is mounted. The route
	// relies on the global bearer auth and adds no per-route guard: every
	// logged-in user may read a version's chain (spec §5.1).
	Chain *chainhttp.Handlers
	// Workspace holds the P8 todo-centre handler group. When set,
	// GET /api/todos is mounted. The route relies on the global bearer auth and
	// resolves the administrator flag from the authenticated identity itself:
	// every logged-in user gets the review group, administrators additionally
	// get takeover/gate items (spec §6.3).
	Workspace *workspacehttp.Handlers
	// Search holds the P8 global-search handler group. When set, GET /api/search
	// is mounted. It relies on the global bearer auth and adds no per-route
	// guard: every logged-in user may search (spec §6.2).
	Search *searchhttp.Handlers
	// Quality holds the P8 evaluation-results handler group. When set, the
	// /api/quality/eval-runs routes are mounted behind the global bearer auth
	// and the per-route admin guard: the eval runner and the quality dashboard
	// are administrator-only (spec §9.1/§11.5).
	Quality *qualityhttp.Handlers
	// GC is the P8 evidence GC use case. When set, POST /api/admin/gc is
	// mounted behind the global bearer auth and the per-route admin guard
	// (spec §10.2). Non-dry-run execution is additionally gated by
	// EZTEST_GC_ENABLED, which the handler checks (409 when disabled).
	GC GCService
}

// New assembles the HTTP handler. Middleware runs in the order
// recover -> request ID -> logging -> CORS -> auth -> idempotency -> routes.
func New(cfg config.Config, logger *slog.Logger, deps Dependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/health", handleAPIHealth(cfg, deps.Pinger, deps.LLMConfigs))
	// The OpenAPI document is public (see requiresAuthentication) so tooling can
	// discover the API without a bearer token.
	mux.HandleFunc("GET /api/openapi.yaml", handleOpenAPIDocument(cfg.OpenAPIPath))
	if deps.Auth != nil {
		identityhttp.RegisterAuthRoutes(mux, deps.Auth, identityhttp.NewLoginLimiter(time.Now), deps.Audit)
	}
	if deps.Users != nil {
		identityhttp.RegisterUserRoutes(mux, deps.Users)
	}
	if deps.Audit != nil {
		// The route is mounted here because the admin guard lives in the
		// identity interface package; the audit handler itself only lists.
		mux.Handle("GET /api/admin/audit", identityhttp.RequireAdmin(audithttp.NewHandler(deps.Audit)))
	}
	if deps.GC != nil {
		// Evidence GC (P8) is admin-only; the guard also stamps the request
		// metadata the audit entry carries. A non-dry-run purge is rejected
		// with 409 unless EZTEST_GC_ENABLED=true (spec §10.2).
		mux.Handle("POST /api/admin/gc",
			identityhttp.RequireAdmin(auditRequestContext(handleAdminGC(deps.GC, cfg.GCEnabled))))
	}
	if deps.Events != nil {
		mux.Handle("GET /api/stream/events", agentChannelAdminOnly(eventinghttp.NewSnapshotHandler(deps.Events)))
	}
	if deps.EventReader != nil && deps.EventHub != nil {
		mux.Handle("GET /api/stream",
			agentChannelAdminOnly(eventinghttp.NewSSEHandler(deps.EventReader, deps.EventHub, eventinghttp.DefaultHeartbeat)))
	}
	if deps.LLMConfigs != nil && deps.LLMClientFactory != nil {
		// Authentication is the global middleware; each route only adds the
		// admin guard and attaches the audit actor/request metadata.
		llmHandler := llmhttp.NewHandler(deps.LLMConfigs, deps.LLMClientFactory, deps.Audit)
		adminGuard := func(next http.Handler) http.Handler {
			return identityhttp.RequireAdmin(auditRequestContext(next))
		}
		llmhttp.RegisterRoutes(mux, llmHandler, adminGuard)
	}
	if deps.Storage != nil {
		// As with the LLM routes, authentication is the global middleware and
		// each route only adds the admin guard plus the audit metadata.
		storageHandler := storagehttp.NewHandler(deps.Storage)
		adminGuard := func(next http.Handler) http.Handler {
			return identityhttp.RequireAdmin(auditRequestContext(next))
		}
		storagehttp.RegisterRoutes(mux, storageHandler, adminGuard)
	}
	if deps.Agent != nil {
		// The Agent API is admin-only: the global middleware authenticates, the
		// per-route guard enforces the role and attaches the audit actor the
		// Agent handlers record on every write.
		agentHandler := agenthttp.NewHandler(deps.Agent.Profiles, deps.Agent.Sessions, deps.Agent.Runs, deps.Agent.Tools)
		adminGuard := func(next http.Handler) http.Handler {
			return identityhttp.RequireAdmin(auditRequestContext(next))
		}
		agenthttp.RegisterRoutes(mux, agentHandler, adminGuard)
		// The P8 Agent centre is a separate read-only surface: every logged-in
		// user may read the role catalogue, run observation and quality metrics,
		// while the profile writes above stay admin-only (spec §8.1/D60).
		agenthttp.RegisterCenterRoutes(mux, agenthttp.NewCenterHandler(deps.Agent.Center))
	}
	if deps.Resources != nil {
		// The resource reads need only the global bearer auth; every write and
		// action route carries the per-route admin guard. P7 adds the
		// administrator-only environment db target routes (spec §7.8) to the
		// same group; the guard also stamps the P0 request metadata so the
		// resource audit entries (resource.db_target.*) carry request
		// correlation. The handlers derive the audit actor from the identity
		// context.
		resourceGuard := func(next http.Handler) http.Handler {
			return identityhttp.RequireAdmin(auditRequestContext(next))
		}
		resourcehttp.RegisterRoutes(mux, *deps.Resources, resourceGuard)
	}
	if deps.Version != nil {
		// The version routes (P3) rely on the global bearer auth and add no
		// per-route guard. RegisterRoutes skips a nil handler group, so a
		// partially wired process mounts only the groups it has.
		versionhttp.RegisterRoutes(mux, *deps.Version)
	}
	if deps.CodeIntel != nil {
		// The codeintel routes (P4) rely on the global bearer auth too; the
		// handler group wraps only the T0 index trigger in the identity
		// context's RequireAdmin guard (design spec §6.10). Register mounts each
		// group only when its application service is wired, so a partially
		// assembled process never exposes a route backed by nil dependencies.
		deps.CodeIntel.Register(mux)
	}
	if deps.Mindmap != nil {
		// The mindmap routes (P5) sit under /api/versions/{versionId}/mindmap
		// and rely on the global bearer auth. Register mounts each group only
		// when its services are wired, so a partially assembled process never
		// exposes a route backed by nil dependencies.
		deps.Mindmap.Register(mux)
	}
	if deps.Execution != nil {
		// The execution routes (P6) sit under /api/versions/{id}/batches,
		// .../evidence and .../execution. They rely on the global bearer auth
		// and add no per-route guard (spec §5.11). RegisterRoutes skips nil
		// groups so a partially wired process mounts only what it has.
		deps.Execution.RegisterRoutes(mux)
	}
	if deps.Delivery != nil {
		// The delivery routes (P7) sit under /api/versions/{id}/issues,
		// .../reports, .../batches/{batchId}/analysis and the global
		// /api/issues|reports centres. They rely on the global bearer auth and
		// add no per-route guard (spec §11.1); RegisterRoutes skips nil groups
		// so a partially wired process mounts only what it has.
		deps.Delivery.RegisterRoutes(mux)
	}
	if deps.Chain != nil {
		// The exploration-chain read model (P8) is mounted at
		// GET /api/versions/{versionId}/exploration-chain. It relies on the
		// global bearer auth and adds no per-route guard (spec §5.1: every
		// logged-in user may read it).
		deps.Chain.RegisterRoutes(mux)
	}
	if deps.Workspace != nil {
		// The todo centre (P8) is mounted at GET /api/todos. It relies on the
		// global bearer auth; the handler resolves the admin flag from the
		// identity context and the service filters takeover/gate server-side
		// (spec §6.3).
		deps.Workspace.RegisterRoutes(mux)
	}
	if deps.Search != nil {
		// The global search (P8) is mounted at GET /api/search. It relies on the
		// global bearer auth and adds no per-route guard: every logged-in user
		// may search (spec §6.2).
		deps.Search.RegisterRoutes(mux)
	}
	if deps.Quality != nil {
		// The evaluation results (P8) are mounted at /api/quality/eval-runs.
		// Authentication is the global middleware; every route adds the admin
		// guard because evaluation is administrator-only (spec §9.1/§11.5).
		qualityGuard := func(next http.Handler) http.Handler {
			return identityhttp.RequireAdmin(next)
		}
		deps.Quality.RegisterRoutes(mux, qualityGuard)
	}
	if deps.Events != nil && cfg.EnableTestEndpoints {
		auditor := deps.Audit
		if auditor == nil {
			auditor = auditapp.NopRecorder{}
		}
		publisher := eventinghttp.NewTestPublisher(deps.Events, auditor, cfg.EnableTestEndpoints)
		mux.Handle("POST /api/admin/stream/test-events",
			identityhttp.RequireAdmin(auditRequestContext(publisher)))
	}
	return withMiddleware(mux, cfg, deps, logger)
}

// withMiddleware composes the process-wide middleware chain around the routes
// mux. It is a separate function so tests can exercise the chain with a local
// mux. Auth runs before idempotency: idempotency keys are scoped to the
// authenticated user, so it must see the user that auth stores in the context.
// The rate limiter sits between auth and the business chain so authenticated
// requests are keyed by user ID; public routes pass through auth and use the
// client IP (spec §10.1).
func withMiddleware(next http.Handler, cfg config.Config, deps Dependencies, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	limiter := NewRateLimiter(cfg, time.Now)
	limiter.logger = logger
	var handler http.Handler = next
	handler = idempotency.Middleware(deps.Idempotency, authenticatedUserID)(handler)
	handler = limiter.Middleware()(handler)
	if deps.Auth != nil {
		handler = authenticatedAPI(deps.Auth)(handler)
	}
	handler = CORS(cfg.WebOrigin)(handler)
	handler = loggingMiddleware(logger)(handler)
	handler = requestIDMiddleware(handler)
	handler = recoverMiddleware(logger)(handler)
	return handler
}

// authenticatedAPI applies the bearer-token middleware to the protected /api
// routes. Public routes (health, the OpenAPI document) and the auth context's
// own routes (login is public; me and logout authenticate inside their
// handlers) are passed through untouched.
func authenticatedAPI(authenticator identityhttp.Authenticator) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		authenticate := identityhttp.AuthMiddleware(authenticator)(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !requiresAuthentication(r) {
				next.ServeHTTP(w, r)
				return
			}
			authenticate.ServeHTTP(w, r)
		})
	}
}

// openAPIContentType is the media type of the hand-maintained OpenAPI document.
const openAPIContentType = "application/yaml"

// handleOpenAPIDocument serves the hand-maintained OpenAPI document from path.
// A missing or unreadable file is a 404 in the shared error envelope rather
// than a 500: the deployment simply did not ship the document.
func handleOpenAPIDocument(path string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		document, err := os.ReadFile(path)
		if err != nil {
			platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "openapi document not found")
			return
		}
		w.Header().Set("Content-Type", openAPIContentType)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(document)
	}
}

// requiresAuthentication reports whether the request targets a protected /api
// route. Auth routes are excluded because the identity package mounts its own
// authentication on me/logout while login stays public.
func requiresAuthentication(r *http.Request) bool {
	if r.Method == http.MethodOptions {
		return false
	}
	switch r.URL.Path {
	case "/api/health", "/api/openapi.yaml":
		return false
	}
	return strings.HasPrefix(r.URL.Path, "/api/") && !strings.HasPrefix(r.URL.Path, "/api/auth/")
}

// authenticatedUserID adapts the identity context to idempotency's user lookup.
func authenticatedUserID(ctx context.Context) (int64, bool) {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0, false
	}
	return user.ID, true
}

// auditRequestContext attaches the request metadata and the authenticated actor
// to the context for handlers that record their own audit entries (the eventing
// test publisher). It must run after authentication; the route applies the
// admin guard outside it.
func auditRequestContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := auditapp.WithMeta(r.Context(), auditapp.RequestMeta{
			RequestID: platform.RequestID(r.Context()),
			IP:        remoteIP(r),
			UserAgent: r.UserAgent(),
		})
		if user, ok := identityhttp.UserFromContext(ctx); ok {
			ctx = auditapp.WithActorID(ctx, user.ID)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// agentChannelAdminOnly restricts the Agent activity channels to administrators.
// P1 activity lives on the `agent:{session_id}` channels (design spec §9.5), so
// any channel with that prefix requires the admin role; the P0 `system` channel
// and every other channel keep their previous authenticated-only behaviour. It
// is mounted on the stream routes and therefore runs after the global auth
// middleware.
func agentChannelAdminOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Query().Get("channel"), "agent:") {
			if user, ok := identityhttp.UserFromContext(r.Context()); !ok || user.Role != identity.RoleAdmin {
				platform.WriteError(w, r, http.StatusForbidden, platform.CodeForbidden, "admin role required")
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// gcMaxBodyBytes bounds the admin GC request body. The contract is a tiny JSON
// object ({dryRun, reason}), so 4 KiB is ample.
const gcMaxBodyBytes = 4 << 10

// gcRequest is the POST /api/admin/gc body. DryRun is a pointer so an omitted
// field selects the safe default (dry-run) instead of the zero value.
type gcRequest struct {
	DryRun *bool  `json:"dryRun"`
	Reason string `json:"reason"`
}

// gcEnvelope wraps the GC report in the shared data envelope.
type gcEnvelope struct {
	Data executionapp.GCReport `json:"data"`
}

// handleAdminGC serves POST /api/admin/gc (spec §10.2). The request defaults to
// dry-run; an explicit non-dry-run is rejected with 409 when EZTEST_GC_ENABLED
// is false. The execution service performs the same fail-closed check, so a
// caller that bypasses the handler cannot purge either.
func handleAdminGC(service GCService, enabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if service == nil {
			platform.WriteError(w, r, http.StatusServiceUnavailable, platform.CodeUnavailable,
				"evidence GC is not configured")
			return
		}
		var body gcRequest
		if err := platform.DecodeJSON(r, &body, gcMaxBodyBytes); err != nil {
			platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
				"invalid gc request body")
			return
		}
		dryRun := true
		if body.DryRun != nil {
			dryRun = *body.DryRun
		}
		if !dryRun && !enabled {
			platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
				"evidence GC execution is disabled; set EZTEST_GC_ENABLED=true")
			return
		}
		actorID := int64(0)
		if user, ok := identityhttp.UserFromContext(r.Context()); ok {
			actorID = user.ID
		}
		report, err := service.Run(r.Context(), executionapp.GCRequest{
			DryRun:  dryRun,
			ActorID: actorID,
			Reason:  body.Reason,
		})
		if err != nil {
			if errors.Is(err, executionapp.ErrGCDisabled) {
				platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict,
					"evidence GC execution is disabled; set EZTEST_GC_ENABLED=true")
				return
			}
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
				"evidence GC run failed")
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(gcEnvelope{Data: report})
	}
}

// remoteIP extracts the peer address of the request. Forwarding headers are
// client-controlled and deliberately ignored, matching the login limiter.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
