package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// dbTargetColumnList is the column list shared by every db_target read and the
// column order scanDBTarget expects.
const dbTargetColumnList = `id, environment_id, name, host, port, database_name, username, password,
	ssl_mode, connect_timeout_ms, statement_timeout_ms, max_rows, enabled,
	last_tested_at, last_test_status, last_test_message, created_by, updated_by, created_at, updated_at`

// dbTargetInsertColumnList excludes the server-managed timestamps; created_at
// and updated_at keep their DDL defaults on insert.
const dbTargetInsertColumnList = `id, environment_id, name, host, port, database_name, username, password,
	ssl_mode, connect_timeout_ms, statement_timeout_ms, max_rows, enabled,
	last_tested_at, last_test_status, last_test_message, created_by, updated_by`

// DBTargetRepository implements the environment db target persistence port on a
// pgx pool. The password column is stored in plaintext per D48 and is returned
// only to the resource application layer; it is never logged here.
type DBTargetRepository struct {
	pool *pgxpool.Pool
}

// NewDBTargetRepository returns a db target repository backed by pool.
func NewDBTargetRepository(pool *pgxpool.Pool) *DBTargetRepository {
	return &DBTargetRepository{pool: pool}
}

var _ application.DBTargetRepository = (*DBTargetRepository)(nil)

// scanDBTarget scans one row in dbTargetColumnList order.
func scanDBTarget(row rowScanner) (domain.DBTarget, error) {
	var target domain.DBTarget
	if err := row.Scan(
		&target.ID,
		&target.EnvironmentID,
		&target.Name,
		&target.Host,
		&target.Port,
		&target.DatabaseName,
		&target.Username,
		&target.Password,
		&target.SSLMode,
		&target.ConnectTimeoutMS,
		&target.StatementTimeoutMS,
		&target.MaxRows,
		&target.Enabled,
		&target.LastTestedAt,
		&target.LastTestStatus,
		&target.LastTestMessage,
		&target.CreatedBy,
		&target.UpdatedBy,
		&target.CreatedAt,
		&target.UpdatedAt,
	); err != nil {
		return domain.DBTarget{}, err
	}
	return target, nil
}

// GetDBTarget returns the target of environmentID, or (nil, nil) when the
// environment has none configured.
func (r *DBTargetRepository) GetDBTarget(ctx context.Context, environmentID int64) (*domain.DBTarget, error) {
	target, err := scanDBTarget(r.pool.QueryRow(ctx,
		`SELECT `+dbTargetColumnList+` FROM db_target WHERE environment_id = $1`, environmentID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get db target: %w", err)
	}
	return &target, nil
}

// UpsertDBTarget inserts the target or replaces the row of its environment
// (UNIQUE(environment_id)). The original id/created_by/created_at survive an
// update; the returned row is written back onto target.
func (r *DBTargetRepository) UpsertDBTarget(ctx context.Context, target *domain.DBTarget) error {
	if target == nil {
		return errors.New("postgres: upsert db target: nil target")
	}
	stored, err := scanDBTarget(r.pool.QueryRow(ctx, `
		INSERT INTO db_target (`+dbTargetInsertColumnList+`)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18)
		ON CONFLICT (environment_id) DO UPDATE SET
			name = EXCLUDED.name,
			host = EXCLUDED.host,
			port = EXCLUDED.port,
			database_name = EXCLUDED.database_name,
			username = EXCLUDED.username,
			password = EXCLUDED.password,
			ssl_mode = EXCLUDED.ssl_mode,
			connect_timeout_ms = EXCLUDED.connect_timeout_ms,
			statement_timeout_ms = EXCLUDED.statement_timeout_ms,
			max_rows = EXCLUDED.max_rows,
			enabled = EXCLUDED.enabled,
			last_tested_at = EXCLUDED.last_tested_at,
			last_test_status = EXCLUDED.last_test_status,
			last_test_message = EXCLUDED.last_test_message,
			updated_by = EXCLUDED.updated_by,
			updated_at = now()
		RETURNING `+dbTargetColumnList,
		target.ID,
		target.EnvironmentID,
		target.Name,
		target.Host,
		target.Port,
		target.DatabaseName,
		target.Username,
		target.Password,
		target.SSLMode,
		target.ConnectTimeoutMS,
		target.StatementTimeoutMS,
		target.MaxRows,
		target.Enabled,
		target.LastTestedAt,
		target.LastTestStatus,
		target.LastTestMessage,
		target.CreatedBy,
		target.UpdatedBy,
	))
	if err != nil {
		return fmt.Errorf("postgres: upsert db target: %w", err)
	}
	*target = stored
	return nil
}

// UpdateDBTargetTestResult records only the last connection-test outcome, so a
// probe never rewrites the admin configuration columns.
func (r *DBTargetRepository) UpdateDBTargetTestResult(ctx context.Context, targetID int64, testedAt time.Time, status, message string) error {
	tag, err := r.pool.Exec(ctx, `
		UPDATE db_target
		   SET last_tested_at = $2, last_test_status = $3, last_test_message = $4, updated_at = now()
		 WHERE id = $1`, targetID, testedAt, status, message)
	if err != nil {
		return fmt.Errorf("postgres: update db target test result: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("db_target", targetID)
	}
	return nil
}

// DeleteDBTarget removes the target of environmentID. A missing target is the
// stable db_target_missing 409 so a concurrent delete keeps the API idempotent
// from the caller's point of view.
func (r *DBTargetRepository) DeleteDBTarget(ctx context.Context, environmentID int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM db_target WHERE environment_id = $1`, environmentID)
	if err != nil {
		return fmt.Errorf("postgres: delete db target: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return domain.NewDBTargetMissing(environmentID)
	}
	return nil
}
