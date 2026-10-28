package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// ReportServiceDeps are the ports of the report use cases. Versions, Execution,
// Graph, Issues, Reports, Files and IDs are mandatory; Events, Audit and Logger
// are optional enrichment (a nil port is skipped). Clock defaults to the wall
// clock.
type ReportServiceDeps struct {
	Versions  ReportVersionReader
	Execution ReportExecutionReader
	Graph     ReportCaseGraphReader
	Issues    ReportIssueReader
	Reports   ReportStore
	Files     ReportFileStore
	Events    ReportEventPublisher
	Audit     ReportAuditRecorder
	IDs       ReportIDGenerator
	Clock     ReportClock
	Logger    *slog.Logger
}

// ReportService implements the report use cases of spec §6.5: deterministic
// generation, read-time stale detection, revision regeneration, diff and export.
// It reads the execution/mindmap/version contexts only through the narrow ports
// in report_ports.go and never touches their tables.
type ReportService struct {
	deps ReportServiceDeps
}

// NewReportService wires the report use cases to their ports.
func NewReportService(deps ReportServiceDeps) *ReportService {
	return &ReportService{deps: deps}
}

// Ensure returns the report of a batch, generating and publishing revision 1
// (or max+1 when the previous revision is stale) when needed. The boolean is
// true only when this call created the revision. A concurrent uniqueness
// conflict is resolved by returning the winning revision.
func (s *ReportService) Ensure(ctx context.Context, versionID, batchID, actor int64) (domain.Report, bool, error) {
	if err := s.ready(); err != nil {
		return domain.Report{}, false, err
	}
	if versionID <= 0 || batchID <= 0 {
		return domain.Report{}, false, reportInvalidID("versionId and batchId must be positive")
	}

	existing, err := s.deps.Reports.LatestByBatch(ctx, versionID, batchID)
	switch {
	case err == nil:
		refreshed, refreshErr := s.refreshReportStale(ctx, existing)
		if refreshErr != nil {
			return domain.Report{}, false, refreshErr
		}
		if refreshed.Status != domain.ReportStatusStale {
			return refreshed, false, nil
		}
	case errors.Is(err, domain.ErrReportNotFound):
		// No report yet: generate revision 1.
	default:
		return domain.Report{}, false, fmt.Errorf("delivery: load report of batch %d: %w", batchID, err)
	}

	generated, err := s.generate(ctx, versionID, batchID, actor)
	if err != nil {
		if errors.Is(err, domain.ErrReportRevisionConflict) {
			latest, latestErr := s.deps.Reports.LatestByBatch(ctx, versionID, batchID)
			if latestErr != nil {
				return domain.Report{}, false, fmt.Errorf("delivery: reload report after conflict: %w", latestErr)
			}
			return latest, false, nil
		}
		return domain.Report{}, false, err
	}
	return generated, true, nil
}

// Regenerate appends a new published revision (max+1) to the batch of reportID.
// When a newer revision already exists that revision is returned unchanged
// (spec §7.4). Old revisions and their Markdown files are never deleted.
func (s *ReportService) Regenerate(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error) {
	if err := s.ready(); err != nil {
		return domain.Report{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.Report{}, reportInvalidID("versionId and reportId must be positive")
	}
	report, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.Report{}, err
	}
	latest, err := s.deps.Reports.LatestByBatch(ctx, versionID, report.BatchID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load latest report of batch %d: %w", report.BatchID, err)
	}
	if latest.Revision > report.Revision {
		return latest, nil
	}
	generated, err := s.generate(ctx, versionID, report.BatchID, actor)
	if err != nil {
		if errors.Is(err, domain.ErrReportRevisionConflict) {
			latest, latestErr := s.deps.Reports.LatestByBatch(ctx, versionID, report.BatchID)
			if latestErr != nil {
				return domain.Report{}, fmt.Errorf("delivery: reload report after conflict: %w", latestErr)
			}
			return latest, nil
		}
		return domain.Report{}, err
	}
	return generated, nil
}

// Get returns one report revision, refreshing its stale state first (spec §5.5).
func (s *ReportService) Get(ctx context.Context, versionID, reportID int64) (domain.Report, error) {
	if err := s.ready(); err != nil {
		return domain.Report{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.Report{}, reportInvalidID("versionId and reportId must be positive")
	}
	report, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.Report{}, err
	}
	return s.refreshReportStale(ctx, report)
}

