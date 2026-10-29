package httpapi

import (
	"context"

	workspaceapp "github.com/EdouardRichard/EzTest/backend/internal/workspace/application"
)

// Lister is the read-model use case consumed by the todo handler. The workspace
// application Service satisfies it; tests can substitute a fake.
type Lister interface {
	List(ctx context.Context, query workspaceapp.Query) (workspaceapp.Snapshot, error)
}
