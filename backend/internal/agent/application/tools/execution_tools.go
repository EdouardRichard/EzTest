package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
)

// General-execution capability-domain tool names (design v1.3.1 §4.2 P11). The
// capability domain of every tool is derived from its name (ToolDomain) — the
// registry is the single authority; there is no second domain table.
//
// shell.* and pty.* are two tool families over the executor's ONE PTY shell
// channel: shell.open starts the platform default shell, pty.open names an
// explicit interactive program (ssh/psql/mysql/REPL) and terminal size. Both
// are convenience wrappers, never a capability ceiling: exec.run is the general
// escape valve (constitution art.2.6).
const (
	ToolExecRun    = "exec.run"
	ToolShellOpen  = "shell.open"
	ToolShellWrite = "shell.write"
	ToolShellRead  = "shell.read"
	ToolShellClose = "shell.close"
	ToolFileWrite  = "file.write"
	ToolFileRead   = "file.read"
	ToolFileList   = "file.list"
	ToolPTYOpen    = "pty.open"
	ToolPTYWrite   = "pty.write"
	ToolPTYRead    = "pty.read"
	ToolPTYClose   = "pty.close"
	ToolPTYWait    = "pty.wait"
)

// executionToolNames is the frozen general-execution registration order.
var executionToolNames = []string{
	ToolExecRun,
	ToolShellOpen, ToolShellWrite, ToolShellRead, ToolShellClose,
	ToolFileWrite, ToolFileRead, ToolFileList,
	ToolPTYOpen, ToolPTYWrite, ToolPTYRead, ToolPTYClose, ToolPTYWait,
}

// ExecutionToolNames returns a copy of the general-execution domain tool names.
func ExecutionToolNames() []string {
	return append([]string(nil), executionToolNames...)
}

// GeneralExecutionPort is the additive general-execution surface behind the
// exec/shell/file/pty capability domains. It is a separate interface (not an
// ExecutionPort method set change) so existing execution ports and test fakes
// keep compiling; a port that does not implement it simply does not expose the
// new tools (the DatabaseLister precedent). The production CaseRunner implements
// it: every method validates the case_run dispatch fence server-side
// (requireRunningScope) and settles the recording as immutable evidence.
type GeneralExecutionPort interface {
	ExecRun(ctx context.Context, scope Scope, in ExecRunInput) (any, error)
	ShellOp(ctx context.Context, scope Scope, in ShellOpInput) (any, error)
	FileOp(ctx context.Context, scope Scope, in FileOpInput) (any, error)
}

// ExecRunInput is one exec.run call: which (SUT, account_role) session runs the
// command, and the command itself. Argv is the default form; Shell=true runs the
// single argv entry through the platform shell.
type ExecRunInput struct {
	SUTID       *int64
	AccountRole string
	Argv        []string
	Shell       bool
	Stdin       string
	TimeoutMS   int
	Background  bool
	Env         map[string]string
}

// ShellOpInput is one shell.*/pty.* call. Op is the protocol shell verb
// (create/write/read/close/wait); the unused fields of another op stay zero.
type ShellOpInput struct {
	Op          string
	SUTID       *int64
	AccountRole string
	ShellID     string
	Argv        []string
	Cols        int
	Rows        int
	Env         map[string]string
	Data        string
	Since       int64
	TimeoutMS   int
}

// FileOpInput is one file.* call. Op is upload/download/list; Content carries
// the upload payload as text (default) or base64 per Encoding.
type FileOpInput struct {
	Op          string
	SUTID       *int64
	AccountRole string
	Path        string
	Content     string
	Encoding    string
}

// --- shared argument plumbing ----------------------------------------------

// executionDomainArgs are the session-selector arguments every general-execution
// tool shares. sut_id is the wire name across the whole execution tool surface
// (v0.2.4-C T14/R-C1 renamed the remaining old tools from the stale version-SUT
// name in lockstep); the value is the SUT id from the task context SUT bindings.
type executionDomainArgs struct {
	SUTID       string `json:"sut_id,omitempty"`
	AccountRole string `json:"account_role,omitempty"`
}

