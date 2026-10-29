// Package httpapi exposes the P8 evaluation results over HTTP (design spec
// §9.1/§11.1). Every route is administrator-only: the eval runner authenticates
// with an admin Bearer token and the dashboard reads the same runs. The package
// is transport only; metrics are computed by the application service.
package httpapi

import (
	"context"
	"net/http"

	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	"github.com/EdouardRichard/EzTest/backend/internal/quality/domain"
)

// EvalRunService is the quality use case consumed by the HTTP handlers. The
// quality application Service satisfies it; tests substitute a fake.
type EvalRunService interface {
	Record(ctx context.Context, input qualityapp.RecordInput) (domain.Run, error)
	Get(ctx context.Context, id int64) (domain.Run, error)
	List(ctx context.Context, limit int) ([]domain.Run, error)
}

// AdminGuard wraps a handler with the administrator check. The server's global
// bearer middleware authenticates the request before this guard runs.
type AdminGuard func(next http.Handler) http.Handler

// Handlers is the evaluation-results HTTP handler group. The composition root
// stores it in server.Dependencies.Quality; a nil group mounts nothing.
type Handlers struct {
	runs EvalRunService
}

// NewHandlers builds the handler group. It returns nil when no service is wired,
// mirroring the other contexts' partially-wired-process behaviour.
func NewHandlers(runs EvalRunService) *Handlers {
	if runs == nil {
		return nil
	}
	return &Handlers{runs: runs}
}

// RegisterRoutes mounts the quality routes behind the admin guard (spec
// §11.1/§11.5: evaluation is admin-only). A nil guard mounts the routes without
// the role check; the server always passes identityhttp.RequireAdmin.
func (h *Handlers) RegisterRoutes(mux *http.ServeMux, guard AdminGuard) {
	if h == nil || h.runs == nil || mux == nil {
		return
	}
	wrap := func(handler http.HandlerFunc) http.Handler {
		if guard == nil {
			return handler
		}
		return guard(handler)
	}
	mux.Handle("GET /api/quality/eval-runs", wrap(h.handleList))
	mux.Handle("GET /api/quality/eval-runs/{id}", wrap(h.handleGet))
	mux.Handle("POST /api/quality/eval-runs", wrap(h.handleRecord))
}

// Register mounts the handler group on mux. It is the package-level form of the
// method, for composition roots that prefer an explicit handler argument.
func Register(mux *http.ServeMux, runs EvalRunService, guard AdminGuard) {
	NewHandlers(runs).RegisterRoutes(mux, guard)
}
