package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// nodeColumnList is the shared column order of every case_node query and the
// order scanNode expects.
const nodeColumnList = `id, version_id, parent_id, kind, title, description, priority,
	preconditions, test_data, source, status, sort_order, revision,
	created_by, updated_by, created_at, updated_at`

// NodeRepository implements application.NodeRepository on a pgx querier.
type NodeRepository struct {
	q querier
}

// NewNodeRepository returns a case-node repository backed by pool.
func NewNodeRepository(pool *pgxpool.Pool) application.NodeRepository {
	return newNodeRepository(pool)
}

// newNodeRepository binds the repository to q: the pool for a standalone use,
// or one pgx.Tx when materializing a whole graph atomically.
func newNodeRepository(q querier) *NodeRepository {
	return &NodeRepository{q: q}
}

var _ application.NodeRepository = (*NodeRepository)(nil)

// scanNode scans one row in nodeColumnList order.
func scanNode(row rowScanner) (domain.CaseNode, error) {
	var (
		node     domain.CaseNode
		parentID *int64
		testData []byte
	)
	if err := row.Scan(
		&node.ID,
		&node.VersionID,
		&parentID,
		&node.Kind,
		&node.Title,
		&node.Description,
		&node.Priority,
		&node.Preconditions,
		&testData,
		&node.Source,
		&node.Status,
		&node.SortOrder,
		&node.Revision,
		&node.CreatedBy,
		&node.UpdatedBy,
		&node.CreatedAt,
		&node.UpdatedAt,
	); err != nil {
		return domain.CaseNode{}, err
	}
	node.ParentID = parentID
	node.TestData = testData
	return node, nil
}

// Create inserts n with its explicit id and the caller-supplied timestamps and
// writes the stored row back.
func (r *NodeRepository) Create(ctx context.Context, n domain.CaseNode) (domain.CaseNode, error) {
	const query = `INSERT INTO case_node
		(id, version_id, parent_id, kind, title, description, priority,
		 preconditions, test_data, source, status, sort_order, revision,
		 created_by, updated_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13,
		        $14, $15, $16, $17)
		RETURNING ` + nodeColumnList
	created, err := scanNode(r.q.QueryRow(ctx, query,
		n.ID, n.VersionID, nullableInt(n.ParentID), n.Kind, n.Title, n.Description,
		n.Priority, n.Preconditions, jsonParam(n.TestData), n.Source, n.Status,
		n.SortOrder, n.Revision, n.CreatedBy, n.UpdatedBy, n.CreatedAt, n.UpdatedAt,
	))
	if err != nil {
		if uniqueConstraint(err) == "pk_case_node" {
			return domain.CaseNode{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("node %d already exists", n.ID))
		}
		return domain.CaseNode{}, fmt.Errorf("postgres: create case node: %w", err)
	}
	return created, nil
}

// Get returns one node of versionID.
func (r *NodeRepository) Get(ctx context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
	node, err := scanNode(r.q.QueryRow(ctx,
		`SELECT `+nodeColumnList+` FROM case_node WHERE id = $1 AND version_id = $2`,
		nodeID, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseNode{}, notFound("node", nodeID)
		}
		return domain.CaseNode{}, fmt.Errorf("postgres: get case node: %w", err)
	}
	return node, nil
}

