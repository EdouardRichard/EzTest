package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// runEventOfType returns the single recorded event of eventType.
func runEventOfType(t *testing.T, fixture *runFixture, eventType string) application.Event {
	t.Helper()
	var found []application.Event
	for _, event := range fixture.events {
		if event.Type == eventType {
			found = append(found, event)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s events = %d, want 1", eventType, len(found))
	}
	return found[0]
}

// countResultWarnings counts the run-result warnings with a code.
func countResultWarnings(t *testing.T, run domain.CaseDesignRun, code string) int {
	t.Helper()
	document := runResultDocument(t, run)
	warnings, ok := document["warnings"].([]any)
	if !ok {
		return 0
	}
	count := 0
	for _, warning := range warnings {
		if entry, ok := warning.(map[string]any); ok && entry["code"] == code {
			count++
		}
	}
	return count
}

// phasedVersionLifecycle is the optional P7 version extension: it exposes the
// continuing phase on top of the shared lifecycle fixture.
type phasedVersionLifecycle struct {
	runVersionLifecycle
	phase string
}

// stepsOfNode returns the steps of one node ordered by seq.
func (f *runFixture) stepsOfNode(nodeID int64) []domain.CaseStep {
	steps := make([]domain.CaseStep, 0)
	for _, step := range f.steps {
		if step.NodeID == nodeID {
			steps = append(steps, step)
		}
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Seq < steps[j].Seq })
	return steps
}

// assertionsOfNode returns the assertions of one node ordered by id.
func (f *runFixture) assertionsOfNode(nodeID int64) []domain.CaseAssertion {
	assertions := make([]domain.CaseAssertion, 0)
	for _, assertion := range f.assertions {
		if assertion.NodeID == nodeID {
			assertions = append(assertions, assertion)
		}
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].ID < assertions[j].ID })
	return assertions
}

// sourcesOfNode returns the sources of one node ordered by id.
func (f *runFixture) sourcesOfNode(nodeID int64) []domain.CaseSource {
	sources := make([]domain.CaseSource, 0)
	for _, source := range f.sources {
		if source.NodeID == nodeID {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources
}

// ContinuePhase implements application.ContinuePhaseReader.
func (p phasedVersionLifecycle) ContinuePhase(context.Context, int64) (string, error) {
	return p.phase, nil
}

// runServiceWithVersions wires the run coordinator to the shared fixture with an
// alternative version lifecycle (used for the continue-phase gate tests).
func runServiceWithVersions(fixture *runFixture, versions application.VersionLifecycle) *application.RunService {
	return application.NewRunService(application.RunDeps{
		Runs:               runRunRepo{fixture},
		Graph:              fixture,
		Baselines:          runBaselineRepo{fixture},
		Nodes:              runNodeRepo{fixture},
		Steps:              runStepRepo{fixture},
		Assertions:         runAssertionRepo{fixture},
		Sources:            runSourceRepo{fixture},
		Versions:           versions,
		Batches:            fixture.batches,
		Requirements:       runRequirementReader{fixture},
		Catalog:            runCatalogGateway{fixture},
		CodeFacts:          runCodeFactsGateway{fixture},
		Impact:             fixture.impact,
		Anchors:            runAnchorGateway{fixture},
		Agents:             runAgentRuntime{fixture},
		Events:             runEventSink{fixture},
		IDs:                runIDGenerator{fixture},
		Clock:              runClock{fixture},
		MaxSupplementCases: fixture.maxSupplementCases,
	})
}

// newSupplementMaterializeFixture builds an executing version with a frozen
// baseline and clarified requirements.
func newSupplementMaterializeFixture() *runFixture {
	fixture := newRunFixture()
	fixture.version.Status = "executing"
	fixture.baseline = &domain.CaseBaseline{
		ID:        9000,
		VersionID: runTestVersionID,
		GraphHash: supplementGraphHash,
		FrozenAt:  fixture.now,
	}
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: runTestVersionID, Title: "Refund", Status: "clarified"},
	}
	return fixture
}

// seedSupplementRun stores one running supplement/incremental_design run with a
// batch id in its input, as POST /mindmap/runs would.
func seedSupplementRun(fixture *runFixture, kind, result string) domain.CaseDesignRun {
	run := fixture.seedRunningRun(kind, result)
	run.Input = []byte(`{"kind":"` + kind + `","batchId":"77"}`)
	fixture.runs[run.ID] = run
	return run
}

