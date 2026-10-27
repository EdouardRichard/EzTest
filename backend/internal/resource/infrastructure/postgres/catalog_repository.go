package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/resource/domain"
)

// defaultSessionTTLSeconds mirrors the systems.default_session_ttl_seconds DDL
// default. CreateSystem applies it when the caller leaves the field zero.
const defaultSessionTTLSeconds = 3600

// Column lists shared by every query of a table and the scan order the
// matching scan function expects.
const (
	systemColumnList      = `id, company_id, key, name, kind, base_url, default_session_ttl_seconds, notes, status, created_at, updated_at`
	environmentColumnList = `id, system_id, key, name, base_url, kind, is_prod, allowed_hosts, login_check, allow_auto_login, require_approval, created_at, updated_at`
	appModelColumnList    = `id, system_id, parent_id, kind, key, name, path, method, source, meta, created_at, updated_at`
	companyColumnList     = `id, key, name, status, created_at, updated_at`
)

// CatalogRepository implements the system, environment, app model and company
// persistence ports on a pgx pool.
type CatalogRepository struct {
	pool *pgxpool.Pool
}

// NewCatalogRepository returns a catalog repository backed by pool.
func NewCatalogRepository(pool *pgxpool.Pool) *CatalogRepository {
	return &CatalogRepository{pool: pool}
}

var (
	_ domain.SystemRepository      = (*CatalogRepository)(nil)
	_ domain.EnvironmentRepository = (*CatalogRepository)(nil)
	_ domain.AppModelRepository    = (*CatalogRepository)(nil)
	_ domain.CompanyRepository     = (*CatalogRepository)(nil)
)

// catalogReadError translates pgx.ErrNoRows through mapNotFound and wraps any
// other error with the operation context.
func catalogReadError(err error, resource string, id int64, operation string) error {
	mapped := mapNotFound(err, resource, id)
	var domainErr *domain.Error
	if errors.As(mapped, &domainErr) {
		return domainErr
	}
	return fmt.Errorf("postgres: %s: %w", operation, err)
}

// catalogWriteError translates constraint violations through mapWriteError and
// wraps any other error with the operation context.
func catalogWriteError(err error, duplicateMessage, operation string) error {
	mapped := mapWriteError(err, duplicateMessage)
	var domainErr *domain.Error
	if errors.As(mapped, &domainErr) {
		return domainErr
	}
	return fmt.Errorf("postgres: %s: %w", operation, err)
}

// catalogWriteBackError handles an UPDATE ... RETURNING scan: zero rows means
// the resource is gone, while constraint violations raised by the update are
// translated like any other write error.
func catalogWriteBackError(err error, resource string, id int64, duplicateMessage, operation string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.NewNotFound(resource, id)
	}
	return catalogWriteError(err, duplicateMessage, operation)
}

// encodeLoginCheck renders the optional login_check document for a JSONB
// parameter; nil becomes SQL NULL.
func encodeLoginCheck(loginCheck *domain.LoginCheck) (any, error) {
	if loginCheck == nil {
		return nil, nil
	}
	return encodeJSON(loginCheck)
}

// encodeAppModelMeta renders a node's optional meta document for its NOT NULL
// JSONB column, defaulting to an empty object.
func encodeAppModelMeta(meta json.RawMessage) (any, error) {
	if len(meta) == 0 {
		meta = json.RawMessage(`{}`)
	}
	return encodeJSON(meta)
}

// scanSystem scans one row in systemColumnList order.
func scanSystem(row rowScanner) (domain.System, error) {
	var system domain.System
	if err := row.Scan(
		&system.ID,
		&system.CompanyID,
		&system.Key,
		&system.Name,
		&system.Kind,
		&system.BaseURL,
		&system.DefaultSessionTTLSeconds,
		&system.Notes,
		&system.Status,
		&system.CreatedAt,
		&system.UpdatedAt,
	); err != nil {
		return domain.System{}, err
	}
	return system, nil
}

