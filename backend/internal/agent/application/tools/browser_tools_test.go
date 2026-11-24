package tools

import (
	"strings"
	"testing"
)

// TestBrowserCLIRegisteredInBrowserDomain pins the browser capability-domain
// registration: browser.cli exists when the execution port implements the
// general surface, and the registry derives its domain tag ("browser") from the
// single authority — the tool name (constitution art.5).
func TestBrowserCLIRegisteredInBrowserDomain(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	tool, ok := registry.Get(ToolBrowserCLI)
	if !ok {
		t.Fatalf("%s missing from the registry", ToolBrowserCLI)
	}
	if ToolDomain(tool.Name()) != "browser" {
		t.Fatalf("domain = %q, want browser", ToolDomain(tool.Name()))
	}
	if !containsString(registry.NamesInDomain("browser"), ToolBrowserCLI) {
		t.Fatalf("browser domain membership = %v, want browser.cli", registry.NamesInDomain("browser"))
	}
}

// TestBrowserCLINotRegisteredWithoutGeneralSurface: an execution port that does
// not implement the general surface leaves browser.cli unregistered (the
// GeneralExecutionPort precedent), instead of failing at call time.
func TestBrowserCLINotRegisteredWithoutGeneralSurface(t *testing.T) {
	registry := NewToolGroupRegistry(Deps{Execution: &fakeExecutionPort{}})
	if _, ok := registry.Get(ToolBrowserCLI); ok {
		t.Fatalf("%s registered without the general execution surface", ToolBrowserCLI)
	}
}

// TestBrowserCLIDelegatesPassthrough pins the wire contract: {subcommand, args}
// forward verbatim into exec.run argv = ["playwright-cli", subcommand, ...args],
// with the shared (sut_id, account_role) session selector and timeout. The tool
// does NOT inject -s (the executor binds the CLI session) and does NOT enumerate
// or rewrite the subcommand.
func TestBrowserCLIDelegatesPassthrough(t *testing.T) {
	port := &fakeGeneralExecutionPort{}
	registry := generalRegistry(port)
	result, err := executeTool(t, registry, ToolBrowserCLI, `{
		"sut_id": "7", "account_role": "admin",
		"subcommand": "goto", "args": ["http://sut.local/page", "--raw"],
		"timeout_ms": 8000
	}`)
	if err != nil {
		t.Fatalf("browser.cli: %v", err)
	}
	if port.execCalls != 1 {
		t.Fatalf("ExecRun calls = %d, want 1 (browser.cli is an exec.run passthrough)", port.execCalls)
	}
	in := port.execIn
	if in.SUTID == nil || *in.SUTID != 7 || in.AccountRole != "admin" || in.TimeoutMS != 8000 {
		t.Fatalf("ExecRunInput selector/timeout = %+v", in)
	}
	want := []string{"playwright-cli", "goto", "http://sut.local/page", "--raw"}
	if len(in.Argv) != len(want) {
		t.Fatalf("argv = %v, want %v", in.Argv, want)
	}
	for i := range want {
		if in.Argv[i] != want[i] {
			t.Fatalf("argv = %v, want %v", in.Argv, want)
		}
	}
	if port.scope.CaseRunID != 3001 {
		t.Fatalf("scope was not passed through: %+v", port.scope)
	}
	out, ok := result.(map[string]any)
	if !ok || out["command_id"] != "cmd_1" {
		t.Fatalf("result = %#v, want the exec.run port result", result)
	}
}

// TestBrowserCLISchemaDoesNotEnumerateSubcommands pins the passthrough contract
// (PR-001/002): the subcommand schema is a free string, NOT an enum of business
// actions. The CLI command face floats with @latest; the tool must not freeze it.
func TestBrowserCLISchemaDoesNotEnumerateSubcommands(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	tool, ok := registry.Get(ToolBrowserCLI)
	if !ok {
		t.Fatalf("%s missing", ToolBrowserCLI)
	}
	schema := string(tool.Parameters())
	if strings.Contains(schema, `"enum"`) {
		t.Fatalf("browser.cli schema enumerates values (must be a passthrough): %s", schema)
	}
	if !strings.Contains(schema, `"subcommand"`) || !strings.Contains(schema, `"args"`) {
		t.Fatalf("browser.cli schema missing subcommand/args: %s", schema)
	}
	if !strings.Contains(schema, `"sut_id"`) || strings.Contains(schema, "version_sut_id") {
		t.Fatalf("browser.cli schema must use the sut_id wire name: %s", schema)
	}
}

// TestBrowserCLIValidatesSubcommand: the subcommand is required and must be a
// lower-case CLI token (shape, not an enumerated set); a leading flag is rejected.
func TestBrowserCLIValidatesSubcommand(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	for _, raw := range []string{
		`{}`,
		`{"subcommand": ""}`,
		`{"subcommand": "--raw"}`,
		`{"subcommand": "Goto"}`,
	} {
		_, err := executeTool(t, registry, ToolBrowserCLI, raw)
		toolErr := requireExecutionToolError(t, err, CodeInvalidArgument)
		if field, _ := toolErr.Details["field"].(string); field != "subcommand" {
			t.Fatalf("raw %s: error field = %q, want subcommand", raw, field)
		}
	}
}

// TestBrowserCLIRejectsReservedSessionFlag: the CLI named session is bound to the
// executor session; a model-supplied -s/--session is rejected with a self-
// describing error pointing at the selector and the exec.run escape valve.
func TestBrowserCLIRejectsReservedSessionFlag(t *testing.T) {
	registry := generalRegistry(&fakeGeneralExecutionPort{})
	for _, args := range []string{
		`["-s=other"]`, `["-s", "other"]`, `["--session=other"]`, `["--session", "other"]`,
	} {
		_, err := executeTool(t, registry, ToolBrowserCLI, `{"subcommand":"snapshot","args":`+args+`}`)
		toolErr := requireExecutionToolError(t, err, CodeInvalidArgument)
		if field, _ := toolErr.Details["field"].(string); field != "args" {
			t.Fatalf("args %s: error field = %q, want args", args, field)
		}
	}
}

// TestBrowserCLIInAdminCatalog: the admin profile editor can select browser.cli
// (metadata-only catalog entry, same additive condition as the general domains).
func TestBrowserCLIInAdminCatalog(t *testing.T) {
	catalog := AdminCatalogRegistry()
	if _, ok := catalog.Get(ToolBrowserCLI); !ok {
		t.Fatalf("%s missing from AdminCatalogRegistry", ToolBrowserCLI)
	}
}
