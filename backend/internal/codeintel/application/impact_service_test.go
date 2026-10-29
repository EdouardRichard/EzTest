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
// into code_analysis.stats.
type impactStatsDocument struct {
	Candidates         int  `json:"candidates"`
	Requirements       int  `json:"requirements"`
	AppModels          int  `json:"appModels"`
	Direct             int  `json:"direct"`
	Contract           int  `json:"contract"`
	Relation           int  `json:"relation"`
	AI                 int  `json:"ai"`
	AIEvidenceChecks   int  `json:"aiEvidenceChecks"`
	AIEvidenceFailures int  `json:"aiEvidenceFailures"`
	UnmatchedContracts int  `json:"unmatchedContracts"`
	Unmapped           int  `json:"unmapped"`
	UnmappedTruncated  bool `json:"unmappedTruncated"`
	InvalidCandidates  int  `json:"invalidCandidates"`
	PromptFacts        int  `json:"promptFacts"`
	PromptTruncated    bool `json:"promptTruncated"`
	AIUnavailable      bool `json:"aiUnavailable"`
	AIErrors           int  `json:"aiErrors"`
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
	if len(related.Evidence) != 1 || related.Evidence[0].File != impactClientPath {
		t.Fatalf("relation evidence = %+v, want the triggering anchor's changed-file evidence", related.Evidence)
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
		impactEntity(21, domain.KindPage, "/orders", "src/covered.ts", domain.ConfidenceHigh, "page:/orders",
			domain.Evidence{File: "src/covered.ts", StartLine: 1, EndLine: 1}),
		impactEntity(22, domain.KindClientCall, "POST /api/refund", "src/client.ts", domain.ConfidenceHigh, "client:POST /api/refund",
			domain.Evidence{File: "src/client.ts", StartLine: 1, EndLine: 1}),
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

// TestImpactLowConfidenceFactCoversChangedFile pins review finding 2 and INV-8:
// the unmapped coverage view is built from the head-wide fact snapshot, so an
// evidence-verified low-confidence fact still covers its changed file even
// though INV-1 keeps it out of automatic candidate generation. A low clue with
// no verified evidence covers nothing.
func TestImpactLowConfidenceFactCoversChangedFile(t *testing.T) {
	lowFact := func(evidence ...domain.Evidence) domain.CodeEntity {
		return impactEntity(71, domain.KindModule, "refund", impactPlainPath,
			domain.ConfidenceLow, "symbol:"+impactPlainPath+"#refund", evidence...)
	}

	t.Run("verified low fact covers", func(t *testing.T) {
		f := newImpactFixture(t)
		in := f.input([]domain.ChangedFile{impactChangedFile(impactPlainPath)},
			[]domain.CodeEntity{lowFact(domain.Evidence{
				File: impactPlainPath, StartLine: 1, EndLine: 1, ExcerptHash: "sha256:ee",
			})})
		in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}
		in.Degraded = true

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		for _, entry := range result.Unmapped {
			if entry.Path == impactPlainPath {
				t.Fatalf("low-confidence fact did not cover the changed file: %+v", entry)
			}
		}
		if stats := impactDecodeStats(t, result.Stats); stats.Unmapped != 0 {
			t.Fatalf("stats.unmapped = %d, want 0", stats.Unmapped)
		}
		if len(result.Candidates) != 0 {
			t.Fatalf("low fact produced automatic candidates (INV-1): %+v", result.Candidates)
		}
	})

	t.Run("low fact without evidence does not cover", func(t *testing.T) {
		f := newImpactFixture(t)
		in := f.input([]domain.ChangedFile{impactChangedFile(impactPlainPath)},
			[]domain.CodeEntity{lowFact()})

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		impactUnmappedFor(t, result.Unmapped, impactPlainPath, domain.UnmappedNoFact)
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
	if stats.InvalidCandidates != 1 || stats.AIEvidenceChecks != 2 || stats.AIEvidenceFailures != 1 {
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
		AIEvidenceChecks: 1, Unmapped: 1,
	}
	if stats.Candidates != want.Candidates || stats.Requirements != want.Requirements ||
		stats.AppModels != want.AppModels || stats.Direct != want.Direct ||
		stats.Contract != want.Contract || stats.Relation != want.Relation || stats.AI != want.AI ||
		stats.AIEvidenceChecks != want.AIEvidenceChecks || stats.Unmapped != want.Unmapped {
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

// TestImpactAppModelCandidatesOnlyUseChangedFacts pins review finding 1: the
// head-wide FactsAtHead snapshot must never turn an unrelated high/medium api
// fact from another analysis/version into a direct app model candidate. A fact
// produced by this run keeps counting even when the model attributed it to a
// path outside the change set (AnalysisID provenance).
func TestImpactAppModelCandidatesOnlyUseChangedFacts(t *testing.T) {
	const adminAppModelID int64 = 9301

	newAdminFixture := func(t *testing.T) *impactFixture {
		t.Helper()
		f := newImpactFixture(t)
		f.appModels.nodes = []AppModelNode{{
			ID:       adminAppModelID,
			SystemID: impactSystemID,
			Kind:     domain.KindAPI,
			Key:      "api:POST /api/admin",
			Name:     "Admin",
			Path:     "/api/admin",
			Method:   "POST",
		}}
		return f
	}
	unrelated := func(confidence string) domain.CodeEntity {
		return impactEntity(31, domain.KindAPI, "POST /api/admin", "server/admin.go",
			confidence, "api:POST /api/admin",
			domain.Evidence{File: "server/admin.go", StartLine: 1, EndLine: 2})
	}

	t.Run("unrelated head fact is ignored", func(t *testing.T) {
		for _, confidence := range []string{domain.ConfidenceHigh, domain.ConfidenceMedium} {
			t.Run(confidence, func(t *testing.T) {
				f := newAdminFixture(t)
				in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)},
					[]domain.CodeEntity{unrelated(confidence)})
				in.Degraded = true

				result, err := f.newService(nil).Plan(context.Background(), in)
				if err != nil {
					t.Fatalf("Plan error = %v", err)
				}
				if !impactAbsentCandidate(result.Candidates, domain.CandidateAppModel, adminAppModelID) {
					t.Fatalf("unchanged head fact produced an app model candidate: %+v", result.Candidates)
				}
				if len(result.Candidates) != 0 {
					t.Fatalf("candidates = %+v, want none", result.Candidates)
				}
			})
		}
	})

	t.Run("this run out-of-path fact still counts", func(t *testing.T) {
		f := newAdminFixture(t)
		analysisID := int64(77)
		own := unrelated(domain.ConfidenceHigh)
		own.ID = 32
		own.AnalysisID = &analysisID
		in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)},
			[]domain.CodeEntity{own})
		in.Analysis.ID = analysisID
		in.Degraded = true

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		candidate := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, adminAppModelID)
		if candidate.Source != domain.CandidateDirect || candidate.Confidence != domain.ConfidenceHigh {
			t.Fatalf("run-provenance candidate = %+v, want direct/high", candidate)
		}
	})

	t.Run("low fact never becomes a candidate", func(t *testing.T) {
		f := newAdminFixture(t)
		low := unrelated(domain.ConfidenceLow)
		in := f.input([]domain.ChangedFile{impactChangedFile("server/admin.go")},
			[]domain.CodeEntity{low})
		in.Degraded = true

		result, err := f.newService(nil).Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		if len(result.Candidates) != 0 {
			t.Fatalf("low fact produced automatic candidates (INV-1): %+v", result.Candidates)
		}
	})
}

