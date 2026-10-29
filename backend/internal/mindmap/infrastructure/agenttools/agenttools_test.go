package agenttools

// This file pins the six P5 agent tools of design spec §8.2. The tests drive the
// thin-shell contract: exact registry names, valid JSON schemas with
// additionalProperties=false, the P5 profile allowlist, the run-scoped identity
// resolution (never a model-supplied runId), the argument caps and enums, the
// "store only, never write business tables" rule, the read-summary caps and the
// read-only explore probe.
//
// Every tool is exercised through its public domain.Tool contract; the fakes
// below implement the narrow ports ToolDeps consumes so a test can prove that a
// submit tool only touches the run row.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/infrastructure/httpclient"
)

// expectedToolNames is the exact spec §8.2 tool set in registry order.
var expectedToolNames = []string{
	"case.graph_read",
	"case.graph_propose",
	"case.review_submit",
	"case.data_plan_submit",
	"app_model_propose",
	"explore.http",
}

// fakeGraphReader records the graph reads.
type fakeGraphReader struct {
	view      mindmapapp.GraphView
	err       error
	calls     int
	versionID int64
}

// Get records the version id and returns the canned graph.
func (f *fakeGraphReader) Get(_ context.Context, versionID int64) (mindmapapp.GraphView, error) {
	f.calls++
	f.versionID = versionID
	if f.err != nil {
		return mindmapapp.GraphView{}, f.err
	}
	return f.view, nil
}

// runUpdate is one recorded run-row write.
type runUpdate struct {
	runID int64
	from  string
	to    string
	patch mindmapapp.RunPatch
}

// fakeRunStore is the narrow run repository the tools consume.
type fakeRunStore struct {
	runs       []mindmapdomain.CaseDesignRun
	listErr    error
	updateErr  error
	listCalls  int
	listLimit  int
	updates    []runUpdate
	updateCall int
}

// ListActive records the scan bound and returns the canned active runs.
func (f *fakeRunStore) ListActive(_ context.Context, limit int) ([]mindmapdomain.CaseDesignRun, error) {
	f.listCalls++
	f.listLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]mindmapdomain.CaseDesignRun(nil), f.runs...), nil
}

// UpdateStatus is the CAS the submit tools use to store their payload.
func (f *fakeRunStore) UpdateStatus(_ context.Context, runID int64, from, to string, patch mindmapapp.RunPatch) (mindmapdomain.CaseDesignRun, error) {
	f.updateCall++
	if f.updateErr != nil {
		return mindmapdomain.CaseDesignRun{}, f.updateErr
	}
	for index := range f.runs {
		if f.runs[index].ID != runID {
			continue
		}
		if f.runs[index].Status != from {
			return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewStateConflict("run_state_conflict",
				fmt.Sprintf("run %d is %s, not %s", runID, f.runs[index].Status, from))
		}
		f.updates = append(f.updates, runUpdate{runID: runID, from: from, to: to, patch: patch})
		if patch.Result != nil {
			f.runs[index].Result = append([]byte(nil), patch.Result...)
		}
		if patch.Stats != nil {
			f.runs[index].Stats = append([]byte(nil), patch.Stats...)
		}
		f.runs[index].Status = to
		return f.runs[index], nil
	}
	return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", runID)
}

// fakeAnchorReader records the per-node anchor counts of case.graph_read.
type fakeAnchorReader struct {
	byNode map[int64][]mindmapapp.Anchor
	err    error
	calls  int
}

// List returns the canned anchors of one node.
func (f *fakeAnchorReader) List(_ context.Context, _, nodeID int64) ([]mindmapapp.Anchor, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return f.byNode[nodeID], nil
}

// fakeFindingReader records the version-level finding reads of case.graph_read.
type fakeFindingReader struct {
	findings []mindmapdomain.CaseReviewFinding
	err      error
	calls    int
	status   string
	severity string
}

// List returns the canned findings.
func (f *fakeFindingReader) List(_ context.Context, _ int64, status, severity string, _ *int64) ([]mindmapdomain.CaseReviewFinding, error) {
	f.calls++
	f.status, f.severity = status, severity
	if f.err != nil {
		return nil, f.err
	}
	return f.findings, nil
}

// fakeProber records one read-only probe. It implements the optional method-aware
// extension so the HEAD path is testable.
type fakeProber struct {
	result mindmapapp.ProbeResult
	err    error
	calls  int
	method string
	url    string
	cookie string
	hosts  []string
}

// Probe records a GET probe.
func (f *fakeProber) Probe(_ context.Context, rawURL, cookieHeader string, allowedHosts []string) (mindmapapp.ProbeResult, error) {
	f.calls++
	f.method = "GET"
	f.url = rawURL
	f.cookie = cookieHeader
	f.hosts = allowedHosts
	if f.err != nil {
		return mindmapapp.ProbeResult{}, f.err
	}
	return f.result, nil
}

// ProbeMethod records a method-aware probe (HEAD is only reachable this way).
func (f *fakeProber) ProbeMethod(_ context.Context, method, rawURL, cookieHeader string, allowedHosts []string) (mindmapapp.ProbeResult, error) {
	f.calls++
	f.method = method
	f.url = rawURL
	f.cookie = cookieHeader
	f.hosts = allowedHosts
	if f.err != nil {
		return mindmapapp.ProbeResult{}, f.err
	}
	return f.result, nil
}

// fakeCatalog serves the environment allowlist of an exploration run. It never
// imports app models: a propose tool must only store.
type fakeCatalog struct {
	environments map[int64]mindmapapp.EnvironmentSnapshot
	listCalls    int
	importCalls  int
}

// GetEnvironment returns the canned environment.
func (f *fakeCatalog) GetEnvironment(_ context.Context, envID int64) (mindmapapp.EnvironmentSnapshot, error) {
	environment, ok := f.environments[envID]
	if !ok {
		return mindmapapp.EnvironmentSnapshot{}, errors.New("environment not found")
	}
	return environment, nil
}

// ListAppModels returns an empty model.
func (f *fakeCatalog) ListAppModels(_ context.Context, _ int64) ([]mindmapapp.AppModelSnapshot, error) {
	f.listCalls++
	return nil, nil
}

// ImportAppModels fails the test: the propose tool must never import.
func (f *fakeCatalog) ImportAppModels(_ context.Context, _ int64, _ []mindmapapp.AppModelInput) (int, int, error) {
	f.importCalls++
	return 0, 0, errors.New("app_model_propose must not import")
}

// fakeAudit records the mindmap.tool.invoked entries.
type fakeAudit struct {
	events []mindmapapp.AuditEvent
	err    error
}

// Record appends one audit entry.
func (f *fakeAudit) Record(_ context.Context, event mindmapapp.AuditEvent) error {
	f.events = append(f.events, event)
	return f.err
}

// toolFakes bundles one fake per dependency.
type toolFakes struct {
	graphs   *fakeGraphReader
	runs     *fakeRunStore
	catalog  *fakeCatalog
	prober   *fakeProber
	anchors  *fakeAnchorReader
	findings *fakeFindingReader
	audit    *fakeAudit
}

