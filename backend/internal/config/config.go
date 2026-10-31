// Package config parses the only four EzTest 0.2 startup variables.
//
// Runtime configuration (paths, limits, retention, LLM, agent budgets, ...)
// lives in the database settings center, never in the environment.
package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const (
	defaultAPIPort      = 13333
	defaultWebPort      = 6868
	defaultExecutorPort = 13336
)

// Config is the complete startup configuration.
type Config struct {
	DatabaseURL  string
	APIPort      int
	WebPort      int
	ExecutorPort int
}

// Load reads EZTEST_DATABASE_URL, EZTEST_API_PORT, EZTEST_WEB_PORT and
// EZTEST_EXECUTOR_PORT. No other environment variable is consulted.
func Load() (Config, error) {
	dbURL := strings.TrimSpace(os.Getenv("EZTEST_DATABASE_URL"))
	if dbURL == "" {
		return Config{}, fmt.Errorf("EZTEST_DATABASE_URL is required")
	}
	parsed, err := url.Parse(dbURL)
	if err != nil {
		return Config{}, fmt.Errorf("EZTEST_DATABASE_URL is invalid: %w", err)
	}
	if parsed.Scheme != "postgres" && parsed.Scheme != "postgresql" {
		return Config{}, fmt.Errorf("EZTEST_DATABASE_URL must use postgres/postgresql, got %q", parsed.Scheme)
	}
	if parsed.Host == "" {
		return Config{}, fmt.Errorf("EZTEST_DATABASE_URL must include a host")
	}

	apiPort, err := readPort("EZTEST_API_PORT", defaultAPIPort)
	if err != nil {
		return Config{}, err
	}
	webPort, err := readPort("EZTEST_WEB_PORT", defaultWebPort)
	if err != nil {
		return Config{}, err
	}
	executorPort, err := readPort("EZTEST_EXECUTOR_PORT", defaultExecutorPort)
	if err != nil {
		return Config{}, err
	}

	return Config{
		DatabaseURL:  dbURL,
		APIPort:      apiPort,
		WebPort:      webPort,
		ExecutorPort: executorPort,
	}, nil
}

func readPort(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	port, err := strconv.Atoi(raw)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf("%s must be a port between 1 and 65535, got %q", key, raw)
	}
	return port, nil
}

// WebOrigins returns the two loopback origins the web app may use. The origin
// is derived from EZTEST_WEB_PORT because 0.2 has no EZTEST_WEB_ORIGIN.
func (c Config) WebOrigins() []string {
	return []string{
		fmt.Sprintf("http://localhost:%d", c.WebPort),
		fmt.Sprintf("http://127.0.0.1:%d", c.WebPort),
	}
}

// ExecutorURL derives the default executor URL from EZTEST_EXECUTOR_PORT. The
// value is seeded into the settings center and can be changed there.
func (c Config) ExecutorURL() string {
	return fmt.Sprintf("http://127.0.0.1:%d", c.ExecutorPort)
}
