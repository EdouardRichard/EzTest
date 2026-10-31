// Package storageapi exposes content upload and verified download over HTTP.
package storageapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	storageapp "github.com/EdouardRichard/EzTest/backend/internal/storage/application"
	"github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

// maxMultipartOverhead leaves room for the multipart envelope around the
// 10 MiB file field; a larger request body is rejected before parsing.
const maxMultipartOverhead = 1 << 20

// Service is the application surface the content handler needs.
type Service interface {
	Upload(ctx context.Context, filename, mime string, r io.Reader, maxBytes int64, actorID int64) (storage.Object, error)
	Open(ctx context.Context, sha256 string) (storage.Object, io.ReadCloser, error)
}

// Handler serves POST /api/content and GET /api/content/{sha256}. The server
// mounts it behind bearer authentication.
type Handler struct {
	svc Service
}

// New builds the handler.
func New(svc Service) *Handler {
	return &Handler{svc: svc}
}

// Upload POST /api/content multipart field "file".
func (h *Handler) Upload(w http.ResponseWriter, r *http.Request) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, storageapp.MaxUploadBytes+maxMultipartOverhead)
	file, header, err := r.FormFile("file")
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			httpx.Error(w, r, platform.NewError(http.StatusRequestEntityTooLarge, "payload_too_large", "content exceeds the 10 MiB limit"))
			return
		}
		httpx.Error(w, r, platform.Invalid("file", "multipart field \"file\" is required"))
		return
	}
	defer file.Close()

	object, err := h.svc.Upload(r.Context(), header.Filename, header.Header.Get("Content-Type"), file, storageapp.MaxUploadBytes, actor.ID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toObjectDTO(object))
}

// Download GET /api/content/{sha256}.
func (h *Handler) Download(w http.ResponseWriter, r *http.Request) {
	if _, ok := platform.ActorFrom(r.Context()); !ok {
		httpx.Error(w, r, platform.Unauthenticated())
		return
	}
	sha := r.PathValue("sha256")
	if !storageapp.ValidSHA256(sha) {
		httpx.Error(w, r, platform.Invalid("sha256", "sha256 must be 64 lowercase hex characters"))
		return
	}
	object, reader, err := h.svc.Open(r.Context(), sha)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", object.MIME)
	w.Header().Set("X-Content-SHA256", object.SHA256)
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.Header().Set("Content-Length", strconv.FormatInt(object.Size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}
