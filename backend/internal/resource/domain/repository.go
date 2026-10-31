package resource

import (
	"context"
	"errors"
)

// ErrNotFound is returned when a resource row does not exist.
var ErrNotFound = errors.New("resource not found")

// ErrDuplicate is returned when a uniqueness constraint is violated.
var ErrDuplicate = errors.New("resource already exists")

// SUTGraph is one SUT with every resource nested under its environments. It is
// the read model used to build the catalog and SUT detail views.
type SUTGraph struct {
	SUT          SUT
	Environments []Environment
	Pages        []Page
	APIs         []API
	Databases    []TargetDatabase
	Accounts     []TestAccount
}

// Repository persists the resource catalog. Implementations return ErrNotFound
// for missing rows and ErrDuplicate for uniqueness violations.
type Repository interface {
	ListSUTs(ctx context.Context, limit int, beforeID *int64) ([]SUT, error)
	GetSUT(ctx context.Context, id int64) (SUT, error)
	CreateSUT(ctx context.Context, sut SUT) (SUT, error)
	UpdateSUT(ctx context.Context, sut SUT) (SUT, error)
	DeleteSUT(ctx context.Context, id int64) error

	ListEnvironments(ctx context.Context, sutID int64) ([]Environment, error)
	GetEnvironment(ctx context.Context, id int64) (Environment, error)
	CreateEnvironment(ctx context.Context, environment Environment) (Environment, error)
	UpdateEnvironment(ctx context.Context, environment Environment) (Environment, error)
	DeleteEnvironment(ctx context.Context, id int64) error

	ListPages(ctx context.Context, environmentID int64) ([]Page, error)
	GetPage(ctx context.Context, id int64) (Page, error)
	CreatePage(ctx context.Context, page Page) (Page, error)
	UpdatePage(ctx context.Context, page Page) (Page, error)
	DeletePage(ctx context.Context, id int64) error

	ListAPIs(ctx context.Context, environmentID int64) ([]API, error)
	GetAPI(ctx context.Context, id int64) (API, error)
	CreateAPI(ctx context.Context, api API) (API, error)
	UpdateAPI(ctx context.Context, api API) (API, error)
	DeleteAPI(ctx context.Context, id int64) error

	ListDatabases(ctx context.Context, sutID int64) ([]TargetDatabase, error)
	GetDatabase(ctx context.Context, id int64) (TargetDatabase, error)
	CreateDatabase(ctx context.Context, database TargetDatabase) (TargetDatabase, error)
	UpdateDatabase(ctx context.Context, database TargetDatabase) (TargetDatabase, error)
	DeleteDatabase(ctx context.Context, id int64) error

	ListAccounts(ctx context.Context, sutID int64) ([]TestAccount, error)
	GetAccount(ctx context.Context, id int64) (TestAccount, error)
	CreateAccount(ctx context.Context, account TestAccount) (TestAccount, error)
	UpdateAccount(ctx context.Context, account TestAccount) (TestAccount, error)
	DeleteAccount(ctx context.Context, id int64) error

	ListRepositories(ctx context.Context, limit int, beforeID *int64) ([]CodeRepository, error)
	GetRepository(ctx context.Context, id int64) (CodeRepository, error)
	CreateRepository(ctx context.Context, repository CodeRepository) (CodeRepository, error)
	UpdateRepository(ctx context.Context, repository CodeRepository) (CodeRepository, error)
	DeleteRepository(ctx context.Context, id int64) error

	GetSUTGraph(ctx context.Context, sutID int64) (SUTGraph, error)
	ListCatalog(ctx context.Context) ([]SUTGraph, error)
}
