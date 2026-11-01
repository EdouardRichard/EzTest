// Package resourcepg is the PostgreSQL adapter for the resource catalog.
package resourcepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Repository stores the resource catalog in PostgreSQL.
type Repository struct {
	pool *pgxpool.Pool
}

// New wraps a pgx pool.
func New(pool *pgxpool.Pool) *Repository {
	return &Repository{pool: pool}
}

type rowScanner interface {
	Scan(dest ...any) error
}

// wrapDB maps PostgreSQL errors to the domain sentinels.
func wrapDB(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return resource.ErrNotFound
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		switch pgErr.Code {
		case "23505": // unique_violation
			return resource.ErrDuplicate
		case "23503": // foreign_key_violation
			return resource.ErrNotFound
		}
	}
	return fmt.Errorf("%s: %w", op, err)
}

func (r *Repository) exec(ctx context.Context, op, query string, args ...any) error {
	tag, err := r.pool.Exec(ctx, query, args...)
	if err != nil {
		return wrapDB(op, err)
	}
	if tag.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	return nil
}

// ---------- SUT ----------

const sutColumns = `id, name, intro, family, created_by, created_at, updated_at`

// ListSUTs returns SUTs ordered by ID descending.
func (r *Repository) ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]resource.SUT, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutColumns+`
		FROM sut
		WHERE ($1::BIGINT IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2
	`, beforeID, limit)
	if err != nil {
		return nil, wrapDB("list suts", err)
	}
	defer rows.Close()

	items := make([]resource.SUT, 0, limit)
	for rows.Next() {
		item, err := scanSUT(rows)
		if err != nil {
			return nil, wrapDB("scan sut", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list suts", err)
	}
	return items, nil
}

// GetSUT returns one SUT.
func (r *Repository) GetSUT(ctx context.Context, id int64) (resource.SUT, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+sutColumns+` FROM sut WHERE id = $1`, id)
	item, err := scanSUT(row)
	if err != nil {
		return resource.SUT{}, wrapDB("get sut", err)
	}
	return item, nil
}

// CreateSUT inserts a SUT.
func (r *Repository) CreateSUT(ctx context.Context, sut resource.SUT) (resource.SUT, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut (id, name, intro, family, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at
	`, sut.ID, sut.Name, sut.Intro, sut.Family, sut.CreatedBy, sut.CreatedAt, sut.UpdatedAt)
	if err := row.Scan(&sut.CreatedAt, &sut.UpdatedAt); err != nil {
		return resource.SUT{}, wrapDB("create sut", err)
	}
	return sut, nil
}

// UpdateSUT updates the editable SUT columns.
func (r *Repository) UpdateSUT(ctx context.Context, sut resource.SUT) (resource.SUT, error) {
	if err := r.exec(ctx, "update sut", `
		UPDATE sut SET name = $2, intro = $3, family = $4, updated_at = $5 WHERE id = $1
	`, sut.ID, sut.Name, sut.Intro, sut.Family, sut.UpdatedAt); err != nil {
		return resource.SUT{}, err
	}
	return sut, nil
}

// DeleteSUT deletes a SUT; environments and resources cascade.
func (r *Repository) DeleteSUT(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete sut", `DELETE FROM sut WHERE id = $1`, id)
}

func scanSUT(row rowScanner) (resource.SUT, error) {
	var item resource.SUT
	if err := row.Scan(&item.ID, &item.Name, &item.Intro, &item.Family, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.SUT{}, err
	}
	return item, nil
}

// ---------- Environment ----------

const environmentColumns = `id, sut_id, name, base_url, is_default, status, created_at, updated_at`

// ListEnvironments returns every environment of a SUT in ID order.
func (r *Repository) ListEnvironments(ctx context.Context, sutID int64) ([]resource.Environment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+environmentColumns+`
		FROM sut_environment
		WHERE sut_id = $1
		ORDER BY id ASC
	`, sutID)
	if err != nil {
		return nil, wrapDB("list environments", err)
	}
	defer rows.Close()

	items := make([]resource.Environment, 0)
	for rows.Next() {
		item, err := scanEnvironment(rows)
		if err != nil {
			return nil, wrapDB("scan environment", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list environments", err)
	}
	return items, nil
}

// GetEnvironment returns one environment.
func (r *Repository) GetEnvironment(ctx context.Context, id int64) (resource.Environment, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+environmentColumns+` FROM sut_environment WHERE id = $1`, id)
	item, err := scanEnvironment(row)
	if err != nil {
		return resource.Environment{}, wrapDB("get environment", err)
	}
	return item, nil
}

// CreateEnvironment inserts an environment.
func (r *Repository) CreateEnvironment(ctx context.Context, environment resource.Environment) (resource.Environment, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_environment (id, sut_id, name, base_url, is_default, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING created_at, updated_at
	`, environment.ID, environment.SUTID, environment.Name, environment.BaseURL,
		environment.IsDefault, environment.Status, environment.CreatedAt, environment.UpdatedAt)
	if err := row.Scan(&environment.CreatedAt, &environment.UpdatedAt); err != nil {
		return resource.Environment{}, wrapDB("create environment", err)
	}
	return environment, nil
}

