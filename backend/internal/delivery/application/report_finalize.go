package deliveryapp

import (
	"context"
	"fmt"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// FinalizeRevision is the composition-root finalizer of a report.wave/
// report.version agent run. The runner may only propose a narrative candidate;
// the server owns the deterministic snapshot, the render and the publish. When
// the revision is still a draft the finalizer publishes it with the staged
// narrative (if any) and then verifies that the numbered revision became the
// published current one, so a failed publish keeps the work item retryable
// (spec §4.4/§6.3/§6.5). An already published revision is not re-rendered.
func (s *ReportService) FinalizeRevision(ctx context.Context, reportID int64, revision int32) error {
	if reportID <= 0 || revision <= 0 {
		return fmt.Errorf("%w: report id and revision must be positive", delivery.ErrInvalidArgument)
	}
	if s == nil || s.repo == nil {
		return reportServiceNotConfigured("report repository")
	}
	if err := s.RequirePublished(ctx, reportID, revision); err == nil {
		return nil
	}
	staged, _ := TakeReportNarrative(reportID, revision)
	if _, err := s.Publish(ctx, reportID, revision, staged.SummaryMarkdown); err != nil {
		return err
	}
	return s.RequirePublished(ctx, reportID, revision)
}
