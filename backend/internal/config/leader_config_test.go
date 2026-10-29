package config

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"
)

// TestLeaderDefaults pins the P8 multi-instance defaults of design spec
// §10.5/§11.4: the instance id falls back to hostname+pid (at most 64
// characters) and the advisory lock key defaults to the stable shared value.
func TestLeaderDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeaderLockKey != 1163411512 {
		t.Fatalf("LeaderLockKey=%d want 1163411512", cfg.LeaderLockKey)
	}
	if cfg.InstanceID == "" {
		t.Fatal("InstanceID must default to hostname+pid")
	}
	if !strings.Contains(cfg.InstanceID, fmt.Sprintf("-%d", os.Getpid())) {
		t.Fatalf("InstanceID=%q must carry the process id", cfg.InstanceID)
	}
	if utf8.RuneCountInString(cfg.InstanceID) > 64 {
		t.Fatalf("InstanceID=%q is longer than 64 characters", cfg.InstanceID)
	}
}

// TestLeaderOverrides accepts an explicit instance id and any int64 lock key;
// a negative key is a valid PostgreSQL bigint advisory key.
func TestLeaderOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_INSTANCE_ID"] = " api-1 "
	env["EZTEST_LEADER_LOCK_KEY"] = "42"
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.InstanceID != "api-1" {
		t.Fatalf("InstanceID=%q want api-1", cfg.InstanceID)
	}
	if cfg.LeaderLockKey != 42 {
		t.Fatalf("LeaderLockKey=%d want 42", cfg.LeaderLockKey)
	}

	env["EZTEST_LEADER_LOCK_KEY"] = "-7"
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LeaderLockKey != -7 {
		t.Fatalf("LeaderLockKey=%d want -7", cfg.LeaderLockKey)
	}
}

// TestLeaderInstanceIDLengthBoundary counts characters, not bytes: 64
// multi-byte runes fit, 65 do not.
func TestLeaderInstanceIDLengthBoundary(t *testing.T) {
	env := validEnv()
	env["EZTEST_INSTANCE_ID"] = strings.Repeat("é", 64)
	if _, err := Load(func(k string) string { return env[k] }); err != nil {
		t.Fatalf("64 runes must be accepted: %v", err)
	}

	env["EZTEST_INSTANCE_ID"] = strings.Repeat("é", 65)
	if _, err := Load(func(k string) string { return env[k] }); err == nil {
		t.Fatal("65 runes must be rejected")
	}
}

// TestLeaderRejectsInvalidSettings names the offending variable so a typo is
// fixable from the boot error.
func TestLeaderRejectsInvalidSettings(t *testing.T) {
	cases := []struct {
		key   string
		value string
	}{
		{"EZTEST_INSTANCE_ID", strings.Repeat("x", 65)},
		{"EZTEST_LEADER_LOCK_KEY", "not-a-key"},
		{"EZTEST_LEADER_LOCK_KEY", "9223372036854775808"},
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

// TestDefaultInstanceIDIsBounded proves the hostname+pid fallback always fits
// the documented 64-character limit even on a long hostname, and keeps the pid
// suffix that distinguishes co-located instances.
func TestDefaultInstanceIDIsBounded(t *testing.T) {
	longHost := strings.Repeat("h", 200)
	id := instanceIDFor(longHost, 4242)
	if utf8.RuneCountInString(id) > 64 {
		t.Fatalf("default instance id %q exceeds 64 characters", id)
	}
	if !strings.HasSuffix(id, "-4242") {
		t.Fatalf("default instance id %q lost the pid suffix", id)
	}

	if got := instanceIDFor("", 7); got != "-7" {
		t.Fatalf("empty hostname id=%q want -7", got)
	}
}
