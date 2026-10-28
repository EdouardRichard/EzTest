package config

import (
	"strings"
	"testing"
)

// TestLoadExecutionDefaults pins the P6 execution defaults (spec §5.3, §5.5,
// §5.7): executor URL/token, evidence root and every concurrency/budget/limit
// setting.
func TestLoadExecutionDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutorURL != "http://127.0.0.1:13336" {
		t.Fatalf("ExecutorURL=%q", cfg.ExecutorURL)
	}
	if cfg.ExecutorToken != "" {
		t.Fatalf("ExecutorToken=%q want empty default", cfg.ExecutorToken)
	}
	if cfg.EvidenceDir != ".data/evidence" {
		t.Fatalf("EvidenceDir=%q", cfg.EvidenceDir)
	}
	if cfg.ExecMaxConcurrentCases != 3 {
		t.Fatalf("ExecMaxConcurrentCases=%d", cfg.ExecMaxConcurrentCases)
	}
	if cfg.ExecMaxToolCalls != 80 {
		t.Fatalf("ExecMaxToolCalls=%d", cfg.ExecMaxToolCalls)
	}
	if cfg.ExecCaseTimeoutMS != 900000 {
		t.Fatalf("ExecCaseTimeoutMS=%d", cfg.ExecCaseTimeoutMS)
	}
	if cfg.ExecMaxCasesPerBatch != 200 {
		t.Fatalf("ExecMaxCasesPerBatch=%d", cfg.ExecMaxCasesPerBatch)
	}
	if cfg.ExecWorkerIntervalMS != 2000 {
		t.Fatalf("ExecWorkerIntervalMS=%d", cfg.ExecWorkerIntervalMS)
	}
	if cfg.ExecEvidenceMaxBytes != 10<<20 {
		t.Fatalf("ExecEvidenceMaxBytes=%d", cfg.ExecEvidenceMaxBytes)
	}
	if cfg.ExecEvidencePerCase != 100 {
		t.Fatalf("ExecEvidencePerCase=%d", cfg.ExecEvidencePerCase)
	}
}

// TestLoadExecutionOverrides proves every P6 setting is read from its env var.
func TestLoadExecutionOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_EXECUTOR_URL"] = "http://executor:13336"
	env["EZTEST_EXECUTOR_TOKEN"] = "exec-token"
	env["EZTEST_EVIDENCE_DIR"] = "/data/evidence"
	env["EZTEST_EXEC_MAX_CONCURRENT_CASES"] = "8"
	env["EZTEST_EXEC_MAX_TOOL_CALLS"] = "120"
	env["EZTEST_EXEC_CASE_TIMEOUT_MS"] = "600000"
	env["EZTEST_EXEC_MAX_CASES_PER_BATCH"] = "50"
	env["EZTEST_EXEC_WORKER_INTERVAL_MS"] = "500"
	env["EZTEST_EXEC_EVIDENCE_MAX_BYTES"] = "1048576"
	env["EZTEST_EXEC_EVIDENCE_PER_CASE"] = "20"

	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExecutorURL != "http://executor:13336" || cfg.ExecutorToken != "exec-token" {
		t.Fatalf("executor=%q token=%q", cfg.ExecutorURL, cfg.ExecutorToken)
	}
	if cfg.EvidenceDir != "/data/evidence" {
		t.Fatalf("EvidenceDir=%q", cfg.EvidenceDir)
	}
	if cfg.ExecMaxConcurrentCases != 8 || cfg.ExecMaxToolCalls != 120 ||
		cfg.ExecCaseTimeoutMS != 600000 || cfg.ExecMaxCasesPerBatch != 50 ||
		cfg.ExecWorkerIntervalMS != 500 || cfg.ExecEvidenceMaxBytes != 1048576 ||
		cfg.ExecEvidencePerCase != 20 {
		t.Fatalf("execution overrides not applied: %+v", cfg)
	}
}

// TestLoadExecutionBoundsRejectOutOfRange proves each bounded P6 setting names
// its variable in the error instead of silently accepting a bad value.
func TestLoadExecutionBoundsRejectOutOfRange(t *testing.T) {
	cases := map[string]string{
		"EZTEST_EXEC_MAX_CONCURRENT_CASES": "9",
		"EZTEST_EXEC_MAX_TOOL_CALLS":       "0",
		"EZTEST_EXEC_CASE_TIMEOUT_MS":      "10",
		"EZTEST_EXEC_MAX_CASES_PER_BATCH":  "1001",
		"EZTEST_EXEC_WORKER_INTERVAL_MS":   "10",
		"EZTEST_EXEC_EVIDENCE_MAX_BYTES":   "1",
		"EZTEST_EXEC_EVIDENCE_PER_CASE":    "5000",
	}
	for key, value := range cases {
		env := validEnv()
		env[key] = value
		_, err := Load(func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s=%s: err=%v want a bound error naming the variable", key, value, err)
		}
	}
}
