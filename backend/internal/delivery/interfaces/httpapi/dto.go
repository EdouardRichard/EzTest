package deliveryapi

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

const (
	// maxRequestBody is the shared request-body cap.
	maxRequestBody = 1 << 20
	// maxIssueReasonRunes bounds the issue action reason (contract 1..2000).
	maxIssueReasonRunes = 2000
)

/* -------------------------------- requests -------------------------------- */

// issueActionRequest is the optional body of confirm/dismiss and the shared
// shape of reopen/merge (contract IssueActionRequest).
type issueActionRequest struct {
	Reason           *string `json:"reason"`
	ExpectedRevision *int64  `json:"expected_revision"`
}

// issueAction is the normalized action input handed to the application service.
type issueAction struct {
	Reason   string
	Expected *int32
}

// parse trims and bounds the reason and validates the optional CAS revision.
// reopen requires a non-empty reason; the other transitions accept an empty one.
func (r issueActionRequest) parse(requireReason bool) (issueAction, error) {
	reason := ""
	if r.Reason != nil {
		reason = strings.TrimSpace(*r.Reason)
		if utf8.RuneCountInString(reason) > maxIssueReasonRunes {
			return issueAction{}, platform.Invalid("reason", "must be at most 2000 characters")
		}
	}
	if requireReason && reason == "" {
		return issueAction{}, platform.Invalid("reason", "must not be empty")
	}
	expected, err := parseExpectedRevision(r.ExpectedRevision)
	if err != nil {
		return issueAction{}, err
	}
	return issueAction{Reason: reason, Expected: expected}, nil
}

// parseExpectedRevision converts the optional JSON revision into the service's
// int32 CAS value. A present value must be a positive 32-bit integer.
func parseExpectedRevision(raw *int64) (*int32, error) {
	if raw == nil {
		return nil, nil
	}
	if *raw <= 0 {
		return nil, platform.Invalid("expected_revision", "must be a positive integer")
	}
	if *raw > math.MaxInt32 {
		return nil, platform.Invalid("expected_revision", "must be a 32-bit positive integer")
	}
	value := int32(*raw)
	return &value, nil
}

// mergeIssueRequest is POST .../issues/{issueId}/merge (contract
// MergeIssueRequest).
type mergeIssueRequest struct {
	TargetIssueID    string  `json:"target_issue_id"`
	Reason           *string `json:"reason"`
	ExpectedRevision *int64  `json:"expected_revision"`
}

// parse resolves the required target and the optional reason/revision.
func (r mergeIssueRequest) parse() (int64, issueAction, error) {
	targetID, err := validation.NonEmptyID("target_issue_id", r.TargetIssueID)
	if err != nil {
		return 0, issueAction{}, err
	}
	action, err := issueActionRequest{Reason: r.Reason, ExpectedRevision: r.ExpectedRevision}.parse(false)
	if err != nil {
		return 0, issueAction{}, err
	}
	return targetID, action, nil
}

/* ---------------------------------- views --------------------------------- */

// IssueView is the list/action projection of one issue (contract IssueView).
type IssueView struct {
	ID               string           `json:"id"`
	VersionID        string           `json:"version_id"`
	Title            string           `json:"title"`
	Summary          string           `json:"summary"`
	Status           string           `json:"status"`
	Category         string           `json:"category"`
	Severity         string           `json:"severity"`
	Source           string           `json:"source"`
	ExpectedRevision int32            `json:"expected_revision"`
	MergedIntoID     *string          `json:"merged_into_id"`
	MergedAt         *string          `json:"merged_at"`
	ReopenCount      int              `json:"reopen_count"`
	Refs             []IssueRefView   `json:"refs"`
	Triage           *IssueTriageView `json:"triage"`
	CreatedAt        string           `json:"created_at"`
	UpdatedAt        string           `json:"updated_at"`
}

// IssueRefView is one append-only issue reference (contract IssueRefView).
type IssueRefView struct {
	Kind         string  `json:"kind"`
	RefID        string  `json:"ref_id"`
	VersionSUTID *string `json:"version_sut_id"`
	Role         string  `json:"role"`
}

// IssueTriageView is the latest issue.triage analysis-run projection.
type IssueTriageView struct {
	AnalysisRunID      *string `json:"analysis_run_id"`
	Status             *string `json:"status"`
	Classification     *string `json:"classification"`
	DuplicateOfIssueID *string `json:"duplicate_of_issue_id"`
	Summary            *string `json:"summary"`
}

