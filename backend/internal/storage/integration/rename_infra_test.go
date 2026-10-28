//go:build integration

package integration_test

import (
	"context"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// TestP5RenameInfraTables proves the D34 rename migrations for the P0
// infrastructure contexts landed: every old plural table name is gone and the
// new singular name exists. It inspects the migrated schema only; column,
// constraint and index checks belong to the owning context's tests.
func TestP5RenameInfraTables(t *testing.T) {
	pool := testutil.OpenTestDB(t) // migrations + truncate
	ctx := context.Background()
	pairs := [][2]string{
		{"user_account", "users"}, {"auth_token", "auth_tokens"},
		{"idempotency_key", "idempotency_keys"},
		{"event", "events"}, {"event_channel", "event_channels"},
		{"audit_log", "audit_logs"}, {"llm_config", "llm_configs"},
		{"storage_object", "storage_objects"},
	}
	for _, p := range pairs {
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema='public' AND table_name=$1`, p[0]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Fatalf("new table %s missing", p[0])
		}
		if err := pool.QueryRow(ctx, `SELECT count(*) FROM information_schema.tables
			WHERE table_schema='public' AND table_name=$1`, p[1]).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("old table %s still present", p[1])
		}
	}
}
