package tools

import (
	"encoding/json"
	"strings"
	"testing"

	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
)

func TestP1ToolNamesCoversSpecToolSet(t *testing.T) {
	names := P1ToolNames()
	want := []string{
		ToolContextVersionPack,
		ToolContextTaskPack,
		ToolContextTeamRoomRead,
		ToolContextSummaryUpdate,
		ToolRequirementRead,
		ToolRequirementDraftCreate,
		ToolRequirementDraftUpdate,
		ToolRequirementRelationPropose,
		ToolRequirementReopenPropose,
		ToolAttachmentList,
		ToolAttachmentReadText,
		ToolAttachmentImageRef,
		ToolSUTList,
		ToolSUTPageList,
	}
	if len(names) != len(want) {
		t.Fatalf("tool names = %d %v, want %d", len(names), names, len(want))
	}
	for i, name := range want {
		if names[i] != name {
			t.Fatalf("tool name[%d] = %q, want %q", i, names[i], name)
		}
	}
}

func TestP1SchemasDoNotAcceptModelSuppliedScope(t *testing.T) {
	registry := NewP1Registry(Deps{})
	definitions := registry.Definitions(P1ToolNames())
	if len(definitions) != len(P1ToolNames()) {
		t.Fatalf("definitions = %d", len(definitions))
	}
	for _, definition := range definitions {
		raw := strings.ToLower(string(definition.Parameters))
		if strings.Contains(raw, "version_id") || strings.Contains(raw, "versionid") {
			t.Fatalf("tool %s schema leaks a version scope parameter: %s", definition.Name, definition.Parameters)
		}
		var schema map[string]any
		if err := json.Unmarshal(definition.Parameters, &schema); err != nil {
			t.Fatalf("tool %s schema is not JSON: %v", definition.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("tool %s schema type = %#v", definition.Name, schema["type"])
		}
		if schema["additionalProperties"] != false {
			t.Fatalf("tool %s schema must reject unknown properties: %#v", definition.Name, schema["additionalProperties"])
		}
		if strings.TrimSpace(definition.Description) == "" {
			t.Fatalf("tool %s has no description", definition.Name)
		}
	}
}

func TestRegistryDefinitionsPreserveAllowedOrderAndSkipUnknown(t *testing.T) {
	registry := NewP1Registry(Deps{})
	definitions := registry.Definitions([]string{
		ToolRequirementRead,
		"reserved.p2.tool",
		ToolSUTList,
	})
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v", definitions)
	}
	if definitions[0].Name != ToolRequirementRead || definitions[1].Name != ToolSUTList {
		t.Fatalf("order = %#v", definitions)
	}
}

func TestRegistryExecuteRejectsUnknownTool(t *testing.T) {
	registry := NewP1Registry(Deps{})
	_, err := registry.Execute(t.Context(), Scope{VersionID: 10}, "requirement.close", json.RawMessage(`{}`))
	toolErr := asToolError(t, err)
	if toolErr.Code != CodeInvalidArgument {
		t.Fatalf("code = %q", toolErr.Code)
	}
}

func TestRegistryRejectsUnknownParametersIncludingVersionID(t *testing.T) {
	registry := NewP1Registry(Deps{})
	for _, name := range []string{ToolRequirementRead, ToolContextTaskPack, ToolSUTList} {
		_, err := registry.Execute(t.Context(), Scope{VersionID: 10}, name, json.RawMessage(`{"version_id":"99"}`))
		toolErr := asToolError(t, err)
		if toolErr.Code != CodeInvalidArgument {
			t.Fatalf("tool %s code = %q, want invalid_argument", name, toolErr.Code)
		}
	}
}

func asToolError(t *testing.T, err error) *Error {
	t.Helper()
	if err == nil {
		t.Fatal("expected a tool error, got nil")
	}
	var toolErr *Error
	if !errorsAs(err, &toolErr) {
		t.Fatalf("error %v is not a tool error", err)
	}
	return toolErr
}

// errorsAs keeps the test file free of an errors import cycle in helpers.
func errorsAs(err error, target **Error) bool {
	for err != nil {
		if typed, ok := err.(*Error); ok {
			*target = typed
			return true
		}
		unwrapper, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrapper.Unwrap()
	}
	return false
}

var _ = llmapp.ToolDefinition{}