// newToolFakes returns fakes with empty successful state.
func newToolFakes() *toolFakes {
	return &toolFakes{
		graphs:   &fakeGraphReader{},
		runs:     &fakeRunStore{},
		catalog:  &fakeCatalog{environments: map[int64]mindmapapp.EnvironmentSnapshot{}},
		prober:   &fakeProber{},
		anchors:  &fakeAnchorReader{},
		findings: &fakeFindingReader{},
		audit:    &fakeAudit{},
	}
}

// deps wires the tool dependencies to the fakes.
func (f *toolFakes) deps() ToolDeps {
	return ToolDeps{
		Graphs:   f.graphs,
		Runs:     f.runs,
		Catalog:  f.catalog,
		Prober:   f.prober,
		Anchors:  f.anchors,
		Findings: f.findings,
		Audit:    f.audit,
	}
}

// tools requires the full spec §8.2 tool set from the fakes.
func (f *toolFakes) tools(t *testing.T) []agentdomain.Tool {
	t.Helper()
	tools := NewTools(f.deps())
	if len(tools) != len(expectedToolNames) {
		t.Fatalf("NewTools returned %d tools, want %d", len(tools), len(expectedToolNames))
	}
	return tools
}

// toolContext returns a context whose invocation names the given agent session.
func toolContext(sessionID, actor int64) context.Context {
	return agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{
		SessionID: sessionID, RunID: 900, ActorUserID: &actor, ProfileKey: "case-design",
	})
}

// toolByName indexes the tool set by name.
func toolByName(t *testing.T, tools []agentdomain.Tool, name string) agentdomain.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered", name)
	return nil
}

// executeTool runs one tool with JSON arguments and the given context.
func executeTool(t *testing.T, tools []agentdomain.Tool, name, args string, ctx context.Context) (agentdomain.ToolResult, error) {
	t.Helper()
	return toolByName(t, tools, name).Execute(ctx, json.RawMessage(args))
}

// requireToolErrorCode asserts err is the expected typed tool failure.
func requireToolErrorCode(t *testing.T, err error, code string) {
	t.Helper()
	var toolErr *agentdomain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("err=%v want *domain.ToolError code %q", err, code)
	}
	if toolErr.Code != code {
		t.Fatalf("tool error code=%q want %q (message %q)", toolErr.Code, code, toolErr.Message)
	}
}

// decodeContentMap decodes a successful tool payload and proves it is a JSON
// object.
func decodeContentMap(t *testing.T, result agentdomain.ToolResult) map[string]any {
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

// asMap asserts one decoded value is a JSON object.
func asMap(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("value=%v want a JSON object", value)
	}
	return object
}

// asArray asserts one decoded value is a JSON array.
func asArray(t *testing.T, value any) []any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("value=%v want a JSON array", value)
	}
	return items
}

// int64Ptr builds an optional id fixture.
func int64Ptr(value int64) *int64 { return &value }

// activeRun builds one queued/running run bound to an agent session.
func activeRun(id, versionID, sessionID int64, kind string) mindmapdomain.CaseDesignRun {
	return mindmapdomain.CaseDesignRun{
		ID:             id,
		VersionID:      versionID,
		Kind:           kind,
		Status:         mindmapdomain.RunRunning,
		AgentSessionID: int64Ptr(sessionID),
		CreatedBy:      42,
		Stats:          []byte(`{"contextTruncated":true}`),
	}
}

// graphFixture is the two-node graph summary fixture: one scenario, one case
// with two steps, one assertion, one requirement source and one ai_run source.
func graphFixture() mindmapapp.GraphView {
	requirementID := int64(1838)
	aiRunID := int64(501)
	stepOne := mindmapdomain.CaseStep{ID: 201, NodeID: 102, Seq: 1, Action: "打开"}
	stepTwo := mindmapdomain.CaseStep{ID: 202, NodeID: 102, Seq: 2, Action: "提交"}
	return mindmapapp.GraphView{
		VersionID:     7,
		VersionStatus: "reviewing",
		Editable:      true,
		Nodes: []mindmapapp.GraphNode{
			{Node: mindmapdomain.CaseNode{
				ID: 101, VersionID: 7, Kind: mindmapdomain.NodeKindScenario, Title: "下单主流程",
				Priority: mindmapdomain.PriorityP2, Source: mindmapdomain.NodeSourceUserInput,
				Status: mindmapdomain.NodeStatusDraft, Revision: 1,
			}},
			{
				Node: mindmapdomain.CaseNode{
					ID: 102, VersionID: 7, ParentID: int64Ptr(101), Kind: mindmapdomain.NodeKindCase,
					Title: "正常下单", Priority: mindmapdomain.PriorityP1, Source: mindmapdomain.NodeSourceAIGenerated,
					Status: mindmapdomain.NodeStatusDraft, Revision: 4,
				},
				Steps:      []mindmapdomain.CaseStep{stepOne, stepTwo},
				Assertions: []mindmapdomain.CaseAssertion{{ID: 301, NodeID: 102, Kind: mindmapdomain.AssertionKindUI}},
				Sources: []mindmapdomain.CaseSource{
					{ID: 401, VersionID: 7, NodeID: 102, RefKind: mindmapdomain.RefRequirement, RefID: &requirementID},
					{ID: 402, VersionID: 7, NodeID: 102, RefKind: mindmapdomain.RefAIRun, RefID: &aiRunID},
				},
			},
		},
		Coverage: mindmapdomain.Coverage{
			RequirementIDs:          []int64{1838, 1839},
			UncoveredRequirementIDs: []int64{1839},
			StaleRequirementIDs:     []int64{},
			Matrix:                  []mindmapdomain.CoverageCell{{RequirementID: 1838, CaseIDs: []int64{102}}},
		},
		Baseline: &mindmapdomain.CaseBaseline{
			ID: 9001, VersionID: 7, NodeCount: 2, CaseCount: 1, GraphHash: "abc123",
			FrozenAt: time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
		},
	}
}

