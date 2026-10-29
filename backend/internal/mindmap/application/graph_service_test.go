package application_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

// graphFixture is a stateful in-memory implementation of every GraphService
// port. It mimics the PostgreSQL adapters for the rules the service relies on:
// the node revision CAS, version scoping, cascade deletes and the single
// baseline row.
type graphFixture struct {
	version    application.VersionSnapshot
	baseline   *domain.CaseBaseline
	nodes      map[int64]domain.CaseNode
	steps      map[int64]domain.CaseStep
	assertions map[int64]domain.CaseAssertion
	sources    map[int64]domain.CaseSource
	confirmed  []application.RequirementSnapshot
	relations  []application.RelationSnapshot
	events     []application.Event
	audits     []application.AuditEvent
	anchors    application.CodeAnchorGateway
	logger     *slog.Logger
	now        time.Time
	nextID     int64
}

func newGraphFixture() *graphFixture {
	return &graphFixture{
		version: application.VersionSnapshot{
			ID:     1,
			Name:   "P5 version",
			Status: "designing",
		},
		nodes:      make(map[int64]domain.CaseNode),
		steps:      make(map[int64]domain.CaseStep),
		assertions: make(map[int64]domain.CaseAssertion),
		sources:    make(map[int64]domain.CaseSource),
		now:        time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC),
		nextID:     5000,
	}
}

func (f *graphFixture) service() *application.GraphService {
	return f.serviceWithGraph(f)
}

// serviceWithGraph wires the fixture with an alternative transaction runner so
// a test can replay a transaction that overlapped another committed write.
func (f *graphFixture) serviceWithGraph(graph application.GraphRepository) *application.GraphService {
	return application.NewGraphService(application.GraphDeps{
		Graph:        graph,
		Nodes:        graphNodeRepo{f},
		Steps:        graphStepRepo{f},
		Assertions:   graphAssertionRepo{f},
		Sources:      graphSourceRepo{f},
		Baselines:    graphBaselineRepo{f},
		Versions:     graphVersionLifecycle{f},
		Requirements: graphRequirementReader{f},
		Anchors:      f.anchors,
		Events:       graphEventSink{f},
		Audit:        graphAuditSink{f},
		IDs:          graphIDGenerator{f},
		Clock:        graphClock{f},
		Logger:       f.logger,
	})
}

func (f *graphFixture) id() int64 {
	f.nextID++
	return f.nextID
}

func (f *graphFixture) InTx(_ context.Context, fn func(application.GraphTx) error) error {
	return fn(graphTx{f})
}

