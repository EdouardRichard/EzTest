package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// requireExecutionToolError asserts the typed tool error and returns it so the
// caller can pin the self-describing field name.
func requireExecutionToolError(t *testing.T, err error, code string) *Error {
	t.Helper()
	var toolErr *Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code = %q, want %q", toolErr.Code, code)
	}
	return toolErr
}

// fakeGeneralExecutionPort records every general-execution domain delegation.
// It embeds the frozen P3 port so the same fake satisfies both surfaces, exactly
// like the production CaseRunner does.
type fakeGeneralExecutionPort struct {
	fakeExecutionPort
	execIn      ExecRunInput
	shellIn     ShellOpInput
	fileIn      FileOpInput
	execCalls   int
	shellCalls  int
	fileCalls   int
	execResult  any
	shellResult any
	fileResult  any
	execErr     error
	shellErr    error
	fileErr     error
}

func (f *fakeGeneralExecutionPort) ExecRun(_ context.Context, scope Scope, in ExecRunInput) (any, error) {
	f.scope = scope
	f.execIn = in
	f.execCalls++
	if f.execResult != nil {
		return f.execResult, f.execErr
	}
	return map[string]any{
		"command_id":   "cmd_1",
		"exit":         0,
		"evidence_ids": []string{"5001"},
	}, f.execErr
}

func (f *fakeGeneralExecutionPort) ShellOp(_ context.Context, scope Scope, in ShellOpInput) (any, error) {
	f.scope = scope
	f.shellIn = in
	f.shellCalls++
	if f.shellResult != nil {
		return f.shellResult, f.shellErr
	}
	return map[string]any{"shell_id": "sh_1", "status": "running"}, f.shellErr
}

func (f *fakeGeneralExecutionPort) FileOp(_ context.Context, scope Scope, in FileOpInput) (any, error) {
	f.scope = scope
	f.fileIn = in
	f.fileCalls++
	if f.fileResult != nil {
		return f.fileResult, f.fileErr
	}
	return map[string]any{"path": "out.txt", "bytes": 3}, f.fileErr
}

func generalRegistry(port ExecutionPort) *Registry {
	return NewToolGroupRegistry(Deps{Execution: port})
}

// TestExecutionToolsRegisteredByCapabilityDomain pins the capability-domain
// registration: all 13 general-execution tools exist when the execution port
// implements the general surface, and the registry derives their domain tags
// ("exec"/"shell"/"file"/"pty") from the single authority — the tool names.
func TestExecutionToolsRegisteredByCapabilityDomain(t *testing.T) {
	port := &fakeGeneralExecutionPort{}
	registry := generalRegistry(port)
	want := []string{
		"exec.run",
		"shell.open", "shell.write", "shell.read", "shell.close",
		"file.write", "file.read", "file.list",
		"pty.open", "pty.write", "pty.read", "pty.close", "pty.wait",
	}
	if got := ExecutionToolNames(); len(got) != len(want) {
		t.Fatalf("ExecutionToolNames() = %v, want %d tools", got, len(want))
	}
	for _, name := range want {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("tool %q is not registered", name)
		}
	}
	for domain, members := range map[string][]string{
		"exec":  {"exec.run"},
		"shell": {"shell.close", "shell.open", "shell.read", "shell.write"},
		"file":  {"file.list", "file.read", "file.write"},
		"pty":   {"pty.close", "pty.open", "pty.read", "pty.wait", "pty.write"},
	} {
		got := registry.NamesInDomain(domain)
		if len(got) != len(members) {
			t.Fatalf("NamesInDomain(%q) = %v, want %v", domain, got, members)
		}
		for i := range members {
			if got[i] != members[i] {
				t.Fatalf("NamesInDomain(%q) = %v, want %v", domain, got, members)
			}
		}
	}
	domains := registry.Domains()
	for _, want := range []string{"exec", "shell", "file", "pty"} {
		found := false
		for _, got := range domains {
			if got == want {
				found = true
			}
		}
		if !found {
			t.Fatalf("Domains() = %v, missing %q", domains, want)
		}
	}
}

