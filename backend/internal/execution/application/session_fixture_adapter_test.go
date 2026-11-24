package executionapp_test

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestFixtureSessionAdapterWiring drives the REAL CaseRunner session-adapter
// methods the fixture library consumes (method discipline: drive the real
// implementation, not a wholesale fake): workspace file download/upload go
// through the fenced executor session, the injection runs the official CLI
// state-load, a non-zero exit is a typed fixture_load_failed (never a silent
// success), a truncated download fails explicitly, and the reuse link lands on
// the case_run_sut_context row.
func TestFixtureSessionAdapterWiring(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	run := fixture.caseRuns.rows[3001]
	run.Status = execution.CaseRunRunning
	fixture.caseRuns.rows[3001] = run
	scope := executionScope()
	state := []byte(`{"cookies":[{"name":"sid","value":"a"}],"origins":[]}`)

	t.Run("read downloads the workspace state file", func(t *testing.T) {
		fixture.execution.fileResult = executionapp.FileOutcome{
			Path: "state.json", Base64: base64.StdEncoding.EncodeToString(state), Bytes: int64(len(state)),
		}
		got, err := fixture.runner.ReadSessionFile(fixture.runCtx, scope, 7, "admin", "state.json")
		if err != nil {
			t.Fatalf("ReadSessionFile: %v", err)
		}
		if string(got) != string(state) {
			t.Fatalf("read bytes = %q, want the workspace state file", got)
		}
		ops := fixture.execution.fileOps
		if len(ops) == 0 || ops[len(ops)-1].Op != "download" || ops[len(ops)-1].Path != "state.json" {
			t.Fatalf("file ops = %+v, want a download of state.json", ops)
		}
	})

	t.Run("truncated download fails explicitly", func(t *testing.T) {
		fixture.execution.fileResult = executionapp.FileOutcome{
			Path: "state.json", Base64: base64.StdEncoding.EncodeToString(state), Truncated: true,
		}
		if _, err := fixture.runner.ReadSessionFile(fixture.runCtx, scope, 7, "admin", "state.json"); err == nil {
			t.Fatal("a truncated state download must fail explicitly, never save a partial state")
		}
	})

	t.Run("write uploads the fixture bytes", func(t *testing.T) {
		fixture.execution.fileResult = executionapp.FileOutcome{Path: "fixture-state.json"}
		if err := fixture.runner.WriteSessionFile(fixture.runCtx, scope, 7, "admin", "fixture-state.json", state); err != nil {
			t.Fatalf("WriteSessionFile: %v", err)
		}
		ops := fixture.execution.fileOps
		last := ops[len(ops)-1]
		if last.Op != "upload" || last.Path != "fixture-state.json" {
			t.Fatalf("file op = %+v, want an upload of fixture-state.json", last)
		}
		decoded, err := base64.StdEncoding.DecodeString(last.Base64)
		if err != nil || string(decoded) != string(state) {
			t.Fatalf("upload payload = %q err=%v, want the fixture bytes", decoded, err)
		}
	})

	t.Run("load runs the CLI state-load", func(t *testing.T) {
		fixture.execution.execOutcome = executionapp.ExecOutcome{Exit: intptr(0), Outcome: "completed", Stdout: "Storage state restored"}
		if err := fixture.runner.LoadBrowserState(fixture.runCtx, scope, 7, "admin", "fixture-state.json"); err != nil {
			t.Fatalf("LoadBrowserState: %v", err)
		}
		commands := fixture.execution.commands
		last := commands[len(commands)-1]
		want := []string{"playwright-cli", "state-load", "fixture-state.json"}
		if len(last.Argv) != len(want) {
			t.Fatalf("argv = %v, want %v", last.Argv, want)
		}
		for i := range want {
			if last.Argv[i] != want[i] {
				t.Fatalf("argv = %v, want %v", last.Argv, want)
			}
		}
	})

	t.Run("failed CLI load is a typed fixture_load_failed", func(t *testing.T) {
		fixture.execution.execOutcome = executionapp.ExecOutcome{Exit: intptr(1), Outcome: "completed", Stderr: "storage state rejected"}
		err := fixture.runner.LoadBrowserState(fixture.runCtx, scope, 7, "admin", "fixture-state.json")
		if err == nil {
			t.Fatal("a non-zero state-load exit must surface fixture_load_failed, never a silent success")
		}
		requireFixtureAPIError(t, err, "fixture_load_failed")
	})

	t.Run("empty stderr detail prints the numeric exit code, not a pointer", func(t *testing.T) {
		fixture.execution.execOutcome = executionapp.ExecOutcome{Exit: intptr(2), Outcome: "completed", Stderr: ""}
		err := fixture.runner.LoadBrowserState(fixture.runCtx, scope, 7, "admin", "fixture-state.json")
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error type %T, want *platform.Error", err)
		}
		reason, _ := apiErr.Details["reason"].(string)
		if !strings.Contains(reason, "exited 2") {
			t.Fatalf("reason = %q, want the numeric exit code 'exited 2'", reason)
		}
		if strings.Contains(reason, "0x") || strings.Contains(reason, "<nil>") {
			t.Fatalf("reason = %q, must not print a pointer address or <nil>", reason)
		}
	})

	t.Run("nil exit detail is honest, not a pointer", func(t *testing.T) {
		fixture.execution.execOutcome = executionapp.ExecOutcome{Exit: nil, Outcome: "completed", Stderr: ""}
		err := fixture.runner.LoadBrowserState(fixture.runCtx, scope, 7, "admin", "fixture-state.json")
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error type %T, want *platform.Error", err)
		}
		reason, _ := apiErr.Details["reason"].(string)
		if strings.Contains(reason, "0x") || strings.Contains(reason, "<nil>") {
			t.Fatalf("reason = %q, must not print a pointer address or <nil>", reason)
		}
		if reason == "" {
			t.Fatal("reason must be a non-empty honest description when no exit code is reported")
		}
	})

	t.Run("link records the fixture reference on the SUT context", func(t *testing.T) {
		if err := fixture.runner.LinkSessionFixture(fixture.runCtx, scope, 7, "admin", 77); err != nil {
			t.Fatalf("LinkSessionFixture: %v", err)
		}
		row, ok := fixture.sutContext.row(7, "admin")
		if !ok {
			t.Fatal("no SUT context row for (7, admin)")
		}
		if row.SessionFixtureID == nil || *row.SessionFixtureID != 77 {
			t.Fatalf("session_fixture_id = %v, want 77 (the reuse link)", row.SessionFixtureID)
		}
	})
}

func requireFixtureAPIError(t *testing.T, err error, code string) {
	t.Helper()
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("error type %T (%v), want *platform.Error with code %q", err, err, code)
	}
	if apiErr.Code != code {
		t.Fatalf("error code = %q, want %q", apiErr.Code, code)
	}
}
