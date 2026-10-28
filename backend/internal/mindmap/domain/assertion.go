package domain

import "time"

// Assertion kinds, mirrored from the case_assertion.kind CHECK constraint.
const (
	AssertionKindUI   = "ui"
	AssertionKindAPI  = "api"
	AssertionKindDB   = "db"
	AssertionKindText = "text"
)

// CaseAssertion is one expected outcome of a test case. StepID is optional; when
// set it must reference a step of the same case node (spec §6.1). IDs are
// application-generated Snowflakes.
type CaseAssertion struct {
	ID         int64
	NodeID     int64
	StepID     *int64
	Kind       string
	Target     string
	Expression string
	Expected   string
	Source     string
	CreatedAt  time.Time
	UpdatedAt  time.Time
}
