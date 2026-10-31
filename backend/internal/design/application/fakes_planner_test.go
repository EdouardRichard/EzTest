package designapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	design "github.com/EdouardRichard/EzTest/backend/internal/design/domain"
)

// fakeEnqueuer dedupes tasks by dedupe key like the work queue does.
type fakeEnqueuer struct {
	tasks []TaskInput
}

func (f *fakeEnqueuer) Ensure(_ context.Context, in TaskInput) (bool, error) {
	for _, existing := range f.tasks {
		if existing.DedupeKey == in.DedupeKey {
			return false, nil
		}
	}
	f.tasks = append(f.tasks, in)
	return true, nil
}

func (f *fakeEnqueuer) byType(taskType string) []TaskInput {
	matches := make([]TaskInput, 0)
	for _, task := range f.tasks {
		if task.Type == taskType {
			matches = append(matches, task)
		}
	}
	return matches
}

type fakeConversationCreator struct {
	repo   *fakeRepo
	nextID int64
	calls  int
	titles []string
	err    error
}

func (f *fakeConversationCreator) CreateDesign(_ context.Context, versionID int64, title string) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if f.nextID == 0 {
		f.nextID = 9001
	}
	f.nextID++
	f.titles = append(f.titles, title)
	f.repo.hasConversation = true
	f.repo.latestConversation = f.nextID
	return f.nextID, nil
}

type fakeVersionStarter struct {
	calls []int64
	err   error
}

func (f *fakeVersionStarter) MarkRunning(_ context.Context, versionID int64) error {
	f.calls = append(f.calls, versionID)
	return f.err
}

type fakeStaler struct {
	calls []string
	err   error
}

func (f *fakeStaler) MarkRequirementChanged(_ context.Context, versionID, requirementID int64, reason string, revision int) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.calls = append(f.calls, fmt.Sprintf("%d:%d:%s", versionID, requirementID, reason))
	return []string{fmt.Sprintf("%d", requirementID)}, nil
}

func newPlannerFixture() (*Planner, *fakeRepo, *fakeEnqueuer, *fakeTeamRoom, *fakeConversationCreator, *fakeVersionStarter, *fakeStaler) {
	repo := newFakeRepo()
	repo.versionStatus = "initial"
	enqueuer := &fakeEnqueuer{}
	teamRoom := &fakeTeamRoom{}
	conversations := &fakeConversationCreator{repo: repo}
	versions := &fakeVersionStarter{}
	staler := &fakeStaler{}
	planner := NewPlanner(repo, &fakeIDs{}, newFixedClock(), &fakePublisher{}, enqueuer, teamRoom, conversations, versions, staler)
	return planner, repo, enqueuer, teamRoom, conversations, versions, staler
}

func plannerRequirement(id, versionID int64) design.RequirementRef {
	return design.RequirementRef{
		ID: id, VersionID: 1001, CurrentVersionID: versionID, Revision: 1,
		Title: fmt.Sprintf("需求 %d", id), Status: "clarified",
	}
}

func (f *fakeRepo) WithVersionLock(ctx context.Context, versionID int64, fn func(context.Context) error) error {
	f.versionLockCalls++
	if versionID != f.version.VersionID {
		return design.ErrNotFound
	}
	return fn(ctx)
}

func (f *fakeRepo) ListCandidateVersionIDs(context.Context, int) ([]int64, error) {
	if len(f.candidateVersions) > 0 {
		return f.candidateVersions, nil
	}
	return []int64{f.version.VersionID}, nil
}

func (f *fakeRepo) ListClarifiedRequirements(_ context.Context, versionID int64) ([]design.RequirementRef, error) {
	return f.clarified, nil
}

func (f *fakeRepo) ListActiveScopes(_ context.Context, versionID int64) ([]design.Scope, error) {
	return f.activeScopes, nil
}

func (f *fakeRepo) ListRequirementCoverage(_ context.Context, versionID int64) ([]design.RequirementCoverage, error) {
	return f.coverage, nil
}

func (f *fakeRepo) LatestDesignConversation(_ context.Context, versionID int64) (int64, bool, error) {
	return f.latestConversation, f.hasConversation, nil
}

