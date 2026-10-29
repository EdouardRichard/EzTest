package application_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	chain "github.com/EdouardRichard/EzTest/backend/internal/chain/application"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

// fakeVersion is the VersionProvider fake. It records the resolved version and
// batch arguments so the batch-selection tests can assert the service contract.
type fakeVersion struct {
	batch           chain.BatchRow
	batchErr        error
	requirements    []chain.RequirementRow
	requirementsErr error

	getBatchCalls int
	gotVersionID  int64
	gotBatchID    *int64
	reqCalls      int
}

func (f *fakeVersion) GetBatch(_ context.Context, versionID int64, batchID *int64) (chain.BatchRow, error) {
	f.getBatchCalls++
	f.gotVersionID = versionID
	if batchID == nil {
		f.gotBatchID = nil
	} else {
		value := *batchID
		f.gotBatchID = &value
	}
	return f.batch, f.batchErr
}

func (f *fakeVersion) Requirements(_ context.Context, _ int64) ([]chain.RequirementRow, error) {
	f.reqCalls++
	return f.requirements, f.requirementsErr
}

// fakeMindmap is the MindmapProvider fake.
type fakeMindmap struct {
	cases    []chain.CaseRow
	coverage []chain.CoverageCell
	err      error
}

func (f *fakeMindmap) Cases(_ context.Context, _ int64) ([]chain.CaseRow, error) {
	return f.cases, f.err
}

func (f *fakeMindmap) Coverage(_ context.Context, _ int64) ([]chain.CoverageCell, error) {
	return f.coverage, f.err
}

// fakeExecution is the ExecutionProvider fake.
type fakeExecution struct {
	results  []chain.ResultRow
	err      error
	gotBatch *int64
	calls    int
}

func (f *fakeExecution) LatestResults(_ context.Context, _ int64, batchID *int64) ([]chain.ResultRow, error) {
	f.calls++
	f.gotBatch = cloneID(batchID)
	return f.results, f.err
}

// fakeDelivery is the DeliveryProvider fake.
type fakeDelivery struct {
	issues   []chain.IssueRow
	err      error
	gotBatch *int64
	calls    int
}

func (f *fakeDelivery) Issues(_ context.Context, _ int64, batchID *int64) ([]chain.IssueRow, error) {
	f.calls++
	f.gotBatch = cloneID(batchID)
	return f.issues, f.err
}

// fakeCodeIntel is the CodeIntelProvider fake.
type fakeCodeIntel struct {
	impact []chain.ImpactRow
	err    error
	calls  int
}

func (f *fakeCodeIntel) Impact(_ context.Context, _ int64) ([]chain.ImpactRow, error) {
	f.calls++
	return f.impact, f.err
}

// ---------------------------------------------------------------------------
// Harness
// ---------------------------------------------------------------------------

type fixture struct {
	version   *fakeVersion
	mindmap   *fakeMindmap
	execution *fakeExecution
	delivery  *fakeDelivery
	codeIntel *fakeCodeIntel
}

func newFixture() *fixture {
	return &fixture{
		version: &fakeVersion{
			batch: chain.BatchRow{ID: 7001, BatchNo: 2, Kind: "incremental", Status: "done"},
		},
		mindmap:   &fakeMindmap{},
		execution: &fakeExecution{},
		delivery:  &fakeDelivery{},
		codeIntel: &fakeCodeIntel{},
	}
}

func (f *fixture) providers() chain.Providers {
	return chain.Providers{
		Version:   f.version,
		Mindmap:   f.mindmap,
		Execution: f.execution,
		Delivery:  f.delivery,
		CodeIntel: f.codeIntel,
	}
}

// query is the valid default query every test starts from.
func query() chain.Query {
	return chain.Query{
		VersionID:         "101",
		Limit:             300,
		RequirementLimit:  100,
		IncludeSupplement: true,
	}
}

// build runs Service.Build with the default query mutated by configure.
func (f *fixture) build(t *testing.T, configure func(*chain.Query)) chain.Snapshot {
	t.Helper()
	q := query()
	if configure != nil {
		configure(&q)
	}
	snapshot, err := chain.NewService(f.providers()).Build(context.Background(), q)
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	return snapshot
}

func cloneID(id *int64) *int64 {
	if id == nil {
		return nil
	}
	value := *id
	return &value
}

func idPtr(value int64) *int64 { return &value }

func idsPtr(values ...int64) []int64 { return values }

func nodesOfKind(snapshot chain.Snapshot, kind string) []chain.NodeDTO {
	nodes := make([]chain.NodeDTO, 0, len(snapshot.Nodes))
	for _, node := range snapshot.Nodes {
		if node.Kind == kind {
			nodes = append(nodes, node)
		}
	}
	return nodes
}

func nodeIDs(nodes []chain.NodeDTO) []string {
	ids := make([]string, 0, len(nodes))
	for _, node := range nodes {
		ids = append(ids, node.ID)
	}
	return ids
}

func requireNodeIDs(t *testing.T, nodes []chain.NodeDTO, want ...string) {
	t.Helper()
	got := nodeIDs(nodes)
	if len(got) != len(want) {
		t.Fatalf("node ids = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("node ids = %v, want %v", got, want)
		}
	}
}

func findNode(t *testing.T, snapshot chain.Snapshot, id string) chain.NodeDTO {
	t.Helper()
	for _, node := range snapshot.Nodes {
		if node.ID == id {
			return node
		}
	}
	t.Fatalf("node %q is missing from %v", id, nodeIDs(snapshot.Nodes))
	return chain.NodeDTO{}
}

