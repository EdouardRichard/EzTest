// Package deliveryapi exposes the P4 delivery REST API (spec §4.2): the
// version-scoped issue lifecycle and the report/HTML download surface. Handlers
// only parse the wire DTOs, call the delivery application services and map the
// stable platform errors; no business rule or SQL lives here.
package deliveryapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
)

// Service is the issue application surface the handlers consume. It is
// implemented by *deliveryapp.IssueService; the handler never touches a
// repository directly.
type Service interface {
	List(ctx context.Context, versionID int64, f deliveryapp.IssueFilter) ([]delivery.Issue, error)
	Get(ctx context.Context, versionID, issueID int64) (deliveryapp.IssueDetail, error)
	Confirm(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error)
	Dismiss(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error)
	Reopen(ctx context.Context, versionID, issueID, actorID int64, reason string, expected *int32) (delivery.Issue, error)
	Merge(ctx context.Context, versionID, sourceID, targetID, actorID int64, reason string, expected *int32) (delivery.Issue, error)
}

var _ Service = (*deliveryapp.IssueService)(nil)

// Handler serves the P4 delivery endpoints.
type Handler struct {
	issues  Service
	reports ReportService
}

// Option configures the optional delivery application surfaces.
type Option func(*Handler)

// WithReports attaches the report application service. It is optional so an
// assembly that has not wired the report feature keeps a compiling handler;
// the report routes then answer 500 internal instead of panicking.
func WithReports(reports ReportService) Option {
	return func(handler *Handler) { handler.reports = reports }
}

// New builds the delivery handler for the issue surface. Reports are attached
// with WithReports.
func New(issues Service, opts ...Option) *Handler {
	handler := &Handler{issues: issues}
	for _, opt := range opts {
		if opt != nil {
			opt(handler)
		}
	}
	return handler
}

// issueConflictFallback maps a generic application state conflict (CAS
// mismatch or repository guard) to the endpoint-specific stable subcode.
const (
	confirmFallbackReason = deliveryapp.IssueConflictNotOpen
	dismissFallbackReason = deliveryapp.IssueConflictNotOpen
	reopenFallbackReason  = deliveryapp.IssueConflictNotReopenable
	mergeFallbackReason   = delivery.MergeReasonTargetInvalid
)

/* ---------------------------------- list ---------------------------------- */

// ListIssues GET /api/versions/{versionId}/issues.
func (h *Handler) ListIssues(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	query := r.URL.Query()
	status, err := optionalEnum("status", query.Get("status"),
		"open", "confirmed", "dismissed", "merged")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	category, err := optionalEnum("category", query.Get("category"), "ui", "api", "db")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	severity, err := optionalEnum("severity", query.Get("severity"), "major", "minor", "trivial")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionSUTID, err := optionalQueryID(r, "version_sut_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, before, err := issuePageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}

	filter := deliveryapp.IssueFilter{
		Status:       status,
		Category:     category,
		Severity:     severity,
		VersionSUTID: versionSUTID,
		Before:       before,
		Limit:        limit + 1,
	}
	if filter.Limit > pagination.MaxLimit {
		filter.Limit = pagination.MaxLimit
	}
	issues, err := h.issues.List(r.Context(), versionID, filter)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, ""))
		return
	}

	hasMore := len(issues) > limit
	if hasMore {
		issues = issues[:limit]
	} else if limit == pagination.MaxLimit && len(issues) == limit {
		// The page is exactly full at the shared maximum: probe one row past
		// the cursor so has_more stays exact without exceeding the 100 cap.
		probe := filter
		probe.Before = issues[len(issues)-1].ID
		probe.Limit = 1
		next, probeErr := h.issues.List(r.Context(), versionID, probe)
		if probeErr != nil {
			httpx.Error(w, r, mapIssueError(probeErr, ""))
			return
		}
		hasMore = len(next) > 0
	}

	items := make([]IssueView, 0, len(issues))
	for _, issue := range issues {
		view, viewErr := h.issueView(r.Context(), versionID, issue)
		if viewErr != nil {
			httpx.Error(w, r, viewErr)
			return
		}
		items = append(items, view)
	}
	nextCursor := ""
	if hasMore && len(items) > 0 {
		nextCursor = platformid.Format(issues[len(issues)-1].ID)
	}
	httpx.List(w, http.StatusOK, items, nextCursor, hasMore)
}

// GetIssue GET /api/versions/{versionId}/issues/{issueId}.
func (h *Handler) GetIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId", "issueId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	detail, err := h.issues.Get(r.Context(), versionID, issueID)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, ""))
		return
	}
	history := make([]IssueHistoryView, 0, len(detail.History))
	for _, change := range detail.History {
		history = append(history, toIssueHistoryView(change))
	}
	httpx.JSON(w, http.StatusOK, IssueDetailView{
		IssueView:   toIssueView(detail.Issue, detail.Refs, detail.Triage),
		History:     history,
		RelatedRefs: relatedRefCounts(detail.Refs),
	})
}

/* -------------------------------- actions --------------------------------- */

// ConfirmIssue POST /api/versions/{versionId}/issues/{issueId}/confirm.
func (h *Handler) ConfirmIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId", "issueId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request issueActionRequest
	if err := decodeBody(w, r, &request, false); err != nil {
		httpx.Error(w, r, err)
		return
	}
	action, err := request.parse(false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issue, err := h.issues.Confirm(r.Context(), versionID, issueID, actorID, action.Reason, action.Expected)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, confirmFallbackReason))
		return
	}
	httpx.JSON(w, http.StatusOK, h.actionIssueView(r.Context(), versionID, issue))
}

