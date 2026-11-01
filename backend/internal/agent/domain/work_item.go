package agent

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// WorkItemStatus is the task-board lifecycle state. P1 writes open, claimed,
// running, done, failed and dead; waiting_human is reserved for P3+.
type WorkItemStatus string

// Work item statuses (7-state design; P1 does not write waiting_human).
const (
	WorkItemOpen         WorkItemStatus = "open"
	WorkItemClaimed      WorkItemStatus = "claimed"
	WorkItemRunning      WorkItemStatus = "running"
	WorkItemWaitingHuman WorkItemStatus = "waiting_human"
	WorkItemDone         WorkItemStatus = "done"
	WorkItemFailed       WorkItemStatus = "failed"
	WorkItemDead         WorkItemStatus = "dead"
)

// Valid reports whether the status is part of the frozen 7-state enum.
func (s WorkItemStatus) Valid() bool {
	switch s {
	case WorkItemOpen, WorkItemClaimed, WorkItemRunning, WorkItemWaitingHuman,
		WorkItemDone, WorkItemFailed, WorkItemDead:
		return true
	default:
		return false
	}
}

// Terminal reports whether the work item needs no further worker processing.
func (s WorkItemStatus) Terminal() bool {
	switch s {
	case WorkItemDone, WorkItemFailed, WorkItemDead:
		return true
	default:
		return false
	}
}

// AttemptStatus is the outcome of one lease attempt.
type AttemptStatus string

// Per-attempt outcomes persisted in work_item_attempt.
const (
	AttemptClaimed   AttemptStatus = "claimed"
	AttemptRunning   AttemptStatus = "running"
	AttemptDone      AttemptStatus = "done"
	AttemptFailed    AttemptStatus = "failed"
	AttemptDead      AttemptStatus = "dead"
	AttemptRecovered AttemptStatus = "recovered"
)

// Valid reports whether the status is part of the attempt enum.
func (s AttemptStatus) Valid() bool {
	switch s {
	case AttemptClaimed, AttemptRunning, AttemptDone, AttemptFailed, AttemptDead, AttemptRecovered:
		return true
	default:
		return false
	}
}

// Attempt bounds for a single execution.
const (
	MinMaxAttempts     = 1
	MaxMaxAttempts     = 10
	DefaultMaxAttempts = 3
)

// WorkItem is one row of the single execution queue.
type WorkItem struct {
	ID          int64
	VersionID   int64
	Team        Team
	Type        string
	Status      WorkItemStatus
	Priority    int
	DedupeKey   string
	DependsOn   *int64
	Payload     json.RawMessage
	ClaimedBy   *string
	LeaseUntil  *time.Time
	LeaseEpoch  int64
	Attempts    int
	MaxAttempts int
	LastError   *string
	AvailableAt time.Time
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NewWorkItem is the idempotent Ensure input. Payload holds references and
// small data only (never full chat transcripts).
type NewWorkItem struct {
	VersionID   int64
	Team        string
	Type        string
	Priority    int
	DedupeKey   string
	Payload     any
	MaxAttempts int
}

// Validate checks the required fields, team enum and attempt bound.
func (in NewWorkItem) Validate() error {
	if in.VersionID <= 0 {
		return platform.Invalid("version_id", "must be a positive decimal ID")
	}
	if !Team(in.Team).Valid() {
		return platform.Invalid("team", "must be one of clarification, design, execution, delivery, steward, code")
	}
	if strings.TrimSpace(in.Type) == "" {
		return platform.Invalid("type", "must not be empty")
	}
	if strings.TrimSpace(in.DedupeKey) == "" {
		return platform.Invalid("dedupe_key", "must not be empty")
	}
	maxAttempts := in.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = DefaultMaxAttempts
	}
	if maxAttempts < MinMaxAttempts || maxAttempts > MaxMaxAttempts {
		return platform.Invalid("max_attempts", "must be between 1 and 10")
	}
	if in.Payload != nil {
		raw, err := json.Marshal(in.Payload)
		if err != nil {
			return platform.Invalid("payload", "must be JSON-encodable")
		}
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return platform.Invalid("payload", "must be a JSON object")
		}
	}
	return nil
}

// EncodePayload renders the payload as a JSON object, defaulting to {}.
func (in NewWorkItem) EncodePayload() (json.RawMessage, error) {
	if in.Payload == nil {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(in.Payload)
	if err != nil {
		return nil, platform.Invalid("payload", "must be JSON-encodable")
	}
	return raw, nil
}

// WorkItemAttempt is one immutable attempt-log row for a work item.
type WorkItemAttempt struct {
	ID         int64
	WorkItemID int64
	Attempt    int
	Status     AttemptStatus
	Error      *string
	StartedAt  time.Time
	FinishedAt *time.Time
}

// Validate checks the attempt row identity and status.
func (a WorkItemAttempt) Validate() error {
	if a.WorkItemID <= 0 {
		return platform.Invalid("work_item_id", "must be a positive decimal ID")
	}
	if a.Attempt <= 0 {
		return platform.Invalid("attempt", "must be positive")
	}
	if !a.Status.Valid() {
		return platform.Invalid("status", "must be a valid attempt status")
	}
	return nil
}
