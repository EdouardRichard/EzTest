package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Unit-test fixture ids, paths and contents of the impact service. The ids are
// deliberately distinct from the analysis fixture ids so a failed assertion is
// unambiguous; the paths are repository-relative, slash-separated and the line
// numbers below are chosen against impactClientContent.
const (
	impactVersionID            int64 = 41
	impactRepoID               int64 = 3
	impactSystemID             int64 = 7
	impactRequirementID        int64 = 9101
	impactRelatedRequirementID int64 = 9102
	impactAIRequirementID      int64 = 9103
	impactAppModelID           int64 = 9201
	impactAPIAppModelID        int64 = 9202

	impactClientPath = "web/src/api/refund.ts"
	impactServerPath = "server/refund.go"
	impactPlainPath  = "web/src/plain.ts"

	impactClientContent = "line one\nline two\nline three\n"
)

var impactTestTime = time.Date(2026, 10, 28, 2, 3, 4, 0, time.UTC)

// impactAppModels is an AppModelCatalog double.
type impactAppModels struct {
	mu    sync.Mutex
	nodes []AppModelNode
	err   error
	calls []int64
}

func (c *impactAppModels) ListBySystem(_ context.Context, systemID int64, _ []string) ([]AppModelNode, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls = append(c.calls, systemID)
	if c.err != nil {
		return nil, c.err
	}
	nodes := make([]AppModelNode, len(c.nodes))
	copy(nodes, c.nodes)
	return nodes, nil
}

// impactEdges is an EdgeRepository double serving one route_to_api set.
type impactEdges struct {
	mu        sync.Mutex
	edges     []domain.CodeEdge
	err       error
	listCalls []string
}

func (e *impactEdges) ReplaceFactEdges(context.Context, int64, string, string, int, []domain.CodeEdge) error {
	return nil
}

func (e *impactEdges) ListByEntity(context.Context, int64, string, int64) ([]domain.CodeEdge, error) {
	return nil, nil
}

func (e *impactEdges) ListByRel(_ context.Context, _ int64, _ string, rel string) ([]domain.CodeEdge, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.listCalls = append(e.listCalls, rel)
	if e.err != nil {
		return nil, e.err
	}
	edges := make([]domain.CodeEdge, len(e.edges))
	copy(edges, e.edges)
	return edges, nil
}

// impactFixture assembles the impact service over deterministic test doubles.
type impactFixture struct {
	anchors   *analysisAnchors
	edges     *impactEdges
	appModels *impactAppModels
	git       *analysisGit
	repos     *stubRepos
	versions  *stubVersions
	llm       *stubLLM
	ids       *stubIDs
	clock     *stubClock
}

func newImpactFixture(t *testing.T) *impactFixture {
	t.Helper()
	f := &impactFixture{
		anchors:   &analysisAnchors{},
		edges:     &impactEdges{},
		appModels: &impactAppModels{},
		git: &analysisGit{
			files:   map[string]string{impactClientPath: impactClientContent},
			readErr: map[string]error{},
		},
		repos: &stubRepos{infos: map[int64]RepoInfo{
			impactRepoID: {
				ID:          impactRepoID,
				Key:         "web",
				LocalPath:   "/repo",
				FixedBranch: "main",
				Status:      "active",
			},
		}},
		versions: &stubVersions{info: VersionInfo{
			ID:             impactVersionID,
			SystemID:       impactSystemID,
			HasSystem:      true,
			RepoID:         impactRepoID,
			HasRepo:        true,
			Branch:         "main",
			BaselineCommit: testCommitA,
		}},
		llm:   newStubLLM(),
		ids:   &stubIDs{next: 5000},
		clock: &stubClock{now: impactTestTime},
	}
	f.llm.defaultResponse = `{"affected":[]}`
	return f
}

// newService wires the service and lets the test replace one dependency.
func (f *impactFixture) newService(mutate func(*ImpactServiceDeps)) *ImpactService {
	deps := ImpactServiceDeps{
		LLM:             f.llm,
		Versions:        f.versions,
		AppModels:       f.appModels,
		Anchors:         f.anchors,
		Edges:           f.edges,
		Git:             f.git,
		Repos:           f.repos,
		IDs:             f.ids,
		Clock:           f.clock,
		MaxRequirements: 0,
		MaxFacts:        0,
		MaxPromptBytes:  0,
	}
	if mutate != nil {
		mutate(&deps)
	}
	return NewImpactService(deps)
}

func (f *impactFixture) input(changed []domain.ChangedFile, facts []domain.CodeEntity) ImpactInput {
	return ImpactInput{
		Version:      f.versions.info,
		Analysis:     domain.CodeAnalysis{VersionID: impactVersionID, RepoID: impactRepoID, HeadCommit: testCommitA, Stats: []byte(`{"changeSummary":"退款流程调整"}`)},
		ChangedFiles: changed,
		FactsAtHead:  facts,
	}
}

// impactChangedFile builds one modified changed file with a non-empty hash.
func impactChangedFile(path string) domain.ChangedFile {
	return domain.ChangedFile{Path: path, Status: "M", HeadBlobHash: "git-sha1:" + testBlobA}
}

// impactDeletedFile builds one deleted changed file.
func impactDeletedFile(path string) domain.ChangedFile {
	return domain.ChangedFile{Path: path, Status: "D"}
}

