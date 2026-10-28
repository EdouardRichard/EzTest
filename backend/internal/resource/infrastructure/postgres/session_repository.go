package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// Column lists shared by every query of a table and the scan order the
// matching scan function expects.
const (
	sessionColumnList = `id, account_id, system_id, environment_id, access_id, state, acquisition,
		storage_state, storage_state_hash, storage_state_kind, cookie_count, origin_count,
		expires_at, last_validated_at, last_used_at, failure_reason, invalidated_reason,
		created_by, created_at, updated_at`
	snapshotColumnList = `id, session_id, kind, items, summary, permission_hash, degraded, captured_at`
	leaseColumnList    = `id, session_id, holder, acquired_at, expires_at, released_at, released_reason`
	takeoverColumnList = `id, session_id, account_id, system_id, environment_id, reason, status,
		instructions, requested_by, requested_at, expires_at, completed_at`
)

// snapshotKindLogin is the only session_snapshot.kind the DDL check allows and
// the value the application layer leaves implicit.
const snapshotKindLogin = "login"

// expiredLeaseReleaseReason is the released_reason written when CreateLease
// reclaims an active lease whose expires_at has passed.
const expiredLeaseReleaseReason = "expired"

// SessionRepository implements the session persistence port: sessions, their
// immutable snapshots, application-level leases and manual takeovers.
// UpdateSession and UpdateTakeover are compare-and-swap writes on the state /
// status column; zero affected rows is a domain state conflict.
type SessionRepository struct {
	pool *pgxpool.Pool
}

// NewSessionRepository returns a session repository backed by pool.
func NewSessionRepository(pool *pgxpool.Pool) *SessionRepository {
	return &SessionRepository{pool: pool}
}

var _ domain.SessionRepository = (*SessionRepository)(nil)

// encodeNullableJSON renders an optional JSONB document for a parameter; an
// empty document becomes SQL NULL.
func encodeNullableJSON(value json.RawMessage) (any, error) {
	if len(value) == 0 {
		return nil, nil
	}
	return encodeJSON(value)
}

// encodeJSONOrEmptyObject renders a document for a NOT NULL JSONB column,
// defaulting to the DDL's empty object.
func encodeJSONOrEmptyObject(value json.RawMessage) (any, error) {
	if len(value) == 0 {
		value = json.RawMessage(`{}`)
	}
	return encodeJSON(value)
}

// scanSession scans one row in sessionColumnList order. StorageState is the
// nullable plaintext JSONB document; NULL stays a nil RawMessage.
func scanSession(row rowScanner) (domain.Session, error) {
	var (
		session      domain.Session
		storageState []byte
	)
	if err := row.Scan(
		&session.ID,
		&session.AccountID,
		&session.SystemID,
		&session.EnvironmentID,
		&session.AccessID,
		&session.State,
		&session.Acquisition,
		&storageState,
		&session.StorageStateHash,
		&session.StorageStateKind,
		&session.CookieCount,
		&session.OriginCount,
		&session.ExpiresAt,
		&session.LastValidatedAt,
		&session.LastUsedAt,
		&session.FailureReason,
		&session.InvalidatedReason,
		&session.CreatedBy,
		&session.CreatedAt,
		&session.UpdatedAt,
	); err != nil {
		return domain.Session{}, err
	}
	if len(storageState) > 0 {
		session.StorageState = json.RawMessage(storageState)
	}
	return session, nil
}

// scanSnapshot scans one row in snapshotColumnList order. Items and Summary are
// NOT NULL JSONB documents; an absent value falls back to the DDL default.
func scanSnapshot(row rowScanner) (domain.Snapshot, error) {
	var (
		snapshot domain.Snapshot
		items    []byte
		summary  []byte
	)
	if err := row.Scan(
		&snapshot.ID,
		&snapshot.SessionID,
		&snapshot.Kind,
		&items,
		&summary,
		&snapshot.PermissionHash,
		&snapshot.Degraded,
		&snapshot.CapturedAt,
	); err != nil {
		return domain.Snapshot{}, err
	}
	snapshot.Items = jsonObjectOrEmpty(items)
	snapshot.Summary = jsonObjectOrEmpty(summary)
	return snapshot, nil
}

// scanLease scans one row in leaseColumnList order.
func scanLease(row rowScanner) (domain.Lease, error) {
	var lease domain.Lease
	if err := row.Scan(
		&lease.ID,
		&lease.SessionID,
		&lease.Holder,
		&lease.AcquiredAt,
		&lease.ExpiresAt,
		&lease.ReleasedAt,
		&lease.ReleasedReason,
	); err != nil {
		return domain.Lease{}, err
	}
	return lease, nil
}

