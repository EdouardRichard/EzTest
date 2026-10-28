package application_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// executionGraphHash is a frozen graph hash of the execution fixture.
const executionGraphHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// countingBaselineRepo wraps the shared fixture baseline repository and counts
// the writes MarkExecuted must never issue (spec §5.2: execution never touches
// the baseline).
type countingBaselineRepo struct {
	inner   application.BaselineRepository
	creates int
	deletes int
}

func (r *countingBaselineRepo) Create(ctx context.Context, baseline domain.CaseBaseline) (domain.CaseBaseline, error) {
	r.creates++
	return r.inner.Create(ctx, baseline)
}

func (r *countingBaselineRepo) GetByVersion(ctx context.Context, versionID int64) (domain.CaseBaseline, error) {
	return r.inner.GetByVersion(ctx, versionID)
}

func (r *countingBaselineRepo) DeleteByVersion(ctx context.Context, versionID int64) error {
	r.deletes++
	return r.inner.DeleteByVersion(ctx, versionID)
}

// newExecutionFixture builds an executing version frozen with a baseline.
func newExecutionFixture() (*runFixture, *countingBaselineRepo) {
	fixture := newRunFixture()
	fixture.version.Status = "executing"
	fixture.baseline = &domain.CaseBaseline{
		ID:                 9000,
		VersionID:          runTestVersionID,
		NodeCount:          2,
		CaseCount:          1,
		GraphHash:          executionGraphHash,
		CodeBaselineCommit: fixture.version.CodeBaselineCommit,
		FrozenAt:           fixture.now,
	}
	return fixture, &countingBaselineRepo{inner: runBaselineRepo{f: fixture}}
}

// executionServiceFor wires the production execution service to the shared
// graph fixture.
func executionServiceFor(fixture *runFixture, baselines application.BaselineRepository) *application.ExecutionService {
	return application.NewExecutionService(application.ExecutionDeps{
		Graph:      fixture,
		Nodes:      runNodeRepo{f: fixture},
		Steps:      runStepRepo{f: fixture},
		Assertions: runAssertionRepo{f: fixture},
		Sources:    runSourceRepo{f: fixture},
		Baselines:  baselines,
		Events:     runEventSink{f: fixture},
		Clock:      runClock{f: fixture},
	})
}