// seedScenario inserts one draft scenario.
func (f *graphFixture) seedScenario(id int64, parentID *int64) domain.CaseNode {
	node := domain.CaseNode{
		ID:        id,
		VersionID: f.version.ID,
		ParentID:  parentID,
		Kind:      domain.NodeKindScenario,
		Title:     "scenario",
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: int(id),
		Revision:  1,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.nodes[id] = node
	return node
}

// seedCase inserts one complete case: a node, one step, one assertion and one
// requirement source.
func (f *graphFixture) seedCase(id int64, parentID *int64, requirementID int64) domain.CaseNode {
	node := domain.CaseNode{
		ID:        id,
		VersionID: f.version.ID,
		ParentID:  parentID,
		Kind:      domain.NodeKindCase,
		Title:     "case",
		Priority:  domain.PriorityP2,
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: int(id),
		Revision:  1,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.nodes[id] = node
	step := domain.CaseStep{
		ID:        id*10 + 1,
		NodeID:    id,
		Seq:       1,
		Action:    "open the page",
		Source:    domain.StepSourceUserInput,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.steps[step.ID] = step
	assertion := domain.CaseAssertion{
		ID:         id*10 + 2,
		NodeID:     id,
		StepID:     graphInt64Ptr(step.ID),
		Kind:       domain.AssertionKindUI,
		Expression: "the page is visible",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.assertions[assertion.ID] = assertion
	ref := requirementID
	source := domain.CaseSource{
		ID:         id*10 + 3,
		VersionID:  f.version.ID,
		NodeID:     id,
		RefKind:    domain.RefRequirement,
		RefID:      &ref,
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.sources[source.ID] = source
	return node
}

// nodesOfVersion returns the stored nodes ordered like the adapter: sort_order,
// then id.
func (f *graphFixture) nodesOfVersion() []domain.CaseNode {
	nodes := make([]domain.CaseNode, 0, len(f.nodes))
	for _, node := range f.nodes {
		if node.VersionID != f.version.ID {
			continue
		}
		nodes = append(nodes, node)
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes
}

func (f *graphFixture) stepsOfNode(nodeID int64) []domain.CaseStep {
	steps := make([]domain.CaseStep, 0)
	for _, step := range f.steps {
		if step.NodeID == nodeID {
			steps = append(steps, step)
		}
	}
	sort.Slice(steps, func(i, j int) bool { return steps[i].Seq < steps[j].Seq })
	return steps
}

func (f *graphFixture) assertionsOfNode(nodeID int64) []domain.CaseAssertion {
	assertions := make([]domain.CaseAssertion, 0)
	for _, assertion := range f.assertions {
		if assertion.NodeID == nodeID {
			assertions = append(assertions, assertion)
		}
	}
	return assertions
}

func (f *graphFixture) requirementSourcesOfNode(nodeID int64) []domain.CaseSource {
	sources := make([]domain.CaseSource, 0)
	for _, source := range f.sources {
		if source.NodeID == nodeID && source.RefKind == domain.RefRequirement {
			sources = append(sources, source)
		}
	}
	// The fixture stores sources in a map; order by id so tests are stable.
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources
}

// graphNodeRepo implements application.NodeRepository over graphFixture.
type graphNodeRepo struct {
	f *graphFixture
}

func (r graphNodeRepo) Create(_ context.Context, node domain.CaseNode) (domain.CaseNode, error) {
	if _, exists := r.f.nodes[node.ID]; exists {
		return domain.CaseNode{}, errors.New("graphFixture: duplicate node id")
	}
	r.f.nodes[node.ID] = node
	return node, nil
}

func (r graphNodeRepo) Get(_ context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
	node, ok := r.f.nodes[nodeID]
	if !ok || node.VersionID != versionID {
		return domain.CaseNode{}, domain.NewNotFound("node", nodeID)
	}
	return node, nil
}

func (r graphNodeRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseNode, error) {
	nodes := make([]domain.CaseNode, 0, len(r.f.nodes))
	for _, node := range r.f.nodes {
		if node.VersionID == versionID {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nil
}

func (r graphNodeRepo) Update(_ context.Context, node domain.CaseNode, expectedRevision int) (domain.CaseNode, error) {
	current, ok := r.f.nodes[node.ID]
	if !ok || current.VersionID != node.VersionID {
		return domain.CaseNode{}, domain.NewNotFound("node", node.ID)
	}
	if current.Revision != expectedRevision {
		return domain.CaseNode{}, domain.NewStateConflict("case_revision_conflict", "stale node revision")
	}
	node.Revision = expectedRevision + 1
	r.f.nodes[node.ID] = node
	return node, nil
}

func (r graphNodeRepo) Delete(_ context.Context, versionID, nodeID int64, expectedRevision int) error {
	current, ok := r.f.nodes[nodeID]
	if !ok || current.VersionID != versionID {
		return domain.NewNotFound("node", nodeID)
	}
	if current.Revision != expectedRevision {
		return domain.NewStateConflict("case_revision_conflict", "stale node revision")
	}
	// Cascade like the case_node parent_id FK and the child-table FKs.
	doomed := map[int64]struct{}{nodeID: {}}
	changed := true
	for changed {
		changed = false
		for _, node := range r.f.nodes {
			if node.ParentID == nil {
				continue
			}
			if _, parentDoomed := doomed[*node.ParentID]; parentDoomed {
				if _, already := doomed[node.ID]; !already {
					doomed[node.ID] = struct{}{}
					changed = true
				}
			}
		}
	}
	for id := range doomed {
		delete(r.f.nodes, id)
	}
	for id, step := range r.f.steps {
		if _, doomedNode := doomed[step.NodeID]; doomedNode {
			delete(r.f.steps, id)
		}
	}
	for id, assertion := range r.f.assertions {
		if _, doomedNode := doomed[assertion.NodeID]; doomedNode {
			delete(r.f.assertions, id)
		}
	}
	for id, source := range r.f.sources {
		if _, doomedNode := doomed[source.NodeID]; doomedNode {
			delete(r.f.sources, id)
		}
	}
	return nil
}

func (r graphNodeRepo) UpdateStatusByVersion(_ context.Context, versionID int64, from, to string) (int, error) {
	count := 0
	for id, node := range r.f.nodes {
		if node.VersionID == versionID && node.Status == from {
			node.Status = to
			r.f.nodes[id] = node
			count++
		}
	}
	return count, nil
}

func (r graphNodeRepo) RestoreStatusByVersion(_ context.Context, versionID int64, nodes []domain.CaseNode) error {
	for _, node := range nodes {
		current, ok := r.f.nodes[node.ID]
		if !ok || current.VersionID != versionID {
			return domain.NewNotFound("node", node.ID)
		}
		r.f.nodes[node.ID] = node
	}
	return nil
}

func (r graphNodeRepo) SetSortOrders(_ context.Context, versionID int64, ordered []int64) error {
	for index, id := range ordered {
		node, ok := r.f.nodes[id]
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", id)
		}
		node.SortOrder = index
		r.f.nodes[id] = node
	}
	return nil
}

func (r graphNodeRepo) CountByVersion(_ context.Context, versionID int64) (int, int, error) {
	nodes, cases := 0, 0
	for _, node := range r.f.nodes {
		if node.VersionID != versionID {
			continue
		}
		nodes++
		if node.Kind == domain.NodeKindCase {
			cases++
		}
	}
	return nodes, cases, nil
}

// graphStepRepo implements application.StepRepository over graphFixture.
type graphStepRepo struct {
	f *graphFixture
}

func (r graphStepRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseStep, error) {
	steps := make([]domain.CaseStep, 0, len(r.f.steps))
	for _, step := range r.f.steps {
		if node, ok := r.f.nodes[step.NodeID]; ok && node.VersionID == versionID {
			steps = append(steps, step)
		}
	}
	sort.Slice(steps, func(i, j int) bool {
		if steps[i].NodeID != steps[j].NodeID {
			return steps[i].NodeID < steps[j].NodeID
		}
		if steps[i].Seq != steps[j].Seq {
			return steps[i].Seq < steps[j].Seq
		}
		return steps[i].ID < steps[j].ID
	})
	return steps, nil
}

func (r graphStepRepo) Create(_ context.Context, step domain.CaseStep) (domain.CaseStep, error) {
	if _, exists := r.f.steps[step.ID]; exists {
		return domain.CaseStep{}, errors.New("graphFixture: duplicate step id")
	}
	r.f.steps[step.ID] = step
	return step, nil
}

func (r graphStepRepo) Update(_ context.Context, step domain.CaseStep) error {
	if _, ok := r.f.steps[step.ID]; !ok {
		return domain.NewNotFound("step", step.ID)
	}
	r.f.steps[step.ID] = step
	return nil
}

func (r graphStepRepo) Delete(_ context.Context, nodeID, stepID int64) error {
	step, ok := r.f.steps[stepID]
	if !ok || step.NodeID != nodeID {
		return domain.NewNotFound("step", stepID)
	}
	delete(r.f.steps, stepID)
	return nil
}

func (r graphStepRepo) ReplaceSeq(_ context.Context, nodeID int64, ordered []int64) error {
	for index, id := range ordered {
		step, ok := r.f.steps[id]
		if !ok || step.NodeID != nodeID {
			return domain.NewNotFound("step", id)
		}
		step.Seq = index + 1
		r.f.steps[id] = step
	}
	return nil
}

func (r graphStepRepo) CountByNode(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, step := range r.f.steps {
		if step.NodeID == nodeID {
			count++
		}
	}
	return count, nil
}

// graphAssertionRepo implements application.AssertionRepository over
// graphFixture.
type graphAssertionRepo struct {
	f *graphFixture
}

func (r graphAssertionRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseAssertion, error) {
	assertions := make([]domain.CaseAssertion, 0, len(r.f.assertions))
	for _, assertion := range r.f.assertions {
		if node, ok := r.f.nodes[assertion.NodeID]; ok && node.VersionID == versionID {
			assertions = append(assertions, assertion)
		}
	}
	sort.Slice(assertions, func(i, j int) bool {
		if assertions[i].NodeID != assertions[j].NodeID {
			return assertions[i].NodeID < assertions[j].NodeID
		}
		return assertions[i].ID < assertions[j].ID
	})
	return assertions, nil
}

func (r graphAssertionRepo) Create(_ context.Context, assertion domain.CaseAssertion) (domain.CaseAssertion, error) {
	if _, exists := r.f.assertions[assertion.ID]; exists {
		return domain.CaseAssertion{}, errors.New("graphFixture: duplicate assertion id")
	}
	r.f.assertions[assertion.ID] = assertion
	return assertion, nil
}

func (r graphAssertionRepo) Update(_ context.Context, assertion domain.CaseAssertion) error {
	if _, ok := r.f.assertions[assertion.ID]; !ok {
		return domain.NewNotFound("assertion", assertion.ID)
	}
	r.f.assertions[assertion.ID] = assertion
	return nil
}

func (r graphAssertionRepo) Delete(_ context.Context, nodeID, assertionID int64) error {
	assertion, ok := r.f.assertions[assertionID]
	if !ok || assertion.NodeID != nodeID {
		return domain.NewNotFound("assertion", assertionID)
	}
	delete(r.f.assertions, assertionID)
	return nil
}

func (r graphAssertionRepo) CountByNode(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, assertion := range r.f.assertions {
		if assertion.NodeID == nodeID {
			count++
		}
	}
	return count, nil
}

// graphSourceRepo implements application.SourceRepository over graphFixture.
type graphSourceRepo struct {
	f *graphFixture
}

func (r graphSourceRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseSource, error) {
	sources := make([]domain.CaseSource, 0, len(r.f.sources))
	for _, source := range r.f.sources {
		if source.VersionID == versionID {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources, nil
}

func (r graphSourceRepo) Create(_ context.Context, source domain.CaseSource) (domain.CaseSource, error) {
	if _, exists := r.f.sources[source.ID]; exists {
		return domain.CaseSource{}, errors.New("graphFixture: duplicate source id")
	}
	r.f.sources[source.ID] = source
	return source, nil
}

func (r graphSourceRepo) Delete(_ context.Context, nodeID, sourceID int64) error {
	source, ok := r.f.sources[sourceID]
	if !ok || source.NodeID != nodeID {
		return domain.NewNotFound("source", sourceID)
	}
	delete(r.f.sources, sourceID)
	return nil
}

func (r graphSourceRepo) CountRequirementSources(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, source := range r.f.sources {
		if source.NodeID == nodeID && source.RefKind == domain.RefRequirement {
			count++
		}
	}
	return count, nil
}

// graphBaselineRepo implements application.BaselineRepository over graphFixture.
type graphBaselineRepo struct {
	f *graphFixture
}

func (r graphBaselineRepo) Create(_ context.Context, baseline domain.CaseBaseline) (domain.CaseBaseline, error) {
	copy := baseline
	r.f.baseline = &copy
	return copy, nil
}

func (r graphBaselineRepo) GetByVersion(_ context.Context, versionID int64) (domain.CaseBaseline, error) {
	if r.f.baseline == nil || r.f.baseline.VersionID != versionID {
		return domain.CaseBaseline{}, domain.NewNotFound("baseline", versionID)
	}
	return *r.f.baseline, nil
}

func (r graphBaselineRepo) DeleteByVersion(_ context.Context, versionID int64) error {
	if r.f.baseline != nil && r.f.baseline.VersionID == versionID {
		r.f.baseline = nil
	}
	return nil
}

// graphVersionLifecycle implements application.VersionLifecycle.
type graphVersionLifecycle struct {
	f *graphFixture
}

func (v graphVersionLifecycle) GetVersion(_ context.Context, versionID int64) (application.VersionSnapshot, error) {
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	return v.f.version, nil
}

func (v graphVersionLifecycle) EnterReview(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not used")
}

func (v graphVersionLifecycle) RejectReview(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not used")
}

func (v graphVersionLifecycle) ApproveBaseline(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("not used")
}

// graphRequirementReader implements application.RequirementReader.
type graphRequirementReader struct {
	f *graphFixture
}

func (r graphRequirementReader) ListConfirmed(context.Context, int64) ([]application.RequirementSnapshot, error) {
	return append([]application.RequirementSnapshot(nil), r.f.confirmed...), nil
}

func (r graphRequirementReader) ListRelations(context.Context, int64) ([]application.RelationSnapshot, error) {
	return append([]application.RelationSnapshot(nil), r.f.relations...), nil
}

// graphEventSink records published events.
type graphEventSink struct {
	f *graphFixture
}

func (s graphEventSink) Publish(_ context.Context, event application.Event) error {
	s.f.events = append(s.f.events, event)
	return nil
}

// graphAuditSink records the write-path audit events.
type graphAuditSink struct {
	f *graphFixture
}

func (s graphAuditSink) Record(_ context.Context, event application.AuditEvent) error {
	s.f.audits = append(s.f.audits, event)
	return nil
}

// graphIDGenerator mints deterministic increasing ids.
type graphIDGenerator struct {
	f *graphFixture
}

func (g graphIDGenerator) Next() int64 {
	return g.f.id()
}

// graphClock is a fixed test clock.
type graphClock struct {
	f *graphFixture
}

func (c graphClock) Now() time.Time {
	return c.f.now
}

// graphAnchorGateway is a recording in-memory application.CodeAnchorGateway.
// DeleteNode must list the anchors of every doomed node and delete each one;
// the recorded calls prove the cleanup ran after the node transaction.
type graphAnchorGateway struct {
	byNode    map[int64][]application.Anchor
	listed    []int64
	deleted   []int64
	listErr   error
	deleteErr error
}

func newGraphAnchorGateway() *graphAnchorGateway {
	return &graphAnchorGateway{byNode: make(map[int64][]application.Anchor)}
}

func (g *graphAnchorGateway) add(nodeID, anchorID int64) {
	g.byNode[nodeID] = append(g.byNode[nodeID], application.Anchor{
		ID:         anchorID,
		VersionID:  1,
		SubjectID:  nodeID,
		FilePath:   "src/order/submit.ts",
		Confidence: domain.ConfidenceHigh,
	})
}

func (g *graphAnchorGateway) Create(context.Context, int64, int64, application.AnchorParams) (application.Anchor, error) {
	return application.Anchor{}, errors.New("graphAnchorGateway: Create is not used")
}

func (g *graphAnchorGateway) List(_ context.Context, versionID, nodeID int64) ([]application.Anchor, error) {
	g.listed = append(g.listed, nodeID)
	if g.listErr != nil {
		return nil, g.listErr
	}
	return append([]application.Anchor(nil), g.byNode[nodeID]...), nil
}

func (g *graphAnchorGateway) Delete(_ context.Context, versionID, anchorID int64) error {
	g.deleted = append(g.deleted, anchorID)
	if g.deleteErr != nil {
		return g.deleteErr
	}
	for nodeID, list := range g.byNode {
		kept := make([]application.Anchor, 0, len(list))
		for _, anchor := range list {
			if anchor.ID != anchorID {
				kept = append(kept, anchor)
			}
		}
		g.byNode[nodeID] = kept
	}
	return nil
}

// graphTx implements application.GraphTx over the same fixture.
type graphTx struct {
	f *graphFixture
}

func (tx graphTx) Nodes() application.NodeRepository           { return graphNodeRepo{tx.f} }
func (tx graphTx) Steps() application.StepRepository           { return graphStepRepo{tx.f} }
func (tx graphTx) Assertions() application.AssertionRepository { return graphAssertionRepo{tx.f} }
func (tx graphTx) Sources() application.SourceRepository       { return graphSourceRepo{tx.f} }
func (tx graphTx) Reviews() application.ReviewRepository       { return nil }
func (tx graphTx) Findings() application.FindingRepository     { return nil }
func (tx graphTx) Baselines() application.BaselineRepository   { return graphBaselineRepo{tx.f} }
func (tx graphTx) Runs() application.RunRepository             { return nil }

// graphSnapshot is a copy of the fixture tables. It lets a test replay a
// transaction that began before another write committed.
type graphSnapshot struct {
	nodes      map[int64]domain.CaseNode
	steps      map[int64]domain.CaseStep
	assertions map[int64]domain.CaseAssertion
	sources    map[int64]domain.CaseSource
}

func (f *graphFixture) snapshot() graphSnapshot {
	snapshot := graphSnapshot{
		nodes:      make(map[int64]domain.CaseNode, len(f.nodes)),
		steps:      make(map[int64]domain.CaseStep, len(f.steps)),
		assertions: make(map[int64]domain.CaseAssertion, len(f.assertions)),
		sources:    make(map[int64]domain.CaseSource, len(f.sources)),
	}
	for id, node := range f.nodes {
		snapshot.nodes[id] = node
	}
	for id, step := range f.steps {
		snapshot.steps[id] = step
	}
	for id, assertion := range f.assertions {
		snapshot.assertions[id] = assertion
	}
	for id, source := range f.sources {
		snapshot.sources[id] = source
	}
	return snapshot
}

// restore rolls the fixture tables back to a snapshot, mimicking the rollback
// of a failed transaction.
func (f *graphFixture) restore(snapshot graphSnapshot) {
	f.nodes = snapshot.nodes
	f.steps = snapshot.steps
	f.assertions = snapshot.assertions
	f.sources = snapshot.sources
}

// staleNodeRepo lists the nodes a transaction saw when it started while its
// writes hit the live fixture (like a READ COMMITTED transaction that began
// before a concurrent commit).
type staleNodeRepo struct {
	graphNodeRepo
	nodes map[int64]domain.CaseNode
}

func (r staleNodeRepo) Get(_ context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
	node, ok := r.nodes[nodeID]
	if !ok || node.VersionID != versionID {
		return domain.CaseNode{}, domain.NewNotFound("node", nodeID)
	}
	return node, nil
}

func (r staleNodeRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseNode, error) {
	nodes := make([]domain.CaseNode, 0, len(r.nodes))
	for _, node := range r.nodes {
		if node.VersionID == versionID {
			nodes = append(nodes, node)
		}
	}
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].SortOrder != nodes[j].SortOrder {
			return nodes[i].SortOrder < nodes[j].SortOrder
		}
		return nodes[i].ID < nodes[j].ID
	})
	return nodes, nil
}

// staleSourceRepo lists the source rows a transaction saw when it started while
// its writes hit the live fixture.
type staleSourceRepo struct {
	graphSourceRepo
	sources map[int64]domain.CaseSource
}

func (r staleSourceRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseSource, error) {
	sources := make([]domain.CaseSource, 0, len(r.sources))
	for _, source := range r.sources {
		if source.VersionID == versionID {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources, nil
}

// staleGraphTx reads the graph from a snapshot while writes hit the live
// fixture.
type staleGraphTx struct {
	graphTx
	snapshot graphSnapshot
}

func (tx staleGraphTx) Nodes() application.NodeRepository {
	return staleNodeRepo{graphNodeRepo: graphNodeRepo{tx.f}, nodes: tx.snapshot.nodes}
}

func (tx staleGraphTx) Sources() application.SourceRepository {
	return staleSourceRepo{graphSourceRepo: graphSourceRepo{tx.f}, sources: tx.snapshot.sources}
}

// concurrentSourceDeleteGraph deterministically simulates two overlapping
// source deletes: the first transaction runs against the live fixture while its
// pre-write snapshot is kept, and the second transaction replays that snapshot
// (old node revision, old source rows) and rolls the fixture back when it fails.
type concurrentSourceDeleteGraph struct {
	f        *graphFixture
	snapshot graphSnapshot
	calls    int
}

func (g *concurrentSourceDeleteGraph) InTx(_ context.Context, fn func(application.GraphTx) error) error {
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

func graphInt64Ptr(value int64) *int64    { return &value }
func graphStringPtr(value string) *string { return &value }

func requireDomainError(t *testing.T, err error, kind, reason string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s/%s, got nil", kind, reason)
	}
	var domainErr *domain.Error
	if !errors.As(err, &domainErr) {
		t.Fatalf("expected *domain.Error %s/%s, got %T: %v", kind, reason, err, err)
	}
	if domainErr.Kind != kind || domainErr.Reason != reason {
		t.Fatalf("expected error %s/%s, got %s/%s (%v)", kind, reason, domainErr.Kind, domainErr.Reason, err)
	}
}

func requireEvent(t *testing.T, fixture *graphFixture, index int, eventType string) application.Event {
	t.Helper()
	if len(fixture.events) <= index {
		t.Fatalf("expected at least %d event(s), got %d", index+1, len(fixture.events))
	}
	event := fixture.events[index]
	if event.Type != eventType {
		t.Fatalf("event %d type = %q, want %q", index, event.Type, eventType)
	}
	return event
}

// TestCreateCaseRequiresConfirmedRequirement covers the mandate that a case can
// only be created against clarified requirements of the version; a missing or
// unconfirmed requirement is rejected before any row is written.
func TestCreateCaseRequiresConfirmedRequirement(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Status: "clarified"}}
	service := fixture.service()

	cases := []struct {
		name string
		ids  []int64
	}{
		{name: "missing requirement ids", ids: nil},
		{name: "requirement from another version", ids: []int64{999}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
				Kind:           domain.NodeKindCase,
				Title:          "new case",
				RequirementIDs: testCase.ids,
			})
			requireDomainError(t, err, domain.KindInvalid, "unconfirmed_requirements")
		})
	}
	if len(fixture.nodes) != 0 {
		t.Fatalf("rejected create wrote %d node(s)", len(fixture.nodes))
	}
	if len(fixture.sources) != 0 {
		t.Fatalf("rejected create wrote %d source(s)", len(fixture.sources))
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected create published %d event(s)", len(fixture.events))
	}
}

// TestCreateCaseStoresRequirementSource checks the happy path: a case node and
// its requirement source are created together, revision 1, and one
// case.created event is published after the commit.
func TestCreateCaseStoresRequirementSource(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Status: "clarified"}}
	service := fixture.service()

	created, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:           domain.NodeKindCase,
		Title:          "checkout works",
		Priority:       domain.PriorityP1,
		RequirementIDs: []int64{7},
	})
	if err != nil {
		t.Fatalf("create case: %v", err)
	}
	if created.Revision != 1 || created.Status != domain.NodeStatusDraft {
		t.Fatalf("created node revision/status = %d/%s, want 1/draft", created.Revision, created.Status)
	}
	if created.Source != domain.NodeSourceUserInput {
		t.Fatalf("created source = %q, want user_input", created.Source)
	}
	sources := fixture.requirementSourcesOfNode(created.ID)
	if len(sources) != 1 || sources[0].RefID == nil || *sources[0].RefID != 7 {
		t.Fatalf("requirement sources = %+v, want one for requirement 7", sources)
	}
	event := requireEvent(t, fixture, 0, domain.EventCaseCreated)
	if event.VersionID != 1 || event.ActorID != 42 {
		t.Fatalf("event version/actor = %d/%d, want 1/42", event.VersionID, event.ActorID)
	}
	if event.Detail["kind"] != domain.NodeKindCase {
		t.Fatalf("event detail kind = %v, want case", event.Detail["kind"])
	}
}

