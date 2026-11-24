// Package designpg is the PostgreSQL adapter for the P2 design context. It
// reads version/requirement/conversation rows (cross-context logical
// references) and owns every write to the design tables.
package designpg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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

// VersionContext is the version-existence gate for design proposals.
//
// v0.2.4-B: this function used to build four resource maps (allowed version_sut
// ids and page/api/db -> version_sut lookups) for the proposal ownership group.
// Both the maps (design.VersionContext) and their only consumer (§5.2 "删 SUT
// 归属组") were deleted, so the four queries that filled them went with them --
// they read version_sut / version_sut.scope_exclude / sut_page / sut_api /
// target_database.environment_id, all dropped by resource_0004 + version_0015/0016.
// T27 reduced the projection to the version id: GraphService.Propose discards the
// value and only checks the error, so selecting status / auto_design_new_requirements
// projected fields no caller consumes. A missing version still reports
// design.ErrNotFound so a proposal into it 404s on "version" rather than falling
// through to the requirement read.
func (r *Repository) VersionContext(ctx context.Context, versionID int64) (design.VersionContext, error) {
	var view design.VersionContext
	if err := r.pool.QueryRow(ctx, `
		SELECT id
		FROM version
		WHERE id = $1
	`, versionID).Scan(&view.VersionID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return design.VersionContext{}, design.ErrNotFound
		}
		return design.VersionContext{}, fmt.Errorf("read version context: %w", err)
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
	var criteria []byte
	err := q.QueryRow(ctx, `
		SELECT r.id, r.version_id, r.current_version_id, rv.revision, rv.title,
		       r.status, COALESCE(rv.priority, $3), COALESCE(rv.sut_description, ''),
		       COALESCE(rv.acceptance_criteria, '[]'::jsonb)
		FROM requirement r
		LEFT JOIN requirement_version rv ON rv.id = r.current_version_id
		WHERE r.id = $1 AND r.version_id = $2
	`, requirementID, versionID, version.DefaultPriority).Scan(
		&ref.ID, &ref.VersionID, &currentVersionID, &revision, &title,
		&ref.Status, &ref.Priority, &ref.SutDescription, &criteria,
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
	if ref.ACIDs, err = decodeACIDs(criteria); err != nil {
		return design.RequirementRef{}, err
	}
	return ref, nil
}

// decodeACIDs reads the requirement's acceptance_criteria JSONB document -- the
// [{id, given, when, then}] array the version context writes (design v1.3.1
// §5.1) -- and returns just the ids, in id order.
//
// Design only needs the id set: it is what a case's covered_acs must be a subset
// of (§5.2). The given/when/then text stays in the version context, which owns
// it, so this reader does not have to mirror that shape.
//
// The order is by the numeric suffix of "AC-<n>", not lexicographic, so AC-2
// sorts before AC-10. The sequence parser is version.ACIDSequence -- the same
// canonical authority the version write path uses (constitution 五.1) -- so this
// reader cannot drift from it on what counts as a valid id (it rejects a
// non-canonical "AC-007" exactly as the writer does). A blank id is skipped (it
// is not an id at all); a non-blank id that does not parse is still returned,
// sorted last -- dropping it would silently shrink the set a proposal's
// covered_acs is validated against, which fails closed in the wrong direction.
func decodeACIDs(raw []byte) ([]string, error) {
	ids := make([]string, 0)
	if len(raw) == 0 {
		return ids, nil
	}
	var criteria []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &criteria); err != nil {
		return nil, fmt.Errorf("decode acceptance criteria: %w", err)
	}
	for _, criterion := range criteria {
		id := strings.TrimSpace(criterion.ID)
		if id == "" {
			continue
		}
		ids = append(ids, id)
	}
	sort.SliceStable(ids, func(i, j int) bool {
		left, leftOK := version.ACIDSequence(ids[i])
		right, rightOK := version.ACIDSequence(ids[j])
		if leftOK != rightOK {
			return leftOK
		}
		if leftOK {
			return left < right
		}
		return ids[i] < ids[j]
	})
	return ids, nil
}

func (r *Repository) CountPendingDesign(ctx context.Context, versionID int64) (int, error) {
	var count int
	err := r.pool.QueryRow(ctx, `
		SELECT count(*)
		FROM requirement r
		WHERE r.version_id = $1
		  AND r.status = $2
		  AND NOT EXISTS (
		      SELECT 1 FROM design_scope ds
		      WHERE ds.version_id = $1 AND ds.requirement_id = r.id AND ds.status = $3
		  )
	`, versionID, string(version.RequirementClarified), design.ScopeStatusActive).Scan(&count)
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

// decodeStringArray reads a JSONB array of strings, the shape case_version
// .covered_acs uses (v0.2.4-B §5.2). A NULL/empty column decodes to an empty
// non-nil slice, never nil, so callers can range over it and a len() check
// without a nil guard.
func decodeStringArray(raw []byte) ([]string, error) {
	values := make([]string, 0)
	if len(raw) == 0 {
		return values, nil
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, fmt.Errorf("decode string array: %w", err)
	}
	if values == nil {
		values = make([]string, 0)
	}
	return values, nil
}

func normalizeTime(value *time.Time, fallback time.Time) time.Time {
	if value != nil {
		return *value
	}
	return fallback
}
