package httpapi

import (
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// VersionChanges returns the version-scoped change preview (design spec §7.3):
// the application service resolves the version's bound baseline commit and the
// head (explicit 40-hex descendant of the baseline, or the repository fixed
// branch tip live) and computes the diff without persisting it. The ChangeList
// DTO of R7 is returned inside data; changeSetId stays null until an analysis
// persists the deterministic change set. The preview is not cursor-paged, so
// nextCursor is null and hasMore false.
func (h *Handler) VersionChanges(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	data, base, head, err := h.query.VersionChanges(r.Context(), versionID,
		strings.TrimSpace(r.URL.Query().Get("head")))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	dto, err := newChangeListDTO(data, base, head, 0)
	if err != nil {
		writeInternalError(w, r)
		return
	}
	WriteData(w, http.StatusOK, dto)
}

// StartAnalysis triggers a version-scoped impact analysis or reuses the done one
// for the same input (design spec §6.4/§6.8). The body carries the optional
// headCommit and force flag; the application service validates the version
// binding, the head commit and the active-run rule. A newly queued analysis is a
// 202; a reused one is a 200 (both report reused so the caller can tell them
// apart).
func (h *Handler) StartAnalysis(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var body startAnalysisRequest
	if err := DecodeJSON(r, &body, maxRequestBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	analysis, reused, err := h.analyses.Start(r.Context(), versionID, ActorID(r.Context()), body.HeadCommit, body.Force)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if reused {
		status = http.StatusOK
	}
	WriteData(w, status, StartAnalysisDTO{
		ID:              formatID(analysis.ID),
		Status:          analysis.Status,
		AnalysisVersion: analysis.AnalysisVersion,
		Reused:          reused,
	})
}

// LatestAnalysis returns the newest analysis of the version with its aggregate
// projection, or the documented 404 analysis_not_found.
func (h *Handler) LatestAnalysis(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	analysis, err := h.analyses.GetLatest(r.Context(), versionID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeAnalysis(w, r, analysis)
}

// GetAnalysis returns one analysis of the version. The version scoping is part
// of the application call, so an analysis of another version is a plain 404.
func (h *Handler) GetAnalysis(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	analysisID, err := PathID(r, "analysisId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	analysis, err := h.analyses.Get(r.Context(), versionID, analysisID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.writeAnalysis(w, r, analysis)
}

// writeAnalysis aggregates one analysis with its persisted candidates (design
// spec §7.3: the latest/detail reads include candidates, unmapped changes and
// the fact-version snapshot) and renders the AnalysisDTO.
func (h *Handler) writeAnalysis(w http.ResponseWriter, r *http.Request, analysis domain.CodeAnalysis) {
	candidates, err := h.analyses.ListCandidates(r.Context(), analysis.ID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAnalysisDTO(analysis, candidates))
}

// PatchCandidate persists one human candidate selection (design spec §6.6/§6.11)
// and echoes the updated candidate. The application service enforces the
// version/analysis/candidate ownership and the done state (409
// analysis_not_done); the handler only requires the selected boolean and
// forwards the authenticated actor for the selection metadata.
func (h *Handler) PatchCandidate(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	analysisID, err := PathID(r, "analysisId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	candidateID, err := PathID(r, "candidateId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var body patchCandidateRequest
	if err := DecodeJSON(r, &body, maxRequestBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if body.Selected == nil {
		WriteServiceError(w, r, newBadRequest("selected", reasonInvalidRequest, "selected is required"))
		return
	}
	candidate, err := h.analyses.PatchCandidate(r.Context(), versionID, analysisID, candidateID,
		ActorID(r.Context()), *body.Selected)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newCandidateDTO(candidate))
}
