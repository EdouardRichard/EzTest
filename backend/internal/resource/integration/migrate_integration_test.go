//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestResourceMigrationsCreateTablesAndSeedCompany applies the embedded
// migrations to a clean schema and asserts the 11 P2 tables plus the seeded
// default company. It uses OpenRawTestDB instead of OpenTestDB because
// OpenTestDB truncates every table after migrating, which would remove the
// companies row the migration seeds.
func TestResourceMigrationsCreateTablesAndSeedCompany(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	ctx := context.Background()
	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatalf("migrate embedded: %v", err)
	}

	var companyID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM companies WHERE key='default'`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"systems", "environments", "app_model_nodes", "accounts", "account_system_accesses", "sessions", "session_snapshots", "session_leases", "manual_takeovers", "code_repositories"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
}
