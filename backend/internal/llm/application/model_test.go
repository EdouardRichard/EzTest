package llmapp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// capableModel returns a catalog model declaring every P4-T3 capability.
func capableModel(id, configID int64, name string) llm.Model {
	caps := llm.Capabilities{
		SupportsTools:       true,
		SupportsVision:      true,
		SupportsReasoning:   true,
		SupportsPromptCache: true,
		ContextWindow:       128000,
	}
	model := chatTestModel(id, configID, name, 128000, 16384)
	model.SupportsTools = true
	model.SupportsVision = true
	model.Capabilities = caps
	return model
}

// seedDefaultConfig stores one default config whose explicit default model is
// defaultModelID. A zero defaultModelID leaves the config without a default.
func seedDefaultConfig(repo *fakeRepo, defaultModelID int64) llm.Config {
	cfg := chatTestConfig(7, true)
	cfg.DefaultModelID = defaultModelID
	repo.configs[7] = cfg
	return cfg
}

func TestDefaultModelRequired(t *testing.T) {
	ctx := context.Background()
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	svc, repo := newChatTestService(t, gateway, nil)
	cfg := seedDefaultConfig(repo, 0)
	repo.models[11] = chatTestModel(11, 7, "fake-llm", 64000, 8192)
	repo.models[12] = chatTestModel(12, 7, "fake-llm-mini", 32000, 4096)

	// An empty model must resolve the configured default; without one it is a
	// typed configuration error, never models[0].
	_, err := svc.ResolveModel(ctx, 0, "")
	apiErr := apiError(t, err)
	if apiErr.Status != 400 || apiErr.Code != CodeLLMConfigInvalid || apiErr.Retryable {
		t.Fatalf("error = %#v, want non-retryable %s", apiErr, CodeLLMConfigInvalid)
	}
	if !strings.Contains(strings.ToLower(apiErr.Message), "default model") {
		t.Fatalf("message = %q, want an actionable default-model error", apiErr.Message)
	}

	// The explicit config id takes the same path.
	if _, err := svc.ResolveModel(ctx, cfg.ID, ""); err == nil {
		t.Fatal("ResolveModel with an explicit config succeeded without a default model")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid {
		t.Fatalf("error = %#v", got)
	}

	// Chat uses the same resolution and must not reach the gateway.
	if _, err := svc.Chat(ctx, ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "hi"}}}); err == nil {
		t.Fatal("Chat succeeded without a default model")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid {
		t.Fatalf("chat error = %#v", got)
	}
	if gateway.callCount() != 0 {
		t.Fatalf("gateway calls = %d, want 0", gateway.callCount())
	}

	// An explicit model still wins when the config has no default.
	info, err := svc.ResolveModel(ctx, 0, "fake-llm-mini")
	if err != nil {
		t.Fatalf("ResolveModel explicit model: %v", err)
	}
	if info.Model != "fake-llm-mini" || info.ModelID != 12 {
		t.Fatalf("info = %#v, want the explicit model", info)
	}
}

func TestCapabilityValidation(t *testing.T) {
	ctx := context.Background()

	cases := []struct {
		name     string
		required ModelRequirements
		want     string
	}{
		{"tools", ModelRequirements{Tools: true}, "tools"},
		{"vision", ModelRequirements{Vision: true}, "vision"},
		{"reasoning", ModelRequirements{Reasoning: true}, "reasoning"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, repo := newChatTestService(t, &fakeChatGateway{}, nil)
			seedDefaultConfig(repo, 11)
			repo.models[11] = chatTestModel(11, 7, "plain-llm", 64000, 8192)

			_, err := svc.ResolveModel(ctx, 0, "", tc.required)
			apiErr := apiError(t, err)
			if apiErr.Status != 400 || apiErr.Code != CodeLLMCapabilityMismatch || apiErr.Retryable {
				t.Fatalf("error = %#v, want non-retryable %s", apiErr, CodeLLMCapabilityMismatch)
			}
			if !strings.Contains(strings.ToLower(apiErr.Message), tc.want) {
				t.Fatalf("message = %q, want it to name %q", apiErr.Message, tc.want)
			}
		})
	}

	t.Run("capable model passes", func(t *testing.T) {
		svc, repo := newChatTestService(t, &fakeChatGateway{}, nil)
		seedDefaultConfig(repo, 11)
		repo.models[11] = capableModel(11, 7, "capable-llm")

		info, err := svc.ResolveModel(ctx, 0, "", ModelRequirements{Tools: true, Vision: true, Reasoning: true})
		if err != nil {
			t.Fatalf("ResolveModel: %v", err)
		}
		if !info.Capabilities.SupportsTools || !info.Capabilities.SupportsVision ||
			!info.Capabilities.SupportsReasoning || !info.Capabilities.SupportsPromptCache ||
			info.Capabilities.ContextWindow != 128000 {
			t.Fatalf("capabilities = %#v", info.Capabilities)
		}
	})

	t.Run("thinking enabled requires reasoning support", func(t *testing.T) {
		svc, repo := newChatTestService(t, &fakeChatGateway{}, nil)
		cfg := seedDefaultConfig(repo, 11)
		cfg.ThinkingType = llm.ThinkingTypeEnabled
		repo.configs[7] = cfg
		repo.models[11] = chatTestModel(11, 7, "plain-llm", 64000, 8192)

		if _, err := svc.ResolveModel(ctx, 0, ""); err == nil {
			t.Fatal("thinking_type=enabled accepted a model without reasoning support")
		} else if got := apiError(t, err); got.Code != CodeLLMCapabilityMismatch {
			t.Fatalf("error = %#v", got)
		}

		repo.models[11] = capableModel(11, 7, "capable-llm")
		info, err := svc.ResolveModel(ctx, 0, "")
		if err != nil {
			t.Fatalf("ResolveModel reasoning-capable model: %v", err)
		}
		if !info.Capabilities.SupportsReasoning {
			t.Fatalf("capabilities = %#v", info.Capabilities)
		}
	})
}

