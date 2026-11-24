// Package resourcepg is the PostgreSQL adapter for the resource catalog.
//
// v0.2.4-B inverted the hierarchy: environment is a top-level table, sut carries
// environment_id and base_url, and the assets (sut_page/sut_api/target_database)
// are owned by sut_id alone.
package resourcepg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
			// Page uniqueness is (sut_id, normalized_url); keep route_template
			// collisions non-unique.
			if pgErr.ConstraintName == "uk_sut_page_normalized_url" {
				return resource.ErrPageURLDuplicate
			}
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

// ---------- Environment ----------

const environmentColumns = `id, name, description, status, created_at, updated_at`

// ListEnvironments returns environments ordered by ID descending.
func (r *Repository) ListEnvironments(ctx context.Context, limit int, beforeID *int64) ([]resource.Environment, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+environmentColumns+`
		FROM environment
		WHERE ($1::BIGINT IS NULL OR id < $1)
		ORDER BY id DESC
		LIMIT $2
	`, beforeID, limit)
	if err != nil {
		return nil, wrapDB("list environments", err)
	}
	defer rows.Close()

	items := make([]resource.Environment, 0, limit)
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
	row := r.pool.QueryRow(ctx, `SELECT `+environmentColumns+` FROM environment WHERE id = $1`, id)
	item, err := scanEnvironment(row)
	if err != nil {
		return resource.Environment{}, wrapDB("get environment", err)
	}
	return item, nil
}

