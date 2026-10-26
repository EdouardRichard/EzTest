//go:build integration

package db_test

import (
	"context"
	"errors"
	"testing"
	"testing/fstest"

	"github.com/EdouardRichard/EzTest/backend/internal/db"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

func TestMigrateIdempotentAndChecksum(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	fsys := fstest.MapFS{
		"migrations/order.txt":       {Data: []byte("0001_test_a.sql\n0002_test_b.sql\n")},
		"migrations/0001_test_a.sql": {Data: []byte("CREATE TABLE test_a (id INT PRIMARY KEY);")},
		"migrations/0002_test_b.sql": {Data: []byte("CREATE TABLE test_b (id INT PRIMARY KEY);")},
	}
	if err := db.Migrate(ctx, pool, fsys); err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx, pool, fsys); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("migrations=%d", n)
	}
	// 篡改已应用文件后应失败
	fsys["migrations/0001_test_a.sql"] = &fstest.MapFile{Data: []byte("CREATE TABLE changed (id INT);")}
	if err := db.Migrate(ctx, pool, fsys); !errors.Is(err, db.ErrMigrationChecksum) {
		t.Fatalf("err=%v, want ErrMigrationChecksum", err)
	}
}

// TestMigrateEmbeddedIsIdempotent exercises the embedded migration set
// (migrations/order.txt is empty in this task) and proves repeated startup
// migrations are a no-op.
func TestMigrateEmbeddedIsIdempotent(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatal(err)
	}
	if err := db.MigrateEmbedded(ctx, pool); err != nil {
		t.Fatal(err)
	}
}
