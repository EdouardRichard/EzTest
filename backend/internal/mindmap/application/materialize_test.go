package application_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// runValidDesignPayload is the canonical case.graph_propose payload of spec §6.7
// (requirement ids are decimal strings, D29).
func runValidDesignPayload() string {
	return `{
		"mode": "append",
		"scenarios": [
			{"ref": "s1", "title": "下单主流程", "parentRef": null, "description": "主流程"}
		],
		"cases": [
			{
				"ref": "c1",
				"scenarioRef": "s1",
				"title": "正常下单",
				"priority": "P1",
				"requirementIds": ["7"],
				"preconditions": "已登录且购物车非空",
				"testData": {"sku": "A-1", "qty": 1},
				"steps": [
					{"action": "打开商品页", "expected": "展示价格与库存", "data": ""}
				],
				"assertions": [
					{"stepIndex": 0, "kind": "ui", "target": "/order/confirm", "expression": "订单号非空", "expected": "生成订单号"}
				],
				"anchors": [
					{"file": "src/order/submit.ts", "startLine": 120, "endLine": 140}
				]
			}
		]
	}`
}

// newDesignFixture seeds the confirmed requirements a design run needs.
func newDesignFixture() *runFixture {
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{{
		ID: 7, VersionID: runTestVersionID, Title: "结算页加载", Priority: "P1", Status: "clarified",
	}}
	return fixture
}

// TestDesignProposalMaterializesInOneTx covers spec §6.7: a valid proposal is
// written as scenario/case/step/assertion/source rows in exactly one graph
// transaction, the run finishes done and the anchor is created for the case.
func TestDesignProposalMaterializesInOneTx(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize design run: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want exactly 1", fixture.txCount)
	}
	if len(fixture.nodes) != 2 {
		t.Fatalf("nodes = %d, want scenario + case", len(fixture.nodes))
	}
	var scenario, testCase domain.CaseNode
	for _, node := range fixture.nodes {
		switch node.Kind {
		case domain.NodeKindScenario:
			scenario = node
		case domain.NodeKindCase:
			testCase = node
		}
	}
	if scenario.ID == 0 || scenario.Title != "下单主流程" {
		t.Fatalf("scenario = %+v, want 下单主流程", scenario)
	}
	if testCase.ID == 0 || testCase.Title != "正常下单" || testCase.Priority != domain.PriorityP1 {
		t.Fatalf("case = %+v, want 正常下单/P1", testCase)
	}
	if testCase.ParentID == nil || *testCase.ParentID != scenario.ID {
		t.Fatalf("case parent = %v, want scenario %d", testCase.ParentID, scenario.ID)
	}
	if testCase.Source != domain.NodeSourceAIGenerated || testCase.Status != domain.NodeStatusDraft || testCase.Revision != 1 {
		t.Fatalf("case source/status/revision = %q/%q/%d, want ai_generated/draft/1",
			testCase.Source, testCase.Status, testCase.Revision)
	}
	if len(fixture.steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(fixture.steps))
	}
	step := fixture.stepsOfNodeForRun(testCase.ID)
	if len(step) != 1 || step[0].Seq != 1 || step[0].Action != "打开商品页" || step[0].Source != domain.NodeSourceAIGenerated {
		t.Fatalf("steps = %+v, want one ai_generated step", step)
	}
	assertions := fixture.assertionsOfNodeForRun(testCase.ID)
	if len(assertions) != 1 || assertions[0].Expression != "订单号非空" || assertions[0].StepID == nil || *assertions[0].StepID != step[0].ID {
		t.Fatalf("assertions = %+v, want one assertion linked to the step", assertions)
	}
	sources := fixture.sourcesOfNodeForRun(testCase.ID)
	if len(sources) != 2 {
		t.Fatalf("sources = %+v, want requirement + ai_run", sources)
	}
	var requirementSource, runSource *domain.CaseSource
	for index := range sources {
		switch sources[index].RefKind {
		case domain.RefRequirement:
			requirementSource = &sources[index]
		case domain.RefAIRun:
			runSource = &sources[index]
		}
	}
	if requirementSource == nil || requirementSource.RefID == nil || *requirementSource.RefID != 7 {
		t.Fatalf("requirement source = %+v, want requirement 7", requirementSource)
	}
	if runSource == nil || runSource.RefID == nil || *runSource.RefID != run.ID {
		t.Fatalf("ai_run source = %+v, want run %d", runSource, run.ID)
	}
	if len(fixture.anchorArgs) != 1 || fixture.anchorArgs[0].SubjectID != testCase.ID ||
		fixture.anchorArgs[0].FilePath != "src/order/submit.ts" {
		t.Fatalf("anchor args = %+v, want one anchor on case %d", fixture.anchorArgs, testCase.ID)
	}

	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", updated.Status)
	}
	if updated.FinishedAt == nil {
		t.Fatal("done run has no finished_at")
	}
	stats := runStats(t, updated)
	if stats["materialized"] != true || stats["cases"] != float64(1) {
		t.Fatalf("run stats = %s, want materialized/cases", string(updated.Stats))
	}
	if fixture.eventCount(domain.EventCaseCreated) != 1 {
		t.Fatalf("case.created events = %d, want 1", fixture.eventCount(domain.EventCaseCreated))
	}
	if !fixture.hasEvent(domain.EventCaseRunFinished) {
		t.Fatal("missing case.run_finished event")
	}
	created := fixture.events[0]
	if created.Type != domain.EventCaseCreated || len(created.RefCaseIDs) != 1 || created.RefCaseIDs[0] != testCase.ID {
		t.Fatalf("first event = %+v, want case.created for %d", created, testCase.ID)
	}
	if fixture.version.Status != "reviewing" {
		t.Fatalf("version status = %q, want reviewing after a design run", fixture.version.Status)
	}
	if !fixture.hasEvent(domain.EventGatePending) {
		t.Fatal("missing gate.pending event after entering review")
	}
}

