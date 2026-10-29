package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// maxBatchBodyBytes caps the create-batch body. The only field is a bounded
// case-id array, so 64 KiB is far above any legitimate request.
const maxBatchBodyBytes int64 = 64 << 10

// createBatchRequest is the POST /api/versions/{id}/batches body (spec §7.7).
// An omitted kind is baseline; an omitted or empty caseIds selects every
// baseline case; parentBatchId links incremental batches to the previous
// terminal batch.
type createBatchRequest struct {
	CaseIDs       []string `json:"caseIds"`
	Kind          string   `json:"kind"`
	ParentBatchID *string  `json:"parentBatchId"`
}

// pauseBatchRequest is the POST .../pause body. An omitted mode defaults to
// graceful (spec §5.4).
type pauseBatchRequest struct {
	Mode string `json:"mode"`
}

// BatchHandler serves the batch endpoints of spec §6.2.
type BatchHandler struct {
	batches  BatchService
	executor BrowserHealth
	audit    AuditSink
}

// RegisterRoutes mounts the batch routes; authentication is the server's global
// middleware and there is no per-route guard (spec §5.11).
func (h *BatchHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("POST /api/versions/{id}/batches", h.Create)
	mux.HandleFunc("GET /api/versions/{id}/batches", h.List)
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}", h.Get)
	mux.HandleFunc("POST /api/versions/{id}/batches/{batchId}/pause", h.Pause)
	mux.HandleFunc("POST /api/versions/{id}/batches/{batchId}/resume", h.Resume)
	mux.HandleFunc("POST /api/versions/{id}/batches/{batchId}/cancel", h.Cancel)
}

