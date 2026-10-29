package httpapi

import "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"

// ---------------------------------------------------------------------------
// Issue DTOs (spec §7.3)
// ---------------------------------------------------------------------------

// issueRefDTO is one durable issue reference. Both ids are decimal strings
// (a commit ref keeps its 40-hex form).
type issueRefDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
}

// issueCodeRefDTO is one code anchor of an issue. Anchors are populated by the
// codeintel adapter (T17); until it is wired the array is empty.
type issueCodeRefDTO struct {
	AnchorID string `json:"anchorId"`
	Path     string `json:"path"`
	Line     int    `json:"line"`
	Kind     string `json:"kind"`
	Label    string `json:"label"`
}

// issueCaseSummaryDTO is the lightweight case summary of the issue detail.
type issueCaseSummaryDTO struct {
	CaseRunID  *string `json:"caseRunId"`
	CaseNodeID *string `json:"caseNodeId"`
}

// issueBatchSummaryDTO is the lightweight batch summary of the issue detail.
type issueBatchSummaryDTO struct {
	ID string `json:"id"`
}

// issueRequirementSummaryDTO is the lightweight requirement summary of the issue
// detail.
type issueRequirementSummaryDTO struct {
	ID string `json:"id"`
}

// issueSummaryDTO is the issue list item. Every id is a decimal string; bodies
// are limited to the fields the spec allows and never include evidence content.
type issueSummaryDTO struct {
	ID              string            `json:"id"`
	VersionID       string            `json:"versionId"`
	BatchID         *string           `json:"batchId"`
	CaseRunID       *string           `json:"caseRunId"`
	CaseNodeID      *string           `json:"caseNodeId"`
	StepResultID    *string           `json:"stepResultId"`
	Source          string            `json:"source"`
	Category        string            `json:"category"`
	Severity        string            `json:"severity"`
	Status          string            `json:"status"`
	Confidence      string            `json:"confidence"`
	FailureClass    *string           `json:"failureClass"`
	Title           string            `json:"title"`
	RerunSuggestion string            `json:"rerunSuggestion"`
	RecurrenceCount int               `json:"recurrenceCount"`
	Revision        int               `json:"revision"`
	Refs            []issueRefDTO     `json:"refs"`
	CodeRefs        []issueCodeRefDTO `json:"codeRefs"`
	DismissedReason string            `json:"dismissedReason"`
	CreatedBy       string            `json:"createdBy"`
	ConfirmedBy     *string           `json:"confirmedBy"`
	ConfirmedAt     *string           `json:"confirmedAt"`
	DismissedBy     *string           `json:"dismissedBy"`
	DismissedAt     *string           `json:"dismissedAt"`
	CreatedAt       string            `json:"createdAt"`
	UpdatedAt       string            `json:"updatedAt"`
}

// issueDTO is the issue detail: the summary plus the long bodies and the
// case/batch/requirement summaries that save the front end a second request.
type issueDTO struct {
	issueSummaryDTO
	Description string                      `json:"description"`
	ReproSteps  string                      `json:"reproSteps"`
	Expected    string                      `json:"expected"`
	Actual      string                      `json:"actual"`
	Case        *issueCaseSummaryDTO        `json:"case"`
	Requirement *issueRequirementSummaryDTO `json:"requirement"`
	Batch       *issueBatchSummaryDTO       `json:"batch"`
}

// newIssueCodeRefDTOs maps the optional codeintel anchors to the codeRefs array
// (never nil: an unwired or failing reader yields []).
func newIssueCodeRefDTOs(refs []IssueCodeRef) []issueCodeRefDTO {
	out := make([]issueCodeRefDTO, 0, len(refs))
	for _, ref := range refs {
		out = append(out, issueCodeRefDTO{
			AnchorID: formatID(ref.AnchorID),
			Path:     ref.Path,
			Line:     ref.Line,
			Kind:     ref.Kind,
			Label:    ref.Label,
		})
	}
	return out
}

