package config

import (
	"log/slog"
	"reflect"
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
	if cfg.OpenAPIPath != "docs/openapi/api.yaml" {
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

// TestLoadAgentDefaults pins the Agent runtime defaults from design spec §4.1:
// no file root, localhost-only HTTP allowlist, a 10 s tool budget, four
// concurrent runs and the renamed OpenAPI document.
func TestLoadAgentDefaults(t *testing.T) {
	env := validEnv()
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentFileRoot != "" {
		t.Fatalf("AgentFileRoot=%q want the empty default (file tools disabled)", cfg.AgentFileRoot)
	}
	if want := []string{"127.0.0.1", "localhost"}; !reflect.DeepEqual(cfg.AgentHTTPAllowlist, want) {
		t.Fatalf("AgentHTTPAllowlist=%v want %v", cfg.AgentHTTPAllowlist, want)
	}
	if cfg.AgentToolTimeoutMS != 10000 {
		t.Fatalf("AgentToolTimeoutMS=%d want 10000", cfg.AgentToolTimeoutMS)
	}
	if cfg.AgentMaxConcurrentRuns != 4 {
		t.Fatalf("AgentMaxConcurrentRuns=%d want 4", cfg.AgentMaxConcurrentRuns)
	}
	if cfg.OpenAPIPath != "docs/openapi/api.yaml" {
		t.Fatalf("OpenAPIPath=%q want docs/openapi/api.yaml", cfg.OpenAPIPath)
	}
}

// TestLoadAgentOverrides proves the Agent environment variables are trimmed and
// parsed, including a comma-separated HTTP allowlist with blank entries.
func TestLoadAgentOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_AGENT_FILE_ROOT"] = "  D:/Project_new/EzTest  "
	env["EZTEST_AGENT_HTTP_ALLOWLIST"] = " 10.0.0.1 , api.internal:8443 ,, "
	env["EZTEST_AGENT_TOOL_TIMEOUT_MS"] = "15000"
	env["EZTEST_AGENT_MAX_CONCURRENT_RUNS"] = "8"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AgentFileRoot != "D:/Project_new/EzTest" {
		t.Fatalf("AgentFileRoot=%q", cfg.AgentFileRoot)
	}
	if want := []string{"10.0.0.1", "api.internal:8443"}; !reflect.DeepEqual(cfg.AgentHTTPAllowlist, want) {
		t.Fatalf("AgentHTTPAllowlist=%v want %v", cfg.AgentHTTPAllowlist, want)
	}
	if cfg.AgentToolTimeoutMS != 15000 {
		t.Fatalf("AgentToolTimeoutMS=%d", cfg.AgentToolTimeoutMS)
	}
	if cfg.AgentMaxConcurrentRuns != 8 {
		t.Fatalf("AgentMaxConcurrentRuns=%d", cfg.AgentMaxConcurrentRuns)
	}
}

// TestLoadRejectsInvalidAgentValues proves every Agent bound is validated at
// startup with an error naming the offending environment variable.
func TestLoadRejectsInvalidAgentValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"tool timeout too low", "EZTEST_AGENT_TOOL_TIMEOUT_MS", "10"},
		{"tool timeout too high", "EZTEST_AGENT_TOOL_TIMEOUT_MS", "60001"},
		{"tool timeout not a number", "EZTEST_AGENT_TOOL_TIMEOUT_MS", "soon"},
		{"max concurrent runs zero", "EZTEST_AGENT_MAX_CONCURRENT_RUNS", "0"},
		{"max concurrent runs too high", "EZTEST_AGENT_MAX_CONCURRENT_RUNS", "33"},
		{"max concurrent runs not a number", "EZTEST_AGENT_MAX_CONCURRENT_RUNS", "many"},
		{"allowlist without hosts", "EZTEST_AGENT_HTTP_ALLOWLIST", " , "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env := validEnv()
			env[tc.key] = tc.value
			_, err := Load(func(k string) string { return env[k] })
			if err == nil {
				t.Fatalf("Load accepted %s=%q", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q does not name %s", err, tc.key)
			}
		})
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

// TestLoadP3Defaults pins the P3 version-context defaults from design spec
// §4.1: the attachment filesystem root and the Snowflake node ID.
func TestLoadP3Defaults(t *testing.T) {
	env := validEnv()
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VersionAttachmentDir != ".data/version-attachments" {
		t.Fatalf("VersionAttachmentDir=%q want .data/version-attachments", cfg.VersionAttachmentDir)
	}
	if cfg.SnowflakeNodeID != 1 {
		t.Fatalf("SnowflakeNodeID=%d want 1", cfg.SnowflakeNodeID)
	}
}

// TestLoadP3Overrides proves the P3 environment variables are parsed and
// trimmed, including a Docker-style absolute attachment directory.
func TestLoadP3Overrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_VERSION_ATTACHMENT_DIR"] = "  /data/version-attachments  "
	env["EZTEST_SNOWFLAKE_NODE_ID"] = "17"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VersionAttachmentDir != "/data/version-attachments" {
		t.Fatalf("VersionAttachmentDir=%q", cfg.VersionAttachmentDir)
	}
	if cfg.SnowflakeNodeID != 17 {
		t.Fatalf("SnowflakeNodeID=%d", cfg.SnowflakeNodeID)
	}
}

// TestLoadSnowflakeNodeBounds proves the Snowflake node ID is validated to
// 0..1023 at startup, with an error naming the offending variable.
func TestLoadSnowflakeNodeBounds(t *testing.T) {
	for _, value := range []string{"-1", "1024"} {
		t.Run("rejects "+value, func(t *testing.T) {
			env := validEnv()
			env["EZTEST_SNOWFLAKE_NODE_ID"] = value
			_, err := Load(func(k string) string { return env[k] })
			if err == nil {
				t.Fatalf("Load accepted EZTEST_SNOWFLAKE_NODE_ID=%q", value)
			}
			if !strings.Contains(err.Error(), "EZTEST_SNOWFLAKE_NODE_ID") {
				t.Fatalf("error %q does not name EZTEST_SNOWFLAKE_NODE_ID", err)
			}
		})
	}
	for _, tc := range []struct {
		value string
		want  int64
	}{
		{"0", 0},
		{"1023", 1023},
	} {
		t.Run("accepts "+tc.value, func(t *testing.T) {
			env := validEnv()
			env["EZTEST_SNOWFLAKE_NODE_ID"] = tc.value
			cfg, err := Load(func(k string) string { return env[k] })
			if err != nil {
				t.Fatal(err)
			}
			if cfg.SnowflakeNodeID != tc.want {
				t.Fatalf("SnowflakeNodeID=%d want %d", cfg.SnowflakeNodeID, tc.want)
			}
		})
	}
}