func findEdge(snapshot chain.Snapshot, id string) (chain.EdgeDTO, bool) {
	for _, edge := range snapshot.Edges {
		if edge.ID == id {
			return edge, true
		}
	}
	return chain.EdgeDTO{}, false
}

// edgeEndpoints maps an edge kind to its source/target node kind.
var edgeEndpoints = map[string][2]string{
	chain.EdgeKindRequirementCase: {chain.NodeKindRequirement, chain.NodeKindCase},
	chain.EdgeKindCaseResult:      {chain.NodeKindCase, chain.NodeKindResult},
	chain.EdgeKindResultIssue:     {chain.NodeKindResult, chain.NodeKindIssue},
	chain.EdgeKindCaseIssue:       {chain.NodeKindCase, chain.NodeKindIssue},
	chain.EdgeKindImpactCase:      {chain.NodeKindRequirement, chain.NodeKindCase},
}

// requireEdge asserts the edge identified by its domain ids. The edge id uses
// the domain ids while its source/target carry the prefixed node ids.
func requireEdge(t *testing.T, snapshot chain.Snapshot, kind, source, target string) chain.EdgeDTO {
	t.Helper()
	id := kind + ":" + source + ":" + target
	edge, ok := findEdge(snapshot, id)
	if !ok {
		t.Fatalf("edge %q is missing from %v", id, snapshot.Edges)
	}
	kinds, ok := edgeEndpoints[kind]
	if !ok {
		t.Fatalf("unknown edge kind %q", kind)
	}
	if edge.Kind != kind || edge.Source != kinds[0]+":"+source || edge.Target != kinds[1]+":"+target {
		t.Fatalf("edge %q = %+v", id, edge)
	}
	return edge
}

func requireNoEdge(t *testing.T, snapshot chain.Snapshot, id string) {
	t.Helper()
	if edge, ok := findEdge(snapshot, id); ok {
		t.Fatalf("edge %q must not exist: %+v", id, edge)
	}
}

func requireString(t *testing.T, got, label, want string) {
	t.Helper()
	if got != want {
		t.Fatalf("%s = %q, want %q", label, got, want)
	}
}

func requireStringPtr(t *testing.T, got *string, label, want string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %q", label, want)
	}
	if *got != want {
		t.Fatalf("%s = %q, want %q", label, *got, want)
	}
}

func requireIntPtr(t *testing.T, got *int, label string, want int) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %d", label, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", label, *got, want)
	}
}

func requireInt64Ptr(t *testing.T, got *int64, label string, want int64) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %d", label, want)
	}
	if *got != want {
		t.Fatalf("%s = %d, want %d", label, *got, want)
	}
}

func requireBoolPtr(t *testing.T, got *bool, label string, want bool) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", label, want)
	}
	if *got != want {
		t.Fatalf("%s = %v, want %v", label, *got, want)
	}
}