// impactEntity builds one high/medium AI fact entity.
func impactEntity(id int64, kind, name, path, confidence, factKey string, evidence ...domain.Evidence) domain.CodeEntity {
	key := factKey
	version := 1
	return domain.CodeEntity{
		ID:          id,
		RepoID:      impactRepoID,
		Commit:      testCommitA,
		Kind:        kind,
		Name:        name,
		Path:        path,
		Source:      domain.SourceAI,
		Confidence:  confidence,
		Evidence:    evidence,
		FactKey:     &key,
		FactVersion: &version,
	}
}

// impactFindCandidate returns the candidate of one subject or fails.
func impactFindCandidate(t *testing.T, candidates []domain.ImpactCandidate, subjectKind string, subjectID int64) domain.ImpactCandidate {
	t.Helper()
	for _, candidate := range candidates {
		if candidate.SubjectKind == subjectKind && candidate.SubjectID == subjectID {
			return candidate
		}
	}
	t.Fatalf("candidate %s/%d missing from %+v", subjectKind, subjectID, candidates)
	return domain.ImpactCandidate{}
}

// impactAbsentCandidate reports whether a subject has no candidate.
func impactAbsentCandidate(candidates []domain.ImpactCandidate, subjectKind string, subjectID int64) bool {
	for _, candidate := range candidates {
		if candidate.SubjectKind == subjectKind && candidate.SubjectID == subjectID {
			return false
		}
	}
	return true
}

// impactStatsDocument mirrors the planner stats JSON the analysis service merges
// into code_analyses.stats.
type impactStatsDocument struct {
	Candidates         int  `json:"candidates"`
	Requirements       int  `json:"requirements"`
	AppModels          int  `json:"appModels"`
	Direct             int  `json:"direct"`
	Contract           int  `json:"contract"`
	Relation           int  `json:"relation"`
	AI                 int  `json:"ai"`
	EvidenceChecks     int  `json:"evidenceChecks"`
	EvidenceFailures   int  `json:"evidenceFailures"`
	UnmatchedContracts int  `json:"unmatchedContracts"`
	Unmapped           int  `json:"unmapped"`
	UnmappedTruncated  bool `json:"unmappedTruncated"`
	InvalidCandidates  int  `json:"invalidCandidates"`
	PromptFacts        int  `json:"promptFacts"`
	PromptTruncated    bool `json:"promptTruncated"`
	AIUnavailable      bool `json:"aiUnavailable"`
}

func impactDecodeStats(t *testing.T, raw []byte) impactStatsDocument {
	t.Helper()
	var stats impactStatsDocument
	if err := json.Unmarshal(raw, &stats); err != nil {
		t.Fatalf("decode impact stats %q: %v", string(raw), err)
	}
	return stats
}

// impactUnmappedFor returns the unmapped entry of one path and reason.
func impactUnmappedFor(t *testing.T, entries []domain.UnmappedChange, path, reason string) domain.UnmappedChange {
	t.Helper()
	for _, entry := range entries {
		if entry.Path == path && entry.Reason == reason {
			return entry
		}
	}
	t.Fatalf("unmapped %s/%s missing from %+v", path, reason, entries)
	return domain.UnmappedChange{}
}

