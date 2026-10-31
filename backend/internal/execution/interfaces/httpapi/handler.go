// Package httpapi exposes the P3 execution REST API (spec §9.2/§9.3). Handlers
// only parse DTOs, call the execution application services/repositories and map
// stable platform errors; no execution logic lives here.
package httpapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"strings"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/httpx"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/pagination"
	"github.com/EdouardRichard/EzTest/backend/internal/platform/validation"
)

const (
	// evidenceDetailLimit is the CaseRunDetail evidence snapshot size (§9.1).
	evidenceDetailLimit = 50
	// maxOverviewWaves bounds the overview aggregation scan.
	maxOverviewWaves = 1000
	// maxOpenHumanScan bounds the overview open-request count scan.
	maxOpenHumanScan = 1000
	// settingHumanRequestTimeout is the login-takeover expiry setting (§11.2).
	settingHumanRequestTimeout = "execution.human_request_timeout_seconds"
)

// Deps is the fully wired execution surface; cmd/eztest constructs it, tests
// build it over the real repositories/services.
type Deps struct {
	Intents   executionapp.IntentRepository
	Waves     executionapp.WaveRepository
	CaseRuns  executionapp.CaseRunRepository
	Results   executionapp.ResultRepository
	Evidence  executionapp.EvidenceRepository
	Humans    executionapp.HumanRequestRepository
	Executors executionapp.ExecutorRepository
	Cases     executionapp.CaseReader
	Versions  executionapp.VersionReader
	Content   executionapp.ContentStore
	Settings  executionapp.SettingsReader

	IntentService *executionapp.IntentService
	PauseService  *executionapp.PauseService
	HumanService  *executionapp.HumanService
	Registry      *executionapp.Registry
}

// Handler serves the P3 execution endpoints.
type Handler struct {
	deps Deps
}

// New builds the execution HTTP handler.
func New(deps Deps) *Handler {
	return &Handler{deps: deps}
}

/* --------------------------------- overview -------------------------------- */

// Overview GET /api/versions/{id}/execution/overview.
func (h *Handler) Overview(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}

	counts := emptyWaveCounts()
	var activeView *RunWaveView
	active, err := h.deps.Waves.GetActive(ctx, versionID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "wave"))
		return
	}
	if active != nil {
		counts = active.Counts.Normalize()
		view := toRunWaveView(*active)
		activeView = &view
	}

	versionCounts := emptyWaveCounts()
	if err := h.forEachWave(ctx, versionID, func(wave execution.RunWave) {
		addWaveCounts(&versionCounts, wave.Counts)
	}); err != nil {
		httpx.Error(w, r, err)
		return
	}

	openHumans, err := h.countOpenHumanRequests(ctx, versionID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	executorView, err := h.executorStatus(ctx)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, ExecutionOverviewView{
		VersionID:                  platformid.Format(versionID),
		ActiveWave:                 activeView,
		Counts:                     counts,
		CaseRunCounts:              toCaseRunCounts(versionCounts),
		OpenHumanRequests:          openHumans,
		Executor:                   executorView,
		Quotas:                     h.quotas(ctx),
		HumanRequestTimeoutSeconds: h.settingInt(ctx, settingHumanRequestTimeout, 1800),
	})
}

// forEachWave walks every wave of a version newest first (bounded scan).
func (h *Handler) forEachWave(ctx context.Context, versionID int64, visit func(execution.RunWave)) error {
	var beforeID *int64
	for scanned := 0; scanned < maxOverviewWaves; {
		waves, hasMore, err := h.deps.Waves.List(ctx, versionID, pagination.MaxLimit, beforeID)
		if err != nil {
			return mapRepoError(err, "wave")
		}
		for _, wave := range waves {
			visit(wave)
			scanned++
		}
		if !hasMore || len(waves) == 0 {
			return nil
		}
		last := waves[len(waves)-1].ID
		beforeID = &last
	}
	return nil
}

// countOpenHumanRequests counts the open requests of one version (bounded).
func (h *Handler) countOpenHumanRequests(ctx context.Context, versionID int64) (int, error) {
	count := 0
	var beforeID *int64
	for scanned := 0; scanned < maxOpenHumanScan; {
		items, hasMore, err := h.deps.Humans.ListByVersion(ctx, versionID, string(execution.HumanOpen), pagination.MaxLimit, beforeID)
		if err != nil {
			return 0, mapRepoError(err, "human_request")
		}
		count += len(items)
		scanned += len(items)
		if !hasMore || len(items) == 0 {
			return count, nil
		}
		last := items[len(items)-1].ID
		beforeID = &last
	}
	return count, nil
}

