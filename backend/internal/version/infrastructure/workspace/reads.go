// Package workspace adapts the version context's reviewing-version read to the
// P8 todo-centre provider port (spec §6.3). The provider is read-only and issues
// one statement; aggregation, ordering and truncation belong to the workspace
// application service.
package workspace

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// queryer is the narrow pgx surface of the provider. *pgxpool.Pool satisfies it;
// tests substitute a fake that serves canned rows.
type queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Reads implements workspaceapp.ReviewProvider on the version context's table.
type Reads struct {
	q queryer
}

// New returns the version-side todo provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{q: pool}
}

var _ workspaceapp.ReviewProvider = (*Reads)(nil)

// ReviewingVersions returns every version whose status is reviewing, newest
// first. `updated_at` is surfaced as the pending-review timestamp: the schema
// does not persist the exact reviewing transition time.
func (r *Reads) ReviewingVersions(ctx context.Context) ([]workspaceapp.ReviewRow, error) {
	if r == nil || r.q == nil {
		return nil, errors.New("version workspace reads: provider is not configured")
	}
	rows, err := r.q.Query(ctx, `
		SELECT id, name, status, updated_at
		FROM version
		WHERE status = 'reviewing'
		ORDER BY updated_at DESC, id DESC`)
	if err != nil {
		return nil, fmt.Errorf("version workspace reads: reviewing versions: %w", err)
	}
	defer rows.Close()

	reviews := make([]workspaceapp.ReviewRow, 0)
	for rows.Next() {
		var row workspaceapp.ReviewRow
		if err := rows.Scan(&row.VersionID, &row.VersionName, &row.Status, &row.ReviewingAt); err != nil {
			return nil, fmt.Errorf("version workspace reads: scan reviewing version: %w", err)
		}
		reviews = append(reviews, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("version workspace reads: reviewing versions: %w", err)
	}
	return reviews, nil
}
