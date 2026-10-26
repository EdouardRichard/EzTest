// Package httpapi is the object-storage HTTP interface: the admin-only upload
// and content-addressed download endpoints. It depends only on the application
// use case; the server composition root mounts the routes behind the global
// bearer auth and the per-route admin guard.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	storage "github.com/EdouardRichard/EzTest/backend/internal/storage/domain"
)

const (
	// maxObjectBytes is the upload cap documented by the design: 10 MiB.
	maxObjectBytes int64 = 10 << 20
	// downloadContentType is what a download is served as: the object bytes are
	// opaque to the API and the store does not persist the upload media type.
	downloadContentType = "application/octet-stream"
)

// ObjectService is the storage use case consumed by the admin routes.
// *application.Service satisfies it.
type ObjectService interface {
	Put(ctx context.Context, r io.Reader, contentType string) (storage.Object, error)
	Open(ctx context.Context, sha256 string) (io.ReadCloser, storage.Object, error)
}

// Handler serves the admin object-storage endpoints. Every route is mounted
// behind the global bearer authentication and RequireAdmin.
type Handler struct {
	svc ObjectService
}

// NewHandler returns the object-storage handlers.
func NewHandler(svc ObjectService) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts the admin storage endpoints on mux. guard wraps every
// route with the caller's admin middleware; the server passes RequireAdmin and
// never installs a second authentication middleware here.
func RegisterRoutes(mux *http.ServeMux, h *Handler, guard func(http.Handler) http.Handler) {
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("POST /api/admin/storage/objects", guard(http.HandlerFunc(h.Create)))
	mux.Handle("GET /api/admin/storage/objects/{sha256}", guard(http.HandlerFunc(h.Get)))
}

// objectPayload is the public representation of an uploaded object.
type objectPayload struct {
	SHA256      string `json:"sha256"`
	Size        int64  `json:"size"`
	ContentType string `json:"content_type"`
}

// Create stores the raw request body (at most 10 MiB) and returns its
// content-addressed metadata. The Content-Type header is required and recorded
// with the object; a missing header is invalid_argument and an oversized body
// is 413.
func (h *Handler) Create(w http.ResponseWriter, r *http.Request) {
	body := http.MaxBytesReader(w, r.Body, maxObjectBytes)
	obj, err := h.svc.Put(r.Context(), body, r.Header.Get("Content-Type"))
	if err != nil {
		var tooLarge *http.MaxBytesError
		switch {
		case errors.As(err, &tooLarge):
			platform.WriteError(w, r, http.StatusRequestEntityTooLarge, platform.CodeInvalidArgument,
				fmt.Sprintf("object exceeds the %d byte limit", maxObjectBytes))
		case errors.Is(err, storage.ErrInvalidContentType):
			platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
				"Content-Type header is required")
		default:
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
				"internal server error")
		}
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		Data objectPayload `json:"data"`
	}{Data: objectPayload{
		SHA256:      obj.SHA256,
		Size:        obj.Size,
		ContentType: obj.ContentType,
	}})
}

// Get streams the stored bytes of an object. The bytes are verified against
// their content-addressed id before the response starts; a mismatch is
// storage_corrupted and an unknown object is not_found.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	rc, obj, err := h.svc.Open(r.Context(), r.PathValue("sha256"))
	if err != nil {
		switch {
		case errors.Is(err, storage.ErrInvalidSHA256):
			platform.WriteError(w, r, http.StatusBadRequest, platform.CodeInvalidArgument,
				"invalid object sha256")
		case errors.Is(err, storage.ErrObjectNotFound):
			platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, "object not found")
		case errors.Is(err, storage.ErrCorrupted):
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeStorageCorrupted,
				"stored object failed its integrity check")
		default:
			platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal,
				"internal server error")
		}
		return
	}
	defer rc.Close()

	w.Header().Set("Content-Type", downloadContentType)
	w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, rc); err != nil {
		// The response already started; closing the connection is all that is
		// left. Log the failure without object contents.
		slog.ErrorContext(r.Context(), "storage: stream object", "sha256", obj.SHA256, "error", err)
	}
}

// writeJSON writes a success response in the shared `{"data": ...}` style.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}
