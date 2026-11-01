package orchestrationpg

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	orchestrationapp "github.com/EdouardRichard/EzTest/backend/internal/orchestration/application"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LeaderLockKey is the P7 advisory-lock key. It is deliberately distinct from
// the migration lock (0x455A5430) so a migration and leader election never
// block each other.
const LeaderLockKey int64 = 0x455A5431

const defaultLeaderRetry = 2 * time.Second

// Leader holds a PostgreSQL session advisory lock on a dedicated connection.
// The lock dies with the session, so a crashed replica never leaves a stale
// leader. Run keeps retrying both while another replica holds the lock and
// after a connection loss, which makes leadership transfer automatic.
type Leader struct {
	pool   *pgxpool.Pool
	retry  time.Duration
	logger *slog.Logger

	mu      sync.RWMutex
	leading bool
}

// LeaderOption customizes the leader loop. Wiring only overrides the logger;
// tests shorten the retry interval.
type LeaderOption func(*Leader)

// WithLeaderLogger sets the structured logger (nil keeps slog.Default()).
func WithLeaderLogger(logger *slog.Logger) LeaderOption {
	return func(l *Leader) {
		if logger != nil {
			l.logger = logger
		}
	}
}

// WithLeaderRetry sets the poll/reconnect interval (default 2s).
func WithLeaderRetry(delay time.Duration) LeaderOption {
	return func(l *Leader) {
		if delay > 0 {
			l.retry = delay
		}
	}
}

// NewLeader builds the advisory-lock leader election helper.
func NewLeader(pool *pgxpool.Pool, options ...LeaderOption) *Leader {
	leader := &Leader{pool: pool, retry: defaultLeaderRetry, logger: slog.Default()}
	for _, option := range options {
		option(leader)
	}
	return leader
}

// IsLeader reports whether this replica currently holds the lock.
func (l *Leader) IsLeader() bool {
	if l == nil {
		return false
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.leading
}

// Run blocks until ctx is cancelled. It acquires the advisory lock when free,
// verifies the session while leading, and reconnects/retries whenever the
// session or the lock is lost.
func (l *Leader) Run(ctx context.Context) error {
	if l == nil || l.pool == nil {
		return fmt.Errorf("orchestration leader is not configured")
	}
	for {
		err := l.hold(ctx)
		l.setLeading(false)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			l.logger.Warn("orchestration leader election retry", "error", err, "delay", l.retry)
		}
		timer := time.NewTimer(l.retry)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

// hold owns one dedicated connection for as long as it lives and returns when
// the connection fails or ctx is cancelled. Closing the connection releases the
// session lock with no explicit unlock required.
func (l *Leader) hold(ctx context.Context) error {
	config := l.pool.Config().ConnConfig.Copy()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return fmt.Errorf("connect leader: %w", err)
	}
	leading := false
	defer func() {
		if leading {
			_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", LeaderLockKey)
		}
		l.setLeading(false)
		_ = conn.Close(context.Background())
	}()

	acquired, err := l.tryAcquire(ctx, conn)
	if err != nil {
		return err
	}
	leading = acquired
	l.setLeading(leading)

	ticker := time.NewTicker(l.retry)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if leading {
				// A session lock is lost as soon as the connection dies; a ping
				// is the cheapest way to notice and fall back to election.
				if err := conn.Ping(ctx); err != nil {
					return fmt.Errorf("leader session lost: %w", err)
				}
				continue
			}
			acquired, err := l.tryAcquire(ctx, conn)
			if err != nil {
				return err
			}
			if acquired {
				leading = true
				l.setLeading(true)
			}
		}
	}
}

func (l *Leader) tryAcquire(ctx context.Context, conn *pgx.Conn) (bool, error) {
	var acquired bool
	if err := conn.QueryRow(ctx, "SELECT pg_try_advisory_lock($1)", LeaderLockKey).Scan(&acquired); err != nil {
		return false, fmt.Errorf("try leader advisory lock: %w", err)
	}
	return acquired, nil
}

func (l *Leader) setLeading(value bool) {
	l.mu.Lock()
	l.leading = value
	l.mu.Unlock()
}

var _ orchestrationapp.LeaderGate = (*Leader)(nil)