// List returns one page of the reports of versionID newest first. Published
// reports are refreshed during the read, like Get.
func (s *ReportService) List(ctx context.Context, versionID int64, filter ReportFilter) ([]domain.Report, bool, error) {
	reports, _, hasMore, err := s.listPage(ctx, versionID, filter)
	return reports, hasMore, err
}

// ListPage is List plus the opaque cursor of the next page (the shared
// ReportService.List signature has no cursor return, but spec §7.4 exposes one).
func (s *ReportService) ListPage(ctx context.Context, versionID int64, filter ReportFilter) ([]domain.Report, string, bool, error) {
	return s.listPage(ctx, versionID, filter)
}

// listPage implements the version-scoped report list.
func (s *ReportService) listPage(ctx context.Context, versionID int64, filter ReportFilter) ([]domain.Report, string, bool, error) {
	if err := s.ready(); err != nil {
		return nil, "", false, err
	}
	if versionID <= 0 {
		return nil, "", false, reportInvalidID("versionId must be positive")
	}
	if filter.BatchID != nil && *filter.BatchID <= 0 {
		return nil, "", false, domain.NewInvalid("batchId", domain.ReportReasonInvalidFilter,
			"batchId must be positive")
	}
	if filter.Status != "" && !domain.IsValidReportStatus(filter.Status) {
		return nil, "", false, domain.NewInvalid("status", domain.ReportReasonInvalidFilter,
			fmt.Sprintf("report status %q is invalid", filter.Status))
	}
	if filter.Limit <= 0 {
		filter.Limit = platform.DefaultLimit
	}
	if filter.Limit > platform.MaxLimit {
		filter.Limit = platform.MaxLimit
	}

	reports, cursor, hasMore, err := s.deps.Reports.List(ctx, versionID, filter)
	if err != nil {
		return nil, "", false, fmt.Errorf("delivery: list reports: %w", err)
	}
	refreshed := make([]domain.Report, 0, len(reports))
	for _, report := range reports {
		updated, refreshErr := s.refreshReportStale(ctx, report)
		if refreshErr != nil {
			return nil, "", false, refreshErr
		}
		refreshed = append(refreshed, updated)
	}
	return refreshed, cursor, hasMore, nil
}

// Diff compares reportID with baseReportID, or with the latest report of the
// previous batch (batch_no-1) when baseReportID is nil (spec §5.5/§7.4).
func (s *ReportService) Diff(ctx context.Context, versionID, reportID int64, baseReportID *int64) (domain.ReportDiff, error) {
	if err := s.ready(); err != nil {
		return domain.ReportDiff{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.ReportDiff{}, reportInvalidID("versionId and reportId must be positive")
	}
	target, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.ReportDiff{}, err
	}
	var base domain.Report
	if baseReportID != nil {
		if *baseReportID <= 0 {
			return domain.ReportDiff{}, reportInvalidID("baseReportId must be positive")
		}
		base, err = s.getReport(ctx, versionID, *baseReportID)
	} else {
		var previousBatchID int64
		var ok bool
		previousBatchID, ok, err = s.deps.Execution.PreviousBatchID(ctx, versionID, target.BatchID)
		if err != nil {
			return domain.ReportDiff{}, fmt.Errorf("delivery: resolve previous batch: %w", err)
		}
		if !ok {
			return domain.ReportDiff{}, reportNoPreviousBatch()
		}
		base, err = s.deps.Reports.LatestByBatch(ctx, versionID, previousBatchID)
		if errors.Is(err, domain.ErrReportNotFound) {
			return domain.ReportDiff{}, reportNoPreviousBatch()
		}
	}
	if err != nil {
		return domain.ReportDiff{}, err
	}
	if base.VersionID != target.VersionID {
		return domain.ReportDiff{}, domain.NewInvalid("baseReportId", domain.ReportReasonVersionMismatch,
			"base and target reports belong to different versions")
	}
	diff := domain.BuildReportDiff(base.Content, target.Content)
	diff.BaseReportID = base.ID
	diff.TargetReportID = target.ID
	return diff, nil
}