func (a executionDomainArgs) sessionSelector() (*int64, string, *Error) {
	var sutID *int64
	if strings.TrimSpace(a.SUTID) != "" {
		parsed, err := parseIDString("sut_id", a.SUTID)
		if err != nil {
			return nil, "", err
		}
		sutID = &parsed
	}
	if err := validateText("account_role", a.AccountRole, 0, 100); err != nil {
		return nil, "", err
	}
	return sutID, a.AccountRole, nil
}

func executionDomainProperties(extra map[string]any) map[string]any {
	properties := map[string]any{
		"sut_id": map[string]any{
			"type":        "string",
			"description": "decimal SUT ID selecting the execution session; defaults to the case default SUT (read sut.list, never guess)",
		},
		"account_role": map[string]any{
			"type":        "string",
			"description": "account role of the session (each role owns its own session/workspace)",
		},
	}
	for name, schema := range extra {
		properties[name] = schema
	}
	return properties
}

var (
	execEnvSchema = map[string]any{
		"type":                 "object",
		"description":          "extra environment variables (recorded in full with the command evidence)",
		"additionalProperties": map[string]any{"type": "string"},
	}
	execTimeoutSchema = map[string]any{
		"type": "integer", "minimum": 1, "maximum": 900000,
		"description": "command deadline in ms (default 900000 = 15min, executor-side)",
	}
	shellIDSchema   = map[string]any{"type": "string", "description": "shell id returned by shell.open/pty.open"}
	shellColsSchema = map[string]any{"type": "integer", "minimum": 1, "maximum": 500}
	shellRowsSchema = map[string]any{"type": "integer", "minimum": 1, "maximum": 200}
)

func validateExecEnv(env map[string]string) *Error {
	if len(env) > 100 {
		return Invalid("env", "at most 100 environment variables are allowed")
	}
	for key, value := range env {
		if err := validateText("env", key, 1, 200); err != nil {
			return err
		}
		if err := validateText("env", value, 0, 8192); err != nil {
			return err
		}
	}
	return nil
}

func validateExecArgv(argv []string) *Error {
	if len(argv) == 0 {
		return Invalid("argv", "argv must contain at least the program name")
	}
	if len(argv) > 64 {
		return Invalid("argv", "at most 64 argv entries are allowed")
	}
	if strings.TrimSpace(argv[0]) == "" {
		return Invalid("argv", "argv[0] (the program) must not be empty")
	}
	for _, entry := range argv {
		if err := validateText("argv", entry, 0, 4096); err != nil {
			return err
		}
	}
	return nil
}

func validateExecTimeout(timeout *int) *Error {
	if timeout != nil && (*timeout < 1 || *timeout > 900000) {
		return Invalid("timeout_ms", "must be between 1 and 900000")
	}
	return nil
}

// --- exec.run ----------------------------------------------------------------

type execRunArgs struct {
	executionDomainArgs
	Argv       []string          `json:"argv"`
	Shell      bool              `json:"shell,omitempty"`
	Stdin      string            `json:"stdin,omitempty"`
	TimeoutMS  *int              `json:"timeout_ms,omitempty"`
	Background bool              `json:"background,omitempty"`
	Env        map[string]string `json:"env,omitempty"`
}

type execRunTool struct{ deps Deps }

