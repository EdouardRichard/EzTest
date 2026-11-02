package llmapi_test

import (
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
)

// TestModelCatalogOnlyEnabledConfigsAndSelectableModels pins the 0.2.1 public
// model catalog: only models owned by an active LLM configuration that are
// themselves enabled and not deprecated are listed. The response carries the
// capability set and the config's default marker, but never credentials or the
// gateway base URL.
func TestModelCatalogOnlyEnabledConfigsAndSelectableModels(t *testing.T) {
	stub := &stubService{}
	active := sampleConfig()
	active.DefaultModelID = 201
	active.APIKey = secretKey
	disabledConfig := sampleConfig()
	disabledConfig.ID = 102
	disabledConfig.Name = "Retired gateway"
	disabledConfig.Status = llm.StatusDisabled
	disabledConfig.DefaultModelID = 202
	stub.configs = []llm.Config{active, disabledConfig}

	live := sampleModel()
	deprecated := sampleModel()
	deprecated.ID = 203
	deprecated.Model = "retired-llm"
	deprecated.Deprecated = true
	disabled := sampleModel()
	disabled.ID = 204
	disabled.Model = "switched-off-llm"
	disabled.Status = llm.ModelStatusDisabled
	foreign := sampleModel()
	foreign.ID = 202
	foreign.ConfigID = 102
	foreign.Model = "foreign-llm"
	stub.models = []llm.Model{live, deprecated, disabled, foreign}

	handler := llmapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/llm/models", handler.ListPublicModels)

	recorder := call(t, mux, http.MethodGet, "/api/llm/models", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want the one selectable model", items)
	}
	item := items[0].(map[string]any)
	if item["id"] != "201" || item["config_id"] != "101" || item["config_name"] != "Primary" ||
		item["model"] != "gpt-4o" || item["display_name"] != "gpt-4o" {
		t.Fatalf("item = %#v", item)
	}
	if item["default"] != true || item["deprecated"] != false {
		t.Fatalf("flags = %#v", item)
	}
	capabilities, ok := item["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities = %#v", item["capabilities"])
	}
	for _, field := range []string{"supports_tools", "supports_vision", "supports_reasoning", "supports_prompt_cache", "context_window"} {
		if _, ok := capabilities[field]; !ok {
			t.Fatalf("capabilities missing %q: %#v", field, capabilities)
		}
	}
	if capabilities["supports_vision"] != true || capabilities["context_window"] != float64(128000) {
		t.Fatalf("capabilities = %#v", capabilities)
	}
	for _, forbidden := range []string{"api_key", "base_url", "headers"} {
		if _, ok := item[forbidden]; ok {
			t.Fatalf("public model leaked %q: %#v", forbidden, item)
		}
	}
}

// TestModelCatalogEmptyKeepsArrayShape pins the envelope: no selectable
// model is an empty data array, never null.
func TestModelCatalogEmptyKeepsArrayShape(t *testing.T) {
	stub := &stubService{}
	handler := llmapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/llm/models", handler.ListPublicModels)

	recorder := call(t, mux, http.MethodGet, "/api/llm/models", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if data := dataList(t, recorder); len(data) != 0 {
		t.Fatalf("data = %#v, want []", data)
	}
}