// TestImpactDirectAnchorAndAppModelMatch pins the deterministic direct sources
// of design spec §6.6 step 3: anchors on changed files produce requirement and
// app-model candidates (version anchors stay display-only) and high/medium facts
// matching version-system app model keys produce app-model candidates.
func TestImpactDirectAnchorAndAppModelMatch(t *testing.T) {
	f := newImpactFixture(t)
	f.appModels.nodes = []AppModelNode{{
		ID:       impactAPIAppModelID,
		SystemID: impactSystemID,
		Kind:     domain.KindAPI,
		Key:      "api:POST /api/refund",
		Name:     "Refund",
		Path:     "/api/refund",
		Method:   "POST",
	}}

	fact := impactEntity(11, domain.KindAPI, "POST /api/refund", impactServerPath,
		domain.ConfidenceHigh, "api:POST /api/refund",
		domain.Evidence{File: impactServerPath, StartLine: 1, EndLine: 2})

	requirementAnchor := domain.CodeAnchor{
		ID: 1, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, Confidence: domain.ConfidenceHigh,
	}
	versionAnchor := domain.CodeAnchor{
		ID: 2, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectVersion, SubjectID: impactVersionID,
		FilePath: impactClientPath, Confidence: domain.ConfidenceHigh,
	}
	appModelAnchor := domain.CodeAnchor{
		ID: 3, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectAppModel, SubjectID: impactAppModelID,
		FilePath: impactServerPath, Confidence: domain.ConfidenceMedium,
	}

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath), impactChangedFile(impactServerPath)},
		[]domain.CodeEntity{fact})
	in.Anchors = []domain.CodeAnchor{requirementAnchor, versionAnchor, appModelAnchor}
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}
	in.Degraded = true // deterministic sources only

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 3 {
		t.Fatalf("candidates = %+v, want 3", result.Candidates)
	}

	direct := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRequirementID)
	if direct.Source != domain.CandidateDirect || direct.Confidence != domain.ConfidenceHigh || !direct.Selected {
		t.Fatalf("requirement candidate = %+v, want direct/high/selected", direct)
	}
	if direct.Reason != "changed file "+impactClientPath {
		t.Fatalf("requirement reason = %q, want the changed file reason", direct.Reason)
	}
	if len(direct.Evidence) != 1 || direct.Evidence[0].File != impactClientPath ||
		direct.Evidence[0].StartLine != 0 || direct.Evidence[0].ExcerptHash != "" {
		t.Fatalf("requirement evidence = %+v, want the anchor file only", direct.Evidence)
	}

	anchoredModel := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, impactAppModelID)
	if anchoredModel.Source != domain.CandidateDirect || anchoredModel.Confidence != domain.ConfidenceMedium {
		t.Fatalf("app model anchor candidate = %+v, want direct/medium", anchoredModel)
	}
	if anchoredModel.Reason != "changed file "+impactServerPath {
		t.Fatalf("app model anchor reason = %q", anchoredModel.Reason)
	}

	matchedModel := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, impactAPIAppModelID)
	if matchedModel.Source != domain.CandidateDirect || matchedModel.Confidence != domain.ConfidenceHigh {
		t.Fatalf("fact-matched app model candidate = %+v, want direct/high", matchedModel)
	}
	if matchedModel.Reason != "changed api POST /api/refund" {
		t.Fatalf("fact-matched reason = %q, want changed <kind> <name>", matchedModel.Reason)
	}
	if len(matchedModel.Evidence) != 1 || matchedModel.Evidence[0].File != impactServerPath {
		t.Fatalf("fact-matched evidence = %+v, want the fact evidence", matchedModel.Evidence)
	}
	if !impactAbsentCandidate(result.Candidates, domain.SubjectVersion, impactVersionID) {
		t.Fatalf("version anchor leaked into candidates: %+v", result.Candidates)
	}
	// The changed api fact has no client_call counterpart, so the extra
	// unmatched-contract entry is expected (§6.5/§6.6 step 7).
	if len(result.Unmapped) != 1 {
		t.Fatalf("unmapped = %+v, want the unmatched-contract entry", result.Unmapped)
	}
	if entry := impactUnmappedFor(t, result.Unmapped, impactServerPath, domain.UnmappedContract); entry.Status != "M" {
		t.Fatalf("unmatched contract entry = %+v", entry)
	}
	stats := impactDecodeStats(t, result.Stats)
	if stats.UnmatchedContracts != 1 {
		t.Fatalf("stats.unmatchedContracts = %d, want 1", stats.UnmatchedContracts)
	}
	if calls := f.appModels.calls; len(calls) != 1 || calls[0] != impactSystemID {
		t.Fatalf("app model catalog calls = %v, want one system lookup", calls)
	}
}

// TestImpactContractPropagation pins the one-hop §6.5 contract propagation: a
// changed client_call fact is followed through its route_to_api edge to the
// counterpart api method/path and yields the api app-model candidate.
func TestImpactContractPropagation(t *testing.T) {
	f := newImpactFixture(t)
	f.appModels.nodes = []AppModelNode{{
		ID:       impactAPIAppModelID,
		SystemID: impactSystemID,
		Kind:     domain.KindAPI,
		Key:      "api:GET /api/orders",
		Name:     "Orders",
		Path:     "/api/orders",
		Method:   "GET",
	}}
	f.edges.edges = []domain.CodeEdge{{
		ID:          31,
		RepoID:      impactRepoID,
		Commit:      testCommitA,
		SrcEntityID: 11,
		DstEntityID: 99,
		Rel:         domain.RelRouteToAPI,
		Source:      domain.SourceAI,
		Confidence:  domain.ConfidenceHigh,
		Evidence:    []domain.Evidence{{File: impactClientPath, StartLine: 1, EndLine: 2, ExcerptHash: "sha256:aa"}},
		Meta:        []byte(`{"method":"GET","normalizedPath":"/api/orders","match":"exact"}`),
	}}

	client := impactEntity(11, domain.KindClientCall, "GET /api/orders", impactClientPath,
		domain.ConfidenceHigh, "client:GET /api/orders",
		domain.Evidence{File: impactClientPath, StartLine: 1, EndLine: 1, ExcerptHash: "sha256:bb"})

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, []domain.CodeEntity{client})

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the contract candidate", result.Candidates)
	}
	contract := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, impactAPIAppModelID)
	if contract.Source != domain.CandidateContract || contract.Confidence != domain.ConfidenceHigh || !contract.Selected {
		t.Fatalf("contract candidate = %+v, want contract/high/selected", contract)
	}
	if contract.Reason != "contract GET /api/orders changed" {
		t.Fatalf("contract reason = %q", contract.Reason)
	}
	if len(contract.Evidence) != 2 {
		t.Fatalf("contract evidence = %+v, want the merged edge + fact evidence", contract.Evidence)
	}
	if got := f.edges.listCalls; len(got) != 1 || got[0] != domain.RelRouteToAPI {
		t.Fatalf("edge list calls = %v, want one route_to_api query", got)
	}
}

