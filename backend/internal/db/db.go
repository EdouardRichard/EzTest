// Package db owns the PostgreSQL connection pool and the migration runner. It
// contains no business logic.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is the process-wide database handle. It wraps the pgx connection pool so
// the rest of the service depends on this package instead of pgx directly.
type DB struct {
	Pool *pgxpool.Pool
}

// Open creates a connection pool for url and verifies that the database is
// reachable before returning.
func Open(ctx context.Context, url string) (*DB, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return &DB{Pool: pool}, nil
}

// Close releases every pooled connection.
func (d *DB) Close() {
	d.Pool.Close()
}

// Ping reports whether the database is currently reachable. It satisfies
// server.Pinger and is used by the readiness endpoint.
func (d *DB) Ping(ctx context.Context) error {
	if err := d.Pool.Ping(ctx); err != nil {
		return fmt.Errorf("db: ping: %w", err)
	}
	return nil
}
