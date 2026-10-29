//go:build integration

package integration

// This file wires the real ImpactService over the real gitx/PostgreSQL/P2/P3
// adapters: a temporary dual-commit Git repository, seeded P2 app model nodes,
// P3 requirements/relations, codeintel facts/contract edges and anchors. It
// proves the design spec §6.6 candidate pipeline end to end (direct anchors,
// exact app model match, one-hop contract propagation, one-hop requirement
// relations and AI reasoning with evidence verification) against real storage.

import (
	"context"
	"encoding/json"
	"fmt"
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

// impactIntegrationLLM is the deterministic AI impact-reasoning completer. It
// answers with one affected requirement whose evidence is one of the changed
// files (lines 2-3 of the head content).
type impactIntegrationLLM struct {
	mu            sync.Mutex
	requirementID int64
	evidenceFile  string
	calls         int
	prompts       []string
}

func (l *impactIntegrationLLM) Complete(_ context.Context, _ string, user string) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls++
	l.prompts = append(l.prompts, user)
	return fmt.Sprintf(`{"affected":[{"requirementId":%q,"reason":"退款原因影响","confidence":"high","evidence":[{"file":%q,"startLine":2,"endLine":3}]}]}`,
		fmt.Sprintf("%d", l.requirementID), l.evidenceFile), nil
}

func (l *impactIntegrationLLM) Model() string { return "fake-impact-model" }

func (l *impactIntegrationLLM) callCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls
}

// impactIntegrationFixture is the wired impact service and the seeded ids.
type impactIntegrationFixture struct {
	service        *codeintelapp.ImpactService
	llm            *impactIntegrationLLM
	versionCatalog *codeintelversion.Catalog
	entities       *codeintelpg.EntityRepo
	repoID         int64
	versionID      int64
	requirementID  int64
	relatedID      int64
	aiRequirement  int64
	appModelID     int64
	headCommit     string
}

// impactIntegrationSeedSystem inserts a P2 company + system and returns the
// system id.
func impactIntegrationSeedSystem(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	ctx := context.Background()
	var companyID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO company (key, name) VALUES ($1, 'P4 impact') RETURNING id`,
		fmt.Sprintf("p4-impact-company-%d", nextSeedID())).Scan(&companyID); err != nil {
		t.Fatalf("seed company: %v", err)
	}
	var systemID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO system (company_id, key, name, kind, base_url)
		 VALUES ($1, $2, 'P4 impact system', 'web', 'http://127.0.0.1:13335') RETURNING id`,
		companyID, fmt.Sprintf("p4-impact-system-%d", nextSeedID())).Scan(&systemID); err != nil {
		t.Fatalf("seed system: %v", err)
	}
	return systemID
}

// impactIntegrationSeedAppModel inserts one P2 app model node.
func impactIntegrationSeedAppModel(t *testing.T, pool *pgxpool.Pool, systemID int64, kind, key, name, path, method string) int64 {
	t.Helper()
	var nodeID int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO app_model_node (system_id, kind, key, name, path, method)
		 VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		systemID, kind, key, name, path, method).Scan(&nodeID); err != nil {
		t.Fatalf("seed app model %s: %v", key, err)
	}
	return nodeID
}

// impactIntegrationSeedRequirement inserts one requirement of versionID.
func impactIntegrationSeedRequirement(t *testing.T, pool *pgxpool.Pool, versionID int64, title string) int64 {
	t.Helper()
	requirementID := nextSeedID()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO requirement (id, version_id, title, created_by, updated_by)
		 VALUES ($1, $2, $3, 1, 1)`, requirementID, versionID, title); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	return requirementID
}

// impactIntegrationSeedRelation inserts one requirement relation.
func impactIntegrationSeedRelation(t *testing.T, pool *pgxpool.Pool, versionID, fromID, toID int64, relationType string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO requirement_relation
		 (id, version_id, from_requirement_id, to_requirement_id, relation_type, created_by)
		 VALUES ($1, $2, $3, $4, $5, 1)`,
		nextSeedID(), versionID, fromID, toID, relationType); err != nil {
		t.Fatalf("seed requirement relation: %v", err)
	}
}

