package application

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

// analysisTestNow is the deterministic clock of the fake harness.
var analysisTestNow = time.Date(2026, 10, 28, 21, 0, 0, 0, time.UTC)

// analysisTestDedupe is a valid sha256-shaped issue dedupe key.
const analysisTestDedupe = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// analysisTestInt64 returns a pointer to value for optional draft references.
func analysisTestInt64(value int64) *int64 { return &value }

// analysisTestCandidate returns a valid failure candidate for the harness.
func analysisTestCandidate(caseRunID, caseNodeID int64) domain.FailureCandidate {
	return domain.FailureCandidate{
		CaseRunID:    caseRunID,
		CaseNodeID:   caseNodeID,
		Title:        "checkout fails",
		Description:  "submitting the order returns a nil pointer",
		Severity:     "major",
		Confidence:   "high",
		FailureClass: "application",
		EvidenceIDs:  []int64{701},
	}
}

// analysisTestDraft returns a valid triage issue draft for the harness.
func analysisTestDraft() domain.IssueDraft {
	return domain.IssueDraft{
		DedupeKey:    analysisTestDedupe,
		Title:        "checkout fails",
		Description:  "nil pointer on submit",
		Severity:     domain.SeverityMajor,
		Confidence:   domain.ConfidenceHigh,
		Category:     domain.CategoryDefect,
		FailureClass: domain.FailureClassApplication,
		CaseRunID:    analysisTestInt64(11),
		CaseNodeID:   analysisTestInt64(101),
		EvidenceIDs:  []int64{701},
	}
}

// analysisFakeStore is an in-memory AnalysisStore. It keeps the analysis run
// documents and the CAS semantics of the real repository.
type analysisFakeStore struct {
	mu        sync.Mutex
	runs      map[int64]domain.AnalysisRun
	createErr error
	updateErr error
}

// newAnalysisFakeStore builds an empty store.
func newAnalysisFakeStore() *analysisFakeStore {
	return &analysisFakeStore{runs: map[int64]domain.AnalysisRun{}}
}

// seed stores run without going through the service.
func (s *analysisFakeStore) seed(run domain.AnalysisRun) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
}

// get returns one stored run.
func (s *analysisFakeStore) get(id int64) (domain.AnalysisRun, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	return run, ok
}

// Create implements AnalysisStore.
func (s *analysisFakeStore) Create(_ context.Context, run domain.AnalysisRun) (domain.AnalysisRun, error) {
	if s.createErr != nil {
		return domain.AnalysisRun{}, s.createErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.runs {
		if existing.BatchID == run.BatchID && existing.Kind == run.Kind && existing.IsActive() {
			return domain.AnalysisRun{}, domain.NewAnalysisConflict(domain.ReasonAnalysisActive, "analysis already active")
		}
	}
	s.runs[run.ID] = run
	return run, nil
}

// analysisFakeNotFound is the shared not-found error of the fake store.
func analysisFakeNotFound() error { return ErrAnalysisRunNotFound }

// Get implements AnalysisStore.
func (s *analysisFakeStore) Get(_ context.Context, versionID, batchID, id int64) (domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok || run.VersionID != versionID || run.BatchID != batchID {
		return domain.AnalysisRun{}, analysisFakeNotFound()
	}
	return run, nil
}

// GetByID implements AnalysisStore.
func (s *analysisFakeStore) GetByID(_ context.Context, id int64) (domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return domain.AnalysisRun{}, analysisFakeNotFound()
	}
	return run, nil
}

// List implements AnalysisStore.
func (s *analysisFakeStore) List(_ context.Context, versionID, batchID int64) ([]domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]domain.AnalysisRun, 0, len(s.runs))
	for _, run := range s.runs {
		if run.VersionID == versionID && run.BatchID == batchID {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID > runs[j].ID })
	return runs, nil
}

// Active implements AnalysisStore.
func (s *analysisFakeStore) Active(_ context.Context, batchID int64, kind string) (domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, run := range s.runs {
		if run.BatchID == batchID && run.Kind == kind && run.IsActive() {
			return run, nil
		}
	}
	return domain.AnalysisRun{}, analysisFakeNotFound()
}

// Latest implements AnalysisStore.
func (s *analysisFakeStore) Latest(_ context.Context, batchID int64, kind string) (domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	var latest domain.AnalysisRun
	for _, run := range s.runs {
		if run.BatchID != batchID || run.Kind != kind {
			continue
		}
		if !found || run.CreatedAt.After(latest.CreatedAt) || (run.CreatedAt.Equal(latest.CreatedAt) && run.ID > latest.ID) {
			latest = run
			found = true
		}
	}
	if !found {
		return domain.AnalysisRun{}, analysisFakeNotFound()
	}
	return latest, nil
}

