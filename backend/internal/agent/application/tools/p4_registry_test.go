package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// p4RegistryTestTool is a minimal Tool used to prove that domain groups
// registered through RegisterP4ToolGroup are composed by NewP4Registry.
type p4RegistryTestTool struct{ name string }

func (t *p4RegistryTestTool) Name() string                { return t.name }
func (t *p4RegistryTestTool) Description() string         { return "P4 registry test tool" }
func (t *p4RegistryTestTool) Parameters() json.RawMessage { return emptyObjectSchema }
func (t *p4RegistryTestTool) Execute(context.Context, Scope, json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

// resetP4ToolGroupsForTest isolates a test from the process-global P4 group
// registry. Tests register groups through RegisterP4ToolGroup, which appends
// for the lifetime of the process; without isolation a later test with
// -shuffle=on would observe groups registered by an earlier one. The snapshot
// is restored on cleanup so other test files keep the state they started with.
func resetP4ToolGroupsForTest(t *testing.T) {
	t.Helper()
	p4ToolGroupMu.Lock()
	saved := p4ToolGroups
	p4ToolGroups = nil
	p4ToolGroupMu.Unlock()
	t.Cleanup(func() {
		p4ToolGroupMu.Lock()
		p4ToolGroups = saved
		p4ToolGroupMu.Unlock()
	})
}

func TestP4RegistryStartsWithNoInjectedGroups(t *testing.T) {
	resetP4ToolGroupsForTest(t)

	const injected = "p4.test.injected"

	registry := NewP4Registry(Deps{})
	if _, ok := registry.Get(injected); ok {
		t.Fatalf("NewP4Registry exposed %q before its group was registered", injected)
	}
	// The P4 registry must stay a superset of the frozen P1/P2/P3 set: the
	// runner swaps registry construction to NewP4Registry for every phase.
	for _, name := range []string{ToolContextTaskPack, ToolDesignGraphRead, ToolExecutionStepRecord} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("P4 registry is missing base tool %q", name)
		}
	}
}

func TestP4RegistryComposesInjectedGroups(t *testing.T) {
	resetP4ToolGroupsForTest(t)

	const injected = "p4.test.injected"

	RegisterP4ToolGroup(nil) // nil groups are ignored defensively
	RegisterP4ToolGroup(func(Deps) []Tool {
		return []Tool{&p4RegistryTestTool{name: injected}}
	})

	registry := NewP4Registry(Deps{})
	tool, ok := registry.Get(injected)
	if !ok {
		t.Fatalf("P4 group tool %q was not registered", injected)
	}
	value, err := tool.Execute(context.Background(), Scope{VersionID: 1}, json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute injected tool: %v", err)
	}
	result, ok := value.(map[string]any)
	if !ok || result["ok"] != true {
		t.Fatalf("injected tool result = %#v", value)
	}
}

func TestP4RegistryPassesDepsToGroups(t *testing.T) {
	resetP4ToolGroupsForTest(t)

	const injected = "p4.test.deps_injected"

	// The group only exposes its tool when NewP4Registry hands it real deps.
	RegisterP4ToolGroup(func(deps Deps) []Tool {
		if deps.VersionPack == nil {
			return nil
		}
		return []Tool{&p4RegistryTestTool{name: injected}}
	})

	if _, ok := NewP4Registry(Deps{}).Get(injected); ok {
		t.Fatalf("group exposed %q when NewP4Registry received zero deps", injected)
	}

	called := false
	deps := Deps{VersionPack: func(context.Context, Scope) (any, error) {
		called = true
		return nil, nil
	}}
	if _, ok := NewP4Registry(deps).Get(injected); !ok {
		t.Fatal("NewP4Registry did not pass its deps to the registered group")
	}
	if called {
		t.Fatal("group must not evaluate the dependency while registering tools")
	}
}