// executorStatus builds the overview executor block for the configured endpoint.
func (h *Handler) executorStatus(ctx context.Context) (ExecutorStatusView, error) {
	endpoint := h.settingString(ctx, executionapp.SettingExecutorURL, "")
	instances, err := h.deps.Executors.List(ctx)
	if err != nil {
		return ExecutorStatusView{}, mapRepoError(err, "executor")
	}
	for index := range instances {
		if normalizeEndpoint(instances[index].Endpoint) == normalizeEndpoint(endpoint) {
			return toExecutorStatusView(&instances[index], endpoint), nil
		}
	}
	if len(instances) > 0 && endpoint == "" {
		return toExecutorStatusView(&instances[0], instances[0].Endpoint), nil
	}
	return toExecutorStatusView(nil, endpoint), nil
}

func (h *Handler) quotas(ctx context.Context) ExecutionQuotas {
	return ExecutionQuotas{
		Global:      h.settingInt(ctx, "execution.global_concurrency", 3),
		PerSUT:      h.settingInt(ctx, "execution.per_sut_concurrency", 2),
		CrossSystem: h.settingInt(ctx, "execution.cross_system_concurrency", 1),
	}
}

/* ---------------------------------- intents -------------------------------- */

// ListIntents GET /api/versions/{id}/execution/intents.
func (h *Handler) ListIntents(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := requireEnum("status", r.URL.Query().Get("status"),
		string(execution.IntentOpen), string(execution.IntentClaimed), string(execution.IntentRunning),
		string(execution.IntentDone), string(execution.IntentFailed), string(execution.IntentCancelled),
		string(execution.IntentSuperseded))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	intents, hasMore, err := h.deps.Intents.List(ctx, versionID, status, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "intent"))
		return
	}
	items := make([]IntentView, 0, len(intents))
	for _, intent := range intents {
		snapshot := h.caseSnapshot(ctx, versionID, intent.CaseVersionID)
		caseID, title := "", ""
		if snapshot != nil {
			caseID = platformid.Format(snapshot.CaseID)
			title = snapshot.Title
		}
		items = append(items, toIntentView(intent, caseID, title))
	}
	httpx.List(w, http.StatusOK, items, nextCursor(hasMore, len(items), lastIntentID(intents)), hasMore)
}

// CreateIntent POST /api/versions/{id}/execution/intents.
func (h *Handler) CreateIntent(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request createIntentRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseVersionID, purpose, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if purpose == execution.PurposeRetest {
		// A retest requires an existing non-passed result (spec §5.8): a manual
		// intent is the way to run a case that has no result yet.
		latest, err := h.deps.CaseRuns.LatestByCaseVersions(ctx, versionID, []int64{caseVersionID})
		if err != nil {
			httpx.Error(w, r, mapRepoError(err, "case run"))
			return
		}
		run, ok := latest[caseVersionID]
		if !ok || (run.Status == execution.CaseRunPassed || run.Status == execution.CaseRunPending || run.Status == execution.CaseRunRunning || run.Status == execution.CaseRunPaused) {
			httpx.Error(w, r, platform.Invalid("purpose", "retest requires an existing failed result; use manual to run a case without a result"))
			return
		}
	}
	intent, err := h.deps.IntentService.CreateManual(ctx, versionID, caseVersionID, purpose, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	snapshot := h.caseSnapshot(ctx, versionID, intent.CaseVersionID)
	caseID, title := "", ""
	if snapshot != nil {
		caseID = platformid.Format(snapshot.CaseID)
		title = snapshot.Title
	}
	httpx.JSON(w, http.StatusCreated, toIntentView(intent, caseID, title))
}

// CancelIntent POST /api/versions/{id}/execution/intents/{intentId}/cancel.
func (h *Handler) CancelIntent(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	intentID, err := pathParamID(r, "intentId", "intent_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	intent, err := h.deps.Intents.Get(ctx, intentID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "intent"))
		return
	}
	if intent.VersionID != versionID {
		httpx.Error(w, r, platform.NotFound("intent"))
		return
	}
	// Cancelling a terminal intent is idempotent (200 current value); an active
	// intent must also supersede its non-terminal case_runs (§5.8).
	if !intent.Status.Terminal() {
		if err := h.deps.PauseService.CancelIntent(ctx, versionID, intentID, actorID); err != nil {
			var apiErr *platform.Error
			if errors.As(err, &apiErr) && apiErr.Code == "state_conflict" {
				if current, getErr := h.deps.Intents.Get(ctx, intentID); getErr == nil && current.Status.Terminal() {
					intent = current
					err = nil
				}
			}
			if err != nil {
				httpx.Error(w, r, err)
				return
			}
		}
	}
	intent, err = h.deps.Intents.Get(ctx, intentID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "intent"))
		return
	}
	snapshot := h.caseSnapshot(ctx, versionID, intent.CaseVersionID)
	caseID, title := "", ""
	if snapshot != nil {
		caseID = platformid.Format(snapshot.CaseID)
		title = snapshot.Title
	}
	httpx.JSON(w, http.StatusOK, toIntentView(intent, caseID, title))
}

