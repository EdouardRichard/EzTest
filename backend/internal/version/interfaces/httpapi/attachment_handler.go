package versionapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// AttachmentImageService is the application surface the attachment image
// routes need. *versionapp.AttachmentService implements it.
type AttachmentImageService interface {
	ListImages(ctx context.Context, attachmentID int64) ([]versionapp.AttachmentImageView, error)
	// Reanalyze re-queues the attachment's failed/skipped images and re-runs
	// vision analysis without duplicating rows or content objects.
	Reanalyze(ctx context.Context, attachmentID int64) ([]versionapp.AttachmentImageView, error)
}

// AttachmentVersionResolver is the ownership check behind the H7 version-scoped
// image routes: it resolves one attachment inside its version, so an
// authenticated user can never read or reanalyze another version's attachment
// by guessing an ID. *versionapp.AttachmentService implements it.
type AttachmentVersionResolver interface {
	Get(ctx context.Context, versionID, attachmentID int64) (versionapp.AttachmentView, error)
}

// AttachmentHandler serves the attachment observation routes. Routes are
// mounted by the composition root (Task 37); the handler only parses path IDs,
// maps DTOs and delegates to the application service.
type AttachmentHandler struct {
	svc       AttachmentImageService
	versioner AttachmentVersionResolver
}

// NewAttachmentHandler builds the attachment handler.
func NewAttachmentHandler(svc AttachmentImageService) *AttachmentHandler {
	return &AttachmentHandler{svc: svc}
}

// SetVersionResolver wires the version/attachment ownership check used by the
// H7 scoped routes. Without it the scoped routes answer 503 instead of exposing
// an unscoped read.
func (h *AttachmentHandler) SetVersionResolver(resolver AttachmentVersionResolver) {
	h.versioner = resolver
}

var _ AttachmentImageService = (*versionapp.AttachmentService)(nil)

// ListImagesForVersion GET /api/versions/{id}/attachments/{attachmentId}/images.
//
// H7: the attachment is resolved inside the addressed version first, so the
// route is version-scoped and authorized instead of reading any attachment by
// raw ID. The response is the same image list as the legacy read.
func (h *AttachmentHandler) ListImagesForVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, attachmentID, ok := attachmentScopeIDs(w, r)
	if !ok {
		return
	}
	if h.svc == nil || h.versioner == nil {
		httpx.Error(w, r, errors.New("attachment image service is not configured"))
		return
	}
	if _, err := h.versioner.Get(r.Context(), versionID, attachmentID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.ListImages(r.Context(), attachmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	h.writeImages(w, views)
}

// ReanalyzeForVersion POST /api/versions/{id}/attachments/{attachmentId}/reanalyze.
//
// H7: version/attachment ownership is verified before any vision work, and the
// legacy unscoped route is not mounted. Idempotent like the underlying service.
func (h *AttachmentHandler) ReanalyzeForVersion(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	versionID, attachmentID, ok := attachmentScopeIDs(w, r)
	if !ok {
		return
	}
	if h.svc == nil || h.versioner == nil {
		httpx.Error(w, r, errors.New("attachment image service is not configured"))
		return
	}
	if _, err := h.versioner.Get(r.Context(), versionID, attachmentID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, err := h.svc.Reanalyze(r.Context(), attachmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	h.writeImages(w, views)
}

// attachmentScopeIDs parses the version/attachment path pair of the H7 routes.
func attachmentScopeIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	versionID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return 0, 0, false
	}
	attachmentID, err := validation.NonEmptyID("attachmentId", r.PathValue("attachmentId"))
	if err != nil {
		httpx.Error(w, r, err)
		return 0, 0, false
	}
	return versionID, attachmentID, true
}

// ListImages GET /api/attachments/{id}/images.
//
// One row per extracted image with its MIME/dimensions/source kind/status and
// the caption or failure reason; this is the R-E3-2/R-E3-6 visibility contract
// used by the message chip and the attachment detail preview. The original
// bytes are reachable through the content endpoint, never in this JSON.
func (h *AttachmentHandler) ListImages(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	attachmentID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.svc == nil {
		httpx.Error(w, r, errors.New("attachment image service is not configured"))
		return
	}
	views, err := h.svc.ListImages(r.Context(), attachmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	h.writeImages(w, views)
}

// Reanalyze POST /api/attachments/{id}/reanalyze.
//
// Idempotent: only failed/skipped image rows are reset and analyzed again; the
// existing content references are reused. The response is the refreshed image
// list so the caller renders the final status/captions without a second read.
func (h *AttachmentHandler) Reanalyze(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireActor(w, r); !ok {
		return
	}
	attachmentID, err := validation.NonEmptyID("id", r.PathValue("id"))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.svc == nil {
		httpx.Error(w, r, errors.New("attachment image service is not configured"))
		return
	}
	views, err := h.svc.Reanalyze(r.Context(), attachmentID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	h.writeImages(w, views)
}

func (h *AttachmentHandler) writeImages(w http.ResponseWriter, views []versionapp.AttachmentImageView) {
	items := make([]attachmentImageDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toAttachmentImageDTO(view))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// attachmentImageDTO is the wire shape of one attachment_image row. 64-bit IDs
// are decimal strings (Agent.md §6.7); URL is the authenticated content
// endpoint the UI can use for preview and never an internal storage path.
type attachmentImageDTO struct {
	ID           string    `json:"id"`
	AttachmentID string    `json:"attachment_id"`
	Seq          int       `json:"seq"`
	ContentRefID string    `json:"content_ref_id"`
	SHA256       string    `json:"sha256"`
	URL          string    `json:"url,omitempty"`
	MIME         string    `json:"mime"`
	Width        int       `json:"width,omitempty"`
	Height       int       `json:"height,omitempty"`
	SourceKind   string    `json:"source_kind"`
	Status       string    `json:"status"`
	Caption      string    `json:"caption"`
	Error        string    `json:"error"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func toAttachmentImageDTO(view versionapp.AttachmentImageView) attachmentImageDTO {
	url := ""
	if view.SHA256 != "" {
		url = "/api/content/" + view.SHA256
	}
	return attachmentImageDTO{
		ID:           platformid.Format(view.ID),
		AttachmentID: platformid.Format(view.AttachmentID),
		Seq:          view.Seq,
		ContentRefID: platformid.Format(view.ContentRefID),
		SHA256:       view.SHA256,
		URL:          url,
		MIME:         view.MIME,
		Width:        view.Width,
		Height:       view.Height,
		SourceKind:   view.SourceKind,
		Status:       view.Status,
		Caption:      view.Caption,
		Error:        view.Error,
		CreatedAt:    view.CreatedAt,
		UpdatedAt:    view.UpdatedAt,
	}
}