// ListActive implements AnalysisStore.
func (s *analysisFakeStore) ListActive(_ context.Context, limit int) ([]domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	runs := make([]domain.AnalysisRun, 0, len(s.runs))
	for _, run := range s.runs {
		if run.IsActive() {
			runs = append(runs, run)
		}
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	if limit > 0 && len(runs) > limit {
		runs = runs[:limit]
	}
	return runs, nil
}

// UpdateStatus implements the analysis store CAS.
func (s *analysisFakeStore) UpdateStatus(_ context.Context, id int64, from, to string, patch AnalysisRunPatch) (domain.AnalysisRun, error) {
	if s.updateErr != nil {
		return domain.AnalysisRun{}, s.updateErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	run, ok := s.runs[id]
	if !ok {
		return domain.AnalysisRun{}, analysisFakeNotFound()
	}
	if run.Status != from {
		return domain.AnalysisRun{}, ErrAnalysisStateConflict
	}
	run.Status = to
	if patch.AgentSessionID != nil {
		run.AgentSessionID = patch.AgentSessionID
	}
	if patch.AgentRunID != nil {
		run.AgentRunID = patch.AgentRunID
	}
	if patch.Model != nil {
		run.Model = *patch.Model
	}
	if patch.PromptVersion != nil {
		run.PromptVersion = *patch.PromptVersion
	}
	if patch.Input != nil {
		run.Input = patch.Input
	}
	if patch.Result != nil {
		run.Result = patch.Result
	}
	if patch.Stats != nil {
		run.Stats = patch.Stats
	}
	if patch.Errors != nil {
		run.Errors = patch.Errors
	}
	if patch.StartedAt != nil {
		run.StartedAt = patch.StartedAt
	}
	if patch.FinishedAt != nil {
		run.FinishedAt = patch.FinishedAt
	}
	run.UpdatedAt = analysisTestNow
	s.runs[id] = run
	return run, nil
}

// RecoverStale implements AnalysisStore: it fails every active run and returns
// the recovered rows to the service so recovered triage runs get the §5.6
// fallback.
func (s *analysisFakeStore) RecoverStale(_ context.Context) ([]domain.AnalysisRun, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recovered := make([]domain.AnalysisRun, 0)
	for id, run := range s.runs {
		if !run.IsActive() {
			continue
		}
		run.Status = domain.AnalysisFailed
		run.Errors = json.RawMessage(`{"reason":"stale_run_recovered"}`)
		finished := analysisTestNow
		run.FinishedAt = &finished
		s.runs[id] = run
		recovered = append(recovered, run)
	}
	sort.Slice(recovered, func(i, j int) bool { return recovered[i].ID < recovered[j].ID })
	return recovered, nil
}

// analysisFakeExecutionReader returns the configured failed cases and records
// the query scope.
type analysisFakeExecutionReader struct {
	cases     []AnalysisFailedCase
	err       error
	lastLimit int
	lastVerID int64
	lastBatch int64
}

// ListFailedCases implements ExecutionReader.
func (r *analysisFakeExecutionReader) ListFailedCases(_ context.Context, versionID, batchID int64, limit int) ([]AnalysisFailedCase, error) {
	r.lastVerID, r.lastBatch, r.lastLimit = versionID, batchID, limit
	if r.err != nil {
		return nil, r.err
	}
	if limit > 0 && len(r.cases) > limit {
		return r.cases[:limit], nil
	}
	return r.cases, nil
}

// analysisFakeIssueWriter records ApplyDrafts calls. By default it emulates the
// IssueService contract: one unique issue per dedupe key, stable across replays
// (the returned slice may be shorter than the drafts when several drafts merge
// into the same issue).
type analysisFakeIssueWriter struct {
	mu          sync.Mutex
	calls       int
	drafts      []domain.IssueDraft
	issues      []domain.Issue
	err         error
	version     int64
	batch       int64
	actor       int64
	nextIssueID int64
	byDedupeKey map[string]domain.Issue
}

// ApplyDrafts implements IssueWriter.
func (w *analysisFakeIssueWriter) ApplyDrafts(_ context.Context, versionID, batchID, actor int64, drafts []domain.IssueDraft) ([]domain.Issue, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.calls++
	w.drafts = append([]domain.IssueDraft(nil), drafts...)
	w.version, w.batch, w.actor = versionID, batchID, actor
	if w.err != nil {
		return nil, w.err
	}
	if w.issues != nil {
		return w.issues, nil
	}
	if w.byDedupeKey == nil {
		w.byDedupeKey = map[string]domain.Issue{}
	}
	issues := make([]domain.Issue, 0, len(drafts))
	seen := make(map[int64]struct{}, len(drafts))
	for _, draft := range drafts {
		issue, found := w.byDedupeKey[draft.DedupeKey]
		if !found {
			if w.nextIssueID == 0 {
				w.nextIssueID = 9000
			}
			w.nextIssueID++
			issue = domain.Issue{ID: w.nextIssueID, VersionID: versionID, Title: draft.Title, DedupeKey: draft.DedupeKey}
			w.byDedupeKey[draft.DedupeKey] = issue
		}
		if _, duplicate := seen[issue.ID]; duplicate {
			continue
		}
		seen[issue.ID] = struct{}{}
		issues = append(issues, issue)
	}
	return issues, nil
}

// analysisFakeIssueReader returns the configured open issues.
type analysisFakeIssueReader struct {
	issues    []domain.OpenIssue
	err       error
	lastLimit int
}

// ListOpen implements IssueReader.
func (r *analysisFakeIssueReader) ListOpen(_ context.Context, _ int64, limit int) ([]domain.OpenIssue, error) {
	r.lastLimit = limit
	return r.issues, r.err
}

// analysisFakeAnchorPort records issue anchor creation calls.
type analysisFakeAnchorPort struct {
	mu       sync.Mutex
	calls    int
	issueIDs []int64
	refs     [][]domain.CodeRef
	warnings []string
	err      error
}

// CreateIssueAnchors implements CodeAnchorPort.
func (p *analysisFakeAnchorPort) CreateIssueAnchors(_ context.Context, _ /*versionID*/, issueID, _ /*actor*/ int64, refs []domain.CodeRef) ([]string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	p.issueIDs = append(p.issueIDs, issueID)
	p.refs = append(p.refs, append([]domain.CodeRef(nil), refs...))
	return p.warnings, p.err
}

// analysisFakeAgentRuntime is a scripted P1 agent runtime.
type analysisFakeAgentRuntime struct {
	mu            sync.Mutex
	sessions      map[int64]*AnalysisAgentSessionStatus
	nextSessionID int64
	nextRunID     int64
	createErr     error
	startErr      error
	created       int
	started       int
	lastProfile   string
	lastTitle     string
	lastActor     int64
	lastChannel   string
	lastVersion   string
	lastText      string
	lastScope     AnalysisAgentRunScope
}

// newAnalysisFakeAgentRuntime builds an empty agent runtime.
func newAnalysisFakeAgentRuntime() *analysisFakeAgentRuntime {
	return &analysisFakeAgentRuntime{
		sessions:      map[int64]*AnalysisAgentSessionStatus{},
		nextSessionID: 5000,
		nextRunID:     6000,
	}
}

// CreateSession implements AgentRuntime.
func (a *analysisFakeAgentRuntime) CreateSession(_ context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.createErr != nil {
		return 0, a.createErr
	}
	a.created++
	a.lastProfile, a.lastTitle, a.lastActor = profileKey, title, actor
	a.lastChannel, a.lastVersion = channel, versionID
	a.nextSessionID++
	id := a.nextSessionID
	a.sessions[id] = &AnalysisAgentSessionStatus{SessionID: id, Status: "pending"}
	return id, nil
}

// Start implements AgentRuntime.
func (a *analysisFakeAgentRuntime) Start(_ context.Context, sessionID int64, text string, _ int64, scope AnalysisAgentRunScope) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.startErr != nil {
		return 0, a.startErr
	}
	a.started++
	a.lastText, a.lastScope = text, scope
	a.nextRunID++
	runID := a.nextRunID
	session, ok := a.sessions[sessionID]
	if !ok {
		return 0, fmt.Errorf("fake agent: unknown session %d", sessionID)
	}
	session.Status = "running"
	session.LastRun = &AnalysisAgentRunStatus{ID: runID, Status: "running"}
	return runID, nil
}

// GetSession implements AgentRuntime.
func (a *analysisFakeAgentRuntime) GetSession(_ context.Context, sessionID int64) (AnalysisAgentSessionStatus, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok {
		return AnalysisAgentSessionStatus{}, fmt.Errorf("fake agent: unknown session %d", sessionID)
	}
	return *session, nil
}

// Abort implements AgentRuntime.
func (a *analysisFakeAgentRuntime) Abort(_ context.Context, sessionID, _ int64) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session, ok := a.sessions[sessionID]; ok {
		session.Status = "aborted"
	}
	return nil
}

// setStatus scripts the session status used by the next ProcessOnce.
func (a *analysisFakeAgentRuntime) setStatus(sessionID int64, status string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if session, ok := a.sessions[sessionID]; ok {
		session.Status = status
		if session.LastRun != nil {
			session.LastRun.Status = status
		}
	}
}

// setTerminal scripts the session status and the P1 terminal reason used by the
// next ProcessOnce.
func (a *analysisFakeAgentRuntime) setTerminal(sessionID int64, status, terminalReason string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session, ok := a.sessions[sessionID]
	if !ok {
		return
	}
	session.Status = status
	if session.LastRun == nil {
		session.LastRun = &AnalysisAgentRunStatus{}
	}
	session.LastRun.Status = status
	session.LastRun.TerminalReason = terminalReason
}

// analysisFakeProfileReader serves the model of an agent profile seed (spec
// §7.5).
type analysisFakeProfileReader struct {
	model string
	err   error
}

// ProfileModel implements AnalysisAgentProfileReader.
func (r *analysisFakeProfileReader) ProfileModel(context.Context, string) (string, error) {
	return r.model, r.err
}

// analysisFakeEventPublisher records published analysis events.
type analysisFakeEventPublisher struct {
	mu     sync.Mutex
	events []AnalysisEvent
}

// Publish implements AnalysisEventPublisher.
func (p *analysisFakeEventPublisher) Publish(_ context.Context, event AnalysisEvent) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event)
	return nil
}

// analysisFakeAuditRecorder records audit actions.
type analysisFakeAuditRecorder struct {
	mu      sync.Mutex
	entries []AnalysisAuditEvent
}

// Record implements AnalysisAuditRecorder.
func (r *analysisFakeAuditRecorder) Record(_ context.Context, event AnalysisAuditEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = append(r.entries, event)
	return nil
}

// analysisFakeClock is a frozen clock.
type analysisFakeClock struct{}

// Now implements AnalysisClock.
func (analysisFakeClock) Now() time.Time { return analysisTestNow }

// analysisFakeIDs mints sequential ids.
type analysisFakeIDs struct{ next int64 }

// Next implements AnalysisIDGenerator.
func (i *analysisFakeIDs) Next() int64 {
	i.next++
	return 1000 + i.next - 1
}

// analysisHarness wires an AnalysisService to fakes.
type analysisHarness struct {
	service    *AnalysisService
	store      *analysisFakeStore
	execution  *analysisFakeExecutionReader
	issues     *analysisFakeIssueWriter
	openIssues *analysisFakeIssueReader
	anchors    *analysisFakeAnchorPort
	agents     *analysisFakeAgentRuntime
	profiles   *analysisFakeProfileReader
	events     *analysisFakeEventPublisher
	audit      *analysisFakeAuditRecorder
	clock      analysisFakeClock
	ids        *analysisFakeIDs
}

// newAnalysisHarness builds a service with every port stubbed.
func newAnalysisHarness() *analysisHarness {
	h := &analysisHarness{
		store:      newAnalysisFakeStore(),
		execution:  &analysisFakeExecutionReader{},
		issues:     &analysisFakeIssueWriter{},
		openIssues: &analysisFakeIssueReader{},
		anchors:    &analysisFakeAnchorPort{},
		agents:     newAnalysisFakeAgentRuntime(),
		profiles:   &analysisFakeProfileReader{model: "deepseek-chat"},
		events:     &analysisFakeEventPublisher{},
		audit:      &analysisFakeAuditRecorder{},
		ids:        &analysisFakeIDs{},
	}
	h.service = NewAnalysisService(AnalysisDeps{
		Runs:       h.store,
		Execution:  h.execution,
		Issues:     h.issues,
		OpenIssues: h.openIssues,
		Anchors:    h.anchors,
		Agents:     h.agents,
		Profiles:   h.profiles,
		Events:     h.events,
		Audit:      h.audit,
		IDs:        h.ids,
		Clock:      h.clock,
	})
	return h
}

// analysisFailureRun returns a persisted failure run used as the triage input.
func (h *analysisHarness) analysisFailureRun(status string, candidates []domain.FailureCandidate, caseRunIDs []int64) domain.AnalysisRun {
	run := domain.AnalysisRun{
		ID:            700,
		VersionID:     7,
		BatchID:       9,
		Kind:          domain.AnalysisKindFailure,
		Status:        status,
		PromptVersion: domain.AnalysisPromptVersionFailure,
		Input:         domain.EncodeFailureInput(7, 9, caseRunIDs),
		CreatedBy:     42,
		CreatedAt:     analysisTestNow.Add(-time.Minute),
		UpdatedAt:     analysisTestNow.Add(-time.Minute),
	}
	if candidates != nil {
		run.Result = domain.EncodeFailureResult(candidates, nil)
	}
	h.store.seed(run)
	return run
}

