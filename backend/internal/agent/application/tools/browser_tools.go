package tools

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
)

// ToolBrowserCLI is the browser capability-domain tool (design v1.3.1 §4.2
// P10/P11, Q3=B). It is a thin convenience passthrough to the OFFICIAL
// `@playwright/cli` running inside the case_run's executor session — NOT a
// capability ceiling and NOT a business-action enumeration. Every CLI
// subcommand passes through verbatim; the schema deliberately does NOT list
// them (the CLI command face floats with @latest). The general exec.run channel
// remains the escape valve (constitution art.2.6): a model can run playwright-cli
// or anything else directly.
//
// The executor aligns the CLI's named session (-s) with the executor session and
// settles the CLI's output files as typed evidence (snapshot/screenshot/trace/
// console). The tool layer only forwards {subcommand, args} plus the shared
// (sut_id, account_role) session selector.
const ToolBrowserCLI = "browser.cli"

// browserCliSubcommandPattern matches the CLI subcommand SHAPE (a lower-case
// kebab token), not an enumerated set: any current or future subcommand passes.
// A leading flag is rejected (the subcommand is positional).
var browserCliSubcommandPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// browserCLIArgs is one browser.cli call. Subcommand is the positional CLI verb;
// Args are its positional arguments and flags, forwarded verbatim. The shared
// executionDomainArgs carries the (sut_id, account_role) session selector.
type browserCLIArgs struct {
	executionDomainArgs
	Subcommand string   `json:"subcommand"`
	Args       []string `json:"args,omitempty"`
	TimeoutMS  *int     `json:"timeout_ms,omitempty"`
}

type browserCLITool struct{ deps Deps }

func (t *browserCLITool) Name() string { return ToolBrowserCLI }

func (t *browserCLITool) Description() string {
	return "Run an official playwright-cli subcommand in the case_run browser session (browser capability domain). " +
		"Pass the subcommand and its arguments verbatim — the full CLI command face is available (open/goto/snapshot/click/fill/dblclick/hover/drag/select/upload/press/tab-*/state-save/state-load/route/tracing-start/tracing-stop/console/requests/resize/run-code/eval/...); this tool does not enumerate or limit them. " +
		"snapshot returns ARIA refs (e.g. e15, cross-frame f1e2) usable as click/fill targets; add --raw for machine-readable output. " +
		"Console/network READ (the named replacement for the retired browser.console/browser.network tools): use `console [min-level] [--all]` for console output and `requests [--all] [--filter=<regex>]` + `request <index>` + `response-body <index>` for recorded network traffic; the executor also pure-records browser traffic as network evidence automatically. " +
		"The CLI session is bound to the executor session automatically (do not pass -s/--session). " +
		"Every command and its snapshot/screenshot/trace/console outputs are recorded as immutable evidence automatically. " +
		"For anything the CLI does not cover, use exec.run (general escape valve)."
}

func (t *browserCLITool) Parameters() json.RawMessage {
	return schemaObject(executionDomainProperties(map[string]any{
		"subcommand": map[string]any{
			"type":        "string",
			"description": "the playwright-cli subcommand (positional verb, e.g. goto, snapshot, click, dblclick, tab-list, upload, run-code); not an enumerated set",
		},
		"args": map[string]any{
			"type":        "array",
			"maxItems":    64,
			"items":       map[string]any{"type": "string"},
			"description": "positional arguments and flags forwarded verbatim to the CLI (e.g. [\"http://host/page\"], [\"e15\"], [\"--raw\"]); -s/--session is reserved for the executor",
		},
		"timeout_ms": execTimeoutSchema,
	}), "subcommand")
}