// TestInvalidProposalWritesNothing covers spec §6.7/§6.13: every payload rule
// violation fails the run with errors.validationErrors and writes no row.
func TestInvalidProposalWritesNothing(t *testing.T) {
	cases := []struct {
		name    string
		payload string
	}{
		{
			name: "duplicate scenario ref",
			payload: `{"mode":"append","scenarios":[
				{"ref":"s1","title":"A"},{"ref":"s1","title":"B"}],"cases":[]}`,
		},
		{
			name: "unconfirmed requirement",
			payload: `{"mode":"append","scenarios":[],"cases":[{
				"ref":"c1","scenarioRef":null,"title":"正常下单","priority":"P1",
				"requirementIds":["999"],
				"steps":[{"action":"a"}],
				"assertions":[{"kind":"ui","expression":"x"}]}]}`,
		},
		{
			name: "step index out of range",
			payload: `{"mode":"append","scenarios":[],"cases":[{
				"ref":"c1","scenarioRef":null,"title":"正常下单","priority":"P1",
				"requirementIds":["7"],
				"steps":[{"action":"a"}],
				"assertions":[{"stepIndex":3,"kind":"ui","expression":"x"}]}]}`,
		},
		{
			name:    "missing submission",
			payload: `{}`,
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newDesignFixture()
			fixture.seedCaseNode(11, 1, "人工草稿")
			run := fixture.seedRunningRun(domain.RunKindDesign, testCase.payload)
			service := fixture.service()

			err := service.Materialize(ctx, run.ID)
			requireDomainError(t, err, domain.KindInvalid, "invalid_case_proposal")
			if fixture.txCount != 0 {
				t.Fatalf("InTx calls = %d, want 0 for a rejected proposal", fixture.txCount)
			}
			if len(fixture.nodes) != 1 {
				t.Fatalf("nodes = %d, want only the seeded draft", len(fixture.nodes))
			}
			if len(fixture.steps) != 0 || len(fixture.assertions) != 0 || len(fixture.sources) != 0 {
				t.Fatalf("rejected proposal wrote steps=%d assertions=%d sources=%d",
					len(fixture.steps), len(fixture.assertions), len(fixture.sources))
			}
			if fixture.eventCount(domain.EventCaseCreated) != 0 {
				t.Fatalf("rejected proposal published %d case.created event(s)", fixture.eventCount(domain.EventCaseCreated))
			}
			updated := fixture.runs[run.ID]
			if updated.Status != domain.RunFailed {
				t.Fatalf("run status = %q, want failed", updated.Status)
			}
			reason, validationErrors := runErrors(t, updated)
			if reason != "invalid_case_proposal" {
				t.Fatalf("run reason = %q, want invalid_case_proposal", reason)
			}
			if len(validationErrors) == 0 || len(validationErrors) > 20 {
				t.Fatalf("validationErrors = %d, want 1..20", len(validationErrors))
			}
			for _, message := range validationErrors {
				if len([]rune(message)) > 500 {
					t.Fatalf("validation error is %d runes, want at most 500", len([]rune(message)))
				}
			}
			if fixture.version.Status != "designing" {
				t.Fatalf("version status = %q, want unchanged designing", fixture.version.Status)
			}
		})
	}
}

