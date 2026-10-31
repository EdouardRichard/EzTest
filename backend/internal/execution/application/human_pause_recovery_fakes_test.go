package executionapp

import (
	"context"
	"sort"
	"sync"
	"time"

	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// ---- Task 9 fakes: human requests, executors, SUT contexts, cancellation ----

type fakeHumanStore struct {
	mu     sync.Mutex
	byID   map[int64]execution.HumanRequest
	nextID int64
}

func newFakeHumanStore() *fakeHumanStore {
	return &fakeHumanStore{byID: map[int64]execution.HumanRequest{}}
}

func (s *fakeHumanStore) Create(_ context.Context, request execution.HumanRequest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.ID == 0 {
		s.nextID++
		request.ID = 700 + s.nextID
	}
	if _, ok := s.byID[request.ID]; ok {
		return execution.ErrDuplicate
	}
	s.byID[request.ID] = request
	return nil
}

func (s *fakeHumanStore) Get(_ context.Context, id int64) (execution.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.byID[id]
	if !ok {
		return execution.HumanRequest{}, execution.ErrNotFound
	}
	return request, nil
}

func (s *fakeHumanStore) GetOpenByCaseRun(_ context.Context, caseRunID int64) (*execution.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.byID {
		if request.Status == execution.HumanOpen && request.CaseRunID != nil && *request.CaseRunID == caseRunID {
			copied := request
			return &copied, nil
		}
	}
	return nil, nil
}

func (s *fakeHumanStore) ListByVersion(_ context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.HumanRequest, 0)
	for _, request := range s.byID {
		if request.VersionID != versionID || (status != "" && string(request.Status) != status) {
			continue
		}
		items = append(items, request)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	return items, false, nil
}

func (s *fakeHumanStore) ListOpen(_ context.Context) ([]execution.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.HumanRequest, 0)
	for _, request := range s.byID {
		if request.Status == execution.HumanOpen {
			items = append(items, request)
		}
	}
	return items, nil
}

func (s *fakeHumanStore) CountByCaseRun(_ context.Context, caseRunID int64) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	count := 0
	for _, request := range s.byID {
		if request.CaseRunID != nil && *request.CaseRunID == caseRunID {
			count++
		}
	}
	return count, nil
}

func (s *fakeHumanStore) Transition(_ context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	request, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if request.Status != from {
		return false, nil
	}
	request.Status = to
	request.Response = response
	if to == execution.HumanResolved || to == execution.HumanCancelled {
		request.ResolvedBy = actorPtr(actorID)
		request.ResolvedAt = &at
	}
	request.UpdatedAt = at
	s.byID[id] = request
	return true, nil
}

func (s *fakeHumanStore) Expire(_ context.Context, now time.Time) ([]execution.HumanRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	expired := make([]execution.HumanRequest, 0)
	for id, request := range s.byID {
		if request.Status != execution.HumanOpen || !request.ExpiresAt.Before(now) {
			continue
		}
		request.Status = execution.HumanExpired
		request.UpdatedAt = now
		s.byID[id] = request
		expired = append(expired, request)
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].ID < expired[j].ID })
	return expired, nil
}

type fakeExecutorStore struct {
	mu   sync.Mutex
	byID map[int64]execution.ExecutorInstance
}

func newFakeExecutorStore() *fakeExecutorStore {
	return &fakeExecutorStore{byID: map[int64]execution.ExecutorInstance{}}
}

func (s *fakeExecutorStore) UpsertHeartbeat(_ context.Context, instance execution.ExecutorInstance) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[instance.ID] = instance
	return nil
}

func (s *fakeExecutorStore) GetByEndpoint(_ context.Context, endpoint string) (execution.ExecutorInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, instance := range s.byID {
		if instance.Endpoint == endpoint {
			return instance, nil
		}
	}
	return execution.ExecutorInstance{}, execution.ErrNotFound
}

