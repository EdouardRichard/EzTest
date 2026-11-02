package tools_test

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeintel "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

const (
	testVersionID    = int64(1001)
	testRepoID       = int64(7001)
	testOtherRepoID  = int64(7002)
	testCaseVersion  = int64(41)
	testAnalysisID   = int64(9001)
	baselineCommit   = "1111111111111111111111111111111111111111"
	latestCommit     = "2222222222222222222222222222222222222222"
	explicitCommit   = "3333333333333333333333333333333333333333"
	testStepSeq      = 3
	testAssertionSeq = 2
)

type fakeCodeVersionReader struct {
	bindings []codeintelapp.VersionSUTBinding
	err      error
	version  int64
}

func (f *fakeCodeVersionReader) VersionSUTBindings(_ context.Context, versionID int64) ([]codeintelapp.VersionSUTBinding, error) {
	f.version = versionID
	return f.bindings, f.err
}

type fakeCodeIndexReader struct {
	tree      []codeintel.Entity
	next      string
	hasMore   bool
	treeErr   error
	latest    codeintel.IndexRun
	latestErr error
	calls     []fakeIndexCall
}

type fakeIndexCall struct {
	repoID int64
	commit string
	path   string
	cursor string
	limit  int
}

func (f *fakeCodeIndexReader) Tree(_ context.Context, repoID int64, commit, pathPrefix, cursor string, limit int) ([]codeintel.Entity, string, bool, error) {
	f.calls = append(f.calls, fakeIndexCall{repoID: repoID, commit: commit, path: pathPrefix, cursor: cursor, limit: limit})
	if f.treeErr != nil {
		return nil, "", false, f.treeErr
	}
	return f.tree, f.next, f.hasMore, nil
}

func (f *fakeCodeIndexReader) Latest(_ context.Context, repoID int64) (codeintel.IndexRun, error) {
	if f.latestErr != nil {
		return codeintel.IndexRun{}, f.latestErr
	}
	run := f.latest
	run.RepoID = repoID
	return run, nil
}

type fakeCodeQueryReader struct {
	window     codeintel.FileWindow
	fileErr    error
	diff       codeintel.DiffResult
	diffNext   string
	diffMore   bool
	diffErr    error
	fileCalls  []fakeFileCall
	diffCalls  []fakeDiffCall
	windowArgs codeintel.FileWindow
}

type fakeFileCall struct {
	repoID int64
	commit string
	path   string
	start  int
	end    int
}

type fakeDiffCall struct {
	repoID int64
	base   string
	head   string
	path   string
	cursor string
	limit  int
}

func (f *fakeCodeQueryReader) File(_ context.Context, repoID int64, commit, filePath string, start, end int) (codeintel.FileWindow, error) {
	f.fileCalls = append(f.fileCalls, fakeFileCall{repoID: repoID, commit: commit, path: filePath, start: start, end: end})
	return f.window, f.fileErr
}

func (f *fakeCodeQueryReader) Diff(_ context.Context, repoID int64, base, head, pathFilter, cursor string, limit int) (codeintel.DiffResult, string, bool, error) {
	f.diffCalls = append(f.diffCalls, fakeDiffCall{repoID: repoID, base: base, head: head, path: pathFilter, cursor: cursor, limit: limit})
	return f.diff, f.diffNext, f.diffMore, f.diffErr
}

type fakeCodeAnalysisReader struct {
	facts      []codeintel.Fact
	factsNext  string
	factsMore  bool
	factsErr   error
	entities   []codeintel.Entity
	searchErr  error
	latest     codeintel.Analysis
	latestErr  error
	get        codeintel.Analysis
	getErr     error
	factCalls  []fakeFactsCall
	searchCall fakeSearchCall
}

type fakeFactsCall struct {
	repoID int64
	commit string
	kind   string
	path   string
	cursor string
	limit  int
}

type fakeSearchCall struct {
	repoID int64
	commit string
	query  string
	kind   string
	limit  int
}

func (f *fakeCodeAnalysisReader) Facts(_ context.Context, repoID int64, commit, kind, pathFilter, cursor string, limit int) ([]codeintel.Fact, string, bool, error) {
	f.factCalls = append(f.factCalls, fakeFactsCall{repoID: repoID, commit: commit, kind: kind, path: pathFilter, cursor: cursor, limit: limit})
	if f.factsErr != nil {
		return nil, "", false, f.factsErr
	}
	return f.facts, f.factsNext, f.factsMore, nil
}

