package application_test

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeClarificationRepo is an in-memory domain.ClarificationRepository with the
// semantics the service relies on: the single-active-session constraint, the
// status compare-and-swaps and the decision link that answers a question.
type fakeClarificationRepo struct {
	sessions  map[int64]domain.ClarificationSession
	questions map[int64]domain.Question
	decisions map[int64]domain.Decision

	createSessionErr  error
	createQuestionErr error
	createDecisionErr error
	// linkedAnswerNoop simulates a linked question that a concurrent actor
	// changed before the decision transaction's open-only answer update ran. The
	// fake marks the question dismissed (the update affects zero rows) without
	// answering it.
	linkedAnswerNoop bool
	writes           int
}

// newFakeClarificationRepo returns an empty repository.
func newFakeClarificationRepo() *fakeClarificationRepo {
	return &fakeClarificationRepo{
		sessions:  map[int64]domain.ClarificationSession{},
		questions: map[int64]domain.Question{},
		decisions: map[int64]domain.Decision{},
	}
}

// stamp returns a deterministic timestamp for one row version.
func (r *fakeClarificationRepo) stamp(id int64, bump int) time.Time {
	return testNow.Add(time.Duration(id)*time.Millisecond + time.Duration(bump)*time.Second)
}

// CreateSession stores an active session and enforces version/status defaults.
func (r *fakeClarificationRepo) CreateSession(_ context.Context, session *domain.ClarificationSession) error {
	if r.createSessionErr != nil {
		return r.createSessionErr
	}
	if _, exists := r.sessions[session.ID]; exists {
		return domain.NewConflict("duplicate_key", "clarification session already exists")
	}
	if session.Status == "" {
		session.Status = domain.SessionActive
	}
	if session.Status == domain.SessionActive {
		for _, stored := range r.sessions {
			if stored.VersionID == session.VersionID && stored.Status == domain.SessionActive {
				return domain.NewConflict("clarification_session_active", "version already has an active session")
			}
		}
	}
	session.StartedAt = testNow
	r.sessions[session.ID] = *session
	r.writes++
	return nil
}

// GetActiveSession returns the version's active session or nil.
func (r *fakeClarificationRepo) GetActiveSession(_ context.Context, versionID int64) (*domain.ClarificationSession, error) {
	for _, stored := range r.sessions {
		if stored.VersionID == versionID && stored.Status == domain.SessionActive {
			copied := stored
			return &copied, nil
		}
	}
	return nil, nil
}

// GetSession returns one stored session.
func (r *fakeClarificationRepo) GetSession(_ context.Context, id int64) (*domain.ClarificationSession, error) {
	stored, ok := r.sessions[id]
	if !ok {
		return nil, domain.NewNotFound("clarification session", id)
	}
	copied := stored
	return &copied, nil
}

// CompleteSession completes an active session.
func (r *fakeClarificationRepo) CompleteSession(_ context.Context, id int64, actor int64) (*domain.ClarificationSession, error) {
	stored, ok := r.sessions[id]
	if !ok {
		return nil, domain.NewNotFound("clarification session", id)
	}
	if stored.Status != domain.SessionActive {
		return nil, domain.NewStateConflict("clarification_session_not_active", "session is not active")
	}
	ended := testNow
	stored.Status = domain.SessionCompleted
	stored.EndedAt = &ended
	r.sessions[id] = stored
	r.writes++
	copied := stored
	return &copied, nil
}

// UpdateContextSummary updates the active session summary.
func (r *fakeClarificationRepo) UpdateContextSummary(_ context.Context, versionID int64, summary string, actor int64) error {
	for id, stored := range r.sessions {
		if stored.VersionID == versionID && stored.Status == domain.SessionActive {
			updated := testNow
			stored.ContextSummary = summary
			stored.SummaryUpdatedAt = &updated
			r.sessions[id] = stored
			r.writes++
			return nil
		}
	}
	return domain.NewStateConflict("clarification_session_not_active", "no active session")
}

// CreateQuestion stores one question.
func (r *fakeClarificationRepo) CreateQuestion(_ context.Context, question *domain.Question) error {
	if r.createQuestionErr != nil {
		return r.createQuestionErr
	}
	if _, exists := r.questions[question.ID]; exists {
		return domain.NewConflict("duplicate_key", "question already exists")
	}
	question.CreatedAt = testNow
	question.UpdatedAt = testNow
	r.questions[question.ID] = *question
	r.writes++
	return nil
}

// GetQuestion returns one stored question.
func (r *fakeClarificationRepo) GetQuestion(_ context.Context, id int64) (*domain.Question, error) {
	stored, ok := r.questions[id]
	if !ok {
		return nil, domain.NewNotFound("question", id)
	}
	copied := stored
	return &copied, nil
}

