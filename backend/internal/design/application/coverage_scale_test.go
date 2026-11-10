package designapp

import (
	"context"
	"fmt"
	"strings"
	"testing"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// Task 79 scaling fixtures: the live S3 version imported 67 V1 requirements
// (29 P0, 31 P1, 7 P2) into one design round and then starved them: the
// design.requirement tasks ran with the wrong priority, the repair loop minted
// 199 authoring generations, and only one requirement ever got cases.
const (
	task79RequirementCount = 67
	task79P0Count          = 29
	task79VersionID        = 1001
	task79ConversationID   = 9001
	task79RoundID          = 8001
)

// task79RequirementRefs builds the 67 V1 requirements in the import order of
// the live version: P0 requirements first, then P1/P2 in their original order.
// The returned IDs keep that order so a FIFO claim is the starvation shape.
func task79RequirementRefs() []design.RequirementRef {
	refs := make([]design.RequirementRef, 0, task79RequirementCount)
	for index := 0; index < task79RequirementCount; index++ {
		ref := plannerRequirement(int64(3000+index*2), int64(3001+index*2))
		switch {
		case index < task79P0Count:
			ref.Priority = "P0"
		case index%3 == 0:
			ref.Priority = "P2"
		default:
			ref.Priority = "P1"
		}
		refs = append(refs, ref)
	}
	return refs
}

func task79RequirementIDs(refs []design.RequirementRef) []int64 {
	ids := make([]int64, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.ID)
	}
	return ids
}

func task79ActiveScopes(refs []design.RequirementRef) []design.Scope {
	roundID := int64(task79RoundID)
	scopes := make([]design.Scope, 0, len(refs))
	for index, ref := range refs {
		scopes = append(scopes, design.Scope{
			ID:                 int64(6000 + index),
			VersionID:          task79VersionID,
			RequirementID:      ref.ID,
			ActivatedByRoundID: &roundID,
			Status:             design.ScopeStatusActive,
		})
	}
	return scopes
}

// task79Coverage marks every requirement covered or uncovered. A covered
// requirement has one scenario and one current draft case, the shape the design
// convergence gate accepts as usable coverage.
func task79Coverage(refs []design.RequirementRef, uncovered map[int64]bool) []design.RequirementCoverage {
	coverage := make([]design.RequirementCoverage, 0, len(refs))
	for _, ref := range refs {
		item := design.RequirementCoverage{Requirement: ref}
		if !uncovered[ref.ID] {
			item.ScenarioCount = 1
			item.DraftCount = 1
		}
		coverage = append(coverage, item)
	}
	return coverage
}

func task79UncoveredSet(refs []design.RequirementRef) map[int64]bool {
	uncovered := make(map[int64]bool, len(refs))
	for _, ref := range refs {
		uncovered[ref.ID] = true
	}
	return uncovered
}

func task79MarkAuthoringTasksDone(enqueuer *fakeEnqueuer) {
	for _, task := range enqueuer.byType(TaskTypeDesignRequirement) {
		enqueuer.statuses[task.DedupeKey] = "done"
	}
}

func task79TasksByGeneration(enqueuer *fakeEnqueuer, generation int) []TaskInput {
	matches := make([]TaskInput, 0)
	for _, task := range enqueuer.byType(TaskTypeDesignRequirement) {
		switch {
		case generation == 0 && !strings.Contains(task.DedupeKey, ":repair:"):
			matches = append(matches, task)
		case generation > 0 && strings.HasSuffix(task.DedupeKey, fmt.Sprintf(":repair:%d", generation)):
			matches = append(matches, task)
		}
	}
	return matches
}

func task79RequirementPriority(t *testing.T, refs []design.RequirementRef, requirementID string) string {
	t.Helper()
	for _, ref := range refs {
		if fmt.Sprint(ref.ID) == requirementID {
			return ref.Priority
		}
	}
	t.Fatalf("scope task references unknown requirement %s", requirementID)
	return ""
}

