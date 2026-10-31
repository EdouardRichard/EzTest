package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed migrations
var migrationsFS embed.FS

const (
	migrationsDir   = "migrations"
	orderFile       = "migrations/order.txt"
	advisoryLockKey = int64(0x455A5430)
)

// ErrMigrationChecksum is returned when an already-applied migration changed.
var ErrMigrationChecksum = errors.New("migration checksum changed")

type migration struct {
	Name     string
	SQL      string
	Checksum string
}

// Migrate applies every pending migration in order.txt under a session advisory
// lock. Re-running is a no-op; changing an applied file is an error.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	migrations, err := orderedMigrations()
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1)", advisoryLockKey); err != nil {
		return nil, fmt.Errorf("acquire migration lock: %w", err)
	}
	defer func() {
		_, _ = conn.Exec(context.Background(), "SELECT pg_advisory_unlock($1)", advisoryLockKey)
	}()

	if _, err := conn.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version TEXT PRIMARY KEY,
			checksum TEXT NOT NULL,
			applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
		)
	`); err != nil {
		return nil, fmt.Errorf("create schema_migrations: %w", err)
	}

	applied := make([]string, 0, len(migrations))
	for _, item := range migrations {
		var existing string
		err := conn.QueryRow(ctx, `SELECT checksum FROM schema_migrations WHERE version = $1`, item.Name).Scan(&existing)
		switch {
		case err == nil:
			if existing != item.Checksum {
				return nil, fmt.Errorf("%w: %s", ErrMigrationChecksum, item.Name)
			}
			continue
		case !errors.Is(err, pgxNoRows):
			return nil, fmt.Errorf("read migration state %s: %w", item.Name, err)
		}

		tx, err := conn.Begin(ctx)
		if err != nil {
			return nil, fmt.Errorf("begin migration %s: %w", item.Name, err)
		}
		if _, err := tx.Conn().PgConn().Exec(ctx, item.SQL).ReadAll(); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("apply migration %s: %w", item.Name, err)
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
			item.Name, item.Checksum,
		); err != nil {
			_ = tx.Rollback(ctx)
			return nil, fmt.Errorf("record migration %s: %w", item.Name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit migration %s: %w", item.Name, err)
		}
		applied = append(applied, item.Name)
	}
	return applied, nil
}

// orderedMigrations validates that order.txt covers every embedded .sql file
// exactly once, then loads the files in order.
func orderedMigrations() ([]migration, error) {
	orderRaw, err := migrationsFS.ReadFile(orderFile)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", orderFile, err)
	}

	names := make([]string, 0, 8)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(string(orderRaw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if _, ok := seen[line]; ok {
			return nil, fmt.Errorf("duplicate migration in order.txt: %s", line)
		}
		seen[line] = struct{}{}
		names = append(names, line)
	}

	files, err := fs.Glob(migrationsFS, migrationsDir+"/*.sql")
	if err != nil {
		return nil, err
	}
	if len(files) != len(names) {
		return nil, fmt.Errorf("order.txt lists %d migrations but %d .sql files exist", len(names), len(files))
	}
	for _, file := range files {
		name := strings.TrimPrefix(file, migrationsDir+"/")
		if _, ok := seen[name]; !ok {
			return nil, fmt.Errorf("migration %s is not listed in order.txt", name)
		}
	}

	result := make([]migration, 0, len(names))
	for _, name := range names {
		raw, err := migrationsFS.ReadFile(migrationsDir + "/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(raw)
		result = append(result, migration{
			Name:     name,
			SQL:      string(raw),
			Checksum: hex.EncodeToString(sum[:]),
		})
	}
	return result, nil
}