// ListQuestions filters and pages the version's questions ordered by id.
func (r *fakeClarificationRepo) ListQuestions(_ context.Context, filter domain.QuestionFilter) ([]domain.Question, bool, error) {
	matches := make([]domain.Question, 0, len(r.questions))
	for _, stored := range r.questions {
		if stored.VersionID != filter.VersionID {
			continue
		}
		if filter.Status != "" && stored.Status != filter.Status {
			continue
		}
		if filter.RequirementID != nil && (stored.RequirementID == nil || *stored.RequirementID != *filter.RequirementID) {
			continue
		}
		matches = append(matches, stored)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	cursorID, _ := platform.DecodeCursor(filter.Page.Cursor)
	if cursorID != 0 {
		trimmed := matches[:0]
		for _, stored := range matches {
			if stored.ID > cursorID {
				trimmed = append(trimmed, stored)
			}
		}
		matches = trimmed
	}
	limit := filter.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// UpdateQuestion applies the fromStatus compare-and-swap.
func (r *fakeClarificationRepo) UpdateQuestion(_ context.Context, question *domain.Question, fromStatus string) error {
	stored, ok := r.questions[question.ID]
	if !ok {
		return domain.NewNotFound("question", question.ID)
	}
	if stored.Status != fromStatus {
		return domain.NewStateConflict("clarification_question_state_conflict", "question status does not allow this transition")
	}
	stored.Question = question.Question
	stored.Status = question.Status
	stored.Answer = question.Answer
	stored.AnsweredVia = question.AnsweredVia
	stored.AnsweredBy = question.AnsweredBy
	stored.AnsweredAt = question.AnsweredAt
	stored.RequirementID = question.RequirementID
	stored.SessionID = question.SessionID
	stored.UpdatedAt = testNow.Add(time.Second)
	r.questions[stored.ID] = stored
	*question = stored
	r.writes++
	return nil
}

// CreateDecision stores one decision and answers a linked open question when the
// decision is accepted (the real repository does this in one transaction).
func (r *fakeClarificationRepo) CreateDecision(_ context.Context, decision *domain.Decision) error {
	if r.createDecisionErr != nil {
		return r.createDecisionErr
	}
	if _, exists := r.decisions[decision.ID]; exists {
		return domain.NewConflict("duplicate_key", "decision already exists")
	}
	decision.CreatedAt = testNow
	decision.UpdatedAt = testNow
	r.decisions[decision.ID] = *decision
	if decision.Status == domain.DecisionAccepted {
		r.answerLinkedQuestion(decision)
	}
	r.writes++
	return nil
}

// answerLinkedQuestion mirrors the repository's in-transaction link.
func (r *fakeClarificationRepo) answerLinkedQuestion(decision *domain.Decision) {
	if decision.QuestionID == nil {
		return
	}
	stored, ok := r.questions[*decision.QuestionID]
	if !ok || stored.Status != domain.QuestionOpen {
		return
	}
	if r.linkedAnswerNoop {
		// A concurrent dismissal won the race: the repository's
		// WHERE status='open' update touches no row.
		stored.Status = domain.QuestionDismissed
		stored.Answer = ""
		stored.AnsweredBy = nil
		stored.AnsweredAt = nil
		stored.UpdatedAt = testNow.Add(time.Second)
		r.questions[stored.ID] = stored
		return
	}
	stored.Status = domain.QuestionAnswered
	stored.Answer = decision.Decision
	stored.AnsweredVia = domain.AnsweredViaUI
	stored.AnsweredBy = decision.DecidedBy
	stored.AnsweredAt = decision.DecidedAt
	stored.UpdatedAt = testNow.Add(time.Second)
	r.questions[stored.ID] = stored
}

// GetDecision returns one stored decision.
func (r *fakeClarificationRepo) GetDecision(_ context.Context, id int64) (*domain.Decision, error) {
	stored, ok := r.decisions[id]
	if !ok {
		return nil, domain.NewNotFound("decision", id)
	}
	copied := stored
	return &copied, nil
}

// ListDecisions filters and pages the version's decisions ordered by id.
func (r *fakeClarificationRepo) ListDecisions(_ context.Context, filter domain.DecisionFilter) ([]domain.Decision, bool, error) {
	matches := make([]domain.Decision, 0, len(r.decisions))
	for _, stored := range r.decisions {
		if stored.VersionID != filter.VersionID {
			continue
		}
		if filter.Status != "" && stored.Status != filter.Status {
			continue
		}
		matches = append(matches, stored)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].ID < matches[j].ID })
	cursorID, _ := platform.DecodeCursor(filter.Page.Cursor)
	if cursorID != 0 {
		trimmed := matches[:0]
		for _, stored := range matches {
			if stored.ID > cursorID {
				trimmed = append(trimmed, stored)
			}
		}
		matches = trimmed
	}
	limit := filter.Page.Limit
	if limit <= 0 {
		limit = 20
	}
	more := len(matches) > limit
	if more {
		matches = matches[:limit]
	}
	return matches, more, nil
}

// UpdateDecision applies the fromStatus compare-and-swap and answers a linked
// open question when the decision becomes accepted.
func (r *fakeClarificationRepo) UpdateDecision(_ context.Context, decision *domain.Decision, fromStatus string) error {
	stored, ok := r.decisions[decision.ID]
	if !ok {
		return domain.NewNotFound("decision", decision.ID)
	}
	if stored.Status != fromStatus {
		return domain.NewStateConflict("decision_state_conflict", "decision status does not allow this transition")
	}
	stored.Status = decision.Status
	stored.Rationale = decision.Rationale
	stored.DecidedBy = decision.DecidedBy
	stored.DecidedAt = decision.DecidedAt
	stored.UpdatedAt = testNow.Add(time.Second)
	r.decisions[stored.ID] = stored
	*decision = stored
	if stored.Status == domain.DecisionAccepted {
		r.answerLinkedQuestion(&stored)
	}
	r.writes++
	return nil
}

// clarificationFixture wires the real clarification service to in-memory fakes.
type clarificationFixture struct {
	service      *application.ClarificationService
	repo         *fakeClarificationRepo
	requirements *fakeRequirementRepo
	versions     *fakeVersionRepo
	agents       *fakeAgentSessions
	events       *fakeEvents
	ids          *fixedIDs
}

// newClarificationFixture returns a fixture whose first generated id is 5000.
func newClarificationFixture() *clarificationFixture {
	f := &clarificationFixture{
		repo:         newFakeClarificationRepo(),
		requirements: newFakeRequirementRepo(),
		versions:     newFakeVersionRepo(),
		agents:       newFakeAgentSessions(),
		events:       &fakeEvents{},
		ids:          &fixedIDs{next: 5000},
	}
	f.service = application.NewClarificationService(
		f.repo, f.requirements, f.versions, f.agents, f.events,
		application.WithClarificationIDGenerator(f.ids),
	)
	return f
}

// seedVersion stores a version with the given status.
func (f *clarificationFixture) seedVersion(status string) domain.Version {
	return f.seedVersionWithID(100, status)
}

// seedVersionWithID stores a version with an explicit id and status.
func (f *clarificationFixture) seedVersionWithID(id int64, status string) domain.Version {
	return f.versions.seed(domain.Version{
		ID: id, Name: "Order v2", Status: status, Revision: 1, CreatedBy: 1, UpdatedBy: 1,
	})
}

// seedRequirement stores a requirement in versionID.
func (f *clarificationFixture) seedRequirement(versionID, id int64) domain.Requirement {
	requirement := domain.Requirement{
		ID: id, VersionID: versionID, Title: "Checkout flow", Priority: domain.PriorityShould,
		Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7, CreatedAt: testNow, UpdatedAt: testNow,
	}
	f.requirements.requirements[id] = requirement
	return requirement
}

// startSession starts one session and fails the test on error.
func (f *clarificationFixture) startSession(t *testing.T, versionID int64) *domain.ClarificationSession {
	t.Helper()
	session, err := f.service.StartSession(context.Background(), versionID, 42)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	return session
}

