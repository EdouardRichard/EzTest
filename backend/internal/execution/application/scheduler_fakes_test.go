package executionapp

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ---- shared test doubles for Task 6 planner/scheduler/intent tests ----

type coreClock struct{ now time.Time }

func (c *coreClock) Now() time.Time { return c.now }

type coreIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *coreIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

type corePublisher struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (p *corePublisher) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

func (p *corePublisher) types() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]string, 0, len(p.events))
	for _, event := range p.events {
		result = append(result, event.Type)
	}
	return result
}

func (p *corePublisher) count(eventType string) int {
	count := 0
	for _, item := range p.types() {
		if item == eventType {
			count++
		}
	}
	return count
}

type coreAudit struct {
	mu      sync.Mutex
	actions []string
}

func (a *coreAudit) Record(_ context.Context, event auditapp.Event) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.actions = append(a.actions, event.Action)
	return nil
}

// ---- repositories ----

type fakeIntentStore struct {
	mu      sync.Mutex
	nextID  int64
	byID    map[int64]execution.ExecutionIntent
	createC int
}

func newFakeIntentStore() *fakeIntentStore {
	return &fakeIntentStore{byID: map[int64]execution.ExecutionIntent{}}
}

func (s *fakeIntentStore) Create(_ context.Context, intent execution.ExecutionIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.byID {
		if existing.CaseVersionID == intent.CaseVersionID &&
			existing.EnvironmentFingerprint == intent.EnvironmentFingerprint &&
			existing.CodeBaseline == intent.CodeBaseline &&
			existing.Purpose == intent.Purpose && existing.Status.Active() {
			return execution.ErrDuplicate
		}
		if existing.ID == intent.ID {
			return execution.ErrDuplicate
		}
	}
	if intent.ID == 0 {
		s.nextID++
		intent.ID = 100 + s.nextID
	}
	s.byID[intent.ID] = intent
	s.createC++
	return nil
}

func (s *fakeIntentStore) Get(_ context.Context, id int64) (execution.ExecutionIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.byID[id]
	if !ok {
		return execution.ExecutionIntent{}, execution.ErrNotFound
	}
	return intent, nil
}

func (s *fakeIntentStore) List(_ context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.ExecutionIntent, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.ExecutionIntent, 0)
	for _, intent := range s.byID {
		if intent.VersionID != versionID || (status != "" && string(intent.Status) != status) {
			continue
		}
		if beforeID != nil && intent.ID >= *beforeID {
			continue
		}
		items = append(items, intent)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, false, nil
}

func (s *fakeIntentStore) ListOpen(_ context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return s.listActive(execution.IntentOpen, versionID)
}

func (s *fakeIntentStore) ListActive(_ context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	return s.listActive("", versionID)
}