// scanTakeover scans one row in takeoverColumnList order. Instructions is a NOT
// NULL JSONB document; an absent value falls back to the DDL default.
func scanTakeover(row rowScanner) (domain.Takeover, error) {
	var (
		takeover     domain.Takeover
		instructions []byte
	)
	if err := row.Scan(
		&takeover.ID,
		&takeover.SessionID,
		&takeover.AccountID,
		&takeover.SystemID,
		&takeover.EnvironmentID,
		&takeover.Reason,
		&takeover.Status,
		&instructions,
		&takeover.RequestedBy,
		&takeover.RequestedAt,
		&takeover.ExpiresAt,
		&takeover.CompletedAt,
	); err != nil {
		return domain.Takeover{}, err
	}
	takeover.Instructions = jsonObjectOrEmpty(instructions)
	return takeover, nil
}

// jsonObjectOrEmpty returns document, or the empty JSON object when the
// database value is absent.
func jsonObjectOrEmpty(document []byte) json.RawMessage {
	if len(document) == 0 {
		return json.RawMessage(`{}`)
	}
	return json.RawMessage(document)
}

// CreateSession inserts a session. An empty Acquisition becomes imported,
// matching the DDL default. StorageState is plaintext JSONB by decision D28 and
// is stored as SQL NULL when absent; the full stored row (ID and server
// timestamps included) is written back to s.
func (r *SessionRepository) CreateSession(ctx context.Context, s *domain.Session) error {
	acquisition := s.Acquisition
	if acquisition == "" {
		acquisition = domain.AcquisitionImported
	}
	storageState, err := encodeNullableJSON(s.StorageState)
	if err != nil {
		return fmt.Errorf("postgres: encode session storage_state: %w", err)
	}
	const query = `INSERT INTO session
		(account_id, system_id, environment_id, access_id, state, acquisition, storage_state,
		 storage_state_hash, storage_state_kind, cookie_count, origin_count, expires_at,
		 last_validated_at, last_used_at, failure_reason, invalidated_reason, created_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING ` + sessionColumnList
	created, err := scanSession(r.pool.QueryRow(ctx, query,
		s.AccountID, s.SystemID, s.EnvironmentID, s.AccessID, s.State, acquisition,
		storageState, s.StorageStateHash, s.StorageStateKind, s.CookieCount, s.OriginCount,
		s.ExpiresAt, s.LastValidatedAt, s.LastUsedAt, s.FailureReason, s.InvalidatedReason,
		s.CreatedBy))
	if err != nil {
		return catalogWriteError(err, "duplicate key", "create session")
	}
	*s = created
	return nil
}

