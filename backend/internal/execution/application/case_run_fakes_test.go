package executionapp_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// --- repository fakes ------------------------------------------------------

type fakeIntentRepo struct {
	mu      sync.Mutex
	rows    map[int64]execution.ExecutionIntent
	updates []execution.IntentStatus
	// transitionErr injects a failure of the next Transition call, so a test can
	// pin that a failed create+transition leaves no orphan intent behind.
	transitionErr error
}

func newFakeIntentRepo() *fakeIntentRepo {
	return &fakeIntentRepo{rows: map[int64]execution.ExecutionIntent{}}
}

func (f *fakeIntentRepo) Create(_ context.Context, intent execution.ExecutionIntent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[intent.ID] = intent
	return nil
}

// CreateAndTransition models the repository contract atomically: the row is
// inserted already transitioned, or nothing is inserted at all (a failed
// transition rolls the insert back — no orphan open intent).
func (f *fakeIntentRepo) CreateAndTransition(_ context.Context, intent execution.ExecutionIntent, from []execution.IntentStatus, to execution.IntentStatus) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transitionErr != nil {
		err := f.transitionErr
		f.transitionErr = nil
		return err
	}
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
	intent.Status = to
	f.rows[intent.ID] = intent
	f.updates = append(f.updates, to)
	return nil
}

func (f *fakeIntentRepo) Get(_ context.Context, id int64) (execution.ExecutionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return execution.ExecutionIntent{}, execution.ErrNotFound
	}
	return row, nil
}

func (f *fakeIntentRepo) List(context.Context, int64, string, int, *int64) ([]execution.ExecutionIntent, bool, error) {
	return nil, false, nil
}

func (f *fakeIntentRepo) ListOpen(_ context.Context, versionID int64) ([]execution.ExecutionIntent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.ExecutionIntent, 0)
	for _, row := range f.rows {
		if row.Status == execution.IntentOpen && (versionID <= 0 || row.VersionID == versionID) {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeIntentRepo) ListActive(context.Context, int64) ([]execution.ExecutionIntent, error) {
	return nil, nil
}

func (f *fakeIntentRepo) Transition(_ context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.transitionErr != nil {
		err := f.transitionErr
		f.transitionErr = nil
		return false, err
	}
	row, ok := f.rows[id]
	if !ok {
		return false, nil
	}
	matched := len(from) == 0
	for _, status := range from {
		if row.Status == status {
			matched = true
			break
		}
	}
	if !matched {
		return false, nil
	}
	row.Status = to
	f.rows[id] = row
	f.updates = append(f.updates, to)
	return true, nil
}

func (f *fakeIntentRepo) CountInitialFor(context.Context, int64, string, string) (int, error) {
	return 0, nil
}

type fakeCaseRunRepo struct {
	mu            sync.Mutex
	rows          map[int64]execution.CaseRun
	updates       int
	claimFailures int
	claimAttempts int
	// beforeUpdateCAS simulates a concurrent owner racing the next CAS write. It
	// runs with the repository lock held and must only touch f.rows directly.
	beforeUpdateCAS func()
}

func newFakeCaseRunRepo() *fakeCaseRunRepo {
	return &fakeCaseRunRepo{rows: map[int64]execution.CaseRun{}}
}

func (f *fakeCaseRunRepo) Get(_ context.Context, id int64) (execution.CaseRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return execution.CaseRun{}, execution.ErrNotFound
	}
	return row, nil
}

func (f *fakeCaseRunRepo) ListByWave(context.Context, int64, string, int, *int64) ([]execution.CaseRun, bool, error) {
	return nil, false, nil
}

func (f *fakeCaseRunRepo) ListAllByWave(_ context.Context, waveID int64) ([]execution.CaseRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.CaseRun, 0)
	for _, row := range f.rows {
		if row.WaveID == waveID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeCaseRunRepo) ListRunning(context.Context) ([]execution.CaseRun, error) {
	return nil, nil
}

func (f *fakeCaseRunRepo) Create(_ context.Context, caseRun execution.CaseRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[caseRun.ID] = caseRun
	return nil
}

