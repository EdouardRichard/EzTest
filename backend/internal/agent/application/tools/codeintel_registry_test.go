package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
)

func TestCodeIntelRegistryIncludesFrozenToolSet(t *testing.T) {
	registry := tools.NewCodeIntelRegistry(tools.Deps{})

	frozen := tools.CodeIntelToolNames()
	if len(frozen) != 10 {
		t.Fatalf("CodeIntelToolNames() = %d tools, want 10: %v", len(frozen), frozen)
	}
	for _, name := range frozen {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("NewCodeIntelRegistry is missing codeintel tool %q", name)
		}
	}
	// P1–P4 tools stay registered: profiles select their own subset.
	for _, name := range []string{
		tools.ToolContextTaskPack,
		tools.ToolDesignGraphRead,
		tools.ToolExecutionStepRecord,
	} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("NewCodeIntelRegistry is missing base tool %q", name)
		}
	}
	if _, ok := registry.Get("case.confirm"); ok {
		t.Fatal("NewCodeIntelRegistry must not register confirmation tools")
	}
}

func TestCodeIntelToolSchemasAreObjectWithoutVersionScope(t *testing.T) {
	registry := tools.NewCodeIntelRegistry(tools.Deps{})
	for _, name := range tools.CodeIntelToolNames() {
		tool, ok := registry.Get(name)
		if !ok {
			t.Fatalf("tool %q is not registered", name)
		}
		var schema struct {
			Type       string                    `json:"type"`
			Properties map[string]map[string]any `json:"properties"`
			Required   []string                  `json:"required"`
		}
		if err := json.Unmarshal(tool.Parameters(), &schema); err != nil {
			t.Fatalf("%s Parameters() is not valid JSON: %v", name, err)
		}
		if schema.Type != "object" {
			t.Fatalf("%s Parameters() type = %q, want object", name, schema.Type)
		}
		for property := range schema.Properties {
			lower := strings.ToLower(property)
			if strings.Contains(lower, "version") {
				t.Fatalf("%s schema exposes version scope property %q", name, property)
			}
		}
	}
}

func TestCodeIntelRegistryGroupRegistrationComposes(t *testing.T) {
	const injected = "p5.test.injected"
	tools.RegisterCodeIntelToolGroup(nil) // nil factories are ignored defensively
	tools.RegisterCodeIntelToolGroup(func(deps tools.Deps) []tools.Tool {
		if deps.CodeIntel.Propose == nil {
			return nil
		}
		return []tools.Tool{codeIntelTestTool{name: injected}}
	})

	if _, ok := tools.NewCodeIntelRegistry(tools.Deps{}).Get(injected); ok {
		t.Fatalf("group exposed %q when NewCodeIntelRegistry received zero codeintel deps", injected)
	}
	if _, ok := tools.NewCodeIntelRegistry(tools.Deps{CodeIntel: tools.CodeIntelDeps{Propose: &fakeSutProposalWriter{}}}).Get(injected); !ok {
		t.Fatal("NewCodeIntelRegistry did not pass its deps to the registered codeintel group")
	}
}

type codeIntelTestTool struct{ name string }

func (t codeIntelTestTool) Name() string        { return t.name }
func (t codeIntelTestTool) Description() string { return "P5 registry test tool" }
func (t codeIntelTestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t codeIntelTestTool) Execute(context.Context, tools.Scope, json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}
