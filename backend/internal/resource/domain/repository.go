package domain

import "context"

// Page is a cursor page request. AfterID is exclusive; Limit is the requested
// page size (the HTTP layer bounds it to 1-100).
type Page struct {
	Limit   int
	AfterID int64
}

// SystemRepository is the persistence port for systems.
type SystemRepository interface {
	CreateSystem(ctx context.Context, s *System) error
	GetSystem(ctx context.Context, id int64) (*System, error)
	// ListSystems returns one page ordered by ID ascending and reports whether
	// another row exists after it.
	ListSystems(ctx context.Context, f SystemFilter) ([]System, bool, error)
	UpdateSystem(ctx context.Context, s *System) error
	DeleteSystem(ctx context.Context, id int64) error
	CountEnvironments(ctx context.Context, systemID int64) (int, error)
	CountAccesses(ctx context.Context, systemID int64) (int, error)
}

// EnvironmentRepository is the persistence port for environments.
type EnvironmentRepository interface {
	CreateEnvironment(ctx context.Context, e *Environment) error
	GetEnvironment(ctx context.Context, id int64) (*Environment, error)
	ListEnvironments(ctx context.Context, systemID int64) ([]Environment, error)
	UpdateEnvironment(ctx context.Context, e *Environment) error
	DeleteEnvironment(ctx context.Context, id int64) error
	CountSessions(ctx context.Context, environmentID int64) (int, error)
}

// AppModelRepository is the persistence port for app model nodes.
type AppModelRepository interface {
	CreateAppModel(ctx context.Context, n *AppModelNode) error
	GetAppModel(ctx context.Context, id int64) (*AppModelNode, error)
	ListAppModels(ctx context.Context, f AppModelFilter) ([]AppModelNode, bool, error)
	UpdateAppModel(ctx context.Context, n *AppModelNode) error
	DeleteAppModel(ctx context.Context, id int64) error
	// ListAppModelsByKeys returns the nodes of systemID whose keys are in keys,
	// indexed by key. Missing keys are simply absent from the map.
	ListAppModelsByKeys(ctx context.Context, systemID int64, keys []string) (map[string]AppModelNode, error)
}

// CompanyRepository is the persistence port for companies. P2 only reads the
// single seeded default company.
type CompanyRepository interface {
	GetCompanyByKey(ctx context.Context, key string) (*Company, error)
}

// AccountRepository is the persistence port for accounts.
type AccountRepository interface {
	CreateAccount(ctx context.Context, a *Account) error
	GetAccount(ctx context.Context, id int64) (*Account, error)
	ListAccounts(ctx context.Context, f AccountFilter) ([]Account, bool, error)
	UpdateAccount(ctx context.Context, a *Account) error
	DeleteAccount(ctx context.Context, id int64) error
	CountAccesses(ctx context.Context, accountID int64) (int, error)
	CountSessionsByAccount(ctx context.Context, accountID int64) (int, error)
}

// AccessRepository is the persistence port for account/system accesses.
type AccessRepository interface {
	CreateAccess(ctx context.Context, a *Access) error
	GetAccess(ctx context.Context, id int64) (*Access, error)
	ListAccesses(ctx context.Context, accountID int64) ([]Access, error)
	UpdateAccess(ctx context.Context, a *Access) error
	DeleteAccess(ctx context.Context, id int64) error
	CountSessionsByAccess(ctx context.Context, accessID int64) (int, error)
}

// SessionRepository is the persistence port for sessions, their immutable
// snapshots, leases and manual takeovers. UpdateSession and UpdateTakeover are
// compare-and-swap operations: expectedState/expectedStatus is matched in the
// WHERE clause, and zero affected rows is reported as a state conflict.
type SessionRepository interface {
	CreateSession(ctx context.Context, s *Session) error
	GetSession(ctx context.Context, id int64) (*Session, error)
	ListSessions(ctx context.Context, f SessionFilter) ([]Session, bool, error)
	UpdateSession(ctx context.Context, s *Session, expectedState string) error
	CreateSnapshot(ctx context.Context, s *Snapshot) error
	GetLatestSnapshot(ctx context.Context, sessionID int64) (*Snapshot, error)
	ListSnapshots(ctx context.Context, sessionID int64, p Page) ([]Snapshot, bool, error)
	CreateLease(ctx context.Context, l *Lease) error
	GetActiveLease(ctx context.Context, sessionID int64) (*Lease, error)
	ReleaseLease(ctx context.Context, sessionID int64, holder, reason string) error
	ReleaseAllLeases(ctx context.Context, sessionID int64, reason string) error
	CreateTakeover(ctx context.Context, t *Takeover) error
	GetTakeover(ctx context.Context, id int64) (*Takeover, error)
	GetWaitingTakeover(ctx context.Context, sessionID int64) (*Takeover, error)
	GetLatestTakeover(ctx context.Context, sessionID int64) (*Takeover, error)
	ListTakeovers(ctx context.Context, f TakeoverFilter) ([]Takeover, bool, error)
	UpdateTakeover(ctx context.Context, t *Takeover, expectedStatus string) error
}

// CodeRepoRepository is the persistence port for local code repositories.
type CodeRepoRepository interface {
	CreateCodeRepo(ctx context.Context, r *CodeRepository) error
	GetCodeRepo(ctx context.Context, id int64) (*CodeRepository, error)
	ListCodeRepos(ctx context.Context, f CodeRepoFilter) ([]CodeRepository, bool, error)
	UpdateCodeRepo(ctx context.Context, r *CodeRepository) error
	DeleteCodeRepo(ctx context.Context, id int64) error
}
