package versionpg

import (
	"context"
	"fmt"

	designapp "github.com/EdouardRichard/EzTest/backend/internal/design/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PendingDesignCounter is the design context's application read surface consumed
// by the version reader. The pending design count is owned by the design
// context, so the version reader never joins design tables: main injects the
// design query service, which satisfies this interface. The batch call keeps the
// design_pending todo build free of N+1 reads.
type PendingDesignCounter interface {
	PendingDesignCounts(ctx context.Context, versionIDs []int64) (map[int64]int, error)
}

var _ PendingDesignCounter = (*designapp.QueryService)(nil)

var _ versionapp.QueryReader = (*QueryReader)(nil)

// QueryReader is the version context's P6 read-only source. It reads the
// version/requirement tables and obtains the pending design count through the
// injected design application port.
type QueryReader struct {
	pool          *pgxpool.Pool
	pendingDesign PendingDesignCounter
}

// NewQueryReader wires the version read source. A nil pool or missing design
// port is a wiring error.
func NewQueryReader(pool *pgxpool.Pool, pendingDesign PendingDesignCounter) (*QueryReader, error) {
	if pool == nil {
		return nil, fmt.Errorf("version query reader: nil pool")
	}
	if pendingDesign == nil {
		return nil, fmt.Errorf("version query reader: pending design counter is not configured")
	}
	return &QueryReader{pool: pool, pendingDesign: pendingDesign}, nil
}

// CountRequirementClosure returns closed requirements over the countable
// requirements (draft+clarified+closed; obsolete excluded), for one version when
// versionID is non-nil and globally otherwise.
func (r *QueryReader) CountRequirementClosure(ctx context.Context, versionID *int64) (int, int, error) {
	var num, denom int
	if err := r.pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE status = 'closed'),
			count(*) FILTER (WHERE status IN ('draft', 'clarified', 'closed'))
		FROM requirement
		WHERE $1::bigint IS NULL OR version_id = $1
	`, versionID).Scan(&num, &denom); err != nil {
		return 0, 0, fmt.Errorf("count requirement closure: %w", err)
	}
	return num, denom, nil
}

// SearchVersions returns version title matches, newest first.
func (r *QueryReader) SearchVersions(ctx context.Context, q string, limit int) ([]versionapp.VersionSearchRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, title, status, updated_at
		FROM version
		WHERE title ILIKE '%' || $1 || '%'
		ORDER BY updated_at DESC, id DESC
		LIMIT $2
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search versions: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.VersionSearchRow, 0)
	for rows.Next() {
		var item versionapp.VersionSearchRow
		if err := rows.Scan(&item.ID, &item.Title, &item.Status, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan version search row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan version search rows: %w", err)
	}
	return items, nil
}

// SearchRequirements returns current requirement-revision title matches with
// their version context, newest first. Historical revisions never match.
func (r *QueryReader) SearchRequirements(ctx context.Context, q string, limit int) ([]versionapp.RequirementSearchRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.version_id, v.title, rv.title, r.status, r.updated_at
		FROM requirement r
		JOIN version v ON v.id = r.version_id
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE rv.title ILIKE '%' || $1 || '%'
		ORDER BY r.updated_at DESC, r.id DESC
		LIMIT $2
	`, q, limit)
	if err != nil {
		return nil, fmt.Errorf("search requirements: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.RequirementSearchRow, 0)
	for rows.Next() {
		var item versionapp.RequirementSearchRow
		if err := rows.Scan(&item.ID, &item.VersionID, &item.VersionTitle,
			&item.Title, &item.Status, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan requirement search row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan requirement search rows: %w", err)
	}
	return items, nil
}

// VersionTitles resolves the title of every existing id in one batch.
func (r *QueryReader) VersionTitles(ctx context.Context, ids []int64) (map[int64]string, error) {
	titles := make(map[int64]string, len(ids))
	if len(ids) == 0 {
		return titles, nil
	}
	rows, err := r.pool.Query(ctx, `
		SELECT id, title FROM version WHERE id = ANY($1)
	`, ids)
	if err != nil {
		return nil, fmt.Errorf("read version titles: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int64
		var title string
		if err := rows.Scan(&id, &title); err != nil {
			return nil, fmt.Errorf("scan version title: %w", err)
		}
		titles[id] = title
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan version titles: %w", err)
	}
	return titles, nil
}

// ListPendingDesignVersions returns the versions with a positive pending design
// count. The version read is one SQL page and the pending counts are a single
// batch application call to the design context, so the todo build is free of
// N+1 cross-context reads.
func (r *QueryReader) ListPendingDesignVersions(ctx context.Context) ([]versionapp.PendingDesignVersionRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, title
		FROM version
		ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("list versions for pending design: %w", err)
	}
	defer rows.Close()

	type pendingVersion struct {
		id    int64
		title string
	}
	versions := make([]pendingVersion, 0)
	for rows.Next() {
		var item pendingVersion
		if err := rows.Scan(&item.id, &item.title); err != nil {
			return nil, fmt.Errorf("scan version for pending design: %w", err)
		}
		versions = append(versions, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan versions for pending design: %w", err)
	}
	if len(versions) == 0 {
		return []versionapp.PendingDesignVersionRow{}, nil
	}

	ids := make([]int64, 0, len(versions))
	for _, item := range versions {
		ids = append(ids, item.id)
	}
	counts, err := r.pendingDesign.PendingDesignCounts(ctx, ids)
	if err != nil {
		return nil, fmt.Errorf("read pending design counts: %w", err)
	}

	items := make([]versionapp.PendingDesignVersionRow, 0)
	for _, item := range versions {
		if count := counts[item.id]; count > 0 {
			items = append(items, versionapp.PendingDesignVersionRow{
				VersionID:    item.id,
				VersionTitle: item.title,
				PendingCount: count,
			})
		}
	}
	return items, nil
}

// ListDraftRequirements returns the current draft requirements, newest first.
func (r *QueryReader) ListDraftRequirements(ctx context.Context, limit int) ([]versionapp.DraftRequirementRow, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT r.id, r.version_id, v.title, rv.title, r.created_at
		FROM requirement r
		JOIN version v ON v.id = r.version_id
		JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.status = 'draft'
		ORDER BY r.created_at DESC, r.id DESC
		LIMIT $1
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list draft requirements: %w", err)
	}
	defer rows.Close()

	items := make([]versionapp.DraftRequirementRow, 0)
	for rows.Next() {
		var item versionapp.DraftRequirementRow
		if err := rows.Scan(&item.ID, &item.VersionID, &item.VersionTitle,
			&item.Title, &item.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan draft requirement: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan draft requirements: %w", err)
	}
	return items, nil
}