// Export returns the verified Markdown bytes of one report revision. The stored
// file must match markdown_sha256 and markdown_size; a missing or corrupted file
// is report_export_failed (spec §6.5). The file is never silently regenerated.
func (s *ReportService) Export(ctx context.Context, versionID, reportID int64) (domain.ReportExport, error) {
	if err := s.ready(); err != nil {
		return domain.ReportExport{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.ReportExport{}, reportInvalidID("versionId and reportId must be positive")
	}
	report, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.ReportExport{}, err
	}
	reader, err := s.deps.Files.Open(ctx, report.MarkdownPath)
	if err != nil {
		return domain.ReportExport{}, s.exportFailure(ctx, report.ID, err)
	}
	content, readErr := io.ReadAll(reader)
	closeErr := reader.Close()
	if readErr != nil {
		return domain.ReportExport{}, s.exportFailure(ctx, report.ID, readErr)
	}
	if closeErr != nil {
		return domain.ReportExport{}, s.exportFailure(ctx, report.ID, closeErr)
	}
	sum := sha256.Sum256(content)
	if hex.EncodeToString(sum[:]) != report.MarkdownSHA256 || int64(len(content)) != report.MarkdownSize {
		return domain.ReportExport{}, s.exportFailure(ctx, report.ID, errors.New("sha256/size mismatch"))
	}
	exported := domain.ReportExport{
		ReportID:  report.ID,
		VersionID: report.VersionID,
		BatchID:   report.BatchID,
		Revision:  report.Revision,
		Content:   content,
		SHA256:    report.MarkdownSHA256,
		Size:      report.MarkdownSize,
		Filename:  fmt.Sprintf("report-%d-r%d.md", report.ID, report.Revision),
	}
	s.recordExportedAudit(ctx, exported)
	return exported, nil
}

// Publish flips a draft report to published (spec §7.4). A report that is
// already published or stale is report_not_publishable.
func (s *ReportService) Publish(ctx context.Context, versionID, reportID, actor int64) (domain.Report, error) {
	if err := s.ready(); err != nil {
		return domain.Report{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.Report{}, reportInvalidID("versionId and reportId must be positive")
	}
	report, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.Report{}, err
	}
	if report.Status != domain.ReportStatusDraft {
		return domain.Report{}, domain.NewStateConflict(domain.ReportReasonNotPublishable,
			fmt.Sprintf("report %d is %s, only draft reports can be published", report.ID, report.Status))
	}
	now := s.now()
	published, err := s.deps.Reports.MarkPublished(ctx, versionID, reportID, now)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: publish report %d: %w", reportID, err)
	}
	if !published {
		return domain.Report{}, domain.NewStateConflict(domain.ReportReasonNotPublishable,
			fmt.Sprintf("report %d is no longer a draft", reportID))
	}
	report.Status = domain.ReportStatusPublished
	report.PublishedAt = &now
	report.UpdatedAt = now
	s.publishReportPublished(ctx, report, actor)
	s.recordPublishedAudit(ctx, report, actor)
	return report, nil
}

// RefreshStale recomputes the input fingerprint of one report and marks a
// published report stale (once) when it changed.
func (s *ReportService) RefreshStale(ctx context.Context, versionID, reportID int64) (domain.Report, error) {
	if err := s.ready(); err != nil {
		return domain.Report{}, err
	}
	if versionID <= 0 || reportID <= 0 {
		return domain.Report{}, reportInvalidID("versionId and reportId must be positive")
	}
	report, err := s.getReport(ctx, versionID, reportID)
	if err != nil {
		return domain.Report{}, err
	}
	return s.refreshReportStale(ctx, report)
}

