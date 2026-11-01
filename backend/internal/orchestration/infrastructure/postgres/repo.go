// Package orchestrationpg is the PostgreSQL adapter of the P7 orchestration
// context. The api_instance registry is the single coordination surface for
// replica registration, heartbeat and drain state; the leader adapter holds a
// session advisory lock on a dedicated connection.
package orchestrationpg

import (
	"context"
	"errors"
	"fmt"
	"time"

	domain "github.com/EdouardRichard/EzTest/backend/internal/orchestration/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const instanceColumns = `id, instance_key, hostname, pid, api_port, node_id, status,
	version, last_error, started_at, heartbeat_at, draining_at, stopped_at,
	created_at, updated_at`

// PostgreSQL unique-constraint names from orchestration_0001_instance.sql.
const (
	constraintInstanceKey = "uk_api_instance_instance_key"
	constraintNodeID      = "uk_api_instance_node_id"
)

// Repository persists api_instance rows.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// UsedNodeIDs returns the node IDs held by live instances. Offline/stopped rows
// are deliberately omitted: their node ID is reclaimable by Insert.
func (r *Repository) UsedNodeIDs(ctx context.Context) ([]int, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("orchestration repository is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT node_id
		FROM api_instance
		WHERE status IN ('active', 'draining')
		ORDER BY node_id
	`)
	if err != nil {
		return nil, fmt.Errorf("list used node ids: %w", err)
	}
	defer rows.Close()

	nodeIDs := make([]int, 0)
	for rows.Next() {
		var nodeID int
		if err := rows.Scan(&nodeID); err != nil {
			return nil, fmt.Errorf("scan used node id: %w", err)
		}
		nodeIDs = append(nodeIDs, nodeID)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list used node ids: %w", err)
	}
	return nodeIDs, nil
}

// Insert registers one instance. A unique conflict is resolved in two steps:
// a row from an offline/stopped instance holding the same node ID (or the same
// hostname:pid) is reclaimed -- those are the only states whose process is
// confirmed gone -- and the insert is retried once. Live conflicts surface as
// domain.ErrNodeTaken / domain.ErrInstanceKeyTaken so the application tries the
// next candidate node.
func (r *Repository) Insert(ctx context.Context, instance domain.Instance) error {
	if r == nil || r.pool == nil {
		return errors.New("orchestration repository is not configured")
	}
	err := r.insert(ctx, instance)
	if !errors.Is(err, domain.ErrNodeTaken) && !errors.Is(err, domain.ErrInstanceKeyTaken) {
		return err
	}
	reclaimed, reclaimErr := r.reclaim(ctx, instance)
	if reclaimErr != nil {
		return reclaimErr
	}
	if !reclaimed {
		return err
	}
	return r.insert(ctx, instance)
}

func (r *Repository) insert(ctx context.Context, instance domain.Instance) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO api_instance (
			id, instance_key, hostname, pid, api_port, node_id, status, version,
			last_error, started_at, heartbeat_at, created_at, updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
	`, instance.ID, instance.InstanceKey, instance.Hostname, instance.PID, instance.APIPort,
		instance.NodeID, string(instance.Status), instance.Version, instance.LastError,
		instance.StartedAt, instance.HeartbeatAt, instance.CreatedAt, instance.UpdatedAt)
	if err == nil {
		return nil
	}
	return classifyInsertError(err)
}

// reclaim deletes the conflicting row when (and only when) it is offline or
// stopped. Offline is set exclusively by the stale-heartbeat scan and stopped
// only by graceful Stop, so both states confirm no live process owns the node.
func (r *Repository) reclaim(ctx context.Context, instance domain.Instance) (bool, error) {
	tag, err := r.pool.Exec(ctx, `
		DELETE FROM api_instance
		WHERE (node_id = $1 OR instance_key = $2)
		  AND status IN ('offline', 'stopped')
	`, instance.NodeID, instance.InstanceKey)
	if err != nil {
		return false, fmt.Errorf("reclaim stale api_instance: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

func classifyInsertError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		switch pgErr.ConstraintName {
		case constraintInstanceKey:
			return domain.ErrInstanceKeyTaken
		case constraintNodeID:
			return domain.ErrNodeTaken
		}
	}
	return fmt.Errorf("insert api_instance: %w", err)
}

// Get returns one instance or domain.ErrNotFound.
func (r *Repository) Get(ctx context.Context, id int64) (domain.Instance, error) {
	if r == nil || r.pool == nil {
		return domain.Instance{}, errors.New("orchestration repository is not configured")
	}
	instance, err := scanInstance(r.pool.QueryRow(ctx,
		`SELECT `+instanceColumns+` FROM api_instance WHERE id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Instance{}, domain.ErrNotFound
	}
	if err != nil {
		return domain.Instance{}, fmt.Errorf("get api_instance: %w", err)
	}
	return instance, nil
}

