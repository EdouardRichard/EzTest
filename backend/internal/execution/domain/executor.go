package execution

import "time"

// ExecutorStatus is the executor_instance status (§4.8).
type ExecutorStatus string

const (
	ExecutorActive   ExecutorStatus = "active"
	ExecutorDraining ExecutorStatus = "draining"
	ExecutorOffline  ExecutorStatus = "offline"
)

// Valid reports whether status is a persisted executor status.
func (s ExecutorStatus) Valid() bool {
	switch s {
	case ExecutorActive, ExecutorDraining, ExecutorOffline:
		return true
	default:
		return false
	}
}

// Available reports whether the executor instance still owns in-flight
// contexts (active or draining).
func (s ExecutorStatus) Available() bool { return s == ExecutorActive || s == ExecutorDraining }

// Dispatchable reports whether new case_runs may be dispatched to it.
func (s ExecutorStatus) Dispatchable() bool { return s == ExecutorActive }

// ExecutorInstance is one registered Playwright executor instance. P3 keeps a
// single endpoint; the table is the P7 multi-executor reservation.
type ExecutorInstance struct {
	ID             int64
	Name           string
	Endpoint       string
	TokenHash      string
	Capacity       int
	ActiveContexts int
	Labels         map[string]any
	Status         ExecutorStatus
	LastHeartbeat  *time.Time
	Version        string
	LastError      string
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// FreeContexts returns the remaining browser-context capacity.
func (e ExecutorInstance) FreeContexts() int {
	free := e.Capacity - e.ActiveContexts
	if free < 0 {
		return 0
	}
	return free
}

// Executor heartbeat/context defaults used when settings are absent.
const (
	DefaultExecutorHeartbeatSeconds  = 10
	DefaultExecutorOfflineSeconds    = 30
	DefaultExecutorContextTTLSeconds = 300
)
