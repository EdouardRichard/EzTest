package codeintelapi

import (
	"net/http"
	"strings"

	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// codeIndexRequest is CodeIndexRequest.
type codeIndexRequest struct {
	RepoID string `json:"repoId"`
	Commit string `json:"commit"`
	Branch string `json:"branch"`
}

// StartIndex POST /api/versions/{versionId}/code/index (op 1).
func (h *Handler) StartIndex(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request codeIndexRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := validation.NonEmptyID("repoId", request.RepoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	commit := strings.TrimSpace(request.Commit)
	if commit != "" && !codeintelappValidCommit(commit) {
		writeError(w, r, platform.Invalid("commit", "must be a 40-character lowercase hex commit SHA"))
		return
	}
	run, created, err := h.index.Start(r.Context(), versionID, repoID, commit)
	if err != nil {
		writeError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if !created {
		status = http.StatusOK
	}
	httpx.JSON(w, status, toIndexRunDTO(run))
}

// ListRepos GET /api/versions/{versionId}/code/repos (op 2).
func (h *Handler) ListRepos(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	statuses, err := h.index.RepoStatus(r.Context(), versionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeRepoStatusDTO, 0, len(statuses))
	for _, status := range statuses {
		items = append(items, toRepoStatusDTO(status))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// LatestIndexRun GET /api/code/repos/{repoId}/index-runs/latest (op 3).
func (h *Handler) LatestIndexRun(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	run, err := h.index.Latest(r.Context(), repoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toIndexRunDTO(run))
}

// GetIndexRun GET /api/code/repos/{repoId}/index-runs/{runId} (op 4).
func (h *Handler) GetIndexRun(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	runID, err := pathID(r, "runId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	run, err := h.index.Get(r.Context(), runID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	if run.RepoID != repoID {
		writeError(w, r, platform.NotFound("code index run"))
		return
	}
	httpx.JSON(w, http.StatusOK, toIndexRunDTO(run))
}

// Tree GET /api/code/repos/{repoId}/tree (op 5).
func (h *Handler) Tree(w http.ResponseWriter, r *http.Request) {
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
	pathFilter := strings.TrimSpace(r.URL.Query().Get("path"))
	entities, cursor, hasMore, err := h.index.Tree(r.Context(), repoID, commit, pathFilter,
		strings.TrimSpace(r.URL.Query().Get("cursor")), listLimit(r, 50, 200))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeTreeEntryDTO, 0, len(entities))
	for _, entity := range entities {
		items = append(items, toTreeEntryDTO(entity))
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

// File GET /api/code/repos/{repoId}/file (op 6).
func (h *Handler) File(w http.ResponseWriter, r *http.Request) {
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
	filePath, err := requiredQuery(r, "path")
	if err != nil {
		writeError(w, r, err)
		return
	}
	start, err := optionalInt(r, "startLine")
	if err != nil {
		writeError(w, r, err)
		return
	}
	end, err := optionalInt(r, "endLine")
	if err != nil {
		writeError(w, r, err)
		return
	}
	window, err := h.query.File(r.Context(), repoID, commit, filePath, start, end)
	if err != nil {
		if binaryFileCode(err) {
			binary := codeintel.FileWindow{StartLine: 1, EndLine: 1}
			httpx.JSON(w, http.StatusOK, newFileWindowDTO(repoID, commit, filePath, binary, true, ""))
			return
		}
		writeError(w, r, err)
		return
	}
	content := strings.Join(window.Lines, "\n")
	httpx.JSON(w, http.StatusOK, newFileWindowDTO(repoID, commit, window.Path, window, false, content))
}

// Diff GET /api/code/repos/{repoId}/diff (op 7).
func (h *Handler) Diff(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	base, err := commitQuery(r, "base")
	if err != nil {
		writeError(w, r, err)
		return
	}
	head, err := commitQuery(r, "head")
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, cursor, hasMore, err := h.query.Diff(r.Context(), repoID, base, head,
		strings.TrimSpace(r.URL.Query().Get("path")),
		strings.TrimSpace(r.URL.Query().Get("cursor")), listLimit(r, 200, 2000))
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeChangedFileDTO, 0, len(result.Files))
	for _, file := range result.Files {
		items = append(items, toChangedFileDTO(file))
	}
	httpx.List(w, http.StatusOK, items, cursor, hasMore)
}

// FileDiff GET /api/code/repos/{repoId}/diff/file (op 8).
func (h *Handler) FileDiff(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	base, err := commitQuery(r, "base")
	if err != nil {
		writeError(w, r, err)
		return
	}
	head, err := commitQuery(r, "head")
	if err != nil {
		writeError(w, r, err)
		return
	}
	filePath, err := requiredQuery(r, "path")
	if err != nil {
		writeError(w, r, err)
		return
	}
	fileDiff, err := h.query.FileDiff(r.Context(), repoID, base, head, filePath)
	if err != nil {
		writeError(w, r, err)
		return
	}
	result, _, _, err := h.query.Diff(r.Context(), repoID, base, head, fileDiff.Path, "", 10)
	if err != nil {
		writeError(w, r, err)
		return
	}
	var changed *codeintel.ChangedFile
	for index := range result.Files {
		if result.Files[index].Path == fileDiff.Path {
			changed = &result.Files[index]
			break
		}
	}
	if changed == nil {
		writeError(w, r, platform.NotFound("changed file"))
		return
	}
	httpx.JSON(w, http.StatusOK, toFileDiffDTO(fileDiff, *changed))
}

// Commit GET /api/code/repos/{repoId}/commits/{sha} (op 11).
func (h *Handler) Commit(w http.ResponseWriter, r *http.Request) {
	repoID, err := pathID(r, "repoId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	sha := strings.TrimSpace(r.PathValue("sha"))
	if !codeintelappValidCommit(sha) {
		writeError(w, r, platform.Invalid("sha", "must be a 40-character lowercase hex commit SHA"))
		return
	}
	view, err := h.query.Commit(r.Context(), repoID, sha)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toCommitDTO(view))
}
