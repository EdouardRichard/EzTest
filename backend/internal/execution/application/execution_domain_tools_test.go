package executionapp_test

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	executorexec "github.com/EdouardRichard/EzTest/backend/internal/execution/infrastructure/executor"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// TestExecRunToolResultShape pins the model-facing exec.run wrapper of the T10
// Exec use case: the typed port input decodes from the tool DTO, the command
// runs in the dispatch-fenced session, and the result carries command_id plus
// the settled evidence (artifact) refs.
func TestExecRunToolResultShape(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	exit := 0
	fixture.execution.execOutcome = executionapp.ExecOutcome{
		CommandID: "cmd_9", Exit: &exit, Outcome: "exited", DurationMS: 12,
		Stdout: "hello", Stderr: "", StdoutTruncated: true, StdoutRef: "content:77",
	}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{
			{Kind: "command", CommandID: "cmd_9", Filename: "command-cmd_9.json", Base64: "e30="},
		},
		NextCursor: 1,
	}}
	result, err := fixture.runner.ExecRun(context.Background(), executionScope(), tools.ExecRunInput{
		SUTID: int64ptr(7), AccountRole: "admin",
		Argv: []string{"echo", "hello"}, Stdin: "in", TimeoutMS: 5000, Env: map[string]string{"K": "v"},
	})
	if err != nil {
		t.Fatalf("ExecRun: %v", err)
	}
	out, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("ExecRun result = %#v, want a map", result)
	}
	if out["command_id"] != "cmd_9" || out["outcome"] != "exited" || out["stdout"] != "hello" {
		t.Fatalf("ExecRun result = %#v", out)
	}
	if out["exit"] == nil || *out["exit"].(*int) != 0 {
		t.Fatalf("exit = %#v, want a pointer to 0", out["exit"])
	}
	if out["stdout_truncated"] != true || out["stdout_ref"] != "content:77" {
		t.Fatalf("truncation must be explicit with a full-content ref: %#v", out)
	}
	ids, ok := out["evidence_ids"].([]string)
	if !ok || len(ids) != 1 {
		t.Fatalf("evidence_ids = %#v, want the settled command artifact", out["evidence_ids"])
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if len(fixture.execution.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(fixture.execution.commands))
	}
	command := fixture.execution.commands[0]
	if len(command.Argv) != 2 || command.Argv[0] != "echo" || command.Stdin != "in" || command.TimeoutMS != 5000 || command.Env["K"] != "v" {
		t.Fatalf("ExecCommand = %+v, want the tool input verbatim", command)
	}
	for _, epoch := range fixture.execution.leaseEpochs {
		if epoch != 3 {
			t.Fatalf("a write carried epoch %d, want the dispatch epoch 3", epoch)
		}
	}
}

// TestExecRunTimedOutCommandIsData: a timed-out command still ran — its recorded
// stdout/exit is the observation the model needs, not a blind tool error. The
// evidence settles either way (T10), and the result marks timed_out explicitly.
func TestExecRunTimedOutCommandIsData(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.mu.Lock()
	fixture.execution.execOutcome = executionapp.ExecOutcome{CommandID: "cmd_slow", Outcome: "timeout", TimedOut: true, Stdout: "partial"}
	fixture.execution.execErr = platform.NewError(504, "executor_timeout", "command timed out")
	fixture.execution.mu.Unlock()
	result, err := fixture.runner.ExecRun(context.Background(), executionScope(), tools.ExecRunInput{
		Argv: []string{"sleep", "999"},
	})
	if err != nil {
		t.Fatalf("a recorded timeout must surface as data, not a tool error: %v", err)
	}
	out := result.(map[string]any)
	if out["timed_out"] != true || out["stdout"] != "partial" || out["command_id"] != "cmd_slow" {
		t.Fatalf("timeout result = %#v", out)
	}
}