// TestImpactRelationOneHop pins §6.6 step 3's requirement relation expansion:
// one hop in any direction, no recursion, direct hits excluded.
func TestImpactRelationOneHop(t *testing.T) {
	f := newImpactFixture(t)

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
	in.Anchors = []domain.CodeAnchor{{
		ID: 1, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, Confidence: domain.ConfidenceHigh,
	}}
	in.Requirements = []RequirementInfo{
		{ID: impactRequirementID, Title: "direct"},
		{ID: impactRelatedRequirementID, Title: "related"},
		{ID: impactAIRequirementID, Title: "far"},
	}
	in.Relations = []RelationInfo{
		// R2 -> R1: the relation is found from the direct hit even though the
		// direct requirement is the relation target.
		{FromID: impactRelatedRequirementID, ToID: impactRequirementID, Type: "depends_on"},
		// R2 -> R3 must not expand again (no recursion).
		{FromID: impactRelatedRequirementID, ToID: impactAIRequirementID, Type: "overlaps"},
	}
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the direct and one-hop relation candidates", result.Candidates)
	}
	direct := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRequirementID)
	if direct.Source != domain.CandidateDirect {
		t.Fatalf("direct candidate = %+v", direct)
	}
	related := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRelatedRequirementID)
	if related.Source != domain.CandidateRelation || related.Confidence != domain.ConfidenceMedium {
		t.Fatalf("relation candidate = %+v, want relation/medium", related)
	}
	wantReason := fmt.Sprintf("related to R%d via depends_on", impactRequirementID)
	if related.Reason != wantReason {
		t.Fatalf("relation reason = %q, want %q", related.Reason, wantReason)
	}
	if !impactAbsentCandidate(result.Candidates, domain.CandidateRequirement, impactAIRequirementID) {
		t.Fatalf("relation expanded twice: %+v", result.Candidates)
	}
}

// TestImpactDedupeSortAndLowSelection pins §6.6 steps 4-6: one candidate per
// subject with the best source/confidence, merged reasons/evidence, the
// deterministic order and selected = confidence != low.
func TestImpactDedupeSortAndLowSelection(t *testing.T) {
	f := newImpactFixture(t)
	f.appModels.nodes = []AppModelNode{
		{ID: impactAppModelID, SystemID: impactSystemID, Kind: domain.KindPage, Key: "page:/orders", Name: "Orders", Path: "/orders"},
		{ID: impactAPIAppModelID, SystemID: impactSystemID, Kind: domain.KindAPI, Key: "api:POST /api/refund", Name: "Refund", Path: "/api/refund", Method: "POST"},
	}
	apiFact := impactEntity(11, domain.KindAPI, "POST /api/refund", impactClientPath,
		domain.ConfidenceHigh, "api:POST /api/refund",
		domain.Evidence{File: impactClientPath, StartLine: 1, EndLine: 2})
	clientFact := impactEntity(12, domain.KindClientCall, "POST /api/refund", impactClientPath,
		domain.ConfidenceHigh, "client:POST /api/refund",
		domain.Evidence{File: impactClientPath, StartLine: 1, EndLine: 1})
	f.edges.edges = []domain.CodeEdge{{
		ID: 31, RepoID: impactRepoID, Commit: testCommitA,
		SrcEntityID: 12, DstEntityID: 99,
		Rel: domain.RelRouteToAPI, Source: domain.SourceAI, Confidence: domain.ConfidenceHigh,
		Evidence: []domain.Evidence{{File: impactClientPath, StartLine: 1, EndLine: 1, ExcerptHash: "sha256:cc"}},
		Meta:     []byte(`{"method":"POST","normalizedPath":"/api/refund","match":"exact"}`),
	}}

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath), impactChangedFile(impactServerPath)},
		[]domain.CodeEntity{apiFact, clientFact})
	in.Anchors = []domain.CodeAnchor{
		{ID: 1, VersionID: impactVersionID, RepoID: impactRepoID, SubjectKind: domain.SubjectRequirement,
			SubjectID: impactRequirementID, FilePath: impactClientPath, Confidence: domain.ConfidenceHigh},
		{ID: 2, VersionID: impactVersionID, RepoID: impactRepoID, SubjectKind: domain.SubjectRequirement,
			SubjectID: impactRequirementID, FilePath: impactServerPath, Confidence: domain.ConfidenceMedium},
		{ID: 3, VersionID: impactVersionID, RepoID: impactRepoID, SubjectKind: domain.SubjectAppModel,
			SubjectID: impactAppModelID, FilePath: impactClientPath, Confidence: domain.ConfidenceHigh},
	}
	in.Requirements = []RequirementInfo{
		{ID: impactRequirementID, Title: "direct"},
		{ID: impactRelatedRequirementID, Title: "related"},
		{ID: impactAIRequirementID, Title: "ai"},
	}
	f.llm.defaultResponse = fmt.Sprintf(`{"affected":[
		{"requirementId":"%d","reason":"退款影响","confidence":"medium","evidence":[{"file":%q,"startLine":1,"endLine":1}]},
		{"requirementId":"%d","reason":"低置信","confidence":"low","evidence":[]},
		{"requirementId":"424242","reason":"未知需求","confidence":"high","evidence":[{"file":%q,"startLine":1,"endLine":1}]}
	]}`, impactRelatedRequirementID, impactClientPath, impactAIRequirementID, impactClientPath)

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 5 {
		t.Fatalf("candidates = %+v, want 5 deduplicated subjects", result.Candidates)
	}

	direct := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRequirementID)
	if direct.Source != domain.CandidateDirect || direct.Confidence != domain.ConfidenceHigh || !direct.Selected {
		t.Fatalf("merged direct candidate = %+v, want direct/high/selected", direct)
	}
	if !strings.Contains(direct.Reason, "changed file "+impactClientPath) ||
		!strings.Contains(direct.Reason, "changed file "+impactServerPath) {
		t.Fatalf("merged reason = %q, want both changed-file reasons", direct.Reason)
	}
	if len(direct.Evidence) != 2 {
		t.Fatalf("merged evidence = %+v, want two merged entries", direct.Evidence)
	}

	model := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, impactAPIAppModelID)
	if model.Source != domain.CandidateDirect || model.Confidence != domain.ConfidenceHigh {
		t.Fatalf("merged app model candidate = %+v, want direct/high (contract is weaker)", model)
	}
	if len(model.Evidence) != 3 {
		t.Fatalf("merged contract evidence = %+v, want the fact, edge and client-call evidence", model.Evidence)
	}

	related := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRelatedRequirementID)
	if related.Source != domain.CandidateAI || related.Confidence != domain.ConfidenceMedium || !related.Selected {
		t.Fatalf("AI candidate = %+v, want ai/medium/selected", related)
	}
	low := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactAIRequirementID)
	if low.Source != domain.CandidateAI || low.Confidence != domain.ConfidenceLow || low.Selected {
		t.Fatalf("low AI candidate = %+v, want ai/low/not selected", low)
	}

	wantOrder := []string{
		fmt.Sprintf("%s/%d", domain.CandidateRequirement, impactRequirementID),
		fmt.Sprintf("%s/%d", domain.CandidateAppModel, impactAppModelID),
		fmt.Sprintf("%s/%d", domain.CandidateAppModel, impactAPIAppModelID),
		fmt.Sprintf("%s/%d", domain.CandidateRequirement, impactRelatedRequirementID),
		fmt.Sprintf("%s/%d", domain.CandidateRequirement, impactAIRequirementID),
	}
	for index, want := range wantOrder {
		got := fmt.Sprintf("%s/%d", result.Candidates[index].SubjectKind, result.Candidates[index].SubjectID)
		if got != want {
			t.Fatalf("candidate order[%d] = %s, want %s (all: %+v)", index, got, want, result.Candidates)
		}
	}

	stats := impactDecodeStats(t, result.Stats)
	if stats.InvalidCandidates != 1 || stats.Candidates != 5 {
		t.Fatalf("stats = %+v, want one dropped unknown requirement and five candidates", stats)
	}
}