// analysisRunningRun seeds one queued/running run with an agent session.
func (h *analysisHarness) analysisRunningRun(kind string, input json.RawMessage, sessionID int64) domain.AnalysisRun {
	run := domain.AnalysisRun{
		ID:             800,
		VersionID:      7,
		BatchID:        9,
		Kind:           kind,
		Status:         domain.AnalysisRunning,
		AgentSessionID: analysisTestInt64(sessionID),
		Input:          input,
		CreatedBy:      42,
		CreatedAt:      analysisTestNow.Add(-time.Minute),
		UpdatedAt:      analysisTestNow.Add(-time.Minute),
	}
	h.store.seed(run)
	h.agents.mu.Lock()
	h.agents.sessions[sessionID] = &AnalysisAgentSessionStatus{SessionID: sessionID, Status: "running"}
	h.agents.mu.Unlock()
	return run
}

// analysisEventDetails returns the details of events of one type.
func analysisEventDetails(t *testing.T, events []AnalysisEvent, eventType string) []map[string]any {
	t.Helper()
	var details []map[string]any
	for _, event := range events {
		if event.Type == eventType {
			details = append(details, event.Detail)
		}
	}
	return details
}

// analysisAuditActions returns the recorded actions.
func analysisAuditActions(entries []AnalysisAuditEvent) []string {
	actions := make([]string, 0, len(entries))
	for _, entry := range entries {
		actions = append(actions, entry.Action)
	}
	return actions
}

// TestStartFailureHonorsConfiguredMaxCases pins the P8 wiring of
// EZTEST_ANALYSIS_MAX_CASES (design spec §10.5/§11.4): the configured budget,
// not the compiled default, bounds the failed cases copied into a run.
func TestStartFailureHonorsConfiguredMaxCases(t *testing.T) {
	h := newAnalysisHarness()
	h.service = NewAnalysisService(AnalysisDeps{
		Runs:       h.store,
		Execution:  h.execution,
		Issues:     h.issues,
		OpenIssues: h.openIssues,
		Anchors:    h.anchors,
		Agents:     h.agents,
		Profiles:   h.profiles,
		Events:     h.events,
		Audit:      h.audit,
		IDs:        h.ids,
		Clock:      h.clock,
		MaxCases:   3,
	})
	for index := int64(0); index < 5; index++ {
		h.execution.cases = append(h.execution.cases, AnalysisFailedCase{
			CaseRunID: 100 + index, CaseNodeID: 200 + index, FailureClass: "application",
		})
	}

	run, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartFailure: %v", err)
	}
	if h.execution.lastLimit != 4 {
		t.Fatalf("execution limit = %d, want MaxCases+1 = 4", h.execution.lastLimit)
	}
	input, err := domain.DecodeFailureInput(run.Input)
	if err != nil {
		t.Fatalf("decode failure input: %v", err)
	}
	if len(input.CaseRunIDs) != 3 {
		t.Fatalf("input case runs = %v, want the configured 3", input.CaseRunIDs)
	}
	var stats map[string]any
	if err := json.Unmarshal(run.Stats, &stats); err != nil {
		t.Fatalf("decode stats %s: %v", run.Stats, err)
	}
	if stats["truncated"] != true {
		t.Fatalf("stats = %v, want truncated=true", stats)
	}
}

// TestStartFailureFiltersApplicationFailuresAndTruncates pins spec §5.6/§6.3:
// only failed(application) case runs enter the input, at most 20, and the
// overflow is recorded in stats instead of failing.
func TestStartFailureFiltersApplicationFailuresAndTruncates(t *testing.T) {
	h := newAnalysisHarness()
	for index := int64(0); index < 22; index++ {
		h.execution.cases = append(h.execution.cases, AnalysisFailedCase{
			CaseRunID: 100 + index, CaseNodeID: 200 + index, FailureClass: "application",
		})
	}
	h.execution.cases = append(h.execution.cases,
		AnalysisFailedCase{CaseRunID: 300, CaseNodeID: 400, FailureClass: "environment"},
		AnalysisFailedCase{CaseRunID: 301, CaseNodeID: 401, FailureClass: "case"},
	)

	run, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartFailure: %v", err)
	}
	if run.Kind != domain.AnalysisKindFailure || run.Status != domain.AnalysisRunning {
		t.Fatalf("run = %+v, want a running failure run", run)
	}
	if run.PromptVersion != domain.AnalysisPromptVersionFailure {
		t.Fatalf("prompt version = %q", run.PromptVersion)
	}
	if run.Model != "deepseek-chat" {
		t.Fatalf("run model = %q, want the profile model", run.Model)
	}
	if run.StartedAt == nil || run.FinishedAt != nil {
		t.Fatalf("timestamps = %v/%v, want a started run without finished_at", run.StartedAt, run.FinishedAt)
	}
	view := domain.NewAnalysisView(run)
	if view.Model != "deepseek-chat" {
		t.Fatalf("view model = %q, want the run model of spec §7.5", view.Model)
	}
	if h.execution.lastVerID != 7 || h.execution.lastBatch != 9 {
		t.Fatalf("execution query scope = %d/%d, want 7/9", h.execution.lastVerID, h.execution.lastBatch)
	}
	if h.execution.lastLimit != domain.AnalysisMaxFailureCandidates+1 {
		t.Fatalf("execution limit = %d, want %d", h.execution.lastLimit, domain.AnalysisMaxFailureCandidates+1)
	}

	input, err := domain.DecodeFailureInput(run.Input)
	if err != nil {
		t.Fatalf("decode failure input: %v", err)
	}
	if input.VersionID != 7 || input.BatchID != 9 {
		t.Fatalf("input scope = %+v", input)
	}
	if len(input.CaseRunIDs) != domain.AnalysisMaxFailureCandidates {
		t.Fatalf("input case runs = %d, want %d", len(input.CaseRunIDs), domain.AnalysisMaxFailureCandidates)
	}
	for _, caseRunID := range input.CaseRunIDs {
		if caseRunID >= 300 {
			t.Fatalf("input contains a non-application case run %d", caseRunID)
		}
	}
	var stats map[string]any
	if err := json.Unmarshal(run.Stats, &stats); err != nil {
		t.Fatalf("decode stats %s: %v", run.Stats, err)
	}
	if truncated, _ := stats["truncated"].(bool); !truncated {
		t.Fatalf("stats = %s, want truncated=true", run.Stats)
	}

	if h.agents.lastProfile != "failure-analysis-agent" {
		t.Fatalf("agent profile = %q, want failure-analysis-agent", h.agents.lastProfile)
	}
	if h.agents.lastChannel != "version:7" {
		t.Fatalf("agent channel = %q, want version:7", h.agents.lastChannel)
	}
	if h.agents.lastScope != (AnalysisAgentRunScope{VersionID: 7, BatchID: 9, AnalysisRunID: run.ID}) {
		t.Fatalf("agent scope = %+v", h.agents.lastScope)
	}

	started := analysisEventDetails(t, h.events.events, domain.EventAnalysisStarted)
	if len(started) != 1 {
		t.Fatalf("analysis.started events = %d, want 1", len(started))
	}
	if started[0]["analysisId"] != strconv.FormatInt(run.ID, 10) {
		t.Fatalf("event analysisId = %v, want decimal string", started[0]["analysisId"])
	}
	if actions := analysisAuditActions(h.audit.entries); len(actions) != 1 || actions[0] != "delivery.analysis.started" {
		t.Fatalf("audit actions = %v", actions)
	}
}

// TestStartFailureRejectsNoApplicationFailures pins analysis_not_applicable.
func TestStartFailureRejectsNoApplicationFailures(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 300, CaseNodeID: 400, FailureClass: "environment"},
		{CaseRunID: 301, CaseNodeID: 401, FailureClass: "case"},
	}

	_, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err == nil {
		t.Fatal("StartFailure succeeded without application failures")
	}
	if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisNotApplicable {
		t.Fatalf("reason = %q, want %q", reason, domain.ReasonAnalysisNotApplicable)
	}
	if len(h.store.runs) != 0 || h.agents.created != 0 {
		t.Fatalf("store runs = %d, sessions = %d, want none", len(h.store.runs), h.agents.created)
	}
}

// TestStartFailureRejectsActiveRun pins the unique-active rule.
func TestStartFailureRejectsActiveRun(t *testing.T) {
	h := newAnalysisHarness()
	h.store.seed(domain.AnalysisRun{
		ID: 5, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindFailure, Status: domain.AnalysisRunning,
	})

	_, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err == nil {
		t.Fatal("StartFailure started a second active run")
	}
	if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisActive {
		t.Fatalf("reason = %q, want %q", reason, domain.ReasonAnalysisActive)
	}
	if h.agents.created != 0 {
		t.Fatalf("agent sessions = %d, want none", h.agents.created)
	}
}

