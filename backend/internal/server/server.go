// Package server assembles the HTTP routes and the middleware chain. It
// contains no business logic.
package server

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/config"
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

	var handler http.Handler = mux
	handler = loggingMiddleware(logger)(handler)
	handler = requestIDMiddleware(handler)
	handler = recoverMiddleware(logger)(handler)
	return handler
}