func TestProfileModelOverride(t *testing.T) {
	ctx := context.Background()
	gateway := &fakeChatGateway{response: ChatResponse{Content: "ok"}}
	svc, repo := newChatTestService(t, gateway, nil)
	seedDefaultConfig(repo, 11)
	repo.models[11] = chatTestModel(11, 7, "default-llm", 64000, 8192)
	repo.models[12] = chatTestModel(12, 7, "profile-llm", 128000, 16384)

	// The agent passes agent_profile.model as the explicit model: it wins over
	// the config default model.
	info, err := svc.ResolveModel(ctx, 0, "profile-llm")
	if err != nil {
		t.Fatalf("ResolveModel: %v", err)
	}
	if info.Model != "profile-llm" || info.ModelID != 12 || info.ConfigID != 7 {
		t.Fatalf("info = %#v, want the profile override", info)
	}

	if _, err := svc.Chat(ctx, ChatRequest{
		Model:    "profile-llm",
		Messages: []ChatMessage{{Role: "user", Content: "hi"}},
	}); err != nil {
		t.Fatalf("Chat: %v", err)
	}
	if call := gateway.lastCall(t); call.req.Model != "profile-llm" {
		t.Fatalf("chat model = %q, want the profile override", call.req.Model)
	}

	// An empty profile model falls back to the config's default model.
	info, err = svc.ResolveModel(ctx, 0, "")
	if err != nil {
		t.Fatalf("ResolveModel default: %v", err)
	}
	if info.Model != "default-llm" || info.ModelID != 11 {
		t.Fatalf("info = %#v, want the config default model", info)
	}
}