/* ----------------------------------- waves --------------------------------- */

// ListWaves GET /api/versions/{id}/waves.
func (h *Handler) ListWaves(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := requireEnum("status", r.URL.Query().Get("status"),
		string(execution.WavePlanned), string(execution.WaveRunning), string(execution.WavePaused),
		string(execution.WaveDone), string(execution.WaveFailed), string(execution.WaveCancelled))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waves, hasMore, err := h.deps.Waves.List(ctx, versionID, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "wave"))
		return
	}
	items := make([]RunWaveView, 0, len(waves))
	for _, wave := range waves {
		if status != "" && string(wave.Status) != status {
			continue
		}
		items = append(items, toRunWaveView(wave))
	}
	lastID := int64(0)
	if len(waves) > 0 {
		lastID = waves[len(waves)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor(hasMore, len(waves), lastID), hasMore)
}

// GetWave GET /api/versions/{id}/waves/{waveId}.
func (h *Handler) GetWave(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := pathParamID(r, "waveId", "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	wave, err := h.waveOfVersion(ctx, versionID, waveID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunWaveView(wave))
}

// PauseWave POST /api/versions/{id}/waves/{waveId}/pause.
func (h *Handler) PauseWave(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := pathParamID(r, "waveId", "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request pauseWaveRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	mode, reason, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	wave, err := h.deps.PauseService.PauseWave(r.Context(), versionID, waveID, actorID, mode, reason)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunWaveView(wave))
}

// ResumeWave POST /api/versions/{id}/waves/{waveId}/resume.
func (h *Handler) ResumeWave(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := pathParamID(r, "waveId", "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	wave, err := h.deps.PauseService.ResumeWave(r.Context(), versionID, waveID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunWaveView(wave))
}

// CancelWave POST /api/versions/{id}/waves/{waveId}/cancel.
func (h *Handler) CancelWave(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := pathParamID(r, "waveId", "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	wave, err := h.deps.PauseService.CancelWave(ctx, versionID, waveID, actorID)
	if err != nil {
		// The frozen contract returns the current value for an already terminal
		// wave (cancel is idempotent); the application keeps that rule for
		// cancelled but rejects done/failed, so map it here.
		var apiErr *platform.Error
		if errors.As(err, &apiErr) && apiErr.Code == "state_conflict" {
			if current, getErr := h.waveOfVersion(ctx, versionID, waveID); getErr == nil && current.Status.Terminal() {
				httpx.JSON(w, http.StatusOK, toRunWaveView(current))
				return
			}
		}
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toRunWaveView(wave))
}

// ListWaveCaseRuns GET /api/versions/{id}/waves/{waveId}/case-runs.
func (h *Handler) ListWaveCaseRuns(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	waveID, err := pathParamID(r, "waveId", "wave_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	if _, err := h.waveOfVersion(ctx, versionID, waveID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := requireEnum("status", r.URL.Query().Get("status"),
		string(execution.CaseRunPending), string(execution.CaseRunRunning), string(execution.CaseRunPaused),
		string(execution.CaseRunPassed), string(execution.CaseRunFailed), string(execution.CaseRunBlocked),
		string(execution.CaseRunInconclusive), string(execution.CaseRunSkipped),
		string(execution.CaseRunInterrupted), string(execution.CaseRunSuperseded))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	runs, hasMore, err := h.deps.CaseRuns.ListByWave(ctx, waveID, status, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "case run"))
		return
	}
	items, err := h.caseRunViews(ctx, versionID, runs)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	lastID := int64(0)
	if len(runs) > 0 {
		lastID = runs[len(runs)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor(hasMore, len(runs), lastID), hasMore)
}

/* --------------------------------- case runs ------------------------------- */

// GetCaseRun GET /api/versions/{id}/case-runs/{caseRunId}.
func (h *Handler) GetCaseRun(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := pathParamID(r, "caseRunId", "case_run_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	run, err := h.caseRunOfVersion(ctx, versionID, caseRunID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.caseRunView(ctx, versionID, run)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	steps, err := h.deps.Results.ListSteps(ctx, run.ID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "step result"))
		return
	}
	assertions, err := h.deps.Results.ListAssertions(ctx, run.ID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "assertion result"))
		return
	}
	evidence, err := h.deps.Evidence.ListByCaseRun(ctx, run.ID, evidenceDetailLimit)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "evidence"))
		return
	}
	detail := CaseRunDetailView{
		CaseRun:    view,
		Steps:      make([]CaseRunStepView, 0, len(steps)),
		Assertions: make([]CaseRunAssertionView, 0, len(assertions)),
		Evidence:   make([]EvidenceView, 0, len(evidence)),
	}
	for _, step := range steps {
		detail.Steps = append(detail.Steps, toCaseRunStepView(step))
	}
	for _, assertion := range assertions {
		detail.Assertions = append(detail.Assertions, toCaseRunAssertionView(assertion))
	}
	for _, item := range evidence {
		detail.Evidence = append(detail.Evidence, toEvidenceView(item))
	}
	httpx.JSON(w, http.StatusOK, detail)
}

