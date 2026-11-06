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
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

const maxRequestBody = 1 << 20

// GraphService is the design application surface the handlers need.
type GraphService interface {
	ReadGraph(ctx context.Context, versionID int64) (designapp.GraphView, error)
	Overview(ctx context.Context, versionID int64) (designapp.OverviewView, error)
	GetCase(ctx context.Context, versionID, caseID int64, caseVersionID *int64) (designapp.CaseDetailView, error)
	Confirm(ctx context.Context, versionID int64, in designapp.ConfirmInput, actorID int64) (designapp.ConfirmResult, error)
	RollbackCaseVersion(ctx context.Context, versionID, caseVersionID int64, reason string, actorID int64) (designapp.CaseVersionRollbackView, error)
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
	stages  StageReader
}

// StageReader is the deterministic version-stage projection used to keep the
// single-case confirmation at the case gate only (Task 78).
// *versionapp.StageMachine satisfies it.
type StageReader interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
}

// New builds the design handler.
func New(graph GraphService, exports ExportService) *Handler {
	return &Handler{graph: graph, exports: exports}
}

// SetStageReader wires the stage projection. Without it the handler keeps the
// P2 behavior (no stage gate); production always sets it.
func (h *Handler) SetStageReader(reader StageReader) {
	h.stages = reader
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

// requireCaseReview rejects design confirmations that are only meaningful at
// the case hard gate. A version still clarifying/designing must not silently
// freeze case versions; the typed 409 carries the requested scope and a
// scope-appropriate next action so the UI can guide the user.
func (h *Handler) requireCaseReview(ctx context.Context, versionID int64, scope string) error {
	if h.stages == nil {
		return nil
	}
	stage, err := h.stages.Stage(ctx, versionID)
	if err != nil {
		return err
	}
	if string(stage.Stage) == "case_review" {
		return nil
	}
	return &platform.Error{
		Status:  http.StatusConflict,
		Code:    "invalid_stage_for_action",
		Message: scope + " confirmation requires stage case_review, but the version is at " + string(stage.Stage),
		Details: map[string]any{
			"action":         "confirm_cases",
			"scope":          scope,
			"required_stage": "case_review",
			"current_stage":  string(stage.Stage),
			"next_action":    confirmationScopeNextAction(scope),
		},
	}
}

// confirmationScopeNextAction tailors the guidance to the confirmation unit:
// a case confirmation names one case, while a scenario/requirement batch
// confirms every draft case below it.
func confirmationScopeNextAction(scope string) string {
	switch scope {
	case "scenario":
		return "confirm the requirements and wait for design generation to reach the case gate, then confirm the scenario's draft cases"
	case "requirement":
		return "confirm the requirements and wait for design generation to reach the case gate, then confirm the requirement's draft cases"
	default:
		return "confirm the requirements and wait for design generation to reach the case gate, then confirm this case"
	}
}
