//go:build integration

package testutil_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5"
)

// testDBLockID is the advisory lock documented in testutil/db.go. It is kept
// here as a literal so the test also pins the lock id (and that it is not the
// migration runner's 4242).
const testDBLockID = 4243

// TestOpenRawTestDBHoldsAndReleasesAdvisoryLock proves the cross-package
// serialisation contract of the helpers: the helper holds advisory lock 4243
// for the whole test (here observed through pg_locks from another pool
// connection), and releases it when the test's cleanup runs. Go runs different
// packages' test binaries in parallel, so a second package's helper blocks on
// this lock until the first package's test finishes, instead of dropping the
// first package's tables mid-test.
func TestOpenRawTestDBHoldsAndReleasesAdvisoryLock(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()

	var holderPID int32
	t.Run("holds lock while test is running", func(t *testing.T) {
		pool := testutil.OpenRawTestDB(t)
		if err := pool.QueryRow(ctx, `
			SELECT pid
			FROM pg_locks
			WHERE locktype = 'advisory'
			  AND classid = 0
			  AND objid = $1
			  AND objsubid = 1
			  AND granted`, testDBLockID).Scan(&holderPID); err != nil {
			t.Fatalf("advisory lock %d is not held by the helper: %v", testDBLockID, err)
		}
	})
	// The subtest's cleanup has run by now (LIFO: truncate, unlock/release,
	// pool close). Inspect from a separate session so the check itself does not
	// take the lock.
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatalf("connect to test database: %v", err)
	}
	defer func() {
		_ = conn.Close(ctx)
	}()

	var stillHeld int
	if err := conn.QueryRow(ctx, `
		SELECT count(*)
		FROM pg_locks
		WHERE locktype = 'advisory'
		  AND classid = 0
		  AND objid = $1
		  AND pid = $2`, testDBLockID, holderPID).Scan(&stillHeld); err != nil {
		t.Fatalf("query advisory lock %d after cleanup: %v", testDBLockID, err)
	}
	if stillHeld != 0 {
		t.Fatalf("advisory lock %d is still held by pid %d after cleanup", testDBLockID, holderPID)
	}
}
