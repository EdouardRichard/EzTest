// Package server assembles the HTTP routes and the middleware chain. It
// contains no business logic.
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
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
	// has wired authentication, so the /api/auth routes are mounted.
	Auth identityhttp.AuthService
}

// New assembles the HTTP handler. Middleware runs in the order
// recover -> request ID -> logging -> routes.
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

	var handler http.Handler = mux
	handler = loggingMiddleware(logger)(handler)
	handler = requestIDMiddleware(handler)
	handler = recoverMiddleware(logger)(handler)
	return handler
}