// TestDesignReplaceRequiresConfirmation covers spec §6.7 rule 6: replace mode
// without input.confirmReplace is rejected, and a reviewed node blocks replace.
func TestDesignReplaceRequiresConfirmation(t *testing.T) {
	ctx := context.Background()
	payload := strings.Replace(runValidDesignPayload(), `"mode": "append"`, `"mode": "replace"`, 1)
	fixture := newDesignFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, payload)
	service := fixture.service()

	err := service.Materialize(ctx, run.ID)
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_proposal")
	if fixture.runs[run.ID].Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", fixture.runs[run.ID].Status)
	}

	confirmed := newDesignFixture()
	confirmedRun := confirmed.seedRunningRun(domain.RunKindDesign, payload)
	confirmed.runs[confirmedRun.ID] = runWithInput(confirmed.runs[confirmedRun.ID], `{"mode":"replace","confirmReplace":true}`)
	reviewed := confirmed.seedCaseNode(11, 1, "已评审草稿")
	reviewed.Status = domain.NodeStatusReviewed
	confirmed.nodes[11] = reviewed

	err = confirmed.service().Materialize(ctx, confirmedRun.ID)
	requireDomainError(t, err, domain.KindStateConflict, "baseline_frozen")
	if len(confirmed.nodes) != 1 {
		t.Fatalf("nodes = %d, want the reviewed node untouched", len(confirmed.nodes))
	}
}

// runWithInput rewrites one run's stored input document.
func runWithInput(run domain.CaseDesignRun, input string) domain.CaseDesignRun {
	run.Input = []byte(input)
	return run
}

// TestDesignProposalDedupesRequirementIDs covers spec §6.7 rule 2: repeated
// requirement ids are deduplicated into one requirement source.
func TestDesignProposalDedupesRequirementIDs(t *testing.T) {
	ctx := context.Background()
	payload := strings.Replace(runValidDesignPayload(), `"requirementIds": ["7"]`, `"requirementIds": ["7", "7"]`, 1)
	if payload == runValidDesignPayload() {
		t.Fatal("the payload fixture changed; the duplicate ids were not injected")
	}
	fixture := newDesignFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, payload)
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize design run with duplicate requirement ids: %v", err)
	}
	requirementSources := 0
	for _, source := range fixture.sources {
		if source.RefKind == domain.RefRequirement {
			requirementSources++
		}
	}
	if requirementSources != 1 {
		t.Fatalf("requirement sources = %d, want 1 after deduplication", requirementSources)
	}
}

// TestAnchorMissingFileDroppedNotFail covers spec §6.7 rule 4: an anchor whose
// path does not exist in the code baseline is dropped and counted, never failed.
func TestAnchorMissingFileDroppedNotFail(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	fixture.files = map[string]bool{}
	run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize design run: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want 1", fixture.txCount)
	}
	if len(fixture.anchors) != 0 || len(fixture.anchorArgs) != 0 {
		t.Fatalf("anchors = %+v / %+v, want none", fixture.anchors, fixture.anchorArgs)
	}
	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", updated.Status)
	}
	if stats := runStats(t, updated); stats["anchorsDropped"] != float64(1) {
		t.Fatalf("stats = %s, want anchorsDropped=1", string(updated.Stats))
	}
}

