package codeintelapi

import (
	"context"
	"net/http"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// ImpactServicePort is the application surface the impact handler needs.
type ImpactServicePort interface {
	Union(ctx context.Context, versionID int64) ([]codeintelapp.ImpactCandidateView, error)
	Detail(ctx context.Context, analysisID int64) (codeintel.Analysis, []codeintelapp.ImpactCandidateView, error)
	SetSelected(ctx context.Context, analysisID, candidateID, actorID int64, selected bool) (codeintel.ImpactCandidate, error)
	AnalysisVersion(ctx context.Context, analysisID int64) (int64, error)
}

// ImpactHandler serves the impact union and candidate-selection operations
// (spec §4.2 #16–#17). Analysis detail candidates are provided by the same
// service through List when the assembly root composes the detail view.
type ImpactHandler struct {
	impact ImpactServicePort
}

// NewImpactHandler builds the impact handler.
func NewImpactHandler(impact ImpactServicePort) *ImpactHandler {
	return &ImpactHandler{impact: impact}
}

// Register mounts the impact routes behind the authenticated middleware.
func (h *ImpactHandler) Register(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	mux.Handle("GET /api/versions/{versionId}/code/impact", authenticated(http.HandlerFunc(h.Union)))
	mux.Handle("PATCH /api/versions/{versionId}/code/analyses/{analysisId}/candidates/{candidateId}", authenticated(http.HandlerFunc(h.SetSelected)))
}

// toImpactCandidateDTO renders one candidate with its multi-repo tags.
func toImpactCandidateDTO(view codeintelapp.ImpactCandidateView) codeCandidateDTO {
	content := view.ImpactCandidate
	dto := codeCandidateDTO{
		ID:                 platformid.Format(content.ID),
		AnalysisID:         platformid.Format(content.AnalysisID),
		SubjectKind:        string(content.SubjectKind),
		SubjectID:          platformid.Format(content.SubjectID),
		Reason:             content.Reason,
		Confidence:         string(content.Confidence),
		Source:             string(content.Source),
		Evidence:           decodeObjectArray(content.Evidence),
		Selected:           content.Selected,
		SelectionUpdatedBy: nil,
		SelectionUpdatedAt: content.SelectionUpdatedAt,
		CreatedAt:          content.CreatedAt,
	}
	if view.RepoID > 0 {
		dto.RepoID = stringPtr(platformid.Format(view.RepoID))
	}
	if len(view.VersionSUTIDs) > 0 {
		dto.VersionSUTID = stringPtr(platformid.Format(view.VersionSUTIDs[0]))
	}
	if content.SelectionUpdatedBy != nil {
		dto.SelectionUpdatedBy = stringPtr(platformid.Format(*content.SelectionUpdatedBy))
	}
	if dto.Evidence == nil {
		dto.Evidence = []map[string]any{}
	}
	return dto
}

// toSingleCandidateDTO renders one candidate without the union tags.
func toSingleCandidateDTO(candidate codeintel.ImpactCandidate) codeCandidateDTO {
	return toImpactCandidateDTO(codeintelapp.ImpactCandidateView{ImpactCandidate: candidate})
}

// toAnalysisDetailDTOWithCandidates fills the CodeAnalysisDetailView candidate
// array Task 8 intentionally left empty.
func toAnalysisDetailDTOWithCandidates(analysis codeintel.Analysis, views []codeintelapp.ImpactCandidateView) codeAnalysisDetailDTO {
	detail := toAnalysisDetailDTO(analysis)
	detail.Candidates = make([]codeCandidateDTO, 0, len(views))
	for _, view := range views {
		detail.Candidates = append(detail.Candidates, toImpactCandidateDTO(view))
	}
	return detail
}

// Detail GET /api/versions/{versionId}/code/analyses/{analysisId} (spec §4.2
// #15). It is not registered by Register to avoid duplicating the T1 handler's
// route: the assembly root mounts op 15 on exactly one of the two handlers (or
// calls toAnalysisDetailDTOWithCandidates from the T1 handler).
func (h *ImpactHandler) Detail(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	analysisID, err := pathID(r, "analysisId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	analysis, views, err := h.impact.Detail(r.Context(), analysisID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if analysis.VersionID != versionID {
		writeError(w, r, platform.NotFound("code analysis"))
		return
	}
	httpx.JSON(w, http.StatusOK, toAnalysisDetailDTOWithCandidates(analysis, views))
}

// Union GET /api/versions/{versionId}/code/impact?repoId=.
func (h *ImpactHandler) Union(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := queryIDOptional(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	views, err := h.impact.Union(r.Context(), versionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeCandidateDTO, 0, len(views))
	for _, view := range views {
		if repoID > 0 && view.RepoID != repoID {
			continue
		}
		items = append(items, toImpactCandidateDTO(view))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// impactSelectionRequest is the PATCH candidate payload.
type impactSelectionRequest struct {
	Selected *bool `json:"selected"`
}

// SetSelected PATCH /api/versions/{versionId}/code/analyses/{analysisId}/candidates/{candidateId}.
func (h *ImpactHandler) SetSelected(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	analysisID, err := pathID(r, "analysisId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	candidateID, err := pathID(r, "candidateId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request impactSelectionRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	if request.Selected == nil {
		writeError(w, r, platform.Invalid("selected", "is required"))
		return
	}
	analysisVersion, err := h.impact.AnalysisVersion(r.Context(), analysisID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if analysisVersion != versionID {
		writeError(w, r, platform.NotFound("code analysis"))
		return
	}
	updated, err := h.impact.SetSelected(r.Context(), analysisID, candidateID, actorID, *request.Selected)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toSingleCandidateDTO(updated))
}
