package adapters

import (
	"context"
	"fmt"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// WaivedReportService is the delivery application report surface the waiver
// consumes (*deliveryapp.ReportService satisfies it). It is the synchronous
// post-waive-close regeneration entry (R-G8).
type WaivedReportService interface {
	RegenerateWaivedVersionReport(ctx context.Context, versionID int64) (delivery.ReportRevision, error)
}

// WaivedReportRegenerator adapts the delivery report service onto the
// version-context waiver port. It only translates the published revision into
// the version-context projection; the report lifecycle stays in delivery.
type WaivedReportRegenerator struct {
	reports WaivedReportService
}

// NewWaivedReportRegenerator wires the delivery report service.
func NewWaivedReportRegenerator(reports WaivedReportService) *WaivedReportRegenerator {
	return &WaivedReportRegenerator{reports: reports}
}

var _ versionapp.WaivedReportRegenerator = (*WaivedReportRegenerator)(nil)

// RegenerateWaivedVersionReport implements versionapp.WaivedReportRegenerator.
func (a *WaivedReportRegenerator) RegenerateWaivedVersionReport(ctx context.Context, versionID int64) (versionapp.WaivedReportPublication, error) {
	if versionID <= 0 {
		return versionapp.WaivedReportPublication{}, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if a == nil || a.reports == nil {
		return versionapp.WaivedReportPublication{}, fmt.Errorf("waived report regenerator is not configured")
	}
	revision, err := a.reports.RegenerateWaivedVersionReport(ctx, versionID)
	if err != nil {
		return versionapp.WaivedReportPublication{}, err
	}
	return versionapp.WaivedReportPublication{ReportID: revision.ReportID, Revision: revision.Revision}, nil
}