func (f *fakeRepo) AutoDesignEnabled(_ context.Context, versionID int64) (bool, error) {
	return f.autoDesign, nil
}

func (f *fakeRepo) GetRoundByConversation(_ context.Context, conversationID int64) (*design.Round, error) {
	for index := range f.rounds {
		if f.rounds[index].ConversationID == conversationID {
			round := f.rounds[index]
			return &round, nil
		}
	}
	return nil, nil
}

func (f *fakeRepo) GetRound(_ context.Context, versionID, roundID int64) (*design.Round, error) {
	for index := range f.rounds {
		if f.rounds[index].ID == roundID {
			round := f.rounds[index]
			return &round, nil
		}
	}
	return nil, nil
}

func (f *fakeRepo) GetLatestRound(context.Context, int64) (*design.Round, error) {
	if f.latestRound != nil {
		return f.latestRound, nil
	}
	if len(f.rounds) == 0 {
		return nil, nil
	}
	round := f.rounds[len(f.rounds)-1]
	return &round, nil
}

func (f *fakeRepo) ListRounds(_ context.Context, versionID int64) ([]design.Round, error) {
	return f.rounds, nil
}

func (f *fakeRepo) CreateRound(_ context.Context, round design.Round) (design.Round, error) {
	f.rounds = append(f.rounds, round)
	return round, nil
}

func (f *fakeRepo) UpdateRoundRequirements(_ context.Context, roundID int64, requirementIDs, requirementVersionIDs []int64) error {
	for index := range f.rounds {
		if f.rounds[index].ID == roundID {
			f.rounds[index].RequirementIDs = requirementIDs
			f.rounds[index].RequirementVersionIDs = requirementVersionIDs
		}
	}
	f.updatedRoundIDs = append(f.updatedRoundIDs, roundID)
	return nil
}

func (f *fakeRepo) UpsertScope(_ context.Context, scope design.Scope) (design.Scope, bool, error) {
	for index := range f.activeScopes {
		if f.activeScopes[index].VersionID == scope.VersionID && f.activeScopes[index].RequirementID == scope.RequirementID {
			f.activeScopes[index].ActivatedByRoundID = scope.ActivatedByRoundID
			f.activeScopes[index].ActivatedAt = scope.ActivatedAt
			f.activeScopes[index].Status = scope.Status
			if scope.ID > 0 {
				f.activeScopes[index].ID = scope.ID
			}
			return f.activeScopes[index], false, nil
		}
	}
	f.activeScopes = append(f.activeScopes, scope)
	return scope, true, nil
}

func (f *fakeRepo) CountPendingDesign(_ context.Context, versionID int64) (int, error) {
	if f.pendingOverride != nil {
		return *f.pendingOverride, nil
	}
	active := map[int64]struct{}{}
	for _, scope := range f.activeScopes {
		active[scope.RequirementID] = struct{}{}
	}
	count := 0
	for _, requirement := range f.clarified {
		if _, ok := active[requirement.ID]; !ok {
			count++
		}
	}
	return count, nil
}

func (f *fakeRepo) CaseCounts(context.Context, int64) (design.CaseCounts, error) {
	return f.caseCounts, nil
}

func (f *fakeRepo) ListOutdatedConfirmedRequirements(context.Context, int) ([]design.OutdatedRequirement, error) {
	return f.outdated, nil
}

// sortedTaskDedupeKeys is a deterministic view for assertions.
func sortedTaskDedupeKeys(tasks []TaskInput) []string {
	keys := make([]string, 0, len(tasks))
	for _, task := range tasks {
		keys = append(keys, task.DedupeKey)
	}
	sort.Strings(keys)
	return keys
}

func taskPayloadString(task TaskInput, key string) string {
	value, ok := task.Payload[key]
	if !ok {
		return ""
	}
	return fmt.Sprint(value)
}

// autoTitleHasDate reports whether a generated conversation title follows the
// "AI 设计 YYYY-MM-DD HH:mm" contract.
func autoTitleHasDate(title string) bool {
	if !strings.HasPrefix(title, "AI 设计 ") {
		return false
	}
	rest := strings.TrimPrefix(title, "AI 设计 ")
	return len(rest) >= 16 && strings.Contains(rest, "-") && strings.Contains(rest, ":")
}