// CreateEnvironment inserts a top-level environment.
func (r *Repository) CreateEnvironment(ctx context.Context, environment resource.Environment) (resource.Environment, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO environment (id, name, description, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING created_at, updated_at
	`, environment.ID, environment.Name, environment.Description, environment.Status,
		environment.CreatedAt, environment.UpdatedAt)
	if err := row.Scan(&environment.CreatedAt, &environment.UpdatedAt); err != nil {
		return resource.Environment{}, wrapDB("create environment", err)
	}
	return environment, nil
}

// UpdateEnvironment updates an environment.
func (r *Repository) UpdateEnvironment(ctx context.Context, environment resource.Environment) (resource.Environment, error) {
	if err := r.exec(ctx, "update environment", `
		UPDATE environment
		SET name = $2, description = $3, status = $4, updated_at = $5
		WHERE id = $1
	`, environment.ID, environment.Name, environment.Description, environment.Status,
		environment.UpdatedAt); err != nil {
		return resource.Environment{}, err
	}
	return environment, nil
}

// DeleteEnvironment deletes an environment. Its SUTs and their assets cascade
// through fk_sut_environment; the sut_repository associations of those SUTs are
// logical cross-table rows without an FK, so they are removed explicitly in the
// same transaction to avoid orphans.
func (r *Repository) DeleteEnvironment(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete environment: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		DELETE FROM sut_repository WHERE sut_id IN (SELECT id FROM sut WHERE environment_id = $1)
	`, id); err != nil {
		return wrapDB("delete environment sut repositories", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM environment WHERE id = $1`, id)
	if err != nil {
		return wrapDB("delete environment", err)
	}
	if tag.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete environment: commit: %w", err)
	}
	return nil
}

func scanEnvironment(row rowScanner) (resource.Environment, error) {
	var item resource.Environment
	if err := row.Scan(&item.ID, &item.Name, &item.Description, &item.Status,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.Environment{}, err
	}
	return item, nil
}

// ---------- SUT ----------

const sutColumns = `id, environment_id, name, intro, base_url, family, source, status, created_by, created_at, updated_at`

// ListSUTsByEnvironment returns every SUT of one environment in ID order.
func (r *Repository) ListSUTsByEnvironment(ctx context.Context, environmentID int64) ([]resource.SUT, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutColumns+`
		FROM sut
		WHERE environment_id = $1
		ORDER BY id ASC
	`, environmentID)
	if err != nil {
		return nil, wrapDB("list suts by environment", err)
	}
	defer rows.Close()

	items := make([]resource.SUT, 0)
	for rows.Next() {
		item, err := scanSUT(rows)
		if err != nil {
			return nil, wrapDB("scan sut", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list suts by environment", err)
	}
	return items, nil
}

// ListSUTs returns SUTs across every environment ordered by ID descending.
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
		INSERT INTO sut (id, environment_id, name, intro, base_url, family, source, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at, updated_at
	`, sut.ID, sut.EnvironmentID, sut.Name, sut.Intro, sut.BaseURL, sut.Family,
		sut.Source, sut.Status, sut.CreatedBy, sut.CreatedAt, sut.UpdatedAt)
	if err := row.Scan(&sut.CreatedAt, &sut.UpdatedAt); err != nil {
		return resource.SUT{}, wrapDB("create sut", err)
	}
	return sut, nil
}

// CreateSUTWithRegistration inserts a SUT and its birth registration fact in ONE
// transaction (v0.2.4-B R-B4, I-1). The SUT and its audit-trail birth record must
// commit together: if the append fails (connection drop, pool timeout, constraint),
// the SUT INSERT is rolled back too, so a SUT can never exist without its birth
// record — a half-apply the read path would otherwise surface as a successful empty
// list with no repair path. This is the same state-change-plus-fact-in-one-tx
// pattern that closed V-0020 (ApplyStage). AppendSUTRegistration remains the
// standalone append path for later (subproject F) registrations; append-only is
// untouched.
func (r *Repository) CreateSUTWithRegistration(ctx context.Context, sut resource.SUT, registration resource.SUTRegistration) (resource.SUT, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return resource.SUT{}, fmt.Errorf("create sut with registration: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row := tx.QueryRow(ctx, `
		INSERT INTO sut (id, environment_id, name, intro, base_url, family, source, status, created_by, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at, updated_at
	`, sut.ID, sut.EnvironmentID, sut.Name, sut.Intro, sut.BaseURL, sut.Family,
		sut.Source, sut.Status, sut.CreatedBy, sut.CreatedAt, sut.UpdatedAt)
	if err := row.Scan(&sut.CreatedAt, &sut.UpdatedAt); err != nil {
		return resource.SUT{}, wrapDB("create sut", err)
	}

	evidence, err := encodeStrings(registration.EvidenceRefs)
	if err != nil {
		return resource.SUT{}, fmt.Errorf("encode registration evidence_refs: %w", err)
	}
	regRow := tx.QueryRow(ctx, `
		INSERT INTO sut_registration (id, sut_id, source, evidence_refs, resolution_method, gate_result, created_at, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
		RETURNING created_at
	`, registration.ID, registration.SUTID, registration.Source, evidence,
		registration.ResolutionMethod, registration.GateResult, registration.CreatedAt, registration.CreatedBy)
	if err := regRow.Scan(&registration.CreatedAt); err != nil {
		return resource.SUT{}, wrapDB("append sut registration", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return resource.SUT{}, fmt.Errorf("create sut with registration: commit: %w", err)
	}
	return sut, nil
}

// UpdateSUT updates the editable SUT columns. The owning environment is not
// movable, so environment_id is never rewritten.
func (r *Repository) UpdateSUT(ctx context.Context, sut resource.SUT) (resource.SUT, error) {
	// status is written so the admin reject path (RejectSUT, v0.2.4-F R-B4/R-F1)
	// persists proposed/approved → rejected. The admin edit path (UpdateSUT) loads
	// the full row first, so it writes the unchanged status back (no behavior change);
	// source is immutable after creation and is deliberately not updated.
	if err := r.exec(ctx, "update sut", `
		UPDATE sut SET name = $2, intro = $3, base_url = $4, family = $5, updated_at = $6, status = $7
		WHERE id = $1
	`, sut.ID, sut.Name, sut.Intro, sut.BaseURL, sut.Family, sut.UpdatedAt, sut.Status); err != nil {
		return resource.SUT{}, err
	}
	return sut, nil
}

// DeleteSUT deletes a SUT; its pages, APIs and target databases cascade. The
// sut_repository associations are logical cross-table rows without an FK, so
// they are removed explicitly in the same transaction to avoid orphans.
func (r *Repository) DeleteSUT(ctx context.Context, id int64) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("delete sut: begin: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `DELETE FROM sut_repository WHERE sut_id = $1`, id); err != nil {
		return wrapDB("delete sut repositories", err)
	}
	tag, err := tx.Exec(ctx, `DELETE FROM sut WHERE id = $1`, id)
	if err != nil {
		return wrapDB("delete sut", err)
	}
	if tag.RowsAffected() == 0 {
		return resource.ErrNotFound
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("delete sut: commit: %w", err)
	}
	return nil
}

func scanSUT(row rowScanner) (resource.SUT, error) {
	var item resource.SUT
	if err := row.Scan(&item.ID, &item.EnvironmentID, &item.Name, &item.Intro, &item.BaseURL,
		&item.Family, &item.Source, &item.Status, &item.CreatedBy, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.SUT{}, err
	}
	return item, nil
}

// ---------- SUT registration (append-only, v0.2.4-B R-B4) ----------

const sutRegistrationColumns = `id, sut_id, source, evidence_refs, resolution_method, gate_result, created_at, created_by`

// AppendSUTRegistration inserts one registration fact. It is one of two INSERT
// paths for sut_registration (the other is CreateSUTWithRegistration, which writes
// a SUT and its birth fact in one transaction); there is still no Update/Delete, so
// registering the same SUT twice appends a second row and never rewrites the first
// (append-only). This is the standalone append path for LATER registrations of an
// existing SUT (e.g. subproject F's Agent proposals). Creating a NEW SUT must go
// through CreateSUTWithRegistration so the SUT and its birth record commit together
// — do not wire CreateSUT + AppendSUTRegistration as two steps (that re-introduces
// the I-1 half-apply).
func (r *Repository) AppendSUTRegistration(ctx context.Context, registration resource.SUTRegistration) (resource.SUTRegistration, error) {
	evidence, err := encodeStrings(registration.EvidenceRefs)
	if err != nil {
		return resource.SUTRegistration{}, fmt.Errorf("encode registration evidence_refs: %w", err)
	}
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_registration (id, sut_id, source, evidence_refs, resolution_method, gate_result, created_at, created_by)
		VALUES ($1, $2, $3, $4::jsonb, $5, $6, $7, $8)
		RETURNING created_at
	`, registration.ID, registration.SUTID, registration.Source, evidence,
		registration.ResolutionMethod, registration.GateResult, registration.CreatedAt, registration.CreatedBy)
	if err := row.Scan(&registration.CreatedAt); err != nil {
		return resource.SUTRegistration{}, wrapDB("append sut registration", err)
	}
	return registration, nil
}

// ListSUTRegistrations returns one SUT's registration facts in time order
// (created_at, then id). It is the production read path behind
// GET /api/resources/suts/{id}/registrations.
func (r *Repository) ListSUTRegistrations(ctx context.Context, sutID int64) ([]resource.SUTRegistration, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutRegistrationColumns+`
		FROM sut_registration
		WHERE sut_id = $1
		ORDER BY created_at ASC, id ASC
	`, sutID)
	if err != nil {
		return nil, wrapDB("list sut registrations", err)
	}
	defer rows.Close()

	items := make([]resource.SUTRegistration, 0)
	for rows.Next() {
		item, err := scanSUTRegistration(rows)
		if err != nil {
			return nil, wrapDB("scan sut registration", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list sut registrations", err)
	}
	return items, nil
}

func scanSUTRegistration(row rowScanner) (resource.SUTRegistration, error) {
	var item resource.SUTRegistration
	var evidence []byte
	if err := row.Scan(&item.ID, &item.SUTID, &item.Source, &evidence,
		&item.ResolutionMethod, &item.GateResult, &item.CreatedAt, &item.CreatedBy); err != nil {
		return resource.SUTRegistration{}, err
	}
	decoded, err := decodeStrings(evidence)
	if err != nil {
		return resource.SUTRegistration{}, fmt.Errorf("decode registration evidence_refs: %w", err)
	}
	item.EvidenceRefs = decoded
	return item, nil
}

// ---------- Page ----------

const pageColumns = `id, sut_id, name, url, route_template, page_type,
	auth_role, module, status, source, created_at, updated_at`

// ListPages returns every page of a SUT in ID order.
func (r *Repository) ListPages(ctx context.Context, sutID int64) ([]resource.Page, error) {
	return r.listPages(ctx, `WHERE sut_id = $1 ORDER BY id ASC`, sutID)
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

// GetPageByRouteTemplate resolves one page by (sut_id, route_template) in any
// status. The comparison uses the same trailing-slash normalization as the
// discovery observations (root keeps its slash).
func (r *Repository) GetPageByRouteTemplate(ctx context.Context, sutID int64, routeTemplate string) (resource.Page, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+pageColumns+`
		FROM sut_page
		WHERE sut_id = $1
		  AND rtrim(COALESCE(route_template, ''), '/') = $2
		ORDER BY id ASC
		LIMIT 1
	`, sutID, normalizeTemplateLookup(routeTemplate))
	item, err := scanPage(row)
	if err != nil {
		return resource.Page{}, wrapDB("get page by route template", err)
	}
	return item, nil
}

// normalizeTemplateLookup collapses a route/path template to the lookup key
// shared by the page and API queries: trimmed, with every trailing slash
// removed. The root "/" therefore becomes "", matching the SQL rtrim form.
func normalizeTemplateLookup(value string) string {
	return strings.TrimRight(strings.TrimSpace(value), "/")
}

// CreatePage inserts a page.
func (r *Repository) CreatePage(ctx context.Context, page resource.Page) (resource.Page, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_page (id, sut_id, name, url, route_template,
			page_type, auth_role, module, status, source, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING created_at, updated_at
	`, page.ID, page.SUTID, page.Name, page.URL, page.RouteTemplate,
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
	if err := row.Scan(&item.ID, &item.SUTID, &item.Name, &item.URL,
		&item.RouteTemplate, &item.PageType, &item.AuthRole, &item.Module,
		&item.Status, &item.Source, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.Page{}, err
	}
	return item, nil
}

// ---------- API ----------

const apiColumns = `id, sut_id, method, path_template, name,
	auth_role, source, status, schema_ref, created_at, updated_at`

// ListAPIs returns every API of a SUT in ID order.
func (r *Repository) ListAPIs(ctx context.Context, sutID int64) ([]resource.API, error) {
	return r.listAPIs(ctx, `WHERE sut_id = $1 ORDER BY id ASC`, sutID)
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

// GetAPIByTemplate resolves one API by (sut_id, method, path_template) in any
// status. The method comparison is case-insensitive; the path uses the same
// trailing-slash normalization as the page lookup.
func (r *Repository) GetAPIByTemplate(ctx context.Context, sutID int64, method, pathTemplate string) (resource.API, error) {
	row := r.pool.QueryRow(ctx, `
		SELECT `+apiColumns+`
		FROM sut_api
		WHERE sut_id = $1
		  AND upper(method) = upper($2)
		  AND rtrim(COALESCE(path_template, ''), '/') = $3
		ORDER BY id ASC
		LIMIT 1
	`, sutID, strings.TrimSpace(method), normalizeTemplateLookup(pathTemplate))
	item, err := scanAPI(row)
	if err != nil {
		return resource.API{}, wrapDB("get api by template", err)
	}
	return item, nil
}

// CreateAPI inserts an API.
func (r *Repository) CreateAPI(ctx context.Context, api resource.API) (resource.API, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_api (id, sut_id, method, path_template, name,
			auth_role, source, status, schema_ref, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at, updated_at
	`, api.ID, api.SUTID, api.Method, api.PathTemplate, api.Name,
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
	if err := row.Scan(&item.ID, &item.SUTID, &item.Method,
		&item.PathTemplate, &item.Name, &item.AuthRole, &item.Source,
		&item.Status, &item.SchemaRef, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.API{}, err
	}
	return item, nil
}

// ---------- Target database ----------

const databaseColumns = `id, sut_id, name, kind, dsn, username,
	password, read_only, status, created_at, updated_at`

// ListDatabases returns every target database of a SUT in ID order.
func (r *Repository) ListDatabases(ctx context.Context, sutID int64) ([]resource.TargetDatabase, error) {
	return r.listDatabases(ctx, `WHERE sut_id = $1 ORDER BY id ASC`, sutID)
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
		INSERT INTO target_database (id, sut_id, name, kind, dsn,
			username, password, read_only, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING created_at, updated_at
	`, database.ID, database.SUTID, database.Name, database.Kind,
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
	if err := row.Scan(&item.ID, &item.SUTID, &item.Name, &item.Kind,
		&item.DSN, &item.Username, &item.Password, &item.ReadOnly, &item.Status,
		&item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.TargetDatabase{}, err
	}
	return item, nil
}

// ---------- Test account ----------

const accountColumns = `id, sut_id, username, password, roles,
	notes, status, created_at, updated_at`

// ListAccounts returns the global test-account pool in ID order (v0.22 DB-15).
func (r *Repository) ListAccounts(ctx context.Context) ([]resource.TestAccount, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+accountColumns+`
		FROM test_account
		ORDER BY id ASC
	`)
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
		INSERT INTO test_account (id, sut_id, username, password, roles,
			notes, status, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5::jsonb, $6, $7, $8, $9)
		RETURNING created_at, updated_at
	`, account.ID, account.SUTID, account.Username,
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
		SET sut_id = $2, username = $3, password = $4, roles = $5::jsonb, notes = $6,
			status = $7, updated_at = $8
		WHERE id = $1
	`, account.ID, account.SUTID, account.Username, account.Password, roles, account.Notes,
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
	if err := row.Scan(&item.ID, &item.SUTID, &item.Username,
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

// ---------- SUT repositories ----------

const sutRepositoryColumns = `id, sut_id, repo_id, role, COALESCE(code_baseline_commit, ''),
	enabled, created_at, updated_at`

// ListSUTRepositories returns the SUT-level repository associations ordered by
// repository ID so the deterministic code baseline is stable (v0.22 DB-12).
func (r *Repository) ListSUTRepositories(ctx context.Context, sutID int64) ([]resource.SUTRepository, error) {
	return r.listSUTRepositories(ctx, `WHERE sut_id = $1`, sutID)
}

// ListEnvironmentSUTRepositories returns the environment's SUT-repository
// associations (v0.2.4-B GD-03), ordered by repository ID then SUT ID then
// association ID so a repository shared by two SUTs of the same environment
// resolves to one deterministic row (first wins).
//
// H-B1 (v0.2.4-H): this query is RAW catalog state — it does NOT gate on the SUT
// registration status. The R-B4 effective-scope gate (a proposed/rejected SUT
// contributes nothing to version scope, including its repository associations,
// so it can never supply pool[0] and decide the code baseline) is applied by
// Service.ListEnvironmentRepositories through the SINGLE predicate
// resourceapp.SUTView.IsApproved. The former SQL mirror of that predicate — and
// the hand-written lockstep agreement it needed with the version handoff adapter
// — is gone: one rule, one implementation, and the in-memory repository fake now
// agrees with the SQL adapter instead of silently diverging from it.
func (r *Repository) ListEnvironmentSUTRepositories(ctx context.Context, environmentID int64) ([]resource.SUTRepository, error) {
	return r.listSUTRepositories(ctx, `
		WHERE sut_id IN (SELECT id FROM sut WHERE environment_id = $1)`,
		environmentID)
}

func (r *Repository) listSUTRepositories(ctx context.Context, where string, args ...any) ([]resource.SUTRepository, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT `+sutRepositoryColumns+`
		FROM sut_repository `+where+`
		ORDER BY repo_id ASC, sut_id ASC, id ASC
	`, args...)
	if err != nil {
		return nil, wrapDB("list sut repositories", err)
	}
	defer rows.Close()

	items := make([]resource.SUTRepository, 0)
	for rows.Next() {
		item, err := scanSUTRepository(rows)
		if err != nil {
			return nil, wrapDB("scan sut repository", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list sut repositories", err)
	}
	return items, nil
}

// CreateSUTRepository inserts one SUT-repository association.
func (r *Repository) CreateSUTRepository(ctx context.Context, repository resource.SUTRepository) (resource.SUTRepository, error) {
	row := r.pool.QueryRow(ctx, `
		INSERT INTO sut_repository (id, sut_id, repo_id, role, code_baseline_commit, enabled, created_at, updated_at)
		VALUES ($1, $2, $3, $4, NULLIF($5, ''), $6, $7, $8)
		RETURNING created_at, updated_at
	`, repository.ID, repository.SUTID, repository.RepoID, repository.Role,
		repository.CodeBaselineCommit, repository.Enabled, repository.CreatedAt, repository.UpdatedAt)
	if err := row.Scan(&repository.CreatedAt, &repository.UpdatedAt); err != nil {
		return resource.SUTRepository{}, wrapDB("create sut repository", err)
	}
	return repository, nil
}

// DeleteSUTRepository removes one SUT-repository association.
func (r *Repository) DeleteSUTRepository(ctx context.Context, id int64) error {
	return r.exec(ctx, "delete sut repository", `DELETE FROM sut_repository WHERE id = $1`, id)
}

// SetSUTRepositoryBaselineCommit pins the resolved commit SHA of one
// (SUT, repository) pair; an empty commit clears it back to unresolved. It is
// the single write path shared by the version binding resolution and the code
// index backfill.
func (r *Repository) SetSUTRepositoryBaselineCommit(ctx context.Context, sutID, repoID int64, commit string) error {
	if err := r.exec(ctx, "set sut repository baseline commit", `
		UPDATE sut_repository
		SET code_baseline_commit = NULLIF($3, ''), updated_at = now()
		WHERE sut_id = $1 AND repo_id = $2
	`, sutID, repoID, commit); err != nil {
		return err
	}
	return nil
}

func scanSUTRepository(row rowScanner) (resource.SUTRepository, error) {
	var item resource.SUTRepository
	if err := row.Scan(&item.ID, &item.SUTID, &item.RepoID, &item.Role,
		&item.CodeBaselineCommit, &item.Enabled, &item.CreatedAt, &item.UpdatedAt); err != nil {
		return resource.SUTRepository{}, err
	}
	return item, nil
}

// ---------- Aggregates ----------

// GetEnvironmentGraph returns one environment with its SUTs and every asset
// those SUTs own.
func (r *Repository) GetEnvironmentGraph(ctx context.Context, environmentID int64) (resource.EnvironmentGraph, error) {
	environment, err := r.GetEnvironment(ctx, environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	suts, err := r.ListSUTsByEnvironment(ctx, environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	pages, err := r.listPages(ctx, `
		WHERE sut_id IN (SELECT id FROM sut WHERE environment_id = $1) ORDER BY id ASC`, environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	apis, err := r.listAPIs(ctx, `
		WHERE sut_id IN (SELECT id FROM sut WHERE environment_id = $1) ORDER BY id ASC`, environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	databases, err := r.listDatabases(ctx, `
		WHERE sut_id IN (SELECT id FROM sut WHERE environment_id = $1) ORDER BY id ASC`, environmentID)
	if err != nil {
		return resource.EnvironmentGraph{}, err
	}
	return resource.EnvironmentGraph{
		Environment: environment,
		SUTs:        suts,
		Pages:       pages,
		APIs:        apis,
		Databases:   databases,
	}, nil
}

// ListCatalog returns every environment with its nested SUTs and assets, ordered
// by environment ID ascending.
func (r *Repository) ListCatalog(ctx context.Context) ([]resource.EnvironmentGraph, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+environmentColumns+` FROM environment ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list catalog", err)
	}
	environments := make([]resource.Environment, 0)
	for rows.Next() {
		item, err := scanEnvironment(rows)
		if err != nil {
			rows.Close()
			return nil, wrapDB("scan catalog environment", err)
		}
		environments = append(environments, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, wrapDB("list catalog", err)
	}
	rows.Close()

	suts, err := r.listAllSUTs(ctx)
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
	databases, err := r.listDatabases(ctx, `ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}

	sutsByEnvironment := make(map[int64][]resource.SUT, len(environments))
	for _, item := range suts {
		sutsByEnvironment[item.EnvironmentID] = append(sutsByEnvironment[item.EnvironmentID], item)
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

	graphs := make([]resource.EnvironmentGraph, 0, len(environments))
	for _, environment := range environments {
		environmentSUTs := sutsByEnvironment[environment.ID]
		graphPages := make([]resource.Page, 0)
		graphAPIs := make([]resource.API, 0)
		graphDatabases := make([]resource.TargetDatabase, 0)
		for _, sut := range environmentSUTs {
			graphPages = append(graphPages, pagesBySUT[sut.ID]...)
			graphAPIs = append(graphAPIs, apisBySUT[sut.ID]...)
			graphDatabases = append(graphDatabases, databasesBySUT[sut.ID]...)
		}
		graphs = append(graphs, resource.EnvironmentGraph{
			Environment: environment,
			SUTs:        environmentSUTs,
			Pages:       graphPages,
			APIs:        graphAPIs,
			Databases:   graphDatabases,
		})
	}
	return graphs, nil
}

func (r *Repository) listAllSUTs(ctx context.Context) ([]resource.SUT, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+sutColumns+` FROM sut ORDER BY id ASC`)
	if err != nil {
		return nil, wrapDB("list all suts", err)
	}
	defer rows.Close()
	items := make([]resource.SUT, 0)
	for rows.Next() {
		item, err := scanSUT(rows)
		if err != nil {
			return nil, wrapDB("scan sut", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, wrapDB("list all suts", err)
	}
	return items, nil
}

func (r *Repository) listDatabases(ctx context.Context, where string, args ...any) ([]resource.TargetDatabase, error) {
	rows, err := r.pool.Query(ctx, `SELECT `+databaseColumns+` FROM target_database `+where, args...)
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
