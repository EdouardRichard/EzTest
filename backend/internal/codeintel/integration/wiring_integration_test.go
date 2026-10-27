//go:build integration

package integration

// This file exercises the P4 composition root end to end against the real
// PostgreSQL repositories: the nine codeintel repositories, the cross-context
// adapters (P2 resource catalog/index state, P3 version catalog, P0 eventing and
// audit), the codeintel worker with its startup recovery, the HTTP handler
// group and the server route mounting. No LLM is configured, matching a
// deployment without a default model: analyses must degrade, not fail startup.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	auditpostgres "github.com/EdouardRichard/EzTest/backend/internal/audit/infrastructure/postgres"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	codeintelaudit "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/audit"
	codeinteleventing "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/eventing"
	codeintelgitx "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	codeintelversion "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/version"
	codeintelhttp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/config"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	eventinginfra "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure"
	eventpostgres "github.com/EdouardRichard/EzTest/backend/internal/eventing/infrastructure/postgres"
	identityapp "github.com/EdouardRichard/EzTest/backend/internal/identity/application"
	identity "github.com/EdouardRichard/EzTest/backend/internal/identity/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	"github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/gitexec"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/server"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

const (
	// wiringWorkerTimeout bounds one async run in the wiring fixture. The tests
	// never execute a real run, so a short budget keeps a stuck job from
	// delaying shutdown.
	wiringWorkerTimeout = 30 * time.Second
	// wiringPromptVersion is the design-spec §4.1 default prompt version.
	wiringPromptVersion = "p4.1"
)

// wiringClock is the production Clock shape used by the fixture.
type wiringClock struct{}

// Now returns the current time in UTC.
func (wiringClock) Now() time.Time { return time.Now().UTC() }

// wiringAuth authenticates one fixed bearer token so the wiring test can prove
// the global auth middleware still guards the codeintel routes.
type wiringAuth struct{ tokens map[string]identity.User }

func (a wiringAuth) Authenticate(_ context.Context, rawToken string) (identity.User, error) {
	user, ok := a.tokens[rawToken]
	if !ok {
		return identity.User{}, identity.ErrUnauthenticated
	}
	return user, nil
}

func (wiringAuth) Login(context.Context, string, string) (identityapp.LoginResult, error) {
	return identityapp.LoginResult{}, identity.ErrUnauthenticated
}

func (wiringAuth) Logout(context.Context, string) error { return nil }

// wiringRunner is the composition root's Runner adapter over the two async run
// services. It mirrors the adapter main.go registers with the worker.
type wiringRunner struct {
	index    *codeintelapp.IndexService
	analyses *codeintelapp.AnalysisService
}

func (r wiringRunner) RunIndex(ctx context.Context, runID int64) error {
	return r.index.Run(ctx, runID)
}

func (r wiringRunner) RunAnalysis(ctx context.Context, analysisID int64) error {
	return r.analyses.Run(ctx, analysisID)
}

var _ codeintelapp.Runner = wiringRunner{}

// wiringFixture is the assembled codeintel context the composition root wires.
type wiringFixture struct {
	worker  *codeintelapp.Worker
	handler *codeintelhttp.Handler
}

