package httpapi

import (
	"context"
	"log/slog"
	"net/http"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// IssueHandler serves the version-scoped issue endpoints of spec §7.3. It only
// calls the issue application service, the issue-ref reader and the optional
// codeintel anchor reader; ownership and state rules live in the service.
type IssueHandler struct {
	issues   deliveryapp.IssueService
	refs     deliveryapp.IssueRefReader
	codeRefs IssueCodeRefReader
}

// RegisterRoutes mounts the issue routes. Authentication is the server's global
// middleware; every authenticated user may read and write issues (spec §11.1).
func (h *IssueHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/issues", h.List)
	mux.HandleFunc("POST /api/versions/{id}/issues", h.Create)
	mux.HandleFunc("GET /api/versions/{id}/issues/{issueId}", h.Get)
	mux.HandleFunc("PATCH /api/versions/{id}/issues/{issueId}", h.Patch)
}

// issueListQuery is the raw §7.3 filter of GET /versions/{id}/issues.
type issueListQuery struct {
	Status     string
	Severity   string
	Category   string
	BatchID    *int64
	CaseRunID  *int64
	CaseNodeID *int64
	Query      string
	Cursor     int64
	Limit      int
}

// parseIssueListQuery parses and bounds the list query. Cursor and limit are
// validated here so the application service only sees normalized values.
func parseIssueListQuery(r *http.Request) (issueListQuery, error) {
	var query issueListQuery
	values := r.URL.Query()
	query.Status = optionalString(values.Get("status"))
	query.Severity = optionalString(values.Get("severity"))
	query.Category = optionalString(values.Get("category"))
	query.Query = optionalString(values.Get("q"))
	var err error
	if query.BatchID, err = parseOptionalQueryID(r, "batchId"); err != nil {
		return issueListQuery{}, err
	}
	if query.CaseRunID, err = parseOptionalQueryID(r, "caseRunId"); err != nil {
		return issueListQuery{}, err
	}
	if query.CaseNodeID, err = parseOptionalQueryID(r, "caseNodeId"); err != nil {
		return issueListQuery{}, err
	}
	if query.Limit, err = parseLimit(r); err != nil {
		return issueListQuery{}, err
	}
	if query.Cursor, err = parseCursorID(r); err != nil {
		return issueListQuery{}, err
	}
	return query, nil
}

// List returns one page of the version's issues ordered by severity weight then
// id descending. The next cursor is the opaque encoding of the last issue id.
func (h *IssueHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	query, err := parseIssueListQuery(r)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	issues, hasMore, err := h.issues.List(r.Context(), versionID, deliveryapp.IssueFilter{
		Status:     query.Status,
		Severity:   query.Severity,
		Category:   query.Category,
		BatchID:    query.BatchID,
		CaseRunID:  query.CaseRunID,
		CaseNodeID: query.CaseNodeID,
		Query:      query.Query,
		Cursor:     query.Cursor,
		Limit:      query.Limit,
	})
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	items := make([]issueSummaryDTO, 0, len(issues))
	for _, issue := range issues {
		items = append(items, newIssueSummaryDTO(issue,
			h.refsFor(r.Context(), versionID, issue.ID),
			readIssueCodeRefs(r.Context(), h.codeRefs, versionID, issue.ID)))
	}
	next := ""
	if hasMore && len(issues) > 0 {
		next = platform.EncodeCursor(issues[len(issues)-1].ID)
	}
	writePage(w, items, next, hasMore)
}

// createIssueBody is the POST /versions/{id}/issues body. Ids are decimal
// strings (D29).
type createIssueBody struct {
	Title         string  `json:"title"`
	Severity      string  `json:"severity"`
	Description   string  `json:"description"`
	Category      string  `json:"category"`
	Confidence    string  `json:"confidence"`
	CaseRunID     *string `json:"caseRunId"`
	CaseNodeID    *string `json:"caseNodeId"`
	RequirementID *string `json:"requirementId"`
	Expected      string  `json:"expected"`
	Actual        string  `json:"actual"`
	ReproSteps    string  `json:"reproSteps"`
}

// Create creates a human-authored open issue (spec §7.3) and returns 201.
func (h *IssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	var body createIssueBody
	if err := decodeJSON(r, &body, 0); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	cmd := deliveryapp.CreateIssueCommand{
		Title:       body.Title,
		Severity:    body.Severity,
		Description: body.Description,
		Category:    body.Category,
		Confidence:  body.Confidence,
		Expected:    body.Expected,
		Actual:      body.Actual,
		ReproSteps:  body.ReproSteps,
	}
	if cmd.CaseRunID, err = optionalDecimalID("caseRunId", body.CaseRunID); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if cmd.CaseNodeID, err = optionalDecimalID("caseNodeId", body.CaseNodeID); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if cmd.RequirementID, err = optionalDecimalID("requirementId", body.RequirementID); err != nil {
		writeDeliveryError(w, r, err)
		return
	}

	issue, err := h.issues.CreateHuman(r.Context(), versionID, actorID(r.Context()), cmd)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newIssueDTO(issue,
		h.refsFor(r.Context(), versionID, issue.ID),
		readIssueCodeRefs(r.Context(), h.codeRefs, versionID, issue.ID)))
}