// scanEnvironment scans one row in environmentColumnList order.
func scanEnvironment(row rowScanner) (domain.Environment, error) {
	var (
		environment domain.Environment
		loginCheck  []byte
	)
	if err := row.Scan(
		&environment.ID,
		&environment.SystemID,
		&environment.Key,
		&environment.Name,
		&environment.BaseURL,
		&environment.Kind,
		&environment.IsProd,
		&environment.AllowedHosts,
		&loginCheck,
		&environment.AllowAutoLogin,
		&environment.RequireApproval,
		&environment.CreatedAt,
		&environment.UpdatedAt,
	); err != nil {
		return domain.Environment{}, err
	}
	if environment.AllowedHosts == nil {
		environment.AllowedHosts = []string{}
	}
	if len(loginCheck) > 0 && string(loginCheck) != "null" {
		var decoded domain.LoginCheck
		if err := json.Unmarshal(loginCheck, &decoded); err != nil {
			return domain.Environment{}, fmt.Errorf("decode environment login_check: %w", err)
		}
		environment.LoginCheck = &decoded
	}
	return environment, nil
}

// scanAppModel scans one row in appModelColumnList order.
func scanAppModel(row rowScanner) (domain.AppModelNode, error) {
	var (
		node domain.AppModelNode
		meta []byte
	)
	if err := row.Scan(
		&node.ID,
		&node.SystemID,
		&node.ParentID,
		&node.Kind,
		&node.Key,
		&node.Name,
		&node.Path,
		&node.Method,
		&node.Source,
		&meta,
		&node.CreatedAt,
		&node.UpdatedAt,
	); err != nil {
		return domain.AppModelNode{}, err
	}
	if len(meta) == 0 {
		node.Meta = json.RawMessage(`{}`)
	} else {
		node.Meta = json.RawMessage(meta)
	}
	return node, nil
}

// scanCompany scans one row in companyColumnList order.
func scanCompany(row rowScanner) (domain.Company, error) {
	var company domain.Company
	if err := row.Scan(
		&company.ID,
		&company.Key,
		&company.Name,
		&company.Status,
		&company.CreatedAt,
		&company.UpdatedAt,
	); err != nil {
		return domain.Company{}, err
	}
	return company, nil
}

// CreateSystem inserts a system. An empty Status becomes active and a zero
// DefaultSessionTTLSeconds becomes the DDL default, so the caller may leave
// both unset; the full stored row (ID and timestamps included) is written back
// to s.
func (r *CatalogRepository) CreateSystem(ctx context.Context, s *domain.System) error {
	status := s.Status
	if status == "" {
		status = domain.StatusActive
	}
	ttl := s.DefaultSessionTTLSeconds
	if ttl == 0 {
		ttl = defaultSessionTTLSeconds
	}
	const query = `INSERT INTO systems
		(company_id, key, name, kind, base_url, default_session_ttl_seconds, notes, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		RETURNING ` + systemColumnList
	created, err := scanSystem(r.pool.QueryRow(ctx, query,
		s.CompanyID, s.Key, s.Name, s.Kind, s.BaseURL, ttl, s.Notes, status))
	if err != nil {
		return catalogWriteError(err, "a system with this key already exists", "create system")
	}
	*s = created
	return nil
}