// TestStartFailureDegradesWhenAgentUnavailable pins the degradation matrix of
// spec §9.7: an unavailable LLM leaves the run degraded, not failed, so the
// batch chain continues.
func TestStartFailureDegradesWhenAgentUnavailable(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application"}}
	h.agents.createErr = errors.New("llm gateway down")

	run, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartFailure returned an error for an unavailable LLM: %v", err)
	}
	if run.Status != domain.AnalysisDegraded {
		t.Fatalf("run status = %q, want degraded", run.Status)
	}
	var failures map[string]any
	if err := json.Unmarshal(run.Errors, &failures); err != nil {
		t.Fatalf("decode errors %s: %v", run.Errors, err)
	}
	if failures["reason"] != domain.ReasonAIUnavailable {
		t.Fatalf("errors = %s, want reason %s", run.Errors, domain.ReasonAIUnavailable)
	}
	stored, ok := h.store.get(run.ID)
	if !ok || stored.Status != domain.AnalysisDegraded {
		t.Fatalf("stored run = %+v, ok=%v", stored, ok)
	}
	finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
	if len(finished) != 1 || finished[0]["status"] != domain.AnalysisDegraded || finished[0]["degraded"] != true {
		t.Fatalf("analysis.finished = %+v, want a degraded document", finished)
	}
	if !strings.Contains(strings.Join(analysisAuditActions(h.audit.entries), ","), "delivery.analysis.finished") {
		t.Fatalf("audit actions = %v, want a finished entry", analysisAuditActions(h.audit.entries))
	}
}

// TestStartFailureTelemetryCarriesNoEvidenceOrPromptBody pins the security
// boundary: only ids/status/counts reach the run document and the telemetry.
func TestStartFailureTelemetryCarriesNoEvidenceOrPromptBody(t *testing.T) {
	const secret = "SECRET-EVIDENCE-BODY-7f0c"
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{{
		CaseRunID: 11, CaseNodeID: 101, FailureClass: "application",
		FailureReason: secret,
		EvidenceIDs:   []int64{701},
	}}

	run, err := h.service.StartFailure(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartFailure: %v", err)
	}
	for name, document := range map[string][]byte{
		"input": run.Input, "result": run.Result, "stats": run.Stats, "errors": run.Errors,
	} {
		if strings.Contains(string(document), secret) {
			t.Fatalf("%s contains the evidence body", name)
		}
	}
	if strings.Contains(h.agents.lastText, secret) {
		t.Fatal("agent instruction contains the evidence body")
	}
	for _, event := range h.events.events {
		encoded, _ := json.Marshal(event)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("event contains the evidence body: %s", encoded)
		}
	}
	for _, entry := range h.audit.entries {
		encoded, _ := json.Marshal(entry)
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("audit entry contains the evidence body: %s", encoded)
		}
	}
}

// TestStartTriageRequiresDoneFailureWithCandidates pins analysis_not_applicable
// for missing/running/failed/empty failure analyses.
func TestStartTriageRequiresDoneFailureWithCandidates(t *testing.T) {
	t.Run("missing failure", func(t *testing.T) {
		h := newAnalysisHarness()
		_, err := h.service.StartTriage(context.Background(), 7, 9, 42)
		if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisNotApplicable {
			t.Fatalf("reason = %q, want analysis_not_applicable", reason)
		}
	})
	t.Run("failed failure run", func(t *testing.T) {
		h := newAnalysisHarness()
		h.analysisFailureRun(domain.AnalysisFailed, nil, []int64{11})
		_, err := h.service.StartTriage(context.Background(), 7, 9, 42)
		if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisNotApplicable {
			t.Fatalf("reason = %q, want analysis_not_applicable", reason)
		}
	})
	t.Run("done without candidates", func(t *testing.T) {
		h := newAnalysisHarness()
		h.analysisFailureRun(domain.AnalysisDone, []domain.FailureCandidate{}, []int64{11})
		_, err := h.service.StartTriage(context.Background(), 7, 9, 42)
		if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisNotApplicable {
			t.Fatalf("reason = %q, want analysis_not_applicable", reason)
		}
	})
}

// TestStartTriageInputCarriesCandidatesAndOpenIssues pins the issue_triage input
// document of spec §5.6.
func TestStartTriageInputCarriesCandidatesAndOpenIssues(t *testing.T) {
	h := newAnalysisHarness()
	h.analysisFailureRun(domain.AnalysisDone, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.openIssues.issues = []domain.OpenIssue{{IssueID: 55, Title: "old defect", Severity: "major"}}

	run, err := h.service.StartTriage(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartTriage: %v", err)
	}
	if run.Kind != domain.AnalysisKindIssueTriage || run.Status != domain.AnalysisRunning {
		t.Fatalf("run = %+v, want a running triage run", run)
	}
	if run.PromptVersion != domain.AnalysisPromptVersionTriage {
		t.Fatalf("prompt version = %q", run.PromptVersion)
	}
	input, err := domain.DecodeTriageInput(run.Input)
	if err != nil {
		t.Fatalf("decode triage input: %v", err)
	}
	if len(input.Candidates) != 1 || len(input.ExistingIssues) != 1 || input.ExistingIssues[0].IssueID != 55 {
		t.Fatalf("triage input = %+v", input)
	}
	if !strings.Contains(string(run.Input), `"issueId":"55"`) {
		t.Fatalf("triage input = %s, want a decimal string issue id", run.Input)
	}
	if h.agents.lastProfile != "issue-triage-agent" {
		t.Fatalf("agent profile = %q", h.agents.lastProfile)
	}
}

// TestStartTriageDegradedFallsBackToFailureCandidates pins the triage-failure
// fallback of spec §5.6: when the triage agent cannot start, the failure
// candidates are materialized with a downgraded confidence.
func TestStartTriageDegradedFallsBackToFailureCandidates(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.agents.createErr = errors.New("llm unavailable")

	run, err := h.service.StartTriage(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartTriage returned an error for an unavailable LLM: %v", err)
	}
	if run.Status != domain.AnalysisDegraded {
		t.Fatalf("run status = %q, want degraded", run.Status)
	}
	if run.FinishedAt == nil {
		t.Fatal("finished_at = nil, want the degradation timestamp")
	}
	if h.issues.calls != 1 {
		t.Fatalf("ApplyDrafts calls = %d, want 1 fallback materialization", h.issues.calls)
	}
	if len(h.issues.drafts) != 1 {
		t.Fatalf("fallback drafts = %d, want 1", len(h.issues.drafts))
	}
	if h.issues.drafts[0].Confidence != "medium" {
		t.Fatalf("fallback confidence = %q, want downgraded medium", h.issues.drafts[0].Confidence)
	}
	if h.issues.drafts[0].CaseNodeID == nil || *h.issues.drafts[0].CaseNodeID != 101 {
		t.Fatalf("fallback draft = %+v, want the failure candidate case node", h.issues.drafts[0])
	}
	if h.issues.actor != 42 {
		t.Fatalf("fallback actor = %d, want the run creator 42", h.issues.actor)
	}
}

// TestProposeFailureCandidatesValidatesAndPersists pins the agent-facing
// submission path: invalid candidates are reported as warnings and dropped, the
// valid ones are stored with decimal string ids, and the run keeps running.
func TestProposeFailureCandidatesValidatesAndPersists(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
		{CaseRunID: 12, CaseNodeID: 102, FailureClass: "application"},
	}
	run := h.analysisRunningRun(domain.AnalysisKindFailure,
		domain.EncodeFailureInput(7, 9, []int64{11, 12}), 5001)

	invalidClass := analysisTestCandidate(12, 102)
	invalidClass.FailureClass = "environment"
	updated, warnings, err := h.service.ProposeFailureCandidates(context.Background(), run.ID, []domain.FailureCandidate{
		analysisTestCandidate(11, 101), invalidClass,
	})
	if err != nil {
		t.Fatalf("ProposeFailureCandidates: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("warnings = none, want the invalid candidate reported")
	}
	if updated.Status != domain.AnalysisRunning {
		t.Fatalf("run status = %q, want running", updated.Status)
	}
	result, err := domain.DecodeFailureResult(updated.Result)
	if err != nil {
		t.Fatalf("decode result: %v", err)
	}
	if len(result.Candidates) != 1 || result.Candidates[0].CaseRunID != 11 {
		t.Fatalf("stored candidates = %+v, want only the valid one", result.Candidates)
	}
	if !strings.Contains(string(updated.Result), `"evidenceIds":["701"]`) {
		t.Fatalf("result = %s, want decimal string evidence ids", updated.Result)
	}
}

// TestProcessOnceMaterializesFailureDone pins the failure materialization: a
// done agent run validates the submitted candidates, stores them and finishes
// done with an analysis.finished event.
func TestProcessOnceMaterializesFailureDone(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	run := h.analysisRunningRun(domain.AnalysisKindFailure,
		domain.EncodeFailureInput(7, 9, []int64{11}), 5001)
	if _, _, err := h.service.ProposeFailureCandidates(context.Background(), run.ID,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}); err != nil {
		t.Fatalf("ProposeFailureCandidates: %v", err)
	}
	h.agents.setStatus(5001, "done")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	stored, ok := h.store.get(run.ID)
	if !ok || stored.Status != domain.AnalysisDone {
		t.Fatalf("stored run = %+v, want done", stored)
	}
	if stored.FinishedAt == nil {
		t.Fatal("finished_at = nil, want the finish timestamp")
	}
	result, err := domain.DecodeFailureResult(stored.Result)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("stored result = %s (%v), want one candidate", stored.Result, err)
	}
	if got := result.Candidates[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("stored candidate evidence = %v, want the failed case evidence [701]", got)
	}
	finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
	if len(finished) != 1 {
		t.Fatalf("analysis.finished events = %d, want 1", len(finished))
	}
	if finished[0]["status"] != domain.AnalysisDone || finished[0]["candidateCount"] != 1 || finished[0]["degraded"] != false {
		t.Fatalf("analysis.finished detail = %+v", finished[0])
	}
	if h.issues.calls != 0 {
		t.Fatalf("ApplyDrafts calls = %d, want none for a failure analysis", h.issues.calls)
	}
}

