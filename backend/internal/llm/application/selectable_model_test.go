package llmapp

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
)

// TestResolveSelectableModel pins the conversation override validator directly.
// An empty override resolves the configuration's explicit default_model_id
// (models[0] is never used), an explicit name must be a selectable model of the
// default configuration, and disabled, deprecated, unknown or foreign-config
// names are non-retryable typed errors on the model_override field. This is the
// same resolver the conversation PATCH endpoint uses, so the public model
// catalog is only consistent when it advertises a subset of these models.
func TestResolveSelectableModel(t *testing.T) {
	ctx := context.Background()
	svc, repo := newChatTestService(t, &fakeChatGateway{}, nil)
	cfg := seedDefaultConfig(repo, 11)
	repo.models[11] = chatTestModel(11, 7, "default-llm", 64000, 8192)
	repo.models[12] = chatTestModel(12, 7, "alt-llm", 64000, 8192)
	// A second, active but non-default configuration: its models are never
	// selectable, which is exactly the disagreement the public catalog used to
	// expose.
	repo.configs[8] = chatTestConfig(8, false)
	repo.models[13] = chatTestModel(13, 8, "foreign-llm", 64000, 8192)

	model, err := svc.ResolveSelectableModel(ctx, "")
	if err != nil {
		t.Fatalf("ResolveSelectableModel default: %v", err)
	}
	if cfg.DefaultModelID != 11 || model.ID != 11 || model.Model != "default-llm" || model.ConfigID != 7 {
		t.Fatalf("default model = %#v, want the explicit default_model_id", model)
	}

	model, err = svc.ResolveSelectableModel(ctx, "alt-llm")
	if err != nil {
		t.Fatalf("ResolveSelectableModel explicit: %v", err)
	}
	if model.ID != 12 || model.Model != "alt-llm" {
		t.Fatalf("explicit model = %#v", model)
	}

	assertOverrideRejected := func(t *testing.T, override string) {
		t.Helper()
		_, err := svc.ResolveSelectableModel(ctx, override)
		if err == nil {
			t.Fatalf("ResolveSelectableModel(%q) succeeded", override)
		}
		apiErr := apiError(t, err)
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_argument" || apiErr.Retryable {
			t.Fatalf("ResolveSelectableModel(%q) error = %#v, want non-retryable 400 invalid_argument", override, apiErr)
		}
		if apiErr.Details["field"] != "model_override" {
			t.Fatalf("ResolveSelectableModel(%q) details = %#v, want field=model_override", override, apiErr.Details)
		}
	}

	// Disabled and deprecated rows cannot be pinned even though they belong to
	// the default configuration.
	disabled := repo.models[12]
	disabled.Status = llm.ModelStatusDisabled
	repo.models[12] = disabled
	assertOverrideRejected(t, "alt-llm")
	deprecated := repo.models[12]
	deprecated.Status = llm.ModelStatusActive
	deprecated.Deprecated = true
	repo.models[12] = deprecated
	assertOverrideRejected(t, "alt-llm")

	// A model of another active configuration and an unknown model both 400
	// with the same field instead of resolving to the config default.
	assertOverrideRejected(t, "foreign-llm")
	assertOverrideRejected(t, "missing-llm")

	// A default configuration without an explicit default_model_id is a typed
	// configuration error naming the field; it never falls back to a catalog row.
	cfg.DefaultModelID = 0
	repo.configs[7] = cfg
	if _, err := svc.ResolveSelectableModel(ctx, ""); err == nil {
		t.Fatal("ResolveSelectableModel resolved a config without default_model_id")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid || got.Retryable {
		t.Fatalf("no-default error = %#v, want non-retryable %s", got, CodeLLMConfigInvalid)
	}

	// A default_model_id whose row is disabled is a configuration error too, not
	// a silent fallback to another model.
	disabledDefault := repo.models[11]
	disabledDefault.Status = llm.ModelStatusDisabled
	repo.models[11] = disabledDefault
	cfg.DefaultModelID = 11
	repo.configs[7] = cfg
	if _, err := svc.ResolveSelectableModel(ctx, ""); err == nil {
		t.Fatal("ResolveSelectableModel resolved a disabled default model")
	} else if got := apiError(t, err); got.Code != CodeLLMConfigInvalid || got.Retryable {
		t.Fatalf("disabled-default error = %#v, want non-retryable %s", got, CodeLLMConfigInvalid)
	}
}