// fakeAgentSessions is an in-memory application.AgentSessions.
type fakeAgentSessions struct {
	created   []fakeAgentCreate
	texts     []fakeAgentText
	partRuns  []fakeAgentParts
	running   map[string]bool
	nextAgent int64
	nextRun   int64
	createErr error
	startErr  error
}

// fakeAgentCreate is one recorded AgentSessions.Create call.
type fakeAgentCreate struct {
	ProfileKey string
	Title      string
	Actor      int64
	Channel    string
	VersionID  string
}

// fakeAgentText is one recorded StartText call.
type fakeAgentText struct {
	AgentSessionID int64
	Text           string
	Actor          int64
}

// fakeAgentParts is one recorded StartParts call.
type fakeAgentParts struct {
	AgentSessionID int64
	Parts          []application.AgentPart
	Actor          int64
}

// newFakeAgentSessions returns an empty fake.
func newFakeAgentSessions() *fakeAgentSessions {
	return &fakeAgentSessions{running: map[string]bool{}, nextAgent: 7000, nextRun: 8000}
}

// Create records the call and returns a new agent session id.
func (f *fakeAgentSessions) Create(_ context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	if f.createErr != nil {
		return 0, f.createErr
	}
	f.created = append(f.created, fakeAgentCreate{
		ProfileKey: profileKey, Title: title, Actor: actor, Channel: channel, VersionID: versionID,
	})
	id := f.nextAgent
	f.nextAgent++
	return id, nil
}

// StartText records the text run and returns a new run id.
func (f *fakeAgentSessions) StartText(_ context.Context, agentSessionID int64, text string, actor int64) (int64, error) {
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.texts = append(f.texts, fakeAgentText{AgentSessionID: agentSessionID, Text: text, Actor: actor})
	id := f.nextRun
	f.nextRun++
	return id, nil
}

// StartParts records the multimodal run and returns a new run id.
func (f *fakeAgentSessions) StartParts(_ context.Context, agentSessionID int64, parts []application.AgentPart, actor int64) (int64, error) {
	if f.startErr != nil {
		return 0, f.startErr
	}
	f.partRuns = append(f.partRuns, fakeAgentParts{AgentSessionID: agentSessionID, Parts: parts, Actor: actor})
	id := f.nextRun
	f.nextRun++
	return id, nil
}

// Messages is unused by the clarification service.
func (f *fakeAgentSessions) Messages(context.Context, int64, int64, int) ([]application.AgentMessage, bool, error) {
	return nil, false, nil
}

// HasRunningByChannel reports the configured busy channels.
func (f *fakeAgentSessions) HasRunningByChannel(_ context.Context, channel string) (bool, error) {
	return f.running[channel], nil
}

// TestClarificationStartSessionSingleActive covers one active session per
// version, the version status gate, the P1 session parameters and the event.
func TestClarificationStartSessionSingleActive(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)

	session := f.startSession(t, version.ID)
	if session.ID != 5000 || session.VersionID != version.ID || session.Status != domain.SessionActive {
		t.Fatalf("session = %+v", session)
	}
	if session.AgentSessionID == nil || *session.AgentSessionID != 7000 {
		t.Fatalf("agentSessionId = %v, want 7000", session.AgentSessionID)
	}
	if session.StartedBy != 42 || session.StartedAt.IsZero() {
		t.Fatalf("session audit fields = %+v", session)
	}
	if len(f.agents.created) != 1 {
		t.Fatalf("agent sessions created = %d, want 1", len(f.agents.created))
	}
	created := f.agents.created[0]
	if created.ProfileKey != "clarification-dialog" || created.Channel != "version:100" ||
		created.VersionID != "100" || created.Actor != 42 {
		t.Fatalf("agent session create = %+v", created)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %v, want [clarification.session_started]", eventTypesOf(f.events.events))
	}
	started := f.events.events[0]
	if started.Type != "clarification.session_started" || started.VersionID != version.ID ||
		started.RefSessionID == nil || *started.RefSessionID != session.ID ||
		started.ActorID != 42 || started.Source != domain.SourceUser {
		t.Fatalf("session started event = %+v", started)
	}

	// A second start while the first is active is a 409 and must not create
	// another P1 session or event.
	_, err := f.service.StartSession(ctx, version.ID, 43)
	assertDomainReason(t, err, domain.KindConflict, "clarification_session_active")
	if len(f.agents.created) != 1 || len(f.events.events) != 1 {
		t.Fatalf("blocked start created agent sessions=%d events=%d", len(f.agents.created), len(f.events.events))
	}

	// Sessions only start while the version is clarifying or designing.
	draft := f.seedVersionWithID(101, domain.VersionDraft)
	_, err = f.service.StartSession(ctx, draft.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "version_state_conflict")

	// A missing version is a 404.
	_, err = f.service.StartSession(ctx, 999999, 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
}

// TestClarificationStartSessionRepositoryFailureLeavesNoActiveSessionOrEvent
// proves a failed P3 insert publishes no event, stores no active row and
// surfaces the repository error.
func TestClarificationStartSessionRepositoryFailureLeavesNoActiveSessionOrEvent(t *testing.T) {
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	f.repo.createSessionErr = errors.New("insert failed")

	_, err := f.service.StartSession(context.Background(), version.ID, 42)
	if err == nil || !strings.Contains(err.Error(), "insert failed") {
		t.Fatalf("StartSession error = %v, want the repository failure", err)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("failed start published events: %v", eventTypesOf(f.events.events))
	}
	active, err := f.repo.GetActiveSession(context.Background(), version.ID)
	if err != nil || active != nil {
		t.Fatalf("failed start left an active session: %+v/%v", active, err)
	}
}

