//go:build integration

package idempotency_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/idempotency"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

func createTestUser(t *testing.T, ctx context.Context, pool *pgxpool.Pool, username string) int64 {
	t.Helper()
	var userID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO user_account (username, password_hash, role, status) VALUES ($1, $2, 'user', 'active') RETURNING id`,
		username, "not-a-real-hash").Scan(&userID); err != nil {
		t.Fatalf("insert user: %v", err)
	}
	return userID
}

func countIdempotencyRows(t *testing.T, ctx context.Context, pool *pgxpool.Pool) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM idempotency_key`).Scan(&count); err != nil {
		t.Fatalf("count idempotency rows: %v", err)
	}
	return count
}

func assertJSONEqual(t *testing.T, got, want []byte) {
	t.Helper()
	var gotValue, wantValue any
	if err := json.Unmarshal(got, &gotValue); err != nil {
		t.Fatalf("stored body is not JSON: %v (%s)", err, got)
	}
	if err := json.Unmarshal(want, &wantValue); err != nil {
		t.Fatalf("expected body is not JSON: %v (%s)", err, want)
	}
	gotJSON, _ := json.Marshal(gotValue)
	wantJSON, _ := json.Marshal(wantValue)
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("stored body=%s, want %s", gotJSON, wantJSON)
	}
}

func TestPostgresStoreRoundTripAndFirstSaveWins(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userID := createTestUser(t, ctx, pool, "qa01")
	store := idempotency.NewPostgresStore(pool)

	missing, err := store.Get(ctx, userID, "key_12345678", "POST", "/api/users")
	if err != nil {
		t.Fatalf("Get missing: %v", err)
	}
	if missing != nil {
		t.Fatalf("Get missing=%+v, want nil", missing)
	}

	const requestHash = "hash-one"
	body := []byte(`{"data":{"id":1}}`)
	if err := store.Save(ctx, userID, "key_12345678", "POST", "/api/users", requestHash, 201, body); err != nil {
		t.Fatalf("Save: %v", err)
	}

	stored, err := store.Get(ctx, userID, "key_12345678", "POST", "/api/users")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored == nil {
		t.Fatal("Get returned nil after Save")
	}
	if stored.Status != 201 {
		t.Fatalf("status=%d, want 201", stored.Status)
	}
	if stored.RequestHash != requestHash {
		t.Fatalf("request hash=%q, want %q", stored.RequestHash, requestHash)
	}
	assertJSONEqual(t, stored.Body, body)

	// A second Save for the same scope must not overwrite the first response:
	// the first successful response is the one that is replayed.
	if err := store.Save(ctx, userID, "key_12345678", "POST", "/api/users", "hash-two", 500, []byte(`{"data":{"id":2}}`)); err != nil {
		t.Fatalf("second Save: %v", err)
	}
	stored, err = store.Get(ctx, userID, "key_12345678", "POST", "/api/users")
	if err != nil {
		t.Fatalf("Get after second Save: %v", err)
	}
	if stored.Status != 201 || stored.RequestHash != requestHash {
		t.Fatalf("second Save overwrote the first response: %+v", stored)
	}
	if count := countIdempotencyRows(t, ctx, pool); count != 1 {
		t.Fatalf("rows=%d, want 1", count)
	}
}

func TestPostgresStoreTTLIs24Hours(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userID := createTestUser(t, ctx, pool, "qa01")
	store := idempotency.NewPostgresStore(pool)

	if err := store.Save(ctx, userID, "key_12345678", "POST", "/api/users", "hash", 200, []byte(`{"data":{}}`)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	var ttl time.Duration
	if err := pool.QueryRow(ctx,
		`SELECT expires_at - created_at FROM idempotency_key`).Scan(&ttl); err != nil {
		t.Fatalf("read ttl: %v", err)
	}
	if ttl < 24*time.Hour-time.Minute || ttl > 24*time.Hour+time.Minute {
		t.Fatalf("ttl=%v, want 24h", ttl)
	}
}

func TestPostgresStorePurgesExpiredRecords(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userID := createTestUser(t, ctx, pool, "qa01")
	store := idempotency.NewPostgresStore(pool)

	if err := store.Save(ctx, userID, "key_12345678", "POST", "/api/users", "old-hash", 201, []byte(`{"data":{"id":1}}`)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE idempotency_key SET expires_at = now() - interval '1 second'`); err != nil {
		t.Fatalf("expire row: %v", err)
	}

	stored, err := store.Get(ctx, userID, "key_12345678", "POST", "/api/users")
	if err != nil {
		t.Fatalf("Get expired: %v", err)
	}
	if stored != nil {
		t.Fatalf("expired record was returned: %+v", stored)
	}
	// Get cleans up expired records on the way.
	if count := countIdempotencyRows(t, ctx, pool); count != 0 {
		t.Fatalf("rows after expired Get=%d, want 0", count)
	}

	// Save must also clean up an expired row it is replacing; a sibling CTE
	// would still see the old row and silently drop the new insert.
	if err := store.Save(ctx, userID, "key_12345678", "POST", "/api/users", "new-hash", 201, []byte(`{"data":{"id":2}}`)); err != nil {
		t.Fatalf("Save a new response: %v", err)
	}
	stored, err = store.Get(ctx, userID, "key_12345678", "POST", "/api/users")
	if err != nil {
		t.Fatalf("Get new: %v", err)
	}
	if stored == nil || stored.RequestHash != "new-hash" {
		t.Fatalf("new response was not stored after expiry: %+v", stored)
	}
}

func TestPostgresStoreScopesRecords(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	userOne := createTestUser(t, ctx, pool, "qa01")
	userTwo := createTestUser(t, ctx, pool, "qa02")
	store := idempotency.NewPostgresStore(pool)

	if err := store.Save(ctx, userOne, "key_12345678", "POST", "/api/users", "hash-one", 201, []byte(`{"data":{"id":1}}`)); err != nil {
		t.Fatalf("Save user one: %v", err)
	}
	if err := store.Save(ctx, userTwo, "key_12345678", "POST", "/api/users", "hash-two", 201, []byte(`{"data":{"id":2}}`)); err != nil {
		t.Fatalf("Save user two: %v", err)
	}

	one, err := store.Get(ctx, userOne, "key_12345678", "POST", "/api/users")
	if err != nil || one == nil || one.RequestHash != "hash-one" {
		t.Fatalf("user one lookup: %+v, %v", one, err)
	}
	two, err := store.Get(ctx, userTwo, "key_12345678", "POST", "/api/users")
	if err != nil || two == nil || two.RequestHash != "hash-two" {
		t.Fatalf("user two lookup: %+v, %v", two, err)
	}
	if count := countIdempotencyRows(t, ctx, pool); count != 2 {
		t.Fatalf("rows=%d, want 2", count)
	}
}
