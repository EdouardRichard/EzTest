package tools

// This file pins the P4 codeintel read-only tools (design spec §8.1). The tests
// drive the thin-shell contract: registry names/order, valid JSON schemas, the
// argument mapping onto the narrow codeintel ports, the response caps, the
// "summaries only, never code content" rule and coexistence with the P3
// clarification tools.
//
// The tools declare local narrow ports (CodeIntelQueryPort etc.) so this
// infrastructure package never imports another context's infrastructure or
// interfaces; the compile-time assertions below prove the committed codeintel
// application services satisfy those ports structurally.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	codeintelapp "github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	codeinteldomain "github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
)

// Compile-time proof that the committed application services satisfy the tool
// ports. The repository catalog is adapted by the composition root because
// application.RepoInfo is not the tool's local projection type.
var (
	_ CodeIntelQueryPort    = (*codeintelapp.QueryService)(nil)
	_ CodeIntelAnalysisPort = (*codeintelapp.AnalysisService)(nil)
	_ CodeIntelAnchorPort   = (*codeintelapp.AnchorService)(nil)
	_ CodeIntelIndexPort    = (*codeintelapp.IndexService)(nil)
)

// codeIntelTestCommitA and codeIntelTestCommitB are valid lowercase 40-hex
// commit ids used by the argument-mapping tests.
const (
	codeIntelTestCommitA = "1111111111111111111111111111111111111111"
	codeIntelTestCommitB = "2222222222222222222222222222222222222222"
)

// expectedCodeIntelToolNames is the exact spec §8.1 tool set, in registry
// order.
var expectedCodeIntelToolNames = []string{
	"code.repos",
	"code.search",
	"code.diff",
	"code.facts",
	"code.routes",
	"code.apis",
	"code.analyze",
	"code.impact",
	"code.anchors",
}

// fakeCodeIntelRepoCatalog records the repository-list reads.
type fakeCodeIntelRepoCatalog struct {
	repos []CodeIntelRepo
	err   error
	calls int
}

// ListRepos records the call and returns the canned repository projections.
func (f *fakeCodeIntelRepoCatalog) ListRepos(_ context.Context) ([]CodeIntelRepo, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.repos, nil
}

// fakeCodeIntelIndexRuns records the latest-index-run lookups of code.repos.
type fakeCodeIntelIndexRuns struct {
	runs    map[int64]codeinteldomain.IndexRun
	err     error
	queried []int64
}

// GetLatest records the repository id and returns the canned run. A repository
// without an entry is reported as the documented not-found so the tool can
// leave its index block empty.
func (f *fakeCodeIntelIndexRuns) GetLatest(_ context.Context, repoID int64) (codeinteldomain.IndexRun, error) {
	f.queried = append(f.queried, repoID)
	if f.err != nil {
		return codeinteldomain.IndexRun{}, f.err
	}
	run, ok := f.runs[repoID]
	if !ok {
		return codeinteldomain.IndexRun{}, codeinteldomain.NewNotFound("index_run_not_found",
			"codeintel: no index run")
	}
	return run, nil
}

// fakeCodeIntelQueries records the search/diff/facts calls.
type fakeCodeIntelQueries struct {
	searchEntities []codeinteldomain.CodeEntity
	searchMore     bool
	searchErr      error
	searchCalls    int
	searchRepoID   int64
	searchCommit   string
	searchQuery    string
	searchKind     string
	searchLimit    int
	searchCursor   string

	diffData   codeinteldomain.ChangeSetData
	diffErr    error
	diffCalls  int
	diffRepoID int64
	diffBase   string
	diffHead   string
	diffLimit  int
	diffCursor string

	factsResult []codeinteldomain.CodeEntity
	factsMore   bool
	factsErr    error
	factsCalls  int
	factsRepoID int64
	factsFilter codeinteldomain.EntityFilter
}

// Search records the query and returns the canned entity page.
func (f *fakeCodeIntelQueries) Search(_ context.Context, repoID int64, commit, q, kind string, limit int, cursor string) ([]codeinteldomain.CodeEntity, string, bool, error) {
	f.searchCalls++
	f.searchRepoID, f.searchCommit, f.searchQuery, f.searchKind = repoID, commit, q, kind
	f.searchLimit, f.searchCursor = limit, cursor
	if f.searchErr != nil {
		return nil, "", false, f.searchErr
	}
	return f.searchEntities, "next-page", f.searchMore, nil
}

// Diff records the change-list request and returns the canned page.
func (f *fakeCodeIntelQueries) Diff(_ context.Context, repoID int64, base, head string, limit int, cursor string) (codeinteldomain.ChangeSetData, error) {
	f.diffCalls++
	f.diffRepoID, f.diffBase, f.diffHead = repoID, base, head
	f.diffLimit, f.diffCursor = limit, cursor
	if f.diffErr != nil {
		return codeinteldomain.ChangeSetData{}, f.diffErr
	}
	return f.diffData, nil
}

// Facts records the entity filter and returns the canned entity page.
func (f *fakeCodeIntelQueries) Facts(_ context.Context, repoID int64, filter codeinteldomain.EntityFilter) ([]codeinteldomain.CodeEntity, string, bool, error) {
	f.factsCalls++
	f.factsRepoID, f.factsFilter = repoID, filter
	if f.factsErr != nil {
		return nil, "", false, f.factsErr
	}
	return f.factsResult, "next-page", f.factsMore, nil
}

// fakeCodeIntelAnalyses records the analysis trigger/read calls.
type fakeCodeIntelAnalyses struct {
	startResult  codeinteldomain.CodeAnalysis
	startReused  bool
	startErr     error
	startCalls   int
	startVersion int64
	startActor   int64
	startHead    string
	startForce   bool

	latestResult  codeinteldomain.CodeAnalysis
	latestErr     error
	latestCalls   int
	latestVersion int64

	getResult     codeinteldomain.CodeAnalysis
	getErr        error
	getCalls      int
	getVersion    int64
	getAnalysisID int64

	candidates           []codeinteldomain.ImpactCandidate
	candidatesErr        error
	candidatesCalls      int
	candidatesAnalysisID int64
}

// Start records the trigger and returns the canned queued/reused analysis.
func (f *fakeCodeIntelAnalyses) Start(_ context.Context, versionID, actorID int64, headCommit string, force bool) (codeinteldomain.CodeAnalysis, bool, error) {
	f.startCalls++
	f.startVersion, f.startActor, f.startHead, f.startForce = versionID, actorID, headCommit, force
	if f.startErr != nil {
		return codeinteldomain.CodeAnalysis{}, false, f.startErr
	}
	return f.startResult, f.startReused, nil
}

// GetLatest records the version id and returns the canned latest analysis.
func (f *fakeCodeIntelAnalyses) GetLatest(_ context.Context, versionID int64) (codeinteldomain.CodeAnalysis, error) {
	f.latestCalls++
	f.latestVersion = versionID
	if f.latestErr != nil {
		return codeinteldomain.CodeAnalysis{}, f.latestErr
	}
	return f.latestResult, nil
}