// CreateAndTransition models the repository contract atomically: the row is
// inserted already transitioned, or nothing is inserted at all (a failed
// guard/fence rolls the insert back — no orphan anchor).
func (f *fakeCaseRunRepo) CreateAndTransition(_ context.Context, caseRun execution.CaseRun, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	matched := len(from) == 0
	for _, status := range from {
		if caseRun.Status == status {
			matched = true
			break
		}
	}
	if !matched {
		return execution.ErrStateConflict
	}
	if epoch != 0 && caseRun.DispatchEpoch != epoch {
		return execution.ErrStateConflict
	}
	if token != "" && caseRun.DispatchToken != token {
		return execution.ErrStateConflict
	}
	caseRun.Status = to
	f.rows[caseRun.ID] = caseRun
	return nil
}

func (f *fakeCaseRunRepo) UpdateCAS(_ context.Context, id, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beforeUpdateCAS != nil {
		hook := f.beforeUpdateCAS
		f.beforeUpdateCAS = nil
		hook()
	}
	row, ok := f.rows[id]
	if !ok {
		return false, nil
	}
	if row.Status.Finished() {
		return false, nil
	}
	if epoch != 0 && row.DispatchEpoch != epoch {
		return false, nil
	}
	if token != "" && row.DispatchToken != token {
		return false, nil
	}
	if err := mutate(&row); err != nil {
		return false, err
	}
	f.rows[id] = row
	f.updates++
	return true, nil
}

func (f *fakeCaseRunRepo) Update(_ context.Context, caseRun execution.CaseRun) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	stored, ok := f.rows[caseRun.ID]
	if !ok {
		return false, nil
	}
	// Mirror the production terminal-transition guard (V-0022): Update can
	// never move a row into or out of a terminal status.
	if caseRun.Status != stored.Status && (caseRun.Status.Terminal() || stored.Status.Terminal()) {
		return false, execution.ErrStateConflict
	}
	f.rows[caseRun.ID] = caseRun
	return true, nil
}

// ReclassifyTerminal mirrors the production guarded terminal->terminal
// reclassification primitive (V-0022).
func (f *fakeCaseRunRepo) ReclassifyTerminal(_ context.Context, id, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if !row.Status.CanReclassifyTo(row.Status) {
		return false, nil
	}
	if (epoch > 0 && row.DispatchEpoch != epoch) || (token != "" && row.DispatchToken != token) {
		return false, nil
	}
	priorStatus := row.Status
	if mutate != nil {
		if err := mutate(&row); err != nil {
			return false, err
		}
	}
	if !priorStatus.CanReclassifyTo(row.Status) {
		return false, execution.ErrStateConflict
	}
	f.rows[id] = row
	return true, nil
}

func (f *fakeCaseRunRepo) Transition(_ context.Context, id int64, from []execution.CaseRunStatus, to execution.CaseRunStatus, epoch int64, token string) (bool, error) {
	return f.UpdateCAS(context.Background(), id, epoch, token, func(row *execution.CaseRun) error {
		for _, status := range from {
			if row.Status == status {
				row.Status = to
				return nil
			}
		}
		return execution.ErrStateConflict
	})
}

func (f *fakeCaseRunRepo) ClaimLease(_ context.Context, id, epoch int64, until time.Time) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimAttempts++
	if f.claimFailures > 0 {
		f.claimFailures--
		return false, errors.New("case_run store temporarily unavailable")
	}
	row, ok := f.rows[id]
	if !ok || row.DispatchEpoch != epoch {
		return false, nil
	}
	row.LeaseUntil = &until
	f.rows[id] = row
	return true, nil
}

// failNextClaims injects transient ClaimLease errors (a store blip).
func (f *fakeCaseRunRepo) failNextClaims(count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.claimFailures = count
}

// claimCount reports how many times the lease renewal was attempted.
func (f *fakeCaseRunRepo) claimCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.claimAttempts
}

func (f *fakeCaseRunRepo) ListRunningByExecutor(context.Context, int64) ([]execution.CaseRun, error) {
	return nil, nil
}

// AddEvidenceDrop mirrors the production atomic increment of the evidence-drop
// fact (no dispatch fence: a terminal capture must still be able to record one).
func (f *fakeCaseRunRepo) AddEvidenceDrop(_ context.Context, id int64, delta int64) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return 0, execution.ErrNotFound
	}
	row.EvidenceDropped += delta
	f.rows[id] = row
	return row.EvidenceDropped, nil
}