// TestClarificationCompleteSessionBlockedByRunningAgent covers the completion
// gate and the active -> completed transition.
func TestClarificationCompleteSessionBlockedByRunningAgent(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	session := f.startSession(t, version.ID)
	f.events.events = nil
	f.agents.running["version:100"] = true

	_, err := f.service.CompleteSession(ctx, version.ID, session.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "agent_busy")
	active, err := f.repo.GetActiveSession(ctx, version.ID)
	if err != nil || active == nil || active.Status != domain.SessionActive {
		t.Fatalf("blocked completion changed the session: %+v/%v", active, err)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("blocked completion published events: %v", eventTypesOf(f.events.events))
	}

	f.agents.running["version:100"] = false
	completed, err := f.service.CompleteSession(ctx, version.ID, session.ID, 42)
	if err != nil {
		t.Fatalf("CompleteSession: %v", err)
	}
	if completed.Status != domain.SessionCompleted || completed.EndedAt == nil {
		t.Fatalf("completed session = %+v", completed)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "clarification.session_completed" ||
		f.events.events[0].RefSessionID == nil || *f.events.events[0].RefSessionID != session.ID {
		t.Fatalf("events = %+v", f.events.events)
	}

	// The session is no longer active, and a second completion fails.
	active, err = f.repo.GetActiveSession(ctx, version.ID)
	if err != nil || active != nil {
		t.Fatalf("active session after completion = %+v/%v, want nil", active, err)
	}
	_, err = f.service.CompleteSession(ctx, version.ID, session.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_not_active")

	// Version scoping and missing rows are 404.
	_, err = f.service.CompleteSession(ctx, 999999, session.ID, 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.CompleteSession(ctx, version.ID, 999999, 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")

	// A new session can start after the previous one completed.
	second := f.startSession(t, version.ID)
	if second.ID == session.ID || second.Status != domain.SessionActive {
		t.Fatalf("second session = %+v", second)
	}
}

// TestClarificationSendMessageRequiresActiveAndIdle covers send-message gating
// and the text run start.
func TestClarificationSendMessageRequiresActiveAndIdle(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)

	// No active session at all.
	_, err := f.service.SendMessage(ctx, version.ID, "hello", 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_not_active")

	session := f.startSession(t, version.ID)
	f.events.events = nil

	// A busy channel blocks the message.
	f.agents.running["version:100"] = true
	_, err = f.service.SendMessage(ctx, version.ID, "hello", 42)
	assertDomainReason(t, err, domain.KindStateConflict, "agent_busy")
	if len(f.agents.texts) != 0 {
		t.Fatalf("blocked message started runs: %+v", f.agents.texts)
	}
	f.agents.running["version:100"] = false

	// The message text is bounded and non-empty.
	for _, message := range []string{"", "   ", strings.Repeat("m", application.MaxClarificationMessageChars+1)} {
		_, err = f.service.SendMessage(ctx, version.ID, message, 42)
		assertDomainReason(t, err, domain.KindInvalid, "invalid_message")
	}
	if len(f.agents.texts) != 0 {
		t.Fatalf("invalid messages started runs: %+v", f.agents.texts)
	}

	result, err := f.service.SendMessage(ctx, version.ID, "Which coupons apply?", 42)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != session.ID || result.AgentSessionID != *session.AgentSessionID ||
		result.RunID != 8000 || result.Status != "running" {
		t.Fatalf("start run result = %+v", result)
	}
	if len(f.agents.texts) != 1 {
		t.Fatalf("text runs = %d, want 1", len(f.agents.texts))
	}
	run := f.agents.texts[0]
	if run.AgentSessionID != *session.AgentSessionID || run.Text != "Which coupons apply?" || run.Actor != 42 {
		t.Fatalf("text run = %+v", run)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("message send published a version event: %v", eventTypesOf(f.events.events))
	}

	// A completed session is no longer active.
	if _, err := f.service.CompleteSession(ctx, version.ID, session.ID, 42); err != nil {
		t.Fatalf("CompleteSession: %v", err)
	}
	_, err = f.service.SendMessage(ctx, version.ID, "again", 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_not_active")

	// An active session without a P1 agent session cannot start a run.
	f.agents.running["version:100"] = false
	f.repo.sessions[6000] = domain.ClarificationSession{
		ID: 6000, VersionID: version.ID, Status: domain.SessionActive, StartedBy: 42, StartedAt: testNow,
	}
	_, err = f.service.SendMessage(ctx, version.ID, "hello", 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_not_active")

	// A missing version is a 404.
	_, err = f.service.SendMessage(ctx, 999999, "hello", 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
}

// TestClarificationAnalyzeReconRequireActiveSession covers the one-shot analyze
// and recon runs and their gates.
func TestClarificationAnalyzeReconRequireActiveSession(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)

	// Analyze/recon require an active clarification session.
	_, err := f.service.StartAnalysis(ctx, version.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_required")
	_, err = f.service.StartRecon(ctx, version.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_session_required")
	if len(f.agents.created) != 0 {
		t.Fatalf("blocked analyze/recon created agent sessions: %+v", f.agents.created)
	}

	session := f.startSession(t, version.ID)
	f.agents.created = nil
	f.events.events = nil

	// A busy channel blocks both one-shot runs.
	f.agents.running["version:100"] = true
	_, err = f.service.StartAnalysis(ctx, version.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "agent_busy")
	_, err = f.service.StartRecon(ctx, version.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "agent_busy")
	if len(f.agents.created) != 0 {
		t.Fatalf("blocked analyze/recon created agent sessions: %+v", f.agents.created)
	}
	f.agents.running["version:100"] = false

	analysis, err := f.service.StartAnalysis(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartAnalysis: %v", err)
	}
	if analysis.SessionID != session.ID || analysis.AgentSessionID != 7001 || analysis.RunID != 8000 ||
		analysis.Status != "running" {
		t.Fatalf("analysis result = %+v", analysis)
	}
	if len(f.agents.created) != 1 || f.agents.created[0].ProfileKey != "requirement-analysis" ||
		f.agents.created[0].Channel != "version:100" || f.agents.created[0].VersionID != "100" {
		t.Fatalf("analysis agent session = %+v", f.agents.created)
	}
	if len(f.agents.texts) != 1 || f.agents.texts[0].AgentSessionID != analysis.AgentSessionID ||
		!strings.Contains(f.agents.texts[0].Text, "100") {
		t.Fatalf("analysis instruction = %+v", f.agents.texts)
	}

	recon, err := f.service.StartRecon(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartRecon: %v", err)
	}
	if recon.SessionID != session.ID || recon.AgentSessionID != 7002 || recon.RunID != 8001 ||
		recon.Status != "running" {
		t.Fatalf("recon result = %+v", recon)
	}
	if len(f.agents.created) != 2 || f.agents.created[1].ProfileKey != "context-recon" {
		t.Fatalf("recon agent session = %+v", f.agents.created)
	}
	if len(f.agents.partRuns) != 1 || f.agents.partRuns[0].AgentSessionID != recon.AgentSessionID {
		t.Fatalf("recon parts run = %+v", f.agents.partRuns)
	}
	if len(f.agents.partRuns[0].Parts) == 0 || f.agents.partRuns[0].Parts[0].Type != "text" ||
		!strings.Contains(f.agents.partRuns[0].Parts[0].Text, "100") {
		t.Fatalf("recon parts = %+v", f.agents.partRuns[0].Parts)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("analyze/recon published version events: %v", eventTypesOf(f.events.events))
	}

	// A missing version is a 404.
	_, err = f.service.StartAnalysis(ctx, 999999, 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.StartRecon(ctx, 999999, 42)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
}

// TestClarificationStartReconWithParts covers the multimodal recon entry point:
// the caller's attachment parts follow the instruction text part, and StartRecon
// keeps delegating with no extra parts.
func TestClarificationStartReconWithParts(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	session := f.startSession(t, version.ID)
	f.agents.created = nil
	f.events.events = nil

	extra := []application.AgentPart{
		{Type: "text", Text: "Attachment manifest: spec.md"},
		{Type: "image", ImageRef: "attachment:501"},
	}
	result, err := f.service.StartReconWithParts(ctx, version.ID, extra, 42)
	if err != nil {
		t.Fatalf("StartReconWithParts: %v", err)
	}
	if result.SessionID != session.ID || result.AgentSessionID != 7001 ||
		result.RunID != 8000 || result.Status != "running" {
		t.Fatalf("recon result = %+v", result)
	}
	if len(f.agents.created) != 1 || f.agents.created[0].ProfileKey != "context-recon" {
		t.Fatalf("recon agent session = %+v", f.agents.created)
	}
	if len(f.agents.partRuns) != 1 || f.agents.partRuns[0].AgentSessionID != result.AgentSessionID {
		t.Fatalf("recon parts run = %+v", f.agents.partRuns)
	}
	parts := f.agents.partRuns[0].Parts
	if len(parts) != 1+len(extra) {
		t.Fatalf("recon parts = %+v, want the instruction plus %d extra parts", parts, len(extra))
	}
	if parts[0].Type != "text" || !strings.Contains(parts[0].Text, "100") {
		t.Fatalf("instruction must stay the first text part: %+v", parts)
	}
	for i, want := range extra {
		if parts[i+1] != want {
			t.Fatalf("extra part %d = %+v, want %+v", i, parts[i+1], want)
		}
	}
	if len(f.events.events) != 0 {
		t.Fatalf("recon published version events: %v", eventTypesOf(f.events.events))
	}

	// StartRecon delegates with no extra parts: the instruction is the only part.
	if _, err := f.service.StartRecon(ctx, version.ID, 42); err != nil {
		t.Fatalf("StartRecon: %v", err)
	}
	if len(f.agents.partRuns) != 2 || len(f.agents.partRuns[1].Parts) != 1 ||
		f.agents.partRuns[1].Parts[0].Type != "text" {
		t.Fatalf("plain recon parts = %+v", f.agents.partRuns)
	}
}

// TestClarificationQuestionTransitions covers create/update, the idempotent
// upsert, answer/dismiss/reopen transitions and the state conflicts.
func TestClarificationQuestionTransitions(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, 300)
	session := f.startSession(t, version.ID)
	f.events.events = nil

	created, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion create: %v", err)
	}
	if created.ID != 5001 || created.VersionID != version.ID || created.Status != domain.QuestionOpen ||
		created.Source != domain.SourceAI || created.AnsweredVia != domain.AnsweredViaUI ||
		created.CreatedBy != 42 || created.RequirementID == nil || *created.RequirementID != requirement.ID {
		t.Fatalf("created question = %+v", created)
	}
	if created.SessionID == nil || *created.SessionID != session.ID {
		t.Fatalf("AI question session link = %v, want %d", created.SessionID, session.ID)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "clarification.question_updated" {
		t.Fatalf("events = %v", eventTypesOf(f.events.events))
	}

	// The same requirement + text open question is returned idempotently.
	duplicate, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 43, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion duplicate: %v", err)
	}
	if duplicate.ID != created.ID {
		t.Fatalf("duplicate question id = %d, want %d", duplicate.ID, created.ID)
	}
	if len(f.repo.questions) != 1 || len(f.events.events) != 1 {
		t.Fatalf("idempotent upsert wrote again: questions=%d events=%d", len(f.repo.questions), len(f.events.events))
	}

	// Updating an open question text is allowed.
	updated, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &created.ID, Question: "Which coupons apply at checkout?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion update: %v", err)
	}
	if updated.ID != created.ID || updated.Question != "Which coupons apply at checkout?" ||
		updated.RequirementID == nil || *updated.RequirementID != requirement.ID {
		t.Fatalf("updated question = %+v", updated)
	}

	// An answer is required and bounded.
	_, err = f.service.AnswerQuestion(ctx, version.ID, created.ID, "", 42, domain.AnsweredViaUI)
	assertDomainReason(t, err, domain.KindInvalid, "invalid_answer")
	_, err = f.service.AnswerQuestion(ctx, version.ID, created.ID, "   ", 42, domain.AnsweredViaUI)
	assertDomainReason(t, err, domain.KindInvalid, "invalid_answer")

	answered, err := f.service.AnswerQuestion(ctx, version.ID, created.ID, "All coupons", 42, domain.AnsweredViaAgent)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answered.Status != domain.QuestionAnswered || answered.Answer != "All coupons" ||
		answered.AnsweredVia != domain.AnsweredViaAgent || answered.AnsweredBy == nil ||
		*answered.AnsweredBy != 42 || answered.AnsweredAt == nil {
		t.Fatalf("answered question = %+v", answered)
	}

	// Answered and dismissed are terminal for answering.
	_, err = f.service.AnswerQuestion(ctx, version.ID, created.ID, "again", 42, domain.AnsweredViaUI)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")
	_, err = f.service.DismissQuestion(ctx, version.ID, created.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")

	// A human may rewrite the text of an answered question without changing its
	// state or the stored answer; an AI caller may not.
	humanEdit, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &created.ID, Question: "Which coupons apply, precisely?",
	}, 42, domain.SourceUser)
	if err != nil {
		t.Fatalf("UpsertQuestion human edit: %v", err)
	}
	if humanEdit.Status != domain.QuestionAnswered || humanEdit.Answer != "All coupons" ||
		humanEdit.Question != "Which coupons apply, precisely?" {
		t.Fatalf("human-edited answered question = %+v", humanEdit)
	}
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &created.ID, Question: "AI rewrite of an answered question",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")

	// Reopening clears the answer fields.
	reopened, err := f.service.ReopenQuestion(ctx, version.ID, created.ID, 42)
	if err != nil {
		t.Fatalf("ReopenQuestion: %v", err)
	}
	if reopened.Status != domain.QuestionOpen || reopened.Answer != "" ||
		reopened.AnsweredBy != nil || reopened.AnsweredAt != nil {
		t.Fatalf("reopened question = %+v", reopened)
	}
	_, err = f.service.ReopenQuestion(ctx, version.ID, created.ID, 42)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")

	// Dismissing an open question is allowed; it can be reopened afterwards.
	dismissed, err := f.service.DismissQuestion(ctx, version.ID, created.ID, 42)
	if err != nil {
		t.Fatalf("DismissQuestion: %v", err)
	}
	if dismissed.Status != domain.QuestionDismissed {
		t.Fatalf("dismissed question = %+v", dismissed)
	}
	if _, err := f.service.ReopenQuestion(ctx, version.ID, created.ID, 42); err != nil {
		t.Fatalf("ReopenQuestion after dismiss: %v", err)
	}

	// An answered question cannot be updated as an open one.
	answeredAgain, err := f.service.AnswerQuestion(ctx, version.ID, created.ID, "All coupons", 42, domain.AnsweredViaUI)
	if err != nil {
		t.Fatalf("AnswerQuestion reopen round: %v", err)
	}
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &answeredAgain.ID, Question: "Edited answered question",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")

	// Version scoping: a question of another version is a 404 and a missing
	// question is a 404.
	other := f.seedVersionWithID(101, domain.VersionClarifying)
	_, err = f.service.AnswerQuestion(ctx, other.ID, created.ID, "answer", 42, domain.AnsweredViaUI)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: ptr(int64(999999)), Question: "missing",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")

	// A requirement outside the version cannot be linked.
	foreign := f.seedRequirement(other.ID, 301)
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &foreign.ID, Question: "cross-version question",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")

	// Invalid sources and empty questions are validation errors.
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{Question: "   "}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindInvalid, "invalid_question")
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		Question: "valid text",
	}, 42, "robot")
	assertDomainReason(t, err, domain.KindInvalid, "invalid_source")
}

// TestClarificationDecisionAcceptClosesLinkedQuestion covers the proposed ->
// accepted transition, the linked-question answer and the event stream.
func TestClarificationDecisionAcceptClosesLinkedQuestion(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, 300)
	f.startSession(t, version.ID)
	question, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	f.events.events = nil

	proposed, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &question.ID,
		Topic: "Coupons", Decision: "Apply all coupons", Rationale: "user said so",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("CreateDecision: %v", err)
	}
	if proposed.ID <= 0 || proposed.ID == question.ID || proposed.Status != domain.DecisionProposed ||
		proposed.Source != domain.SourceAI || proposed.ProposedBy != 42 || proposed.DecidedBy != nil {
		t.Fatalf("proposed decision = %+v", proposed)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "decision.proposed" {
		t.Fatalf("events = %v", eventTypesOf(f.events.events))
	}
	stored, err := f.repo.GetQuestion(ctx, question.ID)
	if err != nil || stored.Status != domain.QuestionOpen {
		t.Fatalf("proposal already answered the question: %+v/%v", stored, err)
	}
	f.events.events = nil

	accepted, err := f.service.Decide(ctx, version.ID, proposed.ID, true, 43)
	if err != nil {
		t.Fatalf("Decide accept: %v", err)
	}
	if accepted.Status != domain.DecisionAccepted || accepted.DecidedBy == nil ||
		*accepted.DecidedBy != 43 || accepted.DecidedAt == nil {
		t.Fatalf("accepted decision = %+v", accepted)
	}
	stored, err = f.repo.GetQuestion(ctx, question.ID)
	if err != nil {
		t.Fatalf("GetQuestion: %v", err)
	}
	if stored.Status != domain.QuestionAnswered || stored.Answer != "Apply all coupons" ||
		stored.AnsweredVia != domain.AnsweredViaUI || stored.AnsweredBy == nil || *stored.AnsweredBy != 43 ||
		stored.AnsweredAt == nil {
		t.Fatalf("linked question after accept = %+v", stored)
	}
	wantTypes := []string{"decision.decided", "clarification.question_updated"}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypesOf(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want {
			t.Fatalf("event %d = %s, want %s", i, f.events.events[i].Type, want)
		}
	}
	if f.events.events[0].RefDecisionID == nil || *f.events.events[0].RefDecisionID != proposed.ID {
		t.Fatalf("decision event = %+v", f.events.events[0])
	}

	// Accepted is terminal.
	_, err = f.service.Decide(ctx, version.ID, proposed.ID, false, 43)
	assertDomainReason(t, err, domain.KindStateConflict, "decision_state_conflict")

	// Rejecting a proposed decision leaves the linked question open.
	question2, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which tax rate?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion second: %v", err)
	}
	proposal2, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &question2.ID,
		Topic: "Tax", Decision: "Use 20%",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("CreateDecision second: %v", err)
	}
	f.events.events = nil
	rejected, err := f.service.Decide(ctx, version.ID, proposal2.ID, false, 43, "too risky")
	if err != nil {
		t.Fatalf("Decide reject: %v", err)
	}
	if rejected.Status != domain.DecisionRejected || rejected.DecidedBy == nil || *rejected.DecidedBy != 43 {
		t.Fatalf("rejected decision = %+v", rejected)
	}
	if rejected.Rationale != "too risky" {
		t.Fatalf("rejection rationale = %q, want %q", rejected.Rationale, "too risky")
	}
	storedRejection, err := f.repo.GetDecision(ctx, proposal2.ID)
	if err != nil || storedRejection.Rationale != "too risky" {
		t.Fatalf("stored rejection rationale = %+v/%v", storedRejection, err)
	}
	stored, err = f.repo.GetQuestion(ctx, question2.ID)
	if err != nil || stored.Status != domain.QuestionOpen || stored.Answer != "" {
		t.Fatalf("rejection answered the question: %+v/%v", stored, err)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "decision.decided" {
		t.Fatalf("reject events = %v", eventTypesOf(f.events.events))
	}

	// A human-created decision is accepted immediately and closes the linked
	// open question in the same write.
	question3, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which currency?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion third: %v", err)
	}
	human, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &question3.ID,
		Topic: "Currency", Decision: "EUR",
	}, 44, domain.SourceUser)
	if err != nil {
		t.Fatalf("CreateDecision human: %v", err)
	}
	if human.Status != domain.DecisionAccepted || human.Source != domain.SourceUser ||
		human.DecidedBy == nil || *human.DecidedBy != 44 || human.DecidedAt == nil {
		t.Fatalf("human decision = %+v", human)
	}
	stored, err = f.repo.GetQuestion(ctx, question3.ID)
	if err != nil || stored.Status != domain.QuestionAnswered || stored.Answer != "EUR" {
		t.Fatalf("human decision did not answer the question: %+v/%v", stored, err)
	}

	// Cross-version links are 404s.
	other := f.seedVersionWithID(101, domain.VersionClarifying)
	foreignRequirement := f.seedRequirement(other.ID, 301)
	foreignQuestion, err := f.service.UpsertQuestion(ctx, other.ID, application.QuestionInput{
		RequirementID: &foreignRequirement.ID, Question: "foreign?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion foreign: %v", err)
	}
	_, err = f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		QuestionID: &foreignQuestion.ID, Topic: "Cross", Decision: "No",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &foreignRequirement.ID, Topic: "Cross", Decision: "No",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.Decide(ctx, other.ID, proposed.ID, true, 43)
	assertDomainReason(t, err, domain.KindNotFound, "not_found")

	// Validation errors.
	_, err = f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		Topic: "", Decision: "x",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindInvalid, "invalid_topic")
	_, err = f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		Topic: "t", Decision: "   ",
	}, 42, domain.SourceAI)
	assertDomainReason(t, err, domain.KindInvalid, "invalid_decision")
}