// PauseCaseRun POST /api/versions/{id}/case-runs/{caseRunId}/pause.
func (h *Handler) PauseCaseRun(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := pathParamID(r, "caseRunId", "case_run_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request pauseCaseRunRequest
	if err := decodeBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	mode, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	run, err := h.deps.PauseService.PauseCaseRun(ctx, versionID, caseRunID, actorID, mode, "")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.caseRunView(ctx, versionID, run)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

// ResumeCaseRun POST /api/versions/{id}/case-runs/{caseRunId}/resume.
func (h *Handler) ResumeCaseRun(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := pathParamID(r, "caseRunId", "case_run_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	run, err := h.deps.PauseService.ResumeCaseRun(ctx, versionID, caseRunID, actorID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	view, err := h.caseRunView(ctx, versionID, run)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, view)
}

/* --------------------------------- evidence -------------------------------- */

// ListEvidence GET /api/versions/{id}/evidence.
func (h *Handler) ListEvidence(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	caseRunID, err := optionalQueryID(r, "case_run_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	versionSUTID, err := optionalQueryID(r, "version_sut_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	kind, err := requireEnum("kind", r.URL.Query().Get("kind"),
		string(execution.EvidenceScreenshot), string(execution.EvidenceSnapshot),
		string(execution.EvidenceConsole), string(execution.EvidenceNetwork),
		string(execution.EvidenceHTTP), string(execution.EvidenceDB),
		string(execution.EvidenceDownload), string(execution.EvidenceText))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	items, hasMore, err := h.deps.Evidence.ListByVersion(ctx, versionID, execution.EvidenceFilter{
		CaseRunID:    caseRunID,
		VersionSUTID: versionSUTID,
		Kind:         execution.EvidenceKind(kind),
		BeforeID:     beforeID,
		Limit:        limit,
	})
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "evidence"))
		return
	}
	views := make([]EvidenceView, 0, len(items))
	for _, item := range items {
		views = append(views, toEvidenceView(item))
	}
	lastID := int64(0)
	if len(items) > 0 {
		lastID = items[len(items)-1].ID
	}
	httpx.List(w, http.StatusOK, views, nextCursor(hasMore, len(items), lastID), hasMore)
}

// GetEvidence GET /api/versions/{id}/evidence/{evidenceId}.
func (h *Handler) GetEvidence(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	evidenceID, err := pathParamID(r, "evidenceId", "evidence_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	evidence, err := h.evidenceOfVersion(ctx, versionID, evidenceID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toEvidenceView(evidence))
}

// DownloadEvidence GET /api/versions/{id}/evidence/{evidenceId}/content.
//
// It verifies case_run -> wave -> version ownership, re-computes sha256/size from
// the content ledger and refuses a mismatch with 409 state_conflict. The storage
// path is never exposed; Content-Disposition uses a sanitized filename.
func (h *Handler) DownloadEvidence(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	evidenceID, err := pathParamID(r, "evidenceId", "evidence_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	evidence, err := h.evidenceOfVersion(ctx, versionID, evidenceID)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if h.deps.Content == nil {
		httpx.Error(w, r, platform.NewError(http.StatusInternalServerError, "internal", "evidence content store is not configured"))
		return
	}
	data, err := h.deps.Content.Open(ctx, evidence.SHA256)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "evidence content"))
		return
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != evidence.SHA256 || int64(len(data)) != evidence.SizeBytes {
		httpx.Error(w, r, platform.StateConflict("evidence bytes do not match the recorded sha256/size"))
		return
	}
	contentType := evidence.MIME
	if strings.TrimSpace(contentType) == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", platformid.Format(int64(len(data))))
	w.Header().Set("X-Content-SHA256", evidence.SHA256)
	w.Header().Set("Content-Disposition", contentDisposition(evidence.Filename))
	w.Header().Set("Cache-Control", "private, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// contentDisposition builds a safe attachment header; CR/LF/quote characters are
// stripped and the path is never included.
func contentDisposition(filename string) string {
	name := sanitizeFilename(filename)
	if name == "" {
		name = "evidence"
	}
	if formatted := mime.FormatMediaType("attachment", map[string]string{"filename": name}); formatted != "" {
		return formatted
	}
	return "attachment"
}

func sanitizeFilename(filename string) string {
	name := strings.TrimSpace(filename)
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.NewReplacer("\r", "_", "\n", "_", "\"", "_").Replace(name)
	if len(name) > 255 {
		name = name[:255]
	}
	return name
}

/* -------------------------------- human work ------------------------------- */

// ListHumanRequests GET /api/versions/{id}/human-requests.
func (h *Handler) ListHumanRequests(w http.ResponseWriter, r *http.Request) {
	versionID, err := h.versionID(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.requireVersion(ctx, versionID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	status, err := requireEnum("status", r.URL.Query().Get("status"),
		string(execution.HumanOpen), string(execution.HumanResolved),
		string(execution.HumanCancelled), string(execution.HumanExpired))
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	limit, beforeID, err := pageParams(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	requests, hasMore, err := h.deps.Humans.ListByVersion(ctx, versionID, status, limit, beforeID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "human_request"))
		return
	}
	items := make([]HumanRequestView, 0, len(requests))
	for _, request := range requests {
		items = append(items, toHumanRequestView(request))
	}
	lastID := int64(0)
	if len(requests) > 0 {
		lastID = requests[len(requests)-1].ID
	}
	httpx.List(w, http.StatusOK, items, nextCursor(hasMore, len(requests), lastID), hasMore)
}

// ResolveHumanRequest POST /api/human-requests/{humanRequestId}/resolve.
func (h *Handler) ResolveHumanRequest(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	humanRequestID, err := pathParamID(r, "humanRequestId", "human_request_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	var request resolveHumanRequestRequest
	if err := decodeStrictBody(w, r, &request); err != nil {
		httpx.Error(w, r, err)
		return
	}
	response, err := request.parse()
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	resolved, err := h.deps.HumanService.Resolve(r.Context(), humanRequestID, actorID, response)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, toHumanRequestView(resolved))
}

// CancelHumanRequest POST /api/human-requests/{humanRequestId}/cancel.
func (h *Handler) CancelHumanRequest(w http.ResponseWriter, r *http.Request) {
	actorID, err := requireActor(r)
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	humanRequestID, err := pathParamID(r, "humanRequestId", "human_request_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	if err := h.deps.HumanService.Cancel(ctx, humanRequestID, actorID); err != nil {
		httpx.Error(w, r, err)
		return
	}
	request, err := h.deps.Humans.Get(ctx, humanRequestID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "human_request"))
		return
	}
	httpx.JSON(w, http.StatusOK, toHumanRequestView(request))
}

/* ------------------------------ admin executors ---------------------------- */

// ListExecutors GET /api/admin/executors (admin only).
func (h *Handler) ListExecutors(w http.ResponseWriter, r *http.Request) {
	instances, err := h.deps.Registry.List(r.Context())
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "executor"))
		return
	}
	items := make([]ExecutorView, 0, len(instances))
	for _, instance := range instances {
		items = append(items, toExecutorView(instance))
	}
	httpx.List(w, http.StatusOK, items, "", false)
}

