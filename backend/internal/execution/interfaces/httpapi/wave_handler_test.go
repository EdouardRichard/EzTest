package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
)

// W1-7 D1 contract: POST /api/versions/{id}/waves starts a wave immediately from
// the optional case selection (empty = every current+confirmed case).

type waveHandlerWaves struct {
	mu     sync.Mutex
	byID   map[int64]execution.RunWave
	runs   map[int64][]execution.CaseRun
	nextID int64
}

func newWaveHandlerWaves() *waveHandlerWaves {
	return &waveHandlerWaves{
		byID: map[int64]execution.RunWave{},
		runs: map[int64][]execution.CaseRun{},
	}
}

func (w *waveHandlerWaves) seed(wave execution.RunWave) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if wave.CreatedAt.IsZero() {
		wave.CreatedAt = time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	}
	if wave.UpdatedAt.IsZero() {
		wave.UpdatedAt = wave.CreatedAt
	}
	w.byID[wave.ID] = wave
}

func (w *waveHandlerWaves) CreateWithCaseRuns(_ context.Context, wave execution.RunWave, _ []execution.ExecutionIntent, caseRuns []execution.CaseRun) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if wave.ID == 0 {
		w.nextID++
		wave.ID = 700 + w.nextID
	}
	if wave.CreatedAt.IsZero() {
		wave.CreatedAt = time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	}
	if wave.UpdatedAt.IsZero() {
		wave.UpdatedAt = wave.CreatedAt
	}
	w.byID[wave.ID] = wave
	w.runs[wave.ID] = append([]execution.CaseRun(nil), caseRuns...)
	return nil
}

func (w *waveHandlerWaves) Get(_ context.Context, id int64) (execution.RunWave, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	wave, ok := w.byID[id]
	if !ok {
		return execution.RunWave{}, execution.ErrNotFound
	}
	return wave, nil
}

func (w *waveHandlerWaves) GetActive(_ context.Context, versionID int64) (*execution.RunWave, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, wave := range w.byID {
		if wave.VersionID == versionID && wave.Status.Active() {
			copied := wave
			return &copied, nil
		}
	}
	return nil, nil
}

func (w *waveHandlerWaves) List(_ context.Context, versionID int64, _ int, _ *int64) ([]execution.RunWave, bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	items := make([]execution.RunWave, 0)
	for _, wave := range w.byID {
		if wave.VersionID == versionID {
			items = append(items, wave)
		}
	}
	return items, false, nil
}

func (w *waveHandlerWaves) Update(_ context.Context, wave execution.RunWave) (bool, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, ok := w.byID[wave.ID]; !ok {
		return false, nil
	}
	w.byID[wave.ID] = wave
	return true, nil
}

func (w *waveHandlerWaves) Transition(context.Context, int64, []execution.WaveStatus, execution.WaveStatus, execution.RunWave) (bool, error) {
	return false, nil
}

func (w *waveHandlerWaves) Counts(_ context.Context, waveID int64) (execution.WaveCounts, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	counts := execution.WaveCounts{ByVersionSUT: map[string]execution.SUTCounts{}}
	for range w.runs[waveID] {
		counts.Total++
		counts.Pending++
	}
	return counts, nil
}

func (w *waveHandlerWaves) ListActive(context.Context) ([]execution.RunWave, error) {
	return nil, nil
}

type waveHandlerVersions struct {
	ids map[int64]bool
	// stage overrides the version-stage projection; empty means executing.
	stage string
}

func (v *waveHandlerVersions) Get(_ context.Context, id int64) (executionapp.VersionBinding, error) {
	if !v.ids[id] {
		return executionapp.VersionBinding{}, execution.ErrNotFound
	}
	stage := v.stage
	if stage == "" {
		stage = "executing"
	}
	return executionapp.VersionBinding{VersionID: id, Stage: stage}, nil
}

func (v *waveHandlerVersions) Reopen(context.Context, int64) error { return nil }

