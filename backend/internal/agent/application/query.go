package agentapp

import (
	"context"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// QueryReader is the agent context's P6 read-only data source for the work-item
// retry metric. It is implemented outside the application layer; the
// application service only normalizes the version scope.
type QueryReader interface {
	// CountTaskRetries returns work_items with attempts>1 (num) over work_items
	// with attempts>=1 (denom), for one version when versionID is non-nil and
	// globally otherwise.
	CountTaskRetries(ctx context.Context, versionID *int64) (num, denom int, err error)
}

// QueryService is the P6 read-only query port of the agent context.
type QueryService struct {
	reader QueryReader
}

// NewQueryService wires the agent read-only query port.
func NewQueryService(reader QueryReader) *QueryService {
	return &QueryService{reader: reader}
}

func (s *QueryService) requireReader() error {
	if s == nil || s.reader == nil {
		return platform.Unavailable("agent query reader is not configured")
	}
	return nil
}

// CountTaskRetries returns the task retry-rate counters (spec §5.2). A zero
// denominator is a valid observation.
func (s *QueryService) CountTaskRetries(ctx context.Context, versionID *int64) (int, int, error) {
	if err := s.requireReader(); err != nil {
		return 0, 0, err
	}
	if versionID != nil && *versionID <= 0 {
		return 0, 0, platform.Invalid("version_id", "must be a positive decimal ID")
	}
	return s.reader.CountTaskRetries(ctx, versionID)
}
