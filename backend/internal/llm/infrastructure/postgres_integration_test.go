//go:build integration

package llminfra_test

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func newIDs(t *testing.T) *id.Snowflake {
	t.Helper()
	generator, err := id.NewSnowflake(1)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	return generator
}

func ptr[T any](value T) *T { return &value }

func createConfig(t *testing.T, repo *llminfra.Repository, ids *id.Snowflake, name string, isDefault bool) llm.Config {
	t.Helper()
	return createConfigWithProtocol(t, repo, ids, name, llm.ProtocolOpenAI, isDefault)
}

func createConfigWithProtocol(t *testing.T, repo *llminfra.Repository, ids *id.Snowflake, name, protocol string, isDefault bool) llm.Config {
	t.Helper()
	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	cfg := llm.Config{
		ID:             ids.Next(),
		Name:           name,
		Provider:       llm.ProviderOpenAI,
		Protocol:       protocol,
		BaseURL:        "http://127.0.0.1:13334/v1",
		APIKey:         "sk-plain-text",
		Headers:        map[string]string{"X-Org": "team-a"},
		TimeoutSeconds: 60,
		MaxRetries:     2,
		IsDefault:      isDefault,
		Status:         llm.StatusActive,
		CreatedBy:      ptr(int64(42)),
		CreatedAt:      at,
		UpdatedAt:      at,
	}
	created, err := repo.CreateConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("CreateConfig(%s): %v", name, err)
	}
	return created
}

func TestRepositoryConfigLifecycle(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := llminfra.NewRepository(pool)
	ids := newIDs(t)
	ctx := context.Background()

	first := createConfig(t, repo, ids, "Primary", true)

	stored, err := repo.GetConfig(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetConfig: %v", err)
	}
	if stored.APIKey != "sk-plain-text" {
		t.Fatalf("api_key must be stored in plain text, got %q", stored.APIKey)
	}
	if stored.Headers["X-Org"] != "team-a" {
		t.Fatalf("headers = %#v", stored.Headers)
	}
	if stored.CreatedBy == nil || *stored.CreatedBy != 42 {
		t.Fatalf("created_by = %v", stored.CreatedBy)
	}
	if !stored.CreatedAt.Equal(first.CreatedAt) || !stored.UpdatedAt.Equal(first.UpdatedAt) {
		t.Fatalf("timestamps = %v/%v", stored.CreatedAt, stored.UpdatedAt)
	}

	// A second default config must demote the first one in the same call.
	second := createConfig(t, repo, ids, "Secondary", true)
	demoted, err := repo.GetConfig(ctx, first.ID)
	if err != nil {
		t.Fatalf("GetConfig(first): %v", err)
	}
	if demoted.IsDefault {
		t.Fatal("first config is still default after creating a second default")
	}
	promoted, err := repo.GetConfig(ctx, second.ID)
	if err != nil || !promoted.IsDefault {
		t.Fatalf("second config default = %v, %v", promoted.IsDefault, err)
	}

	// Names are unique case-insensitively.
	if _, err := repo.CreateConfig(ctx, llm.Config{
		ID: ids.Next(), Name: "primary", Provider: llm.ProviderOpenAI,
		Protocol: llm.ProtocolOpenAI, BaseURL: "http://127.0.0.1:13334/v1",
		TimeoutSeconds: 60, MaxRetries: 2, Status: llm.StatusActive,
	}); !errors.Is(err, llm.ErrNameTaken) {
		t.Fatalf("duplicate name error = %v", err)
	}

	// Updating the second config can demote it and clear the secret.
	second.Name = "Secondary renamed"
	second.APIKey = ""
	second.Headers = map[string]string{}
	second.IsDefault = false
	second.UpdatedAt = second.UpdatedAt.Add(time.Hour)
	if _, err := repo.UpdateConfig(ctx, second); err != nil {
		t.Fatalf("UpdateConfig: %v", err)
	}
	updated, err := repo.GetConfig(ctx, second.ID)
	if err != nil {
		t.Fatalf("GetConfig(updated): %v", err)
	}
	if updated.Name != "Secondary renamed" || updated.APIKey != "" || len(updated.Headers) != 0 || updated.IsDefault {
		t.Fatalf("updated = %#v", updated)
	}
	if !updated.CreatedAt.Equal(second.CreatedAt) || !updated.UpdatedAt.After(second.CreatedAt) {
		t.Fatalf("update must preserve created_at and advance updated_at: %v/%v", updated.CreatedAt, updated.UpdatedAt)
	}

	if _, err := repo.GetConfig(ctx, 999); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("missing GetConfig = %v", err)
	}
	missing := first
	missing.ID = 999
	if _, err := repo.UpdateConfig(ctx, missing); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("missing UpdateConfig = %v", err)
	}

	if err := repo.DeleteConfig(ctx, first.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	if _, err := repo.GetConfig(ctx, first.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("deleted config still readable: %v", err)
	}
	if err := repo.DeleteConfig(ctx, first.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("second delete = %v", err)
	}

	list, err := repo.ListConfigs(ctx)
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	if len(list) != 1 || list[0].ID != second.ID {
		t.Fatalf("configs = %#v", list)
	}
}

