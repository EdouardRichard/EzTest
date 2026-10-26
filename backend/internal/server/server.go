// Package server assembles the HTTP routes and the middleware chain. It
// contains no business logic.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
)

// Pinger reports whether a backing dependency (for example the database) is
// reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Dependencies holds the collaborators wired into the HTTP layer. Later tasks
// extend this struct.
type Dependencies struct {
	Pinger Pinger
	// Auth is the identity auth use-case port. When set, the composition root
	// has wired authentication, so the /api/auth routes are mounted and the
	// protected /api routes require a bearer token.
	Auth identityhttp.AuthService
	// Idempotency stores replayable write responses. A nil store disables
	// idempotent replay (for example in unit tests that do not exercise it).
	Idempotency idempotency.Store
}

// New assembles the HTTP handler. Middleware runs in the order
// recover -> request ID -> logging -> CORS -> auth -> idempotency -> routes.
func New(cfg config.Config, logger *slog.Logger, deps Dependencies) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", handleHealthz)
	mux.HandleFunc("GET /api/health", handleAPIHealth(deps.Pinger))
	if deps.Auth != nil {
		identityhttp.RegisterAuthRoutes(mux, deps.Auth, identityhttp.NewLoginLimiter(time.Now))
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