// Get records the requested analysis lookup.
func (f *fakeCodeIntelAnalyses) Get(_ context.Context, versionID, analysisID int64) (codeinteldomain.CodeAnalysis, error) {
	f.getCalls++
	f.getVersion, f.getAnalysisID = versionID, analysisID
	if f.getErr != nil {
		return codeinteldomain.CodeAnalysis{}, f.getErr
	}
	return f.getResult, nil
}

// ListCandidates records the analysis id and returns the canned candidates.
func (f *fakeCodeIntelAnalyses) ListCandidates(_ context.Context, analysisID int64) ([]codeinteldomain.ImpactCandidate, error) {
	f.candidatesCalls++
	f.candidatesAnalysisID = analysisID
	if f.candidatesErr != nil {
		return nil, f.candidatesErr
	}
	return f.candidates, nil
}

// fakeCodeIntelAnchors records the anchor list calls.
type fakeCodeIntelAnchors struct {
	result    []codeinteldomain.CodeAnchor
	err       error
	calls     int
	versionID int64
	filter    codeinteldomain.AnchorFilter
}

// List records the version/filter and returns the canned anchors.
func (f *fakeCodeIntelAnchors) List(_ context.Context, versionID int64, filter codeinteldomain.AnchorFilter) ([]codeinteldomain.CodeAnchor, error) {
	f.calls++
	f.versionID, f.filter = versionID, filter
	if f.err != nil {
		return nil, f.err
	}
	return f.result, nil
}

// codeIntelFakes bundles one fake per narrow port.
type codeIntelFakes struct {
	repos    *fakeCodeIntelRepoCatalog
	index    *fakeCodeIntelIndexRuns
	queries  *fakeCodeIntelQueries
	analyses *fakeCodeIntelAnalyses
	anchors  *fakeCodeIntelAnchors
}

// newCodeIntelFakes returns fakes with empty successful state.
func newCodeIntelFakes() *codeIntelFakes {
	return &codeIntelFakes{
		repos:    &fakeCodeIntelRepoCatalog{},
		index:    &fakeCodeIntelIndexRuns{runs: map[int64]codeinteldomain.IndexRun{}},
		queries:  &fakeCodeIntelQueries{},
		analyses: &fakeCodeIntelAnalyses{},
		anchors:  &fakeCodeIntelAnchors{},
	}
}

// deps returns the tool dependencies wired to the fakes.
func (f *codeIntelFakes) deps() CodeIntelToolDeps {
	return CodeIntelToolDeps{
		Repos:    f.repos,
		Indexes:  f.index,
		Queries:  f.queries,
		Analyses: f.analyses,
		Anchors:  f.anchors,
	}
}

// tools requires the full spec §8.1 tool set from the fakes.
func (f *codeIntelFakes) tools(t *testing.T) []domain.Tool {
	t.Helper()
	tools := NewCodeIntelTools(f.deps())
	if len(tools) != len(expectedCodeIntelToolNames) {
		t.Fatalf("NewCodeIntelTools returned %d tools, want %d", len(tools), len(expectedCodeIntelToolNames))
	}
	return tools
}

// codeIntelToolByName indexes tools by registry name.
func codeIntelToolByName(t *testing.T, tools []domain.Tool, name string) domain.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

// executeCodeIntelTool runs one tool with JSON arguments.
func executeCodeIntelTool(t *testing.T, tools []domain.Tool, name, args string) (domain.ToolResult, error) {
	t.Helper()
	return codeIntelToolByName(t, tools, name).Execute(invocationContext(42), json.RawMessage(args))
}

// codeIntelContentMap decodes a successful tool payload and proves it is a JSON
// object.
func codeIntelContentMap(t *testing.T, result domain.ToolResult) map[string]any {
	t.Helper()
	if result.IsError {
		t.Fatalf("tool returned a business error: %s", result.Content)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("tool content is not a JSON object: %v\n%s", err, result.Content)
	}
	return payload
}

// codeIntelObject asserts one decoded value is a JSON object.
func codeIntelObject(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value=%v want a JSON object", value)
	}
	return object
}

// codeIntelArray asserts one decoded value is a JSON array.
func codeIntelArray(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value=%v want a JSON array", value)
	}
	return items
}

// forbiddenCodeContentKeys are the payload keys that would leak source text or
// diffs to the model. Tools may return paths, line numbers, hashes and counts
// only.
var forbiddenCodeContentKeys = map[string]bool{
	"content":   true,
	"patch":     true,
	"signature": true,
	"meta":      true,
	"blobHash":  true,
	"prompt":    true,
	"snippet":   true,
}

// assertNoCodeContentKeys walks a decoded tool payload and fails on any key
// that would carry source content or a diff.
func assertNoCodeContentKeys(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if forbiddenCodeContentKeys[key] {
				t.Fatalf("tool output exposes forbidden content key %q", key)
			}
			assertNoCodeContentKeys(t, child)
		}
	case []any:
		for _, child := range typed {
			assertNoCodeContentKeys(t, child)
		}
	}
}

// codeIntelRequiredSet returns the schema required field set; a schema without
// required is the empty set.
func codeIntelRequiredSet(t *testing.T, schema map[string]any) map[string]bool {
	t.Helper()
	required := map[string]bool{}
	raw, present := schema["required"]
	if !present {
		return required
	}
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("schema required=%v want an array", raw)
	}
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			t.Fatalf("schema required contains a non-string: %v", item)
		}
		required[name] = true
	}
	return required
}

// codeIntelSchemaProperty returns one schema property object.
func codeIntelSchemaProperty(t *testing.T, tool domain.Tool, field string) map[string]any {
	t.Helper()
	schema := schemaObject(t, tool)
	property, ok := propertyMap(t, schema)[field].(map[string]any)
	if !ok {
		t.Fatalf("%s schema is missing property %q", tool.Name(), field)
	}
	return property
}

// codeIntelEntity builds one AI fact whose private fields carry sentinel text
// that must never reach the model.
func codeIntelEntity(path, name, kind string, startLine, endLine int) codeinteldomain.CodeEntity {
	return codeinteldomain.CodeEntity{
		ID:         4242,
		Kind:       kind,
		Path:       path,
		Name:       name,
		StartLine:  &startLine,
		EndLine:    &endLine,
		Signature:  "SECRET signature text",
		BlobHash:   "git-sha1:SECRET",
		Source:     codeinteldomain.SourceAI,
		Confidence: codeinteldomain.ConfidenceHigh,
		Evidence: []codeinteldomain.Evidence{{
			File: path, StartLine: startLine, EndLine: endLine, ExcerptHash: "sha256:deadbeef",
		}},
		Meta: []byte(`{"content":"SECRET"}`),
	}
}

