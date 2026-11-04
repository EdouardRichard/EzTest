package llmapi_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/clock"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
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

// catalogRepo is the minimal in-memory llm.Repository the cross-layer
// consistency test needs: the real catalog service sits behind the HTTP handler
// so the advertised set can be checked against the exact resolver the PATCH
// endpoint uses. The embedded interface keeps the unused admin write methods out
// of the test (calling one panics).
type catalogRepo struct {
	llm.Repository
	configs []llm.Config
	models  []llm.Model
}

func (r *catalogRepo) ListConfigs(context.Context) ([]llm.Config, error) {
	return r.configs, nil
}

func (r *catalogRepo) GetConfig(_ context.Context, id int64) (llm.Config, error) {
	for _, cfg := range r.configs {
		if cfg.ID == id {
			return cfg, nil
		}
	}
	return llm.Config{}, llm.ErrConfigNotFound
}

func (r *catalogRepo) ListModels(context.Context) ([]llm.Model, error) {
	return r.models, nil
}

func (r *catalogRepo) ListModelsByConfig(_ context.Context, configID int64) ([]llm.Model, error) {
	items := make([]llm.Model, 0, len(r.models))
	for _, model := range r.models {
		if model.ConfigID == configID {
			items = append(items, model)
		}
	}
	return items, nil
}

// TestPublicModelCatalogMatchesOverrideValidator pins the cross-layer invariant
// behind the conversation model picker: every model the public catalog
// advertises must be accepted by the same resolver the conversation PATCH uses.
// Several active LLM configurations are allowed (only is_default is unique),
// but only the default configuration's models are selectable, so advertising
// another active configuration's models would make the selector offer options
// that every PATCH rejects.
func TestPublicModelCatalogMatchesOverrideValidator(t *testing.T) {
	ctx := context.Background()

	defaultConfig := sampleConfig()
	defaultConfig.DefaultModelID = 201
	otherConfig := sampleConfig()
	otherConfig.ID = 102
	otherConfig.Name = "Secondary"
	otherConfig.IsDefault = false
	otherConfig.DefaultModelID = 301

	defaultModel := sampleModel()
	visionModel := sampleModel()
	visionModel.ID = 202
	visionModel.Model = "vision-plus"
	visionModel.DisplayName = "Vision Plus"
	disabledModel := sampleModel()
	disabledModel.ID = 203
	disabledModel.Model = "switched-off-llm"
	disabledModel.Status = llm.ModelStatusDisabled
	foreignModel := sampleModel()
	foreignModel.ID = 301
	foreignModel.ConfigID = 102
	foreignModel.Model = "other-llm"

	repo := &catalogRepo{
		configs: []llm.Config{defaultConfig, otherConfig},
		models:  []llm.Model{defaultModel, visionModel, disabledModel, foreignModel},
	}
	ids, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("NewSnowflake: %v", err)
	}
	service := llmapp.New(repo, nil, ids, clock.System{}, nil, nil)
	handler := llmapi.New(service)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/llm/models", handler.ListPublicModels)

	recorder := call(t, mux, http.MethodGet, "/api/llm/models", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	advertised := make(map[string]bool, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("item = %#v, want an object", raw)
		}
		name, ok := item["model"].(string)
		if !ok {
			t.Fatalf("item = %#v, want a model name", item)
		}
		advertised[name] = true
		if _, err := service.ResolveSelectableModel(ctx, name); err != nil {
			t.Fatalf("catalog advertised %q but the override resolver rejects it: %v", name, err)
		}
	}

	if len(advertised) != 2 || !advertised["gpt-4o"] || !advertised["vision-plus"] {
		t.Fatalf("advertised models = %#v, want exactly the default config's selectable models", advertised)
	}
	// The excluded cross-config model is not merely hidden: the override
	// resolver rejects it, which is why advertising it would 400 every PATCH.
	if _, err := service.ResolveSelectableModel(ctx, foreignModel.Model); err == nil {
		t.Fatal("cross-config model was unexpectedly accepted by the override resolver")
	}
}

// TestModelCatalogOnlyListsVisionModels covers R-E2-2 (spec §4.25): the
// conversation model selector is backed by this catalog, so a model without
// supports_vision=true must not be advertised at all. Keeping it out of the
// list is what prevents the selector from offering a model that the image
// send gate would reject.
func TestModelCatalogOnlyListsVisionModels(t *testing.T) {
	stub := &stubService{}
	active := sampleConfig()
	active.DefaultModelID = 201
	stub.configs = []llm.Config{active}

	vision := sampleModel()
	textOnly := sampleModel()
	textOnly.ID = 202
	textOnly.Model = "text-only"
	textOnly.DisplayName = "Text Only"
	textOnly.SupportsVision = false
	textOnly.Capabilities.SupportsVision = false
	stub.models = []llm.Model{vision, textOnly}

	handler := llmapi.New(stub)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/llm/models", handler.ListPublicModels)

	recorder := call(t, mux, http.MethodGet, "/api/llm/models", "", nil)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	items := dataList(t, recorder)
	if len(items) != 1 {
		t.Fatalf("items = %#v, want only the vision-capable model", items)
	}
	item := items[0].(map[string]any)
	if item["model"] != "gpt-4o" {
		t.Fatalf("item = %#v", item)
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