// GetSystem returns one system by ID.
func (r *CatalogRepository) GetSystem(ctx context.Context, id int64) (*domain.System, error) {
	system, err := scanSystem(r.pool.QueryRow(ctx,
		`SELECT `+systemColumnList+` FROM systems WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "system", id, "get system")
	}
	return &system, nil
}

// ListSystems returns one page of systems ordered by ID ascending. An empty
// Status or Kind filter matches every value; more reports whether a row exists
// after the returned page.
func (r *CatalogRepository) ListSystems(ctx context.Context, f domain.SystemFilter) ([]domain.System, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list systems: limit must be positive")
	}
	const query = `SELECT ` + systemColumnList + `
		FROM systems
		WHERE id > $1
		  AND ($2 = '' OR status = $2)
		  AND ($3 = '' OR kind = $3)
		ORDER BY id ASC
		LIMIT $4`
	rows, err := r.pool.Query(ctx, query,
		f.Page.AfterID, f.Status, f.Kind, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list systems: %w", err)
	}
	defer rows.Close()

	systems := make([]domain.System, 0, f.Page.Limit+1)
	for rows.Next() {
		system, err := scanSystem(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan system: %w", err)
		}
		systems = append(systems, system)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list systems: %w", err)
	}

	more := len(systems) > f.Page.Limit
	if more {
		systems = systems[:f.Page.Limit]
	}
	return systems, more, nil
}

// UpdateSystem writes the mutable system columns and refreshes the server
// timestamps on s. The company and key are immutable after creation.
func (r *CatalogRepository) UpdateSystem(ctx context.Context, s *domain.System) error {
	const query = `UPDATE systems
		SET name = $2, kind = $3, base_url = $4, default_session_ttl_seconds = $5,
		    notes = $6, status = $7, updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		s.ID, s.Name, s.Kind, s.BaseURL, s.DefaultSessionTTLSeconds, s.Notes, s.Status,
	).Scan(&s.CreatedAt, &s.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "system", s.ID,
			"a system with this key already exists", "update system")
	}
	return nil
}

// DeleteSystem removes a system; dependent rows cascade in the database.
func (r *CatalogRepository) DeleteSystem(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM systems WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete system")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("system", id)
	}
	return nil
}

// CountEnvironments returns the number of environments of one system.
func (r *CatalogRepository) CountEnvironments(ctx context.Context, systemID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM environments WHERE system_id = $1`, systemID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count environments: %w", err)
	}
	return count, nil
}

// CountAccesses returns the number of account/system accesses of one system.
func (r *CatalogRepository) CountAccesses(ctx context.Context, systemID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM account_system_accesses WHERE system_id = $1`, systemID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count accesses: %w", err)
	}
	return count, nil
}

// CreateEnvironment inserts an environment; the full stored row (ID and
// timestamps included) is written back to e.
func (r *CatalogRepository) CreateEnvironment(ctx context.Context, e *domain.Environment) error {
	allowedHosts := e.AllowedHosts
	if allowedHosts == nil {
		allowedHosts = []string{}
	}
	loginCheck, err := encodeLoginCheck(e.LoginCheck)
	if err != nil {
		return fmt.Errorf("postgres: encode environment login_check: %w", err)
	}
	const query = `INSERT INTO environments
		(system_id, key, name, base_url, kind, is_prod, allowed_hosts, login_check,
		 allow_auto_login, require_approval)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING ` + environmentColumnList
	created, err := scanEnvironment(r.pool.QueryRow(ctx, query,
		e.SystemID, e.Key, e.Name, e.BaseURL, e.Kind, e.IsProd, allowedHosts,
		loginCheck, e.AllowAutoLogin, e.RequireApproval))
	if err != nil {
		return catalogWriteError(err, "an environment with this key already exists", "create environment")
	}
	*e = created
	return nil
}