// TestCodeIntelToolNamesAndSchemas pins the nine registry names, their order,
// the non-empty descriptions and the JSON-schema shape of every tool.
func TestCodeIntelToolNamesAndSchemas(t *testing.T) {
	fakes := newCodeIntelFakes()
	tools := fakes.tools(t)

	if len(tools) != len(expectedCodeIntelToolNames) {
		t.Fatalf("tool count=%d want %d", len(tools), len(expectedCodeIntelToolNames))
	}
	seen := map[string]bool{}
	for index, tool := range tools {
		if tool.Name() != expectedCodeIntelToolNames[index] {
			t.Fatalf("tool[%d]=%q want %q", index, tool.Name(), expectedCodeIntelToolNames[index])
		}
		if seen[tool.Name()] {
			t.Fatalf("duplicate tool name %q", tool.Name())
		}
		seen[tool.Name()] = true
		if strings.TrimSpace(tool.Description()) == "" {
			t.Fatalf("%s has an empty description", tool.Name())
		}
		schema := schemaObject(t, tool)
		properties := propertyMap(t, schema)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema must reject additional properties", tool.Name())
		}
		required := codeIntelRequiredSet(t, schema)
		for field := range required {
			if _, present := properties[field]; !present {
				t.Fatalf("%s requires %q but does not declare it", tool.Name(), field)
			}
		}
		for _, forbidden := range []string{"actor", "actorId"} {
			if _, present := properties[forbidden]; present {
				t.Fatalf("%s schema exposes a model-supplied %s", tool.Name(), forbidden)
			}
		}
	}

	byName := map[string]domain.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}

	wantRequired := map[string][]string{
		"code.repos":   {},
		"code.search":  {"repoId", "q"},
		"code.diff":    {"repoId", "base", "head"},
		"code.facts":   {"repoId", "commit"},
		"code.routes":  {"repoId", "commit"},
		"code.apis":    {"repoId", "commit"},
		"code.analyze": {"versionId"},
		"code.impact":  {"versionId"},
		"code.anchors": {"versionId"},
	}
	for name, expected := range wantRequired {
		required := codeIntelRequiredSet(t, schemaObject(t, byName[name]))
		if len(required) != len(expected) {
			t.Fatalf("%s required=%v want %v", name, required, expected)
		}
		for _, field := range expected {
			if !required[field] {
				t.Fatalf("%s required=%v is missing %q", name, required, field)
			}
		}
	}

	t.Run("repo and version identifiers keep their wire types", func(t *testing.T) {
		if property := codeIntelSchemaProperty(t, byName["code.search"], "repoId"); property["type"] != "integer" {
			t.Fatalf("code.search repoId type=%v want integer", property["type"])
		}
		for _, name := range []string{"code.analyze", "code.impact", "code.anchors"} {
			if property := codeIntelSchemaProperty(t, byName[name], "versionId"); property["type"] != "string" {
				t.Fatalf("%s versionId type=%v want string", name, property["type"])
			}
		}
		for _, name := range []string{"code.impact"} {
			if property := codeIntelSchemaProperty(t, byName[name], "analysisId"); property["type"] != "string" {
				t.Fatalf("%s analysisId type=%v want string", name, property["type"])
			}
		}
		for _, name := range []string{"code.anchors"} {
			if property := codeIntelSchemaProperty(t, byName[name], "subjectId"); property["type"] != "string" {
				t.Fatalf("%s subjectId type=%v want string", name, property["type"])
			}
		}
	})

	t.Run("commits are lowercase 40-hex", func(t *testing.T) {
		type commitField struct{ tool, field string }
		fields := []commitField{
			{"code.search", "commit"},
			{"code.diff", "base"},
			{"code.diff", "head"},
			{"code.facts", "commit"},
			{"code.routes", "commit"},
			{"code.apis", "commit"},
			{"code.analyze", "headCommit"},
		}
		for _, item := range fields {
			property := codeIntelSchemaProperty(t, byName[item.tool], item.field)
			if property["type"] != "string" || property["pattern"] != "^[0-9a-f]{40}$" {
				t.Fatalf("%s.%s schema=%v want a 40-hex string", item.tool, item.field, property)
			}
		}
	})

	t.Run("limits stay inside the documented 1-200 bound", func(t *testing.T) {
		for _, name := range []string{"code.search", "code.diff", "code.facts", "code.routes", "code.apis"} {
			property := codeIntelSchemaProperty(t, byName[name], "limit")
			if property["type"] != "integer" || property["minimum"] != float64(1) || property["maximum"] != float64(200) {
				t.Fatalf("%s limit schema=%v want integer 1..200", name, property)
			}
		}
	})

	t.Run("a nil port disables the whole tool set", func(t *testing.T) {
		if NewCodeIntelTools(CodeIntelToolDeps{}) != nil {
			t.Fatal("NewCodeIntelTools with no deps must return nil")
		}
		full := fakes.deps()
		cases := []CodeIntelToolDeps{
			{Indexes: full.Indexes, Queries: full.Queries, Analyses: full.Analyses, Anchors: full.Anchors},
			{Repos: full.Repos, Queries: full.Queries, Analyses: full.Analyses, Anchors: full.Anchors},
			{Repos: full.Repos, Indexes: full.Indexes, Analyses: full.Analyses, Anchors: full.Anchors},
			{Repos: full.Repos, Indexes: full.Indexes, Queries: full.Queries, Anchors: full.Anchors},
			{Repos: full.Repos, Indexes: full.Indexes, Queries: full.Queries, Analyses: full.Analyses},
		}
		for index, deps := range cases {
			if NewCodeIntelTools(deps) != nil {
				t.Fatalf("deps case %d: a missing port must disable the tool set", index)
			}
		}
	})
}

