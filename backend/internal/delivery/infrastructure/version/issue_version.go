// IssueVersionReader adapts the P3 version application service to the delivery
// context's IssueVersionReader: an issue must not be written for a version that
// does not exist. Delivery never reads the version table itself (spec §3.2,
// Agent.md §6.2).
package version

import (
	"context"
	"errors"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// IssueVersionReader implements delivery/application.IssueVersionReader over the
// version application service.
type IssueVersionReader struct {
	versions VersionReader
}

// NewIssueVersionReader wires the issue version reader. A nil service fails
// closed on every call.
func NewIssueVersionReader(versions VersionReader) *IssueVersionReader {
	return &IssueVersionReader{versions: versions}
}

var _ deliveryapp.IssueVersionReader = (*IssueVersionReader)(nil)

// VersionExists reports whether versionID exists. A version-context not-found is
// (false, nil); any other lookup failure is propagated so the issue write aborts
// instead of being mistaken for a missing version.
func (r *IssueVersionReader) VersionExists(ctx context.Context, versionID int64) (bool, error) {
	if r == nil || r.versions == nil {
		return false, errors.New("delivery: issue version reader is not configured")
	}
	version, err := r.versions.Get(ctx, versionID)
	if err != nil {
		if isVersionNotFound(err) {
			return false, nil
		}
		return false, err
	}
	return version != nil, nil
}

// isVersionNotFound reports a version-context not-found domain error.
func isVersionNotFound(err error) bool {
	var domainErr *versiondomain.Error
	return errors.As(err, &domainErr) && domainErr != nil && domainErr.Kind == versiondomain.KindNotFound
}
