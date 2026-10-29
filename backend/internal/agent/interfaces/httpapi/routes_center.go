package httpapi

import (
	"context"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
)

// CenterReader is the read-only Agent-centre use case consumed by the center
// handler. The application *CenterService satisfies it; tests can substitute a
// fake. Every method is a pure read: the center endpoints never mutate profiles,
// sessions or runs.
type CenterReader interface {
	Catalog(ctx context.Context) (application.CenterCatalog, error)
	Sessions(ctx context.Context, query application.CenterSessionQuery) (application.CenterSessionPage, error)
	Quality(ctx context.Context) (application.CenterQuality, error)
}

// CenterHandler serves the read-only P8 Agent-centre endpoints. The routes are
// mounted under the global bearer authentication only: every logged-in user may
// read the catalogue/run observation/quality, while profile writes stay behind
// the P1 admin guard (design spec §8.1/D60).
type CenterHandler struct {
	reader CenterReader
}

// NewCenterHandler returns the Agent-centre handler, or nil when no reader is
// wired so a partially assembled process mounts nothing.
func NewCenterHandler(reader CenterReader) *CenterHandler {
	if reader == nil {
		return nil
	}
	return &CenterHandler{reader: reader}
}

// RegisterRoutes mounts the read-only center endpoints on mux. Authentication is
// the server's global bearer middleware; no per-route admin guard is installed,
// because the catalogue and run observation are readable by regular users.
func (h *CenterHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || h.reader == nil || mux == nil {
		return
	}
	mux.Handle("GET /api/agent-center/catalog", http.HandlerFunc(h.handleCatalog))
	mux.Handle("GET /api/agent-center/sessions", http.HandlerFunc(h.handleSessions))
	mux.Handle("GET /api/agent-center/quality", http.HandlerFunc(h.handleQuality))
}

// RegisterCenterRoutes mounts handler on mux. It is the package-level form of
// the method, for composition roots that prefer an explicit handler argument.
func RegisterCenterRoutes(mux *http.ServeMux, handler *CenterHandler) {
	handler.RegisterRoutes(mux)
}