func (s *fakeExecutorStore) Get(_ context.Context, id int64) (execution.ExecutorInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.byID[id]
	if !ok {
		return execution.ExecutorInstance{}, execution.ErrNotFound
	}
	return instance, nil
}

func (s *fakeExecutorStore) List(_ context.Context) ([]execution.ExecutorInstance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.ExecutorInstance, 0, len(s.byID))
	for _, instance := range s.byID {
		items = append(items, instance)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *fakeExecutorStore) SetStatus(_ context.Context, id int64, status execution.ExecutorStatus) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	instance, ok := s.byID[id]
	if !ok {
		return false, nil
	}
	instance.Status = status
	s.byID[id] = instance
	return true, nil
}

type fakeSUTContextStore struct {
	mu     sync.Mutex
	byID   map[int64]execution.CaseRunSUTContext
	nextID int64
}

func newFakeSUTContextStore() *fakeSUTContextStore {
	return &fakeSUTContextStore{byID: map[int64]execution.CaseRunSUTContext{}}
}

func (s *fakeSUTContextStore) Create(_ context.Context, item execution.CaseRunSUTContext) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if item.ID == 0 {
		s.nextID++
		item.ID = 900 + s.nextID
	}
	s.byID[item.ID] = item
	return nil
}

func (s *fakeSUTContextStore) ListByCaseRun(_ context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRunSUTContext, 0)
	for _, item := range s.byID {
		if item.CaseRunID == caseRunID {
			items = append(items, item)
		}
	}
	return items, nil
}

func (s *fakeSUTContextStore) UpdateCAS(_ context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	item, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if err := mutate(&item); err != nil {
		return false, err
	}
	s.byID[id] = item
	return true, nil
}

func (s *fakeSUTContextStore) ListStale(_ context.Context, createdBefore time.Time, limit int) ([]execution.CaseRunSUTContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRunSUTContext, 0)
	for _, item := range s.byID {
		if item.Status != execution.SUTContextClosed && item.CreatedAt.Before(createdBefore) {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

func (s *fakeSUTContextStore) ListAll(_ context.Context) ([]execution.CaseRunSUTContext, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]execution.CaseRunSUTContext, 0)
	for _, item := range s.byID {
		if item.Status != execution.SUTContextClosed {
			items = append(items, item)
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return items, nil
}

type fakeCanceller struct {
	mu      sync.Mutex
	cancels []int64
	runs    map[int64]context.CancelFunc
}

func newFakeCanceller() *fakeCanceller {
	return &fakeCanceller{runs: map[int64]context.CancelFunc{}}
}

func (c *fakeCanceller) RegisterRun(caseRunID int64, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.runs[caseRunID] = cancel
}

func (c *fakeCanceller) CancelRun(caseRunID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels = append(c.cancels, caseRunID)
	if cancel, ok := c.runs[caseRunID]; ok {
		cancel()
	}
}

func (c *fakeCanceller) ClearRun(caseRunID int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.runs, caseRunID)
}

func (c *fakeCanceller) cancelled(caseRunID int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.cancels {
		if id == caseRunID {
			return true
		}
	}
	return false
}

type fakeExecutorControl struct {
	mu      sync.Mutex
	deleted []string
	cancels []string
	live    []ExecutorContext
}

func (c *fakeExecutorControl) CancelContext(_ context.Context, contextID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cancels = append(c.cancels, contextID)
	return nil
}

func (c *fakeExecutorControl) DeleteContext(_ context.Context, contextID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deleted = append(c.deleted, contextID)
	return nil
}

func (c *fakeExecutorControl) ListContexts(context.Context) ([]ExecutorContext, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]ExecutorContext(nil), c.live...), nil
}

func (c *fakeExecutorControl) deletedContext(contextID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, id := range c.deleted {
		if id == contextID {
			return true
		}
	}
	return false
}

