package httpapi

import "net/http"

// CreateFactReview records one logged-in user's verdict on a fact of the path
// repository (design spec §7.2). The handler only parses the transport: the
// application validates the verdict enum/note bound, enforces the repository
// ownership (404 fact_not_found for a fact of another repository), overwrites the
// same (repo, fact, commit) row and writes the summary-only audit entry. Every
// authenticated user may review; no admin guard is applied.
func (h *Handler) CreateFactReview(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	factID, err := PathID(r, "factId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var body createFactReviewRequest
	if err := DecodeJSON(r, &body, maxRequestBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	review, err := h.factReviews.Create(r.Context(), repoID, ActorID(r.Context()), factID,
		body.Verdict, body.Reason)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newFactReviewDTO(review))
}

// RepoQuality returns the repository quality aggregate (design spec §9.2): the
// evidence pass rate, invalid facts, confidence distribution, human review
// counters and the newest evaluation's impact precision/recall. It is read-only
// and login-readable; a missing repository is the repository catalog's 422 and
// missing data sources stay null/zero and flagged.
func (h *Handler) RepoQuality(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	quality, err := h.repoQuality.Quality(r.Context(), repoID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newRepoQualityDTO(quality))
}