// TestCodeIntelReposListIndexStatus pins code.repos: repository list with the
// latest index-run projection, the 50-repository cap and the not-found
// handling of a never-indexed repository.
func TestCodeIntelReposListIndexStatus(t *testing.T) {
	fakes := newCodeIntelFakes()
	indexedAt := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	startedAt := time.Date(2026, 10, 28, 1, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 10, 28, 1, 2, 3, 0, time.UTC)
	fakes.repos.repos = []CodeIntelRepo{
		{ID: 3, Key: "web", Name: "Web", Status: "active", FixedBranch: "main", LastIndexedAt: &indexedAt},
		{ID: 4, Key: "api", Name: "API", Status: "active", FixedBranch: "main"},
	}
	fakes.index.runs[3] = codeinteldomain.IndexRun{
		ID: 123456789012345, CommitSHA: codeIntelTestCommitA, Status: codeinteldomain.RunDone,
		StartedAt: &startedAt, FinishedAt: &finishedAt,
	}
	tools := fakes.tools(t)

	result, err := executeCodeIntelTool(t, tools, "code.repos", `{}`)
	if err != nil {
		t.Fatalf("code.repos returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)

	repos := codeIntelArray(t, content["repos"])
	if len(repos) != 2 {
		t.Fatalf("repos=%d want 2", len(repos))
	}
	first := codeIntelObject(t, repos[0])
	if first["id"] != float64(3) || first["key"] != "web" || first["status"] != "active" {
		t.Fatalf("first repo=%v", first)
	}
	index := codeIntelObject(t, first["index"])
	if index["runId"] != "123456789012345" {
		t.Fatalf("index runId=%v want the decimal string", index["runId"])
	}
	if index["status"] != codeinteldomain.RunDone || index["commit"] != codeIntelTestCommitA {
		t.Fatalf("index=%v", index)
	}
	second := codeIntelObject(t, repos[1])
	if _, present := second["index"]; present {
		t.Fatalf("a never-indexed repository must not carry an index block: %v", second)
	}
	if content["truncated"] != false {
		t.Fatalf("truncated=%v want false", content["truncated"])
	}
	if len(fakes.index.queried) != 2 || fakes.index.queried[0] != 3 || fakes.index.queried[1] != 4 {
		t.Fatalf("index lookups=%v want [3 4]", fakes.index.queried)
	}

	t.Run("the repository list is capped at 50", func(t *testing.T) {
		capped := newCodeIntelFakes()
		for index := 1; index <= 60; index++ {
			capped.repos.repos = append(capped.repos.repos, CodeIntelRepo{
				ID: int64(index), Key: "repo", Name: "Repo", Status: "active", FixedBranch: "main",
			})
		}
		result, err := executeCodeIntelTool(t, capped.tools(t), "code.repos", `{}`)
		if err != nil {
			t.Fatalf("code.repos returned an infrastructure error: %v", err)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["repos"])) != 50 {
			t.Fatalf("repos=%d want the 50 cap", len(codeIntelArray(t, content["repos"])))
		}
		if content["truncated"] != true {
			t.Fatal("a capped repository list must set truncated=true")
		}
		if len(capped.index.queried) != 50 {
			t.Fatalf("index lookups=%d want only the first 50 repositories", len(capped.index.queried))
		}
	})

	t.Run("repository and index failures propagate", func(t *testing.T) {
		failing := newCodeIntelFakes()
		failing.repos.err = errors.New("repo catalog down")
		if _, err := executeCodeIntelTool(t, failing.tools(t), "code.repos", `{}`); err == nil {
			t.Fatal("a repository catalog failure must surface as an error")
		}

		failingIndex := newCodeIntelFakes()
		failingIndex.repos.repos = []CodeIntelRepo{{ID: 3, Status: "active"}}
		failingIndex.index.err = errors.New("index repository down")
		if _, err := executeCodeIntelTool(t, failingIndex.tools(t), "code.repos", `{}`); err == nil {
			t.Fatal("an index-run read failure must surface as an error")
		}
	})
}

// TestCodeIntelSearchClampsLimitAndOmitsContent pins code.search: the 1-200
// request bound, the 50-summary cap and the path/name/kind/confidence-only
// projection.
func TestCodeIntelSearchClampsLimitAndOmitsContent(t *testing.T) {
	fakes := newCodeIntelFakes()
	for index := 0; index < 60; index++ {
		fakes.queries.searchEntities = append(fakes.queries.searchEntities,
			codeIntelEntity("src/file"+strings.Repeat("x", index%3)+".go", "Name", codeinteldomain.KindAPI, 4, 6))
	}
	fakes.queries.searchMore = true
	tools := fakes.tools(t)

	result, err := executeCodeIntelTool(t, tools, "code.search", `{"repoId":7,"q":"refund","limit":1000}`)
	if err != nil {
		t.Fatalf("code.search returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)
	if strings.Contains(result.Content, "SECRET") {
		t.Fatalf("search leaked private entity fields: %s", result.Content)
	}

	if fakes.queries.searchCalls != 1 {
		t.Fatalf("search calls=%d want 1", fakes.queries.searchCalls)
	}
	if fakes.queries.searchLimit != 200 {
		t.Fatalf("search limit=%d want the clamped 200", fakes.queries.searchLimit)
	}
	if fakes.queries.searchRepoID != 7 || fakes.queries.searchQuery != "refund" || fakes.queries.searchCommit != "" {
		t.Fatalf("search args=%d/%q/%q", fakes.queries.searchRepoID, fakes.queries.searchQuery, fakes.queries.searchCommit)
	}
	results := codeIntelArray(t, content["results"])
	if len(results) != 50 {
		t.Fatalf("results=%d want the 50-summary cap", len(results))
	}
	for _, raw := range results {
		entry := codeIntelObject(t, raw)
		if len(entry) != 4 {
			t.Fatalf("search summary=%v want exactly path/name/kind/confidence", entry)
		}
		for _, field := range []string{"path", "name", "kind", "confidence"} {
			if _, present := entry[field]; !present {
				t.Fatalf("search summary=%v is missing %q", entry, field)
			}
		}
	}
	if content["truncated"] != true {
		t.Fatal("a capped search result must set truncated=true")
	}
	if content["hasMore"] != true {
		t.Fatal("the service hasMore signal must be forwarded")
	}

	t.Run("the limit defaults to the 50-summary cap", func(t *testing.T) {
		defaulted := newCodeIntelFakes()
		defaulted.queries.searchEntities = []codeinteldomain.CodeEntity{
			codeIntelEntity("src/a.go", "A", codeinteldomain.KindFile, 1, 1),
		}
		result, err := executeCodeIntelTool(t, defaulted.tools(t), "code.search", `{"repoId":7,"q":"refund"}`)
		if err != nil {
			t.Fatalf("code.search returned an infrastructure error: %v", err)
		}
		if defaulted.queries.searchLimit != 50 {
			t.Fatalf("search limit=%d want the 50 default", defaulted.queries.searchLimit)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["results"])) != 1 || content["truncated"] != false {
			t.Fatalf("search output=%v", content)
		}
	})

	t.Run("optional commit and kind filters are forwarded", func(t *testing.T) {
		filtered := newCodeIntelFakes()
		filtered.queries.searchEntities = []codeinteldomain.CodeEntity{
			codeIntelEntity("src/a.go", "A", codeinteldomain.KindRoute, 1, 1),
		}
		args := `{"repoId":7,"q":"orders","commit":"` + codeIntelTestCommitA + `","kind":"route","limit":5}`
		if _, err := executeCodeIntelTool(t, filtered.tools(t), "code.search", args); err != nil {
			t.Fatalf("code.search returned an infrastructure error: %v", err)
		}
		if filtered.queries.searchCommit != codeIntelTestCommitA ||
			filtered.queries.searchKind != "route" ||
			filtered.queries.searchLimit != 5 {
			t.Fatalf("search filter=%q/%q/%d", filtered.queries.searchCommit, filtered.queries.searchKind, filtered.queries.searchLimit)
		}
	})
}

// TestCodeIntelDiffSummary pins code.diff: file-level summaries plus stats, no
// patch/old-path/blob-hash, and the 200-file cap.
func TestCodeIntelDiffSummary(t *testing.T) {
	oldPath := "src/old.go"
	headHash := "git-sha1:SECRET"
	fakes := newCodeIntelFakes()
	fakes.queries.diffData = codeinteldomain.ChangeSetData{
		Stats: []byte(`{"changedFiles":2,"additions":5,"deletions":1,"truncated":false}`),
		Files: []codeinteldomain.ChangedFile{
			{Path: "src/a.go", Status: "M", OldPath: &oldPath, Additions: 3, Deletions: 1, HeadBlobHash: headHash},
			{Path: "assets/logo.png", Status: "A", Binary: true, HeadBlobHash: "git-sha1:1"},
		},
	}
	tools := fakes.tools(t)
	args := `{"repoId":7,"base":"` + codeIntelTestCommitA + `","head":"` + codeIntelTestCommitB + `"}`

	result, err := executeCodeIntelTool(t, tools, "code.diff", args)
	if err != nil {
		t.Fatalf("code.diff returned an infrastructure error: %v", err)
	}
	if strings.Contains(result.Content, "SECRET") {
		t.Fatalf("diff leaked a blob hash/old path: %s", result.Content)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)

	files := codeIntelArray(t, content["files"])
	if len(files) != 2 {
		t.Fatalf("files=%d want 2", len(files))
	}
	first := codeIntelObject(t, files[0])
	if len(first) != 5 {
		t.Fatalf("changed file=%v want exactly path/status/additions/deletions/binary", first)
	}
	if first["path"] != "src/a.go" || first["status"] != "M" ||
		first["additions"] != float64(3) || first["deletions"] != float64(1) || first["binary"] != false {
		t.Fatalf("changed file=%v", first)
	}
	if codeIntelObject(t, files[1])["binary"] != true {
		t.Fatalf("binary file=%v", files[1])
	}
	stats := codeIntelObject(t, content["stats"])
	if stats["changedFiles"] != float64(2) || stats["additions"] != float64(5) || stats["deletions"] != float64(1) {
		t.Fatalf("stats=%v", stats)
	}
	if content["truncated"] != false {
		t.Fatalf("truncated=%v want false", content["truncated"])
	}
	if fakes.queries.diffRepoID != 7 || fakes.queries.diffBase != codeIntelTestCommitA ||
		fakes.queries.diffHead != codeIntelTestCommitB || fakes.queries.diffLimit != 200 {
		t.Fatalf("diff args=%d/%q/%q/%d", fakes.queries.diffRepoID, fakes.queries.diffBase, fakes.queries.diffHead, fakes.queries.diffLimit)
	}

	t.Run("the change list is capped at 200 files", func(t *testing.T) {
		capped := newCodeIntelFakes()
		for index := 0; index < 250; index++ {
			capped.queries.diffData.Files = append(capped.queries.diffData.Files,
				codeinteldomain.ChangedFile{Path: "src/file.go", Status: "M"})
		}
		result, err := executeCodeIntelTool(t, capped.tools(t), "code.diff", args)
		if err != nil {
			t.Fatalf("code.diff returned an infrastructure error: %v", err)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["files"])) != 200 || content["truncated"] != true {
			t.Fatalf("capped diff files=%d truncated=%v", len(codeIntelArray(t, content["files"])), content["truncated"])
		}
	})

	t.Run("an absent stats document becomes an empty object", func(t *testing.T) {
		empty := newCodeIntelFakes()
		result, err := executeCodeIntelTool(t, empty.tools(t), "code.diff", args)
		if err != nil {
			t.Fatalf("code.diff returned an infrastructure error: %v", err)
		}
		stats := codeIntelObject(t, codeIntelContentMap(t, result)["stats"])
		if len(stats) != 0 {
			t.Fatalf("stats=%v want {}", stats)
		}
	})
}

// TestCodeIntelFactsRoutesAPIsFilters pins the facts/routes/apis tools: the
// fixed kind filters, the optional path filter, the 100-summary cap, the
// 1-200 request limit and the evidence-reference-only projection.
func TestCodeIntelFactsRoutesAPIsFilters(t *testing.T) {
	fakes := newCodeIntelFakes()
	for index := 0; index < 120; index++ {
		fakes.queries.factsResult = append(fakes.queries.factsResult,
			codeIntelEntity("server/refund.go", "POST /api/refund", codeinteldomain.KindAPI, 13, 15))
	}
	fakes.queries.factsMore = true
	tools := fakes.tools(t)
	commitArgs := `"repoId":7,"commit":"` + codeIntelTestCommitA + `"`

	result, err := executeCodeIntelTool(t, tools, "code.facts",
		`{`+commitArgs+`,"kind":"api","path":"server","limit":1000}`)
	if err != nil {
		t.Fatalf("code.facts returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)
	if strings.Contains(result.Content, "SECRET") {
		t.Fatalf("facts leaked private entity fields: %s", result.Content)
	}
	wantFilter := codeinteldomain.EntityFilter{
		Commit: codeIntelTestCommitA,
		Kind:   codeinteldomain.KindAPI,
		Path:   "server",
		Limit:  200,
	}
	if fakes.queries.factsRepoID != 7 || fakes.queries.factsFilter != wantFilter {
		t.Fatalf("facts filter=%+v want %+v", fakes.queries.factsFilter, wantFilter)
	}
	facts := codeIntelArray(t, content["facts"])
	if len(facts) != 100 || content["truncated"] != true || content["hasMore"] != true {
		t.Fatalf("facts=%d truncated=%v hasMore=%v", len(facts), content["truncated"], content["hasMore"])
	}
	first := codeIntelObject(t, facts[0])
	if len(first) != 7 {
		t.Fatalf("fact summary=%v want kind/name/path/startLine/endLine/confidence/evidence", first)
	}
	if first["kind"] != codeinteldomain.KindAPI || first["name"] != "POST /api/refund" ||
		first["path"] != "server/refund.go" || first["confidence"] != codeinteldomain.ConfidenceHigh ||
		first["startLine"] != float64(13) || first["endLine"] != float64(15) {
		t.Fatalf("fact summary=%v", first)
	}
	evidence := codeIntelArray(t, first["evidence"])
	if len(evidence) != 1 {
		t.Fatalf("evidence=%v want one reference", evidence)
	}
	reference := codeIntelObject(t, evidence[0])
	if reference["file"] != "server/refund.go" || reference["excerptHash"] != "sha256:deadbeef" {
		t.Fatalf("evidence reference=%v", reference)
	}
	if _, contentKey := reference["content"]; contentKey {
		t.Fatal("evidence reference carries content")
	}

	t.Run("routes query the route kind", func(t *testing.T) {
		routesFake := newCodeIntelFakes()
		routesFake.queries.factsResult = []codeinteldomain.CodeEntity{
			codeIntelEntity("src/router.ts", "/orders", codeinteldomain.KindRoute, 5, 7),
		}
		result, err := executeCodeIntelTool(t, routesFake.tools(t), "code.routes", `{`+commitArgs+`}`)
		if err != nil {
			t.Fatalf("code.routes returned an infrastructure error: %v", err)
		}
		want := codeinteldomain.EntityFilter{
			Commit: codeIntelTestCommitA,
			Kind:   codeinteldomain.KindRoute,
			Limit:  100,
		}
		if routesFake.queries.factsFilter != want {
			t.Fatalf("routes filter=%+v want %+v", routesFake.queries.factsFilter, want)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["facts"])) != 1 || content["truncated"] != false {
			t.Fatalf("routes output=%v", content)
		}
	})

	t.Run("apis query the api kind", func(t *testing.T) {
		apisFake := newCodeIntelFakes()
		apisFake.queries.factsResult = []codeinteldomain.CodeEntity{
			codeIntelEntity("server/refund.go", "POST /api/refund", codeinteldomain.KindAPI, 13, 15),
		}
		if _, err := executeCodeIntelTool(t, apisFake.tools(t), "code.apis", `{`+commitArgs+`,"limit":3}`); err != nil {
			t.Fatalf("code.apis returned an infrastructure error: %v", err)
		}
		want := codeinteldomain.EntityFilter{
			Commit: codeIntelTestCommitA,
			Kind:   codeinteldomain.KindAPI,
			Limit:  3,
		}
		if apisFake.queries.factsFilter != want {
			t.Fatalf("apis filter=%+v want %+v", apisFake.queries.factsFilter, want)
		}
	})
}

// TestCodeIntelAnalyzeTriggersWithoutBlocking pins code.analyze: the run-scoped
// actor is used, the trigger is called exactly once and the tool returns the
// queued/reused status without polling the analysis.
func TestCodeIntelAnalyzeTriggersWithoutBlocking(t *testing.T) {
	fakes := newCodeIntelFakes()
	fakes.analyses.startResult = codeinteldomain.CodeAnalysis{
		ID: 9007199254740993, Status: codeinteldomain.RunQueued,
	}
	tools := fakes.tools(t)
	args := `{"versionId":"77","headCommit":"` + codeIntelTestCommitA + `","force":true}`

	result, err := codeIntelToolByName(t, tools, "code.analyze").Execute(invocationContext(42), json.RawMessage(args))
	if err != nil {
		t.Fatalf("code.analyze returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	if len(content) != 3 {
		t.Fatalf("analyze output=%v want exactly analysisId/status/reused", content)
	}
	if content["analysisId"] != "9007199254740993" {
		t.Fatalf("analysisId=%v want the decimal string", content["analysisId"])
	}
	if content["status"] != codeinteldomain.RunQueued || content["reused"] != false {
		t.Fatalf("analyze output=%v", content)
	}
	if fakes.analyses.startCalls != 1 ||
		fakes.analyses.startVersion != 77 ||
		fakes.analyses.startActor != 42 ||
		fakes.analyses.startHead != codeIntelTestCommitA ||
		!fakes.analyses.startForce {
		t.Fatalf("start mapping=%+v", fakes.analyses)
	}
	if fakes.analyses.latestCalls != 0 || fakes.analyses.candidatesCalls != 0 {
		t.Fatal("code.analyze must not wait for the run by polling its status")
	}

	t.Run("a reused analysis reports reused=true", func(t *testing.T) {
		reusedFake := newCodeIntelFakes()
		reusedFake.analyses.startResult = codeinteldomain.CodeAnalysis{ID: 12, Status: codeinteldomain.RunDone}
		reusedFake.analyses.startReused = true
		result, err := executeCodeIntelTool(t, reusedFake.tools(t), "code.analyze", `{"versionId":"77"}`)
		if err != nil {
			t.Fatalf("code.analyze returned an infrastructure error: %v", err)
		}
		content := codeIntelContentMap(t, result)
		if content["reused"] != true || content["status"] != codeinteldomain.RunDone || content["analysisId"] != "12" {
			t.Fatalf("reused output=%v", content)
		}
	})

	t.Run("a missing run-scoped actor fails closed", func(t *testing.T) {
		noActor := newCodeIntelFakes()
		tool := codeIntelToolByName(t, noActor.tools(t), "code.analyze")
		result, err := tool.Execute(context.Background(), json.RawMessage(`{"versionId":"77"}`))
		if err != nil {
			t.Fatalf("code.analyze without an actor returned an infrastructure error: %v", err)
		}
		if !result.IsError || !strings.Contains(result.Content, "invalid_arguments") {
			t.Fatalf("code.analyze without an actor=%+v", result)
		}
		if noActor.analyses.startCalls != 0 {
			t.Fatal("code.analyze without an actor reached the analysis service")
		}
	})
}

// TestCodeIntelImpactReturnsCandidatesSummary pins code.impact: latest versus
// requested analysis selection and the string-id candidate summary projection.
func TestCodeIntelImpactReturnsCandidatesSummary(t *testing.T) {
	fakes := newCodeIntelFakes()
	fakes.analyses.latestResult = codeinteldomain.CodeAnalysis{ID: 555, Status: codeinteldomain.RunDone}
	fakes.analyses.candidates = []codeinteldomain.ImpactCandidate{
		{
			ID: 1, SubjectKind: codeinteldomain.CandidateRequirement, SubjectID: 9007199254740993,
			Reason: "changed api POST /api/refund", Confidence: codeinteldomain.ConfidenceHigh,
			Source: codeinteldomain.CandidateDirect, Selected: true,
			Evidence: []codeinteldomain.Evidence{{File: "server/refund.go", StartLine: 13, EndLine: 15}},
		},
		{
			ID: 2, SubjectKind: codeinteldomain.CandidateAppModel, SubjectID: 42,
			Reason: "changed route /orders", Confidence: codeinteldomain.ConfidenceLow,
			Source: codeinteldomain.CandidateAI, Selected: false,
		},
	}
	tools := fakes.tools(t)

	result, err := executeCodeIntelTool(t, tools, "code.impact", `{"versionId":"77"}`)
	if err != nil {
		t.Fatalf("code.impact returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)
	if fakes.analyses.latestCalls != 1 || fakes.analyses.latestVersion != 77 || fakes.analyses.candidatesAnalysisID != 555 {
		t.Fatalf("impact latest mapping=%+v", fakes.analyses)
	}
	if content["analysisId"] != "555" || content["status"] != codeinteldomain.RunDone {
		t.Fatalf("impact analysis=%v", content)
	}
	candidates := codeIntelArray(t, content["candidates"])
	if len(candidates) != 2 {
		t.Fatalf("candidates=%d want 2", len(candidates))
	}
	first := codeIntelObject(t, candidates[0])
	if len(first) != 5 {
		t.Fatalf("candidate=%v want exactly subjectKind/subjectId/reason/confidence/selected", first)
	}
	if first["subjectKind"] != codeinteldomain.CandidateRequirement ||
		first["subjectId"] != "9007199254740993" ||
		first["reason"] != "changed api POST /api/refund" ||
		first["confidence"] != codeinteldomain.ConfidenceHigh ||
		first["selected"] != true {
		t.Fatalf("candidate=%v", first)
	}
	second := codeIntelObject(t, candidates[1])
	if second["subjectKind"] != codeinteldomain.CandidateAppModel || second["subjectId"] != "42" || second["selected"] != false {
		t.Fatalf("second candidate=%v", second)
	}

	t.Run("an explicit analysisId reads that analysis", func(t *testing.T) {
		requested := newCodeIntelFakes()
		requested.analyses.getResult = codeinteldomain.CodeAnalysis{ID: 666, Status: codeinteldomain.RunDone}
		requested.analyses.candidates = []codeinteldomain.ImpactCandidate{}
		result, err := executeCodeIntelTool(t, requested.tools(t), "code.impact",
			`{"versionId":"77","analysisId":"666"}`)
		if err != nil {
			t.Fatalf("code.impact returned an infrastructure error: %v", err)
		}
		if requested.analyses.getCalls != 1 || requested.analyses.getVersion != 77 || requested.analyses.getAnalysisID != 666 {
			t.Fatalf("impact get mapping=%+v", requested.analyses)
		}
		if requested.analyses.latestCalls != 0 {
			t.Fatal("an explicit analysisId must not fall back to the latest analysis")
		}
		content := codeIntelContentMap(t, result)
		if content["analysisId"] != "666" || len(codeIntelArray(t, content["candidates"])) != 0 {
			t.Fatalf("requested impact=%v", content)
		}
	})

	t.Run("the candidate list is capped at 200", func(t *testing.T) {
		capped := newCodeIntelFakes()
		capped.analyses.latestResult = codeinteldomain.CodeAnalysis{ID: 555, Status: codeinteldomain.RunDone}
		for index := 0; index < 250; index++ {
			capped.analyses.candidates = append(capped.analyses.candidates, codeinteldomain.ImpactCandidate{
				SubjectKind: codeinteldomain.CandidateRequirement, SubjectID: int64(index + 1),
				Reason: "reason", Confidence: codeinteldomain.ConfidenceHigh,
			})
		}
		result, err := executeCodeIntelTool(t, capped.tools(t), "code.impact", `{"versionId":"77"}`)
		if err != nil {
			t.Fatalf("code.impact returned an infrastructure error: %v", err)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["candidates"])) != 200 || content["truncated"] != true {
			t.Fatalf("capped impact=%d/%v", len(codeIntelArray(t, content["candidates"])), content["truncated"])
		}
	})

	t.Run("a missing analysis is a correctable business failure", func(t *testing.T) {
		missing := newCodeIntelFakes()
		missing.analyses.latestErr = codeinteldomain.NewNotFound("analysis_not_found",
			"codeintel: version 77 has no analysis")
		result, err := executeCodeIntelTool(t, missing.tools(t), "code.impact", `{"versionId":"77"}`)
		if err != nil {
			t.Fatalf("code.impact returned an infrastructure error: %v", err)
		}
		if !result.IsError || !strings.Contains(result.Content, "analysis_not_found") {
			t.Fatalf("missing analysis result=%+v", result)
		}
	})
}

// TestCodeIntelAnchorsList pins code.anchors: the version-scoped subject filter,
// the string-id projection and the 200-anchor cap.
func TestCodeIntelAnchorsList(t *testing.T) {
	fakes := newCodeIntelFakes()
	startLine, endLine := 10, 12
	fakes.anchors.result = []codeinteldomain.CodeAnchor{
		{
			ID: 777, VersionID: 77, SubjectKind: codeinteldomain.SubjectRequirement,
			SubjectID: 9007199254740993, FilePath: "src/a.go",
			StartLine: &startLine, EndLine: &endLine,
			Confidence: codeinteldomain.ConfidenceHigh, CreatedByKind: "user",
		},
		{
			ID: 778, VersionID: 77, SubjectKind: codeinteldomain.SubjectVersion,
			SubjectID: 77, FilePath: "src/b.go",
			Confidence: codeinteldomain.ConfidenceHigh, CreatedByKind: "user",
		},
	}
	tools := fakes.tools(t)

	result, err := executeCodeIntelTool(t, tools, "code.anchors",
		`{"versionId":"77","subjectKind":"requirement","subjectId":"9007199254740993"}`)
	if err != nil {
		t.Fatalf("code.anchors returned an infrastructure error: %v", err)
	}
	content := codeIntelContentMap(t, result)
	assertNoCodeContentKeys(t, content)
	if fakes.anchors.versionID != 77 ||
		fakes.anchors.filter.SubjectKind != codeinteldomain.SubjectRequirement ||
		fakes.anchors.filter.SubjectID == nil || *fakes.anchors.filter.SubjectID != 9007199254740993 {
		t.Fatalf("anchor filter=%+v", fakes.anchors.filter)
	}
	anchors := codeIntelArray(t, content["anchors"])
	if len(anchors) != 2 {
		t.Fatalf("anchors=%d want 2", len(anchors))
	}
	first := codeIntelObject(t, anchors[0])
	if first["id"] != "777" || first["versionId"] != "77" ||
		first["subjectKind"] != codeinteldomain.SubjectRequirement ||
		first["subjectId"] != "9007199254740993" ||
		first["filePath"] != "src/a.go" ||
		first["startLine"] != float64(10) || first["endLine"] != float64(12) ||
		first["confidence"] != codeinteldomain.ConfidenceHigh ||
		first["createdByKind"] != "user" {
		t.Fatalf("anchor=%v", first)
	}
	second := codeIntelObject(t, anchors[1])
	if _, present := second["startLine"]; present {
		t.Fatalf("an anchor without lines must not carry startLine: %v", second)
	}

	t.Run("an empty filter lists every anchor", func(t *testing.T) {
		all := newCodeIntelFakes()
		if _, err := executeCodeIntelTool(t, all.tools(t), "code.anchors", `{"versionId":"77"}`); err != nil {
			t.Fatalf("code.anchors returned an infrastructure error: %v", err)
		}
		if all.anchors.filter.SubjectKind != "" || all.anchors.filter.SubjectID != nil {
			t.Fatalf("anchor filter=%+v want empty", all.anchors.filter)
		}
	})

	t.Run("the anchor list is capped at 200", func(t *testing.T) {
		capped := newCodeIntelFakes()
		for index := 0; index < 250; index++ {
			capped.anchors.result = append(capped.anchors.result, codeinteldomain.CodeAnchor{
				ID: int64(index + 1), VersionID: 77, SubjectKind: codeinteldomain.SubjectVersion,
				SubjectID: 77, FilePath: "src/a.go", Confidence: codeinteldomain.ConfidenceHigh,
			})
		}
		result, err := executeCodeIntelTool(t, capped.tools(t), "code.anchors", `{"versionId":"77"}`)
		if err != nil {
			t.Fatalf("code.anchors returned an infrastructure error: %v", err)
		}
		content := codeIntelContentMap(t, result)
		if len(codeIntelArray(t, content["anchors"])) != 200 || content["truncated"] != true {
			t.Fatalf("capped anchors=%d/%v", len(codeIntelArray(t, content["anchors"])), content["truncated"])
		}
	})

	t.Run("an unsupported subject kind is rejected before the port", func(t *testing.T) {
		invalid := newCodeIntelFakes()
		result, err := executeCodeIntelTool(t, invalid.tools(t), "code.anchors",
			`{"versionId":"77","subjectKind":"case"}`)
		if err != nil {
			t.Fatalf("code.anchors returned an infrastructure error: %v", err)
		}
		if !result.IsError || !strings.Contains(result.Content, "invalid_arguments") {
			t.Fatalf("invalid subject kind result=%+v", result)
		}
		if invalid.anchors.calls != 0 {
			t.Fatal("an invalid subject kind reached the anchor service")
		}
	})
}

// TestCodeIntelToolErrors pins the invalid-argument handling and the
// business/infrastructure error split inherited from the P3 tools.
func TestCodeIntelToolErrors(t *testing.T) {
	t.Run("invalid arguments are correctable and never reach a port", func(t *testing.T) {
		cases := []struct{ name, args string }{
			{"code.repos", `{`},
			{"code.search", `{"repoId":0,"q":"refund"}`},
			{"code.search", `{"repoId":7,"q":""}`},
			{"code.diff", `{"repoId":7,"base":"abc","head":"` + codeIntelTestCommitB + `"}`},
			{"code.facts", `{"repoId":7}`},
			{"code.facts", `{"repoId":7,"commit":"FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF"}`},
			{"code.routes", `{"repoId":7}`},
			{"code.apis", `{"repoId":7}`},
			{"code.analyze", `{"versionId":"-1"}`},
			{"code.impact", `{"versionId":"77","analysisId":"0"}`},
			{"code.anchors", `{"versionId":"77","subjectKind":"case"}`},
			{"code.anchors", `{"versionId":"77","subjectId":"abc"}`},
		}
		for _, testCase := range cases {
			t.Run(testCase.name+" "+testCase.args, func(t *testing.T) {
				fakes := newCodeIntelFakes()
				tools := fakes.tools(t)
				result, err := executeCodeIntelTool(t, tools, testCase.name, testCase.args)
				if err != nil {
					t.Fatalf("%s invalid arguments returned an infrastructure error: %v", testCase.name, err)
				}
				if !result.IsError || !strings.Contains(result.Content, "invalid_arguments") {
					t.Fatalf("%s invalid arguments=%+v", testCase.name, result)
				}
				if fakes.repos.calls != 0 || fakes.queries.searchCalls != 0 || fakes.queries.diffCalls != 0 ||
					fakes.queries.factsCalls != 0 || fakes.analyses.startCalls != 0 ||
					fakes.analyses.latestCalls != 0 || fakes.anchors.calls != 0 {
					t.Fatal("an invalid argument reached a codeintel port")
				}
			})
		}
	})

	t.Run("typed codeintel errors become correctable business failures", func(t *testing.T) {
		fakes := newCodeIntelFakes()
		business := codeinteldomain.NewConflict("tool_business_error", "the resource is busy")
		fakes.repos.err = business
		fakes.index.err = business
		fakes.queries.searchErr = business
		fakes.queries.diffErr = business
		fakes.queries.factsErr = business
		fakes.analyses.startErr = business
		fakes.analyses.latestErr = business
		fakes.analyses.getErr = business
		fakes.analyses.candidatesErr = business
		fakes.anchors.err = business
		tools := fakes.tools(t)
		args := map[string]string{
			"code.repos":   `{}`,
			"code.search":  `{"repoId":7,"q":"refund"}`,
			"code.diff":    `{"repoId":7,"base":"` + codeIntelTestCommitA + `","head":"` + codeIntelTestCommitB + `"}`,
			"code.facts":   `{"repoId":7,"commit":"` + codeIntelTestCommitA + `"}`,
			"code.routes":  `{"repoId":7,"commit":"` + codeIntelTestCommitA + `"}`,
			"code.apis":    `{"repoId":7,"commit":"` + codeIntelTestCommitA + `"}`,
			"code.analyze": `{"versionId":"77"}`,
			"code.impact":  `{"versionId":"77"}`,
			"code.anchors": `{"versionId":"77"}`,
		}
		for _, name := range expectedCodeIntelToolNames {
			tool := codeIntelToolByName(t, tools, name)
			result, err := tool.Execute(invocationContext(42), json.RawMessage(args[name]))
			if err != nil {
				t.Fatalf("%s business failure returned an infrastructure error: %v", name, err)
			}
			if !result.IsError || !strings.Contains(result.Content, "tool_business_error") {
				t.Fatalf("%s business failure=%+v", name, result)
			}
		}
	})

	t.Run("infrastructure failures are returned as errors", func(t *testing.T) {
		fakes := newCodeIntelFakes()
		fakes.queries.factsErr = errors.New("database down")
		result, err := executeCodeIntelTool(t, fakes.tools(t), "code.facts",
			`{"repoId":7,"commit":"`+codeIntelTestCommitA+`"}`)
		if err == nil || !strings.Contains(err.Error(), "database down") {
			t.Fatalf("infrastructure failure error=%v", err)
		}
		if result.IsError {
			t.Fatalf("infrastructure failure reported a tool-level error: %+v", result)
		}
	})
}

// TestCodeIntelToolsDoNotBreakExistingRegistry proves the nine new tools
// coexist with the eleven P3 clarification tools: no duplicate names and every
// P3 tool still registers.
func TestCodeIntelToolsDoNotBreakExistingRegistry(t *testing.T) {
	clarificationTools := NewClarificationTools(&fakeVersionPort{content: "{}"})
	codeIntelTools := newCodeIntelFakes().tools(t)
	registry := agentapp.NewToolRegistry(append(append([]domain.Tool{}, clarificationTools...), codeIntelTools...)...)

	names := registry.Names()
	if len(names) != len(expectedToolNames)+len(expectedCodeIntelToolNames) {
		t.Fatalf("registry names=%d want %d", len(names), len(expectedToolNames)+len(expectedCodeIntelToolNames))
	}
	seen := map[string]bool{}
	for _, name := range names {
		if seen[name] {
			t.Fatalf("duplicate registry name %q", name)
		}
		seen[name] = true
	}
	for _, name := range expectedToolNames {
		if !seen[name] {
			t.Fatalf("P3 tool %q disappeared from the registry", name)
		}
	}
	for _, name := range expectedCodeIntelToolNames {
		if !seen[name] {
			t.Fatalf("codeintel tool %q is missing from the registry", name)
		}
	}
	if seen["code.tree"] != true || seen["code.read"] != true {
		t.Fatal("the P3 code.tree/code.read tools must stay registered")
	}

	specs := registry.Specs(names)
	if len(specs) != len(names) {
		t.Fatalf("specs=%d want %d", len(specs), len(names))
	}
	for _, spec := range specs {
		var schema map[string]any
		if err := json.Unmarshal(spec.Parameters, &schema); err != nil {
			t.Fatalf("%s parameters are not valid JSON: %v", spec.Name, err)
		}
		if schema["type"] != "object" {
			t.Fatalf("%s parameters type=%v want object", spec.Name, schema["type"])
		}
	}
}