func requireStringIDs(t *testing.T, got *[]string, label string, want ...string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %v", label, want)
	}
	values := *got
	if len(values) != len(want) {
		t.Fatalf("%s = %v, want %v", label, values, want)
	}
	for index := range want {
		if values[index] != want[index] {
			t.Fatalf("%s = %v, want %v", label, values, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Step 1 rules
// ---------------------------------------------------------------------------

// TestLatestAttemptPerCase proves only the newest attempt of a case is returned
// as its result node, with the attempt descending sort and a single case_result
// edge (spec §5.1).
func TestLatestAttemptPerCase(t *testing.T) {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{
		{ID: 1, Title: "login", Status: "clarified", Priority: "must", SortOrder: 0},
	}
	f.mindmap.cases = []chain.CaseRow{
		{ID: 11, Title: "login succeeds", Status: "executed", Source: "user_input",
			RequirementIDs: idsPtr(1), StepCount: 3, AssertionCount: 2, SortOrder: 0},
	}
	f.execution.results = []chain.ResultRow{
		{ID: 1101, CaseNodeID: 11, Attempt: 1, Status: "blocked", BatchID: 7001},
		{ID: 1103, CaseNodeID: 11, Attempt: 3, Status: "passed", BatchID: 7001,
			DurationMs: 2100, Summary: "ok", EvidenceCount: 2},
		{ID: 1102, CaseNodeID: 11, Attempt: 2, Status: "failed", BatchID: 7001},
	}

	snapshot := f.build(t, nil)

	results := nodesOfKind(snapshot, chain.NodeKindResult)
	requireNodeIDs(t, results, "result:1103")
	result := results[0]
	if result.Title != "login succeeds" {
		t.Fatalf("result title = %q, want the case title", result.Title)
	}
	requireString(t, result.Status, "result status", "passed")
	requireStringPtr(t, result.CaseRunID, "result caseRunId", "1103")
	requireStringPtr(t, result.CaseNodeID, "result caseNodeId", "11")
	requireIntPtr(t, result.Attempt, "result attempt", 3)
	requireInt64Ptr(t, result.DurationMs, "result durationMs", 2100)
	requireIntPtr(t, result.EvidenceCount, "result evidenceCount", 2)
	requireStringPtr(t, result.BatchID, "result batchId", "7001")

	requireEdge(t, snapshot, chain.EdgeKindCaseResult, "11", "1103")
	for _, stale := range []string{"case_result:11:1101", "case_result:11:1102"} {
		requireNoEdge(t, snapshot, stale)
	}
	if snapshot.Counts.Results != 1 {
		t.Fatalf("counts.results = %d, want 1 (latest per case only)", snapshot.Counts.Results)
	}
	if snapshot.Counts.ByResultStatus["passed"] != 1 ||
		snapshot.Counts.ByResultStatus["failed"] != 0 {
		t.Fatalf("counts.byResultStatus = %v, want only one passed latest result", snapshot.Counts.ByResultStatus)
	}
}

// TestIssueEdgePrefersLatestResult proves an issue whose caseRunId hits the
// latest result of its case draws a result_issue edge; every other issue falls
// back to case_issue (or stays unlinked), per spec §5.1.
func TestIssueEdgePrefersLatestResult(t *testing.T) {
	f := newFixture()
	f.mindmap.cases = []chain.CaseRow{
		{ID: 11, Title: "login", Status: "executed", Source: "user_input", SortOrder: 0},
	}
	f.execution.results = []chain.ResultRow{
		{ID: 1103, CaseNodeID: 11, Attempt: 3, Status: "failed", BatchID: 7001},
	}
	f.delivery.issues = []chain.IssueRow{
		{ID: 31, Title: "latest run", Severity: "major", Status: "open",
			CaseRunID: idPtr(1103), CaseNodeID: idPtr(11)},
		{ID: 32, Title: "older run", Severity: "major", Status: "open",
			CaseRunID: idPtr(1101), CaseNodeID: idPtr(11)},
		{ID: 33, Title: "case only", Severity: "minor", Status: "confirmed",
			CaseNodeID: idPtr(11)},
		{ID: 34, Title: "unlinked", Severity: "info", Status: "dismissed"},
	}

	snapshot := f.build(t, nil)

	requireEdge(t, snapshot, chain.EdgeKindResultIssue, "1103", "31")
	requireEdge(t, snapshot, chain.EdgeKindCaseIssue, "11", "32")
	requireEdge(t, snapshot, chain.EdgeKindCaseIssue, "11", "33")
	for _, id := range []string{
		"result_issue:1101:32", "case_issue:11:31", "case_issue:11:34", "result_issue:1103:34",
	} {
		requireNoEdge(t, snapshot, id)
	}
	if snapshot.Counts.Issues != 4 {
		t.Fatalf("counts.issues = %d, want 4", snapshot.Counts.Issues)
	}
	if _, ok := findEdge(snapshot, "case_issue:11:34"); ok {
		t.Fatal("an issue without a case must not draw an edge")
	}
}

// TestTruncationKeepsTrueCounts proves Limit truncates the returned nodes while
// Counts keeps the real pre-truncation totals (spec §5.2).
func TestTruncationKeepsTrueCounts(t *testing.T) {
	f := newFixture()
	for index := 0; index < 5; index++ {
		f.mindmap.cases = append(f.mindmap.cases, chain.CaseRow{
			ID:        int64(100 + index),
			Title:     fmt.Sprintf("case %d", index),
			Status:    "executed",
			Source:    "user_input",
			SortOrder: index,
		})
		f.execution.results = append(f.execution.results, chain.ResultRow{
			ID: int64(1100 + index), CaseNodeID: int64(100 + index), Attempt: 1, Status: "passed",
		})
	}

	snapshot := f.build(t, func(q *chain.Query) { q.Limit = 2 })

	if !snapshot.Truncated.Cases {
		t.Fatalf("truncated.cases = false, want true (5 cases, limit 2)")
	}
	if snapshot.Counts.Cases != 5 {
		t.Fatalf("counts.cases = %d, want the real total 5", snapshot.Counts.Cases)
	}
	if got := len(nodesOfKind(snapshot, chain.NodeKindCase)); got != 2 {
		t.Fatalf("returned cases = %d, want 2", got)
	}
	if !snapshot.Truncated.Results {
		t.Fatalf("truncated.results = false, want true (the node budget is exhausted)")
	}
	if snapshot.Counts.Results != 5 {
		t.Fatalf("counts.results = %d, want the real total 5", snapshot.Counts.Results)
	}
	if snapshot.Truncated.Requirements || snapshot.Truncated.Issues {
		t.Fatalf("truncated = %+v, want only cases and results", snapshot.Truncated)
	}
}

// TestRequirementLimitTruncatesRoots proves RequirementLimit caps the root layer
// while Counts.Requirements keeps the true total.
func TestRequirementLimitTruncatesRoots(t *testing.T) {
	f := newFixture()
	for index := 0; index < 5; index++ {
		f.version.requirements = append(f.version.requirements, chain.RequirementRow{
			ID: int64(index + 1), Title: fmt.Sprintf("requirement %d", index), SortOrder: index,
		})
	}

	snapshot := f.build(t, func(q *chain.Query) { q.RequirementLimit = 2 })

	if !snapshot.Truncated.Requirements {
		t.Fatal("truncated.requirements = false, want true")
	}
	if snapshot.Counts.Requirements != 5 {
		t.Fatalf("counts.requirements = %d, want 5", snapshot.Counts.Requirements)
	}
	if got := len(nodesOfKind(snapshot, chain.NodeKindRequirement)); got != 2 {
		t.Fatalf("returned requirements = %d, want 2", got)
	}
}

// TestTotalLimitCapsRootLayer proves the global node budget also caps the
// requirement layer.
func TestTotalLimitCapsRootLayer(t *testing.T) {
	f := newFixture()
	for index := 0; index < 5; index++ {
		f.version.requirements = append(f.version.requirements, chain.RequirementRow{
			ID: int64(index + 1), Title: fmt.Sprintf("requirement %d", index), SortOrder: index,
		})
		f.mindmap.cases = append(f.mindmap.cases, chain.CaseRow{
			ID: int64(100 + index), Title: fmt.Sprintf("case %d", index), SortOrder: index,
		})
	}

	snapshot := f.build(t, func(q *chain.Query) { q.Limit = 2 })

	if got := len(nodesOfKind(snapshot, chain.NodeKindRequirement)); got != 2 {
		t.Fatalf("returned requirements = %d, want 2", got)
	}
	if got := len(nodesOfKind(snapshot, chain.NodeKindCase)); got != 0 {
		t.Fatalf("returned cases = %d, want 0 (budget consumed by requirements)", got)
	}
	if !snapshot.Truncated.Requirements || !snapshot.Truncated.Cases {
		t.Fatalf("truncated = %+v, want requirements and cases", snapshot.Truncated)
	}
	if snapshot.Counts.Requirements != 5 || snapshot.Counts.Cases != 5 {
		t.Fatalf("counts = %+v, want real totals 5/5", snapshot.Counts)
	}
}

// TestInvalidArguments proves the service rejects malformed queries with the
// typed ErrInvalidArgument before reaching any provider (spec §5.1).
func TestInvalidArguments(t *testing.T) {
	tests := []struct {
		name  string
		query chain.Query
		field string
	}{
		{name: "limit zero", query: chain.Query{VersionID: "101", Limit: 0, RequirementLimit: 100}, field: "limit"},
		{name: "limit negative", query: chain.Query{VersionID: "101", Limit: -3, RequirementLimit: 100}, field: "limit"},
		{name: "requirement limit zero", query: chain.Query{VersionID: "101", Limit: 300, RequirementLimit: 0}, field: "requirementLimit"},
		{name: "empty version", query: chain.Query{VersionID: "", Limit: 300, RequirementLimit: 100}, field: "versionId"},
		{name: "malformed version", query: chain.Query{VersionID: "abc", Limit: 300, RequirementLimit: 100}, field: "versionId"},
		{name: "malformed batch", query: chain.Query{VersionID: "101", BatchID: "batch", Limit: 300, RequirementLimit: 100}, field: "batchId"},
		{name: "zero batch", query: chain.Query{VersionID: "101", BatchID: "0", Limit: 300, RequirementLimit: 100}, field: "batchId"},
		{name: "malformed requirement", query: chain.Query{VersionID: "101", RequirementID: "x", Limit: 300, RequirementLimit: 100}, field: "requirementId"},
		{name: "unknown result status", query: chain.Query{VersionID: "101", ResultStatus: "running", Limit: 300, RequirementLimit: 100}, field: "resultStatus"},
		{name: "unknown issue status", query: chain.Query{VersionID: "101", IssueStatus: "closed", Limit: 300, RequirementLimit: 100}, field: "issueStatus"},
		{name: "unknown issue severity", query: chain.Query{VersionID: "101", IssueSeverity: "critical", Limit: 300, RequirementLimit: 100}, field: "issueSeverity"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			f := newFixture()
			_, err := chain.NewService(f.providers()).Build(context.Background(), test.query)
			var invalid *chain.ErrInvalidArgument
			if !errors.As(err, &invalid) {
				t.Fatalf("Build() error = %v, want ErrInvalidArgument", err)
			}
			if invalid.Field != test.field {
				t.Fatalf("ErrInvalidArgument.Field = %q, want %q", invalid.Field, test.field)
			}
			if invalid.Reason != chain.ReasonInvalidArgument {
				t.Fatalf("ErrInvalidArgument.Reason = %q, want %q", invalid.Reason, chain.ReasonInvalidArgument)
			}
			if f.version.getBatchCalls != 0 || f.version.reqCalls != 0 || f.execution.calls != 0 {
				t.Fatal("providers must not be called for an invalid query")
			}
		})
	}
}

// TestSearchTooLongRejects proves the q length constraint of spec §5.1.
func TestSearchTooLongRejects(t *testing.T) {
	f := newFixture()
	q := query()
	q.Search = string(make([]rune, 101))
	_, err := chain.NewService(f.providers()).Build(context.Background(), q)
	var invalid *chain.ErrInvalidArgument
	if !errors.As(err, &invalid) || invalid.Field != "q" {
		t.Fatalf("Build() error = %v, want ErrInvalidArgument{field:q}", err)
	}
}

// TestUnknownVersionReturnsNotFound proves a missing version surfaces
// ErrNotFound (spec §5.1: unknown version 404).
func TestUnknownVersionReturnsNotFound(t *testing.T) {
	f := newFixture()
	f.version.batchErr = &chain.ErrNotFound{Resource: "version", ID: 999}

	_, err := chain.NewService(f.providers()).Build(context.Background(), query())
	var notFound *chain.ErrNotFound
	if !errors.As(err, &notFound) {
		t.Fatalf("Build() error = %v, want ErrNotFound", err)
	}
	if notFound.Resource != "version" || notFound.ID != 999 {
		t.Fatalf("ErrNotFound = %+v, want version/999", notFound)
	}
}

// TestForeignBatchReturnsNotFound proves an explicit batch outside the version
// surfaces the provider's ErrNotFound unchanged.
func TestForeignBatchReturnsNotFound(t *testing.T) {
	f := newFixture()
	f.version.batchErr = &chain.ErrNotFound{Resource: "batch", ID: 88}
	q := query()
	q.BatchID = "88"

	_, err := chain.NewService(f.providers()).Build(context.Background(), q)
	var notFound *chain.ErrNotFound
	if !errors.As(err, &notFound) || notFound.Resource != "batch" || notFound.ID != 88 {
		t.Fatalf("Build() error = %v, want ErrNotFound{batch,88}", err)
	}
}

// TestMissingProvidersRejected proves a structural wiring error is not silently
// turned into an empty graph.
func TestMissingProvidersRejected(t *testing.T) {
	cases := []struct {
		name      string
		providers chain.Providers
	}{
		{name: "no version", providers: chain.Providers{Mindmap: &fakeMindmap{}, Execution: &fakeExecution{}}},
		{name: "no mindmap", providers: chain.Providers{Version: &fakeVersion{}, Execution: &fakeExecution{}}},
		{name: "no execution", providers: chain.Providers{Version: &fakeVersion{}, Mindmap: &fakeMindmap{}}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if _, err := chain.NewService(test.providers).Build(context.Background(), query()); err == nil {
				t.Fatal("Build() error = nil, want a wiring error")
			}
		})
	}
}

// TestProviderErrorPropagates proves unexpected provider failures are surfaced
// (the HTTP layer maps them to 500) instead of being swallowed.
func TestProviderErrorPropagates(t *testing.T) {
	f := newFixture()
	f.version.batchErr = errors.New("db down")

	_, err := chain.NewService(f.providers()).Build(context.Background(), query())
	if err == nil || !contains(err.Error(), "db down") {
		t.Fatalf("Build() error = %v, want the provider failure", err)
	}
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

// filterFixture builds a four-case, three-requirement graph covering every
// filter dimension of spec §5.1.
func filterFixture() *fixture {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{
		{ID: 1, Title: "Alpha requirement", Status: "clarified", Priority: "must", SortOrder: 2},
		{ID: 2, Title: "Beta requirement", Status: "clarified", Priority: "should", SortOrder: 1},
		{ID: 3, Title: "Gamma requirement", Status: "clarified", Priority: "could", SortOrder: 3},
	}
	f.mindmap.cases = []chain.CaseRow{
		{ID: 11, Title: "Alpha login", Status: "executed", Source: "user_input",
			RequirementIDs: idsPtr(1), StepCount: 3, AssertionCount: 2, SortOrder: 2},
		{ID: 12, Title: "Beta login supplement", Status: "needs_revalidation", Source: "ai_supplement",
			RequirementIDs: idsPtr(1, 2), SortOrder: 1},
		{ID: 13, Title: "Beta checkout", Status: "executed", Source: "user_input",
			RequirementIDs: idsPtr(2), SortOrder: 3},
		{ID: 14, Title: "Gamma flow", Status: "needs_revalidation", Source: "ai_supplement",
			RequirementIDs: idsPtr(3), SortOrder: 0},
	}
	f.execution.results = []chain.ResultRow{
		{ID: 1101, CaseNodeID: 11, Attempt: 1, Status: "passed", BatchID: 7001},
		{ID: 1201, CaseNodeID: 12, Attempt: 1, Status: "failed", BatchID: 7001},
		{ID: 1301, CaseNodeID: 13, Attempt: 2, Status: "blocked", BatchID: 7001},
		{ID: 1401, CaseNodeID: 14, Attempt: 1, Status: "passed", BatchID: 7001},
	}
	f.delivery.issues = []chain.IssueRow{
		{ID: 31, Title: "major issue", Severity: "major", Status: "open", CaseNodeID: idPtr(11)},
		{ID: 32, Title: "blocker issue", Severity: "blocker", Status: "confirmed", CaseNodeID: idPtr(12)},
		{ID: 33, Title: "unlinked issue", Severity: "info", Status: "dismissed"},
	}
	return f
}

func TestFilters(t *testing.T) {
	t.Run("requirement id scopes the graph", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.RequirementID = "2" })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement), "requirement:2")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:12", "case:13")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindResult), "result:1301", "result:1201")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindIssue), "issue:32", "issue:33")
		requireStringIDs(t, findNode(t, snapshot, "case:12").RequirementIDs, "case 12 requirementIds", "1", "2")
		if snapshot.Counts.Requirements != 1 || snapshot.Counts.Cases != 2 ||
			snapshot.Counts.Results != 2 || snapshot.Counts.Issues != 2 {
			t.Fatalf("counts = %+v", snapshot.Counts)
		}
	})

	t.Run("result status cascades to cases and requirements", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.ResultStatus = "passed" })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement), "requirement:1", "requirement:3")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:14", "case:11")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindResult), "result:1101", "result:1401")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindIssue), "issue:31", "issue:33")
		if snapshot.Counts.ByResultStatus["passed"] != 2 ||
			snapshot.Counts.ByResultStatus["failed"] != 0 ||
			snapshot.Counts.ByResultStatus["blocked"] != 0 {
			t.Fatalf("byResultStatus = %v, want only the filtered passed results", snapshot.Counts.ByResultStatus)
		}
	})

	t.Run("abnormal only", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.AbnormalOnly = true })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:12", "case:13")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindResult), "result:1301", "result:1201")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement), "requirement:2", "requirement:1")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindIssue), "issue:32", "issue:33")
	})

	t.Run("issue status and severity filter only the issue layer", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) {
			q.IssueStatus = "open"
			q.IssueSeverity = "major"
		})

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindIssue), "issue:31")
		if got := len(nodesOfKind(snapshot, chain.NodeKindRequirement)); got != 3 {
			t.Fatalf("requirements = %d, want all three (issue filters do not cascade)", got)
		}
		if got := len(nodesOfKind(snapshot, chain.NodeKindCase)); got != 4 {
			t.Fatalf("cases = %d, want all four (issue filters do not cascade)", got)
		}
		if snapshot.Counts.Issues != 1 || snapshot.Counts.ByIssueSeverity["major"] != 1 ||
			snapshot.Counts.ByIssueSeverity["blocker"] != 0 {
			t.Fatalf("counts = %+v, want only the major issue", snapshot.Counts)
		}
	})

	t.Run("search is case insensitive and keeps connected nodes", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.Search = "ALPHA" })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement), "requirement:2", "requirement:1")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:12", "case:11")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindResult), "result:1101", "result:1201")
	})

	t.Run("include supplement false drops AI supplement cases", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.IncludeSupplement = false })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:11", "case:13")
		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement),
			"requirement:2", "requirement:1", "requirement:3")
	})

	t.Run("revalidation only drops everything else", func(t *testing.T) {
		snapshot := filterFixture().build(t, func(q *chain.Query) { q.RevalidationOnly = true })

		requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase), "case:14", "case:12")
		for _, caseNode := range nodesOfKind(snapshot, chain.NodeKindCase) {
			requireBoolPtr(t, caseNode.Revalidation, caseNode.ID+" revalidation", true)
		}
	})
}

