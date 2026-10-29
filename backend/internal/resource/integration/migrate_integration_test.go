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
// company row the migration seeds.
func TestResourceMigrationsCreateTablesAndSeedCompany(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	ctx := context.Background()
	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatalf("migrate embedded: %v", err)
	}

	var companyID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM company WHERE key='default'`).Scan(&companyID); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"system", "environment", "app_model_node", "account", "account_system_access", "session", "session_snapshot", "session_lease", "manual_takeover", "code_repository"} {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM `+table).Scan(&n); err != nil {
			t.Fatalf("%s: %v", table, err)
		}
	}
}
