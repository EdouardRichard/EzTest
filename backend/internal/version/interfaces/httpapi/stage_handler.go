package versionapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// StageService is the legacy (pre-v0.22) application surface the two hard-gate
// confirmation routes need. The server adapts the v0.22 *StageService onto this
// interface so the handlers never write version.stage themselves. New stage
// writes go through StageWorkflow.
type StageService interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
	OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (versionapp.StageView, error)
	OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (versionapp.StageView, error)
	Reject(ctx context.Context, versionID int64, reason string, target *version.Stage) (versionapp.StageView, error)
	RecordGateDenied(ctx context.Context, versionID int64, gate string)
}

// StageWorkflow is the v0.22 orchestrated stage write surface (Task 25): the
// single application entry point the HTTP layer uses for forward requests,
// structured blockers and pause/resume. *versionapp.StageService implements it.
type StageWorkflow interface {
	// Stage returns the current stage projection.
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
	// Request validates and applies one explicit target stage.
	Request(ctx context.Context, versionID int64, to version.Stage) error
	// Advance applies the next forward workflow stage.
	Advance(ctx context.Context, versionID int64) error
	// Block enters blocked with a structured reason/action/next.
	Block(ctx context.Context, versionID int64, blocker versionapp.Blocker) error
	// Unblock resumes a blocked version to its recorded stage.
	Unblock(ctx context.Context, versionID int64) error
	// Pause enters paused and pauses the running waves.
	Pause(ctx context.Context, versionID int64, reason string) error
	// Resume leaves paused and continues the paused waves.
	Resume(ctx context.Context, versionID int64) error
}

var (
	_ StageService  = (*versionapp.StageMachine)(nil)
	_ StageWorkflow = (*versionapp.StageService)(nil)
)

// Gate identifiers recorded on a forbidden hard-gate attempt.
const (
	stageGateRequirement = "requirement"
	stageGateCase        = "case"
	stageGateReject      = "reject"
)

// StageHandler serves the version stage-machine API (spec §5.3/§5.5): the
// stage read, the two user-only hard gates, the gate reject and the v0.22
// request/block/unblock/pause/resume writes.
type StageHandler struct {
	svc      StageService
	workflow StageWorkflow
}

// NewStageHandler builds the stage handler.
func NewStageHandler(svc StageService) *StageHandler {
	return &StageHandler{svc: svc}
}

// SetStageWorkflow wires the v0.22 stage write surface. Without it the new
// request/block/pause routes answer 503 instead of panicking; the production
// assembly always sets it (Task 37).
func (h *StageHandler) SetStageWorkflow(workflow StageWorkflow) {
	h.workflow = workflow
}