// seedExecutionScenario stores one draft scenario node.
func seedExecutionScenario(fixture *runFixture, id int64) domain.CaseNode {
	node := domain.CaseNode{
		ID:        id,
		VersionID: runTestVersionID,
		Kind:      domain.NodeKindScenario,
		Title:     "checkout",
		Priority:  domain.PriorityP2,
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: int(id),
		Revision:  1,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	fixture.nodes[id] = node
	return node
}

// seedExecutionCase stores one complete case: the node, two steps in reverse
// sequence order, two assertions with descending ids and two requirement
// sources, so the snapshot ordering is observable.
func seedExecutionCase(fixture *runFixture, id int64, status string) domain.CaseNode {
	node := domain.CaseNode{
		ID:            id,
		VersionID:     runTestVersionID,
		Kind:          domain.NodeKindCase,
		Title:         "pay order",
		Description:   "pay an order end to end",
		Priority:      domain.PriorityP1,
		Preconditions: "the user is signed in",
		TestData:      []byte(`{"account":"alice"}`),
		Source:        domain.NodeSourceUserInput,
		Status:        status,
		SortOrder:     int(id),
		Revision:      4,
		CreatedAt:     fixture.now,
		UpdatedAt:     fixture.now,
	}
	fixture.nodes[id] = node
	second := domain.CaseStep{
		ID:        id*100 + 2,
		NodeID:    id,
		Seq:       2,
		Action:    "confirm the payment",
		Expected:  "the payment is accepted",
		Data:      `{"amount":10}`,
		Source:    domain.StepSourceUserInput,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	first := domain.CaseStep{
		ID:        id*100 + 1,
		NodeID:    id,
		Seq:       1,
		Action:    "open the checkout page",
		Expected:  "the checkout page is visible",
		Data:      `{"currency":"CNY"}`,
		Source:    domain.StepSourceUserInput,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	fixture.steps[first.ID] = first
	fixture.steps[second.ID] = second
	// Insert the assertions with descending ids and links to the first step: the
	// snapshot must order them by id and resolve step_seq from the step set.
	assertionB := domain.CaseAssertion{
		ID:         id*1000 + 2,
		NodeID:     id,
		StepID:     graphInt64Ptr(first.ID),
		Kind:       domain.AssertionKindAPI,
		Target:     "/api/payments",
		Expression: "status == 201",
		Expected:   "the payment is created",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	assertionA := domain.CaseAssertion{
		ID:         id*1000 + 1,
		NodeID:     id,
		Kind:       domain.AssertionKindUI,
		Expression: "the receipt is visible",
		Expected:   "a receipt is shown",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	fixture.assertions[assertionA.ID] = assertionA
	fixture.assertions[assertionB.ID] = assertionB
	for index, requirementID := range []int64{9, 7} {
		sourceID := id*10000 + int64(index)
		ref := requirementID
		fixture.sources[sourceID] = domain.CaseSource{
			ID:         sourceID,
			VersionID:  runTestVersionID,
			NodeID:     id,
			RefKind:    domain.RefRequirement,
			RefID:      &ref,
			Source:     domain.NodeSourceUserInput,
			Confidence: domain.ConfidenceHigh,
			CreatedAt:  fixture.now,
			UpdatedAt:  fixture.now,
		}
	}
	return node
}

// TestExecutionBaselineRequiresBaseline covers spec §5.1/§5.2: exposing the
// execution baseline of an unfrozen version is the stable 409 baseline_required.
func TestExecutionBaselineRequiresBaseline(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	baselines := &countingBaselineRepo{inner: runBaselineRepo{f: fixture}}
	service := executionServiceFor(fixture, baselines)

	_, err := service.Baseline(ctx, runTestVersionID)
	requireDomainError(t, err, domain.KindStateConflict, "baseline_required")
}

// TestExecutionBaselineReturnsHashCommitAndCaseIDs covers the frozen baseline
// projection: the graph hash, the optional code baseline commit and every case
// node id in ascending order (scenarios and other versions are excluded).
func TestExecutionBaselineReturnsHashCommitAndCaseIDs(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionScenario(fixture, 1000)
	seedExecutionCase(fixture, 3000, domain.NodeStatusReviewed)
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	foreign := seedExecutionCase(fixture, 4000, domain.NodeStatusReviewed)
	foreign.VersionID = 2
	fixture.nodes[foreign.ID] = foreign

	baseline, err := executionServiceFor(fixture, baselines).Baseline(ctx, runTestVersionID)
	if err != nil {
		t.Fatalf("baseline: %v", err)
	}
	if baseline.GraphHash != executionGraphHash {
		t.Fatalf("graph hash = %q, want %q", baseline.GraphHash, executionGraphHash)
	}
	if baseline.CodeBaselineCommit == nil || *baseline.CodeBaselineCommit != *fixture.version.CodeBaselineCommit {
		t.Fatalf("code baseline commit = %v, want %v", baseline.CodeBaselineCommit, fixture.version.CodeBaselineCommit)
	}
	if len(baseline.CaseIDs) != 2 || baseline.CaseIDs[0] != 2000 || baseline.CaseIDs[1] != 3000 {
		t.Fatalf("case ids = %v, want [2000 3000]", baseline.CaseIDs)
	}
}

// TestExecutionSnapshotCasesAllAndSubset covers the snapshot selection rule:
// empty caseIDs means every baseline case, an explicit subset is filtered and
// the result is always ordered by case node id ascending.
func TestExecutionSnapshotCasesAllAndSubset(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionScenario(fixture, 1000)
	seedExecutionCase(fixture, 3000, domain.NodeStatusReviewed)
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	service := executionServiceFor(fixture, baselines)

	all, err := service.SnapshotCases(ctx, runTestVersionID, nil)
	if err != nil {
		t.Fatalf("snapshot all: %v", err)
	}
	if len(all) != 2 || all[0].NodeID != 2000 || all[1].NodeID != 3000 {
		t.Fatalf("all snapshot node ids = %v, want [2000 3000]", executionSnapshotIDs(all))
	}

	subset, err := service.SnapshotCases(ctx, runTestVersionID, []int64{3000, 2000, 3000})
	if err != nil {
		t.Fatalf("snapshot subset: %v", err)
	}
	if len(subset) != 2 || subset[0].NodeID != 2000 || subset[1].NodeID != 3000 {
		t.Fatalf("subset snapshot node ids = %v, want [2000 3000]", executionSnapshotIDs(subset))
	}
}

// TestExecutionSnapshotCasePayload covers the frozen payload of one case: the
// node fields, steps by seq, assertions by id with their resolved step_seq, the
// requirement sources and the frozen graph hash.
func TestExecutionSnapshotCasePayload(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	service := executionServiceFor(fixture, baselines)

	cases, err := service.SnapshotCases(ctx, runTestVersionID, []int64{2000})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(cases) != 1 {
		t.Fatalf("snapshots = %d, want 1", len(cases))
	}
	snapshot := cases[0]
	if snapshot.NodeID != 2000 || snapshot.Revision != 4 {
		t.Fatalf("node id/revision = %d/%d, want 2000/4", snapshot.NodeID, snapshot.Revision)
	}
	if snapshot.Title != "pay order" || snapshot.Priority != domain.PriorityP1 {
		t.Fatalf("title/priority = %q/%q", snapshot.Title, snapshot.Priority)
	}
	if snapshot.Preconditions != "the user is signed in" {
		t.Fatalf("preconditions = %q", snapshot.Preconditions)
	}
	if string(snapshot.TestData) != `{"account":"alice"}` {
		t.Fatalf("test data = %q", string(snapshot.TestData))
	}
	if snapshot.GraphHash != executionGraphHash {
		t.Fatalf("graph hash = %q, want %q", snapshot.GraphHash, executionGraphHash)
	}
	if len(snapshot.Steps) != 2 || snapshot.Steps[0].Seq != 1 || snapshot.Steps[1].Seq != 2 {
		t.Fatalf("steps = %+v, want seq 1 then 2", snapshot.Steps)
	}
	if snapshot.Steps[0].Action != "open the checkout page" || snapshot.Steps[1].Action != "confirm the payment" {
		t.Fatalf("steps out of seq order: %+v", snapshot.Steps)
	}
	if len(snapshot.Assertions) != 2 || snapshot.Assertions[0].ID >= snapshot.Assertions[1].ID {
		t.Fatalf("assertions = %+v, want ascending ids", snapshot.Assertions)
	}
	if snapshot.Assertions[0].ID != 2000001 || snapshot.Assertions[1].ID != 2000002 {
		t.Fatalf("assertion ids = %d/%d, want 2000001/2000002",
			snapshot.Assertions[0].ID, snapshot.Assertions[1].ID)
	}
	if snapshot.Assertions[0].StepSeq != nil {
		t.Fatalf("assertion without a step must have a nil step_seq, got %v", *snapshot.Assertions[0].StepSeq)
	}
	if snapshot.Assertions[1].StepSeq == nil || *snapshot.Assertions[1].StepSeq != 1 {
		t.Fatalf("assertion step_seq = %v, want 1", snapshot.Assertions[1].StepSeq)
	}
	if snapshot.Assertions[1].Kind != domain.AssertionKindAPI || snapshot.Assertions[1].Target != "/api/payments" {
		t.Fatalf("assertion = %+v", snapshot.Assertions[1])
	}
	if len(snapshot.RequirementIDs) != 2 || snapshot.RequirementIDs[0] != 7 || snapshot.RequirementIDs[1] != 9 {
		t.Fatalf("requirement ids = %v, want [7 9]", snapshot.RequirementIDs)
	}
}

// TestExecutionSnapshotCasesRejectsForeignAndNonCaseIDs covers the snapshot
// selection validation: an id outside the version baseline or a scenario id is
// invalid_case_ids, and an unfrozen version is baseline_required.
func TestExecutionSnapshotCasesRejectsForeignAndNonCaseIDs(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionScenario(fixture, 1000)
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	foreign := seedExecutionCase(fixture, 4000, domain.NodeStatusReviewed)
	foreign.VersionID = 2
	fixture.nodes[foreign.ID] = foreign
	// A case created after the freeze cannot belong to the baseline snapshot.
	late := seedExecutionCase(fixture, 5000, domain.NodeStatusReviewed)
	late.CreatedAt = fixture.baseline.FrozenAt.Add(time.Hour)
	fixture.nodes[late.ID] = late
	service := executionServiceFor(fixture, baselines)

	for _, id := range []int64{1000, 4000, 5000, 9999} {
		_, err := service.SnapshotCases(ctx, runTestVersionID, []int64{id})
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_ids")
	}

	unfrozen := newRunFixture()
	unfrozenBaselines := &countingBaselineRepo{inner: runBaselineRepo{f: unfrozen}}
	_, err := executionServiceFor(unfrozen, unfrozenBaselines).SnapshotCases(ctx, runTestVersionID, nil)
	requireDomainError(t, err, domain.KindStateConflict, "baseline_required")
}

// TestMarkExecutedOnlyReviewedCasesAndKeepsBaseline covers spec §5.2: execution
// marks reviewed cases executed and never writes the baseline or the frozen
// content (steps, assertions, sources).
func TestMarkExecutedOnlyReviewedCasesAndKeepsBaseline(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionCase(fixture, 42, domain.NodeStatusReviewed)
	service := executionServiceFor(fixture, baselines)
	baselineBefore := *fixture.baseline
	stepsBefore := runCloneMap(fixture.steps)
	assertionsBefore := runCloneMap(fixture.assertions)
	sourcesBefore := runCloneMap(fixture.sources)

	if err := service.MarkExecuted(ctx, runTestVersionID, []int64{42}); err != nil {
		t.Fatalf("mark executed: %v", err)
	}
	if got := fixture.nodes[42].Status; got != domain.NodeStatusExecuted {
		t.Fatalf("status = %q, want executed", got)
	}
	if baselines.creates != 0 || baselines.deletes != 0 {
		t.Fatalf("baseline writes = %d/%d, want 0/0", baselines.creates, baselines.deletes)
	}
	if *fixture.baseline != baselineBefore {
		t.Fatalf("baseline changed: %+v, want %+v", *fixture.baseline, baselineBefore)
	}
	if len(fixture.steps) != len(stepsBefore) || len(fixture.assertions) != len(assertionsBefore) ||
		len(fixture.sources) != len(sourcesBefore) {
		t.Fatal("mark executed changed the frozen child rows")
	}
	for id, step := range stepsBefore {
		if fixture.steps[id] != step {
			t.Fatalf("step %d changed: %+v, want %+v", id, fixture.steps[id], step)
		}
	}
	for id, assertion := range assertionsBefore {
		if fixture.assertions[id] != assertion {
			t.Fatalf("assertion %d changed: %+v, want %+v", id, fixture.assertions[id], assertion)
		}
	}
	for id, source := range sourcesBefore {
		if fixture.sources[id] != source {
			t.Fatalf("source %d changed: %+v, want %+v", id, fixture.sources[id], source)
		}
	}
	node := fixture.nodes[42]
	if node.Title != "pay order" || node.Preconditions != "the user is signed in" ||
		string(node.TestData) != `{"account":"alice"}` || node.Priority != domain.PriorityP1 {
		t.Fatalf("mark executed changed the case content: %+v", node)
	}
	if len(fixture.events) != 1 {
		t.Fatalf("events = %d, want one case.status_changed", len(fixture.events))
	}
	event := fixture.events[0]
	if event.Type != domain.EventCaseStatusChanged {
		t.Fatalf("event type = %q, want %q", event.Type, domain.EventCaseStatusChanged)
	}
	if event.VersionID != runTestVersionID || len(event.RefCaseIDs) != 1 || event.RefCaseIDs[0] != 42 {
		t.Fatalf("event = %+v, want version 1 case 42", event)
	}
	if event.Detail["from"] != domain.NodeStatusReviewed || event.Detail["to"] != domain.NodeStatusExecuted {
		t.Fatalf("event detail = %+v", event.Detail)
	}
}

// TestMarkExecutedIsIdempotent covers the reviewed→executed idempotency: a
// second call on an already executed case succeeds without a second event.
func TestMarkExecutedIsIdempotent(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionCase(fixture, 42, domain.NodeStatusReviewed)
	service := executionServiceFor(fixture, baselines)

	if err := service.MarkExecuted(ctx, runTestVersionID, []int64{42}); err != nil {
		t.Fatalf("first mark: %v", err)
	}
	revision := fixture.nodes[42].Revision
	if err := service.MarkExecuted(ctx, runTestVersionID, []int64{42}); err != nil {
		t.Fatalf("second mark: %v", err)
	}
	if got := fixture.nodes[42].Status; got != domain.NodeStatusExecuted {
		t.Fatalf("status = %q, want executed", got)
	}
	if fixture.nodes[42].Revision != revision {
		t.Fatalf("revision = %d, want unchanged %d", fixture.nodes[42].Revision, revision)
	}
	if len(fixture.events) != 1 {
		t.Fatalf("events = %d, want one event from the first call only", len(fixture.events))
	}
	if baselines.creates != 0 || baselines.deletes != 0 {
		t.Fatalf("baseline writes = %d/%d, want 0/0", baselines.creates, baselines.deletes)
	}
}

// TestMarkExecutedRejectsOtherStatuses covers the reviewed-only guard: every
// other node status is a 409 and writes nothing.
func TestMarkExecutedRejectsOtherStatuses(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{
		domain.NodeStatusDraft,
		domain.NodeStatusSupplemented,
		domain.NodeStatusMerged,
		domain.NodeStatusRejected,
		domain.NodeStatusNeedsRevalidation,
	} {
		t.Run(status, func(t *testing.T) {
			fixture, baselines := newExecutionFixture()
			seedExecutionCase(fixture, 42, status)
			service := executionServiceFor(fixture, baselines)

			err := service.MarkExecuted(ctx, runTestVersionID, []int64{42})
			requireDomainError(t, err, domain.KindStateConflict, "case_status_conflict")
			if got := fixture.nodes[42].Status; got != status {
				t.Fatalf("status = %q, want unchanged %q", got, status)
			}
			if len(fixture.events) != 0 {
				t.Fatalf("events = %d, want 0", len(fixture.events))
			}
			if baselines.creates != 0 || baselines.deletes != 0 {
				t.Fatalf("baseline writes = %d/%d, want 0/0", baselines.creates, baselines.deletes)
			}
		})
	}
}

// TestMarkExecutedRejectsUnknownCase covers the version scoping: an id that is
// not a case node of the version is invalid_case_ids and writes nothing.
func TestMarkExecutedRejectsUnknownCase(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionScenario(fixture, 1000)
	seedExecutionCase(fixture, 2000, domain.NodeStatusReviewed)
	foreign := seedExecutionCase(fixture, 4000, domain.NodeStatusReviewed)
	foreign.VersionID = 2
	fixture.nodes[foreign.ID] = foreign
	service := executionServiceFor(fixture, baselines)

	for _, id := range []int64{1000, 4000, 9999} {
		err := service.MarkExecuted(ctx, runTestVersionID, []int64{2000, id})
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_ids")
	}
	if fixture.nodes[2000].Status != domain.NodeStatusReviewed {
		t.Fatalf("a rejected mark must not execute the valid case: status = %q", fixture.nodes[2000].Status)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("events = %d, want 0", len(fixture.events))
	}
}

// TestMarkExecutedEmptyListIsNoOp covers the empty batch: nothing to mark is a
// successful no-op that publishes nothing.
func TestMarkExecutedEmptyListIsNoOp(t *testing.T) {
	ctx := context.Background()
	fixture, baselines := newExecutionFixture()
	seedExecutionCase(fixture, 42, domain.NodeStatusReviewed)

	if err := executionServiceFor(fixture, baselines).MarkExecuted(ctx, runTestVersionID, nil); err != nil {
		t.Fatalf("mark empty: %v", err)
	}
	if got := fixture.nodes[42].Status; got != domain.NodeStatusReviewed {
		t.Fatalf("status = %q, want reviewed", got)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("events = %d, want 0", len(fixture.events))
	}
}

// executionSnapshotIDs projects the case node ids of a snapshot list.
func executionSnapshotIDs(cases []domain.ExecutionCase) []int64 {
	ids := make([]int64, 0, len(cases))
	for _, executionCase := range cases {
		ids = append(ids, executionCase.NodeID)
	}
	return ids
}