// UpdateEnvironment updates an environment.
func (r *Repository) UpdateEnvironment(ctx context.Context, environment resource.Environment) (resource.Environment, error) {
	if err := r.exec(ctx, "update environment", `
		UPDATE sut_environment
		SET name = $2, base_url = $3, is_default = $4, status = $5, updated_at = $6
		WHERE id = $1
	`, environment.ID, environment.Name, environment.BaseURL, environment.IsDefault,
		environment.Status, environment.UpdatedAt); err != nil {
		return resource.Environment{}, err
	}
	return environment, nil
}

// DeleteEnvironment deletes an environment; its resources cascade.
func (r *Repository) DeleteEnvironment(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete environment", `DELETE FROM sut_environment WHERE id = $1`, id)
}

func scanEnvironment(row rowScanner) (resource.Environment, error) {
	var item resource.Environment
	if err := row.Scan(&item.ID, &item.SUTID, &item.Name, &item.BaseURL,
		&item.IsDefault, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.Environment{}, err
	}
	return item, nil
}

// ---------- Page ----------

const pageColumns = `id, sut_id, environment_id, name, url, route_template, page_type,
	auth_role, module, status, source, created_at, updated_at`

// ListPages returns every page of an environment in ID order.
func (r *Repository) ListPages(ctx context.Context, environmentID int64) ([]resource.Page, error) {
	return r.listPages(ctx, `WHERE environment_id = $1 ORDER BY id ASC`, environmentID)
}

// GetPage returns one page.
func (r *Repository) GetPage(ctx context.Context, id int64) (resource.Page, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+pageColumns+` FROM sut_page WHERE id = $1`, id)
	item, err := scanPage(row)
	if err != nil {
		return resource.Page{}, wrapDB("get page", err)
	}
	return item, nil
}