// Get GET /api/versions/{id}/stage. Any authenticated user may read the stage.
func (h *StageHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Stage(r.Context(), versionID)
	if err != nil {
		writeStageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
}

// ConfirmRequirements POST /api/versions/{id}/stage/confirm-requirements.
// Only a user token may confirm the requirement hard gate; assistant and
// service tokens are rejected with 403 and an audit entry. An empty
// requirement_ids list confirms every current draft requirement (409
// no_requirements_to_confirm when the version has none).
func (h *StageHandler) ConfirmRequirements(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireUserGate(w, r, versionID, stageGateRequirement) {
		return
	}
	var request stageConfirmRequirementsRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ids, err := parseStageIDs(request.RequirementIDs, "requirement_ids")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.OnRequirementConfirmed(r.Context(), versionID, ids)
	if err != nil {
		writeStageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
}

// ConfirmCases POST /api/versions/{id}/stage/confirm-cases. Only a user token
// may confirm the case hard gate. A non-empty case_ids list is a partial
// confirmation; an empty list confirms every current draft case (409
// no_cases_to_confirm when the version has none).
func (h *StageHandler) ConfirmCases(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireUserGate(w, r, versionID, stageGateCase) {
		return
	}
	var request stageConfirmCasesRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ids, err := parseStageIDs(request.CaseIDs, "case_ids")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.OnCasesConfirmed(r.Context(), versionID, ids)
	if err != nil {
		writeStageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
}

// Reject POST /api/versions/{id}/stage/reject. Returns the current hard gate
// one stage back; only a user token may reject a gate.
func (h *StageHandler) Reject(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !h.requireUserGate(w, r, versionID, stageGateReject) {
		return
	}
	var request stageRejectRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	reason := strings.TrimSpace(request.Reason)
	if reason == "" {
		httpx.Error(w, r, platform.Invalid("reason", "must not be empty"))
		return
	}
	target, err := parseStageRejectTarget(request.TargetStage)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Reject(r.Context(), versionID, reason, target)
	if err != nil {
		writeStageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
}

// Request POST /api/versions/{id}/stage/request. The body carries the explicit
// target stage (`{"to":"designing"}`). Only forward workflow edges are accepted;
// suspension stages have their own block/pause entry points. Typed gate
// rejections map to 409 `stage_gate_failed`, invalid edges to 409
// `invalid_stage_transition`.
func (h *StageHandler) Request(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	var request stageRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	target, err := parseStageTarget(request.To)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.workflow.Request(r.Context(), versionID, target); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// Advance POST /api/versions/{id}/stage/advance. Applies the next forward
// workflow stage without naming it.
func (h *StageHandler) Advance(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	if err := h.workflow.Advance(r.Context(), versionID); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// Block POST /api/versions/{id}/stage/block. The structured kind/reason/action/
// next are required (manual is the default kind) and persisted in version.blocker.
func (h *StageHandler) Block(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	var request stageBlockRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	blocker, err := (versionapp.Blocker{
		Kind:   request.Kind,
		Reason: request.Reason,
		Action: request.Action,
		Next:   request.Next,
	}).Normalize()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.workflow.Block(r.Context(), versionID, blocker); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// Unblock POST /api/versions/{id}/stage/unblock. The blocker-clearance gate is
// evaluated server-side; a still-blocked reason answers the typed gate 409.
func (h *StageHandler) Unblock(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	if err := h.workflow.Unblock(r.Context(), versionID); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// Pause POST /api/versions/{id}/stage/pause. The optional reason is recorded in
// version.blocker and the running waves pause gracefully (R-B4-5).
func (h *StageHandler) Pause(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	var request stagePauseRequest
	if err := decodeOptionalStageBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.workflow.Pause(r.Context(), versionID, request.Reason); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// Resume POST /api/versions/{id}/stage/resume. Only a paused version may resume;
// blocked versions use unblock.
func (h *StageHandler) Resume(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.workflow == nil {
		httpx.Error(w, r, platform.Unavailable("version stage workflow is not configured"))
		return
	}
	if err := h.workflow.Resume(r.Context(), versionID); err != nil {
		writeStageError(w, r, err)
		return
	}
	h.writeCurrentStage(w, r, versionID)
}

// writeCurrentStage renders the stage projection after a successful write.
func (h *StageHandler) writeCurrentStage(w http.ResponseWriter, r *http.Request, versionID int64) {
	view, err := h.svc.Stage(r.Context(), versionID)
	if err != nil {
		writeStageError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
}

// writeStageError maps the typed stage gate/transition rejections through their
// APIError envelope (409 stage_gate_failed / invalid_stage_transition) and
// leaves every other platform/application error to the shared mapper.
func writeStageError(w http.ResponseWriter, r *http.Request, err error) {
	var typed interface{ APIError() *platform.Error }
	if errors.As(err, &typed) {
		httpx.Error(w, r, typed.APIError())
		return
	}
	httpx.Error(w, r, err)
}

// requireUserGate enforces the D7 rule that only a user API may trigger a hard
// gate. The denied attempt is audited before the 403 is returned.
func (h *StageHandler) requireUserGate(w http.ResponseWriter, r *http.Request, versionID int64, gate string) bool {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		httpx.Error(w, r, platform.Unauthenticated())
		return false
	}
	if actor.Role != "user" && actor.Role != "admin" {
		h.svc.RecordGateDenied(r.Context(), versionID, gate)
		httpx.Error(w, r, platform.Forbidden())
		return false
	}
	return true
}

// ---------- DTOs ----------

type stageDTO struct {
	VersionID      string  `json:"version_id"`
	Stage          string  `json:"stage"`
	StageEnteredAt string  `json:"stage_entered_at"`
	Blocker        *string `json:"blocker"`
}

type stageConfirmRequirementsRequest struct {
	RequirementIDs []string `json:"requirement_ids"`
}

type stageConfirmCasesRequest struct {
	CaseIDs []string `json:"case_ids"`
}

type stageRejectRequest struct {
	Reason      string  `json:"reason"`
	TargetStage *string `json:"target_stage"`
}

// stageRequest is the POST /stage/request body.
type stageRequest struct {
	To string `json:"to"`
}

// stageBlockRequest is the POST /stage/block structured blocker body (R-B4-4).
type stageBlockRequest struct {
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
	Action string `json:"action"`
	Next   string `json:"next"`
}

// stagePauseRequest is the optional POST /stage/pause body.
type stagePauseRequest struct {
	Reason string `json:"reason"`
}

func toStageDTO(view versionapp.StageView) stageDTO {
	return stageDTO{
		VersionID:      formatDecimal(view.VersionID),
		Stage:          string(view.Stage),
		StageEnteredAt: formatTime(view.StageEnteredAt),
		Blocker:        formatOptionalString(view.Blocker),
	}
}

// ---------- parsing helpers ----------

func stagePathID(r *http.Request) (int64, error) {
	return validation.NonEmptyID("id", r.PathValue("id"))
}

func parseStageIDs(raw []string, field string) ([]int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	for index, value := range raw {
		id, err := validation.NonEmptyID(fmt.Sprintf("%s[%d]", field, index), value)
		if err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func parseStageRejectTarget(raw *string) (*version.Stage, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}
	target := version.Stage(strings.TrimSpace(*raw))
	switch target {
	case version.StageClarifying, version.StageDesigning:
		return &target, nil
	default:
		return nil, platform.Invalid("target_stage", "must be one of clarifying, designing")
	}
}

// parseStageTarget validates an explicit stage-request target. Suspension
// stages are accepted by the parser and then rejected by the workflow with the
// typed invalid-transition envelope, so the API explains the dedicated
// block/pause entry point instead of answering a generic bad request.
func parseStageTarget(raw string) (version.Stage, error) {
	target := version.Stage(strings.TrimSpace(raw))
	switch target {
	case version.StageCreated, version.StageClarifying, version.StageRequirementReview,
		version.StageDesigning, version.StageCaseReview, version.StageExecuting,
		version.StageReporting, version.StageCompleted, version.StageBlocked,
		version.StagePaused:
		return target, nil
	default:
		return "", platform.Invalid("to", "must be a known version stage")
	}
}

// decodeOptionalStageBody accepts an absent/empty body (pause carries an
// optional reason). It reuses the shared optional-body decoder so malformed JSON
// keeps the typed 400 envelope.
func decodeOptionalStageBody(w http.ResponseWriter, r *http.Request, dst any) error {
	return decodeOptionalExtractionBody(w, r, dst)
}

// formatDecimal renders a decimal ID as the JSON string used by published
// version IDs.
func formatDecimal(value int64) string {
	return fmt.Sprintf("%d", value)
}
