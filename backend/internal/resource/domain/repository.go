package resource

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a resource row does not exist.
var ErrNotFound = errors.New("resource not found")

// ErrDuplicate is returned when a uniqueness constraint is violated.
var ErrDuplicate = errors.New("resource already exists")

// EnvironmentGraph is one environment with its SUTs and every asset those SUTs
// own. It is the read model used to build the catalog and the environment detail
// view (v0.2.4-B hierarchy: environment -> SUT -> assets).
type EnvironmentGraph struct {
	Environment Environment
	SUTs        []SUT
	Pages       []Page
	APIs        []API
	Databases   []TargetDatabase
}

// Repository persists the resource catalog. Implementations return ErrNotFound
// for missing rows and ErrDuplicate for uniqueness violations.
type Repository interface {
	// Environments are top-level (v0.2.4-B SC-B1).
	ListEnvironments(ctx context.Context, limit int, beforeID *int64) ([]Environment, error)
	GetEnvironment(ctx context.Context, id int64) (Environment, error)
	CreateEnvironment(ctx context.Context, environment Environment) (Environment, error)
	UpdateEnvironment(ctx context.Context, environment Environment) (Environment, error)
	DeleteEnvironment(ctx context.Context, id int64) error

	// SUTs belong to one environment and are unique by name inside it.
	// ListSUTsByEnvironment returns them in ID order without pagination; ListSUTs
	// is the global cursor-paginated listing behind GET /api/resources/suts.
	ListSUTsByEnvironment(ctx context.Context, environmentID int64) ([]SUT, error)
	ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]SUT, error)
	GetSUT(ctx context.Context, id int64) (SUT, error)
	// CreateSUT inserts a SUT WITHOUT a birth registration fact — fixture/test use
	// only. Production creation must go through CreateSUTWithRegistration so the SUT
	// and its birth record commit together (calling this alone re-creates the I-1
	// half-apply: a SUT with no birth record).
	CreateSUT(ctx context.Context, sut SUT) (SUT, error)
	UpdateSUT(ctx context.Context, sut SUT) (SUT, error)
	DeleteSUT(ctx context.Context, id int64) error

	// SUT registrations are append-only facts (v0.2.4-B R-B4): the repository
	// exposes only Append/List, never Update/Delete. Registering the same SUT
	// twice appends a second row and never rewrites the first. ListSUTRegistrations
	// returns one SUT's facts in time order (created_at, then id).
	AppendSUTRegistration(ctx context.Context, registration SUTRegistration) (SUTRegistration, error)
	ListSUTRegistrations(ctx context.Context, sutID int64) ([]SUTRegistration, error)
	// CreateSUTWithRegistration inserts a SUT and its birth registration fact in
	// ONE transaction (I-1): a failed append must roll the SUT back, never leave a
	// SUT that exists without its birth record. Append-only is untouched — this is
	// a creation, not an update/delete; AppendSUTRegistration remains the standalone
	// append path for later registrations.
	CreateSUTWithRegistration(ctx context.Context, sut SUT, registration SUTRegistration) (SUT, error)

	// Assets are owned by their SUT alone; uniqueness is scoped to sut_id.
	ListPages(ctx context.Context, sutID int64) ([]Page, error)
	GetPage(ctx context.Context, id int64) (Page, error)
	// GetPageByRouteTemplate resolves the page of one SUT by its route_template,
	// regardless of status. It is the W1-12 "already registered" lookup:
	// observations only carry host + template, so the normalized_url dedupe path
	// cannot be reused. A trailing slash (except the root) is ignored. It returns
	// ErrNotFound when no page matches.
	GetPageByRouteTemplate(ctx context.Context, sutID int64, routeTemplate string) (Page, error)
	CreatePage(ctx context.Context, page Page) (Page, error)
	UpdatePage(ctx context.Context, page Page) (Page, error)
	DeletePage(ctx context.Context, id int64) error

	ListAPIs(ctx context.Context, sutID int64) ([]API, error)
	GetAPI(ctx context.Context, id int64) (API, error)
	// GetAPIByTemplate resolves the API of one SUT by method + path_template,
	// regardless of status (W1-12). A trailing slash (except the root) is
	// ignored. It returns ErrNotFound when no API matches.
	GetAPIByTemplate(ctx context.Context, sutID int64, method, pathTemplate string) (API, error)
	CreateAPI(ctx context.Context, api API) (API, error)
	UpdateAPI(ctx context.Context, api API) (API, error)
	DeleteAPI(ctx context.Context, id int64) error

	ListDatabases(ctx context.Context, sutID int64) ([]TargetDatabase, error)
	GetDatabase(ctx context.Context, id int64) (TargetDatabase, error)
	CreateDatabase(ctx context.Context, database TargetDatabase) (TargetDatabase, error)
	UpdateDatabase(ctx context.Context, database TargetDatabase) (TargetDatabase, error)
	DeleteDatabase(ctx context.Context, id int64) error

	// The test-account pool is global (v0.22 DB-15); sut_id is an affinity hint.
	ListAccounts(ctx context.Context) ([]TestAccount, error)
	GetAccount(ctx context.Context, id int64) (TestAccount, error)
	CreateAccount(ctx context.Context, account TestAccount) (TestAccount, error)
	UpdateAccount(ctx context.Context, account TestAccount) (TestAccount, error)
	DeleteAccount(ctx context.Context, id int64) error

	// SUT repositories are the SUT-level multi-repository associations (DB-12).
	// The environment code pool is the union of its SUTs' repositories (GD-03),
	// de-duplicated by repository ID and ordered by repository ID so the
	// deterministic baseline repository is stable. Both reads return RAW catalog
	// state: the R-B4 effective-scope gate (only approved SUTs contribute) is
	// applied by the application through SUTView.IsApproved, never in SQL.
	ListSUTRepositories(ctx context.Context, sutID int64) ([]SUTRepository, error)
	ListEnvironmentSUTRepositories(ctx context.Context, environmentID int64) ([]SUTRepository, error)
	CreateSUTRepository(ctx context.Context, repository SUTRepository) (SUTRepository, error)
	DeleteSUTRepository(ctx context.Context, id int64) error
	// SetSUTRepositoryBaselineCommit pins the resolved commit SHA of one
	// (SUT, repository) pair. An empty commit clears it back to unresolved. It
	// returns ErrNotFound when the association does not exist.
	SetSUTRepositoryBaselineCommit(ctx context.Context, sutID, repoID int64, commit string) error

	ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]CodeRepository, error)
	GetRepository(ctx context.Context, id int64) (CodeRepository, error)
	CreateRepository(ctx context.Context, repository CodeRepository) (CodeRepository, error)
	UpdateRepository(ctx context.Context, repository CodeRepository) (CodeRepository, error)
	DeleteRepository(ctx context.Context, id int64) error

	GetEnvironmentGraph(ctx context.Context, environmentID int64) (EnvironmentGraph, error)
	ListCatalog(ctx context.Context) ([]EnvironmentGraph, error)
}