// CreatePage inserts a page.
func (r *Repository) CreatePage(ctx context.Context, page resource.Page) (resource.Page, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_page (id, sut_id, environment_id, name, url, route_template,
			page_type, auth_role, module, status, source, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING created_at, updated_at
	`, page.ID, page.SUTID, page.EnvironmentID, page.Name, page.URL, page.RouteTemplate,
		page.PageType, page.AuthRole, page.Module, page.Status, page.Source, page.CreatedAt, page.UpdatedAt)
	if err := row.Scan(&page.CreatedAt, &page.UpdatedAt); err != nil {
		return resource.Page{}, wrapDB("create page", err)
	}
	return page, nil
}

// UpdatePage updates a page.
func (r *Repository) UpdatePage(ctx context.Context, page resource.Page) (resource.Page, error) {
	if err := r.exec(ctx, "update page", `
		UPDATE sut_page
		SET name = $2, url = $3, route_template = $4, page_type = $5,
			auth_role = $6, module = $7, status = $8, source = $9, updated_at = $10
		WHERE id = $1
	`, page.ID, page.Name, page.URL, page.RouteTemplate, page.PageType,
		page.AuthRole, page.Module, page.Status, page.Source, page.UpdatedAt); err != nil {
		return resource.Page{}, err
	}
	return page, nil
}

// DeletePage deletes a page.
func (r *Repository) DeletePage(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete page", `DELETE FROM sut_page WHERE id = $1`, id)
}

func (r *Repository) listPages(ctx context.Context, where string, args ...any) ([]resource.Page, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+pageColumns+` FROM sut_page `+where, args...)
	if err != nil {
		return nil, wrapDB("list pages", err)
	}
	defer rows.Close()

	items := make([]resource.Page, 0)
	for rows.Next() {
		item, err := scanPage(rows)
		if err != nil {
			return nil, wrapDB("scan page", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list pages", err)
	}
	return items, nil
}

func scanPage(row rowScanner) (resource.Page, error) {
	var item resource.Page
	if err := row.Scan(&item.ID, &item.SUTID, &item.EnvironmentID, &item.Name, &item.URL,
		&item.RouteTemplate, &item.PageType, &item.AuthRole, &item.Module,
		&item.Status, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.Page{}, err
	}
	return item, nil
}

// ---------- API ----------

const apiColumns = `id, sut_id, environment_id, method, path_template, name,
	auth_role, source, status, schema_ref, created_at, updated_at`

// ListAPIs returns every API of an environment in ID order.
func (r *Repository) ListAPIs(ctx context.Context, environmentID int64) ([]resource.API, error) {
	return r.listAPIs(ctx, `WHERE environment_id = $1 ORDER BY id ASC`, environmentID)
}

// GetAPI returns one API.
func (r *Repository) GetAPI(ctx context.Context, id int64) (resource.API, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+apiColumns+` FROM sut_api WHERE id = $1`, id)
	item, err := scanAPI(row)
	if err != nil {
		return resource.API{}, wrapDB("get api", err)
	}
	return item, nil
}

// CreateAPI inserts an API.
func (r *Repository) CreateAPI(ctx context.Context, api resource.API) (resource.API, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_api (id, sut_id, environment_id, method, path_template, name,
			auth_role, source, status, schema_ref, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at
	`, api.ID, api.SUTID, api.EnvironmentID, api.Method, api.PathTemplate, api.Name,
		api.AuthRole, api.Source, api.Status, api.SchemaRef, api.CreatedAt, api.UpdatedAt)
	if err := row.Scan(&api.CreatedAt, &api.UpdatedAt); err != nil {
		return resource.API{}, wrapDB("create api", err)
	}
	return api, nil
}

// UpdateAPI updates an API.
func (r *Repository) UpdateAPI(ctx context.Context, api resource.API) (resource.API, error) {
	if err := r.exec(ctx, "update api", `
		UPDATE sut_api
		SET method = $2, path_template = $3, name = $4, auth_role = $5,
			source = $6, status = $7, schema_ref = $8, updated_at = $9
		WHERE id = $1
	`, api.ID, api.Method, api.PathTemplate, api.Name, api.AuthRole,
		api.Source, api.Status, api.SchemaRef, api.UpdatedAt); err != nil {
		return resource.API{}, err
	}
	return api, nil
}

// DeleteAPI deletes an API.
func (r *Repository) DeleteAPI(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete api", `DELETE FROM sut_api WHERE id = $1`, id)
}

