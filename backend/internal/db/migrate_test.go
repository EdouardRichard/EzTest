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
	if len(migrations) != 15 {
		t.Fatalf("len = %d, want 15", len(migrations))
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
	if migrations[len(migrations)-1].Name != "eventing_0002_event_index.sql" {
		t.Fatalf("last migration = %q", migrations[len(migrations)-1].Name)
	}
}
