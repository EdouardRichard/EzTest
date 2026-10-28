package agent_tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	agentapp "github.com/EdouardRichard/EzTest/backend/internal/agent/application"
	agentdomain "github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	mindmapapp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	mindmapdomain "github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// --- fakes -----------------------------------------------------------------

// fakeRunStore implements mindmap/application.RunRepository for the tool tests.
// Only the run-scope read and the result CAS are meaningful; the other methods
// exist to satisfy the port.
type fakeRunStore struct {
	mu          sync.Mutex
	runs        map[int64]mindmapdomain.CaseDesignRun
	updateCalls int
	lastPatch   mindmapapp.RunPatch
	updateErr   error
	listErr     error
}

func newFakeRunStore(runs ...mindmapdomain.CaseDesignRun) *fakeRunStore {
	store := &fakeRunStore{runs: make(map[int64]mindmapdomain.CaseDesignRun, len(runs))}
	for _, run := range runs {
		store.runs[run.ID] = run
	}
	return store
}

func (f *fakeRunStore) Create(_ context.Context, run mindmapdomain.CaseDesignRun) (mindmapdomain.CaseDesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs[run.ID] = run
	return run, nil
}

func (f *fakeRunStore) Get(_ context.Context, versionID, runID int64) (mindmapdomain.CaseDesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	run, ok := f.runs[runID]
	if !ok || run.VersionID != versionID {
		return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", runID)
	}
	return run, nil
}

func (f *fakeRunStore) List(_ context.Context, _ int64, _, _ string, _ int, _ string) ([]mindmapdomain.CaseDesignRun, string, bool, error) {
	return nil, "", false, nil
}

func (f *fakeRunStore) UpdateStatus(_ context.Context, runID int64, from, to string, patch mindmapapp.RunPatch) (mindmapdomain.CaseDesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.updateCalls++
	f.lastPatch = patch
	if f.updateErr != nil {
		return mindmapdomain.CaseDesignRun{}, f.updateErr
	}
	run, ok := f.runs[runID]
	if !ok {
		return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", runID)
	}
	if run.Status != from {
		return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewStateConflict("run_state_conflict", "run moved on")
	}
	if len(patch.Result) > 0 {
		run.Result = append([]byte(nil), patch.Result...)
	}
	if len(patch.Stats) > 0 {
		run.Stats = append([]byte(nil), patch.Stats...)
	}
	run.Status = to
	f.runs[runID] = run
	return run, nil
}

func (f *fakeRunStore) LatestByKind(_ context.Context, _ int64, _ string) (mindmapdomain.CaseDesignRun, error) {
	return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", 0)
}

func (f *fakeRunStore) ActiveByKind(_ context.Context, versionID int64, kind string) (mindmapdomain.CaseDesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, run := range f.runs {
		if run.VersionID == versionID && run.Kind == kind && run.IsActive() {
			return run, nil
		}
	}
	return mindmapdomain.CaseDesignRun{}, mindmapdomain.NewNotFound("run", 0)
}

func (f *fakeRunStore) ListActive(_ context.Context, limit int) ([]mindmapdomain.CaseDesignRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	active := make([]mindmapdomain.CaseDesignRun, 0, len(f.runs))
	for _, run := range f.runs {
		if run.IsActive() {
			active = append(active, run)
		}
	}
	if limit > 0 && len(active) > limit {
		active = active[:limit]
	}
	return active, nil
}

func (f *fakeRunStore) RecoverStale(_ context.Context, _ time.Time) (int64, error) { return 0, nil }

// fakeGraph implements the case.graph_read style GraphReader slice.
type fakeGraph struct {
	view mindmapapp.GraphView
	err  error
}

func (f *fakeGraph) Get(_ context.Context, _ int64) (mindmapapp.GraphView, error) {
	return f.view, f.err
}

// fakeRequirements implements mindmap/application.RequirementReader.
type fakeRequirements struct {
	confirmed []mindmapapp.RequirementSnapshot
	relations []mindmapapp.RelationSnapshot
}

func (f *fakeRequirements) ListConfirmed(_ context.Context, _ int64) ([]mindmapapp.RequirementSnapshot, error) {
	return f.confirmed, nil
}

func (f *fakeRequirements) ListRelations(_ context.Context, _ int64) ([]mindmapapp.RelationSnapshot, error) {
	return f.relations, nil
}

// fakeImpact implements the read-only P4 port.
type fakeImpact struct {
	snapshot   mindmapapp.ImpactSnapshot
	snapshotOK bool
	candidates []mindmapapp.ImpactCandidate
}

func (f *fakeImpact) GetLatestAnalysis(_ context.Context, _ int64) (mindmapapp.ImpactSnapshot, error) {
	if !f.snapshotOK {
		return mindmapapp.ImpactSnapshot{}, nil
	}
	return f.snapshot, nil
}

func (f *fakeImpact) ListCandidates(_ context.Context, _ int64) ([]mindmapapp.ImpactCandidate, error) {
	return f.candidates, nil
}

func (f *fakeImpact) FileExists(_ context.Context, _ int64, _ string) (bool, error) {
	return false, nil
}

