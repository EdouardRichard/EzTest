// Package httpapi exposes the execution context's HTTP interface (spec §6).
// Handlers stay thin: they decode DTOs, call the application services and render
// the shared response envelopes. Every execution-owned id crosses this boundary
// as a decimal string, never as a JSON number (spec §5.12 invariant 9).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// BatchService is the batch application port consumed by the HTTP layer.
// *application.BatchService implements it.
type BatchService interface {
	Create(ctx context.Context, versionID, actor int64, cmd executionapp.CreateBatchCommand) (*domain.RunBatch, error)
	Get(ctx context.Context, versionID, batchID int64) (*domain.RunBatch, error)
	List(ctx context.Context, versionID int64, beforeSeq int64, limit int) ([]domain.RunBatch, bool, error)
	Pause(ctx context.Context, versionID, batchID int64, mode domain.PauseMode, actor int64) (*domain.RunBatch, error)
	Resume(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
	Cancel(ctx context.Context, versionID, batchID, actor int64) (*domain.RunBatch, error)
	Rerun(ctx context.Context, versionID, batchID, caseRunID, actor int64) (*domain.CaseRun, error)
}

// CaseRunReader is the batch-scoped case-run read the API needs.
// *postgres.CaseRunRepository implements it.
type CaseRunReader interface {
	GetCaseRun(ctx context.Context, batchID, caseRunID int64) (domain.CaseRun, error)
	ListCaseRuns(ctx context.Context, batchID int64, status domain.CaseRunStatus, beforeID int64, limit int) ([]domain.CaseRun, bool, error)
}

// StepReader lists the recorded steps of one case run.
type StepReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.StepResult, error)
}

// AssertionReader lists the recorded assertions of one case run.
type AssertionReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.AssertionResult, error)
}

// EvidenceReader lists the artifacts of one case run without reading bytes.
type EvidenceReader interface {
	ListByCaseRun(ctx context.Context, caseRunID int64) ([]domain.Evidence, error)
}

// EvidenceService reads one verified evidence artifact. *application.EvidenceService
// implements it.
type EvidenceService interface {
	Open(ctx context.Context, versionID, evidenceID int64) (io.ReadCloser, domain.Evidence, error)
}

// ReportService renders the derived Batch 1 report. *application.ReportService
// implements it.
type ReportService interface {
	BatchReport(ctx context.Context, versionID, batchID int64) (executionapp.ReportView, error)
	Markdown(ctx context.Context, versionID, batchID int64) ([]byte, string, error)
}

// SessionGate resolves the waiting human takeover of a session.
type SessionGate interface {
	PendingTakeover(ctx context.Context, sessionID int64) (*executionapp.TakeoverRef, error)
}

// AgentSessionLister lists this version's execution agent sessions.
type AgentSessionLister interface {
	ListSessions(ctx context.Context, versionID int64) ([]executionapp.AgentSessionRef, error)
}

// BrowserHealth probes the executor before a batch is created (spec §5.1).
type BrowserHealth interface {
	Health(ctx context.Context) error
}

// AuditSink records the execution audit entries of spec §5.11.
type AuditSink interface {
	BatchCreated(ctx context.Context, batch domain.RunBatch, actor int64) error
	BatchPaused(ctx context.Context, batch domain.RunBatch, paused bool, actor int64) error
	BatchCancelled(ctx context.Context, batchID, versionID int64, actorID int64) error
}

// CaseRunSession is the credential-free session projection of a case run.
type CaseRunSession struct {
	ID      int64
	Account string
	State   string
}

// SessionInfoReader resolves the credential-free session of a case run.
type SessionInfoReader interface {
	CaseRunSession(ctx context.Context, sessionID int64) (CaseRunSession, error)
}

// EvidenceRefReader reads the evidence ids linked to step/assertion results.
type EvidenceRefReader interface {
	ListByRef(ctx context.Context, refKind string, refIDs []int64) (map[int64][]int64, error)
}

