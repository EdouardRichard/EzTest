// Package httpapi exposes the P8 exploration-chain read model over HTTP
// (spec §5.1): one GET endpoint that returns the whole requirement → case →
// result → issue graph for a version. Handlers stay thin: they validate the
// path/query input, call the aggregation service and render the shared
// envelopes. Every id crosses this boundary as a decimal string (D29).
package httpapi

import (
	"context"
	"net/http"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Builder is the read-model use case consumed by the handler. The chain
// application Service satisfies it; tests can substitute a fake.
type Builder interface {
	Build(ctx context.Context, query chainapp.Query) (chainapp.Snapshot, error)
}

// Handlers is the exploration-chain HTTP handler group. The composition root
// stores it in server.Dependencies.Chain; a nil group mounts nothing.
type Handlers struct {
	builder Builder
}

// NewHandlers builds the handler group. It returns nil when no builder is
// wired, mirroring the other contexts' partially-wired-process behaviour.
func NewHandlers(builder Builder) *Handlers {
	if builder == nil {
		return nil
	}
	return &Handlers{builder: builder}
}

// RegisterRoutes mounts GET /api/versions/{versionId}/exploration-chain.
// Authentication is the server's global bearer middleware; the endpoint adds no
// per-route guard (spec §5.1: any logged-in user may read a version chain).
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || h.builder == nil || mux == nil {
		return
	}
	mux.Handle("GET /api/versions/{versionId}/exploration-chain",
		http.HandlerFunc(h.handleExplorationChain))
}

// RegisterRoutes mounts handler on mux. It is the package-level form of the
// method, for composition roots that prefer an explicit handler argument.
func RegisterRoutes(mux *http.ServeMux, handler *Handlers) {
	handler.RegisterRoutes(mux)
}
