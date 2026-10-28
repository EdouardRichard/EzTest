package application_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
)

const runTestVersionID int64 = 1

// runFixture is a stateful in-memory implementation of every RunService port. It
// mimics the PostgreSQL adapters for the rules the coordinator and the
// materializer rely on: the active-run single-flight index, the run status CAS,
// stale recovery, the node revision CAS, review/finding scoping and the
// in-memory transaction runner used to prove one-transaction materialization.
type runFixture struct {
	version  application.VersionSnapshot
	baseline *domain.CaseBaseline

	nodes      map[int64]domain.CaseNode
	steps      map[int64]domain.CaseStep
	assertions map[int64]domain.CaseAssertion
	sources    map[int64]domain.CaseSource
	reviews    map[int64]domain.CaseReview
	findings   map[int64]domain.CaseReviewFinding
	runs       map[int64]domain.CaseDesignRun

	confirmed []application.RequirementSnapshot
	relations []application.RelationSnapshot
	appModels []application.AppModelSnapshot
	facts     []application.FactSummary
	files     map[string]bool

	anchors    []application.Anchor
	anchorArgs []application.AnchorParams
	sessions   map[int64]application.AgentSessionStatus
	texts      []string
	profiles   []string
	channels   []string
	titles     []string
	aborted    []int64
	events     []application.Event

	now    time.Time
	nextID int64
	// txCount counts GraphRepository.InTx callbacks; a rejected materialization
	// must not open one and an accepted one must open exactly one.
	txCount int

	enterReviewErr   error
	createSessionErr error
	startTextErr     error
	// runningCASErr makes the queued→running run status CAS fail, mimicking a
	// concurrent worker that already moved the row.
	runningCASErr error
}

// newRunFixture builds a version with a bound system and code baseline.
func newRunFixture() *runFixture {
	systemID, environmentID, repositoryID := int64(5), int64(9), int64(3)
	commit := strings.Repeat("a", 40)
	now := time.Date(2026, 10, 28, 10, 0, 0, 0, time.UTC)
	return &runFixture{
		version: application.VersionSnapshot{
			ID:                 runTestVersionID,
			Name:               "P5 payment",
			Status:             "designing",
			SystemID:           &systemID,
			EnvironmentID:      &environmentID,
			CodeRepositoryID:   &repositoryID,
			CodeBaselineCommit: &commit,
		},
		nodes:      make(map[int64]domain.CaseNode),
		steps:      make(map[int64]domain.CaseStep),
		assertions: make(map[int64]domain.CaseAssertion),
		sources:    make(map[int64]domain.CaseSource),
		reviews:    make(map[int64]domain.CaseReview),
		findings:   make(map[int64]domain.CaseReviewFinding),
		runs:       make(map[int64]domain.CaseDesignRun),
		files:      map[string]bool{"src/order/submit.ts": true},
		sessions:   make(map[int64]application.AgentSessionStatus),
		now:        now,
		nextID:     5000,
	}
}

// service wires the fixture into the production constructor.
func (f *runFixture) service() *application.RunService {
	return application.NewRunService(application.RunDeps{
		Runs:         runRunRepo{f},
		Graph:        f,
		Baselines:    runBaselineRepo{f},
		Nodes:        runNodeRepo{f},
		Steps:        runStepRepo{f},
		Assertions:   runAssertionRepo{f},
		Sources:      runSourceRepo{f},
		Versions:     runVersionLifecycle{f},
		Requirements: runRequirementReader{f},
		Catalog:      runCatalogGateway{f},
		CodeFacts:    runCodeFactsGateway{f},
		Anchors:      runAnchorGateway{f},
		Agents:       runAgentRuntime{f},
		Events:       runEventSink{f},
		IDs:          runIDGenerator{f},
		Clock:        runClock{f},
	})
}

func (f *runFixture) id() int64 {
	f.nextID++
	return f.nextID
}

// InTx runs one callback like the pgx transaction adapter, restoring the
// in-memory state when the callback fails.
func (f *runFixture) InTx(_ context.Context, fn func(application.GraphTx) error) error {
	f.txCount++
	before := f.snapshot()
	if err := fn(runTx{f}); err != nil {
		f.restore(before)
		return err
	}
	return nil
}

// runFixtureState is the snapshot restored when a transaction callback fails.
type runFixtureState struct {
	nodes      map[int64]domain.CaseNode
	steps      map[int64]domain.CaseStep
	assertions map[int64]domain.CaseAssertion
	sources    map[int64]domain.CaseSource
	reviews    map[int64]domain.CaseReview
	findings   map[int64]domain.CaseReviewFinding
	runs       map[int64]domain.CaseDesignRun
}

func (f *runFixture) snapshot() runFixtureState {
	return runFixtureState{
		nodes:      runCloneMap(f.nodes),
		steps:      runCloneMap(f.steps),
		assertions: runCloneMap(f.assertions),
		sources:    runCloneMap(f.sources),
		reviews:    runCloneMap(f.reviews),
		findings:   runCloneMap(f.findings),
		runs:       runCloneMap(f.runs),
	}
}

func (f *runFixture) restore(state runFixtureState) {
	f.nodes = state.nodes
	f.steps = state.steps
	f.assertions = state.assertions
	f.sources = state.sources
	f.reviews = state.reviews
	f.findings = state.findings
	f.runs = state.runs
}

