package executionapp_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorexec "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestSessionGoneCodesPinClientConstants is the cross-package lockstep pin
// (T10 routing → T11): application/tools.go sessionGone duplicates the client's
// typed error codes as string literals because the application layer must not
// import infrastructure. This test drives the REAL recovery behavior with errors
// built from the CLIENT CONSTANTS — if either side's vocabulary drifts, the
// reservation is no longer released (or is released when it must not be) and
// this test fails. The expectations come from the protocol contract, not from
// observed output.
func TestSessionGoneCodesPinClientConstants(t *testing.T) {
	// Every code that means "this session is dead for THIS owner": the
	// reservation must be released so the next call re-creates a session.
	goneCodes := []struct {
		name   string
		code   string
		status int
	}{
		{"session_not_found", executorexec.CodeSessionNotFound, 404},
		{"lease_expired", executorexec.CodeLeaseExpired, 409},
		{"session_reclaiming", executorexec.CodeSessionReclaiming, 409},
	}
	for _, tc := range goneCodes {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newCaseRunnerFixture(t)
			prepareCaseRun(t, fixture)
			first, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
				Command: executionapp.ExecCommand{Argv: []string{"true"}},
			})
			if err != nil {
				t.Fatalf("first Exec: %v", err)
			}
			fixture.execution.mu.Lock()
			fixture.execution.execErrBySession = map[string]error{
				first.SessionID: platform.NewError(tc.status, tc.code, "session is gone"),
			}
			fixture.execution.mu.Unlock()
			if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
				Command: executionapp.ExecCommand{Argv: []string{"true"}},
			}); err == nil {
				t.Fatal("the command against the dead session must surface its typed error")
			}
			third, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
				Command: executionapp.ExecCommand{Argv: []string{"true"}},
			})
			if err != nil {
				t.Fatalf("Exec after %s: %v — sessionGone no longer recognizes the client code %q (literal drift)", tc.name, err, tc.code)
			}
			if third.SessionID == first.SessionID {
				t.Fatalf("session %q reused after %s — the dead reservation was not released", first.SessionID, tc.name)
			}
		})
	}

	// The reverse pin: lease_lost means a NEWER owner holds the session. The
	// reservation must NOT be released, or the stale dispatch would steal work.
	t.Run("lease_lost_keeps_reservation", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		first, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
			Command: executionapp.ExecCommand{Argv: []string{"true"}},
		})
		if err != nil {
			t.Fatalf("first Exec: %v", err)
		}
		fixture.execution.mu.Lock()
		fixture.execution.execErrBySession = map[string]error{
			first.SessionID: platform.NewError(409, executorexec.CodeLeaseLost, "stale epoch"),
		}
		fixture.execution.mu.Unlock()
		if _, err := fixture.runner.Exec(context.Background(), executionScope(), executionapp.ExecCommandInput{
			Command: executionapp.ExecCommand{Argv: []string{"true"}},
		}); err == nil {
			t.Fatal("a fenced write must surface lease_lost")
		}
		row, ok := fixture.sutContext.row(7, "")
		if !ok || row.SessionID != first.SessionID || row.Status != execution.SUTContextActive {
			t.Fatalf("reservation = %+v (found %v), want the newer owner's session untouched", row, ok)
		}
	})

	// The shell/PTY path shares the same classifier: pin one gone code through
	// ShellOp so a future refactor cannot quietly drop the recovery there.
	t.Run("shell_path_shares_the_classifier", func(t *testing.T) {
		fixture := newCaseRunnerFixture(t)
		prepareCaseRun(t, fixture)
		fixture.execution.shellResult = executionapp.ShellOutcome{ShellID: "sh_1", Status: "running"}
		if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "create"}); err != nil {
			t.Fatalf("first ShellOp: %v", err)
		}
		fixture.execution.mu.Lock()
		firstSession := fixture.execution.createdSessions[0]
		fixture.execution.shellErrBySession = map[string]error{
			firstSession: platform.NewError(409, executorexec.CodeLeaseExpired, "lease expired"),
		}
		fixture.execution.mu.Unlock()
		if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "read", ShellID: "sh_1"}); err == nil {
			t.Fatal("the op against the expired session must surface its typed error")
		}
		row, ok := fixture.sutContext.row(7, "")
		if !ok || row.Status != execution.SUTContextFailed || row.SessionID != "" {
			t.Fatalf("reservation = %+v (found %v), want the expired session released on the shell path", row, ok)
		}
	})
}
