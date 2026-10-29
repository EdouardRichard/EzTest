package leader_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform/leader"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// discardLogger keeps the lock tests' output pristine while still exercising
// the logging paths.
func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestTryAcquiresReleasesAndReacquires covers the basic contract of the
// PostgreSQL advisory lock used by the multi-instance leader (spec §10.5): a
// second connection cannot take the same key while it is held, releasing makes
// it available again, and the release function is idempotent.
func TestTryAcquiresReleasesAndReacquires(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	locker := leader.New(pool, "lock-test-a", discardLogger())
	ctx := context.Background()
	const key = int64(73001)

	release, ok, err := locker.Try(ctx, key)
	if err != nil {
		t.Fatalf("Try: %v", err)
	}
	if !ok {
		t.Fatal("first Try = false, want true")
	}

	// The same key on a second connection must be refused while held.
	if _, ok, err := locker.Try(ctx, key); err != nil {
		t.Fatalf("second Try: %v", err)
	} else if ok {
		t.Fatal("second Try on a held key = true, want false")
	}

	release()
	release() // idempotent: a double release must not unlock someone else.

	again, ok, err := locker.Try(ctx, key)
	if err != nil {
		t.Fatalf("Try after release: %v", err)
	}
	if !ok {
		t.Fatal("Try after release = false, want true")
	}
	again()
}

// TestTryDifferentKeysAreIndependent proves the lock is scoped to the key, not
// the connection or the instance: the scheduler, autonomy worker and
// maintenance entry use distinct keys without blocking each other.
func TestTryDifferentKeysAreIndependent(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	lockerA := leader.New(pool, "lock-test-a", discardLogger())
	lockerB := leader.New(pool, "lock-test-b", discardLogger())
	ctx := context.Background()

	releaseA, okA, err := lockerA.Try(ctx, 1001)
	if err != nil || !okA {
		t.Fatalf("key 1001 Try = (%v, %v), want (true, nil)", okA, err)
	}
	defer releaseA()

	releaseB, okB, err := lockerB.Try(ctx, 1002)
	if err != nil || !okB {
		t.Fatalf("key 1002 Try = (%v, %v), want (true, nil) despite key 1001 being held", okB, err)
	}
	releaseB()

	// Holding key 1002 must not affect a later acquisition of key 1001.
	if _, okC, err := lockerB.Try(ctx, 1001); err != nil || okC {
		if err != nil {
			t.Fatalf("held key 1001 Try: %v", err)
		}
		t.Fatalf("held key 1001 Try = true, want false")
	}
}

// TestTryIsMutuallyExclusiveAcrossConnections pins the single-winner guarantee:
// concurrent ticks on the same key from independent connections acquire the
// lock exactly once until the winner releases.
func TestTryIsMutuallyExclusiveAcrossConnections(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	locker := leader.New(pool, "lock-test", discardLogger())
	ctx := context.Background()
	const key = int64(73002)
	const contenders = 8

	var acquired atomic.Int32
	attempted := make(chan struct{}, contenders)
	unblock := make(chan struct{})
	errs := make(chan error, contenders)
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, ok, err := locker.Try(ctx, key)
			if err != nil {
				errs <- err
				attempted <- struct{}{}
				return
			}
			if !ok {
				attempted <- struct{}{}
				return
			}
			acquired.Add(1)
			attempted <- struct{}{}
			// Hold the lock until every contender has tried, so a slow
			// goroutine cannot acquire it after the winner already released.
			<-unblock
			release()
		}()
	}
	for i := 0; i < contenders; i++ {
		<-attempted
	}
	close(unblock)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent Try: %v", err)
	}
	if got := acquired.Load(); got != 1 {
		t.Fatalf("locked contenders = %d, want exactly 1", got)
	}
}

// TestTryTimesOutWhenNoConnectionIsAvailable proves a tick can never hang on
// pool exhaustion: when the single pool connection is held elsewhere, Try
// reports the context deadline instead of blocking.
func TestTryTimesOutWhenNoConnectionIsAvailable(t *testing.T) {
	url := strings.TrimSpace(os.Getenv("EZTEST_TEST_DATABASE_URL"))
	if url == "" {
		t.Skip("EZTEST_TEST_DATABASE_URL is not set")
	}
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatalf("parse test database url: %v", err)
	}
	cfg.MaxConns = 1
	limited, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("open limited pool: %v", err)
	}
	defer limited.Close()
	held, err := limited.Acquire(context.Background())
	if err != nil {
		t.Fatalf("hold the only connection: %v", err)
	}
	defer held.Release()

	locker := leader.New(limited, "lock-test", discardLogger())
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	release, ok, err := locker.Try(ctx, 73003)
	if release != nil {
		release()
	}
	if err == nil || ok {
		t.Fatalf("Try with exhausted pool = (%v, %v), want (false, error)", ok, err)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Try error = %v, want context.DeadlineExceeded", err)
	}
}

// TestReleaseUnlocksAfterContextCancellation protects the graceful-shutdown
// path: the tick context is canceled first, but the release closure must still
// reach PostgreSQL and free the key for another instance.
func TestReleaseUnlocksAfterContextCancellation(t *testing.T) {
	pool := testutil.OpenRawTestDB(t)
	locker := leader.New(pool, "lock-test", discardLogger())
	other := leader.New(pool, "lock-test-other", discardLogger())
	const key = int64(73004)

	ctx, cancel := context.WithCancel(context.Background())
	release, ok, err := locker.Try(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Try = (%v, %v), want (true, nil)", ok, err)
	}
	cancel()
	release()

	releaseOther, ok, err := other.Try(context.Background(), key)
	if err != nil {
		t.Fatalf("Try after canceled-context release: %v", err)
	}
	if !ok {
		t.Fatal("Try after canceled-context release = false, want true")
	}
	releaseOther()
}

// TestTryErrors covers the misconfiguration paths without a live database: a
// nil pool and a closed pool both fail closed.
func TestTryErrors(t *testing.T) {
	if _, ok, err := leader.New(nil, "lock-test", discardLogger()).Try(context.Background(), 73005); err == nil || ok {
		t.Fatalf("nil pool Try = (%v, %v), want (false, error)", ok, err)
	}

	pool, err := pgxpool.New(context.Background(), "postgres://user:pass@127.0.0.1:1/eztest")
	if err != nil {
		t.Fatalf("build closed pool: %v", err)
	}
	pool.Close()
	if _, ok, err := leader.New(pool, "lock-test", discardLogger()).Try(context.Background(), 73005); err == nil || ok {
		t.Fatalf("closed pool Try = (%v, %v), want (false, error)", ok, err)
	}
}
