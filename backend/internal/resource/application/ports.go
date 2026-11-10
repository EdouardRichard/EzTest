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
	// ListSUTRepositories returns the SUT-level multi-repository associations
	// (v0.22 R-C2). A version inherits the enabled rows.
	ListSUTRepositories(context.Context, int64) ([]SUTRepositoryView, error)
	// ListAllAccounts returns the global test-account pool (v0.22 DB-15).
	ListAllAccounts(context.Context) ([]AccountView, error)
}

// VersionRef identifies one version whose binding references a resource. The
// title is read from the version row by the version context; the resource
// context never imports the version package. Status and Stage describe the
// version lifecycle, and Editable reports whether a binding edit is actually
// possible (executing/reporting/completed versions are read-only).
type VersionRef struct {
	ID       int64
	Title    string
	Status   string
	Stage    string
	Editable bool
}

// UsageChecker answers whether a resource is still referenced by a version
// binding. A page/API/database guard blocks resources that are inside a bound
// environment's effective range (approved/active and not scope-excluded); an
// explicitly excluded resource is not part of the version range. The version
// context implements it; the resource context never imports the version package.
type UsageChecker interface {
	SUTInUse(context.Context, int64) (bool, error)
	EnvironmentInUse(context.Context, int64) (bool, error)
	PageInUse(context.Context, int64) (bool, error)
	APIInUse(context.Context, int64) (bool, error)
	DatabaseInUse(context.Context, int64) (bool, error)
	AccountInUse(context.Context, int64) (bool, error)
	RepositoryInUse(context.Context, int64) (bool, error)
	// ReferencingVersions lists the versions whose binding references the
	// resource of the given kind: "sut", "environment", "page", "api",
	// "database", "account" or "repository". The delete guard calls it after a
	// positive in-use check to build stable 409 details; callers cap and
	// de-duplicate the list.
	ReferencingVersions(ctx context.Context, kind string, id int64) ([]VersionRef, error)
}

// KnowledgeBindingChange is the resource context's neutral page/route/account
// invalidation signal (spec §10.1): every non-empty dimension names a binding
// value that genuinely changed. SystemIDs/EnvironmentIDs are reserved for a
// future SUT/environment move; a page or account edit never passes them, so it
// cannot invalidate the whole SUT's knowledge.
type KnowledgeBindingChange struct {
	SystemIDs      []int64
	EnvironmentIDs []int64
	PageKeys       []string
	AccountRoles   []string
	Reason         string
}

// KnowledgeBindingInvalidation mirrors the knowledge application's
// BindingInvalidation counts so the resource context can log a pass without
// importing the knowledge package.
type KnowledgeBindingInvalidation struct {
	Scanned int
	Matched int
	Marked  int
	Failed  int
}

// KnowledgeInvalidator is the cross-context sink for a resource change that
// invalidates knowledge. The assembly root adapts the knowledge application's
// MarkBindingsStale onto it; implementation is best effort and must not fail the
// resource write.
type KnowledgeInvalidator interface {
	MarkBindingsStale(ctx context.Context, change KnowledgeBindingChange) (KnowledgeBindingInvalidation, error)
}
