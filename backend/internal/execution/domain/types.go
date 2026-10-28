// Package domain holds the execution context's core types, entities and
// invariants. ID fields are int64 Snowflake values; decimal strings are only
// used at JSON boundaries by the layers that persist or serve them.
package domain

import "fmt"

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

// BatchKind mirrors the run_batch.kind CHECK constraint (spec §5.3). A P6
// historical row is always baseline.
type BatchKind string

// Run batch kinds.
const (
	BatchKindBaseline    BatchKind = "baseline"
	BatchKindIncremental BatchKind = "incremental"
	BatchKindSupplement  BatchKind = "supplement"
)

// BaseFallbackHistoryRewritten is the run_batch.base_fallback_reason value of an
// incremental batch whose parent head could not be used as the base (D49).
const BaseFallbackHistoryRewritten = "history_rewritten"

// ParseBatchKind normalizes an API kind: an omitted kind is baseline and an
// unknown kind is rejected. The database CHECK constraint mirrors the same set.
func ParseBatchKind(raw string) (BatchKind, error) {
	switch kind := BatchKind(raw); kind {
	case "":
		return BatchKindBaseline, nil
	case BatchKindBaseline, BatchKindIncremental, BatchKindSupplement:
		return kind, nil
	default:
		return "", fmt.Errorf("batch kind %q must be baseline, incremental or supplement", raw)
	}
}

// CaseSource mirrors the case_run.source CHECK constraint (spec §5.3): a case
// run executes either a frozen baseline case or a materialized AI supplement.
type CaseSource string

// Case run sources.
const (
	CaseSourceBaseline     CaseSource = "baseline"
	CaseSourceAISupplement CaseSource = "ai_supplement"
)

// ParseCaseSource normalizes a case source. P6 historical rows and snapshots
// without an origin are baseline (spec §4.2.6).
func ParseCaseSource(raw string) CaseSource {
	if CaseSource(raw) == CaseSourceAISupplement {
		return CaseSourceAISupplement
	}
	return CaseSourceBaseline
}

// FinishDecision is the outcome proposed by an execution agent when it finishes
// a case run. FailureClass is only meaningful for failed, blocked and
// inconclusive verdicts; the server may override it and records that override.
type FinishDecision struct {
	Verdict      Verdict
	FailureClass *FailureClass
	Reason       string
}