// TestClarificationDecisionPhantomQuestionEvent pins the race where the linked
// question changes between the service's pre-read and the decision transaction:
// the repository's open-only answer step is then a no-op, so no phantom
// clarification.question_updated may be published, while the decision event is.
func TestClarificationDecisionPhantomQuestionEvent(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, 300)
	f.startSession(t, version.ID)
	question, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	proposal, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &question.ID,
		Topic: "Coupons", Decision: "Apply all coupons",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("CreateDecision: %v", err)
	}

	// Decide: the linked question is dismissed by a concurrent actor before the
	// repository transaction answers it, so only decision.decided is published.
	f.events.events = nil
	f.repo.linkedAnswerNoop = true
	accepted, err := f.service.Decide(ctx, version.ID, proposal.ID, true, 43)
	if err != nil {
		t.Fatalf("Decide accept: %v", err)
	}
	if accepted.Status != domain.DecisionAccepted {
		t.Fatalf("accepted decision = %+v", accepted)
	}
	if got := eventTypesOf(f.events.events); len(got) != 1 || got[0] != "decision.decided" {
		t.Fatalf("events = %v, want only decision.decided", got)
	}

	// CreateDecision (human source, accepted immediately) follows the same rule.
	question2, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which currency?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion second: %v", err)
	}
	f.events.events = nil
	human, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &question2.ID,
		Topic: "Currency", Decision: "EUR",
	}, 44, domain.SourceUser)
	if err != nil {
		t.Fatalf("CreateDecision human: %v", err)
	}
	if human.Status != domain.DecisionAccepted {
		t.Fatalf("human decision = %+v", human)
	}
	if got := eventTypesOf(f.events.events); len(got) != 1 || got[0] != "decision.decided" {
		t.Fatalf("events = %v, want only decision.decided", got)
	}
}

