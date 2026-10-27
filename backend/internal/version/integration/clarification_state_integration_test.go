//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/EdouardRichard/EzTest/backend/internal/testutil"
	"github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/version/infrastructure/postgres"
)

// integrationAgentSessions is an in-memory P1 AgentSessions adapter. It records
// the profile/channel each P3 run uses and lets the test toggle channel busyness.
type integrationAgentSessions struct {
	created []integrationAgentCreate
	texts   []integrationAgentText
	parts   []integrationAgentParts
	running map[string]bool
	next    int64
	nextRun int64
}

// integrationAgentCreate is one recorded AgentSessions.Create call.
type integrationAgentCreate struct {
	ProfileKey string
	Title      string
	Actor      int64
	Channel    string
	VersionID  string
}

// integrationAgentText is one recorded StartText call.
type integrationAgentText struct {
	AgentSessionID int64
	Text           string
	Actor          int64
}

// integrationAgentParts is one recorded StartParts call.
type integrationAgentParts struct {
	AgentSessionID int64
	Parts          []application.AgentPart
	Actor          int64
}

// newIntegrationAgentSessions returns a fake with deterministic agent/run ids.
func newIntegrationAgentSessions() *integrationAgentSessions {
	return &integrationAgentSessions{running: map[string]bool{}, next: 9500, nextRun: 9600}
}

// Create records the call and returns a fresh P1 agent session id.
func (a *integrationAgentSessions) Create(_ context.Context, profileKey, title string, actor int64, channel, versionID string) (int64, error) {
	a.created = append(a.created, integrationAgentCreate{
		ProfileKey: profileKey, Title: title, Actor: actor, Channel: channel, VersionID: versionID,
	})
	id := a.next
	a.next++
	return id, nil
}

// StartText records the run and returns a fresh run id.
func (a *integrationAgentSessions) StartText(_ context.Context, agentSessionID int64, text string, actor int64) (int64, error) {
	a.texts = append(a.texts, integrationAgentText{AgentSessionID: agentSessionID, Text: text, Actor: actor})
	id := a.nextRun
	a.nextRun++
	return id, nil
}

// StartParts records the run and returns a fresh run id.
func (a *integrationAgentSessions) StartParts(_ context.Context, agentSessionID int64, parts []application.AgentPart, actor int64) (int64, error) {
	a.parts = append(a.parts, integrationAgentParts{AgentSessionID: agentSessionID, Parts: parts, Actor: actor})
	id := a.nextRun
	a.nextRun++
	return id, nil
}

// Messages is unused by the clarification service.
func (a *integrationAgentSessions) Messages(context.Context, int64, int64, int) ([]application.AgentMessage, bool, error) {
	return nil, false, nil
}

// HasRunningByChannel reports the configured busy channels.
func (a *integrationAgentSessions) HasRunningByChannel(_ context.Context, channel string) (bool, error) {
	return a.running[channel], nil
}

// clarificationFixture wires the real clarification service to the real
// PostgreSQL repositories and a fake P1 agent adapter.
type clarificationFixture struct {
	pool         *pgxpool.Pool
	clar         *postgres.ClarificationRepository
	requirements *postgres.RequirementRepository
	versions     *postgres.VersionRepository
	agents       *integrationAgentSessions
	events       *integrationEvents
	ids          *integrationIDs
	service      *application.ClarificationService
}

// newClarificationFixture opens the migrated test database and wires the parts.
func newClarificationFixture(t *testing.T) *clarificationFixture {
	t.Helper()
	pool := testutil.OpenTestDB(t)
	clar := postgres.NewClarificationRepository(pool)
	requirements := postgres.NewRequirementRepository(pool)
	versions := postgres.NewVersionRepository(pool)
	events := &integrationEvents{}
	ids := &integrationIDs{next: 99001}
	agents := newIntegrationAgentSessions()
	f := &clarificationFixture{
		pool: pool, clar: clar, requirements: requirements, versions: versions,
		agents: agents, events: events, ids: ids,
	}
	f.service = application.NewClarificationService(
		clar, requirements, versions, agents, events,
		application.WithClarificationIDGenerator(ids),
	)
	return f
}

