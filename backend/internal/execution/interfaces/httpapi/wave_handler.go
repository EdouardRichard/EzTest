package httpapi

import (
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// StartWave POST /api/versions/{id}/waves (API-42, W1-7/D1).
//
// It starts one wave immediately: the optional body selects the wave scope by
// case ID (absent/empty means every current+confirmed case), the initial intents
// are ensured through the intent service, and one scheduler reconcile plans and
// dispatches the wave. The empty selection uses the explicit ForCases(nil) scan,
// not the D23 repair scan: the repair scan can only scope itself to a persisted
// execute.batch selection and would answer the contract's valid empty selection
// with ErrNoPersistedBatchSelection (500). Both scans keep the typed 409 for a
// version that is not at executing. An already active wave is a 409, and so is a
// reconcile that leaves no active wave (no dispatchable case, or the version is
// not at the executing stage). The response carries the wave id, status and
// case_run count.
func (h *Handler) StartWave(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Planner == nil {
		httpx.Error(w, r, platform.Unavailable("the wave planner is not configured"))
		return
	}
	active, err := h.deps.Waves.GetActive(ctx, versionID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "wave"))
		return
	}
	if active != nil {
		httpx.Error(w, r, platform.StateConflict("a wave is already active"))
		return
	}
	var request startWaveRequest
	if r.ContentLength != 0 {
		if err := decodeBody(w, r, &request); err != nil {
			httpx.Error(w, r, err)
			return
		}
	}
	caseIDs, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if len(caseIDs) > 0 {
		if _, err := h.deps.IntentService.EnsureInitialIntentsForCases(ctx, versionID, caseIDs); err != nil {
			httpx.Error(w, r, err)
			return
		}
	} else if _, err := h.deps.IntentService.EnsureInitialIntentsForCases(ctx, versionID, nil); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if err := h.deps.Planner.ReconcileVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	active, err = h.deps.Waves.GetActive(ctx, versionID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "wave"))
		return
	}
	if active == nil {
		httpx.Error(w, r, platform.StateConflict("no dispatchable cases are available for a wave"))
		return
	}
	counts := active.Counts.Normalize()
	if counts.Total == 0 {
		// planVersion stores the wave before its counts snapshot is computed, so a
		// freshly planned wave carries an empty counts object. The repository
		// helper recomputes the authoritative case_run total.
		recomputed, err := h.deps.Waves.Counts(ctx, active.ID)
		if err != nil {
			httpx.Error(w, r, mapRepoError(err, "wave"))
			return
		}
		counts = recomputed.Normalize()
	}
	httpx.JSON(w, http.StatusCreated, StartWaveView{
		WaveID:       platformid.Format(active.ID),
		Status:       string(active.Status),
		CaseRunCount: counts.Total,
	})
}
