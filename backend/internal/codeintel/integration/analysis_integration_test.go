//go:build integration

package integration

// This file wires the real analysis service (gitx + PostgreSQL repositories +
// real P2/P3 catalogs) against a temporary dual-commit Git repository and a
// deterministic fake LLM to prove the design spec §6.2–§6.6 analysis pipeline
// end to end: change set persistence, T1 facts/edges/fact versions, the
// file-level cache across versions, forced analysis versioning and the degraded
// LLM path. The unit-level rules live in codeintel/application.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/gitx"
	codeintelpg "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/postgres"
	codeintelresource "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/resource"
	codeintelversion "github.com/EdouardRichard/EzTest/backend/internal/codeintel/infrastructure/version"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	resourcepg "github.com/EdouardRichard/EzTest/backend/internal/resource/infrastructure/postgres"
	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionpg "github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// Files of the temporary analysis repository.
const (
	analysisClientPath = "web/src/api/refund.ts"
	analysisServerPath = "server/refund.go"
	analysisUtilsPath  = "web/src/utils.ts"
	analysisLegacyPath = "web/src/legacy.ts"
)

// analysisBaseFiles and analysisHeadFiles are the two fixture commits. The head
// content keeps the evidence line ranges of the fake LLM responses valid.
var analysisBaseFiles = map[string]string{
	analysisClientPath: "import { post } from './http';\n" +
		"export function refund(orderId: string) {\n" +
		"  return post('/api/refund', { orderId });\n" +
		"}\n",
	analysisServerPath: "package server\n" +
		"\n" +
		"func Refund() {}\n",
	analysisUtilsPath:  "export const format = (value: string) => value;\n",
	analysisLegacyPath: "export const legacy = true;\n",
}

var analysisHeadFiles = map[string]string{
	analysisClientPath: "import { post } from './http';\n" +
		"export function refund(orderId: string, reason: string) {\n" +
		"  return post('/api/refund', { orderId, reason });\n" +
		"}\n",
	analysisServerPath: "package server\n" +
		"\n" +
		"func Refund(reason string) {}\n",
	analysisUtilsPath: "export const format = (value: string) => value.trim();\n" +
		"export const normalize = (value: string) => value;\n",
}