// GetSession returns one session by ID.
func (r *SessionRepository) GetSession(ctx context.Context, id int64) (*domain.Session, error) {
	session, err := scanSession(r.pool.QueryRow(ctx,
		`SELECT `+sessionColumnList+` FROM session WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "session", id, "get session")
	}
	return &session, nil
}

// ListSessions returns one page of sessions ordered by ID ascending. A zero ID
// filter and an empty State filter match every value; more reports whether a
// row exists after the returned page.
func (r *SessionRepository) ListSessions(ctx context.Context, f domain.SessionFilter) ([]domain.Session, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list sessions: limit must be positive")
	}
	const query = `SELECT ` + sessionColumnList + `
		FROM session
		WHERE id > $1
		  AND ($2 = 0 OR account_id = $2)
		  AND ($3 = 0 OR system_id = $3)
		  AND ($4 = 0 OR environment_id = $4)
		  AND ($5 = 0 OR access_id = $5)
		  AND ($6 = '' OR state = $6)
		ORDER BY id ASC
		LIMIT $7`
	rows, err := r.pool.Query(ctx, query,
		f.Page.AfterID, f.AccountID, f.SystemID, f.EnvironmentID, f.AccessID, f.State,
		f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]domain.Session, 0, f.Page.Limit+1)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan session: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list sessions: %w", err)
	}

	more := len(sessions) > f.Page.Limit
	if more {
		sessions = sessions[:f.Page.Limit]
	}
	return sessions, more, nil
}

// UpdateSession writes the mutable session columns with a state compare-and-swap
// (WHERE id AND state = expectedState) and refreshes updated_at. The identifying
// columns and acquisition are immutable after creation. Zero affected rows is a
// session_state_conflict, including when the session no longer exists. The full
// stored row (canonical JSONB and timestamp precision included) is written back
// to s.
func (r *SessionRepository) UpdateSession(ctx context.Context, s *domain.Session, expectedState string) error {
	storageState, err := encodeNullableJSON(s.StorageState)
	if err != nil {
		return fmt.Errorf("postgres: encode session storage_state: %w", err)
	}
	const query = `UPDATE session
		SET state = $3, storage_state = $4, storage_state_hash = $5, storage_state_kind = $6,
		    cookie_count = $7, origin_count = $8, expires_at = $9, last_validated_at = $10,
		    last_used_at = $11, failure_reason = $12, invalidated_reason = $13,
		    updated_at = now()
		WHERE id = $1 AND state = $2
		RETURNING ` + sessionColumnList
	updated, err := scanSession(r.pool.QueryRow(ctx, query,
		s.ID, expectedState, s.State, storageState, s.StorageStateHash, s.StorageStateKind,
		s.CookieCount, s.OriginCount, s.ExpiresAt, s.LastValidatedAt, s.LastUsedAt,
		s.FailureReason, s.InvalidatedReason,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewStateConflict(fmt.Sprintf("session %d is not in state %q", s.ID, expectedState))
		}
		return catalogWriteError(err, "duplicate key", "update session")
	}
	*s = updated
	return nil
}

// TouchLastUsed records a session's lease acquisition time with a single-column
// write. Unlike UpdateSession it never rewrites storage state or the other
// mutable columns, so a stale copy cannot clobber a concurrent login check. A
// missing row is a no-op: the touch only affects least-recently-used ordering.
func (r *SessionRepository) TouchLastUsed(ctx context.Context, id int64, at time.Time) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE session SET last_used_at = $2, updated_at = now() WHERE id = $1`,
		id, at); err != nil {
		return fmt.Errorf("postgres: touch session last_used_at: %w", err)
	}
	return nil
}

// CreateSnapshot appends an immutable session snapshot. An empty Kind becomes
// login and empty Items/Summary become the DDL default empty objects; a zero
// CapturedAt becomes the server clock, while an explicit value is preserved.
// The full stored row is written back to s.
func (r *SessionRepository) CreateSnapshot(ctx context.Context, s *domain.Snapshot) error {
	kind := s.Kind
	if kind == "" {
		kind = snapshotKindLogin
	}
	items, err := encodeJSONOrEmptyObject(s.Items)
	if err != nil {
		return fmt.Errorf("postgres: encode snapshot items: %w", err)
	}
	summary, err := encodeJSONOrEmptyObject(s.Summary)
	if err != nil {
		return fmt.Errorf("postgres: encode snapshot summary: %w", err)
	}
	var capturedAt any
	if !s.CapturedAt.IsZero() {
		capturedAt = s.CapturedAt
	}
	const query = `INSERT INTO session_snapshot
		(session_id, kind, items, summary, permission_hash, degraded, captured_at)
		VALUES ($1, $2, $3, $4, $5, $6, COALESCE($7::timestamptz, now()))
		RETURNING ` + snapshotColumnList
	created, err := scanSnapshot(r.pool.QueryRow(ctx, query,
		s.SessionID, kind, items, summary, s.PermissionHash, s.Degraded, capturedAt))
	if err != nil {
		return catalogWriteError(err, "duplicate key", "create snapshot")
	}
	*s = created
	return nil
}

// GetLatestSnapshot returns the newest snapshot of a session by captured_at (id
// breaks ties). A session without snapshots yields (nil, nil): an absent
// snapshot is a normal state, not a not-found error.
func (r *SessionRepository) GetLatestSnapshot(ctx context.Context, sessionID int64) (*domain.Snapshot, error) {
	snapshot, err := scanSnapshot(r.pool.QueryRow(ctx,
		`SELECT `+snapshotColumnList+`
		   FROM session_snapshot
		  WHERE session_id = $1
		  ORDER BY captured_at DESC, id DESC
		  LIMIT 1`, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get latest snapshot: %w", err)
	}
	return &snapshot, nil
}

// ListSnapshots returns one page of a session's snapshots ordered by ID
// ascending. more reports whether a row exists after the returned page.
func (r *SessionRepository) ListSnapshots(ctx context.Context, sessionID int64, p domain.Page) ([]domain.Snapshot, bool, error) {
	if p.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list snapshots: limit must be positive")
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+snapshotColumnList+`
		   FROM session_snapshot
		  WHERE session_id = $1 AND id > $2
		  ORDER BY id ASC
		  LIMIT $3`,
		sessionID, p.AfterID, p.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list snapshots: %w", err)
	}
	defer rows.Close()

	snapshots := make([]domain.Snapshot, 0, p.Limit+1)
	for rows.Next() {
		snapshot, err := scanSnapshot(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan snapshot: %w", err)
		}
		snapshots = append(snapshots, snapshot)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list snapshots: %w", err)
	}

	more := len(snapshots) > p.Limit
	if more {
		snapshots = snapshots[:p.Limit]
	}
	return snapshots, more, nil
}

// CreateLease inserts an exclusive lease. Active leases must be unique per
// session (uq_session_active_lease); a violation is a lease_conflict. An active
// lease whose expires_at has passed is reclaimed in the same transaction, so an
// expired lease never blocks a new acquisition while no background sweeper
// exists. The full stored row is written back to l.
func (r *SessionRepository) CreateLease(ctx context.Context, l *domain.Lease) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("postgres: create lease: %w", err)
	}
	defer func() {
		// The commit below makes this a no-op; it releases the connection when
		// the insert fails.
		_ = tx.Rollback(ctx)
	}()

	if _, err := tx.Exec(ctx,
		`UPDATE session_lease
		    SET released_at = now(), released_reason = $2
		  WHERE session_id = $1 AND released_at IS NULL AND expires_at <= now()`,
		l.SessionID, expiredLeaseReleaseReason); err != nil {
		return fmt.Errorf("postgres: reclaim expired leases: %w", err)
	}

	const query = `INSERT INTO session_lease (session_id, holder, expires_at)
		VALUES ($1, $2, $3)
		RETURNING ` + leaseColumnList
	created, err := scanLease(tx.QueryRow(ctx, query, l.SessionID, l.Holder, l.ExpiresAt))
	if err != nil {
		mapped := mapLeaseConflict(err)
		var domainErr *domain.Error
		if errors.As(mapped, &domainErr) {
			return domainErr
		}
		return catalogWriteError(err, "duplicate key", "create lease")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("postgres: create lease: %w", err)
	}
	*l = created
	return nil
}

