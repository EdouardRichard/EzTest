package httpapi

import (
	"io"
	"net/http"
	"strconv"
)

// EvidenceHandler serves the version-scoped evidence reads of spec §6.2. The
// bytes endpoint proves ownership, verifies the sha256 and advertises the hash
// and MIME type; the metadata endpoint never exposes the filesystem path
// (spec §5.7).
type EvidenceHandler struct {
	evidence EvidenceService
}

// RegisterRoutes mounts the evidence routes. Authentication is the server's
// global middleware: every logged-in user may read a visible version's evidence.
func (h *EvidenceHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/evidence/{evidenceId}", h.Download)
	mux.HandleFunc("GET /api/versions/{id}/evidence/{evidenceId}/meta", h.Meta)
}

// Download streams one verified artifact with its MIME type and X-Content-SHA256.
func (h *EvidenceHandler) Download(w http.ResponseWriter, r *http.Request) {
	versionID, evidenceID, err := evidencePathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	reader, evidence, err := h.evidence.Open(r.Context(), versionID, evidenceID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	defer reader.Close()

	mimeType := evidence.Mime
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("X-Content-SHA256", evidence.SHA256)
	// Evidence is user-influenced content: force a download and stop the browser
	// from MIME-sniffing a different type than the one recorded (spec §5.7).
	w.Header().Set("Content-Disposition", "attachment")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.FormatInt(evidence.SizeBytes, 10))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, reader)
}

// Meta returns the artifact metadata without its bytes.
func (h *EvidenceHandler) Meta(w http.ResponseWriter, r *http.Request) {
	versionID, evidenceID, err := evidencePathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	reader, evidence, err := h.evidence.Open(r.Context(), versionID, evidenceID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	_ = reader.Close()
	metadata := evidence.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	writeData(w, http.StatusOK, evidenceMetaDTO{
		ID:           evidence.ID,
		Kind:         evidence.Kind,
		Mime:         evidence.Mime,
		SizeBytes:    evidence.SizeBytes,
		SHA256:       evidence.SHA256,
		BatchID:      evidence.BatchID,
		CaseRunID:    evidence.CaseRunID,
		StepResultID: evidence.StepResultID,
		Metadata:     metadata,
		CreatedAt:    evidence.CreatedAt,
	})
}

// evidencePathIDs parses the version and evidence ids.
func evidencePathIDs(r *http.Request) (int64, int64, error) {
	versionID, err := PathID(r, "id")
	if err != nil {
		return 0, 0, err
	}
	evidenceID, err := PathID(r, "evidenceId")
	if err != nil {
		return 0, 0, err
	}
	return versionID, evidenceID, nil
}