// newIssueSummaryDTO maps one issue row, its refs and code anchors to the list
// shape.
func newIssueSummaryDTO(issue domain.Issue, refs []domain.IssueRef, codeRefs []IssueCodeRef) issueSummaryDTO {
	refDTOs := make([]issueRefDTO, 0, len(refs))
	for _, ref := range refs {
		refDTOs = append(refDTOs, issueRefDTO{Kind: ref.RefKind, ID: ref.RefID})
	}
	return issueSummaryDTO{
		ID:              formatID(issue.ID),
		VersionID:       formatID(issue.VersionID),
		BatchID:         formatOptionalID(issue.BatchID),
		CaseRunID:       formatOptionalID(issue.CaseRunID),
		CaseNodeID:      formatOptionalID(issue.CaseNodeID),
		StepResultID:    formatOptionalID(issue.StepResultID),
		Source:          issue.Source,
		Category:        issue.Category,
		Severity:        issue.Severity,
		Status:          issue.Status,
		Confidence:      issue.Confidence,
		FailureClass:    formatOptionalString(issue.FailureClass),
		Title:           issue.Title,
		RerunSuggestion: issue.RerunSuggestion,
		RecurrenceCount: issue.RecurrenceCount,
		Revision:        issue.Revision,
		Refs:            refDTOs,
		CodeRefs:        newIssueCodeRefDTOs(codeRefs),
		DismissedReason: issue.DismissedReason,
		CreatedBy:       formatID(issue.CreatedBy),
		ConfirmedBy:     formatOptionalID(issue.ConfirmedBy),
		ConfirmedAt:     formatOptionalTime(issue.ConfirmedAt),
		DismissedBy:     formatOptionalID(issue.DismissedBy),
		DismissedAt:     formatOptionalTime(issue.DismissedAt),
		CreatedAt:       formatTime(issue.CreatedAt),
		UpdatedAt:       formatTime(issue.UpdatedAt),
	}
}

// newIssueDTO maps one issue row and its refs to the detail shape.
func newIssueDTO(issue domain.Issue, refs []domain.IssueRef, codeRefs []IssueCodeRef) issueDTO {
	return issueDTO{
		issueSummaryDTO: newIssueSummaryDTO(issue, refs, codeRefs),
		Description:     issue.Description,
		ReproSteps:      issue.ReproSteps,
		Expected:        issue.Expected,
		Actual:          issue.Actual,
		Case:            issueCaseSummary(issue, refs),
		Requirement:     issueRequirementSummary(refs),
		Batch:           issueBatchSummary(issue, refs),
	}
}

// issueCaseSummary derives the case summary from the issue links and refs.
func issueCaseSummary(issue domain.Issue, refs []domain.IssueRef) *issueCaseSummaryDTO {
	caseRunID := issue.CaseRunID
	caseNodeID := issue.CaseNodeID
	for _, ref := range refs {
		switch ref.RefKind {
		case domain.RefKindCaseRun:
			if caseRunID == nil {
				if id, err := parseDecimalID("caseRunId", ref.RefID); err == nil {
					caseRunID = &id
				}
			}
		case domain.RefKindCaseNode:
			if caseNodeID == nil {
				if id, err := parseDecimalID("caseNodeId", ref.RefID); err == nil {
					caseNodeID = &id
				}
			}
		}
	}
	if caseRunID == nil && caseNodeID == nil {
		return nil
	}
	return &issueCaseSummaryDTO{
		CaseRunID:  formatOptionalID(caseRunID),
		CaseNodeID: formatOptionalID(caseNodeID),
	}
}

// issueBatchSummary derives the batch summary from the issue row and refs.
func issueBatchSummary(issue domain.Issue, refs []domain.IssueRef) *issueBatchSummaryDTO {
	if issue.BatchID != nil {
		return &issueBatchSummaryDTO{ID: formatID(*issue.BatchID)}
	}
	for _, ref := range refs {
		if ref.RefKind != domain.RefKindBatch {
			continue
		}
		if id, err := parseDecimalID("batchId", ref.RefID); err == nil {
			return &issueBatchSummaryDTO{ID: formatID(id)}
		}
	}
	return nil
}

// issueRequirementSummary derives the first requirement summary from the refs.
func issueRequirementSummary(refs []domain.IssueRef) *issueRequirementSummaryDTO {
	for _, ref := range refs {
		if ref.RefKind != domain.RefKindRequirement {
			continue
		}
		if id, err := parseDecimalID("requirementId", ref.RefID); err == nil {
			return &issueRequirementSummaryDTO{ID: formatID(id)}
		}
	}
	return nil
}

// issueDeepLinkDTO is the deep link of the global issue centre (spec §7.10).
type issueDeepLinkDTO struct {
	VersionID string `json:"versionId"`
	Tab       string `json:"tab"`
	IssueID   string `json:"issueId"`
}

// globalIssueDTO is one row of GET /api/issues.
type globalIssueDTO struct {
	issueSummaryDTO
	VersionName string           `json:"versionName"`
	DeepLink    issueDeepLinkDTO `json:"deepLink"`
}

