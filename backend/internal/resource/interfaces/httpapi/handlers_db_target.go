package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// maxDBTargetBodyBytes caps the db target upsert/test request bodies. The body
// is a small connection descriptor, so the ordinary catalog cap is enough.
const maxDBTargetBodyBytes int64 = 64 << 10

// DBTargetHandler serves the environment db target endpoints (spec §7.8). All
// four routes are administrator-only, including the read: reading a db target
// exposes connection parameters (spec §11.3).
type DBTargetHandler struct {
	svc   *application.DBTargetService
	audit application.DBTargetAuditSink
}

// DBTargetHandlerOption customises the handler dependencies.
type DBTargetHandlerOption func(*DBTargetHandler)

// WithDBTargetAuditSink wires the db target audit recorder. A nil sink disables
// auditing (used by partially wired processes and tests); an audit failure is
// logged and never fails the request (spec §11.2).
func WithDBTargetAuditSink(sink application.DBTargetAuditSink) DBTargetHandlerOption {
	return func(handler *DBTargetHandler) {
		handler.audit = sink
	}
}

// NewDBTargetHandler returns the db target handler for svc.
func NewDBTargetHandler(svc *application.DBTargetService, opts ...DBTargetHandlerOption) *DBTargetHandler {
	handler := &DBTargetHandler{svc: svc}
	for _, opt := range opts {
		if opt != nil {
			opt(handler)
		}
	}
	return handler
}

// RegisterRoutes mounts the db target endpoints on mux. The guard wraps every
// route (the server passes the identity context's admin guard); a nil guard is a
// passthrough so handler tests can mount the routes directly.
func (h *DBTargetHandler) RegisterRoutes(mux *http.ServeMux, guard func(http.Handler) http.Handler) {
	if mux == nil {
		return
	}
	if guard == nil {
		guard = func(next http.Handler) http.Handler { return next }
	}
	mux.Handle("GET /api/resources/environments/{id}/db-target", guard(http.HandlerFunc(h.Get)))
	mux.Handle("PUT /api/resources/environments/{id}/db-target", guard(http.HandlerFunc(h.Upsert)))
	mux.Handle("DELETE /api/resources/environments/{id}/db-target", guard(http.HandlerFunc(h.Delete)))
	mux.Handle("POST /api/resources/environments/{id}/db-target/test", guard(http.HandlerFunc(h.Test)))
}