// GetActiveLease returns the unexpired, unreleased lease of a session, or
// (nil, nil) when the session has none. Expiry is evaluated with the database
// clock; no background reclamation runs.
func (r *SessionRepository) GetActiveLease(ctx context.Context, sessionID int64) (*domain.Lease, error) {
	lease, err := scanLease(r.pool.QueryRow(ctx,
		`SELECT `+leaseColumnList+`
		   FROM session_lease
		  WHERE session_id = $1 AND released_at IS NULL AND expires_at > now()
		  ORDER BY id DESC
		  LIMIT 1`, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get active lease: %w", err)
	}
	return &lease, nil
}

// ReleaseLease releases the session's active lease when it is held by holder.
// Releasing when no matching active lease exists is a no-op, so a stale holder
// cannot fail the release path.
func (r *SessionRepository) ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE session_lease
		    SET released_at = now(), released_reason = $3
		  WHERE session_id = $1 AND holder = $2 AND released_at IS NULL`,
		sessionID, holder, reason); err != nil {
		return fmt.Errorf("postgres: release lease: %w", err)
	}
	return nil
}

// ReleaseAllLeases releases every active lease of a session. Used when a
// session is invalidated or revoked; it is idempotent.
func (r *SessionRepository) ReleaseAllLeases(ctx context.Context, sessionID int64, reason string) error {
	if _, err := r.pool.Exec(ctx,
		`UPDATE session_lease
		    SET released_at = now(), released_reason = $2
		  WHERE session_id = $1 AND released_at IS NULL`,
		sessionID, reason); err != nil {
		return fmt.Errorf("postgres: release all leases: %w", err)
	}
	return nil
}

// CreateTakeover inserts a manual takeover. An empty Status becomes waiting and
// empty Instructions becomes the DDL default empty object; a zero RequestedAt
// becomes the server clock. The full stored row is written back to t.
func (r *SessionRepository) CreateTakeover(ctx context.Context, t *domain.Takeover) error {
	status := t.Status
	if status == "" {
		status = domain.TakeoverStatusWaiting
	}
	instructions, err := encodeJSONOrEmptyObject(t.Instructions)
	if err != nil {
		return fmt.Errorf("postgres: encode takeover instructions: %w", err)
	}
	var requestedAt any
	if !t.RequestedAt.IsZero() {
		requestedAt = t.RequestedAt
	}
	const query = `INSERT INTO manual_takeover
		(session_id, account_id, system_id, environment_id, reason, status, instructions,
		 requested_by, requested_at, expires_at, completed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, COALESCE($9::timestamptz, now()), $10, $11)
		RETURNING ` + takeoverColumnList
	created, err := scanTakeover(r.pool.QueryRow(ctx, query,
		t.SessionID, t.AccountID, t.SystemID, t.EnvironmentID, t.Reason, status,
		instructions, t.RequestedBy, requestedAt, t.ExpiresAt, t.CompletedAt))
	if err != nil {
		return catalogWriteError(err, "duplicate key", "create takeover")
	}
	*t = created
	return nil
}

// GetTakeover returns one takeover by ID.
func (r *SessionRepository) GetTakeover(ctx context.Context, id int64) (*domain.Takeover, error) {
	takeover, err := scanTakeover(r.pool.QueryRow(ctx,
		`SELECT `+takeoverColumnList+` FROM manual_takeover WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "takeover", id, "get takeover")
	}
	return &takeover, nil
}