// newWiringFixture assembles the codeintel context exactly as cmd/eztest does:
// nine PostgreSQL repositories, the adapters over the P2/P3/P0 contexts, the
// worker with its runner and startup recoverer, the five application services
// and the HTTP handler group. The worker is not started so a test can seed
// leftover runs first; Stop is registered as cleanup.
func newWiringFixture(t *testing.T, pool *pgxpool.Pool) *wiringFixture {
	t.Helper()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	// P2 resource application services (read-only catalog + index state writer).
	codeRepoStore := resourcepg.NewCodeRepository(pool)
	codeRepoService := resourceapp.NewCodeRepoService(codeRepoStore, gitexec.NewValidator(), wiringClock{})
	catalogRepo := resourcepg.NewCatalogRepository(pool)
	catalogService := resourceapp.NewCatalogService(catalogRepo, catalogRepo, catalogRepo, catalogRepo, wiringClock{})

	// P3 version application services. Only their read projections are used by
	// the codeintel adapters, so the optional event/id dependencies stay nil
	// (the existing P3 integration fixtures wire them the same way).
	versionRepo := versionpg.NewVersionRepository(pool)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	versionService := versionapp.NewVersionService(versionRepo, nil, nil, nil)
	requirementService := versionapp.NewRequirementService(requirementRepo, versionRepo, nil)
	relationService := versionapp.NewRelationService(requirementRepo)

	// P0 eventing and audit application services.
	eventService := eventingapp.NewService(eventpostgres.NewEventStore(pool), eventinginfra.NewHub())
	auditService := auditapp.NewService(auditpostgres.NewRepository(pool))

	// One Snowflake node for the fixture (the integration seeds use node 3);
	// the worker jobs and every analysis share this generator, as in main.go.
	generator, err := id.NewSnowflake(13)
	if err != nil {
		t.Fatalf("create snowflake generator: %v", err)
	}

	// Cross-context codeintel adapters.
	repoCatalog := codeintelresource.NewCatalog(codeRepoService, catalogService)
	indexState := codeintelresource.NewIndexStateWriter(codeRepoService)
	versionCatalog := codeintelversion.NewCatalog(versionService, requirementService, relationService)
	notifier := codeinteleventing.NewPublisher(eventService)
	auditor := codeintelaudit.NewRecorder(auditService)

	// The read-only Git adapter. The tests never execute Git, but the service
	// constructors require the port.
	git := codeintelgitx.NewGitReader(codeintelgitx.GitConfig{CommandTimeout: 10 * time.Second})

	// The nine codeintel PostgreSQL repositories (T5).
	indexRuns := codeintelpg.NewIndexRunRepository(pool)
	entities := codeintelpg.NewEntityRepository(pool)
	changeSets := codeintelpg.NewChangeSetRepository(pool)
	analysesRepo := codeintelpg.NewAnalysisRepository(pool)
	candidates := codeintelpg.NewCandidateRepository(pool)
	facts := codeintelpg.NewFactVersionRepository(pool)
	caches := codeintelpg.NewCacheRepository(pool)
	edges := codeintelpg.NewEdgeRepository(pool)
	anchorsRepo := codeintelpg.NewAnchorRepository(pool)

	worker := codeintelapp.NewWorker(codeintelapp.WorkerConfig{
		QueueSize:   16,
		Concurrency: 1,
		Timeout:     wiringWorkerTimeout,
		Logger:      logger,
	})

	// No LLM configuration exists in the fixture: the nil completer is the
	// documented degraded mode (analyses still produce deterministic results).
	impact := codeintelapp.NewImpactService(codeintelapp.ImpactServiceDeps{
		LLM:             nil,
		Versions:        versionCatalog,
		AppModels:       repoCatalog,
		Anchors:         anchorsRepo,
		Edges:           edges,
		Git:             git,
		Repos:           repoCatalog,
		IDs:             generator,
		MaxRequirements: codeinteldomain.MaxRequirementsInPrompt,
		MaxFacts:        200,
		MaxPromptBytes:  64 << 10,
	})
	index := codeintelapp.NewIndexService(codeintelapp.IndexServiceDeps{
		Git:           git,
		Repos:         repoCatalog,
		IndexRuns:     indexRuns,
		Entities:      entities,
		ChangeSets:    changeSets,
		State:         indexState,
		Queue:         worker,
		Audit:         auditor,
		IDs:           generator,
		Clock:         wiringClock{},
		MaxIndexFiles: 5000,
	})
	analysis := codeintelapp.NewAnalysisService(codeintelapp.AnalysisServiceDeps{
		Git:        git,
		Repos:      repoCatalog,
		Versions:   versionCatalog,
		ChangeSets: changeSets,
		Entities:   entities,
		Edges:      edges,
		Facts:      facts,
		Cache:      caches,
		Analyses:   analysesRepo,
		Candidates: candidates,
		Anchors:    anchorsRepo,
		Queue:      worker,
		LLM:        nil,
		Planner:    impact,
		Notifier:   notifier,
		Audit:      auditor,
		IDs:        generator,
		Clock:      wiringClock{},
		Config: codeintelapp.AnalysisConfig{
			MaxChangedFiles: 20,
			MaxFileBytes:    65536,
			MaxExcerptLines: 40,
			MaxIndexFiles:   5000,
			PromptVersion:   wiringPromptVersion,
		},
	})
	query := codeintelapp.NewQueryService(codeintelapp.QueryServiceDeps{
		Git:        git,
		Repos:      repoCatalog,
		Entities:   entities,
		ChangeSets: changeSets,
		Versions:   versionCatalog,
		Audit:      auditor,
		Config: codeintelapp.QueryConfig{
			MaxTreeEntries:  codeinteldomain.MaxTreeEntriesPerPage,
			MaxReadLines:    codeinteldomain.MaxReadLines,
			MaxDiffBytes:    262144,
			MaxChangedFiles: codeinteldomain.MaxChangedFilesStored,
		},
	})
	anchor := codeintelapp.NewAnchorService(codeintelapp.AnchorServiceDeps{
		Repos:     repoCatalog,
		Versions:  versionCatalog,
		AppModels: repoCatalog,
		Git:       git,
		Anchors:   anchorsRepo,
		IDs:       generator,
		Clock:     wiringClock{},
	})

	// The services exist, so the worker can be bound to them (the
	// services↔worker cycle is broken by construction, design spec §3.6).
	worker.SetRunner(wiringRunner{index: index, analyses: analysis})
	worker.SetRecoverer(codeintelapp.StaleRunRecoverer{IndexRuns: indexRuns, Analyses: analysesRepo})

	fixture := &wiringFixture{
		worker: worker,
		handler: codeintelhttp.NewHandler(codeintelhttp.HandlerDeps{
			Index:    index,
			Query:    query,
			Analyses: analysis,
			Anchors:  anchor,
			Repos:    repoCatalog,
		}),
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		worker.Stop(stopCtx)
	})
	return fixture
}

