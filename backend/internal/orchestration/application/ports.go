// Package orchestrationapp implements the P7 multi-replica orchestration use
// cases: api_instance registration, snowflake node allocation, heartbeat,
// drain/activate, stale-instance scanning and the readiness probe. It depends
// only on the orchestration domain, the shared platform kernel and the ports
// declared here.
package orchestrationapp

import (
	"context"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	"github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
)

// Settings keys consumed by the registry (contracts/settings.md §4g).
const (
	SettingNodeID           = "runtime.snowflake_node_id"
	SettingHeartbeatSeconds = "instance.heartbeat_seconds"
	SettingOfflineSeconds   = "instance.offline_seconds"
)

// Snowflake node ID bounds (10 node bits).
const (
	MinNodeID = 0
	MaxNodeID = 1023
)

// Defaults apply when the settings reader is missing or returns an
// out-of-range value (contracts/settings.md defaults).
const (
	DefaultHeartbeatSeconds = 10
	DefaultOfflineSeconds   = 30
)

// System-channel events emitted by the registry (contracts/events.md §16).
const (
	EventRegistered = "instance.registered"
	EventDrained    = "instance.drained"
	EventActivated  = "instance.activated"
	EventOffline    = "instance.offline"
)

// Admin audit actions emitted by drain/activate.
const (
	ActionDrained   = "orchestration.instance.drained"
	ActionActivated = "orchestration.instance.activated"
)

// StartMeta is the process identity a replica registers. InstanceKey is built
// as hostname:pid (spec §4.5).
type StartMeta struct {
	Hostname string
	PID      int
	APIPort  int
	Version  string
}

// Repository persists api_instance rows. The PostgreSQL adapter owns the SQL;
// it translates uniqueness violations into the orchestration domain sentinels
// and reclaims node IDs held only by offline/stopped rows.
type Repository interface {
	// UsedNodeIDs returns the node IDs held by live instances (active or
	// draining). Offline/stopped rows are reclaimable and therefore omitted.
	UsedNodeIDs(ctx context.Context) ([]int, error)
	// Insert registers one instance. It returns orchestration.ErrNodeTaken when
	// a live instance owns the node and orchestration.ErrInstanceKeyTaken when
	// hostname:pid is already registered.
	Insert(ctx context.Context, instance orchestration.Instance) error
	// Get returns one instance or orchestration.ErrNotFound.
	Get(ctx context.Context, id int64) (orchestration.Instance, error)
	// List returns every registered instance.
	List(ctx context.Context) ([]orchestration.Instance, error)
	// Heartbeat updates heartbeat_at when the instance is still active or
	// draining. It reports the persisted status and whether the row matched, so
	// the service can mirror an externally requested drain.
	Heartbeat(ctx context.Context, id int64, at time.Time) (orchestration.Status, bool, error)
	// SetStatus applies an optimistic status transition (WHERE status = from)
	// and returns the updated row plus whether the transition happened.
	SetStatus(ctx context.Context, id int64, from, to orchestration.Status, at time.Time) (orchestration.Instance, bool, error)
	// MarkStopped marks a gracefully stopped instance and returns whether a row
	// changed. Stopped rows are reclaimable by the next allocation.
	MarkStopped(ctx context.Context, id int64, at time.Time) (bool, error)
	// MarkOfflineStale marks every active instance whose heartbeat is older
	// than cutoff as offline, returning the rows that transitioned exactly
	// once.
	MarkOfflineStale(ctx context.Context, cutoff time.Time) ([]orchestration.Instance, error)
	// Status returns the persisted status of one instance.
	Status(ctx context.Context, id int64) (orchestration.Status, error)
	// Ping checks database reachability for readiness.
	Ping(ctx context.Context) error
}

// SettingsReader is the registry's view of the settings centre.
type SettingsReader interface {
	Int(ctx context.Context, key string) (int64, error)
}

// StorageProbe verifies that the shared data root is writable from this
// replica (readiness storage condition). It is implemented by the filesystem
// adapter.
type StorageProbe interface {
	Probe(ctx context.Context) error
}

// Readiness is the GET /api/health/ready condition set: database reachable,
// shared storage writable, and this instance not draining.
type Readiness struct {
	Database   bool
	Storage    bool
	Draining   bool
	InstanceID int64
}

// Ready reports whether the instance may serve traffic.
func (r Readiness) Ready() bool {
	return r.Database && r.Storage && !r.Draining
}

// Publisher and Recorder are aliases for the shared ports, kept local so this
// file documents the full application surface.
type Publisher = eventingapp.Publisher
type Recorder = auditapp.Recorder