// TestConfigThinkingSettings pins the 0.2.1 config read/write fields: create
// applies the schema defaults, explicit values survive the service and the
// repository, invalid enums are rejected, and default_model_id may only name a
// model of the same configuration.
func TestConfigThinkingSettings(t *testing.T) {
	ctx := context.Background()
	svc, repo, _, audit, _ := newTestService(t)

	input := validCreate()
	input.ThinkingType = strPtr(llm.ThinkingTypeEnabled)
	input.ReasoningEffort = strPtr(llm.ReasoningEffortHigh)
	cfg, err := svc.CreateConfig(ctx, input, 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	if cfg.ThinkingType != llm.ThinkingTypeEnabled || cfg.ReasoningEffort != llm.ReasoningEffortHigh {
		t.Fatalf("thinking settings = %q/%q", cfg.ThinkingType, cfg.ReasoningEffort)
	}
	stored, err := repo.GetConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.ThinkingType != llm.ThinkingTypeEnabled || stored.ReasoningEffort != llm.ReasoningEffortHigh {
		t.Fatalf("stored thinking settings = %q/%q", stored.ThinkingType, stored.ReasoningEffort)
	}
	if len(audit.events) == 0 || audit.events[0].Metadata["thinking_type"] != llm.ThinkingTypeEnabled {
		t.Fatalf("audit = %#v", audit.events)
	}

	defaultInput := validCreate()
	defaultInput.Name = strPtr("Defaults")
	defaultCfg, err := svc.CreateConfig(ctx, defaultInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig defaults: %v", err)
	}
	if defaultCfg.ThinkingType != llm.ThinkingTypeAuto || defaultCfg.ReasoningEffort != llm.ReasoningEffortNone {
		t.Fatalf("defaults = %q/%q", defaultCfg.ThinkingType, defaultCfg.ReasoningEffort)
	}

	for _, field := range []string{"thinking_type", "reasoning_effort"} {
		bad := validCreate()
		bad.Name = strPtr("Bad " + field)
		if field == "thinking_type" {
			bad.ThinkingType = strPtr("sometimes")
		} else {
			bad.ReasoningEffort = strPtr("extreme")
		}
		if _, err := svc.CreateConfig(ctx, bad, 1); err == nil {
			t.Fatalf("%s accepted an unknown value", field)
		} else if got := apiError(t, err).Status; got != http.StatusBadRequest {
			t.Fatalf("%s status = %d, want 400", field, got)
		}
	}

	model, err := svc.AddModel(ctx, cfg.ID, "thinking-model", 128000, 8192, true, false, 1)
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	defaultModelID := model.ID
	updated, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{DefaultModelID: &defaultModelID}, 1)
	if err != nil {
		t.Fatalf("UpdateConfig default model: %v", err)
	}
	if updated.DefaultModelID != model.ID {
		t.Fatalf("default_model_id = %d, want %d", updated.DefaultModelID, model.ID)
	}

	otherInput := validCreate()
	otherInput.Name = strPtr("Other")
	other, err := svc.CreateConfig(ctx, otherInput, 1)
	if err != nil {
		t.Fatalf("CreateConfig other: %v", err)
	}
	if _, err := svc.UpdateConfig(ctx, other.ID, ConfigInput{DefaultModelID: &defaultModelID}, 1); err == nil {
		t.Fatal("default model of another config was accepted")
	} else if got := apiError(t, err).Status; got != http.StatusBadRequest {
		t.Fatalf("cross-config default model status = %d, want 400", got)
	}

	disabled, err := svc.UpdateConfig(ctx, cfg.ID, ConfigInput{
		ThinkingType:    strPtr(llm.ThinkingTypeDisabled),
		ReasoningEffort: strPtr(llm.ReasoningEffortNone),
	}, 1)
	if err != nil {
		t.Fatalf("UpdateConfig thinking: %v", err)
	}
	if disabled.ThinkingType != llm.ThinkingTypeDisabled || disabled.ReasoningEffort != llm.ReasoningEffortNone {
		t.Fatalf("updated thinking settings = %q/%q", disabled.ThinkingType, disabled.ReasoningEffort)
	}
}

func TestDeprecatedModelExcluded(t *testing.T) {
	ctx := context.Background()
	svc, repo := newChatTestService(t, &fakeChatGateway{}, nil)

	deprecated := chatTestModel(11, 7, "retired-llm", 64000, 8192)
	deprecated.Deprecated = true
	repo.models[11] = deprecated
	repo.models[12] = chatTestModel(12, 7, "live-llm", 64000, 8192)
	seedDefaultConfig(repo, 11)

	// A deprecated model must never be selected as the default...
	if _, err := svc.ResolveModel(ctx, 0, ""); err == nil {
		t.Fatal("deprecated default model was selected")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid {
		t.Fatalf("error = %#v", got)
	}
	// ...nor through an explicit override.
	if _, err := svc.ResolveModel(ctx, 0, "retired-llm"); err == nil {
		t.Fatal("deprecated explicit model was selected")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid {
		t.Fatalf("error = %#v", got)
	}
	info, err := svc.ResolveModel(ctx, 0, "live-llm")
	if err != nil || info.Model != "live-llm" {
		t.Fatalf("ResolveModel live model = %#v, %v", info, err)
	}

	// Refreshing the catalog keeps retired models and marks them deprecated
	// instead of silently deleting them.
	svc, repo, client, _, _ := newTestService(t)
	cfg, err := svc.CreateConfig(ctx, validCreate(), 1)
	if err != nil {
		t.Fatalf("CreateConfig: %v", err)
	}
	client.models = []RemoteModel{{ID: "gpt-4o"}, {ID: "gpt-4o-mini"}}
	if _, err := svc.RefreshModels(ctx, cfg.ID, 1); err != nil {
		t.Fatalf("first RefreshModels: %v", err)
	}
	client.models = []RemoteModel{{ID: "gpt-4o-mini"}}
	refreshed, err := svc.RefreshModels(ctx, cfg.ID, 1)
	if err != nil {
		t.Fatalf("second RefreshModels: %v", err)
	}
	if len(refreshed) != 2 {
		t.Fatalf("refreshed models = %#v, want the retired model kept", refreshed)
	}
	retired, live := 0, 0
	for _, model := range refreshed {
		switch model.Model {
		case "gpt-4o":
			retired++
			if !model.Deprecated {
				t.Fatalf("retired model = %#v, want deprecated=true", model)
			}
		case "gpt-4o-mini":
			live++
			if model.Deprecated {
				t.Fatalf("live model = %#v, want deprecated=false", model)
			}
		}
	}
	if retired != 1 || live != 1 {
		t.Fatalf("refreshed models = %#v", refreshed)
	}
}
