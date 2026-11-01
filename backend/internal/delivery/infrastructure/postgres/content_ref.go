package deliverypg

import (
	"context"
	"fmt"

	deliveryapp "github.com/EdouardRichard/EzTest/backend/internal/delivery/application"
	"github.com/EdouardRichard/EzTest/backend/internal/delivery/infrastructure/content"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ContentRefReader resolves content_ref rows by ID for the report content
// adapter. The P0 storage application service has no by-ID ref lookup, so the
// delivery infrastructure layer owns this small read (SQL stays in
// infrastructure/postgres).
type ContentRefReader struct {
	pool *pgxpool.Pool
}

var _ content.RefReader = (*ContentRefReader)(nil)

// NewContentRefReader wraps a pgx pool.
func NewContentRefReader(pool *pgxpool.Pool) *ContentRefReader {
	return &ContentRefReader{pool: pool}
}

// GetContentRef returns one active content_ref row together with the §6.4
// owner check: a delivery_report_revision whose id equals owner_id must point
// its html_ref at this exact ref. A missing or soft-deleted ref is
// ErrReportContentNotFound.
func (r *ContentRefReader) GetContentRef(ctx context.Context, refID int64) (content.Ref, error) {
	ctx = contextOrBackground(ctx)
	var ref content.Ref
	err := r.pool.QueryRow(ctx, `
		SELECT r.object_id, r.owner_kind, r.owner_id, r.role,
		       EXISTS (
			       SELECT 1
			       FROM delivery_report_revision rev
			       WHERE rev.id = r.owner_id AND rev.html_ref = r.id
		       )
		FROM content_ref r
		WHERE r.id = $1 AND r.deleted_at IS NULL
	`, refID).Scan(&ref.ObjectID, &ref.OwnerKind, &ref.OwnerID, &ref.Role, &ref.OwnedByRevision)
	if isNoRows(err) {
		return content.Ref{}, deliveryapp.ErrReportContentNotFound
	}
	if err != nil {
		return content.Ref{}, fmt.Errorf("get content ref: %w", err)
	}
	return ref, nil
}