// TestDesignCoversAllP0Requirements pins the 67-requirement scaling contract:
// every confirmed requirement (P0 29/29 included) receives exactly one
// traceable authoring task, P0 tasks are claimed before every lower priority,
// no design.round work item is enqueued (D-04: its v0.22 runner profile is
// gone, so it could only fail with invalid_work_type), and convergence still
// requires real coverage for every requirement. Before the fix all tasks shared
// priority 0 (FIFO by requirement import order) and the round payload carried
// all 67 ids, so the live round burned its budget on a scope no single run can
// serve.
func TestDesignCoversAllP0Requirements(t *testing.T) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	refs := task79RequirementRefs()
	repo.clarified = refs

	ctx := context.Background()
	if _, err := planner.StartStageRound(ctx, task79VersionID, task79RequirementIDs(refs)); err != nil {
		t.Fatalf("StartStageRound: %v", err)
	}

	tasks := enqueuer.byType(TaskTypeDesignRequirement)
	if len(tasks) != task79RequirementCount {
		t.Fatalf("authoring tasks = %d, want one per V1 requirement (%d)", len(tasks), task79RequirementCount)
	}
	seen := make(map[string]bool, len(tasks))
	byPriority := make(map[string]int)
	p0Tasks := 0
	for _, task := range tasks {
		requirementID := taskPayloadString(task, "requirement_id")
		requirementVersionID := taskPayloadString(task, "requirement_version_id")
		if requirementID == "" || requirementVersionID == "" {
			t.Fatalf("authoring task is not traceable: %#v", task.Payload)
		}
		key := requirementID + ":" + requirementVersionID
		if seen[key] {
			t.Fatalf("requirement %s got duplicate authoring tasks", requirementID)
		}
		seen[key] = true
		priority := task79RequirementPriority(t, refs, requirementID)
		if previous, ok := byPriority[priority]; ok && previous != task.Priority {
			t.Fatalf("requirement priority %s maps to inconsistent claim priorities %d and %d",
				priority, previous, task.Priority)
		}
		byPriority[priority] = task.Priority
		if priority == "P0" {
			p0Tasks++
		}
		if taskPayloadString(task, "conversation_id") == "" || taskPayloadString(task, "round_id") == "" {
			t.Fatalf("authoring task %s lost its conversation/round association: %#v", key, task.Payload)
		}
	}
	if p0Tasks != task79P0Count {
		t.Fatalf("P0 authoring tasks = %d, want %d", p0Tasks, task79P0Count)
	}
	if !(byPriority["P0"] > byPriority["P1"] && byPriority["P1"] > byPriority["P2"]) {
		t.Fatalf("claim priorities P0/P1/P2 = %d/%d/%d, want strict P0 > P1 > P2",
			byPriority["P0"], byPriority["P1"], byPriority["P2"])
	}

	// D-04: the one-run design.round authoring item is gone with its v0.22
	// runner profile. Every requirement is authored by its own
	// design.requirement task above; a round item could only fail.
	assertNoDesignRoundTasks(t, enqueuer)

	// Convergence still requires real, traceable coverage for the whole scope.
	converger := &fakeDesignConverger{}
	planner.SetDesignConverger(converger)
	// Every requirement is covered except one P0 requirement, whose authoring
	// run finished without writing a case.
	repo.coverage = task79Coverage(refs, map[int64]bool{refs[0].ID: true})
	// The first authoring generation already finished without writing the last
	// P0 case: the reconciler must request a repair for exactly that P0
	// requirement, keeping its P0 claim priority.
	task79MarkAuthoringTasksDone(enqueuer)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile with one uncovered P0: %v", err)
	}
	if len(converger.versionIDs) != 0 {
		t.Fatalf("uncovered P0 requirement converged the version: %v", converger.versionIDs)
	}
	repairs := task79TasksByGeneration(enqueuer, 1)
	if len(repairs) != 1 {
		t.Fatalf("repair generations = %d, want one for the uncovered P0", len(repairs))
	}
	if got := repairs[0].Priority; got != byPriority["P0"] {
		t.Fatalf("P0 repair priority = %d, want %d", got, byPriority["P0"])
	}

	// Once the last P0 requirement has a real case, the same reconciler
	// converges the version exactly once and the P0 requirement is covered.
	repo.coverage = task79Coverage(refs, nil)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after full coverage: %v", err)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != task79VersionID {
		t.Fatalf("converged versions = %v, want [%d]", converger.versionIDs, task79VersionID)
	}
	covered := 0
	for _, item := range repo.coverage {
		if item.ScenarioCount > 0 && (item.DraftCount > 0 || item.ConfirmedCount > 0) && item.StaleCount == 0 {
			covered++
		}
	}
	if covered != task79RequirementCount {
		t.Fatalf("covered requirements = %d, want all %d (P0 29/29)", covered, task79RequirementCount)
	}
}

