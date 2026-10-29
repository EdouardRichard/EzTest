package application_test

import (
	"context"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphEventOfType returns the single recorded event of eventType.
func graphEventOfType(t *testing.T, fixture *graphFixture, eventType string) application.Event {
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

// detailString reads one string detail field.
func detailString(t *testing.T, event application.Event, key string) string {
	t.Helper()
	value, ok := event.Detail[key].(string)
	if !ok {
		t.Fatalf("%s detail[%q] = %#v, want a string", event.Type, key, event.Detail[key])
	}
	return value
}

// graphSourcesOfNode returns every source row of nodeID ordered by id.
func graphSourcesOfNode(fixture *graphFixture, nodeID int64) []domain.CaseSource {
	sources := make([]domain.CaseSource, 0)
	for _, source := range fixture.sources {
		if source.NodeID == nodeID {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources
}

// hasEvent reports whether the graph fixture recorded an event of the given type.
func (f *graphFixture) hasEvent(eventType string) bool {
	return f.eventCount(eventType) > 0
}

// eventCount counts the recorded events of the given type.
func (f *graphFixture) eventCount(eventType string) int {
	count := 0
	for _, event := range f.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// supplementGraphHash is a frozen graph hash of the supplement fixture.
const supplementGraphHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// newSupplementFixture builds an executing version with a frozen baseline and
// one confirmed requirement: the only state in which P7 supplement writes are
// allowed (spec §5.2).
func newSupplementFixture() *graphFixture {
	fixture := newGraphFixture()
	fixture.version.Status = "executing"
	fixture.baseline = &domain.CaseBaseline{
		ID:        9000,
		VersionID: fixture.version.ID,
		NodeCount: 2,
		CaseCount: 1,
		GraphHash: supplementGraphHash,
		FrozenAt:  fixture.now,
	}
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: fixture.version.ID, Title: "Refund", Status: "clarified"},
		{ID: 8, VersionID: fixture.version.ID, Title: "Cancel", Status: "clarified"},
	}
	return fixture
}

// supplementCommand is a structurally valid supplement node proposal.
func supplementCommand() application.SupplementNodeCommand {
	return application.SupplementNodeCommand{
		Title:          "Refund a paid order",
		Description:    "refund the order end to end",
		Priority:       domain.PriorityP1,
		Preconditions:  "the user is signed in",
		RequirementIDs: []int64{7},
		Steps: []application.SupplementStep{
			{Action: "open the order", Expected: "the order page is visible"},
			{Action: "submit the refund", Expected: "the refund is accepted", Data: `{"reason":"damaged"}`},
		},
		Assertions: []application.SupplementAssertion{
			{Kind: domain.AssertionKindUI, Target: "body", Expression: "the refund banner is visible", Expected: "visible"},
			{StepIndex: graphIntPtr(1), Kind: domain.AssertionKindAPI, Target: "/api/refunds", Expression: "status == 201"},
		},
		Confidence: domain.ConfidenceHigh,
		RunID:      4242,
	}
}

// TestAppendSupplementNodeAfterFreezeKeepsBaseline covers the first baseline-guard
// exception of spec §5.2: a supplement node is inserted after the freeze with its
// steps, assertions and sources, and the baseline row/hash never changes.
func TestAppendSupplementNodeAfterFreezeKeepsBaseline(t *testing.T) {
	fixture := newSupplementFixture()
	fixture.seedScenario(100, nil)
	fixture.seedCase(200, graphInt64Ptr(100), 7)
	baselineBefore := *fixture.baseline
	nodesBefore := len(fixture.nodes)

	node, err := fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, supplementCommand())
	if err != nil {
		t.Fatalf("append supplement node: %v", err)
	}

	stored, ok := fixture.nodes[node.ID]
	if !ok {
		t.Fatalf("node %d was not persisted", node.ID)
	}
	if stored.Kind != domain.NodeKindCase {
		t.Fatalf("kind = %q, want case", stored.Kind)
	}
	if stored.Source != domain.NodeSourceAISupplement {
		t.Fatalf("source = %q, want ai_supplement", stored.Source)
	}
	if stored.Status != domain.NodeStatusSupplemented {
		t.Fatalf("status = %q, want supplemented", stored.Status)
	}
	if stored.Revision != 1 || stored.CreatedBy != 42 || stored.UpdatedBy != 42 {
		t.Fatalf("revision/actors = %d/%d/%d", stored.Revision, stored.CreatedBy, stored.UpdatedBy)
	}
	if len(fixture.nodes) != nodesBefore+1 {
		t.Fatalf("nodes = %d, want %d", len(fixture.nodes), nodesBefore+1)
	}

	steps := fixture.stepsOfNode(node.ID)
	if len(steps) != 2 || steps[0].Seq != 1 || steps[1].Seq != 2 {
		t.Fatalf("steps = %+v, want two contiguous steps", steps)
	}
	if steps[0].Source != domain.StepSourceAIGenerated {
		t.Fatalf("step source = %q, want ai_generated", steps[0].Source)
	}
	assertions := fixture.assertionsOfNode(node.ID)
	if len(assertions) != 2 {
		t.Fatalf("assertions = %d, want 2", len(assertions))
	}
	for _, assertion := range assertions {
		if assertion.Source != domain.StepSourceAIGenerated {
			t.Fatalf("assertion source = %q, want ai_generated", assertion.Source)
		}
	}

	sources := graphSourcesOfNode(fixture, node.ID)
	if len(sources) != 2 {
		t.Fatalf("sources = %d, want the requirement and the ai_run trace", len(sources))
	}
	var requirementID, runID int64
	for _, source := range sources {
		if source.Source != domain.NodeSourceAISupplement {
			t.Fatalf("source row source = %q, want ai_supplement", source.Source)
		}
		switch source.RefKind {
		case domain.RefRequirement:
			requirementID = *source.RefID
			if source.Confidence != domain.ConfidenceHigh {
				t.Fatalf("requirement confidence = %q, want high", source.Confidence)
			}
		case domain.RefAIRun:
			runID = *source.RefID
		default:
			t.Fatalf("unexpected source kind %q", source.RefKind)
		}
	}
	if requirementID != 7 || runID != 4242 {
		t.Fatalf("sources = requirement %d run %d, want 7/4242", requirementID, runID)
	}

	if *fixture.baseline != baselineBefore {
		t.Fatalf("baseline changed: %+v", *fixture.baseline)
	}
	if fixture.hasEvent(domain.EventCaseSupplemented) != true || fixture.eventCount(domain.EventCaseSupplemented) != 1 {
		t.Fatalf("case.supplemented events = %d, want 1", fixture.eventCount(domain.EventCaseSupplemented))
	}
	if fixture.eventCount(domain.EventCaseCreated) != 1 {
		t.Fatalf("case.created events = %d, want 1", fixture.eventCount(domain.EventCaseCreated))
	}
}

// TestAppendSupplementNodeRequiresConfirmedRequirement covers the spec §5.2
// rule: at least one clarified requirement of the version is mandatory.
func TestAppendSupplementNodeRequiresConfirmedRequirement(t *testing.T) {
	fixture := newSupplementFixture()
	fixture.seedScenario(100, nil)
	nodesBefore := len(fixture.nodes)

	noRequirement := supplementCommand()
	noRequirement.RequirementIDs = nil
	_, err := fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, noRequirement)
	requireDomainError(t, err, domain.KindInvalid, "unconfirmed_requirements")

	unconfirmed := supplementCommand()
	unconfirmed.RequirementIDs = []int64{99}
	_, err = fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, unconfirmed)
	requireDomainError(t, err, domain.KindInvalid, "unconfirmed_requirements")

	if len(fixture.nodes) != nodesBefore {
		t.Fatalf("a rejected append wrote %d nodes", len(fixture.nodes)-nodesBefore)
	}
}