// impactIntegrationSeedAnchor inserts one requirement anchor on a changed file.
func impactIntegrationSeedAnchor(t *testing.T, pool *pgxpool.Pool, repoID, versionID, requirementID int64, path string) int64 {
	t.Helper()
	anchorID := nextSeedID()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO code_anchor
		 (id, version_id, repo_id, subject_kind, subject_id, file_path, confidence, created_by_kind, created_by)
		 VALUES ($1, $2, $3, 'requirement', $4, $5, 'high', 'user', 1)`,
		anchorID, versionID, repoID, requirementID, path); err != nil {
		t.Fatalf("seed anchor: %v", err)
	}
	return anchorID
}

// impactIntegrationSeedFact inserts one current AI fact entity.
func impactIntegrationSeedFact(t *testing.T, pool *pgxpool.Pool, id, repoID int64, commit, kind, name, path, factKey string, evidence []domain.Evidence) {
	t.Helper()
	evidenceJSON, err := json.Marshal(evidence)
	if err != nil {
		t.Fatalf("encode fact evidence: %v", err)
	}
	version := 1
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO code_entity
		 (id, repo_id, commit_sha, kind, path, name, source, confidence, evidence, fact_key, fact_version)
		 VALUES ($1, $2, $3, $4, $5, $6, 'ai', 'high', $7, $8, $9)`,
		id, repoID, commit, kind, path, name, evidenceJSON, factKey, version); err != nil {
		t.Fatalf("seed fact %s: %v", factKey, err)
	}
}

