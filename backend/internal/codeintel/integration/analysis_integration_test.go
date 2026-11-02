//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/jackc/pgx/v5/pgxpool"
)

// integrationExtractor is the stub LLM used by integration tests (real LLM is
// reserved for the acceptance suite; spec §2.4).
type integrationExtractor struct {
	mu      sync.Mutex
	outputs map[string]codeintelapp.FactExtractOutput
	err     error
	calls   []string
	model   string
}

func (e *integrationExtractor) Extract(_ context.Context, in codeintelapp.FactExtractInput) (codeintelapp.FactExtractOutput, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls = append(e.calls, in.Path)
	if e.err != nil {
		return codeintelapp.FactExtractOutput{}, e.err
	}
	return e.outputs[in.Path], nil
}

func (e *integrationExtractor) Impact(_ context.Context, _ codeintelapp.ImpactInferenceInput) (codeintelapp.ImpactInferenceOutput, error) {
	return codeintelapp.ImpactInferenceOutput{}, nil
}

func (e *integrationExtractor) Model(_ context.Context) (string, error) { return e.model, nil }

func (e *integrationExtractor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

type analysisIntegrationFixture struct {
	service   *codeintelapp.AnalysisService
	analyses  *codeintelpg.AnalysisRepository
	facts     *codeintelpg.FactRepository
	entities  *codeintelpg.EntityRepository
	versions  *integrationVersions
	extractor *integrationExtractor
	queue     *integrationQueue
	events    *integrationEvents
	settings  *integrationSettings
}

func newAnalysisIntegrationFixture(t *testing.T, pool *pgxpool.Pool, repoPath string) *analysisIntegrationFixture {
	t.Helper()
	ids, err := platformid.NewSnowflake(2)
	if err != nil {
		t.Fatalf("snowflake: %v", err)
	}
	git := gitx.New(nil, 10*time.Second)
	versions := &integrationVersions{bindings: []codeintelapp.VersionSUTBinding{{
		VersionSUTID: 11, VersionID: 1, RepoID: 10, Branch: "main",
	}}}
	resources := &integrationResources{repo: codeintelapp.RepoRef{ID: 10, Name: "fixture", Path: repoPath, Branch: "main"}}
	extractor := &integrationExtractor{
		model: "stub-model",
		outputs: map[string]codeintelapp.FactExtractOutput{
			"src/routes.ts": {
				Facts: []codeintelapp.FactDraft{{
					Kind: codeintel.EntityRoute, Name: "/orders", Signature: "orders",
					StartLine: 1, EndLine: 2, Confidence: codeintel.ConfidenceHigh,
					Body:     json.RawMessage(`{"route":"/orders"}`),
					Evidence: []codeintelapp.EvidenceDraft{{File: "src/routes.ts", StartLine: 1, EndLine: 2}},
				}},
			},
			"src/new.ts": {
				Facts: []codeintelapp.FactDraft{{
					Kind: codeintel.EntitySymbol, Name: "added", Signature: "added",
					StartLine: 1, EndLine: 1, Confidence: codeintel.ConfidenceMedium,
					Evidence: []codeintelapp.EvidenceDraft{{File: "src/new.ts", StartLine: 1, EndLine: 1}},
				}},
			},
		},
	}
	queue := &integrationQueue{}
	events := &integrationEvents{}
	settings := &integrationSettings{
		ints: map[string]int64{
			"codeintel.max_changed_files":        20,
			"codeintel.max_file_bytes":           65536,
			"codeintel.max_excerpt_lines":        40,
			"codeintel.analysis_timeout_seconds": 120,
		},
		strings: map[string]string{"codeintel.prompt_version": "p5.1"},
	}
	analyses := codeintelpg.NewAnalysisRepository(pool)
	facts := codeintelpg.NewFactRepository(pool, ids)
	entities := codeintelpg.NewEntityRepository(pool, ids)
	service := codeintelapp.NewAnalysisService(codeintelapp.AnalysisDeps{
		Analyses: analyses, Facts: facts, Entities: entities, Git: git,
		Extractor: extractor, Models: extractor, Bulk: git,
		Versions: versions, Resources: resources, Queue: queue, Events: events,
		Audit: integrationAudit{}, Settings: settings,
		Clock: integrationClock{now: time.Now()}, IDs: ids,
	})
	return &analysisIntegrationFixture{
		service: service, analyses: analyses, facts: facts, entities: entities,
		versions: versions, extractor: extractor, queue: queue, events: events, settings: settings,
	}
}

func countRows(t *testing.T, pool *pgxpool.Pool, table string) int {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM `+table).Scan(&count); err != nil {
		t.Fatalf("count %s: %v", table, err)
	}
	return count
}

func TestAnalysisIntegrationT1FactsAndCache(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := newIntegrationRepo(t)

	// Seed the version baseline through a real T0 index of commit A.
	indexFixture := newIndexIntegrationFixture(t, pool, repo.dir, 5000)
	indexRun, _, err := indexFixture.service.Start(context.Background(), 1, 10, repo.base)
	if err != nil {
		t.Fatalf("index Start: %v", err)
	}
	if err := indexFixture.service.Execute(context.Background(), indexRun.ID); err != nil {
		t.Fatalf("index Execute: %v", err)
	}

	analysisFixture := newAnalysisIntegrationFixture(t, pool, repo.dir)
	// Share the baseline resolved by the index backfill.
	analysisFixture.versions.bindings = indexFixture.versions.bindings
	ctx := context.Background()

	analysis, created, err := analysisFixture.service.Start(ctx, 1, 10, repo.head, false)
	if err != nil {
		t.Fatalf("analysis Start: %v", err)
	}
	if !created || analysis.Status != codeintel.AnalysisQueued {
		t.Fatalf("analysis = %+v created=%v", analysis, created)
	}
	if len(analysisFixture.queue.ensured) != 1 || analysisFixture.queue.ensured[0].Type != "code.analyze" {
		t.Fatalf("work items = %+v", analysisFixture.queue.ensured)
	}
	if err := analysisFixture.service.Execute(ctx, analysis.ID); err != nil {
		t.Fatalf("analysis Execute: %v", err)
	}
	stored, err := analysisFixture.analyses.Get(ctx, analysis.ID)
	if err != nil {
		t.Fatalf("analysis Get: %v", err)
	}
	if stored.Status != codeintel.AnalysisDone || stored.Degraded {
		t.Fatalf("stored analysis = %+v", stored)
	}
	if count := countRows(t, pool, "code_fact"); count != 2 {
		t.Fatalf("fact rows = %d, want 2", count)
	}
	if count := countRows(t, pool, "code_entity"); count == 0 {
		t.Fatal("no code_entity projection rows")
	}
	var factVersions map[string]int
	if err := json.Unmarshal(stored.FactVersions, &factVersions); err != nil {
		t.Fatalf("fact versions: %v (%s)", err, stored.FactVersions)
	}
	if factVersions["route:/orders"] != 1 || factVersions["symbol:src/new.ts#added"] != 1 {
		t.Fatalf("fact version snapshot = %+v", factVersions)
	}
	var evidence []map[string]any
	var evidenceJSON []byte
	if err := pool.QueryRow(ctx, `SELECT evidence FROM code_fact WHERE fact_key = 'route:/orders'`).Scan(&evidenceJSON); err != nil {
		t.Fatalf("load route evidence: %v", err)
	}
	if err := json.Unmarshal(evidenceJSON, &evidence); err != nil || len(evidence) != 1 {
		t.Fatalf("route evidence = %s (%v)", evidenceJSON, err)
	}
	if hash, _ := evidence[0]["excerpt_hash"].(string); len(hash) != len("sha256:")+64 {
		t.Fatalf("excerpt hash = %q", hash)
	}

	// A forced rerun of the same range must hit the file-level fact cache and
	// not call the extractor or insert new fact versions.
	callsBefore := analysisFixture.extractor.callCount()
	second, created, err := analysisFixture.service.Start(ctx, 1, 10, repo.head, true)
	if err != nil || !created {
		t.Fatalf("second Start = %+v created=%v err=%v", second, created, err)
	}
	if second.AnalysisVersion != 2 {
		t.Fatalf("analysis version = %d", second.AnalysisVersion)
	}
	if err := analysisFixture.service.Execute(ctx, second.ID); err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if calls := analysisFixture.extractor.callCount(); calls != callsBefore {
		t.Fatalf("cache hit called extractor: %d -> %d", callsBefore, calls)
	}
	if count := countRows(t, pool, "code_fact"); count != 2 {
		t.Fatalf("fact rows after cache = %d", count)
	}
	secondStored, err := analysisFixture.analyses.Get(ctx, second.ID)
	if err != nil {
		t.Fatalf("second Get: %v", err)
	}
	var stats map[string]any
	if err := json.Unmarshal(secondStored.Stats, &stats); err != nil {
		t.Fatalf("stats: %v (%s)", err, secondStored.Stats)
	}
	if stats["cacheHits"] != float64(2) {
		t.Fatalf("stats = %+v", stats)
	}

	// A changed body with a new prompt version must create fact version 2 and
	// supersede version 1 (spec §5.2).
	analysisFixture.extractor.mu.Lock()
	analysisFixture.extractor.outputs["src/routes.ts"] = codeintelapp.FactExtractOutput{
		Facts: []codeintelapp.FactDraft{{
			Kind: codeintel.EntityRoute, Name: "/orders", Signature: "orders",
			StartLine: 1, EndLine: 2, Confidence: codeintel.ConfidenceHigh,
			Body:     json.RawMessage(`{"route":"/orders","revised":true}`),
			Evidence: []codeintelapp.EvidenceDraft{{File: "src/routes.ts", StartLine: 1, EndLine: 2}},
		}},
	}
	analysisFixture.extractor.mu.Unlock()
	analysisFixture.settings.strings["codeintel.prompt_version"] = "p5.2"
	third, created, err := analysisFixture.service.Start(ctx, 1, 10, repo.head, true)
	if err != nil || !created {
		t.Fatalf("third Start = %+v created=%v err=%v", third, created, err)
	}
	if err := analysisFixture.service.Execute(ctx, third.ID); err != nil {
		t.Fatalf("third Execute: %v", err)
	}
	var versions, superseded, latestVersion int
	if err := pool.QueryRow(ctx, `
		SELECT count(*), count(*) FILTER (WHERE superseded_at IS NOT NULL), max(version)
		FROM code_fact WHERE fact_key = 'route:/orders'
	`).Scan(&versions, &superseded, &latestVersion); err != nil {
		t.Fatalf("fact version stats: %v", err)
	}
	if versions != 2 || superseded != 1 || latestVersion != 2 {
		t.Fatalf("fact versions = %d/%d/%d", versions, superseded, latestVersion)
	}
	thirdStored, err := analysisFixture.analyses.Get(ctx, third.ID)
	if err != nil {
		t.Fatalf("third Get: %v", err)
	}
	var thirdVersions map[string]int
	if err := json.Unmarshal(thirdStored.FactVersions, &thirdVersions); err != nil {
		t.Fatalf("third fact versions: %v", err)
	}
	if thirdVersions["route:/orders"] != 2 {
		t.Fatalf("third fact version snapshot = %+v", thirdVersions)
	}
}

func TestAnalysisIntegrationDegraded(t *testing.T) {
	pool := testutil.NewDB(t)
	repo := newIntegrationRepo(t)

	indexFixture := newIndexIntegrationFixture(t, pool, repo.dir, 5000)
	indexRun, _, err := indexFixture.service.Start(context.Background(), 1, 10, repo.base)
	if err != nil {
		t.Fatalf("index Start: %v", err)
	}
	if err := indexFixture.service.Execute(context.Background(), indexRun.ID); err != nil {
		t.Fatalf("index Execute: %v", err)
	}

	analysisFixture := newAnalysisIntegrationFixture(t, pool, repo.dir)
	analysisFixture.versions.bindings = indexFixture.versions.bindings
	analysisFixture.extractor.err = errors.New("upstream unavailable")
	ctx := context.Background()
	analysis, _, err := analysisFixture.service.Start(ctx, 1, 10, repo.head, false)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := analysisFixture.service.Execute(ctx, analysis.ID); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored, err := analysisFixture.analyses.Get(ctx, analysis.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if stored.Status != codeintel.AnalysisDone || !stored.Degraded {
		t.Fatalf("degraded analysis = %+v", stored)
	}
	if count := countRows(t, pool, "code_fact"); count != 0 {
		t.Fatalf("degraded run wrote facts: %d", count)
	}
	var unmapped []map[string]any
	if err := json.Unmarshal(stored.Unmapped, &unmapped); err != nil || len(unmapped) == 0 {
		t.Fatalf("unmapped = %s (%v)", stored.Unmapped, err)
	}
	hasAIUnavailable := false
	for _, entry := range unmapped {
		if entry["reason"] == "ai_unavailable" {
			hasAIUnavailable = true
		}
	}
	if !hasAIUnavailable {
		t.Fatalf("unmapped has no ai_unavailable entry: %+v", unmapped)
	}
}
