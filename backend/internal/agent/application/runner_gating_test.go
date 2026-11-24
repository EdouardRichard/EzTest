package agentapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agent "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// TestGatingProfileAllowsToolDomainWildcard pins the capability-domain gating: a
// profile whitelist entry "exec.*" allows every registered exec-domain tool.
// Before v0.2.4-C T11 profileAllowsTool was exact-match only while
// ToolSet.Allows already supported wildcards — two divergent implementations of
// one rule (constitution art.5).
func TestGatingProfileAllowsToolDomainWildcard(t *testing.T) {
	profile := agent.AgentProfile{
		Key:          "case_executor",
		AllowedTools: []string{"exec.*", "shell.*", "file.*", "pty.*", "http.request", "execution.step_record"},
	}
	allowed := []string{
		"exec.run",
		"shell.open", "shell.write", "shell.read", "shell.close",
		"file.write", "file.read", "file.list",
		"pty.open", "pty.write", "pty.read", "pty.close", "pty.wait",
		"http.request", "execution.step_record",
	}
	for _, name := range allowed {
		if !profileAllowsTool(profile, name) {
			t.Fatalf("profile with domain wildcards rejected %q", name)
		}
	}
	denied := []string{
		"browser.goto",   // no browser.* entry
		"db.query",       // not listed
		"execx.run",      // prefix must match a whole namespace segment
		"exec",           // the bare namespace is not a tool
		"execution.wait", // execution.* was NOT granted, only one exact member
		"",               // empty name
	}
	for _, name := range denied {
		if profileAllowsTool(profile, name) {
			t.Fatalf("profile granted unlisted tool %q", name)
		}
	}
}

// TestGatingProfileAllowsToolExactNamesUnchanged: exact-match whitelists keep their
// frozen semantics (no accidental widening from the wildcard support).
func TestGatingProfileAllowsToolExactNamesUnchanged(t *testing.T) {
	profile := agent.AgentProfile{Key: "design_host", AllowedTools: []string{"context.version_pack", "requirement.read"}}
	if !profileAllowsTool(profile, "context.version_pack") || !profileAllowsTool(profile, "requirement.read") {
		t.Fatal("exact whitelist entries must keep working")
	}
	if profileAllowsTool(profile, "context.task_pack") {
		t.Fatal("an unlisted sibling must stay denied")
	}
	if profileAllowsTool(agent.AgentProfile{Key: "empty"}, "exec.run") {
		t.Fatal("an empty whitelist must deny everything")
	}
}

// TestGatingProfileAllowsToolMatchesToolSet is the alignment pin: the profile gate and
// the role ToolSet must agree on every whitelist/name combination — the rule has
// exactly one implementation.
func TestGatingProfileAllowsToolMatchesToolSet(t *testing.T) {
	whitelists := [][]string{
		{"exec.*"},
		{"exec.run"},
		{"exec.*", "shell.close"},
		{"*", "exec.*"},
		{" exec.* ", "http.request"},
		nil,
	}
	names := []string{"exec.run", "exec", "execx.run", "shell.close", "shell.open", "http.request", "browser.goto", ""}
	for _, whitelist := range whitelists {
		set := tools.NewToolSet("probe", whitelist)
		profile := agent.AgentProfile{Key: "probe", AllowedTools: whitelist}
		for _, name := range names {
			if got, want := profileAllowsTool(profile, name), set.Allows(name); got != want {
				t.Fatalf("whitelist %v name %q: profileAllowsTool=%v but ToolSet.Allows=%v (rule divergence)", whitelist, name, got, want)
			}
		}
	}
}

// TestGatingWildcardNeverInventsUnregisteredTools: a wildcard only permits namespace
// members the registry actually registers; an unknown name is still rejected.
func TestGatingWildcardNeverInventsUnregisteredTools(t *testing.T) {
	registry := tools.NewRegistry()
	set := tools.NewToolSet("probe", []string{"ghost.*"})
	if !set.Allows("ghost.run") {
		t.Fatal("the wildcard should allow the namespace member")
	}
	_, err := set.Execute(context.Background(), registry, tools.Scope{}, "ghost.run", json.RawMessage(`{}`))
	var toolErr *tools.Error
	if !errors.As(err, &toolErr) || toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("unregistered tool error = %v, want invalid_argument", err)
	}
}