// TestMaterializeIsIdempotent covers spec §6.6/§6.13: materializing the same run
// twice has no side effect at all.
func TestMaterializeIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("first materialize: %v", err)
	}
	nodes := len(fixture.nodes)
	steps := len(fixture.steps)
	events := len(fixture.events)
	anchors := len(fixture.anchors)

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("second materialize: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want 1 (second materialize is a no-op)", fixture.txCount)
	}
	if len(fixture.nodes) != nodes || len(fixture.steps) != steps || len(fixture.anchors) != anchors {
		t.Fatalf("second materialize changed rows: nodes %d->%d steps %d->%d anchors %d->%d",
			nodes, len(fixture.nodes), steps, len(fixture.steps), anchors, len(fixture.anchors))
	}
	if len(fixture.events) != events {
		t.Fatalf("second materialize published %d extra event(s)", len(fixture.events)-events)
	}
}

// TestReviewProposalDismissesPreviousAIFindings covers spec §6.8: a review run
// creates a draft review plus findings and dismisses the previous open AI
// findings only, leaving human and non-open findings untouched.
func TestReviewProposalDismissesPreviousAIFindings(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "reviewing"
	fixture.seedCaseNode(11, 1, "已存在用例")
	fixture.seedReview(900, domain.ReviewDraft)
	fixture.seedFinding(901, 900, domain.FindingSourceAI, domain.FindingOpen)
	fixture.seedFinding(902, 900, domain.FindingSourceHuman, domain.FindingOpen)
	fixture.seedFinding(903, 900, domain.FindingSourceAI, domain.FindingFixed)
	payload := `{"findings":[
		{"nodeId":"11","category":"coverage","severity":"blocker","detail":"缺少异常流程"},
		{"category":"other","severity":"info","detail":"建议补充边界值"}
	]}`
	run := fixture.seedRunningRun(domain.RunKindReview, payload)
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize review run: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want 1", fixture.txCount)
	}
	var review *domain.CaseReview
	for _, candidate := range fixture.reviews {
		if candidate.RunID != nil && *candidate.RunID == run.ID {
			copy := candidate
			review = &copy
		}
	}
	if review == nil || review.Status != domain.ReviewDraft {
		t.Fatalf("new review = %+v, want a draft linked to run %d", review, run.ID)
	}
	if fixture.findings[901].Status != domain.FindingDismissed {
		t.Fatalf("previous AI finding status = %q, want dismissed", fixture.findings[901].Status)
	}
	if fixture.findings[902].Status != domain.FindingOpen {
		t.Fatalf("human finding status = %q, want open", fixture.findings[902].Status)
	}
	if fixture.findings[903].Status != domain.FindingFixed {
		t.Fatalf("fixed AI finding status = %q, want fixed", fixture.findings[903].Status)
	}
	created := 0
	for _, finding := range fixture.findings {
		if finding.ReviewID == review.ID {
			created++
			if finding.Source != domain.FindingSourceAI || finding.Status != domain.FindingOpen {
				t.Fatalf("new finding = %+v, want ai/open", finding)
			}
		}
	}
	if created != 2 {
		t.Fatalf("new findings = %d, want 2", created)
	}
	if !fixture.hasEvent(domain.EventCaseReviewed) || !fixture.hasEvent(domain.EventGatePending) {
		t.Fatalf("events = %v, want case.reviewed and gate.pending", fixture.eventTypes())
	}
	if updated := fixture.runs[run.ID]; updated.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", updated.Status)
	}
}

