package adapters

import (
	"context"

	qualityapp "github.com/EdouardRichard/EzTest/backend/internal/quality/application"
	visualapp "github.com/EdouardRichard/EzTest/backend/internal/visual/application"
)

// VisualOverview is the visual context's read-only metrics surface
// (*visualapp.OverviewService satisfies it).
type VisualOverview interface {
	Global(ctx context.Context, versionID *int64) (visualapp.Metrics, error)
}

// VisualClassification adapts the visual metrics overview onto the quality
// snapshot port. Inconclusive/blocked diffs are not an evaluation opportunity, so
// the accuracy denominator is passed+failed only.
type VisualClassification struct {
	overview VisualOverview
}

// NewVisualClassification wires the visual classification adapter.
func NewVisualClassification(overview VisualOverview) *VisualClassification {
	return &VisualClassification{overview: overview}
}

var _ qualityapp.VisualSnapshotPort = (*VisualClassification)(nil)

// VisualClassification implements qualityapp.VisualSnapshotPort.
func (a *VisualClassification) VisualClassification(ctx context.Context, versionID *int64) (int, int, error) {
	if a == nil || a.overview == nil {
		return 0, 0, nil
	}
	metrics, err := a.overview.Global(ctx, versionID)
	if err != nil {
		return 0, 0, err
	}
	return metrics.Passed, metrics.Failed, nil
}
