//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	llmapp "github.com/EdouardRichard/EzTest/backend/internal/llm/application"
	llm "github.com/EdouardRichard/EzTest/backend/internal/llm/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/llm/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/crypto"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

var ctx = context.Background()

func ptr[T any](value T) *T {
	return &value
}

func mustKeyring(t *testing.T) *crypto.Keyring {
	t.Helper()
	kr, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return kr
}

func TestConfigRepositoryEncryptsAndSeeds(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	kr, _ := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	repo := postgres.NewConfigRepository(pool, kr)
	cfg, err := repo.Create(ctx, llm.CreateParams{Name: "default", Protocol: llm.ProtocolChat, BaseURL: "https://api.deepseek.com", Model: "deepseek-flash", APIKey: "sk-test", Headers: map[string]string{"x-test": "1"}, IsDefault: true})
	if err != nil {
		t.Fatal(err)
	}
	var raw []byte
	pool.QueryRow(ctx, `SELECT api_key_encrypted FROM llm_configs WHERE id=$1`, cfg.ID).Scan(&raw)
	if bytes.Contains(raw, []byte("sk-test")) {
		t.Fatal("key stored in plaintext")
	}
	got, err := repo.Get(ctx, cfg.ID)
	if err != nil || got.APIKey != "sk-test" || got.Headers["x-test"] != "1" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}