// DismissIssue POST /api/versions/{versionId}/issues/{issueId}/dismiss.
func (h *Handler) DismissIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId", "issueId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request issueActionRequest
	if err := decodeBody(w, r, &request, false); err != nil {
		httpx.Error(w, r, err)
		return
	}
	action, err := request.parse(false)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issue, err := h.issues.Dismiss(r.Context(), versionID, issueID, actorID, action.Reason, action.Expected)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, dismissFallbackReason))
		return
	}
	httpx.JSON(w, http.StatusOK, h.actionIssueView(r.Context(), versionID, issue))
}

// ReopenIssue POST /api/versions/{versionId}/issues/{issueId}/reopen. The
// reason is mandatory and bounded by the contract (1..2000).
func (h *Handler) ReopenIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId", "issueId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request issueActionRequest
	if err := decodeBody(w, r, &request, true); err != nil {
		httpx.Error(w, r, err)
		return
	}
	action, err := request.parse(true)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issue, err := h.issues.Reopen(r.Context(), versionID, issueID, actorID, action.Reason, action.Expected)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, reopenFallbackReason))
		return
	}
	httpx.JSON(w, http.StatusOK, h.actionIssueView(r.Context(), versionID, issue))
}

// MergeIssue POST /api/versions/{versionId}/issues/{issueId}/merge.
func (h *Handler) MergeIssue(w http.ResponseWriter, r *http.Request) {
	if h.issues == nil {
		writeNotConfigured(w, r, "issue service")
		return
	}
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := pathID(r, "versionId", "versionId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	sourceID, err := pathID(r, "issueId", "issueId")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request mergeIssueRequest
	if err := decodeBody(w, r, &request, true); err != nil {
		httpx.Error(w, r, err)
		return
	}
	targetID, action, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	issue, err := h.issues.Merge(r.Context(), versionID, sourceID, targetID, actorID, action.Reason, action.Expected)
	if err != nil {
		httpx.Error(w, r, mapIssueError(err, mergeFallbackReason))
		return
	}
	httpx.JSON(w, http.StatusOK, h.actionIssueView(r.Context(), versionID, issue))
}

/* --------------------------------- helpers -------------------------------- */

// issueView loads the refs/triage projection of one list row. A row deleted
// between the list and the enrichment read degrades to an empty refs list
// instead of failing the whole page.
func (h *Handler) issueView(ctx context.Context, versionID int64, issue delivery.Issue) (IssueView, error) {
	detail, err := h.issues.Get(ctx, versionID, issue.ID)
	if err != nil {
		if errors.Is(err, deliveryapp.ErrNotFound) {
			return toIssueView(issue, nil, nil), nil
		}
		return IssueView{}, mapIssueError(err, "")
	}
	projected := issue
	if detail.Issue.ID > 0 {
		projected = detail.Issue
	}
	return toIssueView(projected, detail.Refs, detail.Triage), nil
}

// actionIssueView enriches an action response with refs/triage. The write has
// already committed, so an enrichment failure must not turn a successful
// transition into a 500: the base view is returned instead.
func (h *Handler) actionIssueView(ctx context.Context, versionID int64, issue delivery.Issue) IssueView {
	if issue.ID <= 0 {
		return toIssueView(issue, nil, nil)
	}
	detail, err := h.issues.Get(ctx, versionID, issue.ID)
	if err != nil || detail.Issue.ID <= 0 {
		return toIssueView(issue, nil, nil)
	}
	return toIssueView(detail.Issue, detail.Refs, detail.Triage)
}

// requireActor reads the authenticated caller injected by the server
// middleware.
func requireActor(r *http.Request) (int64, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return 0, platform.Unauthenticated()
	}
	return actor.ID, nil
}

// mapIssueError maps the delivery application/domain sentinels onto the stable
// API envelope. fallbackReason is the endpoint-specific state_conflict subcode
// used when the application only reports the generic sentinel (CAS mismatch).
func mapIssueError(err error, fallbackReason string) error {
	if err == nil {
		return nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	var mergeErr *delivery.MergeError
	if errors.As(err, &mergeErr) {
		return issueStateConflict(mergeErr.Reason)
	}
	var stateErr *deliveryapp.IssueStateConflictError
	if errors.As(err, &stateErr) {
		return issueStateConflict(stateErr.Reason)
	}
	switch {
	case errors.Is(err, deliveryapp.ErrNotFound):
		return platform.NotFound("issue")
	case errors.Is(err, deliveryapp.ErrInvalidArgument):
		return invalidArgumentError(err)
	case errors.Is(err, deliveryapp.ErrStateConflict):
		if fallbackReason != "" {
			return issueStateConflict(fallbackReason)
		}
		return platform.StateConflict("issue is not in a valid state")
	default:
		return err
	}
}

// issueStateConflict builds a 409 state_conflict carrying the stable
// details.reason subcode of spec §5.2.
func issueStateConflict(reason string) error {
	message := "issue is not in a valid state for this operation"
	switch reason {
	case deliveryapp.IssueConflictNotOpen:
		message = "issue is not open"
	case deliveryapp.IssueConflictNotReopenable:
		message = "issue is not reopenable"
	case delivery.MergeReasonTargetInvalid:
		message = "merge target is invalid"
	case delivery.MergeReasonCycle:
		message = "merge would create a cycle"
	}
	apiErr := platform.StateConflict(message)
	if reason != "" {
		apiErr.Details = map[string]any{"reason": reason}
	}
	return apiErr
}

// invalidArgumentError strips the domain sentinel prefix so the envelope
// message stays user-facing.
func invalidArgumentError(err error) error {
	message := strings.TrimPrefix(err.Error(), deliveryapp.ErrInvalidArgument.Error()+": ")
	return platform.NewError(http.StatusBadRequest, "invalid_argument", message)
}