// TestImpactUnmappedReasons pins §6.6 step 7 and INV-8: every changed file not
// covered by a valid fact or anchor is reported, with the five documented
// reasons (the AI-unavailable reason needs a degraded run).
func TestImpactUnmappedReasons(t *testing.T) {
	binaryFile := domain.ChangedFile{Path: "src/binary.png", Status: "M", Binary: true, HeadBlobHash: "git-sha1:" + testBlobB}
	clientFile := impactChangedFile("src/client.ts")
	deletedFile := impactDeletedFile("src/deleted.ts")
	plainFile := impactChangedFile(impactPlainPath)
	coveredFile := impactChangedFile("src/covered.ts")

	availableFacts := []domain.CodeEntity{
		impactEntity(21, domain.KindPage, "/orders", "src/covered.ts", domain.ConfidenceHigh, "page:/orders"),
		impactEntity(22, domain.KindClientCall, "POST /api/refund", "src/client.ts", domain.ConfidenceHigh, "client:POST /api/refund"),
	}

	t.Run("available", func(t *testing.T) {
		f := newImpactFixture(t)
		in := f.input([]domain.ChangedFile{binaryFile, clientFile, deletedFile, plainFile, coveredFile}, availableFacts)
		in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "refund"}}

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		if len(result.Unmapped) != 4 {
			t.Fatalf("unmapped = %+v, want 4 entries", result.Unmapped)
		}
		if entry := impactUnmappedFor(t, result.Unmapped, "src/binary.png", domain.UnmappedUnsupported); entry.Status != "M" {
			t.Fatalf("binary unmapped = %+v", entry)
		}
		deleted := impactUnmappedFor(t, result.Unmapped, "src/deleted.ts", domain.UnmappedDeleted)
		if deleted.Status != "D" || deleted.Suggestion != "人工确认删除影响" {
			t.Fatalf("deleted unmapped = %+v, want the documented suggestion", deleted)
		}
		impactUnmappedFor(t, result.Unmapped, impactPlainPath, domain.UnmappedNoFact)
		impactUnmappedFor(t, result.Unmapped, "src/client.ts", domain.UnmappedContract)

		stats := impactDecodeStats(t, result.Stats)
		if stats.UnmatchedContracts != 1 || stats.Unmapped != 4 || stats.AIUnavailable {
			t.Fatalf("stats = %+v, want one unmatched contract and no AI degradation", stats)
		}
	})

	t.Run("degraded", func(t *testing.T) {
		f := newImpactFixture(t)
		f.llm.defaultResponse = `{"affected":[]}`
		// No facts exist in a degraded T0-only run, so the covered file has no
		// coverage either.
		in := f.input([]domain.ChangedFile{binaryFile, clientFile, deletedFile, plainFile}, nil)
		in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "refund"}}
		in.Degraded = true

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		if got := f.llm.callCount(); got != 0 {
			t.Fatalf("LLM calls = %d, want 0 on a degraded run", got)
		}
		if len(result.Unmapped) != 4 {
			t.Fatalf("unmapped = %+v, want 4 entries", result.Unmapped)
		}
		impactUnmappedFor(t, result.Unmapped, "src/binary.png", domain.UnmappedUnsupported)
		impactUnmappedFor(t, result.Unmapped, "src/client.ts", domain.UnmappedAIUnavailable)
		impactUnmappedFor(t, result.Unmapped, "src/deleted.ts", domain.UnmappedDeleted)
		impactUnmappedFor(t, result.Unmapped, impactPlainPath, domain.UnmappedAIUnavailable)

		stats := impactDecodeStats(t, result.Stats)
		if !stats.AIUnavailable || stats.UnmatchedContracts != 0 {
			t.Fatalf("stats = %+v, want aiUnavailable and no unmatched contract", stats)
		}
	})
}

