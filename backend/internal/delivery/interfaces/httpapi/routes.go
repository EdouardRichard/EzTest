// Package httpapi exposes the P7 delivery context's HTTP interface: issues,
// reports, analysis runs and the global issue/report centres (spec §7.3–§7.5,
// §7.10). Handlers stay thin: they validate path/query input, call the delivery
// application services or the consumer-defined ports below, and render the P0
// envelopes. Every delivery-owned id crosses this boundary as a decimal string
// (D29); nothing sensitive (credentials, evidence bodies, prompts, report file
// paths) is ever exposed.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	executiondomain "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	identityhttp "github.com/EdouardRichard/EzTest/backend/internal/identity/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ReportService is the report use-case surface consumed by the handlers.
// *application.ReportService implements it.
type ReportService interface {
	Ensure(ctx context.Context, versionID, batchID, actor int64) (domain.Report, bool, error)
	Regenerate(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error)
	Get(ctx context.Context, versionID, reportID int64) (domain.Report, error)
	ListPage(ctx context.Context, versionID int64, filter deliveryapp.ReportFilter) ([]domain.Report, string, bool, error)
	Diff(ctx context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error)
	Export(ctx context.Context, versionID, reportID int64) (domain.ReportExport, error)
	Publish(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error)
}

// AnalysisService is the analysis-run use-case surface consumed by the handlers.
// *application.AnalysisService implements it.
type AnalysisService interface {
	StartFailure(ctx context.Context, versionID, batchID, actor int64) (domain.AnalysisRun, error)
	Get(ctx context.Context, versionID, batchID, analysisID int64) (domain.AnalysisRun, error)
	List(ctx context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error)
}

// BatchReader is the execution batch projection the delivery endpoints need:
// manual analysis must only start on a terminal batch of the addressed version
// (spec §7.5), and report generation only on a batch of that version. The
// execution application's BatchService.Get satisfies it directly, so the
// composition root can wire the same execution use case it mounts for P6.
type BatchReader interface {
	Get(ctx context.Context, versionID, batchID int64) (*executiondomain.RunBatch, error)
}

// IssueCodeRef is one code anchor of an issue (spec §7.3): the codeintel
// code_anchor row with subject_kind=issue, projected onto the IssueView. Kind is
// the anchor location kind (e.g. file_line); Label is the optional human label.
type IssueCodeRef struct {
	AnchorID int64
	Path     string
	Line     int
	Kind     string
	Label    string
}

// IssueCodeRefReader lists the code anchors of one issue. It is an optional
// consumer port: the composition root wires the codeintel adapter (T17), and a
// nil reader or a read failure degrades to an empty codeRefs array (spec §7.3).
type IssueCodeRefReader interface {
	ListIssueCodeRefs(ctx context.Context, versionID, issueID int64) ([]IssueCodeRef, error)
}

// Compile-time proof that the production services satisfy the consumer ports.
var (
	_ ReportService   = (*deliveryapp.ReportService)(nil)
	_ AnalysisService = (*deliveryapp.AnalysisService)(nil)
)

// HandlerDeps is the dependency set of the delivery HTTP handlers. Every field
// is optional: a partially wired process mounts only the groups it can serve.
type HandlerDeps struct {
	Issues        deliveryapp.IssueService
	IssueRefs     deliveryapp.IssueRefReader
	IssueCodeRefs IssueCodeRefReader
	Reports       ReportService
	Analyses      AnalysisService
	Batches       BatchReader
	GlobalIssues  GlobalIssueReader
	GlobalReports GlobalReportReader
}

// Handlers groups the delivery HTTP handlers so the composition root wires them
// into the server through one dependency. A nil group is skipped.
type Handlers struct {
	Issues        *IssueHandler
	Reports       *ReportHandler
	Analysis      *AnalysisHandler
	GlobalIssues  *GlobalIssueHandler
	GlobalReports *GlobalReportHandler
}

// NewHandlers builds the handler groups whose primary dependency is wired. The
// analysis group needs both the analysis service and the batch reader: batch
// ownership and terminality are validated before a manual run starts. The report
// group mounts its ensure route only when the batch reader is wired too (the
// batch ownership gate must never be skipped); its read routes are unaffected.
func NewHandlers(deps HandlerDeps) *Handlers {
	handlers := &Handlers{}
	if deps.Issues != nil {
		handlers.Issues = &IssueHandler{
			issues:   deps.Issues,
			refs:     deps.IssueRefs,
			codeRefs: deps.IssueCodeRefs,
		}
	}
	if deps.Reports != nil {
		handlers.Reports = &ReportHandler{reports: deps.Reports, batches: deps.Batches}
	}
	if deps.Analyses != nil && deps.Batches != nil {
		handlers.Analysis = &AnalysisHandler{analyses: deps.Analyses, batches: deps.Batches}
	}
	if deps.GlobalIssues != nil {
		handlers.GlobalIssues = &GlobalIssueHandler{
			issues:   deps.GlobalIssues,
			codeRefs: deps.IssueCodeRefs,
		}
	}
	if deps.GlobalReports != nil {
		handlers.GlobalReports = &GlobalReportHandler{reports: deps.GlobalReports}
	}
	return handlers
}