// TestCreateNodeAppendsSortOrder checks that new siblings are appended after
// the existing ones (spec §6.1 rule 5).
func TestCreateNodeAppendsSortOrder(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	service := fixture.service()

	first, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:  domain.NodeKindScenario,
		Title: "first scenario",
	})
	if err != nil {
		t.Fatalf("create first scenario: %v", err)
	}
	second, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:  domain.NodeKindScenario,
		Title: "second scenario",
	})
	if err != nil {
		t.Fatalf("create second scenario: %v", err)
	}
	if first.SortOrder != 0 || second.SortOrder != 1 {
		t.Fatalf("sort orders = %d/%d, want 0/1", first.SortOrder, second.SortOrder)
	}
	requireEvent(t, fixture, 1, domain.EventCaseCreated)
}

// TestUpdateNodeStaleRevisionConflict covers OCC on a node edit: a stale
// revision is a case_revision_conflict and writes nothing.
func TestUpdateNodeStaleRevisionConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	node := fixture.seedCase(11, nil, 7)
	stale := node
	stale.Revision = 2
	fixture.nodes[11] = stale
	service := fixture.service()

	_, err := service.UpdateNode(ctx, 1, 11, 42, application.UpdateNodeCommand{
		Revision: 1,
		Title:    graphStringPtr("stale edit"),
	})
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	if fixture.nodes[11].Title != "case" {
		t.Fatalf("stale edit changed the node title to %q", fixture.nodes[11].Title)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("stale edit changed the revision to %d", fixture.nodes[11].Revision)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("stale edit published %d event(s)", len(fixture.events))
	}
}