// ---------------------------------------------------------------------------
// Sorting
// ---------------------------------------------------------------------------

func TestLayerOrdering(t *testing.T) {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{
		{ID: 20, Title: "late", SortOrder: 5},
		{ID: 30, Title: "second", SortOrder: 0},
		{ID: 10, Title: "first", SortOrder: 0},
	}
	f.mindmap.cases = []chain.CaseRow{
		{ID: 200, Title: "late", SortOrder: 2},
		{ID: 300, Title: "second", SortOrder: 0},
		{ID: 100, Title: "first", SortOrder: 0},
	}
	f.execution.results = []chain.ResultRow{
		{ID: 2001, CaseNodeID: 200, Attempt: 2, Status: "passed"},
		{ID: 3001, CaseNodeID: 300, Attempt: 5, Status: "passed"},
		{ID: 1001, CaseNodeID: 100, Attempt: 3, Status: "passed"},
	}
	older := time.Date(2026, 10, 20, 10, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	newest := time.Date(2026, 10, 27, 10, 0, 0, 0, time.UTC)
	f.delivery.issues = []chain.IssueRow{
		{ID: 41, Title: "minor", Severity: "minor", Status: "open", CreatedAt: newer},
		{ID: 42, Title: "blocker", Severity: "blocker", Status: "open", CreatedAt: older},
		{ID: 43, Title: "major new", Severity: "major", Status: "open", CreatedAt: newest},
		{ID: 44, Title: "major old", Severity: "major", Status: "open", CreatedAt: newer},
		{ID: 45, Title: "info", Severity: "info", Status: "open", CreatedAt: newest},
	}

	snapshot := f.build(t, nil)

	requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindRequirement),
		"requirement:10", "requirement:30", "requirement:20")
	requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindCase),
		"case:100", "case:300", "case:200")
	requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindResult),
		"result:3001", "result:1001", "result:2001")
	requireNodeIDs(t, nodesOfKind(snapshot, chain.NodeKindIssue),
		"issue:42", "issue:43", "issue:44", "issue:41", "issue:45")
}

