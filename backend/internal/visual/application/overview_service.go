package visualapp

import (
	"context"
	"errors"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	visual "github.com/EdouardRichard/EzTest/backend/internal/visual/domain"
)

// OverviewServiceDeps wires the visual quality overview service.
type OverviewServiceDeps struct {
	Diffs DiffRepository
	Clock Clock
}

// OverviewService aggregates the version/global visual quality metrics.
type OverviewService struct {
	diffs DiffRepository
	clock Clock
}

// NewOverviewService wires the overview service.
func NewOverviewService(deps OverviewServiceDeps) *OverviewService {
	return &OverviewService{diffs: deps.Diffs, clock: deps.Clock}
}

// Overview is GET /api/versions/{id}/visual/overview (spec §7.5).
type Overview struct {
	VersionID             int64
	AssertionTotal        int
	BaselineCovered       int
	BaselineCoverageRatio *float64
	Passed                int
	Failed                int
	Inconclusive          int
	Blocked               int
	PassRatio             *float64
	InconclusiveReasons   []ReasonCount
	DiffRatioP50          *float64
	DiffRatioP95          *float64
	RecentFailures        []visual.Diff
}

// Metrics is GET /api/quality/visual-metrics (global or single version).
type Metrics struct {
	VersionID              *int64
	Assertions             int
	Baselines              int
	Diffs                  int
	Passed                 int
	Failed                 int
	Inconclusive           int
	Blocked                int
	BaselineCoverage       *float64
	PassRate               *float64
	InconclusiveRate       *float64
	TopInconclusiveReasons []ReasonCount
	GeneratedAt            time.Time
}

// Version aggregates the metrics of one version.
func (s *OverviewService) Version(ctx context.Context, versionID int64) (Overview, error) {
	if versionID <= 0 {
		return Overview{}, platform.Invalid("version_id", "must be a positive ID")
	}
	data, err := s.diffs.Overview(ctx, &versionID)
	if err != nil {
		return Overview{}, mapVisualError(err, "visual_overview")
	}
	return buildOverview(versionID, data), nil
}

// Global aggregates every version, or one version when versionID is set.
func (s *OverviewService) Global(ctx context.Context, versionID *int64) (Metrics, error) {
	if versionID != nil && *versionID <= 0 {
		return Metrics{}, platform.Invalid("version_id", "must be a positive ID")
	}
	if s.diffs == nil {
		return Metrics{}, errors.New("visual overview service is not configured")
	}
	data, err := s.diffs.Overview(ctx, versionID)
	if err != nil {
		return Metrics{}, mapVisualError(err, "visual_overview")
	}
	counts := normalizedCounts(data.DiffCounts)
	total := counts[visual.ClassificationPassed] + counts[visual.ClassificationFailed] +
		counts[visual.ClassificationInconclusive] + counts[visual.ClassificationBlocked]
	return Metrics{
		VersionID:              versionID,
		Assertions:             data.AssertionTotal,
		Baselines:              data.BaselineCovered,
		Diffs:                  total,
		Passed:                 counts[visual.ClassificationPassed],
		Failed:                 counts[visual.ClassificationFailed],
		Inconclusive:           counts[visual.ClassificationInconclusive],
		Blocked:                counts[visual.ClassificationBlocked],
		BaselineCoverage:       visual.Ratio(data.BaselineCovered, data.AssertionTotal),
		PassRate:               visual.Ratio(counts[visual.ClassificationPassed], counts[visual.ClassificationPassed]+counts[visual.ClassificationFailed]),
		InconclusiveRate:       visual.Ratio(counts[visual.ClassificationInconclusive], total),
		TopInconclusiveReasons: boundedReasons(data.InconclusiveReasons, 10),
		GeneratedAt:            s.now(),
	}, nil
}

func buildOverview(versionID int64, data OverviewData) Overview {
	counts := normalizedCounts(data.DiffCounts)
	overview := Overview{
		VersionID:             versionID,
		AssertionTotal:        data.AssertionTotal,
		BaselineCovered:       data.BaselineCovered,
		BaselineCoverageRatio: visual.Ratio(data.BaselineCovered, data.AssertionTotal),
		Passed:                counts[visual.ClassificationPassed],
		Failed:                counts[visual.ClassificationFailed],
		Inconclusive:          counts[visual.ClassificationInconclusive],
		Blocked:               counts[visual.ClassificationBlocked],
		PassRatio:             visual.Ratio(counts[visual.ClassificationPassed], counts[visual.ClassificationPassed]+counts[visual.ClassificationFailed]),
		InconclusiveReasons:   boundedReasons(data.InconclusiveReasons, 20),
		RecentFailures:        data.RecentFailures,
	}
	if len(data.DiffRatios) > 0 {
		p50 := visual.Percentile(data.DiffRatios, 0.50)
		p95 := visual.Percentile(data.DiffRatios, 0.95)
		overview.DiffRatioP50 = &p50
		overview.DiffRatioP95 = &p95
	}
	return overview
}

func normalizedCounts(input map[visual.Classification]int) map[visual.Classification]int {
	counts := map[visual.Classification]int{
		visual.ClassificationPassed:       0,
		visual.ClassificationFailed:       0,
		visual.ClassificationInconclusive: 0,
		visual.ClassificationBlocked:      0,
	}
	for key, value := range input {
		counts[key] = value
	}
	return counts
}

func boundedReasons(reasons []ReasonCount, max int) []ReasonCount {
	if len(reasons) <= max {
		return reasons
	}
	return reasons[:max]
}

func (s *OverviewService) now() time.Time {
	if s.clock != nil {
		return s.clock.Now()
	}
	return time.Now()
}
