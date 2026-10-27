package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// accountColumnList and accessColumnList are the shared column projections and
// the scan order scanAccount/scanAccess expect.
const (
	accountColumnList = `id, company_id, key, label, username, kind, status, notes, created_at, updated_at`
	accessColumnList  = `id, account_id, system_id, auth_type, credential, username_override, login_url, enabled, concurrency_limit, session_ttl_seconds, permission_note, created_at, updated_at`
)

// AccessRepository implements the account and access persistence ports on a
// pgx pool. The two ports name their session counters differently
// (CountSessionsByAccount vs CountSessionsByAccess) so one value can serve
// both.
type AccessRepository struct {
	pool *pgxpool.Pool
}

// NewAccessRepository returns an account/access repository backed by pool.
func NewAccessRepository(pool *pgxpool.Pool) *AccessRepository {
	return &AccessRepository{pool: pool}
}

var (
	_ domain.AccountRepository = (*AccessRepository)(nil)
	_ domain.AccessRepository  = (*AccessRepository)(nil)
)

// scanAccount scans one row in accountColumnList order.
func scanAccount(row rowScanner) (domain.Account, error) {
	var account domain.Account
	if err := row.Scan(
		&account.ID,
		&account.CompanyID,
		&account.Key,
		&account.Label,
		&account.Username,
		&account.Kind,
		&account.Status,
		&account.Notes,
		&account.CreatedAt,
		&account.UpdatedAt,
	); err != nil {
		return domain.Account{}, err
	}
	return account, nil
}

// scanAccess scans one row in accessColumnList order. Credential is read
// verbatim: P2 stores target-system credentials in plaintext by decision D28,
// and the HTTP layer is responsible for never exposing the value.
func scanAccess(row rowScanner) (domain.Access, error) {
	var access domain.Access
	if err := row.Scan(
		&access.ID,
		&access.AccountID,
		&access.SystemID,
		&access.AuthType,
		&access.Credential,
		&access.UsernameOverride,
		&access.LoginURL,
		&access.Enabled,
		&access.ConcurrencyLimit,
		&access.SessionTTLSeconds,
		&access.PermissionNote,
		&access.CreatedAt,
		&access.UpdatedAt,
	); err != nil {
		return domain.Access{}, err
	}
	return access, nil
}

// CreateAccount inserts an account. An empty Status becomes active, matching
// the accounts.status DDL default, because the create API input carries no
// status; the full stored row (ID and timestamps included) is written back to
// a.
func (r *AccessRepository) CreateAccount(ctx context.Context, a *domain.Account) error {
	status := a.Status
	if status == "" {
		status = domain.StatusActive
	}
	const query = `INSERT INTO accounts (company_id, key, label, username, kind, status, notes)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING ` + accountColumnList
	created, err := scanAccount(r.pool.QueryRow(ctx, query,
		a.CompanyID, a.Key, a.Label, a.Username, a.Kind, status, a.Notes))
	if err != nil {
		return catalogWriteError(err, "an account with this key already exists", "create account")
	}
	*a = created
	return nil
}

// GetAccount returns one account by ID.
func (r *AccessRepository) GetAccount(ctx context.Context, id int64) (*domain.Account, error) {
	account, err := scanAccount(r.pool.QueryRow(ctx,
		`SELECT `+accountColumnList+` FROM accounts WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "account", id, "get account")
	}
	return &account, nil
}

// ListAccounts returns one page of accounts ordered by ID ascending. An empty
// Status or Kind filter matches every value; more reports whether a row exists
// after the returned page.
func (r *AccessRepository) ListAccounts(ctx context.Context, f domain.AccountFilter) ([]domain.Account, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list accounts: limit must be positive")
	}
	const query = `SELECT ` + accountColumnList + `
		FROM accounts
		WHERE id > $1
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR kind = $3)
		ORDER BY id ASC
		LIMIT $4`
	rows, err := r.pool.Query(ctx, query,
		f.Page.AfterID, f.Status, f.Kind, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list accounts: %w", err)
	}
	defer rows.Close()

	accounts := make([]domain.Account, 0, f.Page.Limit+1)
	for rows.Next() {
		account, err := scanAccount(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan account: %w", err)
		}
		accounts = append(accounts, account)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list accounts: %w", err)
	}

	more := len(accounts) > f.Page.Limit
	if more {
		accounts = accounts[:f.Page.Limit]
	}
	return accounts, more, nil
}

// UpdateAccount writes the mutable account columns and refreshes the server
// timestamps on a. The company and key are immutable after creation.
func (r *AccessRepository) UpdateAccount(ctx context.Context, a *domain.Account) error {
	const query = `UPDATE accounts
		SET label = $2, username = $3, kind = $4, notes = $5, status = $6, updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		a.ID, a.Label, a.Username, a.Kind, a.Notes, a.Status,
	).Scan(&a.CreatedAt, &a.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "account", a.ID,
			"an account with this key already exists", "update account")
	}
	return nil
}

