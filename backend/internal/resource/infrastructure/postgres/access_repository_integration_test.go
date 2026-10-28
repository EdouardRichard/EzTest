//go:build integration

package postgres_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
)

// accessSeedSystem inserts one system directly. Account/access tests seed the
// catalog rows they depend on with raw SQL so they do not depend on the
// CatalogRepository's defaults.
func accessSeedSystem(t *testing.T, ctx context.Context, pool *pgxpool.Pool, companyID int64, key string) int64 {
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

// accessSeedEnvironment inserts one environment of systemID.
func accessSeedEnvironment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, systemID int64, key string) int64 {
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

// accessSeedSession inserts one session owned by accessID. Sessions are seeded
// with raw SQL because the session repository belongs to a later task.
func accessSeedSession(t *testing.T, ctx context.Context, pool *pgxpool.Pool, accountID, systemID, environmentID, accessID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO session (account_id, system_id, environment_id, access_id, state)
		 VALUES ($1, $2, $3, $4, 'valid')
		 RETURNING id`,
		accountID, systemID, environmentID, accessID).Scan(&id); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id
}

// accessAccountMatches compares accounts ignoring the server-managed
// timestamps.
func accessAccountMatches(a, b domain.Account) bool {
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// accessMatches compares accesses ignoring the server-managed timestamps.
func accessMatches(a, b domain.Access) bool {
	a.CreatedAt, a.UpdatedAt = time.Time{}, time.Time{}
	b.CreatedAt, b.UpdatedAt = time.Time{}, time.Time{}
	return reflect.DeepEqual(a, b)
}

// exerciseAccessRoundTrip walks one account and two accesses through create,
// read, list, update and delete.
func TestAccessRepositoryAccountAndAccessRoundTrip(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	// The same repository value satisfies both ports now that the session
	// counters have distinct names (CountSessionsByAccount/ByAccess).
	accounts := postgres.NewAccessRepository(pool)
	accesses := accounts

	sysOne := accessSeedSystem(t, ctx, pool, company.ID, "shop-one")
	sysTwo := accessSeedSystem(t, ctx, pool, company.ID, "shop-two")

	// The API input of CreateAccount has no status, so the repository mirrors
	// the account.status DDL default.
	account := &domain.Account{
		CompanyID: company.ID,
		Key:       "alice",
		Label:     "Alice",
		Username:  "alice@example.test",
		Kind:      domain.AccountKindHuman,
		Notes:     "primary identity",
	}
	if err := accounts.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	if account.ID == 0 || account.Status != domain.StatusActive {
		t.Fatalf("created account=%+v", account)
	}
	if account.CreatedAt.IsZero() || account.UpdatedAt.IsZero() {
		t.Fatalf("created account timestamps=%v/%v", account.CreatedAt, account.UpdatedAt)
	}
	loadedAccount, err := accounts.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !accessAccountMatches(*loadedAccount, *account) {
		t.Fatalf("account=%+v\nwant %+v", *loadedAccount, *account)
	}
	if !loadedAccount.CreatedAt.Equal(account.CreatedAt) || !loadedAccount.UpdatedAt.Equal(account.UpdatedAt) {
		t.Fatalf("account timestamps=%v/%v want %v/%v",
			loadedAccount.CreatedAt, loadedAccount.UpdatedAt, account.CreatedAt, account.UpdatedAt)
	}

	ttl := 900
	access := &domain.Access{
		AccountID:         account.ID,
		SystemID:          sysOne,
		AuthType:          domain.AuthTypePassword,
		Credential:        "s3cret-p@ss word",
		UsernameOverride:  "alice-override",
		LoginURL:          "https://shop-one.example.test/login",
		Enabled:           true,
		ConcurrencyLimit:  3,
		SessionTTLSeconds: &ttl,
		PermissionNote:    "read-only",
	}
	if err := accesses.CreateAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	if access.ID == 0 || access.CreatedAt.IsZero() || access.UpdatedAt.IsZero() {
		t.Fatalf("created access=%+v", access)
	}
	loadedAccess, err := accesses.GetAccess(ctx, access.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !accessMatches(*loadedAccess, *access) {
		t.Fatalf("access=%+v\nwant %+v", *loadedAccess, *access)
	}
	if loadedAccess.SessionTTLSeconds == nil || *loadedAccess.SessionTTLSeconds != ttl {
		t.Fatalf("session ttl=%v want %d", loadedAccess.SessionTTLSeconds, ttl)
	}
	if !loadedAccess.CreatedAt.Equal(access.CreatedAt) || !loadedAccess.UpdatedAt.Equal(access.UpdatedAt) {
		t.Fatalf("access timestamps=%v/%v want %v/%v",
			loadedAccess.CreatedAt, loadedAccess.UpdatedAt, access.CreatedAt, access.UpdatedAt)
	}

	// A NULL session_ttl_seconds inherits the System default, and auth types
	// without a credential may store the empty string.
	manual := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysTwo,
		AuthType:         domain.AuthTypeManual,
		Enabled:          false,
		ConcurrencyLimit: 1,
		PermissionNote:   "manual login",
	}
	if err := accesses.CreateAccess(ctx, manual); err != nil {
		t.Fatal(err)
	}
	loadedManual, err := accesses.GetAccess(ctx, manual.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedManual.SessionTTLSeconds != nil {
		t.Fatalf("manual session ttl=%v want nil", loadedManual.SessionTTLSeconds)
	}
	if loadedManual.Enabled {
		t.Fatal("manual access enabled=true want false")
	}
	if !accessMatches(*loadedManual, *manual) {
		t.Fatalf("manual access=%+v\nwant %+v", *loadedManual, *manual)
	}

	// ListAccesses returns every access of the account ordered by ID and is the
	// only access projection that carries the plaintext credential (the HTTP
	// layer is responsible for never exposing it).
	ofAccount, err := accesses.ListAccesses(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(ofAccount) != 2 || ofAccount[0].ID != access.ID || ofAccount[1].ID != manual.ID {
		t.Fatalf("accesses of account=%+v", ofAccount)
	}
	if ofAccount[0].Credential != access.Credential {
		t.Fatalf("listed credential=%q want %q", ofAccount[0].Credential, access.Credential)
	}
	if empty, err := accesses.ListAccesses(ctx, 999999); err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("accesses of missing account=%v err=%v", empty, err)
	}

	if count, err := accounts.CountAccesses(ctx, account.ID); err != nil || count != 2 {
		t.Fatalf("CountAccesses=%d err=%v", count, err)
	}
	if count, err := accounts.CountAccesses(ctx, 999999); err != nil || count != 0 {
		t.Fatalf("CountAccesses of missing account=%d err=%v", count, err)
	}
	if count, err := accesses.CountSessionsByAccess(ctx, access.ID); err != nil || count != 0 {
		t.Fatalf("CountSessionsByAccess=%d err=%v", count, err)
	}
	if count, err := accounts.CountSessionsByAccount(ctx, account.ID); err != nil || count != 0 {
		t.Fatalf("account CountSessionsByAccount=%d err=%v", count, err)
	}

	// Update every mutable column; company_id and key are immutable.
	account.Label = "Alice Cooper"
	account.Username = "alice@corp.example.test"
	account.Kind = domain.AccountKindService
	account.Notes = ""
	account.Status = domain.StatusDisabled
	if err := accounts.UpdateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	reloadedAccount, err := accounts.GetAccount(ctx, account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !accessAccountMatches(*reloadedAccount, *account) {
		t.Fatalf("updated account=%+v\nwant %+v", *reloadedAccount, *account)
	}
	// UpdateAccount writes the refreshed server timestamps back to account, so
	// the reload must carry exactly those values and updated_at must have
	// advanced past created_at.
	if !reloadedAccount.CreatedAt.Equal(account.CreatedAt) || !reloadedAccount.UpdatedAt.Equal(account.UpdatedAt) {
		t.Fatalf("updated account timestamps=%v/%v want %v/%v",
			reloadedAccount.CreatedAt, reloadedAccount.UpdatedAt, account.CreatedAt, account.UpdatedAt)
	}
	if !reloadedAccount.UpdatedAt.After(reloadedAccount.CreatedAt) {
		t.Fatalf("updated account updated_at=%v not after created_at=%v",
			reloadedAccount.UpdatedAt, reloadedAccount.CreatedAt)
	}

	access.AuthType = domain.AuthTypeToken
	access.Credential = "rotated-token-value"
	access.UsernameOverride = ""
	access.LoginURL = ""
	access.Enabled = false
	access.ConcurrencyLimit = 5
	access.SessionTTLSeconds = nil
	access.PermissionNote = ""
	if err := accesses.UpdateAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	reloadedAccess, err := accesses.GetAccess(ctx, access.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !accessMatches(*reloadedAccess, *access) {
		t.Fatalf("updated access=%+v\nwant %+v", *reloadedAccess, *access)
	}
	if reloadedAccess.SessionTTLSeconds != nil {
		t.Fatalf("updated session ttl=%v want nil", reloadedAccess.SessionTTLSeconds)
	}
	if !reloadedAccess.CreatedAt.Equal(access.CreatedAt) || !reloadedAccess.UpdatedAt.Equal(access.UpdatedAt) {
		t.Fatalf("updated access timestamps=%v/%v want %v/%v",
			reloadedAccess.CreatedAt, reloadedAccess.UpdatedAt, access.CreatedAt, access.UpdatedAt)
	}
	if !reloadedAccess.UpdatedAt.After(reloadedAccess.CreatedAt) {
		t.Fatalf("updated access updated_at=%v not after created_at=%v",
			reloadedAccess.UpdatedAt, reloadedAccess.CreatedAt)
	}

	if err := accesses.DeleteAccess(ctx, manual.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := accesses.GetAccess(ctx, manual.ID); err == nil {
		t.Fatal("deleted access still readable")
	}
	if remaining, err := accesses.ListAccesses(ctx, account.ID); err != nil || len(remaining) != 1 || remaining[0].ID != access.ID {
		t.Fatalf("accesses after delete=%+v err=%v", remaining, err)
	}

	// Deleting the account cascades to its remaining accesses.
	cascade := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysTwo,
		AuthType:         domain.AuthTypeSSO,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, cascade); err != nil {
		t.Fatal(err)
	}
	if err := accounts.DeleteAccount(ctx, account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := accounts.GetAccount(ctx, account.ID); err == nil {
		t.Fatal("deleted account still readable")
	}
	for _, id := range []int64{access.ID, cascade.ID} {
		if _, err := accesses.GetAccess(ctx, id); err == nil {
			t.Fatalf("access %d survived the account delete", id)
		}
	}
	if count, err := accounts.CountAccesses(ctx, account.ID); err != nil || count != 0 {
		t.Fatalf("CountAccesses after delete=%d err=%v", count, err)
	}
}

// TestAccessRepositoryCredentialPlaintext pins D28: credential is read and
// written verbatim (no hashing, encryption, trimming or redaction at the
// repository layer).
func TestAccessRepositoryCredentialPlaintext(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	// The same repository value satisfies both ports now that the session
	// counters have distinct names (CountSessionsByAccount/ByAccess).
	accounts := postgres.NewAccessRepository(pool)
	accesses := accounts

	account := &domain.Account{
		CompanyID: company.ID,
		Key:       "service-bot",
		Label:     "Service bot",
		Kind:      domain.AccountKindService,
	}
	if err := accounts.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	systemID := accessSeedSystem(t, ctx, pool, company.ID, "shop")

	credential := "pa$$w0rd\r\n\ttabs 鐪塶鑼俢ode "
	access := &domain.Access{
		AccountID:        account.ID,
		SystemID:         systemID,
		AuthType:         domain.AuthTypeBasic,
		Credential:       credential,
		UsernameOverride: "bot",
		Enabled:          true,
		ConcurrencyLimit: 2,
	}
	if err := accesses.CreateAccess(ctx, access); err != nil {
		t.Fatal(err)
	}

	assertCredential := func(want string) {
		t.Helper()
		loaded, err := accesses.GetAccess(ctx, access.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Credential != want {
			t.Fatalf("GetAccess credential=%q want %q", loaded.Credential, want)
		}
		listed, err := accesses.ListAccesses(ctx, account.ID)
		if err != nil {
			t.Fatal(err)
		}
		if len(listed) != 1 || listed[0].Credential != want {
			t.Fatalf("ListAccesses credential=%+v want %q", listed, want)
		}
		var stored string
		if err := pool.QueryRow(ctx,
			`SELECT credential FROM account_system_access WHERE id = $1`, access.ID).Scan(&stored); err != nil {
			t.Fatal(err)
		}
		if stored != want {
			t.Fatalf("stored credential=%q want %q", stored, want)
		}
	}

	assertCredential(credential)

	// The PATCH contract rotates and then clears the credential; both writes
	// reach the column verbatim ('' is not NULL).
	rotated := "rotated:{\"token\":\"abc\"}"
	access.Credential = rotated
	if err := accesses.UpdateAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	assertCredential(rotated)

	access.Credential = ""
	if err := accesses.UpdateAccess(ctx, access); err != nil {
		t.Fatal(err)
	}
	assertCredential("")
}

func TestAccessRepositoryConflictsAndDependencies(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	// The same repository value satisfies both ports now that the session
	// counters have distinct names (CountSessionsByAccount/ByAccess).
	accounts := postgres.NewAccessRepository(pool)
	accesses := accounts

	account := &domain.Account{
		CompanyID: company.ID,
		Key:       "alice",
		Label:     "Alice",
		Kind:      domain.AccountKindHuman,
	}
	if err := accounts.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	duplicateAccount := &domain.Account{
		CompanyID: company.ID,
		Key:       "alice",
		Label:     "Duplicate",
		Kind:      domain.AccountKindHuman,
	}
	assertCatalogKind(t, accounts.CreateAccount(ctx, duplicateAccount), domain.KindConflict, "duplicate_key")

	// (company_id, key) is unique, so another company may reuse the key.
	if _, err := pool.Exec(ctx, `INSERT INTO company (key, name) VALUES ('other', 'Other')`); err != nil {
		t.Fatal(err)
	}
	var otherCompanyID int64
	if err := pool.QueryRow(ctx, `SELECT id FROM company WHERE key = 'other'`).Scan(&otherCompanyID); err != nil {
		t.Fatal(err)
	}
	foreignAccount := &domain.Account{
		CompanyID: otherCompanyID,
		Key:       "alice",
		Label:     "Alice elsewhere",
		Kind:      domain.AccountKindHuman,
	}
	if err := accounts.CreateAccount(ctx, foreignAccount); err != nil {
		t.Fatalf("same account key under another company: %v", err)
	}

	sysOne := accessSeedSystem(t, ctx, pool, company.ID, "shop-one")
	sysTwo := accessSeedSystem(t, ctx, pool, company.ID, "shop-two")

	first := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysOne,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, first); err != nil {
		t.Fatal(err)
	}
	duplicateAccess := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysOne,
		AuthType:         domain.AuthTypeToken,
		ConcurrencyLimit: 1,
	}
	assertCatalogKind(t, accesses.CreateAccess(ctx, duplicateAccess), domain.KindConflict, "duplicate_key")

	// The pair is unique, not the system id or the account id on its own.
	otherSystem := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysTwo,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, otherSystem); err != nil {
		t.Fatalf("same account on another system: %v", err)
	}
	otherAccount := &domain.Access{
		AccountID:        foreignAccount.ID,
		SystemID:         sysOne,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, otherAccount); err != nil {
		t.Fatalf("another account on the same system: %v", err)
	}

	// Missing referenced rows surface as dependency errors (SQLSTATE 23503).
	orphanAccount := &domain.Account{
		CompanyID: 999999,
		Key:       "orphan",
		Label:     "Orphan",
		Kind:      domain.AccountKindHuman,
	}
	assertCatalogKind(t, accounts.CreateAccount(ctx, orphanAccount), domain.KindDependency, "has_dependencies")
	orphanAccess := &domain.Access{
		AccountID:        999999,
		SystemID:         sysOne,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	assertCatalogKind(t, accesses.CreateAccess(ctx, orphanAccess), domain.KindDependency, "has_dependencies")
	missingSystemAccess := &domain.Access{
		AccountID:        account.ID,
		SystemID:         999999,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	assertCatalogKind(t, accesses.CreateAccess(ctx, missingSystemAccess), domain.KindDependency, "has_dependencies")
}

func TestAccessRepositoryAccountPaginationAndFilters(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	accounts := postgres.NewAccessRepository(pool)

	empty, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{Page: domain.Page{Limit: 10}})
	if err != nil {
		t.Fatal(err)
	}
	if empty == nil || len(empty) != 0 || more {
		t.Fatalf("accounts of empty table=%v more=%v", empty, more)
	}

	created := []*domain.Account{
		{CompanyID: company.ID, Key: "alice", Label: "Alice", Kind: domain.AccountKindHuman},
		{CompanyID: company.ID, Key: "bob", Label: "Bob", Kind: domain.AccountKindHuman, Status: domain.StatusDisabled},
		{CompanyID: company.ID, Key: "svc", Label: "Service", Kind: domain.AccountKindService},
	}
	for _, account := range created {
		if err := accounts.CreateAccount(ctx, account); err != nil {
			t.Fatal(err)
		}
	}

	first, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{Page: domain.Page{Limit: 2}})
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 2 || !more || first[0].ID != created[0].ID || first[1].ID != created[1].ID {
		t.Fatalf("first page=%+v more=%v", first, more)
	}
	second, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{
		Page: domain.Page{Limit: 2, AfterID: first[len(first)-1].ID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 1 || more || second[0].ID != created[2].ID {
		t.Fatalf("second page=%+v more=%v", second, more)
	}

	serviceAccounts, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{
		Kind: domain.AccountKindService,
		Page: domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(serviceAccounts) != 1 || more || serviceAccounts[0].ID != created[2].ID {
		t.Fatalf("service accounts=%+v more=%v", serviceAccounts, more)
	}
	activeAccounts, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{
		Status: domain.StatusActive,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeAccounts) != 2 || more {
		t.Fatalf("active accounts=%+v more=%v", activeAccounts, more)
	}
	activeService, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{
		Status: domain.StatusActive,
		Kind:   domain.AccountKindService,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(activeService) != 1 || more || activeService[0].ID != created[2].ID {
		t.Fatalf("active service accounts=%+v more=%v", activeService, more)
	}
	none, more, err := accounts.ListAccounts(ctx, domain.AccountFilter{
		Status: domain.StatusDisabled,
		Kind:   domain.AccountKindService,
		Page:   domain.Page{Limit: 10},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 || more {
		t.Fatalf("disabled service accounts=%+v more=%v", none, more)
	}

	if _, _, err := accounts.ListAccounts(ctx, domain.AccountFilter{Page: domain.Page{Limit: 0}}); err == nil {
		t.Fatal("ListAccounts accepted a zero limit")
	}
}

// TestAccessRepositorySessionCounts pins the two session-count semantics: the
// same repository counts sessions by account_id for the account port and by
// access_id for the access port. The seeded IDs overlap numerically (first
// account and first access are both 1), so an implementation that used one
// column for both counters cannot pass.
func TestAccessRepositorySessionCounts(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	// The same repository value satisfies both ports now that the session
	// counters have distinct names (CountSessionsByAccount/ByAccess).
	accounts := postgres.NewAccessRepository(pool)
	accesses := accounts

	account := &domain.Account{
		CompanyID: company.ID,
		Key:       "alice",
		Label:     "Alice",
		Kind:      domain.AccountKindHuman,
	}
	if err := accounts.CreateAccount(ctx, account); err != nil {
		t.Fatal(err)
	}
	otherAccount := &domain.Account{
		CompanyID: company.ID,
		Key:       "bob",
		Label:     "Bob",
		Kind:      domain.AccountKindHuman,
	}
	if err := accounts.CreateAccount(ctx, otherAccount); err != nil {
		t.Fatal(err)
	}

	sysOne := accessSeedSystem(t, ctx, pool, company.ID, "shop-one")
	sysTwo := accessSeedSystem(t, ctx, pool, company.ID, "shop-two")
	envOne := accessSeedEnvironment(t, ctx, pool, sysOne, "dev")
	envTwo := accessSeedEnvironment(t, ctx, pool, sysTwo, "dev")

	first := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysOne,
		AuthType:         domain.AuthTypePassword,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, first); err != nil {
		t.Fatal(err)
	}
	second := &domain.Access{
		AccountID:        account.ID,
		SystemID:         sysTwo,
		AuthType:         domain.AuthTypeToken,
		ConcurrencyLimit: 1,
	}
	if err := accesses.CreateAccess(ctx, second); err != nil {
		t.Fatal(err)
	}
	if account.ID != first.ID {
		t.Fatalf("test setup: account id=%d access id=%d, want overlapping ids", account.ID, first.ID)
	}

	accessSeedSession(t, ctx, pool, account.ID, sysOne, envOne, first.ID)
	accessSeedSession(t, ctx, pool, account.ID, sysOne, envOne, first.ID)
	accessSeedSession(t, ctx, pool, account.ID, sysTwo, envTwo, second.ID)

	if count, err := accounts.CountSessionsByAccount(ctx, account.ID); err != nil || count != 3 {
		t.Fatalf("account CountSessionsByAccount=%d err=%v want 3", count, err)
	}
	if count, err := accounts.CountSessionsByAccount(ctx, otherAccount.ID); err != nil || count != 0 {
		t.Fatalf("other account CountSessionsByAccount=%d err=%v want 0", count, err)
	}
	if count, err := accesses.CountSessionsByAccess(ctx, first.ID); err != nil || count != 2 {
		t.Fatalf("first access CountSessionsByAccess=%d err=%v want 2", count, err)
	}
	if count, err := accesses.CountSessionsByAccess(ctx, second.ID); err != nil || count != 1 {
		t.Fatalf("second access CountSessionsByAccess=%d err=%v want 1", count, err)
	}
	if count, err := accesses.CountSessionsByAccess(ctx, 999999); err != nil || count != 0 {
		t.Fatalf("missing access CountSessionsByAccess=%d err=%v want 0", count, err)
	}
}

func TestAccessRepositoryNotFound(t *testing.T) {
	ctx := context.Background()
	pool := testutil.OpenTestDB(t)
	company := seedCatalogCompany(t, ctx, pool)
	// The same repository value satisfies both ports now that the session
	// counters have distinct names (CountSessionsByAccount/ByAccess).
	accounts := postgres.NewAccessRepository(pool)
	accesses := accounts

	if _, err := accounts.GetAccount(ctx, 999999); err == nil {
		t.Fatal("GetAccount of a missing account returned no error")
	} else {
		assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	}
	if _, err := accesses.GetAccess(ctx, 999999); err == nil {
		t.Fatal("GetAccess of a missing access returned no error")
	} else {
		assertCatalogKind(t, err, domain.KindNotFound, "not_found")
	}

	missingAccount := &domain.Account{
		ID: 999999, CompanyID: company.ID, Key: "missing", Label: "Missing",
		Kind: domain.AccountKindHuman, Status: domain.StatusActive,
	}
	assertCatalogKind(t, accounts.UpdateAccount(ctx, missingAccount), domain.KindNotFound, "not_found")
	assertCatalogKind(t, accounts.DeleteAccount(ctx, 999999), domain.KindNotFound, "not_found")

	systemID := accessSeedSystem(t, ctx, pool, company.ID, "shop")
	missingAccess := &domain.Access{
		ID: 999999, AccountID: 1, SystemID: systemID,
		AuthType: domain.AuthTypePassword, ConcurrencyLimit: 1,
	}
	assertCatalogKind(t, accesses.UpdateAccess(ctx, missingAccess), domain.KindNotFound, "not_found")
	assertCatalogKind(t, accesses.DeleteAccess(ctx, 999999), domain.KindNotFound, "not_found")
}