// ---------------------------------------------------------------------------
// Coverage, counts and impact
// ---------------------------------------------------------------------------

// TestCoverageUnionAndCounts proves coverage.matrix contributes requirement →
// case links (union with case_source), uncovered requirements are flagged and
// the aggregate counters of spec §5.1 are filled.
func TestCoverageUnionAndCounts(t *testing.T) {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{
		{ID: 1, Title: "covered", Status: "clarified", SortOrder: 0,
			OpenQuestionCount: 2},
		{ID: 2, Title: "uncovered", Status: "clarified", SortOrder: 1},
	}
	f.mindmap.cases = []chain.CaseRow{
		{ID: 11, Title: "covered case", Status: "executed", Source: "user_input",
			StepCount: 1, AssertionCount: 1, SortOrder: 0},
		{ID: 12, Title: "supplement", Status: "needs_revalidation", Source: "ai_supplement",
			RequirementIDs: idsPtr(1), SortOrder: 1},
	}
	// Case 11 is only linked through the coverage matrix, exercising the union.
	f.mindmap.coverage = []chain.CoverageCell{
		{RequirementID: 1, CaseIDs: []int64{11}},
	}
	f.execution.results = []chain.ResultRow{
		{ID: 1101, CaseNodeID: 11, Attempt: 1, Status: "passed"},
		{ID: 1201, CaseNodeID: 12, Attempt: 1, Status: "failed"},
	}
	f.delivery.issues = []chain.IssueRow{
		{ID: 31, Title: "blocker", Severity: "blocker", Status: "open"},
		{ID: 32, Title: "major", Severity: "major", Status: "confirmed"},
	}

	snapshot := f.build(t, nil)

	requireEdge(t, snapshot, chain.EdgeKindRequirementCase, "1", "11")

	covered := findNode(t, snapshot, "requirement:1")
	requireBoolPtr(t, covered.Uncovered, "requirement 1 uncovered", false)
	requireIntPtr(t, covered.CaseCount, "requirement 1 caseCount", 2)
	requireIntPtr(t, covered.ExecutedCount, "requirement 1 executedCount", 2)
	requireIntPtr(t, covered.FailedCount, "requirement 1 failedCount", 1)
	requireIntPtr(t, covered.OpenQuestionCount, "requirement 1 openQuestionCount", 2)
	requireStringPtr(t, covered.Priority, "requirement 1 priority", "")
	requireString(t, covered.Status, "requirement 1 status", "clarified")

	uncovered := findNode(t, snapshot, "requirement:2")
	requireBoolPtr(t, uncovered.Uncovered, "requirement 2 uncovered", true)
	requireIntPtr(t, uncovered.CaseCount, "requirement 2 caseCount", 0)

	counts := snapshot.Counts
	if counts.Requirements != 2 || counts.Cases != 2 || counts.Results != 2 || counts.Issues != 2 {
		t.Fatalf("counts = %+v", counts)
	}
	if counts.UncoveredRequirements != 1 {
		t.Fatalf("counts.uncoveredRequirements = %d, want 1", counts.UncoveredRequirements)
	}
	if counts.RevalidationCases != 1 {
		t.Fatalf("counts.revalidationCases = %d, want 1", counts.RevalidationCases)
	}
	if counts.SupplementCases != 1 {
		t.Fatalf("counts.supplementCases = %d, want 1", counts.SupplementCases)
	}
	if counts.ByResultStatus["passed"] != 1 || counts.ByResultStatus["failed"] != 1 {
		t.Fatalf("byResultStatus = %v", counts.ByResultStatus)
	}
	for _, status := range []string{"pending", "running", "passed", "failed", "blocked",
		"inconclusive", "skipped", "interrupted"} {
		if _, ok := counts.ByResultStatus[status]; !ok {
			t.Fatalf("byResultStatus is missing %q: %v", status, counts.ByResultStatus)
		}
	}
	if counts.ByIssueSeverity["blocker"] != 1 || counts.ByIssueSeverity["major"] != 1 ||
		counts.ByIssueSeverity["minor"] != 0 || counts.ByIssueSeverity["info"] != 0 {
		t.Fatalf("byIssueSeverity = %v", counts.ByIssueSeverity)
	}
}

