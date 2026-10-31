package design

import (
	"strings"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Confirmation scopes. A confirmation batch confirms one of these selection
// sets (spec §4.5).
const (
	ConfirmScopeCase        = "case"
	ConfirmScopeScenario    = "scenario"
	ConfirmScopeRequirement = "requirement"
)

// Confirmation is one frozen case-version confirmation row.
type Confirmation struct {
	ID                     int64
	CaseVersionID          int64
	Scope                  string
	TargetScopeID          int64
	BatchID                string
	AffectedCaseVersionIDs []string
	Snapshot               map[string]any
	ConfirmedBy            int64
	ConfirmedAt            time.Time
}

// ConfirmedCase is one case version confirmed in a batch.
type ConfirmedCase struct {
	CaseID        int64
	CaseVersionID int64
}

// SkippedCase is one case version that was already confirmed and therefore
// skipped by an idempotent confirmation.
type SkippedCase struct {
	CaseID        int64
	CaseVersionID int64
	Status        string
}

// ConfirmSelection is the requested confirmation target.
type ConfirmSelection struct {
	Scope    string
	TargetID int64
	// BatchID is optional; the server generates one when empty.
	BatchID string
	// RequirementID is resolved by the application service for scenario/case
	// scopes and is used for row locking and clarified-state validation.
	RequirementID int64
}

// ConfirmResult is the outcome of one confirmation transaction.
type ConfirmResult struct {
	Scope       string
	TargetID    int64
	BatchID     string
	Confirmed   []ConfirmedCase
	Skipped     []SkippedCase
	ConfirmedAt time.Time
}

// ValidateConfirmationScope reports whether scope is a P2 confirmation scope.
func ValidateConfirmationScope(scope string) error {
	switch strings.TrimSpace(scope) {
	case ConfirmScopeCase, ConfirmScopeScenario, ConfirmScopeRequirement:
		return nil
	default:
		return platform.Invalid("scope", "must be one of case, scenario, requirement")
	}
}

// ValidateBatchID checks an optional client-supplied confirmation batch ID.
func ValidateBatchID(raw string) error {
	batchID := strings.TrimSpace(raw)
	if batchID == "" {
		return nil
	}
	if len(batchID) > 64 {
		return platform.Invalid("batch_id", "must be at most 64 characters")
	}
	return nil
}

// NewBatchID returns the server-generated confirmation batch ID
// ("batch_" plus 16 lowercase hex characters).
func NewBatchID() (string, error) {
	raw, err := randomHex(8)
	if err != nil {
		return "", err
	}
	return "batch_" + raw, nil
}

// GraphHash is the design graph content hash type alias (64 lowercase hex).
type GraphHash = string

// caseVersionIDString renders a case version ID in its decimal string form,
// used by event payloads and confirmation snapshots.
func caseVersionIDString(value int64) string {
	return id.Format(value)
}