// TestExecutionToolsAbsentWithoutGeneralPort: the additive registration pattern
// (DatabaseLister precedent) — an execution port without the general surface
// leaves the new tools unregistered instead of failing at call time.
func TestExecutionToolsAbsentWithoutGeneralPort(t *testing.T) {
	registry := generalRegistry(&fakeExecutionPort{})
	if _, ok := registry.Get(ToolExecRun); ok {
		t.Fatal("exec.run must stay unregistered without the general execution surface")
	}
	if _, ok := registry.Get(ToolPTYOpen); ok {
		t.Fatal("pty.open must stay unregistered without the general execution surface")
	}
}

// TestExecutionToolsExecRunDelegates pins the exec.run wire contract: the model
// arguments decode into the typed port input (sut_id is the NEW wire name) and
// the injected scope reaches the port unchanged.
func TestExecutionToolsExecRunDelegates(t *testing.T) {
	port := &fakeGeneralExecutionPort{}
	registry := generalRegistry(port)
	result, err := executeTool(t, registry, ToolExecRun, `{
		"sut_id": "7", "account_role": "admin",
		"argv": ["node", "-e", "console.log(1)"],
		"shell": false, "stdin": "input", "timeout_ms": 5000,
		"background": true, "env": {"FOO": "bar"}
	}`)
	if err != nil {
		t.Fatalf("exec.run: %v", err)
	}
	if port.execCalls != 1 {
		t.Fatalf("ExecRun calls = %d, want 1", port.execCalls)
	}
	in := port.execIn
	if in.SUTID == nil || *in.SUTID != 7 {
		t.Fatalf("SUTID = %v, want 7 (sut_id wire name)", in.SUTID)
	}
	if in.AccountRole != "admin" || in.Shell || in.Stdin != "input" || in.TimeoutMS != 5000 || !in.Background {
		t.Fatalf("ExecRunInput = %+v", in)
	}
	if len(in.Argv) != 3 || in.Argv[0] != "node" || in.Env["FOO"] != "bar" {
		t.Fatalf("argv/env = %+v", in)
	}
	if port.scope.CaseRunID != 3001 || port.scope.DispatchEpoch != 3 || port.scope.DispatchToken != "dispatch-token" {
		t.Fatalf("scope was not passed through: %+v", port.scope)
	}
	out, ok := result.(map[string]any)
	if !ok || out["command_id"] != "cmd_1" {
		t.Fatalf("result = %#v, want the port result with command_id", result)
	}
	if ids, ok := out["evidence_ids"].([]string); !ok || len(ids) != 1 {
		t.Fatalf("result = %#v, want evidence (artifact) refs", result)
	}
}

// TestExecutionToolsUseSutIDWireName: every new tool schema publishes sut_id,
// never the stale version-SUT name. The remaining old tools were renamed to
// sut_id in lockstep by v0.2.4-C T14 (R-C1); the new tools must not be born with
// the stale name either.
func TestExecutionToolsUseSutIDWireName(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	for _, name := range ExecutionToolNames() {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("%s missing", name)
		}
		schema := string(tool.Parameters())
		if strings.Contains(schema, "version_sut_id") {
			t.Fatalf("%s schema still carries version_sut_id: %s", name, schema)
		}
		if !strings.Contains(schema, `"sut_id"`) {
			t.Fatalf("%s schema has no sut_id: %s", name, schema)
		}
	}
	// The error field name matches the schema field (six-surface discipline).
	_, err := executeTool(t, registry, ToolExecRun, `{"sut_id": "abc", "argv": ["true"]}`)
	toolErr := requireExecutionToolError(t, err, CodeInvalidArgument)
	if field, _ := toolErr.Details["field"].(string); field != "sut_id" {
		t.Fatalf("error field = %q, want sut_id", field)
	}
}

