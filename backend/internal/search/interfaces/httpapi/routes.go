package httpapi

import (
	"context"

	searchapp "github.com/EdouardRichard/EzTest/backend/internal/search/application"
)

// Searcher is the global-search use case consumed by the HTTP handler. The
// search application Service satisfies it; tests can substitute a fake.
type Searcher interface {
	Search(ctx context.Context, query searchapp.Query) (searchapp.Snapshot, error)
}
