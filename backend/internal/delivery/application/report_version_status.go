package deliveryapp

import (
	"context"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// VersionReportStatus is the report-context projection consumed by the version
// close predicate (spec §7.3 check 8). Status is the effective close status: it
// is "published" only when both the report header and its current revision are
// published with rendered HTML; a published header whose current revision is
// still a draft is reported as draft so the predicate can never pass on an
// unusable report. RevisionID is the current revision row ID, Fingerprint its
// stored input fingerprint and CurrentFingerprint the recomputed fingerprint of
// the current report input.
type VersionReportStatus struct {
	Status             delivery.ReportStatus
	Fingerprint        string
	RevisionID         int64
	CurrentFingerprint string
}

// VersionReportStatus returns the close-relevant projection of the latest
// version report (kind='version'). A version that never produced a report
// returns the zero value with a nil error; storage failures propagate.
func (s *ReportService) VersionReportStatus(ctx context.Context, versionID int64) (VersionReportStatus, error) {
	if versionID <= 0 {
		return VersionReportStatus{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s == nil || s.repo == nil {
		return VersionReportStatus{}, reportServiceNotConfigured("report repository")
	}
	kind := delivery.ReportKindVersion
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: &kind, Limit: 1})
	if err != nil {
		return VersionReportStatus{}, mapReportError(err)
	}
	if len(reports) == 0 {
		return VersionReportStatus{}, nil
	}
	report := reports[0]

	status := VersionReportStatus{Status: report.Status}
	var current *delivery.ReportRevision
	if report.CurrentRevisionID != nil {
		revisions, err := s.repo.ListRevisions(ctx, report.ID)
		if err != nil {
			return VersionReportStatus{}, mapReportError(err)
		}
		for index := range revisions {
			if revisions[index].ID == *report.CurrentRevisionID {
				revision := revisions[index]
				current = &revision
				break
			}
		}
	}
	if current != nil {
		status.RevisionID = current.ID
		status.Fingerprint = current.InputFingerprint
		revisionReady := current.Status == delivery.ReportStatusPublished &&
			current.RenderStatus == reportRenderDone && current.HTMLRef != nil
		if report.Status == delivery.ReportStatusPublished && !revisionReady {
			// A published header without a published/rendered current revision
			// is not a usable report: never expose it as published.
			status.Status = delivery.ReportStatusDraft
		}
	}
	if s.data == nil {
		return status, reportServiceNotConfigured("report data reader")
	}
	snapshot, err := s.snapshotFor(ctx, report)
	if err != nil {
		return VersionReportStatus{}, err
	}
	status.CurrentFingerprint = delivery.Fingerprint(snapshot)
	return status, nil
}

// MarkVersionReportStale marks the latest version report stale after a version
// reopen and emits report.stale once (spec §7.5). A version without a report is
// a no-op. The caller treats the projection as best effort: the reopen is
// already committed and the reconciler retries the stale scan.
func (s *ReportService) MarkVersionReportStale(ctx context.Context, versionID int64, _ string) error {
	if versionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if s == nil || s.repo == nil {
		return reportServiceNotConfigured("report repository")
	}
	kind := delivery.ReportKindVersion
	reports, err := s.repo.ListReports(ctx, versionID, ReportFilter{Kind: &kind, Limit: 1})
	if err != nil {
		return mapReportError(err)
	}
	if len(reports) == 0 {
		return nil
	}
	report := reports[0]
	changed, err := s.repo.MarkStale(ctx, report.ID)
	if err != nil {
		return mapReportError(err)
	}
	if !changed {
		return nil
	}
	report.Status = delivery.ReportStatusStale
	s.publishStaleEvent(ctx, report)
	return nil
}