func (r *Repository) listAPIs(ctx context.Context, where string, args ...any) ([]resource.API, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+apiColumns+` FROM sut_api `+where, args...)
	if err != nil {
		return nil, wrapDB("list apis", err)
	}
	defer rows.Close()

	items := make([]resource.API, 0)
	for rows.Next() {
		item, err := scanAPI(rows)
		if err != nil {
			return nil, wrapDB("scan api", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list apis", err)
	}
	return items, nil
}

func scanAPI(row rowScanner) (resource.API, error) {
	var item resource.API
	if err := row.Scan(&item.ID, &item.SUTID, &item.EnvironmentID, &item.Method,
		&item.PathTemplate, &item.Name, &item.AuthRole, &item.Source,
		&item.Status, &item.SchemaRef, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.API{}, err
	}
	return item, nil
}

// ---------- Target database ----------

const databaseColumns = `id, sut_id, environment_id, name, kind, dsn, username,
	password, read_only, status, created_at, updated_at`

// ListDatabases returns every target database of a SUT in ID order.
func (r *Repository) ListDatabases(ctx context.Context, sutID int64) ([]resource.TargetDatabase, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+databaseColumns+`
		FROM target_database
		WHERE sut_id = $1
		ORDER BY id ASC
	`, sutID)
	if err != nil {
		return nil, wrapDB("list databases", err)
	}
	defer rows.Close()

	items := make([]resource.TargetDatabase, 0)
	for rows.Next() {
		item, err := scanDatabase(rows)
		if err != nil {
			return nil, wrapDB("scan database", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list databases", err)
	}
	return items, nil
}

// GetDatabase returns one target database.
func (r *Repository) GetDatabase(ctx context.Context, id int64) (resource.TargetDatabase, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+databaseColumns+` FROM target_database WHERE id = $1`, id)
	item, err := scanDatabase(row)
	if err != nil {
		return resource.TargetDatabase{}, wrapDB("get database", err)
	}
	return item, nil
}

// CreateDatabase inserts a target database.
func (r *Repository) CreateDatabase(ctx context.Context, database resource.TargetDatabase) (resource.TargetDatabase, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO target_database (id, sut_id, environment_id, name, kind, dsn,
			username, password, read_only, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at
	`, database.ID, database.SUTID, database.EnvironmentID, database.Name, database.Kind,
		database.DSN, database.Username, database.Password, database.ReadOnly,
		database.Status, database.CreatedAt, database.UpdatedAt)
	if err := row.Scan(&database.CreatedAt, &database.UpdatedAt); err != nil {
		return resource.TargetDatabase{}, wrapDB("create database", err)
	}
	return database, nil
}

// UpdateDatabase updates a target database.
func (r *Repository) UpdateDatabase(ctx context.Context, database resource.TargetDatabase) (resource.TargetDatabase, error) {
	if err := r.exec(ctx, "update database", `
		UPDATE target_database
		SET name = $2, kind = $3, dsn = $4, username = $5, password = $6,
			read_only = $7, status = $8, updated_at = $9
		WHERE id = $1
	`, database.ID, database.Name, database.Kind, database.DSN, database.Username,
		database.Password, database.ReadOnly, database.Status, database.UpdatedAt); err != nil {
		return resource.TargetDatabase{}, err
	}
	return database, nil
}

// DeleteDatabase deletes a target database.
func (r *Repository) DeleteDatabase(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete database", `DELETE FROM target_database WHERE id = $1`, id)
}

func scanDatabase(row rowScanner) (resource.TargetDatabase, error) {
	var item resource.TargetDatabase
	if err := row.Scan(&item.ID, &item.SUTID, &item.EnvironmentID, &item.Name, &item.Kind,
		&item.DSN, &item.Username, &item.Password, &item.ReadOnly, &item.Status,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.TargetDatabase{}, err
	}
	return item, nil
}

// ---------- Test account ----------

const accountColumns = `id, sut_id, environment_id, username, password, roles,
	notes, status, created_at, updated_at`

// ListAccounts returns every test account of a SUT in ID order.
func (r *Repository) ListAccounts(ctx context.Context, sutID int64) ([]resource.TestAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+accountColumns+`
		FROM test_account
		WHERE sut_id = $1
		ORDER BY id ASC
	`, sutID)
	if err != nil {
		return nil, wrapDB("list accounts", err)
	}
	defer rows.Close()

	items := make([]resource.TestAccount, 0)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, wrapDB("scan account", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list accounts", err)
	}
	return items, nil
}

// GetAccount returns one test account.
func (r *Repository) GetAccount(ctx context.Context, id int64) (resource.TestAccount, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+accountColumns+` FROM test_account WHERE id = $1`, id)
	item, err := scanAccount(row)
	if err != nil {
		return resource.TestAccount{}, wrapDB("get account", err)
	}
	return item, nil
}

// CreateAccount inserts a test account.
func (r *Repository) CreateAccount(ctx context.Context, account resource.TestAccount) (resource.TestAccount, error) {
	roles, err := encodeStrings(account.Roles)
	if err != nil {
		return resource.TestAccount{}, fmt.Errorf("encode account roles: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO test_account (id, sut_id, environment_id, username, password, roles,
			notes, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6::jsonb, $7, $8, $9, $10)
		RETURNING created_at, updated_at
	`, account.ID, account.SUTID, account.EnvironmentID, account.Username,
		account.Password, roles, account.Notes, account.Status, account.CreatedAt, account.UpdatedAt)
	if err := row.Scan(&account.CreatedAt, &account.UpdatedAt); err != nil {
		return resource.TestAccount{}, wrapDB("create account", err)
	}
	if account.Roles == nil {
		account.Roles = []string{}
	}
	return account, nil
}

// UpdateAccount updates a test account.
func (r *Repository) UpdateAccount(ctx context.Context, account resource.TestAccount) (resource.TestAccount, error) {
	roles, err := encodeStrings(account.Roles)
	if err != nil {
		return resource.TestAccount{}, fmt.Errorf("encode account roles: %w", err)
	}
	if err := r.exec(ctx, "update account", `
		UPDATE test_account
		SET username = $2, password = $3, roles = $4::jsonb, notes = $5,
			status = $6, updated_at = $7
		WHERE id = $1
	`, account.ID, account.Username, account.Password, roles, account.Notes,
		account.Status, account.UpdatedAt); err != nil {
		return resource.TestAccount{}, err
	}
	if account.Roles == nil {
		account.Roles = []string{}
	}
	return account, nil
}

// DeleteAccount deletes a test account.
func (r *Repository) DeleteAccount(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete account", `DELETE FROM test_account WHERE id = $1`, id)
}

func scanAccount(row rowScanner) (resource.TestAccount, error) {
	var item resource.TestAccount
	var roles []byte
	if err := row.Scan(&item.ID, &item.SUTID, &item.EnvironmentID, &item.Username,
		&item.Password, &roles, &item.Notes, &item.Status,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.TestAccount{}, err
	}
	decoded, err := decodeStrings(roles)
	if err != nil {
		return resource.TestAccount{}, fmt.Errorf("decode account roles: %w", err)
	}
	item.Roles = decoded
	return item, nil
}

// ---------- Code repository ----------

const repositoryColumns = `id, name, path, branch, include, exclude,
	created_by, status, created_at, updated_at`

// ListRepositories returns code repositories ordered by ID descending.
func (r *Repository) ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]resource.CodeRepository, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+repositoryColumns+`
		FROM code_repository
		WHERE ($1::BIGINT IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2
	`, beforeID, limit)
	if err != nil {
		return nil, wrapDB("list repositories", err)
	}
	defer rows.Close()

	items := make([]resource.CodeRepository, 0, limit)
	for rows.Next() {
		item, err := scanRepository(rows)
		if err != nil {
			return nil, wrapDB("scan repository", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list repositories", err)
	}
	return items, nil
}

// GetRepository returns one code repository.
func (r *Repository) GetRepository(ctx context.Context, id int64) (resource.CodeRepository, error) {
	row := r.pool.QueryRow(ctx, `SELECT `+repositoryColumns+` FROM code_repository WHERE id = $1`, id)
	item, err := scanRepository(row)
	if err != nil {
		return resource.CodeRepository{}, wrapDB("get repository", err)
	}
	return item, nil
}

// CreateRepository inserts a code repository.
func (r *Repository) CreateRepository(ctx context.Context, repository resource.CodeRepository) (resource.CodeRepository, error) {
	include, err := encodeStrings(repository.Include)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("encode repository include: %w", err)
	}
	exclude, err := encodeStrings(repository.Exclude)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("encode repository exclude: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO code_repository (id, name, path, branch, include, exclude,
			created_by, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6::jsonb, $7, $8, $9, $10)
		RETURNING created_at, updated_at
	`, repository.ID, repository.Name, repository.Path, repository.Branch, include,
		exclude, repository.CreatedBy, repository.Status, repository.CreatedAt, repository.UpdatedAt)
	if err := row.Scan(&repository.CreatedAt, &repository.UpdatedAt); err != nil {
		return resource.CodeRepository{}, wrapDB("create repository", err)
	}
	if repository.Include == nil {
		repository.Include = []string{}
	}
	if repository.Exclude == nil {
		repository.Exclude = []string{}
	}
	return repository, nil
}

// UpdateRepository updates a code repository.
func (r *Repository) UpdateRepository(ctx context.Context, repository resource.CodeRepository) (resource.CodeRepository, error) {
	include, err := encodeStrings(repository.Include)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("encode repository include: %w", err)
	}
	exclude, err := encodeStrings(repository.Exclude)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("encode repository exclude: %w", err)
	}
	if err := r.exec(ctx, "update repository", `
		UPDATE code_repository
		SET name = $2, path = $3, branch = $4, include = $5::jsonb,
			exclude = $6::jsonb, status = $7, updated_at = $8
		WHERE id = $1
	`, repository.ID, repository.Name, repository.Path, repository.Branch, include,
		exclude, repository.Status, repository.UpdatedAt); err != nil {
		return resource.CodeRepository{}, err
	}
	return repository, nil
}

// DeleteRepository deletes a code repository.
func (r *Repository) DeleteRepository(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete repository", `DELETE FROM code_repository WHERE id = $1`, id)
}

func scanRepository(row rowScanner) (resource.CodeRepository, error) {
	var item resource.CodeRepository
	var include, exclude []byte
	if err := row.Scan(&item.ID, &item.Name, &item.Path, &item.Branch, &include,
		&exclude, &item.CreatedBy, &item.Status, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.CodeRepository{}, err
	}
	decodedInclude, err := decodeStrings(include)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("decode repository include: %w", err)
	}
	decodedExclude, err := decodeStrings(exclude)
	if err != nil {
		return resource.CodeRepository{}, fmt.Errorf("decode repository exclude: %w", err)
	}
	item.Include = decodedInclude
	item.Exclude = decodedExclude
	return item, nil
}

// ---------- Aggregates ----------

// GetSUTGraph returns a SUT with every nested resource.
func (r *Repository) GetSUTGraph(ctx context.Context, sutID int64) (resource.SUTGraph, error) {
	sut, err := r.GetSUT(ctx, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	environments, err := r.ListEnvironments(ctx, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	pages, err := r.listPages(ctx, `WHERE sut_id = $1 ORDER BY id ASC`, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	apis, err := r.listAPIs(ctx, `WHERE sut_id = $1 ORDER BY id ASC`, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	databases, err := r.ListDatabases(ctx, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	accounts, err := r.ListAccounts(ctx, sutID)
	if err != nil {
		return resource.SUTGraph{}, err
	}
	return resource.SUTGraph{
		SUT:          sut,
		Environments: environments,
		Pages:        pages,
		APIs:         apis,
		Databases:    databases,
		Accounts:     accounts,
	}, nil
}

// ListCatalog returns every SUT with nested resources in ID order.
func (r *Repository) ListCatalog(ctx context.Context) ([]resource.SUTGraph, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sutColumns+` FROM sut ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list catalog", err)
	}
	suts := make([]resource.SUT, 0)
	for rows.Next() {
		item, err := scanSUT(rows)
		if err != nil {
			rows.Close()
			return nil, wrapDB("scan catalog sut", err)
		}
		suts = append(suts, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapDB("list catalog", err)
	}
	rows.Close()

	environments, err := r.listAllEnvironments(ctx)
	if err != nil {
		return nil, err
	}
	pages, err := r.listPages(ctx, `ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	apis, err := r.listAPIs(ctx, `ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	databases, err := r.listAllDatabases(ctx)
	if err != nil {
		return nil, err
	}
	accounts, err := r.listAllAccounts(ctx)
	if err != nil {
		return nil, err
	}

	envsBySUT := make(map[int64][]resource.Environment, len(suts))
	for _, item := range environments {
		envsBySUT[item.SUTID] = append(envsBySUT[item.SUTID], item)
	}
	pagesBySUT := make(map[int64][]resource.Page, len(suts))
	for _, item := range pages {
		pagesBySUT[item.SUTID] = append(pagesBySUT[item.SUTID], item)
	}
	apisBySUT := make(map[int64][]resource.API, len(suts))
	for _, item := range apis {
		apisBySUT[item.SUTID] = append(apisBySUT[item.SUTID], item)
	}
	databasesBySUT := make(map[int64][]resource.TargetDatabase, len(suts))
	for _, item := range databases {
		databasesBySUT[item.SUTID] = append(databasesBySUT[item.SUTID], item)
	}
	accountsBySUT := make(map[int64][]resource.TestAccount, len(suts))
	for _, item := range accounts {
		accountsBySUT[item.SUTID] = append(accountsBySUT[item.SUTID], item)
	}

	graphs := make([]resource.SUTGraph, 0, len(suts))
	for _, sut := range suts {
		graphs = append(graphs, resource.SUTGraph{
			SUT:          sut,
			Environments: envsBySUT[sut.ID],
			Pages:        pagesBySUT[sut.ID],
			APIs:         apisBySUT[sut.ID],
			Databases:    databasesBySUT[sut.ID],
			Accounts:     accountsBySUT[sut.ID],
		})
	}
	return graphs, nil
}

func (r *Repository) listAllEnvironments(ctx context.Context) ([]resource.Environment, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+environmentColumns+` FROM sut_environment ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list all environments", err)
	}
	defer rows.Close()
	items := make([]resource.Environment, 0)
	for rows.Next() {
		item, err := scanEnvironment(rows)
		if err != nil {
			return nil, wrapDB("scan environment", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list all environments", err)
	}
	return items, nil
}

func (r *Repository) listAllDatabases(ctx context.Context) ([]resource.TargetDatabase, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+databaseColumns+` FROM target_database ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list all databases", err)
	}
	defer rows.Close()
	items := make([]resource.TargetDatabase, 0)
	for rows.Next() {
		item, err := scanDatabase(rows)
		if err != nil {
			return nil, wrapDB("scan database", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list all databases", err)
	}
	return items, nil
}

func (r *Repository) listAllAccounts(ctx context.Context) ([]resource.TestAccount, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+accountColumns+` FROM test_account ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list all accounts", err)
	}
	defer rows.Close()
	items := make([]resource.TestAccount, 0)
	for rows.Next() {
		item, err := scanAccount(rows)
		if err != nil {
			return nil, wrapDB("scan account", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list all accounts", err)
	}
	return items, nil
}

func encodeStrings(values []string) (string, error) {
	if values == nil {
		values = []string{}
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func decodeStrings(raw []byte) ([]string, error) {
	if len(raw) == 0 {
		return []string{}, nil
	}
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, err
	}
	if values == nil {
		values = []string{}
	}
	return values, nil
}

var _ resource.Repository = (*Repository)(nil)
