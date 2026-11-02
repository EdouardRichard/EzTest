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
// Requirements must return up to limit roots, paging its backing reader when one
// reader page is capped; the service asks for MaxRequirements+1 so BuildChain
// can report requirement_budget truncation.
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

// SettingsReader is the hot settings cache surface consumed per request by the
// P6 chain service (spec §4.3.6). Int must return the currently cached value;
// the settings application service satisfies it, so a PATCH settings update is
// visible on the next chain request without a service rebuild. Read failures
// fall back to the statically wired budget.
type SettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
}

// DesignSnapshotProvider is the optional one-read fast path of the design
// provider. When implemented, the service fetches scenarios and cases from the
// same design graph snapshot instead of reading the graph twice. The query
// budget test counts this as one bounded SQL read.
type DesignSnapshotProvider interface {
	DesignSnapshot(ctx context.Context, versionID int64) (scenarios, cases []chain.Node, err error)
}

// ExecutionSnapshotProvider is the optional one-read fast path of the execution
// provider: waves and case runs come from one wave walk.
type ExecutionSnapshotProvider interface {
	ExecutionSnapshot(ctx context.Context, versionID int64) (waves, caseRuns []chain.Node, err error)
}

// ResourceSnapshotProvider is the optional one-read fast path of the resource
// provider: version-SUT labels and page labels are resolved from one version
// binding read.
type ResourceSnapshotProvider interface {
	ResourceSnapshot(ctx context.Context, versionID int64) (sutNames, pageNames map[int64]string, err error)
}
