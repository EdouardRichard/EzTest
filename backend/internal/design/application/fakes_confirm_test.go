package designapp

import (
	"context"
	"fmt"
	"sort"
	"testing"
	"time"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// fakeTeamRoom records review/team posts.
type fakeTeamRoom struct {
	posts []TeamPost
}

func (f *fakeTeamRoom) Post(_ context.Context, post TeamPost) error {
	f.posts = append(f.posts, post)
	return nil
}

func (f *fakeRepo) ResolveConfirmationTarget(_ context.Context, versionID int64, scope string, targetID int64) (design.ConfirmationTarget, error) {
	target := design.ConfirmationTarget{Scope: scope, TargetID: targetID}
	if versionID != f.version.VersionID {
		return design.ConfirmationTarget{}, design.ErrNotFound
	}
	switch scope {
	case design.ConfirmScopeCase:
		item, ok := f.cases[targetID]
		if !ok {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		scenario, ok := f.scenarioByID(item.entity.ScenarioID)
		if !ok {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		target.CaseID = targetID
		target.ScenarioID = scenario.ID
		target.RequirementID = scenario.RequirementID
	case design.ConfirmScopeScenario:
		scenario, ok := f.scenarioByID(targetID)
		if !ok {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		target.ScenarioID = scenario.ID
		target.RequirementID = scenario.RequirementID
	case design.ConfirmScopeRequirement:
		if targetID != f.requirement.ID {
			return design.ConfirmationTarget{}, design.ErrNotFound
		}
		target.RequirementID = targetID
	default:
		return design.ConfirmationTarget{}, design.ErrNotFound
	}
	return target, nil
}

func (f *fakeRepo) scenarioByID(scenarioID int64) (design.Scenario, bool) {
	for _, scenario := range f.scenarios {
		if scenario.ID == scenarioID {
			return scenario, true
		}
	}
	return design.Scenario{}, false
}

func (f *fakeRepo) WithConfirmationLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.ConfirmationTx) error) error {
	if versionID != f.version.VersionID || requirementID != f.requirement.ID {
		return design.ErrNotFound
	}
	return fn(ctx, &fakeConfirmationTx{repo: f})
}

type fakeConfirmationTx struct {
	repo *fakeRepo
}

func (t *fakeConfirmationTx) Requirement(context.Context) (design.RequirementRef, error) {
	return t.repo.requirement, nil
}

func (t *fakeConfirmationTx) Scenario(_ context.Context, scenarioID int64) (design.Scenario, error) {
	if scenario, ok := t.repo.scenarioByID(scenarioID); ok {
		return scenario, nil
	}
	return design.Scenario{}, design.ErrNotFound
}

func (t *fakeConfirmationTx) Case(_ context.Context, caseID int64) (design.CaseWithCurrent, error) {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.CaseWithCurrent{}, design.ErrNotFound
	}
	return t.repo.caseWithCurrent(item), nil
}

func (t *fakeConfirmationTx) CasesByScenario(_ context.Context, scenarioID int64) ([]design.CaseWithCurrent, error) {
	items := make([]design.CaseWithCurrent, 0)
	for _, item := range t.repo.sortedCases() {
		if item.entity.ScenarioID == scenarioID {
			items = append(items, t.repo.caseWithCurrent(item))
		}
	}
	return items, nil
}

func (t *fakeConfirmationTx) CasesByRequirement(context.Context) ([]design.CaseWithCurrent, error) {
	items := make([]design.CaseWithCurrent, 0)
	for _, item := range t.repo.sortedCases() {
		scenario, ok := t.repo.scenarioByID(item.entity.ScenarioID)
		if !ok || scenario.RequirementID != t.repo.requirement.ID {
			continue
		}
		items = append(items, t.repo.caseWithCurrent(item))
	}
	return items, nil
}

func (f *fakeRepo) caseWithCurrent(item *fakeCase) design.CaseWithCurrent {
	copied := item.entity
	var current *design.CaseVersion
	if item.current != nil {
		versionCopy := *item.current
		current = &versionCopy
	}
	return design.CaseWithCurrent{Case: copied, Current: current}
}

func (t *fakeConfirmationTx) ConfirmCaseVersion(_ context.Context, command design.ConfirmCaseVersionCommand) error {
	item := t.repo.caseByVersionID(command.Confirmation.CaseVersionID)
	if item == nil || item.current == nil {
		return design.ErrNotFound
	}
	if item.current.Status != design.CaseStatusDraft {
		return design.ErrStateConflict
	}
	item.current.Status = design.CaseStatusConfirmed
	for index := range item.versions {
		if item.versions[index].ID == command.Confirmation.CaseVersionID {
			item.versions[index].Status = design.CaseStatusConfirmed
		}
	}
	item.entity.Status = command.EntityStatus
	t.repo.confirmations = append(t.repo.confirmations, command.Confirmation)
	return nil
}

func (t *fakeConfirmationTx) SupersededVersionIDs(_ context.Context, caseID, keepVersionID int64) ([]int64, error) {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return nil, design.ErrNotFound
	}
	ids := make([]int64, 0)
	for _, version := range item.versions {
		if version.ID != keepVersionID &&
			(version.Status == design.CaseStatusConfirmed || version.Status == design.CaseStatusStale) {
			ids = append(ids, version.ID)
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return ids, nil
}

func (t *fakeConfirmationTx) SupersedeVersions(_ context.Context, caseID, keepVersionID int64, _ time.Time) error {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.ErrNotFound
	}
	for index := range item.versions {
		version := &item.versions[index]
		if version.ID != keepVersionID &&
			(version.Status == design.CaseStatusConfirmed || version.Status == design.CaseStatusStale) {
			version.Status = design.CaseStatusSuperseded
		}
	}
	return nil
}

func (t *fakeRepo) caseByVersionID(caseVersionID int64) *fakeCase {
	for _, item := range t.cases {
		if item.current != nil && item.current.ID == caseVersionID {
			return item
		}
		for _, version := range item.versions {
			if version.ID == caseVersionID {
				return item
			}
		}
	}
	return nil
}

func (f *fakeRepo) WithStaleLock(ctx context.Context, versionID, requirementID int64, fn func(context.Context, design.StaleTx) error) error {
	if versionID != f.version.VersionID || requirementID != f.requirement.ID {
		return design.ErrNotFound
	}
	return fn(ctx, &fakeStaleTx{repo: f})
}

type fakeStaleTx struct {
	repo *fakeRepo
}

func (t *fakeStaleTx) Requirement(context.Context) (design.RequirementRef, error) {
	return t.repo.requirement, nil
}

func (t *fakeStaleTx) CasesByRequirement(context.Context) ([]design.CaseWithCurrent, error) {
	items := make([]design.CaseWithCurrent, 0)
	for _, item := range t.repo.sortedCases() {
		items = append(items, t.repo.caseWithCurrent(item))
	}
	return items, nil
}

func (t *fakeStaleTx) MarkCaseVersionStale(_ context.Context, caseVersionID int64, _ time.Time) error {
	item := t.repo.caseByVersionID(caseVersionID)
	if item == nil {
		return design.ErrNotFound
	}
	if item.current != nil && item.current.ID == caseVersionID && item.current.Status == design.CaseStatusConfirmed {
		item.current.Status = design.CaseStatusStale
	}
	for index := range item.versions {
		if item.versions[index].ID == caseVersionID && item.versions[index].Status == design.CaseStatusConfirmed {
			item.versions[index].Status = design.CaseStatusStale
		}
	}
	t.repo.staleVersions = append(t.repo.staleVersions, caseVersionID)
	return nil
}

func (t *fakeStaleTx) MarkCaseEntityStale(_ context.Context, caseID int64, _ time.Time) error {
	item, ok := t.repo.cases[caseID]
	if !ok {
		return design.ErrNotFound
	}
	if item.entity.Status == design.CaseStatusConfirmed {
		item.entity.Status = design.CaseStatusStale
	}
	t.repo.staleEntities = append(t.repo.staleEntities, caseID)
	return nil
}

func (f *fakeRepo) CurrentCaseVersion(_ context.Context, versionID, caseVersionID int64) (design.CaseVersion, error) {
	if versionID != f.version.VersionID {
		return design.CaseVersion{}, design.ErrNotFound
	}
	item := f.caseByVersionID(caseVersionID)
	if item == nil || item.current == nil || item.current.ID != caseVersionID {
		return design.CaseVersion{}, design.ErrNotFound
	}
	return *item.current, nil
}

func (f *fakeRepo) InsertDataPlanRevision(_ context.Context, versionID, baseCaseVersionID int64, revision design.CaseVersion, _ time.Time) (design.CaseVersion, error) {
	if f.dataPlanErr != nil {
		return design.CaseVersion{}, f.dataPlanErr
	}
	if versionID != f.version.VersionID {
		return design.CaseVersion{}, design.ErrNotFound
	}
	item := f.caseByVersionID(baseCaseVersionID)
	if item == nil || item.current == nil || item.current.ID != baseCaseVersionID {
		return design.CaseVersion{}, design.ErrStateConflict
	}
	if item.current.Status != design.CaseStatusDraft {
		return design.CaseVersion{}, design.ErrStateConflict
	}
	if revision.Revision != item.current.Revision+1 {
		return design.CaseVersion{}, design.ErrStateConflict
	}
	item.versions = append(item.versions, revision)
	copied := revision
	item.current = &copied
	item.entity.CurrentVersionID = &copied.ID
	item.entity.Status = design.CaseStatusDraft
	return copied, nil
}

// fakeCaseVersionSpec is one seeded case version: its step/assertion content
// and its status. Task 95 needs histories where the current revision lost the
// assertions an earlier revision carried (the PO O8 1-step/0-assertion version).
type fakeCaseVersionSpec struct {
	steps      int
	assertions int
	status     string
}

// fakeCaseVersion builds one immutable fake case version with deterministic
// child rows.
func fakeCaseVersion(caseID int64, revision int, title string, steps, assertions int, status string) design.CaseVersion {
	version := design.CaseVersion{
		ID: caseID*10 + int64(revision), CaseID: caseID, Revision: revision,
		Title: title, Status: status,
		ContentHash:   fmt.Sprintf("hash-%s-%d", title, revision),
		VersionSUTIDs: []int64{9001}, DefaultVersionSUTID: int64Ptr(9001),
		StepCount: steps, AssertionCount: assertions,
	}
	for seq := 1; seq <= steps; seq++ {
		version.Steps = append(version.Steps, design.CaseStep{
			Seq: seq, Action: fmt.Sprintf("step %d", seq), Expected: "ok",
		})
	}
	for seq := 1; seq <= assertions; seq++ {
		stepSeq := 1
		if steps > 0 {
			stepSeq = (seq-1)%steps + 1
		}
		version.Assertions = append(version.Assertions, design.CaseAssertion{
			StepSeq: stepSeq, AssertionSeq: seq, Kind: "ui", Target: "page", Expected: "ok",
		})
	}
	return version
}

// seedFakeCaseVersions inserts a case with an explicit per-revision content
// history. The last revision is the current one.
func seedFakeCaseVersions(t *testing.T, repo *fakeRepo, caseID, scenarioID int64, title string, specs ...fakeCaseVersionSpec) {
	t.Helper()
	item := &fakeCase{}
	for index, spec := range specs {
		item.versions = append(item.versions, fakeCaseVersion(
			caseID, index+1, title, spec.steps, spec.assertions, spec.status,
		))
	}
	if len(item.versions) > 0 {
		current := item.versions[len(item.versions)-1]
		item.current = &current
		versionID := current.ID
		item.entity = design.CaseEntity{
			ID: caseID, ScenarioID: scenarioID, CurrentVersionID: &versionID, Status: current.Status,
		}
	} else {
		item.entity = design.CaseEntity{ID: caseID, ScenarioID: scenarioID, Status: design.CaseStatusDraft}
	}
	repo.cases[caseID] = item
}

// appendFakeCaseVersion appends the next revision of an existing fake case and
// makes it current. It models the design repair after an incomplete version was
// refused: the repaired revision restores the designed steps/assertions.
func appendFakeCaseVersion(t *testing.T, repo *fakeRepo, caseID int64, steps, assertions int, status string) design.CaseVersion {
	t.Helper()
	item, ok := repo.cases[caseID]
	if !ok || item.current == nil {
		t.Fatalf("appendFakeCaseVersion: case %d has no current version", caseID)
	}
	revision := len(item.versions) + 1
	version := fakeCaseVersion(caseID, revision, item.current.Title, steps, assertions, status)
	item.versions = append(item.versions, version)
	item.current = &version
	versionID := version.ID
	item.entity.CurrentVersionID = &versionID
	item.entity.Status = status
	return version
}

// seedFakeCase inserts a case with a deterministic version history. The last
// version is the current one. Every seeded version carries one step and one
// assertion so it satisfies the Task 95 confirmation completeness gate; tests
// that need the O8 incomplete shape use seedFakeCaseVersions.
func seedFakeCase(t *testing.T, repo *fakeRepo, caseID, scenarioID int64, title string, statuses ...string) {
	t.Helper()
	specs := make([]fakeCaseVersionSpec, 0, len(statuses))
	for _, status := range statuses {
		specs = append(specs, fakeCaseVersionSpec{steps: 1, assertions: 1, status: status})
	}
	seedFakeCaseVersions(t, repo, caseID, scenarioID, title, specs...)
}