// impactIntegrationSeedContractEdge inserts one route_to_api edge.
func impactIntegrationSeedContractEdge(t *testing.T, pool *pgxpool.Pool, repoID int64, commit string, srcID, dstID int64, method, normalizedPath string) {
	t.Helper()
	meta, err := json.Marshal(map[string]string{
		"method": method, "normalizedPath": normalizedPath, "match": "exact",
	})
	if err != nil {
		t.Fatalf("encode contract meta: %v", err)
	}
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO code_edge
		 (id, repo_id, commit_sha, src_entity_id, dst_entity_id, rel, source, confidence, evidence, meta)
		 VALUES ($1, $2, $3, $4, $5, 'route_to_api', 'ai', 'high', '[]'::jsonb, $6)`,
		nextSeedID(), repoID, commit, srcID, dstID, meta); err != nil {
		t.Fatalf("seed contract edge: %v", err)
	}
}

// newImpactIntegrationFixture wires the production impact service over the real
// adapters and seeds the scenario:
//
//   - one requirement anchored on the changed client file (direct candidate);
//   - a second requirement related to the first (one-hop relation candidate);
//   - a third requirement reported by the fake LLM (AI candidate);
//   - one api app model node reachable through the changed client_call's
//     route_to_api edge (contract candidate);
//   - one deleted, one fact-free changed file (unmapped entries).
func newImpactIntegrationFixture(t *testing.T, pool *pgxpool.Pool) *impactIntegrationFixture {
	t.Helper()
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, requirementID := seedAnalysisRepoVersion(t, pool, dir, base)

	systemID := impactIntegrationSeedSystem(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE version SET system_id = $1 WHERE id = $2`, systemID, versionID); err != nil {
		t.Fatalf("bind version system: %v", err)
	}
	appModelID := impactIntegrationSeedAppModel(t, pool, systemID,
		"api", "api:POST /api/refund", "Refund", "/api/refund", "POST")
	impactIntegrationSeedAppModel(t, pool, systemID,
		"page", "page:/orders", "Orders", "/orders", "")

	relatedID := impactIntegrationSeedRequirement(t, pool, versionID, "退款展示")
	aiRequirement := impactIntegrationSeedRequirement(t, pool, versionID, "退款原因")
	impactIntegrationSeedRelation(t, pool, versionID, requirementID, relatedID, "depends_on")
	impactIntegrationSeedAnchor(t, pool, repoID, versionID, requirementID, analysisClientPath)

	// The changed client_call fact and its unchanged api counterpart. The api
	// entity lives outside the change set, so the planner sees only the client
	// fact and must propagate through the contract edge.
	clientEntityID := nextSeedID()
	impactIntegrationSeedFact(t, pool, clientEntityID, repoID, head,
		"client_call", "POST /api/refund", analysisClientPath, "client:POST /api/refund",
		[]domain.Evidence{{File: analysisClientPath, StartLine: 2, EndLine: 3}})
	apiEntityID := nextSeedID()
	impactIntegrationSeedFact(t, pool, apiEntityID, repoID, head,
		"api", "POST /api/refund", "server/impl.go", "api:POST /api/refund",
		[]domain.Evidence{{File: "server/impl.go", StartLine: 1, EndLine: 2}})
	impactIntegrationSeedContractEdge(t, pool, repoID, head, clientEntityID, apiEntityID, "POST", "/api/refund")

	llm := &impactIntegrationLLM{requirementID: aiRequirement, evidenceFile: analysisClientPath}

	catalogRepo := resourcepg.NewCatalogRepository(pool)
	appCatalog := resourceapp.NewCatalogService(catalogRepo, catalogRepo, catalogRepo, catalogRepo, indexIntegrationClock{})
	repoCatalog := codeintelresource.NewCatalog(
		resourceapp.NewCodeRepoService(resourcepg.NewCodeRepository(pool), nil, indexIntegrationClock{}),
		appCatalog)

	versionRepo := versionpg.NewVersionRepository(pool)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	versionService := versionapp.NewVersionService(versionRepo, nil, nil, nil)
	requirementService := versionapp.NewRequirementService(requirementRepo, versionRepo, nil)
	relationService := versionapp.NewRelationService(requirementRepo)
	versionCatalog := codeintelversion.NewCatalog(versionService, requirementService, relationService)

	generator, err := id.NewSnowflake(12)
	if err != nil {
		t.Fatalf("new impact snowflake generator: %v", err)
	}
	service := codeintelapp.NewImpactService(codeintelapp.ImpactServiceDeps{
		LLM:             llm,
		Versions:        versionCatalog,
		AppModels:       repoCatalog,
		Anchors:         codeintelpg.NewAnchorRepository(pool),
		Edges:           codeintelpg.NewEdgeRepository(pool),
		Git:             gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second}),
		Repos:           repoCatalog,
		IDs:             generator,
		MaxRequirements: 100,
		MaxFacts:        200,
		MaxPromptBytes:  64 << 10,
	})
	return &impactIntegrationFixture{
		service:        service,
		llm:            llm,
		versionCatalog: versionCatalog,
		entities:       codeintelpg.NewEntityRepository(pool),
		repoID:         repoID,
		versionID:      versionID,
		requirementID:  requirementID,
		relatedID:      relatedID,
		aiRequirement:  aiRequirement,
		appModelID:     appModelID,
		headCommit:     head,
	}
}

