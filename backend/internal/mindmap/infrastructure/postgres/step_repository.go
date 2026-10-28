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

// stepColumnList is the shared column order of every case_step query and the
// order scanStep expects.
const stepColumnList = `id, node_id, seq, action, expected, data, source, created_at, updated_at`

// StepRepository implements application.StepRepository on a pgx querier.
type StepRepository struct {
	q querier
}

// NewStepRepository returns a case-step repository backed by pool.
func NewStepRepository(pool *pgxpool.Pool) application.StepRepository {
	return newStepRepository(pool)
}

// newStepRepository binds the repository to q.
func newStepRepository(q querier) *StepRepository {
	return &StepRepository{q: q}
}

var _ application.StepRepository = (*StepRepository)(nil)

// scanStep scans one row in stepColumnList order.
func scanStep(row rowScanner) (domain.CaseStep, error) {
	var step domain.CaseStep
	if err := row.Scan(
		&step.ID,
		&step.NodeID,
		&step.Seq,
		&step.Action,
		&step.Expected,
		&step.Data,
		&step.Source,
		&step.CreatedAt,
		&step.UpdatedAt,
	); err != nil {
		return domain.CaseStep{}, err
	}
	return step, nil
}

// ListByVersion returns every step of the version ordered by owning case and
// sequence.
func (r *StepRepository) ListByVersion(ctx context.Context, versionID int64) ([]domain.CaseStep, error) {
	rows, err := r.q.Query(ctx, `
		SELECT s.id, s.node_id, s.seq, s.action, s.expected, s.data, s.source,
		       s.created_at, s.updated_at
		FROM case_step AS s
		JOIN case_node AS n ON n.id = s.node_id
		WHERE n.version_id = $1
		ORDER BY s.node_id ASC, s.seq ASC, s.id ASC`, versionID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list case steps: %w", err)
	}
	defer rows.Close()

	steps := make([]domain.CaseStep, 0)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case step: %w", err)
		}
		steps = append(steps, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list case steps: %w", err)
	}
	return steps, nil
}

