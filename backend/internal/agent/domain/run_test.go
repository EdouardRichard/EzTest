package domain_test

import (
	"testing"

	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

func TestTerminalReasonSets(t *testing.T) {
	reasons := []agent.TerminalReason{
		agent.ReasonCompleted,
		agent.ReasonMaxTurns,
		agent.ReasonTimeout,
		agent.ReasonModelError,
		agent.ReasonContextOverflow,
		agent.ReasonAbortedStreaming,
		agent.ReasonAbortedTools,
		agent.ReasonAborted,
		agent.ReasonShutdown,
		agent.ReasonRunHardTimeout,
		agent.ReasonInterrupted,
	}
	// The set must match the agent_run.terminal_reason CHECK constraint exactly.
	if len(reasons) != 11 {
		t.Fatalf("terminal reason count=%d want 11", len(reasons))
	}
	seen := make(map[agent.TerminalReason]bool, len(reasons))
	for _, reason := range reasons {
		if reason == "" {
			t.Fatal("terminal reason constants must not be empty")
		}
		if !reason.Valid() {
			t.Fatalf("terminal reason %q is not valid", reason)
		}
		if seen[reason] {
			t.Fatalf("terminal reason %q appears twice in the set", reason)
		}
		seen[reason] = true
	}
	if agent.TerminalReason("bogus").Valid() {
		t.Fatal("Valid accepted an unknown terminal reason")
	}
	if agent.TerminalReason("").Valid() {
		t.Fatal("Valid accepted an empty terminal reason")
	}

	statuses := []agent.RunStatus{
		agent.RunRunning,
		agent.RunDone,
		agent.RunFailed,
		agent.RunAborted,
		agent.RunInterrupted,
	}
	// The set must match the agent_run.status CHECK constraint exactly.
	if len(statuses) != 5 {
		t.Fatalf("run status count=%d want 5", len(statuses))
	}
	seenStatus := make(map[agent.RunStatus]bool, len(statuses))
	for _, status := range statuses {
		if status == "" {
			t.Fatal("run status constants must not be empty")
		}
		if !status.Valid() {
			t.Fatalf("run status %q is not valid", status)
		}
		if seenStatus[status] {
			t.Fatalf("run status %q appears twice in the set", status)
		}
		seenStatus[status] = true
	}
	if agent.RunStatus("bogus").Valid() {
		t.Fatal("Valid accepted an unknown run status")
	}
}

func TestRunResultDefaults(t *testing.T) {
	// A zero value must be safe to inspect; Finish relies on Validate to reject
	// it instead of writing an empty status into the NOT NULL column.
	var result agent.RunResult
	if err := result.Validate(); err == nil {
		t.Fatal("zero RunResult must be rejected: Status is required")
	}

	result.Status = agent.RunDone
	result.TerminalReason = agent.ReasonCompleted
	if err := result.Validate(); err != nil {
		t.Fatalf("valid RunResult rejected: %v", err)
	}

	result.Status = agent.RunStatus("bogus")
	if err := result.Validate(); err == nil {
		t.Fatal("RunResult with unknown Status must be rejected")
	}
	result.Status = agent.RunDone

	result.TerminalReason = agent.TerminalReason("bogus")
	if err := result.Validate(); err == nil {
		t.Fatal("RunResult with unknown TerminalReason must be rejected")
	}
	result.TerminalReason = agent.ReasonCompleted

	// A running result has no terminal reason yet; it is still structurally
	// valid because Validate only requires a status and a known non-empty
	// reason when one is set.
	result.TerminalReason = ""
	result.Status = agent.RunRunning
	if err := result.Validate(); err != nil {
		t.Fatalf("running RunResult rejected: %v", err)
	}
}

func TestRunResultCarriesUsageAndToolState(t *testing.T) {
	result := agent.RunResult{
		Status:          agent.RunAborted,
		TerminalReason:  agent.ReasonAbortedTools,
		AbortCode:       "user_abort",
		AbortMessage:    "stopped by user",
		Turns:           3,
		Usage:           agent.Usage{PromptTokens: 10, CompletionTokens: 20, ReasoningTokens: 30, TotalTokens: 60},
		LastToolName:    "file.read",
		LastToolPending: true,
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("valid aborted RunResult rejected: %v", err)
	}
	if result.Usage.ReasoningTokens != 30 || result.LastToolName != "file.read" || !result.LastToolPending {
		t.Fatalf("RunResult lost fields: %+v", result)
	}
}

func TestTurnAndStartResultModels(t *testing.T) {
	ids := agent.TurnIDs{RunID: 7, UserMessageID: 9}
	if ids.RunID != 7 || ids.UserMessageID != 9 {
		t.Fatalf("TurnIDs=%+v", ids)
	}
	start := agent.StartResult{RunID: ids.RunID, UserMessageID: ids.UserMessageID}
	if start != (agent.StartResult{RunID: 7, UserMessageID: 9}) {
		t.Fatalf("StartResult=%+v", start)
	}
	aborted := agent.AbortResult{Status: "stopping"}
	if aborted.Status != "stopping" {
		t.Fatalf("AbortResult=%+v", aborted)
	}
}
