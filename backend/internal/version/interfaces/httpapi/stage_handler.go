package versionapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// StageService is the application surface the stage handlers need. The stage
// machine is the only implementation: the HTTP layer never writes version.stage
// itself.
type StageService interface {
	Stage(ctx context.Context, versionID int64) (versionapp.StageView, error)
	OnRequirementConfirmed(ctx context.Context, versionID int64, requirementIDs []int64) (versionapp.StageView, error)
	OnCasesConfirmed(ctx context.Context, versionID int64, caseIDs []int64) (versionapp.StageView, error)
	Reject(ctx context.Context, versionID int64, reason string, target *version.Stage) (versionapp.StageView, error)
	RecordGateDenied(ctx context.Context, versionID int64, gate string)
}

// Gate identifiers recorded on a forbidden hard-gate attempt.
const (
	stageGateRequirement = "requirement"
	stageGateCase        = "case"
	stageGateReject      = "reject"
)

// StageHandler serves the version stage-machine API (spec §5.3/§5.5): the
// stage read and the two user-only hard gates plus the gate reject.
type StageHandler struct {
	svc StageService
}

// NewStageHandler builds the stage handler.
func NewStageHandler(svc StageService) *StageHandler {
	return &StageHandler{svc: svc}
}

var _ StageService = (*versionapp.StageMachine)(nil)

// Get GET /api/versions/{id}/stage. Any authenticated user may read the stage.
func (h *StageHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, err := stagePathID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.svc.Stage(r.Context(), versionID)
	if err != nil {
		httpx.Error(w, r, err)
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
		httpx.Error(w, r, err)
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
		httpx.Error(w, r, err)
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
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toStageDTO(view))
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

// formatDecimal renders a decimal ID as the JSON string used by published
// version IDs.
func formatDecimal(value int64) string {
	return fmt.Sprintf("%d", value)
}