// TestAppendSupplementNodeShapeValidation covers the structural minimums of
// spec §5.2: at least one step and at least one assertion.
func TestAppendSupplementNodeShapeValidation(t *testing.T) {
	fixture := newSupplementFixture()

	noSteps := supplementCommand()
	noSteps.Steps = nil
	_, err := fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, noSteps)
	if err == nil {
		t.Fatal("append without steps must be rejected")
	}

	noAssertions := supplementCommand()
	noAssertions.Assertions = nil
	_, err = fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, noAssertions)
	if err == nil {
		t.Fatal("append without assertions must be rejected")
	}

	if len(fixture.nodes) != 0 {
		t.Fatalf("rejected appends wrote %d nodes", len(fixture.nodes))
	}
}

// TestSupplementEventDetailsUseSpecFields pins the spec §5.2/§8.1/§8.2 event
// contract: `case.supplemented` carries the decimal-string nodeId, the decimal
// string runId, the source and requirementCount (not the old "requirements"
// count), and never the case body.
func TestSupplementEventDetailsUseSpecFields(t *testing.T) {
	fixture := newSupplementFixture()
	node, err := fixture.service().AppendSupplementNode(context.Background(), fixture.version.ID, 42, supplementCommand())
	if err != nil {
		t.Fatalf("append supplement node: %v", err)
	}
	event := graphEventOfType(t, fixture, domain.EventCaseSupplemented)
	if got := detailString(t, event, "nodeId"); got != strconv.FormatInt(node.ID, 10) {
		t.Fatalf("detail.nodeId = %q, want %d as a decimal string", got, node.ID)
	}
	if got := detailString(t, event, "runId"); got != "4242" {
		t.Fatalf("detail.runId = %q, want the decimal string 4242", got)
	}
	if got, ok := event.Detail["requirementCount"].(int); !ok || got != 1 {
		t.Fatalf("detail.requirementCount = %#v, want 1", event.Detail["requirementCount"])
	}
	if got := detailString(t, event, "source"); got != domain.NodeSourceAISupplement {
		t.Fatalf("detail.source = %q, want %q", got, domain.NodeSourceAISupplement)
	}
	if _, exists := event.Detail["requirements"]; exists {
		t.Fatalf("detail still uses the non-spec requirements field: %+v", event.Detail)
	}
}