func TestConfigRepositoryDefaultSwitch(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewConfigRepository(pool, mustKeyring(t))
	base := llm.CreateParams{
		Protocol:        llm.ProtocolChat,
		BaseURL:         "https://api.deepseek.com",
		Model:           "deepseek-flash",
		RetryCount:      2,
		RetryIntervalMS: 500,
		TimeoutMS:       60000,
	}

	first := base
	first.Name = "first"
	first.IsDefault = true
	firstCfg, err := repo.Create(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	if !firstCfg.IsDefault {
		t.Fatalf("first=%+v", firstCfg)
	}

	second := base
	second.Name = "second"
	second.IsDefault = true
	secondCfg, err := repo.Create(ctx, second)
	if err != nil {
		t.Fatal(err)
	}

	gotFirst, err := repo.Get(ctx, firstCfg.ID)
	if err != nil || gotFirst.IsDefault {
		t.Fatalf("first default was not cleared: %+v err=%v", gotFirst, err)
	}
	gotSecond, err := repo.Get(ctx, secondCfg.ID)
	if err != nil || !gotSecond.IsDefault {
		t.Fatalf("second not default: %+v err=%v", gotSecond, err)
	}
	def, err := repo.GetDefault(ctx)
	if err != nil || def.ID != secondCfg.ID {
		t.Fatalf("default=%+v err=%v", def, err)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM llm_configs WHERE is_default`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("default rows=%d want 1", count)
	}

	third := base
	third.Name = "third"
	thirdCfg, err := repo.Create(ctx, third)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Update(ctx, thirdCfg.ID, llm.UpdateParams{IsDefault: ptr(true)}); err != nil {
		t.Fatal(err)
	}
	gotSecond, err = repo.Get(ctx, secondCfg.ID)
	if err != nil || gotSecond.IsDefault {
		t.Fatalf("second default was not cleared by update: %+v err=%v", gotSecond, err)
	}
	def, err = repo.GetDefault(ctx)
	if err != nil || def.ID != thirdCfg.ID {
		t.Fatalf("default after update=%+v err=%v", def, err)
	}

	// A failed update that also becomes the default must roll back the default
	// clearing: the previous default stays intact.
	fourth := base
	fourth.Name = "fourth"
	fourthCfg, err := repo.Create(ctx, fourth)
	if err != nil {
		t.Fatal(err)
	}
	fifth := base
	fifth.Name = "fifth"
	if _, err := repo.Create(ctx, fifth); err != nil {
		t.Fatal(err)
	}
	conflictName := "fifth"
	isDefault := true
	if _, err := repo.Update(ctx, fourthCfg.ID, llm.UpdateParams{Name: &conflictName, IsDefault: &isDefault}); !errors.Is(err, llm.ErrNameTaken) {
		t.Fatalf("conflicting default update err=%v, want ErrNameTaken", err)
	}
	def, err = repo.GetDefault(ctx)
	if err != nil || def.ID != thirdCfg.ID {
		t.Fatalf("default after failed update=%+v err=%v", def, err)
	}
	if gotFourth, err := repo.Get(ctx, fourthCfg.ID); err != nil || gotFourth.IsDefault {
		t.Fatalf("failed update left fourth default: %+v err=%v", gotFourth, err)
	}
}

func TestConfigRepositoryCRUDAndCatalog(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewConfigRepository(pool, mustKeyring(t))

	created, err := repo.Create(ctx, llm.CreateParams{
		Name:              "custom",
		Protocol:          llm.ProtocolResponses,
		BaseURL:           "https://api.deepseek.com/v1",
		Model:             "m1",
		APIKey:            "sk-1",
		Headers:           map[string]string{"X-Test": "v"},
		RetryCount:        3,
		RetryIntervalMS:   1000,
		TimeoutMS:         120000,
		StreamEnabled:     false,
		MultimodalEnabled: false,
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ID == 0 || created.Name != "custom" || created.Protocol != llm.ProtocolResponses {
		t.Fatalf("created=%+v", created)
	}
	if created.RetryCount != 3 || created.RetryIntervalMS != 1000 || created.TimeoutMS != 120000 {
		t.Fatalf("timing=%+v", created)
	}
	if created.StreamEnabled || created.MultimodalEnabled || created.IsDefault {
		t.Fatalf("flags=%+v", created)
	}
	if created.KeyVersion != 1 {
		t.Fatalf("key_version=%d want 1", created.KeyVersion)
	}
	if len(created.ModelCatalog) != 0 || created.CatalogFetchedAt != nil {
		t.Fatalf("catalog=%+v fetched=%v", created.ModelCatalog, created.CatalogFetchedAt)
	}

	listed, err := repo.List(ctx)
	if err != nil || len(listed) != 1 || listed[0].ID != created.ID {
		t.Fatalf("listed=%+v err=%v", listed, err)
	}
	if listed[0].APIKey != "sk-1" || listed[0].Headers["X-Test"] != "v" {
		t.Fatalf("list did not decrypt secrets: %+v", listed[0])
	}

	name := "renamed"
	model := "m2"
	retry := 5
	interval := 100
	timeout := 1000
	stream := true
	multimodal := true
	catalog := []llm.ModelInfo{{
		ID:              "m2",
		ContextWindow:   ptr[int64](1048576),
		MaxOutputTokens: ptr[int64](8192),
		InputModalities: []string{"text", "image"},
	}}
	updated, err := repo.Update(ctx, created.ID, llm.UpdateParams{
		Name:              &name,
		Model:             &model,
		RetryCount:        &retry,
		RetryIntervalMS:   &interval,
		TimeoutMS:         &timeout,
		StreamEnabled:     &stream,
		MultimodalEnabled: &multimodal,
		ModelCatalog:      &catalog,
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Name != "renamed" || updated.Model != "m2" || updated.RetryCount != 5 {
		t.Fatalf("updated=%+v", updated)
	}
	if !updated.StreamEnabled || !updated.MultimodalEnabled {
		t.Fatalf("updated flags=%+v", updated)
	}
	if len(updated.ModelCatalog) != 1 || updated.ModelCatalog[0].ID != "m2" {
		t.Fatalf("catalog=%+v", updated.ModelCatalog)
	}
	if updated.ModelCatalog[0].ContextWindow == nil || *updated.ModelCatalog[0].ContextWindow != 1048576 {
		t.Fatalf("context window=%v", updated.ModelCatalog[0].ContextWindow)
	}
	if updated.CatalogFetchedAt == nil {
		t.Fatal("catalog_fetched_at was not set when the catalog was saved")
	}
	if updated.APIKey != "sk-1" || updated.Headers["X-Test"] != "v" {
		t.Fatalf("update lost secrets: %+v", updated)
	}

	if _, err := repo.Create(ctx, llm.CreateParams{
		Name:            "other",
		Protocol:        llm.ProtocolChat,
		BaseURL:         "https://api.deepseek.com",
		Model:           "m1",
		RetryCount:      2,
		RetryIntervalMS: 500,
		TimeoutMS:       60000,
	}); err != nil {
		t.Fatal(err)
	}
	duplicate := "other"
	if _, err := repo.Update(ctx, created.ID, llm.UpdateParams{Name: &duplicate}); !errors.Is(err, llm.ErrNameTaken) {
		t.Fatalf("duplicate name err=%v, want ErrNameTaken", err)
	}
	if _, err := repo.Create(ctx, llm.CreateParams{Name: "other", Protocol: llm.ProtocolChat, BaseURL: "https://api.deepseek.com", Model: "m1", RetryCount: 2, RetryIntervalMS: 500, TimeoutMS: 60000}); !errors.Is(err, llm.ErrNameTaken) {
		t.Fatalf("duplicate create err=%v, want ErrNameTaken", err)
	}

	if _, err := repo.Get(ctx, 999999); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("get unknown err=%v, want ErrConfigNotFound", err)
	}
	if err := repo.Delete(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, created.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("get deleted err=%v, want ErrConfigNotFound", err)
	}
	if err := repo.Delete(ctx, created.ID); !errors.Is(err, llm.ErrConfigNotFound) {
		t.Fatalf("second delete err=%v, want ErrConfigNotFound", err)
	}
}

func TestConfigRepositoryBindsSecretsToTheirColumn(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewConfigRepository(pool, mustKeyring(t))
	cfg, err := repo.Create(ctx, llm.CreateParams{
		Name:            "aad",
		Protocol:        llm.ProtocolChat,
		BaseURL:         "https://api.deepseek.com",
		Model:           "deepseek-flash",
		APIKey:          "sk-aad",
		Headers:         map[string]string{"x-secret": "header-value"},
		RetryCount:      2,
		RetryIntervalMS: 500,
		TimeoutMS:       60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	var apiKeyRaw, headersRaw []byte
	if err := pool.QueryRow(ctx,
		`SELECT api_key_encrypted, headers_encrypted FROM llm_configs WHERE id=$1`, cfg.ID,
	).Scan(&apiKeyRaw, &headersRaw); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(apiKeyRaw, []byte("sk-aad")) {
		t.Fatal("api key stored in plaintext")
	}
	if bytes.Contains(headersRaw, []byte("x-secret")) || bytes.Contains(headersRaw, []byte("header-value")) {
		t.Fatal("headers stored in plaintext")
	}
	if bytes.Equal(apiKeyRaw, headersRaw) {
		t.Fatal("api key and headers use the same ciphertext")
	}

	// Copying one encrypted column into the other must fail authentication
	// because each column is bound to its own AAD.
	if _, err := pool.Exec(ctx, `UPDATE llm_configs SET api_key_encrypted = headers_encrypted WHERE id=$1`, cfg.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, cfg.ID); !errors.Is(err, crypto.ErrInvalidCiphertext) {
		t.Fatalf("cross-column ciphertext err=%v, want ErrInvalidCiphertext", err)
	}
}

func TestConfigRepositoryReencryptsWithActiveVersionOnSave(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	krV1, err := crypto.NewKeyring(map[int][]byte{1: bytes.Repeat([]byte{1}, 32)}, 1)
	if err != nil {
		t.Fatal(err)
	}
	repoV1 := postgres.NewConfigRepository(pool, krV1)
	cfg, err := repoV1.Create(ctx, llm.CreateParams{
		Name:            "rotate",
		Protocol:        llm.ProtocolChat,
		BaseURL:         "https://api.deepseek.com",
		Model:           "deepseek-flash",
		APIKey:          "sk-rotate",
		Headers:         map[string]string{"X-Test": "v"},
		RetryCount:      2,
		RetryIntervalMS: 500,
		TimeoutMS:       60000,
	})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.KeyVersion != 1 {
		t.Fatalf("created key_version=%d want 1", cfg.KeyVersion)
	}

	krV2, err := crypto.NewKeyring(map[int][]byte{
		1: bytes.Repeat([]byte{1}, 32),
		2: bytes.Repeat([]byte{2}, 32),
	}, 2)
	if err != nil {
		t.Fatal(err)
	}
	repoV2 := postgres.NewConfigRepository(pool, krV2)
	renamed := "rotated"
	updated, err := repoV2.Update(ctx, cfg.ID, llm.UpdateParams{Name: &renamed})
	if err != nil {
		t.Fatal(err)
	}
	if updated.KeyVersion != 2 {
		t.Fatalf("updated key_version=%d want 2", updated.KeyVersion)
	}
	got, err := repoV2.Get(ctx, cfg.ID)
	if err != nil || got.APIKey != "sk-rotate" || got.Headers["X-Test"] != "v" {
		t.Fatalf("got=%+v err=%v", got, err)
	}

	// The v1-only keyring must refuse the re-encrypted record explicitly.
	if _, err := repoV1.Get(ctx, cfg.ID); !errors.Is(err, crypto.ErrUnknownKeyVersion) {
		t.Fatalf("old keyring err=%v, want ErrUnknownKeyVersion", err)
	}
}

func TestConfigServiceSeedFromEnvAgainstDatabase(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewConfigRepository(pool, mustKeyring(t))
	auditor := audit.NewService(auditpostgres.NewRepository(pool))
	svc := llmapp.NewConfigService(repo, auditor)

	seeded, err := svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-seed", "deepseek-flash")
	if err != nil || !seeded {
		t.Fatalf("seeded=%v err=%v", seeded, err)
	}
	cfg, err := svc.GetDefault(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "default" || cfg.Protocol != llm.ProtocolChat || cfg.Model != "deepseek-flash" {
		t.Fatalf("seeded config=%+v", cfg)
	}
	if cfg.RetryCount != 2 || cfg.RetryIntervalMS != 500 || cfg.TimeoutMS != 60000 || !cfg.StreamEnabled || !cfg.MultimodalEnabled {
		t.Fatalf("seeded defaults=%+v", cfg)
	}
	if cfg.APIKey != "sk-seed" {
		t.Fatalf("seeded key=%q", cfg.APIKey)
	}

	var rawKey []byte
	if err := pool.QueryRow(ctx, `SELECT api_key_encrypted FROM llm_configs WHERE id=$1`, cfg.ID).Scan(&rawKey); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(rawKey, []byte("sk-seed")) {
		t.Fatal("seeded key stored in plaintext")
	}

	entries, _, err := auditor.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	var (
		createdCount int
		targetID     string
		metadata     string
	)
	for _, entry := range entries {
		if entry.Action == audit.ActionLLMConfigCreated {
			createdCount++
			targetID = entry.TargetID
			metadata = fmt.Sprint(entry.Metadata)
		}
	}
	if createdCount != 1 {
		t.Fatalf("llm.config_created entries=%d want 1", createdCount)
	}
	if targetID != fmt.Sprint(cfg.ID) {
		t.Fatalf("audit target=%q want %d", targetID, cfg.ID)
	}
	if bytes.Contains([]byte(metadata), []byte("sk-seed")) {
		t.Fatalf("audit metadata leaks the key: %s", metadata)
	}

	seeded, err = svc.SeedFromEnv(ctx, "https://api.deepseek.com", "sk-other", "other-model")
	if err != nil || seeded {
		t.Fatalf("second seed seeded=%v err=%v", seeded, err)
	}
	configs, err := svc.List(ctx)
	if err != nil || len(configs) != 1 {
		t.Fatalf("configs=%d err=%v", len(configs), err)
	}
	entries, _, err = auditor.List(ctx, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, entry := range entries {
		if entry.Action == audit.ActionLLMConfigCreated {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("llm.config_created entries after re-seed=%d want 1", count)
	}
}