// TestUpdateNodeAppliesFields checks a successful node edit bumps the revision
// and publishes case.updated.
func TestUpdateNodeAppliesFields(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	node := fixture.seedCase(11, nil, 7)
	node.Revision = 2
	fixture.nodes[11] = node
	service := fixture.service()

	updated, err := service.UpdateNode(ctx, 1, 11, 42, application.UpdateNodeCommand{
		Revision: 2,
		Title:    graphStringPtr("renamed"),
		Priority: graphStringPtr(domain.PriorityP0),
	})
	if err != nil {
		t.Fatalf("update node: %v", err)
	}
	if updated.Revision != 3 {
		t.Fatalf("updated revision = %d, want 3", updated.Revision)
	}
	if fixture.nodes[11].Title != "renamed" || fixture.nodes[11].Priority != domain.PriorityP0 {
		t.Fatalf("stored node = %+v, want renamed/P0", fixture.nodes[11])
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestUpdateNodeReplacesRequirementSources checks that requirementIds replaces
// the requirement source set of a case while keeping the other sources.
func TestUpdateNodeReplacesRequirementSources(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: 1, Status: "clarified"},
		{ID: 8, VersionID: 1, Status: "clarified"},
	}
	service := fixture.service()

	replacement := []int64{8}
	if _, err := service.UpdateNode(ctx, 1, 11, 42, application.UpdateNodeCommand{
		Revision:       1,
		RequirementIDs: &replacement,
	}); err != nil {
		t.Fatalf("update node requirement ids: %v", err)
	}
	sources := fixture.requirementSourcesOfNode(11)
	if len(sources) != 1 || sources[0].RefID == nil || *sources[0].RefID != 8 {
		t.Fatalf("requirement sources = %+v, want only requirement 8", sources)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestDeleteLastRequirementSourceRejected covers spec §6.2 rule 4: deleting the
// last requirement source would leave the case without coverage and is a 409
// invalid_case_tree with no write.
func TestDeleteLastRequirementSourceRejected(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()
	sourceID := fixture.requirementSourcesOfNode(11)[0].ID

	err := service.DeleteSource(ctx, 1, sourceID, 42)
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.requirementSourcesOfNode(11)) != 1 {
		t.Fatal("rejected delete removed the last requirement source")
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected delete published %d event(s)", len(fixture.events))
	}
}

// TestDeleteSourceKeepsCaseWhenOtherRequirementRemains checks the happy path of
// a source delete when another requirement source still covers the case.
func TestDeleteSourceKeepsCaseWhenOtherRequirementRemains(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	extra := domain.CaseSource{
		ID:         900,
		VersionID:  1,
		NodeID:     11,
		RefKind:    domain.RefRequirement,
		RefID:      graphInt64Ptr(8),
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
	}
	fixture.sources[extra.ID] = extra
	service := fixture.service()
	sources := fixture.requirementSourcesOfNode(11)
	if len(sources) != 2 || sources[0].ID != 113 || sources[1].ID != 900 {
		t.Fatalf("requirement sources = %+v, want source 113 (requirement 7) then 900 (requirement 8)", sources)
	}

	if err := service.DeleteSource(ctx, 1, sources[0].ID, 42); err != nil {
		t.Fatalf("delete source: %v", err)
	}
	if len(fixture.sources) != 1 || fixture.sources[900].ID != 900 {
		t.Fatalf("sources after delete = %+v, want only source 900", fixture.sources)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision after source delete = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestDeleteLastAssertionRejected covers spec §7.2 endpoint 12: the last
// assertion of a case cannot be deleted (invalid_case_tree, no write).
func TestDeleteLastAssertionRejected(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()
	assertions := fixture.assertionsOfNode(11)
	if len(assertions) != 1 {
		t.Fatalf("fixture has %d assertions, want 1", len(assertions))
	}

	err := service.DeleteAssertion(ctx, 1, assertions[0].ID, 42, 1)
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.assertionsOfNode(11)) != 1 {
		t.Fatal("rejected delete removed the last assertion")
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected delete published %d event(s)", len(fixture.events))
	}
}

// TestAddAssertionAppendsAndBumpsRevision covers the assertion create happy
// path: the child row is stored and the owning node revision is bumped in the
// same transaction.
func TestAddAssertionAppendsAndBumpsRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()

	created, err := service.AddAssertion(ctx, 1, 11, 42, application.AddAssertionCommand{
		NodeRevision: 1,
		StepID:       graphInt64Ptr(111),
		Kind:         domain.AssertionKindAPI,
		Target:       "/api/orders",
		Expression:   "status == 200",
		Expected:     "200",
	})
	if err != nil {
		t.Fatalf("add assertion: %v", err)
	}
	if created.NodeID != 11 || created.Kind != domain.AssertionKindAPI || created.StepID == nil || *created.StepID != 111 {
		t.Fatalf("created assertion = %+v, want node 11 api assertion on step 111", created)
	}
	if len(fixture.assertionsOfNode(11)) != 2 {
		t.Fatalf("assertions = %d, want 2", len(fixture.assertionsOfNode(11)))
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestDeleteAssertionKeepsRemainingAssertion covers the assertion delete happy
// path: with two assertions one is removed and the other remains.
func TestDeleteAssertionKeepsRemainingAssertion(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	second := domain.CaseAssertion{
		ID:         200,
		NodeID:     11,
		Kind:       domain.AssertionKindText,
		Expression: "second assertion",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	fixture.assertions[second.ID] = second
	service := fixture.service()

	if err := service.DeleteAssertion(ctx, 1, second.ID, 42, 1); err != nil {
		t.Fatalf("delete assertion: %v", err)
	}
	remaining := fixture.assertionsOfNode(11)
	if len(remaining) != 1 || remaining[0].ID != 112 {
		t.Fatalf("assertions after delete = %+v, want only assertion 112", remaining)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestScenarioCannotHaveSteps covers spec §6.1 rule 2: a scenario must never own
// steps.
func TestScenarioCannotHaveSteps(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedScenario(10, nil)
	service := fixture.service()

	_, err := service.AddStep(ctx, 1, 10, 42, application.AddStepCommand{
		NodeRevision: 1,
		Action:       "not allowed",
	})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.steps) != 0 {
		t.Fatalf("rejected add step wrote %d step(s)", len(fixture.steps))
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected add step published %d event(s)", len(fixture.events))
	}
}

// TestSourcesOnlyOnCaseNodes covers spec §6.2 rule 2: scenarios never own
// sources and a scenario create cannot carry requirementIds.
func TestSourcesOnlyOnCaseNodes(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedScenario(10, nil)
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Status: "clarified"}}
	service := fixture.service()

	if _, err := service.AddSource(ctx, 1, 10, 42, application.AddSourceCommand{
		RefKind: domain.RefRequirement,
		RefID:   graphInt64Ptr(7),
	}); err == nil {
		t.Fatal("expected adding a source to a scenario to fail")
	} else {
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	}
	if len(fixture.sources) != 0 {
		t.Fatalf("rejected add source wrote %d source(s)", len(fixture.sources))
	}

	if _, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:           domain.NodeKindScenario,
		Title:          "scenario with requirements",
		RequirementIDs: []int64{7},
	}); err == nil {
		t.Fatal("expected a scenario create with requirementIds to fail")
	} else {
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected writes published %d event(s)", len(fixture.events))
	}
}

// TestFrozenVersionRejectsWrites covers spec §6.4 rule 3: once a baseline
// exists every graph write is a baseline_frozen 409, including when the version
// has already moved to executing (the post-approve state).
func TestFrozenVersionRejectsWrites(t *testing.T) {
	ctx := context.Background()
	for _, status := range []string{"designing", "executing"} {
		t.Run(status, func(t *testing.T) {
			fixture := newGraphFixture()
			fixture.version.Status = status
			fixture.seedCase(11, nil, 7)
			fixture.baseline = &domain.CaseBaseline{ID: 99, VersionID: 1, GraphHash: "abc"}
			service := fixture.service()
			scenarioID := int64(0)
			nodeID := int64(11)
			steps := fixture.stepsOfNode(nodeID)
			assertions := fixture.assertionsOfNode(nodeID)
			sources := fixture.requirementSourcesOfNode(nodeID)
			emptyIDs := []int64{}

			writes := map[string]func() error{
				"create node": func() error {
					_, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{Kind: domain.NodeKindScenario, Title: "x"})
					return err
				},
				"update node": func() error {
					_, err := service.UpdateNode(ctx, 1, nodeID, 42, application.UpdateNodeCommand{Revision: 1, Title: graphStringPtr("x")})
					return err
				},
				"delete node": func() error {
					return service.DeleteNode(ctx, 1, nodeID, 42, 1)
				},
				"add step": func() error {
					_, err := service.AddStep(ctx, 1, nodeID, 42, application.AddStepCommand{NodeRevision: 1, Action: "x"})
					return err
				},
				"update step": func() error {
					_, err := service.UpdateStep(ctx, 1, steps[0].ID, 42, application.UpdateStepCommand{NodeRevision: 1, Action: graphStringPtr("x")})
					return err
				},
				"delete step": func() error {
					return service.DeleteStep(ctx, 1, steps[0].ID, 42, 1)
				},
				"reorder steps": func() error {
					return service.ReorderSteps(ctx, 1, nodeID, 42, application.ReorderStepsCommand{NodeRevision: 1, StepIDs: emptyIDs})
				},
				"add assertion": func() error {
					_, err := service.AddAssertion(ctx, 1, nodeID, 42, application.AddAssertionCommand{NodeRevision: 1, Kind: domain.AssertionKindUI, Expression: "x"})
					return err
				},
				"update assertion": func() error {
					_, err := service.UpdateAssertion(ctx, 1, assertions[0].ID, 42, application.UpdateAssertionCommand{NodeRevision: 1, Expression: graphStringPtr("x")})
					return err
				},
				"delete assertion": func() error {
					return service.DeleteAssertion(ctx, 1, assertions[0].ID, 42, 1)
				},
				"add source": func() error {
					_, err := service.AddSource(ctx, 1, nodeID, 42, application.AddSourceCommand{RefKind: domain.RefRequirement, RefID: graphInt64Ptr(7)})
					return err
				},
				"delete source": func() error {
					return service.DeleteSource(ctx, 1, sources[0].ID, 42)
				},
				"reorder": func() error {
					return service.Reorder(ctx, 1, 42, application.ReorderCommand{ParentID: &scenarioID, NodeIDs: nil})
				},
			}
			for name, write := range writes {
				t.Run(name, func(t *testing.T) {
					requireDomainError(t, write(), domain.KindStateConflict, "baseline_frozen")
				})
			}
			if len(fixture.events) != 0 {
				t.Fatalf("frozen writes published %d event(s)", len(fixture.events))
			}
			if fixture.nodes[11].Revision != 1 {
				t.Fatalf("frozen write bumped the node revision to %d", fixture.nodes[11].Revision)
			}
		})
	}
}