func (t *execRunTool) Name() string { return ToolExecRun }
func (t *execRunTool) Description() string {
	return "Run one command in the case_run workspace session (argv by default; shell=true runs the single argv entry through the platform shell). Every command is recorded automatically as immutable command evidence (argv/cwd/env in full/stdout/stderr/exit); large output spills to the content ledger and is marked stdout_truncated/stderr_truncated with a full-content ref. Workspace file changes are recorded as file evidence automatically."
}
func (t *execRunTool) Parameters() json.RawMessage {
	return schemaObject(executionDomainProperties(map[string]any{
		"argv": map[string]any{
			"type": "array", "minItems": 1, "maxItems": 64,
			"items":       map[string]any{"type": "string"},
			"description": "program and arguments; with shell=true a single command-line string",
		},
		"shell":      map[string]any{"type": "boolean", "description": "run argv[0] through the platform shell (explicit opt-in)"},
		"stdin":      map[string]any{"type": "string"},
		"timeout_ms": execTimeoutSchema,
		"background": map[string]any{"type": "boolean", "description": "start detached and return the PID immediately"},
		"env":        execEnvSchema,
	}), "argv")
}
func (t *execRunTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	port, ok := t.deps.Execution.(GeneralExecutionPort)
	if !ok {
		return nil, missingDep("general execution")
	}
	var args execRunArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	sutID, role, err := args.sessionSelector()
	if err != nil {
		return nil, err
	}
	if err := validateExecArgv(args.Argv); err != nil {
		return nil, err
	}
	if err := validateExecTimeout(args.TimeoutMS); err != nil {
		return nil, err
	}
	if err := validateText("stdin", args.Stdin, 0, 1<<20); err != nil {
		return nil, err
	}
	if err := validateExecEnv(args.Env); err != nil {
		return nil, err
	}
	in := ExecRunInput{
		SUTID: sutID, AccountRole: role, Argv: args.Argv, Shell: args.Shell,
		Stdin: args.Stdin, Background: args.Background, Env: args.Env,
	}
	if args.TimeoutMS != nil {
		in.TimeoutMS = *args.TimeoutMS
	}
	return portResult(port.ExecRun(ctx, scope, in))
}

// --- shell.* / pty.* ----------------------------------------------------------

type shellOpenArgs struct {
	executionDomainArgs
	Argv []string          `json:"argv,omitempty"`
	Cols *int              `json:"cols,omitempty"`
	Rows *int              `json:"rows,omitempty"`
	Env  map[string]string `json:"env,omitempty"`
}

type shellIDArgs struct {
	executionDomainArgs
	ShellID   string `json:"shell_id"`
	Data      string `json:"data,omitempty"`
	Since     *int64 `json:"since,omitempty"`
	TimeoutMS *int   `json:"timeout_ms,omitempty"`
}

// shellDomainTool implements one shell.*/pty.* verb over the shared port op.
type shellDomainTool struct {
	deps        Deps
	name        string
	op          string
	description string
	parameters  json.RawMessage
}

func (t *shellDomainTool) Name() string                { return t.name }
func (t *shellDomainTool) Description() string         { return t.description }
func (t *shellDomainTool) Parameters() json.RawMessage { return t.parameters }

func (t *shellDomainTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	port, ok := t.deps.Execution.(GeneralExecutionPort)
	if !ok {
		return nil, missingDep("general execution")
	}
	in := ShellOpInput{Op: t.op}
	if t.op == "create" {
		var args shellOpenArgs
		if err := decodeParams(raw, &args); err != nil {
			return nil, err
		}
		sutID, role, err := args.sessionSelector()
		if err != nil {
			return nil, err
		}
		if len(args.Argv) > 0 {
			if err := validateExecArgv(args.Argv); err != nil {
				return nil, err
			}
		}
		if args.Cols != nil && (*args.Cols < 1 || *args.Cols > 500) {
			return nil, Invalid("cols", "must be between 1 and 500")
		}
		if args.Rows != nil && (*args.Rows < 1 || *args.Rows > 200) {
			return nil, Invalid("rows", "must be between 1 and 200")
		}
		if err := validateExecEnv(args.Env); err != nil {
			return nil, err
		}
		in.SUTID, in.AccountRole, in.Argv, in.Env = sutID, role, args.Argv, args.Env
		if args.Cols != nil {
			in.Cols = *args.Cols
		}
		if args.Rows != nil {
			in.Rows = *args.Rows
		}
		return portResult(port.ShellOp(ctx, scope, in))
	}
	var args shellIDArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	sutID, role, err := args.sessionSelector()
	if err != nil {
		return nil, err
	}
	if err := validateText("shell_id", args.ShellID, 1, 200); err != nil {
		return nil, err
	}
	in.SUTID, in.AccountRole, in.ShellID = sutID, role, args.ShellID
	switch t.op {
	case "write":
		if args.Data == "" {
			return nil, Invalid("data", "data is required")
		}
		if err := validateText("data", args.Data, 1, 1<<20); err != nil {
			return nil, err
		}
		in.Data = args.Data
	case "read":
		if args.Since != nil && *args.Since < 0 {
			return nil, Invalid("since", "must not be negative")
		}
		if args.Since != nil {
			in.Since = *args.Since
		}
	case "wait":
		if err := validateExecTimeout(args.TimeoutMS); err != nil {
			return nil, err
		}
		if args.TimeoutMS != nil {
			in.TimeoutMS = *args.TimeoutMS
		}
	}
	return portResult(port.ShellOp(ctx, scope, in))
}