func TestRepositoryModelUpsertAndCascade(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := llminfra.NewRepository(pool)
	ids := newIDs(t)
	ctx := context.Background()
	cfg := createConfig(t, repo, ids, "Catalog", false)

	firstAt := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	first := []llm.Model{
		{
			ID: ids.Next(), ConfigID: cfg.ID, Model: "gpt-4o", DisplayName: "gpt-4o",
			ContextWindow: 128000, MaxOutput: 16384, SupportsTools: true, SupportsVision: true,
			Source: llm.SourceCatalog, Status: llm.ModelStatusActive, CreatedAt: firstAt, UpdatedAt: firstAt,
		},
		{
			ID: ids.Next(), ConfigID: cfg.ID, Model: "gpt-4o-mini", DisplayName: "gpt-4o-mini",
			Source: llm.SourceCatalog, Status: llm.ModelStatusActive, CreatedAt: firstAt, UpdatedAt: firstAt,
		},
	}
	stored, err := repo.UpsertModels(ctx, cfg.ID, first, firstAt)
	if err != nil {
		t.Fatalf("UpsertModels: %v", err)
	}
	if len(stored) != 2 || stored[0].Model != "gpt-4o" || stored[1].Model != "gpt-4o-mini" {
		t.Fatalf("stored = %#v", stored)
	}
	originalID := stored[0].ID

	secondAt := firstAt.Add(2 * time.Hour)
	second := []llm.Model{
		{
			ID: ids.Next(), ConfigID: cfg.ID, Model: "gpt-4o", DisplayName: "gpt-4o",
			ContextWindow: 200000, MaxOutput: 32000, SupportsTools: true,
			Source: llm.SourceCatalog, Status: llm.ModelStatusActive, CreatedAt: secondAt, UpdatedAt: secondAt,
		},
		{
			ID: ids.Next(), ConfigID: cfg.ID, Model: "o3", DisplayName: "o3",
			Source: llm.SourceCatalog, Status: llm.ModelStatusActive, CreatedAt: secondAt, UpdatedAt: secondAt,
		},
	}
	upserted, err := repo.UpsertModels(ctx, cfg.ID, second, secondAt)
	if err != nil {
		t.Fatalf("second UpsertModels: %v", err)
	}
	if len(upserted) != 3 {
		t.Fatalf("models after upsert = %#v", upserted)
	}
	if upserted[0].ID != originalID {
		t.Fatalf("upsert changed the existing row ID: %d -> %d", originalID, upserted[0].ID)
	}
	if upserted[0].ContextWindow != 200000 || upserted[0].MaxOutput != 32000 {
		t.Fatalf("upsert did not update values: %#v", upserted[0])
	}
	if !upserted[0].CreatedAt.Equal(firstAt) || !upserted[0].UpdatedAt.Equal(secondAt) {
		t.Fatalf("upsert timestamps = %v/%v", upserted[0].CreatedAt, upserted[0].UpdatedAt)
	}

	byConfig, err := repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("ListModelsByConfig: %v", err)
	}
	if len(byConfig) != 3 {
		t.Fatalf("by config = %#v", byConfig)
	}
	all, err := repo.ListModels(ctx)
	if err != nil || len(all) != 3 {
		t.Fatalf("ListModels = %#v, %v", all, err)
	}

	// Manual models share the same uniqueness rule.
	manual := llm.Model{
		ID: ids.Next(), ConfigID: cfg.ID, Model: "gpt-4o", DisplayName: "gpt-4o",
		Source: llm.SourceManual, Status: llm.ModelStatusActive, CreatedAt: secondAt, UpdatedAt: secondAt,
	}
	if _, err := repo.InsertModel(ctx, manual); !errors.Is(err, llm.ErrModelTaken) {
		t.Fatalf("duplicate manual model = %v", err)
	}
	manual.Model = "manual-model"
	inserted, err := repo.InsertModel(ctx, manual)
	if err != nil {
		t.Fatalf("InsertModel: %v", err)
	}
	if inserted.ID == 0 || inserted.Model != "manual-model" {
		t.Fatalf("inserted = %#v", inserted)
	}
	manualMissing := manual
	manualMissing.ID = ids.Next()
	manualMissing.ConfigID = 999
	manualMissing.Model = "orphan"
	if _, err := repo.InsertModel(ctx, manualMissing); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("orphan model = %v", err)
	}

	if err := repo.DeleteModel(ctx, inserted.ID); err != nil {
		t.Fatalf("DeleteModel: %v", err)
	}
	if err := repo.DeleteModel(ctx, inserted.ID); !errors.Is(err, llm.ErrModelNotFound) {
		t.Fatalf("second DeleteModel = %v", err)
	}

	// Deleting the config cascades every remaining model.
	if err := repo.DeleteConfig(ctx, cfg.ID); err != nil {
		t.Fatalf("DeleteConfig: %v", err)
	}
	remaining, err := repo.ListModelsByConfig(ctx, cfg.ID)
	if err != nil {
		t.Fatalf("ListModelsByConfig after cascade: %v", err)
	}
	if len(remaining) != 0 {
		t.Fatalf("models survived the config delete: %#v", remaining)
	}
}

