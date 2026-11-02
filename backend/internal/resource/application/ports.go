package resourceapp

import "context"

// CatalogReader is the read port consumed by the version context. The resource
// application Service implements it.
type CatalogReader interface {
	GetSUT(context.Context, int64) (SUTView, error)
	GetEnvironment(context.Context, int64) (EnvironmentView, error)
	GetPage(context.Context, int64) (PageView, error)
	GetAPI(context.Context, int64) (APIView, error)
	GetDatabase(context.Context, int64) (DatabaseView, error)
	GetAccount(context.Context, int64) (AccountView, error)
	GetRepository(context.Context, int64) (CodeRepositoryView, error)
}

// UsageChecker answers whether a resource is still referenced by a version
// binding. The version context implements it; the resource context never
// imports the version package.
type UsageChecker interface {
	SUTInUse(context.Context, int64) (bool, error)
	EnvironmentInUse(context.Context, int64) (bool, error)
	PageInUse(context.Context, int64) (bool, error)
	APIInUse(context.Context, int64) (bool, error)
	DatabaseInUse(context.Context, int64) (bool, error)
	AccountInUse(context.Context, int64) (bool, error)
	RepositoryInUse(context.Context, int64) (bool, error)
}