// TestExecutionDomainToolsRequireDispatchScope: the scope fence
// (requireRunningScope) holds for every new domain use case — a call without the
// injected dispatch epoch/token is rejected before any session is created.
func TestExecutionDomainToolsRequireDispatchScope(t *testing.T) {
	calls := map[string]func(ctx context.Context, runner *executionapp.CaseRunner, scope tools.Scope) (any, error){
		"exec.run": func(ctx context.Context, runner *executionapp.CaseRunner, scope tools.Scope) (any, error) {
			return runner.ExecRun(ctx, scope, tools.ExecRunInput{Argv: []string{"true"}})
		},
		"shell.open": func(ctx context.Context, runner *executionapp.CaseRunner, scope tools.Scope) (any, error) {
			return runner.ShellOp(ctx, scope, tools.ShellOpInput{Op: "create"})
		},
		"file.list": func(ctx context.Context, runner *executionapp.CaseRunner, scope tools.Scope) (any, error) {
			return runner.FileOp(ctx, scope, tools.FileOpInput{Op: "list"})
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			fixture := newCaseRunnerFixture(t)
			prepareCaseRun(t, fixture)
			scope := executionScope()
			scope.DispatchEpoch = 0
			scope.DispatchToken = ""
			_, err := call(context.Background(), fixture.runner, scope)
			var apiErr *platform.Error
			if err == nil || !errors.As(err, &apiErr) || apiErr.Code != "invalid_argument" {
				t.Fatalf("%s without dispatch scope: err = %v, want invalid_argument", name, err)
			}
			fixture.execution.mu.Lock()
			defer fixture.execution.mu.Unlock()
			if fixture.execution.sessions != 0 {
				t.Fatalf("%s created %d sessions without a dispatch fence", name, fixture.execution.sessions)
			}
		})
	}
	// A stale epoch is lease_lost, never a silent write.
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	scope := executionScope()
	scope.DispatchEpoch = 2
	if _, err := fixture.runner.ShellOp(context.Background(), scope, tools.ShellOpInput{Op: "create"}); !executionapp.IsRunLeaseLost(err) {
		t.Fatalf("stale epoch err = %v, want lease_lost", err)
	}
}

// TestShellOpRunsThroughTheSessionAndDrainsEvidence: the shell/PTY family uses
// the same lazily created, dispatch-fenced session as exec, and every op drains
// the recording ledger (a PTY shell settles one command artifact on exit).
// The op vocabulary is pinned against the client protocol constants.
func TestShellOpRunsThroughTheSessionAndDrainsEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.shellResult = executionapp.ShellOutcome{ShellID: "sh_1", PID: 4242, Status: "running"}
	// The fake serves one ledger page per pull: the create drain sees nothing
	// new; the close drain sees the shell's settled command artifact.
	fixture.execution.artifactPages = []executionapp.ArtifactPage{
		{},
		{
			Artifacts: []executionapp.ExecutionArtifact{
				{Kind: "command", CommandID: "cmd_shell", Filename: "command-cmd_shell.json", Base64: "e30="},
			},
			NextCursor: 1,
		},
	}
	result, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{
		Op: "create", AccountRole: "admin", Cols: 120, Rows: 40, Env: map[string]string{"TERM": "xterm"},
	})
	if err != nil {
		t.Fatalf("ShellOp create: %v", err)
	}
	out := result.(map[string]any)
	if out["shell_id"] != "sh_1" || out["status"] != "running" {
		t.Fatalf("shell create result = %#v", out)
	}
	fixture.execution.mu.Lock()
	if len(fixture.execution.shellOps) != 1 {
		fixture.execution.mu.Unlock()
		t.Fatalf("shell ops = %d, want 1", len(fixture.execution.shellOps))
	}
	op := fixture.execution.shellOps[0]
	fixture.execution.mu.Unlock()
	if op.Op != executorexec.ShellCreate {
		t.Fatalf("op = %q, want the protocol constant %q", op.Op, executorexec.ShellCreate)
	}
	if op.Cols != 120 || op.Rows != 40 || op.Env["TERM"] != "xterm" {
		t.Fatalf("shell op = %+v", op)
	}
	// Close settles the shell's command artifact into evidence.
	fixture.execution.mu.Lock()
	fixture.execution.shellResult = executionapp.ShellOutcome{ShellID: "sh_1", Status: "exited", OK: true}
	fixture.execution.mu.Unlock()
	result, err = fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "close", ShellID: "sh_1", AccountRole: "admin"})
	if err != nil {
		t.Fatalf("ShellOp close: %v", err)
	}
	out = result.(map[string]any)
	ids, ok := out["evidence_ids"].([]string)
	if !ok || len(ids) != 1 {
		t.Fatalf("close result = %#v, want the settled shell command evidence", out)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.shellOps[1].Op != executorexec.ShellClose || fixture.execution.shellOps[1].ShellID != "sh_1" {
		t.Fatalf("close op = %+v", fixture.execution.shellOps[1])
	}
	if fixture.execution.sessions != 1 {
		t.Fatalf("sessions = %d, want the exec/shell shared session reused", fixture.execution.sessions)
	}
	for _, epoch := range fixture.execution.leaseEpochs {
		if epoch != 3 {
			t.Fatalf("a shell write carried epoch %d, want 3", epoch)
		}
	}
}

