package httpapi

// API-19 / R-EX-4: GET /api/versions/{id}/scope-snapshot?wave_id= exposes the
// frozen effective scope recorded on a run_wave. The handler is deliberately
// read-only: the snapshot is written once by the planner when the wave is
// started and is never recomputed here.

import (
	"encoding/json"
	"net/http"
	"strings"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ScopeSnapshotView is the GET /api/versions/{id}/scope-snapshot projection.
// ScopeSnapshot is the stored JSON object, emitted verbatim so consumers see
// the frozen shape (an empty stored value is still the object {}).
type ScopeSnapshotView struct {
	WaveID        string               `json:"wave_id"`
	Status        string               `json:"status"`
	ScopeSnapshot any                  `json:"scope_snapshot"`
	Counts        execution.WaveCounts `json:"counts"`
}

// ScopeSnapshot GET /api/versions/{id}/scope-snapshot?wave_id=.
//
// wave_id is required and must be a positive decimal ID (400 otherwise). A
// missing wave and a wave that belongs to another version both answer 404, so
// the endpoint cannot be used to probe cross-version existence.
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
	wave, err := h.deps.Waves.Get(ctx, waveID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "wave"))
		return
	}
	if wave.VersionID != versionID {
		httpx.Error(w, r, platform.NotFound("wave"))
		return
	}
	httpx.JSON(w, http.StatusOK, ScopeSnapshotView{
		WaveID:        platformid.Format(wave.ID),
		Status:        string(wave.Status),
		ScopeSnapshot: decodeScopeSnapshot(wave.ScopeSnapshot),
		Counts:        wave.Counts.Normalize(),
	})
}

// decodeScopeSnapshot decodes the stored JSON object. An empty or corrupt value
// degrades to {} so the endpoint keeps a stable object shape.
func decodeScopeSnapshot(raw string) any {
	snapshot := any(map[string]any{})
	if strings.TrimSpace(raw) == "" {
		return snapshot
	}
	if err := json.Unmarshal([]byte(raw), &snapshot); err != nil {
		return map[string]any{}
	}
	return snapshot
}