// generate produces the snapshot, Markdown file and report row of one new
// revision. The row is inserted before the file is written (spec §6.5); if the
// file write fails the row is removed so the next tick retries cleanly.
func (s *ReportService) generate(ctx context.Context, versionID, batchID, actor int64) (domain.Report, error) {
	version, err := s.deps.Versions.GetReportVersion(ctx, versionID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load report version: %w", err)
	}
	batch, err := s.deps.Execution.GetReportBatch(ctx, versionID, batchID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load report batch: %w", err)
	}
	// Defense in depth: the adapters are version-scoped, but a foreign batch
	// must never become part of a version's report snapshot.
	if version.ID != 0 && version.ID != versionID {
		return domain.Report{}, domain.NewInvalid("versionId", domain.ReasonInvalidArgument,
			fmt.Sprintf("version reader returned version %d for %d", version.ID, versionID))
	}
	if batch.VersionID != 0 && batch.VersionID != versionID {
		return domain.Report{}, domain.NewInvalid("batchId", domain.ReasonInvalidArgument,
			fmt.Sprintf("batch %d does not belong to version %d", batch.ID, versionID))
	}
	runs, err := s.deps.Execution.ListReportCaseRuns(ctx, batchID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load report case runs: %w", err)
	}
	requirements, err := s.deps.Graph.ListReportRequirements(ctx, versionID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load report requirements: %w", err)
	}
	issues, err := s.deps.Issues.ListReportIssues(ctx, versionID, batchID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: load report issues: %w", err)
	}

	content, requirementCases := s.buildContent(version, batch, runs, requirements, issues)
	fingerprint, err := reportFingerprint(batch, runs, issues)
	if err != nil {
		return domain.Report{}, err
	}
	revision, err := s.deps.Reports.NextRevision(ctx, versionID, batchID)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: next report revision: %w", err)
	}
	reportID := s.deps.IDs.Next()
	if reportID <= 0 {
		return domain.Report{}, errors.New("delivery: id generator returned a non-positive id")
	}
	markdown := renderReportMarkdown(content, revision, fingerprint, requirementCases)
	sum := sha256.Sum256(markdown)
	now := s.now()
	report := domain.Report{
		ID:               reportID,
		VersionID:        versionID,
		BatchID:          batchID,
		Revision:         revision,
		Status:           domain.ReportStatusPublished,
		Title:            reportTitle(version, batch),
		Content:          content,
		InputFingerprint: fingerprint,
		MarkdownPath:     reportMarkdownPath(versionID, reportID, revision),
		MarkdownSHA256:   hex.EncodeToString(sum[:]),
		MarkdownSize:     int64(len(markdown)),
		GeneratedBy:      reportActorPointer(actor),
		PublishedAt:      &now,
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	if err := report.Validate(); err != nil {
		return domain.Report{}, fmt.Errorf("delivery: generated report is invalid: %w", err)
	}
	created, err := s.deps.Reports.Create(ctx, report)
	if err != nil {
		if errors.Is(err, domain.ErrReportRevisionConflict) {
			return domain.Report{}, err
		}
		return domain.Report{}, fmt.Errorf("delivery: create report: %w", err)
	}
	if _, _, _, err := s.deps.Files.Write(ctx, report.MarkdownPath, markdown); err != nil {
		if deleteErr := s.deps.Reports.Delete(ctx, versionID, reportID); deleteErr != nil {
			s.warn(ctx, "delivery report row cleanup failed",
				"report_id", reportID, "error", deleteErr)
		}
		return domain.Report{}, fmt.Errorf("delivery: write report markdown: %w", err)
	}
	s.publishReportPublished(ctx, created, actor)
	s.recordPublishedAudit(ctx, created, actor)
	return created, nil
}