// IssueHistoryView is one append-only status history entry.
type IssueHistoryView struct {
	FromStatus *string `json:"from_status"`
	ToStatus   string  `json:"to_status"`
	Reason     string  `json:"reason"`
	ActorID    *string `json:"actor_id"`
	ActorKind  string  `json:"actor_kind"`
	CreatedAt  string  `json:"created_at"`
}

// IssueDetailView is IssueView plus history and per-kind related-ref counts.
type IssueDetailView struct {
	IssueView
	History     []IssueHistoryView `json:"history"`
	RelatedRefs map[string]int     `json:"related_refs"`
}

func toIssueView(issue delivery.Issue, refs []delivery.IssueRef, triage *delivery.AnalysisRun) IssueView {
	source := issue.Source
	if source == "" {
		source = delivery.IssueCandidateSource
	}
	view := IssueView{
		ID:               platformid.Format(issue.ID),
		VersionID:        platformid.Format(issue.VersionID),
		Title:            issue.Title,
		Summary:          issue.Summary,
		Status:           string(issue.Status),
		Category:         issue.Category,
		Severity:         issue.Severity,
		Source:           source,
		ExpectedRevision: issue.ExpectedRevision,
		MergedIntoID:     optionalIDString(issue.MergedIntoID),
		MergedAt:         formatOptionalTime(issue.MergedAt),
		ReopenCount:      issue.ReopenCount,
		Refs:             make([]IssueRefView, 0, len(refs)),
		Triage:           toIssueTriageView(triage),
		CreatedAt:        formatTime(issue.CreatedAt),
		UpdatedAt:        formatTime(issue.UpdatedAt),
	}
	for _, ref := range refs {
		view.Refs = append(view.Refs, toIssueRefView(ref))
	}
	return view
}

func toIssueRefView(ref delivery.IssueRef) IssueRefView {
	role := ref.Role
	if role == "" {
		role = delivery.IssueRefRoleSource
	}
	return IssueRefView{
		Kind:         ref.Kind,
		RefID:        platformid.Format(ref.RefID),
		VersionSUTID: optionalIDString(ref.VersionSUTID),
		Role:         role,
	}
}

// toIssueTriageView projects the analysis-run output consumed by the UI. The
// output is an agent proposal JSON object; an absent or malformed output still
// yields the run identity/status with null proposal fields.
func toIssueTriageView(run *delivery.AnalysisRun) *IssueTriageView {
	if run == nil {
		return nil
	}
	view := &IssueTriageView{}
	if run.ID > 0 {
		view.AnalysisRunID = strPtr(platformid.Format(run.ID))
	}
	if status := strings.TrimSpace(string(run.Status)); status != "" {
		view.Status = strPtr(status)
	}
	var output map[string]any
	if len(run.Output) > 0 {
		_ = json.Unmarshal(run.Output, &output)
	}
	view.Classification = payloadText(output, "classification")
	view.DuplicateOfIssueID = payloadDecimalID(output, "duplicate_of_issue_id")
	view.Summary = payloadText(output, "summary")
	if view.Summary == nil {
		// issue.propose has no dedicated summary field; the merge reason is the
		// human-readable explanation of the proposal.
		view.Summary = payloadText(output, "merge_reason")
	}
	return view
}

func toIssueHistoryView(change delivery.IssueStatusChange) IssueHistoryView {
	actorKind := change.ActorKind
	if actorKind == "" {
		actorKind = "system"
	}
	return IssueHistoryView{
		FromStatus: optionalIssueStatus(change.From),
		ToStatus:   string(change.To),
		Reason:     change.Reason,
		ActorID:    optionalIDString(change.ActorID),
		ActorKind:  actorKind,
		CreatedAt:  formatTime(change.CreatedAt),
	}
}

// relatedRefCounts groups the detail refs by kind (related_refs contract).
func relatedRefCounts(refs []delivery.IssueRef) map[string]int {
	counts := make(map[string]int, len(refs))
	for _, ref := range refs {
		if ref.Kind == "" {
			continue
		}
		counts[ref.Kind]++
	}
	return counts
}

func optionalIssueStatus(status *delivery.IssueStatus) *string {
	if status == nil {
		return nil
	}
	return strPtr(string(*status))
}

/* --------------------------------- reports -------------------------------- */

