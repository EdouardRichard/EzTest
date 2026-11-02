// Package designapi exposes the P2 design graph/confirmation/export HTTP API
// (spec §7). The server mounts these handlers behind the bearer-auth guard;
// handlers only parse DTOs, validate input and map errors.
package designapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

const maxRequestBody = 1 << 20

// GraphService is the design application surface the handlers need.
type GraphService interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
	Overview(ctx context.Context, versionID int64) (designapp.OverviewView, error)
	GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error)
	Confirm(ctx context.Context, versionID int64, in designapp.ConfirmInput, actorID int64) (designapp.ConfirmResult, error)
}

// ExportService is the design export application surface.
type ExportService interface {
	Create(ctx context.Context, versionID int64, in designapp.ExportInput, actorID int64) (designapp.ExportView, error)
	List(ctx context.Context, versionID int64, format string, limit int, beforeID *int64) ([]designapp.ExportView, bool, error)
	Get(ctx context.Context, exportID int64) (designapp.ExportView, error)
	Open(ctx context.Context, exportID int64) (designapp.ExportView, io.ReadCloser, error)
}

// Handler serves the P2 design endpoints.
type Handler struct {
	graph   GraphService
	exports ExportService
}

// New builds the design handler.
func New(graph GraphService, exports ExportService) *Handler {
	return &Handler{graph: graph, exports: exports}
}

var (
	_ GraphService  = (*designapp.GraphService)(nil)
	_ ExportService = (*designapp.ExportService)(nil)
)

func requireActor(w http.ResponseWriter, r *http.Request) (int64, bool) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return 0, false
	}
	return actor.ID, true
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}

func pathID(r *http.Request, field, name string) (int64, error) {
	return validation.NonEmptyID(field, r.PathValue(name))
}

func queryOptionalID(r *http.Request, field, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	value, err := validation.NonEmptyID(field, raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}
