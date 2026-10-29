// Package version adapts the P3 version application service to the delivery
// context's ReportVersionReader (spec §6.5): the report header and title need the
// version name/label/status and the code baseline commit. Delivery never reads
// the version table itself.
package version

import (
	"context"
	"errors"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// VersionReader is the version application slice the adapter needs.
// *version/application.VersionService satisfies it.
type VersionReader interface {
	Get(ctx context.Context, id int64) (*versionapp.Version, error)
}

// ReportVersionReader implements delivery/application.ReportVersionReader.
type ReportVersionReader struct {
	versions VersionReader
}

// NewReportVersionReader wires the report version reader. A nil service fails
// closed on every call.
func NewReportVersionReader(versions VersionReader) *ReportVersionReader {
	return &ReportVersionReader{versions: versions}
}

var _ deliveryapp.ReportVersionReader = (*ReportVersionReader)(nil)

// GetReportVersion implements delivery/application.ReportVersionReader.
func (r *ReportVersionReader) GetReportVersion(ctx context.Context, versionID int64) (deliveryapp.ReportVersionSource, error) {
	if r == nil || r.versions == nil {
		return deliveryapp.ReportVersionSource{}, errors.New("delivery: version reader is not configured")
	}
	version, err := r.versions.Get(ctx, versionID)
	if err != nil {
		return deliveryapp.ReportVersionSource{}, err
	}
	source := deliveryapp.ReportVersionSource{
		ID:     version.ID,
		Name:   version.Name,
		Label:  version.Label,
		Status: version.Status,
	}
	if version.CodeBaselineCommit != nil {
		source.CodeBaselineCommit = *version.CodeBaselineCommit
	}
	return source, nil
}