func (f *fakeCodeAnalysisReader) Search(_ context.Context, repoID int64, commit, query, kind string, limit int) ([]codeintel.Entity, error) {
	f.searchCall = fakeSearchCall{repoID: repoID, commit: commit, query: query, kind: kind, limit: limit}
	if f.searchErr != nil {
		return nil, f.searchErr
	}
	return f.entities, nil
}

func (f *fakeCodeAnalysisReader) Latest(_ context.Context, versionID, repoID int64) (codeintel.Analysis, error) {
	if f.latestErr != nil {
		return codeintel.Analysis{}, f.latestErr
	}
	analysis := f.latest
	analysis.VersionID = versionID
	analysis.RepoID = repoID
	return analysis, nil
}

func (f *fakeCodeAnalysisReader) Get(_ context.Context, analysisID int64) (codeintel.Analysis, error) {
	if f.getErr != nil {
		return codeintel.Analysis{}, f.getErr
	}
	return f.get, nil
}

type fakeCodeImpactReader struct {
	views      []codeintelapp.ImpactCandidateView
	listErr    error
	unionErr   error
	listCalls  []int64
	unionCalls []int64
}

func (f *fakeCodeImpactReader) List(_ context.Context, analysisID int64) ([]codeintelapp.ImpactCandidateView, error) {
	f.listCalls = append(f.listCalls, analysisID)
	return f.views, f.listErr
}

func (f *fakeCodeImpactReader) Union(_ context.Context, versionID int64) ([]codeintelapp.ImpactCandidateView, error) {
	f.unionCalls = append(f.unionCalls, versionID)
	return f.views, f.unionErr
}

type fakeCodeAnchorReader struct {
	anchors []codeintel.Anchor
	err     error
	calls   []fakeAnchorCall
}

type fakeAnchorCall struct {
	versionID   int64
	subjectKind string
	subjectID   int64
}

func (f *fakeCodeAnchorReader) List(_ context.Context, versionID int64, subjectKind string, subjectID int64) ([]codeintel.Anchor, error) {
	f.calls = append(f.calls, fakeAnchorCall{versionID: versionID, subjectKind: subjectKind, subjectID: subjectID})
	if f.err != nil {
		return nil, f.err
	}
	return f.anchors, nil
}

type fakeSutProposalWriter struct {
	result tools.SutProposalResult
	err    error
	calls  []fakeProposalCall
}

type fakeProposalCall struct {
	versionID int64
	input     tools.SutProposalInput
}

func (f *fakeSutProposalWriter) ProposeSUTResource(_ context.Context, versionID int64, in tools.SutProposalInput) (tools.SutProposalResult, error) {
	f.calls = append(f.calls, fakeProposalCall{versionID: versionID, input: in})
	if f.err != nil {
		return tools.SutProposalResult{}, f.err
	}
	return f.result, nil
}

func singleRepoDeps(t *testing.T) (tools.Deps, *fakeCodeVersionReader, *fakeCodeIndexReader, *fakeCodeQueryReader, *fakeCodeAnalysisReader) {
	t.Helper()
	bindings := &fakeCodeVersionReader{bindings: []codeintelapp.VersionSUTBinding{{
		VersionSUTID: 5001, VersionID: testVersionID, SUTID: 6001, RepoID: testRepoID,
		Branch: "main", CodeBaselineCommit: baselineCommit,
	}}}
	index := &fakeCodeIndexReader{latest: codeintel.IndexRun{CommitSHA: latestCommit, Status: "done"}}
	query := &fakeCodeQueryReader{}
	analysis := &fakeCodeAnalysisReader{}
	deps := tools.Deps{P5: tools.P5Deps{
		Versions: bindings,
		Index:    index,
		Query:    query,
		Analysis: analysis,
	}}
	return deps, bindings, index, query, analysis
}

func execute(t *testing.T, registry *tools.Registry, scope tools.Scope, name, raw string) (any, error) {
	t.Helper()
	return registry.Execute(context.Background(), scope, name, json.RawMessage(raw))
}