// TestDataPlanSkipsHumanEditedNodes covers spec §6.8: data planning updates only
// preconditions/test_data of nodes whose nodeRevision still matches, creates no
// node and counts the skipped mismatches.
func TestDataPlanSkipsHumanEditedNodes(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.seedCaseNode(11, 2, "已登录下单")
	fixture.seedCaseNode(12, 3, "人工已改")
	payload := `{"items":[
		{"nodeId":"11","nodeRevision":2,"preconditions":"已登录且余额充足","testData":{"user":"qa"}},
		{"nodeId":"12","nodeRevision":1,"preconditions":"过期写入"}
	]}`
	run := fixture.seedRunningRun(domain.RunKindDataPlan, payload)
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize data plan run: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want 1", fixture.txCount)
	}
	if len(fixture.nodes) != 2 {
		t.Fatalf("nodes = %d, want no new node", len(fixture.nodes))
	}
	updated := fixture.nodes[11]
	if updated.Preconditions != "已登录且余额充足" {
		t.Fatalf("node 11 preconditions = %q, want the planned value", updated.Preconditions)
	}
	if string(updated.TestData) != `{"user":"qa"}` && string(updated.TestData) != `{"user": "qa"}` {
		t.Fatalf("node 11 testData = %s, want the planned document", string(updated.TestData))
	}
	if updated.Revision != 3 {
		t.Fatalf("node 11 revision = %d, want 3 after the update", updated.Revision)
	}
	skipped := fixture.nodes[12]
	if skipped.Preconditions != "" || skipped.Revision != 3 {
		t.Fatalf("node 12 = %+v, want untouched revision 3", skipped)
	}
	updatedRun := fixture.runs[run.ID]
	if updatedRun.Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", updatedRun.Status)
	}
	stats := runStats(t, updatedRun)
	if stats["updated"] != float64(1) || stats["skipped"] != float64(1) {
		t.Fatalf("stats = %s, want updated=1 skipped=1", string(updatedRun.Stats))
	}
	if !fixture.hasEvent(domain.EventCaseRunFinished) {
		t.Fatalf("events = %v, want case.run_finished", fixture.eventTypes())
	}
}

// TestMaterializeRejectsFrozenVersion covers spec §6.4 rule 3: after the version
// left designing/reviewing, design and data-plan materialization writes nothing.
func TestMaterializeRejectsFrozenVersion(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		payload string
	}{
		{name: "design", kind: domain.RunKindDesign, payload: runValidDesignPayload()},
		{name: "data plan", kind: domain.RunKindDataPlan, payload: `{"items":[{"nodeId":"11","nodeRevision":1,"preconditions":"x"}]}`},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newDesignFixture()
			fixture.version.Status = "executing"
			fixture.seedCaseNode(11, 1, "人工草稿")
			run := fixture.seedRunningRun(testCase.kind, testCase.payload)
			service := fixture.service()

			err := service.Materialize(ctx, run.ID)
			requireDomainError(t, err, domain.KindStateConflict, "baseline_frozen")
			if fixture.txCount != 0 {
				t.Fatalf("InTx calls = %d, want 0 on a frozen version", fixture.txCount)
			}
			if len(fixture.nodes) != 1 || fixture.nodes[11].Preconditions != "" {
				t.Fatalf("frozen materialization wrote nodes: %+v", fixture.nodes)
			}
			if fixture.runs[run.ID].Status != domain.RunFailed {
				t.Fatalf("run status = %q, want failed", fixture.runs[run.ID].Status)
			}
			if fixture.version.Status != "executing" {
				t.Fatalf("version status = %q, want unchanged executing", fixture.version.Status)
			}
		})
	}
}