func (s *fakeIntentStore) listActive(status execution.IntentStatus, versionID int64) ([]execution.ExecutionIntent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.ExecutionIntent, 0)
	for _, intent := range s.byID {
		if versionID > 0 && intent.VersionID != versionID {
			continue
		}
		if status != "" {
			if intent.Status != status {
				continue
			}
		} else if !intent.Status.Active() {
			continue
		}
		items = append(items, intent)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *fakeIntentStore) Transition(_ context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	intent, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	for _, status := range from {
		if intent.Status == status {
			intent.Status = to
			s.byID[id] = intent
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeIntentStore) CountInitialFor(_ context.Context, caseVersionID int64, fingerprint, baseline string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, intent := range s.byID {
		if intent.CaseVersionID == caseVersionID && intent.EnvironmentFingerprint == fingerprint &&
			intent.CodeBaseline == baseline && intent.Purpose == execution.PurposeInitial {
			count++
		}
	}
	return count, nil
}

type fakeWaveStore struct {
	mu        sync.Mutex
	byID      map[int64]execution.RunWave
	runs      map[int64][]execution.CaseRun
	nextID    int64
	claim     func([]execution.ExecutionIntent)
	runsStore *fakeCaseRunStore
}

func newFakeWaveStore() *fakeWaveStore {
	return &fakeWaveStore{byID: map[int64]execution.RunWave{}, runs: map[int64][]execution.CaseRun{}}
}

func (s *fakeWaveStore) CreateWithCaseRuns(_ context.Context, wave execution.RunWave, intents []execution.ExecutionIntent, caseRuns []execution.CaseRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.byID {
		if existing.VersionID == wave.VersionID && existing.Status.Active() {
			return execution.ErrDuplicate
		}
	}
	if wave.ID == 0 {
		s.nextID++
		wave.ID = 500 + s.nextID
	}
	if s.claim != nil {
		s.claim(intents)
	}
	s.byID[wave.ID] = wave
	s.runs[wave.ID] = append([]execution.CaseRun(nil), caseRuns...)
	if s.runsStore != nil {
		for _, run := range caseRuns {
			_ = s.runsStore.Create(context.Background(), run)
		}
	}
	return nil
}

func (s *fakeWaveStore) Get(_ context.Context, id int64) (execution.RunWave, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wave, ok := s.byID[id]
	if !ok {
		return execution.RunWave{}, execution.ErrNotFound
	}
	return wave, nil
}

func (s *fakeWaveStore) GetActive(_ context.Context, versionID int64) (*execution.RunWave, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, wave := range s.byID {
		if wave.VersionID == versionID && wave.Status.Active() {
			copied := wave
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *fakeWaveStore) List(_ context.Context, versionID int64, limit int, beforeID *int64) ([]execution.RunWave, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.RunWave, 0)
	for _, wave := range s.byID {
		if wave.VersionID == versionID {
			items = append(items, wave)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, false, nil
}

func (s *fakeWaveStore) Update(_ context.Context, wave execution.RunWave) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[wave.ID]; !ok {
		return false, nil
	}
	s.byID[wave.ID] = wave
	return true, nil
}

func (s *fakeWaveStore) Transition(_ context.Context, id int64, from []execution.WaveStatus, to execution.WaveStatus, fields execution.RunWave) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wave, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	for _, status := range from {
		if wave.Status == status {
			wave.Status = to
			if fields.StartedAt != nil {
				wave.StartedAt = fields.StartedAt
			}
			if fields.FinishedAt != nil {
				wave.FinishedAt = fields.FinishedAt
			}
			if fields.PausedAt != nil {
				wave.PausedAt = fields.PausedAt
			}
			if fields.PausedBy > 0 {
				wave.PausedBy = fields.PausedBy
			}
			if fields.PauseReason != "" {
				wave.PauseReason = fields.PauseReason
			}
			if fields.PauseMode != "" {
				wave.PauseMode = fields.PauseMode
			}
			s.byID[id] = wave
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeWaveStore) Counts(_ context.Context, waveID int64) (execution.WaveCounts, error) {
	s.mu.Lock()
	runs := append([]execution.CaseRun(nil), s.runs[waveID]...)
	store := s.runsStore
	s.mu.Unlock()
	if store != nil {
		if live, err := store.ListAllByWave(context.Background(), waveID); err == nil {
			runs = live
		}
	}
	counts := execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}}
	for _, run := range runs {
		counts.Total++
		switch run.Status {
		case execution.CaseRunPending:
			counts.Pending++
		case execution.CaseRunRunning:
			counts.Running++
		case execution.CaseRunPaused:
			counts.Paused++
		case execution.CaseRunPassed:
			counts.Passed++
		case execution.CaseRunFailed:
			counts.Failed++
		case execution.CaseRunBlocked:
			counts.Blocked++
		case execution.CaseRunInconclusive:
			counts.Inconclusive++
		case execution.CaseRunSkipped:
			counts.Skipped++
		case execution.CaseRunInterrupted:
			counts.Interrupted++
		case execution.CaseRunSuperseded:
			counts.Superseded++
		}
	}
	return counts, nil
}

func (s *fakeWaveStore) ListActive(_ context.Context) ([]execution.RunWave, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.RunWave, 0)
	for _, wave := range s.byID {
		if wave.Status.Active() {
			items = append(items, wave)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

type fakeCaseRunStore struct {
	mu   sync.Mutex
	byID map[int64]execution.CaseRun
}

func newFakeCaseRunStore() *fakeCaseRunStore {
	return &fakeCaseRunStore{byID: map[int64]execution.CaseRun{}}
}

func (s *fakeCaseRunStore) Get(_ context.Context, id int64) (execution.CaseRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.byID[id]
	if !ok {
		return execution.CaseRun{}, execution.ErrNotFound
	}
	return run, nil
}

func (s *fakeCaseRunStore) ListByWave(_ context.Context, waveID int64, status string, limit int, beforeID *int64) ([]execution.CaseRun, bool, error) {
	runs, _ := s.ListAllByWave(context.Background(), waveID)
	filtered := make([]execution.CaseRun, 0)
	for _, run := range runs {
		if status == "" || string(run.Status) == status {
			filtered = append(filtered, run)
		}
	}
	return filtered, false, nil
}

func (s *fakeCaseRunStore) ListAllByWave(_ context.Context, waveID int64) ([]execution.CaseRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRun, 0)
	for _, run := range s.byID {
		if run.WaveID == waveID {
			items = append(items, run)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *fakeCaseRunStore) ListRunning(_ context.Context) ([]execution.CaseRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRun, 0)
	for _, run := range s.byID {
		if run.Status == execution.CaseRunRunning {
			items = append(items, run)
		}
	}
	return items, nil
}

func (s *fakeCaseRunStore) Create(_ context.Context, run execution.CaseRun) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[run.ID]; ok {
		return execution.ErrDuplicate
	}
	s.byID[run.ID] = run
	return nil
}

func (s *fakeCaseRunStore) UpdateCAS(_ context.Context, id int64, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if run.Status.Terminal() || (epoch > 0 && run.DispatchEpoch != epoch) || (token != "" && run.DispatchToken != token) {
		return false, nil
	}
	if mutate != nil {
		if err := mutate(&run); err != nil {
			return false, err
		}
	}
	s.byID[id] = run
	return true, nil
}

func (s *fakeCaseRunStore) Update(_ context.Context, run execution.CaseRun) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.byID[run.ID]; !ok {
		return false, nil
	}
	s.byID[run.ID] = run
	return true, nil
}

func (s *fakeCaseRunStore) Transition(_ context.Context, id int64, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	for _, status := range from {
		if run.Status == status {
			run.Status = to
			s.byID[id] = run
			return true, nil
		}
	}
	return false, nil
}

func (s *fakeCaseRunStore) ClaimLease(context.Context, int64, int64, time.Time) (bool, error) {
	return true, nil
}

func (s *fakeCaseRunStore) ListRunningByExecutor(_ context.Context, executorID int64) ([]execution.CaseRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRun, 0)
	for _, run := range s.byID {
		if run.Status == execution.CaseRunRunning && run.OwnerExecutorID != nil && *run.OwnerExecutorID == executorID {
			items = append(items, run)
		}
	}
	return items, nil
}

func (s *fakeCaseRunStore) LatestByCaseVersions(_ context.Context, _ int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := map[int64]struct{}{}
	for _, id := range caseVersionIDs {
		wanted[id] = struct{}{}
	}
	result := map[int64]execution.CaseRun{}
	for _, run := range s.byID {
		if _, ok := wanted[run.CaseVersionID]; !ok {
			continue
		}
		existing, ok := result[run.CaseVersionID]
		if !ok || run.ID > existing.ID {
			result[run.CaseVersionID] = run
		}
	}
	return result, nil
}

// ---- cross-context and infrastructure ports ----

type fakeCaseReader struct {
	mu    sync.Mutex
	cases map[int64]execution.CaseSnapshot
}

func (r *fakeCaseReader) GetCaseVersion(_ context.Context, versionID, caseVersionID int64) (execution.CaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	item, ok := r.cases[caseVersionID]
	if !ok {
		return execution.CaseSnapshot{}, execution.ErrNotFound
	}
	return item, nil
}

func (r *fakeCaseReader) ListExecutable(_ context.Context, versionID int64) ([]execution.CaseSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]execution.CaseSnapshot, 0, len(r.cases))
	for _, item := range r.cases {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CaseVersionID < items[j].CaseVersionID })
	return items, nil
}