func toolError(t *testing.T, err error) *tools.Error {
	t.Helper()
	var toolErr *tools.Error
	if !errors.As(err, &toolErr) {
		t.Fatalf("error %v is not a *tools.Error", err)
	}
	return toolErr
}

func TestCodeTreeResolvesScopeRepositoryAndBaseline(t *testing.T) {
	deps, _, index, _, _ := singleRepoDeps(t)
	index.tree = []codeintel.Entity{
		{Kind: codeintel.EntityFile, Path: "src/main.tsx", Name: "main.tsx", BlobHash: "git-sha1:blob"},
		{Kind: codeintel.EntityModule, Path: "src/app.ts", Name: "app.ts"},
	}
	index.next = "src/app.ts"
	index.hasMore = true
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{}`)
	if err != nil {
		t.Fatalf("code.tree: %v", err)
	}
	if len(index.calls) != 1 {
		t.Fatalf("Tree calls = %d", len(index.calls))
	}
	call := index.calls[0]
	if call.repoID != testRepoID || call.commit != baselineCommit || call.limit != 50 {
		t.Fatalf("Tree call = %+v, want repo %d commit %s limit 50", call, testRepoID, baselineCommit)
	}
	result, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("code.tree result = %#v", value)
	}
	if result["repo_id"] != "7001" || result["commit"] != baselineCommit || result["next_cursor"] != "src/app.ts" || result["has_more"] != true {
		t.Fatalf("code.tree result = %#v", result)
	}
	if entries, ok := result["entries"].([]map[string]any); !ok || len(entries) != 2 || entries[0]["path"] != "src/main.tsx" {
		t.Fatalf("code.tree entries = %#v", result["entries"])
	}
}

func TestCodeTreeRequiresExplicitRepoForMultiRepoVersion(t *testing.T) {
	deps, _, _, _, _ := singleRepoDeps(t)
	bindings := deps.P5.Versions.(*fakeCodeVersionReader)
	bindings.bindings = append(bindings.bindings, codeintelapp.VersionSUTBinding{
		VersionSUTID: 5002, VersionID: testVersionID, RepoID: testOtherRepoID, Branch: "main",
	})
	registry := tools.NewP5Registry(deps)

	_, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{}`)
	toolErr := toolError(t, err)
	if toolErr.Code != tools.CodeInvalidArgument || toolErr.Details["field"] != "repoId" {
		t.Fatalf("multi repo error = %+v", toolErr)
	}

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{"repoId":"7002"}`)
	if err != nil {
		t.Fatalf("code.tree with repoId: %v", err)
	}
	if value.(map[string]any)["repo_id"] != "7002" {
		t.Fatalf("resolved repo = %v", value.(map[string]any)["repo_id"])
	}

	_, err = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{"repoId":"7999"}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("unbound repo error = %+v", toolErr)
	}
}

func TestCodeToolsRequireInjectedVersionScope(t *testing.T) {
	deps, _, _, _, _ := singleRepoDeps(t)
	registry := tools.NewP5Registry(deps)
	for _, name := range []string{tools.ToolCodeTree, tools.ToolCodeSearch, tools.ToolCodeFacts} {
		_, err := execute(t, registry, tools.Scope{}, name, `{}`)
		if err == nil {
			t.Fatalf("%s accepted a missing version scope", name)
		}
		if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
			t.Fatalf("%s error = %+v", name, toolErr)
		}
	}
}

func TestCodeToolSchemasRejectVersionIDArgument(t *testing.T) {
	deps, _, _, _, _ := singleRepoDeps(t)
	registry := tools.NewP5Registry(deps)
	_, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{"versionId":"9"}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("code.tree versionId error = %+v", toolErr)
	}
}

func TestCodeTreeClampsLimit(t *testing.T) {
	deps, _, index, _, _ := singleRepoDeps(t)
	registry := tools.NewP5Registry(deps)
	if _, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{"limit":500}`); err != nil {
		t.Fatalf("code.tree: %v", err)
	}
	if len(index.calls) != 1 || index.calls[0].limit != 50 {
		t.Fatalf("Tree call = %+v, want limit 50", index.calls)
	}
	_, _ = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{"limit":10}`)
	if index.calls[1].limit != 10 {
		t.Fatalf("Tree explicit limit = %d, want 10", index.calls[1].limit)
	}
}

func TestCodeReadReturnsBoundedFileWindow(t *testing.T) {
	deps, _, _, query, _ := singleRepoDeps(t)
	query.window = codeintel.FileWindow{
		Path: "src/main.tsx", Commit: baselineCommit, Lines: []string{"line1", "line2"},
		StartLine: 10, EndLine: 11, TotalLines: 50, Truncated: false,
	}
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeRead,
		`{"path":"src/main.tsx","startLine":10,"endLine":11}`)
	if err != nil {
		t.Fatalf("code.read: %v", err)
	}
	if len(query.fileCalls) != 1 {
		t.Fatalf("File calls = %d", len(query.fileCalls))
	}
	call := query.fileCalls[0]
	if call.repoID != testRepoID || call.commit != baselineCommit || call.path != "src/main.tsx" || call.start != 10 || call.end != 11 {
		t.Fatalf("File call = %+v", call)
	}
	result := value.(map[string]any)
	if result["repo_id"] != "7001" || result["path"] != "src/main.tsx" || result["start_line"] != 10 || result["end_line"] != 11 {
		t.Fatalf("code.read result = %#v", result)
	}
	if lines, ok := result["lines"].([]string); !ok || !reflect.DeepEqual(lines, []string{"line1", "line2"}) {
		t.Fatalf("code.read lines = %#v", result["lines"])
	}

	_, err = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeRead, `{}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("code.read without path error = %+v", toolErr)
	}
}