// TestImpactEdges proves selected / high / medium requirement candidates produce
// impact_case edges and mark their case nodes.
func TestImpactEdges(t *testing.T) {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{
		{ID: 1, Title: "one", SortOrder: 0},
		{ID: 2, Title: "two", SortOrder: 1},
	}
	f.mindmap.cases = []chain.CaseRow{
		{ID: 11, Title: "one case", RequirementIDs: idsPtr(1), SortOrder: 0},
		{ID: 12, Title: "two case", RequirementIDs: idsPtr(2), SortOrder: 1},
	}
	f.codeIntel.impact = []chain.ImpactRow{
		{SubjectKind: "requirement", SubjectID: 1, Confidence: "low", Reason: "ignored"},
		{SubjectKind: "requirement", SubjectID: 1, Confidence: "high", Reason: "changed API"},
		{SubjectKind: "requirement", SubjectID: 2, Selected: true, Confidence: "low", Reason: "selected"},
		{SubjectKind: "app_model", SubjectID: 9, Selected: true, Confidence: "high", Reason: "app model"},
	}

	snapshot := f.build(t, nil)

	requireEdge(t, snapshot, chain.EdgeKindImpactCase, "1", "11")
	requireEdge(t, snapshot, chain.EdgeKindImpactCase, "2", "12")
	requireNoEdge(t, snapshot, "impact_case:1:12")
	requireNoEdge(t, snapshot, "impact_case:9:11")

	impacted := findNode(t, snapshot, "case:11")
	requireBoolPtr(t, impacted.Impacted, "case 11 impacted", true)
	requireStringPtr(t, impacted.ImpactReason, "case 11 impactReason", "changed API")

	if len(snapshot.Edges) != 4 { // 2 requirement_case + 2 impact_case
		t.Fatalf("edges = %v, want exactly two requirement_case and two impact_case edges", snapshot.Edges)
	}
}