// TestClarificationVersionNotFound proves every version-scoped write and read
// rejects a missing version before writing anything.
func TestClarificationVersionNotFound(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	if _, err := f.service.StartSession(ctx, 999999, 42); err == nil {
		t.Fatal("StartSession accepted a missing version")
	}
	if _, err := f.service.CompleteSession(ctx, 999999, 1, 42); err == nil {
		t.Fatal("CompleteSession accepted a missing version")
	}
	if _, err := f.service.SendMessage(ctx, 999999, "hi", 42); err == nil {
		t.Fatal("SendMessage accepted a missing version")
	}
	if _, err := f.service.StartAnalysis(ctx, 999999, 42); err == nil {
		t.Fatal("StartAnalysis accepted a missing version")
	}
	if _, err := f.service.StartRecon(ctx, 999999, 42); err == nil {
		t.Fatal("StartRecon accepted a missing version")
	}
	if _, err := f.service.UpsertQuestion(ctx, 999999, application.QuestionInput{Question: "q"}, 42, domain.SourceUser); err == nil {
		t.Fatal("UpsertQuestion accepted a missing version")
	}
	if _, err := f.service.AnswerQuestion(ctx, 999999, 1, "a", 42, domain.AnsweredViaUI); err == nil {
		t.Fatal("AnswerQuestion accepted a missing version")
	}
	if _, err := f.service.CreateDecision(ctx, 999999, application.DecisionInput{Topic: "t", Decision: "d"}, 42, domain.SourceUser); err == nil {
		t.Fatal("CreateDecision accepted a missing version")
	}
	if _, err := f.service.Decide(ctx, 999999, 1, true, 42); err == nil {
		t.Fatal("Decide accepted a missing version")
	}
	var notFound *domain.Error
	_, err := f.service.CurrentSession(ctx, 999999)
	if !errors.As(err, &notFound) || notFound.Kind != domain.KindNotFound {
		t.Fatalf("CurrentSession error = %v, want not_found", err)
	}
	if len(f.repo.sessions)+len(f.repo.questions)+len(f.repo.decisions) != 0 || len(f.events.events) != 0 {
		t.Fatalf("missing-version calls wrote rows or events")
	}
}

