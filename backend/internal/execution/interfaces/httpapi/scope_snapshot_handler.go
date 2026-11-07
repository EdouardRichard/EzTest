package httpapi

// API-19 / R-EX-4: GET /api/versions/{id}/scope-snapshot?wave_id= exposes the
// frozen effective scope recorded on a run_wave. The handler is deliberately
// read-only: the snapshot is written once by the planner when the wave is
// started and is never recomputed here.
//
// The stored snapshot is passed through as json.RawMessage instead of being
// decoded into any: snowflake ids exceed 2^53, so a float64 round-trip would
// silently rewrite every id in the payload (e.g. 375994775003729921 ->
// 375994775003729900).

import (
	"bytes"
	"encoding/json"
	"net/http"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ScopeSnapshotView is the GET /api/versions/{id}/scope-snapshot projection.
// ScopeSnapshot is the stored JSON object emitted verbatim: raw bytes preserve
// every number token exactly (snowflake ids above 2^53 included), and an empty
// or non-object stored value is still the object {}.
type ScopeSnapshotView struct {
	WaveID        string               `json:"wave_id"`
	Status        string               `json:"status"`
	ScopeSnapshot json.RawMessage      `json:"scope_snapshot"`
	Counts        execution.WaveCounts `json:"counts"`
}

// ScopeSnapshot GET /api/versions/{id}/scope-snapshot?wave_id=.
//
// wave_id is required and must be a positive canonical decimal ID (400
// otherwise). A missing wave and a wave that belongs to another version both
// answer 404, so the endpoint cannot be used to probe cross-version existence.
func (h *Handler) ScopeSnapshot(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := requiredQueryID(r, "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	wave, err := h.waveOfVersion(ctx, versionID, waveID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ScopeSnapshotView{
		WaveID:        platformid.Format(wave.ID),
		Status:        string(wave.Status),
		ScopeSnapshot: scopeSnapshotJSON(wave.ScopeSnapshot),
		Counts:        wave.Counts.Normalize(),
	})
}

// scopeSnapshotJSON passes the stored document through as raw JSON bytes.
// json.RawMessage is re-emitted verbatim (only insignificant whitespace is
// compacted), so snowflake ids keep their exact decimal token. The JSONB column
// is CHECKed to be an object and DEFAULTs to '{}', so an empty or non-object
// value can only come from a legacy or hand-built row; it degrades to {} to keep
// the frozen object shape.
func scopeSnapshotJSON(raw string) json.RawMessage {
	trimmed := bytes.TrimSpace([]byte(raw))
	if len(trimmed) == 0 || trimmed[0] != '{' || !json.Valid(trimmed) {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(trimmed)
}
