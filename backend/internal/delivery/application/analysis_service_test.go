package deliveryapp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	delivery "github.com/EdouardRichard/EzTest/backend/internal/delivery/domain"
)

var analysisTestNow = time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC)

// fakeAnalysisRepository is the in-memory AnalysisRepository of the analysis
// tests. It mirrors the database invariants the service relies on: at most one
// active run per (version, kind, dedupe_key), one agent_run per analysis run,
// and terminal-only completion.
type fakeAnalysisRepository struct {
	runs      map[int64]delivery.AnalysisRun
	nextID    int64
	createErr error
	markErr   error
	compErr   error
	// createCalls counts CreateQueued invocations even when an active dedupe
	// row is returned.
	createCalls int
	latestCalls []int64
}

func newFakeAnalysisRepository() *fakeAnalysisRepository {
	return &fakeAnalysisRepository{runs: make(map[int64]delivery.AnalysisRun), nextID: 1000}
}

func (r *fakeAnalysisRepository) CreateQueued(_ context.Context, run delivery.AnalysisRun) (delivery.AnalysisRun, error) {
	r.createCalls++
	if r.createErr != nil {
		return delivery.AnalysisRun{}, r.createErr
	}
	for _, existing := range r.runs {
		if existing.VersionID == run.VersionID && existing.Kind == run.Kind &&
			existing.DedupeKey == run.DedupeKey && analysisActive(existing.Status) {
			return existing, nil
		}
	}
	r.nextID++
	run.ID = r.nextID
	run.Status = delivery.AnalysisStatusQueued
	if run.CreatedAt.IsZero() {
		run.CreatedAt = analysisTestNow
	}
	if run.UpdatedAt.IsZero() {
		run.UpdatedAt = analysisTestNow
	}
	r.runs[run.ID] = run
	return run, nil
}

func (r *fakeAnalysisRepository) MarkRunning(_ context.Context, id int64, agentRunID int64) error {
	if r.markErr != nil {
		return r.markErr
	}
	run, ok := r.runs[id]
	if !ok {
		return delivery.ErrNotFound
	}
	if run.Status == delivery.AnalysisStatusRunning && run.AgentRunID != nil && *run.AgentRunID == agentRunID {
		return nil
	}
	if !analysisActive(run.Status) {
		return delivery.ErrStateConflict
	}
	for _, other := range r.runs {
		if other.ID != id && other.AgentRunID != nil && *other.AgentRunID == agentRunID {
			return delivery.ErrStateConflict
		}
	}
	runner := agentRunID
	run.AgentRunID = &runner
	run.Status = delivery.AnalysisStatusRunning
	r.runs[id] = run
	return nil
}

func (r *fakeAnalysisRepository) Complete(_ context.Context, id int64, status delivery.AnalysisStatus, output json.RawMessage, errMsg string) error {
	if r.compErr != nil {
		return r.compErr
	}
	run, ok := r.runs[id]
	if !ok {
		return delivery.ErrNotFound
	}
	if !analysisTerminal(status) {
		return delivery.ErrInvalidArgument
	}
	if !analysisActive(run.Status) {
		return delivery.ErrStateConflict
	}
	run.Status = status
	run.Output = append(json.RawMessage(nil), output...)
	run.Error = errMsg
	r.runs[id] = run
	return nil
}

func (r *fakeAnalysisRepository) LatestByIssue(_ context.Context, issueID int64) (delivery.AnalysisRun, bool, error) {
	r.latestCalls = append(r.latestCalls, issueID)
	var latest delivery.AnalysisRun
	found := false
	for _, run := range r.runs {
		if run.Kind != analysisKindTriage || !analysisInputHasIssue(run.InputRef, issueID) {
			continue
		}
		if run.Status != delivery.AnalysisStatusDone && run.Status != delivery.AnalysisStatusDegraded {
			continue
		}
		if !found || run.ID > latest.ID {
			latest = run
			found = true
		}
	}
	return latest, found, nil
}

var _ AnalysisRepository = (*fakeAnalysisRepository)(nil)

func analysisActive(status delivery.AnalysisStatus) bool {
	return status == delivery.AnalysisStatusQueued || status == delivery.AnalysisStatusRunning
}

func analysisTerminal(status delivery.AnalysisStatus) bool {
	switch status {
	case delivery.AnalysisStatusDone, delivery.AnalysisStatusFailed, delivery.AnalysisStatusDegraded:
		return true
	default:
		return false
	}
}