// TestCodeIntelWiringServesRepositoryList proves the assembled context starts
// its worker, mounts the codeintel routes behind the global auth and serves the
// live repository list with the real P2 catalog adapter.
func TestCodeIntelWiringServesRepositoryList(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repoID, versionID, _ := seedRepoVersionRequirement(t, pool)
	fixture := newWiringFixture(t, pool)
	fixture.worker.Start()

	user := identity.User{ID: 2, Username: "qa01", Role: identity.RoleUser, Status: identity.StatusActive}
	serverHandler := server.New(config.Config{Env: "test"}, slog.New(slog.NewTextHandler(io.Discard, nil)),
		server.Dependencies{
			Auth:      wiringAuth{tokens: map[string]identity.User{"ezt_user": user}},
			CodeIntel: fixture.handler,
		})

	// Anonymous callers never reach the handler.
	rec := httptest.NewRecorder()
	serverHandler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/code/repos", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous GET /api/code/repos=%d body=%s want 401", rec.Code, rec.Body)
	}

	// An authenticated request reaches the real handler and the real catalog.
	req := httptest.NewRequest(http.MethodGet, "/api/code/repos", nil)
	req.Header.Set("Authorization", "Bearer ezt_user")
	rec = httptest.NewRecorder()
	serverHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("authenticated GET /api/code/repos=%d body=%s want 200", rec.Code, rec.Body)
	}
	var listBody struct {
		Data []struct {
			ID    int64  `json:"id"`
			Key   string `json:"key"`
			Index any    `json:"index"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode repository list: %v body=%s", err, rec.Body)
	}
	if len(listBody.Data) != 1 || listBody.Data[0].ID != repoID {
		t.Fatalf("repository list=%+v want the seeded repo %d", listBody.Data, repoID)
	}
	if listBody.Data[0].Key == "" {
		t.Fatal("repository list carries an empty key")
	}
	if listBody.Data[0].Index != nil {
		t.Fatalf("never-indexed repository index=%v want null", listBody.Data[0].Index)
	}

	// The version-scoped surface is mounted too: the seeded version has no
	// analysis, so the real service answers the documented 404 envelope instead
	// of the mux's plain-text 404.
	req = httptest.NewRequest(http.MethodGet,
		"/api/versions/"+strconv.FormatInt(versionID, 10)+"/code/analyses/latest", nil)
	req.Header.Set("Authorization", "Bearer ezt_user")
	rec = httptest.NewRecorder()
	serverHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"not_found"`) {
		t.Fatalf("latest analysis=%d body=%s want the handler's 404 envelope", rec.Code, rec.Body)
	}

	// The worker stops accepting work once stopped (graceful shutdown order:
	// HTTP server first, then the worker, then the database).
	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture.worker.Stop(stopCtx)
	if err := fixture.worker.Enqueue(codeintelapp.Job{Kind: codeintelapp.JobKindIndex, ID: 1}); !errors.Is(err, codeintelapp.ErrWorkerStopped) {
		t.Fatalf("Enqueue after Stop=%v want ErrWorkerStopped", err)
	}
}