func TestCodeSearchClampsLimit(t *testing.T) {
	deps, _, _, _, analysis := singleRepoDeps(t)
	analysis.entities = []codeintel.Entity{{Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "orders"}}
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeSearch,
		`{"q":"orders","limit":500}`)
	if err != nil {
		t.Fatalf("code.search: %v", err)
	}
	if analysis.searchCall.repoID != testRepoID || analysis.searchCall.commit != baselineCommit ||
		analysis.searchCall.query != "orders" || analysis.searchCall.limit != 50 {
		t.Fatalf("Search call = %+v", analysis.searchCall)
	}
	if result := value.(map[string]any); result["repo_id"] != "7001" || result["commit"] != baselineCommit {
		t.Fatalf("code.search result = %#v", result)
	}
}

func TestCodeFactsPagesAndCapsAt100(t *testing.T) {
	deps, _, _, _, analysis := singleRepoDeps(t)
	analysis.facts = []codeintel.Fact{{
		ID: 3001, Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "orders",
		Confidence: codeintel.ConfidenceHigh, FactKey: "route:orders", Version: 2,
	}}
	analysis.factsNext, analysis.factsMore = "route:orders", true
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeFacts,
		`{"kind":"route","path":"src/routes.ts","cursor":"prev","limit":500}`)
	if err != nil {
		t.Fatalf("code.facts: %v", err)
	}
	if len(analysis.factCalls) != 1 {
		t.Fatalf("Facts calls = %d", len(analysis.factCalls))
	}
	call := analysis.factCalls[0]
	if call.repoID != testRepoID || call.commit != baselineCommit || call.kind != "route" || call.path != "src/routes.ts" || call.cursor != "prev" || call.limit != 100 {
		t.Fatalf("Facts call = %+v", call)
	}
	result := value.(map[string]any)
	if result["next_cursor"] != "route:orders" || result["has_more"] != true {
		t.Fatalf("code.facts paging = %#v", result)
	}
	if facts, ok := result["facts"].([]map[string]any); !ok || len(facts) != 1 || facts[0]["fact_key"] != "route:orders" {
		t.Fatalf("code.facts = %#v", result["facts"])
	}
}

func TestCodeRoutesAndAPIsMergeKinds(t *testing.T) {
	deps, _, _, _, analysis := singleRepoDeps(t)
	analysis.facts = []codeintel.Fact{{ID: 3001, Kind: codeintel.EntityRoute, Path: "src/routes.ts", Name: "orders"}}
	registry := tools.NewP5Registry(deps)

	if _, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeRoutes, `{}`); err != nil {
		t.Fatalf("code.routes: %v", err)
	}
	if len(analysis.factCalls) != 2 || analysis.factCalls[0].kind != "route" || analysis.factCalls[1].kind != "page" {
		t.Fatalf("code.routes calls = %+v", analysis.factCalls)
	}
	analysis.factCalls = nil

	if _, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeAPIs, `{}`); err != nil {
		t.Fatalf("code.apis: %v", err)
	}
	if len(analysis.factCalls) != 2 || analysis.factCalls[0].kind != "api" || analysis.factCalls[1].kind != "client_call" {
		t.Fatalf("code.apis calls = %+v", analysis.factCalls)
	}
}