func analysisInputHasIssue(raw json.RawMessage, issueID int64) bool {
	var input struct {
		IssueID string `json:"issue_id"`
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		return false
	}
	return input.IssueID == formatIssueID(issueID)
}

type analysisFixture struct {
	issues   *issueServiceFixture
	repo     *fakeAnalysisRepository
	clock    fakeIssueClock
	svc      *AnalysisService
	issueSvc *IssueService
}

func newAnalysisFixture() *analysisFixture {
	issues := newIssueServiceFixture()
	clock := fakeIssueClock{now: analysisTestNow}
	repo := newFakeAnalysisRepository()
	return &analysisFixture{
		issues:   issues,
		repo:     repo,
		clock:    clock,
		svc:      NewAnalysisService(repo, issues.svc, clock),
		issueSvc: issues.svc,
	}
}

func (f *analysisFixture) seedIssue(issueID, versionID int64) {
	f.issues.repo.getIssues[issueID] = delivery.Issue{
		ID:               issueID,
		VersionID:        versionID,
		Title:            "checkout fails",
		Status:           delivery.IssueStatusOpen,
		Category:         "ui",
		Severity:         "major",
		ExpectedRevision: 1,
	}
}

func TestAnalysisStartTriageCreatesQueuedRun(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.seedIssue(900, 7)
	fixture.issues.repo.refs = []delivery.IssueRef{
		{Kind: delivery.IssueRefKindCaseRun, RefID: 3001, Role: delivery.IssueRefRoleSource},
	}

	run, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("StartTriage: %v", err)
	}
	if run.ID <= 0 {
		t.Fatalf("analysis run ID = %d, want > 0", run.ID)
	}
	if run.VersionID != 7 || run.Kind != analysisKindTriage || run.Status != delivery.AnalysisStatusQueued {
		t.Fatalf("analysis run = %+v", run)
	}
	if run.DedupeKey != "triage:900" {
		t.Fatalf("dedupe key = %q, want triage:900", run.DedupeKey)
	}
	if !analysisInputHasIssue(run.InputRef, 900) {
		t.Fatalf("input_ref = %s, want issue_id=900", run.InputRef)
	}
	var input struct {
		VersionID string `json:"version_id"`
		Refs      []struct {
			Kind  string `json:"kind"`
			RefID string `json:"ref_id"`
		} `json:"refs"`
	}
	if err := json.Unmarshal(run.InputRef, &input); err != nil {
		t.Fatalf("decode input_ref: %v", err)
	}
	if input.VersionID != "7" || len(input.Refs) != 1 || input.Refs[0].Kind != delivery.IssueRefKindCaseRun || input.Refs[0].RefID != "3001" {
		t.Fatalf("input_ref = %+v", input)
	}
	if fixture.repo.createCalls != 1 {
		t.Fatalf("CreateQueued calls = %d, want 1", fixture.repo.createCalls)
	}
}

func TestAnalysisStartTriageReusesActiveRun(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.seedIssue(900, 7)

	first, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("first StartTriage: %v", err)
	}
	second, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("second StartTriage: %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("active dedupe created a second run: %d then %d", first.ID, second.ID)
	}
	if len(fixture.repo.runs) != 1 {
		t.Fatalf("analysis rows = %d, want 1 active run", len(fixture.repo.runs))
	}
}

func TestAnalysisStartTriageStartsNewRunAfterTerminal(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.seedIssue(900, 7)

	first, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("first StartTriage: %v", err)
	}
	if err := fixture.svc.Complete(context.Background(), first.ID, delivery.AnalysisStatusDone, json.RawMessage(`{"ok":true}`), ""); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	second, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("second StartTriage: %v", err)
	}
	if second.ID == first.ID {
		t.Fatalf("terminal run was reused: %d", first.ID)
	}
	if second.Status != delivery.AnalysisStatusQueued {
		t.Fatalf("new run status = %q, want queued", second.Status)
	}
}

func TestAnalysisStartTriageRejectsUnknownIssue(t *testing.T) {
	fixture := newAnalysisFixture()

	_, err := fixture.svc.StartTriage(context.Background(), 404)
	if !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("StartTriage(unknown) error = %v, want delivery.ErrNotFound", err)
	}
	if fixture.repo.createCalls != 0 {
		t.Fatalf("CreateQueued calls = %d, want 0 for an unknown issue", fixture.repo.createCalls)
	}
}

