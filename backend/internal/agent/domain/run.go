package domain

import (
	"errors"
	"fmt"
	"time"
)

// RunStatus is the lifecycle state of one run. The values mirror the
// agent_run.status CHECK constraint.
type RunStatus string

const (
	RunRunning     RunStatus = "running"
	RunDone        RunStatus = "done"
	RunFailed      RunStatus = "failed"
	RunAborted     RunStatus = "aborted"
	RunInterrupted RunStatus = "interrupted"
)

// Valid reports whether s is one of the statuses allowed by the schema.
func (s RunStatus) Valid() bool {
	switch s {
	case RunRunning, RunDone, RunFailed, RunAborted, RunInterrupted:
		return true
	default:
		return false
	}
}

// TerminalReason records why a run reached its terminal status. The values
// mirror the agent_run.terminal_reason CHECK constraint.
type TerminalReason string

const (
	ReasonCompleted        TerminalReason = "completed"
	ReasonMaxTurns         TerminalReason = "max_turns"
	ReasonTimeout          TerminalReason = "timeout"
	ReasonModelError       TerminalReason = "model_error"
	ReasonContextOverflow  TerminalReason = "context_overflow"
	ReasonAbortedStreaming TerminalReason = "aborted_streaming"
	ReasonAbortedTools     TerminalReason = "aborted_tools"
	ReasonAborted          TerminalReason = "aborted"
	ReasonShutdown         TerminalReason = "shutdown"
	ReasonRunHardTimeout   TerminalReason = "run_hard_timeout"
	ReasonInterrupted      TerminalReason = "interrupted"
)

// Valid reports whether r is one of the terminal reasons allowed by the
// schema.
func (r TerminalReason) Valid() bool {
	switch r {
	case ReasonCompleted,
		ReasonMaxTurns,
		ReasonTimeout,
		ReasonModelError,
		ReasonContextOverflow,
		ReasonAbortedStreaming,
		ReasonAbortedTools,
		ReasonAborted,
		ReasonShutdown,
		ReasonRunHardTimeout,
		ReasonInterrupted:
		return true
	default:
		return false
	}
}

// Run is one agent execution. Usage columns accumulate across the run's
// completed turns so a crash keeps the usage that was already spent.
type Run struct {
	ID        int64
	SessionID int64

	Status         RunStatus
	TerminalReason TerminalReason
	AbortCode      string
	AbortMessage   string
	ErrorCode      string
	ErrorMessage   string

	Turns           int
	InputTokens     int64
	OutputTokens    int64
	ReasoningTokens int64

	LastToolName    string
	LastToolPending bool

	StartedAt time.Time
	EndedAt   *time.Time
}

// RunResult is the terminal result a run is finished with. Status is required;
// TerminalReason may be empty only while the run is still running.
type RunResult struct {
	Status          RunStatus
	TerminalReason  TerminalReason
	AbortCode       string
	AbortMessage    string
	ErrorCode       string
	ErrorMessage    string
	Turns           int
	Usage           Usage
	LastToolName    string
	LastToolPending bool
}

// Validate guards the repository's Finish write: the status column has no
// default, so an empty or unknown status must never reach PostgreSQL.
func (r RunResult) Validate() error {
	if r.Status == "" {
		return errors.New("agent: run result status is required")
	}
	if !r.Status.Valid() {
		return fmt.Errorf("agent: run result status %q is not valid", r.Status)
	}
	if r.TerminalReason != "" && !r.TerminalReason.Valid() {
		return fmt.Errorf("agent: run result terminal reason %q is not valid", r.TerminalReason)
	}
	return nil
}

// TurnIDs identifies the rows created atomically when a user turn starts a
// run: the run and the user message that triggered it.
type TurnIDs struct {
	RunID         int64
	UserMessageID int64
}

// StartResult is returned to the API caller when a message starts a run.
type StartResult struct {
	RunID         int64
	UserMessageID int64
}

// AbortResult reports whether an abort request found a live run to stop.
// Status is "stopping" when a run was cancelled and "idle" when none was
// active.
type AbortResult struct {
	Status string
}
