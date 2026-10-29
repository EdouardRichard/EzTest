package httpapi

import (
	"errors"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// ListRepos returns every configured P2 code repository with its latest index
// run projection (design spec §7.2). A repository without any run is not an
// error: its index is null. P2 repository order (id ascending) is preserved; the
// latest-run lookup is one application call per repository.
func (h *Handler) ListRepos(w http.ResponseWriter, r *http.Request) {
	repositories, err := h.repos.List(r.Context())
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	summaries := make([]RepoSummaryDTO, 0, len(repositories))
	for _, repository := range repositories {
		summary := newRepoSummaryDTO(repository)
		run, err := h.index.GetLatest(r.Context(), repository.ID)
		switch {
		case err == nil:
			dto := newIndexRunDTO(run)
			summary.Index = &dto
			// RepoInfo exposes no P2 last_commit column, so the newest index run
			// commit is the freshest live commit this read can project.
			summary.LastCommit = run.CommitSHA
		case errors.Is(err, domain.ErrNotFound):
			// Never indexed: index stays null and the whole list still succeeds.
		default:
			WriteServiceError(w, r, err)
			return
		}
		summaries = append(summaries, summary)
	}
	WriteData(w, http.StatusOK, summaries)
}

// StartIndex triggers a T0 index run of the repository's fixed branch tip
// (admin only; design spec §6.1/§6.10). The run is persisted queued and the
// worker is enqueued by the application service, so the handler answers 202 with
// the run identity.
func (h *Handler) StartIndex(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	run, err := h.index.Start(r.Context(), repoID, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusAccepted, IndexRunAcceptedDTO{
		RunID:  formatID(run.ID),
		Status: run.Status,
		Commit: run.CommitSHA,
	})
}

// LatestIndexRun returns the newest index run of the repository or the
// documented 404 index_run_not_found.
func (h *Handler) LatestIndexRun(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	run, err := h.index.GetLatest(r.Context(), repoID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newIndexRunDTO(run))
}

// GetIndexRun returns one run of the repository. The repository scoping is part
// of the application call, so a run of another repository is a plain 404.
func (h *Handler) GetIndexRun(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	runID, err := PathID(r, "runId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	run, err := h.index.Get(r.Context(), repoID, runID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newIndexRunDTO(run))
}