// seedVersion stores one version with the given status.
func (f *clarificationFixture) seedVersion(t *testing.T, id int64, status string) domain.Version {
	t.Helper()
	version := domain.Version{
		ID: id, Name: "Order v2", Status: status, Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	if err := f.versions.Create(context.Background(), &version); err != nil {
		t.Fatalf("seed version %s: %v", status, err)
	}
	return version
}

// seedRequirement stores one draft requirement.
func (f *clarificationFixture) seedRequirement(t *testing.T, versionID, id int64) domain.Requirement {
	t.Helper()
	requirement := domain.Requirement{
		ID: id, VersionID: versionID, Title: "Checkout flow", Priority: domain.PriorityShould,
		Status: domain.RequirementDraft, Source: domain.SourceUser, LastUpdateSource: domain.SourceUser,
		Revision: 1, CreatedBy: 7, UpdatedBy: 7,
	}
	if err := f.requirements.Create(context.Background(), &requirement); err != nil {
		t.Fatalf("seed requirement: %v", err)
	}
	return requirement
}

// sessionStatus reads one session row through the repository.
func (f *clarificationFixture) sessionStatus(t *testing.T, id int64) domain.ClarificationSession {
	t.Helper()
	session, err := f.clar.GetSession(context.Background(), id)
	if err != nil {
		t.Fatalf("GetSession %d: %v", id, err)
	}
	return *session
}

// TestClarificationSessionStateLifecycle drives the real schema through session
// start/complete, the agent gates and the one-shot analyze/recon runs.
func TestClarificationSessionStateLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture(t)
	version := f.seedVersion(t, 9001, domain.VersionClarifying)

	// Start creates a real P3 row and a clarification-dialog P1 session on the
	// version channel.
	session, err := f.service.StartSession(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	stored := f.sessionStatus(t, session.ID)
	if stored.VersionID != version.ID || stored.Status != domain.SessionActive ||
		stored.AgentSessionID == nil || stored.StartedBy != 42 || stored.EndedAt != nil {
		t.Fatalf("stored session = %+v", stored)
	}
	if len(f.agents.created) != 1 {
		t.Fatalf("agent sessions = %+v", f.agents.created)
	}
	created := f.agents.created[0]
	if created.ProfileKey != "clarification-dialog" || created.Channel != "version:9001" ||
		created.VersionID != "9001" || created.Actor != 42 {
		t.Fatalf("agent session create = %+v", created)
	}
	if len(f.events.events) != 1 || f.events.events[0].Type != "clarification.session_started" ||
		f.events.events[0].RefSessionID == nil || *f.events.events[0].RefSessionID != session.ID {
		t.Fatalf("events = %+v", f.events.events)
	}

	// The partial unique index and the service both reject a second active.
	_, err = f.service.StartSession(ctx, version.ID, 43)
	assertIntegrationReason(t, err, domain.KindConflict, "clarification_session_active")
	if len(f.agents.created) != 1 {
		t.Fatalf("blocked start created a P1 session: %+v", f.agents.created)
	}

	// A running agent blocks completion and message sending.
	f.agents.running["version:9001"] = true
	_, err = f.service.CompleteSession(ctx, version.ID, session.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "agent_busy")
	_, err = f.service.SendMessage(ctx, version.ID, "hello", 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "agent_busy")
	_, err = f.service.StartAnalysis(ctx, version.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "agent_busy")
	_, err = f.service.StartRecon(ctx, version.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "agent_busy")
	if stored = f.sessionStatus(t, session.ID); stored.Status != domain.SessionActive || stored.EndedAt != nil {
		t.Fatalf("blocked completion changed the row: %+v", stored)
	}
	f.agents.running["version:9001"] = false

	// An idle active session sends a text run on its own P1 session.
	result, err := f.service.SendMessage(ctx, version.ID, "Which coupons apply?", 42)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if result.SessionID != session.ID || result.AgentSessionID != *session.AgentSessionID ||
		result.Status != "running" || result.RunID == 0 {
		t.Fatalf("send result = %+v", result)
	}
	if len(f.agents.texts) != 1 || f.agents.texts[0].AgentSessionID != *session.AgentSessionID ||
		f.agents.texts[0].Text != "Which coupons apply?" {
		t.Fatalf("text runs = %+v", f.agents.texts)
	}

	// Analyze and recon open one-shot P1 sessions on the same version channel.
	analysis, err := f.service.StartAnalysis(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartAnalysis: %v", err)
	}
	if analysis.SessionID != session.ID || len(f.agents.created) != 2 ||
		f.agents.created[1].ProfileKey != "requirement-analysis" ||
		f.agents.created[1].Channel != "version:9001" {
		t.Fatalf("analysis = %+v created=%+v", analysis, f.agents.created)
	}
	if len(f.agents.texts) != 2 || f.agents.texts[1].AgentSessionID != analysis.AgentSessionID ||
		!strings.Contains(f.agents.texts[1].Text, "9001") {
		t.Fatalf("analysis instruction = %+v", f.agents.texts)
	}

	recon, err := f.service.StartRecon(ctx, version.ID, 42)
	if err != nil {
		t.Fatalf("StartRecon: %v", err)
	}
	if recon.SessionID != session.ID || len(f.agents.created) != 3 ||
		f.agents.created[2].ProfileKey != "context-recon" {
		t.Fatalf("recon = %+v created=%+v", recon, f.agents.created)
	}
	if len(f.agents.parts) != 1 || f.agents.parts[0].AgentSessionID != recon.AgentSessionID ||
		len(f.agents.parts[0].Parts) == 0 || f.agents.parts[0].Parts[0].Type != "text" ||
		!strings.Contains(f.agents.parts[0].Parts[0].Text, "9001") {
		t.Fatalf("recon parts = %+v", f.agents.parts)
	}

	// Completion writes the completed row and event; afterwards a new session can
	// start and analyze requires an active one.
	completed, err := f.service.CompleteSession(ctx, version.ID, session.ID, 42)
	if err != nil {
		t.Fatalf("CompleteSession: %v", err)
	}
	if completed.Status != domain.SessionCompleted || completed.EndedAt == nil {
		t.Fatalf("completed session = %+v", completed)
	}
	if stored = f.sessionStatus(t, session.ID); stored.Status != domain.SessionCompleted || stored.EndedAt == nil {
		t.Fatalf("stored completed session = %+v", stored)
	}
	var completedEvents int
	for _, event := range f.events.events {
		if event.Type == "clarification.session_completed" {
			completedEvents++
		}
	}
	if completedEvents != 1 {
		t.Fatalf("completed events = %d (%v)", completedEvents, eventTypes(f.events.events))
	}
	_, err = f.service.StartAnalysis(ctx, version.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "clarification_session_required")
	_, err = f.service.StartRecon(ctx, version.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "clarification_session_required")
	_, err = f.service.SendMessage(ctx, version.ID, "again", 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "clarification_session_not_active")

	second, err := f.service.StartSession(ctx, version.ID, 44)
	if err != nil {
		t.Fatalf("StartSession after completion: %v", err)
	}
	if second.ID == session.ID {
		t.Fatalf("restarted session reused id %d", session.ID)
	}

	// Sessions only start in clarifying or designing versions.
	draft := f.seedVersion(t, 9002, domain.VersionDraft)
	_, err = f.service.StartSession(ctx, draft.ID, 42)
	assertIntegrationReason(t, err, domain.KindStateConflict, "version_state_conflict")
	designing := f.seedVersion(t, 9003, domain.VersionDesigning)
	if _, err := f.service.StartSession(ctx, designing.ID, 42); err != nil {
		t.Fatalf("StartSession designing: %v", err)
	}

	// A missing version is a 404 for every entry point.
	_, err = f.service.StartSession(ctx, 999999, 42)
	assertIntegrationReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.SendMessage(ctx, 999999, "hello", 42)
	assertIntegrationReason(t, err, domain.KindNotFound, "not_found")
}

// TestClarificationQuestionDecisionStateLifecycle drives the real question and
// decision tables: question transitions, decision acceptance closing a linked
// question in the same transaction, and the post-commit event stream.
func TestClarificationQuestionDecisionStateLifecycle(t *testing.T) {
	ctx := context.Background()
	f := newClarificationFixture(t)
	version := f.seedVersion(t, 9001, domain.VersionClarifying)
	requirement := f.seedRequirement(t, version.ID, 9101)
	if _, err := f.service.StartSession(ctx, version.ID, 42); err != nil {
		t.Fatalf("StartSession: %v", err)
	}
	f.events.events = nil

	// Create is idempotent for the same requirement + text open question.
	created, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion: %v", err)
	}
	if created.Status != domain.QuestionOpen || created.Source != domain.SourceAI ||
		created.RequirementID == nil || *created.RequirementID != requirement.ID ||
		created.SessionID == nil {
		t.Fatalf("created question = %+v", created)
	}
	duplicate, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which coupons apply?",
	}, 43, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion duplicate: %v", err)
	}
	if duplicate.ID != created.ID {
		t.Fatalf("duplicate id = %d, want %d", duplicate.ID, created.ID)
	}
	var questionRows int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*)::int FROM clarification_questions WHERE version_id = $1`, version.ID).Scan(&questionRows); err != nil {
		t.Fatalf("count questions: %v", err)
	}
	if questionRows != 1 {
		t.Fatalf("questions = %d, want the idempotent 1", questionRows)
	}

	// Answered requires a non-empty answer.
	_, err = f.service.AnswerQuestion(ctx, version.ID, created.ID, "  ", 42, domain.AnsweredViaUI)
	assertIntegrationReason(t, err, domain.KindInvalid, "invalid_answer")
	answered, err := f.service.AnswerQuestion(ctx, version.ID, created.ID, "All coupons", 42, domain.AnsweredViaAgent)
	if err != nil {
		t.Fatalf("AnswerQuestion: %v", err)
	}
	if answered.Status != domain.QuestionAnswered || answered.AnsweredVia != domain.AnsweredViaAgent ||
		answered.AnsweredBy == nil || *answered.AnsweredBy != 42 || answered.AnsweredAt == nil {
		t.Fatalf("answered question = %+v", answered)
	}

	// A human may fix the text of an answered question without changing its
	// state or answer; an AI caller may not.
	edited, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &created.ID, Question: "Which coupons apply, precisely?",
	}, 42, domain.SourceUser)
	if err != nil {
		t.Fatalf("UpsertQuestion human edit: %v", err)
	}
	if edited.Status != domain.QuestionAnswered || edited.Answer != "All coupons" {
		t.Fatalf("human-edited answered question = %+v", edited)
	}
	_, err = f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		QuestionID: &created.ID, Question: "AI rewrite",
	}, 42, domain.SourceAI)
	assertIntegrationReason(t, err, domain.KindStateConflict, "clarification_question_state_conflict")

	// Reopen clears the answer, dismiss is allowed afterwards.
	reopened, err := f.service.ReopenQuestion(ctx, version.ID, created.ID, 43)
	if err != nil {
		t.Fatalf("ReopenQuestion: %v", err)
	}
	if reopened.Status != domain.QuestionOpen || reopened.Answer != "" ||
		reopened.AnsweredBy != nil || reopened.AnsweredAt != nil {
		t.Fatalf("reopened question = %+v", reopened)
	}
	dismissed, err := f.service.DismissQuestion(ctx, version.ID, created.ID, 43)
	if err != nil {
		t.Fatalf("DismissQuestion: %v", err)
	}
	if dismissed.Status != domain.QuestionDismissed {
		t.Fatalf("dismissed question = %+v", dismissed)
	}
	if _, err := f.service.ReopenQuestion(ctx, version.ID, created.ID, 43); err != nil {
		t.Fatalf("ReopenQuestion after dismiss: %v", err)
	}

	// A proposed decision leaves the question open; accepting it answers the
	// question with the decision in one transaction.
	proposed, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &created.ID,
		Topic: "Coupons", Decision: "Apply all coupons",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("CreateDecision: %v", err)
	}
	if proposed.Status != domain.DecisionProposed || proposed.DecidedBy != nil {
		t.Fatalf("proposed decision = %+v", proposed)
	}
	var storedQuestionStatus string
	if err := f.pool.QueryRow(ctx,
		`SELECT status FROM clarification_questions WHERE id = $1`, created.ID).Scan(&storedQuestionStatus); err != nil {
		t.Fatalf("read question status: %v", err)
	}
	if storedQuestionStatus != domain.QuestionOpen {
		t.Fatalf("proposal changed the question to %s", storedQuestionStatus)
	}

	accepted, err := f.service.Decide(ctx, version.ID, proposed.ID, true, 43)
	if err != nil {
		t.Fatalf("Decide accept: %v", err)
	}
	if accepted.Status != domain.DecisionAccepted || accepted.DecidedBy == nil || *accepted.DecidedBy != 43 {
		t.Fatalf("accepted decision = %+v", accepted)
	}
	var (
		storedAnswer     string
		storedVia        string
		storedBy         *int64
		storedAnsweredAt *string
	)
	if err := f.pool.QueryRow(ctx, `
		SELECT status, answer, answered_via, answered_by, answered_at::text
		FROM clarification_questions WHERE id = $1`, created.ID).
		Scan(&storedQuestionStatus, &storedAnswer, &storedVia, &storedBy, &storedAnsweredAt); err != nil {
		t.Fatalf("read answered question: %v", err)
	}
	if storedQuestionStatus != domain.QuestionAnswered || storedAnswer != "Apply all coupons" ||
		storedVia != domain.AnsweredViaUI || storedBy == nil || *storedBy != 43 || storedAnsweredAt == nil {
		t.Fatalf("linked question = %s/%s/%s/%v/%v", storedQuestionStatus, storedAnswer, storedVia, storedBy, storedAnsweredAt)
	}
	// Accepted is terminal.
	_, err = f.service.Decide(ctx, version.ID, proposed.ID, false, 43)
	assertIntegrationReason(t, err, domain.KindStateConflict, "decision_state_conflict")

	// A human-created accepted decision also answers its linked open question.
	second, err := f.service.UpsertQuestion(ctx, version.ID, application.QuestionInput{
		RequirementID: &requirement.ID, Question: "Which currency?",
	}, 42, domain.SourceAI)
	if err != nil {
		t.Fatalf("UpsertQuestion second: %v", err)
	}
	human, err := f.service.CreateDecision(ctx, version.ID, application.DecisionInput{
		RequirementID: &requirement.ID, QuestionID: &second.ID,
		Topic: "Currency", Decision: "EUR",
	}, 44, domain.SourceUser)
	if err != nil {
		t.Fatalf("CreateDecision human: %v", err)
	}
	if human.Status != domain.DecisionAccepted || human.Source != domain.SourceUser ||
		human.DecidedBy == nil || *human.DecidedBy != 44 {
		t.Fatalf("human decision = %+v", human)
	}
	if err := f.pool.QueryRow(ctx,
		`SELECT status, answer FROM clarification_questions WHERE id = $1`, second.ID).
		Scan(&storedQuestionStatus, &storedAnswer); err != nil {
		t.Fatalf("read human-linked question: %v", err)
	}
	if storedQuestionStatus != domain.QuestionAnswered || storedAnswer != "EUR" {
		t.Fatalf("human-linked question = %s/%s", storedQuestionStatus, storedAnswer)
	}

	// The event stream records question updates and decisions in order.
	wantTypes := []string{
		"clarification.question_updated", // created
		"clarification.question_updated", // answered through the UI
		"clarification.question_updated", // human text fix on the answered question
		"clarification.question_updated", // reopened
		"clarification.question_updated", // dismissed
		"clarification.question_updated", // reopened after dismiss
		"decision.proposed",              // proposed
		"decision.decided",               // accepted
		"clarification.question_updated", // answered by the accept
		"clarification.question_updated", // second question created
		"decision.decided",               // human accepted decision
		"clarification.question_updated", // answered by the human decision
	}
	if len(f.events.events) != len(wantTypes) {
		t.Fatalf("events = %v, want %v", eventTypes(f.events.events), wantTypes)
	}
	for i, want := range wantTypes {
		if f.events.events[i].Type != want {
			t.Fatalf("event %d = %s, want %s", i, f.events.events[i].Type, want)
		}
		if f.events.events[i].VersionID != version.ID {
			t.Fatalf("event %d version = %d, want %d", i, f.events.events[i].VersionID, version.ID)
		}
	}

	// A missing version is a 404 and nothing is written.
	_, err = f.service.UpsertQuestion(ctx, 999999, application.QuestionInput{Question: "missing"}, 42, domain.SourceUser)
	assertIntegrationReason(t, err, domain.KindNotFound, "not_found")
	_, err = f.service.CreateDecision(ctx, 999999, application.DecisionInput{Topic: "t", Decision: "d"}, 42, domain.SourceUser)
	assertIntegrationReason(t, err, domain.KindNotFound, "not_found")
}
