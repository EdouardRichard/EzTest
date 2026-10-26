package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// migrationsDir is the directory inside fsys that holds the SQL files and
	// order.txt.
	migrationsDir = "migrations"
	// orderFile is the authoritative, append-only execution order of the
	// migrations. Version numbers are the 1-based positions in this file, not
	// the per-context numbers in the file names (for example identity_0001 and
	// llm_0001 would collide otherwise).
	orderFile = migrationsDir + "/order.txt"
	// migrationLockID is the advisory lock that serialises concurrent service
	// startups.
	migrationLockID = 4242
)

// ErrMigrationChecksum is returned when an already applied migration file has
// been modified. Applied migrations are immutable.
var ErrMigrationChecksum = errors.New("db: migration checksum mismatch")

const createSchemaMigrations = `CREATE TABLE IF NOT EXISTS schema_migrations (
	version BIGINT PRIMARY KEY,
	checksum TEXT NOT NULL,
	applied_at TIMESTAMPTZ NOT NULL DEFAULT now()
)`

const (
	lockMigrationSQL   = "SELECT pg_advisory_lock($1)"
	unlockMigrationSQL = "SELECT pg_advisory_unlock($1)"
)

// migration is one ordered migration file with its version and content hash.
type migration struct {
	version  int64
	name     string
	sql      string
	checksum string
}

// Migrate applies the migrations listed in migrations/order.txt from fsys. It
// takes a PostgreSQL advisory lock for the whole run, records applied versions
// with their SHA-256 checksums in schema_migrations, executes each new
// migration in its own transaction and skips versions that are already
// applied. Applied migration files must never change: doing so returns an
// error wrapping ErrMigrationChecksum.
func Migrate(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("db: acquire migration connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, lockMigrationSQL, migrationLockID); err != nil {
		return fmt.Errorf("db: acquire migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, unlockMigrationSQL, migrationLockID); err != nil {
			// Closing the connection releases the session lock server-side and
			// keeps a still-locked connection out of the pool.
			_ = conn.Conn().Close(unlockCtx)
		}
	}()

	if _, err := conn.Exec(ctx, createSchemaMigrations); err != nil {
		return fmt.Errorf("db: create schema_migrations: %w", err)
	}

	order, err := readOrder(fsys)
	if err != nil {
		return err
	}
	applied, err := appliedChecksums(ctx, conn)
	if err != nil {
		return err
	}
	// Validate every file before executing anything so a bad migration cannot
	// leave the database half-updated.
	plan, err := planMigrations(fsys, order, applied)
	if err != nil {
		return err
	}
	for _, m := range plan {
		if err := applyMigration(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

// readOrder returns the migration file names from migrations/order.txt, in
// order, skipping blank lines and rejecting duplicates and path traversal. An
// empty file means "no migrations yet".
func readOrder(fsys fs.FS) ([]string, error) {
	raw, err := fs.ReadFile(fsys, orderFile)
	if err != nil {
		return nil, fmt.Errorf("db: read %s: %w", orderFile, err)
	}
	var order []string
	seen := make(map[string]struct{})
	for i, line := range strings.Split(string(raw), "\n") {
		name := strings.TrimSpace(line)
		if name == "" {
			continue
		}
		if name != path.Base(name) || name == "." || name == ".." {
			return nil, fmt.Errorf("db: %s line %d: invalid migration file name %q", orderFile, i+1, name)
		}
		if _, ok := seen[name]; ok {
			return nil, fmt.Errorf("db: %s lists %q more than once", orderFile, name)
		}
		seen[name] = struct{}{}
		order = append(order, name)
	}
	return order, nil
}

// appliedChecksums loads the versions that are already recorded in the
// database together with their checksums.
func appliedChecksums(ctx context.Context, conn *pgxpool.Conn) (map[int64]string, error) {
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[int64]string)
	for rows.Next() {
		var version int64
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("db: scan schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	return applied, nil
}

// planMigrations reads and validates the ordered migration files and returns
// the ones that still need to be applied. Version i is order[i].
func planMigrations(fsys fs.FS, order []string, applied map[int64]string) ([]migration, error) {
	plan := make([]migration, 0, len(order))
	for i, name := range order {
		version := int64(i + 1)
		content, err := fs.ReadFile(fsys, path.Join(migrationsDir, name))
		if err != nil {
			return nil, fmt.Errorf("db: read migration %q: %w", name, err)
		}
		checksum := checksumOf(content)
		if recorded, ok := applied[version]; ok {
			if recorded != checksum {
				return nil, fmt.Errorf("%w: %s (version %d)", ErrMigrationChecksum, name, version)
			}
			continue
		}
		plan = append(plan, migration{version: version, name: name, sql: string(content), checksum: checksum})
	}
	return plan, nil
}

// applyMigration executes one migration and records its version atomically.
func applyMigration(ctx context.Context, conn *pgxpool.Conn, m migration) error {
	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("db: begin migration %s: %w", m.name, err)
	}
	// Exec without arguments uses pgx's simple protocol, so a migration file
	// may contain multiple statements.
	if _, err := tx.Exec(ctx, m.sql); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("db: apply migration %s: %w", m.name, err)
	}
	if _, err := tx.Exec(ctx,
		`INSERT INTO schema_migrations (version, checksum) VALUES ($1, $2)`,
		m.version, m.checksum); err != nil {
		_ = tx.Rollback(ctx)
		return fmt.Errorf("db: record migration %s: %w", m.name, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("db: commit migration %s: %w", m.name, err)
	}
	return nil
}

// checksumOf returns the lowercase hex SHA-256 of a migration file.
func checksumOf(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