// List returns every registered instance, newest first.
func (r *Repository) List(ctx context.Context) ([]domain.Instance, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("orchestration repository is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT `+instanceColumns+`
		FROM api_instance
		ORDER BY started_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list api_instances: %w", err)
	}
	defer rows.Close()

	instances := make([]domain.Instance, 0)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan api_instance: %w", err)
		}
		instances = append(instances, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api_instances: %w", err)
	}
	return instances, nil
}

// Heartbeat refreshes heartbeat_at for an active/draining instance and reports
// the persisted status. updated=false means the row is offline/stopped (or
// gone), which the caller logs as a lost registration.
func (r *Repository) Heartbeat(ctx context.Context, id int64, at time.Time) (domain.Status, bool, error) {
	if r == nil || r.pool == nil {
		return "", false, errors.New("orchestration repository is not configured")
	}
	var status string
	err := r.pool.QueryRow(ctx, `
		UPDATE api_instance
		SET heartbeat_at = $2, updated_at = $2
		WHERE id = $1 AND status IN ('active', 'draining')
		RETURNING status
	`, id, at).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("heartbeat api_instance: %w", err)
	}
	return domain.Status(status), true, nil
}

// SetStatus applies an optimistic transition (WHERE status = from) and returns
// the updated row plus whether a row changed.
func (r *Repository) SetStatus(
	ctx context.Context,
	id int64,
	from, to domain.Status,
	at time.Time,
) (domain.Instance, bool, error) {
	if r == nil || r.pool == nil {
		return domain.Instance{}, false, errors.New("orchestration repository is not configured")
	}
	instance, err := scanInstance(r.pool.QueryRow(ctx, `
		UPDATE api_instance
		SET status = $3,
		    draining_at = CASE WHEN $3 = 'draining' THEN $4 ELSE draining_at END,
		    updated_at = $4
		WHERE id = $1 AND status = $2
		RETURNING `+instanceColumns,
		id, string(from), string(to), at))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Instance{}, false, nil
	}
	if err != nil {
		return domain.Instance{}, false, fmt.Errorf("update api_instance status: %w", err)
	}
	return instance, true, nil
}

// MarkStopped marks a graceful stop and reports whether a row changed.
func (r *Repository) MarkStopped(ctx context.Context, id int64, at time.Time) (bool, error) {
	if r == nil || r.pool == nil {
		return false, errors.New("orchestration repository is not configured")
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE api_instance
		SET status = 'stopped', stopped_at = $2, heartbeat_at = $2, updated_at = $2
		WHERE id = $1 AND status <> 'stopped'
	`, id, at)
	if err != nil {
		return false, fmt.Errorf("mark api_instance stopped: %w", err)
	}
	return tag.RowsAffected() > 0, nil
}

// MarkOfflineStale transitions active rows with a stale heartbeat to offline
// and returns exactly the rows that changed. Concurrent scans cannot publish a
// transition twice because the UPDATE only matches status='active'.
func (r *Repository) MarkOfflineStale(ctx context.Context, cutoff time.Time) ([]domain.Instance, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("orchestration repository is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		UPDATE api_instance
		SET status = 'offline', updated_at = now()
		WHERE status = 'active' AND heartbeat_at < $1
		RETURNING `+instanceColumns,
		cutoff)
	if err != nil {
		return nil, fmt.Errorf("mark stale api_instances offline: %w", err)
	}
	defer rows.Close()

	stale := make([]domain.Instance, 0)
	for rows.Next() {
		instance, err := scanInstance(rows)
		if err != nil {
			return nil, fmt.Errorf("scan offline api_instance: %w", err)
		}
		stale = append(stale, instance)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mark stale api_instances offline: %w", err)
	}
	return stale, nil
}

// Status returns the persisted status of one instance.
func (r *Repository) Status(ctx context.Context, id int64) (domain.Status, error) {
	if r == nil || r.pool == nil {
		return "", errors.New("orchestration repository is not configured")
	}
	var status string
	if err := r.pool.QueryRow(ctx, `SELECT status FROM api_instance WHERE id = $1`, id).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", domain.ErrNotFound
		}
		return "", fmt.Errorf("get api_instance status: %w", err)
	}
	return domain.Status(status), nil
}

// Ping checks database reachability for readiness.
func (r *Repository) Ping(ctx context.Context) error {
	if r == nil || r.pool == nil {
		return errors.New("orchestration repository is not configured")
	}
	return r.pool.Ping(ctx)
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanInstance(row rowScanner) (domain.Instance, error) {
	var instance domain.Instance
	var status string
	if err := row.Scan(
		&instance.ID, &instance.InstanceKey, &instance.Hostname, &instance.PID,
		&instance.APIPort, &instance.NodeID, &status, &instance.Version,
		&instance.LastError, &instance.StartedAt, &instance.HeartbeatAt,
		&instance.DrainingAt, &instance.StoppedAt, &instance.CreatedAt,
		&instance.UpdatedAt,
	); err != nil {
		return domain.Instance{}, err
	}
	instance.Status = domain.Status(status)
	return instance, nil
}
