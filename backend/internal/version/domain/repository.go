package version

import (
	"context"
	"errors"
	"time"
)

// Repository persistence errors.
var (
	ErrNotFound  = errors.New("version not found")
	ErrDuplicate = errors.New("version binding already exists")
)

// Summary is the list read model for one version.
type Summary struct {
	ID        int64
	Title     string
	Status    string
	SUTCount  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Repository persists versions, their SUT bindings and the selected database/
// account rows. Create must write the whole aggregate in one transaction.
type Repository interface {
	Create(ctx context.Context, version Version, bindings []Binding) error
	Get(ctx context.Context, id int64) (Version, []Binding, error)
	List(ctx context.Context, limit int, beforeID *int64) ([]Summary, error)
	Update(ctx context.Context, version Version) (Version, error)

	// Usage guards answer whether a resource is still referenced by a version
	// binding. They back resourceapp.UsageChecker.
	SUTInUse(ctx context.Context, id int64) (bool, error)
	EnvironmentInUse(ctx context.Context, id int64) (bool, error)
	PageInUse(ctx context.Context, id int64) (bool, error)
	APIInUse(ctx context.Context, id int64) (bool, error)
	DatabaseInUse(ctx context.Context, id int64) (bool, error)
	AccountInUse(ctx context.Context, id int64) (bool, error)
	RepositoryInUse(ctx context.Context, id int64) (bool, error)
}
