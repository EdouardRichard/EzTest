package designapp

import (
	"context"
	"sort"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
)

// fakeIDs is a deterministic generator for service tests.
type fakeIDs struct {
	next int64
}

func (g *fakeIDs) Next() int64 {
	if g.next == 0 {
		g.next = 1000
	}
	g.next++
	return g.next
}

type fixedClock struct {
	now time.Time
}

func (c fixedClock) Now() time.Time { return c.now }

func newFixedClock() fixedClock {
	return fixedClock{now: time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)}
}

type fakePublisher struct {
	events []eventingapp.Event
}

func (p *fakePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.events = append(p.events, event)
	return nil
}

func (p *fakePublisher) types() []string {
	types := make([]string, 0, len(p.events))
	for _, event := range p.events {
		types = append(types, event.Type)
	}
	return types
}

func (p *fakePublisher) byType(eventType string) []eventingapp.Event {
	matches := make([]eventingapp.Event, 0)
	for _, event := range p.events {
		if event.Type == eventType {
			matches = append(matches, event)
		}
	}
	return matches
}

type fakeAudit struct {
	events []auditapp.Event
}

func (a *fakeAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return nil
}

// fakeRepo is an in-memory design.Repository. Only the graph/propose methods are
// implemented; unused interface methods are satisfied by the embedded nil
// interface and panic if ever called by mistake.
type fakeRepo struct {
	design.Repository

	version     design.VersionContext
	requirement design.RequirementRef

	scenarios []design.Scenario
	cases     map[int64]*fakeCase

	lockCalls int
	graph     design.GraphData
	caseData  design.CaseData
	overview  design.OverviewData

	pending int

	proposeErr error

	confirmations []design.Confirmation
	staleVersions []int64
	staleEntities []int64
	dataPlanErr   error
	latestRound   *design.Round
}

type fakeCase struct {
	entity   design.CaseEntity
	current  *design.CaseVersion
	versions []design.CaseVersion
}

// versions returns the case version history in ascending revision order.
func (f *fakeRepo) versions(caseID int64) []design.CaseVersion {
	item, ok := f.cases[caseID]
	if !ok {
		return nil
	}
	copied := make([]design.CaseVersion, len(item.versions))
	copy(copied, item.versions)
	sort.SliceStable(copied, func(i, j int) bool { return copied[i].Revision < copied[j].Revision })
	return copied
}

func newFakeRepo() *fakeRepo {
	return &fakeRepo{
		cases: map[int64]*fakeCase{},
		version: design.VersionContext{
			VersionID:          1001,
			Status:             "initial",
			AllowedVersionSUTs: []int64{9001, 9002},
			PageToSUT:          map[int64]int64{6001: 9001, 6002: 9002},
			APIToSUT:           map[int64]int64{4001: 9001, 4002: 9002},
			DBTOSUT:            map[int64]int64{8001: 9002},
		},
		requirement: design.RequirementRef{
			ID:               3001,
			VersionID:        1001,
			CurrentVersionID: 3002,
			Revision:         1,
			Title:            "登录",
			Status:           "clarified",
		},
	}
}

func (f *fakeRepo) VersionContext(_ context.Context, versionID int64) (design.VersionContext, error) {
	if versionID != f.version.VersionID {
		return design.VersionContext{}, design.ErrNotFound
	}
	return f.version, nil
}

func (f *fakeRepo) RequirementContext(_ context.Context, versionID, requirementID int64) (design.RequirementRef, error) {
	if versionID != f.requirement.VersionID || requirementID != f.requirement.ID {
		return design.RequirementRef{}, design.ErrNotFound
	}
	return f.requirement, nil
}

func (f *fakeRepo) ReadGraph(_ context.Context, versionID int64) (design.GraphData, error) {
	graph := f.graph
	graph.VersionID = versionID
	graph.PendingDesignCount = f.pending
	graph.AutoDesignNewRequirements = f.version.AutoDesignNewRequirements
	requirements := make([]design.RequirementGraph, 0)
	if f.requirement.ID > 0 {
		requirementGraph := design.RequirementGraph{
			Requirement:  f.requirement,
			DesignStatus: "in_design",
			Scenarios:    []design.ScenarioGraph{},
		}
		for _, scenario := range f.scenarios {
			scenarioGraph := design.ScenarioGraph{Scenario: scenario, Cases: []design.CaseGraph{}}
			for _, item := range f.sortedCases() {
				if item.entity.ScenarioID != scenario.ID || item.current == nil {
					continue
				}
				scenarioGraph.Cases = append(scenarioGraph.Cases, design.CaseGraph{
					Case: item.entity, Current: *item.current, StepCount: len(item.current.Steps),
				})
			}
			requirementGraph.Scenarios = append(requirementGraph.Scenarios, scenarioGraph)
		}
		requirements = append(requirements, requirementGraph)
	}
	graph.Requirements = requirements
	graph.Stats = design.GraphStats{Requirements: len(requirements)}
	for _, requirement := range requirements {
		for _, scenario := range requirement.Scenarios {
			graph.Stats.Scenarios++
			for _, item := range scenario.Cases {
				graph.Stats.Cases++
				switch item.Current.Status {
				case design.CaseStatusDraft:
					graph.Stats.Draft++
				case design.CaseStatusConfirmed:
					graph.Stats.Confirmed++
				case design.CaseStatusStale:
					graph.Stats.Stale++
				}
			}
		}
	}
	return graph, nil
}