func (f *fakeImpact) ListFacts(_ context.Context, _ int64, _ int) ([]mindmapapp.FactSummary, error) {
	return nil, nil
}

func (f *fakeImpact) ListBaselineFiles(_ context.Context, _ int64, _ int) ([]string, error) {
	return nil, nil
}

// fakeResults implements the optional execution result reader.
type fakeResults struct {
	summary BatchResultSummary
	err     error
}

func (f *fakeResults) LatestBatchResults(_ context.Context, _ int64, _ int64) (BatchResultSummary, error) {
	return f.summary, f.err
}

// --- helpers ---------------------------------------------------------------

// testRun is the active supplement run every tool resolves in these tests.
func testRun(kind string) mindmapdomain.CaseDesignRun {
	batchID := int64(41)
	return mindmapdomain.CaseDesignRun{
		ID:        55,
		VersionID: 7,
		Kind:      kind,
		Status:    mindmapdomain.RunRunning,
		BatchID:   &batchID,
	}
}

// toolContext builds the P1 invocation scope of a supplement run.
func toolContext(run mindmapdomain.CaseDesignRun, actor int64) context.Context {
	return agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{
		SessionID:       900,
		RunID:           901,
		ActorUserID:     &actor,
		ProfileKey:      mindmapapp.ProfileKeySupplement,
		VersionID:       run.VersionID,
		BatchID:         derefBatch(run),
		SupplementRunID: run.ID,
	})
}

func derefBatch(run mindmapdomain.CaseDesignRun) int64 {
	if run.BatchID == nil {
		return 0
	}
	return *run.BatchID
}

// toolByName returns one registered tool.
func toolByName(t *testing.T, tools []agentdomain.Tool, name string) agentdomain.Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name() == name {
			return tool
		}
	}
	t.Fatalf("tool %q is not registered (have %d tools)", name, len(tools))
	return nil
}

// executeTool runs one tool and decodes a successful JSON result.
func executeTool(t *testing.T, tool agentdomain.Tool, ctx context.Context, args string) map[string]any {
	t.Helper()
	result, err := tool.Execute(ctx, json.RawMessage(args))
	if err != nil {
		t.Fatalf("%s returned an unexpected error: %v", tool.Name(), err)
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(result.Content), &payload); err != nil {
		t.Fatalf("%s result is not JSON: %v (content=%s)", tool.Name(), err, result.Content)
	}
	return payload
}

// executeToolError runs one tool and requires a typed tool error with code.
func executeToolError(t *testing.T, tool agentdomain.Tool, ctx context.Context, args string) *agentdomain.ToolError {
	t.Helper()
	_, err := tool.Execute(ctx, json.RawMessage(args))
	if err == nil {
		t.Fatalf("%s returned no error, want a typed tool error", tool.Name())
	}
	var toolErr *agentdomain.ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("%s error %v is not a *ToolError", tool.Name(), err)
	}
	return toolErr
}

// proposedCase builds one valid supplement.case_propose entry.
func proposedCase(title, requirementID string) string {
	return fmt.Sprintf(`{"title":%q,"description":"d","priority":"P1","preconditions":"p","testData":{"k":"v"},"steps":[{"action":"open","expected":"visible","data":""}],"assertions":[{"kind":"ui","target":"page","expression":"visible","expected":"true"}],"requirementIds":[%q],"confidence":"high"}`, title, requirementID)
}

// proposedCases builds the {"cases":[...]} argument object.
func proposedCases(cases ...string) string {
	return fmt.Sprintf(`{"cases":[%s]}`, strings.Join(cases, ","))
}

// validDeps returns tool deps whose ports are all configured.
func validDeps(run mindmapdomain.CaseDesignRun) (Deps, *fakeRunStore) {
	store := newFakeRunStore(run)
	return Deps{
		Runs:         store,
		Graphs:       &fakeGraph{view: mindmapapp.GraphView{VersionID: run.VersionID}},
		Requirements: &fakeRequirements{confirmed: []mindmapapp.RequirementSnapshot{{ID: 11, VersionID: run.VersionID, Title: "login", Status: "clarified"}}},
	}, store
}

// --- tests -----------------------------------------------------------------

// TestSupplementToolsRegistered pins the five spec §9.2 tools, their order and
// their JSON schemas, and the fail-closed constructor.
func TestSupplementToolsRegistered(t *testing.T) {
	if tools := NewTools(Deps{}); tools != nil {
		t.Fatalf("NewTools without ports returned %d tools, want nil (fail closed)", len(tools))
	}
	deps, _ := validDeps(testRun(mindmapapp.RunKindSupplement))
	tools := NewTools(deps)
	want := []string{
		"supplement.context_read",
		"supplement.case_propose",
		"supplement.affected_cases_propose",
		"supplement.escalate",
		"supplement.finish",
	}
	if len(tools) != len(want) {
		t.Fatalf("registered %d tools, want %d", len(tools), len(want))
	}
	for index, name := range want {
		if tools[index].Name() != name {
			t.Fatalf("tool[%d]=%q want %q", index, tools[index].Name(), name)
		}
		if !json.Valid(tools[index].InputSchema()) {
			t.Fatalf("%s schema is not valid JSON: %s", name, tools[index].InputSchema())
		}
		var schema map[string]any
		if err := json.Unmarshal(tools[index].InputSchema(), &schema); err != nil || schema["type"] != "object" {
			t.Fatalf("%s schema is not an object schema: %s", name, tools[index].InputSchema())
		}
	}
}