// GetWaitingTakeover returns the newest waiting takeover of a session, or
// (nil, nil) when none is waiting. Expiry is deliberately not filtered here:
// the application service lazily marks an expired waiting takeover before
// deciding whether a new request may be created.
func (r *SessionRepository) GetWaitingTakeover(ctx context.Context, sessionID int64) (*domain.Takeover, error) {
	takeover, err := scanTakeover(r.pool.QueryRow(ctx,
		`SELECT `+takeoverColumnList+`
		   FROM manual_takeover
		  WHERE session_id = $1 AND status = $2
		  ORDER BY id DESC
		  LIMIT 1`, sessionID, domain.TakeoverStatusWaiting))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get waiting takeover: %w", err)
	}
	return &takeover, nil
}

// GetLatestTakeover returns the most recently requested takeover of a session
// regardless of its status, or (nil, nil) when the session has none.
func (r *SessionRepository) GetLatestTakeover(ctx context.Context, sessionID int64) (*domain.Takeover, error) {
	takeover, err := scanTakeover(r.pool.QueryRow(ctx,
		`SELECT `+takeoverColumnList+`
		   FROM manual_takeover
		  WHERE session_id = $1
		  ORDER BY requested_at DESC, id DESC
		  LIMIT 1`, sessionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("postgres: get latest takeover: %w", err)
	}
	return &takeover, nil
}

// ListTakeovers returns one page of takeovers ordered by ID ascending. An empty
// Status filter matches every value; more reports whether a row exists after
// the returned page.
func (r *SessionRepository) ListTakeovers(ctx context.Context, f domain.TakeoverFilter) ([]domain.Takeover, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list takeovers: limit must be positive")
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+takeoverColumnList+`
		   FROM manual_takeover
		  WHERE id > $1
		    AND ($2 = '' OR status = $2)
		  ORDER BY id ASC
		  LIMIT $3`,
		f.Page.AfterID, f.Status, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list takeovers: %w", err)
	}
	defer rows.Close()

	takeovers := make([]domain.Takeover, 0, f.Page.Limit+1)
	for rows.Next() {
		takeover, err := scanTakeover(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan takeover: %w", err)
		}
		takeovers = append(takeovers, takeover)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list takeovers: %w", err)
	}

	more := len(takeovers) > f.Page.Limit
	if more {
		takeovers = takeovers[:f.Page.Limit]
	}
	return takeovers, more, nil
}

// UpdateTakeover writes the mutable takeover columns with a status
// compare-and-swap (WHERE id AND status = expectedStatus). A takeover completed
// by this call always carries a completed_at: an unset value is filled with the
// server clock, while cancelled/expired rows keep the caller's (normally nil)
// value. Zero affected rows is a session_state_conflict, including when the
// takeover no longer exists. The full stored row is written back to t.
func (r *SessionRepository) UpdateTakeover(ctx context.Context, t *domain.Takeover, expectedStatus string) error {
	instructions, err := encodeJSONOrEmptyObject(t.Instructions)
	if err != nil {
		return fmt.Errorf("postgres: encode takeover instructions: %w", err)
	}
	const query = `UPDATE manual_takeover
		SET status = $3,
		    instructions = $4,
		    expires_at = $5,
		    completed_at = CASE WHEN $3 = $6 THEN COALESCE($7::timestamptz, now())
		                        ELSE $7::timestamptz END
		WHERE id = $1 AND status = $2
		RETURNING ` + takeoverColumnList
	updated, err := scanTakeover(r.pool.QueryRow(ctx, query,
		t.ID, expectedStatus, t.Status, instructions, t.ExpiresAt,
		domain.TakeoverStatusCompleted, t.CompletedAt,
	))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.NewStateConflict(fmt.Sprintf("takeover %d is not in status %q", t.ID, expectedStatus))
		}
		return catalogWriteError(err, "duplicate key", "update takeover")
	}
	*t = updated
	return nil
}
