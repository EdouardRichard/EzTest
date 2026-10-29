package config

import (
	"strings"
	"testing"
)

// TestRateLimitAndGCDefaults pins the P8 hardening environment defaults from
// design spec §11.4: 50 rps / 100 burst / 10 SSE per user, GC disabled with a
// 30 day retention and the latest 3 batches protected.
func TestRateLimitAndGCDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimitRPS != 50 {
		t.Fatalf("RateLimitRPS=%d want 50", cfg.RateLimitRPS)
	}
	if cfg.RateLimitBurst != 100 {
		t.Fatalf("RateLimitBurst=%d want 100", cfg.RateLimitBurst)
	}
	if cfg.RateLimitSSEMax != 10 {
		t.Fatalf("RateLimitSSEMax=%d want 10", cfg.RateLimitSSEMax)
	}
	if cfg.GCEnabled {
		t.Fatal("GCEnabled must default to false (dry-run only)")
	}
	if cfg.GCRetentionDays != 30 {
		t.Fatalf("GCRetentionDays=%d want 30", cfg.GCRetentionDays)
	}
	if cfg.GCBatchKeep != 3 {
		t.Fatalf("GCBatchKeep=%d want 3", cfg.GCBatchKeep)
	}
}

// TestRateLimitAndGCOverrides accepts the documented ranges and the RPS=0 off
// switch.
func TestRateLimitAndGCOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_RATE_LIMIT_RPS"] = "0"
	env["EZTEST_RATE_LIMIT_BURST"] = "5000"
	env["EZTEST_RATE_LIMIT_SSE_MAX"] = "100"
	env["EZTEST_GC_ENABLED"] = "true"
	env["EZTEST_GC_RETENTION_DAYS"] = "3650"
	env["EZTEST_GC_BATCH_KEEP"] = "50"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RateLimitRPS != 0 || cfg.RateLimitBurst != 5000 || cfg.RateLimitSSEMax != 100 {
		t.Fatalf("rate limit overrides not applied: %+v", cfg)
	}
	if !cfg.GCEnabled || cfg.GCRetentionDays != 3650 || cfg.GCBatchKeep != 50 {
		t.Fatalf("GC overrides not applied: %+v", cfg)
	}
}

// TestRateLimitAndGCRejectOutOfRange pins the validation ranges so a typo can
// never silently disable or weaken the limiter or GC safety.
func TestRateLimitAndGCRejectOutOfRange(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"EZTEST_RATE_LIMIT_RPS", "-1"},
		{"EZTEST_RATE_LIMIT_RPS", "1001"},
		{"EZTEST_RATE_LIMIT_RPS", "fast"},
		{"EZTEST_RATE_LIMIT_BURST", "0"},
		{"EZTEST_RATE_LIMIT_BURST", "5001"},
		{"EZTEST_RATE_LIMIT_SSE_MAX", "0"},
		{"EZTEST_RATE_LIMIT_SSE_MAX", "101"},
		{"EZTEST_GC_RETENTION_DAYS", "0"},
		{"EZTEST_GC_RETENTION_DAYS", "3651"},
		{"EZTEST_GC_BATCH_KEEP", "0"},
		{"EZTEST_GC_BATCH_KEEP", "51"},
		{"EZTEST_GC_ENABLED", "maybe"},
	}
	for _, tc := range cases {
		env := validEnv()
		env[tc.key] = tc.value
		_, err := Load(func(k string) string { return env[k] })
		if err == nil {
			t.Fatalf("%s=%q was accepted", tc.key, tc.value)
		}
		if !strings.Contains(err.Error(), tc.key) {
			t.Fatalf("%s=%q error does not name the variable: %v", tc.key, tc.value, err)
		}
	}
}
