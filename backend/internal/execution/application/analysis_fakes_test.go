package executionapp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

type fakeResultStore struct {
	mu         sync.Mutex
	steps      map[int64][]execution.StepResult
	assertions map[int64][]execution.AssertionResult
}

func newFakeResultStore() *fakeResultStore {
	return &fakeResultStore{steps: map[int64][]execution.StepResult{}, assertions: map[int64][]execution.AssertionResult{}}
}

func (s *fakeResultStore) UpsertStep(context.Context, execution.StepResult, int64, string) (bool, error) {
	return true, nil
}

func (s *fakeResultStore) UpsertAssertion(context.Context, execution.AssertionResult, int64, string) (bool, error) {
	return true, nil
}

func (s *fakeResultStore) ListSteps(_ context.Context, caseRunID int64) ([]execution.StepResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]execution.StepResult(nil), s.steps[caseRunID]...), nil
}

func (s *fakeResultStore) ListAssertions(_ context.Context, caseRunID int64) ([]execution.AssertionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]execution.AssertionResult(nil), s.assertions[caseRunID]...), nil
}

type fakeCoverageReader struct{ requirements []RequirementRef }

func (r *fakeCoverageReader) ListRequirements(context.Context, int64) ([]RequirementRef, error) {
	return append([]RequirementRef(nil), r.requirements...), nil
}

type fakeGapEnsurer struct {
	mu   sync.Mutex
	keys []string
}

func (g *fakeGapEnsurer) EnsureGap(_ context.Context, versionID, requirementID, caseVersionID int64, reason string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	key := formatGapKey(requirementID, caseVersionID, reason)
	g.keys = append(g.keys, key)
	return nil
}

func formatGapKey(requirementID, caseVersionID int64, reason string) string {
	return fmt.Sprintf("gap:%d:%d:%s", requirementID, caseVersionID, reason)
}

func (g *fakeGapEnsurer) count() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.keys)
}

type fakeTeamRoom struct {
	mu    sync.Mutex
	posts []TeamPost
}

func (t *fakeTeamRoom) Post(_ context.Context, post TeamPost) error {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.posts = append(t.posts, post)
	return nil
}

func (t *fakeTeamRoom) count() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.posts)
}

type fakeSettings struct {
	values map[string]any
}

func (s *fakeSettings) String(_ context.Context, key string) (string, error) {
	if value, ok := s.values[key]; ok {
		return value.(string), nil
	}
	return "", execution.ErrNotFound
}

func (s *fakeSettings) Int(_ context.Context, key string) (int64, error) {
	if value, ok := s.values[key]; ok {
		return value.(int64), nil
	}
	return 0, execution.ErrNotFound
}

func (s *fakeSettings) Bool(_ context.Context, key string) (bool, error) {
	if value, ok := s.values[key]; ok {
		return value.(bool), nil
	}
	return false, execution.ErrNotFound
}

// analysisFixture wires the deterministic analysis service with in-memory
// fakes. It reuses the Task 6 repository fakes.
type analysisFixture struct {
	waves        *fakeWaveStore
	runs         *fakeCaseRunStore
	results      *fakeResultStore
	cases        *fakeCaseReader
	requirements *fakeCoverageReader
	gaps         *fakeGapEnsurer
	team         *fakeTeamRoom
	queue        *fakeWorkQueue
	settings     *fakeSettings
	events       *corePublisher
	versionID    int64
	wave         execution.RunWave
	analysis     *AnalysisService
}

func newAnalysisFixture() *analysisFixture {
	now := fixedAnalysisTime()
	f := &analysisFixture{
		waves:        newFakeWaveStore(),
		runs:         newFakeCaseRunStore(),
		results:      newFakeResultStore(),
		cases:        &fakeCaseReader{cases: map[int64]execution.CaseSnapshot{}},
		requirements: &fakeCoverageReader{},
		gaps:         &fakeGapEnsurer{},
		team:         &fakeTeamRoom{},
		queue:        &fakeWorkQueue{},
		settings:     &fakeSettings{values: map[string]any{"feature.auto_gap_design": true}},
		events:       &corePublisher{},
		versionID:    100,
	}
	f.wave = execution.RunWave{
		ID: 50, VersionID: f.versionID, Status: execution.WaveRunning, Trigger: execution.WaveTriggerAuto,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	f.waves.byID[f.wave.ID] = f.wave
	f.analysis = NewAnalysisService(AnalysisServiceDeps{
		Waves:        f.waves,
		CaseRuns:     f.runs,
		Results:      f.results,
		Cases:        f.cases,
		Requirements: f.requirements,
		Gaps:         f.gaps,
		TeamRoom:     f.team,
		WorkQueue:    f.queue,
		Settings:     f.settings,
		Clock:        &coreClock{now: now},
		Events:       f.events,
	})
	return f
}

func fixedAnalysisTime() time.Time {
	return time.Date(2026, 11, 1, 2, 0, 0, 0, time.UTC)
}

func (f *analysisFixture) addRun(id, intentID, caseVersionID int64, status execution.CaseRunStatus, class execution.FailureClass) execution.CaseRun {
	run := execution.CaseRun{
		ID: id, WaveID: f.wave.ID, IntentID: intentID, CaseVersionID: caseVersionID,
		Status: status, FailureClass: class,
	}
	f.runs.byID[id] = run
	return run
}

func (f *analysisFixture) addFailedAssertion(caseRunID int64, stepSeq, assertionSeq int) {
	f.results.assertions[caseRunID] = append(f.results.assertions[caseRunID], execution.AssertionResult{
		CaseRunID: caseRunID, StepSeq: stepSeq, AssertionSeq: assertionSeq, Kind: "ui",
		Verdict: execution.AssertionFailed, Expected: "expected", Actual: "actual",
	})
}

func (f *analysisFixture) addFailedStep(caseRunID int64, seq int) {
	f.results.steps[caseRunID] = append(f.results.steps[caseRunID], execution.StepResult{
		CaseRunID: caseRunID, Seq: seq, Status: execution.StepFailed, Error: "target_not_found",
	})
}

func (f *analysisFixture) addCase(caseVersionID, requirementID, requirementVersionID int64, priority string) {
	f.cases.cases[caseVersionID] = execution.CaseSnapshot{
		CaseID: caseVersionID + 1000, CaseVersionID: caseVersionID,
		RequirementID: requirementID, RequirementVersionID: requirementVersionID,
		Priority: priority, Title: "case",
	}
}

func (f *analysisFixture) caseRunEvents(eventType string) []eventingapp.Event {
	result := make([]eventingapp.Event, 0)
	for _, event := range f.events.events {
		if event.Type == eventType {
			result = append(result, event)
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprint(result[i].Payload["case_run_id"]) < fmt.Sprint(result[j].Payload["case_run_id"])
	})
	return result
}