// TestExecutionToolsShellAndPTYFamilies: shell.* and pty.* are two tool families
// over the executor's ONE PTY shell channel; every tool maps to the protocol op
// and carries the shell id / data / cursor arguments verbatim.
func TestExecutionToolsShellAndPTYFamilies(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		op   string
	}{
		{ToolShellOpen, `{"account_role": "admin"}`, "create"},
		{ToolPTYOpen, `{"argv": ["psql", "-h", "db"], "cols": 120, "rows": 40}`, "create"},
		{ToolShellWrite, `{"shell_id": "sh_1", "data": "ls\n"}`, "write"},
		{ToolPTYWrite, `{"shell_id": "sh_1", "data": "select 1;\n"}`, "write"},
		{ToolShellRead, `{"shell_id": "sh_1", "since": 12}`, "read"},
		{ToolPTYRead, `{"shell_id": "sh_1"}`, "read"},
		{ToolShellClose, `{"shell_id": "sh_1"}`, "close"},
		{ToolPTYClose, `{"shell_id": "sh_1"}`, "close"},
		{ToolPTYWait, `{"shell_id": "sh_1", "timeout_ms": 2000}`, "wait"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			port := &fakeGeneralExecutionPort{}
			registry := generalRegistry(port)
			if _, err := executeTool(t, registry, tc.name, tc.raw); err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			if port.shellCalls != 1 || port.shellIn.Op != tc.op {
				t.Fatalf("%s delegated %+v, want op %q", tc.name, port.shellIn, tc.op)
			}
		})
	}
	port := &fakeGeneralExecutionPort{}
	registry := generalRegistry(port)
	if _, err := executeTool(t, registry, ToolPTYOpen, `{"argv": ["psql"], "cols": 120, "rows": 40, "env": {"PGPASSWORD": "pw"}}`); err != nil {
		t.Fatalf("pty.open: %v", err)
	}
	if len(port.shellIn.Argv) != 1 || port.shellIn.Cols != 120 || port.shellIn.Rows != 40 || port.shellIn.Env["PGPASSWORD"] != "pw" {
		t.Fatalf("pty.open input = %+v", port.shellIn)
	}
	if _, err := executeTool(t, registry, ToolShellRead, `{"shell_id": "sh_1", "since": 12}`); err != nil {
		t.Fatalf("shell.read: %v", err)
	}
	if port.shellIn.ShellID != "sh_1" || port.shellIn.Since != 12 {
		t.Fatalf("shell.read input = %+v", port.shellIn)
	}
	if _, err := executeTool(t, registry, ToolPTYWait, `{"shell_id": "sh_1", "timeout_ms": 2000}`); err != nil {
		t.Fatalf("pty.wait: %v", err)
	}
	if port.shellIn.TimeoutMS != 2000 {
		t.Fatalf("pty.wait input = %+v", port.shellIn)
	}
}

// TestExecutionToolsFileOps pins the file domain: write uploads text or base64
// content, read downloads, list enumerates the workspace.
func TestExecutionToolsFileOps(t *testing.T) {
	port := &fakeGeneralExecutionPort{}
	registry := generalRegistry(port)
	if _, err := executeTool(t, registry, ToolFileWrite, `{"path": "seed.sql", "content": "SELECT 1;"}`); err != nil {
		t.Fatalf("file.write: %v", err)
	}
	if port.fileIn.Op != "upload" || port.fileIn.Path != "seed.sql" || port.fileIn.Content != "SELECT 1;" || port.fileIn.Encoding != "text" {
		t.Fatalf("file.write input = %+v", port.fileIn)
	}
	if _, err := executeTool(t, registry, ToolFileWrite, `{"path": "bin.dat", "content": "AAEC", "encoding": "base64"}`); err != nil {
		t.Fatalf("file.write base64: %v", err)
	}
	if port.fileIn.Encoding != "base64" || port.fileIn.Content != "AAEC" {
		t.Fatalf("file.write base64 input = %+v", port.fileIn)
	}
	if _, err := executeTool(t, registry, ToolFileRead, `{"path": "out.txt"}`); err != nil {
		t.Fatalf("file.read: %v", err)
	}
	if port.fileIn.Op != "download" || port.fileIn.Path != "out.txt" {
		t.Fatalf("file.read input = %+v", port.fileIn)
	}
	if _, err := executeTool(t, registry, ToolFileList, `{"path": "sub"}`); err != nil {
		t.Fatalf("file.list: %v", err)
	}
	if port.fileIn.Op != "list" || port.fileIn.Path != "sub" {
		t.Fatalf("file.list input = %+v", port.fileIn)
	}
}