func runCloneMap[K comparable, V any](source map[K]V) map[K]V {
	clone := make(map[K]V, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

// seedCaseNode stores one draft case node with the given revision.
func (f *runFixture) seedCaseNode(id int64, revision int, title string) domain.CaseNode {
	node := domain.CaseNode{
		ID:        id,
		VersionID: runTestVersionID,
		Kind:      domain.NodeKindCase,
		Title:     title,
		Priority:  domain.PriorityP1,
		Source:    domain.NodeSourceUserInput,
		Status:    domain.NodeStatusDraft,
		SortOrder: int(id),
		Revision:  revision,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.nodes[id] = node
	return node
}

// seedRunningRun stores a running design run with an agent session and result.
func (f *runFixture) seedRunningRun(kind, result string) domain.CaseDesignRun {
	sessionID := f.id()
	agentRunID := f.id()
	started := f.now
	promptVersion := domain.PromptVersionDesign
	switch kind {
	case domain.RunKindReview:
		promptVersion = domain.PromptVersionReview
	case domain.RunKindDataPlan:
		promptVersion = domain.PromptVersionDataPlan
	}
	run := domain.CaseDesignRun{
		ID:             f.id(),
		VersionID:      runTestVersionID,
		Kind:           kind,
		Status:         domain.RunRunning,
		AgentSessionID: &sessionID,
		AgentRunID:     &agentRunID,
		PromptVersion:  promptVersion,
		Input:          []byte(`{}`),
		Result:         []byte(result),
		CreatedBy:      42,
		CreatedAt:      f.now,
		UpdatedAt:      f.now,
		StartedAt:      &started,
	}
	f.runs[run.ID] = run
	f.sessions[sessionID] = application.AgentSessionStatus{
		SessionID: sessionID,
		Status:    "running",
		LastRun:   &application.AgentRunStatus{ID: agentRunID, Status: "running"},
	}
	return run
}

// seedReview stores one review of the fixture version.
func (f *runFixture) seedReview(id int64, status string) domain.CaseReview {
	review := domain.CaseReview{
		ID:         id,
		VersionID:  runTestVersionID,
		Status:     status,
		ReviewerID: 42,
		CreatedAt:  f.now,
		UpdatedAt:  f.now,
	}
	f.reviews[id] = review
	return review
}

// seedFinding stores one review finding.
func (f *runFixture) seedFinding(id, reviewID int64, source, status string) domain.CaseReviewFinding {
	finding := domain.CaseReviewFinding{
		ID:        id,
		ReviewID:  reviewID,
		Category:  domain.FindingCoverage,
		Severity:  domain.SeverityMinor,
		Detail:    "previous finding",
		Status:    status,
		Source:    source,
		CreatedBy: 42,
		CreatedAt: f.now,
		UpdatedAt: f.now,
	}
	f.findings[id] = finding
	return finding
}

// hasEvent reports whether the fixture recorded an event of the given type.
func (f *runFixture) hasEvent(eventType string) bool {
	for _, event := range f.events {
		if event.Type == eventType {
			return true
		}
	}
	return false
}

// eventCount counts the recorded events of the given type.
func (f *runFixture) eventCount(eventType string) int {
	count := 0
	for _, event := range f.events {
		if event.Type == eventType {
			count++
		}
	}
	return count
}

// runStats decodes one run's stats document.
func runStats(t *testing.T, run domain.CaseDesignRun) map[string]any {
	t.Helper()
	if len(run.Stats) == 0 {
		return map[string]any{}
	}
	var stats map[string]any
	if err := json.Unmarshal(run.Stats, &stats); err != nil {
		t.Fatalf("decode run stats %q: %v", string(run.Stats), err)
	}
	return stats
}

// runErrors decodes one run's errors document.
func runErrors(t *testing.T, run domain.CaseDesignRun) (string, []string) {
	t.Helper()
	if len(run.Errors) == 0 {
		return "", nil
	}
	var document struct {
		Reason           string   `json:"reason"`
		ValidationErrors []string `json:"validationErrors"`
	}
	if err := json.Unmarshal(run.Errors, &document); err != nil {
		t.Fatalf("decode run errors %q: %v", string(run.Errors), err)
	}
	return document.Reason, document.ValidationErrors
}

// runNodeRepo implements application.NodeRepository over runFixture.
type runNodeRepo struct {
	f *runFixture
}

func (r runNodeRepo) Create(_ context.Context, node domain.CaseNode) (domain.CaseNode, error) {
	if _, exists := r.f.nodes[node.ID]; exists {
		return domain.CaseNode{}, errors.New("runFixture: duplicate node id")
	}
	r.f.nodes[node.ID] = node
	return node, nil
}

func (r runNodeRepo) Get(_ context.Context, versionID, nodeID int64) (domain.CaseNode, error) {
	node, ok := r.f.nodes[nodeID]
	if !ok || node.VersionID != versionID {
		return domain.CaseNode{}, domain.NewNotFound("node", nodeID)
	}
	return node, nil
}

func (r runNodeRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseNode, error) {
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

func (r runNodeRepo) Update(_ context.Context, node domain.CaseNode, expectedRevision int) (domain.CaseNode, error) {
	current, ok := r.f.nodes[node.ID]
	if !ok || current.VersionID != node.VersionID {
		return domain.CaseNode{}, domain.NewNotFound("node", node.ID)
	}
	if current.Revision != expectedRevision {
		return domain.CaseNode{}, domain.NewStateConflict("case_revision_conflict", "stale node revision")
	}
	node.Revision = expectedRevision + 1
	node.UpdatedAt = r.f.now
	r.f.nodes[node.ID] = node
	return node, nil
}

func (r runNodeRepo) Delete(_ context.Context, versionID, nodeID int64, expectedRevision int) error {
	current, ok := r.f.nodes[nodeID]
	if !ok || current.VersionID != versionID {
		return domain.NewNotFound("node", nodeID)
	}
	if current.Revision != expectedRevision {
		return domain.NewStateConflict("case_revision_conflict", "stale node revision")
	}
	// case_node.parent_id is ON DELETE CASCADE, so removing a scenario removes
	// its whole subtree; the fixture mirrors that so a replace that deletes only
	// the roots behaves like PostgreSQL.
	r.f.deleteNodeSubtree(nodeID)
	return nil
}

// deleteNodeSubtree removes one node, its descendants and the child rows of the
// whole subtree.
func (f *runFixture) deleteNodeSubtree(nodeID int64) {
	for id, node := range f.nodes {
		if node.ParentID != nil && *node.ParentID == nodeID {
			f.deleteNodeSubtree(id)
		}
	}
	delete(f.nodes, nodeID)
	for id, step := range f.steps {
		if step.NodeID == nodeID {
			delete(f.steps, id)
		}
	}
	for id, assertion := range f.assertions {
		if assertion.NodeID == nodeID {
			delete(f.assertions, id)
		}
	}
	for id, source := range f.sources {
		if source.NodeID == nodeID {
			delete(f.sources, id)
		}
	}
}

func (r runNodeRepo) UpdateStatusByVersion(_ context.Context, versionID int64, from, to string) (int, error) {
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

func (r runNodeRepo) RestoreStatusByVersion(_ context.Context, versionID int64, nodes []domain.CaseNode) error {
	for _, node := range nodes {
		current, ok := r.f.nodes[node.ID]
		if !ok || current.VersionID != versionID {
			return domain.NewNotFound("node", node.ID)
		}
		r.f.nodes[node.ID] = node
	}
	return nil
}

func (r runNodeRepo) SetSortOrders(_ context.Context, versionID int64, ordered []int64) error {
	for index, id := range ordered {
		node, ok := r.f.nodes[id]
		if !ok || node.VersionID != versionID {
			return domain.NewNotFound("node", id)
		}
		node.SortOrder = index + 1
		r.f.nodes[id] = node
	}
	return nil
}

func (r runNodeRepo) CountByVersion(_ context.Context, versionID int64) (int, int, error) {
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

// runStepRepo implements application.StepRepository over runFixture.
type runStepRepo struct {
	f *runFixture
}

func (r runStepRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseStep, error) {
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
		return steps[i].Seq < steps[j].Seq
	})
	return steps, nil
}

func (r runStepRepo) Create(_ context.Context, step domain.CaseStep) (domain.CaseStep, error) {
	if _, exists := r.f.steps[step.ID]; exists {
		return domain.CaseStep{}, errors.New("runFixture: duplicate step id")
	}
	r.f.steps[step.ID] = step
	return step, nil
}

func (r runStepRepo) Update(_ context.Context, step domain.CaseStep) error {
	if _, ok := r.f.steps[step.ID]; !ok {
		return domain.NewNotFound("step", step.ID)
	}
	r.f.steps[step.ID] = step
	return nil
}

func (r runStepRepo) Delete(_ context.Context, nodeID, stepID int64) error {
	step, ok := r.f.steps[stepID]
	if !ok || step.NodeID != nodeID {
		return domain.NewNotFound("step", stepID)
	}
	delete(r.f.steps, stepID)
	return nil
}

func (r runStepRepo) ReplaceSeq(_ context.Context, nodeID int64, ordered []int64) error {
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

func (r runStepRepo) CountByNode(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, step := range r.f.steps {
		if step.NodeID == nodeID {
			count++
		}
	}
	return count, nil
}

// runAssertionRepo implements application.AssertionRepository over runFixture.
type runAssertionRepo struct {
	f *runFixture
}

func (r runAssertionRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseAssertion, error) {
	assertions := make([]domain.CaseAssertion, 0, len(r.f.assertions))
	for _, assertion := range r.f.assertions {
		if node, ok := r.f.nodes[assertion.NodeID]; ok && node.VersionID == versionID {
			assertions = append(assertions, assertion)
		}
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].ID < assertions[j].ID })
	return assertions, nil
}

func (r runAssertionRepo) Create(_ context.Context, assertion domain.CaseAssertion) (domain.CaseAssertion, error) {
	if _, exists := r.f.assertions[assertion.ID]; exists {
		return domain.CaseAssertion{}, errors.New("runFixture: duplicate assertion id")
	}
	r.f.assertions[assertion.ID] = assertion
	return assertion, nil
}

func (r runAssertionRepo) Update(_ context.Context, assertion domain.CaseAssertion) error {
	if _, ok := r.f.assertions[assertion.ID]; !ok {
		return domain.NewNotFound("assertion", assertion.ID)
	}
	r.f.assertions[assertion.ID] = assertion
	return nil
}

func (r runAssertionRepo) Delete(_ context.Context, nodeID, assertionID int64) error {
	assertion, ok := r.f.assertions[assertionID]
	if !ok || assertion.NodeID != nodeID {
		return domain.NewNotFound("assertion", assertionID)
	}
	delete(r.f.assertions, assertionID)
	return nil
}

func (r runAssertionRepo) CountByNode(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, assertion := range r.f.assertions {
		if assertion.NodeID == nodeID {
			count++
		}
	}
	return count, nil
}

// runSourceRepo implements application.SourceRepository over runFixture.
type runSourceRepo struct {
	f *runFixture
}

func (r runSourceRepo) ListByVersion(_ context.Context, versionID int64) ([]domain.CaseSource, error) {
	sources := make([]domain.CaseSource, 0, len(r.f.sources))
	for _, source := range r.f.sources {
		if source.VersionID == versionID {
			sources = append(sources, source)
		}
	}
	sort.Slice(sources, func(i, j int) bool { return sources[i].ID < sources[j].ID })
	return sources, nil
}

func (r runSourceRepo) Create(_ context.Context, source domain.CaseSource) (domain.CaseSource, error) {
	if _, exists := r.f.sources[source.ID]; exists {
		return domain.CaseSource{}, errors.New("runFixture: duplicate source id")
	}
	r.f.sources[source.ID] = source
	return source, nil
}

func (r runSourceRepo) Delete(_ context.Context, nodeID, sourceID int64) error {
	source, ok := r.f.sources[sourceID]
	if !ok || source.NodeID != nodeID {
		return domain.NewNotFound("source", sourceID)
	}
	delete(r.f.sources, sourceID)
	return nil
}

func (r runSourceRepo) CountRequirementSources(_ context.Context, nodeID int64) (int, error) {
	count := 0
	for _, source := range r.f.sources {
		if source.NodeID == nodeID && source.RefKind == domain.RefRequirement {
			count++
		}
	}
	return count, nil
}

// runRunRepo implements application.RunRepository over runFixture.
type runRunRepo struct {
	f *runFixture
}

func (r runRunRepo) Create(_ context.Context, run domain.CaseDesignRun) (domain.CaseDesignRun, error) {
	for _, existing := range r.f.runs {
		if existing.VersionID == run.VersionID && existing.Kind == run.Kind && existing.IsActive() {
			return domain.CaseDesignRun{}, domain.NewConflict("run_in_progress",
				"a run of this kind is already queued or running")
		}
	}
	if _, exists := r.f.runs[run.ID]; exists {
		return domain.CaseDesignRun{}, domain.NewConflict("duplicate_key", "run already exists")
	}
	r.f.runs[run.ID] = run
	return run, nil
}

func (r runRunRepo) Get(_ context.Context, versionID, runID int64) (domain.CaseDesignRun, error) {
	run, ok := r.f.runs[runID]
	if !ok || run.VersionID != versionID {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", runID)
	}
	return run, nil
}

func (r runRunRepo) List(_ context.Context, _ int64, _, _ string, _ int, _ string) ([]domain.CaseDesignRun, string, bool, error) {
	return nil, "", false, errors.New("runFixture: List is not used")
}

func (r runRunRepo) UpdateStatus(_ context.Context, runID int64, from, to string, patch application.RunPatch) (domain.CaseDesignRun, error) {
	if r.f.runningCASErr != nil && from == domain.RunQueued && to == domain.RunRunning {
		// The strict adapter returns the zero row plus the CAS error; the
		// coordinator must not rely on that return value.
		return domain.CaseDesignRun{}, r.f.runningCASErr
	}
	run, ok := r.f.runs[runID]
	if !ok {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", runID)
	}
	if run.Status != from {
		return domain.CaseDesignRun{}, domain.NewStateConflict("run_state_conflict",
			fmt.Sprintf("run %d is %s, not %s", runID, run.Status, from))
	}
	if patch.AgentSessionID != nil {
		run.AgentSessionID = patch.AgentSessionID
	}
	if patch.AgentRunID != nil {
		run.AgentRunID = patch.AgentRunID
	}
	if patch.Model != nil {
		run.Model = *patch.Model
	}
	if patch.PromptVersion != nil {
		run.PromptVersion = *patch.PromptVersion
	}
	if patch.Input != nil {
		run.Input = patch.Input
	}
	if patch.Result != nil {
		run.Result = patch.Result
	}
	if patch.Stats != nil {
		run.Stats = patch.Stats
	}
	if patch.Errors != nil {
		run.Errors = patch.Errors
	}
	if patch.StartedAt != nil {
		run.StartedAt = patch.StartedAt
	}
	if patch.FinishedAt != nil {
		run.FinishedAt = patch.FinishedAt
	}
	run.Status = to
	run.UpdatedAt = r.f.now
	r.f.runs[runID] = run
	return run, nil
}

func (r runRunRepo) LatestByKind(_ context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	var latest domain.CaseDesignRun
	for _, run := range r.f.runs {
		if run.VersionID == versionID && run.Kind == kind && run.ID > latest.ID {
			latest = run
		}
	}
	if latest.ID == 0 {
		return domain.CaseDesignRun{}, domain.NewNotFound("run", versionID)
	}
	return latest, nil
}

func (r runRunRepo) ActiveByKind(_ context.Context, versionID int64, kind string) (domain.CaseDesignRun, error) {
	for _, run := range r.f.runs {
		if run.VersionID == versionID && run.Kind == kind && run.IsActive() {
			return run, nil
		}
	}
	return domain.CaseDesignRun{}, domain.NewNotFound("run", versionID)
}

func (r runRunRepo) ListActive(_ context.Context, limit int) ([]domain.CaseDesignRun, error) {
	runs := make([]domain.CaseDesignRun, 0)
	for _, run := range r.f.runs {
		if run.IsActive() {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool {
		if !runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].CreatedAt.Before(runs[j].CreatedAt)
		}
		return runs[i].ID < runs[j].ID
	})
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

func (r runRunRepo) RecoverStale(_ context.Context, before time.Time) (int64, error) {
	recovered := int64(0)
	for id, run := range r.f.runs {
		if !run.IsActive() || !run.UpdatedAt.Before(before) {
			continue
		}
		finished := r.f.now
		run.Status = domain.RunFailed
		run.FinishedAt = &finished
		run.UpdatedAt = r.f.now
		if len(run.Errors) == 0 {
			run.Errors = []byte(`{"reason":"worker_restart"}`)
		}
		r.f.runs[id] = run
		recovered++
	}
	return recovered, nil
}

// runReviewRepo implements application.ReviewRepository over runFixture.
type runReviewRepo struct {
	f *runFixture
}

func (r runReviewRepo) Create(_ context.Context, review domain.CaseReview) (domain.CaseReview, error) {
	if _, exists := r.f.reviews[review.ID]; exists {
		return domain.CaseReview{}, domain.NewConflict("duplicate_key", "review already exists")
	}
	// uk_case_review_approved: at most one approved review per version.
	if review.Status == domain.ReviewApproved {
		for _, existing := range r.f.reviews {
			if existing.VersionID == review.VersionID && existing.Status == domain.ReviewApproved {
				return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
					"the version already has an approved review")
			}
		}
	}
	r.f.reviews[review.ID] = review
	return review, nil
}

func (r runReviewRepo) GetLatest(_ context.Context, versionID int64) (domain.CaseReview, error) {
	var latest domain.CaseReview
	for _, review := range r.f.reviews {
		if review.VersionID == versionID && review.ID > latest.ID {
			latest = review
		}
	}
	if latest.ID == 0 {
		return domain.CaseReview{}, domain.NewNotFound("review", versionID)
	}
	return latest, nil
}

func (r runReviewRepo) GetApproved(_ context.Context, versionID int64) (domain.CaseReview, error) {
	for _, review := range r.f.reviews {
		if review.VersionID == versionID && review.Status == domain.ReviewApproved {
			return review, nil
		}
	}
	return domain.CaseReview{}, domain.NewNotFound("review", versionID)
}

func (r runReviewRepo) Decide(_ context.Context, reviewID int64, status string, comment string, actor int64) (domain.CaseReview, error) {
	review, ok := r.f.reviews[reviewID]
	if !ok {
		return domain.CaseReview{}, domain.NewNotFound("review", reviewID)
	}
	// The PostgreSQL Decide is a draft-only CAS: an already-decided row is a
	// conflict, and an approved row specifically is baseline_frozen.
	if review.Status != domain.ReviewDraft {
		if review.Status == domain.ReviewApproved {
			return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
				"the review is already approved")
		}
		return domain.CaseReview{}, domain.NewConflict("conflict",
			"the review is already "+review.Status)
	}
	// uk_case_review_approved: approving a second review of the version is a
	// baseline_frozen conflict, exactly like the PostgreSQL unique index.
	if status == domain.ReviewApproved {
		for id, existing := range r.f.reviews {
			if id != reviewID && existing.VersionID == review.VersionID && existing.Status == domain.ReviewApproved {
				return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
					"the version already has an approved review")
			}
		}
	}
	review.Status = status
	review.Comment = comment
	decidedBy := actor
	review.DecidedBy = &decidedBy
	decidedAt := r.f.now
	review.DecidedAt = &decidedAt
	review.UpdatedAt = r.f.now
	r.f.reviews[reviewID] = review
	return review, nil
}

