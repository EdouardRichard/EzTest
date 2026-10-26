// Package server assembles the HTTP routes and the middleware chain. It
// contains no business logic.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	audithttp "github.com/EdouardRichard/EzTest/backend/internal/audit/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventinghttp "github.com/EdouardRichard/EzTest/backend/internal/eventing/interfaces/httpapi"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llmhttp "github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
	storagehttp "github.com/EdouardRichard/EzTest/backend/internal/storage/interfaces/httpapi"
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
	// guard.
	Storage storagehttp.ObjectService
}

// New assembles the HTTP handler. Middleware runs in the order
// recover -> request ID -> logging -> CORS -> auth -> idempotency -> routes.
func New(cfg config.Config, logger *slog.Logger, deps Dependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/health", handleAPIHealth(deps.Pinger, deps.LLMConfigs))
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
	if deps.Events != nil {
		mux.Handle("GET /api/stream/events", eventinghttp.NewSnapshotHandler(deps.Events))
	}
	if deps.EventReader != nil && deps.EventHub != nil {
		mux.Handle("GET /api/stream",
			eventinghttp.NewSSEHandler(deps.EventReader, deps.EventHub, eventinghttp.DefaultHeartbeat))
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
func withMiddleware(next http.Handler, cfg config.Config, deps Dependencies, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	var handler http.Handler = next
	handler = idempotency.Middleware(deps.Idempotency, authenticatedUserID)(handler)
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

// remoteIP extracts the peer address of the request. Forwarding headers are
// client-controlled and deliberately ignored, matching the login limiter.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