// TestExecutionToolsDescribeAutomaticRecording: the prompt-facing descriptions
// must state the recording semantics — every command / file change becomes
// immutable evidence automatically (GD-01: governance by recording, not gates).
func TestExecutionToolsDescribeAutomaticRecording(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	for _, name := range ExecutionToolNames() {
		tool, _ := registry.Get(name)
		description := strings.ToLower(tool.Description())
		if !strings.Contains(description, "evidence") || !strings.Contains(description, "record") {
			t.Fatalf("%s description does not state the automatic recording semantics: %q", name, tool.Description())
		}
	}
}

// TestExecutionToolsValidateArguments: field-level invalid_argument errors name
// the exact schema field so the model can correct and retry.
func TestExecutionToolsValidateArguments(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	cases := []struct {
		name  string
		raw   string
		field string
	}{
		{ToolExecRun, `{}`, "argv"},
		{ToolExecRun, `{"argv": []}`, "argv"},
		{ToolExecRun, `{"argv": [""]}`, "argv"},
		{ToolExecRun, `{"argv": ["true"], "timeout_ms": 0}`, "timeout_ms"},
		{ToolShellWrite, `{"data": "ls"}`, "shell_id"},
		{ToolShellWrite, `{"shell_id": "sh_1"}`, "data"},
		{ToolPTYRead, `{}`, "shell_id"},
		{ToolPTYWait, `{"shell_id": "sh_1", "timeout_ms": -1}`, "timeout_ms"},
		{ToolFileWrite, `{"content": "x"}`, "path"},
		{ToolFileWrite, `{"path": "a.txt"}`, "content"},
		{ToolFileWrite, `{"path": "a.txt", "content": "x", "encoding": "utf16"}`, "encoding"},
		{ToolFileRead, `{}`, "path"},
		{ToolExecRun, `{"argv": ["true"], "version_sut_id": "7"}`, "parameters"},
	}
	for _, tc := range cases {
		t.Run(tc.name+tc.raw, func(t *testing.T) {
			_, err := executeTool(t, registry, tc.name, tc.raw)
			toolErr := requireExecutionToolError(t, err, CodeInvalidArgument)
			if field, _ := toolErr.Details["field"].(string); field != tc.field {
				t.Fatalf("error field = %q (err %v), want %q", field, err, tc.field)
			}
		})
	}
}

// TestExecutionToolsAdminCatalog: the admin profile editor can select the new
// domain tools (metadata-only; Execute fails closed without a wired port).
func TestExecutionToolsAdminCatalog(t *testing.T) {
	catalog := AdminCatalogRegistry()
	for _, name := range ExecutionToolNames() {
		tool, ok := catalog.Get(name)
		if !ok {
			t.Fatalf("AdminCatalogRegistry is missing %s", name)
		}
		if len(tool.Parameters()) == 0 || tool.Description() == "" {
			t.Fatalf("%s catalog metadata is empty", name)
		}
		if _, err := tool.Execute(context.Background(), executionScope(), json.RawMessage(`{"argv":["true"]}`)); err == nil {
			t.Fatalf("%s catalog tool executed without a wired port", name)
		}
	}
}

// TestToolDomainIsDerivedFromName: the domain tag is a pure function of the tool
// name (single source of truth — no second hand-maintained domain table).
func TestToolDomainIsDerivedFromName(t *testing.T) {
	for name, want := range map[string]string{
		"exec.run":          "exec",
		"shell.open":        "shell",
		"file.read":         "file",
		"pty.wait":          "pty",
		"browser.goto":      "browser",
		"http.request":      "http",
		"execution.wait":    "execution",
		"context.task_pack": "context",
		"":                  "",
		"nodot":             "",
	} {
		if got := ToolDomain(name); got != want {
			t.Fatalf("ToolDomain(%q) = %q, want %q", name, got, want)
		}
	}
}
