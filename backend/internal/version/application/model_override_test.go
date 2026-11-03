package versionapp_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeConversationModelCatalog stands in for the default LLM configuration's
// selectable model catalog (the llm application service in production).
type fakeConversationModelCatalog struct {
	defaultModel string
	selectable   map[string]string
	// visionless marks selectable models whose supports_vision capability is
	// false; the vision send gate reads it through the same catalog resolution.
	visionless map[string]bool
	err        error
	calls      []string
}

func (f *fakeConversationModelCatalog) ResolveConversationModel(_ context.Context, modelOverride string) (versionapp.ConversationModel, error) {
	f.calls = append(f.calls, modelOverride)
	if f.err != nil {
		return versionapp.ConversationModel{}, f.err
	}
	model := func(name string) versionapp.ConversationModel {
		return versionapp.ConversationModel{Model: name, SupportsVision: !f.visionless[name]}
	}
	if modelOverride == "" {
		if f.defaultModel == "" {
			return versionapp.ConversationModel{}, platform.Invalid("model_override", "no default model is configured")
		}
		return model(f.defaultModel), nil
	}
	if name, ok := f.selectable[modelOverride]; ok {
		return model(name), nil
	}
	return versionapp.ConversationModel{}, platform.Invalid("model_override", "must name a model of the default llm configuration")
}