func TestCodeDiffUsesVersionBaselineAndLatestIndex(t *testing.T) {
	deps, _, _, query, _ := singleRepoDeps(t)
	query.diff = codeintel.DiffResult{Files: []codeintel.ChangedFile{
		{Path: "src/main.tsx", Status: "M", Additions: 3, Deletions: 1},
		{Path: "src/old.tsx", OldPath: "src/old2.tsx", Status: "R"},
	}}
	query.diffMore = true
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeDiff, `{}`)
	if err != nil {
		t.Fatalf("code.diff: %v", err)
	}
	if len(query.diffCalls) != 1 {
		t.Fatalf("Diff calls = %d", len(query.diffCalls))
	}
	call := query.diffCalls[0]
	if call.repoID != testRepoID || call.base != baselineCommit || call.head != latestCommit || call.limit != 200 {
		t.Fatalf("Diff call = %+v", call)
	}
	result := value.(map[string]any)
	if result["base"] != baselineCommit || result["head"] != latestCommit || result["truncated"] != true {
		t.Fatalf("code.diff result = %#v", result)
	}
	files := result["files"].([]map[string]any)
	if len(files) != 2 || files[0]["path"] != "src/main.tsx" || files[1]["old_path"] != "src/old2.tsx" {
		t.Fatalf("code.diff files = %#v", files)
	}
}

func TestCodeImpactUsesUnionWhenAnalysisOmitted(t *testing.T) {
	deps, _, _, _, analysis := singleRepoDeps(t)
	impact := &fakeCodeImpactReader{views: []codeintelapp.ImpactCandidateView{{
		ImpactCandidate: codeintel.ImpactCandidate{
			ID: 4001, AnalysisID: testAnalysisID, SubjectKind: codeintel.SubjectCaseVersion,
			SubjectID: 41, Reason: "changed", Confidence: codeintel.ConfidenceHigh,
			Source: codeintel.CandidateDirect, Selected: true,
		},
		RepoID: testRepoID, VersionSUTIDs: []int64{5001},
	}}}
	deps.P5.Impact = impact
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeImpact, `{}`)
	if err != nil {
		t.Fatalf("code.impact: %v", err)
	}
	if len(impact.unionCalls) != 1 || impact.unionCalls[0] != testVersionID {
		t.Fatalf("Union calls = %v", impact.unionCalls)
	}
	candidates := value.(map[string]any)["candidates"].([]map[string]any)
	if len(candidates) != 1 || candidates[0]["subject_kind"] != "case_version" ||
		candidates[0]["subject_id"] != "41" || candidates[0]["repo_id"] != "7001" {
		t.Fatalf("code.impact candidates = %#v", candidates)
	}

	impact.unionCalls = nil
	analysis.get = codeintel.Analysis{ID: testAnalysisID, VersionID: testVersionID, RepoID: testRepoID}
	if _, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeImpact, `{"analysisId":"9001"}`); err != nil {
		t.Fatalf("code.impact analysis: %v", err)
	}
	if len(impact.listCalls) != 1 || impact.listCalls[0] != testAnalysisID {
		t.Fatalf("List calls = %v", impact.listCalls)
	}

	analysis.get = codeintel.Analysis{ID: testAnalysisID, VersionID: 999, RepoID: testRepoID}
	_, err = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeImpact, `{"analysisId":"9001"}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeNotFound {
		t.Fatalf("cross-version analysis error = %+v", toolErr)
	}
}

func TestCodeAnchorsFilterByRepo(t *testing.T) {
	deps, _, _, _, _ := singleRepoDeps(t)
	anchors := &fakeCodeAnchorReader{anchors: []codeintel.Anchor{
		{ID: 5001, VersionID: testVersionID, RepoID: testRepoID, SubjectKind: codeintel.AnchorSubjectCaseVersion, SubjectID: 41, FilePath: "src/main.tsx", Status: "valid"},
		{ID: 5002, VersionID: testVersionID, RepoID: testOtherRepoID, SubjectKind: codeintel.AnchorSubjectIssue, SubjectID: 51, FilePath: "src/other.tsx", Status: "valid"},
	}}
	deps.P5.Anchors = anchors
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeAnchors,
		`{"repoId":"7001","subjectKind":"case_version","subjectId":"41"}`)
	if err != nil {
		t.Fatalf("code.anchors: %v", err)
	}
	if len(anchors.calls) != 1 || anchors.calls[0].versionID != testVersionID ||
		anchors.calls[0].subjectKind != "case_version" || anchors.calls[0].subjectID != 41 {
		t.Fatalf("Anchor List call = %+v", anchors.calls)
	}
	list := value.(map[string]any)["anchors"].([]map[string]any)
	if len(list) != 1 || list[0]["id"] != "5001" {
		t.Fatalf("code.anchors = %#v", list)
	}
}

func TestCodeToolMapsServiceErrors(t *testing.T) {
	deps, bindings, _, _, _ := singleRepoDeps(t)
	bindings.err = platform.NotFound("version")
	registry := tools.NewP5Registry(deps)
	_, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeNotFound {
		t.Fatalf("not_found mapping = %+v", toolErr)
	}

	bindings.err = platform.Unavailable("codeintel offline")
	_, err = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeSearch, `{"q":"x"}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeUnavailable {
		t.Fatalf("unavailable mapping = %+v", toolErr)
	}
}

