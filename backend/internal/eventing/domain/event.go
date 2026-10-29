// Package eventing is the eventing context's domain model: immutable stream
// events addressed by a channel-local sequence number. The V1 envelope is
// frozen for the P0 API, so its field set matches the persisted columns and the
// SSE payload one-to-one.
package eventing

import (
	"encoding/json"
	"time"
)

// Refs groups the references an event points at. The four arrays are always
// present in the API envelope; a missing reference is an empty array, never
// null. The JSON keys are the frozen lowercase envelope names the refs JSONB
// column stores.
type Refs struct {
	Evidence    []string `json:"evidence"`
	Case        []string `json:"case"`
	Requirement []string `json:"requirement"`
	Code        []string `json:"code"`
}

// Event is one persisted stream event. Seq is assigned by the store and is
// monotonic per channel. Detail is a JSON object no larger than 64 KiB.
type Event struct {
	Seq       int64
	Channel   string
	Type      string
	VersionID *string
	BatchID   *string
	CaseID    *string
	StepID    *string
	AgentRole *string
	SessionID *string
	TS        time.Time
	Summary   string
	Detail    json.RawMessage
	Refs      Refs
}

// NewEvent is an event waiting to be published: the store assigns Seq. A zero
// TS is replaced with the current time.
type NewEvent struct {
	Channel   string
	Type      string
	VersionID *string
	BatchID   *string
	CaseID    *string
	StepID    *string
	AgentRole *string
	SessionID *string
	Summary   string
	Detail    json.RawMessage
	Refs      Refs
	TS        time.Time
}
