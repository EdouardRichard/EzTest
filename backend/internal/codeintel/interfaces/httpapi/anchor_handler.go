package codeintelapi

import (
	"context"
	"net/http"
	"strings"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// AnchorServicePort is the application surface the anchor handler needs.
type AnchorServicePort interface {
	Create(ctx context.Context, in codeintelapp.CreateAnchorInput) (codeintel.Anchor, error)
	List(ctx context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error)
	Validate(ctx context.Context, versionID, anchorID int64) (codeintel.Anchor, error)
	Delete(ctx context.Context, versionID, anchorID int64) error
}

// AnchorHandler serves the four code-anchor operations (spec §4.2 #18–#21). The
// assembly root mounts Register behind the authenticated middleware.
type AnchorHandler struct {
	anchors AnchorServicePort
}

// NewAnchorHandler builds the code-anchor handler.
func NewAnchorHandler(anchors AnchorServicePort) *AnchorHandler {
	return &AnchorHandler{anchors: anchors}
}

// Register mounts the code-anchor routes.
func (h *AnchorHandler) Register(mux *http.ServeMux, authenticated func(http.Handler) http.Handler) {
	mux.Handle("GET /api/versions/{versionId}/code/anchors", authenticated(http.HandlerFunc(h.List)))
	mux.Handle("POST /api/versions/{versionId}/code/anchors", authenticated(http.HandlerFunc(h.Create)))
	mux.Handle("POST /api/versions/{versionId}/code/anchors/{anchorId}/validate", authenticated(http.HandlerFunc(h.Validate)))
	mux.Handle("DELETE /api/versions/{versionId}/code/anchors/{anchorId}", authenticated(http.HandlerFunc(h.DeleteAnchor)))
}

// codeAnchorDTO is CodeAnchorView. IDs are decimal strings and the excerpt hash
// is the server-recomputed sha256; the API never returns file content.
type codeAnchorDTO struct {
	ID              string     `json:"id"`
	VersionID       string     `json:"version_id"`
	RepoID          string     `json:"repo_id"`
	SubjectKind     string     `json:"subject_kind"`
	SubjectID       string     `json:"subject_id"`
	FilePath        string     `json:"file_path"`
	StartLine       *int       `json:"start_line"`
	EndLine         *int       `json:"end_line"`
	EntityID        *string    `json:"entity_id"`
	CommitSHA       *string    `json:"commit_sha"`
	ExcerptSHA      string     `json:"excerpt_sha"`
	Status          string     `json:"status"`
	Confidence      string     `json:"confidence"`
	CreatedByKind   string     `json:"created_by_kind"`
	CreatedBy       *string    `json:"created_by"`
	LastValidatedAt *time.Time `json:"last_validated_at"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

func toAnchorDTO(anchor codeintel.Anchor) codeAnchorDTO {
	view := codeAnchorDTO{
		ID:              platformid.Format(anchor.ID),
		VersionID:       platformid.Format(anchor.VersionID),
		RepoID:          platformid.Format(anchor.RepoID),
		SubjectKind:     string(anchor.SubjectKind),
		SubjectID:       platformid.Format(anchor.SubjectID),
		FilePath:        anchor.FilePath,
		StartLine:       anchor.StartLine,
		EndLine:         anchor.EndLine,
		ExcerptSHA:      anchor.ExcerptSHA,
		Status:          anchor.Status,
		Confidence:      anchor.Confidence,
		CreatedByKind:   anchor.CreatedByKind,
		LastValidatedAt: anchor.LastValidatedAt,
		CreatedAt:       anchor.CreatedAt,
		UpdatedAt:       anchor.UpdatedAt,
	}
	if anchor.EntityID != nil {
		view.EntityID = stringPtr(platformid.Format(*anchor.EntityID))
	}
	if anchor.CommitSHA != "" {
		view.CommitSHA = stringPtr(anchor.CommitSHA)
	}
	if anchor.CreatedBy != nil {
		view.CreatedBy = stringPtr(platformid.Format(*anchor.CreatedBy))
	}
	return view
}

// codeAnchorCreateRequest is the POST payload (camelCase, IDs as decimal
// strings; line numbers are optional integers).
type codeAnchorCreateRequest struct {
	SubjectKind string `json:"subjectKind"`
	SubjectID   string `json:"subjectId"`
	RepoID      string `json:"repoId"`
	FilePath    string `json:"filePath"`
	StartLine   *int   `json:"startLine"`
	EndLine     *int   `json:"endLine"`
	ExcerptSHA  string `json:"excerptSha"`
	CommitSHA   string `json:"commit"`
}

// List GET /api/versions/{versionId}/code/anchors.
func (h *AnchorHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	subjectID, err := queryIDOptional(r, "subjectId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	anchors, err := h.anchors.List(r.Context(), versionID, strings.TrimSpace(r.URL.Query().Get("subjectKind")), subjectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	items := make([]codeAnchorDTO, 0, len(anchors))
	for _, anchor := range anchors {
		items = append(items, toAnchorDTO(anchor))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// Create POST /api/versions/{versionId}/code/anchors.
func (h *AnchorHandler) Create(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	var request codeAnchorCreateRequest
	if err := decodeBody(w, r, &request); err != nil {
		writeError(w, r, err)
		return
	}
	subjectKind := strings.TrimSpace(request.SubjectKind)
	if subjectKind == "" {
		writeError(w, r, platform.Invalid("subjectKind", "is required"))
		return
	}
	subjectID, err := validation.NonEmptyID("subjectId", request.SubjectID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	repoID, err := validation.NonEmptyID("repoId", request.RepoID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	filePath := strings.TrimSpace(request.FilePath)
	if filePath == "" {
		writeError(w, r, platform.Invalid("filePath", "is required"))
		return
	}
	commit := strings.TrimSpace(request.CommitSHA)
	if commit != "" && !codeintelapp.ValidCommitSHA(commit) {
		writeError(w, r, platform.Invalid("commit", "must be a 40-character lowercase hex commit SHA"))
		return
	}
	anchor, err := h.anchors.Create(r.Context(), codeintelapp.CreateAnchorInput{
		VersionID:   versionID,
		SubjectKind: codeintel.AnchorSubject(subjectKind),
		SubjectID:   subjectID,
		RepoID:      repoID,
		FilePath:    filePath,
		StartLine:   request.StartLine,
		EndLine:     request.EndLine,
		ExcerptSHA:  strings.TrimSpace(request.ExcerptSHA),
		CommitSHA:   commit,
	})
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toAnchorDTO(anchor))
}

// Validate POST /api/versions/{versionId}/code/anchors/{anchorId}/validate.
func (h *AnchorHandler) Validate(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	anchorID, err := pathID(r, "anchorId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	anchor, err := h.anchors.Validate(r.Context(), versionID, anchorID)
	if err != nil {
		writeError(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toAnchorDTO(anchor))
}

// DeleteAnchor DELETE /api/versions/{versionId}/code/anchors/{anchorId}.
func (h *AnchorHandler) DeleteAnchor(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	anchorID, err := pathID(r, "anchorId")
	if err != nil {
		writeError(w, r, err)
		return
	}
	if err := h.anchors.Delete(r.Context(), versionID, anchorID); err != nil {
		writeError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