// TestImpactAICandidateValidation pins §6.6 step 3's AI candidate rules: unknown
// requirement ids are dropped, invalid evidence degrades the candidate to low and
// valid evidence keeps the model confidence with a server-recomputed hash.
func TestImpactAICandidateValidation(t *testing.T) {
	f := newImpactFixture(t)
	f.git.files[impactServerPath] = "server line one\nserver line two\n"
	f.llm.defaultResponse = fmt.Sprintf(`{"affected":[
		{"requirementId":"%d","reason":"退款原因影响","confidence":"high","evidence":[{"file":%q,"startLine":1,"endLine":2}]},
		{"requirementId":"%d","reason":"证据无效","confidence":"high","evidence":[{"file":%q,"startLine":90,"endLine":99}]},
		{"requirementId":"999999","reason":"未知需求","confidence":"high","evidence":[{"file":%q,"startLine":1,"endLine":1}]}
	]}`, impactRequirementID, impactServerPath, impactRelatedRequirementID, impactClientPath, impactClientPath)

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
	in.Requirements = []RequirementInfo{
		{ID: impactRequirementID, Title: "退款"},
		{ID: impactRelatedRequirementID, Title: "相关"},
	}

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want two known requirements", result.Candidates)
	}
	valid := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRequirementID)
	if valid.Source != domain.CandidateAI || valid.Confidence != domain.ConfidenceHigh || !valid.Selected {
		t.Fatalf("valid AI candidate = %+v, want ai/high/selected", valid)
	}
	if len(valid.Evidence) != 1 || valid.Evidence[0].File != impactServerPath ||
		valid.Evidence[0].StartLine != 1 || valid.Evidence[0].EndLine != 2 ||
		!strings.HasPrefix(valid.Evidence[0].ExcerptHash, domain.ExcerptHashPrefix) {
		t.Fatalf("valid AI evidence = %+v, want a verified excerpt from any commit file (R14)", valid.Evidence)
	}

	invalid := impactFindCandidate(t, result.Candidates, domain.CandidateRequirement, impactRelatedRequirementID)
	if invalid.Confidence != domain.ConfidenceLow || invalid.Selected || len(invalid.Evidence) != 0 {
		t.Fatalf("invalid-evidence AI candidate = %+v, want low with no evidence", invalid)
	}
	if !impactAbsentCandidate(result.Candidates, domain.CandidateRequirement, 999999) {
		t.Fatalf("unknown requirement id leaked into candidates: %+v", result.Candidates)
	}

	stats := impactDecodeStats(t, result.Stats)
	if stats.InvalidCandidates != 1 || stats.EvidenceChecks != 2 || stats.EvidenceFailures != 1 {
		t.Fatalf("stats = %+v, want one invalid candidate and one failed evidence check", stats)
	}
}

// TestImpactDegradedSkipsAI pins INV-10/§6.6: a degraded run never calls the
// LLM but still returns deterministic candidates and the ai_unavailable
// unmapped entries.
func TestImpactDegradedSkipsAI(t *testing.T) {
	f := newImpactFixture(t)
	f.llm.defaultResponse = fmt.Sprintf(`{"affected":[{"requirementId":"%d","reason":"x","confidence":"high","evidence":[]}]}`, impactRequirementID)

	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath), impactChangedFile(impactServerPath)}, nil)
	in.Anchors = []domain.CodeAnchor{{
		ID: 1, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, Confidence: domain.ConfidenceHigh,
	}}
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if got := f.llm.callCount(); got != 0 {
		t.Fatalf("LLM calls = %d, want 0", got)
	}
	if len(result.Candidates) != 1 {
		t.Fatalf("candidates = %+v, want the direct anchor candidate", result.Candidates)
	}
	if entry := impactUnmappedFor(t, result.Unmapped, impactServerPath, domain.UnmappedAIUnavailable); entry.Status != "M" {
		t.Fatalf("unmapped = %+v", entry)
	}
	stats := impactDecodeStats(t, result.Stats)
	if !stats.AIUnavailable || stats.Unmapped != 1 {
		t.Fatalf("stats = %+v, want aiUnavailable with one unmapped entry", stats)
	}
}

