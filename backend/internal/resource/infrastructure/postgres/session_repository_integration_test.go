//go:build integration

package postgres_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// sessionFixture is one account脳system脳environment脳access diamond seeded with
// raw SQL, so the session repository tests do not depend on the catalog or
// access repositories.
type sessionFixture struct {
	CompanyID     int64
	SystemID      int64
	EnvironmentID int64
	AccountID     int64
	AccessID      int64
}

// sessionSeedSystem inserts one system of companyID directly.
func sessionSeedSystem(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID int64, key string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO system (company_id, key, name, kind, base_url)
		 VALUES ($1, $2, $2, 'web', 'http://localhost:13335')
		 RETURNING id`,
		companyID, key).Scan(&id); err != nil {
		t.Fatalf("seed system %q: %v", key, err)
	}
	return id
}

// sessionSeedEnvironment inserts one environment of systemID directly.
func sessionSeedEnvironment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, systemID int64, key string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO environment (system_id, key, name, base_url, kind)
		 VALUES ($1, $2, $2, 'http://localhost:13335', 'dev')
		 RETURNING id`,
		systemID, key).Scan(&id); err != nil {
		t.Fatalf("seed environment %q: %v", key, err)
	}
	return id
}

// sessionSeedAccount inserts one account of companyID directly.
func sessionSeedAccount(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID int64, key string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO account (company_id, key, label, kind)
		 VALUES ($1, $2, $2, 'human')
		 RETURNING id`,
		companyID, key).Scan(&id); err != nil {
		t.Fatalf("seed account %q: %v", key, err)
	}
	return id
}

// sessionSeedAccess inserts one password access for accountID on systemID.
func sessionSeedAccess(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, systemID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO account_system_access (account_id, system_id, auth_type)
		 VALUES ($1, $2, 'password')
		 RETURNING id`,
		accountID, systemID).Scan(&id); err != nil {
		t.Fatalf("seed access (account %d, system %d): %v", accountID, systemID, err)
	}
	return id
}

// seedSessionFixture seeds the four parents a session needs and returns their
// IDs.
func seedSessionFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) sessionFixture {
	t.Helper()
	company := seedCatalogCompany(t, ctx, pool)
	systemID := sessionSeedSystem(t, ctx, pool, company.ID, "shop")
	environmentID := sessionSeedEnvironment(t, ctx, pool, systemID, "dev")
	accountID := sessionSeedAccount(t, ctx, pool, company.ID, "alice")
	accessID := sessionSeedAccess(t, ctx, pool, accountID, systemID)
	return sessionFixture{
		CompanyID:     company.ID,
		SystemID:      systemID,
		EnvironmentID: environmentID,
		AccountID:     accountID,
		AccessID:      accessID,
	}
}

// session builds a session for the fixture in state.
func (f sessionFixture) session(state string) *domain.Session {
	return &domain.Session{
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		AccessID:      f.AccessID,
		State:         state,
		Acquisition:   domain.AcquisitionImported,
	}
}

// sessionInt64 returns a pointer to value, for nullable BIGINT columns.
func sessionInt64(value int64) *int64 { return &value }

// sessionTime returns a pointer to value, for nullable TIMESTAMPTZ columns.
func sessionTime(value time.Time) *time.Time { return &value }

// assertSessionJSON compares two nullable JSONB documents semantically. Both
// nil means SQL NULL; one nil is a mismatch.
func assertSessionJSON(t *testing.T, got, want json.RawMessage) {
	t.Helper()
	if len(got) == 0 || len(want) == 0 {
		if len(got) != len(want) {
			t.Fatalf("json=%q want %q", got, want)
		}
		return
	}
	assertCatalogJSON(t, got, want)
}

// assertSessionMatches compares sessions, comparing StorageState semantically
// and ignoring the server-managed timestamps.
func assertSessionMatches(t *testing.T, got, want domain.Session) {
	t.Helper()
	assertSessionJSON(t, got.StorageState, want.StorageState)
	got.StorageState, want.StorageState = nil, nil
	got.CreatedAt, got.UpdatedAt = time.Time{}, time.Time{}
	want.CreatedAt, want.UpdatedAt = time.Time{}, time.Time{}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("session=%+v\nwant %+v", got, want)
	}
}

// assertTakeoverMatches compares takeovers, comparing Instructions semantically.
func assertTakeoverMatches(t *testing.T, got, want domain.Takeover) {
	t.Helper()
	assertSessionJSON(t, got.Instructions, want.Instructions)
	got.Instructions, want.Instructions = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("takeover=%+v\nwant %+v", got, want)
	}
}

