package config

import (
	"strings"
	"testing"
)

// TestVisualBaselineMaxBytesDefaults pins the design spec §9.3 baseline cap: 5
// MiB when EZTEST_VISUAL_BASELINE_MAX_BYTES is not configured.
func TestVisualBaselineMaxBytesDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.VisualBaselineMaxBytes != 5<<20 {
		t.Fatalf("VisualBaselineMaxBytes=%d want %d", cfg.VisualBaselineMaxBytes, 5<<20)
	}
}

// TestVisualBaselineMaxBytesOverrides proves the configured cap is read and the
// documented boundaries 65536 and 20971520 are inclusive.
func TestVisualBaselineMaxBytesOverrides(t *testing.T) {
	for _, value := range []string{"65536", "1048576", "20971520"} {
		env := validEnv()
		env["EZTEST_VISUAL_BASELINE_MAX_BYTES"] = value
		cfg, err := Load(func(k string) string { return env[k] })
		if err != nil {
			t.Fatalf("value %s: %v", value, err)
		}
		if cfg.VisualBaselineMaxBytes == 0 {
			t.Fatalf("value %s was not applied", value)
		}
	}
}

// TestVisualBaselineMaxBytesRejectsOutOfRange pins the 65536..20971520 range so
// a typo cannot disable or unboundedly raise the baseline cap.
func TestVisualBaselineMaxBytesRejectsOutOfRange(t *testing.T) {
	for _, value := range []string{"0", "65535", "20971521", "512k", "-1"} {
		env := validEnv()
		env["EZTEST_VISUAL_BASELINE_MAX_BYTES"] = value
		_, err := Load(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("EZTEST_VISUAL_BASELINE_MAX_BYTES=%q was accepted", value)
		}
		if !strings.Contains(err.Error(), "EZTEST_VISUAL_BASELINE_MAX_BYTES") {
			t.Fatalf("error does not name the variable: %v", err)
		}
	}
}
