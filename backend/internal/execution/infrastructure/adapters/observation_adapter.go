package adapters

import (
	"context"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// DiscoveryObservationDeltaStore is the narrow execution-side query surface the
// adapter needs. *executionpg.DiscoveryObservationRepository implements it; the
// interface keeps this package free of a concrete postgres import.
type DiscoveryObservationDeltaStore interface {
	ListForDelta(ctx context.Context, versionID, waveID int64) ([]executionapp.DiscoveryDeltaObservation, error)
	MarkDiscoveryObservationDecision(ctx context.Context, observationID int64, decision string, resourceID, factID int64) error
}

// DiscoveryObservationAdapter projects execution discovery observations and
// decision writes onto the version context's delta ports (W1-12). The version
// context owns the classification but may not read or write the execution
// table directly (Agent.md §6.2); this adapter is the only bridge.
type DiscoveryObservationAdapter struct {
	store DiscoveryObservationDeltaStore
}

// NewDiscoveryObservationAdapter wires one adapter over the execution
// observation repository.
func NewDiscoveryObservationAdapter(store DiscoveryObservationDeltaStore) *DiscoveryObservationAdapter {
	return &DiscoveryObservationAdapter{store: store}
}

var (
	_ versionapp.DiscoveryObservationReader  = (*DiscoveryObservationAdapter)(nil)
	_ versionapp.DiscoveryObservationDecider = (*DiscoveryObservationAdapter)(nil)
)

// ListForDelta returns the delta projection of a wave. It implements
// versionapp.DiscoveryObservationReader.
func (a *DiscoveryObservationAdapter) ListForDelta(ctx context.Context, versionID, waveID int64) ([]versionapp.DiscoveryObservation, error) {
	rows, err := a.store.ListForDelta(ctx, versionID, waveID)
	if err != nil {
		return nil, err
	}
	observations := make([]versionapp.DiscoveryObservation, 0, len(rows))
	for _, row := range rows {
		observations = append(observations, versionapp.DiscoveryObservation{
			ObservationID:    row.ObservationID,
			VersionID:        row.VersionID,
			WaveID:           row.WaveID,
			CaseRunID:        row.CaseRunID,
			VersionSUTID:     row.VersionSUTID,
			EnvironmentID:    row.EnvironmentID,
			Kind:             row.Kind,
			Method:           row.Method,
			Host:             row.Host,
			ResourceTemplate: row.ResourceTemplate,
			Decision:         row.Decision,
			ResourceID:       row.ResourceID,
			FactID:           row.FactID,
		})
	}
	return observations, nil
}

// MarkDecision persists the classification of one observation. It implements
// versionapp.DiscoveryObservationDecider.
func (a *DiscoveryObservationAdapter) MarkDecision(ctx context.Context, observationID int64, decision string, resourceID, factID int64) error {
	return a.store.MarkDiscoveryObservationDecision(ctx, observationID, decision, resourceID, factID)
}
