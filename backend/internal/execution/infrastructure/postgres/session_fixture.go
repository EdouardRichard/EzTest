package executionpg

import (
	"context"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/jackc/pgx/v5"
)

// SessionFixtureRepository persists the append-only session_fixture fact
// (v0.2.4-F P18, SC-F5). It exposes only Append/List — never Update/Delete
// (append-only fact, constitution art.5.7; the sut_resolution precedent).
// Saving the same reuse key again appends a second row (latest wins); the
// (version,sut,environment,role) key is a lookup index, not a unique constraint.
type SessionFixtureRepository struct{ base }

var _ execution.SessionFixtureRepository = (*SessionFixtureRepository)(nil)

const sessionFixtureColumns = `id, version_id, sut_id, environment_id, role,
	storage_state_ref, created_at, created_by`

// AppendSessionFixture inserts one fixture fact. version/sut/environment are
// logical cross-context BIGINT references (no physical FK); the application
// validates the SUT against the version's effective catalog before appending
// (server-truth). storage_state_ref is the content-ledger sha256 of the saved
// state; the bytes never live in this table.
func (r *SessionFixtureRepository) AppendSessionFixture(ctx context.Context, fixture execution.SessionFixture) (execution.SessionFixture, error) {
	ctx = contextOrBackground(ctx)
	if err := fixture.Validate(); err != nil {
		return execution.SessionFixture{}, err
	}
	fixture.ID = r.insertID(fixture.ID)
	row := r.pool.QueryRow(ctx, `
		INSERT INTO session_fixture (id, version_id, sut_id, environment_id, role, storage_state_ref, created_at, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7, now()), $8)
		RETURNING created_at
	`, fixture.ID, fixture.VersionID, fixture.SUTID, fixture.EnvironmentID, fixture.Role,
		fixture.StorageStateRef, nullableTime(fixture.CreatedAt), fixture.CreatedBy)
	if err := row.Scan(&fixture.CreatedAt); err != nil {
		return execution.SessionFixture{}, wrapDB("append session fixture", err)
	}
	return fixture, nil
}

// ListSessionFixtures returns one reuse key's (version,sut,environment,role)
// facts in append order (id ascending); the caller takes the latest as the
// effective fixture. It is the production read path of the fact (the fixture
// download tool's current-fixture derivation).
func (r *SessionFixtureRepository) ListSessionFixtures(ctx context.Context, versionID, sutID, environmentID int64, role string) ([]execution.SessionFixture, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+sessionFixtureColumns+`
		FROM session_fixture
		WHERE version_id = $1 AND sut_id = $2 AND environment_id = $3 AND role = $4
		ORDER BY id ASC
	`, versionID, sutID, environmentID, role)
	if err != nil {
		return nil, wrapDB("list session fixtures", err)
	}
	defer rows.Close()
	return collectSessionFixtures(rows)
}

// ListSessionFixturesByVersion returns one version's fixture facts newest first
// (the admin visibility listing, SC-F5), paginated by beforeID/limit. Fixture
// facts are append-only and unbounded, so the listing uses the limit+1 probe
// (fetchLimit) and reports hasMore honestly — never a silent truncation
// (constitution art.7.6). It is a read-only projection of the same append-only
// facts — metadata only, never the state bytes.
func (r *SessionFixtureRepository) ListSessionFixturesByVersion(ctx context.Context, versionID int64, limit int, beforeID *int64) ([]execution.SessionFixture, bool, error) {
	ctx = contextOrBackground(ctx)
	rows, err := r.pool.Query(ctx, `
		SELECT `+sessionFixtureColumns+`
		FROM session_fixture
		WHERE version_id = $1 AND ($2::BIGINT IS NULL OR id < $2)
		ORDER BY id DESC
		LIMIT $3
	`, versionID, beforeID, fetchLimit(limit))
	if err != nil {
		return nil, false, wrapDB("list session fixtures by version", err)
	}
	defer rows.Close()
	items, err := collectSessionFixtures(rows)
	if err != nil {
		return nil, false, err
	}
	return pageOfSessionFixtures(items, limit)
}

// pageOfSessionFixtures trims the limit+1 probe page to limit and reports
// whether more rows remain (the pageOfIntents precedent).
func pageOfSessionFixtures(items []execution.SessionFixture, limit int) ([]execution.SessionFixture, bool, error) {
	limit = effectiveLimit(limit)
	hasMore := len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func collectSessionFixtures(rows pgx.Rows) ([]execution.SessionFixture, error) {
	items := make([]execution.SessionFixture, 0)
	for rows.Next() {
		var item execution.SessionFixture
		if err := rows.Scan(&item.ID, &item.VersionID, &item.SUTID, &item.EnvironmentID, &item.Role,
			&item.StorageStateRef, &item.CreatedAt, &item.CreatedBy); err != nil {
			return nil, wrapDB("scan session fixture", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list session fixtures", err)
	}
	return items, nil
}