// DeleteAccount removes an account; its accesses and sessions cascade in the
// database.
func (r *AccessRepository) DeleteAccount(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM accounts WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete account")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("account", id)
	}
	return nil
}

// CountAccesses returns the number of account/system accesses of one account.
func (r *AccessRepository) CountAccesses(ctx context.Context, accountID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM account_system_accesses WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count account accesses: %w", err)
	}
	return count, nil
}

// CountSessionsByAccount returns the number of sessions of one account
// (sessions.account_id).
func (r *AccessRepository) CountSessionsByAccount(ctx context.Context, accountID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE account_id = $1`, accountID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count account sessions: %w", err)
	}
	return count, nil
}

// CreateAccess inserts an access. The full stored row (ID and timestamps
// included) is written back to a; the credential is stored verbatim (D28).
func (r *AccessRepository) CreateAccess(ctx context.Context, a *domain.Access) error {
	const query = `INSERT INTO account_system_accesses
		(account_id, system_id, auth_type, credential, username_override, login_url,
		 enabled, concurrency_limit, session_ttl_seconds, permission_note)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + accessColumnList
	created, err := scanAccess(r.pool.QueryRow(ctx, query,
		a.AccountID, a.SystemID, a.AuthType, a.Credential, a.UsernameOverride, a.LoginURL,
		a.Enabled, a.ConcurrencyLimit, a.SessionTTLSeconds, a.PermissionNote))
	if err != nil {
		return catalogWriteError(err, "an access for this account and system already exists", "create access")
	}
	*a = created
	return nil
}

// GetAccess returns one access by ID, including its plaintext credential.
func (r *AccessRepository) GetAccess(ctx context.Context, id int64) (*domain.Access, error) {
	access, err := scanAccess(r.pool.QueryRow(ctx,
		`SELECT `+accessColumnList+` FROM account_system_accesses WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "access", id, "get access")
	}
	return &access, nil
}

// ListAccesses returns every access of an account ordered by ID ascending.
// This is the repository-level projection that carries the plaintext
// credential; HTTP responses must never expose it.
func (r *AccessRepository) ListAccesses(ctx context.Context, accountID int64) ([]domain.Access, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+accessColumnList+` FROM account_system_accesses WHERE account_id = $1 ORDER BY id ASC`,
		accountID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list accesses: %w", err)
	}
	defer rows.Close()

	accesses := make([]domain.Access, 0)
	for rows.Next() {
		access, err := scanAccess(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan access: %w", err)
		}
		accesses = append(accesses, access)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list accesses: %w", err)
	}
	return accesses, nil
}

// UpdateAccess writes every mutable access column, including the plaintext
// credential (D28), and refreshes the server timestamps on a. The account and
// system are immutable after creation.
func (r *AccessRepository) UpdateAccess(ctx context.Context, a *domain.Access) error {
	const query = `UPDATE account_system_accesses
		SET auth_type = $2, credential = $3, username_override = $4, login_url = $5,
		    enabled = $6, concurrency_limit = $7, session_ttl_seconds = $8,
		    permission_note = $9, updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		a.ID, a.AuthType, a.Credential, a.UsernameOverride, a.LoginURL,
		a.Enabled, a.ConcurrencyLimit, a.SessionTTLSeconds, a.PermissionNote,
	).Scan(&a.CreatedAt, &a.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "access", a.ID,
			"an access for this account and system already exists", "update access")
	}
	return nil
}

// DeleteAccess removes an access; its sessions cascade in the database.
func (r *AccessRepository) DeleteAccess(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM account_system_accesses WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete access")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("access", id)
	}
	return nil
}

// CountSessionsByAccess returns the number of sessions of one access
// (sessions.access_id).
func (r *AccessRepository) CountSessionsByAccess(ctx context.Context, accessID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE access_id = $1`, accessID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count access sessions: %w", err)
	}
	return count, nil
}