type waveHandlerIntents struct {
	ensureAll      int
	ensureForCases [][]int64
	err            error
}

func (s *waveHandlerIntents) EnsureInitialIntents(context.Context, int64) (int, error) {
	s.ensureAll++
	return 0, s.err
}

func (s *waveHandlerIntents) EnsureInitialIntentsForCases(_ context.Context, _ int64, caseIDs []int64) (int, error) {
	s.ensureForCases = append(s.ensureForCases, append([]int64(nil), caseIDs...))
	return 0, s.err
}

func (s *waveHandlerIntents) CreateManual(context.Context, int64, int64, execution.Purpose, int64) (execution.ExecutionIntent, error) {
	return execution.ExecutionIntent{}, execution.ErrNotFound
}

type waveHandlerPlanner struct {
	waves    *waveHandlerWaves
	calls    int
	versions []int64
	// create controls whether the planner manufactures a planned wave, i.e.
	// whether the version had a dispatchable case.
	create bool
	runs   int
	err    error
}

func (p *waveHandlerPlanner) ReconcileVersion(_ context.Context, versionID int64) error {
	p.calls++
	p.versions = append(p.versions, versionID)
	if p.err != nil {
		return p.err
	}
	if !p.create {
		return nil
	}
	wave := execution.RunWave{ID: 7001, VersionID: versionID, Status: execution.WavePlanned}
	caseRuns := make([]execution.CaseRun, 0, p.runs)
	for index := 0; index < p.runs; index++ {
		caseRuns = append(caseRuns, execution.CaseRun{
			ID: int64(8000 + index), WaveID: wave.ID, Status: execution.CaseRunPending,
		})
	}
	return p.waves.CreateWithCaseRuns(context.Background(), wave, nil, caseRuns)
}

type startWaveFixture struct {
	handler  *Handler
	waves    *waveHandlerWaves
	versions *waveHandlerVersions
	intents  *waveHandlerIntents
	planner  *waveHandlerPlanner
}

func newStartWaveFixture() *startWaveFixture {
	waves := newWaveHandlerWaves()
	versions := &waveHandlerVersions{ids: map[int64]bool{7: true}}
	intents := &waveHandlerIntents{}
	planner := &waveHandlerPlanner{waves: waves}
	return &startWaveFixture{
		handler:  New(Deps{Waves: waves, Versions: versions, IntentService: intents, Planner: planner}),
		waves:    waves,
		versions: versions,
		intents:  intents,
		planner:  planner,
	}
}

// startWaveCases is an empty executable-case projection: the intent-service
// integration fixtures assert which scan the handler chooses, not what the scan
// creates, so the scan can complete with zero cases.
type startWaveCases struct{}

func (startWaveCases) GetCaseVersion(context.Context, int64, int64) (execution.CaseSnapshot, error) {
	return execution.CaseSnapshot{}, execution.ErrNotFound
}

func (startWaveCases) ListExecutable(context.Context, int64) ([]execution.CaseSnapshot, error) {
	return nil, nil
}

func (startWaveCases) ResolveReplacement(context.Context, int64, int64) (execution.CaseSnapshot, bool, error) {
	return execution.CaseSnapshot{}, false, nil
}

func (startWaveCases) CaseVersionLabel(context.Context, int64, int64) (executionapp.CaseVersionLabel, error) {
	return executionapp.CaseVersionLabel{}, execution.ErrNotFound
}

// startWaveServiceIntents delegates to the real intent service (so the D23
// stage/selection semantics are exercised) while recording which scan the
// handler chose.
type startWaveServiceIntents struct {
	service     *executionapp.IntentService
	ensureAll   int
	forCaseCall [][]int64
}

func (s *startWaveServiceIntents) EnsureInitialIntents(ctx context.Context, versionID int64) (int, error) {
	s.ensureAll++
	return s.service.EnsureInitialIntents(ctx, versionID)
}