// DrainExecutor POST /api/admin/executors/{executorId}/drain (admin only).
func (h *Handler) DrainExecutor(w http.ResponseWriter, r *http.Request) {
	h.setExecutorStatus(w, r, execution.ExecutorDraining)
}

// ActivateExecutor POST /api/admin/executors/{executorId}/activate (admin only).
func (h *Handler) ActivateExecutor(w http.ResponseWriter, r *http.Request) {
	h.setExecutorStatus(w, r, execution.ExecutorActive)
}

func (h *Handler) setExecutorStatus(w http.ResponseWriter, r *http.Request, status execution.ExecutorStatus) {
	executorID, err := pathParamID(r, "executorId", "executor_id")
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	ctx := r.Context()
	var changed bool
	switch status {
	case execution.ExecutorDraining:
		changed, err = h.deps.Registry.Drain(ctx, executorID)
	default:
		changed, err = h.deps.Registry.Activate(ctx, executorID)
	}
	if err != nil {
		httpx.Error(w, r, err)
		return
	}
	if !changed {
		httpx.Error(w, r, platform.NotFound("executor"))
		return
	}
	instance, err := h.deps.Executors.Get(ctx, executorID)
	if err != nil {
		httpx.Error(w, r, mapRepoError(err, "executor"))
		return
	}
	httpx.JSON(w, http.StatusOK, toExecutorView(instance))
}