// buildContent assembles the deterministic snapshot and the requirement→case
// coverage mapping used only by the Markdown graph. The snapshot is normalized
// before it is returned, so the stored JSON and the Markdown share one order.
func (s *ReportService) buildContent(
	version ReportVersionSource,
	batch ReportBatchSource,
	runs []ReportCaseRunSource,
	requirements []ReportRequirementSource,
	issues []ReportIssueSource,
) (domain.ReportContent, map[int64][]int64) {
	summary := domain.ReportSummary{CaseCount: len(runs), FlakyCount: len(batch.FlakyCaseIDs)}
	cases := make([]domain.ReportCase, 0, len(runs))
	statusByNode := make(map[int64]string, len(runs))
	for _, run := range runs {
		statusByNode[run.CaseNodeID] = run.Status
		switch run.Status {
		case "passed":
			summary.Passed++
		case "failed":
			summary.Failed++
		case "blocked":
			summary.Blocked++
		case "inconclusive":
			summary.Inconclusive++
		case "skipped":
			summary.Skipped++
		case "interrupted":
			summary.Interrupted++
		}
		cases = append(cases, domain.ReportCase{
			CaseRunID:      reportIDString(run.CaseRunID),
			CaseNodeID:     reportIDString(run.CaseNodeID),
			Title:          run.Title,
			Source:         run.Source,
			Status:         run.Status,
			FailureClass:   run.FailureClass,
			Attempt:        run.Attempt,
			StepCount:      run.StepCount,
			AssertionCount: run.AssertionCount,
			EvidenceCount:  run.EvidenceCount,
		})
	}

	requirementRows := make([]domain.ReportRequirement, 0, len(requirements))
	requirementCases := make(map[int64][]int64, len(requirements))
	for _, requirement := range requirements {
		covered := make([]int64, 0, len(requirement.CaseNodeIDs))
		seen := make(map[int64]bool, len(requirement.CaseNodeIDs))
		passed, failed := 0, 0
		for _, caseNodeID := range requirement.CaseNodeIDs {
			if seen[caseNodeID] {
				continue
			}
			status, inBatch := statusByNode[caseNodeID]
			if !inBatch {
				continue
			}
			seen[caseNodeID] = true
			covered = append(covered, caseNodeID)
			switch status {
			case "passed":
				passed++
			case "failed":
				failed++
			}
		}
		requirementCases[requirement.RequirementID] = covered
		requirementRows = append(requirementRows, domain.ReportRequirement{
			RequirementID: reportIDString(requirement.RequirementID),
			Title:         requirement.Title,
			Status:        requirement.Status,
			CaseCount:     len(covered),
			Passed:        passed,
			Failed:        failed,
		})
	}

	issueRows := make([]domain.ReportIssue, 0, len(issues))
	seenIssues := make(map[int64]bool, len(issues))
	for _, issue := range issues {
		if seenIssues[issue.IssueID] {
			continue
		}
		seenIssues[issue.IssueID] = true
		caseNodeID := ""
		if issue.CaseNodeID != nil {
			caseNodeID = reportIDString(*issue.CaseNodeID)
		}
		codeRefs := append([]domain.ReportCodeRef(nil), issue.CodeRefs...)
		issueRows = append(issueRows, domain.ReportIssue{
			IssueID:    reportIDString(issue.IssueID),
			Title:      issue.Title,
			Severity:   issue.Severity,
			Status:     issue.Status,
			Confidence: issue.Confidence,
			Category:   issue.Category,
			CaseNodeID: caseNodeID,
			CodeRefs:   codeRefs,
		})
	}

	generatedAt := s.now().UTC().Format(time.RFC3339)
	if batch.FinishedAt != nil {
		generatedAt = batch.FinishedAt.UTC().Format(time.RFC3339)
	}
	content := domain.ReportContent{
		Schema: domain.ReportSchemaV1,
		Version: domain.ReportVersion{
			ID:                 reportIDString(version.ID),
			Name:               version.Name,
			Label:              version.Label,
			Status:             version.Status,
			CodeBaselineCommit: version.CodeBaselineCommit,
		},
		Batch: domain.ReportBatch{
			ID:             reportIDString(batch.ID),
			BatchNo:        batch.BatchNo,
			Kind:           batch.Kind,
			Status:         batch.Status,
			BaselineHash:   batch.BaselineHash,
			GraphHash:      batch.GraphHash,
			CodeBaseCommit: batch.CodeBaseCommit,
			HeadCommit:     batch.HeadCommit,
			StartedAt:      reportFormatOptionalTime(batch.StartedAt),
			FinishedAt:     reportFormatOptionalTime(batch.FinishedAt),
		},
		Summary:      summary,
		Cases:        cases,
		Requirements: requirementRows,
		Issues:       issueRows,
		Risks:        reportRisks(summary, issues, batch.Degraded),
		Degraded:     batch.Degraded,
		Appendix: domain.ReportAppendix{
			Environment: batch.Environment,
			System:      batch.System,
			Commit:      batch.HeadCommit,
			GeneratedAt: generatedAt,
		},
	}
	return content.Normalized(), requirementCases
}