func (s *startWaveServiceIntents) EnsureInitialIntentsForCases(ctx context.Context, versionID int64, caseIDs []int64) (int, error) {
	s.forCaseCall = append(s.forCaseCall, append([]int64(nil), caseIDs...))
	return s.service.EnsureInitialIntentsForCases(ctx, versionID, caseIDs)
}

func (s *startWaveServiceIntents) CreateManual(ctx context.Context, versionID, caseVersionID int64, purpose execution.Purpose, actorID int64) (execution.ExecutionIntent, error) {
	return s.service.CreateManual(ctx, versionID, caseVersionID, purpose, actorID)
}

// startWaveServiceFixture wires the handler to the real intent service with no
// BatchSelectionReader, which is exactly the "executing version without a
// persisted execute.batch selection" state the D23 repair scan cannot handle.
type startWaveServiceFixture struct {
	handler  *Handler
	waves    *waveHandlerWaves
	versions *waveHandlerVersions
	intents  *startWaveServiceIntents
	planner  *waveHandlerPlanner
}

func newStartWaveServiceFixture(stage string) *startWaveServiceFixture {
	waves := newWaveHandlerWaves()
	versions := &waveHandlerVersions{ids: map[int64]bool{7: true}, stage: stage}
	intents := &startWaveServiceIntents{service: executionapp.NewIntentService(executionapp.IntentServiceDeps{
		Cases:    startWaveCases{},
		Versions: versions,
	})}
	planner := &waveHandlerPlanner{waves: waves}
	return &startWaveServiceFixture{
		handler:  New(Deps{Waves: waves, Versions: versions, IntentService: intents, Planner: planner}),
		waves:    waves,
		versions: versions,
		intents:  intents,
		planner:  planner,
	}
}

func postStartWave(t *testing.T, handler *Handler, body string) *httptest.ResponseRecorder {
	t.Helper()
	requestPath := "/api/versions/7/waves"
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(http.MethodPost, requestPath, nil)
	} else {
		request = httptest.NewRequest(http.MethodPost, requestPath, strings.NewReader(body))
	}
	request.SetPathValue("id", "7")
	recorder := httptest.NewRecorder()
	handler.StartWave(recorder, request)
	return recorder
}

