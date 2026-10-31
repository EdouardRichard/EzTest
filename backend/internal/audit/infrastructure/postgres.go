// Package auditpg is the PostgreSQL adapter for the audit context.
package auditpg

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/audit/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores append-only audit entries.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// Insert appends one audit entry.
func (r *Repository) Insert(ctx context.Context, entry audit.Entry) error {
	metadata := entry.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	if _, err := r.pool.Exec(ctx, `
		INSERT INTO audit_log
			(id, actor_id, action, target_kind, target_id, request_id, ip, user_agent, metadata, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10)
	`, entry.ID, entry.ActorID, entry.Action, entry.TargetKind, entry.TargetID,
		entry.RequestID, entry.IP, entry.UserAgent, string(raw), entry.CreatedAt); err != nil {
		return fmt.Errorf("insert audit entry: %w", err)
	}
	return nil
}

// List returns up to limit entries older than beforeID, newest first.
func (r *Repository) List(ctx context.Context, filter audit.Filter, limit int, beforeID *int64) ([]audit.Entry, error) {
	if limit <= 0 {
		return nil, fmt.Errorf("list audit entries: limit must be positive")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, actor_id, action, target_kind, target_id, request_id, ip, user_agent, metadata, created_at
		FROM audit_log
		WHERE ($1::BIGINT IS NULL OR id < $1)
		  AND ($2 = '' OR action = $2)
		  AND ($3::BIGINT IS NULL OR actor_id = $3)
		ORDER BY id DESC
		LIMIT $4
	`, beforeID, filter.Action, filter.ActorID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	defer rows.Close()

	entries := make([]audit.Entry, 0, limit)
	for rows.Next() {
		var entry audit.Entry
		var metadata []byte
		if err := rows.Scan(
			&entry.ID, &entry.ActorID, &entry.Action, &entry.TargetKind, &entry.TargetID,
			&entry.RequestID, &entry.IP, &entry.UserAgent, &metadata, &entry.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan audit entry: %w", err)
		}
		entry.Metadata = make(map[string]any)
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &entry.Metadata); err != nil {
				return nil, fmt.Errorf("decode audit metadata: %w", err)
			}
			if entry.Metadata == nil {
				entry.Metadata = make(map[string]any)
			}
		}
		entries = append(entries, entry)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list audit entries: %w", err)
	}
	return entries, nil
}
