package config

import (
	"log/slog"
	"strings"
	"testing"
)

func validEnv() map[string]string {
	return map[string]string{
		"EZTEST_DATABASE_URL":      "postgres://u:p@localhost:5433/db",
		"EZTEST_SECRET_KEYS":       `{"1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`,
		"EZTEST_SECRET_KEY_ACTIVE": "1",
	}
}

func TestLoadDefaultsAndValidation(t *testing.T) {
	env := map[string]string{
		"EZTEST_DATABASE_URL":      "postgres://u:p@localhost:5433/db",
		"EZTEST_SECRET_KEYS":       `{"1":"MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="}`,
		"EZTEST_SECRET_KEY_ACTIVE": "1",
	}
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != ":13333" {
		t.Fatalf("HTTPAddr=%q", cfg.HTTPAddr)
	}
	if cfg.WebOrigin != "http://localhost:6868" {
		t.Fatalf("WebOrigin=%q", cfg.WebOrigin)
	}
	if cfg.Env != "dev" {
		t.Fatalf("Env=%q", cfg.Env)
	}
	if len(cfg.SecretKeys[1]) == 0 {
		t.Fatal("missing secret key")
	}

	delete(env, "EZTEST_DATABASE_URL")
	_, err = Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_DATABASE_URL") {
		t.Fatalf("expected EZTEST_DATABASE_URL error, got %v", err)
	}
}

func TestLoadRemainingDefaults(t *testing.T) {
	env := validEnv()
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StorageDir != ".data/objects" {
		t.Fatalf("StorageDir=%q", cfg.StorageDir)
	}
	if cfg.OpenAPIPath != "docs/openapi/p0.yaml" {
		t.Fatalf("OpenAPIPath=%q", cfg.OpenAPIPath)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Fatalf("LogLevel=%v", cfg.LogLevel)
	}
	if !cfg.EnableTestEndpoints {
		t.Fatal("test endpoints should default to enabled in dev")
	}
}

func TestLoadProdDisablesTestEndpointsByDefault(t *testing.T) {
	env := validEnv()
	env["EZTEST_ENV"] = "prod"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableTestEndpoints {
		t.Fatal("test endpoints must default to disabled in prod")
	}
}

func TestLoadExplicitTestEndpointsFlag(t *testing.T) {
	env := validEnv()
	env["EZTEST_ENV"] = "test"
	env["EZTEST_ENABLE_TEST_ENDPOINTS"] = "false"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EnableTestEndpoints {
		t.Fatal("explicit EZTEST_ENABLE_TEST_ENDPOINTS=false was ignored")
	}
}

func TestLoadRejectsMissingSecretKeys(t *testing.T) {
	env := validEnv()
	delete(env, "EZTEST_SECRET_KEYS")
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_SECRET_KEYS") {
		t.Fatalf("expected EZTEST_SECRET_KEYS error, got %v", err)
	}
}

func TestLoadRejectsMissingActiveKey(t *testing.T) {
	env := validEnv()
	delete(env, "EZTEST_SECRET_KEY_ACTIVE")
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_SECRET_KEY_ACTIVE") {
		t.Fatalf("expected EZTEST_SECRET_KEY_ACTIVE error, got %v", err)
	}
}

func TestLoadRejectsActiveKeyNotInKeys(t *testing.T) {
	env := validEnv()
	env["EZTEST_SECRET_KEY_ACTIVE"] = "2"
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_SECRET_KEY_ACTIVE") {
		t.Fatalf("expected EZTEST_SECRET_KEY_ACTIVE error, got %v", err)
	}
}

func TestLoadRejectsMalformedSecretKeys(t *testing.T) {
	env := validEnv()
	env["EZTEST_SECRET_KEYS"] = `not-json`
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_SECRET_KEYS") {
		t.Fatalf("expected EZTEST_SECRET_KEYS error, got %v", err)
	}
}

func TestLoadRejectsWrongSecretKeyLength(t *testing.T) {
	env := validEnv()
	env["EZTEST_SECRET_KEYS"] = `{"1":"c2hvcnQ="}`
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "32") {
		t.Fatalf("expected 32-byte key error, got %v", err)
	}
}

func TestLoadLogLevel(t *testing.T) {
	cases := []struct {
		raw  string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
	}
	for _, tc := range cases {
		t.Run(tc.raw, func(t *testing.T) {
			env := validEnv()
			env["EZTEST_LOG_LEVEL"] = tc.raw
			cfg, err := Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.LogLevel != tc.want {
				t.Fatalf("LogLevel=%v want %v", cfg.LogLevel, tc.want)
			}
		})
	}
}

func TestLoadRejectsInvalidLogLevel(t *testing.T) {
	env := validEnv()
	env["EZTEST_LOG_LEVEL"] = "verbose"
	_, err := Load(func(k string) string { return env[k] })
	if err == nil || !strings.Contains(err.Error(), "EZTEST_LOG_LEVEL") {
		t.Fatalf("expected EZTEST_LOG_LEVEL error, got %v", err)
	}
}

func TestLoadOptionalFields(t *testing.T) {
	env := validEnv()
	env["EZTEST_TEST_DATABASE_URL"] = "postgres://test@localhost:5433/eztest_test"
	env["EZTEST_ADMIN_USERNAME"] = "admin"
	env["EZTEST_ADMIN_PASSWORD"] = "secret"
	env["llm_base_url"] = "https://api.deepseek.com"
	env["llm_key"] = "sk-test"
	env["llm_model_name"] = "deepseek-flash"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.TestDatabaseURL != "postgres://test@localhost:5433/eztest_test" {
		t.Fatalf("TestDatabaseURL=%q", cfg.TestDatabaseURL)
	}
	if cfg.AdminUsername != "admin" || cfg.AdminPassword != "secret" {
		t.Fatalf("admin=%q/%q", cfg.AdminUsername, cfg.AdminPassword)
	}
	if cfg.LLMSeed.BaseURL != "https://api.deepseek.com" ||
		cfg.LLMSeed.Key != "sk-test" ||
		cfg.LLMSeed.Model != "deepseek-flash" {
		t.Fatalf("LLMSeed=%+v", cfg.LLMSeed)
	}
}
