package version

import (
	"context"
	"errors"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
)

// VersionNamesReader implements delivery/application.VersionNameReader for the
// global centres (spec §7.10). It resolves display names through the version
// application service, so delivery never reads the version table itself
// (Agent.md §6.2).
type VersionNamesReader struct {
	versions VersionReader
}

// NewVersionNamesReader wires the version-name port. A nil service fails closed
// on every call.
func NewVersionNamesReader(versions VersionReader) *VersionNamesReader {
	return &VersionNamesReader{versions: versions}
}

var _ deliveryapp.VersionNameReader = (*VersionNamesReader)(nil)

// VersionNames resolves each positive, distinct version id once and returns its
// display name. An empty request performs no version read.
func (r *VersionNamesReader) VersionNames(ctx context.Context, versionIDs []int64) (map[int64]string, error) {
	if r == nil || r.versions == nil {
		return nil, errors.New("delivery: version name reader is not configured")
	}
	names := make(map[int64]string, len(versionIDs))
	for _, versionID := range versionIDs {
		if versionID <= 0 {
			continue
		}
		if _, resolved := names[versionID]; resolved {
			continue
		}
		version, err := r.versions.Get(ctx, versionID)
		if err != nil {
			return nil, fmt.Errorf("delivery: load version %d name: %w", versionID, err)
		}
		if version == nil {
			return nil, fmt.Errorf("delivery: load version %d name: version reader returned nil", versionID)
		}
		names[versionID] = version.Name
	}
	return names, nil
}