// TestProcessOnceFailureFailedMarksRunFailed pins agent failure handling: no
// issues, run failed, report degrades.
func TestProcessOnceFailureFailedMarksRunFailed(t *testing.T) {
	h := newAnalysisHarness()
	run := h.analysisRunningRun(domain.AnalysisKindFailure,
		domain.EncodeFailureInput(7, 9, []int64{11}), 5001)
	h.agents.setStatus(5001, "failed")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	stored, _ := h.store.get(run.ID)
	if stored.Status != domain.AnalysisFailed {
		t.Fatalf("run status = %q, want failed", stored.Status)
	}
	var failures map[string]any
	if err := json.Unmarshal(stored.Errors, &failures); err != nil || failures["reason"] != "agent_failed" {
		t.Fatalf("errors = %s (%v), want reason agent_failed", stored.Errors, err)
	}
	finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
	if len(finished) != 1 || finished[0]["status"] != domain.AnalysisFailed || finished[0]["degraded"] != true {
		t.Fatalf("analysis.finished = %+v, want failed/degraded", finished)
	}
	if h.issues.calls != 0 {
		t.Fatalf("ApplyDrafts calls = %d, want none", h.issues.calls)
	}
}

// TestProcessOnceTriageDoneAppliesDrafts pins the triage materialization: the
// validated drafts go through IssueWriter.ApplyDrafts and issue anchors are
// created best-effort.
func TestProcessOnceTriageDoneAppliesDrafts(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	failure := h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	draft := analysisTestDraft()
	draft.CodeRefs = []domain.CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}}
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)},
			[]domain.OpenIssue{{IssueID: 55, Title: "old", Severity: "major"}}), 5002)
	if _, _, err := h.service.ProposeIssueDrafts(context.Background(), run.ID, []domain.IssueDraft{draft}); err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	_ = failure
	h.agents.setStatus(5002, "done")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	stored, _ := h.store.get(run.ID)
	if stored.Status != domain.AnalysisDone {
		t.Fatalf("triage status = %q, want done: %s", stored.Status, stored.Errors)
	}
	if stored.FinishedAt == nil {
		t.Fatal("finished_at = nil, want the finish timestamp")
	}
	if h.issues.calls != 1 || len(h.issues.drafts) != 1 || h.issues.drafts[0].DedupeKey != analysisTestDedupe {
		t.Fatalf("ApplyDrafts calls/drafts = %d/%+v", h.issues.calls, h.issues.drafts)
	}
	if got := h.issues.drafts[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("draft evidence = %v, want the allowed [701]", got)
	}
	if h.issues.version != 7 || h.issues.batch != 9 || h.issues.actor != 42 {
		t.Fatalf("ApplyDrafts scope = %d/%d/%d, want 7/9/42", h.issues.version, h.issues.batch, h.issues.actor)
	}
	if h.anchors.calls != 1 || len(h.anchors.refs) != 1 || h.anchors.refs[0][0].Path != "src/cart.go" {
		t.Fatalf("anchor calls/refs = %d/%+v", h.anchors.calls, h.anchors.refs)
	}
	finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
	if len(finished) != 1 || finished[0]["status"] != domain.AnalysisDone || finished[0]["issueCount"] != 1 {
		t.Fatalf("analysis.finished = %+v", finished)
	}
}

// TestProcessOnceTriageFailedFallsBackToFailureDrafts pins the failed-triage
// fallback and the failed status of the triage run.
func TestProcessOnceTriageFailedFallsBackToFailureDrafts(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)
	h.agents.setStatus(5002, "aborted")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	stored, _ := h.store.get(run.ID)
	if stored.Status != domain.AnalysisFailed {
		t.Fatalf("triage status = %q, want failed", stored.Status)
	}
	if h.issues.calls != 1 || len(h.issues.drafts) != 1 {
		t.Fatalf("fallback ApplyDrafts calls/drafts = %d/%+v", h.issues.calls, h.issues.drafts)
	}
	if h.issues.drafts[0].Confidence != "medium" || h.issues.drafts[0].DedupeKey == "" {
		t.Fatalf("fallback draft = %+v, want downgraded confidence and a dedupe key", h.issues.drafts[0])
	}
	finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
	if len(finished) != 1 || finished[0]["status"] != domain.AnalysisFailed || finished[0]["degraded"] != true {
		t.Fatalf("analysis.finished = %+v", finished)
	}
	if finished[0]["issueCount"] != 1 {
		t.Fatalf("analysis.finished issueCount = %v, want the fallback issues (1)", finished[0]["issueCount"])
	}
	var finishedAudit map[string]any
	for _, entry := range h.audit.entries {
		if entry.Action == "delivery.analysis.finished" {
			finishedAudit = entry.Metadata
		}
	}
	if finishedAudit == nil || finishedAudit["issueCount"] != 1 {
		t.Fatalf("audit finished metadata = %+v, want issueCount 1 after the fallback", finishedAudit)
	}
}

// TestProcessOnceTriageDoneWithoutDraftsDegrades pins the "done without a valid
// submission" degradation and its fallback.
func TestProcessOnceTriageDoneWithoutDraftsDegrades(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)
	h.agents.setStatus(5002, "completed")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	stored, _ := h.store.get(run.ID)
	if stored.Status != domain.AnalysisDegraded {
		t.Fatalf("triage status = %q, want degraded", stored.Status)
	}
	var failures map[string]any
	if err := json.Unmarshal(stored.Errors, &failures); err != nil || failures["reason"] != domain.ReasonNoCandidates {
		t.Fatalf("errors = %s (%v), want reason %s", stored.Errors, err, domain.ReasonNoCandidates)
	}
	if h.issues.calls != 1 {
		t.Fatalf("ApplyDrafts calls = %d, want the failure-candidate fallback", h.issues.calls)
	}
}

// TestProposeIssueDraftsDropsInvalidDraftsWithoutFailingRun pins §9.4: malformed
// drafts are warnings; the run stays running until the agent finishes.
func TestProposeIssueDraftsDropsInvalidDraftsWithoutFailingRun(t *testing.T) {
	h := newAnalysisHarness()
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, nil, nil), 5002)
	invalid := analysisTestDraft()
	invalid.DedupeKey = "short"
	invalid.Title = ""

	updated, warnings, err := h.service.ProposeIssueDrafts(context.Background(), run.ID,
		[]domain.IssueDraft{analysisTestDraft(), invalid})
	if err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	if len(warnings) == 0 {
		t.Fatal("warnings = none, want the invalid draft reported")
	}
	if updated.Status != domain.AnalysisRunning {
		t.Fatalf("run status = %q, want running", updated.Status)
	}
	result, err := domain.DecodeTriageResult(updated.Result)
	if err != nil || len(result.Drafts) != 1 || result.Drafts[0].DedupeKey != analysisTestDedupe {
		t.Fatalf("stored drafts = %+v (%v), want only the valid one", result.Drafts, err)
	}
}

// TestEnsureFallbackIssuesWithoutFailureReturnsZero pins the no-op fallback.
func TestEnsureFallbackIssuesWithoutFailureReturnsZero(t *testing.T) {
	h := newAnalysisHarness()
	created, err := h.service.EnsureFallbackIssues(context.Background(), 7, 9, 42)
	if err != nil || created != 0 {
		t.Fatalf("EnsureFallbackIssues = %d, %v; want 0, nil", created, err)
	}
	if h.issues.calls != 0 {
		t.Fatalf("ApplyDrafts calls = %d, want none", h.issues.calls)
	}
}