// TestExecutingVersionRejectsWrites covers the version state gate: outside
// designing/reviewing every write is version_state_conflict.
func TestExecutingVersionRejectsWrites(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.version.Status = "executing"
	service := fixture.service()

	_, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
		Kind:  domain.NodeKindScenario,
		Title: "not allowed",
	})
	requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	if len(fixture.nodes) != 0 || len(fixture.events) != 0 {
		t.Fatalf("rejected write stored %d node(s) / %d event(s)", len(fixture.nodes), len(fixture.events))
	}
}

// TestReorderRequiresCompleteChildSet covers spec §7.2 endpoint 5: the nodeIds
// list must contain exactly the children of the parent, then the order is
// applied.
func TestReorderRequiresCompleteChildSet(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	parentID := int64(10)
	fixture.seedScenario(parentID, nil)
	fixture.seedCase(11, &parentID, 7)
	fixture.seedCase(12, &parentID, 7)
	service := fixture.service()

	invalid := [][]int64{
		{11},
		{11, 11},
		{11, 99},
	}
	for _, ids := range invalid {
		err := service.Reorder(ctx, 1, 42, application.ReorderCommand{ParentID: &parentID, NodeIDs: ids})
		requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected reorder published %d event(s)", len(fixture.events))
	}

	if err := service.Reorder(ctx, 1, 42, application.ReorderCommand{
		ParentID: &parentID,
		NodeIDs:  []int64{12, 11},
	}); err != nil {
		t.Fatalf("reorder: %v", err)
	}
	if fixture.nodes[12].SortOrder >= fixture.nodes[11].SortOrder {
		t.Fatalf("sort orders = 12:%d 11:%d, want 12 before 11",
			fixture.nodes[12].SortOrder, fixture.nodes[11].SortOrder)
	}
	if len(fixture.nodes) != 3 {
		t.Fatalf("nodes after reorder = %d, want 3", len(fixture.nodes))
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestReorderStepsRequiresCompleteSet covers spec §7.2 endpoint 9 and the step
// ordering OCC: the stepIds list must contain every step of the case.
func TestReorderStepsRequiresCompleteSet(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	second := domain.CaseStep{
		ID:        112,
		NodeID:    11,
		Seq:       2,
		Action:    "second step",
		Source:    domain.StepSourceUserInput,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	fixture.steps[second.ID] = second
	service := fixture.service()
	first := fixture.stepsOfNode(11)[0]

	err := service.ReorderSteps(ctx, 1, 11, 42, application.ReorderStepsCommand{
		NodeRevision: 1,
		StepIDs:      []int64{first.ID},
	})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.events) != 0 {
		t.Fatalf("rejected step reorder published %d event(s)", len(fixture.events))
	}

	if err := service.ReorderSteps(ctx, 1, 11, 42, application.ReorderStepsCommand{
		NodeRevision: 1,
		StepIDs:      []int64{second.ID, first.ID},
	}); err != nil {
		t.Fatalf("reorder steps: %v", err)
	}
	steps := fixture.stepsOfNode(11)
	if len(steps) != 2 || steps[0].ID != second.ID || steps[0].Seq != 1 || steps[1].ID != first.ID || steps[1].Seq != 2 {
		t.Fatalf("steps after reorder = %+v, want step 111 then 111's predecessor", steps)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision after reorder = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestAddStepAppendsAndInsertsAfterSeq covers the step create endpoint: appended
// by default, insertable after an existing seq, and the sequence is renumbered.
func TestAddStepAppendsAndInsertsAfterSeq(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()
	first := fixture.stepsOfNode(11)[0]

	appended, err := service.AddStep(ctx, 1, 11, 42, application.AddStepCommand{
		NodeRevision: 1,
		Action:       "appended",
	})
	if err != nil {
		t.Fatalf("append step: %v", err)
	}
	if appended.Seq != 2 {
		t.Fatalf("appended seq = %d, want 2", appended.Seq)
	}

	inserted, err := service.AddStep(ctx, 1, 11, 42, application.AddStepCommand{
		NodeRevision: 2,
		Action:       "inserted",
		AfterSeq:     graphIntPtr(1),
	})
	if err != nil {
		t.Fatalf("insert step: %v", err)
	}
	if inserted.Seq != 2 {
		t.Fatalf("inserted seq = %d, want 2", inserted.Seq)
	}
	steps := fixture.stepsOfNode(11)
	if len(steps) != 3 {
		t.Fatalf("steps = %d, want 3", len(steps))
	}
	if steps[0].ID != first.ID || steps[1].ID != inserted.ID || steps[2].ID != appended.ID {
		t.Fatalf("step order = %+v, want first, inserted, appended", steps)
	}
	for index, step := range steps {
		if step.Seq != index+1 {
			t.Fatalf("step %d seq = %d, want %d", step.ID, step.Seq, index+1)
		}
	}
	if fixture.nodes[11].Revision != 3 {
		t.Fatalf("node revision = %d, want 3", fixture.nodes[11].Revision)
	}
	if len(fixture.events) != 2 {
		t.Fatalf("events = %d, want 2", len(fixture.events))
	}
}

// TestAddStepStaleNodeRevisionConflict covers the step create OCC token: a
// stale nodeRevision is a case_revision_conflict and writes nothing.
func TestAddStepStaleNodeRevisionConflict(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()

	_, err := service.AddStep(ctx, 1, 11, 42, application.AddStepCommand{
		NodeRevision: 2,
		Action:       "stale append",
	})
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	if len(fixture.stepsOfNode(11)) != 1 {
		t.Fatal("stale step add wrote a step")
	}
	if fixture.nodes[11].Revision != 1 {
		t.Fatalf("stale step add bumped the node revision to %d", fixture.nodes[11].Revision)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("stale step add published %d event(s)", len(fixture.events))
	}
}

// TestAddStepEnforcesThirtyStepCap covers the 1..30 case_step cap: the 30th
// step is accepted and the 31st is invalid_case_tree with no write.
func TestAddStepEnforcesThirtyStepCap(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	for index := 0; index < 28; index++ {
		step := domain.CaseStep{
			ID:        int64(200 + index),
			NodeID:    11,
			Seq:       index + 2,
			Action:    "seeded step",
			Source:    domain.StepSourceUserInput,
			CreatedAt: fixture.now,
			UpdatedAt: fixture.now,
		}
		fixture.steps[step.ID] = step
	}
	if len(fixture.stepsOfNode(11)) != domain.MaxStepsPerCase-1 {
		t.Fatalf("seeded steps = %d, want %d", len(fixture.stepsOfNode(11)), domain.MaxStepsPerCase-1)
	}
	service := fixture.service()

	created, err := service.AddStep(ctx, 1, 11, 42, application.AddStepCommand{
		NodeRevision: 1,
		Action:       "the 30th step",
	})
	if err != nil {
		t.Fatalf("add the 30th step: %v", err)
	}
	if created.Seq != domain.MaxStepsPerCase {
		t.Fatalf("30th step seq = %d, want %d", created.Seq, domain.MaxStepsPerCase)
	}
	if len(fixture.stepsOfNode(11)) != domain.MaxStepsPerCase {
		t.Fatalf("steps = %d, want %d", len(fixture.stepsOfNode(11)), domain.MaxStepsPerCase)
	}

	_, err = service.AddStep(ctx, 1, 11, 42, application.AddStepCommand{
		NodeRevision: 2,
		Action:       "the 31st step",
	})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.stepsOfNode(11)) != domain.MaxStepsPerCase {
		t.Fatalf("rejected 31st step wrote a step")
	}
	if len(fixture.events) != 1 {
		t.Fatalf("step writes published %d event(s), want only the committed one", len(fixture.events))
	}
}

// TestUpdateStepUnderNodeRevision checks the step edit OCC: a stale
// nodeRevision conflicts, a matching one applies and bumps the node revision.
func TestUpdateStepUnderNodeRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	node := fixture.seedCase(11, nil, 7)
	node.Revision = 2
	fixture.nodes[11] = node
	service := fixture.service()
	step := fixture.stepsOfNode(11)[0]

	_, err := service.UpdateStep(ctx, 1, step.ID, 42, application.UpdateStepCommand{
		NodeRevision: 1,
		Action:       graphStringPtr("stale"),
	})
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	if fixture.steps[step.ID].Action != "open the page" {
		t.Fatal("stale step edit changed the step")
	}

	updated, err := service.UpdateStep(ctx, 1, step.ID, 42, application.UpdateStepCommand{
		NodeRevision: 2,
		Action:       graphStringPtr("updated action"),
		Expected:     graphStringPtr("updated expected"),
	})
	if err != nil {
		t.Fatalf("update step: %v", err)
	}
	if updated.Action != "updated action" || fixture.steps[step.ID].Expected != "updated expected" {
		t.Fatalf("stored step = %+v, want updated action/expected", fixture.steps[step.ID])
	}
	if fixture.nodes[11].Revision != 3 {
		t.Fatalf("node revision = %d, want 3", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestDeleteStepRenumbersAndRemovesItsAssertions covers spec §7.2 endpoint 8:
// deleting a step renumbers the remaining steps and deletes the assertions tied
// to that step.
func TestDeleteStepRenumbersAndRemovesItsAssertions(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	first := fixture.stepsOfNode(11)[0]
	second := domain.CaseStep{
		ID:        112,
		NodeID:    11,
		Seq:       2,
		Action:    "second step",
		Source:    domain.StepSourceUserInput,
		CreatedAt: fixture.now,
		UpdatedAt: fixture.now,
	}
	fixture.steps[second.ID] = second
	secondAssertion := domain.CaseAssertion{
		ID:         200,
		NodeID:     11,
		StepID:     graphInt64Ptr(second.ID),
		Kind:       domain.AssertionKindText,
		Expression: "second assertion",
		Source:     domain.NodeSourceUserInput,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	fixture.assertions[secondAssertion.ID] = secondAssertion
	service := fixture.service()

	if err := service.DeleteStep(ctx, 1, second.ID, 42, 1); err != nil {
		t.Fatalf("delete step: %v", err)
	}
	steps := fixture.stepsOfNode(11)
	if len(steps) != 1 || steps[0].ID != first.ID || steps[0].Seq != 1 {
		t.Fatalf("steps after delete = %+v, want only the first step at seq 1", steps)
	}
	if _, ok := fixture.assertions[secondAssertion.ID]; ok {
		t.Fatal("assertion of the deleted step still exists")
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestAssertionStepMustBelongToNode covers spec §6.1 rule 4: an assertion can
// only reference a step of the same case node.
func TestAssertionStepMustBelongToNode(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.seedCase(12, nil, 7)
	service := fixture.service()
	foreignStep := fixture.stepsOfNode(12)[0]

	_, err := service.AddAssertion(ctx, 1, 11, 42, application.AddAssertionCommand{
		NodeRevision: 1,
		StepID:       graphInt64Ptr(foreignStep.ID),
		Kind:         domain.AssertionKindAPI,
		Expression:   "must not attach",
	})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.assertionsOfNode(11)) != 1 {
		t.Fatal("rejected assertion was written")
	}
	if fixture.nodes[11].Revision != 1 {
		t.Fatalf("rejected assertion bumped the node revision to %d", fixture.nodes[11].Revision)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected assertion published %d event(s)", len(fixture.events))
	}
}

// TestUpdateAssertionUnderNodeRevision checks a successful assertion edit and
// the node revision bump that guards it.
func TestUpdateAssertionUnderNodeRevision(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	node := fixture.seedCase(11, nil, 7)
	node.Revision = 2
	fixture.nodes[11] = node
	service := fixture.service()
	assertion := fixture.assertionsOfNode(11)[0]

	updated, err := service.UpdateAssertion(ctx, 1, assertion.ID, 42, application.UpdateAssertionCommand{
		NodeRevision: 2,
		Kind:         graphStringPtr(domain.AssertionKindAPI),
		Expression:   graphStringPtr("updated expression"),
	})
	if err != nil {
		t.Fatalf("update assertion: %v", err)
	}
	if updated.Kind != domain.AssertionKindAPI || fixture.assertions[assertion.ID].Expression != "updated expression" {
		t.Fatalf("stored assertion = %+v, want updated kind/expression", fixture.assertions[assertion.ID])
	}
	if fixture.nodes[11].Revision != 3 {
		t.Fatalf("node revision = %d, want 3", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestAddSourceRequiresConfirmedRequirement covers spec §6.2 rule 1 on the
// source endpoint: a requirement source must reference a clarified requirement
// of the version.
func TestAddSourceRequiresConfirmedRequirement(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: 1, Status: "clarified"},
		{ID: 8, VersionID: 1, Status: "clarified"},
	}
	service := fixture.service()

	_, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
		RefKind: domain.RefRequirement,
		RefID:   graphInt64Ptr(999),
	})
	requireDomainError(t, err, domain.KindInvalid, "unconfirmed_requirements")
	if len(fixture.sources) != 1 {
		t.Fatalf("rejected add source wrote a source: %+v", fixture.sources)
	}

	created, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
		RefKind: domain.RefRequirement,
		RefID:   graphInt64Ptr(8),
	})
	if err != nil {
		t.Fatalf("add confirmed requirement source: %v", err)
	}
	if created.RefID == nil || *created.RefID != 8 {
		t.Fatalf("created source = %+v, want requirement 8", created)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision after source add = %d, want 2", fixture.nodes[11].Revision)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestAddSourceRequiresRefIDForNonManualKinds covers the case_source CHECK
// (ref_kind='manual' OR ref_id IS NOT NULL): requirement, app_model and ai_run
// sources must carry a refId, only a manual source may be refKey-only, and a
// missing refId is a domain invalid error before any write.
func TestAddSourceRequiresRefIDForNonManualKinds(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: 1, Status: "clarified"}}
	service := fixture.service()

	for _, refKind := range []string{domain.RefRequirement, domain.RefAppModel, domain.RefAIRun} {
		t.Run(refKind, func(t *testing.T) {
			_, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
				RefKind: refKind,
				RefKey:  "some-ref-key",
			})
			requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
		})
	}
	if len(fixture.sources) != 1 {
		t.Fatalf("rejected add source wrote sources: %+v", fixture.sources)
	}
	if len(fixture.events) != 0 {
		t.Fatalf("rejected add source published %d event(s)", len(fixture.events))
	}

	created, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
		RefKind: domain.RefManual,
		RefKey:  "manual evidence",
	})
	if err != nil {
		t.Fatalf("add refKey-only manual source: %v", err)
	}
	if created.RefID != nil || created.RefKey != "manual evidence" {
		t.Fatalf("manual source = %+v, want a nil refId and refKey 'manual evidence'", created)
	}
	requireEvent(t, fixture, 0, domain.EventCaseUpdated)
}

