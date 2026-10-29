package httpapi

import (
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// exportRequest is the POST /export body (spec §7.2 endpoint 25).
type exportRequest struct {
	Format string `json:"format"`
}

// exportMediaType is the download content type of every server-generated export
// file (spec §7.2 endpoint 27). The real format is carried by the filename
// extension; nosniff stops a browser from re-interpreting the bytes.
const exportMediaType = "application/octet-stream"

// createExport handles POST /export: 201 for a newly generated file, 200 when the
// frozen (version, format, baseline) export is reused (spec §7.2 endpoint 25).
func (h *Handler) createExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var body exportRequest
	if err := decodeBody(r, &body); err != nil {
		writeServiceError(w, r, err)
		return
	}
	// The service is the authority on reuse: it marks a metadata row served by
	// the baseline-bound reuse lookup, so the status never depends on a racing
	// pre-check in the handler.
	export, err := h.deps.Exports.Create(ctx, versionID, actorID(ctx), body.Format)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	status := http.StatusCreated
	if export.Reused {
		status = http.StatusOK
	}
	writeData(w, status, newExportDTO(export))
}

// listExports handles GET /exports (200): one cursor page of export metadata.
func (h *Handler) listExports(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, cursor, err := queryPage(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	format := strings.TrimSpace(r.URL.Query().Get("format"))
	exports, next, hasMore, err := h.deps.Exports.List(r.Context(), versionID, format, limit, cursor)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	rows := make([]caseExportDTO, 0, len(exports))
	for _, export := range exports {
		rows = append(rows, newExportDTO(export))
	}
	writePage(w, rows, next, hasMore)
}

// downloadExport handles GET /exports/{exportId}/download (200): the stored bytes
// as an octet-stream attachment. The service re-reads the file by metadata and
// verifies its sha256, so a corrupted file surfaces as 500 export_corrupted and
// the internal path is never exposed.
func (h *Handler) downloadExport(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	versionID, err := pathID(r, "versionId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	exportID, err := pathID(r, "exportId")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	content, export, err := h.deps.Exports.Download(ctx, versionID, exportID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	defer func() { _ = content.Close() }()

	w.Header().Set("Content-Type", exportMediaType)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", attachmentDisposition(exportFileName(export)))
	if export.SizeBytes > 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(export.SizeBytes, 10))
	}
	w.WriteHeader(http.StatusOK)
	if _, err := io.Copy(w, content); err != nil {
		// The headers are already written; the connection is the only signal left.
		h.logger.WarnContext(ctx, "mindmap: streaming export failed",
			"version_id", versionID, "export_id", exportID, "error", err)
	}
}

// exportExtensions maps each server export format to its filename extension. PNG
// is rendered client-side and never stored (D38).
var exportExtensions = map[string]string{
	domain.ExportMarkdown: "md",
	domain.ExportMermaid:  "mmd",
	domain.ExportOPML:     "opml",
}

// exportFileName is the suggested attachment name of one export. The internal
// file path is never used, so a download cannot leak the storage layout.
func exportFileName(export domain.CaseExport) string {
	extension, ok := exportExtensions[export.Format]
	if !ok {
		extension = "bin"
	}
	return fmt.Sprintf("mindmap-%s-%s.%s", export.Format, idString(export.ID), extension)
}

// attachmentDisposition renders the RFC 5987 attachment header of the download.
// The filename is percent-encoded as UTF-8, so the header stays ASCII and cannot
// inject a second header line.
func attachmentDisposition(filename string) string {
	var builder strings.Builder
	builder.WriteString("attachment; filename*=UTF-8''")
	for i := 0; i < len(filename); i++ {
		c := filename[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9',
			c == '.', c == '-', c == '_', c == '~':
			builder.WriteByte(c)
		default:
			fmt.Fprintf(&builder, "%%%02X", c)
		}
	}
	return builder.String()
}