func TestCodeToolsFailWhenPortsAreNotWired(t *testing.T) {
	registry := tools.NewP5Registry(tools.Deps{})
	_, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolCodeTree, `{}`)
	if err == nil {
		t.Fatal("code.tree without wired deps succeeded")
	}
	var toolErr *tools.Error
	if errors.As(err, &toolErr) {
		t.Fatalf("missing dependency must stay an internal error, got %+v", toolErr)
	}
	if !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("missing dependency error = %v", err)
	}
}

func TestSutPageProposeWritesCandidateOnly(t *testing.T) {
	deps, _, _, _, _ := singleRepoDeps(t)
	writer := &fakeSutProposalWriter{result: tools.SutProposalResult{Created: true, ID: 12001, Status: "proposed"}}
	deps.P5.Propose = writer
	registry := tools.NewP5Registry(deps)

	value, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolSUTPagePropose,
		`{"kind":"page","name":"订单列表","url":"http://127.0.0.1:13338/orders","routeTemplate":"/orders","reason":"route fact"}`)
	if err != nil {
		t.Fatalf("sut.page_propose: %v", err)
	}
	if len(writer.calls) != 1 || writer.calls[0].versionID != testVersionID {
		t.Fatalf("propose calls = %+v", writer.calls)
	}
	if input := writer.calls[0].input; input.Kind != "page" || input.Name != "订单列表" || input.RouteTemplate != "/orders" || input.Reason != "route fact" {
		t.Fatalf("propose input = %+v", input)
	}
	if result := value.(map[string]any); result["created"] != true || result["skipped"] != false || result["id"] != "12001" || result["status"] != "proposed" {
		t.Fatalf("sut.page_propose result = %#v", result)
	}

	_, err = execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolSUTPagePropose,
		`{"kind":"api","name":"创建订单","method":"POST","pathTemplate":"/api/orders"}`)
	if err != nil {
		t.Fatalf("sut.page_propose api: %v", err)
	}
	if input := writer.calls[1].input; input.Kind != "api" || input.Method != "POST" || input.PathTemplate != "/api/orders" {
		t.Fatalf("api propose input = %+v", input)
	}

	for _, raw := range []string{
		`{"kind":"component","name":"x"}`,
		`{"kind":"page"}`,
		`{"kind":"api","name":"x"}`,
		`{"kind":"page","name":"x","status":"approved"}`,
	} {
		_, err := execute(t, registry, tools.Scope{VersionID: testVersionID}, tools.ToolSUTPagePropose, raw)
		if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
			t.Fatalf("sut.page_propose(%s) error = %+v", raw, toolErr)
		}
	}

	_, err = execute(t, registry, tools.Scope{}, tools.ToolSUTPagePropose, `{"kind":"page","name":"x"}`)
	if toolErr := toolError(t, err); toolErr.Code != tools.CodeInvalidArgument {
		t.Fatalf("sut.page_propose without version scope error = %+v", toolErr)
	}
}