// RegisterRoutes mounts every wired handler group. Authentication is the
// server's global middleware: the delivery surface has no admin-only route
// (spec §11.1).
func (h *Handlers) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	if h.Issues != nil {
		h.Issues.RegisterRoutes(mux)
	}
	if h.Reports != nil {
		h.Reports.RegisterRoutes(mux)
	}
	if h.Analysis != nil {
		h.Analysis.RegisterRoutes(mux)
	}
	if h.GlobalIssues != nil {
		h.GlobalIssues.RegisterRoutes(mux)
	}
	if h.GlobalReports != nil {
		h.GlobalReports.RegisterRoutes(mux)
	}
}

// ---------------------------------------------------------------------------
// Response envelopes
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

// writeJSON writes payload with the shared JSON content type.
func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

// writeData writes {"data": data} with the given status code.
func writeData(w http.ResponseWriter, status int, data any) {
	writeJSON(w, status, dataEnvelope{Data: data})
}

// writePage writes {"data": items, "next_cursor": next, "has_more": hasMore}.
func writePage(w http.ResponseWriter, items any, next string, hasMore bool) {
	writeJSON(w, http.StatusOK, pageEnvelope{Data: items, NextCursor: next, HasMore: hasMore})
}

// ---------------------------------------------------------------------------
// Request helpers
// ---------------------------------------------------------------------------

// pathID parses a positive decimal {name} path value (D29). A malformed value is
// a 422 invalid_argument.
func pathID(r *http.Request, name string) (int64, error) {
	return parseDecimalID(name, r.PathValue(name))
}

// parseDecimalID parses a positive decimal id and rejects any other spelling.
func parseDecimalID(field, raw string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || id <= 0 {
		return 0, domain.NewInvalid(field, domain.ReasonInvalidArgument,
			field+" must be a positive decimal id")
	}
	return id, nil
}

// optionalDecimalID parses an optional decimal-string request id. A JSON number
// does not decode into *string, so numeric ids are rejected by decodeJSON before
// reaching this helper (D29).
func optionalDecimalID(field string, raw *string) (*int64, error) {
	if raw == nil {
		return nil, nil
	}
	id, err := parseDecimalID(field, *raw)
	if err != nil {
		return nil, err
	}
	if strconv.FormatInt(id, 10) != strings.TrimSpace(*raw) {
		return nil, domain.NewInvalid(field, domain.ReasonInvalidArgument,
			field+" must be a decimal string id")
	}
	return &id, nil
}

// parseLimit parses the shared limit query parameter (1–100, default 20).
func parseLimit(r *http.Request) (int, error) {
	limit, err := platform.ParseLimit(r.URL.Query().Get("limit"))
	if err != nil {
		return 0, domain.NewInvalid("limit", domain.ReasonInvalidArgument,
			"limit must be an integer between 1 and 100")
	}
	return limit, nil
}

// parseCursorID decodes the opaque cursor of an id-keyed list.
func parseCursorID(r *http.Request) (int64, error) {
	cursor, err := platform.DecodeCursor(r.URL.Query().Get("cursor"))
	if err != nil {
		return 0, domain.NewInvalid("cursor", domain.ReasonInvalidArgument,
			"cursor is not a valid pagination cursor")
	}
	return cursor, nil
}

// parseOpaqueCursor validates and returns the keyset cursor of a created_at list.
// The value stays opaque: the repository owns its encoding.
func parseOpaqueCursor(r *http.Request) (string, error) {
	raw := strings.TrimSpace(r.URL.Query().Get("cursor"))
	if raw == "" {
		return "", nil
	}
	if _, err := platform.DecodeCursor(raw); err != nil {
		return "", domain.NewInvalid("cursor", domain.ReasonInvalidArgument,
			"cursor is not a valid pagination cursor")
	}
	return raw, nil
}

// parseOptionalQueryID parses an optional positive decimal id query filter.
func parseOptionalQueryID(r *http.Request, name string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return nil, nil
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return nil, domain.NewInvalid(name, domain.ReasonInvalidArgument,
			name+" must be a positive decimal id")
	}
	return &id, nil
}

// decodeJSON decodes a strict JSON body into dst. A malformed body is a 422.
func decodeJSON(r *http.Request, dst any, maxBytes int64) error {
	if r == nil || r.Body == nil || r.Body == http.NoBody || r.ContentLength == 0 {
		return nil
	}
	if err := platform.DecodeJSON(r, dst, maxBytes); err != nil {
		if errors.Is(err, platform.ErrBodyTooLarge) {
			return domain.NewInvalid("body", domain.ReasonInvalidArgument, "request body is too large")
		}
		return domain.NewInvalid("body", domain.ReasonInvalidArgument,
			"request body is not a valid JSON object")
	}
	return nil
}