// TestAppendSupplementNodeRequiresFrozenBaseline covers the freeze precondition:
// supplement nodes only exist after the case baseline froze the version.
func TestAppendSupplementNodeRequiresFrozenBaseline(t *testing.T) {
	unfrozen := newGraphFixture()
	unfrozen.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Title: "Refund", Status: "clarified"}}
	_, err := unfrozen.service().AppendSupplementNode(context.Background(), 1, 42, supplementCommand())
	requireDomainError(t, err, domain.KindStateConflict, "baseline_required")

	completed := newSupplementFixture()
	completed.version.Status = "completed"
	_, err = completed.service().AppendSupplementNode(context.Background(), 1, 42, supplementCommand())
	requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
}

// TestAppendSupplementNodeRejectsDuplicate covers the supplement_key dedupe of
// spec §5.2: the same normalized content is not inserted twice.
func TestAppendSupplementNodeRejectsDuplicate(t *testing.T) {
	fixture := newSupplementFixture()
	first, err := fixture.service().AppendSupplementNode(context.Background(), 1, 42, supplementCommand())
	if err != nil {
		t.Fatalf("first append: %v", err)
	}
	nodesAfterFirst := len(fixture.nodes)

	duplicate := supplementCommand()
	duplicate.Title = "  " + strings.ToUpper(duplicate.Title) + " "
	_, err = fixture.service().AppendSupplementNode(context.Background(), 1, 42, duplicate)
	requireDomainError(t, err, domain.KindStateConflict, "duplicate_supplement")

	if len(fixture.nodes) != nodesAfterFirst {
		t.Fatalf("the duplicate append wrote a node")
	}
	if _, ok := fixture.nodes[first.ID]; !ok {
		t.Fatalf("the first node disappeared")
	}
}

// TestFrozenNodeContentEditStillRejected pins that the freeze guard keeps
// rejecting content edits of an already frozen node (spec §5.2).
func TestFrozenNodeContentEditStillRejected(t *testing.T) {
	fixture := newSupplementFixture()
	node := fixture.seedCase(200, nil, 7)
	title := "edited after freeze"
	_, err := fixture.service().UpdateNode(context.Background(), fixture.version.ID, node.ID, 42,
		application.UpdateNodeCommand{Revision: node.Revision, Title: &title})
	requireDomainError(t, err, domain.KindStateConflict, "baseline_frozen")
	if got := fixture.nodes[node.ID].Title; got == title {
		t.Fatal("the frozen node content changed")
	}
}

