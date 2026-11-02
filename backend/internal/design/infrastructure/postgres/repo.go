// Package designpg is the PostgreSQL adapter for the P2 design context. It
// reads version/requirement/conversation rows (cross-context logical
// references) and owns every write to the design tables.
package designpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository implements design.Repository on top of one pgx pool.
type Repository struct {
	pool *pgxpool.Pool
}

var _ design.Repository = (*Repository)(nil)

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

// querier is satisfied by both *pgxpool.Pool and pgx.Tx.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func (r *Repository) VersionContext(ctx context.Context, versionID int64) (design.VersionContext, error) {
	var view design.VersionContext
	var autoDesign bool
	if err := r.pool.QueryRow(ctx, `
		SELECT id, status, auto_design_new_requirements
		FROM version
		WHERE id = $1
	`, versionID).Scan(&view.VersionID, &view.Status, &autoDesign); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.VersionContext{}, design.ErrNotFound
		}
		return design.VersionContext{}, fmt.Errorf("read version context: %w", err)
	}
	view.AutoDesignNewRequirements = autoDesign
	view.PageToSUT = map[int64]int64{}
	view.APIToSUT = map[int64]int64{}
	view.DBTOSUT = map[int64]int64{}

	rows, err := r.pool.Query(ctx, `
		SELECT id, scope
		FROM version_sut
		WHERE version_id = $1 AND status = 'active'
		ORDER BY id
	`, versionID)
	if err != nil {
		return design.VersionContext{}, fmt.Errorf("read version bindings: %w", err)
	}
	versionSUTIDs := make([]int64, 0)
	for rows.Next() {
		var versionSUTID int64
		var rawScope []byte
		if err := rows.Scan(&versionSUTID, &rawScope); err != nil {
			rows.Close()
			return design.VersionContext{}, fmt.Errorf("scan version binding: %w", err)
		}
		var scope struct {
			Pages []int64 `json:"pages"`
			APIs  []int64 `json:"apis"`
		}
		if len(rawScope) > 0 {
			if err := json.Unmarshal(rawScope, &scope); err != nil {
				rows.Close()
				return design.VersionContext{}, fmt.Errorf("decode version binding scope: %w", err)
			}
		}
		for _, pageID := range scope.Pages {
			view.PageToSUT[pageID] = versionSUTID
		}
		for _, apiID := range scope.APIs {
			view.APIToSUT[apiID] = versionSUTID
		}
		versionSUTIDs = append(versionSUTIDs, versionSUTID)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return design.VersionContext{}, fmt.Errorf("scan version bindings: %w", err)
	}
	view.AllowedVersionSUTs = versionSUTIDs

	if len(versionSUTIDs) > 0 {
		resourceRows, err := r.pool.Query(ctx, `
			SELECT version_sut_id, resource_id
			FROM version_sut_resource
			WHERE version_sut_id = ANY($1) AND kind = 'database' AND selected = true
		`, versionSUTIDs)
		if err != nil {
			return design.VersionContext{}, fmt.Errorf("read version databases: %w", err)
		}
		defer resourceRows.Close()
		for resourceRows.Next() {
			var versionSUTID, databaseID int64
			if err := resourceRows.Scan(&versionSUTID, &databaseID); err != nil {
				return design.VersionContext{}, fmt.Errorf("scan version database: %w", err)
			}
			view.DBTOSUT[databaseID] = versionSUTID
		}
		if err := resourceRows.Err(); err != nil {
			return design.VersionContext{}, fmt.Errorf("scan version databases: %w", err)
		}
	}
	return view, nil
}

func (r *Repository) RequirementContext(ctx context.Context, versionID, requirementID int64) (design.RequirementRef, error) {
	return requirementRef(ctx, r.pool, versionID, requirementID)
}

func requirementRef(ctx context.Context, q querier, versionID, requirementID int64) (design.RequirementRef, error) {
	var ref design.RequirementRef
	var currentVersionID *int64
	var revision *int
	var title *string
	var targetSUTs []byte
	err := q.QueryRow(ctx, `
		SELECT r.id, r.version_id, r.current_version_id, rv.revision, rv.title, r.status,
		       COALESCE(rv.target_version_suts, '[]'::jsonb)
		FROM requirement r
		LEFT JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.id = $1 AND r.version_id = $2
	`, requirementID, versionID).Scan(
		&ref.ID, &ref.VersionID, &currentVersionID, &revision, &title, &ref.Status, &targetSUTs,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return design.RequirementRef{}, design.ErrNotFound
	}
	if err != nil {
		return design.RequirementRef{}, fmt.Errorf("read requirement context: %w", err)
	}
	if currentVersionID != nil {
		ref.CurrentVersionID = *currentVersionID
	}
	if revision != nil {
		ref.Revision = *revision
	}
	if title != nil {
		ref.Title = *title
	}
	if ref.TargetVersionSUTIDs, err = decodeIDArray(targetSUTs); err != nil {
		return design.RequirementRef{}, err
	}
	return ref, nil
}