type fakeVersionReader struct {
	mu       sync.Mutex
	bindings map[int64]VersionBinding
	reopened []int64
}

func (r *fakeVersionReader) Get(_ context.Context, id int64) (VersionBinding, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	binding, ok := r.bindings[id]
	if !ok {
		return VersionBinding{}, execution.ErrNotFound
	}
	return binding, nil
}

func (r *fakeVersionReader) Reopen(_ context.Context, id int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reopened = append(r.reopened, id)
	binding := r.bindings[id]
	binding.Status = "running"
	r.bindings[id] = binding
	return nil
}

type fakeExecutorRegistry struct {
	executor *execution.ExecutorInstance
	err      error
}

func (r *fakeExecutorRegistry) Select(context.Context) (*execution.ExecutorInstance, error) {
	if r.err != nil {
		return nil, r.err
	}
	if r.executor == nil {
		return nil, nil
	}
	copied := *r.executor
	return &copied, nil
}

type fakeQuotaReader struct {
	mu      sync.Mutex
	running []execution.RunningCase
}

func (r *fakeQuotaReader) RunningCases(context.Context) ([]execution.RunningCase, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]execution.RunningCase(nil), r.running...), nil
}

type ensuredItem struct {
	VersionID int64
	Type      string
	DedupeKey string
	Payload   map[string]any
}

type fakeWorkQueue struct {
	mu    sync.Mutex
	items []ensuredItem
	byKey map[string]bool
}

func (q *fakeWorkQueue) Ensure(_ context.Context, in WorkItemInput) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.byKey == nil {
		q.byKey = map[string]bool{}
	}
	if q.byKey[in.DedupeKey] {
		return 0, false, nil
	}
	q.byKey[in.DedupeKey] = true
	q.items = append(q.items, ensuredItem{VersionID: in.VersionID, Type: in.Type, DedupeKey: in.DedupeKey, Payload: in.Payload})
	return int64(len(q.items)), true, nil
}