// refreshReportStale applies the read-time stale rule of spec §5.5: recompute the
// fingerprint, and for a published report with a different fingerprint flip it
// to stale via CAS and publish report.stale exactly once.
func (s *ReportService) refreshReportStale(ctx context.Context, report domain.Report) (domain.Report, error) {
	if report.Status != domain.ReportStatusPublished {
		return report, nil
	}
	fingerprint, err := s.currentFingerprint(ctx, report.VersionID, report.BatchID)
	if err != nil {
		return domain.Report{}, err
	}
	if fingerprint == report.InputFingerprint {
		return report, nil
	}
	now := s.now()
	updated, err := s.deps.Reports.MarkStale(ctx, report.VersionID, report.ID,
		report.InputFingerprint, ReportStaleReasonInputChanged, now)
	if err != nil {
		return domain.Report{}, fmt.Errorf("delivery: mark report %d stale: %w", report.ID, err)
	}
	if !updated {
		// A concurrent reader already flipped it: return the current row without
		// publishing a second report.stale event.
		current, currentErr := s.getReport(ctx, report.VersionID, report.ID)
		if currentErr != nil {
			return domain.Report{}, currentErr
		}
		return current, nil
	}
	report.Status = domain.ReportStatusStale
	report.StaleReason = ReportStaleReasonInputChanged
	report.UpdatedAt = now
	s.publishReportStale(ctx, report)
	return report, nil
}

// currentFingerprint recomputes the §5.5 fingerprint from the current ports.
func (s *ReportService) currentFingerprint(ctx context.Context, versionID, batchID int64) (string, error) {
	batch, err := s.deps.Execution.GetReportBatch(ctx, versionID, batchID)
	if err != nil {
		return "", fmt.Errorf("delivery: load report batch for fingerprint: %w", err)
	}
	runs, err := s.deps.Execution.ListReportCaseRuns(ctx, batchID)
	if err != nil {
		return "", fmt.Errorf("delivery: load case runs for fingerprint: %w", err)
	}
	issues, err := s.deps.Issues.ListReportIssues(ctx, versionID, batchID)
	if err != nil {
		return "", fmt.Errorf("delivery: load issues for fingerprint: %w", err)
	}
	return reportFingerprint(batch, runs, issues)
}

// getReport loads one revision and maps the repository not-found to the report
// reason.
func (s *ReportService) getReport(ctx context.Context, versionID, reportID int64) (domain.Report, error) {
	report, err := s.deps.Reports.Get(ctx, versionID, reportID)
	if err != nil {
		if errors.Is(err, domain.ErrReportNotFound) {
			return domain.Report{}, reportNotFound(reportID)
		}
		return domain.Report{}, fmt.Errorf("delivery: get report %d: %w", reportID, err)
	}
	return report, nil
}

// publishReportPublished emits report.published after the revision commits.
func (s *ReportService) publishReportPublished(ctx context.Context, report domain.Report, actor int64) {
	if s.deps.Events == nil {
		return
	}
	event := ReportEvent{
		VersionID: report.VersionID,
		BatchID:   report.BatchID,
		Type:      ReportEventPublished,
		Summary:   fmt.Sprintf("report published: %d revision %d", report.ID, report.Revision),
		ActorID:   actor,
		Detail: map[string]any{
			"reportId": reportIDString(report.ID),
			"batchId":  reportIDString(report.BatchID),
			"revision": report.Revision,
			"degraded": report.Content.Degraded,
		},
	}
	if err := s.deps.Events.PublishReportEvent(ctx, event); err != nil {
		s.warn(ctx, "delivery report event publish failed",
			"type", event.Type, "report_id", report.ID, "error", err)
	}
}

// publishReportStale emits report.stale after the CAS flip.
func (s *ReportService) publishReportStale(ctx context.Context, report domain.Report) {
	if s.deps.Events == nil {
		return
	}
	event := ReportEvent{
		VersionID: report.VersionID,
		BatchID:   report.BatchID,
		Type:      ReportEventStale,
		Summary:   fmt.Sprintf("report stale: %d", report.ID),
		Detail: map[string]any{
			"reportId": reportIDString(report.ID),
			"batchId":  reportIDString(report.BatchID),
			"reason":   report.StaleReason,
		},
	}
	if err := s.deps.Events.PublishReportEvent(ctx, event); err != nil {
		s.warn(ctx, "delivery report stale event publish failed",
			"report_id", report.ID, "error", err)
	}
}