// Create inserts s with its explicit id and writes the stored row back.
func (r *StepRepository) Create(ctx context.Context, s domain.CaseStep) (domain.CaseStep, error) {
	const query = `INSERT INTO case_step
		(id, node_id, seq, action, expected, data, source, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + stepColumnList
	created, err := scanStep(r.q.QueryRow(ctx, query,
		s.ID, s.NodeID, s.Seq, s.Action, s.Expected, s.Data, s.Source,
		s.CreatedAt, s.UpdatedAt,
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_step":
			return domain.CaseStep{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("step %d already exists", s.ID))
		case "uk_case_step_node_seq":
			// Another writer touched the node's step subset concurrently: the
			// application layer's revision set is stale, not the id duplicate.
			return domain.CaseStep{}, domain.NewStateConflict("case_revision_conflict",
				fmt.Sprintf("node %d already has a step at seq %d", s.NodeID, s.Seq))
		}
		return domain.CaseStep{}, fmt.Errorf("postgres: create case step: %w", err)
	}
	return created, nil
}

// Update rewrites the content columns of s. The node revision CAS around the
// step subset is owned by the application service (spec §6.5); the repository
// only persists the row and reports a missing row as step_not_found.
func (r *StepRepository) Update(ctx context.Context, s domain.CaseStep) error {
	tag, err := r.q.Exec(ctx, `
		UPDATE case_step
		SET seq = $3, action = $4, expected = $5, data = $6, source = $7,
		    updated_at = $8
		WHERE id = $1 AND node_id = $2`,
		s.ID, s.NodeID, s.Seq, s.Action, s.Expected, s.Data, s.Source, s.UpdatedAt)
	if err != nil {
		if uniqueConstraint(err) == "uk_case_step_node_seq" {
			return domain.NewStateConflict("case_revision_conflict",
				fmt.Sprintf("node %d already has a step at seq %d", s.NodeID, s.Seq))
		}
		return fmt.Errorf("postgres: update case step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("step", s.ID)
	}
	return nil
}

// Delete removes one step of nodeID.
func (r *StepRepository) Delete(ctx context.Context, nodeID, stepID int64) error {
	tag, err := r.q.Exec(ctx,
		`DELETE FROM case_step WHERE id = $1 AND node_id = $2`, stepID, nodeID)
	if err != nil {
		return fmt.Errorf("postgres: delete case step: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return notFound("step", stepID)
	}
	return nil
}

// CountByNode returns the number of steps of one case.
func (r *StepRepository) CountByNode(ctx context.Context, nodeID int64) (int, error) {
	var count int
	if err := r.q.QueryRow(ctx,
		`SELECT count(*)::int FROM case_step WHERE node_id = $1`, nodeID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count case steps: %w", err)
	}
	return count, nil
}

// ReplaceSeq rewrites the 1-based seq of the node's steps in the given order
// inside one transaction. ordered must be the complete, duplicate-free set of
// the node's steps (the PUT /nodes/{nodeId}/steps/order contract of spec §7.2).
//
// The rewrite cannot be a single UPDATE ... FROM unnest: uk_case_step_node_seq
// is checked per row, so any permutation (1,2 -> 2,1) fails with 23505 before
// the statement completes. The repository instead moves rows one at a time into
// slots that are already free, parking one row on a spare seq to unwind a
// cycle. When all 30 slots are occupied there is no spare seq (and seq has no
// value outside the 1..30 CHECK), so the step set is rebuilt around the
// assertions that reference it (see rebuildStepSeq).
//
// The transaction first locks the case_node row: two concurrent reorders of the
// same node would otherwise move the same rows in opposite directions and
// deadlock (SQLSTATE 40P01). The lock serializes them; reorders of different
// nodes stay independent.
func (r *StepRepository) ReplaceSeq(ctx context.Context, nodeID int64, ordered []int64) error {
	return withTx(ctx, r.q, func(q querier) error {
		if err := lockCaseNode(ctx, q, nodeID); err != nil {
			return err
		}
		return replaceStepSeq(ctx, q, nodeID, ordered)
	})
}

// lockCaseNode takes the row lock of nodeID for the rest of the transaction. It
// is the sequence rewrite's serialization point: the node row is locked before
// any case_step row, so concurrent same-node reorders queue up instead of
// deadlocking. A missing node is node_not_found.
func lockCaseNode(ctx context.Context, q querier, nodeID int64) error {
	var one int
	if err := q.QueryRow(ctx,
		`SELECT 1 FROM case_node WHERE id = $1 FOR UPDATE`, nodeID).Scan(&one); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("node", nodeID)
		}
		return fmt.Errorf("postgres: lock case node %d: %w", nodeID, err)
	}
	return nil
}

// replaceStepSeq implements ReplaceSeq inside the transaction q.
func replaceStepSeq(ctx context.Context, q querier, nodeID int64, ordered []int64) error {
	steps, err := loadStepsByNode(ctx, q, nodeID)
	if err != nil {
		return err
	}
	if len(ordered) == 0 {
		return fmt.Errorf("postgres: replace step seq: the ordered list is empty")
	}
	if len(ordered) > domain.MaxStepsPerCase {
		return fmt.Errorf("postgres: replace step seq: %d steps exceed the %d-step limit",
			len(ordered), domain.MaxStepsPerCase)
	}
	seen := make(map[int64]struct{}, len(ordered))
	for _, id := range ordered {
		if _, ok := steps[id]; !ok {
			return notFound("step", id)
		}
		if _, duplicate := seen[id]; duplicate {
			return fmt.Errorf("postgres: replace step seq: duplicate step %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(steps) != len(ordered) {
		return fmt.Errorf("postgres: replace step seq: ordered list has %d steps, node %d has %d",
			len(ordered), nodeID, len(steps))
	}

	current := make(map[int64]int, len(steps))
	occupied := make(map[int]int64, len(steps))
	for id, step := range steps {
		current[id] = step.Seq
		occupied[step.Seq] = id
	}
	misplaced := 0
	for i, id := range ordered {
		if current[id] != i+1 {
			misplaced++
		}
	}
	if misplaced == 0 {
		return nil
	}

	move := func(id int64, seq int) error {
		old := current[id]
		if old == seq {
			return nil
		}
		if _, err := q.Exec(ctx,
			`UPDATE case_step SET seq = $3 WHERE id = $1 AND node_id = $2`,
			id, nodeID, seq); err != nil {
			return fmt.Errorf("postgres: replace step seq: move step %d: %w", id, err)
		}
		delete(occupied, old)
		occupied[seq] = id
		current[id] = seq
		return nil
	}

	for moves := 0; misplaced > 0; {
		if moves > 4*len(ordered)+domain.MaxStepsPerCase {
			return fmt.Errorf("postgres: replace step seq: did not converge for node %d", nodeID)
		}
		progressed := false
		for i, id := range ordered {
			want := i + 1
			if current[id] == want {
				continue
			}
			if _, taken := occupied[want]; taken {
				continue
			}
			if err := move(id, want); err != nil {
				return err
			}
			misplaced--
			moves++
			progressed = true
		}
		if misplaced == 0 {
			return nil
		}
		if progressed {
			continue
		}
		spare := firstFreeSeq(occupied)
		if spare == 0 {
			return rebuildStepSeq(ctx, q, nodeID, ordered, steps)
		}
		// Every remaining target slot is occupied, so the remaining rows form
		// cycles. Park one of them on the spare slot; its old slot becomes free
		// and the greedy pass above can unwind the cycle.
		parked := false
		for i, id := range ordered {
			if current[id] == i+1 {
				continue
			}
			if err := move(id, spare); err != nil {
				return err
			}
			moves++
			parked = true
			break
		}
		if !parked {
			return fmt.Errorf("postgres: replace step seq: no movable step for node %d", nodeID)
		}
	}
	return nil
}

// loadStepsByNode reads the current step rows of one case.
func loadStepsByNode(ctx context.Context, q querier, nodeID int64) (map[int64]domain.CaseStep, error) {
	rows, err := q.Query(ctx,
		`SELECT `+stepColumnList+` FROM case_step WHERE node_id = $1 ORDER BY seq ASC`, nodeID)
	if err != nil {
		return nil, fmt.Errorf("postgres: load case steps: %w", err)
	}
	defer rows.Close()

	steps := make(map[int64]domain.CaseStep)
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan case step: %w", err)
		}
		steps[step.ID] = step
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: load case steps: %w", err)
	}
	return steps, nil
}

// firstFreeSeq returns the first seq between 1 and domain.MaxStepsPerCase that
// no step of the node occupies, or 0 when the node uses every slot.
func firstFreeSeq(occupied map[int]int64) int {
	for seq := 1; seq <= domain.MaxStepsPerCase; seq++ {
		if _, taken := occupied[seq]; !taken {
			return seq
		}
	}
	return 0
}

// rebuildStepSeq is the full-occupancy fallback of ReplaceSeq: all 1..30 seq
// slots are taken, so no row can move into a target slot and seq has no
// out-of-range value (CHECK 1..30). The step rows are replaced instead: the
// assertions that reference them are temporarily unlinked (their FK is ON
// DELETE CASCADE and would otherwise drop them), the steps are reinserted with
// the new seqs and preserved timestamps, and the links are restored. Everything
// runs in the caller's transaction, so a concurrent reader sees either the old
// or the new sequence, never the unlinked state.
func rebuildStepSeq(ctx context.Context, q querier, nodeID int64, ordered []int64, steps map[int64]domain.CaseStep) error {
	stepIDs := make([]int64, 0, len(steps))
	for id := range steps {
		stepIDs = append(stepIDs, id)
	}

	type assertionLink struct {
		assertionID int64
		stepID      int64
	}
	rows, err := q.Query(ctx,
		`SELECT id, step_id FROM case_assertion WHERE step_id = ANY($1::bigint[])`, stepIDs)
	if err != nil {
		return fmt.Errorf("postgres: replace step seq: load assertion links: %w", err)
	}
	links := make([]assertionLink, 0)
	for rows.Next() {
		var link assertionLink
		if err := rows.Scan(&link.assertionID, &link.stepID); err != nil {
			rows.Close()
			return fmt.Errorf("postgres: replace step seq: scan assertion link: %w", err)
		}
		links = append(links, link)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("postgres: replace step seq: load assertion links: %w", err)
	}
	rows.Close()

	if len(links) > 0 {
		if _, err := q.Exec(ctx,
			`UPDATE case_assertion SET step_id = NULL WHERE step_id = ANY($1::bigint[])`,
			stepIDs); err != nil {
			return fmt.Errorf("postgres: replace step seq: unlink assertion links: %w", err)
		}
	}
	if _, err := q.Exec(ctx, `DELETE FROM case_step WHERE node_id = $1`, nodeID); err != nil {
		return fmt.Errorf("postgres: replace step seq: delete steps: %w", err)
	}
	for i, id := range ordered {
		step := steps[id]
		if _, err := q.Exec(ctx, `
			INSERT INTO case_step
				(id, node_id, seq, action, expected, data, source, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
			step.ID, nodeID, i+1, step.Action, step.Expected, step.Data,
			step.Source, step.CreatedAt, step.UpdatedAt); err != nil {
			return fmt.Errorf("postgres: replace step seq: reinsert step %d: %w", step.ID, err)
		}
	}
	for _, link := range links {
		if _, err := q.Exec(ctx,
			`UPDATE case_assertion SET step_id = $1 WHERE id = $2`,
			link.stepID, link.assertionID); err != nil {
			return fmt.Errorf("postgres: replace step seq: restore assertion link: %w", err)
		}
	}
	return nil
}
