package application_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
)

// svcTestTool is a minimal domain.Tool used to build registries in tests.
type svcTestTool struct {
	name   string
	schema json.RawMessage
}

// Name implements domain.Tool.
func (t *svcTestTool) Name() string { return t.name }

// Description implements domain.Tool.
func (t *svcTestTool) Description() string { return "tool " + t.name }

// InputSchema implements domain.Tool.
func (t *svcTestTool) InputSchema() json.RawMessage { return t.schema }

// Execute implements domain.Tool.
func (t *svcTestTool) Execute(_ context.Context, _ json.RawMessage) (domain.ToolResult, error) {
	return domain.ToolResult{Content: t.name}, nil
}

func TestToolCatalogInfos(t *testing.T) {
	tools := []domain.Tool{
		&svcTestTool{name: "zeta", schema: json.RawMessage(`{"type":"object"}`)},
		&svcTestTool{name: "alpha", schema: json.RawMessage(`{"type":"object"}`)},
		&svcTestTool{name: "mike", schema: json.RawMessage(`{"type":"object"}`)},
		&svcTestTool{name: "bravo", schema: json.RawMessage(`{"type":"object"}`)},
		&svcTestTool{name: "echo", schema: json.RawMessage(`{"type":"object"}`)},
	}
	catalog := application.NewToolCatalog(application.NewToolRegistry(tools...))

	infos := catalog.Infos()
	if len(infos) != 5 {
		t.Fatalf("len(Infos)=%d want 5", len(infos))
	}
	names := make([]string, len(infos))
	for i, info := range infos {
		names[i] = info.Name
		if info.Description != "tool "+info.Name {
			t.Fatalf("Infos[%d].Description=%q want the registered description", i, info.Description)
		}
		if string(info.Schema) != `{"type":"object"}` {
			t.Fatalf("Infos[%d].Schema=%s want the registered schema", i, info.Schema)
		}
	}
	want := []string{"alpha", "bravo", "echo", "mike", "zeta"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("Infos order=%v want sorted %v", names, want)
	}
}

func TestToolCatalogInfosWithoutRegistry(t *testing.T) {
	if infos := application.NewToolCatalog(nil).Infos(); len(infos) != 0 {
		t.Fatalf("len(Infos)=%d want 0 for a nil registry", len(infos))
	}
}