// Reopen mirrors the PostgreSQL approved-only CAS: a missing row is not found, a
// non-approved row is a conflict and an empty comment keeps the stored one.
func (r runReviewRepo) Reopen(_ context.Context, reviewID int64, comment string, _ int64) (domain.CaseReview, error) {
	review, ok := r.f.reviews[reviewID]
	if !ok {
		return domain.CaseReview{}, domain.NewNotFound("review", reviewID)
	}
	if review.Status != domain.ReviewApproved {
		return domain.CaseReview{}, domain.NewConflict("baseline_frozen",
			"the review is not approved")
	}
	review.Status = domain.ReviewDraft
	if comment != "" {
		review.Comment = comment
	}
	review.UpdatedAt = r.f.now
	r.f.reviews[reviewID] = review
	return review, nil
}

func (r runReviewRepo) List(_ context.Context, versionID int64, _ int, _ string) ([]domain.CaseReview, string, bool, error) {
	reviews := make([]domain.CaseReview, 0)
	for _, review := range r.f.reviews {
		if review.VersionID == versionID {
			reviews = append(reviews, review)
		}
	}
	sort.Slice(reviews, func(i, j int) bool { return reviews[i].ID < reviews[j].ID })
	return reviews, "", false, nil
}

// runFindingRepo implements application.FindingRepository over runFixture.
type runFindingRepo struct {
	f *runFixture
}

