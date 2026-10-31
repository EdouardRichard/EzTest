// Package chainapp implements the read-only exploration-chain use case and
// declares the provider ports it consumes. Domain packages never import
// another context; every provider returns chain domain nodes whose Kind
// identifies the entity, so the builder can group them without knowing the
// provider's context.
package chainapp

import (
	"context"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/domain"
)

// VersionChainProvider supplies requirement and version SUT nodes (spec §8.1).
type VersionChainProvider interface {
	Requirements(ctx context.Context, versionID int64, limit int) ([]chain.Node, error)
	VersionSUTs(ctx context.Context, versionID int64) ([]chain.Node, error)
}

// DesignChainProvider supplies scenario and case-version nodes. CaseVersionsByIDs
// is the historical reader: it must return superseded rows with their title.
type DesignChainProvider interface {
	Scenarios(ctx context.Context, versionID int64) ([]chain.Node, error)
	Cases(ctx context.Context, versionID int64) ([]chain.Node, error)
	CaseVersionsByIDs(ctx context.Context, versionID int64, ids []int64) ([]chain.Node, error)
}

// ExecutionChainProvider supplies wave and case-run nodes (spec §8.1).
type ExecutionChainProvider interface {
	Waves(ctx context.Context, versionID int64) ([]chain.Node, error)
	CaseRuns(ctx context.Context, versionID int64) ([]chain.Node, error)
}

// DeliveryChainProvider supplies issue, report and closure nodes (spec §8.1).
type DeliveryChainProvider interface {
	Issues(ctx context.Context, versionID int64) ([]chain.Node, error)
	Reports(ctx context.Context, versionID int64) ([]chain.Node, error)
	Closures(ctx context.Context, versionID int64) ([]chain.Node, error)
}

// ResourceChainProvider resolves display names for version SUTs and pages
// (spec §8.1).
type ResourceChainProvider interface {
	SUTNames(ctx context.Context, versionID int64) (map[int64]string, error)
	PageNames(ctx context.Context, versionID int64) (map[int64]string, error)
}

// CursorReader reads the latest event seq of one channel; the chain service
// returns it as ChainView.SnapshotCursor (spec §8.4).
type CursorReader interface {
	LatestCursor(ctx context.Context, channel string) (int64, error)
}

// EventTimeReader maps one event seq of a channel to the event's created_at
// timestamp. The chain service uses it to turn event_from/event_to into
// approximate entity created_at bounds (spec §8.4); found=false means the
// channel no longer exposes a row for that seq.
type EventTimeReader interface {
	EventTime(ctx context.Context, channel string, seq int64) (createdAt time.Time, found bool, err error)
}