func TestCreateConversationWithModelOverride(t *testing.T) {
	env := newConversationEnv(t)
	catalog := &fakeConversationModelCatalog{
		defaultModel: "gpt-4o",
		selectable:   map[string]string{"vision-plus": "vision-plus"},
	}
	env.svc.SetModelCatalog(catalog)

	override := stringPtr("vision-plus")
	view, err := env.svc.CreateWithModel(requestContext(), env.versionID, "clarification", "", override, 42)
	if err != nil {
		t.Fatalf("CreateWithModel: %v", err)
	}
	if view.ModelOverride == nil || *view.ModelOverride != "vision-plus" {
		t.Fatalf("model_override = %#v", view.ModelOverride)
	}
	if view.EffectiveModel == nil || *view.EffectiveModel != "vision-plus" {
		t.Fatalf("effective_model = %#v", view.EffectiveModel)
	}
	stored := env.repo.conversations[view.ID]
	if stored.ModelOverride == nil || *stored.ModelOverride != "vision-plus" {
		t.Fatalf("stored conversation = %#v", stored)
	}

	// Without an override the conversation follows the config default model.
	plain, err := env.svc.Create(requestContext(), env.versionID, "clarification", "", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if plain.ModelOverride != nil {
		t.Fatalf("plain model_override = %#v", plain.ModelOverride)
	}
	if plain.EffectiveModel == nil || *plain.EffectiveModel != "gpt-4o" {
		t.Fatalf("plain effective_model = %#v", plain.EffectiveModel)
	}

	// A model outside the selectable catalog is rejected before the row exists.
	unknown := stringPtr("unknown-llm")
	if _, err := env.svc.CreateWithModel(requestContext(), env.versionID, "clarification", "", unknown, 42); err == nil {
		t.Fatal("unknown model override accepted")
	} else if got := apiError(t, err); got.Status != http.StatusBadRequest ||
		got.Details["field"] != "model_override" {
		t.Fatalf("error = %#v", got)
	}
	if len(env.repo.conversations) != 2 {
		t.Fatalf("rejected create persisted a conversation: %#v", env.repo.conversations)
	}
}

func TestSetConversationModelOverride(t *testing.T) {
	env := newConversationEnv(t)
	catalog := &fakeConversationModelCatalog{
		defaultModel: "gpt-4o",
		selectable:   map[string]string{"vision-plus": "vision-plus"},
	}
	env.svc.SetModelCatalog(catalog)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	override := stringPtr("vision-plus")
	view, err := env.svc.SetModelOverride(requestContext(), env.versionID, conversation.ID, override)
	if err != nil {
		t.Fatalf("SetModelOverride: %v", err)
	}
	if view.ID != conversation.ID || view.ModelOverride == nil || *view.ModelOverride != "vision-plus" ||
		view.EffectiveModel == nil || *view.EffectiveModel != "vision-plus" {
		t.Fatalf("view = %#v", view)
	}
	stored := env.repo.conversations[conversation.ID]
	if stored.ModelOverride == nil || *stored.ModelOverride != "vision-plus" {
		t.Fatalf("stored = %#v", stored)
	}

	// model_override=null clears the override and falls back to the config
	// default; the model is not silently switched anywhere else.
	cleared, err := env.svc.SetModelOverride(requestContext(), env.versionID, conversation.ID, nil)
	if err != nil {
		t.Fatalf("SetModelOverride clear: %v", err)
	}
	if cleared.ModelOverride != nil || cleared.EffectiveModel == nil || *cleared.EffectiveModel != "gpt-4o" {
		t.Fatalf("cleared view = %#v", cleared)
	}
	if stored := env.repo.conversations[conversation.ID]; stored.ModelOverride != nil {
		t.Fatalf("stored override after clear = %#v", stored.ModelOverride)
	}

	// An invalid model is rejected and never replaces the stored override.
	unknown := stringPtr("unknown-llm")
	if _, err := env.svc.SetModelOverride(requestContext(), env.versionID, conversation.ID, unknown); err == nil {
		t.Fatal("unknown model override accepted")
	} else if got := apiError(t, err); got.Status != http.StatusBadRequest {
		t.Fatalf("error = %#v", got)
	}
	if stored := env.repo.conversations[conversation.ID]; stored.ModelOverride != nil {
		t.Fatalf("rejected override changed the row: %#v", stored.ModelOverride)
	}

	// A conversation of another version is a 404, not a cross-version write.
	if _, err := env.svc.SetModelOverride(requestContext(), env.versionID+1, conversation.ID, override); err == nil {
		t.Fatal("cross-version override succeeded")
	} else if got := apiError(t, err); got.Status != http.StatusNotFound {
		t.Fatalf("cross-version error = %#v", got)
	}
}

func TestConversationViewsExposeEffectiveModel(t *testing.T) {
	env := newConversationEnv(t)
	catalog := &fakeConversationModelCatalog{
		defaultModel: "gpt-4o",
		selectable:   map[string]string{"vision-plus": "vision-plus"},
	}
	env.svc.SetModelCatalog(catalog)

	defaulted, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Default", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	override := stringPtr("vision-plus")
	overridden, err := env.svc.CreateWithModel(requestContext(), env.versionID, "clarification", "Override", override, 42)
	if err != nil {
		t.Fatalf("CreateWithModel: %v", err)
	}

	items, _, err := env.svc.List(requestContext(), env.versionID, "", 10, nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	byID := make(map[int64]versionapp.ConversationView, len(items))
	for _, item := range items {
		byID[item.ID] = item
	}
	if got := byID[defaulted.ID].EffectiveModel; got == nil || *got != "gpt-4o" {
		t.Fatalf("defaulted effective_model = %#v", got)
	}
	if got := byID[overridden.ID].EffectiveModel; got == nil || *got != "vision-plus" {
		t.Fatalf("overridden effective_model = %#v", got)
	}

	detail, err := env.svc.Get(requestContext(), env.versionID, overridden.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if detail.ModelOverride == nil || *detail.ModelOverride != "vision-plus" ||
		detail.EffectiveModel == nil || *detail.EffectiveModel != "vision-plus" {
		t.Fatalf("detail = %#v", detail)
	}
}

func TestConversationMessageViewExposesActualModel(t *testing.T) {
	env := newConversationEnv(t)
	conversation, err := env.svc.Create(requestContext(), env.versionID, "clarification", "Chat", 42)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	env.repo.messages[conversation.ID] = []version.Message{
		{
			ID: 9001, ConversationID: conversation.ID, Role: "assistant",
			Parts:       []version.MessagePart{{Type: "text", Text: "drafted"}},
			ActualModel: "gpt-4o", CreatedAt: testTime,
		},
		{
			ID: 9002, ConversationID: conversation.ID, Role: "user",
			Parts:     []version.MessagePart{{Type: "text", Text: "hello"}},
			CreatedAt: testTime,
		},
	}

	views, _, err := env.svc.Messages(requestContext(), env.versionID, conversation.ID, 0, 10)
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("views = %#v", views)
	}
	if views[0].ActualModel == nil || *views[0].ActualModel != "gpt-4o" {
		t.Fatalf("assistant actual_model = %#v", views[0].ActualModel)
	}
	if views[1].ActualModel != nil {
		t.Fatalf("user actual_model = %#v, want nil", views[1].ActualModel)
	}
}