type startWaveEnvelope struct {
	Data *struct {
		WaveID       string `json:"wave_id"`
		Status       string `json:"status"`
		CaseRunCount int    `json:"case_run_count"`
	} `json:"data"`
	Error *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func decodeStartWave(t *testing.T, recorder *httptest.ResponseRecorder) startWaveEnvelope {
	t.Helper()
	var envelope startWaveEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope
}

func TestStartWaveCreatesWaveAndListsIt(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = true
	f.planner.runs = 2

	recorder := postStartWave(t, f.handler, "")
	t.Logf("POST /api/versions/7/waves -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST waves = %d body=%s, want 201", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Data == nil {
		t.Fatalf("missing data: %s", recorder.Body.String())
	}
	if envelope.Data.WaveID != "7001" || envelope.Data.Status != string(execution.WavePlanned) {
		t.Fatalf("data = %+v, want wave 7001 planned", envelope.Data)
	}
	if envelope.Data.CaseRunCount != 2 {
		t.Fatalf("case_run_count = %d, want 2 (the wave's case runs)", envelope.Data.CaseRunCount)
	}
	if f.planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", f.planner.calls)
	}
	if f.intents.ensureAll != 0 || len(f.intents.ensureForCases) != 1 || f.intents.ensureForCases[0] != nil {
		t.Fatalf("intent scan = all %d / selection %v, want one explicit ForCases(nil) scan",
			f.intents.ensureAll, f.intents.ensureForCases)
	}

	// The active wave is now visible through the existing GET /waves route.
	listRequest := httptest.NewRequest(http.MethodGet, "/api/versions/7/waves", nil)
	listRequest.SetPathValue("id", "7")
	listRecorder := httptest.NewRecorder()
	f.handler.ListWaves(listRecorder, listRequest)
	t.Logf("GET /api/versions/7/waves -> %d %s", listRecorder.Code, strings.TrimSpace(listRecorder.Body.String()))
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("GET waves = %d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var listBody struct {
		Data []RunWaveView `json:"data"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode GET waves: %v", err)
	}
	if len(listBody.Data) != 1 || listBody.Data[0].ID != "7001" || listBody.Data[0].Status != string(execution.WavePlanned) {
		t.Fatalf("GET waves = %+v", listBody.Data)
	}
}

func TestStartWaveRejectsSecondActiveWave(t *testing.T) {
	f := newStartWaveFixture()
	f.waves.seed(execution.RunWave{ID: 9001, VersionID: 7, Status: execution.WaveRunning})

	recorder := postStartWave(t, f.handler, "")
	t.Logf("POST with active wave -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST waves with active wave = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Error == nil || envelope.Error.Code != "state_conflict" {
		t.Fatalf("error = %+v, want state_conflict", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "already active") {
		t.Fatalf("message = %q, want an already active wave instruction", envelope.Error.Message)
	}
	if f.planner.calls != 0 {
		t.Fatalf("planner calls = %d, want 0", f.planner.calls)
	}
	if f.intents.ensureAll != 0 || len(f.intents.ensureForCases) != 0 {
		t.Fatalf("intent service ran despite the active wave")
	}
}

func TestStartWaveConflictsWhenNothingWasPlanned(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = false

	recorder := postStartWave(t, f.handler, "")
	t.Logf("POST with no dispatchable case -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST waves without dispatchable cases = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Error == nil || envelope.Error.Code != "state_conflict" {
		t.Fatalf("error = %+v, want state_conflict", envelope.Error)
	}
	if !strings.Contains(envelope.Error.Message, "no dispatchable cases") {
		t.Fatalf("message = %q, want the no-dispatchable-cases explanation", envelope.Error.Message)
	}
	if f.planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", f.planner.calls)
	}
}

func TestStartWaveRejectsInvalidCaseID(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = true

	recorder := postStartWave(t, f.handler, `{"case_ids":["abc"]}`)
	t.Logf("POST invalid case_ids -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("POST waves invalid case_ids = %d body=%s, want 400", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Error == nil || envelope.Error.Code != "invalid_argument" {
		t.Fatalf("error = %+v, want invalid_argument", envelope.Error)
	}
	if f.planner.calls != 0 || f.intents.ensureAll != 0 || len(f.intents.ensureForCases) != 0 {
		t.Fatalf("work ran for an invalid body (planner %d, all %d, selection %v)",
			f.planner.calls, f.intents.ensureAll, f.intents.ensureForCases)
	}
}

func TestStartWaveSelectionEnsuresOnlySelectedCases(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = true
	f.planner.runs = 2

	recorder := postStartWave(t, f.handler, `{"case_ids":["11","12"]}`)
	t.Logf("POST selection -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST waves with selection = %d body=%s, want 201", recorder.Code, recorder.Body.String())
	}
	if len(f.intents.ensureForCases) != 1 || len(f.intents.ensureForCases[0]) != 2 ||
		f.intents.ensureForCases[0][0] != 11 || f.intents.ensureForCases[0][1] != 12 {
		t.Fatalf("selection scan = %v, want [[11 12]]", f.intents.ensureForCases)
	}
	if f.intents.ensureAll != 0 {
		t.Fatalf("unselected scan ran for a selection request (%d)", f.intents.ensureAll)
	}
	if f.planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", f.planner.calls)
	}
}

func TestStartWaveEmptySelectionEnsuresAllCases(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = true

	recorder := postStartWave(t, f.handler, `{"case_ids":[]}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST waves with empty selection = %d body=%s, want 201", recorder.Code, recorder.Body.String())
	}
	if f.intents.ensureAll != 0 || len(f.intents.ensureForCases) != 1 || f.intents.ensureForCases[0] != nil {
		t.Fatalf("intent scan = all %d / selection %v, want one explicit ForCases(nil) scan",
			f.intents.ensureAll, f.intents.ensureForCases)
	}
}

// W1-7 fix 1: an empty body means "all current+confirmed cases". It must use the
// explicit ForCases(nil) scan, not the D23 repair scan: the repair scan has no
// persisted execute.batch selection to scope it on an executing version and
// returns ErrNoPersistedBatchSelection (a 500), while the contract's empty
// selection is a valid all-current+confirmed batch.
func TestStartWaveEmptyBodyUsesExplicitAllCasesScanWithoutPersistedSelection(t *testing.T) {
	f := newStartWaveServiceFixture("executing")
	f.planner.create = true
	f.planner.runs = 2

	recorder := postStartWave(t, f.handler, "")
	t.Logf("POST empty body without persisted selection -> %d %s",
		recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST waves without a persisted batch selection = %d body=%s, want 201",
			recorder.Code, recorder.Body.String())
	}
	if len(f.intents.forCaseCall) != 1 || f.intents.forCaseCall[0] != nil {
		t.Fatalf("explicit selection scan = %v, want exactly one ForCases(nil) call",
			f.intents.forCaseCall)
	}
	if f.intents.ensureAll != 0 {
		t.Fatalf("D23 repair scan ran for an empty body (%d)", f.intents.ensureAll)
	}
	if f.planner.calls != 1 {
		t.Fatalf("planner calls = %d, want 1", f.planner.calls)
	}
}

