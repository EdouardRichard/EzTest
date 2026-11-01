package orchestration

import "errors"

// Repository sentinel errors. Adapters translate driver-specific uniqueness
// violations and missing rows into these values; the application layer never
// looks at pgx error codes.
var (
	// ErrNotFound means no api_instance row matches the requested ID.
	ErrNotFound = errors.New("orchestration: instance not found")
	// ErrNodeTaken means the candidate node_id is held by a live
	// (active or draining) instance.
	ErrNodeTaken = errors.New("orchestration: node id already allocated")
	// ErrInstanceKeyTaken means hostname:pid is already registered.
	ErrInstanceKeyTaken = errors.New("orchestration: instance key already registered")
)
