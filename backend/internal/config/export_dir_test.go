package config

import "testing"

// TestLoadExportDir pins the P5 export root (D30): EZTEST_EXPORT_DIR defaults to
// .data/exports and can be overridden by the environment.
func TestLoadExportDir(t *testing.T) {
	env := validEnv()
	cfg, err := Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExportDir != ".data/exports" {
		t.Fatalf("ExportDir=%q, want .data/exports", cfg.ExportDir)
	}

	env["EZTEST_EXPORT_DIR"] = "/data/exports"
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExportDir != "/data/exports" {
		t.Fatalf("ExportDir=%q, want /data/exports", cfg.ExportDir)
	}

	// A blank value falls back to the documented default instead of disabling
	// the export endpoints.
	env["EZTEST_EXPORT_DIR"] = "   "
	cfg, err = Load(func(k string) string { return env[k] })
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ExportDir != ".data/exports" {
		t.Fatalf("ExportDir=%q, want the default for a blank value", cfg.ExportDir)
	}
}