// materializedSupplementNode returns the single ai_supplement node of the
// fixture, failing when there is not exactly one.
func materializedSupplementNode(t *testing.T, fixture *runFixture) domain.CaseNode {
	t.Helper()
	var found []domain.CaseNode
	for _, node := range fixture.nodes {
		if node.Source == domain.NodeSourceAISupplement {
			found = append(found, node)
		}
	}
	if len(found) != 1 {
		t.Fatalf("ai_supplement nodes = %d, want 1", len(found))
	}
	return found[0]
}

// validSupplementCaseJSON is one structurally valid supplement.case_propose
// entry (ids are decimal strings, D29).
func validSupplementCaseJSON(title string) string {
	return `{
		"title": "` + title + `",
		"description": "refund the order end to end",
		"priority": "P1",
		"preconditions": "the user is signed in",
		"requirementIds": ["7"],
		"confidence": "high",
		"steps": [
			{"action": "open the order", "expected": "the order page is visible"},
			{"action": "submit the refund", "expected": "the refund is accepted"}
		],
		"assertions": [
			{"kind": "ui", "target": "body", "expression": "the refund banner is visible"},
			{"stepIndex": 1, "kind": "api", "target": "/api/refunds", "expression": "status == 201"}
		]
	}`
}

// TestMaterializeSupplementCreatesSupplementedNodes covers spec §6.4.4: a
// supplement run materializes new nodes with supplemented status, ai_supplement
// source, steps, assertions, requirement and ai_run sources, and publishes the
// case.created + case.supplemented events without touching the baseline.
func TestMaterializeSupplementCreatesSupplementedNodes(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	baselineBefore := *fixture.baseline
	graphHashBefore := fixture.baseline.GraphHash
	run := seedSupplementRun(fixture, application.RunKindSupplement,
		`{"cases":[`+validSupplementCaseJSON("Refund a paid order")+`]}`)

	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	node := materializedSupplementNode(t, fixture)
	if node.Status != domain.NodeStatusSupplemented {
		t.Fatalf("status = %q, want supplemented", node.Status)
	}
	if node.Kind != domain.NodeKindCase || node.VersionID != runTestVersionID {
		t.Fatalf("node = %+v", node)
	}
	if len(fixture.stepsOfNode(node.ID)) != 2 || len(fixture.assertionsOfNode(node.ID)) != 2 {
		t.Fatalf("steps/assertions = %d/%d, want 2/2",
			len(fixture.stepsOfNode(node.ID)), len(fixture.assertionsOfNode(node.ID)))
	}
	sources := fixture.sourcesOfNode(node.ID)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want the requirement and ai_run sources", sources)
	}
	var sawRequirement, sawRun bool
	for _, source := range sources {
		if source.Source != domain.NodeSourceAISupplement {
			t.Fatalf("source row = %+v, want ai_supplement", source)
		}
		switch source.RefKind {
		case domain.RefRequirement:
			sawRequirement = source.RefID != nil && *source.RefID == 7
		case domain.RefAIRun:
			sawRun = source.RefID != nil && *source.RefID == run.ID
		}
	}
	if !sawRequirement || !sawRun {
		t.Fatalf("sources = %+v, want requirement 7 and ai_run %d", sources, run.ID)
	}

	done := fixture.runs[run.ID]
	if done.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", done.Status)
	}
	stats := runStats(t, done)
	if stats["cases"] != float64(1) || stats["materialized"] != true {
		t.Fatalf("stats = %+v", stats)
	}
	result := runResultDocument(t, done)
	if cases, ok := result["cases"].([]any); ok && len(cases) != 0 {
		t.Fatalf("materialized result kept the raw cases: %+v", result)
	}

	if *fixture.baseline != baselineBefore || fixture.baseline.GraphHash != graphHashBefore {
		t.Fatal("supplement materialization changed the baseline")
	}
	if fixture.eventCount(domain.EventCaseCreated) != 1 || fixture.eventCount(domain.EventCaseSupplemented) != 1 {
		t.Fatalf("events created/supplemented = %d/%d, want 1/1",
			fixture.eventCount(domain.EventCaseCreated), fixture.eventCount(domain.EventCaseSupplemented))
	}
	if !fixture.hasEvent(domain.EventCaseRunFinished) {
		t.Fatal("the supplement run did not publish case.run_finished")
	}
}