func (r runFindingRepo) CreateBatch(_ context.Context, findings []domain.CaseReviewFinding) error {
	for _, finding := range findings {
		if _, exists := r.f.findings[finding.ID]; exists {
			return domain.NewConflict("duplicate_key", "finding already exists")
		}
		r.f.findings[finding.ID] = finding
	}
	return nil
}

func (r runFindingRepo) List(_ context.Context, versionID int64, status, severity string, nodeID *int64) ([]domain.CaseReviewFinding, error) {
	findings := make([]domain.CaseReviewFinding, 0)
	for _, finding := range r.f.findings {
		review, ok := r.f.reviews[finding.ReviewID]
		if !ok || review.VersionID != versionID {
			continue
		}
		if status != "" && finding.Status != status {
			continue
		}
		if severity != "" && finding.Severity != severity {
			continue
		}
		if nodeID != nil && (finding.NodeID == nil || *finding.NodeID != *nodeID) {
			continue
		}
		findings = append(findings, finding)
	}
	sort.Slice(findings, func(i, j int) bool { return findings[i].ID < findings[j].ID })
	return findings, nil
}

func (r runFindingRepo) UpdateStatus(_ context.Context, findingID int64, status string, actor int64) (domain.CaseReviewFinding, error) {
	finding, ok := r.f.findings[findingID]
	if !ok {
		return domain.CaseReviewFinding{}, domain.NewNotFound("finding", findingID)
	}
	finding.Status = status
	finding.UpdatedAt = r.f.now
	r.f.findings[findingID] = finding
	return finding, nil
}