// TestToolSpecsAndSchemas pins the six names/order, non-empty bounded
// descriptions and valid object schemas that reject additional properties.
func TestToolSpecsAndSchemas(t *testing.T) {
	fakes := newToolFakes()
	tools := fakes.tools(t)

	seen := map[string]bool{}
	for index, tool := range tools {
		if tool.Name() != expectedToolNames[index] {
			t.Fatalf("tool[%d]=%q want %q", index, tool.Name(), expectedToolNames[index])
		}
		if seen[tool.Name()] {
			t.Fatalf("duplicate tool name %q", tool.Name())
		}
		seen[tool.Name()] = true
		if description := strings.TrimSpace(tool.Description()); description == "" {
			t.Fatalf("%s has an empty description", tool.Name())
		}
		if count := utf8.RuneCountInString(tool.Description()); count > 300 {
			t.Fatalf("%s description has %d characters; the limit is 300", tool.Name(), count)
		}
		schema := schemaObject(t, tool)
		properties := propertyMap(t, schema)
		if schema["additionalProperties"] != false {
			t.Fatalf("%s schema must reject additional properties", tool.Name())
		}
		if required, ok := schema["required"].([]any); ok {
			for _, field := range required {
				name, ok := field.(string)
				if !ok {
					t.Fatalf("%s required contains a non-string: %v", tool.Name(), field)
				}
				if _, present := properties[name]; !present {
					t.Fatalf("%s requires %q but does not declare it", tool.Name(), name)
				}
			}
		}
		for _, forbidden := range []string{"actor", "actorId", "runId"} {
			if _, present := properties[forbidden]; present {
				t.Fatalf("%s schema exposes the trustable-only field %q", tool.Name(), forbidden)
			}
		}
	}

	wantRequired := map[string][]string{
		"case.graph_read": {"versionId"},
		// case.graph_propose accepts both the flat §6.7 shape (scenarios/cases
		// at the top level) and the nested compatibility shape (payload), so
		// only versionId is unconditionally required.
		"case.graph_propose":    {"versionId"},
		"case.review_submit":    {"versionId", "findings"},
		"case.data_plan_submit": {"versionId", "items"},
		"app_model_propose":     {"systemId", "nodes"},
		"explore.http":          {"url"},
	}
	byName := map[string]agentdomain.Tool{}
	for _, tool := range tools {
		byName[tool.Name()] = tool
	}
	for name, expected := range wantRequired {
		required := requiredSet(t, schemaObject(t, byName[name]))
		if len(required) != len(expected) {
			t.Fatalf("%s required=%v want %v", name, required, expected)
		}
		for _, field := range expected {
			if !required[field] {
				t.Fatalf("%s required=%v is missing %q", name, required, field)
			}
		}
	}

	t.Run("version and system ids are decimal strings", func(t *testing.T) {
		for _, name := range []string{"case.graph_read", "case.graph_propose", "case.review_submit", "case.data_plan_submit"} {
			property := schemaProperty(t, byName[name], "versionId")
			if property["type"] != "string" || property["pattern"] != "^[0-9]+$" {
				t.Fatalf("%s versionId schema=%v want a decimal string", name, property)
			}
		}
		property := schemaProperty(t, byName["app_model_propose"], "systemId")
		if property["type"] != "string" || property["pattern"] != "^[0-9]+$" {
			t.Fatalf("app_model_propose systemId schema=%v want a decimal string", property)
		}
	})

	t.Run("explore.http only allows GET and HEAD", func(t *testing.T) {
		property := schemaProperty(t, byName["explore.http"], "method")
		values := asArray(t, property["enum"])
		if len(values) != 2 || values[0] != "GET" || values[1] != "HEAD" {
			t.Fatalf("explore.http method enum=%v want [GET HEAD]", values)
		}
	})
}

// TestToolsMatchP5ProfilesAllowlist proves the produced tools are exactly the
// P5-owned entries of the four seeded profile allowlists (spec §8.1/§8.2).
func TestToolsMatchP5ProfilesAllowlist(t *testing.T) {
	allowed := map[string]bool{}
	for _, profile := range mindmapdomain.P5Profiles() {
		for _, name := range profile.AllowedTools {
			allowed[name] = true
		}
	}
	expected := map[string]bool{}
	for _, name := range expectedToolNames {
		expected[name] = true
		if !allowed[name] {
			t.Fatalf("tool %q is not in any P5 profile allowlist", name)
		}
	}
	for name := range allowed {
		if strings.HasPrefix(name, "case.") || name == "app_model_propose" || name == "explore.http" {
			if !expected[name] {
				t.Fatalf("profile allowlist names %q but NewTools does not produce it", name)
			}
		}
	}
}

// TestNewToolsRequiresCoreDeps pins the fail-closed construction: a missing core
// port disables the whole tool set instead of exposing half-working tools.
func TestNewToolsRequiresCoreDeps(t *testing.T) {
	if NewTools(ToolDeps{}) != nil {
		t.Fatal("NewTools with no deps must return nil")
	}
	full := newToolFakes().deps()
	cases := []ToolDeps{
		{Runs: full.Runs, Prober: full.Prober},
		{Graphs: full.Graphs, Prober: full.Prober},
		{Graphs: full.Graphs, Runs: full.Runs},
	}
	for index, deps := range cases {
		if NewTools(deps) != nil {
			t.Fatalf("deps case %d: a missing core port must disable the tool set", index)
		}
	}
}

// TestGraphReadSummary pins case.graph_read's node/coverage/baseline summary,
// including the optional anchor and open-blocker counts.
func TestGraphReadSummary(t *testing.T) {
	fakes := newToolFakes()
	fakes.graphs.view = graphFixture()
	fakes.anchors.byNode = map[int64][]mindmapapp.Anchor{
		102: {{ID: 1, SubjectID: 102}, {ID: 2, SubjectID: 102}},
	}
	fakes.findings.findings = []mindmapdomain.CaseReviewFinding{
		{ID: 1, NodeID: int64Ptr(102), Severity: mindmapdomain.SeverityBlocker, Status: mindmapdomain.FindingOpen},
		{ID: 2, Severity: mindmapdomain.SeverityBlocker, Status: mindmapdomain.FindingOpen},
	}
	tools := fakes.tools(t)

	result, err := executeTool(t, tools, "case.graph_read", `{"versionId":"7"}`, context.Background())
	if err != nil {
		t.Fatalf("case.graph_read failed: %v", err)
	}
	payload := decodeContentMap(t, result)
	if payload["versionId"] != "7" || payload["versionStatus"] != "reviewing" || payload["editable"] != true {
		t.Fatalf("version summary=%v", payload)
	}
	if payload["nodeCount"] != float64(2) || payload["truncated"] != false {
		t.Fatalf("nodeCount/truncated=%v/%v", payload["nodeCount"], payload["truncated"])
	}
	if fakes.graphs.versionID != 7 {
		t.Fatalf("graph read version=%d want 7", fakes.graphs.versionID)
	}

	nodes := asArray(t, payload["nodes"])
	if len(nodes) != 2 {
		t.Fatalf("nodes=%d want 2", len(nodes))
	}
	scenario := asMap(t, nodes[0])
	if scenario["id"] != "101" || scenario["kind"] != "scenario" || scenario["stepCount"] != float64(0) {
		t.Fatalf("scenario summary=%v", scenario)
	}
	caseNode := asMap(t, nodes[1])
	if caseNode["id"] != "102" || caseNode["kind"] != "case" || caseNode["title"] != "正常下单" {
		t.Fatalf("case summary=%v", caseNode)
	}
	if caseNode["priority"] != "P1" || caseNode["source"] != "ai_generated" || caseNode["status"] != "draft" {
		t.Fatalf("case fields=%v", caseNode)
	}
	if caseNode["revision"] != float64(4) || caseNode["stepCount"] != float64(2) || caseNode["assertionCount"] != float64(1) {
		t.Fatalf("case counts=%v", caseNode)
	}
	if caseNode["anchorCount"] != float64(2) || caseNode["openBlockerCount"] != float64(1) {
		t.Fatalf("case anchor/blocker counts=%v", caseNode)
	}
	requirementIDs := asArray(t, caseNode["requirementIds"])
	if len(requirementIDs) != 1 || requirementIDs[0] != "1838" {
		t.Fatalf("requirementIds=%v want [1838]", requirementIDs)
	}

	coverage := asMap(t, payload["coverage"])
	uncovered := asArray(t, coverage["uncoveredRequirementIds"])
	if len(uncovered) != 1 || uncovered[0] != "1839" {
		t.Fatalf("uncoveredRequirementIds=%v", uncovered)
	}
	baseline := asMap(t, payload["baseline"])
	if baseline["present"] != true || baseline["id"] != "9001" || baseline["caseCount"] != float64(1) {
		t.Fatalf("baseline=%v", baseline)
	}
	if payload["openBlockerCount"] != float64(2) {
		t.Fatalf("openBlockerCount=%v want 2", payload["openBlockerCount"])
	}
	if fakes.findings.status != mindmapdomain.FindingOpen || fakes.findings.severity != mindmapdomain.SeverityBlocker {
		t.Fatalf("finding filter=%q/%q", fakes.findings.status, fakes.findings.severity)
	}
}