// TestClarificationReadsAndCounts covers the read helpers the HTTP layer needs.
func TestClarificationReadsAndCounts(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	requirement := f.seedRequirement(version.ID, 300)

	current, err := f.service.CurrentSession(ctx, version.ID)
	if err != nil || current != nil {
		t.Fatalf("CurrentSession before start = %+v/%v", current, err)
	}
	session := f.startSession(t, version.ID)
	current, err = f.service.CurrentSession(ctx, version.ID)
	if err != nil || current == nil || current.ID != session.ID {
		t.Fatalf("CurrentSession = %+v/%v", current, err)
	}
	if err := f.service.UpdateContextSummary(ctx, version.ID, "the user wants coupons", 42); err != nil {
		t.Fatalf("UpdateContextSummary: %v", err)
	}
	current, err = f.service.CurrentSession(ctx, version.ID)
	if err != nil || current.ContextSummary != "the user wants coupons" || current.SummaryUpdatedAt == nil {
		t.Fatalf("updated context summary = %+v/%v", current, err)
	}

	first, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "first",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion first: %v", err)
	}
	second, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "second",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion second: %v", err)
	}
	third, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		Question: "version level",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion third: %v", err)
	}
	if _, err := f.service.AnswerQuestion(ctx, version.ID, first.ID, "answer", 42, domain.AnsweredViaUI); err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if _, err := f.service.DismissQuestion(ctx, version.ID, second.ID, 42); err != nil {
		t.Fatalf("DismissQuestion: %v", err)
	}

	answered, more, err := f.service.ListQuestions(ctx, version.ID, domain.QuestionAnswered, nil, domain.Page{Limit: 10})
	if err != nil || more || len(answered) != 1 || answered[0].ID != first.ID {
		t.Fatalf("answered questions = %+v more=%v err=%v", answered, more, err)
	}
	byRequirement, _, err := f.service.ListQuestions(ctx, version.ID, "", &requirement.ID, domain.Page{Limit: 10})
	if err != nil || len(byRequirement) != 2 {
		t.Fatalf("requirement questions = %+v err=%v", byRequirement, err)
	}
	paged, more, err := f.service.ListQuestions(ctx, version.ID, "", nil, domain.Page{Limit: 2})
	if err != nil || !more || len(paged) != 2 || paged[0].ID != first.ID || paged[1].ID != second.ID {
		t.Fatalf("question page = %+v more=%v err=%v", paged, more, err)
	}

	proposed, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, Topic: "one", Decision: "a",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("CreateDecision proposed: %v", err)
	}
	if _, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		Topic: "two", Decision: "b",
	}, 42, domain.SourceUser); err != nil {
		t.Fatalf("CreateDecision accepted: %v", err)
	}
	if _, err := f.service.Decide(ctx, version.ID, proposed.ID, false, 43); err != nil {
		t.Fatalf("Decide: %v", err)
	}
	rejected, more, err := f.service.ListDecisions(ctx, version.ID, domain.DecisionRejected, domain.Page{Limit: 10})
	if err != nil || more || len(rejected) != 1 || rejected[0].ID != proposed.ID {
		t.Fatalf("rejected decisions = %+v more=%v err=%v", rejected, more, err)
	}

	counts, err := f.service.Counts(ctx, version.ID)
	if err != nil {
		t.Fatalf("Counts: %v", err)
	}
	if counts.OpenQuestions != 1 || counts.AnsweredQuestions != 1 || counts.DismissedQuestions != 1 {
		t.Fatalf("question counts = %+v", counts)
	}
	if counts.ProposedDecisions != 0 || counts.AcceptedDecisions != 1 || counts.RejectedDecisions != 1 {
		t.Fatalf("decision counts = %+v", counts)
	}
	if third.Status != domain.QuestionOpen {
		t.Fatalf("version-level question = %+v", third)
	}
}