// TestMaterializeIncrementalDesignMarksAffectedCases covers spec §5.8/§6.4:
// high/medium confidence affected cases become needs_revalidation, low confidence
// is only listed, and the validated list is stored in run.result.
func TestMaterializeIncrementalDesignMarksAffectedCases(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.version.Status = "continuing"
	fixture.nodes[42] = domain.CaseNode{
		ID: 42, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Pay order",
		Priority: domain.PriorityP1, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusExecuted,
		SortOrder: 42, Revision: 3, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[43] = domain.CaseNode{
		ID: 43, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Cancel order",
		Priority: domain.PriorityP2, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
		SortOrder: 43, Revision: 2, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[44] = domain.CaseNode{
		ID: 44, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "List orders",
		Priority: domain.PriorityP2, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
		SortOrder: 44, Revision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{
		"affectedCases": [
			{"caseNodeId":"42","confidence":"high","reason":"the refund API changed","source":"code"},
			{"caseNodeId":"43","confidence":"medium","reason":"the order page changed","source":"requirement"},
			{"caseNodeId":"44","confidence":"low","reason":"maybe related","source":"code"}
		]
	}`)

	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if got := fixture.nodes[42].Status; got != domain.NodeStatusNeedsRevalidation {
		t.Fatalf("high-confidence case status = %q, want needs_revalidation", got)
	}
	if got := fixture.nodes[43].Status; got != domain.NodeStatusNeedsRevalidation {
		t.Fatalf("medium-confidence case status = %q, want needs_revalidation", got)
	}
	if got := fixture.nodes[44].Status; got != domain.NodeStatusReviewed {
		t.Fatalf("low-confidence case status = %q, want reviewed", got)
	}
	if fixture.eventCount(domain.EventCaseRevalidationMarked) != 2 {
		t.Fatalf("revalidation events = %d, want 2", fixture.eventCount(domain.EventCaseRevalidationMarked))
	}
	if fixture.eventCount(domain.EventCaseStatusChanged) != 2 {
		t.Fatalf("status events = %d, want 2", fixture.eventCount(domain.EventCaseStatusChanged))
	}

	done := fixture.runs[run.ID]
	result := runResultDocument(t, done)
	affected, ok := result["affectedCases"].([]any)
	if !ok || len(affected) != 3 {
		t.Fatalf("run result affectedCases = %+v, want 3 records", result["affectedCases"])
	}
	if stats := runStats(t, done); stats["affected"] != float64(3) || stats["affectedMarked"] != float64(2) {
		t.Fatalf("stats = %+v", stats)
	}
}

// TestMaterializeSupplementSkipsDuplicates covers the supplement_key dedupe:
// a proposal that matches an existing case is skipped, counted and reported as a
// warning instead of failing the run.
func TestMaterializeSupplementSkipsDuplicates(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	payload := `{"cases":[` + validSupplementCaseJSON("Refund a paid order") + `]}`
	first := seedSupplementRun(fixture, application.RunKindSupplement, payload)
	if err := fixture.service().Materialize(context.Background(), first.ID); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	nodeCount := len(fixture.nodes)

	second := seedSupplementRun(fixture, application.RunKindSupplement, payload)
	if err := fixture.service().Materialize(context.Background(), second.ID); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	if len(fixture.nodes) != nodeCount {
		t.Fatalf("duplicate materialization wrote %d nodes", len(fixture.nodes)-nodeCount)
	}
	done := fixture.runs[second.ID]
	if done.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", done.Status)
	}
	stats := runStats(t, done)
	if stats["duplicateSkipped"] != float64(1) {
		t.Fatalf("stats = %+v, want duplicateSkipped=1", stats)
	}
	if !hasResultWarning(t, done, "duplicate_case") {
		t.Fatalf("run result = %s, want a duplicate_case warning", done.Result)
	}
}

// TestMaterializeSupplementHonorsConfiguredMaxCases pins the P8 wiring of
// EZTEST_SUPPLEMENT_MAX_CASES into the materializer: the configured budget, not
// the compiled 5-case default, bounds the materialized supplement nodes.
func TestMaterializeSupplementHonorsConfiguredMaxCases(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.maxSupplementCases = 2
	cases := make([]string, 0, 3)
	for index := 1; index <= 3; index++ {
		cases = append(cases, validSupplementCaseJSON("Configured case "+string(rune('0'+index))))
	}
	run := seedSupplementRun(fixture, application.RunKindSupplement, `{"cases":[`+strings.Join(cases, ",")+`]}`)

	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	supplementNodes := 0
	for _, node := range fixture.nodes {
		if node.Source == domain.NodeSourceAISupplement {
			supplementNodes++
		}
	}
	if supplementNodes != 2 {
		t.Fatalf("materialized nodes = %d, want the configured 2-case cap", supplementNodes)
	}
	if !hasResultWarning(t, fixture.runs[run.ID], "case_limit_reached") {
		t.Fatalf("run result = %s, want a case_limit_reached warning", fixture.runs[run.ID].Result)
	}
}

// TestMaterializeSupplementEnforcesCaseCap covers the per-run cap of five new
// cases: excess proposals are skipped with a warning.
func TestMaterializeSupplementEnforcesCaseCap(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	cases := make([]string, 0, 6)
	for index := 1; index <= 6; index++ {
		cases = append(cases, validSupplementCaseJSON("Supplement case "+string(rune('0'+index))))
	}
	run := seedSupplementRun(fixture, application.RunKindSupplement, `{"cases":[`+strings.Join(cases, ",")+`]}`)

	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	supplementNodes := 0
	for _, node := range fixture.nodes {
		if node.Source == domain.NodeSourceAISupplement {
			supplementNodes++
		}
	}
	if supplementNodes != 5 {
		t.Fatalf("materialized nodes = %d, want the 5-case cap", supplementNodes)
	}
	if !hasResultWarning(t, fixture.runs[run.ID], "case_limit_reached") {
		t.Fatalf("run result = %s, want a case_limit_reached warning", fixture.runs[run.ID].Result)
	}
}

// TestMaterializeSupplementEmptySubmissionSucceeds covers spec §6.4.5: no new
// cases is done, not failed.
func TestMaterializeSupplementEmptySubmissionSucceeds(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	run := seedSupplementRun(fixture, application.RunKindSupplement, `{}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	done := fixture.runs[run.ID]
	if done.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", done.Status)
	}
	if len(fixture.nodes) != 0 {
		t.Fatalf("nodes = %d, want 0", len(fixture.nodes))
	}
}

// TestStartSupplementRunGate covers the spec §7.6 run gates: supplement runs need
// executing, incremental_design runs need continuing+designing, and both need
// the parent batch id.
func TestStartSupplementRunGate(t *testing.T) {
	t.Run("supplement requires executing", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "reviewing"
		_, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	})
	t.Run("supplement starts on executing", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		run, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
		if err != nil {
			t.Fatalf("start: %v", err)
		}
		if run.Status != domain.RunRunning || run.PromptVersion != application.PromptVersionSupplement {
			t.Fatalf("run = %+v", run)
		}
		if len(fixture.profiles) != 1 || fixture.profiles[0] != application.ProfileKeySupplement {
			t.Fatalf("profiles = %v", fixture.profiles)
		}
		if len(fixture.texts) != 1 || !strings.Contains(fixture.texts[0], "supplement.case_propose") {
			t.Fatalf("instruction = %v", fixture.texts)
		}
		var input struct {
			BatchID string `json:"batchId"`
			Mode    string
		}
		if err := json.Unmarshal(run.Input, &input); err != nil {
			t.Fatalf("input = %s: %v", run.Input, err)
		}
		if input.BatchID != "77" || input.Mode != "post_batch" {
			t.Fatalf("input = %s, want the decimal-string batchId 77 and mode post_batch", run.Input)
		}
		// The migration column is authoritative for T14/T16 lookups: the run row
		// itself carries the parent batch id, not only the JSON input.
		if run.BatchID == nil || *run.BatchID != 77 {
			t.Fatalf("run batch id = %v, want 77", run.BatchID)
		}
		if stored := fixture.runs[run.ID]; stored.BatchID == nil || *stored.BatchID != 77 {
			t.Fatalf("stored run batch id = %v, want 77", stored.BatchID)
		}
	})
	t.Run("batch id is required", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		_, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindSupplement})
		requireDomainError(t, err, domain.KindInvalid, "invalid_batch_id")
	})
	t.Run("incremental_design requires continuing and designing", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		_, err := runServiceWithVersions(fixture, phasedVersionLifecycle{runVersionLifecycle{f: fixture}, "clarifying"}).
			Start(context.Background(), runTestVersionID, 42,
				application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")

		fixture = newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		_, err = runServiceWithVersions(fixture, phasedVersionLifecycle{runVersionLifecycle{f: fixture}, "designing"}).
			Start(context.Background(), runTestVersionID, 42,
				application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77})
		if err != nil {
			t.Fatalf("designing start: %v", err)
		}
	})
	t.Run("incremental_design rejects executing", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		_, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
			application.StartRunCommand{Kind: application.RunKindIncrementalDesign, BatchID: 77})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	})
}