func (t *browserCLITool) Execute(ctx context.Context, scope Scope, raw json.RawMessage) (any, error) {
	port, ok := t.deps.Execution.(GeneralExecutionPort)
	if !ok {
		return nil, missingDep("general execution")
	}
	var args browserCLIArgs
	if err := decodeParams(raw, &args); err != nil {
		return nil, err
	}
	sutID, role, err := args.sessionSelector()
	if err != nil {
		return nil, err
	}
	subcommand := strings.TrimSpace(args.Subcommand)
	if subcommand == "" {
		return nil, Invalid("subcommand", "subcommand is required")
	}
	if !browserCliSubcommandPattern.MatchString(subcommand) {
		return nil, Invalid("subcommand", "must be a lower-case CLI subcommand token (e.g. goto, snapshot, tab-list)")
	}
	if err := validateBrowserCLIArgs(args.Args); err != nil {
		return nil, err
	}
	if err := validateExecTimeout(args.TimeoutMS); err != nil {
		return nil, err
	}
	// The executor resolves the installed CLI entry and injects the session-bound
	// -s flag; the tool forwards the bare program name + subcommand + args.
	argv := make([]string, 0, len(args.Args)+2)
	argv = append(argv, "playwright-cli", subcommand)
	argv = append(argv, args.Args...)
	in := ExecRunInput{
		SUTID:       sutID,
		AccountRole: role,
		Argv:        argv,
	}
	if args.TimeoutMS != nil {
		in.TimeoutMS = *args.TimeoutMS
	}
	return portResult(port.ExecRun(ctx, scope, in))
}

// validateBrowserCLIArgs bounds the forwarded argv and rejects the reserved
// session flags. The CLI named session is owned by the executor (bound 1:1 to
// the executor session so concurrent case_runs never share browser state); a
// model-supplied -s/--session would break that alignment. This is session
// plumbing, not a capability restriction — exec.run can still run playwright-cli
// with any -s directly (the escape valve).
func validateBrowserCLIArgs(args []string) *Error {
	if len(args) > 64 {
		return Invalid("args", "at most 64 CLI arguments are allowed")
	}
	for _, entry := range args {
		if err := validateText("args", entry, 0, 4096); err != nil {
			return err
		}
		if entry == "-s" || entry == "--session" ||
			strings.HasPrefix(entry, "-s=") || strings.HasPrefix(entry, "--session=") {
			return Invalid("args", "the CLI session (-s/--session) is bound to the executor session; use sut_id/account_role to select it, or exec.run for a custom session")
		}
	}
	return nil
}

// browserDomainTools builds the browser capability-domain tools. Today that is
// the single browser.cli passthrough; the domain is the organizing unit, not a
// ceiling (constitution art.2.1).
//
// Capability three-list (constitution Constraints B.1; SC-C4 console/network
// non-regression). The retired browser.console / browser.network atomic tools
// (T13) are NOT a silent capability loss — their READ capability is Enabled
// through the named browser.cli paths below, and browser traffic is pure-recorded
// as network evidence (T16, design §4.3):
//
//   - console read  — Enabled: browser.cli `console [min-level] [--all] [--clear]`.
//   - network read  — Enabled: browser.cli `requests [--all] [--filter]` +
//     `request <index>` + `request-headers/body` + `response-headers/body <index>`;
//     recorded traffic also settles as network evidence (execution.evidence_read).
//   - network mock  — Enabled: browser.cli `route <pattern>` / `route-list` /
//     `unroute` (recording stays pure; route is an explicit model action, GD-01).
//   - Constrained: none in this domain (the CLI command face floats at @latest;
//     the tool layer enumerates nothing). -s/--session is reserved for executor
//     session plumbing, not a capability limit (exec.run is the escape valve).
//   - Forbidden: none (GD-01 removed the sandbox/guard layer; no security-based
//     restriction is reintroduced).
func browserDomainTools(deps Deps) []Tool {
	return []Tool{&browserCLITool{deps: deps}}
}

// init registers the browser domain through the additive P4 tool-group mechanism
// (the GeneralExecutionPort precedent): an execution port without the general
// surface leaves browser.cli unregistered instead of failing at call time.
func init() {
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.Execution == nil {
			return nil
		}
		if _, ok := deps.Execution.(GeneralExecutionPort); !ok {
			return nil
		}
		return browserDomainTools(deps)
	})
}