func (r runFindingRepo) DismissOpenAI(_ context.Context, versionID int64) (int, error) {
	dismissed := 0
	for id, finding := range r.f.findings {
		if finding.Source != domain.FindingSourceAI || finding.Status != domain.FindingOpen {
			continue
		}
		review, ok := r.f.reviews[finding.ReviewID]
		if !ok || review.VersionID != versionID {
			continue
		}
		finding.Status = domain.FindingDismissed
		finding.UpdatedAt = r.f.now
		r.f.findings[id] = finding
		dismissed++
	}
	return dismissed, nil
}

func (r runFindingRepo) CountOpenBlockers(_ context.Context, versionID int64) (int, error) {
	count := 0
	for _, finding := range r.f.findings {
		review, ok := r.f.reviews[finding.ReviewID]
		if !ok || review.VersionID != versionID {
			continue
		}
		if finding.Status == domain.FindingOpen && finding.Severity == domain.SeverityBlocker {
			count++
		}
	}
	return count, nil
}

// runBaselineRepo implements application.BaselineRepository over runFixture.
type runBaselineRepo struct {
	f *runFixture
}

func (r runBaselineRepo) Create(_ context.Context, baseline domain.CaseBaseline) (domain.CaseBaseline, error) {
	copy := baseline
	r.f.baseline = &copy
	return copy, nil
}

func (r runBaselineRepo) GetByVersion(_ context.Context, versionID int64) (domain.CaseBaseline, error) {
	if r.f.baseline == nil || r.f.baseline.VersionID != versionID {
		return domain.CaseBaseline{}, domain.NewNotFound("baseline", versionID)
	}
	return *r.f.baseline, nil
}

func (r runBaselineRepo) DeleteByVersion(_ context.Context, versionID int64) error {
	if r.f.baseline != nil && r.f.baseline.VersionID == versionID {
		r.f.baseline = nil
	}
	return nil
}

// runVersionLifecycle implements application.VersionLifecycle over runFixture.
type runVersionLifecycle struct {
	f *runFixture
}

func (v runVersionLifecycle) GetVersion(_ context.Context, versionID int64) (application.VersionSnapshot, error) {
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	return v.f.version, nil
}

func (v runVersionLifecycle) EnterReview(_ context.Context, versionID, actor int64) (application.VersionSnapshot, error) {
	if v.f.enterReviewErr != nil {
		return application.VersionSnapshot{}, v.f.enterReviewErr
	}
	if versionID != v.f.version.ID {
		return application.VersionSnapshot{}, domain.NewNotFound("version", versionID)
	}
	switch v.f.version.Status {
	case "designing":
		v.f.version.Status = "reviewing"
		return v.f.version, nil
	case "reviewing":
		return v.f.version, nil
	default:
		return application.VersionSnapshot{}, domain.NewStateConflict("version_state_conflict",
			"version is not designing")
	}
}

func (v runVersionLifecycle) RejectReview(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("runFixture: RejectReview is not used")
}

func (v runVersionLifecycle) ApproveBaseline(context.Context, int64, int64) (application.VersionSnapshot, error) {
	return application.VersionSnapshot{}, errors.New("runFixture: ApproveBaseline is not used")
}

// runRequirementReader implements application.RequirementReader.
type runRequirementReader struct {
	f *runFixture
}

func (r runRequirementReader) ListConfirmed(context.Context, int64) ([]application.RequirementSnapshot, error) {
	return append([]application.RequirementSnapshot(nil), r.f.confirmed...), nil
}

func (r runRequirementReader) ListRelations(context.Context, int64) ([]application.RelationSnapshot, error) {
	return append([]application.RelationSnapshot(nil), r.f.relations...), nil
}

// runCatalogGateway implements application.CatalogGateway.
type runCatalogGateway struct {
	f *runFixture
}

func (g runCatalogGateway) GetEnvironment(_ context.Context, envID int64) (application.EnvironmentSnapshot, error) {
	return application.EnvironmentSnapshot{ID: envID, SystemID: 5, Name: "test", BaseURL: "http://localhost", Kind: "web"}, nil
}

func (g runCatalogGateway) ListAppModels(context.Context, int64) ([]application.AppModelSnapshot, error) {
	return append([]application.AppModelSnapshot(nil), g.f.appModels...), nil
}

func (g runCatalogGateway) ImportAppModels(context.Context, int64, []application.AppModelInput) (int, int, error) {
	return 0, 0, errors.New("runFixture: ImportAppModels is not used")
}

// runCodeFactsGateway implements application.CodeFactsGateway.
type runCodeFactsGateway struct {
	f *runFixture
}

func (g runCodeFactsGateway) GetLatestAnalysis(context.Context, int64) (application.ImpactSnapshot, error) {
	return application.ImpactSnapshot{}, nil
}

