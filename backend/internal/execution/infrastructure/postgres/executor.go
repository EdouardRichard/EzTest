package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ExecutorRepository implements the matching executionapp repository port.
type ExecutorRepository struct{ base }

const executorColumns = `id, name, endpoint, token_hash, capacity, active_contexts, labels,
	status, last_heartbeat, version, last_error, created_at, updated_at`

// UpsertHeartbeat inserts or updates the executor registration by endpoint.
// The P3 registry passes the desired status; a draining executor is preserved
// by the caller.
func (r *ExecutorRepository) UpsertHeartbeat(ctx context.Context, instance execution.ExecutorInstance) error {
	ctx = contextOrBackground(ctx)
	labels, err := encodeJSONObject("labels", instance.Labels)
	if err != nil {
		return err
	}
	_, err = r.pool.Exec(ctx, `
		INSERT INTO executor_instance (id, name, endpoint, token_hash, capacity,
			active_contexts, labels, status, last_heartbeat, version, last_error)
		VALUES ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11)
		ON CONFLICT (endpoint) DO UPDATE SET
			name = EXCLUDED.name,
			token_hash = EXCLUDED.token_hash,
			capacity = EXCLUDED.capacity,
			active_contexts = EXCLUDED.active_contexts,
			labels = EXCLUDED.labels,
			status = EXCLUDED.status,
			last_heartbeat = EXCLUDED.last_heartbeat,
			version = EXCLUDED.version,
			last_error = EXCLUDED.last_error,
			updated_at = now()
	`, r.insertID(instance.ID), instance.Name, instance.Endpoint, instance.TokenHash,
		instance.Capacity, instance.ActiveContexts, labels, string(instance.Status),
		instance.LastHeartbeat, instance.Version, instance.LastError)
	return wrapDB("upsert executor heartbeat", err)
}

// GetByEndpoint loads one executor instance by its unique endpoint.
func (r *ExecutorRepository) GetByEndpoint(ctx context.Context, endpoint string) (execution.ExecutorInstance, error) {
	ctx = contextOrBackground(ctx)
	instance, err := scanExecutor(r.pool.QueryRow(ctx, `
		SELECT `+executorColumns+` FROM executor_instance WHERE endpoint = $1
	`, endpoint))
	if err != nil {
		return execution.ExecutorInstance{}, wrapDB("get executor by endpoint", err)
	}
	return instance, nil
}

// Get loads one executor instance.
func (r *ExecutorRepository) Get(ctx context.Context, id int64) (execution.ExecutorInstance, error) {
	ctx = contextOrBackground(ctx)
	instance, err := scanExecutor(r.pool.QueryRow(ctx, `
		SELECT `+executorColumns+` FROM executor_instance WHERE id = $1
	`, id))
	if err != nil {
		return execution.ExecutorInstance{}, wrapDB("get executor", err)
	}
	return instance, nil
}

// List returns every registered executor instance.
func (r *ExecutorRepository) List(ctx context.Context) ([]execution.ExecutorInstance, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `SELECT `+executorColumns+` FROM executor_instance ORDER BY id`)
	if err != nil {
		return nil, wrapDB("list executors", err)
	}
	defer rows.Close()
	items := make([]execution.ExecutorInstance, 0)
	for rows.Next() {
		instance, err := scanExecutor(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, instance)
	}
	return items, rows.Err()
}

// SetStatus changes one executor instance status.
func (r *ExecutorRepository) SetStatus(ctx context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	ctx = contextOrBackground(ctx)
	tag, err := r.pool.Exec(ctx, `
		UPDATE executor_instance SET status = $2, updated_at = now() WHERE id = $1
	`, id, string(status))
	if err != nil {
		return false, wrapDB("set executor status", err)
	}
	return tag.RowsAffected() > 0, nil
}

func scanExecutor(row rowScanner) (execution.ExecutorInstance, error) {
	var instance execution.ExecutorInstance
	var labelsRaw []byte
	var status string
	err := row.Scan(&instance.ID, &instance.Name, &instance.Endpoint, &instance.TokenHash,
		&instance.Capacity, &instance.ActiveContexts, &labelsRaw, &status,
		&instance.LastHeartbeat, &instance.Version, &instance.LastError,
		&instance.CreatedAt, &instance.UpdatedAt)
	if err != nil {
		return execution.ExecutorInstance{}, err
	}
	instance.Status = execution.ExecutorStatus(status)
	if len(labelsRaw) > 0 {
		var labels map[string]any
		if err := decodeJSONObject("labels", labelsRaw, &labels); err != nil {
			return execution.ExecutorInstance{}, err
		}
		instance.Labels = labels
	}
	return instance, nil
}
