package execution

import "time"

// StepStatus is the step_result.status enum (P3-D-07).
type StepStatus string

const (
	StepPending StepStatus = "pending"
	StepRunning StepStatus = "running"
	StepPassed  StepStatus = "passed"
	StepFailed  StepStatus = "failed"
	StepBlocked StepStatus = "blocked"
	StepSkipped StepStatus = "skipped"
)

// Valid reports whether status is a persisted step status.
func (s StepStatus) Valid() bool {
	switch s {
	case StepPending, StepRunning, StepPassed, StepFailed, StepBlocked, StepSkipped:
		return true
	default:
		return false
	}
}

// Terminal reports whether a step no longer changes.
func (s StepStatus) Terminal() bool {
	switch s {
	case StepPassed, StepFailed, StepBlocked, StepSkipped:
		return true
	default:
		return false
	}
}

// StepResult is one persisted step of a case_run. Step results are upserted by
// (case_run_id, seq) and fenced by dispatch epoch/token.
type StepResult struct {
	ID                 int64
	CaseRunID          int64
	Seq                int
	TargetVersionSUTID *int64
	Status             StepStatus
	Observed           map[string]any
	Error              string
	StartedAt          *time.Time
	FinishedAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// AssertionVerdict is the assertion_result.verdict enum (P3-D-07).
type AssertionVerdict string

const (
	AssertionPassed       AssertionVerdict = "passed"
	AssertionFailed       AssertionVerdict = "failed"
	AssertionInconclusive AssertionVerdict = "inconclusive"
	AssertionSkipped      AssertionVerdict = "skipped"
)

// Valid reports whether verdict is a persisted assertion verdict.
func (v AssertionVerdict) Valid() bool {
	switch v {
	case AssertionPassed, AssertionFailed, AssertionInconclusive, AssertionSkipped:
		return true
	default:
		return false
	}
}

// Terminal reports whether the verdict is final. A skipped visual assertion is
// reserved for P5 and may be re-evaluated there, so it is not terminal.
func (v AssertionVerdict) Terminal() bool {
	switch v {
	case AssertionPassed, AssertionFailed, AssertionInconclusive:
		return true
	default:
		return false
	}
}

// AssertionResult is one persisted assertion outcome. expected is overwritten
// from the P2 case_assertion row; assertion_seq is derived by ascending P2
// assertion ID (P3-D-06).
type AssertionResult struct {
	ID                 int64
	CaseRunID          int64
	StepSeq            int
	AssertionSeq       int
	Kind               string
	Verdict            AssertionVerdict
	Reason             string
	Expected           string
	Actual             string
	TargetVersionSUTID *int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Assertion kinds are the frozen P2 values; P3 only consumes them.
const (
	AssertionKindUI     = "ui"
	AssertionKindAPI    = "api"
	AssertionKindDB     = "db"
	AssertionKindText   = "text"
	AssertionKindVisual = "visual"
)

// Result text limits (spec §3.2).
const (
	MaxStepErrorLength       = 2000
	MaxAssertionReasonLength = 2000
	MaxExpectedLength        = 4000
	MaxActualLength          = 4000
)