// TestValidateSourceStateRequiresRefID covers the same refId rule on the write
// path: a stored app_model source without a refId is rejected by the source
// validation before any graph write.
func TestValidateSourceStateRequiresRefID(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.sources[900] = domain.CaseSource{
		ID:         900,
		VersionID:  1,
		NodeID:     11,
		RefKind:    domain.RefAppModel,
		RefKey:     "app-model",
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	service := fixture.service()

	_, err := service.AddAssertion(ctx, 1, 11, 42, application.AddAssertionCommand{
		NodeRevision: 1,
		Kind:         domain.AssertionKindUI,
		Expression:   "must not attach",
	})
	requireDomainError(t, err, domain.KindInvalid, "invalid_case_tree")
	if len(fixture.assertions) != 1 {
		t.Fatalf("rejected write stored %d assertion(s), want 1", len(fixture.assertions))
	}

	fixture.sources[900] = domain.CaseSource{
		ID:         900,
		VersionID:  1,
		NodeID:     11,
		RefKind:    domain.RefManual,
		RefKey:     "app-model",
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	if _, err := service.AddAssertion(ctx, 1, 11, 42, application.AddAssertionCommand{
		NodeRevision: 1,
		Kind:         domain.AssertionKindUI,
		Expression:   "refKey-only manual source is allowed",
	}); err != nil {
		t.Fatalf("add assertion with a refKey-only manual source: %v", err)
	}
}

// TestAddSourceRejectsDuplicateSource covers the uk_case_source_ref key: a
// duplicate (refKind, refId, refKey) on the same node is duplicate_case_source
// (409) rejected before the insert.
func TestAddSourceRejectsDuplicateSource(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 7, VersionID: 1, Status: "clarified"},
		{ID: 8, VersionID: 1, Status: "clarified"},
	}
	service := fixture.service()

	_, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
		RefKind: domain.RefRequirement,
		RefID:   graphInt64Ptr(7),
	})
	requireDomainError(t, err, domain.KindConflict, "duplicate_case_source")
	if len(fixture.sources) != 1 {
		t.Fatalf("rejected duplicate wrote %d source(s), want 1", len(fixture.sources))
	}

	if _, err := service.AddSource(ctx, 1, 11, 42, application.AddSourceCommand{
		RefKind: domain.RefRequirement,
		RefID:   graphInt64Ptr(8),
	}); err != nil {
		t.Fatalf("add a different requirement source: %v", err)
	}
	manual := application.AddSourceCommand{RefKind: domain.RefManual, RefKey: "doc:1"}
	if _, err := service.AddSource(ctx, 1, 11, 42, manual); err != nil {
		t.Fatalf("add manual source: %v", err)
	}
	_, err = service.AddSource(ctx, 1, 11, 42, manual)
	requireDomainError(t, err, domain.KindConflict, "duplicate_case_source")
	if len(fixture.sources) != 3 {
		t.Fatalf("sources after the duplicate manual add = %d, want 3", len(fixture.sources))
	}
	if fixture.nodes[11].Revision != 3 {
		t.Fatalf("node revision after two source adds = %d, want 3", fixture.nodes[11].Revision)
	}
}