// TestImpactAbsentWithoutProvider proves the optional P4 provider degrades to
// no impact information.
func TestImpactAbsentWithoutProvider(t *testing.T) {
	f := newFixture()
	f.version.requirements = []chain.RequirementRow{{ID: 1, Title: "one"}}
	f.mindmap.cases = []chain.CaseRow{{ID: 11, Title: "one case", RequirementIDs: idsPtr(1)}}

	snapshot, err := chain.NewService(chain.Providers{
		Version:   f.version,
		Mindmap:   f.mindmap,
		Execution: f.execution,
	}).Build(context.Background(), query())
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	for _, edge := range snapshot.Edges {
		if edge.Kind == chain.EdgeKindImpactCase {
			t.Fatalf("impact edge without a P4 provider: %+v", edge)
		}
	}
	caseNode := findNode(t, snapshot, "case:11")
	requireBoolPtr(t, caseNode.Impacted, "case impacted", false)
	requireStringPtr(t, caseNode.ImpactReason, "case impactReason", "")
}

// ---------------------------------------------------------------------------
// Batch selection
// ---------------------------------------------------------------------------

func TestBatchSelection(t *testing.T) {
	t.Run("default selects the latest batch", func(t *testing.T) {
		f := newFixture()
		f.build(t, nil)

		if f.version.gotVersionID != 101 {
			t.Fatalf("version id = %d, want 101", f.version.gotVersionID)
		}
		if f.version.gotBatchID != nil {
			t.Fatalf("GetBatch batch id = %v, want nil (latest)", f.version.gotBatchID)
		}
		requireInt64Ptr(t, f.execution.gotBatch, "execution batch", 7001)
		requireInt64Ptr(t, f.delivery.gotBatch, "delivery batch", 7001)
	})

	t.Run("all scopes results and issues to every batch", func(t *testing.T) {
		f := newFixture()
		f.build(t, func(q *chain.Query) { q.BatchID = "all" })

		if f.version.gotBatchID != nil {
			t.Fatalf("GetBatch batch id = %v, want nil (latest for display)", f.version.gotBatchID)
		}
		if f.execution.gotBatch != nil || f.delivery.gotBatch != nil {
			t.Fatalf("provider batch = %v/%v, want nil (all batches)",
				f.execution.gotBatch, f.delivery.gotBatch)
		}
	})

	t.Run("explicit batch is forwarded", func(t *testing.T) {
		f := newFixture()
		f.version.batch = chain.BatchRow{ID: 8888, BatchNo: 9, Kind: "baseline", Status: "done"}
		snapshot := f.build(t, func(q *chain.Query) { q.BatchID = "8888" })

		requireInt64Ptr(t, f.version.gotBatchID, "GetBatch batch id", 8888)
		requireInt64Ptr(t, f.execution.gotBatch, "execution batch", 8888)
		requireInt64Ptr(t, f.delivery.gotBatch, "delivery batch", 8888)
		if snapshot.Batch.ID != "8888" || snapshot.Batch.BatchNo != 9 ||
			snapshot.Batch.Kind != "baseline" || snapshot.Batch.Status != "done" {
			t.Fatalf("batch = %+v", snapshot.Batch)
		}
	})
}

