package designapi

import (
	"io"
	"net/http"
	"strconv"
	"strings"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

// CreateExport POST /api/versions/{id}/exports.
func (h *Handler) CreateExport(w http.ResponseWriter, r *http.Request) {
	actorID, ok := requireActor(w, r)
	if !ok {
		return
	}
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request createExportRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	ownerKind := strings.TrimSpace(request.OwnerKind)
	if ownerKind != "case" {
		httpx.Error(w, r, platform.Invalid("owner_kind", "P2 only supports case exports"))
		return
	}
	ownerID, err := validation.NonEmptyID("owner_id", request.OwnerID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if ownerID != versionID {
		httpx.Error(w, r, platform.Invalid("owner_id", "P2 case exports must target the whole version"))
		return
	}
	format := strings.TrimSpace(request.Format)
	if !design.ValidExportFormat(format) {
		httpx.Error(w, r, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv"))
		return
	}
	view, err := h.exports.Create(r.Context(), versionID, designapp.ExportInput{
		OwnerKind: ownerKind,
		OwnerID:   ownerID,
		Format:    format,
	}, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusCreated, toExportViewDTO(view))
}

// ListExports GET /api/versions/{id}/exports?format=&limit=&before=.
func (h *Handler) ListExports(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id", "id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	format := strings.TrimSpace(query.Get("format"))
	if format != "" && !design.ValidExportFormat(format) {
		httpx.Error(w, r, platform.Invalid("format", "must be one of markdown, mermaid, opml, csv"))
		return
	}
	limit := pagination.Limit(query.Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
	beforeID, err := queryOptionalID(r, "before", "before")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	views, hasMore, err := h.exports.List(r.Context(), versionID, format, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items := make([]exportViewDTO, 0, len(views))
	for _, view := range views {
		items = append(items, toExportViewDTO(view))
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// GetExport GET /api/exports/{exportId}.
func (h *Handler) GetExport(w http.ResponseWriter, r *http.Request) {
	exportID, err := pathID(r, "export_id", "exportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.exports.Get(r.Context(), exportID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toExportViewDTO(view))
}

// DownloadExport GET /api/exports/{exportId}/download. The service verifies the
// stored bytes against the recorded sha256/size before this handler streams
// them; internal paths are never exposed.
func (h *Handler) DownloadExport(w http.ResponseWriter, r *http.Request) {
	exportID, err := pathID(r, "export_id", "exportId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, reader, err := h.exports.Open(r.Context(), exportID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	defer reader.Close()

	w.Header().Set("Content-Type", view.MIME)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+sanitizeFilename(view.Filename)+"\"")
	w.Header().Set("Content-Length", strconv.FormatInt(view.Size, 10))
	w.Header().Set("X-Content-SHA256", view.SHA256)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

// sanitizeFilename keeps the Content-Disposition header well-formed.
func sanitizeFilename(raw string) string {
	name := strings.TrimSpace(raw)
	name = strings.ReplaceAll(name, "\"", "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.ReplaceAll(name, "\n", "")
	if name == "" {
		name = "case-export"
	}
	return name
}