func (r *Repository) CountPendingDesign(ctx context.Context, versionID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM requirement r
		WHERE r.version_id = $1
		  AND r.status = 'clarified'
		  AND NOT EXISTS (
		      SELECT 1 FROM design_scope ds
		      WHERE ds.version_id = $1 AND ds.requirement_id = r.id AND ds.status = 'active'
		  )
	`, versionID).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count pending design requirements: %w", err)
	}
	return count, nil
}

func (r *Repository) CaseCounts(ctx context.Context, versionID int64) (design.CaseCounts, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT cv.status, count(*)
		FROM case_entity ce
		JOIN case_version cv ON cv.id = ce.current_version_id
		JOIN case_scenario s ON s.id = ce.scenario_id
		WHERE s.version_id = $1
		GROUP BY cv.status
	`, versionID)
	if err != nil {
		return design.CaseCounts{}, fmt.Errorf("count current case versions: %w", err)
	}
	defer rows.Close()
	var counts design.CaseCounts
	for rows.Next() {
		var status string
		var value int
		if err := rows.Scan(&status, &value); err != nil {
			return design.CaseCounts{}, fmt.Errorf("scan case count: %w", err)
		}
		switch status {
		case design.CaseStatusDraft:
			counts.Draft = value
		case design.CaseStatusConfirmed:
			counts.Confirmed = value
		case design.CaseStatusStale:
			counts.Stale = value
		}
	}
	if err := rows.Err(); err != nil {
		return design.CaseCounts{}, fmt.Errorf("scan case counts: %w", err)
	}
	return counts, nil
}

func (r *Repository) AutoDesignEnabled(ctx context.Context, versionID int64) (bool, error) {
	var enabled bool
	if err := r.pool.QueryRow(ctx, `
		SELECT auto_design_new_requirements FROM version WHERE id = $1
	`, versionID).Scan(&enabled); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, design.ErrNotFound
		}
		return false, fmt.Errorf("read auto design flag: %w", err)
	}
	return enabled, nil
}

// lockRequirement serializes graph proposals and confirmations of one
// (version, requirement) pair for the duration of the transaction. The
// formatted string is hashed to a stable bigint advisory-lock key.
func lockRequirement(ctx context.Context, tx pgx.Tx, versionID, requirementID int64) error {
	key := fmt.Sprintf("design:requirement:%d:%d", versionID, requirementID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("acquire design requirement lock: %w", err)
	}
	return nil
}

// lockVersion serializes planner rounds and scans of one version.
func lockVersion(ctx context.Context, tx pgx.Tx, versionID int64) error {
	key := fmt.Sprintf("design:version:%d", versionID)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("acquire design version lock: %w", err)
	}
	return nil
}

func decodeIDArray(raw []byte) ([]int64, error) {
	ids := make([]int64, 0)
	if len(raw) == 0 {
		return ids, nil
	}
	// JSON IDs are decimal strings in the wire/DB contract, but tolerate plain
	// numbers written by older fixtures.
	var values []any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode ID array: %w", err)
	}
	for _, value := range values {
		switch typed := value.(type) {
		case string:
			parsed, err := strconv.ParseInt(typed, 10, 64)
			if err != nil || parsed <= 0 {
				return nil, fmt.Errorf("decode ID array: invalid decimal ID %q", typed)
			}
			ids = append(ids, parsed)
		case float64:
			id := int64(typed)
			if id <= 0 || float64(id) != typed {
				return nil, fmt.Errorf("decode ID array: invalid numeric ID %v", typed)
			}
			ids = append(ids, id)
		default:
			return nil, fmt.Errorf("decode ID array: unsupported value %v", value)
		}
	}
	return ids, nil
}

func encodeIDArray(ids []int64) ([]byte, error) {
	values := make([]string, 0, len(ids))
	for _, value := range ids {
		values = append(values, platformid.Format(value))
	}
	encoded, err := json.Marshal(values)
	if err != nil {
		return nil, fmt.Errorf("encode ID array: %w", err)
	}
	return encoded, nil
}

func encodeIDStrings(ids []string) ([]byte, error) {
	if ids == nil {
		ids = []string{}
	}
	encoded, err := json.Marshal(ids)
	if err != nil {
		return nil, fmt.Errorf("encode ID string array: %w", err)
	}
	return encoded, nil
}

func normalizeTime(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}