// TestGetAndListScopeAnalysesToTheBatch pins the §7.5 ownership rules.
func TestGetAndListScopeAnalysesToTheBatch(t *testing.T) {
	h := newAnalysisHarness()
	run := domain.AnalysisRun{
		ID: 11, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindFailure, Status: domain.AnalysisDone,
	}
	other := domain.AnalysisRun{
		ID: 12, VersionID: 7, BatchID: 10, Kind: domain.AnalysisKindFailure, Status: domain.AnalysisDone,
	}
	h.store.seed(run)
	h.store.seed(other)

	got, err := h.service.Get(context.Background(), 7, 9, 11)
	if err != nil || got.ID != 11 {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := h.service.Get(context.Background(), 7, 10, 11); domain.AnalysisReason(err) != domain.ReasonAnalysisNotFound {
		t.Fatalf("cross-batch Get reason = %q, want analysis_not_found", domain.AnalysisReason(err))
	}
	list, err := h.service.List(context.Background(), 7, 9)
	if err != nil || len(list) != 1 || list[0].ID != 11 {
		t.Fatalf("List = %+v, %v", list, err)
	}

	// The execution autonomy adapter addresses analyses by (batch, kind) only.
	latest, err := h.service.LatestByKind(context.Background(), 9, domain.AnalysisKindFailure)
	if err != nil || latest.ID != 11 {
		t.Fatalf("LatestByKind = %+v, %v; want run 11", latest, err)
	}
	if _, err := h.service.LatestByKind(context.Background(), 11, domain.AnalysisKindFailure); domain.AnalysisReason(err) != domain.ReasonAnalysisNotFound {
		t.Fatalf("missing LatestByKind reason = %q, want analysis_not_found", domain.AnalysisReason(err))
	}
}

// TestRecoverStaleAndWorkerBootRecovery pins the boot-time stale recovery of
// spec §6.7 and the fixed 2s/5-run worker cadence. Worker runs the recovery
// synchronously before its first tick, so the recovered rows are visible as soon
// as Worker returns.
func TestRecoverStaleAndWorkerBootRecovery(t *testing.T) {
	if analysisWorkerInterval != 2*time.Second {
		t.Fatalf("worker interval = %s, want 2s", analysisWorkerInterval)
	}
	if analysisWorkerBatch != 5 {
		t.Fatalf("worker batch = %d, want 5", analysisWorkerBatch)
	}

	h := newAnalysisHarness()
	h.store.seed(domain.AnalysisRun{
		ID: 5, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindFailure, Status: domain.AnalysisQueued,
	})
	recovered, err := h.service.RecoverStale(context.Background())
	if err != nil || recovered != 1 {
		t.Fatalf("RecoverStale = %d, %v; want 1, nil", recovered, err)
	}
	stored, _ := h.store.get(5)
	if stored.Status != domain.AnalysisFailed {
		t.Fatalf("recovered run status = %q, want failed", stored.Status)
	}
	if stored.FinishedAt == nil {
		t.Fatal("recovered finished_at = nil, want the recovery timestamp")
	}
	var failures map[string]any
	if err := json.Unmarshal(stored.Errors, &failures); err != nil || failures["reason"] != domain.ReasonStaleRunRecovered {
		t.Fatalf("recovered errors = %s (%v), want %s", stored.Errors, err, domain.ReasonStaleRunRecovered)
	}
	if h.issues.calls != 0 {
		t.Fatalf("ApplyDrafts calls = %d, want no fallback for a recovered failure run", h.issues.calls)
	}

	boot := newAnalysisHarness()
	boot.store.seed(domain.AnalysisRun{
		ID: 6, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindIssueTriage, Status: domain.AnalysisRunning,
	})
	boot.service.Worker(context.Background())
	// The boot recovery is synchronous: no polling window to wait for.
	storedBoot, _ := boot.store.get(6)
	if storedBoot.Status != domain.AnalysisFailed {
		boot.service.Stop()
		t.Fatalf("boot recovered status = %q, want failed", storedBoot.Status)
	}
	boot.service.Stop()
}

// TestWorkerBootRecoveryMaterializesTriageFallback pins finding 3: a triage run
// left queued/running by a previous process is failed stale at boot, and its
// failure-candidate fallback is materialized immediately (spec §5.6) so the
// issues are not lost.
func TestWorkerBootRecoveryMaterializesTriageFallback(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.store.seed(domain.AnalysisRun{
		ID: 800, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindIssueTriage,
		Status: domain.AnalysisRunning, CreatedBy: 42,
		CreatedAt: analysisTestNow.Add(-time.Minute), UpdatedAt: analysisTestNow.Add(-time.Minute),
	})

	h.service.Worker(context.Background())
	defer h.service.Stop()

	stored, _ := h.store.get(800)
	if stored.Status != domain.AnalysisFailed {
		t.Fatalf("recovered triage status = %q, want failed", stored.Status)
	}
	if h.issues.calls != 1 || len(h.issues.drafts) != 1 {
		t.Fatalf("fallback ApplyDrafts calls/drafts = %d/%+v, want the triage fallback", h.issues.calls, h.issues.drafts)
	}
	if h.issues.drafts[0].DedupeKey == "" {
		t.Fatalf("fallback draft = %+v, want the deterministic failure dedupe key", h.issues.drafts[0])
	}
	// A replayed boot (or the execution adapter calling fallback again) must be
	// replay-idempotent: the same dedupe key returns the same issue row.
	before := h.issues.calls
	created, err := h.service.EnsureFallbackIssues(context.Background(), 7, 9, 42)
	if err != nil || created != 1 {
		t.Fatalf("EnsureFallbackIssues replay = %d, %v; want 1, nil", created, err)
	}
	if h.issues.calls != before+1 {
		t.Fatalf("replay ApplyDrafts calls = %d, want %d", h.issues.calls, before+1)
	}
}

// analysisTestDraftWithKey returns a valid triage draft with the given dedupe key.
func analysisTestDraftWithKey(key string) domain.IssueDraft {
	draft := analysisTestDraft()
	draft.DedupeKey = key
	return draft
}

// TestStartTriageRejectsActiveRun pins the unique-active rule on the triage
// side: a second active run is analysis_active and never starts an agent.
func TestStartTriageRejectsActiveRun(t *testing.T) {
	h := newAnalysisHarness()
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.store.seed(domain.AnalysisRun{
		ID: 705, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindIssueTriage, Status: domain.AnalysisRunning,
	})

	_, err := h.service.StartTriage(context.Background(), 7, 9, 42)
	if err == nil {
		t.Fatal("StartTriage started a second active run")
	}
	if reason := domain.AnalysisReason(err); reason != domain.ReasonAnalysisActive {
		t.Fatalf("reason = %q, want %q", reason, domain.ReasonAnalysisActive)
	}
	if h.agents.created != 0 {
		t.Fatalf("agent sessions = %d, want none", h.agents.created)
	}
}

// TestStartTriageToleratesOpenIssueReadFailure pins the best-effort open issue
// read: a failed list degrades the input to no existing issues and never blocks
// the triage run.
func TestStartTriageToleratesOpenIssueReadFailure(t *testing.T) {
	h := newAnalysisHarness()
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.openIssues.err = errors.New("issue store down")

	run, err := h.service.StartTriage(context.Background(), 7, 9, 42)
	if err != nil {
		t.Fatalf("StartTriage returned an error for a failed open issue read: %v", err)
	}
	input, err := domain.DecodeTriageInput(run.Input)
	if err != nil || len(input.ExistingIssues) != 0 {
		t.Fatalf("triage input existing issues = %+v (%v), want none", input.ExistingIssues, err)
	}
}

// TestProposeIssueDraftsDropsNonOpenMergeTarget pins finding 1 and spec §9.4:
// mergeIntoIssueId is only allowed for an open issue; a target that is not open
// is dropped with a warning instead of merging into a confirmed/dismissed issue.
func TestProposeIssueDraftsDropsNonOpenMergeTarget(t *testing.T) {
	h := newAnalysisHarness()
	h.openIssues.issues = []domain.OpenIssue{{IssueID: 55, Title: "open defect", Severity: "major"}}
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, nil, nil), 5002)

	openDraft := analysisTestDraftWithKey(strings.Repeat("a", 64))
	openDraft.MergeIntoIssueID = analysisTestInt64(55)
	closedDraft := analysisTestDraftWithKey(strings.Repeat("b", 64))
	closedDraft.Title = "second draft"
	closedDraft.MergeIntoIssueID = analysisTestInt64(66)

	updated, warnings, err := h.service.ProposeIssueDrafts(context.Background(), run.ID,
		[]domain.IssueDraft{openDraft, closedDraft})
	if err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	result, err := domain.DecodeTriageResult(updated.Result)
	if err != nil || len(result.Drafts) != 2 {
		t.Fatalf("stored drafts = %+v (%v), want both drafts kept", result.Drafts, err)
	}
	if result.Drafts[0].MergeIntoIssueID == nil || *result.Drafts[0].MergeIntoIssueID != 55 {
		t.Fatalf("open merge target = %v, want 55", result.Drafts[0].MergeIntoIssueID)
	}
	if result.Drafts[1].MergeIntoIssueID != nil {
		t.Fatalf("closed merge target = %v, want it dropped", *result.Drafts[1].MergeIntoIssueID)
	}
	joined := strings.Join(warnings, "\n")
	if !strings.Contains(joined, "mergeIntoIssueId") || !strings.Contains(joined, "66") {
		t.Fatalf("warnings = %q, want the dropped merge target reported", joined)
	}
}

