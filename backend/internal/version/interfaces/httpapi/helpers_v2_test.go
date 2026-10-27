package httpapi

import (
	"context"
	"sort"
	"strings"

	eventing "github.com/EdouardRichard/EzTest/backend/internal/eventing/domain"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

// fakeClarificationService is an in-memory ClarificationService. It mirrors the
// documented service contract closely enough for the HTTP layer: session
// uniqueness, the question transitions of spec §6.3, the decision transitions of
// spec §6.6 and injectable failures for the remaining status/reason cases.
type fakeClarificationService struct {
	sessions  map[int64]*versionapp.ClarificationSession
	questions []versionapp.Question
	decisions []versionapp.Decision
	nextID    int64
	counts    versionapp.ClarificationCounts
	result    versionapp.StartRunResult

	sessionErr     error
	completeErr    error
	messageErr     error
	analysisErr    error
	reconErr       error
	currentErr     error
	countsErr      error
	listQuestions  error
	upsertErr      error
	answerErr      error
	dismissErr     error
	reopenErr      error
	listDecisions  error
	createDecision error
	decideErr      error

	calls             []string
	lastActor         int64
	lastVersion       int64
	lastSessionID     int64
	lastMessage       string
	lastReconParts    []versionapp.AgentPart
	lastQuestionInput versionapp.QuestionInput
	lastQuestionSrc   string
	lastQuestionID    int64
	lastAnswer        string
	lastQuestionList  fakeQuestionListCall
	lastDecisionInput versionapp.DecisionInput
	lastDecisionSrc   string
	lastDecisionList  fakeDecisionListCall
	lastDecide        fakeDecideCall
}

// fakeQuestionListCall records one ListQuestions request.
type fakeQuestionListCall struct {
	versionID     int64
	status        string
	requirementID *int64
	page          domain.Page
}

// fakeDecisionListCall records one ListDecisions request.
type fakeDecisionListCall struct {
	versionID int64
	status    string
	page      domain.Page
}

// fakeDecideCall records one Decide request.
type fakeDecideCall struct {
	versionID  int64
	decisionID int64
	accept     bool
	actor      int64
	rationale  []string
}

// newFakeClarificationService returns an empty fake clarification service.
func newFakeClarificationService() *fakeClarificationService {
	return &fakeClarificationService{
		sessions: map[int64]*versionapp.ClarificationSession{},
		result:   versionapp.StartRunResult{AgentSessionID: 7001, RunID: 9001, Status: "running"},
	}
}

// record appends one operation name to the call log.
func (f *fakeClarificationService) record(operation string) {
	f.calls = append(f.calls, operation)
}

// seedSession stores one session fixture for versionID.
func (f *fakeClarificationService) seedSession(session versionapp.ClarificationSession) *versionapp.ClarificationSession {
	if session.Status == "" {
		session.Status = domain.SessionActive
	}
	if session.StartedAt.IsZero() {
		session.StartedAt = testNow
	}
	if session.AgentSessionID == nil {
		agentSessionID := int64(7001)
		session.AgentSessionID = &agentSessionID
	}
	f.sessions[session.VersionID] = &session
	return &session
}

// seedQuestion stores one question fixture.
func (f *fakeClarificationService) seedQuestion(question versionapp.Question) *versionapp.Question {
	f.questions = append(f.questions, question)
	return &f.questions[len(f.questions)-1]
}

// seedDecision stores one decision fixture.
func (f *fakeClarificationService) seedDecision(decision versionapp.Decision) *versionapp.Decision {
	f.decisions = append(f.decisions, decision)
	return &f.decisions[len(f.decisions)-1]
}

// nextFixtureID returns the next created-row id.
func (f *fakeClarificationService) nextFixtureID() int64 {
	if f.nextID == 0 {
		f.nextID = 6001
	}
	id := f.nextID
	f.nextID++
	return id
}

// runResult returns the configured StartRunResult with sensible defaults.
func (f *fakeClarificationService) runResult() versionapp.StartRunResult {
	result := f.result
	if result.AgentSessionID == 0 {
		result.AgentSessionID = 8001
	}
	if result.RunID == 0 {
		result.RunID = 9001
	}
	if result.Status == "" {
		result.Status = "running"
	}
	return result
}

// StartSession implements ClarificationService.
func (f *fakeClarificationService) StartSession(_ context.Context, versionID, actor int64) (*versionapp.ClarificationSession, error) {
	f.record("StartSession")
	f.lastActor = actor
	f.lastVersion = versionID
	if f.sessionErr != nil {
		return nil, f.sessionErr
	}
	if existing, ok := f.sessions[versionID]; ok && existing.Status == domain.SessionActive {
		return nil, domain.NewConflict("clarification_session_active",
			"the version already has an active clarification session")
	}
	id := f.nextFixtureID()
	agentSessionID := id + 1000
	session := &versionapp.ClarificationSession{
		ID:             id,
		VersionID:      versionID,
		Status:         domain.SessionActive,
		AgentSessionID: &agentSessionID,
		StartedBy:      actor,
		StartedAt:      testNow,
	}
	f.sessions[versionID] = session
	return session, nil
}

// CompleteSession implements ClarificationService.
func (f *fakeClarificationService) CompleteSession(_ context.Context, versionID, sessionID, actor int64) (*versionapp.ClarificationSession, error) {
	f.record("CompleteSession")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastSessionID = sessionID
	if f.completeErr != nil {
		return nil, f.completeErr
	}
	session, ok := f.sessions[versionID]
	if !ok || session.ID != sessionID {
		return nil, domain.NewNotFound("clarification session", sessionID)
	}
	if session.Status != domain.SessionActive {
		return nil, domain.NewStateConflict("clarification_session_not_active",
			"the clarification session is not active")
	}
	session.Status = domain.SessionCompleted
	endedAt := testNow
	session.EndedAt = &endedAt
	session.EndedReason = "completed"
	return session, nil
}

// CurrentSession implements ClarificationService. Like the real service it
// returns only an active session; a completed fixture is nil here.
func (f *fakeClarificationService) CurrentSession(_ context.Context, versionID int64) (*versionapp.ClarificationSession, error) {
	f.record("CurrentSession")
	f.lastVersion = versionID
	if f.currentErr != nil {
		return nil, f.currentErr
	}
	session := f.sessions[versionID]
	if session == nil || session.Status != domain.SessionActive {
		return nil, nil
	}
	return session, nil
}

// LatestSession implements ClarificationService: the newest session of the
// version regardless of status, so a completed conversation keeps its history.
func (f *fakeClarificationService) LatestSession(_ context.Context, versionID int64) (*versionapp.ClarificationSession, error) {
	f.record("LatestSession")
	f.lastVersion = versionID
	return f.sessions[versionID], nil
}

// Counts implements ClarificationService.
func (f *fakeClarificationService) Counts(_ context.Context, versionID int64) (versionapp.ClarificationCounts, error) {
	f.record("Counts")
	f.lastVersion = versionID
	if f.countsErr != nil {
		return versionapp.ClarificationCounts{}, f.countsErr
	}
	return f.counts, nil
}

// SendMessage implements ClarificationService.
func (f *fakeClarificationService) SendMessage(_ context.Context, versionID int64, message string, actor int64) (versionapp.StartRunResult, error) {
	f.record("SendMessage")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastMessage = message
	if f.messageErr != nil {
		return versionapp.StartRunResult{}, f.messageErr
	}
	result := f.runResult()
	if result.SessionID == 0 {
		if session, ok := f.sessions[versionID]; ok {
			result.SessionID = session.ID
			if session.AgentSessionID != nil {
				result.AgentSessionID = *session.AgentSessionID
			}
		}
	}
	return result, nil
}

// StartAnalysis implements ClarificationService.
func (f *fakeClarificationService) StartAnalysis(_ context.Context, versionID int64, actor int64) (versionapp.StartRunResult, error) {
	f.record("StartAnalysis")
	f.lastActor = actor
	f.lastVersion = versionID
	if f.analysisErr != nil {
		return versionapp.StartRunResult{}, f.analysisErr
	}
	return f.runResult(), nil
}

// StartReconWithParts implements ClarificationService.
func (f *fakeClarificationService) StartReconWithParts(_ context.Context, versionID int64, extraParts []versionapp.AgentPart, actor int64) (versionapp.StartRunResult, error) {
	f.record("StartReconWithParts")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastReconParts = nil
	if extraParts != nil {
		f.lastReconParts = append([]versionapp.AgentPart(nil), extraParts...)
	}
	if f.reconErr != nil {
		return versionapp.StartRunResult{}, f.reconErr
	}
	return f.runResult(), nil
}

// ListQuestions implements ClarificationService.
func (f *fakeClarificationService) ListQuestions(_ context.Context, versionID int64, status string, requirementID *int64, page domain.Page) ([]versionapp.Question, bool, error) {
	f.record("ListQuestions")
	f.lastQuestionList = fakeQuestionListCall{
		versionID: versionID, status: status, requirementID: requirementID, page: page,
	}
	if f.listQuestions != nil {
		return nil, false, f.listQuestions
	}
	items := make([]versionapp.Question, 0, len(f.questions))
	for _, question := range f.questions {
		if question.VersionID != versionID {
			continue
		}
		if status != "" && question.Status != status {
			continue
		}
		if requirementID != nil &&
			(question.RequirementID == nil || *question.RequirementID != *requirementID) {
			continue
		}
		items = append(items, question)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return pageItems(items, page.Limit)
}

// ListDecisions implements ClarificationService.
func (f *fakeClarificationService) ListDecisions(_ context.Context, versionID int64, status string, page domain.Page) ([]versionapp.Decision, bool, error) {
	f.record("ListDecisions")
	f.lastDecisionList = fakeDecisionListCall{versionID: versionID, status: status, page: page}
	if f.listDecisions != nil {
		return nil, false, f.listDecisions
	}
	items := make([]versionapp.Decision, 0, len(f.decisions))
	for _, decision := range f.decisions {
		if decision.VersionID != versionID {
			continue
		}
		if status != "" && decision.Status != status {
			continue
		}
		items = append(items, decision)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	return pageItems(items, page.Limit)
}

// pageItems applies the limit of one page to an ordered fixture slice.
func pageItems[T any](items []T, limit int) ([]T, bool, error) {
	if limit > 0 && len(items) > limit {
		return items[:limit], true, nil
	}
	return items, false, nil
}

// UpsertQuestion implements ClarificationService.
func (f *fakeClarificationService) UpsertQuestion(_ context.Context, versionID int64, input versionapp.QuestionInput, actor int64, source string) (*versionapp.Question, error) {
	f.record("UpsertQuestion")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastQuestionInput = input
	f.lastQuestionSrc = source
	if f.upsertErr != nil {
		return nil, f.upsertErr
	}
	if input.QuestionID != nil {
		for i := range f.questions {
			if f.questions[i].ID != *input.QuestionID || f.questions[i].VersionID != versionID {
				continue
			}
			f.questions[i].Question = input.Question
			f.questions[i].UpdatedAt = testNow
			updated := f.questions[i]
			return &updated, nil
		}
		return nil, domain.NewNotFound("question", *input.QuestionID)
	}
	question := versionapp.Question{
		ID:            f.nextFixtureID(),
		VersionID:     versionID,
		RequirementID: input.RequirementID,
		SessionID:     input.SessionID,
		Question:      input.Question,
		Status:        domain.QuestionOpen,
		Source:        source,
		AnsweredVia:   domain.AnsweredViaUI,
		CreatedBy:     actor,
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
	}
	f.questions = append(f.questions, question)
	return &question, nil
}

// AnswerQuestion implements ClarificationService.
func (f *fakeClarificationService) AnswerQuestion(_ context.Context, versionID, questionID int64, answer string, actor int64, via string) (*versionapp.Question, error) {
	f.record("AnswerQuestion")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastQuestionID = questionID
	f.lastAnswer = answer
	if f.answerErr != nil {
		return nil, f.answerErr
	}
	question := f.findQuestion(versionID, questionID)
	if question == nil {
		return nil, domain.NewNotFound("question", questionID)
	}
	if question.Status != domain.QuestionOpen {
		return nil, domain.NewStateConflict("clarification_question_state_conflict",
			"the question is not open")
	}
	if strings.TrimSpace(answer) == "" {
		return nil, domain.NewInvalid("answer", "invalid_answer", "answer is required")
	}
	if via == "" {
		via = domain.AnsweredViaUI
	}
	answeredAt := testNow
	question.Status = domain.QuestionAnswered
	question.Answer = answer
	question.AnsweredVia = via
	question.AnsweredBy = &actor
	question.AnsweredAt = &answeredAt
	question.UpdatedAt = testNow
	return question, nil
}

// DismissQuestion implements ClarificationService.
func (f *fakeClarificationService) DismissQuestion(_ context.Context, versionID, questionID int64, actor int64) (*versionapp.Question, error) {
	f.record("DismissQuestion")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastQuestionID = questionID
	if f.dismissErr != nil {
		return nil, f.dismissErr
	}
	question := f.findQuestion(versionID, questionID)
	if question == nil {
		return nil, domain.NewNotFound("question", questionID)
	}
	if question.Status != domain.QuestionOpen {
		return nil, domain.NewStateConflict("clarification_question_state_conflict",
			"the question is not open")
	}
	question.Status = domain.QuestionDismissed
	question.Answer = ""
	question.AnsweredBy = nil
	question.AnsweredAt = nil
	question.UpdatedAt = testNow
	return question, nil
}

// ReopenQuestion implements ClarificationService.
func (f *fakeClarificationService) ReopenQuestion(_ context.Context, versionID, questionID int64, actor int64) (*versionapp.Question, error) {
	f.record("ReopenQuestion")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastQuestionID = questionID
	if f.reopenErr != nil {
		return nil, f.reopenErr
	}
	question := f.findQuestion(versionID, questionID)
	if question == nil {
		return nil, domain.NewNotFound("question", questionID)
	}
	if question.Status != domain.QuestionAnswered && question.Status != domain.QuestionDismissed {
		return nil, domain.NewStateConflict("clarification_question_state_conflict",
			"the question cannot be reopened")
	}
	question.Status = domain.QuestionOpen
	question.Answer = ""
	question.AnsweredBy = nil
	question.AnsweredAt = nil
	question.UpdatedAt = testNow
	return question, nil
}

// findQuestion returns the mutable fixture question, or nil.
func (f *fakeClarificationService) findQuestion(versionID, questionID int64) *versionapp.Question {
	for i := range f.questions {
		if f.questions[i].ID == questionID && f.questions[i].VersionID == versionID {
			return &f.questions[i]
		}
	}
	return nil
}

// CreateDecision implements ClarificationService.
func (f *fakeClarificationService) CreateDecision(_ context.Context, versionID int64, input versionapp.DecisionInput, actor int64, source string) (*versionapp.Decision, error) {
	f.record("CreateDecision")
	f.lastActor = actor
	f.lastVersion = versionID
	f.lastDecisionInput = input
	f.lastDecisionSrc = source
	if f.createDecision != nil {
		return nil, f.createDecision
	}
	decidedAt := testNow
	decision := versionapp.Decision{
		ID:            f.nextFixtureID(),
		VersionID:     versionID,
		RequirementID: input.RequirementID,
		QuestionID:    input.QuestionID,
		Topic:         input.Topic,
		Decision:      input.Decision,
		Rationale:     input.Rationale,
		Status:        domain.DecisionAccepted,
		Source:        source,
		ProposedBy:    actor,
		DecidedBy:     &actor,
		DecidedAt:     &decidedAt,
		CreatedAt:     testNow,
		UpdatedAt:     testNow,
	}
	f.decisions = append(f.decisions, decision)
	return &decision, nil
}

// Decide implements ClarificationService.
func (f *fakeClarificationService) Decide(_ context.Context, versionID, decisionID int64, accept bool, actor int64, rationale ...string) (*versionapp.Decision, error) {
	f.record("Decide")
	f.lastDecide = fakeDecideCall{
		versionID: versionID, decisionID: decisionID, accept: accept,
		actor: actor, rationale: append([]string(nil), rationale...),
	}
	f.lastActor = actor
	if f.decideErr != nil {
		return nil, f.decideErr
	}
	var decision *versionapp.Decision
	for i := range f.decisions {
		if f.decisions[i].ID == decisionID && f.decisions[i].VersionID == versionID {
			decision = &f.decisions[i]
			break
		}
	}
	if decision == nil {
		return nil, domain.NewNotFound("decision", decisionID)
	}
	if decision.Status != domain.DecisionProposed {
		return nil, domain.NewStateConflict("decision_state_conflict",
			"the decision is already terminal")
	}
	decidedAt := testNow
	decision.DecidedBy = &actor
	decision.DecidedAt = &decidedAt
	if len(rationale) > 0 {
		decision.Rationale = rationale[0]
	}
	if accept {
		decision.Status = domain.DecisionAccepted
	} else {
		decision.Status = domain.DecisionRejected
	}
	decision.UpdatedAt = testNow
	return decision, nil
}

// fakeAIReady is the injectable AI runtime readiness probe. A nil err means the
// runtime is available; any err must surface as the retryable 503 ai_unavailable.
type fakeAIReady struct {
	err   error
	calls int
}

// Ready implements the HandlersDeps.AIReady probe.
func (f *fakeAIReady) Ready(context.Context) error {
	f.calls++
	return f.err
}

// fakeClarificationMessages is an in-memory clarification message reader.
type fakeClarificationMessages struct {
	messages           []versionapp.AgentMessage
	hasMore            bool
	err                error
	calls              int
	lastAgentSessionID int64
	lastBeforeID       int64
	lastLimit          int
}

// Messages implements ClarificationMessagesReader.
func (f *fakeClarificationMessages) Messages(_ context.Context, agentSessionID, beforeID int64, limit int) ([]versionapp.AgentMessage, bool, error) {
	f.calls++
	f.lastAgentSessionID = agentSessionID
	f.lastBeforeID = beforeID
	f.lastLimit = limit
	if f.err != nil {
		return nil, false, f.err
	}
	return append([]versionapp.AgentMessage(nil), f.messages...), f.hasMore, nil
}

// fakeEventSnapshotter is an in-memory event snapshot reader.
type fakeEventSnapshotter struct {
	events        []eventing.Event
	cursor        int64
	hasMore       bool
	err           error
	calls         int
	lastChannel   string
	lastBeforeSeq int64
	lastLimit     int
}

// Snapshot implements EventSnapshotter.
func (f *fakeEventSnapshotter) Snapshot(_ context.Context, channel string, beforeSeq int64, limit int) ([]eventing.Event, int64, bool, error) {
	f.calls++
	f.lastChannel = channel
	f.lastBeforeSeq = beforeSeq
	f.lastLimit = limit
	if f.err != nil {
		return nil, 0, false, f.err
	}
	return append([]eventing.Event(nil), f.events...), f.cursor, f.hasMore, nil
}

// fakeEventReader is the SSE history port used by the version stream wrapper.
type fakeEventReader struct {
	events       []eventing.Event
	latest       int64
	listErr      error
	latestErr    error
	channels     []string
	lastAfterSeq int64
	lastLimit    int
}

// ListAfter implements eventinghttp.EventReader.
func (f *fakeEventReader) ListAfter(_ context.Context, channel string, afterSeq int64, limit int) ([]eventing.Event, error) {
	f.channels = append(f.channels, channel)
	f.lastAfterSeq = afterSeq
	f.lastLimit = limit
	if f.listErr != nil {
		return nil, f.listErr
	}
	return append([]eventing.Event(nil), f.events...), nil
}

// LatestSeq implements eventinghttp.EventReader.
func (f *fakeEventReader) LatestSeq(_ context.Context, channel string) (int64, error) {
	f.channels = append(f.channels, channel)
	if f.latestErr != nil {
		return 0, f.latestErr
	}
	return f.latest, nil
}

// fakeEventSubscriber records the channel the version stream subscribes to and
// immediately closes the event channel so the handler returns after the replay.
type fakeEventSubscriber struct {
	channels []string
}

// Subscribe implements eventinghttp.EventSubscriber.
func (f *fakeEventSubscriber) Subscribe(channel string) (<-chan eventing.Event, func()) {
	f.channels = append(f.channels, channel)
	events := make(chan eventing.Event)
	close(events)
	return events, func() {}
}

// containsCall reports whether calls carries operation.
func containsCall(calls []string, operation string) bool {
	for _, call := range calls {
		if call == operation {
			return true
		}
	}
	return false
}