// TestImpactLowFactsProduceNoAutomaticCandidates pins INV-1 for the direct and
// contract sources: low client_call/api facts never yield an app model
// candidate, even when their key matches a node and a route_to_api edge exists.
func TestImpactLowFactsProduceNoAutomaticCandidates(t *testing.T) {
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
	lowAPI := impactEntity(41, domain.KindAPI, "POST /api/refund", impactServerPath,
		domain.ConfidenceLow, "api:POST /api/refund",
		domain.Evidence{File: impactServerPath, StartLine: 1, EndLine: 1})
	lowClient := impactEntity(42, domain.KindClientCall, "POST /api/refund", impactClientPath,
		domain.ConfidenceLow, "client:POST /api/refund",
		domain.Evidence{File: impactClientPath, StartLine: 1, EndLine: 1})
	f.edges.edges = []domain.CodeEdge{{
		ID: 33, RepoID: impactRepoID, Commit: testCommitA,
		SrcEntityID: 42, DstEntityID: 41,
		Rel: domain.RelRouteToAPI, Source: domain.SourceAI, Confidence: domain.ConfidenceLow,
		Meta: []byte(`{"method":"POST","normalizedPath":"/api/refund","match":"exact"}`),
	}}

	in := f.input([]domain.ChangedFile{
		impactChangedFile(impactClientPath), impactChangedFile(impactServerPath),
	}, []domain.CodeEntity{lowAPI, lowClient})
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 0 {
		t.Fatalf("low facts produced automatic candidates (INV-1): %+v", result.Candidates)
	}
	stats := impactDecodeStats(t, result.Stats)
	if stats.UnmatchedContracts != 0 {
		t.Fatalf("stats.unmatchedContracts = %d, want 0 (low facts are not contract sources)", stats.UnmatchedContracts)
	}
}