// actorID returns the authenticated platform user's id. Delivery routes always
// run behind the identity middleware; an absent user yields 0.
func actorID(ctx context.Context) int64 {
	user, ok := identityhttp.UserFromContext(ctx)
	if !ok {
		return 0
	}
	return user.ID
}

// requireBatch loads the batch of versionID and maps an unknown or foreign batch
// to a 404 batch_not_found. When terminal is true a non-terminal batch is a 409
// analysis_not_applicable (the only delivery endpoint with that precondition). A
// missing reader is a wiring error, never a silent bypass of the ownership gate.
func requireBatch(ctx context.Context, batches BatchReader, versionID, batchID int64, terminal bool) error {
	if batches == nil {
		return errors.New("delivery: batch reader is not configured")
	}
	batch, err := batches.Get(ctx, versionID, batchID)
	if err != nil {
		if errors.Is(err, executionapp.ErrBatchNotFound) {
			return domain.NewNotFound("batch", batchID)
		}
		return err
	}
	if batch == nil || (batch.VersionID != 0 && batch.VersionID != versionID) || batch.ID != batchID {
		return domain.NewNotFound("batch", batchID)
	}
	if terminal && !isTerminalBatchStatus(batch.Status) {
		return domain.NewAnalysisNotApplicable(
			"batch " + strconv.FormatInt(batchID, 10) + " is " + string(batch.Status) + ", not terminal")
	}
	return nil
}

// isTerminalBatchStatus reports whether a run_batch status is terminal.
func isTerminalBatchStatus(status executiondomain.BatchStatus) bool {
	switch status {
	case executiondomain.BatchStatusDone, executiondomain.BatchStatusFailed,
		executiondomain.BatchStatusCancelled:
		return true
	default:
		return false
	}
}

// ---------------------------------------------------------------------------
// Error mapping
// ---------------------------------------------------------------------------

// writeDeliveryError maps a delivery domain error (or a transport error) onto the
// shared error envelope, always carrying the stable details.reason of spec §7.11.
func writeDeliveryError(w http.ResponseWriter, r *http.Request, err error) {
	if err == nil {
		platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
		return
	}

	var domainErr *domain.Error
	if errors.As(err, &domainErr) && domainErr != nil {
		status, code := deliveryErrorStatus(domainErr)
		retryable := status == http.StatusInternalServerError && domainErr.Reason == domain.ReportReasonExportFailed
		message := domainErr.Message
		if message == "" {
			message = http.StatusText(status)
		}
		details := map[string]any{"reason": domainErr.Reason}
		if domainErr.Field != "" {
			details["field"] = domainErr.Field
		}
		platform.WriteError(w, r, status, code, message,
			platform.WithDetails(details), platform.WithRetryable(retryable))
		return
	}

	if errors.Is(err, executionapp.ErrBatchNotFound) {
		writeBatchNotFound(w, r)
		return
	}

	platform.WriteError(w, r, http.StatusInternalServerError, platform.CodeInternal, "internal server error")
}

// deliveryErrorStatus maps a delivery domain error kind onto the HTTP status and
// envelope code. The export failure is the only 500 reason of the context.
func deliveryErrorStatus(err *domain.Error) (int, string) {
	switch err.Kind {
	case domain.KindInvalid:
		return http.StatusUnprocessableEntity, platform.CodeInvalidArgument
	case domain.KindNotFound:
		return http.StatusNotFound, platform.CodeNotFound
	case domain.KindConflict, domain.KindStateConflict:
		return http.StatusConflict, platform.CodeConflict
	case "internal":
		return http.StatusInternalServerError, platform.CodeInternal
	default:
		return http.StatusInternalServerError, platform.CodeInternal
	}
}

// writeBatchNotFound writes the 404 of an unknown or foreign execution batch.
func writeBatchNotFound(w http.ResponseWriter, r *http.Request) {
	message := "batch not found"
	platform.WriteError(w, r, http.StatusNotFound, platform.CodeNotFound, message,
		platform.WithDetails(map[string]any{"reason": "batch_not_found"}))
}

// ---------------------------------------------------------------------------
// Small shared helpers
// ---------------------------------------------------------------------------

// formatID renders one id as the decimal string used by the JSON boundary (D29).
func formatID(id int64) string { return strconv.FormatInt(id, 10) }

// formatTime renders a timestamp as RFC3339 in UTC (spec §7.1).
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// formatOptionalTime renders an optional timestamp as RFC3339 in UTC.
func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	rendered := formatTime(*value)
	return &rendered
}

// formatOptionalID renders an optional id, or null.
func formatOptionalID(id *int64) *string {
	if id == nil {
		return nil
	}
	rendered := formatID(*id)
	return &rendered
}

// formatOptionalString renders an optional non-empty string, or null.
func formatOptionalString(value string) *string {
	if value == "" {
		return nil
	}
	rendered := value
	return &rendered
}

// stringInSlice reports whether value is one of the allowed enum values.
func stringInSlice(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

// optionalString trims the query value and returns "" for whitespace-only input.
func optionalString(raw string) string {
	return strings.TrimSpace(raw)
}