// TestConcurrentSourceDeletesCannotZeroRequirementCoverage covers the source
// write serialization: two overlapping deletes both read the same node revision,
// so the second loses the node revision CAS with case_revision_conflict and the
// requirement coverage never drops to zero.
func TestConcurrentSourceDeletesCannotZeroRequirementCoverage(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	fixture.sources[900] = domain.CaseSource{
		ID:         900,
		VersionID:  1,
		NodeID:     11,
		RefKind:    domain.RefRequirement,
		RefID:      graphInt64Ptr(8),
		Source:     domain.NodeSourceUserInput,
		Confidence: domain.ConfidenceHigh,
		CreatedAt:  fixture.now,
		UpdatedAt:  fixture.now,
	}
	graph := &concurrentSourceDeleteGraph{f: fixture}
	service := fixture.serviceWithGraph(graph)

	if err := service.DeleteSource(ctx, 1, 113, 42); err != nil {
		t.Fatalf("first source delete: %v", err)
	}
	if fixture.nodes[11].Revision != 2 {
		t.Fatalf("node revision after first source delete = %d, want 2", fixture.nodes[11].Revision)
	}

	err := service.DeleteSource(ctx, 1, 900, 42)
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	sources := fixture.requirementSourcesOfNode(11)
	if len(sources) != 1 || sources[0].ID != 900 {
		t.Fatalf("requirement sources after the conflicting delete = %+v, want only source 900", sources)
	}
	if len(fixture.events) != 1 {
		t.Fatalf("source deletes published %d event(s), want only the committed one", len(fixture.events))
	}
}

// TestDeleteNodeCascadesSubtree covers spec §6.1 rule 6: deleting a scenario
// removes its whole subtree and the child rows.
func TestDeleteNodeCascadesSubtree(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	parentID := int64(10)
	fixture.seedScenario(parentID, nil)
	fixture.seedCase(11, &parentID, 7)
	service := fixture.service()

	err := service.DeleteNode(ctx, 1, parentID, 42, 2)
	requireDomainError(t, err, domain.KindStateConflict, "case_revision_conflict")
	if _, ok := fixture.nodes[parentID]; !ok {
		t.Fatal("stale delete removed the scenario")
	}

	if err := service.DeleteNode(ctx, 1, parentID, 42, 1); err != nil {
		t.Fatalf("delete scenario: %v", err)
	}
	if len(fixture.nodes) != 0 || len(fixture.steps) != 0 || len(fixture.assertions) != 0 || len(fixture.sources) != 0 {
		t.Fatalf("cascade left nodes=%d steps=%d assertions=%d sources=%d",
			len(fixture.nodes), len(fixture.steps), len(fixture.assertions), len(fixture.sources))
	}
	requireEvent(t, fixture, 0, domain.EventCaseDeleted)
}

// TestGetBuildsCoverageAndEditable covers the read path: nodes grouped with
// their children, the coverage matrix of spec §6.2, and editable derived from
// version status plus the absence of a baseline (spec §7.3).
func TestGetBuildsCoverageAndEditable(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.version.Status = "reviewing"
	parentID := int64(10)
	fixture.seedScenario(parentID, nil)
	fixture.seedCase(11, &parentID, 1)
	fixture.seedCase(12, &parentID, 3)
	fixture.confirmed = []application.RequirementSnapshot{
		{ID: 1, VersionID: 1, Status: "clarified"},
		{ID: 2, VersionID: 1, Status: "clarified"},
	}
	service := fixture.service()

	view, err := service.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get graph: %v", err)
	}
	if view.VersionID != 1 || view.VersionStatus != "reviewing" {
		t.Fatalf("view version/status = %d/%s, want 1/reviewing", view.VersionID, view.VersionStatus)
	}
	if !view.Editable {
		t.Fatal("reviewing version without a baseline must be editable")
	}
	if view.Baseline != nil {
		t.Fatalf("baseline = %+v, want nil", view.Baseline)
	}
	if len(view.Nodes) != 3 {
		t.Fatalf("nodes = %d, want 3", len(view.Nodes))
	}
	if view.Nodes[0].Node.ID != parentID || len(view.Nodes[0].Steps) != 0 {
		t.Fatalf("scenario view = %+v, want parent %d without children", view.Nodes[0], parentID)
	}
	if len(view.Nodes[1].Steps) != 1 || len(view.Nodes[1].Assertions) != 1 || len(view.Nodes[1].Sources) != 1 {
		t.Fatalf("case view = %+v, want one step/assertion/source", view.Nodes[1])
	}
	if len(view.Coverage.RequirementIDs) != 2 {
		t.Fatalf("coverage requirements = %v, want [1 2]", view.Coverage.RequirementIDs)
	}
	if len(view.Coverage.UncoveredRequirementIDs) != 1 || view.Coverage.UncoveredRequirementIDs[0] != 2 {
		t.Fatalf("uncovered = %v, want [2]", view.Coverage.UncoveredRequirementIDs)
	}
	if len(view.Coverage.StaleRequirementIDs) != 1 || view.Coverage.StaleRequirementIDs[0] != 3 {
		t.Fatalf("stale = %v, want [3]", view.Coverage.StaleRequirementIDs)
	}
	if len(view.Coverage.Matrix) != 1 || view.Coverage.Matrix[0].RequirementID != 1 ||
		len(view.Coverage.Matrix[0].CaseIDs) != 1 || view.Coverage.Matrix[0].CaseIDs[0] != 11 {
		t.Fatalf("matrix = %+v, want requirement 1 covered by case 11", view.Coverage.Matrix)
	}

	fixture.baseline = &domain.CaseBaseline{ID: 99, VersionID: 1, GraphHash: "abc", CaseCount: 2, NodeCount: 3}
	view, err = service.Get(ctx, 1)
	if err != nil {
		t.Fatalf("get frozen graph: %v", err)
	}
	if view.Editable {
		t.Fatal("frozen version must not be editable")
	}
	if view.Baseline == nil || view.Baseline.ID != 99 {
		t.Fatalf("baseline = %+v, want id 99", view.Baseline)
	}
}