// HandlerDeps is the dependency set of the execution HTTP handlers. Every field
// is a consumer-defined port so tests can inject fakes and a partially wired
// process can mount only the groups it has.
type HandlerDeps struct {
	Batches         BatchService
	CaseRuns        CaseRunReader
	Steps           StepReader
	Assertions      AssertionReader
	Evidence        EvidenceReader
	EvidenceService EvidenceService
	EvidenceRefs    EvidenceRefReader
	Reports         ReportService
	Sessions        SessionGate
	AgentSessions   AgentSessionLister
	Executor        BrowserHealth
	Audit           AuditSink
	SessionInfo     SessionInfoReader
}

// Handlers groups the execution HTTP handlers so the composition root wires
// them into the server through one dependency. A nil group is skipped, which
// keeps a partially wired process (and most tests) from panicking at startup.
type Handlers struct {
	Batches  *BatchHandler
	CaseRuns *CaseRunHandler
	Evidence *EvidenceHandler
	Reports  *ReportHandler
	Sessions *SessionHandler
}

// NewHandlers builds the handler groups whose primary dependency is wired.
func NewHandlers(deps HandlerDeps) *Handlers {
	handlers := &Handlers{}
	if deps.Batches != nil {
		handlers.Batches = &BatchHandler{
			batches:  deps.Batches,
			executor: deps.Executor,
			audit:    deps.Audit,
		}
	}
	if deps.Batches != nil && deps.CaseRuns != nil {
		handlers.CaseRuns = &CaseRunHandler{
			batches:     deps.Batches,
			runs:        deps.CaseRuns,
			steps:       deps.Steps,
			assertions:  deps.Assertions,
			evidence:    deps.Evidence,
			evidenceRef: deps.EvidenceRefs,
			sessions:    deps.Sessions,
			sessionInfo: deps.SessionInfo,
		}
	}
	if deps.EvidenceService != nil {
		handlers.Evidence = &EvidenceHandler{evidence: deps.EvidenceService}
	}
	if deps.Reports != nil {
		handlers.Reports = &ReportHandler{reports: deps.Reports}
	}
	if deps.AgentSessions != nil {
		handlers.Sessions = &SessionHandler{sessions: deps.AgentSessions}
	}
	return handlers
}

// RegisterRoutes mounts every wired handler group. Authentication is the
// server's global middleware: execution has no admin-only route (spec §5.11).
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	if h.Batches != nil {
		h.Batches.RegisterRoutes(mux)
	}
	if h.CaseRuns != nil {
		h.CaseRuns.RegisterRoutes(mux)
	}
	if h.Evidence != nil {
		h.Evidence.RegisterRoutes(mux)
	}
	if h.Reports != nil {
		h.Reports.RegisterRoutes(mux)
	}
	if h.Sessions != nil {
		h.Sessions.RegisterRoutes(mux)
	}
}

// ---------------------------------------------------------------------------
// Shared response helpers
// ---------------------------------------------------------------------------

// dataEnvelope is the success shape for a single resource.
type dataEnvelope struct {
	Data any `json:"data"`
}