// TestGraphReadCapsNodes pins the 300-node cap and the truncated marker.
func TestGraphReadCapsNodes(t *testing.T) {
	fakes := newToolFakes()
	view := mindmapapp.GraphView{VersionID: 7, VersionStatus: "designing"}
	for index := 0; index < 301; index++ {
		view.Nodes = append(view.Nodes, mindmapapp.GraphNode{Node: mindmapdomain.CaseNode{
			ID: int64(1000 + index), VersionID: 7, Kind: mindmapdomain.NodeKindCase,
			Title: fmt.Sprintf("case %d", index),
		}})
	}
	fakes.graphs.view = view
	tools := fakes.tools(t)

	result, err := executeTool(t, tools, "case.graph_read", `{"versionId":"7"}`, context.Background())
	if err != nil {
		t.Fatalf("case.graph_read failed: %v", err)
	}
	payload := decodeContentMap(t, result)
	if payload["nodeCount"] != float64(301) {
		t.Fatalf("nodeCount=%v want 301", payload["nodeCount"])
	}
	if payload["truncated"] != true {
		t.Fatal("a graph above 300 nodes must be truncated")
	}
	if nodes := asArray(t, payload["nodes"]); len(nodes) != 300 {
		t.Fatalf("nodes=%d want 300", len(nodes))
	}
}

// TestGraphReadCapsBytes pins the 64 KiB output cap while keeping the payload a
// valid JSON object.
func TestGraphReadCapsBytes(t *testing.T) {
	fakes := newToolFakes()
	view := mindmapapp.GraphView{VersionID: 7, VersionStatus: "designing"}
	for index := 0; index < 300; index++ {
		view.Nodes = append(view.Nodes, mindmapapp.GraphNode{Node: mindmapdomain.CaseNode{
			ID: int64(1000 + index), VersionID: 7, Kind: mindmapdomain.NodeKindCase,
			Title: strings.Repeat("测", 400),
		}})
	}
	fakes.graphs.view = view
	tools := fakes.tools(t)

	result, err := executeTool(t, tools, "case.graph_read", `{"versionId":"7"}`, context.Background())
	if err != nil {
		t.Fatalf("case.graph_read failed: %v", err)
	}
	if len(result.Content) > 64*1024 {
		t.Fatalf("content has %d bytes; the limit is 65536", len(result.Content))
	}
	payload := decodeContentMap(t, result)
	if payload["truncated"] != true {
		t.Fatal("an oversized graph summary must set truncated")
	}
	if nodes := asArray(t, payload["nodes"]); len(nodes) >= 300 {
		t.Fatalf("nodes=%d want fewer than 300 after byte truncation", len(nodes))
	}
}

// TestGraphReadMapsMissingVersionToInvalidArguments proves a not-found version is
// a correctable tool error, not a panic or an infrastructure failure.
func TestGraphReadMapsMissingVersionToInvalidArguments(t *testing.T) {
	fakes := newToolFakes()
	fakes.graphs.err = mindmapdomain.NewNotFound("version", 7)
	tools := fakes.tools(t)

	_, err := executeTool(t, tools, "case.graph_read", `{"versionId":"7"}`, context.Background())
	requireToolErrorCode(t, err, "invalid_arguments")
}

