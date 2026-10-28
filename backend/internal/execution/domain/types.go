// Package domain holds the execution context's core types, entities and
// invariants. ID fields are int64 Snowflake values; decimal strings are only
// used at JSON boundaries by the layers that persist or serve them.
package domain

// BatchStatus mirrors the run_batch.status CHECK constraint.
type BatchStatus string

// Run batch statuses.
const (
	BatchStatusPlanned   BatchStatus = "planned"
	BatchStatusRunning   BatchStatus = "running"
	BatchStatusDone      BatchStatus = "done"
	BatchStatusFailed    BatchStatus = "failed"
	BatchStatusCancelled BatchStatus = "cancelled"
)

// CaseRunStatus mirrors the case_run.status CHECK constraint.
type CaseRunStatus string

// Case run statuses.
const (
	CaseRunStatusPending      CaseRunStatus = "pending"
	CaseRunStatusRunning      CaseRunStatus = "running"
	CaseRunStatusPassed       CaseRunStatus = "passed"
	CaseRunStatusFailed       CaseRunStatus = "failed"
	CaseRunStatusBlocked      CaseRunStatus = "blocked"
	CaseRunStatusInconclusive CaseRunStatus = "inconclusive"
	CaseRunStatusSkipped      CaseRunStatus = "skipped"
	CaseRunStatusInterrupted  CaseRunStatus = "interrupted"
)

// FailureClass mirrors the case_run.failure_class CHECK constraint. Flaky is
// derived for display and is deliberately not a stored failure class.
type FailureClass string

// Case run failure classes (spec §5.8).
const (
	FailureClassApplication FailureClass = "application"
	FailureClassEnvironment FailureClass = "environment"
	FailureClassSession     FailureClass = "session"
	FailureClassModel       FailureClass = "model"
	FailureClassCase        FailureClass = "case"
)

// Verdict is the four-state terminal decision of a case run (spec §5.8).
type Verdict string

// Case run verdicts.
const (
	VerdictPassed       Verdict = "passed"
	VerdictFailed       Verdict = "failed"
	VerdictBlocked      Verdict = "blocked"
	VerdictInconclusive Verdict = "inconclusive"
)

// StepStatus mirrors the step_result.status CHECK constraint.
type StepStatus string

// Step result statuses.
const (
	StepStatusPending     StepStatus = "pending"
	StepStatusRunning     StepStatus = "running"
	StepStatusPassed      StepStatus = "passed"
	StepStatusFailed      StepStatus = "failed"
	StepStatusInterrupted StepStatus = "interrupted"
)

// PauseMode mirrors the pause_mode CHECK constraint.
type PauseMode string

// Pause modes.
const (
	PauseModeGraceful  PauseMode = "graceful"
	PauseModeImmediate PauseMode = "immediate"
)

// FinishDecision is the outcome proposed by an execution agent when it finishes
// a case run. FailureClass is only meaningful for failed, blocked and
// inconclusive verdicts; the server may override it and records that override.
type FinishDecision struct {
	Verdict      Verdict
	FailureClass *FailureClass
	Reason       string
}