// GetEnvironment returns one environment by ID.
func (r *CatalogRepository) GetEnvironment(ctx context.Context, id int64) (*domain.Environment, error) {
	environment, err := scanEnvironment(r.pool.QueryRow(ctx,
		`SELECT `+environmentColumnList+` FROM environments WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "environment", id, "get environment")
	}
	return &environment, nil
}

// ListEnvironments returns every environment of a system ordered by ID.
func (r *CatalogRepository) ListEnvironments(ctx context.Context, systemID int64) ([]domain.Environment, error) {
	rows, err := r.pool.Query(ctx,
		`SELECT `+environmentColumnList+` FROM environments WHERE system_id = $1 ORDER BY id ASC`,
		systemID)
	if err != nil {
		return nil, fmt.Errorf("postgres: list environments: %w", err)
	}
	defer rows.Close()

	environments := make([]domain.Environment, 0)
	for rows.Next() {
		environment, err := scanEnvironment(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan environment: %w", err)
		}
		environments = append(environments, environment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list environments: %w", err)
	}
	return environments, nil
}

// UpdateEnvironment writes the mutable environment columns and refreshes the
// server timestamps on e. The system and key are immutable after creation.
func (r *CatalogRepository) UpdateEnvironment(ctx context.Context, e *domain.Environment) error {
	allowedHosts := e.AllowedHosts
	if allowedHosts == nil {
		allowedHosts = []string{}
	}
	loginCheck, err := encodeLoginCheck(e.LoginCheck)
	if err != nil {
		return fmt.Errorf("postgres: encode environment login_check: %w", err)
	}
	const query = `UPDATE environments
		SET name = $2, base_url = $3, kind = $4, is_prod = $5, allowed_hosts = $6,
		    login_check = $7, allow_auto_login = $8, require_approval = $9,
		    updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		e.ID, e.Name, e.BaseURL, e.Kind, e.IsProd, allowedHosts, loginCheck,
		e.AllowAutoLogin, e.RequireApproval,
	).Scan(&e.CreatedAt, &e.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "environment", e.ID,
			"an environment with this key already exists", "update environment")
	}
	return nil
}

// DeleteEnvironment removes an environment.
func (r *CatalogRepository) DeleteEnvironment(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM environments WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete environment")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("environment", id)
	}
	return nil
}

// CountSessions returns the number of sessions of one environment.
func (r *CatalogRepository) CountSessions(ctx context.Context, environmentID int64) (int, error) {
	var count int
	if err := r.pool.QueryRow(ctx,
		`SELECT count(*) FROM sessions WHERE environment_id = $1`, environmentID).Scan(&count); err != nil {
		return 0, fmt.Errorf("postgres: count environment sessions: %w", err)
	}
	return count, nil
}

// CreateAppModel inserts an app model node. An empty Source becomes manual and
// an empty Meta becomes an empty JSON object, matching the DDL defaults; the
// full stored row (ID and timestamps included) is written back to n.
func (r *CatalogRepository) CreateAppModel(ctx context.Context, n *domain.AppModelNode) error {
	source := n.Source
	if source == "" {
		source = "manual"
	}
	meta, err := encodeAppModelMeta(n.Meta)
	if err != nil {
		return fmt.Errorf("postgres: encode app model meta: %w", err)
	}
	const query = `INSERT INTO app_model_nodes
		(system_id, parent_id, kind, key, name, path, method, source, meta)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		RETURNING ` + appModelColumnList
	created, err := scanAppModel(r.pool.QueryRow(ctx, query,
		n.SystemID, n.ParentID, n.Kind, n.Key, n.Name, n.Path, n.Method, source, meta))
	if err != nil {
		return catalogWriteError(err, "an app model node with this key already exists", "create app model")
	}
	*n = created
	return nil
}

// GetAppModel returns one app model node by ID.
func (r *CatalogRepository) GetAppModel(ctx context.Context, id int64) (*domain.AppModelNode, error) {
	node, err := scanAppModel(r.pool.QueryRow(ctx,
		`SELECT `+appModelColumnList+` FROM app_model_nodes WHERE id = $1`, id))
	if err != nil {
		return nil, catalogReadError(err, "app model", id, "get app model")
	}
	return &node, nil
}

