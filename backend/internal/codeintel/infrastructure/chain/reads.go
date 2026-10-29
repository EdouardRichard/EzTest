// Package chain adapts the codeintel context's latest-analysis impact reads to
// the P8 exploration-chain provider port (spec §5.2 query 10). The chain service
// turns requirement candidates into impact_case edges, so the provider keeps the
// subject kind and lets the service decide representability.
package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Reads implements chainapp.CodeIntelProvider on the P4 analysis tables.
type Reads struct {
	pool *pgxpool.Pool
}

// New returns the codeintel-side chain reads provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{pool: pool}
}

var _ chainapp.CodeIntelProvider = (*Reads)(nil)

// Impact returns the candidates of the newest analysis of versionID that are
// selected or carry high/medium confidence. A version without an analysis yields
// an empty layer (no error): the chain omits the impact edges in that case.
func (r *Reads) Impact(ctx context.Context, versionID int64) ([]chainapp.ImpactRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("codeintel chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT c.subject_kind, c.subject_id, c.reason, c.confidence, c.selected
		FROM code_impact_candidate c
		WHERE c.analysis_id = (
			SELECT a.id
			FROM code_analysis a
			WHERE a.version_id = $1
			ORDER BY a.created_at DESC, a.id DESC
			LIMIT 1
		)
		  AND (c.selected OR c.confidence IN ('high', 'medium'))
		ORDER BY c.id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("codeintel chain reads: load impact: %w", err)
	}
	defer rows.Close()

	impact := make([]chainapp.ImpactRow, 0)
	for rows.Next() {
		var row chainapp.ImpactRow
		if err := rows.Scan(&row.SubjectKind, &row.SubjectID, &row.Reason,
			&row.Confidence, &row.Selected); err != nil {
			return nil, fmt.Errorf("codeintel chain reads: scan impact candidate: %w", err)
		}
		impact = append(impact, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("codeintel chain reads: load impact: %w", err)
	}
	return impact, nil
}
