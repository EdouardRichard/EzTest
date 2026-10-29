package domain

import (
	"encoding/json"
	"time"
)

// Snapshot is an immutable record of one login_check or manual confirmation.
// Items and Summary are the JSONB documents described in spec §6.3.
type Snapshot struct {
	ID             int64
	SessionID      int64
	Kind           string
	Items          json.RawMessage
	Summary        json.RawMessage
	PermissionHash string
	Degraded       bool
	CapturedAt     time.Time
}
