// Package orchestration is the P7 API-instance registry domain. One row of
// api_instance represents one running API replica; node_id is its snowflake
// node, unique among live replicas.
package orchestration

import "time"

// Status is the lifecycle state of one API instance.
type Status string

const (
	// StatusActive is a live instance that serves traffic and may claim work.
	StatusActive Status = "active"
	// StatusDraining is a live instance that no longer accepts new work; its
	// in-flight work is allowed to finish before the process stops.
	StatusDraining Status = "draining"
	// StatusOffline is a registered instance whose heartbeat went stale.
	StatusOffline Status = "offline"
	// StatusStopped is a gracefully stopped instance.
	StatusStopped Status = "stopped"
)

// Valid reports whether the status is one of the four persisted states.
func (s Status) Valid() bool {
	switch s {
	case StatusActive, StatusDraining, StatusOffline, StatusStopped:
		return true
	default:
		return false
	}
}

// Instance is one API replica registration (api_instance row).
type Instance struct {
	ID          int64
	InstanceKey string
	Hostname    string
	PID         int
	APIPort     int
	NodeID      int
	Status      Status
	Version     string
	// ActiveRequests/ActiveWork are the in-flight counters reported by the
	// replica's heartbeat (P8 drain observability). They never affect
	// scheduling; they tell an operator when a draining replica is idle.
	ActiveRequests int
	ActiveWork     int
	LastError      *string
	StartedAt      time.Time
	HeartbeatAt    time.Time
	DrainingAt     *time.Time
	StoppedAt      *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// Reclaimable reports whether the instance's node ID may be reallocated to a
// new registration. Only dead instances (offline from a stale heartbeat, or
// gracefully stopped) are reclaimable; a live process must keep its node.
func (i Instance) Reclaimable() bool {
	return i.Status == StatusOffline || i.Status == StatusStopped
}

// CanTransition reports whether the registry may move an instance from one
// status to another. Offline instances are never revived: the process either
// restarts (new registration) or stops; a live process that missed heartbeats
// is treated as split-brain and must not resume under the same node.
func CanTransition(from, to Status) bool {
	switch from {
	case StatusActive:
		return to == StatusDraining || to == StatusOffline || to == StatusStopped
	case StatusDraining:
		return to == StatusActive || to == StatusOffline || to == StatusStopped
	case StatusOffline:
		return to == StatusStopped
	case StatusStopped:
		return false
	default:
		return false
	}
}