// TestSupplementCaseProposeAccumulatesIntoRunScope pins the happy path: the
// proposal is validated, accumulated in the active run's result document and the
// business tables are never touched.
func TestSupplementCaseProposeAccumulatesIntoRunScope(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)
	ctx := toolContext(run, 2)

	payload := executeTool(t, toolByName(t, tools, "supplement.case_propose"), ctx,
		proposedCases(proposedCase("补充登录用例", "11")))
	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1 (payload=%v)", payload["accepted"], payload)
	}
	if store.updateCalls != 1 {
		t.Fatalf("run updates=%d want 1", store.updateCalls)
	}
	var stored struct {
		Cases []struct {
			Title          string   `json:"title"`
			RequirementIDs []string `json:"requirementIds"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v (%s)", err, store.runs[run.ID].Result)
	}
	if len(stored.Cases) != 1 || stored.Cases[0].Title != "补充登录用例" {
		t.Fatalf("stored cases=%+v", stored.Cases)
	}
	if len(stored.Cases[0].RequirementIDs) != 1 || stored.Cases[0].RequirementIDs[0] != "11" {
		t.Fatalf("stored requirement ids=%v want the decimal string 11", stored.Cases[0].RequirementIDs)
	}
}

// TestSupplementCaseProposeRejectsInvalidProposals pins the structure and
// requirement-source validation: an invalid case is a correctable
// invalid_arguments tool error and nothing is stored.
func TestSupplementCaseProposeRejectsInvalidProposals(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)
	ctx := toolContext(run, 2)
	propose := toolByName(t, tools, "supplement.case_propose")

	noSteps := `{"cases":[{"title":"bare","steps":[],"assertions":[{"kind":"ui","target":"t","expression":"x","expected":"y"}],"requirementIds":["11"]}]}`
	if toolErr := executeToolError(t, propose, ctx, noSteps); toolErr.Code != "invalid_arguments" || !strings.Contains(toolErr.Message, "step") {
		t.Fatalf("no-steps error=%+v want invalid_arguments for steps", toolErr)
	}

	unknownRequirement := proposedCases(proposedCase("bad requirement", "99"))
	if toolErr := executeToolError(t, propose, ctx, unknownRequirement); toolErr.Code != "invalid_arguments" || !strings.Contains(toolErr.Message, "requirement") {
		t.Fatalf("unknown-requirement error=%+v want invalid_arguments for requirement", toolErr)
	}

	if store.updateCalls != 0 {
		t.Fatalf("run updates=%d want 0 for rejected proposals", store.updateCalls)
	}
}

// TestSupplementCaseProposeCapsAtFive pins the per-run cap: the overflow is
// skipped with a warning instead of failing the run.
func TestSupplementCaseProposeCapsAtFive(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)
	ctx := toolContext(run, 2)
	propose := toolByName(t, tools, "supplement.case_propose")

	executeTool(t, propose, ctx, proposedCases(
		proposedCase("case 1", "11"), proposedCase("case 2", "11"),
		proposedCase("case 3", "11"), proposedCase("case 4", "11")))
	payload := executeTool(t, propose, ctx, proposedCases(
		proposedCase("case 5", "11"), proposedCase("case 6", "11"), proposedCase("case 7", "11")))

	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1 (only one slot left)", payload["accepted"])
	}
	if !warningsContain(payload["warnings"], "case_limit_reached") {
		t.Fatalf("warnings=%v want case_limit_reached", payload["warnings"])
	}
	var stored struct {
		Cases []json.RawMessage `json:"cases"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil {
		t.Fatalf("stored result: %v", err)
	}
	if len(stored.Cases) != 5 {
		t.Fatalf("stored %d cases, want 5", len(stored.Cases))
	}
}

// TestSupplementAffectedCasesGatesAndCaps pins the incremental_design-only gate,
// the node ownership/status validation and the 20-entry cap.
func TestSupplementAffectedCasesGatesAndCaps(t *testing.T) {
	// A post_batch supplement run may not propose affected cases.
	run := testRun(mindmapapp.RunKindSupplement)
	deps, _ := validDeps(run)
	tools := NewTools(deps)
	affected := toolByName(t, tools, "supplement.affected_cases_propose")
	if toolErr := executeToolError(t, affected, toolContext(run, 2), `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"changed","source":"code"}]}`); toolErr.Code != "invalid_arguments" {
		t.Fatalf("post_batch affected error=%+v want invalid_arguments", toolErr)
	}

	// An incremental_design run validates node ownership and rejects drafts.
	incremental := testRun(mindmapapp.RunKindIncrementalDesign)
	store := newFakeRunStore(incremental)
	graph := &fakeGraph{view: mindmapapp.GraphView{
		VersionID: 7,
		Nodes: []mindmapapp.GraphNode{
			{Node: mindmapdomain.CaseNode{ID: 13, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Status: mindmapdomain.NodeStatusReviewed, Title: "existing"}},
			{Node: mindmapdomain.CaseNode{ID: 14, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Status: mindmapdomain.NodeStatusDraft, Title: "draft"}},
		},
	}}
	deps = Deps{Runs: store, Graphs: graph, Requirements: &fakeRequirements{}}
	tools = NewTools(deps)
	affected = toolByName(t, tools, "supplement.affected_cases_propose")
	payload := executeTool(t, affected, toolContext(incremental, 2), `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"changed","source":"code"}]}`)
	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1", payload["accepted"])
	}
	if toolErr := executeToolError(t, affected, toolContext(incremental, 2), `{"cases":[{"caseNodeId":"14","confidence":"high","reason":"changed","source":"code"}]}`); toolErr.Code != "invalid_arguments" {
		t.Fatalf("draft node error=%+v want invalid_arguments", toolErr)
	}

	// The 21st proposal is skipped with a warning.
	huge := &fakeGraph{view: mindmapapp.GraphView{VersionID: 7}}
	for id := int64(100); id < 121; id++ {
		huge.view.Nodes = append(huge.view.Nodes, mindmapapp.GraphNode{Node: mindmapdomain.CaseNode{
			ID: id, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Status: mindmapdomain.NodeStatusReviewed, Title: fmt.Sprintf("case %d", id),
		}})
	}
	store = newFakeRunStore(incremental)
	tools = NewTools(Deps{Runs: store, Graphs: huge, Requirements: &fakeRequirements{}})
	affected = toolByName(t, tools, "supplement.affected_cases_propose")
	var proposals []string
	for id := int64(100); id < 121; id++ {
		proposals = append(proposals, fmt.Sprintf(`{"caseNodeId":"%d","confidence":"high","reason":"changed","source":"code"}`, id))
	}
	payload = executeTool(t, affected, toolContext(incremental, 2), fmt.Sprintf(`{"cases":[%s]}`, strings.Join(proposals, ",")))
	if accepted, _ := payload["accepted"].(float64); accepted != 20 {
		t.Fatalf("accepted=%v want the 20-entry cap", payload["accepted"])
	}
	if !warningsContain(payload["warnings"], "affected_limit_reached") {
		t.Fatalf("warnings=%v want affected_limit_reached", payload["warnings"])
	}
}