// --- file.* -------------------------------------------------------------------

type fileToolArgs struct {
	executionDomainArgs
	Path     string `json:"path,omitempty"`
	Content  string `json:"content,omitempty"`
	Encoding string `json:"encoding,omitempty"`
}

type fileDomainTool struct {
	deps        Deps
	name        string
	op          string
	description string
	parameters  json.RawMessage
}

func (t *fileDomainTool) Name() string                { return t.name }
func (t *fileDomainTool) Description() string         { return t.description }
func (t *fileDomainTool) Parameters() json.RawMessage { return t.parameters }

func (t *fileDomainTool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	port, ok := t.deps.Execution.(GeneralExecutionPort)
	if !ok {
		return nil, missingDep("general execution")
	}
	var args fileToolArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	sutID, role, err := args.sessionSelector()
	if err != nil {
		return nil, err
	}
	if t.op != "list" || strings.TrimSpace(args.Path) != "" {
		if err := validateText("path", args.Path, 1, 1024); err != nil {
			return nil, err
		}
	}
	in := FileOpInput{Op: t.op, SUTID: sutID, AccountRole: role, Path: args.Path}
	if t.op == "upload" {
		if args.Content == "" {
			return nil, Invalid("content", "content is required")
		}
		if err := validateText("content", args.Content, 1, 3<<20); err != nil {
			return nil, err
		}
		switch args.Encoding {
		case "":
			in.Encoding = "text"
		case "text", "base64":
			in.Encoding = args.Encoding
		default:
			return nil, Invalid("encoding", "encoding must be text or base64")
		}
		if in.Encoding == "base64" {
			if _, decodeErr := base64.StdEncoding.DecodeString(args.Content); decodeErr != nil {
				return nil, Invalid("content", "must be valid base64 when encoding is base64")
			}
		}
		in.Content = args.Content
	}
	return portResult(port.FileOp(ctx, scope, in))
}

// --- registration -------------------------------------------------------------

