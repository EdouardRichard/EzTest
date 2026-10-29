// Package leader serialises the periodic loops of multiple EzTest API
// instances with a PostgreSQL session-level advisory lock (design spec §10.5).
//
// Every tick calls Try: the instance that owns the lock runs the tick, every
// other instance skips it. The lock is held on a dedicated pooled connection
// and released when the tick ends, so a crashed instance releases it with its
// database session.
package leader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// DefaultLockKey is the advisory lock key shared by the scheduler, the
	// autonomous worker and the maintenance entry (design spec §10.5/§11.4).
	DefaultLockKey int64 = 1163411512
	// unlockTimeout bounds the detached unlock issued after a tick's context
	// was canceled (graceful shutdown). A connection that cannot be unlocked is
	// closed instead so PostgreSQL releases the session lock.
	unlockTimeout = 5 * time.Second
)

const (
	tryLockSQL    = "SELECT pg_try_advisory_lock($1)"
	unlockLockSQL = "SELECT pg_advisory_unlock($1)"
)

// Locker takes a named PostgreSQL advisory lock for one tick.
type Locker struct {
	pool       *pgxpool.Pool
	instanceID string
	logger     *slog.Logger
	// unlockTimeout is overridable by tests; production keeps the default.
	unlockTimeout time.Duration
}

// New builds the leader locker. instanceID only appears in the lock
// diagnostics; the composition root passes the configured EZTEST_INSTANCE_ID.
func New(pool *pgxpool.Pool, instanceID string, logger *slog.Logger) *Locker {
	if logger == nil {
		logger = slog.Default()
	}
	return &Locker{
		pool:          pool,
		instanceID:    strings.TrimSpace(instanceID),
		logger:        logger,
		unlockTimeout: unlockTimeout,
	}
}

// Try attempts to acquire the session advisory lock for key without blocking.
//
// When the lock is acquired, Try returns a release function that unlocks and
// returns the connection; the caller must invoke it once the tick finished. The
// release function is idempotent, uses a detached context so a canceled tick
// context still releases the lock, and closes the connection if the unlock
// cannot be confirmed (a returned session must never keep the lock).
//
// When another instance holds the key, Try returns (nil, false, nil) and the
// caller skips the tick; this is logged at debug level only.
func (l *Locker) Try(ctx context.Context, key int64) (func(), bool, error) {
	if l == nil || l.pool == nil {
		return nil, false, errors.New("leader: database pool is not configured")
	}
	conn, err := l.pool.Acquire(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("leader: acquire lock connection: %w", err)
	}
	var locked bool
	if err := conn.QueryRow(ctx, tryLockSQL, key).Scan(&locked); err != nil {
		conn.Release()
		return nil, false, fmt.Errorf("leader: try advisory lock %d: %w", key, err)
	}
	if !locked {
		conn.Release()
		l.logger.DebugContext(ctx, "leader: lock held by another instance; skipping tick",
			"instance_id", l.instanceID, "lock_key", key)
		return nil, false, nil
	}

	l.logger.DebugContext(ctx, "leader: lock acquired",
		"instance_id", l.instanceID, "lock_key", key)
	var once sync.Once
	return func() {
		once.Do(func() { l.unlock(conn, key) })
	}, true, nil
}

// unlock releases the advisory lock on its owning connection and returns the
// connection to the pool. Unlocking must happen on the same session that
// acquired it, so the connection is held for the whole tick.
func (l *Locker) unlock(conn *pgxpool.Conn, key int64) {
	timeout := l.unlockTimeout
	if timeout <= 0 {
		timeout = unlockTimeout
	}
	unlockCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var unlocked bool
	if err := conn.QueryRow(unlockCtx, unlockLockSQL, key).Scan(&unlocked); err != nil {
		// The pool must not receive a connection that may still hold the lock:
		// close the session so PostgreSQL releases its advisory lock.
		l.closeSession(conn)
		l.logger.Warn("leader: unlock failed; closed the lock connection",
			"instance_id", l.instanceID, "lock_key", key, "error", err)
		return
	}
	conn.Release()
	l.logger.Debug("leader: lock released",
		"instance_id", l.instanceID, "lock_key", key, "unlocked", unlocked)
}

// closeSession hijacks and closes the lock connection. Hijack prevents the pool
// from handing a session that may still own the advisory lock to another tick.
func (l *Locker) closeSession(conn *pgxpool.Conn) {
	raw := conn.Hijack()
	if raw == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(context.Background(), unlockTimeout)
	defer cancel()
	if err := raw.Close(closeCtx); err != nil {
		// A failed close is best effort: the process is shutting down or the
		// socket is already gone, and PostgreSQL releases the lock either way.
		l.logger.Warn("leader: close lock connection failed",
			"instance_id", l.instanceID, "error", err)
	}
}