// recordPublishedAudit records delivery.report.published (ids/revision/sha only).
func (s *ReportService) recordPublishedAudit(ctx context.Context, report domain.Report, actor int64) {
	if s.deps.Audit == nil {
		return
	}
	event := ReportAuditEvent{
		Action:     ReportActionPublished,
		ActorID:    actor,
		TargetType: "report",
		TargetID:   reportIDString(report.ID),
		Metadata: map[string]any{
			"report_id": reportIDString(report.ID),
			"batch_id":  reportIDString(report.BatchID),
			"revision":  report.Revision,
			"sha256":    report.MarkdownSHA256,
		},
	}
	if err := s.deps.Audit.RecordReportAudit(ctx, event); err != nil {
		s.warn(ctx, "delivery report publish audit failed",
			"report_id", report.ID, "error", err)
	}
}

// recordExportedAudit records delivery.report.exported. Export's shared
// signature has no actor argument, so the actor comes from WithReportActor when
// the HTTP layer attached it (0 otherwise).
func (s *ReportService) recordExportedAudit(ctx context.Context, exported domain.ReportExport) {
	if s.deps.Audit == nil {
		return
	}
	event := ReportAuditEvent{
		Action:     ReportActionExported,
		ActorID:    reportActorFromContext(ctx),
		TargetType: "report",
		TargetID:   reportIDString(exported.ReportID),
		Metadata: map[string]any{
			"report_id": reportIDString(exported.ReportID),
			"batch_id":  reportIDString(exported.BatchID),
			"revision":  exported.Revision,
			"sha256":    exported.SHA256,
		},
	}
	if err := s.deps.Audit.RecordReportAudit(ctx, event); err != nil {
		s.warn(ctx, "delivery report export audit failed",
			"report_id", exported.ReportID, "error", err)
	}
}

// ready validates the mandatory ports.
func (s *ReportService) ready() error {
	switch {
	case s.deps.Versions == nil, s.deps.Execution == nil, s.deps.Graph == nil,
		s.deps.Issues == nil, s.deps.Reports == nil, s.deps.Files == nil,
		s.deps.IDs == nil:
		return errors.New("delivery: report service is not configured")
	default:
		return nil
	}
}

// now returns the injected clock time in UTC.
func (s *ReportService) now() time.Time {
	if s.deps.Clock != nil {
		return s.deps.Clock.Now().UTC()
	}
	return time.Now().UTC()
}

// warn logs a best-effort failure through the optional logger.
func (s *ReportService) warn(ctx context.Context, message string, args ...any) {
	if s.deps.Logger == nil {
		return
	}
	s.deps.Logger.WarnContext(ctx, message, args...)
}

// reportFingerprint maps the ports to the domain fingerprint input and computes
// its sha256.
func reportFingerprint(batch ReportBatchSource, runs []ReportCaseRunSource, issues []ReportIssueSource) (string, error) {
	caseRuns := make([]domain.ReportFingerprintCaseRun, 0, len(runs))
	for _, run := range runs {
		caseRuns = append(caseRuns, domain.ReportFingerprintCaseRun{
			CaseRunID:    reportIDString(run.CaseRunID),
			Attempt:      run.Attempt,
			Status:       run.Status,
			FailureClass: run.FailureClass,
			UpdatedAt:    reportFormatFingerprintTime(run.UpdatedAt),
		})
	}
	issueRows := make([]domain.ReportFingerprintIssue, 0, len(issues))
	for _, issue := range issues {
		issueRows = append(issueRows, domain.ReportFingerprintIssue{
			IssueID:   reportIDString(issue.IssueID),
			Status:    issue.Status,
			Revision:  issue.Revision,
			UpdatedAt: reportFormatFingerprintTime(issue.UpdatedAt),
		})
	}
	input := domain.ReportFingerprintInput{
		Batch: domain.ReportFingerprintBatch{
			ID:         reportIDString(batch.ID),
			Status:     batch.Status,
			FinishedAt: reportFormatOptionalFingerprintTime(batch.FinishedAt),
			Counts: domain.ReportFingerprintCounts{
				CaseCount:    batch.CaseCount,
				Passed:       batch.PassedCount,
				Failed:       batch.FailedCount,
				Blocked:      batch.BlockedCount,
				Inconclusive: batch.InconclusiveCount,
				Skipped:      batch.SkippedCount,
				Interrupted:  batch.InterruptedCount,
			},
		},
		LatestCaseRuns: caseRuns,
		Issues:         issueRows,
	}
	return input.Compute()
}