// TestSupplementCaseProposePreservesStarterResultKeys pins the run.result owner
// contract: the starter (T16/T17) may preseed the impact signal and diagnostics
// before the agent run, and the tools must carry those keys through every
// rewrite instead of dropping them.
func TestSupplementCaseProposePreservesStarterResultKeys(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	run.Result = []byte(`{
		"impactAvailable":true,
		"unmappedChanges":[{"path":"src/x.ts","summary":"changed"}],
		"degraded":true,
		"warnings":[{"code":"batch_precondition_unavailable","message":"reader missing"}],
		"starterExtra":{"nested":1}
	}`)
	deps, store := validDeps(run)
	tools := NewTools(deps)

	executeTool(t, toolByName(t, tools, "supplement.case_propose"), toolContext(run, 2),
		proposedCases(proposedCase("补充用例", "11")))

	var document map[string]json.RawMessage
	if err := json.Unmarshal(store.runs[run.ID].Result, &document); err != nil {
		t.Fatalf("stored result is not a JSON object: %v (%s)", err, store.runs[run.ID].Result)
	}
	for _, key := range []string{"impactAvailable", "unmappedChanges", "degraded", "warnings", "starterExtra", "cases"} {
		if _, ok := document[key]; !ok {
			t.Fatalf("stored result lost %q: %s", key, store.runs[run.ID].Result)
		}
	}
	if string(document["impactAvailable"]) != "true" {
		t.Fatalf("impactAvailable=%s want true", document["impactAvailable"])
	}
	if string(document["degraded"]) != "true" {
		t.Fatalf("degraded=%s want true", document["degraded"])
	}
	var changes []map[string]any
	if err := json.Unmarshal(document["unmappedChanges"], &changes); err != nil || len(changes) != 1 || changes[0]["path"] != "src/x.ts" {
		t.Fatalf("unmappedChanges=%s err=%v want the starter list", document["unmappedChanges"], err)
	}
	var warnings []map[string]any
	if err := json.Unmarshal(document["warnings"], &warnings); err != nil || len(warnings) != 1 || warnings[0]["code"] != "batch_precondition_unavailable" {
		t.Fatalf("warnings=%s err=%v want the starter warnings", document["warnings"], err)
	}
}

// TestSupplementToolsDoNotFabricateImpactKeys pins the Lead ruling that T14 must
// not preseed the impact signal: a run without starter keys keeps no
// impactAvailable/unmappedChanges/degraded/warnings after a tool write (T16
// preseed and the T17 P4 wiring own those keys).
func TestSupplementToolsDoNotFabricateImpactKeys(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)

	executeTool(t, toolByName(t, tools, "supplement.case_propose"), toolContext(run, 2),
		proposedCases(proposedCase("补充用例", "11")))

	var document map[string]json.RawMessage
	if err := json.Unmarshal(store.runs[run.ID].Result, &document); err != nil {
		t.Fatalf("stored result is not a JSON object: %v (%s)", err, store.runs[run.ID].Result)
	}
	for _, key := range []string{"impactAvailable", "unmappedChanges", "degraded", "warnings"} {
		if _, ok := document[key]; ok {
			t.Fatalf("stored result fabricated %q: %s", key, store.runs[run.ID].Result)
		}
	}
}

