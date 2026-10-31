package execution

import (
	"strings"
	"time"
)

// IntentStatus is the execution_intent.state machine status (spec §4.1).
type IntentStatus string

const (
	IntentOpen       IntentStatus = "open"
	IntentClaimed    IntentStatus = "claimed"
	IntentRunning    IntentStatus = "running"
	IntentDone       IntentStatus = "done"
	IntentFailed     IntentStatus = "failed"
	IntentCancelled  IntentStatus = "cancelled"
	IntentSuperseded IntentStatus = "superseded"
)

// Purpose distinguishes automatic initial intents from explicit reruns.
type Purpose string

const (
	PurposeInitial Purpose = "initial"
	PurposeRetest  Purpose = "retest"
	PurposeManual  Purpose = "manual"
)

// Valid reports whether status is one of the nine persisted statuses.
func (s IntentStatus) Valid() bool {
	switch s {
	case IntentOpen, IntentClaimed, IntentRunning, IntentDone, IntentFailed, IntentCancelled, IntentSuperseded:
		return true
	default:
		return false
	}
}

// Active reports whether the intent occupies the partial-unique active slot.
func (s IntentStatus) Active() bool {
	switch s {
	case IntentOpen, IntentClaimed, IntentRunning:
		return true
	default:
		return false
	}
}

// Terminal reports whether no further transition is possible.
func (s IntentStatus) Terminal() bool { return s.Valid() && !s.Active() }

// CanTransitionTo implements the §4.1 intent state machine. Same-status
// transitions are rejected here; idempotent cancellation is handled by Cancel.
func (s IntentStatus) CanTransitionTo(next IntentStatus) bool {
	switch s {
	case IntentOpen:
		return next == IntentClaimed || next == IntentFailed || next == IntentCancelled || next == IntentSuperseded
	case IntentClaimed:
		return next == IntentRunning || next == IntentFailed || next == IntentCancelled || next == IntentSuperseded
	case IntentRunning:
		return next == IntentDone || next == IntentFailed || next == IntentCancelled || next == IntentSuperseded
	default:
		return false
	}
}

// Valid reports whether purpose is one of the three frozen purposes.
func (p Purpose) Valid() bool {
	switch p {
	case PurposeInitial, PurposeRetest, PurposeManual:
		return true
	default:
		return false
	}
}

// ExecutionIntent is one queued execution request for a current+confirmed case
// version under one environment/code-baseline boundary.
type ExecutionIntent struct {
	ID                     int64
	VersionID              int64
	CaseVersionID          int64
	VersionSUTIDs          []int64
	EnvironmentFingerprint string
	CodeBaseline           string
	Purpose                Purpose
	Status                 IntentStatus
	DedupeKey              string
	AttemptNo              int
	CreatedBy              int64
	CreatedAt              time.Time
	UpdatedAt              time.Time
}

// Validate checks the invariants required before an intent is persisted.
func (i ExecutionIntent) Validate() error {
	if i.VersionID <= 0 {
		return ErrStateConflict
	}
	if i.CaseVersionID <= 0 {
		return ErrStateConflict
	}
	if len(i.EnvironmentFingerprint) != FingerprintLength || !isLowerHex(i.EnvironmentFingerprint) {
		return ErrStateConflict
	}
	if len(i.CodeBaseline) > CodeBaselineMaxLength {
		return ErrStateConflict
	}
	if !i.Purpose.Valid() {
		return ErrStateConflict
	}
	if !i.Status.Valid() {
		return ErrStateConflict
	}
	if i.AttemptNo < 1 {
		return ErrStateConflict
	}
	if len(i.DedupeKey) > DedupeKeyMaxLength {
		return ErrStateConflict
	}
	return nil
}

// Cancel moves an active intent to cancelled. It is idempotent: a cancelled
// intent reports changed=false and no error. Terminal non-cancelled intents
// reject cancellation with ErrStateConflict.
func (i *ExecutionIntent) Cancel() (bool, error) {
	if i.Status == IntentCancelled {
		return false, nil
	}
	if !i.Status.CanTransitionTo(IntentCancelled) {
		return false, ErrStateConflict
	}
	i.Status = IntentCancelled
	return true, nil
}

const (
	// FingerprintLength is the lowercase hex sha256 length.
	FingerprintLength = 64
	// CodeBaselineMaxLength bounds code_baseline (spec §3.1).
	CodeBaselineMaxLength = 256
	// DedupeKeyMaxLength bounds dedupe_key (spec §3.1).
	DedupeKeyMaxLength = 256
)

func isLowerHex(value string) bool {
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

// TrimDedupeKey normalizes a dedupe key before persistence.
func TrimDedupeKey(value string) string { return strings.TrimSpace(value) }
