package httpapi

import (
	"net/http"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Tree returns one cursor page of the commit-pinned file tree, live from Git
// (design spec §7.2). An empty commit resolves the repository's fixed branch
// tip inside the application service; an empty path means the repository root.
func (h *Handler) Tree(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	cursor, err := pathCursor(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	query := r.URL.Query()
	page, err := h.query.Tree(r.Context(), repoID, strings.TrimSpace(query.Get("path")),
		strings.TrimSpace(query.Get("commit")), cursor, limit)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WritePage(w, newTreePageDTO(page), page.NextCursor, page.HasMore)
}

// File returns one 1-based line window of a commit-pinned text file and records
// the code.read audit entry inside the application service (design spec §6.10).
// The service clamps start/end and flags a partial window as truncated, so a
// bounded window is still a 200. The authenticated actor is forwarded for the
// audit entry; the response never carries anything but the requested window.
func (h *Handler) File(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requestedPath, err := requiredQuery(r, "path")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	startLine, err := ParseOptionalPositiveInt(r, "startLine")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	endLine, err := ParseOptionalPositiveInt(r, "endLine")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	window, err := h.query.File(r.Context(), ActorID(r.Context()), repoID,
		strings.TrimSpace(r.URL.Query().Get("commit")), requestedPath, startLine, endLine)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newFileWindowDTO(window))
}

// Diff returns one cursor page of the deterministic file-level change list
// (design spec §7.2). The change set is a read-only preview here: nothing is
// persisted, so changeSetId is null (R7). Both base and head are required
// because the ChangeList reports the resolved commit pair and the preview has no
// way to echo a resolved head when it is omitted.
func (h *Handler) Diff(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	base, err := requiredQuery(r, "base")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	head, err := requiredQuery(r, "head")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	cursor, err := pathCursor(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	data, err := h.query.Diff(r.Context(), repoID, base, head, limit, cursor)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	dto, err := newChangeListDTO(data, base, head, limit)
	if err != nil {
		writeInternalError(w, r)
		return
	}
	WriteData(w, http.StatusOK, dto)
}

// FileDiff returns the on-demand unified diff of one changed file. Binary
// files, deleted files and oversized patches are a 200 with an empty patch and
// the matching flag (R18); a path outside the change set is the 422 not_changed
// verdict. base is required; head defaults inside the application service to the
// fixed branch tip and the resolved pair is echoed in the DTO.
func (h *Handler) FileDiff(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	base, err := requiredQuery(r, "base")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	requestedPath, err := requiredQuery(r, "path")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	diff, err := h.query.FileDiff(r.Context(), repoID, base,
		strings.TrimSpace(r.URL.Query().Get("head")), requestedPath)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newFileDiffDTO(diff))
}

// Facts returns one page of persisted code entities at a commit (T0 file facts
// plus AI facts). kind and path are filters; the T11 FactsPanel queries one kind
// at a time with limit=50, which this handler forwards unchanged (the application
// service applies its own hard page cap).
func (h *Handler) Facts(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	cursor, err := opaqueCursor(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	query := r.URL.Query()
	entities, nextCursor, hasMore, err := h.query.Facts(r.Context(), repoID, domain.EntityFilter{
		Commit: strings.TrimSpace(query.Get("commit")),
		Kind:   strings.TrimSpace(query.Get("kind")),
		Path:   strings.TrimSpace(query.Get("path")),
		Limit:  limit,
		Cursor: cursor,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	// The human review marker is additive: a review-store failure must never
	// fail the facts page itself (the panel still renders the facts).
	var reviews map[int64]domain.FactReview
	if h.factReviews != nil && len(entities) > 0 {
		if loaded, loadErr := h.factReviews.ReviewsForFacts(r.Context(), repoID, entities); loadErr == nil {
			reviews = loaded
		}
	}
	WritePage(w, newFactsPageDTO(entities, reviews), nextCursor, hasMore)
}

// Search returns one page of entities whose path or name contains q: a
// path/name substring search, never full text (design spec §7.2, D24). q is
// required: a missing, empty or whitespace-only q is a 400 invalid_request
// instead of a query that matches every entity (ruling R42), and the trimmed q
// is what reaches the application service. The application service records the
// read audit for the authenticated actor (R32).
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	q, err := requiredQuery(r, "q")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	limit, err := ParseQueryLimit(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	cursor, err := opaqueCursor(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	query := r.URL.Query()
	entities, nextCursor, hasMore, err := h.query.Search(r.Context(), ActorID(r.Context()), repoID,
		strings.TrimSpace(query.Get("commit")), q,
		strings.TrimSpace(query.Get("kind")), limit, cursor)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WritePage(w, newFactsPageDTO(entities, nil), nextCursor, hasMore)
}

// Commit returns the read-only metadata of one commit. The application service
// validates the 40-hex form (422 invalid_head, ruling R19 handoff) and surfaces
// an unknown commit as 422 commit_not_found.
func (h *Handler) Commit(w http.ResponseWriter, r *http.Request) {
	repoID, err := PathID(r, "repoId")
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	info, err := h.query.Commit(r.Context(), repoID, r.PathValue("sha"))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newCommitDTO(info))
}
