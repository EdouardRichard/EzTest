package executionapp_test

import (
	"context"
	"encoding/json"
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

func (f *fakeIntentRepo) ListOpen(context.Context, int64) ([]execution.ExecutionIntent, error) {
	return nil, nil
}

func (f *fakeIntentRepo) ListActive(context.Context, int64) ([]execution.ExecutionIntent, error) {
	return nil, nil
}

func (f *fakeIntentRepo) Transition(_ context.Context, id int64, from []execution.IntentStatus, to execution.IntentStatus) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	mu      sync.Mutex
	rows    map[int64]execution.CaseRun
	updates int
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

func (f *fakeCaseRunRepo) UpdateCAS(_ context.Context, id, epoch int64, token string, mutate func(*execution.CaseRun) error) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
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
	if _, ok := f.rows[caseRun.ID]; !ok {
		return false, nil
	}
	f.rows[caseRun.ID] = caseRun
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
	row, ok := f.rows[id]
	if !ok || row.DispatchEpoch != epoch {
		return false, nil
	}
	row.LeaseUntil = &until
	f.rows[id] = row
	return true, nil
}

func (f *fakeCaseRunRepo) ListRunningByExecutor(context.Context, int64) ([]execution.CaseRun, error) {
	return nil, nil
}

func (f *fakeCaseRunRepo) LatestByCaseVersions(context.Context, int64, []int64) (map[int64]execution.CaseRun, error) {
	return map[int64]execution.CaseRun{}, nil
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
}

func newFakeSUTContextRepo() *fakeSUTContextRepo {
	return &fakeSUTContextRepo{rows: map[int64]execution.CaseRunSUTContext{}}
}

func (f *fakeSUTContextRepo) Create(_ context.Context, sutContext execution.CaseRunSUTContext) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.CaseRunID == sutContext.CaseRunID && row.VersionSUTID == sutContext.VersionSUTID &&
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

func (f *fakeSUTContextRepo) row(versionSUTID int64, role string) (execution.CaseRunSUTContext, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, row := range f.rows {
		if row.VersionSUTID == versionSUTID && row.AccountRole == role {
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

func (f *fakeEvidenceRepo) ListByVersion(context.Context, int64, execution.EvidenceFilter) ([]execution.Evidence, bool, error) {
	return nil, false, nil
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
		if row.CaseRunID != nil && *row.CaseRunID == caseRunID && row.Status == execution.HumanOpen {
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

func (f *fakeHumanRepo) CountByCaseRun(context.Context, int64) (int, error) { return 0, nil }

func (f *fakeHumanRepo) Transition(context.Context, int64, execution.HumanRequestStatus, execution.HumanRequestStatus, map[string]any, int64, time.Time) (bool, error) {
	return false, nil
}

func (f *fakeHumanRepo) Expire(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

// --- readers / ports -------------------------------------------------------

type fakeExecutionCaseReader struct {
	snapshot execution.CaseSnapshot
	err      error
}

func (f *fakeExecutionCaseReader) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	if f.err != nil {
		return execution.CaseSnapshot{}, f.err
	}
	return f.snapshot, nil
}

func (f *fakeExecutionCaseReader) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return nil, nil
}

type fakeBindingResolver struct {
	bindings []execution.SUTBinding
	err      error
}

func (f *fakeBindingResolver) Resolve(context.Context, int64) ([]execution.SUTBinding, error) {
	return f.bindings, f.err
}

type fakeBrowserExecutor struct {
	mu        sync.Mutex
	contexts  int
	actions   []string
	lastArgs  json.RawMessage
	contextID string
	actionErr error
	result    executionapp.BrowserActionResult
	closed    int
	canceled  int
	lastSpec  executionapp.BrowserContextSpec
	createErr error
}

func (f *fakeBrowserExecutor) CreateContext(_ context.Context, spec executionapp.BrowserContextSpec) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return "", f.createErr
	}
	f.contexts++
	f.lastSpec = spec
	if f.contextID != "" {
		return f.contextID, nil
	}
	return "ctx_1", nil
}

func (f *fakeBrowserExecutor) Action(_ context.Context, contextID, action string, args json.RawMessage) (executionapp.BrowserActionResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.actions = append(f.actions, action)
	f.lastArgs = args
	return f.result, f.actionErr
}

func (f *fakeBrowserExecutor) Cancel(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.canceled++
	return nil
}

func (f *fakeBrowserExecutor) Close(context.Context, string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed++
	return nil
}

func (f *fakeBrowserExecutor) ListContexts(context.Context) ([]executionapp.BrowserContextRef, error) {
	return nil, nil
}

type fakeCredentialReader struct {
	account  executionapp.AccountCredential
	database executionapp.DatabaseCredential
	err      error
	roles    []string
}

func (f *fakeCredentialReader) Account(_ context.Context, _, _ int64, role string) (executionapp.AccountCredential, error) {
	f.roles = append(f.roles, role)
	return f.account, f.err
}

func (f *fakeCredentialReader) Database(context.Context, int64, int64, *int64) (executionapp.DatabaseCredential, error) {
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
	return executionapp.ContentRef{ID: 7000 + f.nextRef, SHA256: "sha", SizeBytes: int64(len(data)), MIME: "text/plain"}, nil
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
}

func (f *fakeDatabaseQuerier) Query(_ context.Context, _, _, _, sql string, _ int) (executionapp.DBQueryResult, error) {
	f.lastSQL = sql
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

type caseRunnerClock struct{ now time.Time }

func (c *caseRunnerClock) Now() time.Time { return c.now }

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