// ---------------------------------------------------------------------------
// Empty data and envelope basics
// ---------------------------------------------------------------------------

// TestEmptyVersionReturnsEmptyGraph proves a known version without data returns
// empty arrays (never null) and a complete zero counts object.
func TestEmptyVersionReturnsEmptyGraph(t *testing.T) {
	f := newFixture()
	f.version.batch = chain.BatchRow{}

	snapshot := f.build(t, nil)

	if snapshot.Nodes == nil || len(snapshot.Nodes) != 0 {
		t.Fatalf("nodes = %#v, want an empty non-nil slice", snapshot.Nodes)
	}
	if snapshot.Edges == nil || len(snapshot.Edges) != 0 {
		t.Fatalf("edges = %#v, want an empty non-nil slice", snapshot.Edges)
	}
	if snapshot.VersionID != "101" {
		t.Fatalf("versionId = %q, want 101", snapshot.VersionID)
	}
	if snapshot.Batch.ID != "" || snapshot.Batch.BatchNo != 0 {
		t.Fatalf("batch = %+v, want the zero batch", snapshot.Batch)
	}
	counts := snapshot.Counts
	if counts.Requirements != 0 || counts.Cases != 0 || counts.Results != 0 ||
		counts.Issues != 0 || counts.UncoveredRequirements != 0 ||
		counts.RevalidationCases != 0 || counts.SupplementCases != 0 {
		t.Fatalf("counts = %+v, want zeros", counts)
	}
	if len(counts.ByResultStatus) != 8 || len(counts.ByIssueSeverity) != 4 {
		t.Fatalf("counts maps = %v / %v", counts.ByResultStatus, counts.ByIssueSeverity)
	}
	if snapshot.Truncated.Requirements || snapshot.Truncated.Cases ||
		snapshot.Truncated.Results || snapshot.Truncated.Issues {
		t.Fatalf("truncated = %+v, want false", snapshot.Truncated)
	}
	if snapshot.GeneratedAt.IsZero() {
		t.Fatal("generatedAt is zero")
	}
}

// TestVersionIDCanonicalized proves the service normalizes a decimal version id
// and passes the numeric form to its providers.
func TestVersionIDCanonicalized(t *testing.T) {
	f := newFixture()
	snapshot := f.build(t, func(q *chain.Query) { q.VersionID = "000101" })

	if snapshot.VersionID != "101" {
		t.Fatalf("versionId = %q, want canonical 101", snapshot.VersionID)
	}
	if f.version.gotVersionID != 101 {
		t.Fatalf("provider version id = %d, want 101", f.version.gotVersionID)
	}
}

// contains is a tiny substring helper so the test does not depend on the error
// wrapping style of the service.
func contains(haystack, needle string) bool {
	for index := 0; index+len(needle) <= len(haystack); index++ {
		if haystack[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}

// ensure the fakes satisfy the exported ports at compile time.
var (
	_ chain.VersionProvider   = (*fakeVersion)(nil)
	_ chain.MindmapProvider   = (*fakeMindmap)(nil)
	_ chain.ExecutionProvider = (*fakeExecution)(nil)
	_ chain.DeliveryProvider  = (*fakeDelivery)(nil)
	_ chain.CodeIntelProvider = (*fakeCodeIntel)(nil)
)