// TestImpactStatsCountsAndNoContent pins §6.6 step 8: the stats document counts
// candidates by kind and source, evidence checks/failures, unmatched contracts,
// unmapped and truncation, and never carries code content or reasons.
func TestImpactStatsCountsAndNoContent(t *testing.T) {
	f := newImpactFixture(t)
	f.appModels.nodes = []AppModelNode{{
		ID: impactAPIAppModelID, SystemID: impactSystemID, Kind: domain.KindAPI,
		Key: "api:POST /api/refund", Name: "Refund", Path: "/api/refund", Method: "POST",
	}}
	client := impactEntity(11, domain.KindClientCall, "POST /api/refund", impactClientPath,
		domain.ConfidenceHigh, "client:POST /api/refund",
		domain.Evidence{File: impactClientPath, StartLine: 1, EndLine: 1})
	f.edges.edges = []domain.CodeEdge{{
		ID: 31, RepoID: impactRepoID, Commit: testCommitA,
		SrcEntityID: 11, DstEntityID: 99,
		Rel: domain.RelRouteToAPI, Source: domain.SourceAI, Confidence: domain.ConfidenceHigh,
		Evidence: []domain.Evidence{{File: impactClientPath, StartLine: 1, EndLine: 1}},
		Meta:     []byte(`{"method":"POST","normalizedPath":"/api/refund","match":"exact"}`),
	}}
	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath), impactChangedFile(impactServerPath)},
		[]domain.CodeEntity{client})
	in.Anchors = []domain.CodeAnchor{{
		ID: 1, VersionID: impactVersionID, RepoID: impactRepoID,
		SubjectKind: domain.SubjectRequirement, SubjectID: impactRequirementID,
		FilePath: impactClientPath, Confidence: domain.ConfidenceHigh,
	}}
	in.Requirements = []RequirementInfo{
		{ID: impactRequirementID, Title: "direct"},
		{ID: impactRelatedRequirementID, Title: "related"},
		{ID: impactAIRequirementID, Title: "ai"},
	}
	in.Relations = []RelationInfo{{FromID: impactRequirementID, ToID: impactRelatedRequirementID, Type: "overlaps"}}
	f.llm.defaultResponse = fmt.Sprintf(`{"affected":[{"requirementId":"%d","reason":"AI 推理","confidence":"high","evidence":[{"file":%q,"startLine":1,"endLine":1}]}]}`,
		impactAIRequirementID, impactClientPath)

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	stats := impactDecodeStats(t, result.Stats)
	want := impactStatsDocument{
		Candidates: 4, Requirements: 3, AppModels: 1,
		Direct: 1, Contract: 1, Relation: 1, AI: 1,
		EvidenceChecks: 1, Unmapped: 1,
	}
	if stats.Candidates != want.Candidates || stats.Requirements != want.Requirements ||
		stats.AppModels != want.AppModels || stats.Direct != want.Direct ||
		stats.Contract != want.Contract || stats.Relation != want.Relation || stats.AI != want.AI ||
		stats.EvidenceChecks != want.EvidenceChecks || stats.Unmapped != want.Unmapped {
		t.Fatalf("stats = %+v, want %+v", stats, want)
	}
	for _, forbidden := range []string{impactClientContent, "changed file", "related to", "退款", "line one"} {
		if strings.Contains(string(result.Stats), forbidden) {
			t.Fatalf("stats leaked content %q: %s", forbidden, string(result.Stats))
		}
	}
}

// TestImpactUnmappedTruncation pins the §6.6 step 7 cap: the unmapped list is
// ordered by path and truncated at the domain maximum with the stats flag set.
func TestImpactUnmappedTruncation(t *testing.T) {
	f := newImpactFixture(t)
	total := domain.MaxUnmappedEntries + 5
	changed := make([]domain.ChangedFile, 0, total)
	for index := 0; index < total; index++ {
		changed = append(changed, impactChangedFile(fmt.Sprintf("src/file-%03d.ts", index)))
	}
	in := f.input(changed, nil)
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Unmapped) != domain.MaxUnmappedEntries {
		t.Fatalf("unmapped entries = %d, want the %d cap", len(result.Unmapped), domain.MaxUnmappedEntries)
	}
	if result.Unmapped[0].Path != "src/file-000.ts" ||
		result.Unmapped[len(result.Unmapped)-1].Path != fmt.Sprintf("src/file-%03d.ts", domain.MaxUnmappedEntries-1) {
		t.Fatalf("unmapped order = %s..%s", result.Unmapped[0].Path, result.Unmapped[len(result.Unmapped)-1].Path)
	}
	stats := impactDecodeStats(t, result.Stats)
	if !stats.UnmappedTruncated || stats.Unmapped != domain.MaxUnmappedEntries {
		t.Fatalf("stats = %+v, want truncation", stats)
	}
}

// TestImpactAppModelKeyForFact pins the §6.6 app model key convention
// (page:<path>, route:<path>, api:<METHOD> <path>, component:<Name>).
func TestImpactAppModelKeyForFact(t *testing.T) {
	cases := []struct {
		name    string
		fact    domain.CodeEntity
		wantKey string
		wantOK  bool
	}{
		{
			name:    "page path without slash",
			fact:    domain.CodeEntity{Kind: domain.KindPage, Name: "/orders"},
			wantKey: "page:/orders", wantOK: true,
		},
		{
			name:    "route path without slash",
			fact:    domain.CodeEntity{Kind: domain.KindRoute, Name: "orders/refund"},
			wantKey: "route:/orders/refund", wantOK: true,
		},
		{
			name:    "api normalized",
			fact:    domain.CodeEntity{Kind: domain.KindAPI, Name: "post /api/refund/{id}?x=1"},
			wantKey: "api:POST /api/refund/{}", wantOK: true,
		},
		{
			name:    "component name only",
			fact:    domain.CodeEntity{Kind: domain.KindComponent, Name: "OrdersPage", Path: "src/pages/OrdersPage.tsx"},
			wantKey: "component:OrdersPage", wantOK: true,
		},
		{
			name:   "missing name",
			fact:   domain.CodeEntity{Kind: domain.KindPage, Name: "  "},
			wantOK: false,
		},
		{
			name:   "unsupported kind",
			fact:   domain.CodeEntity{Kind: domain.KindModule, Name: "m"},
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key, ok := impactAppModelKeyForFact(tc.fact)
			if ok != tc.wantOK || key != tc.wantKey {
				t.Fatalf("impactAppModelKeyForFact(%+v) = %q/%v, want %q/%v", tc.fact, key, ok, tc.wantKey, tc.wantOK)
			}
		})
	}
}