// ListByVersion returns every node of the version in display order.
func (r *NodeRepository) ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseNode, error) {
	rows, err := r.q.Query(ctx,
		`SELECT `+nodeColumnList+`
		 FROM case_node
		 WHERE version_id = $1
		 ORDER BY sort_order ASC, id ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list case nodes: %w", err)
	}
	defer rows.Close()

	nodes := make([]domain.CaseNode, 0)
	for rows.Next() {
		node, err := scanNode(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case node: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list case nodes: %w", err)
	}
	return nodes, nil
}

// Update rewrites the mutable columns of n under the optimistic revision lock
// (spec §6.5) and bumps the stored revision by one. A zero-row CAS is always a
// case_revision_conflict, whether the row is gone or another writer already
// bumped it.
func (r *NodeRepository) Update(ctx context.Context, n domain.CaseNode, expectedRevision int) (domain.CaseNode, error) {
	const query = `UPDATE case_node
		SET parent_id = $3, kind = $4, title = $5, description = $6, priority = $7,
		    preconditions = $8, test_data = $9::jsonb, source = $10, status = $11,
		    sort_order = $12, revision = revision + 1, updated_by = $13,
		    updated_at = $14
		WHERE id = $1 AND version_id = $2 AND revision = $15
		RETURNING ` + nodeColumnList
	updated, err := scanNode(r.q.QueryRow(ctx, query,
		n.ID, n.VersionID, nullableInt(n.ParentID), n.Kind, n.Title, n.Description,
		n.Priority, n.Preconditions, jsonParam(n.TestData), n.Source, n.Status,
		n.SortOrder, n.UpdatedBy, n.UpdatedAt, expectedRevision,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseNode{}, revisionConflict(n.ID)
		}
		return domain.CaseNode{}, fmt.Errorf("postgres: update case node: %w", err)
	}
	return updated, nil
}

// Delete removes one node of versionID under the optimistic revision lock. The
// child subtree, steps, assertions and sources go with it through the DDL
// cascades. A zero-row delete is a case_revision_conflict, like a stale Update.
func (r *NodeRepository) Delete(ctx context.Context, versionID, nodeID int64, expectedRevision int) error {
	tag, err := r.q.Exec(ctx,
		`DELETE FROM case_node WHERE id = $1 AND version_id = $2 AND revision = $3`,
		nodeID, versionID, expectedRevision)
	if err != nil {
		return fmt.Errorf("postgres: delete case node: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return revisionConflict(nodeID)
	}
	return nil
}

// UpdateStatusByVersion moves every node of the version from one status to
// another without a per-node CAS (review freeze and version transitions) and
// returns the number of affected rows. The revision is bumped so a client
// holding a pre-freeze revision cannot write stale content afterwards.
func (r *NodeRepository) UpdateStatusByVersion(ctx context.Context, versionID int64, from, to string) (int, error) {
	tag, err := r.q.Exec(ctx,
		`UPDATE case_node
		 SET status = $3, revision = revision + 1, updated_at = now()
		 WHERE version_id = $1 AND status = $2`,
		versionID, from, to)
	if err != nil {
		return 0, fmt.Errorf("postgres: update case node status: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// RestoreStatusByVersion rewrites status and revision exactly for the given
// nodes. It exists only for the approve compensation path (spec §6.4): after a
// failed VersionLifecycle.ApproveBaseline the baseline is deleted and the
// frozen nodes go back to their pre-approval revision.
func (r *NodeRepository) RestoreStatusByVersion(ctx context.Context, versionID int64, nodes []domain.CaseNode) error {
	if len(nodes) == 0 {
		return nil
	}
	ids := make([]int64, len(nodes))
	statuses := make([]string, len(nodes))
	revisions := make([]int32, len(nodes))
	for i, node := range nodes {
		ids[i] = node.ID
		statuses[i] = node.Status
		revisions[i] = int32(node.Revision)
	}
	_, err := r.q.Exec(ctx, `
		UPDATE case_node AS n
		SET status = u.status, revision = u.revision, updated_at = now()
		FROM unnest($2::bigint[], $3::text[], $4::int[]) AS u(id, status, revision)
		WHERE n.id = u.id AND n.version_id = $1`, versionID, ids, statuses, revisions)
	if err != nil {
		return fmt.Errorf("postgres: restore case node status: %w", err)
	}
	return nil
}

// SetSortOrders rewrites sort_order to the 1-based position of every id in
// ordered. The caller (the reorder service) has already validated that ordered
// is the complete, duplicate-free child set of the parent and holds the
// revision set; the repository only persists the new order. Revision is left
// untouched because a reorder is a single version-level operation, not a
// per-node content edit (spec §6.5).
func (r *NodeRepository) SetSortOrders(ctx context.Context, versionID int64, ordered []int64) error {
	if len(ordered) == 0 {
		return nil
	}
	_, err := r.q.Exec(ctx, `
		UPDATE case_node AS n
		SET sort_order = u.ord::int, updated_at = now()
		FROM unnest($2::bigint[]) WITH ORDINALITY AS u(id, ord)
		WHERE n.id = u.id AND n.version_id = $1`, versionID, ordered)
	if err != nil {
		return fmt.Errorf("postgres: set case node sort orders: %w", err)
	}
	return nil
}

// CountByVersion returns the number of nodes and the number of leaf cases of
// one version.
func (r *NodeRepository) CountByVersion(ctx context.Context, versionID int64) (nodes int, cases int, err error) {
	if err := r.q.QueryRow(ctx, `
		SELECT count(*)::int,
		       count(*) FILTER (WHERE kind = 'case')::int
		FROM case_node
		WHERE version_id = $1`, versionID).Scan(&nodes, &cases); err != nil {
		return 0, 0, fmt.Errorf("postgres: count case nodes: %w", err)
	}
	return nodes, cases, nil
}