func (g runCodeFactsGateway) ListCandidates(context.Context, int64) ([]application.ImpactCandidate, error) {
	return nil, nil
}

func (g runCodeFactsGateway) FileExists(_ context.Context, _ int64, path string) (bool, error) {
	return g.f.files[path], nil
}

func (g runCodeFactsGateway) ListFacts(_ context.Context, _ int64, limit int) ([]application.FactSummary, error) {
	facts := append([]application.FactSummary(nil), g.f.facts...)
	if limit > 0 && len(facts) > limit {
		facts = facts[:limit]
	}
	return facts, nil
}

// runAnchorGateway implements application.CodeAnchorGateway and records writes.
type runAnchorGateway struct {
	f *runFixture
}

func (g runAnchorGateway) Create(_ context.Context, versionID, actorID int64, params application.AnchorParams) (application.Anchor, error) {
	g.f.anchorArgs = append(g.f.anchorArgs, params)
	anchor := application.Anchor{
		ID:         g.f.id(),
		VersionID:  versionID,
		SubjectID:  params.SubjectID,
		FilePath:   params.FilePath,
		StartLine:  params.StartLine,
		EndLine:    params.EndLine,
		Confidence: params.Confidence,
		CreatedBy:  actorID,
	}
	g.f.anchors = append(g.f.anchors, anchor)
	return anchor, nil
}

func (g runAnchorGateway) List(_ context.Context, versionID, nodeID int64) ([]application.Anchor, error) {
	anchors := make([]application.Anchor, 0)
	for _, anchor := range g.f.anchors {
		if anchor.VersionID == versionID && anchor.SubjectID == nodeID {
			anchors = append(anchors, anchor)
		}
	}
	return anchors, nil
}

func (g runAnchorGateway) Delete(_ context.Context, _ int64, anchorID int64) error {
	for index, anchor := range g.f.anchors {
		if anchor.ID == anchorID {
			g.f.anchors = append(g.f.anchors[:index], g.f.anchors[index+1:]...)
			return nil
		}
	}
	return domain.NewNotFound("anchor", anchorID)
}

// runAgentRuntime implements application.AgentRuntime and records calls.
type runAgentRuntime struct {
	f *runFixture
}

func (a runAgentRuntime) EnsureProfile(context.Context, application.ProfileSeed) error {
	return nil
}

func (a runAgentRuntime) CreateSession(_ context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if a.f.createSessionErr != nil {
		return 0, a.f.createSessionErr
	}
	id := a.f.id()
	a.f.profiles = append(a.f.profiles, profileKey)
	a.f.channels = append(a.f.channels, channel)
	a.f.titles = append(a.f.titles, title)
	a.f.sessions[id] = application.AgentSessionStatus{SessionID: id, Status: "running"}
	return id, nil
}

func (a runAgentRuntime) StartText(_ context.Context, sessionID int64, text string, actor int64) (int64, error) {
	if a.f.startTextErr != nil {
		return 0, a.f.startTextErr
	}
	a.f.texts = append(a.f.texts, text)
	runID := a.f.id()
	session, ok := a.f.sessions[sessionID]
	if !ok {
		return 0, domain.NewNotFound("session", sessionID)
	}
	session.LastRun = &application.AgentRunStatus{ID: runID, Status: "running"}
	a.f.sessions[sessionID] = session
	return runID, nil
}

func (a runAgentRuntime) GetSession(_ context.Context, sessionID int64) (application.AgentSessionStatus, error) {
	session, ok := a.f.sessions[sessionID]
	if !ok {
		return application.AgentSessionStatus{}, domain.NewNotFound("session", sessionID)
	}
	return session, nil
}

func (a runAgentRuntime) Abort(_ context.Context, sessionID int64, actor int64) error {
	a.f.aborted = append(a.f.aborted, sessionID)
	return nil
}

// runEventSink records published events.
type runEventSink struct {
	f *runFixture
}

func (s runEventSink) Publish(_ context.Context, event application.Event) error {
	s.f.events = append(s.f.events, event)
	return nil
}

// runIDGenerator mints deterministic increasing ids.
type runIDGenerator struct {
	f *runFixture
}

func (g runIDGenerator) Next() int64 {
	return g.f.id()
}

// runClock is a fixed test clock.
type runClock struct {
	f *runFixture
}

func (c runClock) Now() time.Time {
	return c.f.now
}

// runTx implements application.GraphTx over the same fixture.
type runTx struct {
	f *runFixture
}

func (tx runTx) Nodes() application.NodeRepository           { return runNodeRepo{tx.f} }
func (tx runTx) Steps() application.StepRepository           { return runStepRepo{tx.f} }
func (tx runTx) Assertions() application.AssertionRepository { return runAssertionRepo{tx.f} }
func (tx runTx) Sources() application.SourceRepository       { return runSourceRepo{tx.f} }
func (tx runTx) Reviews() application.ReviewRepository       { return runReviewRepo{tx.f} }
func (tx runTx) Findings() application.FindingRepository     { return runFindingRepo{tx.f} }
func (tx runTx) Baselines() application.BaselineRepository   { return runBaselineRepo{tx.f} }
func (tx runTx) Runs() application.RunRepository             { return runRunRepo{tx.f} }

func runStringPtr(value string) *string { return &value }

// TestStartRunSingleFlight covers the per-kind single-flight rule of spec §6.6:
// the first design run starts and the concurrent second one is a run_in_progress
// conflict that writes no row.
func TestStartRunSingleFlight(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: runTestVersionID, Title: "结算页加载", Status: "clarified"}}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("start design run: %v", err)
	}
	if run.Status != domain.RunRunning {
		t.Fatalf("run status = %q, want running", run.Status)
	}
	if run.AgentSessionID == nil || run.AgentRunID == nil {
		t.Fatalf("run agent ids = %v/%v, want both set", run.AgentSessionID, run.AgentRunID)
	}
	if len(fixture.runs) != 1 {
		t.Fatalf("runs stored = %d, want 1", len(fixture.runs))
	}
	if len(fixture.profiles) != 1 || fixture.profiles[0] != domain.ProfileKeyCaseDesign {
		t.Fatalf("profiles = %v, want [%s]", fixture.profiles, domain.ProfileKeyCaseDesign)
	}
	if len(fixture.channels) != 1 || fixture.channels[0] != "version:1" {
		t.Fatalf("channels = %v, want [version:1]", fixture.channels)
	}
	if len(fixture.texts) != 1 || strings.TrimSpace(fixture.texts[0]) == "" {
		t.Fatalf("instruction texts = %d, want one non-empty instruction", len(fixture.texts))
	}

	_, err = service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	requireDomainError(t, err, domain.KindConflict, "run_in_progress")
	if len(fixture.runs) != 1 {
		t.Fatalf("conflicting start stored %d runs, want 1", len(fixture.runs))
	}
}

