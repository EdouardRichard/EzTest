package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ApplyMigrationsThroughForTest applies the first `through` ordered migrations
// and records them in schema_migrations exactly like Migrate, so a subsequent
// Migrate starts at the next migration. It is compiled only into test binaries
// and exists to replay the upgrade path of a migration that mutates existing
// rows (agent_0009 disables the five retired profiles).
func ApplyMigrationsThroughForTest(ctx context.Context, pool *pgxpool.Pool, through int) error {
	migrations, err := orderedMigrations()
	if err != nil {
		return err
	}
	if through < 0 || through > len(migrations) {
		return fmt.Errorf("apply %d migrations: only %d available", through, len(migrations))
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	for _, item := range migrations[:through] {
		tx, err := conn.Begin(ctx)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", item.Name, err)
		}
		if _, err := tx.Conn().PgConn().Exec(ctx, item.SQL).ReadAll(); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("apply migration %s: %w", item.Name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
			item.Name, item.Checksum,
		); err != nil {
			_ = tx.Rollback(ctx)
			return fmt.Errorf("record migration %s: %w", item.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit migration %s: %w", item.Name, err)
		}
	}
	return nil
}
