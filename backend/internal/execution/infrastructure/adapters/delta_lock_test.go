package adapters

// W1-12 fix: the discovery delta lock key is a stable 64-bit hash of
// (version_id, wave_id). Two cooperative processes must compute the same key
// for the same wave and different keys for different waves/versions.
//
// W1-13 fix: a failed unlock must be logged and the pooled session must be
// force-closed. A session advisory lock lives on the connection, so returning a
// still-locked connection to the pool leaks the lock forever.

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestDiscoveryDeltaLockKeyIsStable(t *testing.T) {
	key := discoveryDeltaLockKey(7, 3)
	if key == 0 {
		t.Fatal("lock key must not be zero")
	}
	if again := discoveryDeltaLockKey(7, 3); again != key {
		t.Fatalf("lock key changed between calls: %d != %d", key, again)
	}
	if other := discoveryDeltaLockKey(7, 4); other == key {
		t.Fatalf("lock key for wave 4 equals wave 3: %d", key)
	}
	if other := discoveryDeltaLockKey(8, 3); other == key {
		t.Fatalf("lock key for version 8 equals version 7: %d", key)
	}
	t.Logf("discovery delta lock key(7,3)=%d key(7,4)=%d key(8,3)=%d",
		key, discoveryDeltaLockKey(7, 4), discoveryDeltaLockKey(8, 3))
}

type fakeDeltaLockConn struct {
	unlockErr error
	closed    int
	released  int
	// events records the ordered calls so the test pins "close before release":
	// releasing first would hand the still-locked session back to the pool.
	events []string
}

func (c *fakeDeltaLockConn) record(event string) {
	c.events = append(c.events, event)
}

func (c *fakeDeltaLockConn) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	c.record("unlock")
	return pgconn.CommandTag{}, c.unlockErr
}

func (c *fakeDeltaLockConn) Close(context.Context) error {
	c.closed++
	c.record("close")
	return nil
}

func (c *fakeDeltaLockConn) Release() {
	c.released++
	c.record("release")
}

func assertDeltaLockEvents(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("delta lock calls = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("delta lock calls = %v, want %v (order must be %v)", got, want, want)
		}
	}
}

func captureDefaultLogger(t *testing.T) *bytes.Buffer {
	t.Helper()
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &logs
}

func TestReleaseDiscoveryDeltaLockClosesConnectionWhenUnlockFails(t *testing.T) {
	logs := captureDefaultLogger(t)
	conn := &fakeDeltaLockConn{unlockErr: errors.New("connection reset by peer")}

	releaseDiscoveryDeltaLock(context.Background(), conn, 42)

	if conn.closed != 1 {
		t.Fatalf("closed = %d, want 1 (a session lock must never go back to the pool)", conn.closed)
	}
	if conn.released != 1 {
		t.Fatalf("released = %d, want 1", conn.released)
	}
	// The connection must be closed before it is released: Release returns it
	// to the pool, and a still-locked session handed back to the pool leaks the
	// advisory lock forever.
	assertDeltaLockEvents(t, conn.events, []string{"unlock", "close", "release"})
	if !strings.Contains(logs.String(), "unlock failed") {
		t.Fatalf("unlock error was not logged: %s", logs.String())
	}
}

func TestReleaseDiscoveryDeltaLockKeepsHealthyConnection(t *testing.T) {
	captureDefaultLogger(t)
	conn := &fakeDeltaLockConn{}

	releaseDiscoveryDeltaLock(context.Background(), conn, 42)

	if conn.closed != 0 {
		t.Fatalf("closed = %d, want 0 for a successful unlock", conn.closed)
	}
	if conn.released != 1 {
		t.Fatalf("released = %d, want 1", conn.released)
	}
	assertDeltaLockEvents(t, conn.events, []string{"unlock", "release"})
}
