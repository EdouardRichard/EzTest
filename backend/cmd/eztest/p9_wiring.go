package main

// P9 explore composition root. Task 49 wires the autonomous login/session
// reuse runtime that the exploration capabilities (Task 35 executor actions,
// Task 36/37 discovery/coverage) call into. The process-wide pieces are built
// once here: one HTTP explore-action executor, one bounded version-scoped
// session cache and one version fact service, so every explore caller shares
// the same cache-miss policy (unknown storage_state_ref -> fresh login) and the
// same executor-page cleanup (DELETE /v1/explore?page_id=<id>, scoped to the
// throwaway verification page so concurrent explore pages stay open).
//
// The CaseSuccessHook is intentionally not wired here: the knowledge adapter
// does not implement executionapp.CaseSuccessHook yet, and inventing one is not
// this task's scope. Task 51 owns that adapter + the KnowledgeHook injection;
// P7Deps.Retrieval is Task 51 as well.

import (
	"errors"
	"time"

	exploreapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application/explore"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/infrastructure/versionadapter"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionfactpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// p9RuntimeDeps are the already-constructed P0-P7 services the explore
// composition consumes. The account store is the real resource secret service
// (LoginFlow reads passwords only through it, never through a prompt/fact).
type p9RuntimeDeps struct {
	Pool          *pgxpool.Pool
	IDs           platformid.Generator
	Events        eventingapp.Publisher
	Accounts      exploreapp.AccountStore
	Versions      versionadapter.VersionService
	ExecutorURL   string
	ExecutorToken string
	Now           func() time.Time
}

// p9Runtime is the constructed explore runtime. Sessions is exposed so a
// future explore handler/tool can inspect or invalidate the same cache; Explore
// builds per-run LoginFlow instances over the shared collaborators.
type p9Runtime struct {
	Explore  *versionadapter.ExploreWiring
	Sessions *resourceapp.SessionStore
}

// newP9Runtime builds the explore runtime. Construction fails closed on a
// missing required collaborator instead of wiring a nil dependency that would
// only panic during an acceptance run.
func newP9Runtime(deps p9RuntimeDeps) (*p9Runtime, error) {
	if deps.Pool == nil {
		return nil, errors.New("p9 explore: nil database pool")
	}
	if deps.IDs == nil {
		return nil, errors.New("p9 explore: nil id generator")
	}
	if deps.Accounts == nil {
		return nil, errors.New("p9 explore: nil account store")
	}
	if deps.Versions == nil {
		return nil, errors.New("p9 explore: nil version binding reader")
	}
	sessions := resourceapp.NewSessionStore(resourceapp.DefaultSessionTTL)
	facts := versionapp.NewFactService(
		versionfactpg.NewFactRepository(deps.Pool, deps.IDs), deps.IDs, deps.Events)
	executor := exploreapp.NewHTTPExecutor(deps.ExecutorURL, deps.ExecutorToken, 0)
	wiring, err := versionadapter.NewExploreWiring(versionadapter.ExploreWiringDeps{
		Executor: executor,
		Accounts: deps.Accounts,
		Sessions: sessions,
		Facts:    facts,
		Versions: deps.Versions,
		Now:      deps.Now,
	})
	if err != nil {
		return nil, err
	}
	return &p9Runtime{Explore: wiring, Sessions: sessions}, nil
}

// p9ExploreRuntime is the process-wide explore assembly. The exploration
// worker/tool loops that consume it land with the Task 36/37 application
// packages; holding the constructed runtime at the composition root guarantees
// they share one executor client, one bounded session cache and one fact
// recorder rather than rebuilding a per-call cache that can never hit reuse.
var p9ExploreRuntime *p9Runtime