// TestSupplementAffectedCasesCarryCodeRefs pins the spec §5.8/§9.2 codeRefs
// payload of an affected-case proposal: validated refs (path, optional positive
// line, optional reason) are preserved in the run result so the T9 materializer
// can emit them in case.revalidation_marked refs.code.
func TestSupplementAffectedCasesCarryCodeRefs(t *testing.T) {
	run := testRun(mindmapapp.RunKindIncrementalDesign)
	store := newFakeRunStore(run)
	graph := &fakeGraph{view: mindmapapp.GraphView{
		VersionID: 7,
		Nodes: []mindmapapp.GraphNode{
			{Node: mindmapdomain.CaseNode{ID: 13, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Status: mindmapdomain.NodeStatusReviewed, Title: "existing"}},
		},
	}}
	deps := Deps{Runs: store, Graphs: graph, Requirements: &fakeRequirements{confirmed: []mindmapapp.RequirementSnapshot{{ID: 11, VersionID: 7, Title: "refund", Status: "clarified"}}}}
	tools := NewTools(deps)
	affected := toolByName(t, tools, "supplement.affected_cases_propose")

	payload := executeTool(t, affected, toolContext(run, 2), `{"cases":[{
		"caseNodeId":"13","confidence":"high","reason":"the refund signature changed","source":"code",
		"requirementIds":["11"],
		"codeRefs":[
			{"path":"src/refund.ts","line":42,"reason":"changed signature"},
			{"path":"src/refund_test.ts"}
		]
	}]}`)
	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1 (payload=%v)", payload["accepted"], payload)
	}

	var stored struct {
		AffectedCases []struct {
			CodeRefs []struct {
				Path   string `json:"path"`
				Line   *int   `json:"line"`
				Reason string `json:"reason"`
			} `json:"codeRefs"`
		} `json:"affectedCases"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil {
		t.Fatalf("stored result is not JSON: %v (%s)", err, store.runs[run.ID].Result)
	}
	if len(stored.AffectedCases) != 1 || len(stored.AffectedCases[0].CodeRefs) != 2 {
		t.Fatalf("stored affected codeRefs=%+v want 2 refs", stored.AffectedCases)
	}
	first := stored.AffectedCases[0].CodeRefs[0]
	if first.Path != "src/refund.ts" || first.Line == nil || *first.Line != 42 || first.Reason != "changed signature" {
		t.Fatalf("first codeRef=%+v want {src/refund.ts 42 changed signature}", first)
	}
	second := stored.AffectedCases[0].CodeRefs[1]
	if second.Path != "src/refund_test.ts" || second.Line != nil {
		t.Fatalf("second codeRef=%+v want the path without a line", second)
	}

	// The tool schema advertises the same strict shape and 20-ref cap.
	var schema map[string]any
	if err := json.Unmarshal(affected.InputSchema(), &schema); err != nil {
		t.Fatalf("affected schema: %v", err)
	}
	properties := schema["properties"].(map[string]any)
	cases := properties["cases"].(map[string]any)
	items := cases["items"].(map[string]any)
	codeRefs, ok := items["properties"].(map[string]any)["codeRefs"].(map[string]any)
	if !ok || codeRefs["type"] != "array" || codeRefs["maxItems"].(float64) != 20 {
		t.Fatalf("codeRefs schema=%v want an array capped at 20", items["properties"])
	}
}

// TestSupplementAffectedCasesRejectInvalidCodeRefs pins the codeRefs validation:
// an empty path, a non-positive line and more than 20 refs are correctable
// invalid_arguments failures and nothing is stored.
func TestSupplementAffectedCasesRejectInvalidCodeRefs(t *testing.T) {
	run := testRun(mindmapapp.RunKindIncrementalDesign)
	store := newFakeRunStore(run)
	graph := &fakeGraph{view: mindmapapp.GraphView{
		VersionID: 7,
		Nodes: []mindmapapp.GraphNode{
			{Node: mindmapdomain.CaseNode{ID: 13, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Status: mindmapdomain.NodeStatusReviewed, Title: "existing"}},
		},
	}}
	deps := Deps{Runs: store, Graphs: graph, Requirements: &fakeRequirements{}}
	affected := toolByName(t, NewTools(deps), "supplement.affected_cases_propose")
	ctx := toolContext(run, 2)

	tooMany := make([]string, 0, 21)
	for index := 0; index < 21; index++ {
		tooMany = append(tooMany, fmt.Sprintf(`{"path":"src/file_%d.ts"}`, index))
	}
	cases := []struct {
		name string
		args string
		want string
	}{
		{name: "empty path", args: `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"r","source":"code","codeRefs":[{"path":"  "}]}]}`, want: "path"},
		{name: "zero line", args: `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"r","source":"code","codeRefs":[{"path":"src/x.ts","line":0}]}]}`, want: "line"},
		{name: "negative line", args: `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"r","source":"code","codeRefs":[{"path":"src/x.ts","line":-2}]}]}`, want: "line"},
		{name: "too many refs", args: `{"cases":[{"caseNodeId":"13","confidence":"high","reason":"r","source":"code","codeRefs":[` + strings.Join(tooMany, ",") + `]}]}`, want: "20"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			toolErr := executeToolError(t, affected, ctx, testCase.args)
			if toolErr.Code != "invalid_arguments" || !strings.Contains(toolErr.Message, testCase.want) {
				t.Fatalf("error=%+v want invalid_arguments mentioning %q", toolErr, testCase.want)
			}
		})
	}
	if store.updateCalls != 0 {
		t.Fatalf("run updates=%d want 0 for rejected codeRefs", store.updateCalls)
	}
}

// TestSupplementCaseProposeStoresRationale pins the optional rationale field of
// the spec §9.2 case_propose payload: it is bounded and preserved in run.result
// (the materializer ignores it, but the plan/audit readers must be able to see
// why the agent proposed the case).
func TestSupplementCaseProposeStoresRationale(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	propose := toolByName(t, NewTools(deps), "supplement.case_propose")

	executeTool(t, propose, toolContext(run, 2), `{"cases":[{
		"title":"补充用例","steps":[{"action":"open"}],
		"assertions":[{"kind":"ui","target":"page","expression":"visible"}],
		"requirementIds":["11"],"rationale":"covers the confirmed refund requirement"
	}]}`)
	var stored struct {
		Cases []struct {
			Rationale string `json:"rationale"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil {
		t.Fatalf("stored result: %v", err)
	}
	if len(stored.Cases) != 1 || stored.Cases[0].Rationale != "covers the confirmed refund requirement" {
		t.Fatalf("stored rationale=%+v", stored.Cases)
	}

	overlong := `{"cases":[{"title":"补充用例","steps":[{"action":"open"}],"assertions":[{"kind":"ui","target":"page","expression":"visible"}],"requirementIds":["11"],"rationale":"` + strings.Repeat("x", 4001) + `"}]}`
	if toolErr := executeToolError(t, propose, toolContext(run, 2), overlong); toolErr.Code != "invalid_arguments" || !strings.Contains(toolErr.Message, "rationale") {
		t.Fatalf("over-long rationale error=%+v want invalid_arguments for rationale", toolErr)
	}
}