// TestMaterializeRejectsBaselineWithoutFrozenStatus covers the stale
// approved/executing mismatch: when a baseline exists but the version snapshot
// is still designing, design/data-plan materialization must refuse to write.
func TestMaterializeRejectsBaselineWithoutFrozenStatus(t *testing.T) {
	cases := []struct {
		name    string
		kind    string
		payload string
		seed    func(*runFixture)
	}{
		{name: "design", kind: domain.RunKindDesign, payload: runValidDesignPayload()},
		{
			name:    "data plan",
			kind:    domain.RunKindDataPlan,
			payload: `{"items":[{"nodeId":"11","nodeRevision":1,"preconditions":"x"}]}`,
			seed:    func(f *runFixture) { f.seedCaseNode(11, 1, "人工草稿") },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newDesignFixture()
			fixture.version.Status = "designing"
			if testCase.seed != nil {
				testCase.seed(fixture)
			}
			seeded := len(fixture.nodes)
			fixture.baseline = &domain.CaseBaseline{
				ID: 1234, VersionID: runTestVersionID, ReviewID: 9001,
				NodeCount: 1, CaseCount: 1, GraphHash: strings.Repeat("a", 64),
				FrozenBy: 42, FrozenAt: fixture.now,
			}
			run := fixture.seedRunningRun(testCase.kind, testCase.payload)
			service := fixture.service()

			err := service.Materialize(ctx, run.ID)
			requireDomainError(t, err, domain.KindStateConflict, "baseline_frozen")
			if fixture.txCount != 0 {
				t.Fatalf("InTx calls = %d, want 0 with a frozen baseline", fixture.txCount)
			}
			if len(fixture.nodes) != seeded {
				t.Fatalf("nodes = %d, want the seeded %d untouched", len(fixture.nodes), seeded)
			}
			if fixture.runs[run.ID].Status != domain.RunFailed {
				t.Fatalf("run status = %q, want failed", fixture.runs[run.ID].Status)
			}
			if reason, _ := runErrors(t, fixture.runs[run.ID]); reason != "baseline_frozen" {
				t.Fatalf("run reason = %q, want baseline_frozen", reason)
			}
		})
	}
}

// TestReviewGatePendingOnlyWhenReviewing covers spec §6.11: gate.pending is the
// "entered reviewing" signal, so review materialization emits it only while the
// version is actually reviewing.
func TestReviewGatePendingOnlyWhenReviewing(t *testing.T) {
	cases := []struct {
		name   string
		status string
		want   bool
	}{
		{name: "reviewing", status: "reviewing", want: true},
		{name: "designing", status: "designing", want: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newRunFixture()
			fixture.version.Status = testCase.status
			run := fixture.seedRunningRun(domain.RunKindReview,
				`{"findings":[{"category":"other","severity":"info","detail":"建议补充边界值"}]}`)
			service := fixture.service()

			if err := service.Materialize(ctx, run.ID); err != nil {
				t.Fatalf("materialize review run: %v", err)
			}
			if got := fixture.hasEvent(domain.EventGatePending); got != testCase.want {
				t.Fatalf("gate.pending emitted = %v, want %v on a %s version", got, testCase.want, testCase.status)
			}
			if !fixture.hasEvent(domain.EventCaseReviewed) {
				t.Fatal("missing case.reviewed event")
			}
		})
	}
}

// TestDesignMaterializationEnforcesNodeCap covers spec §7.3: existing nodes plus
// the proposed scenarios/cases must stay within the 300-node cap, otherwise the
// run fails with graph_too_large and zero graph writes.
func TestDesignMaterializationEnforcesNodeCap(t *testing.T) {
	ctx := context.Background()
	t.Run("above the cap", func(t *testing.T) {
		fixture := newDesignFixture()
		for index := 0; index < 299; index++ {
			fixture.seedCaseNode(int64(1000+index), 1, fmt.Sprintf("existing-%d", index))
		}
		run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
		service := fixture.service()

		err := service.Materialize(ctx, run.ID)
		requireDomainError(t, err, domain.KindConflict, "graph_too_large")
		if fixture.txCount != 0 {
			t.Fatalf("InTx calls = %d, want 0 for an oversized graph", fixture.txCount)
		}
		if len(fixture.nodes) != 299 {
			t.Fatalf("nodes = %d, want the seeded 299 untouched", len(fixture.nodes))
		}
		reason, validationErrors := runErrors(t, fixture.runs[run.ID])
		if reason != "graph_too_large" || len(validationErrors) == 0 {
			t.Fatalf("run errors reason=%q validationErrors=%v, want graph_too_large with details",
				reason, validationErrors)
		}
	})
	t.Run("at the cap", func(t *testing.T) {
		fixture := newDesignFixture()
		for index := 0; index < 298; index++ {
			fixture.seedCaseNode(int64(1000+index), 1, fmt.Sprintf("existing-%d", index))
		}
		run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
		service := fixture.service()

		if err := service.Materialize(ctx, run.ID); err != nil {
			t.Fatalf("materialize design run at the cap: %v", err)
		}
		if len(fixture.nodes) != 300 {
			t.Fatalf("nodes = %d, want exactly 300", len(fixture.nodes))
		}
	})
}

