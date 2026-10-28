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
}

// TestLoadP7WiringOverrides proves the report root and the db-assert bounds are
// read from their environment variables.
func TestLoadP7WiringOverrides(t *testing.T) {
	env := validEnv()
	env["EZTEST_REPORT_DIR"] = "/data/reports"
	env["EZTEST_DB_RESULT_MAX_BYTES"] = "2097152"
	env["EZTEST_DB_SQL_MAX_BYTES"] = "65536"

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
}

// TestLoadP7AutonomyBatchesPerTick pins the §16.1 bounds of the autonomous
// worker budget: the default is 4 and the accepted range is 1–20.
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

	for _, value := range []string{"0", "21", "abc"} {
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
		"EZTEST_DB_RESULT_MAX_BYTES": "1024",
		"EZTEST_DB_SQL_MAX_BYTES":    "1023",
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
}