// TestImpactIntegrationPlanCandidates proves the §6.6 planner end to end on real
// PostgreSQL rows and a real repository checkout.
func TestImpactIntegrationPlanCandidates(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	fixture := newImpactIntegrationFixture(t, pool)

	version, err := fixture.versionCatalog.Get(ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("version catalog Get: %v", err)
	}
	requirements, err := fixture.versionCatalog.ListRequirements(ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("ListRequirements: %v", err)
	}
	relations, err := fixture.versionCatalog.ListRelations(ctx, fixture.versionID)
	if err != nil {
		t.Fatalf("ListRelations: %v", err)
	}
	changed := []domain.ChangedFile{
		{Path: analysisClientPath, Status: "M", HeadBlobHash: "git-sha1:1111111111111111111111111111111111111111"},
		{Path: analysisServerPath, Status: "M", HeadBlobHash: "git-sha1:2222222222222222222222222222222222222222"},
		{Path: analysisUtilsPath, Status: "M", HeadBlobHash: "git-sha1:3333333333333333333333333333333333333333"},
		{Path: analysisLegacyPath, Status: "D"},
	}
	changedPaths := []string{analysisClientPath, analysisServerPath, analysisUtilsPath, analysisLegacyPath}
	facts, err := fixture.entities.ListByPaths(ctx, fixture.repoID, fixture.headCommit, changedPaths)
	if err != nil {
		t.Fatalf("ListByPaths: %v", err)
	}
	if len(facts) != 1 {
		t.Fatalf("facts at head = %+v, want the one client_call fact", facts)
	}

	result, err := fixture.service.Plan(ctx, codeintelapp.ImpactInput{
		Version:      version,
		Analysis:     domain.CodeAnalysis{VersionID: fixture.versionID, RepoID: fixture.repoID, HeadCommit: fixture.headCommit, Stats: []byte(`{"changeSummary":"退款接口调整"}`)},
		Requirements: requirements,
		Relations:    relations,
		ChangedFiles: changed,
		FactsAtHead:  facts,
	})
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	direct := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateRequirement, fixture.requirementID)
	if direct.Source != domain.CandidateDirect || direct.Confidence != domain.ConfidenceHigh || !direct.Selected {
		t.Fatalf("direct candidate = %+v, want direct/high/selected", direct)
	}
	if direct.Reason != "changed file "+analysisClientPath {
		t.Fatalf("direct reason = %q", direct.Reason)
	}

	related := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateRequirement, fixture.relatedID)
	if related.Source != domain.CandidateRelation || related.Confidence != domain.ConfidenceMedium {
		t.Fatalf("relation candidate = %+v, want relation/medium", related)
	}
	wantRelationReason := fmt.Sprintf("related to R%d via depends_on", fixture.requirementID)
	if related.Reason != wantRelationReason {
		t.Fatalf("relation reason = %q, want %q", related.Reason, wantRelationReason)
	}

	ai := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateRequirement, fixture.aiRequirement)
	if ai.Source != domain.CandidateAI || ai.Confidence != domain.ConfidenceHigh || !ai.Selected {
		t.Fatalf("AI candidate = %+v, want ai/high/selected", ai)
	}
	if len(ai.Evidence) != 1 || ai.Evidence[0].File != analysisClientPath ||
		ai.Evidence[0].StartLine != 2 || ai.Evidence[0].EndLine != 3 ||
		!strings.HasPrefix(ai.Evidence[0].ExcerptHash, domain.ExcerptHashPrefix) {
		t.Fatalf("AI evidence = %+v, want the verified excerpt", ai.Evidence)
	}

	contract := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateAppModel, fixture.appModelID)
	if contract.Source != domain.CandidateContract || contract.Confidence != domain.ConfidenceHigh {
		t.Fatalf("contract candidate = %+v, want contract/high", contract)
	}
	if contract.Reason != "contract POST /api/refund changed" {
		t.Fatalf("contract reason = %q", contract.Reason)
	}
	if len(result.Candidates) != 4 {
		t.Fatalf("candidates = %+v, want exactly four", result.Candidates)
	}

	if len(result.Unmapped) != 3 {
		t.Fatalf("unmapped = %+v, want three entries", result.Unmapped)
	}
	if entry := impactIntegrationFindUnmapped(t, result.Unmapped, analysisLegacyPath, domain.UnmappedDeleted); entry.Status != "D" {
		t.Fatalf("deleted unmapped = %+v", entry)
	}
	impactIntegrationFindUnmapped(t, result.Unmapped, analysisServerPath, domain.UnmappedNoFact)
	impactIntegrationFindUnmapped(t, result.Unmapped, analysisUtilsPath, domain.UnmappedNoFact)

	if got := fixture.llm.callCount(); got != 1 {
		t.Fatalf("LLM calls = %d, want one impact-reasoning call", got)
	}
	if len(fixture.llm.prompts) != 1 ||
		!strings.Contains(fixture.llm.prompts[0], fmt.Sprintf("%d", fixture.requirementID)) ||
		!strings.Contains(fixture.llm.prompts[0], "变更事实") {
		t.Fatalf("impact prompt = %q, want the requirement list and fact section", fixture.llm.prompts)
	}

	var stats struct {
		Candidates         int `json:"candidates"`
		Requirements       int `json:"requirements"`
		AppModels          int `json:"appModels"`
		Direct             int `json:"direct"`
		Contract           int `json:"contract"`
		Relation           int `json:"relation"`
		AI                 int `json:"ai"`
		AIEvidenceChecks   int `json:"aiEvidenceChecks"`
		AIEvidenceFailures int `json:"aiEvidenceFailures"`
		UnmatchedContracts int `json:"unmatchedContracts"`
		Unmapped           int `json:"unmapped"`
	}
	if err := json.Unmarshal(result.Stats, &stats); err != nil {
		t.Fatalf("decode impact stats %q: %v", string(result.Stats), err)
	}
	if stats.Candidates != 4 || stats.Requirements != 3 || stats.AppModels != 1 ||
		stats.Direct != 1 || stats.Contract != 1 || stats.Relation != 1 || stats.AI != 1 ||
		stats.AIEvidenceChecks != 1 || stats.AIEvidenceFailures != 0 ||
		stats.UnmatchedContracts != 0 || stats.Unmapped != 3 {
		t.Fatalf("impact stats = %+v", stats)
	}
}