// newAnalysisGitRepo builds the deterministic dual-commit repository and
// returns its directory and both valid 40-hex commits.
func newAnalysisGitRepo(t *testing.T) (dir, base, head string) {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not available")
	}
	dir = t.TempDir()
	run := func(args ...string) string {
		t.Helper()
		command := exec.Command(gitPath, append([]string{"-C", dir}, args...)...)
		command.Env = append(os.Environ(),
			"GIT_OPTIONAL_LOCKS=0",
			"GIT_AUTHOR_NAME=EzTest Analysis",
			"GIT_AUTHOR_EMAIL=analysis@example.com",
			"GIT_COMMITTER_NAME=EzTest Analysis",
			"GIT_COMMITTER_EMAIL=analysis@example.com",
			"GIT_AUTHOR_DATE=2026-02-01T00:00:00+0000",
			"GIT_COMMITTER_DATE=2026-02-01T00:00:00+0000",
		)
		out, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	writeFiles := func(files map[string]string) {
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			absolute := filepath.Join(dir, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
				t.Fatalf("mkdir for %s: %v", name, err)
			}
			if err := os.WriteFile(absolute, []byte(files[name]), 0o644); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}

	run("init", "-q", "-b", "main")
	run("config", "user.email", "analysis@example.com")
	run("config", "user.name", "EzTest Analysis")
	run("config", "commit.gpgsign", "false")
	run("config", "core.autocrlf", "false")

	writeFiles(analysisBaseFiles)
	run("add", "-A")
	run("commit", "-q", "-m", "analysis fixture base")
	base = run("rev-parse", "HEAD")

	writeFiles(analysisHeadFiles)
	if err := os.Remove(filepath.Join(dir, filepath.FromSlash(analysisLegacyPath))); err != nil {
		t.Fatalf("delete %s: %v", analysisLegacyPath, err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "analysis fixture head")
	head = run("rev-parse", "HEAD")
	if len(base) != 40 || len(head) != 40 || base == head {
		t.Fatalf("fixture commits = %q/%q, want two distinct 40-hex commits", base, head)
	}
	return dir, base, head
}

// seedAnalysisRepoVersion builds on the T5 seed helper: it points the seeded
// repository at dir and the seeded version at base, so the P2/P3 rows stay
// shared with the rest of the integration suite.
func seedAnalysisRepoVersion(t *testing.T, pool *pgxpool.Pool, dir, base string) (repoID, versionID, requirementID int64) {
	t.Helper()
	repoID, versionID, requirementID = seedRepoVersionRequirement(t, pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx,
		`UPDATE code_repositories SET local_path = $1 WHERE id = $2`, dir, repoID); err != nil {
		t.Fatalf("point code repository at the fixture: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE versions SET code_branch = 'main', code_baseline_commit = $1 WHERE id = $2`,
		base, versionID); err != nil {
		t.Fatalf("point version at the fixture baseline: %v", err)
	}
	return repoID, versionID, requirementID
}

// seedAnalysisVersion inserts one extra version bound to repoID at base.
func seedAnalysisVersion(t *testing.T, pool *pgxpool.Pool, repoID int64, base string) int64 {
	t.Helper()
	versionID := nextSeedID()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO versions
			(id, name, status, code_repository_id, code_branch, code_baseline_commit, created_by, updated_by)
		 VALUES ($1, $2, 'draft', $3, 'main', $4, 1, 1)`,
		versionID, fmt.Sprintf("P4 analysis version %d", versionID), repoID, base,
	); err != nil {
		t.Fatalf("seed analysis version: %v", err)
	}
	return versionID
}

// analysisLLMPath extracts the path line of the T1 user prompt.
func analysisLLMPath(user string) string {
	for _, line := range strings.Split(user, "\n") {
		if strings.HasPrefix(line, "文件：") {
			return strings.TrimSpace(strings.TrimPrefix(line, "文件："))
		}
	}
	return ""
}

// analysisFakeLLM is the deterministic T1 completer. Its responses keep the
// evidence line ranges inside analysisHeadFiles.
type analysisFakeLLM struct {
	mu     sync.Mutex
	model  string
	calls  int
	err    error
	byPath map[string]string
	users  []string
}

func newAnalysisFakeLLM() *analysisFakeLLM {
	return &analysisFakeLLM{
		model: "fake-analysis-model",
		byPath: map[string]string{
			analysisClientPath: `{
				"changeSummary": "refund client call adds a reason parameter",
				"facts": [{
					"kind": "client_call",
					"name": "POST /api/refund",
					"path": "web/src/api/refund.ts",
					"startLine": 1,
					"endLine": 3,
					"signature": "post('/api/refund')",
					"confidence": "high",
					"summary": "client calls the refund API",
					"evidence": [{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3}]
				}],
				"edges": []
			}`,
			analysisServerPath: `{
				"changeSummary": "refund endpoint adds a reason parameter",
				"facts": [{
					"kind": "api",
					"name": "POST /api/refund",
					"path": "server/refund.go",
					"startLine": 1,
					"endLine": 3,
					"signature": "func Refund(reason string)",
					"confidence": "high",
					"summary": "server serves the refund API",
					"evidence": [{"file": "server/refund.go", "startLine": 1, "endLine": 3}]
				}],
				"edges": []
			}`,
			analysisUtilsPath: `{
				"changeSummary": "format and normalize helpers updated",
				"facts": [],
				"edges": []
			}`,
		},
	}
}

func (l *analysisFakeLLM) Complete(_ context.Context, system, user string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.users = append(l.users, user)
	if l.err != nil {
		return "", l.err
	}
	response, ok := l.byPath[analysisLLMPath(user)]
	if !ok {
		return `{"changeSummary": "no facts", "facts": [], "edges": []}`, nil
	}
	return response, nil
}

func (l *analysisFakeLLM) Model() string { return l.model }

// setResponse replaces one path's deterministic T1 response.
func (l *analysisFakeLLM) setResponse(path, response string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.byPath[path] = response
}

func (l *analysisFakeLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// analysisFakePlanner is a deterministic T0-only impact planner stub; the real
// ImpactService is T13's task.
type analysisFakePlanner struct {
	mu            sync.Mutex
	requirementID int64
	calls         []codeintelapp.ImpactInput
}

func (p *analysisFakePlanner) Plan(_ context.Context, in codeintelapp.ImpactInput) (codeintelapp.ImpactResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, in)
	if p.requirementID == 0 || len(in.ChangedFiles) == 0 {
		return codeintelapp.ImpactResult{Stats: []byte(`{"requirements":0,"appModels":0}`)}, nil
	}
	changed := ""
	for _, file := range in.ChangedFiles {
		if file.Status != "D" {
			changed = file.Path
			break
		}
	}
	candidates := []domain.ImpactCandidate{}
	if changed != "" {
		candidates = append(candidates, domain.ImpactCandidate{
			SubjectKind: domain.CandidateRequirement,
			SubjectID:   p.requirementID,
			Reason:      "changed file " + changed,
			Confidence:  domain.ConfidenceHigh,
			Source:      domain.CandidateDirect,
			Evidence:    []domain.Evidence{{File: changed}},
		})
	}
	return codeintelapp.ImpactResult{
		Candidates: candidates,
		Unmapped: []domain.UnmappedChange{{
			Path: analysisLegacyPath, Status: "D", Reason: domain.UnmappedDeleted,
			Suggestion: "人工确认删除影响",
		}},
		Stats: []byte(`{"requirements":1,"appModels":0}`),
	}, nil
}

func (p *analysisFakePlanner) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.calls)
}

func (p *analysisFakePlanner) lastInput() (codeintelapp.ImpactInput, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.calls) == 0 {
		return codeintelapp.ImpactInput{}, false
	}
	return p.calls[len(p.calls)-1], true
}

// analysisFakeQueue captures enqueued jobs.
type analysisFakeQueue struct {
	mu   sync.Mutex
	jobs []codeintelapp.Job
}

func (q *analysisFakeQueue) Enqueue(job codeintelapp.Job) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.jobs = append(q.jobs, job)
	return nil
}

func (q *analysisFakeQueue) len() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.jobs)
}

// analysisFakeNotifier captures the two version-scoped events.
type analysisFakeNotifier struct {
	mu      sync.Mutex
	changed []codeintelapp.ChangedEvent
	impacts []codeintelapp.ImpactEvent
}

func (n *analysisFakeNotifier) Changed(_ context.Context, _ int64, event codeintelapp.ChangedEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.changed = append(n.changed, event)
	return nil
}

func (n *analysisFakeNotifier) ImpactComputed(_ context.Context, _ int64, event codeintelapp.ImpactEvent) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.impacts = append(n.impacts, event)
	return nil
}

func (n *analysisFakeNotifier) impactCount() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.impacts)
}

// analysisFakeAudit captures the code.analyze entries.
type analysisFakeAudit struct {
	mu    sync.Mutex
	calls []string
}

func (a *analysisFakeAudit) Record(_ context.Context, _ int64, action, _ string, _ map[string]any) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.calls = append(a.calls, action)
	return nil
}

func (a *analysisFakeAudit) actions() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.calls...)
}

// analysisIntegration is one wired analysis service plus its test doubles.
type analysisIntegration struct {
	service  *codeintelapp.AnalysisService
	llm      *analysisFakeLLM
	planner  *analysisFakePlanner
	queue    *analysisFakeQueue
	notifier *analysisFakeNotifier
	audit    *analysisFakeAudit
}

// newAnalysisServiceForIntegration assembles the production analysis service
// over the real gitx/PostgreSQL adapters and the real P2/P3 catalogs.
func newAnalysisServiceForIntegration(t *testing.T, pool *pgxpool.Pool, llm *analysisFakeLLM, promptVersion string) *analysisIntegration {
	t.Helper()
	repoCatalog := codeintelresource.NewCatalog(
		resourceapp.NewCodeRepoService(resourcepg.NewCodeRepository(pool), nil, nil))

	versionRepo := versionpg.NewVersionRepository(pool)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	versionService := versionapp.NewVersionService(versionRepo, nil, nil, nil)
	requirementService := versionapp.NewRequirementService(requirementRepo, versionRepo, nil)
	relationService := versionapp.NewRelationService(requirementRepo)
	versionCatalog := codeintelversion.NewCatalog(versionService, requirementService, relationService)

	generator, err := id.NewSnowflake(11)
	if err != nil {
		t.Fatalf("new analysis snowflake generator: %v", err)
	}
	queue := &analysisFakeQueue{}
	planner := &analysisFakePlanner{}
	notifier := &analysisFakeNotifier{}
	audit := &analysisFakeAudit{}

	service := codeintelapp.NewAnalysisService(codeintelapp.AnalysisServiceDeps{
		Git:        gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second}),
		Repos:      repoCatalog,
		Versions:   versionCatalog,
		ChangeSets: codeintelpg.NewChangeSetRepository(pool),
		Entities:   codeintelpg.NewEntityRepository(pool),
		Edges:      codeintelpg.NewEdgeRepository(pool),
		Facts:      codeintelpg.NewFactVersionRepository(pool),
		Cache:      codeintelpg.NewCacheRepository(pool),
		Analyses:   codeintelpg.NewAnalysisRepository(pool),
		Candidates: codeintelpg.NewCandidateRepository(pool),
		Anchors:    codeintelpg.NewAnchorRepository(pool),
		Queue:      queue,
		LLM:        llm,
		Planner:    planner,
		Notifier:   notifier,
		Audit:      audit,
		IDs:        generator,
		Clock:      indexIntegrationClock{},
		Config: codeintelapp.AnalysisConfig{
			MaxChangedFiles: 20,
			MaxFileBytes:    65536,
			MaxExcerptLines: 40,
			PromptVersion:   promptVersion,
		},
	})
	return &analysisIntegration{
		service: service, llm: llm, planner: planner,
		queue: queue, notifier: notifier, audit: audit,
	}
}

// analysisStatsIntegration is the JSON shape the analysis stats document uses.
type analysisStatsIntegration struct {
	ChangedFiles       int    `json:"changedFiles"`
	AnalyzedFiles      int    `json:"analyzedFiles"`
	CacheHits          int    `json:"cacheHits"`
	Skipped            int    `json:"skipped"`
	Facts              int    `json:"facts"`
	Edges              int    `json:"edges"`
	Contracts          int    `json:"contracts"`
	EvidenceChecks     int    `json:"evidenceChecks"`
	EvidenceFailures   int    `json:"evidenceFailures"`
	UnmatchedContracts int    `json:"unmatchedContracts"`
	UnresolvedEdges    int    `json:"unresolvedEdges"`
	Conflicts          int    `json:"conflicts"`
	Unmapped           int    `json:"unmapped"`
	ChangeSummary      string `json:"changeSummary"`
}

// analysisStoredStats decodes the persisted stats document.
func analysisStoredStats(t *testing.T, raw []byte) analysisStatsIntegration {
	t.Helper()
	var stats analysisStatsIntegration
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("decode analysis stats %q: %v", string(raw), err)
	}
	return stats
}

// runAnalysisJob starts and synchronously runs one analysis, returning the
// finished record.
func runAnalysisJob(t *testing.T, fixture *analysisIntegration, analyses *codeintelpg.AnalysisRepo, versionID int64, headCommit string, force bool) domain.CodeAnalysis {
	t.Helper()
	ctx := context.Background()
	analysis, reused, err := fixture.service.Start(ctx, versionID, 1, headCommit, force)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if reused {
		t.Fatalf("Start reused = true, want a new analysis")
	}
	if err := fixture.service.Run(ctx, analysis.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	stored, err := analyses.GetByID(ctx, versionID, analysis.ID)
	if err != nil {
		t.Fatalf("reload analysis: %v", err)
	}
	return stored
}

// TestAnalysisIntegrationMaterialisesFactsCacheAndVersions proves the full
// pipeline and the §6.4 cache/version contract across a second version and a
// forced re-run.
func TestAnalysisIntegrationMaterialisesFactsCacheAndVersions(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	fixture := newAnalysisServiceForIntegration(t, pool, newAnalysisFakeLLM(), "p4.1")
	fixture.planner.requirementID = requirementID
	analyses := codeintelpg.NewAnalysisRepository(pool)

	analysis, reused, err := fixture.service.Start(ctx, versionID, 1, head, false)
	if err != nil {
		t.Fatalf("Start error = %v", err)
	}
	if reused || analysis.Status != domain.RunQueued || analysis.AnalysisVersion != 1 {
		t.Fatalf("Start = %+v reused=%v, want a queued analysisVersion 1", analysis, reused)
	}
	if fixture.queue.len() != 1 {
		t.Fatalf("enqueued jobs = %d, want 1", fixture.queue.len())
	}
	if _, _, err := fixture.service.Start(ctx, versionID, 1, head, false); err == nil || domain.Reason(err) != "analysis_in_progress" {
		t.Fatalf("duplicate Start error = %v, want analysis_in_progress", err)
	}

	if err := fixture.service.Run(ctx, analysis.ID); err != nil {
		t.Fatalf("Run error = %v", err)
	}
	stored, err := analyses.GetByID(ctx, versionID, analysis.ID)
	if err != nil {
		t.Fatalf("reload analysis: %v", err)
	}
	if stored.Status != domain.RunDone || stored.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done and not degraded", stored.Status, stored.Degraded)
	}
	if stored.StartedAt == nil || stored.FinishedAt == nil {
		t.Fatalf("analysis timestamps = %v/%v, want both set", stored.StartedAt, stored.FinishedAt)
	}
	if stored.ChangeSetID == nil {
		t.Fatal("analysis changeSetId = nil, want the persisted change set")
	}
	stats := analysisStoredStats(t, stored.Stats)
	if stats.ChangedFiles != 4 || stats.AnalyzedFiles != 3 || stats.Facts != 2 || stats.Contracts != 1 ||
		stats.EvidenceFailures != 0 || stats.UnmatchedContracts != 0 {
		t.Fatalf("stats = %+v, want changed=4 analyzed=3 facts=2 contracts=1", stats)
	}
	if !strings.Contains(stats.ChangeSummary, "refund") {
		t.Fatalf("changeSummary = %q, want the aggregated file summaries", stats.ChangeSummary)
	}

	// Persisted change set: one deterministic row per (repo, base, head).
	var changedFilesJSON []byte
	var statsJSON []byte
	if err := pool.QueryRow(ctx,
		`SELECT changed_files, stats FROM code_change_sets
		 WHERE repo_id = $1 AND base_commit = $2 AND head_commit = $3`,
		repoID, base, head).Scan(&changedFilesJSON, &statsJSON); err != nil {
		t.Fatalf("read change set: %v", err)
	}
	var changedFiles []map[string]any
	if err := json.Unmarshal(changedFilesJSON, &changedFiles); err != nil {
		t.Fatalf("decode changed_files: %v", err)
	}
	if len(changedFiles) != 4 {
		t.Fatalf("changed files = %d, want 4", len(changedFiles))
	}

	// AI facts: one client_call and one api entity each with a verified excerpt.
	rows, err := pool.Query(ctx,
		`SELECT id, kind, name, source, confidence, fact_key, fact_version,
		        jsonb_array_length(evidence)
		 FROM code_entities
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key IS NOT NULL
		 ORDER BY fact_key`, repoID, head)
	if err != nil {
		t.Fatalf("query fact entities: %v", err)
	}
	defer rows.Close()
	type factRow struct {
		id         int64
		kind       string
		name       string
		source     string
		confidence string
		factKey    string
		factVer    int
		evidence   int
	}
	var facts []factRow
	for rows.Next() {
		var fact factRow
		if err := rows.Scan(&fact.id, &fact.kind, &fact.name, &fact.source,
			&fact.confidence, &fact.factKey, &fact.factVer, &fact.evidence); err != nil {
			t.Fatalf("scan fact entity: %v", err)
		}
		facts = append(facts, fact)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate fact entities: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("fact entities = %+v, want the client_call and api facts", facts)
	}
	wantKeys := map[string]string{
		"client:POST /api/refund": domain.KindClientCall,
		"api:POST /api/refund":    domain.KindAPI,
	}
	for _, fact := range facts {
		if wantKind, ok := wantKeys[fact.factKey]; !ok || fact.kind != wantKind {
			t.Fatalf("fact = %+v, want one of %v", fact, wantKeys)
		}
		if fact.id == 0 || fact.source != domain.SourceAI || fact.confidence != domain.ConfidenceHigh {
			t.Fatalf("fact provenance = %+v, want snowflake ai/high", fact)
		}
		if fact.factVer != 1 || fact.evidence != 1 {
			t.Fatalf("fact = %+v, want version 1 with one evidence entry", fact)
		}
	}

	// Fact-version rows: version 1, canonical body, not superseded.
	var factVersions int
	var superseded int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int, count(superseded_at)::int FROM code_fact_versions
		 WHERE repo_id = $1 AND commit_sha = $2`, repoID, head).Scan(&factVersions, &superseded); err != nil {
		t.Fatalf("count fact versions: %v", err)
	}
	if factVersions != 2 || superseded != 0 {
		t.Fatalf("fact versions = %d (superseded %d), want 2 current version-1 rows", factVersions, superseded)
	}
	var factBody []byte
	if err := pool.QueryRow(ctx,
		`SELECT body FROM code_fact_versions
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = 'client:POST /api/refund' AND version = 1`,
		repoID, head).Scan(&factBody); err != nil {
		t.Fatalf("read fact body: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(factBody, &body); err != nil {
		t.Fatalf("decode fact body: %v", err)
	}
	if body["name"] != "POST /api/refund" || body["confidence"] != "high" {
		t.Fatalf("fact body = %+v, want the validated fact", body)
	}

	// Contract edge route_to_api with the documented meta.
	var edgeCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = 'route_to_api'`,
		repoID, head).Scan(&edgeCount); err != nil {
		t.Fatalf("count contract edges: %v", err)
	}
	if edgeCount != 1 {
		t.Fatalf("contract edges = %d, want 1", edgeCount)
	}
	var edgeSrc, edgeDst int64
	var method, normalizedPath string
	if err := pool.QueryRow(ctx,
		`SELECT src_entity_id, dst_entity_id, meta ->> 'method', meta ->> 'normalizedPath'
		 FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = 'route_to_api'`,
		repoID, head).Scan(&edgeSrc, &edgeDst, &method, &normalizedPath); err != nil {
		t.Fatalf("read contract edge: %v", err)
	}
	if edgeSrc == 0 || edgeDst == 0 || edgeSrc == edgeDst || method != "POST" || normalizedPath != "/api/refund" {
		t.Fatalf("contract edge = %d->%d %s %s, want the client->api POST /api/refund edge",
			edgeSrc, edgeDst, method, normalizedPath)
	}

	// File-level cache rows for the three analysed files.
	var cacheRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_analysis_cache
		 WHERE repo_id = $1 AND commit_sha = $2 AND model = $3 AND prompt_version = $4`,
		repoID, head, "fake-analysis-model", "p4.1").Scan(&cacheRows); err != nil {
		t.Fatalf("count cache rows: %v", err)
	}
	if cacheRows != 3 {
		t.Fatalf("cache rows = %d, want 3", cacheRows)
	}

	// Planner handoff, events and audit.
	input, ok := fixture.planner.lastInput()
	if !ok || input.Degraded || len(input.FactsAtHead) != 2 || len(input.Edges) != 1 {
		t.Fatalf("planner input = %+v, want two facts, one contract edge and no degradation", input)
	}
	if fixture.notifier.impactCount() != 1 {
		t.Fatalf("impact events = %d, want 1", fixture.notifier.impactCount())
	}
	if actions := fixture.audit.actions(); len(actions) < 2 || actions[0] != "code.analyze" {
		t.Fatalf("audit actions = %v, want code.analyze trigger and completion", actions)
	}

	// Candidates persisted with a snowflake id; the old analysis remains.
	var candidateRows int
	var candidateSelected bool
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int, COALESCE(bool_and(selected), false)
		 FROM code_impact_candidates WHERE analysis_id = $1`, analysis.ID,
	).Scan(&candidateRows, &candidateSelected); err != nil {
		t.Fatalf("read candidates: %v", err)
	}
	if candidateRows != 1 || !candidateSelected {
		t.Fatalf("candidates = %d selected=%v, want one selected candidate", candidateRows, candidateSelected)
	}

	// Second version, same repo/base/head: every file is a cache hit and the
	// fake LLM is not called again.
	callsBefore := fixture.llm.callCount()
	version2 := seedAnalysisVersion(t, pool, repoID, base)
	second := runAnalysisJob(t, fixture, analyses, version2, head, false)
	secondStats := analysisStoredStats(t, second.Stats)
	if secondStats.CacheHits != 3 || secondStats.AnalyzedFiles != 3 {
		t.Fatalf("second analysis stats = %+v, want 3 cache hits", secondStats)
	}
	if got := fixture.llm.callCount(); got != callsBefore {
		t.Fatalf("LLM calls after the cached version = %d, want %d", got, callsBefore)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = 'route_to_api'`,
		repoID, head).Scan(&edgeCount); err != nil {
		t.Fatalf("count contract edges after the cached run: %v", err)
	}
	if edgeCount != 1 {
		t.Fatalf("contract edges after the cached run = %d, want 1 (replace, not duplicate)", edgeCount)
	}

	// Force: a new analysis version, the cache is skipped and the unchanged fact
	// bodies still reuse their version 1.
	forced := runAnalysisJob(t, fixture, analyses, versionID, head, true)
	if forced.AnalysisVersion != 2 {
		t.Fatalf("forced analysisVersion = %d, want 2", forced.AnalysisVersion)
	}
	forcedStats := analysisStoredStats(t, forced.Stats)
	if forcedStats.CacheHits != 0 {
		t.Fatalf("forced stats.cacheHits = %d, want 0 (force skips the cache)", forcedStats.CacheHits)
	}
	if got := fixture.llm.callCount(); got != callsBefore+3 {
		t.Fatalf("LLM calls after the forced run = %d, want %d", got, callsBefore+3)
	}
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_fact_versions
		 WHERE repo_id = $1 AND commit_sha = $2`, repoID, head).Scan(&factVersions); err != nil {
		t.Fatalf("count fact versions after force: %v", err)
	}
	if factVersions != 2 {
		t.Fatalf("fact versions after force = %d, want the same 2 version-1 rows", factVersions)
	}
	original, err := analyses.GetByID(ctx, versionID, analysis.ID)
	if err != nil {
		t.Fatalf("old analysis after force: %v", err)
	}
	if original.Status != domain.RunDone || original.AnalysisVersion != 1 {
		t.Fatalf("old analysis after force = %+v, want the untouched version 1", original)
	}

	// The second version's detail can be read back through the service.
	candidates, err := fixture.service.ListCandidates(ctx, second.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("ListCandidates(second) = %+v, %v; want one candidate", candidates, err)
	}
}

// TestAnalysisIntegrationDegraded proves the LLM-unavailable path persists
// done+degraded with the T0-only planner result and never writes AI facts.
func TestAnalysisIntegrationDegraded(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	llm := newAnalysisFakeLLM()
	llm.err = codeintelapp.ErrLLMNotConfigured
	fixture := newAnalysisServiceForIntegration(t, pool, llm, "p4.1")
	fixture.planner.requirementID = requirementID
	analyses := codeintelpg.NewAnalysisRepository(pool)

	stored := runAnalysisJob(t, fixture, analyses, versionID, head, false)
	if stored.Status != domain.RunDone || !stored.Degraded {
		t.Fatalf("analysis = status %q degraded %v, want done and degraded", stored.Status, stored.Degraded)
	}
	if !ciHasAnalysisError(stored.Errors, "llm_unavailable") {
		t.Fatalf("analysis errors = %+v, want llm_unavailable", stored.Errors)
	}
	stats := analysisStoredStats(t, stored.Stats)
	if stats.ChangedFiles != 4 || stats.AnalyzedFiles != 0 || stats.Facts != 0 {
		t.Fatalf("degraded stats = %+v, want changed=4 analyzed=0 facts=0", stats)
	}

	input, ok := fixture.planner.lastInput()
	if !ok || !input.Degraded {
		t.Fatalf("planner input = %+v, want Degraded=true", input)
	}
	if len(input.FactsAtHead) != 0 {
		t.Fatalf("planner facts = %+v, want none on a fresh degraded analysis", input.FactsAtHead)
	}

	var factEntities int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_entities
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key IS NOT NULL`,
		repoID, head).Scan(&factEntities); err != nil {
		t.Fatalf("count degraded fact entities: %v", err)
	}
	if factEntities != 0 {
		t.Fatalf("degraded fact entities = %d, want 0", factEntities)
	}
	var cacheRows int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_analysis_cache WHERE repo_id = $1 AND commit_sha = $2`,
		repoID, head).Scan(&cacheRows); err != nil {
		t.Fatalf("count degraded cache rows: %v", err)
	}
	if cacheRows != 0 {
		t.Fatalf("degraded cache rows = %d, want 0 (failed files are never cached)", cacheRows)
	}

	// The T0-only planner result is still persisted and the run keeps the
	// degraded flag through the read path.
	if len(stored.Unmapped) != 1 || stored.Unmapped[0].Reason != domain.UnmappedDeleted {
		t.Fatalf("degraded unmapped = %+v, want the planner result", stored.Unmapped)
	}
	candidates, err := fixture.service.ListCandidates(ctx, stored.ID)
	if err != nil || len(candidates) != 1 {
		t.Fatalf("ListCandidates = %+v, %v; want one T0 candidate", candidates, err)
	}
	var candidateCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_impact_candidates WHERE analysis_id = $1`,
		stored.ID).Scan(&candidateCount); err != nil {
		t.Fatalf("count degraded candidates: %v", err)
	}
	if candidateCount != 1 {
		t.Fatalf("degraded candidates = %d, want 1", candidateCount)
	}
}