/* --------------------------------- helpers --------------------------------- */

func requireActor(r *http.Request) (int64, error) {
	actor, ok := platform.ActorFrom(r.Context())
	if !ok || actor.ID <= 0 {
		return 0, platform.Unauthenticated()
	}
	return actor.ID, nil
}

func (h *Handler) versionID(r *http.Request) (int64, error) {
	return validation.NonEmptyID("id", r.PathValue("id"))
}

func pathParamID(r *http.Request, name, field string) (int64, error) {
	return validation.NonEmptyID(field, r.PathValue(name))
}

// requireVersion rejects version-scoped calls for a missing version (404).
func (h *Handler) requireVersion(ctx context.Context, versionID int64) error {
	if h.deps.Versions == nil {
		return nil
	}
	if _, err := h.deps.Versions.Get(ctx, versionID); err != nil {
		return mapRepoError(err, "version")
	}
	return nil
}

func (h *Handler) waveOfVersion(ctx context.Context, versionID, waveID int64) (execution.RunWave, error) {
	wave, err := h.deps.Waves.Get(ctx, waveID)
	if err != nil {
		return execution.RunWave{}, mapRepoError(err, "wave")
	}
	if wave.VersionID != versionID {
		return execution.RunWave{}, platform.NotFound("wave")
	}
	return wave, nil
}

func (h *Handler) caseRunOfVersion(ctx context.Context, versionID, caseRunID int64) (execution.CaseRun, error) {
	run, err := h.deps.CaseRuns.Get(ctx, caseRunID)
	if err != nil {
		return execution.CaseRun{}, mapRepoError(err, "case run")
	}
	wave, err := h.deps.Waves.Get(ctx, run.WaveID)
	if err != nil {
		return execution.CaseRun{}, mapRepoError(err, "wave")
	}
	if wave.VersionID != versionID {
		return execution.CaseRun{}, platform.NotFound("case run")
	}
	return run, nil
}

func (h *Handler) evidenceOfVersion(ctx context.Context, versionID, evidenceID int64) (execution.Evidence, error) {
	evidence, err := h.deps.Evidence.Get(ctx, evidenceID)
	if err != nil {
		return execution.Evidence{}, mapRepoError(err, "evidence")
	}
	run, err := h.deps.CaseRuns.Get(ctx, evidence.CaseRunID)
	if err != nil {
		return execution.Evidence{}, mapRepoError(err, "case run")
	}
	wave, err := h.deps.Waves.Get(ctx, run.WaveID)
	if err != nil {
		return execution.Evidence{}, mapRepoError(err, "wave")
	}
	if wave.VersionID != versionID {
		return execution.Evidence{}, platform.NotFound("evidence")
	}
	return evidence, nil
}

func (h *Handler) caseSnapshot(ctx context.Context, versionID, caseVersionID int64) *execution.CaseSnapshot {
	if h.deps.Cases == nil {
		return nil
	}
	snapshot, err := h.deps.Cases.GetCaseVersion(ctx, versionID, caseVersionID)
	if err != nil {
		return nil
	}
	return &snapshot
}