// TestGetRejectsMissingVersion checks the read path propagates the version
// lifecycle error for an unknown version.
func TestGetRejectsMissingVersion(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	service := fixture.service()

	if _, err := service.Get(ctx, 2); err == nil {
		t.Fatal("expected an error for a missing version")
	}
}

// TestDeleteNodeRemovesAnchorsOfSubtree covers spec §6.1 rule 6: deleting a
// scenario removes the code anchors of itself and its descendant cases through
// the CodeAnchorGateway.
func TestDeleteNodeRemovesAnchorsOfSubtree(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	anchors := newGraphAnchorGateway()
	fixture.anchors = anchors
	parentID := int64(10)
	childID := int64(11)
	caseID := int64(12)
	fixture.seedScenario(parentID, nil)
	fixture.seedScenario(childID, &parentID)
	fixture.seedCase(caseID, &childID, 7)
	anchors.add(parentID, 901)
	anchors.add(childID, 902)
	anchors.add(caseID, 903)
	service := fixture.service()

	if err := service.DeleteNode(ctx, 1, parentID, 42, 1); err != nil {
		t.Fatalf("delete scenario: %v", err)
	}
	if len(fixture.nodes) != 0 || len(fixture.steps) != 0 || len(fixture.assertions) != 0 || len(fixture.sources) != 0 {
		t.Fatalf("cascade left nodes=%d steps=%d assertions=%d sources=%d",
			len(fixture.nodes), len(fixture.steps), len(fixture.assertions), len(fixture.sources))
	}
	sort.Slice(anchors.listed, func(i, j int) bool { return anchors.listed[i] < anchors.listed[j] })
	if !graphEqualIDs(anchors.listed, []int64{parentID, childID, caseID}) {
		t.Fatalf("anchor list calls = %v, want every doomed node %v", anchors.listed, []int64{parentID, childID, caseID})
	}
	sort.Slice(anchors.deleted, func(i, j int) bool { return anchors.deleted[i] < anchors.deleted[j] })
	if !graphEqualIDs(anchors.deleted, []int64{901, 902, 903}) {
		t.Fatalf("deleted anchors = %v, want [901 902 903]", anchors.deleted)
	}
	for nodeID, list := range anchors.byNode {
		if len(list) != 0 {
			t.Fatalf("anchors of node %d after delete = %+v, want none", nodeID, list)
		}
	}
	requireEvent(t, fixture, 0, domain.EventCaseDeleted)
}

// TestDeleteNodeAnchorCleanupIsBestEffort covers the best-effort clause of spec
// §6.1 rule 6: an anchor gateway that cannot list or delete never fails the
// node delete.
func TestDeleteNodeAnchorCleanupIsBestEffort(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*graphAnchorGateway)
	}{
		{name: "list failure", configure: func(g *graphAnchorGateway) { g.listErr = errors.New("anchor service unavailable") }},
		{name: "delete failure", configure: func(g *graphAnchorGateway) { g.deleteErr = errors.New("anchor delete denied") }},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			fixture := newGraphFixture()
			anchors := newGraphAnchorGateway()
			anchors.add(11, 901)
			testCase.configure(anchors)
			fixture.anchors = anchors
			fixture.seedCase(11, nil, 7)
			service := fixture.service()

			if err := service.DeleteNode(ctx, 1, 11, 42, 1); err != nil {
				t.Fatalf("delete must not fail when anchor cleanup fails: %v", err)
			}
			if _, ok := fixture.nodes[11]; ok {
				t.Fatal("node survived the delete")
			}
			requireEvent(t, fixture, 0, domain.EventCaseDeleted)
		})
	}
}

// TestAnchorCleanupFailureLogsWarning covers the optional logger: a failed
// anchor list/delete is logged as a warning and never fails the delete.
func TestAnchorCleanupFailureLogsWarning(t *testing.T) {
	cases := []struct {
		name      string
		configure func(*graphAnchorGateway)
	}{
		{
			name:      "list failure",
			configure: func(g *graphAnchorGateway) { g.listErr = errors.New("anchor service unavailable") },
		},
		{
			name:      "delete failure",
			configure: func(g *graphAnchorGateway) { g.deleteErr = errors.New("anchor delete denied") },
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := context.Background()
			var output bytes.Buffer
			fixture := newGraphFixture()
			fixture.logger = slog.New(slog.NewTextHandler(&output, &slog.HandlerOptions{Level: slog.LevelWarn}))
			anchors := newGraphAnchorGateway()
			anchors.add(11, 901)
			testCase.configure(anchors)
			fixture.anchors = anchors
			fixture.seedCase(11, nil, 7)
			service := fixture.service()

			if err := service.DeleteNode(ctx, 1, 11, 42, 1); err != nil {
				t.Fatalf("delete must not fail when anchor cleanup fails: %v", err)
			}
			if !strings.Contains(output.String(), "anchor cleanup") {
				t.Fatalf("anchor cleanup failure was not logged: %q", output.String())
			}
			requireEvent(t, fixture, 0, domain.EventCaseDeleted)
		})
	}
}

// TestDeleteNodeWithoutAnchorGatewaySucceeds covers the optional dependency: a
// nil AnchorGateway must not block deletion.
func TestDeleteNodeWithoutAnchorGatewaySucceeds(t *testing.T) {
	ctx := context.Background()
	fixture := newGraphFixture()
	fixture.seedCase(11, nil, 7)
	service := fixture.service()

	if err := service.DeleteNode(ctx, 1, 11, 42, 1); err != nil {
		t.Fatalf("delete without an anchor gateway: %v", err)
	}
	if _, ok := fixture.nodes[11]; ok {
		t.Fatal("node survived the delete")
	}
	requireEvent(t, fixture, 0, domain.EventCaseDeleted)
}

// seedScenarios stores count root scenarios so a test can reach the node cap.
func seedScenarios(fixture *graphFixture, count int) {
	for index := 0; index < count; index++ {
		fixture.seedScenario(int64(1000+index), nil)
	}
}

// TestGetRejectsGraphOverNodeCap covers spec §7.3: a version with more than 300
// nodes is graph_too_large (409) on the read path; exactly 300 is allowed.
func TestGetRejectsGraphOverNodeCap(t *testing.T) {
	ctx := context.Background()
	t.Run("at the cap", func(t *testing.T) {
		fixture := newGraphFixture()
		seedScenarios(fixture, 300)
		view, err := fixture.service().Get(ctx, 1)
		if err != nil {
			t.Fatalf("get graph at the cap: %v", err)
		}
		if len(view.Nodes) != 300 {
			t.Fatalf("nodes = %d, want 300", len(view.Nodes))
		}
	})
	t.Run("over the cap", func(t *testing.T) {
		fixture := newGraphFixture()
		seedScenarios(fixture, 301)
		_, err := fixture.service().Get(ctx, 1)
		requireDomainError(t, err, domain.KindConflict, "graph_too_large")
	})
}

// TestCreateNodeRejectsGraphAtNodeCap covers spec §7.3 on the write path: the
// 301st node is graph_too_large and nothing is written.
func TestCreateNodeRejectsGraphAtNodeCap(t *testing.T) {
	ctx := context.Background()
	t.Run("below the cap", func(t *testing.T) {
		fixture := newGraphFixture()
		seedScenarios(fixture, 299)
		service := fixture.service()
		if _, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
			Kind:  domain.NodeKindScenario,
			Title: "the 300th node",
		}); err != nil {
			t.Fatalf("create node below the cap: %v", err)
		}
		if len(fixture.nodes) != 300 {
			t.Fatalf("nodes = %d, want 300", len(fixture.nodes))
		}
		requireEvent(t, fixture, 0, domain.EventCaseCreated)
	})
	t.Run("at the cap", func(t *testing.T) {
		fixture := newGraphFixture()
		seedScenarios(fixture, 300)
		service := fixture.service()
		_, err := service.CreateNode(ctx, 1, 42, application.CreateNodeCommand{
			Kind:  domain.NodeKindScenario,
			Title: "over the cap",
		})
		requireDomainError(t, err, domain.KindConflict, "graph_too_large")
		if len(fixture.nodes) != 300 {
			t.Fatalf("rejected create stored %d nodes, want 300", len(fixture.nodes))
		}
		if len(fixture.events) != 0 {
			t.Fatalf("rejected create published %d event(s)", len(fixture.events))
		}
	})
}

// graphEqualIDs reports whether two id lists hold the same values in order.
func graphEqualIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func graphIntPtr(value int) *int { return &value }
