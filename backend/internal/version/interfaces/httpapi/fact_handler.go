package versionapi

// Task 28 (v0.22 B §9.2, spec R-B6-2/API-13): the fact arbitration write route.
//
//	POST /api/versions/{id}/facts/{fid}/arbitrate
//
// The handler only parses the path/body and renders the resolution: the
// conflict selection, superseded_by writes, fact.arbitrated event and audit
// record belong to *versionapp.FactService. Route registration and the OpenAPI
// schema are wired by the server task (Task 37).

import (
	"context"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// FactArbitrationService is the arbitration surface the handler needs.
// *versionapp.FactService implements it.
type FactArbitrationService interface {
	ArbitrateFact(ctx context.Context, versionID, factID int64, decision versionapp.FactArbitrationDecision, reason string, actorID int64) (versionapp.FactArbitration, error)
}

var _ FactArbitrationService = (*versionapp.FactService)(nil)

// FactHandler serves the fact write routes. The server mounts it behind the
// shared bearer-authentication middleware; the handler enforces the
// authenticated actor like the sibling mutating handlers.
type FactHandler struct {
	svc FactArbitrationService
}

// NewFactHandler builds the fact handler. A nil service answers 503.
func NewFactHandler(svc FactArbitrationService) *FactHandler {
	return &FactHandler{svc: svc}
}

// RegisterFactRoutes mounts the fact write routes on mux. A nil handler, mux or
// authentication middleware leaves the route table unchanged; the Task 37
// server assembly owns the middleware value.
func RegisterFactRoutes(mux *http.ServeMux, auth func(http.Handler) http.Handler, h *FactHandler) {
	if mux == nil || auth == nil || h == nil {
		return
	}
	mux.Handle("POST /api/versions/{id}/facts/{fid}/arbitrate", auth(http.HandlerFunc(h.Arbitrate)))
}

// Arbitrate POST /api/versions/{id}/facts/{fid}/arbitrate. The body carries
// decision (keep|drop) and reason. A missing version/fact is 404, an invalid
// decision/reason is 400, and an already-resolved or non-conflicting fact is a
// 409 state conflict; repeating the same decision is an idempotent 200.
func (h *FactHandler) Arbitrate(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	factID, err := validation.NonEmptyID("fid", r.PathValue("fid"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request factArbitrationRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h == nil || h.svc == nil {
		httpx.Error(w, r, platform.Unavailable("fact arbitration is not configured"))
		return
	}
	outcome, err := h.svc.ArbitrateFact(r.Context(), versionID, factID,
		versionapp.FactArbitrationDecision(request.Decision), request.Reason, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toFactArbitrationDTO(outcome))
}

// ---------- DTOs ----------

type factArbitrationRequest struct {
	Decision string `json:"decision"`
	Reason   string `json:"reason"`
}

type factArbitrationFactDTO struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Key          string   `json:"key"`
	Confidence   *float64 `json:"confidence"`
	ContentHash  string   `json:"content_hash"`
	SupersededBy *string  `json:"superseded_by"`
}

// factArbitrationDTO is the API-13 response payload (rendered inside the shared
// {data} envelope by httpx.JSON): the applied decision, the winning row and the
// current rows of the key after the arbitration. dropped_ids uses the same name
// as the fact.arbitrated event payload and the audit metadata (drop_ids is not
// used anywhere); the Task 37 OpenAPI schema must mirror it.
type factArbitrationDTO struct {
	VersionID  string                   `json:"version_id"`
	FactID     string                   `json:"fact_id"`
	Kind       string                   `json:"kind"`
	Key        string                   `json:"key"`
	Decision   string                   `json:"decision"`
	Reason     string                   `json:"reason"`
	KeepID     string                   `json:"keep_id"`
	DroppedIDs []string                 `json:"dropped_ids"`
	Changed    bool                     `json:"changed"`
	Current    []factArbitrationFactDTO `json:"current"`
}

func toFactArbitrationDTO(outcome versionapp.FactArbitration) factArbitrationDTO {
	current := make([]factArbitrationFactDTO, 0, len(outcome.Facts))
	for _, fact := range outcome.Facts {
		current = append(current, factArbitrationFactDTO{
			ID:           formatDecimal(fact.ID),
			Kind:         fact.Kind,
			Key:          fact.Key,
			Confidence:   fact.Confidence,
			ContentHash:  fact.ContentHash,
			SupersededBy: formatOptionalDecimal(fact.SupersededBy),
		})
	}
	dropped := make([]string, 0, len(outcome.DroppedIDs))
	for _, id := range outcome.DroppedIDs {
		dropped = append(dropped, formatDecimal(id))
	}
	return factArbitrationDTO{
		VersionID:  formatDecimal(outcome.VersionID),
		FactID:     formatDecimal(outcome.FactID),
		Kind:       outcome.Kind,
		Key:        outcome.Key,
		Decision:   string(outcome.Decision),
		Reason:     outcome.Reason,
		KeepID:     formatDecimal(outcome.KeepID),
		DroppedIDs: dropped,
		Changed:    outcome.Changed,
		Current:    current,
	}
}