// ListAppModels returns one page of a system's nodes ordered by ID ascending.
// An empty Kind or ParentKey matches every value; more reports whether a row
// exists after the returned page.
func (r *CatalogRepository) ListAppModels(ctx context.Context, f domain.AppModelFilter) ([]domain.AppModelNode, bool, error) {
	if f.Page.Limit <= 0 {
		return nil, false, fmt.Errorf("postgres: list app models: limit must be positive")
	}
	const query = `SELECT ` + appModelColumnList + `
		FROM app_model_nodes
		WHERE system_id = $1
		  AND id > $2
		  AND ($3 = '' OR kind = $3)
		  AND ($4 = '' OR parent_id = (
		        SELECT parent.id FROM app_model_nodes parent
		        WHERE parent.system_id = $1 AND parent.key = $4))
		ORDER BY id ASC
		LIMIT $5`
	rows, err := r.pool.Query(ctx, query,
		f.SystemID, f.Page.AfterID, f.Kind, f.ParentKey, f.Page.Limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("postgres: list app models: %w", err)
	}
	defer rows.Close()

	nodes := make([]domain.AppModelNode, 0, f.Page.Limit+1)
	for rows.Next() {
		node, err := scanAppModel(rows)
		if err != nil {
			return nil, false, fmt.Errorf("postgres: scan app model: %w", err)
		}
		nodes = append(nodes, node)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("postgres: list app models: %w", err)
	}

	more := len(nodes) > f.Page.Limit
	if more {
		nodes = nodes[:f.Page.Limit]
	}
	return nodes, more, nil
}

// UpdateAppModel writes the mutable node columns and refreshes the server
// timestamps on n. The system, kind and key are immutable after creation.
func (r *CatalogRepository) UpdateAppModel(ctx context.Context, n *domain.AppModelNode) error {
	meta, err := encodeAppModelMeta(n.Meta)
	if err != nil {
		return fmt.Errorf("postgres: encode app model meta: %w", err)
	}
	const query = `UPDATE app_model_nodes
		SET parent_id = $2, name = $3, path = $4, method = $5, meta = $6, updated_at = now()
		WHERE id = $1
		RETURNING created_at, updated_at`
	if err := r.pool.QueryRow(ctx, query,
		n.ID, n.ParentID, n.Name, n.Path, n.Method, meta,
	).Scan(&n.CreatedAt, &n.UpdatedAt); err != nil {
		return catalogWriteBackError(err, "app model", n.ID,
			"an app model node with this key already exists", "update app model")
	}
	return nil
}

// DeleteAppModel removes a node; its subtree cascades in the database.
func (r *CatalogRepository) DeleteAppModel(ctx context.Context, id int64) error {
	tag, err := r.pool.Exec(ctx, `DELETE FROM app_model_nodes WHERE id = $1`, id)
	if err != nil {
		return catalogWriteError(err, "duplicate key", "delete app model")
	}
	if tag.RowsAffected() == 0 {
		return domain.NewNotFound("app model", id)
	}
	return nil
}

// ListAppModelsByKeys returns the system's nodes whose keys are in keys,
// indexed by key. Missing keys are absent from the returned map.
func (r *CatalogRepository) ListAppModelsByKeys(ctx context.Context, systemID int64, keys []string) (map[string]domain.AppModelNode, error) {
	if len(keys) == 0 {
		return map[string]domain.AppModelNode{}, nil
	}
	rows, err := r.pool.Query(ctx,
		`SELECT `+appModelColumnList+`
		   FROM app_model_nodes
		  WHERE system_id = $1 AND key = ANY($2)
		  ORDER BY id ASC`,
		systemID, keys)
	if err != nil {
		return nil, fmt.Errorf("postgres: list app models by keys: %w", err)
	}
	defer rows.Close()

	nodes := make(map[string]domain.AppModelNode, len(keys))
	for rows.Next() {
		node, err := scanAppModel(rows)
		if err != nil {
			return nil, fmt.Errorf("postgres: scan app model: %w", err)
		}
		nodes[node.Key] = node
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("postgres: list app models by keys: %w", err)
	}
	return nodes, nil
}

// GetCompanyByKey returns the company with the given key. P2 only reads the
// seeded default company.
func (r *CatalogRepository) GetCompanyByKey(ctx context.Context, key string) (*domain.Company, error) {
	company, err := scanCompany(r.pool.QueryRow(ctx,
		`SELECT `+companyColumnList+` FROM companies WHERE key = $1`, key))
	if err != nil {
		return nil, catalogReadError(err, "company", 0, "get company by key")
	}
	return &company, nil
}