// Create validates the executor health gate and starts a batch (spec §5.1,
// §5.3). The executor probe is an HTTP-layer gate: a dead executor answers 503
// executor_unavailable without persisting a half-created batch. A user may not
// create a supplement batch (only the autonomy worker does), and the kind is
// validated before the service runs. It responds 201 with the created batch.
func (h *BatchHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var request createBatchRequest
	if err := decodeJSON(r, &request, maxBatchBodyBytes); err != nil {
		writeServiceError(w, r, err)
		return
	}
	kind, err := parseCreatedBatchKind(request.Kind)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if kind == domain.BatchKindSupplement {
		writeServiceError(w, r, domain.NewReasonError("supplement_batch_automatic",
			"supplement_batch_automatic",
			"a supplement batch is created automatically by the autonomous worker", false, nil))
		return
	}
	caseIDs, err := parseCaseIDs(request.CaseIDs)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	parentBatchID, err := parseOptionalDecimalID(request.ParentBatchID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if err := h.requireHealthyExecutor(r.Context()); err != nil {
		writeServiceError(w, r, err)
		return
	}
	batch, err := h.batches.Create(r.Context(), versionID, ActorID(r.Context()), executionapp.CreateBatchCommand{
		CaseIDs:       caseIDs,
		Kind:          string(kind),
		ParentBatchID: parentBatchID,
	})
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if batch != nil {
		logAuditFailure(r.Context(), "execution.batch.created",
			auditSink(h.audit).BatchCreated(r.Context(), *batch, ActorID(r.Context())),
			"batch_id", batch.ID, "version_id", batch.VersionID)
	}
	writeData(w, http.StatusCreated, newBatchDTO(batch))
}

// List returns one cursor page of batches, newest first, optionally filtered by
// status and P7 kind. The active-batch lookup of the frontend uses
// status=running&limit=1.
func (h *BatchHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	status, err := parseBatchStatus(r.URL.Query().Get("status"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	kind, err := parseBatchKindFilter(r.URL.Query().Get("kind"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, beforeID, err := parsePagination(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	batches, next, hasMore, err := h.listBatches(r.Context(), versionID, status, kind, beforeID, limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	items := make([]batchDTO, 0, len(batches))
	for i := range batches {
		items = append(items, newBatchDTO(&batches[i]))
	}
	writePage(w, items, next, hasMore)
}

// listBatches reads one page, applying the status/kind filters by scanning
// pages while keeping the keyset cursor correct.
func (h *BatchHandler) listBatches(ctx context.Context, versionID int64, status domain.BatchStatus, kind domain.BatchKind, beforeID int64, limit int) ([]domain.RunBatch, string, bool, error) {
	if status == "" && kind == "" {
		page, hasMore, err := h.batches.List(ctx, versionID, beforeID, limit)
		if err != nil {
			return nil, "", false, err
		}
		next := ""
		if len(page) > 0 {
			next = nextCursor(len(page), page[len(page)-1].ID, hasMore)
		}
		return page, next, hasMore, nil
	}

	collected := make([]domain.RunBatch, 0, limit)
	cursor := beforeID
	for {
		page, hasMore, err := h.batches.List(ctx, versionID, cursor, platform.MaxLimit)
		if err != nil {
			return nil, "", false, err
		}
		lastScanned := cursor
		for _, batch := range page {
			lastScanned = batch.ID
			if status != "" && batch.Status != status {
				continue
			}
			if kind != "" && effectiveBatchKind(batch) != kind {
				continue
			}
			collected = append(collected, batch)
			if len(collected) == limit {
				return collected, nextCursor(len(collected), collected[len(collected)-1].ID, true), true, nil
			}
		}
		if !hasMore || len(page) == 0 {
			return collected, "", false, nil
		}
		cursor = lastScanned
	}
}

// Get returns one batch of the version.
func (h *BatchHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	batch, err := h.batches.Get(r.Context(), versionID, batchID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newBatchDTO(batch))
}

// Pause pauses a batch in the requested mode (spec §5.4).
func (h *BatchHandler) Pause(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	var request pauseBatchRequest
	if err := decodeJSON(r, &request, maxBatchBodyBytes); err != nil {
		writeServiceError(w, r, err)
		return
	}
	batch, err := h.batches.Pause(r.Context(), versionID, batchID, domain.PauseMode(request.Mode), ActorID(r.Context()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if batch != nil {
		logAuditFailure(r.Context(), "execution.batch.paused",
			auditSink(h.audit).BatchPaused(r.Context(), *batch, true, ActorID(r.Context())),
			"batch_id", batch.ID, "version_id", batch.VersionID)
	}
	writeData(w, http.StatusOK, newBatchDTO(batch))
}

// Resume clears the pause flag of a paused batch (spec §5.4).
func (h *BatchHandler) Resume(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	batch, err := h.batches.Resume(r.Context(), versionID, batchID, ActorID(r.Context()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if batch != nil {
		logAuditFailure(r.Context(), "execution.batch.resumed",
			auditSink(h.audit).BatchPaused(r.Context(), *batch, false, ActorID(r.Context())),
			"batch_id", batch.ID, "version_id", batch.VersionID)
	}
	writeData(w, http.StatusOK, newBatchDTO(batch))
}

// Cancel terminates a batch and records the execution.batch.cancelled audit
// entry (spec §5.11).
func (h *BatchHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	batch, err := h.batches.Cancel(r.Context(), versionID, batchID, ActorID(r.Context()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	logAuditFailure(r.Context(), "execution.batch.cancelled",
		auditSink(h.audit).BatchCancelled(r.Context(), batchID, versionID, ActorID(r.Context())),
		"batch_id", batchID, "version_id", versionID)
	writeData(w, http.StatusOK, newBatchDTO(batch))
}

// requireHealthyExecutor probes the executor before a batch is created. A missing
// health probe fails closed: without an executor no case can run.
func (h *BatchHandler) requireHealthyExecutor(ctx context.Context) error {
	if h == nil || h.executor == nil {
		return domain.NewReasonError("executor_unavailable", "executor_unavailable",
			"the browser executor is not configured", true, nil)
	}
	if err := h.executor.Health(ctx); err != nil {
		slog.WarnContext(ctx, "execution executor is unavailable", "error", err)
		return domain.NewReasonError("executor_unavailable", "executor_unavailable",
			"the browser executor is unavailable", true, nil)
	}
	return nil
}

// batchPathIDs parses both ids of a batch route.
func batchPathIDs(r *http.Request) (int64, int64, error) {
	versionID, err := PathID(r, "id")
	if err != nil {
		return 0, 0, err
	}
	batchID, err := PathID(r, "batchId")
	if err != nil {
		return 0, 0, err
	}
	return versionID, batchID, nil
}

// parseCaseIDs decodes the optional caseIds body array of decimal strings.
func parseCaseIDs(raw []string) ([]int64, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	ids := make([]int64, 0, len(raw))
	for _, item := range raw {
		id, err := parseDecimalID(item)
		if err != nil {
			return nil, newInvalid("invalid_case_ids", "caseIds must be positive decimal strings")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// parseOptionalDecimalID parses an optional decimal id string. A nil pointer is
// an absent id; an empty string is rejected because the DTO uses null.
func parseOptionalDecimalID(raw *string) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := parseDecimalID(*raw)
	if err != nil {
		return nil, newInvalid("invalid_parent_batch", "parentBatchId must be a positive decimal string")
	}
	return &id, nil
}

// parseCreatedBatchKind validates the optional kind of a create request and
// defaults it to baseline (spec §7.7).
func parseCreatedBatchKind(raw string) (domain.BatchKind, error) {
	kind, err := domain.ParseBatchKind(strings.TrimSpace(raw))
	if err != nil {
		return "", newInvalid("invalid_batch_kind", err.Error())
	}
	return kind, nil
}

// parseBatchKindFilter validates the optional ?kind= list filter. An empty
// filter means every kind.
func parseBatchKindFilter(raw string) (domain.BatchKind, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	kind, err := domain.ParseBatchKind(raw)
	if err != nil {
		return "", newInvalid("invalid_batch_kind", err.Error())
	}
	return kind, nil
}

// effectiveBatchKind normalizes a P6 historical batch (no stored kind) to
// baseline so list filters and DTOs agree.
func effectiveBatchKind(batch domain.RunBatch) domain.BatchKind {
	if batch.Kind == "" {
		return domain.BatchKindBaseline
	}
	return batch.Kind
}

// parseDecimalID parses one positive decimal id string.
func parseDecimalID(raw string) (int64, error) {
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, newInvalid("invalid_id", "id must be a positive decimal string")
	}
	return id, nil
}
