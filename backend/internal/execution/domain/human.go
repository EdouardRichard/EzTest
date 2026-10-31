package execution

import "time"

// HumanRequestStatus is the human_request state machine status (§4.7).
type HumanRequestStatus string

const (
	HumanOpen      HumanRequestStatus = "open"
	HumanResolved  HumanRequestStatus = "resolved"
	HumanCancelled HumanRequestStatus = "cancelled"
	HumanExpired   HumanRequestStatus = "expired"
)

// HumanKind is the human_request kind enum. P3 only creates login_takeover;
// conflict/other are reserved for P4/P6.
type HumanKind string

const (
	HumanLoginTakeover HumanKind = "login_takeover"
	HumanConflict      HumanKind = "conflict"
	HumanOther         HumanKind = "other"
)

// MaxHumanRequestsPerCase bounds takeover attempts of one case_run (§6.8).
const MaxHumanRequestsPerCase = 3

// Valid reports whether status is a persisted human request status.
func (s HumanRequestStatus) Valid() bool {
	switch s {
	case HumanOpen, HumanResolved, HumanCancelled, HumanExpired:
		return true
	default:
		return false
	}
}

// Terminal reports whether the request is closed.
func (s HumanRequestStatus) Terminal() bool { return s.Valid() && s != HumanOpen }

// CanTransitionTo implements the §4.7 human request state machine.
func (s HumanRequestStatus) CanTransitionTo(next HumanRequestStatus) bool {
	return s == HumanOpen && (next == HumanResolved || next == HumanCancelled || next == HumanExpired)
}

// Valid reports whether kind is a persisted human request kind.
func (k HumanKind) Valid() bool {
	switch k {
	case HumanLoginTakeover, HumanConflict, HumanOther:
		return true
	default:
		return false
	}
}

// HumanRequest is one open takeover request for a case_run; the case_run and
// wave stay paused until it is resolved, cancelled or expired.
type HumanRequest struct {
	ID         int64
	VersionID  int64
	CaseRunID  *int64
	Kind       HumanKind
	Payload    map[string]any
	Status     HumanRequestStatus
	Response   map[string]any
	ExpiresAt  time.Time
	ResolvedBy *int64
	ResolvedAt *time.Time
	CreatedBy  *int64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Human request payload field names (spec §3.3).
const (
	HumanPayloadReason        = "reason"
	HumanPayloadVersionSUTID  = "version_sut_id"
	HumanPayloadAccountRole   = "account_role"
	HumanPayloadPageURL       = "page_url"
	HumanPayloadChallengeKind = "challenge_kind"
	HumanPayloadEvidenceID    = "evidence_id"
)

// Human request response field names (spec §3.3 / §6.8).
const (
	HumanResponseCode = "code"
	HumanResponseNote = "note"
)

// Human request verdict reasons written to case_run.summary.
const (
	VerdictReasonHumanTimeout   = "human_timeout"
	VerdictReasonHumanCancelled = "human_cancelled"
)
