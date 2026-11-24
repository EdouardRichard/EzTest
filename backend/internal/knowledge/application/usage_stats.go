package knowledgeapp

import (
	"context"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// UsageStats is the read-time aggregate of one item's knowledge_usage rows:
// how many times the item was injected into a prompt and when it was last
// used. It is a derived projection of the append-only usage facts (computed on
// read, never stored), surfaced on the item detail API so the write-only
// usage ledger has a real consumer (V-0019 H share, R-H8).
type UsageStats struct {
	InjectionCount int64
	LastUsedAt     *time.Time
}

// ItemUsage returns the usage aggregates of one knowledge item. The item
// detail handler calls it after the item read and the R91 secret gate, so a
// hidden item never reaches the usage read. A nil usage repository is an
// explicit 503 (never a silent "never used"), and a reader failure propagates
// instead of degrading to zero stats.
func (s *Service) ItemUsage(ctx context.Context, id int64) (UsageStats, error) {
	if err := s.requireID(id); err != nil {
		return UsageStats{}, err
	}
	if s.usage == nil {
		return UsageStats{}, platform.Unavailable("knowledge usage repository is not configured")
	}
	stats, err := s.usage.StatsByItem(ctx, id)
	if err != nil {
		return UsageStats{}, err
	}
	return stats, nil
}
