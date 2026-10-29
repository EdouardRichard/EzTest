package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"strconv"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// CaseRunHandler serves the case-run endpoints of spec §6.2.
type CaseRunHandler struct {
	batches     BatchService
	runs        CaseRunReader
	steps       StepReader
	assertions  AssertionReader
	evidence    EvidenceReader
	evidenceRef EvidenceRefReader
	sessions    SessionGate
	sessionInfo SessionInfoReader
}

// RegisterRoutes mounts the case-run routes. Authentication is the server's
// global middleware; every logged-in user may read and rerun.
func (h *CaseRunHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/case-runs", h.List)
	mux.HandleFunc("GET /api/versions/{id}/batches/{batchId}/case-runs/{caseRunId}", h.Get)
	mux.HandleFunc("POST /api/versions/{id}/batches/{batchId}/case-runs/{caseRunId}/rerun", h.Rerun)
}

// List returns one cursor page of a batch's case runs, optionally filtered by
// status. The list projection carries empty detail arrays.
func (h *CaseRunHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if _, err := h.batches.Get(r.Context(), versionID, batchID); err != nil {
		writeServiceError(w, r, err)
		return
	}
	status, err := parseCaseRunStatus(r.URL.Query().Get("status"))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, beforeID, err := parsePagination(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	runs, hasMore, err := h.runs.ListCaseRuns(r.Context(), batchID, status, beforeID, limit)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	items := make([]caseRunDTO, 0, len(runs))
	for i := range runs {
		items = append(items, newCaseRunDTO(&runs[i], nil))
	}
	next := ""
	if len(runs) > 0 {
		next = nextCursor(len(runs), runs[len(runs)-1].ID, hasMore)
	}
	writePage(w, items, next, hasMore)
}

// Get returns one case run with its steps, assertions, evidence, checkpoint and
// waiting takeover (spec §6.3).
func (h *CaseRunHandler) Get(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, caseRunID, err := caseRunPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	if _, err := h.batches.Get(r.Context(), versionID, batchID); err != nil {
		writeServiceError(w, r, err)
		return
	}
	run, err := h.runs.GetCaseRun(r.Context(), batchID, caseRunID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	detail, err := h.loadDetail(r.Context(), run)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusOK, newCaseRunDTO(&run, detail))
}

// Rerun creates the next attempt of a terminal case run and answers 201.
func (h *CaseRunHandler) Rerun(w http.ResponseWriter, r *http.Request) {
	versionID, batchID, caseRunID, err := caseRunPathIDs(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	run, err := h.batches.Rerun(r.Context(), versionID, batchID, caseRunID, ActorID(r.Context()))
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	writeData(w, http.StatusCreated, newCaseRunDTO(run, nil))
}

// loadDetail composes the detail set of one case run. Step/assertion/evidence
// read failures fail the request; the optional projections (session, takeover,
// assertion evidence refs) degrade to absent instead of hiding the run.
func (h *CaseRunHandler) loadDetail(ctx context.Context, run domain.CaseRun) (*caseRunDetail, error) {
	detail := &caseRunDetail{
		Steps:             []domain.StepResult{},
		Assertions:        []domain.AssertionResult{},
		Evidence:          []domain.Evidence{},
		StepEvidence:      map[int64][]int64{},
		AssertionEvidence: map[int64][]int64{},
	}
	if h.steps != nil {
		steps, err := h.steps.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		detail.Steps = steps
	}
	if h.assertions != nil {
		assertions, err := h.assertions.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		detail.Assertions = assertions
	}
	if h.evidence != nil {
		evidence, err := h.evidence.ListByCaseRun(ctx, run.ID)
		if err != nil {
			return nil, err
		}
		detail.Evidence = evidence
	}
	detail.StepEvidence = h.loadStepEvidence(ctx, detail.Steps)
	detail.AssertionEvidence = h.loadAssertionEvidence(ctx, detail.Assertions)
	detail.Session = h.loadSession(ctx, run)
	detail.Takeover = h.loadTakeover(ctx, run)
	return detail, nil
}

// loadStepEvidence reads the evidence refs of the recorded steps. A reference
// read failure only drops the per-step evidence links: the step rows themselves
// stay readable.
func (h *CaseRunHandler) loadStepEvidence(ctx context.Context, steps []domain.StepResult) map[int64][]int64 {
	if h.evidenceRef == nil || len(steps) == 0 {
		return map[int64][]int64{}
	}
	ids := make([]int64, 0, len(steps))
	for _, step := range steps {
		ids = append(ids, step.ID)
	}
	refs, err := h.evidenceRef.ListByRef(ctx, "step_result", ids)
	if err != nil {
		slog.WarnContext(ctx, "execution step evidence refs read failed", "error", err)
		return map[int64][]int64{}
	}
	if refs == nil {
		return map[int64][]int64{}
	}
	return refs
}

// loadAssertionEvidence reads the evidence refs of the recorded assertions. A
// reference read failure only drops the per-assertion evidence links.
func (h *CaseRunHandler) loadAssertionEvidence(ctx context.Context, assertions []domain.AssertionResult) map[int64][]int64 {
	empty := map[int64][]int64{}
	if h.evidenceRef == nil || len(assertions) == 0 {
		return empty
	}
	ids := make([]int64, 0, len(assertions))
	for _, assertion := range assertions {
		ids = append(ids, assertion.ID)
	}
	refs, err := h.evidenceRef.ListByRef(ctx, "assertion_result", ids)
	if err != nil {
		slog.WarnContext(ctx, "execution assertion evidence refs read failed", "error", err)
		return empty
	}
	if refs == nil {
		return empty
	}
	return refs
}

// loadSession resolves the credential-free session projection.
func (h *CaseRunHandler) loadSession(ctx context.Context, run domain.CaseRun) *CaseRunSession {
	if h.sessionInfo == nil || run.SessionID == nil {
		return nil
	}
	session, err := h.sessionInfo.CaseRunSession(ctx, *run.SessionID)
	if err != nil {
		slog.WarnContext(ctx, "execution case-run session projection failed",
			"case_run_id", run.ID, "session_id", *run.SessionID, "error", err)
		return nil
	}
	return &session
}

// loadTakeover resolves the waiting human takeover of the run's session.
func (h *CaseRunHandler) loadTakeover(ctx context.Context, run domain.CaseRun) *executionapp.TakeoverRef {
	if h.sessions == nil || run.SessionID == nil {
		return nil
	}
	takeover, err := h.sessions.PendingTakeover(ctx, *run.SessionID)
	if err != nil {
		slog.WarnContext(ctx, "execution pending takeover read failed",
			"case_run_id", run.ID, "session_id", *run.SessionID, "error", err)
		return nil
	}
	return takeover
}

// caseRunPathIDs parses the three ids of a case-run route.
func caseRunPathIDs(r *http.Request) (int64, int64, int64, error) {
	versionID, batchID, err := batchPathIDs(r)
	if err != nil {
		return 0, 0, 0, err
	}
	caseRunID, err := PathID(r, "caseRunId")
	if err != nil {
		return 0, 0, 0, err
	}
	return versionID, batchID, caseRunID, nil
}

// SessionHandler serves GET /api/versions/{id}/execution/sessions: this
// version's execution Agent sessions for the Sessions tab (spec §6.2).
type SessionHandler struct {
	sessions AgentSessionLister
}

// RegisterRoutes mounts the execution-session route behind the global auth.
func (h *SessionHandler) RegisterRoutes(mux *http.ServeMux) {
	if h == nil || mux == nil {
		return
	}
	mux.HandleFunc("GET /api/versions/{id}/execution/sessions", h.List)
}

// List returns one page of the version channel's execution sessions. The
// underlying P1 projection is capped, so the page is sliced in memory; the
// opaque cursor is the last returned session id.
func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	versionID, err := PathID(r, "id")
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	limit, beforeID, err := parsePagination(r)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	sessions, err := h.sessions.ListSessions(r.Context(), versionID)
	if err != nil {
		writeServiceError(w, r, err)
		return
	}
	expectedChannel := "version:" + strconv.FormatInt(versionID, 10)
	if channel := r.URL.Query().Get("channel"); channel != "" && channel != expectedChannel {
		writePage(w, []executionSessionDTO{}, "", false)
		return
	}
	start := 0
	if beforeID > 0 {
		for i, session := range sessions {
			if session.SessionID == beforeID {
				start = i + 1
				break
			}
		}
	}
	end := start + limit
	if end > len(sessions) {
		end = len(sessions)
	}
	items := make([]executionSessionDTO, 0, end-start)
	for i := start; i < end; i++ {
		items = append(items, newExecutionSessionDTO(&sessions[i], versionID))
	}
	next := ""
	if end < len(sessions) && len(items) > 0 {
		next = platform.EncodeCursor(sessions[end-1].SessionID)
	}
	writePage(w, items, next, end < len(sessions))
}