// TestImpactPromptFactCountCap pins R15's count budget independently of the byte
// budget: only MaxFacts entries reach the prompt and the cut is marked.
func TestImpactPromptFactCountCap(t *testing.T) {
	f := newImpactFixture(t)
	facts := make([]domain.CodeEntity, 0, 12)
	for index := 0; index < 12; index++ {
		facts = append(facts, impactEntity(int64(200+index), domain.KindPage,
			fmt.Sprintf("FACT-%02d", index), impactClientPath, domain.ConfidenceHigh,
			fmt.Sprintf("page:FACT-%02d", index)))
	}
	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, facts)
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}

	svc := f.newService(func(deps *ImpactServiceDeps) {
		deps.MaxFacts = 3
		deps.MaxPromptBytes = 64 << 10
	})
	result, err := svc.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(f.llm.users) != 1 {
		t.Fatalf("captured prompts = %d, want 1", len(f.llm.users))
	}
	user := f.llm.users[0]
	if !strings.Contains(user, "FACT-02") || strings.Contains(user, "FACT-03") {
		t.Fatalf("fact count cap leaked: %s", user)
	}
	if !strings.Contains(user, "截断") {
		t.Fatalf("prompt is missing the truncation marker")
	}
	stats := impactDecodeStats(t, result.Stats)
	if stats.PromptFacts != 3 || !stats.PromptTruncated {
		t.Fatalf("stats = %+v, want exactly three prompt facts", stats)
	}
}

// TestImpactPropagatesDependencyErrors pins the failure contract: storage/catalog
// failures while resolving anchors/app models fail the analysis instead of
// silently producing an empty candidate set.
func TestImpactPropagatesDependencyErrors(t *testing.T) {
	t.Run("anchors", func(t *testing.T) {
		f := newImpactFixture(t)
		f.anchors.err = errors.New("anchor storage down")
		in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
		if _, err := f.newService(nil).Plan(context.Background(), in); err == nil {
			t.Fatal("Plan error = nil, want the anchor repository failure")
		}
	})
	t.Run("app models", func(t *testing.T) {
		f := newImpactFixture(t)
		f.appModels.err = errors.New("app model storage down")
		in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
		if _, err := f.newService(nil).Plan(context.Background(), in); err == nil {
			t.Fatal("Plan error = nil, want the app model catalog failure")
		}
	})
	t.Run("edges", func(t *testing.T) {
		f := newImpactFixture(t)
		f.edges.err = errors.New("edge storage down")
		in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
		if _, err := f.newService(nil).Plan(context.Background(), in); err == nil {
			t.Fatal("Plan error = nil, want the edge repository failure")
		}
	})
}

// TestImpactPromptCaps pins R15: the AI impact prompt keeps at most the
// configured fact count and byte budget and marks every cut.
func TestImpactPromptCaps(t *testing.T) {
	f := newImpactFixture(t)
	facts := make([]domain.CodeEntity, 0, 12)
	for index := 0; index < 12; index++ {
		fact := impactEntity(int64(100+index), domain.KindPage,
			fmt.Sprintf("FACT-%02d", index), impactClientPath, domain.ConfidenceHigh,
			fmt.Sprintf("page:FACT-%02d", index))
		fact.Signature = strings.Repeat("影", 200)
		facts = append(facts, fact)
	}
	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, facts)
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: strings.Repeat("需", 100)}}

	svc := f.newService(func(deps *ImpactServiceDeps) {
		deps.MaxFacts = 10
		deps.MaxPromptBytes = 4 << 10
	})
	result, err := svc.Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if got := f.llm.callCount(); got != 1 {
		t.Fatalf("LLM calls = %d, want 1", got)
	}
	if len(f.llm.users) != 1 {
		t.Fatalf("captured prompts = %d, want 1", len(f.llm.users))
	}
	user := f.llm.users[0]
	if len(user) > 4<<10 {
		t.Fatalf("prompt bytes = %d, want <= 4096", len(user))
	}
	if !strings.Contains(user, "截断") {
		t.Fatalf("prompt is missing the truncation marker:\n%s", user)
	}
	if !strings.Contains(user, "FACT-00") {
		t.Fatalf("prompt dropped the first fact:\n%s", user)
	}
	if strings.Contains(user, "FACT-11") {
		t.Fatalf("prompt kept a fact past the byte/count cap")
	}
	stats := impactDecodeStats(t, result.Stats)
	if !stats.PromptTruncated || stats.PromptFacts <= 0 || stats.PromptFacts >= 10 {
		t.Fatalf("stats = %+v, want a truncated prompt with fewer than 10 facts", stats)
	}
}
