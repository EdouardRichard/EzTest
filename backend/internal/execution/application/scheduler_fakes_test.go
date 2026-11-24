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
	versiondomain "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
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

// payloads returns the payloads of every event of one type, in publish order.
func (p *corePublisher) payloads(eventType string) []map[string]any {
	p.mu.Lock()
	defer p.mu.Unlock()
	result := make([]map[string]any, 0)
	for _, event := range p.events {
		if event.Type == eventType {
			result = append(result, event.Payload)
		}
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
	mu         sync.Mutex
	nextID     int64
	byID       map[int64]execution.ExecutionIntent
	createC    int
	createErrs []error // returned (and consumed) before the next successful Create
	// versions mirrors the repository's under-lock version guard (R-G9): when
	// wired, Create resolves the natural-key dedup FIRST and only a genuinely
	// new intent on a completed version reports ErrVersionCompleted — the same
	// conflict ordering as postgres guardIntentScope, so service tests exercise
	// the real reopen-and-retry contract instead of a fake that silently skips
	// the guard (method discipline 9: the fake must model the collaborator's
	// strategy, not replace it).
	versions *fakeVersionReader
	// transitionErr injects a failure of the execution claim transition
	// (open/claimed -> running) that dispatchPending performs after the work
	// item was queued. It pins the intent-Transition failure release path.
	transitionErr error
	// onTransitionErr runs after transitionErr fired and before the scheduler
	// releases the dispatch. Tests use it to simulate a worker claiming the run
	// in the window between queueing the work item and the release.
	onTransitionErr func()
}

func newFakeIntentStore() *fakeIntentStore {
	return &fakeIntentStore{byID: map[int64]execution.ExecutionIntent{}}
}

// failNextCreate makes the next Create return err, simulating the repository
// guard that detects a close/closure committed under the version advisory lock.
func (s *fakeIntentStore) failNextCreate(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.createErrs = append(s.createErrs, err)
}

func (s *fakeIntentStore) Create(_ context.Context, intent execution.ExecutionIntent) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.createErrs) > 0 {
		err := s.createErrs[0]
		s.createErrs = s.createErrs[1:]
		return err
	}
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
	// R-G9 conflict ordering (mirrors postgres guardIntentScope): the dedup
	// above resolves BEFORE the completed-version reopen signal, so a duplicate
	// never triggers the service's reopen-and-retry loop. A missing binding is
	// allowed, like the repository's missing-row tolerance.
	if s.versions != nil {
		s.versions.mu.Lock()
		binding, ok := s.versions.bindings[intent.VersionID]
		s.versions.mu.Unlock()
		if ok && binding.Status == versiondomain.StatusCompleted {
			return execution.ErrVersionCompleted
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

// CreateAndTransition models the atomic repository contract: insert already
// transitioned, or insert nothing (a failed guard rolls the insert back).
func (s *fakeIntentStore) CreateAndTransition(_ context.Context, intent execution.ExecutionIntent, from []execution.IntentStatus, to execution.IntentStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := len(from) == 0
	for _, status := range from {
		if intent.Status == status {
			matched = true
			break
		}
	}
	if !matched {
		return execution.ErrStateConflict
	}
	if _, ok := s.byID[intent.ID]; ok {
		return execution.ErrDuplicate
	}
	intent.Status = to
	s.byID[intent.ID] = intent
	s.createC++
	return nil
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
	if s.transitionErr != nil && to == execution.IntentRunning {
		err := s.transitionErr
		hook := s.onTransitionErr
		s.mu.Unlock()
		if hook != nil {
			hook()
		}
		return false, err
	}
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
	counts := execution.WaveCounts{BySUT: map[string]execution.SUTCounts{}}
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

// CreateAndTransition models the atomic repository contract: insert already
// transitioned, or insert nothing (a failed guard/fence rolls the insert back).
func (s *fakeCaseRunStore) CreateAndTransition(_ context.Context, run execution.CaseRun, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	matched := len(from) == 0
	for _, status := range from {
		if run.Status == status {
			matched = true
			break
		}
	}
	if !matched || (epoch != 0 && run.DispatchEpoch != epoch) || (token != "" && run.DispatchToken != token) {
		return execution.ErrStateConflict
	}
	if _, ok := s.byID[run.ID]; ok {
		return execution.ErrDuplicate
	}
	run.Status = to
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
	stored, ok := s.byID[run.ID]
	if !ok {
		return false, nil
	}
	// Mirror the production terminal-transition guard (V-0022): Update can
	// never move a row into or out of a terminal status; only non-terminal
	// changes and same-status rewrites are allowed. A refused terminal
	// transition is a loud conflict, never a silent no-op.
	if run.Status != stored.Status && (run.Status.Terminal() || stored.Status.Terminal()) {
		return false, execution.ErrStateConflict
	}
	s.byID[run.ID] = run
	return true, nil
}

// ReclassifyTerminal mirrors the production guarded terminal->terminal
// reclassification primitive: only a failed/blocked/inconclusive terminal row
// whose result stays in that set (domain CanReclassifyTo), fenced by epoch/token.
func (s *fakeCaseRunStore) ReclassifyTerminal(_ context.Context, id int64, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if !run.Status.CanReclassifyTo(run.Status) {
		return false, nil
	}
	if (epoch > 0 && run.DispatchEpoch != epoch) || (token != "" && run.DispatchToken != token) {
		return false, nil
	}
	priorStatus := run.Status
	if mutate != nil {
		if err := mutate(&run); err != nil {
			return false, err
		}
	}
	if !priorStatus.CanReclassifyTo(run.Status) {
		return false, execution.ErrStateConflict
	}
	s.byID[id] = run
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

func (s *fakeCaseRunStore) AddEvidenceDrop(_ context.Context, id int64, delta int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.byID[id]
	if !ok {
		return 0, execution.ErrNotFound
	}
	run.EvidenceDropped += delta
	s.byID[id] = run
	return run.EvidenceDropped, nil
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
	mu           sync.Mutex
	cases        map[int64]execution.CaseSnapshot
	replacements map[int64]int64
	labels       map[int64]CaseVersionLabel
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

// ResolveReplacement mirrors the design read used by the P3 re-plan: it maps a
// historical case version to its replacement's snapshot when the fixture
// declares one.
func (r *fakeCaseReader) ResolveReplacement(_ context.Context, versionID, caseVersionID int64) (execution.CaseSnapshot, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	replacementID, ok := r.replacements[caseVersionID]
	if !ok || replacementID <= 0 {
		return execution.CaseSnapshot{}, false, nil
	}
	replacement, ok := r.cases[replacementID]
	if !ok {
		return execution.CaseSnapshot{}, false, nil
	}
	return replacement, true, nil
}

// CaseVersionLabel mirrors the design historical read used by the terminal-run
// projection.
func (r *fakeCaseReader) CaseVersionLabel(_ context.Context, versionID, caseVersionID int64) (CaseVersionLabel, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	label, ok := r.labels[caseVersionID]
	if !ok {
		return CaseVersionLabel{}, execution.ErrNotFound
	}
	return label, nil
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
	binding.Status = versiondomain.StatusRunning
	r.bindings[id] = binding
	return nil
}

type fakeExecutorRegistry struct {
	executor *execution.ExecutorInstance
	sequence []execution.ExecutorInstance
	selected int
	err      error
	// fleetCapacity / fleetErr drive MaxDispatchableCapacity. A zero
	// fleetCapacity derives the maximum from the configured executor or
	// sequence, so single-executor fixtures keep maxCap == the selected
	// capacity and behave exactly as before review round 4.
	fleetCapacity int
	fleetErr      error
	// ceilingCalls counts MaxDispatchableCapacity invocations. Review round 7:
	// the probe costs an executorConfig (three settings reads) plus a full
	// repo.List, so it must run only on a structural rejection -- hoisting it
	// above the reason check made it run on every rejection in a busy wave.
	ceilingCalls int
	// fleetZero forces MaxDispatchableCapacity to return 0 with no error, which
	// is distinct from fleetErr: it exercises the zero branch of
	// fleetCapacityCeiling's fail-closed rule (review round 7, Minor 1).
	fleetZero bool
}

// MaxDispatchableCapacity implements executionapp.ExecutorRegistry. It is the
// fleet-wide maximum, deliberately NOT the executor Select would return -- see
// Scheduler.fleetCapacityCeiling.
func (r *fakeExecutorRegistry) MaxDispatchableCapacity(context.Context) (int, error) {
	r.ceilingCalls++
	if r.fleetErr != nil {
		return 0, r.fleetErr
	}
	if r.fleetZero {
		return 0, nil
	}
	if r.fleetCapacity > 0 {
		return r.fleetCapacity, nil
	}
	best := 0
	if r.executor != nil && r.executor.Capacity > best {
		best = r.executor.Capacity
	}
	for i := range r.sequence {
		if r.sequence[i].Capacity > best {
			best = r.sequence[i].Capacity
		}
	}
	return best, nil
}

func (r *fakeExecutorRegistry) Select(context.Context) (*execution.ExecutorInstance, error) {
	if r.err != nil {
		return nil, r.err
	}
	if len(r.sequence) > 0 {
		if r.selected >= len(r.sequence) {
			return nil, nil
		}
		copied := r.sequence[r.selected]
		r.selected++
		return &copied, nil
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
	// err injects a queue write failure: the scheduler must release the run it
	// already marked running instead of stranding it until the lease expires.
	err error
}

func (q *fakeWorkQueue) Ensure(_ context.Context, in WorkItemInput) (int64, bool, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.err != nil {
		return 0, false, q.err
	}
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
	intents    *fakeIntentStore
	waves      *fakeWaveStore
	runs       *fakeCaseRunStore
	cases      *fakeCaseReader
	versions   *fakeVersionReader
	selections *fakeBatchSelectionReader
	registry   *fakeExecutorRegistry
	quota      *fakeQuotaReader
	queue      *fakeWorkQueue
	analysis   *fakeAnalyzer
	events     *corePublisher
	clock      *coreClock
	ids        *coreIDs
	// teamRoom receives the human handoff post of a run the scheduler itself
	// terminates (review round 4, Important 2).
	teamRoom  *fakeTeamRoom
	intent    *IntentService
	scheduler *Scheduler
}

func newTask6Fixture(t interface{ Fatalf(string, ...any) }) *task6Fixture {
	now := time.Date(2026, 11, 1, 1, 0, 0, 0, time.UTC)
	f := &task6Fixture{
		intents:    newFakeIntentStore(),
		waves:      newFakeWaveStore(),
		runs:       newFakeCaseRunStore(),
		cases:      &fakeCaseReader{cases: map[int64]execution.CaseSnapshot{}, replacements: map[int64]int64{}, labels: map[int64]CaseVersionLabel{}},
		versions:   &fakeVersionReader{bindings: map[int64]VersionBinding{}},
		selections: &fakeBatchSelectionReader{found: true},
		registry:   &fakeExecutorRegistry{},
		quota:      &fakeQuotaReader{},
		queue:      &fakeWorkQueue{},
		analysis:   &fakeAnalyzer{},
		events:     &corePublisher{},
		teamRoom:   &fakeTeamRoom{},
		clock:      &coreClock{now: now},
		ids:        &coreIDs{next: 1000},
	}
	f.waves.runsStore = f.runs
	// The intent store mirrors the repository's under-lock version guard (R-G9).
	f.intents.versions = f.versions
	f.waves.claim = func(intents []execution.ExecutionIntent) {
		for _, intent := range intents {
			_, _ = f.intents.Transition(context.Background(), intent.ID, []execution.IntentStatus{execution.IntentOpen}, execution.IntentClaimed)
		}
	}
	f.intent = NewIntentService(IntentServiceDeps{
		Intents:         f.intents,
		Cases:           f.cases,
		Versions:        f.versions,
		BatchSelections: f.selections,
		IDs:             f.ids,
		Clock:           f.clock,
		Events:          f.events,
	})
	f.scheduler = f.newScheduler(f.quota)
	return f
}

// newScheduler builds the fixture scheduler with the given quota reader and no
// settings source: the degraded-assembly shape (unbounded admission, no
// resource governance). Passing a nil reader makes runningUsage fall through
// to the real ListRunning producer path instead of the injected fake.
func (f *task6Fixture) newScheduler(quotaReader QuotaReader) *Scheduler {
	return f.newSchedulerWithSettings(quotaReader, nil)
}

// newSchedulerWithQuota builds the fixture scheduler with an explicit admin
// global limit driven through the REAL settings path (the construction-time
// Quota port was removed by ruling P12 -- a settings fake is the only way to
// configure a limit, exactly like production). Claim headroom is generous (4)
// so these tests exercise the admin-limit mechanics in isolation.
func (f *task6Fixture) newSchedulerWithQuota(quota execution.Quota, quotaReader QuotaReader) *Scheduler {
	return f.newSchedulerWithSettings(quotaReader, &fakeSettings{values: map[string]any{
		SettingMaxConcurrentCaseRuns:  int64(quota.Global),
		settingAgentMaxConcurrentRuns: int64(4),
	}})
}

// newSchedulerWithSettings wires the settings reader the production
// composition root passes, so the live per-tick quota read and the
// claim-headroom admission bound are exercised against the real code path.
func (f *task6Fixture) newSchedulerWithSettings(quotaReader QuotaReader, settings SettingsReader) *Scheduler {
	return NewScheduler(SchedulerDeps{
		Intents:     f.intents,
		Waves:       f.waves,
		CaseRuns:    f.runs,
		Cases:       f.cases,
		Versions:    f.versions,
		Executors:   f.registry,
		WorkQueue:   f.queue,
		QuotaReader: quotaReader,
		Settings:    settings,
		Analyzer:    f.analysis,
		Replanner:   f.intent,
		IDs:         f.ids,
		Clock:       f.clock,
		Events:      f.events,
		Instance:    "api-1",
		LeaseTTL:    30 * time.Second,
		TeamRoom:    f.teamRoom,
	})
}

func domainQuota(global int) execution.Quota {
	return execution.Quota{Global: global}
}

func task6Snapshot(caseVersionID int64, suts ...int64) execution.CaseSnapshot {
	snapshot := execution.CaseSnapshot{
		CaseID:               caseVersionID + 1000,
		CaseVersionID:        caseVersionID,
		RequirementID:        caseVersionID + 2000,
		RequirementVersionID: caseVersionID + 3000,
		DefaultSUTID:         suts[0],
		Title:                "case",
		Priority:             "P2",
		SUTIDs:               suts,
	}
	for index := range suts {
		// v0.2.4-B: a step no longer carries a target SUT id, and the account
		// role stays empty for every step exactly as before. NOTE (review
		// M-new-1): requiredContextCount is the DISTINCT ROLE count, not
		// "1 role x len(suts)" -- the roles x bound-SUTs product was the C1
		// livelock and is gone. This fixture therefore reserves 1 context
		// whatever the number of SUTs passed here, so a multi-SUT fixture
		// reserves FEWER contexts than it did before fix round 2.
		snapshot.Steps = append(snapshot.Steps, execution.CaseStep{
			Seq: index + 1, Action: "step", TargetDescription: "target",
		})
		snapshot.Assertions = append(snapshot.Assertions, execution.CaseAssertion{
			StepSeq: index + 1, AssertionSeq: 1, Kind: "ui", Expected: "ok", SUTID: suts[index],
		})
	}
	return snapshot
}

func task6Binding(versionID int64, status string, sutIDs ...int64) VersionBinding {
	binding := VersionBinding{VersionID: versionID, Status: status, Stage: intentExecutableStage}
	for index, sutID := range sutIDs {
		binding.SUTs = append(binding.SUTs, SUTBinding{
			// One SUT key since v0.2.4-B: this used to carry the binding-row
			// id (sutID) and the SUT id (sutID+100) side by side.
			SUTID: sutID, EnvironmentID: sutID + 200,
			SUTName: "sut", EnvironmentName: "env", BaseURL: fmt.Sprintf("http://127.0.0.1:%d", 13330+index),
			PageIDs: []int64{}, APIIDs: []int64{}, DatabaseIDs: []int64{}, AccountRoles: []string{"user"},
		})
	}
	return binding
}