// analysisEntityID reads the persisted entity id of one fact key.
func analysisEntityID(t *testing.T, pool *pgxpool.Pool, repoID int64, commit, factKey string) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`SELECT id FROM code_entities
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = $3`,
		repoID, commit, factKey).Scan(&id); err != nil {
		t.Fatalf("read entity %q: %v", factKey, err)
	}
	return id
}

// TestAnalysisIntegrationAIEdgesAndFactBump proves the §6.2 edge
// materialisation against the real repositories: a fact version bump keeps the
// entity row id, every code_edges endpoint references a persisted entity, an
// unresolved endpoint is counted, and the contract edge keeps both real
// endpoints.
func TestAnalysisIntegrationAIEdgesAndFactBump(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	llm := newAnalysisFakeLLM()
	fixture := newAnalysisServiceForIntegration(t, pool, llm, "p4.1")
	fixture.planner.requirementID = requirementID
	analyses := codeintelpg.NewAnalysisRepository(pool)

	runAnalysisJob(t, fixture, analyses, versionID, head, false)
	firstClientID := analysisEntityID(t, pool, repoID, head, "client:POST /api/refund")
	firstAPIID := analysisEntityID(t, pool, repoID, head, "api:POST /api/refund")
	if firstClientID == 0 || firstAPIID == 0 || firstClientID == firstAPIID {
		t.Fatalf("first-run entity ids = %d/%d, want two distinct persisted rows", firstClientID, firstAPIID)
	}

	// Bump the client fact body and add one resolvable AI edge (client → the
	// existing api fact) plus one edge with an unresolved endpoint.
	llm.setResponse(analysisClientPath, `{
		"changeSummary": "refund client call adds a reason parameter",
		"facts": [{
			"kind": "client_call",
			"name": "POST /api/refund",
			"path": "web/src/api/refund.ts",
			"startLine": 1,
			"endLine": 3,
			"signature": "post('/api/refund')",
			"confidence": "high",
			"summary": "client calls the refund API with a reason",
			"evidence": [{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3}]
		}],
		"edges": [
			{
				"from": {"kind": "client_call", "name": "POST /api/refund"},
				"to": {"kind": "api", "name": "POST /api/refund"},
				"rel": "calls",
				"confidence": "high",
				"evidence": [{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3}]
			},
			{
				"from": {"kind": "client_call", "name": "POST /api/refund"},
				"to": {"kind": "component", "name": "MissingWidget"},
				"rel": "imports",
				"confidence": "high",
				"evidence": [{"file": "web/src/api/refund.ts", "startLine": 2, "endLine": 3}]
			}
		]
	}`)
	second := runAnalysisJob(t, fixture, analyses, versionID, head, true)
	stats := analysisStoredStats(t, second.Stats)
	if stats.Facts != 2 || stats.Contracts != 1 || stats.UnresolvedEdges != 1 {
		t.Fatalf("stats = %+v, want 2 facts, 1 contract, 1 unresolved edge", stats)
	}
	if stats.Edges != 2 {
		t.Fatalf("stats.edges = %d, want calls + route_to_api", stats.Edges)
	}

	// The bumped fact kept its entity row id.
	if got := analysisEntityID(t, pool, repoID, head, "client:POST /api/refund"); got != firstClientID {
		t.Fatalf("client entity id after the bump = %d, want the preserved %d", got, firstClientID)
	}
	if got := analysisEntityID(t, pool, repoID, head, "api:POST /api/refund"); got != firstAPIID {
		t.Fatalf("api entity id = %d, want the preserved %d", got, firstAPIID)
	}
	var clientVersions int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_fact_versions
		 WHERE repo_id = $1 AND commit_sha = $2 AND fact_key = 'client:POST /api/refund'`,
		repoID, head).Scan(&clientVersions); err != nil {
		t.Fatalf("count client fact versions: %v", err)
	}
	if clientVersions != 2 {
		t.Fatalf("client fact versions = %d, want version 1 and the bumped version 2", clientVersions)
	}

	// No edge may reference an entity row that does not exist.
	var dangling int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_edges e
		 WHERE e.repo_id = $1 AND e.commit_sha = $2
		   AND (NOT EXISTS (
		           SELECT 1 FROM code_entities s
		            WHERE s.id = e.src_entity_id AND s.repo_id = e.repo_id AND s.commit_sha = e.commit_sha)
		        OR NOT EXISTS (
		           SELECT 1 FROM code_entities d
		            WHERE d.id = e.dst_entity_id AND d.repo_id = e.repo_id AND d.commit_sha = e.commit_sha))`,
		repoID, head).Scan(&dangling); err != nil {
		t.Fatalf("count dangling edges: %v", err)
	}
	if dangling != 0 {
		t.Fatalf("dangling edges = %d, want every endpoint to reference a persisted entity", dangling)
	}

	var callsSrc, callsDst, routeSrc, routeDst int64
	if err := pool.QueryRow(ctx,
		`SELECT src_entity_id, dst_entity_id FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = 'calls'`,
		repoID, head).Scan(&callsSrc, &callsDst); err != nil {
		t.Fatalf("read calls edge: %v", err)
	}
	if callsSrc != firstClientID || callsDst != firstAPIID {
		t.Fatalf("calls edge = %d->%d, want persisted %d->%d",
			callsSrc, callsDst, firstClientID, firstAPIID)
	}
	if err := pool.QueryRow(ctx,
		`SELECT src_entity_id, dst_entity_id FROM code_edges
		 WHERE repo_id = $1 AND commit_sha = $2 AND rel = 'route_to_api'`,
		repoID, head).Scan(&routeSrc, &routeDst); err != nil {
		t.Fatalf("read route_to_api edge: %v", err)
	}
	if routeSrc != firstClientID || routeDst != firstAPIID {
		t.Fatalf("route_to_api edge = %d->%d, want persisted %d->%d",
			routeSrc, routeDst, firstClientID, firstAPIID)
	}

	// The planner received the aggregated stats and both persisted contract
	// endpoints as facts at head.
	input, ok := fixture.planner.lastInput()
	if !ok {
		t.Fatal("planner was not called")
	}
	var plannerStats map[string]any
	if err := json.Unmarshal(input.Analysis.Stats, &plannerStats); err != nil {
		t.Fatalf("decode planner analysis stats: %v", err)
	}
	if summary, _ := plannerStats["changeSummary"].(string); !strings.Contains(summary, "refund") {
		t.Fatalf("planner stats.changeSummary = %q, want the aggregated summary", summary)
	}
	plannerIDs := make(map[string]int64)
	for _, fact := range input.FactsAtHead {
		if fact.FactKey != nil {
			plannerIDs[*fact.FactKey] = fact.ID
		}
	}
	if plannerIDs["client:POST /api/refund"] != firstClientID || plannerIDs["api:POST /api/refund"] != firstAPIID {
		t.Fatalf("planner fact ids = %+v, want the persisted contract pair %d/%d",
			plannerIDs, firstClientID, firstAPIID)
	}
}

