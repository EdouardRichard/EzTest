package tools

import (
	"context"
	"encoding/json"
	"testing"
)

// toolGroupRegistryTestTool is a minimal Tool used to prove that domain groups
// registered through RegisterToolGroup are composed by NewToolGroupRegistry.
type toolGroupRegistryTestTool struct{ name string }

func (t *toolGroupRegistryTestTool) Name() string                { return t.name }
func (t *toolGroupRegistryTestTool) Description() string         { return "P4 registry test tool" }
func (t *toolGroupRegistryTestTool) Parameters() json.RawMessage { return emptyObjectSchema }
func (t *toolGroupRegistryTestTool) Execute(context.Context, Scope, json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}

// resetToolGroupsForTest isolates a test from the process-global P4 group
// registry. Tests register groups through RegisterToolGroup, which appends
// for the lifetime of the process; without isolation a later test with
// -shuffle=on would observe groups registered by an earlier one. The snapshot
// is restored on cleanup so other test files keep the state they started with.
func resetToolGroupsForTest(t *testing.T) {
	t.Helper()
	toolGroupMu.Lock()
	saved := toolGroups
	toolGroups = nil
	toolGroupMu.Unlock()
	t.Cleanup(func() {
		toolGroupMu.Lock()
		toolGroups = saved
		toolGroupMu.Unlock()
	})
}

func TestToolGroupRegistryStartsWithNoInjectedGroups(t *testing.T) {
	resetToolGroupsForTest(t)

	const injected = "p4.test.injected"

	registry := NewToolGroupRegistry(Deps{})
	if _, ok := registry.Get(injected); ok {
		t.Fatalf("NewToolGroupRegistry exposed %q before its group was registered", injected)
	}
	// The P4 registry must stay a superset of the frozen P1/P2/P3 set: the
	// runner swaps registry construction to NewToolGroupRegistry for every phase.
	for _, name := range []string{ToolContextTaskPack, ToolDesignGraphRead, ToolExecutionStepRecord} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("registry is missing base tool %q", name)
		}
	}
}

func TestToolGroupRegistryComposesInjectedGroups(t *testing.T) {
	resetToolGroupsForTest(t)

	const injected = "p4.test.injected"

	RegisterToolGroup(nil) // nil groups are ignored defensively
	RegisterToolGroup(func(Deps) []Tool {
		return []Tool{&toolGroupRegistryTestTool{name: injected}}
	})

	registry := NewToolGroupRegistry(Deps{})
	tool, ok := registry.Get(injected)
	if !ok {
		t.Fatalf("group tool %q was not registered", injected)
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

func TestToolGroupRegistryPassesDepsToGroups(t *testing.T) {
	resetToolGroupsForTest(t)

	const injected = "p4.test.deps_injected"

	// The group only exposes its tool when NewToolGroupRegistry hands it real deps.
	RegisterToolGroup(func(deps Deps) []Tool {
		if deps.VersionPack == nil {
			return nil
		}
		return []Tool{&toolGroupRegistryTestTool{name: injected}}
	})

	if _, ok := NewToolGroupRegistry(Deps{}).Get(injected); ok {
		t.Fatalf("group exposed %q when NewToolGroupRegistry received zero deps", injected)
	}

	called := false
	deps := Deps{VersionPack: func(context.Context, Scope) (any, error) {
		called = true
		return nil, nil
	}}
	if _, ok := NewToolGroupRegistry(deps).Get(injected); !ok {
		t.Fatal("NewToolGroupRegistry did not pass its deps to the registered group")
	}
	if called {
		t.Fatal("group must not evaluate the dependency while registering tools")
	}
}
