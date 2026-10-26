// Package config parses and validates the EzTest service configuration from
// environment variables. It contains no business logic.
package config

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
)

// LLMSeed is the optional first-boot LLM configuration, used to seed the
// database when no LLM configuration exists yet.
type LLMSeed struct {
	BaseURL string
	Key     string
	Model   string
}

// Config is the process configuration. It is resolved once at startup.
type Config struct {
	Env                 string
	HTTPAddr            string
	WebOrigin           string
	DatabaseURL         string
	TestDatabaseURL     string
	AdminUsername       string
	AdminPassword       string
	SecretKeys          map[int][]byte
	SecretKeyActive     int
	StorageDir          string
	EnableTestEndpoints bool
	LogLevel            slog.Level
	LLMSeed             LLMSeed
}

// Load reads the configuration through getenv and validates it. It returns an
// error that names the offending environment variable instead of panicking.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		Env:             valueOr(getenv, "EZTEST_ENV", "dev"),
		HTTPAddr:        valueOr(getenv, "EZTEST_HTTP_ADDR", ":13333"),
		WebOrigin:       valueOr(getenv, "EZTEST_WEB_ORIGIN", "http://localhost:6868"),
		DatabaseURL:     strings.TrimSpace(getenv("EZTEST_DATABASE_URL")),
		TestDatabaseURL: strings.TrimSpace(getenv("EZTEST_TEST_DATABASE_URL")),
		AdminUsername:   strings.TrimSpace(getenv("EZTEST_ADMIN_USERNAME")),
		AdminPassword:   getenv("EZTEST_ADMIN_PASSWORD"),
		StorageDir:      valueOr(getenv, "EZTEST_STORAGE_DIR", ".data/objects"),
		LLMSeed: LLMSeed{
			BaseURL: strings.TrimSpace(getenv("llm_base_url")),
			Key:     strings.TrimSpace(getenv("llm_key")),
			Model:   strings.TrimSpace(getenv("llm_model_name")),
		},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: EZTEST_DATABASE_URL is required")
	}

	keys, err := parseSecretKeys(getenv("EZTEST_SECRET_KEYS"))
	if err != nil {
		return Config{}, err
	}
	cfg.SecretKeys = keys

	activeRaw := strings.TrimSpace(getenv("EZTEST_SECRET_KEY_ACTIVE"))
	if activeRaw == "" {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE is required")
	}
	active, err := strconv.Atoi(activeRaw)
	if err != nil || active <= 0 {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE must be a positive integer, got %q", activeRaw)
	}
	if _, ok := keys[active]; !ok {
		return Config{}, fmt.Errorf("config: EZTEST_SECRET_KEY_ACTIVE %d is not present in EZTEST_SECRET_KEYS", active)
	}
	cfg.SecretKeyActive = active

	rawTestEndpoints := strings.TrimSpace(getenv("EZTEST_ENABLE_TEST_ENDPOINTS"))
	if rawTestEndpoints == "" {
		cfg.EnableTestEndpoints = cfg.Env != "prod"
	} else {
		enabled, err := strconv.ParseBool(rawTestEndpoints)
		if err != nil {
			return Config{}, fmt.Errorf("config: EZTEST_ENABLE_TEST_ENDPOINTS must be a boolean, got %q", rawTestEndpoints)
		}
		cfg.EnableTestEndpoints = enabled
	}

	level, err := parseLogLevel(getenv("EZTEST_LOG_LEVEL"))
	if err != nil {
		return Config{}, err
	}
	cfg.LogLevel = level

	return cfg, nil
}

// valueOr returns the trimmed value of key, or def when it is unset or empty.
func valueOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// parseSecretKeys decodes EZTEST_SECRET_KEYS: a JSON object mapping key
// version to a base64-encoded 32-byte AES-256 key.
func parseSecretKeys(raw string) (map[int][]byte, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS is required")
	}
	var encoded map[string]string
	if err := json.Unmarshal([]byte(raw), &encoded); err != nil {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS must be a JSON object mapping key version to base64 key: %w", err)
	}
	if len(encoded) == 0 {
		return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS must contain at least one key")
	}
	keys := make(map[int][]byte, len(encoded))
	for version, value := range encoded {
		v, err := strconv.Atoi(strings.TrimSpace(version))
		if err != nil || v <= 0 {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS has invalid key version %q", version)
		}
		key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(value))
		if err != nil {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS key %q is not valid base64: %w", version, err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("config: EZTEST_SECRET_KEYS key %q must decode to 32 bytes, got %d", version, len(key))
		}
		keys[v] = key
	}
	return keys, nil
}

// parseLogLevel maps EZTEST_LOG_LEVEL to a slog level. Empty means info.
func parseLogLevel(raw string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "info":
		return slog.LevelInfo, nil
	case "debug":
		return slog.LevelDebug, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("config: EZTEST_LOG_LEVEL must be one of debug, info, warn, error, got %q", raw)
	}
}