// TestAnalysisIntegrationForceAndReuseOnSameInput proves that a plain trigger on
// the same input reuses the done analysis instead of creating a new version.
func TestAnalysisIntegrationForceAndReuseOnSameInput(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	_, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	fixture := newAnalysisServiceForIntegration(t, pool, newAnalysisFakeLLM(), "p4.1")
	fixture.planner.requirementID = requirementID
	analyses := codeintelpg.NewAnalysisRepository(pool)

	first := runAnalysisJob(t, fixture, analyses, versionID, head, false)
	callsAfterFirst := fixture.llm.callCount()

	reused, reusedFlag, err := fixture.service.Start(ctx, versionID, 1, head, false)
	if err != nil {
		t.Fatalf("Start reuse error = %v", err)
	}
	if !reusedFlag || reused.ID != first.ID {
		t.Fatalf("Start reuse = %+v reused=%v, want analysis %d reused", reused, reusedFlag, first.ID)
	}
	if got := fixture.llm.callCount(); got != callsAfterFirst {
		t.Fatalf("reuse ran the pipeline: LLM calls %d -> %d", callsAfterFirst, got)
	}
	if fixture.queue.len() != 1 {
		t.Fatalf("enqueued jobs = %d, want only the first trigger", fixture.queue.len())
	}
	var analysisCount int
	if err := pool.QueryRow(ctx,
		`SELECT count(*)::int FROM code_analyses WHERE version_id = $1`, versionID).Scan(&analysisCount); err != nil {
		t.Fatalf("count analyses: %v", err)
	}
	if analysisCount != 1 {
		t.Fatalf("analyses = %d, want the reused one only", analysisCount)
	}
}