// TestImpactAIFailuresDegradeWithoutFailing pins §6.6's degradation contract for
// both AI failure modes: a completion error and a malformed JSON response set
// aiUnavailable/aiErrors, leave the analysis successful and mark the uncovered
// changed files ai_unavailable.
func TestImpactAIFailuresDegradeWithoutFailing(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*impactFixture)
	}{
		{
			name: "completion error",
			mutate: func(f *impactFixture) {
				f.llm.defaultErr = errors.New("llm unavailable")
			},
		},
		{
			name: "malformed json",
			mutate: func(f *impactFixture) {
				f.llm.defaultResponse = "this is not the impact schema"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newImpactFixture(t)
			tc.mutate(f)
			in := f.input([]domain.ChangedFile{impactChangedFile(impactPlainPath)}, nil)
			in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}

			result, err := f.newService(nil).Plan(context.Background(), in)
			if err != nil {
				t.Fatalf("Plan error = %v, want a degraded success", err)
			}
			if got := f.llm.callCount(); got != 1 {
				t.Fatalf("LLM calls = %d, want 1", got)
			}
			if len(result.Candidates) != 0 {
				t.Fatalf("candidates = %+v, want none", result.Candidates)
			}
			if entry := impactUnmappedFor(t, result.Unmapped, impactPlainPath, domain.UnmappedAIUnavailable); entry.Suggestion != "AI 不可用，请人工确认" {
				t.Fatalf("unmapped = %+v, want the AI-unavailable suggestion", entry)
			}
			stats := impactDecodeStats(t, result.Stats)
			if !stats.AIUnavailable || stats.AIErrors != 1 || stats.AIEvidenceChecks != 0 {
				t.Fatalf("stats = %+v, want aiUnavailable with one AI error", stats)
			}
		})
	}
}