// Get returns the environment target or data:null when it is not configured.
func (h *DBTargetHandler) Get(w http.ResponseWriter, r *http.Request) {
	environmentID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	target, err := h.svc.Get(r.Context(), environmentID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if target == nil {
		WriteData(w, http.StatusOK, nil)
		return
	}
	WriteData(w, http.StatusOK, newDBTargetDTO(*target))
}

// Upsert creates or replaces the environment target. The password is accepted
// on create and on replacement but is never echoed.
func (h *DBTargetHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	environmentID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var req putDBTargetRequest
	if err := DecodeJSON(w, r, &req, maxDBTargetBodyBytes); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	cmd, err := req.toCommand()
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	target, err := h.svc.Upsert(r.Context(), environmentID, actorID(r.Context()), cmd)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.recordAudit(r.Context(), application.DBTargetAuditEvent{
		Action:     application.ActionDBTargetUpserted,
		TargetType: application.DBTargetAuditTargetType,
		TargetID:   strconv.FormatInt(target.ID, 10),
		ActorID:    actorID(r.Context()),
		Metadata: map[string]any{
			"environment_id": environmentID,
			"host":           target.Host,
			"database":       target.DatabaseName,
			"result":         "upserted",
		},
	})
	WriteData(w, http.StatusOK, newDBTargetDTO(target))
}

// Delete removes the environment target without touching the target database.
func (h *DBTargetHandler) Delete(w http.ResponseWriter, r *http.Request) {
	environmentID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	existing, err := h.svc.Get(r.Context(), environmentID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	if err := h.svc.Delete(r.Context(), environmentID, actorID(r.Context())); err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.auditDeleted(r.Context(), existing, environmentID)
	w.WriteHeader(http.StatusNoContent)
}

// Test probes the stored target or an unsaved body override. A failed probe is
// a 200 with ok=false and a classified reason (spec §7.8).
func (h *DBTargetHandler) Test(w http.ResponseWriter, r *http.Request) {
	environmentID, err := PathID(r)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	var override *application.UpsertDBTargetCommand
	if r.ContentLength != 0 {
		var req putDBTargetRequest
		if err := DecodeJSON(w, r, &req, maxDBTargetBodyBytes); err != nil {
			WriteServiceError(w, r, err)
			return
		}
		if !req.isEmpty() {
			cmd, err := req.toCommand()
			if err != nil {
				WriteServiceError(w, r, err)
				return
			}
			override = &cmd
		}
	}

	// The audit entry names what was tested; the read is best effort and only
	// used for metadata, never to fail the test itself.
	existing, err := h.svc.Get(r.Context(), environmentID)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	result, err := h.svc.Test(r.Context(), environmentID, override)
	if err != nil {
		WriteServiceError(w, r, err)
		return
	}
	h.auditTested(r.Context(), existing, environmentID, override, result)
	WriteData(w, http.StatusOK, newDBTargetTestResultDTO(result))
}

// actorID returns the authenticated actor or 0 when the context has none. The
// route guard already requires an administrator, so 0 only appears in a
// mis-wired process and is treated as "system".
func actorID(ctx context.Context) int64 {
	actor := ActorID(ctx)
	if actor == nil {
		return 0
	}
	return *actor
}

// auditDeleted records resource.db_target.deleted for a target that existed
// before the delete.
func (h *DBTargetHandler) auditDeleted(ctx context.Context, target *domain.DBTarget, environmentID int64) {
	if target == nil {
		return
	}
	h.recordAudit(ctx, application.DBTargetAuditEvent{
		Action:     application.ActionDBTargetDeleted,
		TargetType: application.DBTargetAuditTargetType,
		TargetID:   strconv.FormatInt(target.ID, 10),
		ActorID:    actorID(ctx),
		Metadata: map[string]any{
			"environment_id": environmentID,
			"host":           target.Host,
			"database":       target.DatabaseName,
		},
	})
}

// auditTested records resource.db_target.tested. The host/database of an
// override describe the form values that were probed; the target id is the
// stored target when one exists.
func (h *DBTargetHandler) auditTested(ctx context.Context, stored *domain.DBTarget, environmentID int64, override *application.UpsertDBTargetCommand, result domain.DBTargetTestResult) {
	event := application.DBTargetAuditEvent{
		Action:     application.ActionDBTargetTested,
		TargetType: application.DBTargetAuditTargetType,
		ActorID:    actorID(ctx),
		Metadata: map[string]any{
			"environment_id": environmentID,
			"result":         "failed",
		},
	}
	if result.OK {
		event.Metadata["result"] = "ok"
	}
	if stored != nil {
		event.TargetID = strconv.FormatInt(stored.ID, 10)
		event.Metadata["host"] = stored.Host
		event.Metadata["database"] = stored.DatabaseName
	}
	if override != nil {
		if override.Host != "" {
			event.Metadata["host"] = override.Host
		}
		if override.Database != "" {
			event.Metadata["database"] = override.Database
		}
	}
	h.recordAudit(ctx, event)
}

// recordAudit writes one audit event and logs a failure without failing the
// request (spec §11.2).
func (h *DBTargetHandler) recordAudit(ctx context.Context, event application.DBTargetAuditEvent) {
	if h == nil || h.audit == nil {
		return
	}
	if err := h.audit.RecordDBTargetAudit(ctx, event); err != nil {
		slog.WarnContext(ctx, "resource: db target audit write failed",
			"action", event.Action, "target_id", event.TargetID, "error", err)
	}
}