// TestSupplementCaseProposeRequiresAssertionExpression pins the materializer
// contract: an assertion without an expression is structurally invalid and must
// be rejected as a correctable invalid_arguments error.
func TestSupplementCaseProposeRequiresAssertionExpression(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	propose := toolByName(t, NewTools(deps), "supplement.case_propose")

	blank := `{"cases":[{"title":"补充用例","steps":[{"action":"open"}],"assertions":[{"kind":"ui","target":"page","expression":"  ","expected":"true"}],"requirementIds":["11"]}]}`
	if toolErr := executeToolError(t, propose, toolContext(run, 2), blank); toolErr.Code != "invalid_arguments" || !strings.Contains(toolErr.Message, "expression") {
		t.Fatalf("blank-expression error=%+v want invalid_arguments for expression", toolErr)
	}
	if store.updateCalls != 0 {
		t.Fatalf("run updates=%d want 0", store.updateCalls)
	}
}

// TestSupplementEscalateAcceptsSpecSingleShape pins the spec §9.2 documented
// escalate input ({title,detail,requirementIds}) alongside the array shape the
// original tool accepted; both store the same escalation records.
func TestSupplementEscalateAcceptsSpecSingleShape(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	escalate := toolByName(t, NewTools(deps), "supplement.escalate")

	payload := executeTool(t, escalate, toolContext(run, 2),
		`{"title":"missing refund requirement","detail":"the refund flow has no requirement","requirementIds":["11"]}`)
	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1 (payload=%v)", payload["accepted"], payload)
	}
	var stored struct {
		Escalations []struct {
			Title          string   `json:"title"`
			Detail         string   `json:"detail"`
			RequirementIDs []string `json:"requirementIds"`
		} `json:"escalations"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil {
		t.Fatalf("stored result: %v", err)
	}
	if len(stored.Escalations) != 1 || stored.Escalations[0].Title != "missing refund requirement" ||
		stored.Escalations[0].Detail != "the refund flow has no requirement" ||
		len(stored.Escalations[0].RequirementIDs) != 1 || stored.Escalations[0].RequirementIDs[0] != "11" {
		t.Fatalf("stored escalations=%+v", stored.Escalations)
	}

	// Mixing both shapes is ambiguous and must be rejected, not partially stored.
	mixed := `{"title":"gap","requirementIds":["11"],"escalations":[{"title":"other gap","requirementIds":["11"]}]}`
	if toolErr := executeToolError(t, escalate, toolContext(run, 2), mixed); toolErr.Code != "invalid_arguments" {
		t.Fatalf("mixed-shape error=%+v want invalid_arguments", toolErr)
	}
}

// TestSupplementToolInternalErrorsAreShortAndLogged pins spec §9.6: internal
// tool errors carry a fixed short message (never the store error) while the
// cause is logged for the operator.
func TestSupplementToolInternalErrorsAreShortAndLogged(t *testing.T) {
	previous := slog.Default()
	var output bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&output, nil)))
	defer slog.SetDefault(previous)

	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	propose := toolByName(t, NewTools(deps), "supplement.case_propose")

	// The active-run scan failure must not leak the store error to the model.
	store.listErr = errors.New("run store exploded")
	scanCtx := agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{SessionID: 900, VersionID: 7})
	toolErr := executeToolError(t, propose, scanCtx, proposedCases(proposedCase("case", "11")))
	if toolErr.Code != "internal" || strings.Contains(toolErr.Message, "run store exploded") {
		t.Fatalf("scan error=%+v want a short internal error", toolErr)
	}
	if !strings.Contains(output.String(), "run store exploded") {
		t.Fatalf("the run-store cause was not logged: %s", output.String())
	}

	// The submission CAS failure follows the same rule.
	store.listErr = nil
	store.updateErr = errors.New("update exploded")
	toolErr = executeToolError(t, propose, toolContext(run, 2), proposedCases(proposedCase("case", "11")))
	if toolErr.Code != "internal" || strings.Contains(toolErr.Message, "update exploded") {
		t.Fatalf("store error=%+v want a short internal error", toolErr)
	}
	if !strings.Contains(output.String(), "update exploded") {
		t.Fatalf("the update cause was not logged: %s", output.String())
	}
}

// TestSupplementEscalateCapsAtThree pins the requirement-gap escalation cap and
// the confirmed-requirement source validation.
func TestSupplementEscalateCapsAtThree(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)
	ctx := toolContext(run, 2)
	escalate := toolByName(t, tools, "supplement.escalate")

	executeTool(t, escalate, ctx, `{"escalations":[{"title":"gap 1","detail":"d","requirementIds":["11"]},{"title":"gap 2","detail":"d","requirementIds":["11"]}]}`)
	payload := executeTool(t, escalate, ctx, `{"escalations":[{"title":"gap 3","detail":"d","requirementIds":["11"]},{"title":"gap 4","detail":"d","requirementIds":["11"]},{"title":"gap 5","detail":"d","requirementIds":["11"]}]}`)
	if accepted, _ := payload["accepted"].(float64); accepted != 1 {
		t.Fatalf("accepted=%v want 1", payload["accepted"])
	}
	if !warningsContain(payload["warnings"], "escalation_limit_reached") {
		t.Fatalf("warnings=%v want escalation_limit_reached", payload["warnings"])
	}
	var stored struct {
		Escalations []json.RawMessage `json:"escalations"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil || len(stored.Escalations) != 3 {
		t.Fatalf("stored escalations=%d err=%v want 3", len(stored.Escalations), err)
	}

	if toolErr := executeToolError(t, escalate, ctx, `{"escalations":[{"title":"bad","detail":"d","requirementIds":["99"]}]}`); toolErr.Code != "invalid_arguments" {
		t.Fatalf("unknown requirement error=%+v want invalid_arguments", toolErr)
	}
}