// TestStartRunValidatesInput covers the start guards: an unknown kind, a
// non-editable version and a version without confirmed requirements are
// rejected before any run row exists.
func TestStartRunValidatesInput(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		mutate   func(*runFixture)
		kind     string
		kindType string
		reason   string
	}{
		{
			name:     "unsupported kind",
			mutate:   func(f *runFixture) { f.confirmed = []application.RequirementSnapshot{{ID: 7, Status: "clarified"}} },
			kind:     domain.RunKindExplore,
			kindType: domain.KindInvalid,
			reason:   "invalid_run_kind",
		},
		{
			name: "executing version",
			mutate: func(f *runFixture) {
				f.version.Status = "executing"
				f.confirmed = []application.RequirementSnapshot{{ID: 7, Status: "clarified"}}
			},
			kind:     domain.RunKindDesign,
			kindType: domain.KindStateConflict,
			reason:   "version_state_conflict",
		},
		{
			name:     "no confirmed requirements",
			mutate:   func(f *runFixture) { f.confirmed = nil },
			kind:     domain.RunKindDesign,
			kindType: domain.KindInvalid,
			reason:   "unconfirmed_requirements",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			fixture := newRunFixture()
			testCase.mutate(fixture)
			service := fixture.service()

			_, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: testCase.kind})
			requireDomainError(t, err, testCase.kindType, testCase.reason)
			if len(fixture.runs) != 0 {
				t.Fatalf("rejected start stored %d runs", len(fixture.runs))
			}
			if len(fixture.texts) != 0 {
				t.Fatalf("rejected start sent %d agent instructions", len(fixture.texts))
			}
		})
	}
}

// TestStartInstructionIncludesRequiredContext covers spec §8.3: the instruction
// carries the version, the confirmed requirements, the relations, the app model
// and code-fact summaries, and the mandatory submission tool of the kind.
func TestStartInstructionIncludesRequiredContext(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{{
		ID: 7, VersionID: runTestVersionID, Title: "结算页加载", Priority: "P1",
		Description: "结算页必须加载订单", Status: "clarified",
	}}
	fixture.relations = []application.RelationSnapshot{{
		ID: 501, VersionID: runTestVersionID, FromRequirementID: 7, ToRequirementID: 8,
		RelationType: "depends_on", Description: "订单依赖结算",
	}}
	fixture.appModels = []application.AppModelSnapshot{{
		ID: 601, SystemID: 5, Key: "page:/checkout", Kind: "page", Name: "结算页", Path: "/checkout",
	}}
	fixture.facts = []application.FactSummary{{
		Kind: "route", Key: "route:/api/order", Path: "src/order/route.ts", Name: "POST /api/order",
	}}
	fixture.seedCaseNode(11, 3, "已存在用例")
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("start design run: %v", err)
	}
	instruction := fixture.texts[0]
	for _, want := range []string{
		"P5 payment", "designing", "7", "结算页加载", "depends_on", "订单依赖结算",
		"page:/checkout", "route:/api/order", "已存在用例",
		fmt.Sprintf("runId=%d", run.ID),
		"已确认需求", "需求关系", "应用模型", "代码事实", "现有脑图",
		"case.graph_propose", "提交",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("instruction is missing %q:\n%s", want, instruction)
		}
	}
	if len(instruction) > 64*1024 {
		t.Fatalf("instruction is %d bytes, want at most 64 KiB", len(instruction))
	}
}

// TestInstructionTruncatedOverBudget covers spec §8.3: an oversized context is
// truncated to the 64 KiB budget and the run stats record contextTruncated.
func TestInstructionTruncatedOverBudget(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: runTestVersionID, Title: "结算页加载", Status: "clarified"}}
	for index := 0; index < 100; index++ {
		fixture.facts = append(fixture.facts, application.FactSummary{
			Kind:   "file",
			Key:    fmt.Sprintf("file:%d", index),
			Path:   fmt.Sprintf("src/module/%d.ts", index),
			Name:   fmt.Sprintf("module %d", index),
			Detail: strings.Repeat("z", 900),
		})
	}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDataPlan})
	if err != nil {
		t.Fatalf("start data plan run: %v", err)
	}
	instruction := fixture.texts[0]
	if len(instruction) > 64*1024 {
		t.Fatalf("instruction is %d bytes, want at most 64 KiB", len(instruction))
	}
	if truncated, _ := runStats(t, run)["contextTruncated"].(bool); !truncated {
		t.Fatalf("stats = %s, want contextTruncated=true", string(run.Stats))
	}
	if !strings.Contains(instruction, "case.data_plan_submit") {
		t.Fatalf("instruction is missing the mandatory submission tool:\n%s", instruction)
	}
}

// TestStartRunFailureMarksRunFailed covers the start compensation of spec §8.5:
// when the agent session/text start fails, the queued run becomes failed with
// reason ai_unavailable (releasing the single-flight slot), the orphan session is
// aborted and the error is returned.
func TestStartRunFailureMarksRunFailed(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	fixture.startTextErr = errors.New("llm unavailable")
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	requireDomainError(t, err, domain.KindConflict, "ai_unavailable")
	if run.ID == 0 {
		t.Fatal("a failed start must still return the persisted run")
	}
	stored, ok := fixture.runs[run.ID]
	if !ok {
		t.Fatalf("run %d was not persisted", run.ID)
	}
	if stored.Status != domain.RunFailed || stored.IsActive() {
		t.Fatalf("stored run status = %q, want failed and not active", stored.Status)
	}
	if reason, _ := runErrors(t, stored); reason != "ai_unavailable" {
		t.Fatalf("stored run reason = %q, want ai_unavailable", reason)
	}
	if len(fixture.aborted) != 1 {
		t.Fatalf("aborted sessions = %v, want the orphan session aborted", fixture.aborted)
	}

	fixture.startTextErr = nil
	if _, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign}); err != nil {
		t.Fatalf("the failed run must release the single-flight slot: %v", err)
	}
}