// TestClarificationUpdateContextSummaryLimit enforces the 20 KiB bound of the
// context.summary_update tool (spec §8.3): the exact limit is accepted, one byte
// more is a domain invalid error and writes nothing.
func TestClarificationUpdateContextSummaryLimit(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	session := f.startSession(t, version.ID)
	f.events.events = nil
	writes := f.repo.writes

	over := strings.Repeat("s", application.MaxClarificationContextSummaryBytes+1)
	assertDomainReason(t, f.service.UpdateContextSummary(ctx, version.ID, over, 42),
		domain.KindInvalid, "invalid_context_summary")
	if f.repo.writes != writes {
		t.Fatalf("over-limit summary was written: writes=%d want %d", f.repo.writes, writes)
	}
	if len(f.events.events) != 0 {
		t.Fatalf("over-limit summary published events: %v", eventTypesOf(f.events.events))
	}
	stored, err := f.repo.GetSession(ctx, session.ID)
	if err != nil || stored.ContextSummary != "" {
		t.Fatalf("over-limit summary changed the session: %+v/%v", stored, err)
	}

	atLimit := strings.Repeat("s", application.MaxClarificationContextSummaryBytes)
	if err := f.service.UpdateContextSummary(ctx, version.ID, atLimit, 42); err != nil {
		t.Fatalf("UpdateContextSummary at the limit: %v", err)
	}
	stored, err = f.repo.GetSession(ctx, session.ID)
	if err != nil || stored.ContextSummary != atLimit {
		t.Fatalf("summary at the limit was not stored: %+v/%v", stored, err)
	}
}

// TestClarificationUpdateContextSummaryEvent pins the post-commit
// clarification.context_updated event and its version/tool/actor references.
func TestClarificationUpdateContextSummaryEvent(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	f.startSession(t, version.ID)
	f.events.events = nil

	if err := f.service.UpdateContextSummary(ctx, version.ID, "the user wants coupons", 42); err != nil {
		t.Fatalf("UpdateContextSummary: %v", err)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("events = %v, want [clarification.context_updated]", eventTypesOf(f.events.events))
	}
	event := f.events.events[0]
	if event.Type != "clarification.context_updated" || event.VersionID != version.ID ||
		event.ActorID != 42 || event.Source != domain.SourceAI ||
		event.Summary != "clarification context updated" {
		t.Fatalf("context event = %+v", event)
	}
	if event.Detail["versionId"] != "100" || event.Detail["tool"] != "context.summary_update" ||
		event.Detail["actorId"] != int64(42) {
		t.Fatalf("context event detail = %+v", event.Detail)
	}
}

// TestClarificationPublishFailureIsSwallowed pins the spec §6.10 policy: an
// event publication failure is logged, never returned to the caller.
func TestClarificationPublishFailureIsSwallowed(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture()
	version := f.seedVersion(domain.VersionClarifying)
	f.events.publishErr = errors.New("eventing down")

	session, err := f.service.StartSession(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartSession must succeed despite a publish failure: %v", err)
	}
	if session.Status != domain.SessionActive {
		t.Fatalf("session = %+v", session)
	}
	if len(f.events.events) != 1 {
		t.Fatalf("publish was not attempted: %v", f.events.events)
	}
}

// eventTypesOf projects the event type list for readable failures.
func eventTypesOf(events []application.VersionEvent) []string {
	types := make([]string, len(events))
	for i, event := range events {
		types[i] = event.Type
	}
	return types
}
