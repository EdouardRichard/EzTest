// Package postgres implements the audit repository port with PostgreSQL.
package postgres

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	audit "github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
)

// entryColumns is the column list shared by every audit query and the row order
// scanEntry expects. Text columns are coalesced so nullable rows still scan
// into plain strings.
const entryColumns = `id, actor_user_id, action, COALESCE(target_type, ''), COALESCE(target_id, ''), COALESCE(request_id, ''), COALESCE(ip, ''), COALESCE(user_agent, ''), metadata, created_at`

// Repository implements audit.Repository on a pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

// NewRepository returns an audit repository backed by pool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// rowScanner is the subset of pgx.Row and pgx.Rows used to scan one entry.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanEntry scans one row in entryColumns order. Metadata is decoded from JSONB
// into a non-nil map so callers always get an object.
func scanEntry(row rowScanner) (audit.Entry, error) {
	var (
		entry    audit.Entry
		metadata []byte
	)
	if err := row.Scan(
		&entry.ID,
		&entry.ActorUserID,
		&entry.Action,
		&entry.TargetType,
		&entry.TargetID,
		&entry.RequestID,
		&entry.IP,
		&entry.UserAgent,
		&metadata,
		&entry.CreatedAt,
	); err != nil {
		return audit.Entry{}, err
	}
	entry.Metadata = make(map[string]any)
	if len(metadata) > 0 {
		if err := json.Unmarshal(metadata, &entry.Metadata); err != nil {
			return audit.Entry{}, fmt.Errorf("decode metadata: %w", err)
		}
		if entry.Metadata == nil {
			entry.Metadata = make(map[string]any)
		}
	}
	return entry, nil
}

// Record appends one audit entry. The database assigns ID and, when the caller
// leaves CreatedAt zero, the created_at timestamp.
func (r *Repository) Record(ctx context.Context, entry audit.Entry) error {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("postgres: encode audit metadata: %w", err)
	}
	var createdAt *time.Time
	if !entry.CreatedAt.IsZero() {
		createdAt = &entry.CreatedAt
	}
	const query = `INSERT INTO audit_log
		(actor_user_id, action, target_type, target_id, request_id, ip, user_agent, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8::jsonb, COALESCE($9::timestamptz, now()))`
	if _, err := r.pool.Exec(ctx, query,
		entry.ActorUserID,
		entry.Action,
		entry.TargetType,
		entry.TargetID,
		entry.RequestID,
		entry.IP,
		entry.UserAgent,
		string(raw),
		createdAt,
	); err != nil {
		return fmt.Errorf("postgres: record audit entry: %w", err)
	}
	return nil
}

// List returns up to limit entries older than afterID, newest first. afterID
// <= 0 starts at the newest entry. more reports whether another page exists.
func (r *Repository) List(ctx context.Context, limit int, afterID int64) ([]audit.Entry, bool, error) {
	return r.ListFiltered(ctx, audit.ListFilter{}, limit, afterID)
}

// ListFiltered returns up to limit entries older than afterID, newest first,
// narrowed by filter. Every filter field is optional; nil/empty fields widen the
// query instead of matching NULL. The time bounds are inclusive on created_at.
func (r *Repository) ListFiltered(ctx context.Context, filter audit.ListFilter, limit int, afterID int64) ([]audit.Entry, bool, error) {
	if limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list audit entries: limit must be positive")
	}
	var (
		actor *int64
		from  *time.Time
		to    *time.Time
	)
	if filter.ActorUserID != nil {
		actor = filter.ActorUserID
	}
	if filter.From != nil {
		from = filter.From
	}
	if filter.To != nil {
		to = filter.To
	}
	const query = `SELECT ` + entryColumns + `
		FROM audit_log
		WHERE ($1 <= 0 OR id < $1)
			AND ($2::bigint IS NULL OR actor_user_id = $2)
			AND ($3 = '' OR action = $3)
			AND ($4::timestamptz IS NULL OR created_at >= $4)
			AND ($5::timestamptz IS NULL OR created_at <= $5)
		ORDER BY id DESC
		LIMIT $6`
	rows, err := r.pool.Query(ctx, query, afterID, actor, filter.Action, from, to, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]audit.Entry, 0, limit+1)
	for rows.Next() {
		entry, err := scanEntry(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan audit entry: %w", err)
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list audit entries: %w", err)
	}

	more := len(entries) > limit
	if more {
		entries = entries[:limit]
	}
	return entries, more, nil
}
