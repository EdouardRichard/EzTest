package httpapi

import (
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// AnalysisHandler serves the batch analysis endpoints of spec §7.5. The batch
// ownership/terminality guard runs here (before the service) so a manual start
// never creates a run for a foreign or still-running batch.
type AnalysisHandler struct {
	analyses AnalysisService
	batches  BatchReader
}

// RegisterRoutes mounts the analysis routes. Authentication is the server's
// global middleware; any logged-in user may list runs and retry the manual
// failure analysis (spec §11.1).
func (h *AnalysisHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/analysis", h.List)
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/analysis/{analysisId}", h.Get)
	mux.HandleFunc("POST /api/versions/{id}/batches/{batchId}/analysis", h.Start)
}

// List returns the batch's analysis runs, optionally filtered by kind.
func (h *AnalysisHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := analysisBatchIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	kind := optionalString(r.URL.Query().Get("kind"))
	if kind != "" && kind != domain.AnalysisKindFailure && kind != domain.AnalysisKindIssueTriage {
		writeDeliveryError(w, r, domain.NewInvalid("kind", domain.ReasonInvalidArgument,
			"kind must be failure or issue_triage"))
		return
	}
	if err := requireBatch(r.Context(), h.batches, versionID, batchID, false); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	runs, err := h.analyses.List(r.Context(), versionID, batchID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if kind != "" {
		filtered := make([]domain.AnalysisRun, 0, len(runs))
		for _, run := range runs {
			if run.Kind == kind {
				filtered = append(filtered, run)
			}
		}
		runs = filtered
	}
	writeData(w, http.StatusOK, newAnalysisDTOs(runs))
}

// Get returns one analysis run with its result/stats/errors documents.
func (h *AnalysisHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := analysisBatchIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	analysisID, err := pathID(r, "analysisId")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if err := requireBatch(r.Context(), h.batches, versionID, batchID, false); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	run, err := h.analyses.Get(r.Context(), versionID, batchID, analysisID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newAnalysisDTO(run))
}

// startAnalysisBody is the POST body of the manual analysis start. P7 only
// exposes the failure-analysis retry (spec §7.5).
type startAnalysisBody struct {
	Kind string `json:"kind"`
}

// Start retries the failure analysis of a terminal batch (202). An active run is
// 409 analysis_active and a batch without failed application cases is 409
// analysis_not_applicable, both decided by the service.
func (h *AnalysisHandler) Start(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := analysisBatchIDs(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	var body startAnalysisBody
	if err := decodeJSON(r, &body, 0); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if body.Kind != domain.AnalysisKindFailure {
		writeDeliveryError(w, r, domain.NewInvalid("kind", domain.ReasonInvalidArgument,
			`the manual analysis start only supports kind "failure"`))
		return
	}
	if err := requireBatch(r.Context(), h.batches, versionID, batchID, true); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	run, err := h.analyses.StartFailure(r.Context(), versionID, batchID, actorID(r.Context()))
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusAccepted, newAnalysisDTO(run))
}

// analysisBatchIDs parses the {id} and {batchId} path values.
func analysisBatchIDs(r *http.Request) (int64, int64, error) {
	versionID, err := pathID(r, "id")
	if err != nil {
		return 0, 0, err
	}
	batchID, err := pathID(r, "batchId")
	if err != nil {
		return 0, 0, err
	}
	return versionID, batchID, nil
}