// TestDesignRequirementQueueDrainsBounded pins the queue-drain contract behind
// the 174 design.* work items / 62 open snapshot: one in-flight authoring
// generation per requirement at any time, at most the bounded repair
// generations, and a finite queue that drains as soon as coverage lands. It
// also keeps P0 priority on every repair generation, so a full repair wave
// cannot starve the critical requirements it is repairing.
func TestDesignRequirementQueueDrainsBounded(t *testing.T) {
	planner, repo, enqueuer, _, _, _, _ := newPlannerFixture()
	refs := task79RequirementRefs()
	repo.clarified = refs
	repo.hasConversation = true
	repo.latestConversation = task79ConversationID
	repo.rounds = []design.Round{{
		ID: task79RoundID, VersionID: task79VersionID, ConversationID: task79ConversationID,
		Activation:     design.ActivationAutoNew,
		RequirementIDs: task79RequirementIDs(refs),
		RequirementVersionIDs: func() []int64 {
			ids := make([]int64, 0, len(refs))
			for _, ref := range refs {
				ids = append(ids, ref.CurrentVersionID)
			}
			return ids
		}(),
		Status: design.RoundStatusActive,
	}}
	repo.activeScopes = task79ActiveScopes(refs)
	uncovered := task79UncoveredSet(refs)
	repo.coverage = task79Coverage(refs, uncovered)
	converger := &fakeDesignConverger{}
	planner.SetDesignConverger(converger)
	ctx := context.Background()

	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("first Reconcile: %v", err)
	}
	if got := len(task79TasksByGeneration(enqueuer, 0)); got != task79RequirementCount {
		t.Fatalf("first authoring generation = %d tasks, want %d", got, task79RequirementCount)
	}
	// Repeating the scan while the generation is open must not stampede it.
	firstCount := len(enqueuer.byType(TaskTypeDesignRequirement))
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("second Reconcile: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignRequirement)); got != firstCount {
		t.Fatalf("open generation was duplicated: %d -> %d tasks", firstCount, got)
	}

	// The model finishes every first-generation author without coverage: the
	// planner may ask for one bounded repair generation, never more.
	task79MarkAuthoringTasksDone(enqueuer)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after the first generation: %v", err)
	}
	if got := len(task79TasksByGeneration(enqueuer, 1)); got != task79RequirementCount {
		t.Fatalf("repair generation 1 = %d tasks, want %d", got, task79RequirementCount)
	}
	if got := len(enqueuer.byType(TaskTypeDesignRequirement)); got != task79RequirementCount*2 {
		t.Fatalf("queue after one repair = %d tasks, want %d", got, task79RequirementCount*2)
	}
	assertTask79P0Priority(t, refs, enqueuer.byType(TaskTypeDesignRequirement))

	task79MarkAuthoringTasksDone(enqueuer)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after the second generation: %v", err)
	}
	if got := len(task79TasksByGeneration(enqueuer, 2)); got != task79RequirementCount {
		t.Fatalf("repair generation 2 = %d tasks, want %d", got, task79RequirementCount)
	}

	// The bounded generation budget stops the queue from growing forever.
	task79MarkAuthoringTasksDone(enqueuer)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after the generation budget: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignRequirement)); got != task79RequirementCount*3 {
		t.Fatalf("queue after the bounded generations = %d tasks, want %d",
			got, task79RequirementCount*3)
	}

	// Coverage arrives: the queue drains (no new authoring generation) and the
	// version converges exactly once.
	repo.coverage = task79Coverage(refs, nil)
	if err := planner.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile after coverage: %v", err)
	}
	if got := len(enqueuer.byType(TaskTypeDesignRequirement)); got != task79RequirementCount*3 {
		t.Fatalf("covered requirements requested another generation: %d tasks", got)
	}
	if len(converger.versionIDs) != 1 || converger.versionIDs[0] != task79VersionID {
		t.Fatalf("converged versions = %v, want [%d]", converger.versionIDs, task79VersionID)
	}
}

func assertTask79P0Priority(t *testing.T, refs []design.RequirementRef, tasks []TaskInput) {
	t.Helper()
	byID := make(map[string]string, len(refs))
	for _, ref := range refs {
		byID[fmt.Sprint(ref.ID)] = ref.Priority
	}
	byPriority := make(map[string]int)
	for _, task := range tasks {
		requirementID := taskPayloadString(task, "requirement_id")
		priority, ok := byID[requirementID]
		if !ok {
			continue
		}
		if previous, seen := byPriority[priority]; seen && previous != task.Priority {
			t.Fatalf("requirement priority %s maps to inconsistent claim priorities %d and %d",
				priority, previous, task.Priority)
		}
		byPriority[priority] = task.Priority
	}
	if !(byPriority["P0"] > byPriority["P1"] && byPriority["P1"] > byPriority["P2"]) {
		t.Fatalf("claim priorities P0/P1/P2 = %d/%d/%d, want strict P0 > P1 > P2 (P0 requirements are starved)",
			byPriority["P0"], byPriority["P1"], byPriority["P2"])
	}
}