// TestImpactIntegrationChangedAPIContractPropagation proves the R38 direction of
// §6.5 end to end: a changed api fact reaches its unchanged route_to_api
// counterpart client_call, and the component/page facts on that counterpart path
// match the real P2 app model nodes as contract-sourced candidates.
func TestImpactIntegrationChangedAPIContractPropagation(t *testing.T) {
	pool := testutil.OpenTestDB(t)
	ctx := context.Background()
	dir, base, head := newAnalysisGitRepo(t)
	repoID, versionID, _ := seedAnalysisRepoVersion(t, pool, dir, base)

	systemID := impactIntegrationSeedSystem(t, pool)
	if _, err := pool.Exec(ctx, `UPDATE version SET system_id = $1 WHERE id = $2`, systemID, versionID); err != nil {
		t.Fatalf("bind version system: %v", err)
	}
	componentID := impactIntegrationSeedAppModel(t, pool, systemID,
		"component", "component:RefundButton", "RefundButton", analysisClientPath, "")
	pageID := impactIntegrationSeedAppModel(t, pool, systemID,
		"page", "page:/orders", "Orders", "/orders", "")

	// The changed api fact, its unchanged counterpart client_call and the
	// companion component/page facts on the counterpart path.
	apiEntityID := nextSeedID()
	impactIntegrationSeedFact(t, pool, apiEntityID, repoID, head,
		"api", "POST /api/refund", analysisServerPath, "api:POST /api/refund",
		[]domain.Evidence{{File: analysisServerPath, StartLine: 1, EndLine: 2}})
	clientEntityID := nextSeedID()
	impactIntegrationSeedFact(t, pool, clientEntityID, repoID, head,
		"client_call", "POST /api/refund", analysisClientPath, "client:POST /api/refund",
		[]domain.Evidence{{File: analysisClientPath, StartLine: 2, EndLine: 3}})
	impactIntegrationSeedFact(t, pool, nextSeedID(), repoID, head,
		"component", "RefundButton", analysisClientPath, "component:web/src/api/client.ts#RefundButton",
		[]domain.Evidence{{File: analysisClientPath, StartLine: 2, EndLine: 3}})
	impactIntegrationSeedFact(t, pool, nextSeedID(), repoID, head,
		"page", "/orders", analysisClientPath, "page:/orders",
		[]domain.Evidence{{File: analysisClientPath, StartLine: 2, EndLine: 3}})
	impactIntegrationSeedContractEdge(t, pool, repoID, head, clientEntityID, apiEntityID, "POST", "/api/refund")

	catalogRepo := resourcepg.NewCatalogRepository(pool)
	appCatalog := resourceapp.NewCatalogService(catalogRepo, catalogRepo, catalogRepo, catalogRepo, indexIntegrationClock{})
	repoCatalog := codeintelresource.NewCatalog(
		resourceapp.NewCodeRepoService(resourcepg.NewCodeRepository(pool), nil, indexIntegrationClock{}),
		appCatalog)
	versionRepo := versionpg.NewVersionRepository(pool)
	requirementRepo := versionpg.NewRequirementRepository(pool)
	versionCatalog := codeintelversion.NewCatalog(
		versionapp.NewVersionService(versionRepo, nil, nil, nil),
		versionapp.NewRequirementService(requirementRepo, versionRepo, nil),
		versionapp.NewRelationService(requirementRepo))
	generator, err := id.NewSnowflake(14)
	if err != nil {
		t.Fatalf("new impact snowflake generator: %v", err)
	}
	service := codeintelapp.NewImpactService(codeintelapp.ImpactServiceDeps{
		Versions:  versionCatalog,
		AppModels: repoCatalog,
		Anchors:   codeintelpg.NewAnchorRepository(pool),
		Edges:     codeintelpg.NewEdgeRepository(pool),
		Git:       gitx.NewGitReader(gitx.GitConfig{CommandTimeout: 10 * time.Second}),
		Repos:     repoCatalog,
		IDs:       generator,
	})

	version, err := versionCatalog.Get(ctx, versionID)
	if err != nil {
		t.Fatalf("version catalog Get: %v", err)
	}
	facts, err := codeintelpg.NewEntityRepository(pool).ListByPaths(
		ctx, repoID, head, []string{analysisServerPath, analysisClientPath})
	if err != nil {
		t.Fatalf("ListByPaths: %v", err)
	}
	if len(facts) != 4 {
		t.Fatalf("facts at head = %+v, want the api, client_call and two companion facts", facts)
	}
	result, err := service.Plan(ctx, codeintelapp.ImpactInput{
		Version:      version,
		Analysis:     domain.CodeAnalysis{VersionID: versionID, RepoID: repoID, HeadCommit: head},
		ChangedFiles: []domain.ChangedFile{{Path: analysisServerPath, Status: "M", HeadBlobHash: "git-sha1:4444444444444444444444444444444444444444"}},
		FactsAtHead:  facts,
		Degraded:     true,
	})
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the component and page contract candidates", result.Candidates)
	}
	for _, candidate := range result.Candidates {
		if candidate.Source != domain.CandidateContract || candidate.Reason != "contract POST /api/refund changed" {
			t.Fatalf("candidate = %+v, want contract POST /api/refund changed", candidate)
		}
	}
	component := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateAppModel, componentID)
	page := impactIntegrationFindCandidate(t, result.Candidates, domain.CandidateAppModel, pageID)
	if component.Confidence != domain.ConfidenceHigh || !component.Selected || len(component.Evidence) != 2 {
		t.Fatalf("component candidate = %+v, want contract/high/selected with merged evidence", component)
	}
	if page.Confidence != domain.ConfidenceHigh || !page.Selected {
		t.Fatalf("page candidate = %+v, want contract/high/selected", page)
	}
}

// impactIntegrationFindCandidate returns the candidate of one subject or fails.
func impactIntegrationFindCandidate(t *testing.T, candidates []domain.ImpactCandidate, subjectKind string, subjectID int64) domain.ImpactCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.SubjectKind == subjectKind && candidate.SubjectID == subjectID {
			return candidate
		}
	}
	t.Fatalf("candidate %s/%d missing from %+v", subjectKind, subjectID, candidates)
	return domain.ImpactCandidate{}
}

// impactIntegrationFindUnmapped returns one unmapped entry or fails.
func impactIntegrationFindUnmapped(t *testing.T, entries []domain.UnmappedChange, path, reason string) domain.UnmappedChange {
	t.Helper()
	for _, entry := range entries {
		if entry.Path == path && entry.Reason == reason {
			return entry
		}
	}
	t.Fatalf("unmapped %s/%s missing from %+v", path, reason, entries)
	return domain.UnmappedChange{}
}