// TestCanonicalProtocolPersists pins that both canonical 0.2.1 protocol values
// survive the database round trip. The legacy CHECK in llm_0001_llm.sql only
// allowed openai/openai_compatible, so canonical values validated in Go but
// could not be stored.
func TestCanonicalProtocolPersists(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := llminfra.NewRepository(pool)
	ids := newIDs(t)
	ctx := context.Background()

	canonical := []string{
		string(llm.ProtocolOpenAIChatCompletions),
		string(llm.ProtocolOpenAIResponses),
	}
	for _, protocol := range canonical {
		created := createConfigWithProtocol(t, repo, ids, "canonical-"+protocol, protocol, false)

		stored, err := repo.GetConfig(ctx, created.ID)
		if err != nil {
			t.Fatalf("GetConfig(%s): %v", protocol, err)
		}
		if stored.Protocol != protocol {
			t.Fatalf("stored protocol = %q, want %q", stored.Protocol, protocol)
		}

		stored.UpdatedAt = stored.UpdatedAt.Add(time.Hour)
		updated, err := repo.UpdateConfig(ctx, stored)
		if err != nil {
			t.Fatalf("UpdateConfig(%s): %v", protocol, err)
		}
		if updated.Protocol != protocol {
			t.Fatalf("updated protocol = %q, want %q", updated.Protocol, protocol)
		}
	}

	list, err := repo.ListConfigs(ctx)
	if err != nil {
		t.Fatalf("ListConfigs: %v", err)
	}
	got := make(map[string]bool, len(list))
	for _, cfg := range list {
		got[cfg.Protocol] = true
	}
	for _, protocol := range canonical {
		if !got[protocol] {
			t.Fatalf("ListConfigs lost canonical protocol %q: %#v", protocol, list)
		}
	}
}

// TestInvalidProtocolTyped400 pins that a CHECK violation on
// llm_config.protocol (PostgreSQL 23514) becomes the stable 400
// invalid_protocol instead of an unmapped 500.
func TestInvalidProtocolTyped400(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := llminfra.NewRepository(pool)
	ids := newIDs(t)
	ctx := context.Background()

	assertInvalidProtocol := func(t *testing.T, err error) {
		t.Helper()
		if err == nil {
			t.Fatal("invalid protocol was accepted")
		}
		var apiErr *platform.Error
		if !errors.As(err, &apiErr) {
			t.Fatalf("error = %T %v, want a typed *platform.Error", err, err)
		}
		if apiErr.Status != http.StatusBadRequest || apiErr.Code != "invalid_protocol" {
			t.Fatalf("error = %d %q, want 400 invalid_protocol", apiErr.Status, apiErr.Code)
		}
	}

	at := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	invalid := llm.Config{
		ID:             ids.Next(),
		Name:           "invalid-protocol",
		Provider:       llm.ProviderOpenAI,
		Protocol:       "anthropic",
		BaseURL:        "http://127.0.0.1:13334/v1",
		TimeoutSeconds: 60,
		MaxRetries:     2,
		Status:         llm.StatusActive,
		CreatedAt:      at,
		UpdatedAt:      at,
	}
	_, createErr := repo.CreateConfig(ctx, invalid)
	assertInvalidProtocol(t, createErr)

	valid := createConfig(t, repo, ids, "valid-before-invalid-update", false)
	valid.Protocol = "anthropic"
	valid.UpdatedAt = valid.UpdatedAt.Add(time.Hour)
	_, updateErr := repo.UpdateConfig(ctx, valid)
	assertInvalidProtocol(t, updateErr)
}