func (q *fakeWorkQueue) MarkWaitingHuman(context.Context, int64, int64) error { return nil }
func (q *fakeWorkQueue) Finish(context.Context, int64, string) error          { return nil }

func (q *fakeWorkQueue) countType(kind string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	count := 0
	for _, item := range q.items {
		if item.Type == kind {
			count++
		}
	}
	return count
}

type fakeAnalyzer struct {
	mu       sync.Mutex
	analysed []int64
	err      error
}

func (a *fakeAnalyzer) AnalyzeWave(_ context.Context, waveID int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.analysed = append(a.analysed, waveID)
	return a.err
}

// buildTask6Fixture wires the Task 6 services with in-memory fakes.
type task6Fixture struct {
	intents   *fakeIntentStore
	waves     *fakeWaveStore
	runs      *fakeCaseRunStore
	cases     *fakeCaseReader
	versions  *fakeVersionReader
	registry  *fakeExecutorRegistry
	quota     *fakeQuotaReader
	queue     *fakeWorkQueue
	analysis  *fakeAnalyzer
	events    *corePublisher
	clock     *coreClock
	ids       *coreIDs
	intent    *IntentService
	scheduler *Scheduler
}

func newTask6Fixture(t interface{ Fatalf(string, ...any) }) *task6Fixture {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	f := &task6Fixture{
		intents:  newFakeIntentStore(),
		waves:    newFakeWaveStore(),
		runs:     newFakeCaseRunStore(),
		cases:    &fakeCaseReader{cases: map[int64]execution.CaseSnapshot{}},
		versions: &fakeVersionReader{bindings: map[int64]VersionBinding{}},
		registry: &fakeExecutorRegistry{},
		quota:    &fakeQuotaReader{},
		queue:    &fakeWorkQueue{},
		analysis: &fakeAnalyzer{},
		events:   &corePublisher{},
		clock:    &coreClock{now: now},
		ids:      &coreIDs{next: 1000},
	}
	f.waves.runsStore = f.runs
	f.waves.claim = func(intents []execution.ExecutionIntent) {
		for _, intent := range intents {
			_, _ = f.intents.Transition(context.Background(), intent.ID, []execution.IntentStatus{execution.IntentOpen}, execution.IntentClaimed)
		}
	}
	f.intent = NewIntentService(IntentServiceDeps{
		Intents:  f.intents,
		Cases:    f.cases,
		Versions: f.versions,
		IDs:      f.ids,
		Clock:    f.clock,
		Events:   f.events,
	})
	f.scheduler = NewScheduler(SchedulerDeps{
		Intents:     f.intents,
		Waves:       f.waves,
		CaseRuns:    f.runs,
		Cases:       f.cases,
		Versions:    f.versions,
		Executors:   f.registry,
		WorkQueue:   f.queue,
		Quota:       domainQuota(3, 2, 1),
		QuotaReader: f.quota,
		Analyzer:    f.analysis,
		IDs:         f.ids,
		Clock:       f.clock,
		Events:      f.events,
		Instance:    "api-1",
		LeaseTTL:    30 * time.Second,
	})
	return f
}

func domainQuota(global, perSUT, crossSystem int) execution.Quota {
	return execution.Quota{Global: global, PerSUT: perSUT, CrossSystem: crossSystem}
}

func task6Snapshot(caseVersionID int64, suts ...int64) execution.CaseSnapshot {
	snapshot := execution.CaseSnapshot{
		CaseID:               caseVersionID + 1000,
		CaseVersionID:        caseVersionID,
		RequirementID:        caseVersionID + 2000,
		RequirementVersionID: caseVersionID + 3000,
		DefaultVersionSUTID:  suts[0],
		Title:                "case",
		Priority:             "P2",
		VersionSUTIDs:        suts,
	}
	for index := range suts {
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq: index + 1, Action: "step", TargetVersionSUTID: suts[index],
		})
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq: index + 1, AssertionSeq: 1, Kind: "ui", Expected: "ok", TargetVersionSUTID: suts[index],
		})
	}
	return snapshot
}

func task6Binding(versionID int64, status string, sutIDs ...int64) VersionBinding {
	binding := VersionBinding{VersionID: versionID, Status: status}
	for index, sutID := range sutIDs {
		binding.SUTs = append(binding.SUTs, VersionSUTBinding{
			VersionSUTID: sutID, SUTID: sutID + 100, EnvironmentID: sutID + 200,
			SUTName: "sut", EnvironmentName: "env", BaseURL: fmt.Sprintf("http://127.0.0.1:%d", 13330+index),
			PageIDs: []int64{}, APIIDs: []int64{}, DatabaseIDs: []int64{}, AccountRoles: []string{"user"},
		})
	}
	return binding
}