// newGlobalIssueDTO maps one global issue row and its code anchors.
func newGlobalIssueDTO(row GlobalIssueRow, codeRefs []IssueCodeRef) globalIssueDTO {
	return globalIssueDTO{
		issueSummaryDTO: newIssueSummaryDTO(row.Issue, row.Refs, codeRefs),
		VersionName:     row.VersionName,
		DeepLink: issueDeepLinkDTO{
			VersionID: formatID(row.Issue.VersionID),
			Tab:       "issues",
			IssueID:   formatID(row.Issue.ID),
		},
	}
}

// ---------------------------------------------------------------------------
// Report DTOs (spec §7.4)
// ---------------------------------------------------------------------------

// reportMarkdownDTO is the Markdown artifact summary. It exposes the API
// download path only, never the stored filesystem path (D30).
type reportMarkdownDTO struct {
	SHA256       string `json:"sha256"`
	SizeBytes    int64  `json:"sizeBytes"`
	DownloadPath string `json:"downloadPath"`
}

// reportSummaryDTO is the report list item (no content, no stored path).
type reportSummaryDTO struct {
	ID               string            `json:"id"`
	VersionID        string            `json:"versionId"`
	BatchID          string            `json:"batchId"`
	Revision         int               `json:"revision"`
	Status           string            `json:"status"`
	Title            string            `json:"title"`
	InputFingerprint string            `json:"inputFingerprint"`
	Markdown         reportMarkdownDTO `json:"markdown"`
	StaleReason      string            `json:"staleReason"`
	PublishedAt      *string           `json:"publishedAt"`
	CreatedAt        string            `json:"createdAt"`
	UpdatedAt        string            `json:"updatedAt"`
}

// reportDTO is the report detail: the summary plus the deterministic snapshot.
type reportDTO struct {
	reportSummaryDTO
	BatchNo   int                  `json:"batchNo"`
	BatchKind string               `json:"batchKind"`
	Content   domain.ReportContent `json:"content"`
}

// reportDeepLinkDTO is the deep link of the global report centre (spec §7.10).
type reportDeepLinkDTO struct {
	VersionID string `json:"versionId"`
	Tab       string `json:"tab"`
	ReportID  string `json:"reportId"`
}

// globalReportDTO is one row of GET /api/reports.
type globalReportDTO struct {
	reportSummaryDTO
	VersionName string            `json:"versionName"`
	BatchNo     int               `json:"batchNo"`
	DeepLink    reportDeepLinkDTO `json:"deepLink"`
}

// newReportSummaryDTO maps one report row to the list shape.
func newReportSummaryDTO(report domain.Report) reportSummaryDTO {
	return reportSummaryDTO{
		ID:               formatID(report.ID),
		VersionID:        formatID(report.VersionID),
		BatchID:          formatID(report.BatchID),
		Revision:         report.Revision,
		Status:           report.Status,
		Title:            report.Title,
		InputFingerprint: report.InputFingerprint,
		Markdown: reportMarkdownDTO{
			SHA256:       report.MarkdownSHA256,
			SizeBytes:    report.MarkdownSize,
			DownloadPath: reportDownloadPath(report.VersionID, report.ID),
		},
		StaleReason: report.StaleReason,
		PublishedAt: formatOptionalTime(report.PublishedAt),
		CreatedAt:   formatTime(report.CreatedAt),
		UpdatedAt:   formatTime(report.UpdatedAt),
	}
}

// newReportDTO maps one report row to the detail shape.
func newReportDTO(report domain.Report) reportDTO {
	return reportDTO{
		reportSummaryDTO: newReportSummaryDTO(report),
		BatchNo:          report.Content.Batch.BatchNo,
		BatchKind:        report.Content.Batch.Kind,
		Content:          report.Content,
	}
}

// newGlobalReportDTO maps one global report row.
func newGlobalReportDTO(row GlobalReportRow) globalReportDTO {
	return globalReportDTO{
		reportSummaryDTO: newReportSummaryDTO(row.Report),
		VersionName:      row.VersionName,
		BatchNo:          row.BatchNo,
		DeepLink: reportDeepLinkDTO{
			VersionID: formatID(row.Report.VersionID),
			Tab:       "report",
			ReportID:  formatID(row.Report.ID),
		},
	}
}

// reportDownloadPath builds the API download path of a report revision.
func reportDownloadPath(versionID, reportID int64) string {
	return "/api/versions/" + formatID(versionID) + "/reports/" + formatID(reportID) +
		"/export?format=markdown"
}

// ---------------------------------------------------------------------------
// Report diff DTOs (spec §7.4)
// ---------------------------------------------------------------------------

// reportDiffIssueDTO is one added or fixed issue of the diff.
type reportDiffIssueDTO struct {
	IssueID  string `json:"issueId"`
	Title    string `json:"title"`
	Severity string `json:"severity"`
}