// pageEnvelope is the success shape for a cursor-paged list.
type pageEnvelope struct {
	Data       any    `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// writeData writes {"data": data} with the given status code.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

// writePage writes {"data": items, "next_cursor": next, "has_more": hasMore}.
func writePage(w http.ResponseWriter, items any, next string, hasMore bool) {
	writeJSON(w, http.StatusOK, pageEnvelope{Data: items, NextCursor: next, HasMore: hasMore})
}

// writeJSON writes payload as JSON with the shared content type.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// PathID parses a positive decimal {name} path value.
func PathID(r *http.Request, name string) (int64, error) {
	id, err := strconv.ParseInt(r.PathValue(name), 10, 64)
	if err != nil || id <= 0 {
		return 0, newInvalid("invalid_id", name+" must be a positive decimal id")
	}
	return id, nil
}

// ActorID returns the authenticated platform user's id. Execution routes always
// run behind the identity middleware; an absent user yields 0.
func ActorID(ctx context.Context) int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0
	}
	return user.ID
}

// parsePagination reads limit and the opaque cursor. An invalid value is a 422.
func parsePagination(r *http.Request) (limit int, beforeID int64, err error) {
	limit, err = platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, 0, newInvalid("invalid_limit", "limit must be an integer between 1 and 100")
	}
	beforeID, err = platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return 0, 0, newInvalid("invalid_cursor", "cursor is not a valid pagination cursor")
	}
	return limit, beforeID, nil
}

// nextCursor returns the opaque cursor of the next page.
func nextCursor(items int, lastID int64, hasMore bool) string {
	if !hasMore || items == 0 {
		return ""
	}
	return platform.EncodeCursor(lastID)
}

// parseBatchStatus validates the optional ?status= filter of the batch list.
func parseBatchStatus(raw string) (domain.BatchStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch status := domain.BatchStatus(raw); status {
	case domain.BatchStatusPlanned, domain.BatchStatusRunning, domain.BatchStatusDone,
		domain.BatchStatusFailed, domain.BatchStatusCancelled:
		return status, nil
	default:
		return "", newInvalid("invalid_status", "status must be one of planned, running, done, failed or cancelled")
	}
}

// parseCaseRunStatus validates the optional ?status= filter of the case-run list.
func parseCaseRunStatus(raw string) (domain.CaseRunStatus, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	switch status := domain.CaseRunStatus(raw); status {
	case domain.CaseRunStatusPending, domain.CaseRunStatusRunning, domain.CaseRunStatusPassed,
		domain.CaseRunStatusFailed, domain.CaseRunStatusBlocked, domain.CaseRunStatusInconclusive,
		domain.CaseRunStatusSkipped, domain.CaseRunStatusInterrupted:
		return status, nil
	default:
		return "", newInvalid("invalid_status",
			"status must be one of pending, running, passed, failed, blocked, inconclusive, skipped or interrupted")
	}
}

// decodeJSON decodes a strict JSON body with the given size limit. A malformed
// body is a 422 invalid_json.
func decodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	if err := platform.DecodeJSON(r, dst, maxBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return newInvalid("body_too_large", "request body is too large")
		}
		return newInvalid("invalid_json", "request body is not a valid JSON object")
	}
	return nil
}

// newInvalid builds a 422 invalid_argument error with a stable reason.
func newInvalid(reason, message string) error {
	return domain.NewReasonError("invalid_argument", reason, message, false, nil)
}

// writeServiceError maps a service or transport error onto the shared error
// envelope, always carrying details.reason (spec §6.4).
func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{})
		return
	}

	var reasonErr *domain.ReasonError
	if errors.As(err, &reasonErr) && reasonErr != nil {
		status, code := reasonStatus(reasonErr)
		message := reasonErr.Message
		if message == "" {
			message = http.StatusText(status)
		}
		details := map[string]any{"reason": reasonErr.Reason, "message": message}
		for key, value := range reasonErr.Details {
			if _, exists := details[key]; !exists {
				details[key] = value
			}
		}
		platform.WriteError(w, r, status, code, message,
			platform.WithDetails(details), platform.WithRetryable(reasonErr.Retryable))
		return
	}

	switch {
	case errors.Is(err, executionapp.ErrBatchNotFound):
		writeNotFound(w, r, "not_found", "batch not found")
	case errors.Is(err, executionapp.ErrCaseRunNotFound):
		writeNotFound(w, r, "not_found", "case run not found")
	case errors.Is(err, executionapp.ErrEvidenceNotFound):
		writeNotFound(w, r, "not_found", "evidence not found")
	case errors.Is(err, domain.ErrCaseRunTerminal):
		writeConflict(w, r, "case_run_terminal", err.Error())
	case errors.Is(err, domain.ErrBatchFinished):
		writeConflict(w, r, "batch_finished", err.Error())
	case errors.Is(err, domain.ErrAlreadyPaused):
		writeConflict(w, r, "already_paused", err.Error())
	case errors.Is(err, domain.ErrNotPaused):
		writeConflict(w, r, "not_paused", err.Error())
	case errors.Is(err, domain.ErrInvalidCaseIDs):
		writeInvalid(w, r, "invalid_case_ids", err.Error())
	case errors.Is(err, executionapp.ErrConflict):
		writeConflict(w, r, "conflict", err.Error())
	default:
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
	}
}

// reasonStatus maps a ReasonError's stable code/reason onto the HTTP status and
// envelope code. State conflicts default to 409.
func reasonStatus(err *domain.ReasonError) (int, string) {
	switch err.Code {
	case "not_found":
		return http.StatusNotFound, platform.CodeNotFound
	case "invalid_argument":
		return http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case "unavailable":
		return http.StatusServiceUnavailable, platform.CodeUnavailable
	case "internal":
		return http.StatusInternalServerError, platform.CodeInternal
	case "conflict":
		return http.StatusConflict, platform.CodeConflict
	}
	switch err.Reason {
	case "executor_unavailable", "session_unavailable":
		return http.StatusServiceUnavailable, platform.CodeUnavailable
	}
	return http.StatusConflict, platform.CodeConflict
}

// writeNotFound writes a 404 with the shared reason detail.
func writeNotFound(w http.ResponseWriter, r *http.Request, reason, message string) {
	platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, message,
		platform.WithDetails(map[string]any{"reason": reason, "message": message}))
}

// writeConflict writes a 409 with the shared reason detail.
func writeConflict(w http.ResponseWriter, r *http.Request, reason, message string) {
	platform.WriteError(w, r, http.StatusConflict, platform.CodeConflict, message,
		platform.WithDetails(map[string]any{"reason": reason, "message": message}))
}

// writeInvalid writes a 422 with the shared reason detail.
func writeInvalid(w http.ResponseWriter, r *http.Request, reason, message string) {
	platform.WriteError(w, r, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, message,
		platform.WithDetails(map[string]any{"reason": reason, "message": message}))
}

// logAuditFailure reports a failed audit write without failing the request that
// already committed its business state.
func logAuditFailure(ctx context.Context, action string, err error, attrs ...any) {
	if err == nil {
		return
	}
	slog.WarnContext(ctx, "execution audit write failed", append([]any{"action", action, "error", err}, attrs...)...)
}

// compile-time proof that the production application services satisfy the
// consumer-side ports above.
var (
	_ BatchService    = (*executionapp.BatchService)(nil)
	_ EvidenceService = (*executionapp.EvidenceService)(nil)
	_ ReportService   = (*executionapp.ReportService)(nil)
)

// ensureJSONObject returns a valid JSON object, defaulting to {}.
func ensureJSONObject(raw json.RawMessage) json.RawMessage {
	if len(strings.TrimSpace(string(raw))) == 0 || !json.Valid(raw) {
		return json.RawMessage(`{}`)
	}
	return raw
}

// formatID formats one execution id as a decimal string.
func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// formatOptionalIDs formats an id slice as decimal strings (never nil).
func formatOptionalIDs(ids []int64) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, strconv.FormatInt(id, 10))
	}
	return out
}

// noopAudit is the default audit sink.
type noopAudit struct{}

func (noopAudit) BatchCreated(context.Context, domain.RunBatch, int64) error { return nil }
func (noopAudit) BatchPaused(context.Context, domain.RunBatch, bool, int64) error {
	return nil
}
func (noopAudit) BatchCancelled(context.Context, int64, int64, int64) error { return nil }

var _ AuditSink = noopAudit{}

// auditSink returns the wired sink or the no-op default.
func auditSink(sink AuditSink) AuditSink {
	if sink == nil {
		return noopAudit{}
	}
	return sink
}