// TestProcessOnceTriageStripsNonOpenMergeTarget pins finding 1 at materialization
// time: a merge target that stopped being open after submission is stripped and
// the draft still materializes (with a warning), never merging into the issue.
func TestProcessOnceTriageStripsNonOpenMergeTarget(t *testing.T) {
	h := newAnalysisHarness()
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.openIssues.issues = []domain.OpenIssue{{IssueID: 55, Title: "open defect", Severity: "major"}}
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)
	draft := analysisTestDraft()
	draft.MergeIntoIssueID = analysisTestInt64(55)
	if _, _, err := h.service.ProposeIssueDrafts(context.Background(), run.ID, []domain.IssueDraft{draft}); err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	// The target is confirmed/dismissed by a human before materialization.
	h.openIssues.issues = nil
	h.agents.setStatus(5002, "done")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if h.issues.calls != 1 || len(h.issues.drafts) != 1 {
		t.Fatalf("ApplyDrafts calls/drafts = %d/%+v", h.issues.calls, h.issues.drafts)
	}
	if h.issues.drafts[0].MergeIntoIssueID != nil {
		t.Fatalf("materialized draft merge target = %v, want it stripped", *h.issues.drafts[0].MergeIntoIssueID)
	}
	stored, _ := h.store.get(run.ID)
	result, err := domain.DecodeTriageResult(stored.Result)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "mergeIntoIssueId") {
		t.Fatalf("stored warnings = %v, want the stripped merge target reported", result.Warnings)
	}
}

// TestProposeFailureCandidatesStripsUnknownEvidenceIDs pins finding 2 on the
// failure analysis side: an evidence id outside the failed application case runs
// is stripped with a warning instead of failing the candidate.
func TestProposeFailureCandidatesStripsUnknownEvidenceIDs(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	run := h.analysisRunningRun(domain.AnalysisKindFailure,
		domain.EncodeFailureInput(7, 9, []int64{11}), 5001)
	candidate := analysisTestCandidate(11, 101)
	candidate.EvidenceIDs = []int64{701, 999}

	updated, warnings, err := h.service.ProposeFailureCandidates(context.Background(), run.ID,
		[]domain.FailureCandidate{candidate})
	if err != nil {
		t.Fatalf("ProposeFailureCandidates: %v", err)
	}
	result, err := domain.DecodeFailureResult(updated.Result)
	if err != nil || len(result.Candidates) != 1 {
		t.Fatalf("stored candidates = %+v (%v), want the candidate kept", result.Candidates, err)
	}
	if got := result.Candidates[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("stored evidence ids = %v, want the allowed [701]", got)
	}
	if !strings.Contains(strings.Join(warnings, "\n"), "999") {
		t.Fatalf("warnings = %v, want the foreign evidence id reported", warnings)
	}
}

// TestEnsureFallbackIssuesStripsUnknownEvidence pins finding 2 on the fallback
// path: a foreign evidence id of a stored candidate never reaches ApplyDrafts
// (T2 rejects unknown evidence and would lose every issue).
func TestEnsureFallbackIssuesStripsUnknownEvidence(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	candidate := analysisTestCandidate(11, 101)
	candidate.EvidenceIDs = []int64{701, 999}
	h.analysisFailureRun(domain.AnalysisDone, []domain.FailureCandidate{candidate}, []int64{11})

	created, err := h.service.EnsureFallbackIssues(context.Background(), 7, 9, 42)
	if err != nil || created != 1 {
		t.Fatalf("EnsureFallbackIssues = %d, %v; want 1, nil", created, err)
	}
	if h.issues.calls != 1 || len(h.issues.drafts) != 1 {
		t.Fatalf("ApplyDrafts calls/drafts = %d/%+v", h.issues.calls, h.issues.drafts)
	}
	if got := h.issues.drafts[0].EvidenceIDs; len(got) != 1 || got[0] != 701 {
		t.Fatalf("fallback evidence = %v, want the allowed [701]", got)
	}
}

// TestProcessOnceFailsStaleQueuedRunWithoutSession pins finding 4: a queued run
// without an agent session cannot advance, so the worker fails it stale instead
// of leaking the (batch, kind) active slot forever.
func TestProcessOnceFailsStaleQueuedRunWithoutSession(t *testing.T) {
	queued := func() domain.AnalysisRun {
		return domain.AnalysisRun{
			ID: 810, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindFailure,
			Status: domain.AnalysisQueued, CreatedBy: 42,
			CreatedAt: analysisTestNow.Add(-time.Minute), UpdatedAt: analysisTestNow.Add(-time.Minute),
		}
	}
	t.Run("failure", func(t *testing.T) {
		h := newAnalysisHarness()
		h.store.seed(queued())
		if err := h.service.ProcessOnce(context.Background()); err != nil {
			t.Fatalf("ProcessOnce: %v", err)
		}
		stored, _ := h.store.get(810)
		if stored.Status != domain.AnalysisFailed {
			t.Fatalf("queued run status = %q, want failed stale", stored.Status)
		}
		var failures map[string]any
		if err := json.Unmarshal(stored.Errors, &failures); err != nil || failures["reason"] != domain.ReasonStaleRunRecovered {
			t.Fatalf("errors = %s (%v), want %s", stored.Errors, err, domain.ReasonStaleRunRecovered)
		}
	})
	t.Run("triage fallback", func(t *testing.T) {
		h := newAnalysisHarness()
		h.execution.cases = []AnalysisFailedCase{
			{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
		}
		h.analysisFailureRun(domain.AnalysisDone,
			[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
		stale := queued()
		stale.Kind = domain.AnalysisKindIssueTriage
		h.store.seed(stale)
		if err := h.service.ProcessOnce(context.Background()); err != nil {
			t.Fatalf("ProcessOnce: %v", err)
		}
		stored, _ := h.store.get(810)
		if stored.Status != domain.AnalysisFailed {
			t.Fatalf("queued triage status = %q, want failed stale", stored.Status)
		}
		if h.issues.calls != 1 {
			t.Fatalf("ApplyDrafts calls = %d, want the triage fallback", h.issues.calls)
		}
	})
	t.Run("fresh queued is left alone", func(t *testing.T) {
		h := newAnalysisHarness()
		run := queued()
		run.CreatedAt = analysisTestNow
		run.UpdatedAt = analysisTestNow
		h.store.seed(run)
		if err := h.service.ProcessOnce(context.Background()); err != nil {
			t.Fatalf("ProcessOnce: %v", err)
		}
		stored, _ := h.store.get(810)
		if stored.Status != domain.AnalysisQueued {
			t.Fatalf("fresh queued status = %q, want queued (an in-flight start may be racing)", stored.Status)
		}
	})
}

// TestProcessOnceAdvancesQueuedRunWithSession pins the other half of finding 4:
// a queued run that already carries an agent session advances to running so its
// next tick can poll it.
func TestProcessOnceAdvancesQueuedRunWithSession(t *testing.T) {
	h := newAnalysisHarness()
	h.store.seed(domain.AnalysisRun{
		ID: 811, VersionID: 7, BatchID: 9, Kind: domain.AnalysisKindFailure,
		Status: domain.AnalysisQueued, AgentSessionID: analysisTestInt64(5001), CreatedBy: 42,
		Input:     domain.EncodeFailureInput(7, 9, []int64{11}),
		CreatedAt: analysisTestNow.Add(-time.Minute), UpdatedAt: analysisTestNow.Add(-time.Minute),
	})
	h.agents.sessions[5001] = &AnalysisAgentSessionStatus{SessionID: 5001, Status: "running"}

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	advanced, _ := h.store.get(811)
	if advanced.Status != domain.AnalysisRunning || advanced.StartedAt == nil {
		t.Fatalf("advanced run = %+v, want running with started_at", advanced)
	}
	if started := analysisEventDetails(t, h.events.events, domain.EventAnalysisStarted); len(started) != 1 {
		t.Fatalf("analysis.started events = %d, want 1", len(started))
	}

	h.agents.setStatus(5001, "done")
	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("second ProcessOnce: %v", err)
	}
	stored, _ := h.store.get(811)
	if stored.Status != domain.AnalysisDegraded {
		t.Fatalf("advanced run final status = %q, want degraded (no candidates)", stored.Status)
	}
}

// TestProcessOnceDegradesOnTerminalBudgetReasons pins finding 5 and spec §9.7:
// the P1 terminal reasons for timeouts, budget exhaustion and model/context
// failures degrade the analysis run instead of failing it.
func TestProcessOnceDegradesOnTerminalBudgetReasons(t *testing.T) {
	cases := []struct {
		name   string
		status string
		reason string
	}{
		{name: "timeout done", status: "done", reason: "timeout"},
		{name: "max turns done", status: "done", reason: "max_turns"},
		{name: "hard timeout aborted", status: "aborted", reason: "run_hard_timeout"},
		{name: "model error failed", status: "failed", reason: "model_error"},
		{name: "context overflow failed", status: "failed", reason: "context_overflow"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newAnalysisHarness()
			run := h.analysisRunningRun(domain.AnalysisKindFailure,
				domain.EncodeFailureInput(7, 9, []int64{11}), 5001)
			h.agents.setTerminal(5001, tc.status, tc.reason)

			if err := h.service.ProcessOnce(context.Background()); err != nil {
				t.Fatalf("ProcessOnce: %v", err)
			}
			stored, _ := h.store.get(run.ID)
			if stored.Status != domain.AnalysisDegraded {
				t.Fatalf("status = %q, want degraded", stored.Status)
			}
			var failures map[string]any
			if err := json.Unmarshal(stored.Errors, &failures); err != nil || failures["reason"] != tc.reason {
				t.Fatalf("errors = %s (%v), want reason %s", stored.Errors, err, tc.reason)
			}
			var stats map[string]any
			if err := json.Unmarshal(stored.Stats, &stats); err != nil || stats["terminalReason"] != tc.reason {
				t.Fatalf("stats = %s (%v), want terminalReason %s", stored.Stats, err, tc.reason)
			}
			finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
			if len(finished) != 1 || finished[0]["status"] != domain.AnalysisDegraded {
				t.Fatalf("analysis.finished = %+v, want a degraded document", finished)
			}
		})
	}
	t.Run("triage hard timeout falls back", func(t *testing.T) {
		h := newAnalysisHarness()
		h.execution.cases = []AnalysisFailedCase{
			{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
		}
		h.analysisFailureRun(domain.AnalysisDone,
			[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
		run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
			domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)
		h.agents.setTerminal(5002, "aborted", "run_hard_timeout")

		if err := h.service.ProcessOnce(context.Background()); err != nil {
			t.Fatalf("ProcessOnce: %v", err)
		}
		stored, _ := h.store.get(run.ID)
		if stored.Status != domain.AnalysisDegraded {
			t.Fatalf("triage status = %q, want degraded", stored.Status)
		}
		if h.issues.calls != 1 {
			t.Fatalf("ApplyDrafts calls = %d, want the §5.6 fallback", h.issues.calls)
		}
		finished := analysisEventDetails(t, h.events.events, domain.EventAnalysisFinished)
		if len(finished) != 1 || finished[0]["status"] != domain.AnalysisDegraded || finished[0]["issueCount"] != 1 {
			t.Fatalf("analysis.finished = %+v, want degraded with the fallback issueCount", finished)
		}
	})
}

// TestProcessOnceReplayDoesNotRematerialize pins the replay safety of the worker:
// a second tick over an already terminal triage run neither re-applies drafts nor
// re-runs the fallback.
func TestProcessOnceReplayDoesNotRematerialize(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)
	h.agents.setStatus(5002, "aborted")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	applied := h.issues.calls
	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("replayed ProcessOnce: %v", err)
	}
	if h.issues.calls != applied {
		t.Fatalf("ApplyDrafts calls = %d after replay, want %d", h.issues.calls, applied)
	}
	stored, _ := h.store.get(run.ID)
	if stored.Status != domain.AnalysisFailed {
		t.Fatalf("triage status = %q after replay, want failed", stored.Status)
	}
}

// TestEnsureFallbackIssuesReplayIsStable pins the replay-idempotency the boot
// recovery relies on: a replayed fallback re-delivers the same dedupe keys and
// yields the same issue rows.
func TestEnsureFallbackIssuesReplayIsStable(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})

	first, err := h.service.EnsureFallbackIssues(context.Background(), 7, 9, 42)
	if err != nil || first != 1 {
		t.Fatalf("first EnsureFallbackIssues = %d, %v; want 1, nil", first, err)
	}
	firstIssue := h.issues.byDedupeKey[h.issues.drafts[0].DedupeKey]
	second, err := h.service.EnsureFallbackIssues(context.Background(), 7, 9, 42)
	if err != nil || second != 1 {
		t.Fatalf("replayed EnsureFallbackIssues = %d, %v; want 1, nil", second, err)
	}
	secondIssue := h.issues.byDedupeKey[h.issues.drafts[0].DedupeKey]
	if firstIssue.ID == 0 || firstIssue.ID != secondIssue.ID {
		t.Fatalf("replayed issue = %+v, want the same row as %+v", secondIssue, firstIssue)
	}
}