// TestTransitionNodeStatusMatrix covers every allowed P7 status transition: only
// status/updated_at/updated_by move, the content rows are untouched, and one
// case.status_changed event is published.
func TestTransitionNodeStatusMatrix(t *testing.T) {
	transitions := []struct{ from, to string }{
		{domain.NodeStatusSupplemented, domain.NodeStatusMerged},
		{domain.NodeStatusSupplemented, domain.NodeStatusRejected},
		{domain.NodeStatusReviewed, domain.NodeStatusNeedsRevalidation},
		{domain.NodeStatusReviewed, domain.NodeStatusExecuted},
		{domain.NodeStatusMerged, domain.NodeStatusExecuted},
		{domain.NodeStatusMerged, domain.NodeStatusRejected},
		{domain.NodeStatusNeedsRevalidation, domain.NodeStatusExecuted},
		{domain.NodeStatusNeedsRevalidation, domain.NodeStatusRejected},
		{domain.NodeStatusExecuted, domain.NodeStatusNeedsRevalidation},
	}
	for _, transition := range transitions {
		t.Run(transition.from+"->"+transition.to, func(t *testing.T) {
			fixture := newSupplementFixture()
			node := fixture.seedCase(200, nil, 7)
			node.Status = transition.from
			if transition.from == domain.NodeStatusSupplemented || transition.from == domain.NodeStatusMerged {
				node.Source = domain.NodeSourceAISupplement
			}
			fixture.nodes[node.ID] = node
			stepsBefore := fixture.stepsOfNode(node.ID)
			assertionsBefore := fixture.assertionsOfNode(node.ID)
			sourcesBefore := graphSourcesOfNode(fixture, node.ID)

			updated, err := fixture.service().TransitionNodeStatus(context.Background(), 1, node.ID, 42, transition.to)
			if err != nil {
				t.Fatalf("transition: %v", err)
			}
			if updated.Status != transition.to || updated.Revision != node.Revision+1 {
				t.Fatalf("updated = status %s revision %d, want %s/%d",
					updated.Status, updated.Revision, transition.to, node.Revision+1)
			}
			stored := fixture.nodes[node.ID]
			if stored.Status != transition.to || stored.UpdatedBy != 42 || !stored.UpdatedAt.Equal(fixture.now) {
				t.Fatalf("stored = %+v", stored)
			}
			if stored.Title != node.Title || stored.Priority != node.Priority {
				t.Fatal("a status transition changed the node content")
			}
			if !reflect.DeepEqual(fixture.stepsOfNode(node.ID), stepsBefore) ||
				!reflect.DeepEqual(fixture.assertionsOfNode(node.ID), assertionsBefore) ||
				!reflect.DeepEqual(graphSourcesOfNode(fixture, node.ID), sourcesBefore) {
				t.Fatal("a status transition changed the child rows")
			}
			statusEvents := 0
			for _, event := range fixture.events {
				if event.Type == domain.EventCaseStatusChanged {
					statusEvents++
					if event.Detail["from"] != transition.from || event.Detail["to"] != transition.to {
						t.Fatalf("event detail = %+v", event.Detail)
					}
				}
			}
			if statusEvents != 1 {
				t.Fatalf("case.status_changed events = %d, want 1", statusEvents)
			}
			wantMark := transition.to == domain.NodeStatusNeedsRevalidation
			if fixture.hasEvent(domain.EventCaseRevalidationMarked) != wantMark {
				t.Fatalf("revalidation event = %v, want %v",
					fixture.hasEvent(domain.EventCaseRevalidationMarked), wantMark)
			}
			if wantMark {
				marked := graphEventOfType(t, fixture, domain.EventCaseRevalidationMarked)
				if got := detailString(t, marked, "nodeId"); got != strconv.FormatInt(node.ID, 10) {
					t.Fatalf("revalidation detail.nodeId = %q, want %d as a decimal string", got, node.ID)
				}
				if got := detailString(t, marked, "confidence"); got != domain.ConfidenceMedium {
					t.Fatalf("revalidation detail.confidence = %q, want %q", got, domain.ConfidenceMedium)
				}
				if got := detailString(t, marked, "source"); got != "requirement" {
					t.Fatalf("revalidation detail.source = %q, want requirement", got)
				}
				if len(marked.RefRequirementIDs) != 1 || marked.RefRequirementIDs[0] != 7 {
					t.Fatalf("revalidation refs.requirement = %v, want [7]", marked.RefRequirementIDs)
				}
				if _, exists := marked.Detail["status"]; exists {
					t.Fatalf("revalidation detail still uses the non-spec status field: %+v", marked.Detail)
				}
			}
		})
	}
}

