package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// evidenceRefColumnList is the shared column order of every evidence_ref query
// and the order scanEvidenceRef expects.
const evidenceRefColumnList = `evidence_id, ref_kind, ref_id, created_at`

// EvidenceRefRepository implements application.EvidenceRefRepository on a pgx
// querier.
type EvidenceRefRepository struct {
	q querier
}

// NewEvidenceRefRepository returns an evidence-ref repository backed by pool.
func NewEvidenceRefRepository(pool *pgxpool.Pool) application.EvidenceRefRepository {
	return &EvidenceRefRepository{q: pool}
}

var _ application.EvidenceRefRepository = (*EvidenceRefRepository)(nil)

// scanEvidenceRef scans one row in evidenceRefColumnList order.
func scanEvidenceRef(row rowScanner) (domain.EvidenceRef, error) {
	var ref domain.EvidenceRef
	if err := row.Scan(&ref.EvidenceID, &ref.RefKind, &ref.RefID, &ref.CreatedAt); err != nil {
		return domain.EvidenceRef{}, err
	}
	return ref, nil
}

// Create inserts one evidence link. The primary key makes the call idempotent:
// a repeated link returns the already stored row and timestamp.
func (r *EvidenceRefRepository) Create(ctx context.Context, ref domain.EvidenceRef) (domain.EvidenceRef, error) {
	var createdAt time.Time
	err := r.q.QueryRow(ctx, `
		INSERT INTO evidence_ref (evidence_id, ref_kind, ref_id, created_at)
		VALUES ($1, $2, $3, COALESCE($4, now()))
		ON CONFLICT (evidence_id, ref_kind, ref_id) DO NOTHING
		RETURNING created_at`,
		ref.EvidenceID, ref.RefKind, ref.RefID, timeOrNow(ref.CreatedAt)).Scan(&createdAt)
	if err == nil {
		ref.CreatedAt = createdAt
		return ref, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return domain.EvidenceRef{}, fmt.Errorf("postgres: create evidence ref: %w", err)
	}
	// The link already exists: return the stored row instead of failing, so a
	// retried assertion/evidence recording is idempotent.
	err = r.q.QueryRow(ctx,
		`SELECT created_at FROM evidence_ref
		 WHERE evidence_id = $1 AND ref_kind = $2 AND ref_id = $3`,
		ref.EvidenceID, ref.RefKind, ref.RefID).Scan(&createdAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.EvidenceRef{}, fmt.Errorf(
				"%w: evidence ref %d/%s/%d", application.ErrConflict,
				ref.EvidenceID, ref.RefKind, ref.RefID)
		}
		return domain.EvidenceRef{}, fmt.Errorf("postgres: read existing evidence ref: %w", err)
	}
	ref.CreatedAt = createdAt
	return ref, nil
}
