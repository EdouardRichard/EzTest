package adapters

// W1-12 fix: one delta evaluation per (version, wave) across cooperative
// processes. This adapter implements versionapp.DeltaLocker with a PostgreSQL
// session-level advisory lock held on a dedicated pooled connection. A failed
// try-lock maps to versionapp.ErrDeltaBusy so the caller (W1-14) can skip the
// pass instead of failing the wave.

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
)

// deltaLockCloseTimeout bounds the force-close of a connection whose unlock
// failed. Closing the session is what releases a session advisory lock, so a
// failure here is only a degraded log, never a blocked evaluation.
const deltaLockCloseTimeout = 5 * time.Second

// DiscoveryDeltaLocker holds the (version, wave) delta lock on PostgreSQL.
type DiscoveryDeltaLocker struct {
	pool *pgxpool.Pool
}

// NewDiscoveryDeltaLocker wires the delta lock over the shared pool.
func NewDiscoveryDeltaLocker(pool *pgxpool.Pool) *DiscoveryDeltaLocker {
	return &DiscoveryDeltaLocker{pool: pool}
}

var _ versionapp.DeltaLocker = (*DiscoveryDeltaLocker)(nil)

// discoveryDeltaLockKey derives a stable 64-bit advisory-lock key from
// (version_id, wave_id). FNV-1a is deterministic across processes and
// platforms; the prefix keeps the key space disjoint from the other EzTest
// advisory locks, which use distinct prefixes.
func discoveryDeltaLockKey(versionID, waveID int64) int64 {
	hasher := fnv.New64a()
	_, _ = fmt.Fprintf(hasher, "eztest:discovery-delta:%d:%d", versionID, waveID)
	return int64(hasher.Sum64())
}

// LockDelta takes the wave delta try-lock. On success the returned release
// function unlocks on the same connection and releases it to the pool; it must
// be called exactly once (normally with defer). ErrDeltaBusy reports a lock
// already held by another cooperative evaluation.
func (l *DiscoveryDeltaLocker) LockDelta(ctx context.Context, versionID, waveID int64) (func(), error) {
	if l == nil || l.pool == nil {
		return nil, errors.New("discovery delta lock: missing pool")
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("discovery delta lock: acquire: %w", err)
	}
	key := discoveryDeltaLockKey(versionID, waveID)
	var acquired bool
	if err := conn.QueryRow(ctx, `SELECT pg_try_advisory_lock($1)`, key).Scan(&acquired); err != nil {
		conn.Release()
		return nil, fmt.Errorf("discovery delta lock: try lock: %w", err)
	}
	if !acquired {
		conn.Release()
		return nil, versionapp.ErrDeltaBusy
	}
	return func() {
		// Release on a background context: the lock must be returned even when
		// the evaluation context was canceled.
		releaseDiscoveryDeltaLock(context.Background(), pooledDeltaLockConn{conn: conn}, key)
	}, nil
}

// deltaLockConn is the pooled-connection surface used by the release path. It
// exists so the leak-prevention behavior is unit-testable without PostgreSQL.
type deltaLockConn interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Close(ctx context.Context) error
	Release()
}

// pooledDeltaLockConn adapts a *pgxpool.Conn: Close closes the underlying
// session, so the subsequent Release makes the pool destroy it instead of
// handing a still-locked session to the next caller.
type pooledDeltaLockConn struct {
	conn *pgxpool.Conn
}

func (c pooledDeltaLockConn) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return c.conn.Exec(ctx, sql, args...)
}

func (c pooledDeltaLockConn) Close(ctx context.Context) error {
	return c.conn.Conn().Close(ctx)
}

func (c pooledDeltaLockConn) Release() {
	c.conn.Release()
}

// releaseDiscoveryDeltaLock returns a held advisory lock and then releases (or,
// after an unlock failure, destroys) the pooled connection. A session advisory
// lock lives on the connection: returning it still locked would leak the lock
// for every future caller, so an unlock error force-closes the session.
func releaseDiscoveryDeltaLock(ctx context.Context, conn deltaLockConn, key int64) {
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, key); err != nil {
		slog.Default().ErrorContext(ctx,
			"discovery delta lock: unlock failed; closing the pooled connection to avoid a session-lock leak",
			"lock_key", key, "error", err)
		closeCtx, cancel := context.WithTimeout(context.Background(), deltaLockCloseTimeout)
		if closeErr := conn.Close(closeCtx); closeErr != nil {
			slog.Default().WarnContext(ctx,
				"discovery delta lock: force close failed",
				"lock_key", key, "error", closeErr)
		}
		cancel()
	}
	conn.Release()
}