// regenerateReportRequest is POST .../reports/{reportId}/regenerate (contract
// RegenerateReportRequest). The reason is optional and bounded to 2000 runes.
type regenerateReportRequest struct {
	Reason *string `json:"reason"`
}

func (r regenerateReportRequest) parse() error {
	if r.Reason == nil {
		return nil
	}
	reason := strings.TrimSpace(*r.Reason)
	if utf8.RuneCountInString(reason) > maxIssueReasonRunes {
		return platform.Invalid("reason", "must be at most 2000 characters")
	}
	return nil
}

// ReportView is the list/detail report header projection (contract ReportView).
type ReportView struct {
	ID               string              `json:"id"`
	VersionID        string              `json:"version_id"`
	WaveID           *string             `json:"wave_id"`
	Kind             string              `json:"kind"`
	Status           string              `json:"status"`
	InputFingerprint *string             `json:"input_fingerprint"`
	CurrentRevision  *ReportRevisionView `json:"current_revision"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
}

// ReportRevisionView is one append-only revision projection (contract
// ReportRevisionView).
type ReportRevisionView struct {
	ID               string `json:"id"`
	ReportID         string `json:"report_id"`
	Revision         int32  `json:"revision"`
	Status           string `json:"status"`
	AsOf             string `json:"as_of"`
	InputFingerprint string `json:"input_fingerprint"`
	TemplateVersion  string `json:"template_version"`
	RenderStatus     string `json:"render_status"`
	HTMLAvailable    bool   `json:"html_available"`
	PDFAvailable     bool   `json:"pdf_available"`
	CreatedAt        string `json:"created_at"`
}

// ReportRefView is one report revision logical reference.
type ReportRefView struct {
	Kind  string `json:"kind"`
	RefID string `json:"ref_id"`
}

// ReportDetailView is ReportView plus revisions and logical refs.
type ReportDetailView struct {
	ReportView
	Revisions []ReportRevisionView `json:"revisions"`
	Refs      []ReportRefView      `json:"refs"`
}

// RegenerateReportResult is the 202 body of report regeneration.
type RegenerateReportResult struct {
	ReportID   string  `json:"report_id"`
	Revision   int32   `json:"revision"`
	WorkItemID *string `json:"work_item_id"`
}

// reportRenderDone is the render_status of a downloadable revision.
const reportRenderDone = "done"

func toReportView(report delivery.Report, current *delivery.ReportRevision) ReportView {
	view := ReportView{
		ID:               platformid.Format(report.ID),
		VersionID:        platformid.Format(report.VersionID),
		WaveID:           optionalIDString(report.WaveID),
		Kind:             string(report.Kind),
		Status:           string(report.Status),
		InputFingerprint: optionalFingerprint(report.InputFingerprint),
		CreatedAt:        formatTime(report.CreatedAt),
		UpdatedAt:        formatTime(report.UpdatedAt),
	}
	if current != nil {
		revision := toReportRevisionView(*current)
		view.CurrentRevision = &revision
	}
	return view
}

func toReportRevisionView(revision delivery.ReportRevision) ReportRevisionView {
	return ReportRevisionView{
		ID:               platformid.Format(revision.ID),
		ReportID:         platformid.Format(revision.ReportID),
		Revision:         revision.Revision,
		Status:           string(revision.Status),
		AsOf:             formatTime(revision.AsOf),
		InputFingerprint: revision.InputFingerprint,
		TemplateVersion:  revision.TemplateVersion,
		RenderStatus:     revision.RenderStatus,
		HTMLAvailable: revision.Status == delivery.ReportStatusPublished &&
			revision.RenderStatus == reportRenderDone && revision.HTMLRef != nil,
		PDFAvailable: false, // P4 never renders PDF (spec §6.3)
		CreatedAt:    formatTime(revision.CreatedAt),
	}
}

func toReportRefView(ref delivery.ReportRef) ReportRefView {
	return ReportRefView{
		Kind:  ref.Kind,
		RefID: platformid.Format(ref.RefID),
	}
}

// findReportRevision resolves one revision number in a newest-first list.
func findReportRevision(revisions []delivery.ReportRevision, revision int32) (delivery.ReportRevision, bool) {
	for _, candidate := range revisions {
		if candidate.Revision == revision {
			return candidate, true
		}
	}
	return delivery.ReportRevision{}, false
}

// currentReportRevision resolves the report's current revision id.
func currentReportRevision(report delivery.Report, revisions []delivery.ReportRevision) *delivery.ReportRevision {
	if report.CurrentRevisionID == nil || *report.CurrentRevisionID <= 0 {
		return nil
	}
	for index := range revisions {
		if revisions[index].ID == *report.CurrentRevisionID {
			revision := revisions[index]
			return &revision
		}
	}
	return nil
}

func optionalFingerprint(value string) *string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

/* -------------------------------- helpers --------------------------------- */

// pathID parses one positive decimal path parameter.
func pathID(r *http.Request, name, field string) (int64, error) {
	return validation.NonEmptyID(field, r.PathValue(name))
}

// pageLimit parses the shared limit (default 20, capped at 100).
func pageLimit(r *http.Request) int {
	return pagination.Limit(r.URL.Query().Get("limit"), pagination.DefaultLimit, pagination.MaxLimit)
}

// issuePageParams parses the issue list limit and exclusive before cursor.
func issuePageParams(r *http.Request) (int, int64, error) {
	limit := pageLimit(r)
	before, err := parseBefore(r.URL.Query().Get("before"))
	if err != nil {
		return 0, 0, err
	}
	return limit, before, nil
}

// parseBefore parses the optional exclusive before cursor (0 means absent).
func parseBefore(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	value, err := pagination.Cursor(raw)
	if err != nil || value <= 0 {
		return 0, platform.Invalid("before", "must be a positive decimal ID")
	}
	return value, nil
}

// optionalQueryID parses an optional positive decimal query ID filter.
func optionalQueryID(r *http.Request, field string) (*int64, error) {
	raw := strings.TrimSpace(r.URL.Query().Get(field))
	if raw == "" {
		return nil, nil
	}
	value, err := validation.NonEmptyID(field, raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

// optionalEnum validates an optional enum query filter.
func optionalEnum(field, raw string, allowed ...string) (string, error) {
	value := strings.TrimSpace(raw)
	if value == "" {
		return "", nil
	}
	for _, candidate := range allowed {
		if value == candidate {
			return value, nil
		}
	}
	return "", platform.Invalid(field, "must be one of "+strings.Join(allowed, ", "))
}

// decodeBody decodes one JSON request body with the shared size cap. An empty
// body is accepted unless the operation requires a body.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any, required bool) error {
	if r.Body == nil {
		if required {
			return platform.Invalid("body", "request body must be a valid JSON object")
		}
		return nil
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequestBody))
	if err := decoder.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) && !required {
			return nil
		}
		return platform.Invalid("body", "request body must be a valid JSON object")
	}
	return nil
}

// writeNotConfigured reports a missing wired application service.
func writeNotConfigured(w http.ResponseWriter, r *http.Request, kind string) {
	httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", kind+" is not configured"))
}

/* ------------------------------ value helpers ----------------------------- */

func formatTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339)
}

func formatOptionalTime(value *time.Time) *string {
	if value == nil {
		return nil
	}
	return strPtr(formatTime(*value))
}

func optionalIDString(value *int64) *string {
	if value == nil || *value <= 0 {
		return nil
	}
	return strPtr(platformid.Format(*value))
}

func strPtr(value string) *string { return &value }

// payloadText reads a non-empty string field from a decoded JSON object.
func payloadText(payload map[string]any, key string) *string {
	if payload == nil {
		return nil
	}
	value, present := payload[key]
	if !present || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		return &trimmed
	case json.Number:
		return strPtr(typed.String())
	case float64:
		return strPtr(strconv.FormatFloat(typed, 'f', -1, 64))
	default:
		return nil
	}
}

// payloadDecimalID reads a decimal-string ID field; JSON numbers are accepted.
func payloadDecimalID(payload map[string]any, key string) *string {
	if payload == nil {
		return nil
	}
	value, present := payload[key]
	if !present || value == nil {
		return nil
	}
	switch typed := value.(type) {
	case string:
		trimmed := strings.TrimSpace(typed)
		if trimmed == "" {
			return nil
		}
		if _, err := validation.NonEmptyID(key, trimmed); err != nil {
			return nil
		}
		return &trimmed
	case json.Number:
		text := typed.String()
		if _, err := validation.NonEmptyID(key, text); err != nil {
			return nil
		}
		return &text
	case float64:
		if typed <= 0 || typed != math.Trunc(typed) || typed > math.MaxInt64 {
			return nil
		}
		return strPtr(platformid.Format(int64(typed)))
	default:
		return nil
	}
}