// TestCodeIntelWiringRecoversStaleRuns proves the worker's startup recovery
// marks leftover queued/running runs failed(interrupted) while terminal runs
// stay untouched (design spec §6.8).
func TestCodeIntelWiringRecoversStaleRuns(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	repoID, versionID, _ := seedRepoVersionRequirement(t, pool)
	fixture := newWiringFixture(t, pool)

	ctx := context.Background()
	stale := time.Now().UTC().Add(-time.Hour)

	queuedIndexID, runningIndexID, doneIndexID := nextSeedID(), nextSeedID(), nextSeedID()
	for _, row := range []struct {
		id        int64
		status    string
		startedAt *time.Time
	}{
		{queuedIndexID, "queued", nil},
		{runningIndexID, "running", &stale},
		{doneIndexID, "done", &stale},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO code_index_runs (id, repo_id, commit_sha, status, created_by, created_at, started_at)
			VALUES ($1, $2, $3, $4, 1, $5, $6)`,
			row.id, repoID, seedHeadCommit, row.status, stale, row.startedAt,
		); err != nil {
			t.Fatalf("seed index run %d: %v", row.id, err)
		}
	}

	queuedAnalysisID, runningAnalysisID, doneAnalysisID := nextSeedID(), nextSeedID(), nextSeedID()
	for index, row := range []struct {
		id        int64
		status    string
		startedAt *time.Time
	}{
		{queuedAnalysisID, "queued", nil},
		{runningAnalysisID, "running", &stale},
		{doneAnalysisID, "done", &stale},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO code_analyses
				(id, version_id, repo_id, base_commit, head_commit, status, prompt_version,
				 analysis_version, created_by, created_at, started_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 1, $9, $10)`,
			row.id, versionID, repoID, seedBaseCommit, seedHeadCommit, row.status,
			wiringPromptVersion, index+1, stale, row.startedAt,
		); err != nil {
			t.Fatalf("seed analysis %d: %v", row.id, err)
		}
	}

	// Recovery runs synchronously inside Start, before any job is served.
	fixture.worker.Start()

	for table, ids := range map[string][3]int64{
		"code_index_runs": {queuedIndexID, runningIndexID, doneIndexID},
		"code_analyses":   {queuedAnalysisID, runningAnalysisID, doneAnalysisID},
	} {
		for index, id := range ids {
			var status, runErrors string
			if err := pool.QueryRow(ctx,
				`SELECT status, errors::text FROM `+table+` WHERE id = $1`, id,
			).Scan(&status, &runErrors); err != nil {
				t.Fatalf("read %s row %d: %v", table, id, err)
			}
			want := "failed"
			if index == 2 {
				want = "done"
			}
			if status != want {
				t.Fatalf("%s row %d status=%q want %q (errors=%s)", table, id, status, want, runErrors)
			}
			hasInterrupted := strings.Contains(runErrors, `"interrupted"`)
			if index < 2 && !hasInterrupted {
				t.Fatalf("%s row %d errors=%s want the interrupted reason", table, id, runErrors)
			}
			if index == 2 && hasInterrupted {
				t.Fatalf("%s row %d terminal run was modified: errors=%s", table, id, runErrors)
			}
		}
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fixture.worker.Stop(stopCtx)
}
