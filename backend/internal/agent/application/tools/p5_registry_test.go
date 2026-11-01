package tools_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	agenttool "github.com/EdouardRichard/EzTest/backend/internal/execution/application/agent_tool"
)

// fakeVisualRunner is the execution-side service port used to prove that the
// P5 registry composes the execution.visual_assert group registered by the
// execution agent_tool package.
type fakeVisualRunner struct {
	calls     int
	gotScope  tools.Scope
	gotAssert int64
}

func (f *fakeVisualRunner) VisualAssert(_ context.Context, scope tools.Scope, assertionID int64) (any, error) {
	f.calls++
	f.gotScope = scope
	f.gotAssert = assertionID
	return map[string]any{"classification": "passed", "assertion_result_id": "8101"}, nil
}

func TestP5RegistryIncludesFrozenToolSet(t *testing.T) {
	registry := tools.NewP5Registry(tools.Deps{})

	frozen := tools.P5ToolNames()
	if len(frozen) != 11 {
		t.Fatalf("P5ToolNames() = %d tools, want 11: %v", len(frozen), frozen)
	}
	for _, name := range frozen {
		if _, ok := registry.Get(name); !ok {
			t.Errorf("NewP5Registry is missing P5 tool %q", name)
		}
	}
	// The execution.visual_assert implementation registers itself from the
	// execution agent_tool package init; the external test package imports it.
	if _, ok := registry.Get(agenttool.ToolExecutionVisualAssert); !ok {
		t.Fatalf("NewP5Registry is missing %q registered by the execution tool group", agenttool.ToolExecutionVisualAssert)
	}
	// P1–P4 tools stay registered: profiles select their own subset.
	for _, name := range []string{
		tools.ToolContextTaskPack,
		tools.ToolDesignGraphRead,
		tools.ToolExecutionStepRecord,
	} {
		if _, ok := registry.Get(name); !ok {
			t.Fatalf("NewP5Registry is missing base tool %q", name)
		}
	}
	if _, ok := registry.Get("case.confirm"); ok {
		t.Fatal("NewP5Registry must not register confirmation tools")
	}
}

func TestP5ToolSchemasAreObjectWithoutVersionScope(t *testing.T) {
	registry := tools.NewP5Registry(tools.Deps{})
	for _, name := range tools.P5ToolNames() {
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

func TestP5VisualAssertToolRequiresCaseRunScope(t *testing.T) {
	runner := &fakeVisualRunner{}
	registry := tools.NewP5Registry(tools.Deps{P5: tools.P5Deps{VisualAssert: runner}})
	tool, ok := registry.Get(tools.ToolExecutionVisualAssert)
	if !ok {
		t.Fatalf("execution.visual_assert is not registered")
	}

	_, err := tool.Execute(context.Background(), tools.Scope{VersionID: 1001}, json.RawMessage(`{"assertionId":"6001"}`))
	if err == nil || !strings.Contains(err.Error(), tools.CodeInvalidArgument) {
		t.Fatalf("execution.visual_assert without case_run scope error = %v, want invalid_argument", err)
	}
	if runner.calls != 0 {
		t.Fatalf("visual runner calls = %d, want 0 before scope validation", runner.calls)
	}

	result, err := tool.Execute(context.Background(), tools.Scope{VersionID: 1001, CaseRunID: 8101}, json.RawMessage(`{"assertionId":"6001"}`))
	if err != nil {
		t.Fatalf("execution.visual_assert: %v", err)
	}
	if runner.calls != 1 || runner.gotAssert != 6001 || runner.gotScope.CaseRunID != 8101 {
		t.Fatalf("visual runner calls = %d assertion = %d scope = %+v", runner.calls, runner.gotAssert, runner.gotScope)
	}
	decoded, ok := result.(map[string]any)
	if !ok || decoded["classification"] != "passed" {
		t.Fatalf("visual assert result = %#v", result)
	}
}

func TestP5RegistryGroupRegistrationComposes(t *testing.T) {
	const injected = "p5.test.injected"
	tools.RegisterP5ToolGroup(nil) // nil factories are ignored defensively
	tools.RegisterP5ToolGroup(func(deps tools.Deps) []tools.Tool {
		if deps.P5.VisualAssert == nil {
			return nil
		}
		return []tools.Tool{p5TestTool{name: injected}}
	})

	if _, ok := tools.NewP5Registry(tools.Deps{}).Get(injected); ok {
		t.Fatalf("group exposed %q when NewP5Registry received zero P5 deps", injected)
	}
	runner := &fakeVisualRunner{}
	if _, ok := tools.NewP5Registry(tools.Deps{P5: tools.P5Deps{VisualAssert: runner}}).Get(injected); !ok {
		t.Fatal("NewP5Registry did not pass its deps to the registered P5 group")
	}
}

type p5TestTool struct{ name string }

func (t p5TestTool) Name() string        { return t.name }
func (t p5TestTool) Description() string { return "P5 registry test tool" }
func (t p5TestTool) Parameters() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`)
}
func (t p5TestTool) Execute(context.Context, tools.Scope, json.RawMessage) (any, error) {
	return map[string]any{"ok": true}, nil
}