// TestDataPlanItemsMustBeInRunNodeScope covers spec §6.8: when the run was
// started with an explicit nodeIds scope, every item must target a node in that
// scope.
func TestDataPlanItemsMustBeInRunNodeScope(t *testing.T) {
	ctx := context.Background()
	t.Run("out of scope", func(t *testing.T) {
		fixture := newRunFixture()
		fixture.seedCaseNode(11, 1, "in scope")
		fixture.seedCaseNode(12, 1, "out of scope")
		run := fixture.seedRunningRun(domain.RunKindDataPlan,
			`{"items":[
				{"nodeId":"11","nodeRevision":1,"preconditions":"已登录"},
				{"nodeId":"12","nodeRevision":1,"preconditions":"越界写入"}
			]}`)
		run.Input = []byte(`{"kind":"data_plan","nodeIds":[11]}`)
		fixture.runs[run.ID] = run
		service := fixture.service()

		err := service.Materialize(ctx, run.ID)
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_proposal")
		if fixture.txCount != 0 {
			t.Fatalf("InTx calls = %d, want 0 for an out-of-scope item", fixture.txCount)
		}
		if fixture.nodes[11].Preconditions != "" || fixture.nodes[12].Preconditions != "" {
			t.Fatalf("out-of-scope submission wrote nodes: %+v", fixture.nodes)
		}
		if fixture.runs[run.ID].Status != domain.RunFailed {
			t.Fatalf("run status = %q, want failed", fixture.runs[run.ID].Status)
		}
	})
	t.Run("in scope", func(t *testing.T) {
		fixture := newRunFixture()
		fixture.seedCaseNode(11, 1, "in scope")
		fixture.seedCaseNode(12, 1, "not targeted")
		run := fixture.seedRunningRun(domain.RunKindDataPlan,
			`{"items":[{"nodeId":"11","nodeRevision":1,"preconditions":"已登录"}]}`)
		run.Input = []byte(`{"kind":"data_plan","nodeIds":[11]}`)
		fixture.runs[run.ID] = run
		service := fixture.service()

		if err := service.Materialize(ctx, run.ID); err != nil {
			t.Fatalf("materialize in-scope data plan: %v", err)
		}
		if fixture.nodes[11].Preconditions != "已登录" {
			t.Fatalf("node 11 preconditions = %q, want the planned value", fixture.nodes[11].Preconditions)
		}
		if fixture.nodes[12].Preconditions != "" {
			t.Fatalf("node 12 was written outside the scope: %+v", fixture.nodes[12])
		}
	})
}