// TestRunFailedWhenAgentAborts covers the worker terminal-agent transition of
// spec §6.6/§8.4: an aborted agent run fails the design run without writing any
// graph row.
func TestRunFailedWhenAgentAborts(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: runTestVersionID, Status: "clarified"}}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	if err != nil {
		t.Fatalf("start design run: %v", err)
	}
	session, ok := fixture.sessions[*run.AgentSessionID]
	if !ok {
		t.Fatalf("agent session %d was not created", *run.AgentSessionID)
	}
	session.LastRun.Status = "aborted"
	fixture.sessions[*run.AgentSessionID] = session

	if err := service.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", updated.Status)
	}
	reason, _ := runErrors(t, updated)
	if reason != "agent_aborted" {
		t.Fatalf("run reason = %q, want agent_aborted", reason)
	}
	if len(fixture.nodes) != 0 || fixture.eventCount(domain.EventCaseCreated) != 0 {
		t.Fatalf("aborted run wrote %d node(s) / %d case.created event(s), want none",
			len(fixture.nodes), fixture.eventCount(domain.EventCaseCreated))
	}
}

// TestRunningAgentTimeoutAborts covers spec §8.4: a run past started_at +
// HardTimeout + 60s is aborted and failed with reason agent_timeout.
func TestRunningAgentTimeoutAborts(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
	started := fixture.now.Add(-3 * time.Minute)
	run.StartedAt = &started
	fixture.runs[run.ID] = run
	service := fixture.service()

	if err := service.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", updated.Status)
	}
	if reason, _ := runErrors(t, updated); reason != "agent_timeout" {
		t.Fatalf("run reason = %q, want agent_timeout", reason)
	}
	if len(fixture.aborted) != 1 || fixture.aborted[0] != *run.AgentSessionID {
		t.Fatalf("aborted sessions = %v, want [%d]", fixture.aborted, *run.AgentSessionID)
	}
}

// TestQueuedRunStartTimeout covers spec §8.4: a run still queued after 60s is
// failed with reason agent_start_timeout.
func TestQueuedRunStartTimeout(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	created := fixture.now.Add(-61 * time.Second)
	run := domain.CaseDesignRun{
		ID:        fixture.id(),
		VersionID: runTestVersionID,
		Kind:      domain.RunKindDesign,
		Status:    domain.RunQueued,
		CreatedBy: 42,
		CreatedAt: created,
		UpdatedAt: created,
	}
	fixture.runs[run.ID] = run
	service := fixture.service()

	if err := service.ProcessOnce(ctx); err != nil {
		t.Fatalf("process once: %v", err)
	}
	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", updated.Status)
	}
	if reason, _ := runErrors(t, updated); reason != "agent_start_timeout" {
		t.Fatalf("run reason = %q, want agent_start_timeout", reason)
	}
}

// TestStaleRunRecovered covers the startup recovery of spec §6.6: the service
// delegates the now-10m cutoff to the repository and reports the count.
func TestStaleRunRecovered(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	run := fixture.seedRunningRun(domain.RunKindDesign, runValidDesignPayload())
	stale := fixture.now.Add(-11 * time.Minute)
	run.UpdatedAt = stale
	fixture.runs[run.ID] = run
	service := fixture.service()

	recovered, err := service.RecoverStale(ctx)
	if err != nil {
		t.Fatalf("recover stale: %v", err)
	}
	if recovered != 1 {
		t.Fatalf("recovered = %d, want 1", recovered)
	}
	updated := fixture.runs[run.ID]
	if updated.Status != domain.RunFailed {
		t.Fatalf("run status = %q, want failed", updated.Status)
	}
	if reason, _ := runErrors(t, updated); reason != "worker_restart" {
		t.Fatalf("run reason = %q, want worker_restart", reason)
	}
}

// TestWorkerStopsWithoutLeakingGoroutines covers the in-process worker lifecycle:
// Worker starts the 2s poll loop, Stop terminates it promptly and is idempotent.
func TestWorkerStopsWithoutLeakingGoroutines(t *testing.T) {
	fixture := newRunFixture()
	service := fixture.service()

	service.Worker(context.Background())
	done := make(chan struct{})
	go func() {
		service.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop did not terminate the worker loop")
	}
	service.Stop()
}

// TestStartReviewRunAllowedAfterFreeze covers spec §6.4 rule 3: a read-only
// review run may start on an executing (frozen) version, while design and
// data-plan runs stay version_state_conflict.
func TestStartReviewRunAllowedAfterFreeze(t *testing.T) {
	ctx := context.Background()
	fixture := newRunFixture()
	fixture.version.Status = "executing"
	fixture.confirmed = []application.RequirementSnapshot{{ID: 7, VersionID: runTestVersionID, Status: "clarified"}}
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindReview})
	if err != nil {
		t.Fatalf("start review run on a frozen version: %v", err)
	}
	if run.Kind != domain.RunKindReview || run.Status != domain.RunRunning {
		t.Fatalf("run = %+v, want a running review run", run)
	}
	if len(fixture.profiles) != 1 || fixture.profiles[0] != domain.ProfileKeyCaseReview {
		t.Fatalf("profiles = %v, want [%s]", fixture.profiles, domain.ProfileKeyCaseReview)
	}

	for _, kind := range []string{domain.RunKindDesign, domain.RunKindDataPlan} {
		_, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: kind})
		requireDomainError(t, err, domain.KindStateConflict, "version_state_conflict")
	}
}

// TestStartRunRunningCASFailureAbortsSession covers the queued→running CAS
// failure: the coordinator keeps the pre-update run (non-zero id/status), aborts
// the just-created agent session and marks the persisted row failed instead of
// losing it.
func TestStartRunRunningCASFailureAbortsSession(t *testing.T) {
	ctx := context.Background()
	fixture := newDesignFixture()
	fixture.runningCASErr = errors.New("run was moved by a concurrent worker")
	service := fixture.service()

	run, err := service.Start(ctx, runTestVersionID, 42, application.StartRunCommand{Kind: domain.RunKindDesign})
	requireDomainError(t, err, domain.KindConflict, "ai_unavailable")
	if run.ID == 0 {
		t.Fatal("a failed queued→running CAS must not return a zero run")
	}
	stored, ok := fixture.runs[run.ID]
	if !ok {
		t.Fatalf("run %d was lost", run.ID)
	}
	if stored.Status != domain.RunFailed || stored.IsActive() {
		t.Fatalf("stored run status = %q, want failed and not active", stored.Status)
	}
	if reason, _ := runErrors(t, stored); reason != "ai_unavailable" {
		t.Fatalf("stored run reason = %q, want ai_unavailable", reason)
	}
	if len(fixture.aborted) != 1 {
		t.Fatalf("aborted sessions = %v, want the orphan session aborted", fixture.aborted)
	}
	if _, ok := fixture.sessions[fixture.aborted[0]]; !ok {
		t.Fatalf("aborted session %d was never created", fixture.aborted[0])
	}
}