// task9Fixture wires the Task 9 services over the shared fakes.
type task9Fixture struct {
	intents   *fakeIntentStore
	waves     *fakeWaveStore
	runs      *fakeCaseRunStore
	humans    *fakeHumanStore
	executors *fakeExecutorStore
	contexts  *fakeSUTContextStore
	canceller *fakeCanceller
	control   *fakeExecutorControl
	cases     *fakeCaseReader
	queue     *fakeWorkQueue
	events    *corePublisher
	clock     *coreClock
	ids       *coreIDs
	pause     *PauseService
	human     *HumanService
	recovery  *Recovery
}

func newTask9Fixture() *task9Fixture {
	now := time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)
	f := &task9Fixture{
		intents:   newFakeIntentStore(),
		waves:     newFakeWaveStore(),
		runs:      newFakeCaseRunStore(),
		humans:    newFakeHumanStore(),
		executors: newFakeExecutorStore(),
		contexts:  newFakeSUTContextStore(),
		canceller: newFakeCanceller(),
		control:   &fakeExecutorControl{},
		cases:     &fakeCaseReader{cases: map[int64]execution.CaseSnapshot{}},
		queue:     &fakeWorkQueue{},
		events:    &corePublisher{},
		clock:     &coreClock{now: now},
		ids:       &coreIDs{next: 5000},
	}
	f.pause = NewPauseService(PauseServiceDeps{
		Intents:   f.intents,
		Waves:     f.waves,
		CaseRuns:  f.runs,
		Humans:    f.humans,
		Contexts:  f.contexts,
		Canceller: f.canceller,
		Executor:  f.control,
		Clock:     f.clock,
		Events:    f.events,
	})
	f.human = NewHumanService(HumanServiceDeps{
		Humans:    f.humans,
		CaseRuns:  f.runs,
		Waves:     f.waves,
		Canceller: f.canceller,
		WorkQueue: f.queue,
		IDs:       f.ids,
		Clock:     f.clock,
		Events:    f.events,
		Timeout:   time.Hour,
	})
	f.recovery = NewRecovery(RecoveryDeps{
		Intents:     f.intents,
		Waves:       f.waves,
		CaseRuns:    f.runs,
		Humans:      f.human,
		Executors:   f.executors,
		Contexts:    f.contexts,
		Cases:       f.cases,
		WorkQueue:   f.queue,
		Canceller:   f.canceller,
		Executor:    f.control,
		Clock:       f.clock,
		Events:      f.events,
		MaxAttempts: 3,
		ContextTTL:  5 * time.Minute,
	})
	return f
}

// seedActiveRun creates one active wave with the given case runs.
func (f *task9Fixture) seedWave(status execution.WaveStatus) execution.RunWave {
	now := f.clock.Now()
	wave := execution.RunWave{
		ID: 50, VersionID: 100, Status: status, Trigger: execution.WaveTriggerAuto,
		StartedAt: &now, CreatedAt: now, UpdatedAt: now,
	}
	f.waves.byID[wave.ID] = wave
	return wave
}

func (f *task9Fixture) seedRun(id int64, intentID int64, status execution.CaseRunStatus) execution.CaseRun {
	now := f.clock.Now()
	run := execution.CaseRun{
		ID: id, WaveID: 50, IntentID: intentID, CaseVersionID: 300,
		Status: status, Attempts: 1, DispatchEpoch: 1, DispatchToken: "token-1",
		CreatedAt: now, UpdatedAt: now,
	}
	f.runs.byID[id] = run
	return run
}

func (f *task9Fixture) seedIntent(id, caseVersionID int64) execution.ExecutionIntent {
	intent := execution.ExecutionIntent{
		ID: id, VersionID: 100, CaseVersionID: caseVersionID,
		VersionSUTIDs:          []int64{5001},
		EnvironmentFingerprint: testFingerprintForTask9,
		Purpose:                execution.PurposeInitial,
		Status:                 execution.IntentRunning,
	}
	f.intents.byID[id] = intent
	return intent
}

const testFingerprintForTask9 = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
