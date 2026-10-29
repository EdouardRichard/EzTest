package httpapi

import (
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxCodeRepoBodyBytes caps code repository request bodies (spec §7).
const maxCodeRepoBodyBytes int64 = 64 << 10

// CodeRepoHandler serves the local code repository endpoints (spec §7.7). Read
// routes are mounted plain (the global auth middleware still requires a login);
// writes and the validate action are wrapped by the caller's admin guard.
type CodeRepoHandler struct {
	svc *application.CodeRepoService
}

// NewCodeRepoHandler returns the code repository handlers for svc.
func NewCodeRepoHandler(svc *application.CodeRepoService) *CodeRepoHandler {
	return &CodeRepoHandler{svc: svc}
}

// RegisterRoutes mounts the code repository endpoints on mux. guard wraps every
// write and action route; the server passes an admin guard built from
// RequireAdmin.
func (h *CodeRepoHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}

	mux.HandleFunc("GET /api/resources/code-repos", h.ListCodeRepos)
	mux.HandleFunc("GET /api/resources/code-repos/{id}", h.GetCodeRepo)

	mux.Handle("POST /api/resources/code-repos", guard(http.HandlerFunc(h.CreateCodeRepo)))
	mux.Handle("PATCH /api/resources/code-repos/{id}", guard(http.HandlerFunc(h.UpdateCodeRepo)))
	mux.Handle("DELETE /api/resources/code-repos/{id}", guard(http.HandlerFunc(h.DeleteCodeRepo)))
	mux.Handle("POST /api/resources/code-repos/{id}/validate", guard(http.HandlerFunc(h.ValidateCodeRepo)))
}

// codeRepoDTO is the public CodeRepository representation (camelCase, spec
// §7.7). lastIndexedAt stays null until a later phase indexes the checkout.
type codeRepoDTO struct {
	ID                  int64      `json:"id"`
	Key                 string     `json:"key"`
	Name                string     `json:"name"`
	LocalPath           string     `json:"localPath"`
	FixedBranch         string     `json:"fixedBranch"`
	IncludeGlobs        []string   `json:"includeGlobs"`
	ExcludeGlobs        []string   `json:"excludeGlobs"`
	Status              string     `json:"status"`
	LastCommit          string     `json:"lastCommit"`
	LastValidatedAt     *time.Time `json:"lastValidatedAt"`
	LastValidationError string     `json:"lastValidationError"`
	LastIndexedAt       *time.Time `json:"lastIndexedAt"`
}

// newCodeRepoDTO maps a code repository to its API shape. The NOT NULL glob
// arrays are normalized so the JSON carries [] instead of null.
func newCodeRepoDTO(codeRepo domain.CodeRepository) codeRepoDTO {
	includeGlobs := codeRepo.IncludeGlobs
	if includeGlobs == nil {
		includeGlobs = []string{}
	}
	excludeGlobs := codeRepo.ExcludeGlobs
	if excludeGlobs == nil {
		excludeGlobs = []string{}
	}
	return codeRepoDTO{
		ID:                  codeRepo.ID,
		Key:                 codeRepo.Key,
		Name:                codeRepo.Name,
		LocalPath:           codeRepo.LocalPath,
		FixedBranch:         codeRepo.FixedBranch,
		IncludeGlobs:        includeGlobs,
		ExcludeGlobs:        excludeGlobs,
		Status:              codeRepo.Status,
		LastCommit:          codeRepo.LastCommit,
		LastValidatedAt:     codeRepo.LastValidatedAt,
		LastValidationError: codeRepo.LastValidationError,
		LastIndexedAt:       codeRepo.LastIndexedAt,
	}
}

// createCodeRepoRequest is the POST /code-repos body. Status is not accepted:
// new repositories always start active.
type createCodeRepoRequest struct {
	Key          string   `json:"key"`
	Name         string   `json:"name"`
	LocalPath    string   `json:"localPath"`
	FixedBranch  string   `json:"fixedBranch"`
	IncludeGlobs []string `json:"includeGlobs"`
	ExcludeGlobs []string `json:"excludeGlobs"`
}

// updateCodeRepoRequest is the PATCH /code-repos/{id} body. Nil fields are
// unchanged; key is not accepted because it is immutable after creation.
type updateCodeRepoRequest struct {
	Name         *string   `json:"name"`
	LocalPath    *string   `json:"localPath"`
	FixedBranch  *string   `json:"fixedBranch"`
	IncludeGlobs *[]string `json:"includeGlobs"`
	ExcludeGlobs *[]string `json:"excludeGlobs"`
	Status       *string   `json:"status"`
}

// ListCodeRepos returns one cursor page of code repositories.
func (h *CodeRepoHandler) ListCodeRepos(w http.ResponseWriter, r *http.Request) {
	page, err := ParsePage(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	codeRepos, hasMore, err := h.svc.List(r.Context(), domain.CodeRepoFilter{
		Status: r.URL.Query().Get("status"),
		Page:   page,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	items := make([]codeRepoDTO, len(codeRepos))
	for i, codeRepo := range codeRepos {
		items[i] = newCodeRepoDTO(codeRepo)
	}
	next := ""
	if hasMore && len(codeRepos) > 0 {
		next = platform.EncodeCursor(codeRepos[len(codeRepos)-1].ID)
	}
	WritePage(w, items, next, hasMore)
}

// GetCodeRepo returns one code repository.
func (h *CodeRepoHandler) GetCodeRepo(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	codeRepo, err := h.svc.Get(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newCodeRepoDTO(*codeRepo))
}

// CreateCodeRepo validates the checkout and stores a new code repository.
func (h *CodeRepoHandler) CreateCodeRepo(w http.ResponseWriter, r *http.Request) {
	var req createCodeRepoRequest
	if err := DecodeJSON(w, r, &req, maxCodeRepoBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	codeRepo, err := h.svc.Create(r.Context(), application.CreateCodeRepoInput{
		Key:          req.Key,
		Name:         req.Name,
		LocalPath:    req.LocalPath,
		FixedBranch:  req.FixedBranch,
		IncludeGlobs: req.IncludeGlobs,
		ExcludeGlobs: req.ExcludeGlobs,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newCodeRepoDTO(*codeRepo))
}

// UpdateCodeRepo applies a partial update to a code repository. When the
// checkout changes, the service revalidates it before writing.
func (h *CodeRepoHandler) UpdateCodeRepo(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req updateCodeRepoRequest
	if err := DecodeJSON(w, r, &req, maxCodeRepoBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	codeRepo, err := h.svc.Update(r.Context(), id, application.UpdateCodeRepoInput{
		Name:         req.Name,
		LocalPath:    req.LocalPath,
		FixedBranch:  req.FixedBranch,
		IncludeGlobs: req.IncludeGlobs,
		ExcludeGlobs: req.ExcludeGlobs,
		Status:       req.Status,
	})
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newCodeRepoDTO(*codeRepo))
}

// DeleteCodeRepo deletes a code repository. P2 registers no dependents, so the
// delete is unconditional (spec §6.7).
func (h *CodeRepoHandler) DeleteCodeRepo(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ValidateCodeRepo re-runs the read-only Git check and refreshes the commit.
func (h *CodeRepoHandler) ValidateCodeRepo(w http.ResponseWriter, r *http.Request) {
	id, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	codeRepo, err := h.svc.Validate(r.Context(), id)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newCodeRepoDTO(*codeRepo))
}