// TestDesignReplaceDeletesDraftsAndRematerializes covers spec §6.7 rule 6 and
// §10.3: a confirmed mode=replace deletes the draft graph (children cascade),
// then rematerializes the proposal in the same transaction.
func TestDesignReplaceDeletesDraftsAndRematerializes(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	parent := int64(11)
	stepID := int64(13)
	requirementID := int64(7)
	fixture.nodes[11] = domain.CaseNode{
		ID: 11, VersionID: runTestVersionID, Kind: domain.NodeKindScenario,
		Title: "旧场景", Priority: domain.PriorityP2, Source: domain.NodeSourceUserInput,
		Status: domain.NodeStatusDraft, SortOrder: 1, Revision: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.nodes[12] = domain.CaseNode{
		ID: 12, VersionID: runTestVersionID, ParentID: &parent, Kind: domain.NodeKindCase,
		Title: "旧用例", Priority: domain.PriorityP1, Source: domain.NodeSourceUserInput,
		Status: domain.NodeStatusDraft, SortOrder: 2, Revision: 1,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.steps[stepID] = domain.CaseStep{
		ID: stepID, NodeID: 12, Seq: 1, Action: "旧步骤", Source: domain.StepSourceUserInput,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.assertions[14] = domain.CaseAssertion{
		ID: 14, NodeID: 12, StepID: &stepID, Kind: domain.AssertionKindUI,
		Expression: "旧断言", Source: domain.NodeSourceUserInput,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	fixture.sources[15] = domain.CaseSource{
		ID: 15, VersionID: runTestVersionID, NodeID: 12, RefKind: domain.RefRequirement,
		RefID: &requirementID, Source: domain.NodeSourceUserInput, Confidence: domain.ConfidenceHigh,
		CreatedAt: fixture.now, UpdatedAt: fixture.now,
	}
	payload := strings.Replace(runValidDesignPayload(), `"mode": "append"`, `"mode": "replace"`, 1)
	run := fixture.seedRunningRun(domain.RunKindDesign, payload)
	run.Input = []byte(`{"mode":"replace","confirmReplace":true}`)
	fixture.runs[run.ID] = run
	service := fixture.service()

	if err := service.Materialize(ctx, run.ID); err != nil {
		t.Fatalf("materialize replace run: %v", err)
	}
	if fixture.txCount != 1 {
		t.Fatalf("InTx calls = %d, want exactly 1", fixture.txCount)
	}
	for _, nodeID := range []int64{11, 12} {
		if _, ok := fixture.nodes[nodeID]; ok {
			t.Fatalf("draft node %d survived the replace", nodeID)
		}
	}
	if _, ok := fixture.steps[stepID]; ok {
		t.Fatal("step of a replaced draft survived")
	}
	if _, ok := fixture.assertions[14]; ok {
		t.Fatal("assertion of a replaced draft survived")
	}
	if _, ok := fixture.sources[15]; ok {
		t.Fatal("source of a replaced draft survived")
	}
	if len(fixture.nodes) != 2 {
		t.Fatalf("nodes = %d, want the rematerialized scenario + case", len(fixture.nodes))
	}
	if len(fixture.steps) != 1 || len(fixture.assertions) != 1 || len(fixture.sources) != 2 {
		t.Fatalf("rematerialized rows: steps=%d assertions=%d sources=%d, want 1/1/2",
			len(fixture.steps), len(fixture.assertions), len(fixture.sources))
	}
	for _, node := range fixture.nodes {
		if node.Source != domain.NodeSourceAIGenerated || node.Status != domain.NodeStatusDraft {
			t.Fatalf("rematerialized node = %+v, want ai_generated/draft", node)
		}
	}
	if fixture.runs[run.ID].Status != domain.RunDone {
		t.Fatalf("run status = %q, want done", fixture.runs[run.ID].Status)
	}
}

// stepsOfNodeForRun lists the stored steps of one node in sequence order.
func (f *runFixture) stepsOfNodeForRun(nodeID int64) []domain.CaseStep {
	steps := make([]domain.CaseStep, 0)
	for _, step := range f.steps {
		if step.NodeID == nodeID {
			steps = append(steps, step)
		}
	}
	for left := 0; left < len(steps); left++ {
		for right := left + 1; right < len(steps); right++ {
			if steps[right].Seq < steps[left].Seq {
				steps[left], steps[right] = steps[right], steps[left]
			}
		}
	}
	return steps
}

// assertionsOfNodeForRun lists the stored assertions of one node.
func (f *runFixture) assertionsOfNodeForRun(nodeID int64) []domain.CaseAssertion {
	assertions := make([]domain.CaseAssertion, 0)
	for _, assertion := range f.assertions {
		if assertion.NodeID == nodeID {
			assertions = append(assertions, assertion)
		}
	}
	return assertions
}

// sourcesOfNodeForRun lists the stored sources of one node.
func (f *runFixture) sourcesOfNodeForRun(nodeID int64) []domain.CaseSource {
	sources := make([]domain.CaseSource, 0)
	for _, source := range f.sources {
		if source.NodeID == nodeID {
			sources = append(sources, source)
		}
	}
	return sources
}

// eventTypes returns the recorded event type sequence.
func (f *runFixture) eventTypes() []string {
	types := make([]string, 0, len(f.events))
	for _, event := range f.events {
		types = append(types, event.Type)
	}
	return types
}