// reportDiffCaseDTO is one regressed, recovered or changed case of the diff.
type reportDiffCaseDTO struct {
	CaseNodeID string `json:"caseNodeId"`
	Title      string `json:"title"`
	From       string `json:"from"`
	To         string `json:"to"`
}

// reportDiffNewCaseDTO is one new or removed case of the diff.
type reportDiffNewCaseDTO struct {
	CaseNodeID string `json:"caseNodeId"`
	Title      string `json:"title"`
	Source     string `json:"source"`
}

// reportDiffDTO is the difference between two report revisions.
type reportDiffDTO struct {
	BaseReportID   string                   `json:"baseReportId"`
	TargetReportID string                   `json:"targetReportId"`
	BaseBatchNo    int                      `json:"baseBatchNo"`
	TargetBatchNo  int                      `json:"targetBatchNo"`
	Summary        domain.ReportDiffSummary `json:"summary"`
	AddedIssues    []reportDiffIssueDTO     `json:"addedIssues"`
	FixedIssues    []reportDiffIssueDTO     `json:"fixedIssues"`
	RegressedCases []reportDiffCaseDTO      `json:"regressedCases"`
	RecoveredCases []reportDiffCaseDTO      `json:"recoveredCases"`
	ChangedCases   []reportDiffCaseDTO      `json:"changedCases"`
	NewCases       []reportDiffNewCaseDTO   `json:"newCases"`
	RemovedCases   []reportDiffNewCaseDTO   `json:"removedCases"`
}

// newReportDiffDTO renders the diff with decimal-string ids (the domain type is
// internal and keeps int64 ids).
func newReportDiffDTO(diff domain.ReportDiff) reportDiffDTO {
	return reportDiffDTO{
		BaseReportID:   formatID(diff.BaseReportID),
		TargetReportID: formatID(diff.TargetReportID),
		BaseBatchNo:    diff.BaseBatchNo,
		TargetBatchNo:  diff.TargetBatchNo,
		Summary:        diff.Summary,
		AddedIssues:    newReportDiffIssueDTOs(diff.AddedIssues),
		FixedIssues:    newReportDiffIssueDTOs(diff.FixedIssues),
		RegressedCases: newReportDiffCaseDTOs(diff.RegressedCases),
		RecoveredCases: newReportDiffCaseDTOs(diff.RecoveredCases),
		ChangedCases:   newReportDiffCaseDTOs(diff.ChangedCases),
		NewCases:       newReportDiffNewCaseDTOs(diff.NewCases),
		RemovedCases:   newReportDiffNewCaseDTOs(diff.RemovedCases),
	}
}

// newReportDiffIssueDTOs maps issue rows (never nil).
func newReportDiffIssueDTOs(rows []domain.ReportDiffIssue) []reportDiffIssueDTO {
	out := make([]reportDiffIssueDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportDiffIssueDTO{
			IssueID: formatID(row.IssueID), Title: row.Title, Severity: row.Severity,
		})
	}
	return out
}

// newReportDiffCaseDTOs maps case rows (never nil).
func newReportDiffCaseDTOs(rows []domain.ReportDiffCase) []reportDiffCaseDTO {
	out := make([]reportDiffCaseDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportDiffCaseDTO{
			CaseNodeID: formatID(row.CaseNodeID), Title: row.Title, From: row.From, To: row.To,
		})
	}
	return out
}

// newReportDiffNewCaseDTOs maps new/removed cases (never nil).
func newReportDiffNewCaseDTOs(rows []domain.ReportDiffNewCase) []reportDiffNewCaseDTO {
	out := make([]reportDiffNewCaseDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, reportDiffNewCaseDTO{
			CaseNodeID: formatID(row.CaseNodeID), Title: row.Title, Source: row.Source,
		})
	}
	return out
}

// ---------------------------------------------------------------------------
// Analysis DTOs (spec §7.5)
// ---------------------------------------------------------------------------

// newAnalysisDTO maps one run to the JSON-safe projection. The input document
// (which carries candidate summaries) and any prompt text are never exposed.
func newAnalysisDTO(run domain.AnalysisRun) domain.AnalysisView {
	return domain.NewAnalysisView(run)
}

// newAnalysisDTOs maps a run slice to a non-nil view slice.
func newAnalysisDTOs(runs []domain.AnalysisRun) []domain.AnalysisView {
	out := make([]domain.AnalysisView, 0, len(runs))
	for _, run := range runs {
		out = append(out, newAnalysisDTO(run))
	}
	return out
}