func TestAnalysisStartTriageRequiresIssueRepository(t *testing.T) {
	repo := newFakeAnalysisRepository()
	svc := NewAnalysisService(repo, NewIssueService(nil, nil, nil, nil, nil, nil), nil)
	_, err := svc.StartTriage(context.Background(), 900)
	if err == nil {
		t.Fatal("StartTriage without an issue repository returned nil")
	}
}

func TestAnalysisCompleteValidatesInput(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.seedIssue(900, 7)
	run, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("StartTriage: %v", err)
	}

	cases := []struct {
		name   string
		status delivery.AnalysisStatus
		output json.RawMessage
		errMsg string
	}{
		{"queued is not terminal", delivery.AnalysisStatusQueued, json.RawMessage(`{}`), ""},
		{"running is not terminal", delivery.AnalysisStatusRunning, json.RawMessage(`{}`), ""},
		{"malformed output", delivery.AnalysisStatusDone, json.RawMessage(`{`), ""},
		{"output must be an object", delivery.AnalysisStatusDone, json.RawMessage(`[1]`), ""},
		{"null output", delivery.AnalysisStatusDone, json.RawMessage(`null`), ""},
		{"error message too long", delivery.AnalysisStatusFailed, json.RawMessage(`{}`), string(make([]rune, 2001))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := fixture.svc.Complete(context.Background(), run.ID, tc.status, tc.output, tc.errMsg)
			if !errors.Is(err, delivery.ErrInvalidArgument) {
				t.Fatalf("Complete error = %v, want delivery.ErrInvalidArgument", err)
			}
		})
	}

	if err := fixture.svc.Complete(context.Background(), run.ID, delivery.AnalysisStatusDone, json.RawMessage(`{"classification":"flaky"}`), ""); err != nil {
		t.Fatalf("valid Complete: %v", err)
	}
	stored := fixture.repo.runs[run.ID]
	if stored.Status != delivery.AnalysisStatusDone {
		t.Fatalf("stored status = %q, want done", stored.Status)
	}
	if string(stored.Output) != `{"classification":"flaky"}` {
		t.Fatalf("stored output = %s", stored.Output)
	}
}

func TestAnalysisCompleteUnknownRunIsNotFound(t *testing.T) {
	fixture := newAnalysisFixture()
	err := fixture.svc.Complete(context.Background(), 999, delivery.AnalysisStatusDone, json.RawMessage(`{}`), "")
	if !errors.Is(err, delivery.ErrNotFound) {
		t.Fatalf("Complete(unknown) error = %v, want delivery.ErrNotFound", err)
	}
}

func TestAnalysisMarkRunningBindsAgentRunOnce(t *testing.T) {
	fixture := newAnalysisFixture()
	fixture.seedIssue(900, 7)
	run, err := fixture.svc.StartTriage(context.Background(), 900)
	if err != nil {
		t.Fatalf("StartTriage: %v", err)
	}
	if err := fixture.svc.MarkRunning(context.Background(), run.ID, 5001); err != nil {
		t.Fatalf("MarkRunning: %v", err)
	}
	stored := fixture.repo.runs[run.ID]
	if stored.Status != delivery.AnalysisStatusRunning || stored.AgentRunID == nil || *stored.AgentRunID != 5001 {
		t.Fatalf("run after MarkRunning = %+v", stored)
	}
	// The same agent run binding is idempotent; a different analysis run cannot
	// claim the same agent run (1:1).
	if err := fixture.svc.MarkRunning(context.Background(), run.ID, 5001); err != nil {
		t.Fatalf("idempotent MarkRunning: %v", err)
	}
	other := delivery.AnalysisRun{VersionID: 7, Kind: analysisKindTriage, DedupeKey: "triage:901"}
	storedOther, err := fixture.repo.CreateQueued(context.Background(), other)
	if err != nil {
		t.Fatalf("CreateQueued(other): %v", err)
	}
	if err := fixture.svc.MarkRunning(context.Background(), storedOther.ID, 5001); !errors.Is(err, delivery.ErrStateConflict) {
		t.Fatalf("duplicate agent_run MarkRunning error = %v, want delivery.ErrStateConflict", err)
	}
}