// The empty body keeps the typed stage conflict: on a version that is not at
// executing the explicit scan answers 409 invalid_stage_for_execution and no
// wave is planned.
func TestStartWaveEmptyBodyNonExecutingIsConflict(t *testing.T) {
	f := newStartWaveServiceFixture("case_review")
	f.planner.create = true
	f.planner.runs = 1

	recorder := postStartWave(t, f.handler, "")
	t.Logf("POST empty body at case_review -> %d %s", recorder.Code, strings.TrimSpace(recorder.Body.String()))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("POST waves at case_review = %d body=%s, want 409", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Error == nil || envelope.Error.Code != "invalid_stage_for_execution" {
		t.Fatalf("error = %+v, want invalid_stage_for_execution", envelope.Error)
	}
	if f.planner.calls != 0 {
		t.Fatalf("planner calls = %d, want 0 before the stage conflict", f.planner.calls)
	}
	if len(f.intents.forCaseCall) != 1 || f.intents.forCaseCall[0] != nil {
		t.Fatalf("explicit selection scan = %v, want exactly one ForCases(nil) call",
			f.intents.forCaseCall)
	}
}

func TestStartWaveUnavailableWithoutPlanner(t *testing.T) {
	f := newStartWaveFixture()
	f.handler = New(Deps{Waves: f.waves, Versions: f.versions, IntentService: f.intents})

	recorder := postStartWave(t, f.handler, "")
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("POST waves without a planner = %d body=%s, want 503", recorder.Code, recorder.Body.String())
	}
	envelope := decodeStartWave(t, recorder)
	if envelope.Error == nil || envelope.Error.Code != "unavailable" {
		t.Fatalf("error = %+v, want unavailable", envelope.Error)
	}
}

func TestStartWaveRouteRegistered(t *testing.T) {
	f := newStartWaveFixture()
	f.planner.create = true
	passThrough := func(next http.Handler) http.Handler { return next }
	mux := http.NewServeMux()
	f.handler.Register(mux, passThrough, passThrough)

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/versions/7/waves", nil))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("POST /api/versions/7/waves = %d body=%s, want 201 (route registered)",
			recorder.Code, recorder.Body.String())
	}
}