// Get returns one issue with its refs and case/batch/requirement summaries.
func (h *IssueHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	issue, err := h.issues.Get(r.Context(), versionID, issueID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	refs, err := h.refsRequired(r.Context(), versionID, issueID)
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newIssueDTO(issue, refs,
		readIssueCodeRefs(r.Context(), h.codeRefs, versionID, issueID)))
}

// patchIssueBody is the PATCH /versions/{id}/issues/{issueId} body.
type patchIssueBody struct {
	Status           string `json:"status"`
	ExpectedRevision *int   `json:"expectedRevision"`
	Comment          string `json:"comment"`
}

// Patch confirms or dismisses an open issue under the revision CAS (spec §7.3).
func (h *IssueHandler) Patch(w http.ResponseWriter, r *http.Request) {
	versionID, err := pathID(r, "id")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	issueID, err := pathID(r, "issueId")
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	var body patchIssueBody
	if err := decodeJSON(r, &body, 0); err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	if !stringInSlice(body.Status, domain.StatusConfirmed, domain.StatusDismissed) {
		writeDeliveryError(w, r, domain.NewInvalid("status", domain.ReasonInvalidArgument,
			`status must be "confirmed" or "dismissed"`))
		return
	}
	if body.ExpectedRevision == nil || *body.ExpectedRevision < 1 {
		writeDeliveryError(w, r, domain.NewInvalid("expectedRevision", domain.ReasonInvalidArgument,
			"expectedRevision must be a positive integer"))
		return
	}

	actor := actorID(r.Context())
	var issue domain.Issue
	if body.Status == domain.StatusConfirmed {
		issue, err = h.issues.Confirm(r.Context(), versionID, issueID, actor, *body.ExpectedRevision, body.Comment)
	} else {
		issue, err = h.issues.Dismiss(r.Context(), versionID, issueID, actor, *body.ExpectedRevision, body.Comment)
	}
	if err != nil {
		writeDeliveryError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newIssueDTO(issue,
		h.refsFor(r.Context(), versionID, issue.ID),
		readIssueCodeRefs(r.Context(), h.codeRefs, versionID, issue.ID)))
}

// readIssueCodeRefs loads the optional codeintel anchors of one issue. A nil
// reader or a read failure degrades to an empty codeRefs array (spec §7.3:
// failures/no anchors return []); the issue payload stays readable.
func readIssueCodeRefs(ctx context.Context, reader IssueCodeRefReader, versionID, issueID int64) []IssueCodeRef {
	if reader == nil {
		return nil
	}
	refs, err := reader.ListIssueCodeRefs(ctx, versionID, issueID)
	if err != nil {
		slog.WarnContext(ctx, "delivery issue code refs read failed",
			"version_id", versionID, "issue_id", issueID, "error", err)
		return nil
	}
	return refs
}

// refsFor loads the issue refs for a response. A nil reader or a read failure
// degrades to an empty refs array; the issue payload stays readable.
func (h *IssueHandler) refsFor(ctx context.Context, versionID, issueID int64) []domain.IssueRef {
	if h.refs == nil {
		return nil
	}
	refs, err := h.refs.ListRefs(ctx, versionID, issueID)
	if err != nil {
		slog.WarnContext(ctx, "delivery issue refs read failed",
			"version_id", versionID, "issue_id", issueID, "error", err)
		return nil
	}
	return refs
}

// refsRequired is the detail read: a refs failure is surfaced because the detail
// contract includes refs.
func (h *IssueHandler) refsRequired(ctx context.Context, versionID, issueID int64) ([]domain.IssueRef, error) {
	if h.refs == nil {
		return nil, nil
	}
	return h.refs.ListRefs(ctx, versionID, issueID)
}