// reportRisks derives the deterministic risk bullets of the report.
func reportRisks(summary domain.ReportSummary, issues []ReportIssueSource, degraded bool) []string {
	risks := make([]string, 0, 7)
	if summary.Failed > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个失败用例", summary.Failed))
	}
	if summary.Blocked > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个阻塞用例", summary.Blocked))
	}
	if summary.Inconclusive > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个不确定结果", summary.Inconclusive))
	}
	if summary.FlakyCount > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个不稳定用例", summary.FlakyCount))
	}
	active := map[string]int{}
	for _, issue := range issues {
		if issue.Status == "open" || issue.Status == "confirmed" {
			active[issue.Severity]++
		}
	}
	if active["blocker"] > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个阻塞级未关闭问题", active["blocker"]))
	}
	if active["major"] > 0 {
		risks = append(risks, fmt.Sprintf("存在 %d 个严重级未关闭问题", active["major"]))
	}
	if degraded {
		risks = append(risks, "AI 分析或补全降级，报告结论可能不完整")
	}
	return risks
}

// reportMarkdownPath builds the root-relative Markdown path of one revision.
func reportMarkdownPath(versionID, reportID int64, revision int) string {
	return fmt.Sprintf("versions/%d/reports/%d-r%d.md", versionID, reportID, revision)
}

// reportTitle builds the human-readable report title.
func reportTitle(version ReportVersionSource, batch ReportBatchSource) string {
	return fmt.Sprintf("%s 批次 %d 报告", version.Name, batch.BatchNo)
}

// reportIDString renders a domain id as the decimal string used by the JSON
// boundary and event details (D29).
func reportIDString(id int64) string {
	return strconv.FormatInt(id, 10)
}

// reportActorPointer renders the optional generated_by value.
func reportActorPointer(actor int64) *int64 {
	if actor <= 0 {
		return nil
	}
	return &actor
}

// reportFormatOptionalTime renders an optional timestamp for the snapshot.
func reportFormatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// reportFormatFingerprintTime renders a timestamp for the fingerprint.
func reportFormatFingerprintTime(value time.Time) string {
	return value.UTC().Format(time.RFC3339Nano)
}

// reportFormatOptionalFingerprintTime renders an optional fingerprint timestamp.
func reportFormatOptionalFingerprintTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return reportFormatFingerprintTime(*value)
}

// reportNotFound builds the 404 report error.
func reportNotFound(reportID int64) *domain.Error {
	return domain.NewNotFound("report", reportID)
}

// reportNoPreviousBatch builds the 404 returned when baseReportId is omitted and
// the batch has no previous-batch report to diff against. The target report
// exists, so the message deliberately never mentions its id.
func reportNoPreviousBatch() *domain.Error {
	return &domain.Error{
		Kind:    domain.KindNotFound,
		Reason:  domain.ReportReasonNotFound,
		Message: "no report of the previous batch to diff against",
	}
}

// reportInvalidID builds the generic invalid-id error.
func reportInvalidID(message string) *domain.Error {
	return domain.NewInvalid("id", domain.ReasonInvalidArgument, message)
}

// exportFailure logs the underlying cause, which may embed an OS path, and
// returns the fixed report_export_failed error.
func (s *ReportService) exportFailure(ctx context.Context, reportID int64, cause error) *domain.Error {
	s.warn(ctx, "delivery report export failed", "report_id", reportID, "cause", cause)
	return reportExportFailed()
}

// reportExportFailed builds the 500 report_export_failed error. The delivery
// context has no "internal" Kind constant, so this literal is mapped to the
// generic 500 by the HTTP layer while details.reason stays stable. The message
// is fixed and never embeds the cause (which can carry a filesystem path).
func reportExportFailed() *domain.Error {
	return &domain.Error{
		Kind:    "internal",
		Reason:  domain.ReportReasonExportFailed,
		Message: "report export failed: the stored markdown file is missing or corrupted",
	}
}
