package adapters

// W1-14A: production adapter from the version-context DiscoveryDelta service to
// the scheduler's narrow executionapp.DiscoveryDeltaEvaluator port. The version
// service returns its five-way DeltaResult; the scheduler only needs the
// success/failure signal, so the adapter discards the result.
//
// versionapp.ErrDeltaBusy means another cooperative pass owns the (version,wave)
// advisory lock. The wave-terminal trigger treats that as a skipped pass, never
// as a wave failure, so the adapter maps it to nil and logs at debug level.

import (
	"context"
	"errors"
	"log/slog"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// versionDiscoveryDelta is the narrow version-context surface the adapter needs.
// *versionapp.DiscoveryDelta implements it.
type versionDiscoveryDelta interface {
	Evaluate(ctx context.Context, versionID, waveID int64) (versionapp.DeltaResult, error)
}

// DiscoveryDeltaEvaluator adapts versionapp.DiscoveryDelta to the scheduler's
// executionapp.DiscoveryDeltaEvaluator port.
type DiscoveryDeltaEvaluator struct {
	delta  versionDiscoveryDelta
	logger *slog.Logger
}

// NewDiscoveryDeltaEvaluator wires one adapter. A nil logger uses slog.Default.
func NewDiscoveryDeltaEvaluator(delta versionDiscoveryDelta, logger *slog.Logger) *DiscoveryDeltaEvaluator {
	if logger == nil {
		logger = slog.Default()
	}
	return &DiscoveryDeltaEvaluator{delta: delta, logger: logger}
}

var _ executionapp.DiscoveryDeltaEvaluator = (*DiscoveryDeltaEvaluator)(nil)

// Evaluate runs one delta pass and reports only success/failure. A nil delta
// (degraded assembly) is a no-op; ErrDeltaBusy is a skipped pass (nil).
func (a *DiscoveryDeltaEvaluator) Evaluate(ctx context.Context, versionID, waveID int64) error {
	if a == nil || a.delta == nil {
		return nil
	}
	_, err := a.delta.Evaluate(ctx, versionID, waveID)
	if err == nil {
		return nil
	}
	if errors.Is(err, versionapp.ErrDeltaBusy) {
		a.logger.DebugContext(ctx,
			"execution: discovery delta pass skipped (busy); another evaluation owns the version/wave lock",
			"version_id", versionID, "wave_id", waveID)
		return nil
	}
	return err
}