// TestTransitionNodeStatusRejectsIllegal covers the irreversible edges of the
// matrix: rejected never moves, executed never returns to merged/supplemented,
// and no child/content row is written on a rejected transition.
func TestTransitionNodeStatusRejectsIllegal(t *testing.T) {
	illegal := []struct{ from, to string }{
		{domain.NodeStatusRejected, domain.NodeStatusMerged},
		{domain.NodeStatusRejected, domain.NodeStatusExecuted},
		{domain.NodeStatusExecuted, domain.NodeStatusMerged},
		{domain.NodeStatusExecuted, domain.NodeStatusSupplemented},
		{domain.NodeStatusExecuted, domain.NodeStatusRejected},
		{domain.NodeStatusDraft, domain.NodeStatusNeedsRevalidation},
		{domain.NodeStatusReviewed, domain.NodeStatusMerged},
		{domain.NodeStatusSupplemented, domain.NodeStatusExecuted},
		{domain.NodeStatusNeedsRevalidation, domain.NodeStatusMerged},
	}
	for _, transition := range illegal {
		t.Run(transition.from+"->"+transition.to, func(t *testing.T) {
			fixture := newSupplementFixture()
			node := fixture.seedCase(200, nil, 7)
			node.Status = transition.from
			fixture.nodes[node.ID] = node

			_, err := fixture.service().TransitionNodeStatus(context.Background(), 1, node.ID, 42, transition.to)
			requireDomainError(t, err, domain.KindStateConflict, "case_status_conflict")
			if got := fixture.nodes[node.ID].Status; got != transition.from {
				t.Fatalf("status = %q, want unchanged %q", got, transition.from)
			}
			if len(fixture.events) != 0 {
				t.Fatalf("events = %d, want 0", len(fixture.events))
			}
		})
	}
}

// TestTransitionNodeStatusScoping covers the version/ownership checks: an
// unknown node is a not-found and a node of another version is never touched.
func TestTransitionNodeStatusScoping(t *testing.T) {
	fixture := newSupplementFixture()
	_, err := fixture.service().TransitionNodeStatus(context.Background(), 1, 999, 42, domain.NodeStatusMerged)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")

	foreign := fixture.seedCase(300, nil, 7)
	foreign.VersionID = 2
	fixture.nodes[foreign.ID] = foreign
	_, err = fixture.service().TransitionNodeStatus(context.Background(), 1, foreign.ID, 42, domain.NodeStatusMerged)
	requireDomainError(t, err, domain.KindNotFound, "node_not_found")

	scenario := fixture.seedScenario(400, nil)
	_, err = fixture.service().TransitionNodeStatus(context.Background(), 1, scenario.ID, 42, domain.NodeStatusMerged)
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_ids")
}

// TestTransitionNodeStatusRequiresTransitionableVersion covers the version gate:
// a completed version no longer accepts status writes.
func TestTransitionNodeStatusRequiresTransitionableVersion(t *testing.T) {
	fixture := newSupplementFixture()
	node := fixture.seedCase(200, nil, 7)
	fixture.version.Status = "completed"
	_, err := fixture.service().TransitionNodeStatus(context.Background(), 1, node.ID, 42, domain.NodeStatusMerged)
	requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
}

// concurrentTransitionGraph replays a transition that began before another
// status write committed: the first transaction runs live, the second reads the
// stale snapshot (old revision) and rolls the fixture back when its write hits
// the revision CAS.
type concurrentTransitionGraph struct {
	f        *graphFixture
	snapshot graphSnapshot
	calls    int
}

func (g *concurrentTransitionGraph) InTx(_ context.Context, fn func(application.GraphTx) error) error {
	g.calls++
	if g.calls == 1 {
		g.snapshot = g.f.snapshot()
		return fn(graphTx{g.f})
	}
	backup := g.f.snapshot()
	err := fn(staleGraphTx{graphTx: graphTx{g.f}, snapshot: g.snapshot})
	if err != nil {
		g.f.restore(backup)
	}
	return err
}

// TestTransitionNodeStatusRevisionConflict covers the node revision CAS: a
// status transition that lost a concurrent revision race is
// case_revision_conflict, writes no event and leaves the committed state alone.
func TestTransitionNodeStatusRevisionConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newSupplementFixture()
	node := fixture.seedCase(200, nil, 7)
	node.Status = domain.NodeStatusReviewed
	fixture.nodes[node.ID] = node
	graph := &concurrentTransitionGraph{f: fixture}
	service := fixture.serviceWithGraph(graph)

	if _, err := service.TransitionNodeStatus(ctx, 1, node.ID, 42, domain.NodeStatusNeedsRevalidation); err != nil {
		t.Fatalf("first transition: %v", err)
	}
	eventsAfterCommit := len(fixture.events)

	_, err := service.TransitionNodeStatus(ctx, 1, node.ID, 42, domain.NodeStatusExecuted)
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	if got := fixture.nodes[node.ID].Status; got != domain.NodeStatusNeedsRevalidation {
		t.Fatalf("status after the lost CAS = %q, want needs_revalidation", got)
	}
	if len(fixture.events) != eventsAfterCommit {
		t.Fatalf("the lost CAS published %d extra event(s)", len(fixture.events)-eventsAfterCommit)
	}
}
