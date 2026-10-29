// Package chain adapts the mindmap context's case graph reads to the P8
// exploration-chain provider ports (spec §5.2 queries 3–5). Each method is a
// single SQL statement returning the complete version layer; request filters and
// truncation belong to the chain service.
package chain

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	chainapp "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// Reads implements chainapp.MindmapProvider on the mindmap tables.
type Reads struct {
	pool *pgxpool.Pool
}

// New returns the mindmap-side chain reads provider.
func New(pool *pgxpool.Pool) *Reads {
	return &Reads{pool: pool}
}

var _ chainapp.MindmapProvider = (*Reads)(nil)

// Cases returns every case node of versionID (scenario containers are excluded:
// they group cases and are not themselves chain case nodes). One statement
// aggregates the requirement sources into an id array and the step/assertion
// counts through grouped subqueries, so no per-case Count query is issued.
func (r *Reads) Cases(ctx context.Context, versionID int64) ([]chainapp.CaseRow, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("mindmap chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT n.id, n.title, n.status, n.source, n.priority, n.sort_order,
		       COALESCE(sources.requirement_ids, ARRAY[]::bigint[]),
		       COALESCE(steps.step_count, 0),
		       COALESCE(assertions.assertion_count, 0)
		FROM case_node n
		LEFT JOIN (
			SELECT cs.node_id, array_agg(DISTINCT cs.ref_id ORDER BY cs.ref_id) AS requirement_ids
			FROM case_source cs
			JOIN case_node cn ON cn.id = cs.node_id
			WHERE cn.version_id = $1 AND cs.ref_kind = 'requirement' AND cs.ref_id IS NOT NULL
			GROUP BY cs.node_id
		) sources ON sources.node_id = n.id
		LEFT JOIN (
			SELECT s.node_id, count(*) AS step_count
			FROM case_step s
			JOIN case_node cn ON cn.id = s.node_id
			WHERE cn.version_id = $1
			GROUP BY s.node_id
		) steps ON steps.node_id = n.id
		LEFT JOIN (
			SELECT a.node_id, count(*) AS assertion_count
			FROM case_assertion a
			JOIN case_node cn ON cn.id = a.node_id
			WHERE cn.version_id = $1
			GROUP BY a.node_id
		) assertions ON assertions.node_id = n.id
		WHERE n.version_id = $1 AND n.kind = 'case'
		ORDER BY n.sort_order, n.id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("mindmap chain reads: load cases: %w", err)
	}
	defer rows.Close()

	cases := make([]chainapp.CaseRow, 0)
	for rows.Next() {
		var row chainapp.CaseRow
		if err := rows.Scan(&row.ID, &row.Title, &row.Status, &row.Source, &row.Priority,
			&row.SortOrder, &row.RequirementIDs, &row.StepCount, &row.AssertionCount); err != nil {
			return nil, fmt.Errorf("mindmap chain reads: scan case: %w", err)
		}
		cases = append(cases, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mindmap chain reads: load cases: %w", err)
	}
	return cases, nil
}

// Coverage returns the requirement coverage matrix of versionID in one
// statement. It mirrors mindmap domain.BuildCoverage: only requirement sources
// attached to case nodes count (a scenario never covers a requirement) and only
// still-confirmed (clarified) requirements form a matrix row. The chain service
// unions these cells with the case requirement sources.
func (r *Reads) Coverage(ctx context.Context, versionID int64) ([]chainapp.CoverageCell, error) {
	if r == nil || r.pool == nil {
		return nil, errors.New("mindmap chain reads: provider is not configured")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT cs.ref_id, array_agg(DISTINCT cs.node_id ORDER BY cs.node_id)
		FROM case_source cs
		JOIN case_node cn ON cn.id = cs.node_id AND cn.version_id = cs.version_id
		JOIN requirement r ON r.id = cs.ref_id AND r.version_id = cs.version_id
		WHERE cs.version_id = $1
		  AND cs.ref_kind = 'requirement'
		  AND cs.ref_id IS NOT NULL
		  AND cn.kind = 'case'
		  AND r.status = 'clarified'
		GROUP BY cs.ref_id
		ORDER BY cs.ref_id`, versionID)
	if err != nil {
		return nil, fmt.Errorf("mindmap chain reads: load coverage: %w", err)
	}
	defer rows.Close()

	coverage := make([]chainapp.CoverageCell, 0)
	for rows.Next() {
		var cell chainapp.CoverageCell
		if err := rows.Scan(&cell.RequirementID, &cell.CaseIDs); err != nil {
			return nil, fmt.Errorf("mindmap chain reads: scan coverage cell: %w", err)
		}
		coverage = append(coverage, cell)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mindmap chain reads: load coverage: %w", err)
	}
	return coverage, nil
}
