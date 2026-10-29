package config

import (
	"strings"
	"testing"
)

// TestLoadP7WiringDefaults pins the P7 delivery/db-assert defaults of spec
// §16.1/D30.
func TestLoadP7WiringDefaults(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReportDir != ".data/reports" {
		t.Fatalf("ReportDir=%q want .data/reports", cfg.ReportDir)
	}
	if cfg.DBResultMaxBytes != 1048576 {
		t.Fatalf("DBResultMaxBytes=%d want 1 MiB", cfg.DBResultMaxBytes)
	}
	if cfg.DBSQLMaxBytes != 16384 {
		t.Fatalf("DBSQLMaxBytes=%d want 16 KiB", cfg.DBSQLMaxBytes)
	}
	if cfg.AnalysisMaxCases != 20 {
		t.Fatalf("AnalysisMaxCases=%d want the default 20", cfg.AnalysisMaxCases)
	}
	if cfg.SupplementMaxCases != 5 {
		t.Fatalf("SupplementMaxCases=%d want the default 5", cfg.SupplementMaxCases)
	}
}

// TestLoadP7WiringOverrides proves the report root and the db-assert bounds are
// read from their environment variables.
func TestLoadP7WiringOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_REPORT_DIR"] = "/data/reports"
	env["EZTEST_DB_RESULT_MAX_BYTES"] = "2097152"
	env["EZTEST_DB_SQL_MAX_BYTES"] = "65536"
	env["EZTEST_ANALYSIS_MAX_CASES"] = "33"
	env["EZTEST_SUPPLEMENT_MAX_CASES"] = "9"

	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ReportDir != "/data/reports" {
		t.Fatalf("ReportDir=%q", cfg.ReportDir)
	}
	if cfg.DBResultMaxBytes != 2097152 || cfg.DBSQLMaxBytes != 65536 {
		t.Fatalf("db limits=%d/%d", cfg.DBResultMaxBytes, cfg.DBSQLMaxBytes)
	}
	if cfg.AnalysisMaxCases != 33 || cfg.SupplementMaxCases != 9 {
		t.Fatalf("analysis/supplement caps=%d/%d want 33/9",
			cfg.AnalysisMaxCases, cfg.SupplementMaxCases)
	}
}

// TestLoadP7AutonomyBatchesPerTick pins the §16.1 bounds of the autonomous
// worker budget: the default is 4, the accepted range is 1–20 and an
// out-of-range upper value is clamped to 20 (instead of disabling the worker).
func TestLoadP7AutonomyBatchesPerTick(t *testing.T) {
	cfg, err := Load(func(k string) string { return validEnv()[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutonomyBatchesPerTick != 4 {
		t.Fatalf("AutonomyBatchesPerTick=%d want the default 4", cfg.AutonomyBatchesPerTick)
	}

	env := validEnv()
	env["EZTEST_AUTONOMY_BATCHES_PER_TICK"] = "20"
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AutonomyBatchesPerTick != 20 {
		t.Fatalf("AutonomyBatchesPerTick=%d want 20", cfg.AutonomyBatchesPerTick)
	}

	// An over-max value is clamped to the spec ceiling, not rejected.
	env = validEnv()
	env["EZTEST_AUTONOMY_BATCHES_PER_TICK"] = "21"
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("AutonomyBatchesPerTick=21: %v want it clamped to 20", err)
	}
	if cfg.AutonomyBatchesPerTick != 20 {
		t.Fatalf("AutonomyBatchesPerTick=%d want the clamp to 20", cfg.AutonomyBatchesPerTick)
	}

	for _, value := range []string{"0", "-1", "abc"} {
		env := validEnv()
		env["EZTEST_AUTONOMY_BATCHES_PER_TICK"] = value
		if _, err := Load(func(k string) string { return env[k] }); err == nil ||
			!strings.Contains(err.Error(), "EZTEST_AUTONOMY_BATCHES_PER_TICK") {
			t.Fatalf("value %q: err=%v want a bound error naming the variable", value, err)
		}
	}
}

// TestLoadP7WiringBoundsRejectOutOfRange proves each bounded P7 setting names its
// variable in the error.
func TestLoadP7WiringBoundsRejectOutOfRange(t *testing.T) {
	cases := map[string]string{
		"EZTEST_DB_RESULT_MAX_BYTES":  "1024",
		"EZTEST_DB_SQL_MAX_BYTES":     "1023",
		"EZTEST_ANALYSIS_MAX_CASES":   "0",
		"EZTEST_SUPPLEMENT_MAX_CASES": "0",
	}
	for key, value := range cases {
		env := validEnv()
		env[key] = value
		_, err := Load(func(k string) string { return env[k] })
		if err == nil || !strings.Contains(err.Error(), key) {
			t.Fatalf("%s=%s: err=%v want a bound error naming the variable", key, value, err)
		}
	}
	env := validEnv()
	env["EZTEST_DB_RESULT_MAX_BYTES"] = "16777217"
	env["EZTEST_DB_SQL_MAX_BYTES"] = "65537"
	if _, err := Load(func(k string) string { return env[k] }); err == nil ||
		!strings.Contains(err.Error(), "EZTEST_DB_RESULT_MAX_BYTES") {
		t.Fatalf("err=%v want the over-max result bound error", err)
	}
	for key, value := range map[string]string{
		"EZTEST_ANALYSIS_MAX_CASES":   "51",
		"EZTEST_SUPPLEMENT_MAX_CASES": "21",
	} {
		env := validEnv()
		env[key] = value
		if _, err := Load(func(k string) string { return env[k] }); err == nil ||
			!strings.Contains(err.Error(), key) {
			t.Fatalf("%s=%s: err=%v want the over-max bound error", key, value, err)
		}
	}
}