// TestAnalysisIntegrationPlannerFactsIncludeContractCompanions proves R38
// end to end: the unchanged contract counterpart and the page/component facts
// declared at its path are part of the planner's FactsAtHead, deduplicated by
// entity id, so T13 can propagate a changed api to the page rendering the
// counterpart client call.
func TestAnalysisIntegrationPlannerFactsIncludeContractCompanions(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	const counterpartPath = "web/src/pages/Refund.tsx"
	generator, err := id.NewSnowflake(13)
	if err != nil {
		t.Fatalf("new companion snowflake generator: %v", err)
	}
	entities := codeintelpg.NewEntityRepository(pool)
	clientKey := "client:POST /api/refund"
	clientVersion := 1
	clientID := generator.Next()
	if err := entities.Upsert(ctx, domain.CodeEntity{
		ID: clientID, RepoID: repoID, Commit: head, Kind: domain.KindClientCall,
		Path: counterpartPath, Name: "POST /api/refund", Source: domain.SourceAI,
		Confidence: domain.ConfidenceHigh, FactKey: &clientKey, FactVersion: &clientVersion,
	}); err != nil {
		t.Fatalf("seed counterpart client fact: %v", err)
	}
	companions := make(map[string]int64, 2)
	for _, seed := range []struct {
		kind    string
		name    string
		factKey string
	}{
		{domain.KindComponent, "RefundPage", "component:web/src/pages/Refund.tsx#RefundPage"},
		{domain.KindPage, "RefundPage", "page:RefundPage"},
	} {
		factVersion := 1
		companionID := generator.Next()
		if err := entities.Upsert(ctx, domain.CodeEntity{
			ID: companionID, RepoID: repoID, Commit: head, Kind: seed.kind,
			Path: counterpartPath, Name: seed.name, Source: domain.SourceAI,
			Confidence: domain.ConfidenceHigh, FactKey: &seed.factKey, FactVersion: &factVersion,
		}); err != nil {
			t.Fatalf("seed %s companion: %v", seed.kind, err)
		}
		companions[seed.factKey] = companionID
	}

	fixture := newAnalysisServiceForIntegration(t, pool, newAnalysisFakeLLM(), "p4.1")
	// The changed client file contributes no client_call fact in this fixture:
	// the changed side is the api fact, and the client fact at the counterpart
	// path is the pre-existing entity seeded above (same normalized contract
	// key, so a second materialised client fact would collide).
	fixture.llm.setResponse(analysisClientPath, `{"changeSummary": "client stays the same", "facts": [], "edges": []}`)
	fixture.planner.requirementID = requirementID
	analyses := codeintelpg.NewAnalysisRepository(pool)
	runAnalysisJob(t, fixture, analyses, versionID, head, false)

	input, ok := fixture.planner.lastInput()
	if !ok {
		t.Fatal("planner was not called")
	}
	byKey := make(map[string]domain.CodeEntity, len(input.FactsAtHead))
	byID := make(map[int64]string, len(input.FactsAtHead))
	for _, fact := range input.FactsAtHead {
		if fact.FactKey == nil {
			continue
		}
		if previous, ok := byID[fact.ID]; ok {
			t.Fatalf("planner fact ids = %d repeated by %q and %q, want entity-id dedupe",
				fact.ID, previous, *fact.FactKey)
		}
		byID[fact.ID] = *fact.FactKey
		byKey[*fact.FactKey] = fact
	}
	if got := byKey[clientKey]; got.ID != clientID || got.Path != counterpartPath {
		t.Fatalf("planner counterpart = %+v, want the seeded client call %d at %s",
			got, clientID, counterpartPath)
	}
	for factKey, wantID := range companions {
		got, ok := byKey[factKey]
		if !ok || got.ID != wantID || got.Path != counterpartPath {
			t.Fatalf("planner facts = %+v, want the companion %s (%d) at %s",
				byKey, factKey, wantID, counterpartPath)
		}
	}
}