func (f *fakeRepo) ReadCase(_ context.Context, versionID, caseID int64, caseVersionID *int64) (design.CaseData, error) {
	if f.caseData.Case.ID != 0 {
		return f.caseData, nil
	}
	item, ok := f.cases[caseID]
	if !ok {
		return design.CaseData{}, design.ErrNotFound
	}
	data := design.CaseData{
		Case:        item.entity,
		Scenario:    design.Scenario{ID: item.entity.ScenarioID},
		Requirement: f.requirement,
	}
	if item.current != nil {
		data.Selected = *item.current
	}
	return data, nil
}

func (f *fakeRepo) Overview(_ context.Context, versionID int64) (design.OverviewData, error) {
	overview := f.overview
	overview.PendingDesignCount = f.pending
	return overview, nil
}

func (f *fakeRepo) WithRequirementLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.Tx) error) error {
	f.lockCalls++
	if f.proposeErr != nil {
		return f.proposeErr
	}
	if versionID != f.version.VersionID || requirementID != f.requirement.ID {
		return design.ErrNotFound
	}
	return fn(ctx, &fakeTx{repo: f})
}

func (f *fakeRepo) sortedCases() []*fakeCase {
	ids := make([]int64, 0, len(f.cases))
	for id := range f.cases {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	items := make([]*fakeCase, 0, len(ids))
	for _, id := range ids {
		items = append(items, f.cases[id])
	}
	return items
}

// fakeTx applies proposal writes to the in-memory repository.
type fakeTx struct {
	repo *fakeRepo
}

func (t *fakeTx) RequirementSnapshot(_ context.Context, versionID, requirementID, requirementVersionID int64) (design.RequirementSnapshot, error) {
	snapshot := design.RequirementSnapshot{RequirementVersionID: requirementVersionID}
	scenarioIDs := map[int64]struct{}{}
	for _, scenario := range t.repo.scenarios {
		if scenario.VersionID == versionID && scenario.RequirementID == requirementID &&
			scenario.RequirementVersionID == requirementVersionID {
			snapshot.Scenarios = append(snapshot.Scenarios, scenario)
			scenarioIDs[scenario.ID] = struct{}{}
		}
	}
	for _, item := range t.repo.sortedCases() {
		if _, ok := scenarioIDs[item.entity.ScenarioID]; !ok {
			continue
		}
		copied := item.entity
		snapshot.Cases = append(snapshot.Cases, design.CaseWithCurrent{Case: copied, Current: item.current})
	}
	return snapshot, nil
}

func (t *fakeTx) InsertScenario(_ context.Context, scenario design.Scenario) (design.Scenario, error) {
	t.repo.scenarios = append(t.repo.scenarios, scenario)
	return scenario, nil
}

func (t *fakeTx) UpdateScenario(_ context.Context, scenario design.Scenario) (design.Scenario, error) {
	for index := range t.repo.scenarios {
		if t.repo.scenarios[index].ID == scenario.ID {
			t.repo.scenarios[index] = scenario
			return scenario, nil
		}
	}
	return design.Scenario{}, design.ErrNotFound
}

func (t *fakeTx) InsertCase(_ context.Context, entity design.CaseEntity) error {
	t.repo.cases[entity.ID] = &fakeCase{entity: entity}
	return nil
}

func (t *fakeTx) InsertCaseVersion(_ context.Context, version design.CaseVersion) (design.CaseVersion, error) {
	if version.Status == "" {
		version.Status = design.CaseStatusDraft
	}
	stored := version
	item, ok := t.repo.cases[version.CaseID]
	if !ok {
		return design.CaseVersion{}, design.ErrNotFound
	}
	item.current = &stored
	item.versions = append(item.versions, stored)
	return stored, nil
}

func (t *fakeTx) SetCurrentVersion(_ context.Context, caseID, caseVersionID int64, status string, at time.Time) error {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.ErrNotFound
	}
	item.entity.CurrentVersionID = &caseVersionID
	item.entity.Status = status
	item.entity.UpdatedAt = at
	if item.current != nil {
		item.current.ID = caseVersionID
	}
	return nil
}
