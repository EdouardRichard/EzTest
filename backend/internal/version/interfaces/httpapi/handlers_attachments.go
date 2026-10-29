package httpapi

import (
	"errors"
	"net/http"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const (
	// multipartMemory is the ParseMultipartForm memory threshold; larger parts
	// spill to a temporary file managed by the standard library.
	multipartMemory int64 = 10 << 20
	// maxAttachmentRequestBytes bounds the whole multipart request. The file
	// itself is capped at 10 MiB by the attachment service; the extra MiB pays
	// for the multipart envelope so an oversized file still produces the
	// spec §7.1 attachment_too_large 422 instead of a transport error.
	maxAttachmentRequestBytes int64 = domain.MaxAttachmentBytes + 1<<20
)

// AttachmentHandler serves the requirement attachment endpoints (spec §7.3,
// §6.7). The bytes never pass through the API responses except on the explicit
// download route; storage paths are never exposed.
type AttachmentHandler struct {
	attachments AttachmentService
}

// RegisterRoutes mounts the attachment routes. Authentication is the server's
// global middleware: there is no per-route guard.
func (h *AttachmentHandler) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/versions/{id}/requirements/{rid}/attachments", h.List)
	mux.HandleFunc("POST /api/versions/{id}/requirements/{rid}/attachments", h.Upload)
	mux.HandleFunc("GET /api/versions/{id}/requirements/{rid}/attachments/{aid}/content", h.Download)
	mux.HandleFunc("DELETE /api/versions/{id}/requirements/{rid}/attachments/{aid}", h.Delete)
}

// List returns the attachment metadata of one requirement.
func (h *AttachmentHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	attachments, err := h.attachments.List(r.Context(), versionID, reqID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusOK, newAttachmentDTOs(attachments))
}

// Upload accepts a multipart/form-data request with the single field `file`
// and returns 201 with metadata only.
func (h *AttachmentHandler) Upload(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxAttachmentRequestBytes)
	if err := r.ParseMultipartForm(multipartMemory); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			WriteServiceError(w, r, &requestError{
				Status:  http.StatusUnprocessableEntity,
				Code:    platform.CodeInvalidArgument,
				Message: "attachment must be at most 10 MiB",
				Details: map[string]any{
					"field":   "file",
					"reason":  "attachment_too_large",
					"message": "attachment must be at most 10 MiB",
				},
			})
			return
		}
		WriteServiceError(w, r, newBadRequest("file", "",
			"request body must be a multipart/form-data upload with a file field"))
		return
	}
	if r.MultipartForm != nil {
		defer func() { _ = r.MultipartForm.RemoveAll() }()
	}

	file, header, err := r.FormFile("file")
	if err != nil {
		WriteServiceError(w, r, domain.NewInvalid("file", "invalid_attachment_content",
			"the multipart request must carry a file field"))
		return
	}
	defer func() { _ = file.Close() }()

	declaredType := ""
	if header != nil {
		declaredType = header.Header.Get("Content-Type")
	}
	attachment, err := h.attachments.Upload(r.Context(), versionID, reqID, header.Filename, declaredType, file, ActorID(r.Context()))
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	WriteData(w, http.StatusCreated, newAttachmentDTO(attachment))
}

// Download streams the stored bytes with the persisted MIME type, a nosniff
// header and an RFC 5987 inline Content-Disposition.
func (h *AttachmentHandler) Download(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, attachmentID, ok := h.attachmentPathIDs(w, r)
	if !ok {
		return
	}
	attachment, content, err := h.attachments.OpenContent(r.Context(), versionID, reqID, attachmentID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	defer func() { _ = content.Close() }()

	w.Header().Set("Content-Type", attachment.MIMEType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", contentDisposition(attachment.Filename))
	http.ServeContent(w, r, attachment.Filename, attachment.CreatedAt, content)
}

// Delete removes an attachment's metadata (and then its file best effort) and
// returns 204.
func (h *AttachmentHandler) Delete(w http.ResponseWriter, r *http.Request) {
	versionID, reqID, attachmentID, ok := h.attachmentPathIDs(w, r)
	if !ok {
		return
	}
	if err := h.attachments.Delete(r.Context(), versionID, reqID, attachmentID, ActorID(r.Context())); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// requirementPathIDs parses and validates the {id}/{rid} path pair. On failure
// it renders the 422 and reports false.
func (h *AttachmentHandler) requirementPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	versionID, err := PathID(r, "id")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	reqID, err := PathID(r, "rid")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, false
	}
	return versionID, reqID, true
}

// attachmentPathIDs parses and validates the {id}/{rid}/{aid} path triple.
func (h *AttachmentHandler) attachmentPathIDs(w http.ResponseWriter, r *http.Request) (int64, int64, int64, bool) {
	versionID, reqID, ok := h.requirementPathIDs(w, r)
	if !ok {
		return 0, 0, 0, false
	}
	attachmentID, err := PathID(r, "aid")
	if err != nil {
		WriteServiceError(w, r, err)
		return 0, 0, 0, false
	}
	return versionID, reqID, attachmentID, true
}