// LatestByCaseVersions mirrors the production "newest non-superseded run per
// case version" projection (the coverage reader feeds on it).
func (f *fakeCaseRunRepo) LatestByCaseVersions(_ context.Context, _ int64, caseVersionIDs []int64) (map[int64]execution.CaseRun, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	wanted := make(map[int64]struct{}, len(caseVersionIDs))
	for _, id := range caseVersionIDs {
		wanted[id] = struct{}{}
	}
	latest := make(map[int64]execution.CaseRun, len(caseVersionIDs))
	for _, row := range f.rows {
		if row.Status == execution.CaseRunSuperseded {
			continue
		}
		if _, ok := wanted[row.CaseVersionID]; !ok {
			continue
		}
		if existing, ok := latest[row.CaseVersionID]; !ok || row.ID > existing.ID {
			latest[row.CaseVersionID] = row
		}
	}
	return latest, nil
}

func (f *fakeCaseRunRepo) get(t interface{ Fatalf(string, ...any) }, id int64) execution.CaseRun {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		t.Fatalf("case_run %d not found", id)
	}
	return row
}

type fakeSUTContextRepo struct {
	mu   sync.Mutex
	rows map[int64]execution.CaseRunSUTContext
	// listErr injects a ListByCaseRun failure (the terminal-drain loss path).
	listErr error
}

func newFakeSUTContextRepo() *fakeSUTContextRepo {
	return &fakeSUTContextRepo{rows: map[int64]execution.CaseRunSUTContext{}}
}

func (f *fakeSUTContextRepo) Create(_ context.Context, sutContext execution.CaseRunSUTContext) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.CaseRunID == sutContext.CaseRunID && row.SUTID == sutContext.SUTID &&
			row.AccountRole == sutContext.AccountRole {
			return execution.ErrDuplicate
		}
	}
	f.rows[sutContext.ID] = sutContext
	return nil
}

func (f *fakeSUTContextRepo) ListByCaseRun(_ context.Context, caseRunID int64) ([]execution.CaseRunSUTContext, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	rows := make([]execution.CaseRunSUTContext, 0)
	for _, row := range f.rows {
		if row.CaseRunID == caseRunID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeSUTContextRepo) UpdateCAS(_ context.Context, id int64, mutate func(*execution.CaseRunSUTContext) error) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return false, nil
	}
	if err := mutate(&row); err != nil {
		return false, err
	}
	f.rows[id] = row
	return true, nil
}

func (f *fakeSUTContextRepo) ListStale(context.Context, time.Time, int) ([]execution.CaseRunSUTContext, error) {
	return nil, nil
}

func (f *fakeSUTContextRepo) ListAll(context.Context) ([]execution.CaseRunSUTContext, error) {
	return nil, nil
}

func (f *fakeSUTContextRepo) row(sutID int64, role string) (execution.CaseRunSUTContext, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.SUTID == sutID && row.AccountRole == role {
			return row, true
		}
	}
	return execution.CaseRunSUTContext{}, false
}

type fakeResultRepo struct {
	mu         sync.Mutex
	steps      map[int64]execution.StepResult
	assertions map[int64]execution.AssertionResult
	nextStep   int64
	nextAssert int64
}

func newFakeResultRepo() *fakeResultRepo {
	return &fakeResultRepo{
		steps:      map[int64]execution.StepResult{},
		assertions: map[int64]execution.AssertionResult{},
	}
}

func (f *fakeResultRepo) UpsertStep(_ context.Context, result execution.StepResult, epoch int64, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch <= 0 || token == "" {
		return false, nil
	}
	for id, existing := range f.steps {
		if existing.CaseRunID == result.CaseRunID && existing.Seq == result.Seq {
			result.ID = id
			f.steps[id] = result
			return true, nil
		}
	}
	f.nextStep++
	result.ID = f.nextStep
	f.steps[result.ID] = result
	return true, nil
}

func (f *fakeResultRepo) UpsertAssertion(_ context.Context, result execution.AssertionResult, epoch int64, token string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch <= 0 || token == "" {
		return false, nil
	}
	for id, existing := range f.assertions {
		if existing.CaseRunID == result.CaseRunID && existing.StepSeq == result.StepSeq &&
			existing.AssertionSeq == result.AssertionSeq {
			result.ID = id
			f.assertions[id] = result
			return true, nil
		}
	}
	f.nextAssert++
	result.ID = f.nextAssert
	f.assertions[result.ID] = result
	return true, nil
}