// TestSupplementContextRead pins the read-only context payload: requirements,
// cases, coverage, P4 impact and the recent result summary, all with decimal
// string ids.
func TestSupplementContextRead(t *testing.T) {
	run := testRun(mindmapapp.RunKindIncrementalDesign)
	store := newFakeRunStore(run)
	graph := &fakeGraph{view: mindmapapp.GraphView{
		VersionID: 7,
		Nodes: []mindmapapp.GraphNode{
			{Node: mindmapdomain.CaseNode{ID: 13, VersionID: 7, Kind: mindmapdomain.NodeKindCase, Title: "covered", Status: mindmapdomain.NodeStatusReviewed, Source: mindmapdomain.NodeSourceAISupplement}},
		},
		Coverage: mindmapdomain.Coverage{
			RequirementIDs:          []int64{11, 12},
			UncoveredRequirementIDs: []int64{12},
			Matrix:                  []mindmapdomain.CoverageCell{{RequirementID: 11, CaseIDs: []int64{13}}},
		},
	}}
	deps := Deps{
		Runs:         store,
		Graphs:       graph,
		Requirements: &fakeRequirements{confirmed: []mindmapapp.RequirementSnapshot{{ID: 11, VersionID: 7, Title: "login", Priority: "P1", Status: "clarified"}, {ID: 12, VersionID: 7, Title: "search", Status: "clarified"}}},
		Impact:       &fakeImpact{snapshotOK: true, snapshot: mindmapapp.ImpactSnapshot{AnalysisID: 5, BaseCommit: "aaa", HeadCommit: "bbb", Status: "done"}, candidates: []mindmapapp.ImpactCandidate{{ID: 1, SubjectKind: "case", SubjectID: 13, Reason: "touched", Confidence: "high", Source: "code"}}},
		Results:      &fakeResults{summary: BatchResultSummary{BatchID: 41, Status: "done", Passed: 3, Failed: 1, Cases: []BatchResultCase{{CaseNodeID: 13, Title: "covered", Status: "failed", FailureClass: "application"}}}},
	}
	tools := NewTools(deps)
	payload := executeTool(t, toolByName(t, tools, "supplement.context_read"), toolContext(run, 2),
		`{"mode":"incremental_design","requirementIds":["11"]}`)

	if payload["versionId"] != "7" || payload["batchId"] != "41" || payload["runKind"] != mindmapapp.RunKindIncrementalDesign {
		t.Fatalf("scope payload=%v", payload)
	}
	requirements, _ := payload["requirements"].([]any)
	if len(requirements) != 1 || requirements[0].(map[string]any)["id"] != "11" {
		t.Fatalf("requirements=%v want only the requested 11", requirements)
	}
	cases, _ := payload["cases"].([]any)
	if len(cases) != 1 || cases[0].(map[string]any)["nodeId"] != "13" {
		t.Fatalf("cases=%v", cases)
	}
	coverage, _ := payload["coverage"].(map[string]any)
	uncovered, _ := coverage["uncoveredRequirementIds"].([]any)
	if len(uncovered) != 1 || uncovered[0] != "12" {
		t.Fatalf("coverage=%v", coverage)
	}
	impact, _ := payload["impact"].(map[string]any)
	if impact["analysisId"] != "5" {
		t.Fatalf("impact=%v", impact)
	}
	recent, _ := payload["recentResults"].(map[string]any)
	if recent["batchId"] != "41" || recent["failed"].(float64) != 1 {
		t.Fatalf("recentResults=%v", recent)
	}
}

