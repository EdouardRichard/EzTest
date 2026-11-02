package codeintelapi

import (
	"net/http"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// codeAnalysisRequest is CodeAnalysisRequest.
type codeAnalysisRequest struct {
	RepoID     string `json:"repoId"`
	HeadCommit string `json:"headCommit"`
	Force      bool   `json:"force"`
}

// Changes GET /api/versions/{versionId}/code/changes (op 12).
func (h *Handler) Changes(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := queryIDRequired(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	head, err := optionalCommitQuery(r, "head")
	if err != nil {
		writeError(w, r, err)
		return
	}
	changeSet, err := h.analysis.Changes(r.Context(), versionID, repoID, head)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toChangeSetDTO(changeSet))
}

// CreateAnalysis POST /api/versions/{versionId}/code/analyses (op 13).
func (h *Handler) CreateAnalysis(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request codeAnalysisRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := validation.NonEmptyID("repoId", request.RepoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	head := strings.TrimSpace(request.HeadCommit)
	if head != "" && !codeintelappValidCommit(head) {
		writeError(w, r, platform.Invalid("headCommit", "must be a 40-character lowercase hex commit SHA"))
		return
	}
	analysis, created, err := h.analysis.Start(r.Context(), versionID, repoID, head, request.Force)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	httpx.JSON(w, status, toAnalysisDTO(analysis))
}

// LatestAnalysis GET /api/versions/{versionId}/code/analyses/latest (op 14).
func (h *Handler) LatestAnalysis(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := queryIDRequired(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	analysis, err := h.analysis.Latest(r.Context(), versionID, repoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAnalysisDTO(analysis))
}

// GetAnalysis GET /api/versions/{versionId}/code/analyses/{analysisId} (op 15).
func (h *Handler) GetAnalysis(w http.ResponseWriter, r *http.Request) {
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
	analysis, err := h.analysis.Get(r.Context(), analysisID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if analysis.VersionID != versionID {
		writeError(w, r, platform.NotFound("code analysis"))
		return
	}
	if h.impact != nil {
		detailed, views, detailErr := h.impact.Detail(r.Context(), analysisID)
		if detailErr != nil {
			writeError(w, r, detailErr)
			return
		}
		httpx.JSON(w, http.StatusOK, toAnalysisDetailDTOWithCandidates(detailed, views))
		return
	}
	httpx.JSON(w, http.StatusOK, toAnalysisDetailDTO(analysis))
}

// Facts GET /api/code/repos/{repoId}/facts (op 9).
func (h *Handler) Facts(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	commit, err := commitQuery(r, "commit")
	if err != nil {
		writeError(w, r, err)
		return
	}
	facts, cursor, hasMore, err := h.analysis.Facts(r.Context(), repoID, commit,
		strings.TrimSpace(r.URL.Query().Get("kind")),
		strings.TrimSpace(r.URL.Query().Get("path")),
		strings.TrimSpace(r.URL.Query().Get("cursor")), listLimit(r, 50, 100))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeFactDTO, 0, len(facts))
	for _, fact := range facts {
		items = append(items, toFactDTO(fact))
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

// Search GET /api/code/repos/{repoId}/search (op 10).
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	commit, err := commitQuery(r, "commit")
	if err != nil {
		writeError(w, r, err)
		return
	}
	query, err := requiredQuery(r, "q")
	if err != nil {
		writeError(w, r, err)
		return
	}
	entities, err := h.analysis.Search(r.Context(), repoID, commit, query,
		strings.TrimSpace(r.URL.Query().Get("kind")), listLimit(r, 20, 50))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeEntityDTO, 0, len(entities))
	for _, entity := range entities {
		items = append(items, toEntityDTO(entity))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// codeintelappValidCommit keeps the commit-shape check in one place.
func codeintelappValidCommit(value string) bool {
	return codeintelapp.ValidCommitSHA(value)
}