// executionDomainTools builds the 13 general-execution tools. The descriptions
// state the recording semantics (GD-01: governance by automatic recording, not
// by gates): every command and workspace file change becomes immutable evidence.
func executionDomainTools(deps Deps) []Tool {
	stringSchema := map[string]any{"type": "string"}
	return []Tool{
		&execRunTool{deps: deps},
		&shellDomainTool{deps: deps, name: ToolShellOpen, op: "create",
			description: "Open an interactive platform shell in the case_run workspace session. The shell transcript is recorded automatically: on exit one immutable command evidence entry (argv/cwd/env in full/output/exit) settles; workspace file changes become file evidence.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"cols": shellColsSchema, "rows": shellRowsSchema, "env": execEnvSchema,
			}))},
		&shellDomainTool{deps: deps, name: ToolShellWrite, op: "write",
			description: "Write input to an open shell (include the trailing newline to submit). The shell's full transcript is recorded automatically as command evidence on exit.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"shell_id": shellIDSchema, "data": stringSchema,
			}), "shell_id", "data")},
		&shellDomainTool{deps: deps, name: ToolShellRead, op: "read",
			description: "Read new shell output since a cursor (pass next_cursor from the previous read). Reading is a view; the recording layer keeps the full transcript as evidence automatically.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"shell_id": shellIDSchema,
				"since":    map[string]any{"type": "integer", "minimum": 0, "description": "byte cursor from the previous read's next_cursor"},
			}), "shell_id")},
		&shellDomainTool{deps: deps, name: ToolShellClose, op: "close",
			description: "Close an open shell and settle its recording: the full transcript becomes one immutable command evidence entry automatically.",
			parameters:  schemaObject(executionDomainProperties(map[string]any{"shell_id": shellIDSchema}), "shell_id")},
		&fileDomainTool{deps: deps, name: ToolFileWrite, op: "upload",
			description: "Write a file into the case_run workspace (text by default, base64 for binary). Every workspace file change is recorded automatically as immutable file evidence (sha256/size/mime).",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"path":     map[string]any{"type": "string", "description": "workspace-relative path"},
				"content":  stringSchema,
				"encoding": map[string]any{"type": "string", "enum": []string{"text", "base64"}, "description": "content encoding (default text)"},
			}), "path", "content")},
		&fileDomainTool{deps: deps, name: ToolFileRead, op: "download",
			description: "Read a workspace file (content returned as text when valid UTF-8, base64 otherwise, with an explicit encoding field). Reads are views; workspace changes are recorded as file evidence automatically.",
			parameters:  schemaObject(executionDomainProperties(map[string]any{"path": stringSchema}), "path")},
		&fileDomainTool{deps: deps, name: ToolFileList, op: "list",
			description: "List workspace directory entries (path defaults to the workspace root). The workspace itself is the recorded execution area: file changes become file evidence automatically.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"path": map[string]any{"type": "string", "description": "workspace-relative directory (default root)"},
			}))},
		&shellDomainTool{deps: deps, name: ToolPTYOpen, op: "create",
			description: "Open an interactive PTY program (ssh/psql/mysql/REPL; argv defaults to the platform shell) with an explicit terminal size. The session transcript is recorded automatically as immutable command evidence on exit.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"argv": map[string]any{"type": "array", "minItems": 1, "maxItems": 64, "items": stringSchema},
				"cols": shellColsSchema, "rows": shellRowsSchema, "env": execEnvSchema,
			}))},
		&shellDomainTool{deps: deps, name: ToolPTYWrite, op: "write",
			description: "Write input to an open PTY session (include the trailing newline to submit). The full transcript is recorded automatically as command evidence on exit.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"shell_id": shellIDSchema, "data": stringSchema,
			}), "shell_id", "data")},
		&shellDomainTool{deps: deps, name: ToolPTYRead, op: "read",
			description: "Read new PTY output since a cursor (pass next_cursor from the previous read). Reading is a view; the recording layer keeps the full transcript as evidence automatically.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"shell_id": shellIDSchema,
				"since":    map[string]any{"type": "integer", "minimum": 0},
			}), "shell_id")},
		&shellDomainTool{deps: deps, name: ToolPTYClose, op: "close",
			description: "Close an open PTY session and settle its recording: the full transcript becomes one immutable command evidence entry automatically.",
			parameters:  schemaObject(executionDomainProperties(map[string]any{"shell_id": shellIDSchema}), "shell_id")},
		&shellDomainTool{deps: deps, name: ToolPTYWait, op: "wait",
			description: "Wait for a PTY program to exit and return its exit code/outcome. The full session transcript is recorded automatically as immutable command evidence on exit.",
			parameters: schemaObject(executionDomainProperties(map[string]any{
				"shell_id": shellIDSchema, "timeout_ms": execTimeoutSchema,
			}), "shell_id")},
	}
}

// init registers the general-execution domain through the additive P4 tool-group
// mechanism (the DatabaseLister precedent): an execution port without the
// general surface leaves the tools unregistered instead of failing at call time.
// NewToolGroupRegistry and every later composition (P5–P7, the runtime registry)
// evaluate this group; profile/role allow-lists still filter what a run sees.
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.Execution == nil {
			return nil
		}
		if _, ok := deps.Execution.(GeneralExecutionPort); !ok {
			return nil
		}
		return executionDomainTools(deps)
	})
}
