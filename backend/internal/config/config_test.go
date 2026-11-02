package config

import (
	"strings"
	"testing"
)

const testDatabaseURL = "postgres://eztest:secret@localhost:5433/eztest_v02?sslmode=disable"

func setRequired(t *testing.T) {
	t.Helper()
	t.Setenv("EZTEST_DATABASE_URL", testDatabaseURL)
	t.Setenv("EZTEST_API_PORT", "")
	t.Setenv("EZTEST_WEB_PORT", "")
	t.Setenv("EZTEST_EXECUTOR_PORT", "")
}

func TestLoadDefaults(t *testing.T) {
	setRequired(t)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != testDatabaseURL {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
	if cfg.APIPort != 13333 || cfg.WebPort != 6868 || cfg.ExecutorPort != 13336 {
		t.Fatalf("ports = %d/%d/%d", cfg.APIPort, cfg.WebPort, cfg.ExecutorPort)
	}
	if got, want := cfg.ExecutorURL(), "http://127.0.0.1:13336"; got != want {
		t.Fatalf("ExecutorURL() = %q, want %q", got, want)
	}
	origins := cfg.WebOrigins()
	if len(origins) != 2 || origins[0] != "http://localhost:6868" || origins[1] != "http://127.0.0.1:6868" {
		t.Fatalf("WebOrigins() = %#v", origins)
	}
}

func TestLoadRequiresDatabaseURL(t *testing.T) {
	setRequired(t)
	t.Setenv("EZTEST_DATABASE_URL", "  ")

	if _, err := Load(); err == nil {
		t.Fatal("Load() succeeded without EZTEST_DATABASE_URL")
	} else if !strings.Contains(err.Error(), "EZTEST_DATABASE_URL") {
		t.Fatalf("error = %v, want mention of EZTEST_DATABASE_URL", err)
	}
}

func TestLoadRejectsNonPostgresURL(t *testing.T) {
	setRequired(t)
	t.Setenv("EZTEST_DATABASE_URL", "mysql://localhost/db")

	if _, err := Load(); err == nil {
		t.Fatal("Load() accepted a non-postgres URL")
	}
}

func TestLoadRejectsBadPorts(t *testing.T) {
	for _, value := range []string{"0", "-1", "65536", "abc"} {
		t.Run(value, func(t *testing.T) {
			setRequired(t)
			t.Setenv("EZTEST_API_PORT", value)
			if _, err := Load(); err == nil {
				t.Fatalf("Load() accepted EZTEST_API_PORT=%q", value)
			}
		})
	}
}

func TestLoadUsesOnlyTheFourStartupVariables(t *testing.T) {
	setRequired(t)
	// Legacy 0.1 variables must not change 0.2 behavior.
	t.Setenv("EZTEST_HTTP_ADDR", ":9999")
	t.Setenv("EZTEST_WEB_ORIGIN", "http://example.com")
	t.Setenv("EZTEST_ADMIN_USERNAME", "root")
	t.Setenv("EZTEST_STORAGE_DIR", "/tmp/objects")
	t.Setenv("EZTEST_SNOWFLAKE_NODE_ID", "42")
	t.Setenv("EZTEST_API_PORT", "18080")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.APIPort != 18080 {
		t.Fatalf("APIPort = %d, want 18080", cfg.APIPort)
	}
	if cfg.WebPort != 6868 || cfg.ExecutorPort != 13336 {
		t.Fatalf("legacy env leaked: web=%d executor=%d", cfg.WebPort, cfg.ExecutorPort)
	}
	if origins := cfg.WebOrigins(); len(origins) != 2 || origins[0] != "http://localhost:6868" {
		t.Fatalf("legacy EZTEST_WEB_ORIGIN leaked: %#v", origins)
	}
}

func TestLoadTrimsDatabaseURL(t *testing.T) {
	setRequired(t)
	t.Setenv("EZTEST_DATABASE_URL", "  "+testDatabaseURL+"  ")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.DatabaseURL != testDatabaseURL {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL)
	}
}
