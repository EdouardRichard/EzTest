package codeintelapi

import (
	"context"
	"net/http"
	"strings"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// ProposalServicePort is the application surface the proposal handler needs.
type ProposalServicePort interface {
	Generate(ctx context.Context, versionID, repoID int64) (codeintelapp.ProposalResult, error)
	List(ctx context.Context, versionID int64) ([]codeintelapp.ProposalView, error)
}

// ProposalHandler serves the page/API proposal operations (spec §4.2 #24–#25).
// Generation requires a logged-in user; confirm/reject belongs to the resource
// handler and is admin-only.
type ProposalHandler struct {
	proposals ProposalServicePort
}

// NewProposalHandler builds the proposal handler.
func NewProposalHandler(proposals ProposalServicePort) *ProposalHandler {
	return &ProposalHandler{proposals: proposals}
}

// Register mounts the proposal routes behind the authenticated middleware.
func (h *ProposalHandler) Register(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	mux.Handle("POST /api/versions/{versionId}/code/page-api-proposals", authenticated(http.HandlerFunc(h.Generate)))
	mux.Handle("GET /api/versions/{versionId}/code/page-api-proposals", authenticated(http.HandlerFunc(h.List)))
}

// proposalCountsDTO is one pages/apis count pair.
type proposalCountsDTO struct {
	Created int `json:"created"`
	Skipped int `json:"skipped"`
}

// codeProposalResultDTO is CodeProposalResultView.
type codeProposalResultDTO struct {
	Pages proposalCountsDTO `json:"pages"`
	APIs  proposalCountsDTO `json:"apis"`
}

// codeProposalDTO is CodeProposalView.
type codeProposalDTO struct {
	Kind          string `json:"kind"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	RouteTemplate string `json:"route_template"`
	Method        string `json:"method"`
	PathTemplate  string `json:"path_template"`
	Source        string `json:"source"`
	Status        string `json:"status"`
}

// proposalGenerateRequest is the POST payload; repoId is optional.
type proposalGenerateRequest struct {
	RepoID string `json:"repoId"`
}

// Generate POST /api/versions/{versionId}/code/page-api-proposals.
func (h *ProposalHandler) Generate(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request proposalGenerateRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	repoID := int64(0)
	if raw := strings.TrimSpace(request.RepoID); raw != "" {
		parsed, parseErr := validation.NonEmptyID("repoId", raw)
		if parseErr != nil {
			writeError(w, r, parseErr)
			return
		}
		repoID = parsed
	}
	result, err := h.proposals.Generate(r.Context(), versionID, repoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, codeProposalResultDTO{
		Pages: proposalCountsDTO{Created: result.PagesCreated, Skipped: result.PagesSkipped},
		APIs:  proposalCountsDTO{Created: result.APIsCreated, Skipped: result.APISkipped},
	})
}

// List GET /api/versions/{versionId}/code/page-api-proposals.
func (h *ProposalHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	views, err := h.proposals.List(r.Context(), versionID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeProposalDTO, 0, len(views))
	for _, view := range views {
		items = append(items, codeProposalDTO{
			Kind:          view.Kind,
			ID:            platformid.Format(view.ID),
			Name:          view.Name,
			URL:           view.URL,
			RouteTemplate: view.RouteTemplate,
			Method:        view.Method,
			PathTemplate:  view.PathTemplate,
			Source:        view.Source,
			Status:        view.Status,
		})
	}
	httpx.List(w, http.StatusOK, items, "", false)
}