// TestStartSupplementRunDegradesOnAgentFailure covers spec §9.7: an unavailable
// LLM creates a degraded supplement run instead of failing the request.
func TestStartSupplementRunDegradesOnAgentFailure(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.createSessionErr = errors.New("llm gateway unavailable")
	run, err := fixture.service().Start(context.Background(), runTestVersionID, 42,
		application.StartRunCommand{Kind: application.RunKindSupplement, BatchID: 77})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if run.Status != domain.RunDegraded {
		t.Fatalf("run status = %q, want degraded", run.Status)
	}
	if stored := fixture.runs[run.ID]; stored.Status != domain.RunDegraded {
		t.Fatalf("stored run status = %q, want degraded", stored.Status)
	}
}

// TestMaterializeSupplementEventDetails pins the spec §6.4/§8.1/§8.2 event
// contract: case.created carries the node source and case.supplemented carries
// the decimal-string ids and requirementCount (not the old requirements count).
func TestMaterializeSupplementEventDetails(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	run := seedSupplementRun(fixture, application.RunKindSupplement,
		`{"cases":[`+validSupplementCaseJSON("Refund a paid order")+`]}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	node := materializedSupplementNode(t, fixture)

	created := runEventOfType(t, fixture, domain.EventCaseCreated)
	if created.Source != node.Source {
		t.Fatalf("case.created source = %q, want the node source %q", created.Source, node.Source)
	}

	supplemented := runEventOfType(t, fixture, domain.EventCaseSupplemented)
	if got, ok := supplemented.Detail["nodeId"].(string); !ok || got != strconv.FormatInt(node.ID, 10) {
		t.Fatalf("case.supplemented detail.nodeId = %#v, want %d as a decimal string", supplemented.Detail["nodeId"], node.ID)
	}
	if got, ok := supplemented.Detail["runId"].(string); !ok || got != strconv.FormatInt(run.ID, 10) {
		t.Fatalf("case.supplemented detail.runId = %#v, want %d as a decimal string", supplemented.Detail["runId"], run.ID)
	}
	if got, ok := supplemented.Detail["requirementCount"].(int); !ok || got != 1 {
		t.Fatalf("case.supplemented detail.requirementCount = %#v, want 1", supplemented.Detail["requirementCount"])
	}
	if _, exists := supplemented.Detail["requirements"]; exists {
		t.Fatalf("case.supplemented still uses the non-spec requirements field: %+v", supplemented.Detail)
	}
}

// TestMaterializeIncrementalDesignEventDetails pins the spec §8.1 fields of
// case.revalidation_marked: nodeId/runId decimal strings, the proposal
// confidence and source, and the requirement/code refs.
func TestMaterializeIncrementalDesignEventDetails(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.version.Status = "continuing"
	fixture.nodes[42] = domain.CaseNode{
		ID: 42, VersionID: runTestVersionID, Kind: domain.NodeKindCase, Title: "Pay order",
		Priority: domain.PriorityP1, Source: domain.NodeSourceUserInput, Status: domain.NodeStatusExecuted,
		SortOrder: 42, Revision: 3, CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{
		"affectedCases": [{
			"caseNodeId":"42","confidence":"high","reason":"the refund API changed","source":"code",
			"requirementIds":["7"],
			"codeRefs":[{"path":"src/refund.ts","line":42,"reason":"changed signature"}]
		}]
	}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	marked := runEventOfType(t, fixture, domain.EventCaseRevalidationMarked)
	if got, ok := marked.Detail["nodeId"].(string); !ok || got != "42" {
		t.Fatalf("revalidation detail.nodeId = %#v, want the decimal string 42", marked.Detail["nodeId"])
	}
	if got, ok := marked.Detail["runId"].(string); !ok || got != strconv.FormatInt(run.ID, 10) {
		t.Fatalf("revalidation detail.runId = %#v, want the decimal string %d", marked.Detail["runId"], run.ID)
	}
	if marked.Detail["confidence"] != domain.ConfidenceHigh {
		t.Fatalf("revalidation detail.confidence = %#v, want high", marked.Detail["confidence"])
	}
	if marked.Detail["source"] != "code" {
		t.Fatalf("revalidation detail.source = %#v, want code", marked.Detail["source"])
	}
	if len(marked.RefRequirementIDs) != 1 || marked.RefRequirementIDs[0] != 7 {
		t.Fatalf("revalidation refs.requirement = %v, want [7]", marked.RefRequirementIDs)
	}
	if len(marked.RefCodeRefs) != 1 || marked.RefCodeRefs[0] != "src/refund.ts" {
		t.Fatalf("revalidation refs.code = %v, want [src/refund.ts]", marked.RefCodeRefs)
	}
	if _, exists := marked.Detail["status"]; exists {
		t.Fatalf("revalidation detail still uses the non-spec status field: %+v", marked.Detail)
	}
}

// TestMaterializeIncrementalDesignEnforcesAffectedCap covers the ≤20 affected
// proposals cap of spec §6.4.5/§9.2: the 21st proposal is skipped with an
// affected_limit_reached warning and a run stat, and is never marked.
func TestMaterializeIncrementalDesignEnforcesAffectedCap(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.version.Status = "continuing"
	proposals := make([]string, 0, 21)
	for index := 0; index < 21; index++ {
		nodeID := int64(100 + index)
		fixture.nodes[nodeID] = domain.CaseNode{
			ID: nodeID, VersionID: runTestVersionID, Kind: domain.NodeKindCase,
			Title: fmt.Sprintf("affected case %d", nodeID), Priority: domain.PriorityP2,
			Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
			SortOrder: int(nodeID), Revision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}
		proposals = append(proposals, fmt.Sprintf(
			`{"caseNodeId":"%d","confidence":"high","reason":"impact %d","source":"code"}`, nodeID, index))
	}
	run := seedSupplementRun(fixture, application.RunKindIncrementalDesign,
		`{"affectedCases":[`+strings.Join(proposals, ",")+`]}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	done := fixture.runs[run.ID]
	result := runResultDocument(t, done)
	affected, ok := result["affectedCases"].([]any)
	if !ok || len(affected) != 20 {
		t.Fatalf("affectedCases = %#v, want the 20-proposal cap", result["affectedCases"])
	}
	if countResultWarnings(t, done, "affected_limit_reached") != 1 {
		t.Fatalf("run result = %s, want one affected_limit_reached warning", done.Result)
	}
	stats := runStats(t, done)
	if stats["affected"] != float64(20) || stats["affectedLimitReached"] != true {
		t.Fatalf("stats = %+v, want affected=20 and affectedLimitReached=true", stats)
	}
	if fixture.eventCount(domain.EventCaseRevalidationMarked) != 20 {
		t.Fatalf("revalidation events = %d, want 20", fixture.eventCount(domain.EventCaseRevalidationMarked))
	}
	if got := fixture.nodes[120].Status; got != domain.NodeStatusReviewed {
		t.Fatalf("the 21st affected case status = %q, want untouched reviewed", got)
	}
}

// TestMaterializeIncrementalDesignValidatesAffectedProposals covers the affected
// proposal validation: an unknown source is skipped and an unclarified
// requirementId is dropped from the record with a warning instead of silently
// disappearing.
func TestMaterializeIncrementalDesignValidatesAffectedProposals(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	fixture.version.Status = "continuing"
	for _, nodeID := range []int64{42, 43} {
		fixture.nodes[nodeID] = domain.CaseNode{
			ID: nodeID, VersionID: runTestVersionID, Kind: domain.NodeKindCase,
			Title: fmt.Sprintf("case %d", nodeID), Priority: domain.PriorityP2,
			Source: domain.NodeSourceUserInput, Status: domain.NodeStatusReviewed,
			SortOrder: int(nodeID), Revision: 1, CreatedAt: fixture.now, UpdatedAt: fixture.now,
		}
	}
	run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{
		"affectedCases": [
			{"caseNodeId":"42","confidence":"high","reason":"bad source","source":"bogus"},
			{"caseNodeId":"43","confidence":"high","reason":"unconfirmed ref","source":"code","requirementIds":["999"]}
		]
	}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if got := fixture.nodes[42].Status; got != domain.NodeStatusReviewed {
		t.Fatalf("the invalid-source case status = %q, want untouched reviewed", got)
	}
	if got := fixture.nodes[43].Status; got != domain.NodeStatusNeedsRevalidation {
		t.Fatalf("the valid case status = %q, want needs_revalidation", got)
	}
	done := fixture.runs[run.ID]
	if count := countResultWarnings(t, done, "affected_case_invalid"); count < 2 {
		t.Fatalf("affected_case_invalid warnings = %d, want the source and requirement warnings", count)
	}
	results, ok := runResultDocument(t, done)["affectedCases"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("affectedCases = %#v, want only the valid record", runResultDocument(t, done)["affectedCases"])
	}
	record, ok := results[0].(map[string]any)
	if !ok || record["nodeId"] != "43" {
		t.Fatalf("affected record = %#v, want node 43", results[0])
	}
	if ids, exists := record["requirementIds"]; exists && ids != nil {
		t.Fatalf("affected record kept the unclarified requirement: %#v", ids)
	}
}

// TestMaterializeSupplementEscalationRequiresRequirements covers spec §5.4/§9.2:
// a requirement-gap escalation without requirement refs is rejected with a
// warning (T16 cannot materialize a ref-less requirement_gap issue).
func TestMaterializeSupplementEscalationRequiresRequirements(t *testing.T) {
	fixture := newSupplementMaterializeFixture()
	run := seedSupplementRun(fixture, application.RunKindSupplement, `{
		"escalations":[{"title":"Missing refund requirement","detail":"no requirement covers partial refunds"}]
	}`)
	if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	done := fixture.runs[run.ID]
	if countResultWarnings(t, done, "invalid_escalation") != 1 {
		t.Fatalf("run result = %s, want one invalid_escalation warning", done.Result)
	}
	result := runResultDocument(t, done)
	if escalations, ok := result["escalations"].([]any); ok && len(escalations) != 0 {
		t.Fatalf("escalations = %#v, want none stored", escalations)
	}
	if stats := runStats(t, done); stats["escalations"] != float64(0) {
		t.Fatalf("stats = %+v, want escalations=0", stats)
	}
}

// TestMaterializeIncrementalDesignPreservesImpactAvailability covers the
// impact_unavailable signal of spec §7.6/§9.7: a materialized run without a P4
// result must keep that absence visible so the plan can warn; an explicit empty
// array means "impact ran and found no unmapped changes".
func TestMaterializeIncrementalDesignPreservesImpactAvailability(t *testing.T) {
	t.Run("no P4 result", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{"affectedCases":[]}`)
		if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		result := runResultDocument(t, fixture.runs[run.ID])
		if result["impactAvailable"] != false {
			t.Fatalf("impactAvailable = %#v, want false when the run had no P4 result", result["impactAvailable"])
		}
		if _, exists := result["unmappedChanges"]; exists {
			t.Fatalf("unmappedChanges was fabricated: %#v", result["unmappedChanges"])
		}
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !containsPlanWarning(view.Warnings, "impact_unavailable") {
			t.Fatalf("plan warnings = %+v, want impact_unavailable", view.Warnings)
		}
	})

	t.Run("explicit empty P4 result", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		run := seedSupplementRun(fixture, application.RunKindIncrementalDesign,
			`{"affectedCases":[],"unmappedChanges":[]}`)
		if err := fixture.service().Materialize(context.Background(), run.ID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		result := runResultDocument(t, fixture.runs[run.ID])
		if result["impactAvailable"] != true {
			t.Fatalf("impactAvailable = %#v, want true for an explicit empty P4 result", result["impactAvailable"])
		}
		if changes, ok := result["unmappedChanges"].([]any); !ok || len(changes) != 0 {
			t.Fatalf("unmappedChanges = %#v, want an explicit empty array", result["unmappedChanges"])
		}
		view, err := planService(fixture).IncrementalPlan(context.Background(), runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if containsPlanWarning(view.Warnings, "impact_unavailable") {
			t.Fatalf("plan warnings = %+v, impact data was available", view.Warnings)
		}
	})
}

// TestMaterializeIncrementalDesignPreservesSeededWarnings covers the warning
// owner chain: the starter seeds P4 diagnostics (truncated unmapped list,
// degraded/unavailable) before the agent run, and the materializer must merge
// them into the materialized result instead of rebuilding warnings only from its
// own skips. Duplicates keep the first (seeded) warning of a code.
func TestMaterializeIncrementalDesignPreservesSeededWarnings(t *testing.T) {
	ctx := context.Background()

	t.Run("seeded warnings merge with generated warnings and win the dedupe", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{
			"impactAvailable": true,
			"unmappedChanges": [{"path":"src/x.ts","summary":"changed"}],
			"warnings": [
				{"code":"unmapped_truncated","message":"starter truncation diagnostic"},
				{"code":"invalid_case","message":"starter invalid case diagnostic"}
			],
			"cases": [{"title":""}]
		}`)
		if err := fixture.service().Materialize(ctx, run.ID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		done := fixture.runs[run.ID]
		result := runResultDocument(t, done)
		if result["impactAvailable"] != true {
			t.Fatalf("impactAvailable = %#v, want true", result["impactAvailable"])
		}
		if countResultWarnings(t, done, "unmapped_truncated") != 1 {
			t.Fatalf("run result = %s, want the seeded truncation warning preserved", done.Result)
		}
		if countResultWarnings(t, done, "invalid_case") != 1 {
			t.Fatalf("run result = %s, want the generated invalid_case warning deduped by code", done.Result)
		}
		if message := resultWarningMessage(t, done, "invalid_case"); message != "starter invalid case diagnostic" {
			t.Fatalf("invalid_case message = %q, want the seeded diagnostic to win the dedupe", message)
		}

		view, err := planService(fixture).IncrementalPlan(ctx, runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !containsPlanWarning(view.Warnings, "unmapped_truncated") {
			t.Fatalf("plan warnings = %+v, want the seeded truncation warning", view.Warnings)
		}
		if containsPlanWarning(view.Warnings, "impact_unavailable") {
			t.Fatalf("plan warnings = %+v, impact was available", view.Warnings)
		}
		if len(view.UnmappedChanges) != 1 || view.UnmappedChanges[0].Path != "src/x.ts" {
			t.Fatalf("plan unmappedChanges = %+v, want the seeded change", view.UnmappedChanges)
		}
	})

	t.Run("unavailable degraded seed keeps its diagnostics", func(t *testing.T) {
		fixture := newSupplementMaterializeFixture()
		fixture.version.Status = "continuing"
		run := seedSupplementRun(fixture, application.RunKindIncrementalDesign, `{
			"impactAvailable": false,
			"degraded": true,
			"warnings": [
				{"code":"impact_degraded","message":"starter degraded diagnostic"},
				{"code":"impact_unavailable","message":"starter unavailable warning"}
			]
		}`)
		if err := fixture.service().Materialize(ctx, run.ID); err != nil {
			t.Fatalf("materialize: %v", err)
		}
		done := fixture.runs[run.ID]
		result := runResultDocument(t, done)
		if result["impactAvailable"] != false || result["degraded"] != true {
			t.Fatalf("impactAvailable/degraded = %#v/%#v, want false/true",
				result["impactAvailable"], result["degraded"])
		}
		if countResultWarnings(t, done, "impact_degraded") != 1 ||
			countResultWarnings(t, done, "impact_unavailable") != 1 {
			t.Fatalf("run result = %s, want both seeded diagnostics preserved once", done.Result)
		}

		view, err := planService(fixture).IncrementalPlan(ctx, runTestVersionID)
		if err != nil {
			t.Fatalf("incremental plan: %v", err)
		}
		if !view.Degraded {
			t.Fatal("plan degraded = false, want the seeded degraded state")
		}
		count := 0
		degradedDiagnostics := 0
		for _, warning := range view.Warnings {
			switch warning.Code {
			case "impact_unavailable":
				count++
			case "impact_degraded":
				degradedDiagnostics++
			}
		}
		if count != 1 {
			t.Fatalf("plan warnings = %+v, want exactly one impact_unavailable", view.Warnings)
		}
		if degradedDiagnostics != 1 {
			t.Fatalf("plan warnings = %+v, want the degraded diagnostic preserved", view.Warnings)
		}
	})
}

// runResultDocument decodes one run's result document.
func runResultDocument(t *testing.T, run domain.CaseDesignRun) map[string]any {
	t.Helper()
	document := map[string]any{}
	if len(run.Result) == 0 {
		return document
	}
	if err := json.Unmarshal(run.Result, &document); err != nil {
		t.Fatalf("decode run result %q: %v", string(run.Result), err)
	}
	return document
}

// hasResultWarning reports whether the run result document lists a warning code.
func hasResultWarning(t *testing.T, run domain.CaseDesignRun, code string) bool {
	t.Helper()
	document := runResultDocument(t, run)
	warnings, ok := document["warnings"].([]any)
	if !ok {
		return false
	}
	for _, warning := range warnings {
		entry, ok := warning.(map[string]any)
		if ok && entry["code"] == code {
			return true
		}
	}
	return false
}

// resultWarningMessage returns the message of the first run-result warning with
// a code, or an empty string.
func resultWarningMessage(t *testing.T, run domain.CaseDesignRun, code string) string {
	t.Helper()
	document := runResultDocument(t, run)
	warnings, ok := document["warnings"].([]any)
	if !ok {
		return ""
	}
	for _, warning := range warnings {
		entry, ok := warning.(map[string]any)
		if !ok || entry["code"] != code {
			continue
		}
		message, _ := entry["message"].(string)
		return message
	}
	return ""
}