// TestImpactChangedAPIContractPropagation pins §6.5/§6.6's second propagation
// direction: a changed api fact reaches its route_to_api counterpart client_call
// and the component/page facts on that counterpart path become contract-sourced
// app model candidates (R38 supplies the companion facts in FactsAtHead).
func TestImpactChangedAPIContractPropagation(t *testing.T) {
	const (
		componentAppModelID int64 = 9401
		pageAppModelID      int64 = 9402
	)
	clientPath := impactClientPath
	serverPath := impactServerPath

	f := newImpactFixture(t)
	f.appModels.nodes = []AppModelNode{
		{ID: componentAppModelID, SystemID: impactSystemID, Kind: domain.KindComponent,
			Key: "component:RefundButton", Name: "RefundButton", Path: clientPath},
		{ID: pageAppModelID, SystemID: impactSystemID, Kind: domain.KindPage,
			Key: "page:/orders", Name: "Orders", Path: "/orders"},
	}
	apiFact := impactEntity(51, domain.KindAPI, "POST /api/refund", serverPath,
		domain.ConfidenceHigh, "api:POST /api/refund",
		domain.Evidence{File: serverPath, StartLine: 1, EndLine: 2})
	clientFact := impactEntity(52, domain.KindClientCall, "POST /api/refund", clientPath,
		domain.ConfidenceHigh, "client:POST /api/refund",
		domain.Evidence{File: clientPath, StartLine: 1, EndLine: 1, ExcerptHash: "sha256:bb"})
	componentFact := impactEntity(53, domain.KindComponent, "RefundButton", clientPath,
		domain.ConfidenceMedium, "component:web/src/api/refund.ts#RefundButton",
		domain.Evidence{File: clientPath, StartLine: 2, EndLine: 2, ExcerptHash: "sha256:cc"})
	pageFact := impactEntity(54, domain.KindPage, "/orders", clientPath,
		domain.ConfidenceHigh, "page:/orders",
		domain.Evidence{File: clientPath, StartLine: 3, EndLine: 3, ExcerptHash: "sha256:dd"})
	f.edges.edges = []domain.CodeEdge{{
		ID: 34, RepoID: impactRepoID, Commit: testCommitA,
		SrcEntityID: 52, DstEntityID: 51,
		Rel: domain.RelRouteToAPI, Source: domain.SourceAI, Confidence: domain.ConfidenceHigh,
		Evidence: []domain.Evidence{{File: clientPath, StartLine: 1, EndLine: 1, ExcerptHash: "sha256:aa"}},
		Meta:     []byte(`{"method":"POST","normalizedPath":"/api/refund","match":"exact"}`),
	}}

	in := f.input([]domain.ChangedFile{impactChangedFile(serverPath)},
		[]domain.CodeEntity{apiFact, clientFact, componentFact, pageFact})
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want the component and page contract candidates", result.Candidates)
	}
	for _, candidate := range result.Candidates {
		if candidate.Source != domain.CandidateContract {
			t.Fatalf("candidate = %+v, want source=contract (companions are on the unchanged counterpart path)", candidate)
		}
		if candidate.Reason != "contract POST /api/refund changed" {
			t.Fatalf("candidate reason = %q", candidate.Reason)
		}
	}
	component := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, componentAppModelID)
	if component.Confidence != domain.ConfidenceMedium || !component.Selected {
		t.Fatalf("component candidate = %+v, want contract/medium/selected (min of api, edge and companion)", component)
	}
	if !impactCandidateHasEvidence(component, serverPath) || !impactCandidateHasEvidence(component, clientPath) {
		t.Fatalf("component evidence = %+v, want the api, edge and companion evidence", component.Evidence)
	}
	page := impactFindCandidate(t, result.Candidates, domain.CandidateAppModel, pageAppModelID)
	if page.Confidence != domain.ConfidenceHigh || !page.Selected {
		t.Fatalf("page candidate = %+v, want contract/high/selected", page)
	}
	stats := impactDecodeStats(t, result.Stats)
	if stats.UnmatchedContracts != 0 || stats.Contract != 2 {
		t.Fatalf("stats = %+v, want two contract candidates and no unmatched contract", stats)
	}
}

// impactCandidateHasEvidence reports whether one candidate carries evidence for
// a file.
func impactCandidateHasEvidence(candidate domain.ImpactCandidate, file string) bool {
	for _, evidence := range candidate.Evidence {
		if evidence.File == file {
			return true
		}
	}
	return false
}

// TestImpactPlannerStatsPreserveAnalysisEvidenceCounters pins ruling R37: the
// planner emits aiEvidenceChecks/aiEvidenceFailures and never overwrites the T10
// evidenceChecks/evidenceFailures keys when the analysis stats are merged.
func TestImpactPlannerStatsPreserveAnalysisEvidenceCounters(t *testing.T) {
	f := newImpactFixture(t)
	f.llm.defaultResponse = fmt.Sprintf(
		`{"affected":[{"requirementId":"%d","reason":"证据有效","confidence":"high","evidence":[{"file":%q,"startLine":1,"endLine":1}]}]}`,
		impactRequirementID, impactClientPath)
	in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
	in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}

	var plannerKeys map[string]json.RawMessage
	if err := json.Unmarshal(result.Stats, &plannerKeys); err != nil {
		t.Fatalf("decode planner stats %q: %v", string(result.Stats), err)
	}
	for _, legacy := range []string{"evidenceChecks", "evidenceFailures"} {
		if _, ok := plannerKeys[legacy]; ok {
			t.Fatalf("planner stats still emit %q: %s", legacy, string(result.Stats))
		}
	}

	merged, err := mergeAnalysisStats(analysisStats{EvidenceChecks: 6, EvidenceFailures: 2}, result.Stats)
	if err != nil {
		t.Fatalf("merge stats: %v", err)
	}
	var values struct {
		EvidenceChecks     int `json:"evidenceChecks"`
		EvidenceFailures   int `json:"evidenceFailures"`
		AIEvidenceChecks   int `json:"aiEvidenceChecks"`
		AIEvidenceFailures int `json:"aiEvidenceFailures"`
	}
	if err := json.Unmarshal(merged, &values); err != nil {
		t.Fatalf("decode merged stats %q: %v", string(merged), err)
	}
	if values.EvidenceChecks != 6 || values.EvidenceFailures != 2 {
		t.Fatalf("merged T10 evidence counters = %d/%d, want 6/2",
			values.EvidenceChecks, values.EvidenceFailures)
	}
	if values.AIEvidenceChecks != 1 || values.AIEvidenceFailures != 0 {
		t.Fatalf("merged planner counters = %d/%d, want 1/0",
			values.AIEvidenceChecks, values.AIEvidenceFailures)
	}
}

