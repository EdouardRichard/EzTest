package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// exportColumnList is the shared column order of every case_export query and
// the order scanExport expects.
const exportColumnList = `id, version_id, format, file_path, file_sha256,
	size_bytes, baseline_id, node_count, created_by, created_at, updated_at`

// ExportRepository implements application.ExportRepository on a pgx querier.
type ExportRepository struct {
	q querier
}

// NewExportRepository returns a case-export repository backed by pool.
func NewExportRepository(pool *pgxpool.Pool) application.ExportRepository {
	return newExportRepository(pool)
}

// newExportRepository binds the repository to q.
func newExportRepository(q querier) *ExportRepository {
	return &ExportRepository{q: q}
}

var _ application.ExportRepository = (*ExportRepository)(nil)

// exportNotFound is the version-scoped not-found of the reuse lookup.
func exportNotFound(versionID int64) error {
	return &domain.Error{
		Kind:    domain.KindNotFound,
		Reason:  "export_not_found",
		Message: fmt.Sprintf("version %d has no matching export", versionID),
	}
}

// scanExport scans one row in exportColumnList order.
func scanExport(row rowScanner) (domain.CaseExport, error) {
	var export domain.CaseExport
	if err := row.Scan(
		&export.ID,
		&export.VersionID,
		&export.Format,
		&export.FilePath,
		&export.FileSHA256,
		&export.SizeBytes,
		&export.BaselineID,
		&export.NodeCount,
		&export.CreatedBy,
		&export.CreatedAt,
		&export.UpdatedAt,
	); err != nil {
		return domain.CaseExport{}, err
	}
	return export, nil
}

// Create inserts e with its explicit id and the caller-supplied timestamps. A
// second export of the same frozen (version, format, baseline) violates
// uk_case_export_baseline; the service looks the reusable export up first, so a
// collision is a concurrent export race and maps to export_conflict (spec
// §6.10).
func (r *ExportRepository) Create(ctx context.Context, e domain.CaseExport) (domain.CaseExport, error) {
	const query = `INSERT INTO case_export
		(id, version_id, format, file_path, file_sha256, size_bytes,
		 baseline_id, node_count, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING ` + exportColumnList
	created, err := scanExport(r.q.QueryRow(ctx, query,
		e.ID, e.VersionID, e.Format, e.FilePath, e.FileSHA256, e.SizeBytes,
		nullableInt(e.BaselineID), e.NodeCount, e.CreatedBy, e.CreatedAt,
		e.UpdatedAt,
	))
	if err != nil {
		switch uniqueConstraint(err) {
		case "pk_case_export":
			return domain.CaseExport{}, domain.NewConflict("duplicate_key",
				fmt.Sprintf("export %d already exists", e.ID))
		case "uk_case_export_baseline":
			// The service looks the reusable export up first, so a collision is
			// a concurrent frozen-state export race (spec §6.10).
			return domain.CaseExport{}, domain.NewConflict("export_conflict",
				fmt.Sprintf("version %d already has a %s export for this baseline", e.VersionID, e.Format))
		}
		return domain.CaseExport{}, fmt.Errorf("postgres: create case export: %w", err)
	}
	return created, nil
}

// Get returns one export of versionID.
func (r *ExportRepository) Get(ctx context.Context, versionID, exportID int64) (domain.CaseExport, error) {
	export, err := scanExport(r.q.QueryRow(ctx,
		`SELECT `+exportColumnList+` FROM case_export WHERE id = $1 AND version_id = $2`,
		exportID, versionID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseExport{}, notFound("export", exportID)
		}
		return domain.CaseExport{}, fmt.Errorf("postgres: get case export: %w", err)
	}
	return export, nil
}

// List returns one page ordered by created_at DESC, id DESC with an opaque
// keyset cursor. An empty format matches every format.
func (r *ExportRepository) List(ctx context.Context, versionID int64, format string, limit int, cursor string) ([]domain.CaseExport, string, bool, error) {
	if limit <= 0 {
		return nil, "", false, fmt.Errorf("postgres: list case exports: limit must be positive")
	}
	cursorID, err := platform.DecodeCursor(cursor)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case exports: %w", err)
	}
	var (
		cursorCreated time.Time
		hasCursor     bool
	)
	if cursorID != 0 {
		if err := r.q.QueryRow(ctx,
			`SELECT created_at FROM case_export WHERE id = $1 AND version_id = $2`,
			cursorID, versionID).Scan(&cursorCreated); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return []domain.CaseExport{}, "", false, nil
			}
			return nil, "", false, fmt.Errorf("postgres: list case exports: read cursor: %w", err)
		}
		hasCursor = true
	}

	const query = `SELECT ` + exportColumnList + `
		FROM case_export
		WHERE version_id = $1
		  AND ($2 = '' OR format = $2)
		  AND ($3::boolean = false OR (created_at, id) < ($4::timestamptz, $5::bigint))
		ORDER BY created_at DESC, id DESC
		LIMIT $6`
	rows, err := r.q.Query(ctx, query, versionID, format, hasCursor, cursorCreated, cursorID, limit+1)
	if err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case exports: %w", err)
	}
	defer rows.Close()

	exports := make([]domain.CaseExport, 0, limit+1)
	for rows.Next() {
		export, err := scanExport(rows)
		if err != nil {
			return nil, "", false, fmt.Errorf("postgres: scan case export: %w", err)
		}
		exports = append(exports, export)
	}
	if err := rows.Err(); err != nil {
		return nil, "", false, fmt.Errorf("postgres: list case exports: %w", err)
	}

	more := len(exports) > limit
	nextCursor := ""
	if more {
		exports = exports[:limit]
		nextCursor = platform.EncodeCursor(exports[len(exports)-1].ID)
	}
	return exports, nextCursor, more, nil
}

// FindByBaselineFormat returns the reusable export of the same (version,
// format, baseline): after a freeze the export is bound to the baseline, before
// it baselineID is nil and matches the unfrozen export. A nil result is
// export_not_found, which the export service treats as "generate one".
func (r *ExportRepository) FindByBaselineFormat(ctx context.Context, versionID int64, format string, baselineID *int64) (domain.CaseExport, error) {
	var (
		query string
		args  []any
	)
	if baselineID == nil {
		query = `SELECT ` + exportColumnList + `
			FROM case_export
			WHERE version_id = $1 AND format = $2 AND baseline_id IS NULL
			ORDER BY created_at DESC, id DESC
			LIMIT 1`
		args = []any{versionID, format}
	} else {
		query = `SELECT ` + exportColumnList + `
			FROM case_export
			WHERE version_id = $1 AND format = $2 AND baseline_id = $3
			ORDER BY created_at DESC, id DESC
			LIMIT 1`
		args = []any{versionID, format, *baselineID}
	}
	export, err := scanExport(r.q.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.CaseExport{}, exportNotFound(versionID)
		}
		return domain.CaseExport{}, fmt.Errorf("postgres: find reusable case export: %w", err)
	}
	return export, nil
}