// caseRunView projects one case_run with its current+confirmed case metadata
// (the intent carries the version_sut set).
func (h *Handler) caseRunView(ctx context.Context, versionID int64, run execution.CaseRun) (CaseRunView, error) {
	meta := caseMeta{}
	var versionSUTIDs []int64
	if snapshot := h.caseSnapshot(ctx, versionID, run.CaseVersionID); snapshot != nil {
		meta.CaseID = platformid.Format(snapshot.CaseID)
		meta.Title = snapshot.Title
		versionSUTIDs = snapshot.VersionSUTIDs
	}
	intent, err := h.deps.Intents.Get(ctx, run.IntentID)
	if err != nil && !errors.Is(err, execution.ErrNotFound) {
		return CaseRunView{}, mapRepoError(err, "intent")
	}
	if err == nil && len(versionSUTIDs) == 0 {
		versionSUTIDs = intent.VersionSUTIDs
	}
	evidenceCount := 0
	if h.deps.Evidence != nil {
		if count, countErr := h.deps.Evidence.CountByCaseRun(ctx, run.ID); countErr == nil {
			evidenceCount = count
		}
	}
	return toCaseRunView(run, meta, versionSUTIDs, evidenceCount), nil
}

func (h *Handler) caseRunViews(ctx context.Context, versionID int64, runs []execution.CaseRun) ([]CaseRunView, error) {
	views := make([]CaseRunView, 0, len(runs))
	intents := make(map[int64]*execution.ExecutionIntent, len(runs))
	for _, run := range runs {
		intent, cached := intents[run.IntentID]
		if !cached {
			item, err := h.deps.Intents.Get(ctx, run.IntentID)
			if err == nil {
				intent = &item
			} else if !errors.Is(err, execution.ErrNotFound) {
				return nil, mapRepoError(err, "intent")
			}
			intents[run.IntentID] = intent
		}
		meta := caseMeta{}
		var versionSUTIDs []int64
		if snapshot := h.caseSnapshot(ctx, versionID, run.CaseVersionID); snapshot != nil {
			meta.CaseID = platformid.Format(snapshot.CaseID)
			meta.Title = snapshot.Title
			versionSUTIDs = snapshot.VersionSUTIDs
		}
		if intent != nil && len(versionSUTIDs) == 0 {
			versionSUTIDs = intent.VersionSUTIDs
		}
		evidenceCount := 0
		if h.deps.Evidence != nil {
			if count, err := h.deps.Evidence.CountByCaseRun(ctx, run.ID); err == nil {
				evidenceCount = count
			}
		}
		views = append(views, toCaseRunView(run, meta, versionSUTIDs, evidenceCount))
	}
	return views, nil
}

type caseMeta struct {
	CaseID string
	Title  string
}

func (h *Handler) settingInt(ctx context.Context, key string, fallback int64) int64 {
	if h.deps.Settings == nil {
		return fallback
	}
	value, err := h.deps.Settings.Int(ctx, key)
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func (h *Handler) settingString(ctx context.Context, key, fallback string) string {
	if h.deps.Settings == nil {
		return fallback
	}
	value, err := h.deps.Settings.String(ctx, key)
	if err != nil {
		return fallback
	}
	return value
}

func nextCursor(hasMore bool, count int, lastID int64) string {
	if !hasMore || count == 0 || lastID <= 0 {
		return ""
	}
	return platformid.Format(lastID)
}

func lastIntentID(items []execution.ExecutionIntent) int64 {
	if len(items) == 0 {
		return 0
	}
	return items[len(items)-1].ID
}

func normalizeEndpoint(endpoint string) string {
	return strings.TrimRight(strings.TrimSpace(endpoint), "/")
}

// mapRepoError maps execution repository sentinels onto the stable error
// envelope; already-mapped platform errors pass through.
func mapRepoError(err error, kind string) error {
	if err == nil {
		return nil
	}
	var apiErr *platform.Error
	if errors.As(err, &apiErr) {
		return apiErr
	}
	switch {
	case errors.Is(err, execution.ErrNotFound):
		return platform.NotFound(kind)
	case errors.Is(err, execution.ErrStateConflict):
		return platform.StateConflict("the execution entity is not in a valid state for this operation")
	case errors.Is(err, execution.ErrDuplicate):
		return platform.Conflict("the execution entity already exists")
	default:
		return err
	}
}