// TestCreateIssueAnchorsMapsMergedDraftsToIssues pins finding 6: ApplyDrafts
// returns unique issues (two drafts may merge into one), so anchors are attached
// by mergeIntoIssueId / dedupe key and never by position.
func TestCreateIssueAnchorsMapsMergedDraftsToIssues(t *testing.T) {
	h := newAnalysisHarness()
	h.execution.cases = []AnalysisFailedCase{
		{CaseRunID: 11, CaseNodeID: 101, FailureClass: "application", EvidenceIDs: []int64{701}},
	}
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	h.openIssues.issues = []domain.OpenIssue{{IssueID: 9001, Title: "existing defect", Severity: "major"}}
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, []domain.FailureCandidate{analysisTestCandidate(11, 101)}, nil), 5002)

	keyA, keyB, keyC := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
	mergedA := analysisTestDraftWithKey(keyA)
	mergedA.MergeIntoIssueID = analysisTestInt64(9001)
	mergedA.CodeRefs = []domain.CodeRef{{Path: "src/a.go", Line: 10, Reason: "a"}}
	mergedB := analysisTestDraftWithKey(keyB)
	mergedB.Title = "second candidate, same root cause"
	mergedB.MergeIntoIssueID = analysisTestInt64(9001)
	mergedB.CodeRefs = []domain.CodeRef{{Path: "src/b.go", Line: 20, Reason: "b"}}
	fresh := analysisTestDraftWithKey(keyC)
	fresh.Title = "new defect"
	fresh.CodeRefs = []domain.CodeRef{{Path: "src/c.go", Line: 30, Reason: "c"}}
	if _, _, err := h.service.ProposeIssueDrafts(context.Background(), run.ID,
		[]domain.IssueDraft{mergedA, mergedB, fresh}); err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	// IssueService returns unique issues: the two merged drafts collapse into the
	// existing issue 9001, the third draft creates 9002.
	h.issues.issues = []domain.Issue{
		{ID: 9001, VersionID: 7, DedupeKey: "existing-dedupe"},
		{ID: 9002, VersionID: 7, DedupeKey: keyC},
	}
	h.agents.setStatus(5002, "done")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if h.anchors.calls != 2 {
		t.Fatalf("anchor calls = %d, want one per unique issue", h.anchors.calls)
	}
	if len(h.anchors.issueIDs) != 2 || h.anchors.issueIDs[0] != 9001 || h.anchors.issueIDs[1] != 9002 {
		t.Fatalf("anchor issue ids = %v, want [9001 9002]", h.anchors.issueIDs)
	}
	if len(h.anchors.refs[0]) != 2 || h.anchors.refs[0][0].Path != "src/a.go" || h.anchors.refs[0][1].Path != "src/b.go" {
		t.Fatalf("issue 9001 refs = %+v, want the two merged drafts' refs", h.anchors.refs[0])
	}
	if len(h.anchors.refs[1]) != 1 || h.anchors.refs[1][0].Path != "src/c.go" {
		t.Fatalf("issue 9002 refs = %+v, want the new draft's refs", h.anchors.refs[1])
	}
	stored, _ := h.store.get(run.ID)
	result, err := domain.DecodeTriageResult(stored.Result)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "code anchors") {
			t.Fatalf("unexpected unmapped anchor warning: %q", warning)
		}
	}
}

// TestCreateIssueAnchorsWarnsOnUnmappedDraft pins finding 6's failure mode: when
// ApplyDrafts returns no issue for a draft, the refs are not attached to another
// issue; a warning is recorded instead.
func TestCreateIssueAnchorsWarnsOnUnmappedDraft(t *testing.T) {
	h := newAnalysisHarness()
	h.analysisFailureRun(domain.AnalysisDone,
		[]domain.FailureCandidate{analysisTestCandidate(11, 101)}, []int64{11})
	run := h.analysisRunningRun(domain.AnalysisKindIssueTriage,
		domain.EncodeTriageInput(7, 9, nil, nil), 5002)
	draft := analysisTestDraft()
	draft.CodeRefs = []domain.CodeRef{{Path: "src/cart.go", Line: 42, Reason: "nil dereference"}}
	if _, _, err := h.service.ProposeIssueDrafts(context.Background(), run.ID, []domain.IssueDraft{draft}); err != nil {
		t.Fatalf("ProposeIssueDrafts: %v", err)
	}
	h.issues.issues = []domain.Issue{{ID: 9050, VersionID: 7}}
	h.agents.setStatus(5002, "done")

	if err := h.service.ProcessOnce(context.Background()); err != nil {
		t.Fatalf("ProcessOnce: %v", err)
	}
	if h.anchors.calls != 0 {
		t.Fatalf("anchor calls = %d, want none for an unmapped draft", h.anchors.calls)
	}
	stored, _ := h.store.get(run.ID)
	result, err := domain.DecodeTriageResult(stored.Result)
	if err != nil {
		t.Fatalf("decode stored result: %v", err)
	}
	if !strings.Contains(strings.Join(result.Warnings, "\n"), "code anchors") {
		t.Fatalf("stored warnings = %v, want an unmapped anchor warning", result.Warnings)
	}
}