// TestImpactPromptByteBudgetAndSummaryTruncation pins R15: the configured byte
// budget can only lower the 64 KiB hard cap and a large changeSummary is
// truncated so the final system+user prompt never exceeds the budget.
func TestImpactPromptByteBudgetAndSummaryTruncation(t *testing.T) {
	t.Run("configuration clamps to the R15 hard bound", func(t *testing.T) {
		f := newImpactFixture(t)
		svc := f.newService(func(deps *ImpactServiceDeps) {
			deps.MaxPromptBytes = 8 << 20
			deps.MaxFacts = 5000
		})
		if svc.config.maxPromptBytes != 64<<10 {
			t.Fatalf("maxPromptBytes = %d, want the 64 KiB hard cap", svc.config.maxPromptBytes)
		}
		if svc.config.maxFacts != defaultImpactMaxFacts {
			t.Fatalf("maxFacts = %d, want the %d hard cap", svc.config.maxFacts, defaultImpactMaxFacts)
		}
	})

	t.Run("large change summary is truncated", func(t *testing.T) {
		f := newImpactFixture(t)
		summary := strings.Repeat("摘", 2000)
		stats, err := json.Marshal(map[string]string{"changeSummary": summary})
		if err != nil {
			t.Fatalf("encode stats: %v", err)
		}
		in := f.input([]domain.ChangedFile{impactChangedFile(impactClientPath)}, nil)
		in.Analysis.Stats = stats
		in.Requirements = []RequirementInfo{{ID: impactRequirementID, Title: "退款"}}

		svc := f.newService(func(deps *ImpactServiceDeps) {
			deps.MaxPromptBytes = 4 << 10
		})
		result, err := svc.Plan(context.Background(), in)
		if err != nil {
			t.Fatalf("Plan error = %v", err)
		}
		if len(f.llm.users) != 1 {
			t.Fatalf("captured prompts = %d, want 1", len(f.llm.users))
		}
		user := f.llm.users[0]
		if total := len(impactAnalysisSystem) + len(user); total > 4<<10 {
			t.Fatalf("prompt bytes = %d, want <= 4096", total)
		}
		if !strings.Contains(user, "变更摘要已截断") {
			t.Fatalf("prompt is missing the summary truncation marker")
		}
		if strings.Count(user, "摘") >= 2000 {
			t.Fatalf("prompt kept the full change summary")
		}
		statsDoc := impactDecodeStats(t, result.Stats)
		if !statsDoc.PromptTruncated {
			t.Fatalf("stats = %+v, want promptTruncated", statsDoc)
		}
	})
}

// TestImpactUnmappedCombinedPathOrder pins the deterministic order of the
// combined unmapped list: file entries and unmatched-contract extras are sorted
// together by path before the cap is applied.
func TestImpactUnmappedCombinedPathOrder(t *testing.T) {
	f := newImpactFixture(t)
	client := impactEntity(61, domain.KindClientCall, "GET /api/missing", "src/a.ts",
		domain.ConfidenceHigh, "client:GET /api/missing",
		domain.Evidence{File: "src/a.ts", StartLine: 1, EndLine: 1})
	in := f.input([]domain.ChangedFile{
		impactChangedFile("src/z.ts"),
		impactChangedFile("src/a.ts"),
	}, []domain.CodeEntity{client})
	in.Degraded = true

	result, err := f.newService(nil).Plan(context.Background(), in)
	if err != nil {
		t.Fatalf("Plan error = %v", err)
	}
	if len(result.Unmapped) != 2 {
		t.Fatalf("unmapped = %+v, want two entries", result.Unmapped)
	}
	if result.Unmapped[0].Path != "src/a.ts" || result.Unmapped[0].Reason != domain.UnmappedContract {
		t.Fatalf("unmapped[0] = %+v, want the unmatched-contract entry of src/a.ts", result.Unmapped[0])
	}
	if result.Unmapped[1].Path != "src/z.ts" {
		t.Fatalf("unmapped[1] = %+v, want src/z.ts", result.Unmapped[1])
	}
}