// TestGraphProposeStoresPayload pins case.graph_propose: the run is resolved by
// the invocation session, only the run result/stats are written (last write
// wins) and the returned counts are honest.
func TestGraphProposeStoresPayload(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
	tools := fakes.tools(t)
	ctx := toolContext(11, 42)
	payload := `{"scenarios":[{"ref":"s1","title":"下单主流程","parentRef":null,"description":""}],"cases":[{"ref":"c1","scenarioRef":"s1","title":"正常下单","priority":"P1","requirementIds":["1838"],"preconditions":"已登录","testData":{"sku":"A-1"},"steps":[{"action":"打开","expected":"展示","data":""}],"assertions":[{"stepIndex":0,"kind":"ui","target":"/order/confirm","expression":"订单号非空","expected":"生成订单号"}],"anchors":[{"file":"src/order.ts","startLine":1,"endLine":2}]}]}`
	args := `{"versionId":"7","mode":"append","payload":` + payload + `}`

	result, err := executeTool(t, tools, "case.graph_propose", args, ctx)
	if err != nil {
		t.Fatalf("case.graph_propose failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["stored"] != true || content["proposals"] != float64(1) {
		t.Fatalf("content=%v", content)
	}
	if content["scenarios"] != float64(1) || content["cases"] != float64(1) {
		t.Fatalf("counts=%v", content)
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("run updates=%d want 1", len(fakes.runs.updates))
	}
	update := fakes.runs.updates[0]
	if update.runID != 501 || update.from != mindmapdomain.RunRunning || update.to != mindmapdomain.RunRunning {
		t.Fatalf("update=%+v", update)
	}
	var stored map[string]any
	if err := json.Unmarshal(update.patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	if stored["mode"] != "append" {
		t.Fatalf("stored mode=%v want append", stored["mode"])
	}
	if scenarios := asArray(t, stored["scenarios"]); len(scenarios) != 1 {
		t.Fatalf("stored scenarios=%d want 1", len(scenarios))
	}
	var stats map[string]any
	if err := json.Unmarshal(update.patch.Stats, &stats); err != nil {
		t.Fatalf("stats are not JSON: %v", err)
	}
	if stats["proposals"] != float64(1) {
		t.Fatalf("stats.proposals=%v want 1", stats["proposals"])
	}
	if stats["contextTruncated"] != true {
		t.Fatalf("an existing stats key must be preserved: %v", stats)
	}
	if fakes.graphs.calls != 0 {
		t.Fatal("case.graph_propose must not read or write the business graph")
	}

	// A repeat call keeps the last payload and bumps stats.proposals.
	result, err = executeTool(t, tools, "case.graph_propose", args, ctx)
	if err != nil {
		t.Fatalf("second case.graph_propose failed: %v", err)
	}
	if content := decodeContentMap(t, result); content["proposals"] != float64(2) {
		t.Fatalf("second proposals=%v want 2", content["proposals"])
	}
	if len(fakes.runs.updates) != 2 {
		t.Fatalf("run updates=%d want 2", len(fakes.runs.updates))
	}
}

// TestGraphProposeAcceptsFlatShape pins the spec §6.7 / fake-LLM input shape:
// scenarios and cases arrive at the top level, with the optional top-level mode
// and confirmReplace merged into the stored payload.
func TestGraphProposeAcceptsFlatShape(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
	tools := fakes.tools(t)
	args := `{"versionId":"7","mode":"append","confirmReplace":true,` +
		`"scenarios":[{"ref":"s1","title":"下单主流程","parentRef":null,"description":"场景"}],` +
		`"cases":[{"ref":"c1","scenarioRef":"s1","title":"正常下单","priority":"P1","requirementIds":["1838"],` +
		`"preconditions":"已登录","testData":{"sku":"A-1"},"steps":[{"action":"打开","expected":"展示"}],` +
		`"assertions":[{"kind":"ui","expression":"订单号非空"}]}]}`

	result, err := executeTool(t, tools, "case.graph_propose", args, toolContext(11, 42))
	if err != nil {
		t.Fatalf("flat case.graph_propose failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["stored"] != true || content["proposals"] != float64(1) {
		t.Fatalf("content=%v", content)
	}
	if content["scenarios"] != float64(1) || content["cases"] != float64(1) {
		t.Fatalf("counts=%v", content)
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("run updates=%d want 1", len(fakes.runs.updates))
	}
	var stored map[string]any
	if err := json.Unmarshal(fakes.runs.updates[0].patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	if stored["mode"] != "append" {
		t.Fatalf("stored mode=%v want append", stored["mode"])
	}
	if stored["confirmReplace"] != true {
		t.Fatalf("top-level confirmReplace must be merged into the payload: %v", stored)
	}
	scenarios := asArray(t, stored["scenarios"])
	if len(scenarios) != 1 || asMap(t, scenarios[0])["ref"] != "s1" {
		t.Fatalf("stored scenarios=%v", scenarios)
	}
	cases := asArray(t, stored["cases"])
	if len(cases) != 1 || asMap(t, cases[0])["ref"] != "c1" {
		t.Fatalf("stored cases=%v", cases)
	}
}

// TestGraphProposeFlatShapeWinsOverNested pins the documented ambiguity rule:
// when both shapes are present the flat top-level fields win and the nested
// payload is ignored.
func TestGraphProposeFlatShapeWinsOverNested(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
	tools := fakes.tools(t)
	args := `{"versionId":"7","mode":"replace",` +
		`"scenarios":[{"ref":"flat-s","title":"Flat 场景"}],` +
		`"cases":[{"ref":"flat-c","title":"Flat 用例","priority":"P2","requirementIds":["1838"],"steps":[],"assertions":[]}],` +
		`"payload":{"mode":"append","scenarios":[{"ref":"nested-s","title":"Nested 场景"}]}}`

	result, err := executeTool(t, tools, "case.graph_propose", args, toolContext(11, 42))
	if err != nil {
		t.Fatalf("flat-wins case.graph_propose failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["scenarios"] != float64(1) || content["cases"] != float64(1) {
		t.Fatalf("counts=%v want the flat proposal", content)
	}
	var stored map[string]any
	if err := json.Unmarshal(fakes.runs.updates[0].patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	if stored["mode"] != "replace" {
		t.Fatalf("stored mode=%v want the flat replace", stored["mode"])
	}
	scenarios := asArray(t, stored["scenarios"])
	if len(scenarios) != 1 || asMap(t, scenarios[0])["ref"] != "flat-s" {
		t.Fatalf("stored scenarios=%v want the flat scenario", scenarios)
	}
	cases := asArray(t, stored["cases"])
	if len(cases) != 1 || asMap(t, cases[0])["ref"] != "flat-c" {
		t.Fatalf("stored cases=%v want the flat case", cases)
	}
}

// TestGraphProposeRejectsOversize pins the 1 MiB proposal cap on both the nested
// and the flat input shape.
func TestGraphProposeRejectsOversize(t *testing.T) {
	tests := []struct {
		name string
		args func(t *testing.T) string
	}{
		{
			name: "nested",
			args: func(t *testing.T) string {
				t.Helper()
				payload, err := json.Marshal(map[string]any{
					"scenarios": []any{},
					"cases": []any{map[string]any{
						"ref": "c1", "title": strings.Repeat("a", (1<<20)+16),
						"priority": "P1", "requirementIds": []string{"1"},
						"steps": []any{}, "assertions": []any{},
					}},
				})
				if err != nil {
					t.Fatalf("marshal oversized payload: %v", err)
				}
				args, err := json.Marshal(map[string]any{"versionId": "7", "payload": json.RawMessage(payload)})
				if err != nil {
					t.Fatalf("marshal args: %v", err)
				}
				return string(args)
			},
		},
		{
			name: "flat",
			args: func(t *testing.T) string {
				t.Helper()
				args, err := json.Marshal(map[string]any{
					"versionId": "7",
					"mode":      "append",
					"scenarios": []any{},
					"cases": []any{map[string]any{
						"ref": "c1", "title": strings.Repeat("a", (1<<20)+16),
						"priority": "P1", "requirementIds": []string{"1"},
						"steps": []any{}, "assertions": []any{},
					}},
				})
				if err != nil {
					t.Fatalf("marshal oversized flat args: %v", err)
				}
				return string(args)
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fakes := newToolFakes()
			fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
			tools := fakes.tools(t)

			_, executeErr := executeTool(t, tools, "case.graph_propose", testCase.args(t), toolContext(11, 42))
			requireToolErrorCode(t, executeErr, "proposal_too_large")
			if len(fakes.runs.updates) != 0 {
				t.Fatal("an oversized proposal must not be stored")
			}
		})
	}
}

// TestSubmitToolsRequireActiveRun pins the session->run resolution: no active
// run, a foreign session, a mismatched version or a mismatched kind all fail
// with run_not_found and never write.
func TestSubmitToolsRequireActiveRun(t *testing.T) {
	tests := []struct {
		name    string
		tool    string
		args    string
		runs    []mindmapdomain.CaseDesignRun
		context context.Context
	}{
		{
			name: "no active run", tool: "case.graph_propose",
			args: `{"versionId":"7","payload":{"scenarios":[],"cases":[]}}`,
			runs: nil, context: toolContext(11, 42),
		},
		{
			name: "no active run flat shape", tool: "case.graph_propose",
			args: `{"versionId":"7","mode":"append","scenarios":[],"cases":[]}`,
			runs: nil, context: toolContext(11, 42),
		},
		{
			name: "foreign session", tool: "case.graph_propose",
			args:    `{"versionId":"7","payload":{"scenarios":[],"cases":[]}}`,
			runs:    []mindmapdomain.CaseDesignRun{activeRun(501, 7, 99, mindmapdomain.RunKindDesign)},
			context: toolContext(11, 42),
		},
		{
			name: "mismatched version", tool: "case.graph_propose",
			args:    `{"versionId":"7","payload":{"scenarios":[],"cases":[]}}`,
			runs:    []mindmapdomain.CaseDesignRun{activeRun(501, 8, 11, mindmapdomain.RunKindDesign)},
			context: toolContext(11, 42),
		},
		{
			name: "mismatched kind", tool: "case.review_submit",
			args:    `{"versionId":"7","findings":[]}`,
			runs:    []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)},
			context: toolContext(11, 42),
		},
		{
			name: "no invocation context", tool: "case.data_plan_submit",
			args:    `{"versionId":"7","items":[{"nodeId":"102","nodeRevision":1}]}`,
			runs:    []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDataPlan)},
			context: context.Background(),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fakes := newToolFakes()
			fakes.runs.runs = testCase.runs
			tools := fakes.tools(t)
			_, err := executeTool(t, tools, testCase.tool, testCase.args, testCase.context)
			requireToolErrorCode(t, err, "run_not_found")
			if len(fakes.runs.updates) != 0 {
				t.Fatal("an unresolved run must not be written")
			}
		})
	}
}

// TestGraphProposeValidatesArguments pins the mode/version/payload validation.
func TestGraphProposeValidatesArguments(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
	tools := fakes.tools(t)
	tests := []string{
		`{"payload":{"scenarios":[],"cases":[]}}`,
		`{"versionId":"abc","payload":{"scenarios":[],"cases":[]}}`,
		`{"versionId":"7"}`,
		`{"versionId":"7","payload":[]}`,
		`{"versionId":"7","payload":null}`,
		`{"versionId":"7","mode":"merge","payload":{"scenarios":[],"cases":[]}}`,
		// Flat shape: both scenarios and cases are required and must be arrays.
		`{"versionId":"7","scenarios":[]}`,
		`{"versionId":"7","cases":[]}`,
		`{"versionId":"7","scenarios":{},"cases":[]}`,
		`{"versionId":"7","scenarios":[],"cases":{}}`,
		`{"versionId":"7","scenarios":null,"cases":[]}`,
		`{"versionId":"7","mode":"append","anchors":[]}`,
	}
	for _, args := range tests {
		_, err := executeTool(t, tools, "case.graph_propose", args, toolContext(11, 42))
		requireToolErrorCode(t, err, "invalid_arguments")
	}
	if len(fakes.runs.updates) != 0 {
		t.Fatal("invalid arguments must not be stored")
	}
}

// TestReviewSubmitStoresFindings pins case.review_submit's payload, counts and
// enum validation.
func TestReviewSubmitStoresFindings(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(601, 7, 12, mindmapdomain.RunKindReview)}
	tools := fakes.tools(t)
	args := `{"versionId":"7","findings":[{"nodeId":"102","category":"coverage","severity":"blocker","detail":"缺少正常路径"},{"category":"assertion","severity":"minor","detail":"断言太弱"}]}`

	result, err := executeTool(t, tools, "case.review_submit", args, toolContext(12, 42))
	if err != nil {
		t.Fatalf("case.review_submit failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["stored"] != true || content["findings"] != float64(2) || content["blockers"] != float64(1) {
		t.Fatalf("content=%v", content)
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("run updates=%d want 1", len(fakes.runs.updates))
	}
	var stored map[string]any
	if err := json.Unmarshal(fakes.runs.updates[0].patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	findings := asArray(t, stored["findings"])
	if len(findings) != 2 {
		t.Fatalf("stored findings=%d want 2", len(findings))
	}
	first := asMap(t, findings[0])
	if first["nodeId"] != "102" || first["category"] != "coverage" || first["severity"] != "blocker" || first["detail"] != "缺少正常路径" {
		t.Fatalf("first finding=%v", first)
	}
	if _, present := asMap(t, findings[1])["nodeId"]; present {
		t.Fatal("a version-level finding must omit nodeId")
	}
}

// TestReviewSubmitRejectsOverCapAndBadEnums pins the ≤200 findings, ≤2000 detail
// and enum rules.
func TestReviewSubmitRejectsOverCapAndBadEnums(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(601, 7, 12, mindmapdomain.RunKindReview)}
	tools := fakes.tools(t)

	findings := make([]any, 0, 201)
	for index := 0; index < 201; index++ {
		findings = append(findings, map[string]any{
			"category": "other", "severity": "info", "detail": fmt.Sprintf("finding %d", index),
		})
	}
	overCap, err := json.Marshal(map[string]any{"versionId": "7", "findings": findings})
	if err != nil {
		t.Fatalf("marshal findings: %v", err)
	}
	tests := []string{
		string(overCap),
		`{"versionId":"7","findings":[{"category":"coverage","severity":"blocker","detail":"` + strings.Repeat("x", 2001) + `"}]}`,
		`{"versionId":"7","findings":[{"category":"style","severity":"blocker","detail":"x"}]}`,
		`{"versionId":"7","findings":[{"category":"coverage","severity":"critical","detail":"x"}]}`,
		`{"versionId":"7","findings":[{"category":"coverage","severity":"blocker","detail":""}]}`,
		`{"versionId":"7","findings":[{"nodeId":"x1","category":"coverage","severity":"blocker","detail":"x"}]}`,
	}
	for _, args := range tests {
		_, err := executeTool(t, tools, "case.review_submit", args, toolContext(12, 42))
		requireToolErrorCode(t, err, "invalid_arguments")
	}
	if len(fakes.runs.updates) != 0 {
		t.Fatal("invalid findings must not be stored")
	}
}

// TestDataPlanSubmitStoresItems pins case.data_plan_submit's payload and cap.
func TestDataPlanSubmitStoresItems(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(701, 7, 13, mindmapdomain.RunKindDataPlan)}
	tools := fakes.tools(t)
	args := `{"versionId":"7","items":[{"nodeId":"102","nodeRevision":4,"preconditions":"已登录且购物车非空","testData":{"sku":"A-1","qty":1}}]}`

	result, err := executeTool(t, tools, "case.data_plan_submit", args, toolContext(13, 42))
	if err != nil {
		t.Fatalf("case.data_plan_submit failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["stored"] != true || content["items"] != float64(1) {
		t.Fatalf("content=%v", content)
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("run updates=%d want 1", len(fakes.runs.updates))
	}
	var stored map[string]any
	if err := json.Unmarshal(fakes.runs.updates[0].patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	items := asArray(t, stored["items"])
	item := asMap(t, items[0])
	if item["nodeId"] != "102" || item["nodeRevision"] != float64(4) {
		t.Fatalf("stored item=%v", item)
	}
	if item["preconditions"] != "已登录且购物车非空" {
		t.Fatalf("stored preconditions=%v", item["preconditions"])
	}

	items61 := make([]any, 0, 61)
	for index := 0; index < 61; index++ {
		items61 = append(items61, map[string]any{"nodeId": fmt.Sprintf("%d", 100+index), "nodeRevision": 1})
	}
	overCap, err := json.Marshal(map[string]any{"versionId": "7", "items": items61})
	if err != nil {
		t.Fatalf("marshal items: %v", err)
	}
	for _, invalid := range []string{
		string(overCap),
		`{"versionId":"7","items":[]}`,
		`{"versionId":"7","items":[{"nodeId":"102","nodeRevision":0}]}`,
		`{"versionId":"7","items":[{"nodeId":"0","nodeRevision":1}]}`,
		`{"versionId":"7","items":[{"nodeId":"102","nodeRevision":1,"preconditions":"` + strings.Repeat("x", 4001) + `"}]}`,
	} {
		_, executeErr := executeTool(t, tools, "case.data_plan_submit", invalid, toolContext(13, 42))
		requireToolErrorCode(t, executeErr, "invalid_arguments")
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("invalid items must not be stored (updates=%d)", len(fakes.runs.updates))
	}
}

// TestAppModelProposeStoresNodes pins app_model_propose's payload: it only stores
// the proposal (no import) and validates the shape.
func TestAppModelProposeStoresNodes(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(801, 7, 14, mindmapdomain.RunKindExplore)}
	tools := fakes.tools(t)
	args := `{"systemId":"11","nodes":[{"key":"page:/order","kind":"page","name":"订单"},{"key":"route:/order/confirm","kind":"route","name":"确认","path":"/order/confirm","method":"GET","parentKey":"page:/order"}]}`

	result, err := executeTool(t, tools, "app_model_propose", args, toolContext(14, 42))
	if err != nil {
		t.Fatalf("app_model_propose failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["stored"] != true || content["nodes"] != float64(2) {
		t.Fatalf("content=%v", content)
	}
	if fakes.catalog.importCalls != 0 {
		t.Fatal("app_model_propose must never import app models")
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("run updates=%d want 1", len(fakes.runs.updates))
	}
	var stored map[string]any
	if err := json.Unmarshal(fakes.runs.updates[0].patch.Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v", err)
	}
	if stored["systemId"] != "11" {
		t.Fatalf("stored systemId=%v want 11", stored["systemId"])
	}
	nodes := asArray(t, stored["nodes"])
	if len(nodes) != 2 || asMap(t, nodes[1])["parentKey"] != "page:/order" {
		t.Fatalf("stored nodes=%v", nodes)
	}

	nodes201 := make([]any, 0, 201)
	for index := 0; index < 201; index++ {
		nodes201 = append(nodes201, map[string]any{
			"key": fmt.Sprintf("page:/p%d", index), "kind": "page", "name": fmt.Sprintf("p%d", index),
		})
	}
	overCap, err := json.Marshal(map[string]any{"systemId": "11", "nodes": nodes201})
	if err != nil {
		t.Fatalf("marshal nodes: %v", err)
	}
	for _, invalid := range []string{
		string(overCap),
		`{"systemId":"11","nodes":[]}`,
		`{"systemId":"11","nodes":[{"key":"x","kind":"flow","name":"x"}]}`,
		`{"systemId":"11","nodes":[{"key":"page:/a","kind":"page","name":"a"},{"key":"page:/a","kind":"page","name":"b"}]}`,
		`{"systemId":"11","nodes":[{"key":"page:/a","kind":"page","name":"a","parentKey":"page:/missing"}]}`,
		`{"systemId":"11","nodes":[{"key":"page:/a","kind":"page"}]}`,
	} {
		_, executeErr := executeTool(t, tools, "app_model_propose", invalid, toolContext(14, 42))
		requireToolErrorCode(t, executeErr, "invalid_arguments")
	}
	if len(fakes.runs.updates) != 1 {
		t.Fatalf("invalid nodes must not be stored (updates=%d)", len(fakes.runs.updates))
	}
}

// TestExploreHTTPGetAndHead pins the GET/HEAD methods and the read-only summary.
func TestExploreHTTPGetAndHead(t *testing.T) {
	fakes := newToolFakes()
	fakes.prober.result = mindmapapp.ProbeResult{
		URL: "http://app.example.test/", Status: 200, ContentType: "text/html", Title: "首页",
		Links: []string{"/order", "/login"}, Forms: []string{"POST /logout", "GET /search"}, Truncated: false,
	}
	tools := fakes.tools(t)

	result, err := executeTool(t, tools, "explore.http", `{"url":"http://app.example.test/"}`, context.Background())
	if err != nil {
		t.Fatalf("explore.http GET failed: %v", err)
	}
	content := decodeContentMap(t, result)
	if content["method"] != "GET" || content["status"] != float64(200) || content["title"] != "首页" {
		t.Fatalf("content=%v", content)
	}
	if content["truncated"] != false {
		t.Fatalf("truncated=%v want false", content["truncated"])
	}
	links := asArray(t, content["links"])
	if len(links) != 2 || links[0] != "/order" {
		t.Fatalf("links=%v", links)
	}
	forms := asArray(t, content["forms"])
	if len(forms) != 2 || forms[1] != "GET /search" {
		t.Fatalf("forms=%v", forms)
	}
	if fakes.prober.calls != 1 || fakes.prober.method != "GET" || fakes.prober.url != "http://app.example.test/" {
		t.Fatalf("probe call=%+v", fakes.prober)
	}

	result, err = executeTool(t, tools, "explore.http", `{"url":"http://app.example.test/","method":"HEAD"}`, context.Background())
	if err != nil {
		t.Fatalf("explore.http HEAD failed: %v", err)
	}
	if content := decodeContentMap(t, result); content["method"] != "HEAD" {
		t.Fatalf("method=%v want HEAD", content["method"])
	}
	if fakes.prober.calls != 2 || fakes.prober.method != "HEAD" {
		t.Fatalf("HEAD must reach the method-aware prober, got %+v", fakes.prober)
	}
}

// TestExploreHTTPRejectsUnsafeRequests pins the non-GET/HEAD and allowlist
// failures.
func TestExploreHTTPRejectsUnsafeRequests(t *testing.T) {
	tests := []struct {
		name string
		args string
		err  error
		code string
	}{
		{name: "post", args: `{"url":"http://app.example.test/","method":"POST"}`, code: "explore_not_allowed"},
		{name: "put", args: `{"url":"http://app.example.test/","method":"PUT"}`, code: "explore_not_allowed"},
		{name: "empty url", args: `{"url":""}`, code: "invalid_arguments"},
		{name: "missing url", args: `{}`, code: "invalid_arguments"},
		{
			name: "host outside allowlist", args: `{"url":"http://evil.example.test/"}`,
			err:  fmt.Errorf("httpclient: host is not allowlisted: %w", httpclient.ErrHostNotAllowed),
			code: "explore_not_allowed",
		},
		{
			name: "private address", args: `{"url":"http://127.0.0.1/"}`,
			err:  fmt.Errorf("httpclient: private address: %w", httpclient.ErrPrivateIP),
			code: "explore_not_allowed",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			fakes := newToolFakes()
			fakes.prober.err = testCase.err
			tools := fakes.tools(t)
			_, err := executeTool(t, tools, "explore.http", testCase.args, context.Background())
			requireToolErrorCode(t, err, testCase.code)
			if testCase.code == "explore_not_allowed" && strings.Contains(testCase.args, "POST") && fakes.prober.calls != 0 {
				t.Fatal("a non-GET/HEAD method must be rejected before probing")
			}
		})
	}
}

// TestExploreHTTPUsesRunEnvironmentAllowlist pins the per-environment SSRF
// allowlist: inside an explore run the tool forwards the run environment's base
// host and configured allowed_hosts to the prober, so one shared prober can
// serve many environments without weakening the host check.
func TestExploreHTTPUsesRunEnvironmentAllowlist(t *testing.T) {
	fakes := newToolFakes()
	fakes.catalog.environments[31] = mindmapapp.EnvironmentSnapshot{
		ID: 31, BaseURL: "http://app.example.test", AllowedHosts: []string{"cdn.example.test"},
	}
	run := activeRun(901, 7, 15, mindmapdomain.RunKindExplore)
	run.Input = []byte(`{"systemId":11,"environmentId":31}`)
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{run}
	tools := fakes.tools(t)

	if _, err := executeTool(t, tools, "explore.http", `{"url":"http://app.example.test/"}`, toolContext(15, 42)); err != nil {
		t.Fatalf("explore.http failed: %v", err)
	}
	if len(fakes.prober.hosts) != 2 {
		t.Fatalf("forwarded allowed hosts=%v want [app.example.test cdn.example.test]", fakes.prober.hosts)
	}
	if fakes.prober.hosts[0] != "app.example.test" || fakes.prober.hosts[1] != "cdn.example.test" {
		t.Fatalf("forwarded allowed hosts=%v", fakes.prober.hosts)
	}
}

// TestExploreHTTPCapsOutput pins the 32 KiB summary cap.
func TestExploreHTTPCapsOutput(t *testing.T) {
	fakes := newToolFakes()
	links := make([]string, 0, 2000)
	for index := 0; index < 2000; index++ {
		links = append(links, fmt.Sprintf("/page/%d/%s", index, strings.Repeat("x", 80)))
	}
	fakes.prober.result = mindmapapp.ProbeResult{
		URL: "http://app.example.test/", Status: 200, Title: "首页", Links: links,
	}
	tools := fakes.tools(t)

	result, err := executeTool(t, tools, "explore.http", `{"url":"http://app.example.test/"}`, context.Background())
	if err != nil {
		t.Fatalf("explore.http failed: %v", err)
	}
	if len(result.Content) > 32*1024 {
		t.Fatalf("content has %d bytes; the limit is 32768", len(result.Content))
	}
	payload := decodeContentMap(t, result)
	if payload["truncated"] != true {
		t.Fatal("an oversized probe summary must set truncated")
	}
}

// TestToolAuditRecordsRunScopedInvocation pins the mindmap.tool.invoked audit
// entry: tool/run/actor and counts only, never payload content.
func TestToolAuditRecordsRunScopedInvocation(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{activeRun(501, 7, 11, mindmapdomain.RunKindDesign)}
	tools := fakes.tools(t)
	args := `{"versionId":"7","payload":{"scenarios":[],"cases":[{"ref":"c1","title":"C","priority":"P1","requirementIds":["1838"],"steps":[],"assertions":[]}]}}`
	if _, err := executeTool(t, tools, "case.graph_propose", args, toolContext(11, 42)); err != nil {
		t.Fatalf("case.graph_propose failed: %v", err)
	}
	if len(fakes.audit.events) != 1 {
		t.Fatalf("audit events=%d want 1", len(fakes.audit.events))
	}
	event := fakes.audit.events[0]
	if event.Action != "mindmap.tool.invoked" {
		t.Fatalf("audit action=%q", event.Action)
	}
	if event.TargetType != "case_design_run" || event.TargetID != "501" {
		t.Fatalf("audit target=%q/%q", event.TargetType, event.TargetID)
	}
	if event.ActorID != 42 {
		t.Fatalf("audit actor=%d want 42", event.ActorID)
	}
	if event.Metadata["tool"] != "case.graph_propose" || event.Metadata["runId"] != int64(501) {
		t.Fatalf("audit metadata=%v", event.Metadata)
	}
	if _, present := event.Metadata["payload"]; present {
		t.Fatal("audit metadata must not carry the proposal payload")
	}
}

// TestToolsRejectNonObjectArguments pins the shared argument contract: null,
// arrays, scalars and missing required fields are correctable
// invalid_arguments, never panics.
func TestToolsRejectNonObjectArguments(t *testing.T) {
	fakes := newToolFakes()
	fakes.runs.runs = []mindmapdomain.CaseDesignRun{
		activeRun(501, 7, 11, mindmapdomain.RunKindDesign),
		activeRun(601, 7, 11, mindmapdomain.RunKindReview),
		activeRun(701, 7, 11, mindmapdomain.RunKindDataPlan),
		activeRun(801, 7, 11, mindmapdomain.RunKindExplore),
	}
	tools := fakes.tools(t)
	for _, name := range expectedToolNames {
		for _, args := range []string{`null`, `[]`, `"text"`, `123`, `{}`} {
			func() {
				defer func() {
					if recovered := recover(); recovered != nil {
						t.Fatalf("%s panicked on %q: %v", name, args, recovered)
					}
				}()
				_, err := executeTool(t, tools, name, args, toolContext(11, 42))
				requireToolErrorCode(t, err, "invalid_arguments")
			}()
		}
	}
	if len(fakes.runs.updates) != 0 {
		t.Fatal("invalid arguments must not write a run")
	}
}

// schemaObject decodes one tool schema as a generic JSON object.
func schemaObject(t *testing.T, tool agentdomain.Tool) map[string]any {
	t.Helper()
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema(), &schema); err != nil {
		t.Fatalf("%s schema is not valid JSON: %v", tool.Name(), err)
	}
	if schema["type"] != "object" {
		t.Fatalf("%s schema type=%v want object", tool.Name(), schema["type"])
	}
	return schema
}

// requiredSet returns the schema's required field set.
func requiredSet(t *testing.T, schema map[string]any) map[string]bool {
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

// propertyMap returns the schema's properties object.
func propertyMap(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties=%v want an object", schema["properties"])
	}
	return properties
}

// schemaProperty returns one schema property object.
func schemaProperty(t *testing.T, tool agentdomain.Tool, field string) map[string]any {
	t.Helper()
	property, ok := propertyMap(t, schemaObject(t, tool))[field].(map[string]any)
	if !ok {
		t.Fatalf("%s schema is missing property %q", tool.Name(), field)
	}
	return property
}