// assertSnapshotMatches compares snapshots, comparing Items and Summary
// semantically.
func assertSnapshotMatches(t *testing.T, got, want domain.Snapshot) {
	t.Helper()
	assertSessionJSON(t, got.Items, want.Items)
	assertSessionJSON(t, got.Summary, want.Summary)
	got.Items, want.Items = nil, nil
	got.Summary, want.Summary = nil, nil
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot=%+v\nwant %+v", got, want)
	}
}

func TestSessionRepositorySessionCreateAndGet(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	// A minimal session: the DDL defaults acquisition to imported and every
	// nullable column stays NULL.
	minimal := f.session(domain.StatePending)
	if err := repo.CreateSession(ctx, minimal); err != nil {
		t.Fatal(err)
	}
	if minimal.ID == 0 {
		t.Fatalf("created session id=%d", minimal.ID)
	}
	if minimal.Acquisition != domain.AcquisitionImported {
		t.Fatalf("acquisition=%q want %q", minimal.Acquisition, domain.AcquisitionImported)
	}
	if minimal.StorageState != nil || minimal.ExpiresAt != nil ||
		minimal.LastValidatedAt != nil || minimal.LastUsedAt != nil || minimal.CreatedBy != nil {
		t.Fatalf("nullable columns of minimal session=%+v", minimal)
	}
	if minimal.CreatedAt.IsZero() || minimal.UpdatedAt.IsZero() {
		t.Fatalf("minimal timestamps=%v/%v", minimal.CreatedAt, minimal.UpdatedAt)
	}
	loadedMinimal, err := repo.GetSession(ctx, minimal.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMatches(t, *loadedMinimal, *minimal)
	if !loadedMinimal.CreatedAt.Equal(minimal.CreatedAt) || !loadedMinimal.UpdatedAt.Equal(minimal.UpdatedAt) {
		t.Fatalf("minimal timestamps=%v/%v want %v/%v",
			loadedMinimal.CreatedAt, loadedMinimal.UpdatedAt, minimal.CreatedAt, minimal.UpdatedAt)
	}

	// A fully populated session round-trips every column, including JSONB
	// storage_state, nullable timestamps and created_by.
	now := time.Now().UTC()
	full := &domain.Session{
		AccountID:         f.AccountID,
		SystemID:          f.SystemID,
		EnvironmentID:     f.EnvironmentID,
		AccessID:          f.AccessID,
		State:             domain.StateValid,
		Acquisition:       domain.AcquisitionManual,
		StorageState:      json.RawMessage(`{"kind":"token","headers":{"Authorization":"Bearer fixture-token"}}`),
		StorageStateHash:  "sha256:full",
		StorageStateKind:  domain.StorageStateKindToken,
		CookieCount:       3,
		OriginCount:       2,
		ExpiresAt:         sessionTime(now.Add(2 * time.Hour)),
		LastValidatedAt:   sessionTime(now),
		LastUsedAt:        sessionTime(now.Add(-time.Minute)),
		FailureReason:     "login_failed",
		InvalidatedReason: "",
		CreatedBy:         sessionInt64(4242),
	}
	if err := repo.CreateSession(ctx, full); err != nil {
		t.Fatal(err)
	}
	if full.ID == 0 || full.CreatedAt.IsZero() || full.UpdatedAt.IsZero() {
		t.Fatalf("created full session=%+v", full)
	}
	loadedFull, err := repo.GetSession(ctx, full.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMatches(t, *loadedFull, *full)
	if !loadedFull.CreatedAt.Equal(full.CreatedAt) || !loadedFull.UpdatedAt.Equal(full.UpdatedAt) {
		t.Fatalf("full timestamps=%v/%v want %v/%v",
			loadedFull.CreatedAt, loadedFull.UpdatedAt, full.CreatedAt, full.UpdatedAt)
	}
	if loadedFull.ExpiresAt == nil || !loadedFull.ExpiresAt.Equal(*full.ExpiresAt) {
		t.Fatalf("expires_at=%v want %v", loadedFull.ExpiresAt, full.ExpiresAt)
	}
	if loadedFull.CreatedBy == nil || *loadedFull.CreatedBy != 4242 {
		t.Fatalf("created_by=%v want 4242", loadedFull.CreatedBy)
	}

	// An invalid state is a CHECK violation surfaced as a wrapped write error,
	// not a successful insert.
	invalid := f.session("not-a-state")
	if err := repo.CreateSession(ctx, invalid); err == nil {
		t.Fatal("CreateSession accepted an invalid state")
	}
}

func TestSessionRepositoryUpdateSessionCAS(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	sess := f.session(domain.StatePending)
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	// pending -> valid: the expected state matches, so the write applies and
	// the server-managed timestamps are written back to the entity.
	now := time.Now().UTC()
	sess.State = domain.StateValid
	sess.StorageState = json.RawMessage(`{"cookies":[{"name":"sid","value":"rotated"}],"origins":[]}`)
	sess.StorageStateHash = "sha256:rotated"
	sess.StorageStateKind = domain.StorageStateKindStorageState
	sess.CookieCount = 1
	sess.OriginCount = 0
	sess.ExpiresAt = sessionTime(now.Add(time.Hour))
	sess.LastValidatedAt = sessionTime(now)
	sess.FailureReason = ""
	sess.InvalidatedReason = ""
	if err := repo.UpdateSession(ctx, sess, domain.StatePending); err != nil {
		t.Fatal(err)
	}
	if sess.UpdatedAt.IsZero() || sess.UpdatedAt.Before(sess.CreatedAt) {
		t.Fatalf("updated_at=%v created_at=%v", sess.UpdatedAt, sess.CreatedAt)
	}
	reloaded, err := repo.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSessionMatches(t, *reloaded, *sess)
	if !reloaded.CreatedAt.Equal(sess.CreatedAt) || !reloaded.UpdatedAt.Equal(sess.UpdatedAt) {
		t.Fatalf("updated timestamps=%v/%v want %v/%v",
			reloaded.CreatedAt, reloaded.UpdatedAt, sess.CreatedAt, sess.UpdatedAt)
	}

	// A stale expected state does not write anything: 0 rows is reported as a
	// state conflict, not as success or not-found.
	stale := *reloaded
	stale.State = domain.StateExpired
	stale.InvalidatedReason = "must not persist"
	assertCatalogKind(t, repo.UpdateSession(ctx, &stale, domain.StatePending),
		domain.KindStateConflict, "session_state_conflict")
	unchanged, err := repo.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.State != domain.StateValid || unchanged.InvalidatedReason != "" {
		t.Fatalf("row changed by a failed CAS: %+v", unchanged)
	}

	// The stored state is the CAS token: the valid row now transitions to
	// invalid.
	next := *unchanged
	next.State = domain.StateInvalid
	next.InvalidatedReason = "admin invalidated"
	if err := repo.UpdateSession(ctx, &next, domain.StateValid); err != nil {
		t.Fatal(err)
	}
	reloadedNext, err := repo.GetSession(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedNext.State != domain.StateInvalid || reloadedNext.InvalidatedReason != "admin invalidated" {
		t.Fatalf("invalidated session=%+v", reloadedNext)
	}

	// A missing session is also a failed CAS (0 rows) rather than not-found.
	missing := f.session(domain.StatePending)
	missing.ID = 999999
	assertCatalogKind(t, repo.UpdateSession(ctx, missing, domain.StatePending),
		domain.KindStateConflict, "session_state_conflict")
}

func TestSessionRepositorySnapshots(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	sess := f.session(domain.StateValid)
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}

	// No snapshot yet: the optional getter reports absence without an error and
	// the list is a non-nil empty page.
	missing, err := repo.GetLatestSnapshot(ctx, sess.ID)
	if err != nil || missing != nil {
		t.Fatalf("latest snapshot of a fresh session=%+v err=%v", missing, err)
	}
	empty, more, err := repo.ListSnapshots(ctx, sess.ID, domain.Page{Limit: 10})
	if err != nil || empty == nil || len(empty) != 0 || more {
		t.Fatalf("snapshots of a fresh session=%v more=%v err=%v", empty, more, err)
	}

	first := &domain.Snapshot{
		SessionID:      sess.ID,
		Kind:           "login",
		Items:          json.RawMessage(`{"login":{"kind":"page","observed":"denied","status":403}}`),
		Summary:        json.RawMessage(`{"total":1,"allowed":0,"denied":1,"error":0,"degraded":false}`),
		PermissionHash: "sha256:first",
		Degraded:       false,
	}
	if err := repo.CreateSnapshot(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.ID == 0 || first.CapturedAt.IsZero() {
		t.Fatalf("created snapshot=%+v", first)
	}
	loadedFirst, err := repo.GetLatestSnapshot(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertSnapshotMatches(t, *loadedFirst, *first)

	// An explicit captured_at is honoured, and "latest" is decided by
	// captured_at rather than by id.
	future := time.Now().UTC().Add(time.Hour)
	second := &domain.Snapshot{
		SessionID:      sess.ID,
		Kind:           "login",
		Items:          json.RawMessage(`{}`),
		Summary:        json.RawMessage(`{"degraded":true,"reason":"manual_confirmation"}`),
		PermissionHash: "sha256:second",
		Degraded:       true,
		CapturedAt:     future,
	}
	if err := repo.CreateSnapshot(ctx, second); err != nil {
		t.Fatal(err)
	}
	if second.CapturedAt.IsZero() || !second.CapturedAt.Equal(future.Truncate(time.Microsecond)) {
		t.Fatalf("created captured_at=%v want %v", second.CapturedAt, future)
	}

	// Empty kind, items and summary fall back to the DDL defaults. The server
	// clock captured_at of this row is later than the first row but earlier
	// than the explicitly future-dated second row.
	third := &domain.Snapshot{SessionID: sess.ID, PermissionHash: "sha256:third"}
	if err := repo.CreateSnapshot(ctx, third); err != nil {
		t.Fatal(err)
	}
	if third.Kind != "login" {
		t.Fatalf("kind=%q want login", third.Kind)
	}
	assertCatalogJSON(t, third.Items, json.RawMessage(`{}`))
	assertCatalogJSON(t, third.Summary, json.RawMessage(`{}`))
	if third.CapturedAt.IsZero() {
		t.Fatal("server-captured snapshot has a zero captured_at")
	}

	latest, err := repo.GetLatestSnapshot(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest.ID != second.ID {
		t.Fatalf("latest snapshot id=%d want %d (captured_at ordering)", latest.ID, second.ID)
	}
	assertSnapshotMatches(t, *latest, *second)

	// ListSnapshots pages by id ascending (insertion order), independent of the
	// captured_at ordering used by GetLatestSnapshot.
	pageOne, more, err := repo.ListSnapshots(ctx, sess.ID, domain.Page{Limit: 2})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageOne) != 2 || !more || pageOne[0].ID != first.ID || pageOne[1].ID != second.ID {
		t.Fatalf("first snapshot page=%+v more=%v", pageOne, more)
	}
	pageTwo, more, err := repo.ListSnapshots(ctx, sess.ID, domain.Page{Limit: 2, AfterID: pageOne[1].ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(pageTwo) != 1 || more || pageTwo[0].ID != third.ID {
		t.Fatalf("second snapshot page=%+v more=%v", pageTwo, more)
	}

	// A snapshot of a missing session violates the foreign key.
	orphan := &domain.Snapshot{
		SessionID: 999999,
		Kind:      "login",
		Items:     json.RawMessage(`{}`),
		Summary:   json.RawMessage(`{}`),
	}
	assertCatalogKind(t, repo.CreateSnapshot(ctx, orphan), domain.KindDependency, "has_dependencies")
}

func TestSessionRepositoryLeases(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	sess := f.session(domain.StateValid)
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	other := f.session(domain.StateValid)
	if err := repo.CreateSession(ctx, other); err != nil {
		t.Fatal(err)
	}

	// No active lease yet.
	if lease, err := repo.GetActiveLease(ctx, sess.ID); err != nil || lease != nil {
		t.Fatalf("active lease of a fresh session=%+v err=%v", lease, err)
	}

	expiresAt := time.Now().UTC().Add(30 * time.Minute)
	held := &domain.Lease{SessionID: sess.ID, Holder: "worker-1", ExpiresAt: expiresAt}
	if err := repo.CreateLease(ctx, held); err != nil {
		t.Fatal(err)
	}
	if held.ID == 0 || held.AcquiredAt.IsZero() {
		t.Fatalf("created lease=%+v", held)
	}
	if held.ReleasedAt != nil || held.ReleasedReason != "" {
		t.Fatalf("new lease released_at=%v reason=%q", held.ReleasedAt, held.ReleasedReason)
	}
	active, err := repo.GetActiveLease(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(*active, *held) {
		t.Fatalf("active lease=%+v\nwant %+v", *active, *held)
	}

	// The partial unique index allows at most one active lease per session.
	blocked := &domain.Lease{SessionID: sess.ID, Holder: "worker-2", ExpiresAt: expiresAt.Add(time.Minute)}
	assertCatalogKind(t, repo.CreateLease(ctx, blocked), domain.KindLeaseConflict, "lease_conflict")

	// Another session's lease is independent.
	otherLease := &domain.Lease{SessionID: other.ID, Holder: "worker-2", ExpiresAt: expiresAt}
	if err := repo.CreateLease(ctx, otherLease); err != nil {
		t.Fatalf("lease on another session: %v", err)
	}

	// ReleaseLease only releases the holder's active lease.
	if err := repo.ReleaseLease(ctx, sess.ID, "worker-2", "not-the-holder"); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.GetActiveLease(ctx, sess.ID); err != nil || active == nil || active.ID != held.ID {
		t.Fatalf("active lease after foreign release=%+v err=%v", active, err)
	}
	if err := repo.ReleaseLease(ctx, sess.ID, "worker-1", "done"); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.GetActiveLease(ctx, sess.ID); err != nil || active != nil {
		t.Fatalf("active lease after release=%+v err=%v", active, err)
	}
	var releasedReason string
	var releasedAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT released_reason, released_at FROM session_lease WHERE id = $1`,
		held.ID).Scan(&releasedReason, &releasedAt); err != nil {
		t.Fatal(err)
	}
	if releasedAt == nil || releasedReason != "done" {
		t.Fatalf("released lease released_at=%v reason=%q", releasedAt, releasedReason)
	}
	// Releasing again is idempotent: there is no active lease to match.
	if err := repo.ReleaseLease(ctx, sess.ID, "worker-1", "done"); err != nil {
		t.Fatalf("second release: %v", err)
	}

	// The released slot is reusable.
	reacquired := &domain.Lease{SessionID: sess.ID, Holder: "worker-3", ExpiresAt: expiresAt.Add(5 * time.Minute)}
	if err := repo.CreateLease(ctx, reacquired); err != nil {
		t.Fatalf("reacquire after release: %v", err)
	}

	// ReleaseAllLeases releases the session's active lease and is idempotent.
	if err := repo.ReleaseAllLeases(ctx, sess.ID, "revoked"); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.GetActiveLease(ctx, sess.ID); err != nil || active != nil {
		t.Fatalf("active lease after release-all=%+v err=%v", active, err)
	}
	if err := repo.ReleaseAllLeases(ctx, sess.ID, "revoked"); err != nil {
		t.Fatalf("second release-all: %v", err)
	}
	if err := pool.QueryRow(ctx,
		`SELECT released_reason FROM session_lease WHERE id = $1`,
		reacquired.ID).Scan(&releasedReason); err != nil {
		t.Fatal(err)
	}
	if releasedReason != "revoked" {
		t.Fatalf("release-all reason=%q want revoked", releasedReason)
	}

	// An expired active lease is reclaimed lazily by the next acquisition; it is
	// never "active" for readers.
	if err := repo.ReleaseAllLeases(ctx, other.ID, "cleanup"); err != nil {
		t.Fatal(err)
	}
	expiredLease := &domain.Lease{SessionID: other.ID, Holder: "worker-4", ExpiresAt: time.Now().UTC().Add(-time.Minute)}
	if err := repo.CreateLease(ctx, expiredLease); err != nil {
		t.Fatal(err)
	}
	if active, err := repo.GetActiveLease(ctx, other.ID); err != nil || active != nil {
		t.Fatalf("expired lease reported as active=%+v err=%v", active, err)
	}
	replacement := &domain.Lease{SessionID: other.ID, Holder: "worker-5", ExpiresAt: expiresAt}
	if err := repo.CreateLease(ctx, replacement); err != nil {
		t.Fatalf("replace an expired lease: %v", err)
	}
	var reclaimReason string
	var reclaimAt *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT released_reason, released_at FROM session_lease WHERE id = $1`,
		expiredLease.ID).Scan(&reclaimReason, &reclaimAt); err != nil {
		t.Fatal(err)
	}
	if reclaimAt == nil || reclaimReason != "expired" {
		t.Fatalf("reclaimed lease released_at=%v reason=%q", reclaimAt, reclaimReason)
	}
	if active, err := repo.GetActiveLease(ctx, other.ID); err != nil || active == nil || active.ID != replacement.ID {
		t.Fatalf("active lease after replacement=%+v err=%v", active, err)
	}

	// A lease of a missing session violates the foreign key.
	orphan := &domain.Lease{SessionID: 999999, Holder: "worker-x", ExpiresAt: expiresAt}
	assertCatalogKind(t, repo.CreateLease(ctx, orphan), domain.KindDependency, "has_dependencies")
}

func TestSessionRepositoryTakeovers(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	sess := f.session(domain.StateValid)
	if err := repo.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	otherSess := f.session(domain.StateNeedsManual)
	if err := repo.CreateSession(ctx, otherSess); err != nil {
		t.Fatal(err)
	}

	// A takeover requested before a session exists carries a NULL session_id,
	// defaults to waiting and to an empty instructions document.
	unlinked := &domain.Takeover{
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonOther,
	}
	if err := repo.CreateTakeover(ctx, unlinked); err != nil {
		t.Fatal(err)
	}
	if unlinked.ID == 0 || unlinked.RequestedAt.IsZero() {
		t.Fatalf("created unlinked takeover=%+v", unlinked)
	}
	if unlinked.SessionID != nil || unlinked.Status != domain.TakeoverStatusWaiting ||
		unlinked.ExpiresAt != nil || unlinked.CompletedAt != nil {
		t.Fatalf("unlinked takeover=%+v", unlinked)
	}
	assertCatalogJSON(t, unlinked.Instructions, json.RawMessage(`{}`))

	first := &domain.Takeover{
		SessionID:     &sess.ID,
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonMFA,
		Instructions:  json.RawMessage(`{"text":"enter the one-time code"}`),
		RequestedBy:   sessionInt64(7),
		ExpiresAt:     sessionTime(time.Now().UTC().Add(30 * time.Minute)),
	}
	if err := repo.CreateTakeover(ctx, first); err != nil {
		t.Fatal(err)
	}
	if first.Status != domain.TakeoverStatusWaiting || first.RequestedAt.IsZero() {
		t.Fatalf("created takeover=%+v", first)
	}
	loadedFirst, err := repo.GetTakeover(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	assertTakeoverMatches(t, *loadedFirst, *first)
	if loadedFirst.SessionID == nil || *loadedFirst.SessionID != sess.ID {
		t.Fatalf("takeover session_id=%v want %d", loadedFirst.SessionID, sess.ID)
	}
	if loadedFirst.RequestedBy == nil || *loadedFirst.RequestedBy != 7 {
		t.Fatalf("requested_by=%v want 7", loadedFirst.RequestedBy)
	}

	waiting, err := repo.GetWaitingTakeover(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if waiting == nil || waiting.ID != first.ID {
		t.Fatalf("waiting takeover=%+v want %d", waiting, first.ID)
	}
	if none, err := repo.GetWaitingTakeover(ctx, otherSess.ID); err != nil || none != nil {
		t.Fatalf("waiting takeover of a session without one=%+v err=%v", none, err)
	}

	// waiting -> completed is a CAS; the repository fills completed_at with the
	// server clock when the caller leaves it unset.
	completed := *waiting
	completed.Status = domain.TakeoverStatusCompleted
	completed.CompletedAt = nil
	if err := repo.UpdateTakeover(ctx, &completed, domain.TakeoverStatusWaiting); err != nil {
		t.Fatal(err)
	}
	if completed.CompletedAt == nil {
		t.Fatal("completed takeover has no completed_at")
	}
	reloadedFirst, err := repo.GetTakeover(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedFirst.Status != domain.TakeoverStatusCompleted || reloadedFirst.CompletedAt == nil {
		t.Fatalf("completed takeover=%+v", reloadedFirst)
	}
	if !reloadedFirst.CompletedAt.Equal(*completed.CompletedAt) {
		t.Fatalf("completed_at=%v want %v", reloadedFirst.CompletedAt, completed.CompletedAt)
	}
	assertTakeoverMatches(t, *reloadedFirst, completed)

	// A stale status is a state conflict and writes nothing.
	stale := *reloadedFirst
	stale.Status = domain.TakeoverStatusCancelled
	assertCatalogKind(t, repo.UpdateTakeover(ctx, &stale, domain.TakeoverStatusWaiting),
		domain.KindStateConflict, "session_state_conflict")
	unchanged, err := repo.GetTakeover(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != domain.TakeoverStatusCompleted {
		t.Fatalf("row changed by a failed takeover CAS: %+v", unchanged)
	}

	// The waiting getter no longer sees the completed takeover.
	if none, err := repo.GetWaitingTakeover(ctx, sess.ID); err != nil || none != nil {
		t.Fatalf("waiting takeover after completion=%+v err=%v", none, err)
	}

	// A second takeover of this session becomes the latest one. Cancelling it
	// keeps completed_at NULL.
	third := &domain.Takeover{
		SessionID:     &sess.ID,
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonCaptcha,
		Instructions:  json.RawMessage(`{"text":"solve the captcha"}`),
	}
	if err := repo.CreateTakeover(ctx, third); err != nil {
		t.Fatal(err)
	}
	cancelled := *third
	cancelled.Status = domain.TakeoverStatusCancelled
	if err := repo.UpdateTakeover(ctx, &cancelled, domain.TakeoverStatusWaiting); err != nil {
		t.Fatal(err)
	}
	if cancelled.CompletedAt != nil {
		t.Fatalf("cancelled takeover completed_at=%v want nil", cancelled.CompletedAt)
	}
	reloadedThird, err := repo.GetTakeover(ctx, third.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reloadedThird.Status != domain.TakeoverStatusCancelled || reloadedThird.CompletedAt != nil {
		t.Fatalf("cancelled takeover=%+v", reloadedThird)
	}

	// Cancelling a takeover of the other session leaves the newest takeover of
	// this session untouched.
	other := &domain.Takeover{
		SessionID:     &otherSess.ID,
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonApproval,
	}
	if err := repo.CreateTakeover(ctx, other); err != nil {
		t.Fatal(err)
	}

	latest, err := repo.GetLatestTakeover(ctx, sess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latest == nil || latest.ID != third.ID {
		t.Fatalf("latest takeover=%+v want %d", latest, third.ID)
	}
	latestOther, err := repo.GetLatestTakeover(ctx, otherSess.ID)
	if err != nil {
		t.Fatal(err)
	}
	if latestOther == nil || latestOther.ID != other.ID {
		t.Fatalf("latest takeover of other session=%+v want %d", latestOther, other.ID)
	}

	// ListTakeovers filters by status and pages by id ascending.
	all, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || more ||
		all[0].ID != unlinked.ID || all[1].ID != first.ID ||
		all[2].ID != third.ID || all[3].ID != other.ID {
		t.Fatalf("all takeovers=%+v more=%v", all, more)
	}
	completedRows, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{
		Status: domain.TakeoverStatusCompleted,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(completedRows) != 1 || more || completedRows[0].ID != first.ID {
		t.Fatalf("completed takeovers=%+v more=%v", completedRows, more)
	}
	cancelledRows, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{
		Status: domain.TakeoverStatusCancelled,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cancelledRows) != 1 || more || cancelledRows[0].ID != third.ID {
		t.Fatalf("cancelled takeovers=%+v more=%v", cancelledRows, more)
	}
	waitingRows, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{
		Status: domain.TakeoverStatusWaiting,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(waitingRows) != 2 || more || waitingRows[0].ID != unlinked.ID || waitingRows[1].ID != other.ID {
		t.Fatalf("waiting takeovers=%+v more=%v", waitingRows, more)
	}

	firstPage, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || !more || firstPage[0].ID != unlinked.ID || firstPage[1].ID != first.ID {
		t.Fatalf("first takeover page=%+v more=%v", firstPage, more)
	}
	secondPage, more, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{
		Page: domain.Page{Limit: 2, AfterID: firstPage[len(firstPage)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 2 || more || secondPage[0].ID != third.ID || secondPage[1].ID != other.ID {
		t.Fatalf("second takeover page=%+v more=%v", secondPage, more)
	}
	if _, _, err := repo.ListTakeovers(ctx, domain.TakeoverFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("ListTakeovers accepted a zero limit")
	}

	// Not-found, missing foreign key and missing-CAS cases.
	if _, err := repo.GetTakeover(ctx, 999999); err == nil {
		t.Fatal("GetTakeover of a missing takeover returned no error")
	} else {
		assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	}
	orphan := &domain.Takeover{
		AccountID:     999999,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonOther,
	}
	assertCatalogKind(t, repo.CreateTakeover(ctx, orphan), domain.KindDependency, "has_dependencies")
	missing := &domain.Takeover{
		ID:            999999,
		AccountID:     f.AccountID,
		SystemID:      f.SystemID,
		EnvironmentID: f.EnvironmentID,
		Reason:        domain.TakeoverReasonOther,
		Status:        domain.TakeoverStatusCompleted,
	}
	assertCatalogKind(t, repo.UpdateTakeover(ctx, missing, domain.TakeoverStatusWaiting),
		domain.KindStateConflict, "session_state_conflict")
}

func TestSessionRepositoryListSessionsFiltersAndPagination(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	f := seedSessionFixture(t, ctx, pool)

	empty, more, err := repo.ListSessions(ctx, domain.SessionFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 || more {
		t.Fatalf("sessions of an empty table=%v more=%v", empty, more)
	}

	sysTwo := sessionSeedSystem(t, ctx, pool, f.CompanyID, "shop-two")
	envTwo := sessionSeedEnvironment(t, ctx, pool, sysTwo, "prod")
	bob := sessionSeedAccount(t, ctx, pool, f.CompanyID, "bob")
	bobAccess := sessionSeedAccess(t, ctx, pool, bob, f.SystemID)
	aliceSecondAccess := sessionSeedAccess(t, ctx, pool, f.AccountID, sysTwo)

	created := []*domain.Session{
		{AccountID: f.AccountID, SystemID: f.SystemID, EnvironmentID: f.EnvironmentID, AccessID: f.AccessID, State: domain.StateValid},
		{AccountID: f.AccountID, SystemID: sysTwo, EnvironmentID: envTwo, AccessID: aliceSecondAccess, State: domain.StateValid},
		{AccountID: bob, SystemID: f.SystemID, EnvironmentID: f.EnvironmentID, AccessID: bobAccess, State: domain.StateNeedsManual},
		{AccountID: f.AccountID, SystemID: f.SystemID, EnvironmentID: f.EnvironmentID, AccessID: f.AccessID, State: domain.StateExpired},
	}
	for _, sess := range created {
		if err := repo.CreateSession(ctx, sess); err != nil {
			t.Fatal(err)
		}
	}

	assertIDs := func(label string, got []domain.Session, want ...*domain.Session) {
		t.Helper()
		if len(got) != len(want) {
			t.Fatalf("%s=%+v want %d rows", label, got, len(want))
		}
		for i := range want {
			if got[i].ID != want[i].ID {
				t.Fatalf("%s[%d].ID=%d want %d", label, i, got[i].ID, want[i].ID)
			}
		}
	}

	all, more, err := repo.ListSessions(ctx, domain.SessionFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 4 || more {
		t.Fatalf("all sessions=%+v more=%v", all, more)
	}
	assertIDs("all", all, created...)

	aliceSessions, more, err := repo.ListSessions(ctx, domain.SessionFilter{AccountID: f.AccountID, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("account filter returned more unexpectedly")
	}
	assertIDs("alice sessions", aliceSessions, created[0], created[1], created[3])

	shopSessions, more, err := repo.ListSessions(ctx, domain.SessionFilter{SystemID: f.SystemID, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("system filter returned more unexpectedly")
	}
	assertIDs("shop sessions", shopSessions, created[0], created[2], created[3])

	devSessions, more, err := repo.ListSessions(ctx, domain.SessionFilter{EnvironmentID: f.EnvironmentID, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("environment filter returned more unexpectedly")
	}
	assertIDs("dev sessions", devSessions, created[0], created[2], created[3])

	accessSessions, more, err := repo.ListSessions(ctx, domain.SessionFilter{AccessID: f.AccessID, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("access filter returned more unexpectedly")
	}
	assertIDs("access sessions", accessSessions, created[0], created[3])

	validSessions, more, err := repo.ListSessions(ctx, domain.SessionFilter{State: domain.StateValid, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if more {
		t.Fatal("state filter returned more unexpectedly")
	}
	assertIDs("valid sessions", validSessions, created[0], created[1])

	combined, more, err := repo.ListSessions(ctx, domain.SessionFilter{
		AccountID: f.AccountID,
		State:     domain.StateValid,
		Page:      domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(combined) != 2 || more {
		t.Fatalf("combined filter=%+v more=%v", combined, more)
	}

	none, more, err := repo.ListSessions(ctx, domain.SessionFilter{State: domain.StateRevoked, Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if none == nil || len(none) != 0 || more {
		t.Fatalf("revoked sessions=%v more=%v", none, more)
	}

	firstPage, more, err := repo.ListSessions(ctx, domain.SessionFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 2 || !more {
		t.Fatalf("first session page=%+v more=%v", firstPage, more)
	}
	assertIDs("first page", firstPage, created[0], created[1])
	secondPage, more, err := repo.ListSessions(ctx, domain.SessionFilter{
		Page: domain.Page{Limit: 2, AfterID: firstPage[len(firstPage)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 2 || more {
		t.Fatalf("second session page=%+v more=%v", secondPage, more)
	}
	assertIDs("second page", secondPage, created[2], created[3])
	afterLast, more, err := repo.ListSessions(ctx, domain.SessionFilter{
		Page: domain.Page{Limit: 2, AfterID: created[3].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if afterLast == nil || len(afterLast) != 0 || more {
		t.Fatalf("sessions after the last id=%v more=%v", afterLast, more)
	}

	if _, _, err := repo.ListSessions(ctx, domain.SessionFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("ListSessions accepted a zero limit")
	}
}

func TestSessionRepositoryNotFoundAndOptionalLookups(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	repo := postgres.NewSessionRepository(pool)
	seedSessionFixture(t, ctx, pool)

	_, err := repo.GetSession(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	_, err = repo.GetTakeover(ctx, 999999)
	assertCatalogKind(t, err, domain.KindNotFound, "not_found")

	// Optional relations of a missing session are absent, not channel errors.
	if snapshot, err := repo.GetLatestSnapshot(ctx, 999999); err != nil || snapshot != nil {
		t.Fatalf("latest snapshot of a missing session=%+v err=%v", snapshot, err)
	}
	if snapshots, more, err := repo.ListSnapshots(ctx, 999999, domain.Page{Limit: 10}); err != nil || snapshots == nil || len(snapshots) != 0 || more {
		t.Fatalf("snapshots of a missing session=%v more=%v err=%v", snapshots, more, err)
	}
	if lease, err := repo.GetActiveLease(ctx, 999999); err != nil || lease != nil {
		t.Fatalf("active lease of a missing session=%+v err=%v", lease, err)
	}
	if takeover, err := repo.GetWaitingTakeover(ctx, 999999); err != nil || takeover != nil {
		t.Fatalf("waiting takeover of a missing session=%+v err=%v", takeover, err)
	}
	if takeover, err := repo.GetLatestTakeover(ctx, 999999); err != nil || takeover != nil {
		t.Fatalf("latest takeover of a missing session=%+v err=%v", takeover, err)
	}

	// A zero page limit is a programming error, as in the other repositories.
	if _, _, err := repo.ListSnapshots(ctx, 999999, domain.Page{Limit: 0}); err == nil {
		t.Fatal("ListSnapshots accepted a zero limit")
	}
}