// TestShellOpRecoversFromVanishedSession: the shell path shares the exec
// recovery — a dead session id releases the reservation so the next call
// re-creates a session instead of failing identically for the rest of the run.
func TestShellOpRecoversFromVanishedSession(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.shellResult = executionapp.ShellOutcome{ShellID: "sh_1", Status: "running"}
	if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "create"}); err != nil {
		t.Fatalf("first ShellOp: %v", err)
	}
	fixture.execution.mu.Lock()
	firstSession := fixture.execution.createdSessions[0]
	fixture.execution.shellErrBySession = map[string]error{
		firstSession: platform.NewError(404, executorexec.CodeSessionNotFound, "session does not exist"),
	}
	fixture.execution.mu.Unlock()
	if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "read", ShellID: "sh_1"}); err == nil {
		t.Fatal("the op against the vanished session must surface its typed error")
	}
	if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "create"}); err != nil {
		t.Fatalf("ShellOp after the session vanished: %v (the run must recover)", err)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.sessions != 2 {
		t.Fatalf("sessions = %d, want the dead reservation replaced", fixture.execution.sessions)
	}
}

// TestFileOpUploadsAndDrainsFileEvidence: file.write uploads through the fenced
// session and the workspace watcher's file artifact settles as file evidence.
func TestFileOpUploadsAndDrainsFileEvidence(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	content := []byte("SELECT 1;")
	fixture.execution.mu.Lock()
	fixture.execution.fileResult = executionapp.FileOutcome{Path: "seed.sql", Bytes: int64(len(content)), SHA256: "abc", MIME: "text/plain"}
	fixture.execution.artifactPages = []executionapp.ArtifactPage{{
		Artifacts: []executionapp.ExecutionArtifact{{
			Kind: "file", Path: "seed.sql", Filename: "seed.sql",
			Base64: base64.StdEncoding.EncodeToString(content),
		}},
		NextCursor: 1,
	}}
	fixture.execution.mu.Unlock()
	result, err := fixture.runner.FileOp(context.Background(), executionScope(), tools.FileOpInput{
		Op: "upload", Path: "seed.sql", Content: "SELECT 1;", Encoding: "text", AccountRole: "admin",
	})
	if err != nil {
		t.Fatalf("FileOp upload: %v", err)
	}
	out := result.(map[string]any)
	if out["path"] != "seed.sql" {
		t.Fatalf("upload result = %#v", out)
	}
	ids, ok := out["evidence_ids"].([]string)
	if !ok || len(ids) != 1 {
		t.Fatalf("upload result = %#v, want the settled file evidence", out)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if len(fixture.execution.fileOps) != 1 {
		t.Fatalf("file ops = %d, want 1", len(fixture.execution.fileOps))
	}
	op := fixture.execution.fileOps[0]
	if op.Op != executorexec.FileUpload || op.Path != "seed.sql" {
		t.Fatalf("file op = %+v, want the protocol upload op", op)
	}
	if op.Base64 != base64.StdEncoding.EncodeToString(content) {
		t.Fatalf("upload base64 = %q, want the encoded text content", op.Base64)
	}
	rows, _ := fixture.evidence.ListByCaseRun(context.Background(), 3001, 10)
	if len(rows) != 1 || rows[0].Kind != execution.EvidenceFile {
		t.Fatalf("evidence rows = %+v, want one file evidence", rows)
	}
}

// TestFileOpDownloadDecodesTextExplicitly: a download returns the content with
// an explicit encoding — text when the bytes are valid UTF-8, base64 otherwise
// (never a lossy silent conversion).
func TestFileOpDownloadDecodesTextExplicitly(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.mu.Lock()
	fixture.execution.fileResult = executionapp.FileOutcome{
		Path: "out.txt", Bytes: 5, SHA256: "abc", MIME: "text/plain",
		Base64: base64.StdEncoding.EncodeToString([]byte("hello")),
	}
	fixture.execution.mu.Unlock()
	result, err := fixture.runner.FileOp(context.Background(), executionScope(), tools.FileOpInput{Op: "download", Path: "out.txt"})
	if err != nil {
		t.Fatalf("FileOp download: %v", err)
	}
	out := result.(map[string]any)
	if out["content"] != "hello" || out["encoding"] != "text" || out["sha256"] != "abc" {
		t.Fatalf("download result = %#v", out)
	}
	fixture.execution.mu.Lock()
	fixture.execution.fileResult = executionapp.FileOutcome{
		Path: "bin.dat", Bytes: 2, MIME: "application/octet-stream",
		Base64: base64.StdEncoding.EncodeToString([]byte{0xff, 0xfe}),
	}
	fixture.execution.mu.Unlock()
	result, err = fixture.runner.FileOp(context.Background(), executionScope(), tools.FileOpInput{Op: "download", Path: "bin.dat"})
	if err != nil {
		t.Fatalf("binary download: %v", err)
	}
	out = result.(map[string]any)
	if out["encoding"] != "base64" || out["content"] == "hello" {
		t.Fatalf("binary download must stay base64: %#v", out)
	}
}

// TestFileOpListReturnsEntries: the workspace listing passes the entries and
// the explicit truncation flag through.
func TestFileOpListReturnsEntries(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	fixture.execution.mu.Lock()
	fixture.execution.fileResult = executionapp.FileOutcome{
		Files: []executionapp.FileEntry{{Path: "a.txt", Bytes: 3, MIME: "text/plain"}, {Path: "sub", Dir: true}},
	}
	fixture.execution.mu.Unlock()
	result, err := fixture.runner.FileOp(context.Background(), executionScope(), tools.FileOpInput{Op: "list", Path: ""})
	if err != nil {
		t.Fatalf("FileOp list: %v", err)
	}
	out := result.(map[string]any)
	files, ok := out["files"].([]map[string]any)
	if !ok || len(files) != 2 || files[0]["path"] != "a.txt" || files[1]["dir"] != true {
		t.Fatalf("list result = %#v", out)
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.fileOps[0].Op != executorexec.FileList {
		t.Fatalf("list op = %q, want %q", fixture.execution.fileOps[0].Op, executorexec.FileList)
	}
}

// TestExecutionDomainRejectsUnknownOps: the server validates the op vocabulary
// even though the tool schema already constrains it (server-side truth).
func TestExecutionDomainRejectsUnknownOps(t *testing.T) {
	fixture := newCaseRunnerFixture(t)
	prepareCaseRun(t, fixture)
	if _, err := fixture.runner.ShellOp(context.Background(), executionScope(), tools.ShellOpInput{Op: "teleport"}); err == nil {
		t.Fatal("an unknown shell op must be rejected")
	}
	if _, err := fixture.runner.FileOp(context.Background(), executionScope(), tools.FileOpInput{Op: "teleport", Path: "a"}); err == nil {
		t.Fatal("an unknown file op must be rejected")
	}
	fixture.execution.mu.Lock()
	defer fixture.execution.mu.Unlock()
	if fixture.execution.sessions != 0 {
		t.Fatalf("rejected ops created %d sessions", fixture.execution.sessions)
	}
}
