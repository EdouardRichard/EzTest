// Package codeintel adapts the codeintel application service to the execution
// autonomous worker's CodeImpactPort (spec §6.1/§6.2). The continuing-design
// segment triggers and awaits the P4 impact analysis of the parent batch head;
// start failures are translated to the worker's degrade sentinels so an AI or
// repository outage never wedges the version.
package codeintel

import (
	"context"
	"errors"
	"fmt"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
)

// Service is the codeintel analysis slice the adapter needs.
// *codeintel/application.AnalysisService satisfies it.
type Service interface {
	Start(ctx context.Context, versionID, actorID int64, in codeintelapp.StartAnalysisInput) (codeinteldomain.CodeAnalysis, bool, error)
	Get(ctx context.Context, versionID, analysisID int64) (codeinteldomain.CodeAnalysis, error)
	GetLatest(ctx context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error)
}

// ImpactPort implements execution/application.CodeImpactPort over the codeintel
// analysis service.
type ImpactPort struct {
	service Service
}

// NewImpactPort wires the execution-side code-impact adapter.
func NewImpactPort(service Service) *ImpactPort {
	return &ImpactPort{service: service}
}

var _ executionapp.CodeImpactPort = (*ImpactPort)(nil)

// StartImpact starts (or reuses) the impact analysis of the explicit base
// commit. The system actor (0) owns autonomous analyses, matching the other
// startup writes.
func (p *ImpactPort) StartImpact(ctx context.Context, versionID int64, baseCommit string) (int64, error) {
	if p == nil || p.service == nil {
		return 0, errors.New("execution codeintel: analysis service is not configured")
	}
	analysis, _, err := p.service.Start(ctx, versionID, 0, codeintelapp.StartAnalysisInput{BaseCommit: baseCommit})
	if err != nil {
		return 0, mapImpactStartError(err)
	}
	return analysis.ID, nil
}

// GetImpact reads one analysis of versionID; a missing analysis is (nil, nil).
func (p *ImpactPort) GetImpact(ctx context.Context, versionID, analysisID int64) (*executionapp.ImpactView, error) {
	if p == nil || p.service == nil {
		return nil, errors.New("execution codeintel: analysis service is not configured")
	}
	analysis, err := p.service.Get(ctx, versionID, analysisID)
	if errors.Is(err, codeinteldomain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return impactView(analysis), nil
}

// LatestImpact reads the newest analysis of versionID; none is (nil, nil).
func (p *ImpactPort) LatestImpact(ctx context.Context, versionID int64) (*executionapp.ImpactView, error) {
	if p == nil || p.service == nil {
		return nil, errors.New("execution codeintel: analysis service is not configured")
	}
	analysis, err := p.service.GetLatest(ctx, versionID)
	if errors.Is(err, codeinteldomain.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return impactView(analysis), nil
}

// mapImpactStartError translates a codeintel start error to the worker
// sentinels: an active analysis means wait, an unbindable range means continue
// without impact.
func mapImpactStartError(err error) error {
	if err == nil {
		return nil
	}
	switch codeinteldomain.Reason(err) {
	case "analysis_in_progress":
		return fmt.Errorf("%w: %v", executionapp.ErrAutonomyImpactInProgress, err)
	case "code_repository_not_bound", "baseline_commit_missing",
		"invalid_base_commit", "invalid_head", "head_commit_missing":
		return fmt.Errorf("%w: %v", executionapp.ErrAutonomyImpactUnavailable, err)
	default:
		return err
	}
}

// impactView projects one analysis onto the worker's view.
func impactView(analysis codeinteldomain.CodeAnalysis) *executionapp.ImpactView {
	return &executionapp.ImpactView{
		ID:         analysis.ID,
		Status:     analysis.Status,
		BaseCommit: analysis.BaseCommit,
		HeadCommit: analysis.HeadCommit,
		Degraded:   analysis.Degraded,
	}
}
