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
	Env             string
	HTTPAddr        string
	WebOrigin       string
	DatabaseURL     string
	TestDatabaseURL string
	AdminUsername   string
	AdminPassword   string
	SecretKeys      map[int][]byte
	SecretKeyActive int
	StorageDir      string
	// OpenAPIPath locates the hand-maintained OpenAPI document served by
	// GET /api/openapi.yaml. It is relative to the process working directory by
	// default so the deployed image only needs to ship the file at that path.
	OpenAPIPath string
	// AgentFileRoot is the read-only root of the file.list/file.read tools. It
	// is empty by default, which disables the file tools (design spec §4.1).
	AgentFileRoot string
	// AgentHTTPAllowlist lists the hosts http.get may reach. An entry may carry
	// an explicit :port.
	AgentHTTPAllowlist []string
	// AgentToolTimeoutMS bounds every single tool invocation.
	AgentToolTimeoutMS int
	// AgentMaxConcurrentRuns bounds the process-wide number of running Agent
	// runs.
	AgentMaxConcurrentRuns int

	EnableTestEndpoints bool
	LogLevel            slog.Level
	LLMSeed             LLMSeed
}

// Agent runtime defaults and bounds from design spec §4.1.
const (
	defaultAgentToolTimeoutMS     = 10000
	minAgentToolTimeoutMS         = 1000
	maxAgentToolTimeoutMS         = 60000
	defaultAgentMaxConcurrentRuns = 4
	minAgentMaxConcurrentRuns     = 1
	maxAgentMaxConcurrentRuns     = 32
)

// defaultAgentHTTPAllowlist is the localhost-only HTTP allowlist applied when
// EZTEST_AGENT_HTTP_ALLOWLIST is unset.
var defaultAgentHTTPAllowlist = []string{"127.0.0.1", "localhost"}

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
		OpenAPIPath:     valueOr(getenv, "EZTEST_OPENAPI_PATH", "docs/openapi/api.yaml"),
		AgentFileRoot:   strings.TrimSpace(getenv("EZTEST_AGENT_FILE_ROOT")),
		LLMSeed: LLMSeed{
			BaseURL: strings.TrimSpace(getenv("llm_base_url")),
			Key:     strings.TrimSpace(getenv("llm_key")),
			Model:   strings.TrimSpace(getenv("llm_model_name")),
		},
	}
	if cfg.DatabaseURL == "" {
		return Config{}, fmt.Errorf("config: EZTEST_DATABASE_URL is required")
	}

	allowlist, err := parseAgentHTTPAllowlist(getenv("EZTEST_AGENT_HTTP_ALLOWLIST"))
	if err != nil {
		return Config{}, err
	}
	cfg.AgentHTTPAllowlist = allowlist

	toolTimeout, err := parseAgentInt(getenv, "EZTEST_AGENT_TOOL_TIMEOUT_MS",
		defaultAgentToolTimeoutMS, minAgentToolTimeoutMS, maxAgentToolTimeoutMS)
	if err != nil {
		return Config{}, err
	}
	cfg.AgentToolTimeoutMS = toolTimeout

	maxConcurrentRuns, err := parseAgentInt(getenv, "EZTEST_AGENT_MAX_CONCURRENT_RUNS",
		defaultAgentMaxConcurrentRuns, minAgentMaxConcurrentRuns, maxAgentMaxConcurrentRuns)
	if err != nil {
		return Config{}, err
	}
	cfg.AgentMaxConcurrentRuns = maxConcurrentRuns

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

// parseAgentHTTPAllowlist parses the comma-separated http.get host allowlist.
// An unset variable selects the localhost-only default; a value that contains
// no host at all is rejected so a typo cannot silently disable every host (the
// tool itself fails closed on an empty list).
func parseAgentHTTPAllowlist(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return append([]string(nil), defaultAgentHTTPAllowlist...), nil
	}
	hosts := make([]string, 0, strings.Count(raw, ",")+1)
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			hosts = append(hosts, entry)
		}
	}
	if len(hosts) == 0 {
		return nil, fmt.Errorf("config: EZTEST_AGENT_HTTP_ALLOWLIST must contain at least one host, got %q", raw)
	}
	return hosts, nil
}

// parseAgentInt parses one bounded Agent integer setting. An empty value
// selects def; anything else must be a number inside [min, max].
func parseAgentInt(getenv func(string) string, key string, def, min, max int) (int, error) {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		return def, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("config: %s must be an integer between %d and %d, got %q", key, min, max, raw)
	}
	return value, nil
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
