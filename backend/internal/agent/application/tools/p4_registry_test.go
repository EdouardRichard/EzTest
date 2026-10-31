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

func TestP4RegistryStartsWithNoInjectedGroups(t *testing.T) {
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
	const injected = "p4.test.injected"

	RegisterP4ToolGroup(nil) // nil groups are ignored defensively
	RegisterP4ToolGroup(func() []Tool {
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
