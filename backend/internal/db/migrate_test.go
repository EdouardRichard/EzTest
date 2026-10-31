package db

import (
	"strings"
	"testing"
)

func TestOrderCoversEverySQLFile(t *testing.T) {
	migrations, err := orderedMigrations()
	if err != nil {
		t.Fatalf("orderedMigrations: %v", err)
	}
	if len(migrations) != 24 {
		t.Fatalf("len = %d, want 24", len(migrations))
	}
	for _, item := range migrations {
		if !strings.HasSuffix(item.Name, ".sql") {
			t.Fatalf("migration %q does not end in .sql", item.Name)
		}
		if len(item.Checksum) != 64 {
			t.Fatalf("migration %q checksum = %q", item.Name, item.Checksum)
		}
		if strings.TrimSpace(item.SQL) == "" {
			t.Fatalf("migration %q is empty", item.Name)
		}
	}
	if migrations[0].Name != "settings_0001_settings.sql" {
		t.Fatalf("first migration = %q", migrations[0].Name)
	}
	wantTail := []string{
		"delivery_0001_issue_lifecycle.sql",
		"delivery_0002_report_revision.sql",
		"delivery_0003_analysis_run.sql",
		"version_0005_requirement_closure.sql",
	}
	tail := migrations[len(migrations)-len(wantTail):]
	for i, want := range wantTail {
		if tail[i].Name != want {
			t.Fatalf("migration %d = %q, want %q", len(migrations)-len(wantTail)+i+1, tail[i].Name, want)
		}
	}
}