func (f *fakeResultRepo) ListSteps(_ context.Context, caseRunID int64) ([]execution.StepResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.StepResult, 0)
	for _, row := range f.steps {
		if row.CaseRunID == caseRunID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeResultRepo) ListAssertions(_ context.Context, caseRunID int64) ([]execution.AssertionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.AssertionResult, 0)
	for _, row := range f.assertions {
		if row.CaseRunID == caseRunID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

type fakeEvidenceRepo struct {
	mu      sync.Mutex
	rows    []execution.Evidence
	refs    []execution.EvidenceRef
	nextID  int64
	nextRef int64
	// countErr, when set, makes CountByCaseRun fail so a test can prove the
	// verdict aggregation surfaces a count read failure instead of swallowing it.
	countErr error
}

func newFakeEvidenceRepo() *fakeEvidenceRepo { return &fakeEvidenceRepo{} }

func (f *fakeEvidenceRepo) Insert(_ context.Context, evidence execution.Evidence) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows = append(f.rows, evidence)
	return nil
}

func (f *fakeEvidenceRepo) Get(_ context.Context, id int64) (execution.Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.ID == id {
			return row, nil
		}
	}
	return execution.Evidence{}, execution.ErrNotFound
}

func (f *fakeEvidenceRepo) ListByCaseRun(_ context.Context, caseRunID int64, _ int) ([]execution.Evidence, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rows := make([]execution.Evidence, 0)
	for _, row := range f.rows {
		if row.CaseRunID == caseRunID {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (f *fakeEvidenceRepo) ListByVersion(_ context.Context, _ int64, filter execution.EvidenceFilter) ([]execution.Evidence, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	matching := make([]execution.Evidence, 0)
	for _, row := range f.rows {
		if filter.CaseRunID != nil && row.CaseRunID != *filter.CaseRunID {
			continue
		}
		if filter.SUTID != nil && (row.SUTID == nil || *row.SUTID != *filter.SUTID) {
			continue
		}
		if filter.Kind != "" && row.Kind != filter.Kind {
			continue
		}
		if filter.BeforeID != nil && row.ID >= *filter.BeforeID {
			continue
		}
		matching = append(matching, row)
	}
	// Descending-ID cursor pages like the real repository (the replay loader
	// re-sorts ascending); the repo caps a page at 100.
	sort.Slice(matching, func(i, j int) bool { return matching[i].ID > matching[j].ID })
	limit := filter.Limit
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	hasMore := len(matching) > limit
	if hasMore {
		matching = matching[:limit]
	}
	return matching, hasMore, nil
}

func (f *fakeEvidenceRepo) InsertRef(_ context.Context, ref execution.EvidenceRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextRef++
	ref.ID = f.nextRef
	f.refs = append(f.refs, ref)
	return nil
}

func (f *fakeEvidenceRepo) CountByCaseRun(_ context.Context, caseRunID int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.countErr != nil {
		return 0, f.countErr
	}
	count := 0
	for _, row := range f.rows {
		if row.CaseRunID == caseRunID {
			count++
		}
	}
	return count, nil
}

type fakeHumanRepo struct {
	mu   sync.Mutex
	rows map[int64]execution.HumanRequest
}

func newFakeHumanRepo() *fakeHumanRepo {
	return &fakeHumanRepo{rows: map[int64]execution.HumanRequest{}}
}

func (f *fakeHumanRepo) Create(_ context.Context, request execution.HumanRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rows[request.ID] = request
	return nil
}

func (f *fakeHumanRepo) Get(_ context.Context, id int64) (execution.HumanRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[id]
	if !ok {
		return execution.HumanRequest{}, execution.ErrNotFound
	}
	return row, nil
}

func (f *fakeHumanRepo) GetOpenByCaseRun(_ context.Context, caseRunID int64) (*execution.HumanRequest, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.CaseRunID != nil && *row.CaseRunID == caseRunID && row.Status == execution.HumanPending {
			copied := row
			return &copied, nil
		}
	}
	return nil, nil
}

func (f *fakeHumanRepo) ListByVersion(context.Context, int64, string, int, *int64) ([]execution.HumanRequest, bool, error) {
	return nil, false, nil
}

func (f *fakeHumanRepo) ListOpen(context.Context) ([]execution.HumanRequest, error) { return nil, nil }

func (f *fakeHumanRepo) ListQuestions(context.Context, int64, string, int, *int64) ([]execution.HumanRequest, bool, error) {
	return nil, false, nil
}

func (f *fakeHumanRepo) CountByCaseRun(context.Context, int64) (int, error) { return 0, nil }

func (f *fakeHumanRepo) Transition(context.Context, int64, execution.HumanRequestStatus, execution.HumanRequestStatus, map[string]any, int64, time.Time, *time.Time) (bool, error) {
	return false, nil
}

func (f *fakeHumanRepo) AttachMessage(context.Context, int64, int64) error { return nil }

func (f *fakeHumanRepo) Expire(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (f *fakeHumanRepo) ExpireQuestions(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

// --- readers / ports -------------------------------------------------------

// fakeReplayVersionReader serves the version binding the replay fingerprint
// gate recomputes from. The default binding matches the fixture's SUT 7.
type fakeReplayVersionReader struct {
	binding executionapp.VersionBinding
}

func (f *fakeReplayVersionReader) Get(_ context.Context, id int64) (executionapp.VersionBinding, error) {
	if f.binding.VersionID != id {
		return executionapp.VersionBinding{}, execution.ErrNotFound
	}
	return f.binding, nil
}

func (f *fakeReplayVersionReader) Reopen(context.Context, int64) error { return nil }

type fakeExecutionCaseReader struct {
	snapshot   execution.CaseSnapshot
	executable []execution.CaseSnapshot
	err        error
}

func (f *fakeExecutionCaseReader) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	if f.err != nil {
		return execution.CaseSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func (f *fakeExecutionCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return append([]execution.CaseSnapshot(nil), f.executable...), nil
}

func (f *fakeExecutionCaseReader) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	return execution.CaseSnapshot{}, false, nil
}

func (f *fakeExecutionCaseReader) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	return executionapp.CaseVersionLabel{}, execution.ErrNotFound
}

type fakeBindingResolver struct {
	bindings []execution.SUTBinding
	err      error
}

func (f *fakeBindingResolver) Resolve(context.Context, int64) ([]execution.SUTBinding, error) {
	return f.bindings, f.err
}

// fakeExecutionExecutor is the in-memory executor protocol v2 port: sessions,
// general commands, the recording-ledger pull and the retained browser bridge.
type fakeExecutionExecutor struct {
	mu        sync.Mutex
	sessions  int
	sessionID string
	// networkLog is the raw recorded browser-traffic payload served by NetworkLog
	// (T16 passive CDP recording; replaces the retired bridge's network action).
	networkLog         json.RawMessage
	networkLogErr      error
	networkLogCalls    int
	networkLogSessions []string
	networkLogTails    []int
	// networkLogErrBySession fails the read of one session only, so a
	// multi-session test can prove the capture is per-session best effort.
	networkLogErrBySession map[string]error
	closed                 int
	canceled               int
	lastSpec               executionapp.SessionSpec
	lastExecutorID         int64
	createErr              error
	// leaseEpochs records the epoch every write carried (fencing contract).
	leaseEpochs []int64
	// commands records the general-execution calls.
	commands    []executionapp.ExecCommand
	execOutcome executionapp.ExecOutcome
	execErr     error
	// execErrBySession fails the command of one session only, so a test can
	// simulate a session that vanished mid-run while later ones still work.
	execErrBySession map[string]error
	// createdSessions records every session id handed out, in creation order.
	createdSessions []string
	// artifactPages are served in order, one per Artifacts pull; the last one
	// repeats so a settlement drain after the scripted pages sees no new items.
	artifactPages []executionapp.ArtifactPage
	artifactPulls []int64
	artifactsErr  error
	traceRef      executionapp.TraceRef
	traceProbes   int
	renewedEpochs []int64
	renewErr      error
	// shellOps/fileOps record the general-execution shell/PTY and workspace file
	// calls (op vocabulary + argument shape + fencing epoch).
	shellOps    []executionapp.ShellOperation
	shellResult executionapp.ShellOutcome
	shellErr    error
	// shellErrBySession fails the shell op of one session only (vanished-session
	// recovery on the shell path).
	shellErrBySession map[string]error
	fileOps           []executionapp.FileOperation
	fileResult        executionapp.FileOutcome
	fileErr           error
}

func (f *fakeExecutionExecutor) CreateSession(_ context.Context, spec executionapp.SessionSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.sessions++
	f.lastSpec = spec
	f.leaseEpochs = append(f.leaseEpochs, spec.LeaseEpoch)
	id := f.sessionID
	if id == "" {
		// Sequential ids so a test can prove a dead reservation was REPLACED.
		id = fmt.Sprintf("sess_%d", f.sessions)
	}
	f.createdSessions = append(f.createdSessions, id)
	return id, nil
}

func (f *fakeExecutionExecutor) Exec(_ context.Context, executorID int64, sessionID string, command executionapp.ExecCommand, leaseEpoch int64) (executionapp.ExecOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastExecutorID = executorID
	f.commands = append(f.commands, command)
	f.leaseEpochs = append(f.leaseEpochs, leaseEpoch)
	outcome := f.execOutcome
	outcome.SessionID = sessionID
	if err := f.execErrBySession[sessionID]; err != nil {
		return outcome, err
	}
	return outcome, f.execErr
}

func (f *fakeExecutionExecutor) Artifacts(_ context.Context, executorID int64, _ string, since int64) (executionapp.ArtifactPage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastExecutorID = executorID
	f.artifactPulls = append(f.artifactPulls, since)
	if f.artifactsErr != nil {
		return executionapp.ArtifactPage{}, f.artifactsErr
	}
	if len(f.artifactPulls) <= len(f.artifactPages) {
		return f.artifactPages[len(f.artifactPulls)-1], nil
	}
	if len(f.artifactPages) == 0 {
		return executionapp.ArtifactPage{}, nil
	}
	last := f.artifactPages[len(f.artifactPages)-1]
	return executionapp.ArtifactPage{NextCursor: last.NextCursor, BaseCursor: last.BaseCursor}, nil
}

func (f *fakeExecutionExecutor) Trace(context.Context, int64, string) (executionapp.TraceRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.traceProbes++
	return f.traceRef, nil
}

func (f *fakeExecutionExecutor) RenewLease(_ context.Context, _ int64, _ string, leaseEpoch int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renewedEpochs = append(f.renewedEpochs, leaseEpoch)
	return f.renewErr
}

func (f *fakeExecutionExecutor) Shell(_ context.Context, executorID int64, sessionID string, op executionapp.ShellOperation, leaseEpoch int64) (executionapp.ShellOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastExecutorID = executorID
	f.shellOps = append(f.shellOps, op)
	f.leaseEpochs = append(f.leaseEpochs, leaseEpoch)
	if err := f.shellErrBySession[sessionID]; err != nil {
		return executionapp.ShellOutcome{}, err
	}
	return f.shellResult, f.shellErr
}

func (f *fakeExecutionExecutor) Files(_ context.Context, executorID int64, _ string, op executionapp.FileOperation, leaseEpoch int64) (executionapp.FileOutcome, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastExecutorID = executorID
	f.fileOps = append(f.fileOps, op)
	f.leaseEpochs = append(f.leaseEpochs, leaseEpoch)
	return f.fileResult, f.fileErr
}

func (f *fakeExecutionExecutor) NetworkLog(_ context.Context, executorID int64, sessionID string, tail int) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastExecutorID = executorID
	f.networkLogCalls++
	f.networkLogSessions = append(f.networkLogSessions, sessionID)
	f.networkLogTails = append(f.networkLogTails, tail)
	if err := f.networkLogErrBySession[sessionID]; err != nil {
		return nil, err
	}
	return f.networkLog, f.networkLogErr
}

func (f *fakeExecutionExecutor) Cancel(context.Context, int64, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled++
	return nil
}

func (f *fakeExecutionExecutor) Close(context.Context, int64, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

type fakeCredentialReader struct {
	account  executionapp.AccountCredential
	database executionapp.DatabaseCredential
	err      error
	roles    []string
	// sutIDs records every SUT a credential was requested for, in call order, so
	// a test can prove WHICH SUT an explicit `sut_id` tool argument
	// decoded to rather than inferring it from a single-binding fixture.
	sutIDs []int64
}

func (f *fakeCredentialReader) Account(_ context.Context, _, sutID int64, role string) (executionapp.AccountCredential, error) {
	f.roles = append(f.roles, role)
	f.sutIDs = append(f.sutIDs, sutID)
	return f.account, f.err
}

func (f *fakeCredentialReader) Database(_ context.Context, _, sutID int64, _ *int64) (executionapp.DatabaseCredential, error) {
	f.sutIDs = append(f.sutIDs, sutID)
	return f.database, f.err
}

type fakeContentStore struct {
	mu      sync.Mutex
	puts    [][]byte
	nextRef int64
	putErr  error
	open    []byte
}

func (f *fakeContentStore) PutEvidence(_ context.Context, _ int64, _ execution.EvidenceKind, _ string, data []byte) (executionapp.ContentRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.putErr != nil {
		return executionapp.ContentRef{}, f.putErr
	}
	f.puts = append(f.puts, data)
	f.nextRef++
	// Content-addressed like the real storage producer: the ref SHA-256 is the
	// sha256 of the stored bytes (the content_ref the model reads back).
	sum := sha256.Sum256(data)
	return executionapp.ContentRef{ID: 7000 + f.nextRef, SHA256: hex.EncodeToString(sum[:]), SizeBytes: int64(len(data)), MIME: "text/plain"}, nil
}

func (f *fakeContentStore) Open(context.Context, string) ([]byte, error) {
	return f.open, nil
}

type fakeHTTPDoer struct {
	response executionapp.HTTPResponseData
	err      error
	lastReq  executionapp.HTTPRequestData
}

func (f *fakeHTTPDoer) Do(_ context.Context, req executionapp.HTTPRequestData) (executionapp.HTTPResponseData, error) {
	f.lastReq = req
	return f.response, f.err
}

type fakeDatabaseQuerier struct {
	queryResult  executionapp.DBQueryResult
	schemaResult []executionapp.TableSchema
	err          error
	lastSQL      string
	lastOpts     executionapp.DBQueryOptions
}

func (f *fakeDatabaseQuerier) Query(_ context.Context, _, _, _, sql string, opts executionapp.DBQueryOptions) (executionapp.DBQueryResult, error) {
	f.lastSQL = sql
	f.lastOpts = opts
	return f.queryResult, f.err
}

func (f *fakeDatabaseQuerier) Schema(context.Context, string, string, string, string) ([]executionapp.TableSchema, error) {
	return f.schemaResult, f.err
}

type fakeGapEnsurer struct {
	calls []int64
}

func (f *fakeGapEnsurer) EnsureGap(context.Context, int64, int64, int64, string) error {
	f.calls = append(f.calls, 1)
	return nil
}

type fakeCoverageReader struct {
	requirements []executionapp.RequirementRef
}

func (f *fakeCoverageReader) ListRequirements(context.Context, int64) ([]executionapp.RequirementRef, error) {
	return f.requirements, nil
}

type fakeTeamRoom struct {
	posts []executionapp.TeamPost
}

func (f *fakeTeamRoom) Post(_ context.Context, post executionapp.TeamPost) error {
	f.posts = append(f.posts, post)
	return nil
}

type fakeCaseRunnerSettings struct {
	ints    map[string]int64
	strings map[string]string
}

func (f *fakeCaseRunnerSettings) String(_ context.Context, key string) (string, error) {
	if value, ok := f.strings[key]; ok {
		return value, nil
	}
	return "", platform.NotFound("setting")
}

func (f *fakeCaseRunnerSettings) Int(_ context.Context, key string) (int64, error) {
	if value, ok := f.ints[key]; ok {
		return value, nil
	}
	return 0, platform.NotFound("setting")
}

func (f *fakeCaseRunnerSettings) Bool(context.Context, string) (bool, error) { return false, nil }

type caseRunnerClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *caseRunnerClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *caseRunnerClock) Advance(delta time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(delta)
}

type caseRunnerIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *caseRunnerIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

type caseRunnerEvents struct {
	mu     sync.Mutex
	events []eventingapp.Event
}

func (f *caseRunnerEvents) Publish(_ context.Context, event eventingapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event)
	return nil
}

func (f *caseRunnerEvents) types() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	types := make([]string, 0, len(f.events))
	for _, event := range f.events {
		types = append(types, event.Type)
	}
	return types
}

type caseRunnerAudit struct {
	mu     sync.Mutex
	events []string
}

func (f *caseRunnerAudit) Record(_ context.Context, event auditapp.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, event.Action)
	return nil
}