// TestSupplementFinishStoresSummary pins the optional finish tool.
func TestSupplementFinishStoresSummary(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, store := validDeps(run)
	tools := NewTools(deps)
	ctx := toolContext(run, 2)
	finish := toolByName(t, tools, "supplement.finish")

	executeTool(t, finish, ctx, `{"summary":"covered the gap"}`)
	var stored struct {
		Summary string `json:"summary"`
	}
	if err := json.Unmarshal(store.runs[run.ID].Result, &stored); err != nil || stored.Summary != "covered the gap" {
		t.Fatalf("stored summary=%q err=%v", stored.Summary, err)
	}
	if toolErr := executeToolError(t, finish, ctx, fmt.Sprintf(`{"summary":%q}`, strings.Repeat("x", 5001))); toolErr.Code != "invalid_arguments" {
		t.Fatalf("over-long summary error=%+v want invalid_arguments", toolErr)
	}
}

// TestSupplementToolsRequireRunScope pins the identity source: the tools resolve
// the run from the P1 invocation scope, never from model arguments.
func TestSupplementToolsRequireRunScope(t *testing.T) {
	run := testRun(mindmapapp.RunKindSupplement)
	deps, _ := validDeps(run)
	tools := NewTools(deps)
	propose := toolByName(t, tools, "supplement.case_propose")

	if toolErr := executeToolError(t, propose, context.Background(), proposedCases(proposedCase("case", "11"))); toolErr.Code != "run_not_found" {
		t.Fatalf("no-invocation error=%+v want run_not_found", toolErr)
	}

	unknownRun := agentapp.WithToolInvocation(context.Background(), agentapp.Invocation{
		SessionID: 900, VersionID: 7, SupplementRunID: 999,
	})
	if toolErr := executeToolError(t, propose, unknownRun, proposedCases(proposedCase("case", "11"))); toolErr.Code != "run_not_found" {
		t.Fatalf("unknown-run error=%+v want run_not_found", toolErr)
	}
}

// TestSupplementValidateTools pins the startup fail-closed check.
func TestSupplementValidateTools(t *testing.T) {
	deps, _ := validDeps(testRun(mindmapapp.RunKindSupplement))
	registry := agentapp.NewToolRegistry(NewTools(deps)...)
	if err := ValidateTools(registry); err != nil {
		t.Fatalf("ValidateTools with the full tool set: %v", err)
	}
	empty := agentapp.NewToolRegistry()
	err := ValidateTools(empty)
	if err == nil || !strings.Contains(err.Error(), "supplement.context_read") {
		t.Fatalf("ValidateTools error=%v want the missing tool name", err)
	}
}

// TestSupplementPromptVersionMatchesTheRunConstant pins the profile prompt
// version and key against the constants the run coordinator records.
func TestSupplementPromptVersionMatchesTheRunConstant(t *testing.T) {
	if mindmapdomain.SupplementPromptVersion != mindmapapp.PromptVersionSupplement {
		t.Fatalf("prompt versions drifted: profile=%q run=%q",
			mindmapdomain.SupplementPromptVersion, mindmapapp.PromptVersionSupplement)
	}
	if mindmapdomain.ProfileKeySupplementAgent != mindmapapp.ProfileKeySupplement {
		t.Fatalf("profile keys drifted: seed=%q run=%q",
			mindmapdomain.ProfileKeySupplementAgent, mindmapapp.ProfileKeySupplement)
	}
}

// warningsContain reports whether a decoded warnings array carries code.
func warningsContain(raw any, code string) bool {
	items, ok := raw.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		warning, ok := item.(map[string]any)
		if ok && warning["code"] == code {
			return true
		}
	}
	return false
}
