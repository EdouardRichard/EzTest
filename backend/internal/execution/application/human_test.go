package executionapp

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/application/tools"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// Task 23 structured-question state machine (spec R-B3-1..R-B3-6, B §6).

const (
	questionTestVersion      int64 = 100
	questionTestConversation int64 = 2001
	questionTestRun          int64 = 9901
	questionTestActor        int64 = 7
)

func questionOptions() []execution.HumanQuestionOption {
	return []execution.HumanQuestionOption{
		{ID: "a", Label: "T+1"},
		{ID: "b", Label: "T+3"},
	}
}

func questionBatchOf(n int) []execution.HumanQuestion {
	ids := []string{"q1", "q2", "q3", "q4", "q5"}
	questions := make([]execution.HumanQuestion, 0, n)
	for index := 0; index < n; index++ {
		questions = append(questions, execution.HumanQuestion{
			ID:            ids[index],
			Text:          "问题 " + ids[index],
			Options:       questionOptions(),
			Multiple:      index == 1,
			AllowFreeText: true,
			Required:      index == 0,
			SourceRefs:    []string{"requirement:FR-012"},
		})
	}
	return questions
}

// fakeQuestionMessages is the conversation write port fake. It proves the
// service writes kind=question on creation and kind=answer on answer, and that
// a retried answer never appends a second message.
type fakeQuestionMessages struct {
	mu     sync.Mutex
	nextID int64
	byKey  map[string]int64
	rows   []QuestionMessage
}

func newFakeQuestionMessages() *fakeQuestionMessages {
	return &fakeQuestionMessages{byKey: map[string]int64{}}
}

func (f *fakeQuestionMessages) AppendQuestionMessage(_ context.Context, message QuestionMessage) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if message.ClientMessageID != "" {
		if id, ok := f.byKey[message.ClientMessageID]; ok {
			return id, false, nil
		}
	}
	f.nextID++
	id := 8100 + f.nextID
	if message.ClientMessageID != "" {
		f.byKey[message.ClientMessageID] = id
	}
	f.rows = append(f.rows, message)
	return id, true, nil
}

func (f *fakeQuestionMessages) countKind(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	count := 0
	for _, row := range f.rows {
		if row.Kind == kind {
			count++
		}
	}
	return count
}

func (f *fakeQuestionMessages) last() (QuestionMessage, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.rows) == 0 {
		return QuestionMessage{}, false
	}
	return f.rows[len(f.rows)-1], true
}

type questionFixture struct {
	humans   *fakeHumanStore
	messages *fakeQuestionMessages
	events   *corePublisher
	audit    *coreAudit
	clock    *coreClock
	ids      *coreIDs
	service  *QuestionService
}

func newQuestionFixture() *questionFixture {
	now := time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)
	f := &questionFixture{
		humans:   newFakeHumanStore(),
		messages: newFakeQuestionMessages(),
		events:   &corePublisher{},
		audit:    &coreAudit{},
		clock:    &coreClock{now: now},
		ids:      &coreIDs{next: 5000},
	}
	f.service = NewQuestionService(QuestionServiceDeps{
		Humans:   f.humans,
		Messages: f.messages,
		Settings: &fakeSettings{values: map[string]any{"question.timeout_seconds": int64(86400)}},
		IDs:      f.ids,
		Clock:    f.clock,
		Events:   f.events,
		Audit:    f.audit,
	})
	return f
}

func (f *questionFixture) create(t *testing.T, questions []execution.HumanQuestion) execution.HumanRequest {
	t.Helper()
	request, err := f.service.Create(context.Background(), QuestionCreateInput{
		VersionID:      questionTestVersion,
		ConversationID: questionTestConversation,
		AgentRunID:     questionTestRun,
		Questions:      questions,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return request
}

// StageBlocked reports whether a question write paused a case_run/wave or
// touched the stage machine. Structured questions must never block stage
// progression (R-B3-4/C-2), so this must stay false through timeout.
func (f *questionFixture) StageBlocked() bool {
	for _, eventType := range f.events.types() {
		if eventType == EventCaseRunPaused || eventType == EventWavePaused || strings.Contains(eventType, "stage") {
			return true
		}
	}
	return false
}

func TestCreateQuestionPendingWithMessage(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(3))

	if request.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending", request.Status)
	}
	if request.Kind != execution.HumanClarificationQuestion {
		t.Fatalf("kind = %s, want clarification_question", request.Kind)
	}
	if request.ConversationID == nil || *request.ConversationID != questionTestConversation {
		t.Fatalf("conversation_id = %v, want %d", request.ConversationID, questionTestConversation)
	}
	if request.MessageID == nil || *request.MessageID == 0 {
		t.Fatalf("message_id = %v, want the question card message", request.MessageID)
	}
	if request.Question.BatchID == "" || len(request.Question.Questions) != 3 {
		t.Fatalf("question = %+v, want 3 questions with batch id", request.Question)
	}
	if ttl := request.ExpiresAt.Sub(f.clock.now); ttl != 24*time.Hour {
		t.Fatalf("expires_at ttl = %s, want the 86400s question.timeout_seconds default", ttl)
	}
	if request.BlocksStage() {
		t.Fatal("a pending question must never block stage progression")
	}
	if got := f.messages.countKind(QuestionMessageKindQuestion); got != 1 {
		t.Fatalf("question messages = %d, want 1", got)
	}
	message, _ := f.messages.last()
	if message.VersionID != questionTestVersion || message.ConversationID != questionTestConversation || message.QuestionID != request.ID {
		t.Fatalf("message = %+v, want the version/conversation/question binding", message)
	}
	if f.events.count(EventQuestionCreated) != 1 {
		t.Fatalf("question.created events = %d, want 1", f.events.count(EventQuestionCreated))
	}
	if len(f.audit.actions) == 0 || f.audit.actions[0] != "question.created" {
		t.Fatalf("audit = %v, want question.created", f.audit.actions)
	}
}

func TestAnswerWritesConversationMessage(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(3))

	answered, err := f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{
			{QuestionID: "q1", Selected: []string{"a"}},
			{QuestionID: "q2", Selected: []string{"a", "b"}},
			{QuestionID: "q3", Text: "需要财务确认"},
		})
	if err != nil {
		t.Fatalf("Answer: %v", err)
	}
	if answered.Status != execution.HumanAnswered {
		t.Fatalf("status = %s, want answered", answered.Status)
	}
	if answered.ResolvedBy == nil || *answered.ResolvedBy != questionTestActor {
		t.Fatalf("resolved_by = %v, want %d", answered.ResolvedBy, questionTestActor)
	}
	if answered.ResolvedAt == nil {
		t.Fatal("resolved_at is nil")
	}
	if got := f.messages.countKind(QuestionMessageKindAnswer); got != 1 {
		t.Fatalf("answer messages = %d, want exactly 1 for the whole batch", got)
	}
	message, _ := f.messages.last()
	if message.Kind != QuestionMessageKindAnswer || message.QuestionID != request.ID {
		t.Fatalf("answer message = %+v", message)
	}
	if f.events.count(EventQuestionAnswered) != 1 {
		t.Fatalf("question.answered events = %d, want 1", f.events.count(EventQuestionAnswered))
	}

	// The response keeps selected/text/answered_by/answered_at (R-B3-3).
	answers, ok := answered.Response[execution.HumanResponseAnswers].([]any)
	if !ok || len(answers) != 3 {
		t.Fatalf("response.answers = %#v, want 3 entries", answered.Response[execution.HumanResponseAnswers])
	}
	first, ok := answers[0].(map[string]any)
	if !ok || first["question_id"] != "q1" || first["text"] != "" {
		t.Fatalf("first answer = %#v", answers[0])
	}
	selected, ok := first["selected"].([]any)
	if !ok || len(selected) != 1 || selected[0] != "a" {
		t.Fatalf("first selected = %#v", first["selected"])
	}
	if answered.Response[execution.HumanResponseAnsweredBy] != "7" {
		t.Fatalf("answered_by = %#v, want \"7\"", answered.Response[execution.HumanResponseAnsweredBy])
	}
	if answered.Response[execution.HumanResponseAnsweredAt] == "" {
		t.Fatal("answered_at is empty")
	}
}

func TestAnswerIsIdempotent(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(1))
	answers := []execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"a"}}}

	first, err := f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor, answers)
	if err != nil {
		t.Fatalf("first Answer: %v", err)
	}
	second, err := f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor, answers)
	if err != nil {
		t.Fatalf("retried Answer: %v", err)
	}
	if second.Status != execution.HumanAnswered {
		t.Fatalf("retried status = %s, want answered", second.Status)
	}
	if got := f.messages.countKind(QuestionMessageKindAnswer); got != 1 {
		t.Fatalf("answer messages after retry = %d, want 1", got)
	}
	if got := f.events.count(EventQuestionAnswered); got != 1 {
		t.Fatalf("question.answered events after retry = %d, want 1", got)
	}
	if first.Response[execution.HumanResponseAnsweredAt] != second.Response[execution.HumanResponseAnsweredAt] {
		t.Fatal("idempotent retry rewrote answered_at")
	}

	_, err = f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"b"}}})
	requireAPIError(t, err, "state_conflict")
}

func TestSkipCancelsQuestionAndIsIdempotent(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(1))

	skipped, err := f.service.Skip(context.Background(), questionTestVersion, request.ID, questionTestActor, "暂不确认")
	if err != nil {
		t.Fatalf("Skip: %v", err)
	}
	if skipped.Status != execution.HumanCancelled {
		t.Fatalf("status = %s, want cancelled", skipped.Status)
	}
	if skipped.Response[execution.HumanResponseSkipped] != true {
		t.Fatalf("skipped = %#v, want true", skipped.Response[execution.HumanResponseSkipped])
	}
	if got := f.messages.countKind(QuestionMessageKindAnswer); got != 0 {
		t.Fatalf("answer messages after skip = %d, want 0", got)
	}
	again, err := f.service.Skip(context.Background(), questionTestVersion, request.ID, questionTestActor, "暂不确认")
	if err != nil {
		t.Fatalf("retried Skip: %v", err)
	}
	if again.Status != execution.HumanCancelled {
		t.Fatalf("retried status = %s, want cancelled", again.Status)
	}
	_, err = f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"a"}}})
	requireAPIError(t, err, "state_conflict")
}

func TestReopenReturnsToPendingForOwnerOnly(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(1))
	if _, err := f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"a"}}}); err != nil {
		t.Fatalf("Answer: %v", err)
	}

	_, err := f.service.Reopen(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun + 1})
	requireAPIError(t, err, "forbidden")

	reopened, err := f.service.Reopen(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun})
	if err != nil {
		t.Fatalf("Reopen: %v", err)
	}
	if reopened.Status != execution.HumanPending {
		t.Fatalf("status = %s, want pending", reopened.Status)
	}
	idempotent, err := f.service.Reopen(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun})
	if err != nil {
		t.Fatalf("idempotent Reopen: %v", err)
	}
	if idempotent.Status != execution.HumanPending {
		t.Fatalf("idempotent status = %s, want pending", idempotent.Status)
	}

	// A reopened question can be answered again and appends a new answer message.
	if _, err := f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"b"}}}); err != nil {
		t.Fatalf("re-Answer: %v", err)
	}
	if got := f.messages.countKind(QuestionMessageKindAnswer); got != 2 {
		t.Fatalf("answer messages after reopen = %d, want 2", got)
	}
}

func TestOnlyOwningHostRunCanCreateCancel(t *testing.T) {
	f := newQuestionFixture()
	_, err := f.service.Create(context.Background(), QuestionCreateInput{
		VersionID: questionTestVersion, ConversationID: questionTestConversation, AgentRunID: 0,
		Questions: questionBatchOf(1),
	})
	requireAPIError(t, err, "invalid_argument")

	request := f.create(t, questionBatchOf(1))
	err = f.service.Cancel(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun + 1})
	requireAPIError(t, err, "forbidden")
	if got, _ := f.humans.Get(context.Background(), request.ID); got.Status != execution.HumanPending {
		t.Fatalf("foreign cancel changed status to %s", got.Status)
	}

	if err := f.service.Cancel(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun}); err != nil {
		t.Fatalf("owner Cancel: %v", err)
	}
	got, err := f.service.Get(context.Background(), questionTestVersion, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != execution.HumanCancelled {
		t.Fatalf("status = %s, want cancelled", got.Status)
	}
	if err := f.service.Cancel(context.Background(), questionTestVersion, request.ID, QuestionOwner{AgentRunID: questionTestRun}); err != nil {
		t.Fatalf("idempotent Cancel: %v", err)
	}
}

func TestQuestionPayloadValidation(t *testing.T) {
	six := append(questionBatchOf(5), execution.HumanQuestion{ID: "q6", Text: "额外问题", Options: questionOptions(), AllowFreeText: true})
	duplicate := questionBatchOf(2)
	duplicate[1].ID = duplicate[0].ID
	emptyText := questionBatchOf(1)
	emptyText[0].Text = "  "
	noOptions := questionBatchOf(1)
	noOptions[0].Options = nil
	noOptions[0].AllowFreeText = false
	noOptions[0].Required = true
	badOption := questionBatchOf(1)
	badOption[0].Options = []execution.HumanQuestionOption{{ID: "a", Label: ""}}
	duplicateOption := questionBatchOf(1)
	duplicateOption[0].Options = []execution.HumanQuestionOption{{ID: "a", Label: "A"}, {ID: "a", Label: "B"}}

	cases := map[string][]execution.HumanQuestion{
		"zero":             {},
		"six":              six,
		"duplicate ids":    duplicate,
		"empty text":       emptyText,
		"unanswerable":     noOptions,
		"empty label":      badOption,
		"duplicate option": duplicateOption,
	}
	for name, questions := range cases {
		t.Run(name, func(t *testing.T) {
			f := newQuestionFixture()
			_, err := f.service.Create(context.Background(), QuestionCreateInput{
				VersionID: questionTestVersion, ConversationID: questionTestConversation,
				AgentRunID: questionTestRun, Questions: questions,
			})
			requireAPIError(t, err, "invalid_argument")
			if got := f.messages.countKind(QuestionMessageKindQuestion); got != 0 {
				t.Fatalf("invalid payload wrote %d messages", got)
			}
		})
	}
}

func TestTimeoutExpiresWithoutBlocking(t *testing.T) {
	f := newQuestionFixture()
	request := f.create(t, questionBatchOf(1))

	f.clock.now = f.clock.now.Add(25 * time.Hour)
	got, err := f.service.Get(context.Background(), questionTestVersion, request.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Status != execution.HumanExpired {
		t.Fatalf("status = %s, want expired", got.Status)
	}
	if f.events.count(EventQuestionExpired) != 1 {
		t.Fatalf("question.expired events = %d, want 1", f.events.count(EventQuestionExpired))
	}
	if got.BlocksStage() {
		t.Fatal("expired question reports BlocksStage() = true")
	}
	if f.StageBlocked() {
		t.Fatal("question timeout paused a case run/wave; questions must never block stage progression")
	}

	// An answer after expiry is a state conflict and writes nothing.
	_, err = f.service.Answer(context.Background(), questionTestVersion, request.ID, questionTestActor,
		[]execution.HumanQuestionAnswer{{QuestionID: "q1", Selected: []string{"a"}}})
	requireAPIError(t, err, "state_conflict")
	if got := f.messages.countKind(QuestionMessageKindAnswer); got != 0 {
		t.Fatalf("answer message written after expiry: %d", got)
	}

	// A second read does not duplicate the expiry event.
	if _, err := f.service.Get(context.Background(), questionTestVersion, request.ID); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if f.events.count(EventQuestionExpired) != 1 {
		t.Fatalf("duplicate question.expired events = %d", f.events.count(EventQuestionExpired))
	}
}

func TestListQuestionsExpiresOverdueAndFilters(t *testing.T) {
	f := newQuestionFixture()
	first := f.create(t, questionBatchOf(1))

	f.clock.now = f.clock.now.Add(25 * time.Hour)
	second := f.create(t, questionBatchOf(1))

	expired, _, err := f.service.List(context.Background(), questionTestVersion, string(execution.HumanExpired), 50, nil)
	if err != nil {
		t.Fatalf("List expired: %v", err)
	}
	if len(expired) != 1 || expired[0].ID != first.ID {
		t.Fatalf("expired = %+v, want only %d", expired, first.ID)
	}
	pending, _, err := f.service.List(context.Background(), questionTestVersion, string(execution.HumanPending), 50, nil)
	if err != nil {
		t.Fatalf("List pending: %v", err)
	}
	if len(pending) != 1 || pending[0].ID != second.ID {
		t.Fatalf("pending = %+v, want only %d", pending, second.ID)
	}
	all, _, err := f.service.List(context.Background(), questionTestVersion, "", 50, nil)
	if err != nil {
		t.Fatalf("List all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("all = %d, want 2", len(all))
	}
	if f.events.count(EventQuestionExpired) != 1 {
		t.Fatalf("question.expired events = %d, want 1", f.events.count(EventQuestionExpired))
	}

	// Version scoping: another version never sees the questions.
	other, _, err := f.service.List(context.Background(), questionTestVersion+1, "", 50, nil)
	if err != nil {
		t.Fatalf("List other version: %v", err)
	}
	if len(other) != 0 {
		t.Fatalf("other version questions = %d, want 0", len(other))
	}
	if _, err := f.service.Get(context.Background(), questionTestVersion+1, second.ID); err == nil {
		t.Fatal("cross-version Get succeeded")
	}
}

func TestExpireQuestionsSweepEmitsExpiredEvents(t *testing.T) {
	f := newQuestionFixture()
	f.create(t, questionBatchOf(1))
	f.create(t, questionBatchOf(1))

	f.clock.now = f.clock.now.Add(25 * time.Hour)
	count, err := f.service.ExpireQuestions(context.Background(), f.clock.now)
	if err != nil {
		t.Fatalf("ExpireQuestions: %v", err)
	}
	if count != 2 {
		t.Fatalf("expired = %d, want 2", count)
	}
	if f.events.count(EventQuestionExpired) != 2 {
		t.Fatalf("question.expired events = %d, want 2", f.events.count(EventQuestionExpired))
	}
	again, err := f.service.ExpireQuestions(context.Background(), f.clock.now)
	if err != nil || again != 0 {
		t.Fatalf("second sweep = %d, %v; want 0, nil", again, err)
	}
	if f.StageBlocked() {
		t.Fatal("sweeper paused a case run/wave")
	}
}

func questionToolScope() tools.Scope {
	return tools.Scope{
		VersionID:      questionTestVersion,
		ConversationID: questionTestConversation,
		AgentRunID:     questionTestRun,
	}
}

func questionToolInput() tools.QuestionAskInput {
	return tools.QuestionAskInput{
		BatchID: "qb-test",
		Questions: []tools.QuestionItem{{
			ID:            "q1",
			Text:          "退款到账时间按 T+1 还是 T+3 验收？",
			Options:       []tools.QuestionOption{{ID: "a", Label: "T+1"}, {ID: "b", Label: "T+3"}},
			AllowFreeText: true,
			Required:      true,
			SourceRefs:    []string{"requirement:FR-012"},
		}},
	}
}

func TestQuestionAskFromToolCreatesPendingAndMessage(t *testing.T) {
	f := newQuestionFixture()
	result, err := f.service.AskFromTool(context.Background(), questionToolScope(), questionToolInput())
	if err != nil {
		t.Fatalf("AskFromTool: %v", err)
	}
	if result.ID == 0 || result.MessageID == 0 || result.Status != string(execution.HumanPending) {
		t.Fatalf("result = %+v", result)
	}
	if result.BatchID == "" || result.ExpiresAt.IsZero() {
		t.Fatalf("result = %+v, want batch id and expiry", result)
	}
	request, err := f.service.Get(context.Background(), questionTestVersion, result.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if request.Kind != execution.HumanClarificationQuestion || request.Status != execution.HumanPending {
		t.Fatalf("request = %+v", request)
	}
	if got := f.messages.countKind(QuestionMessageKindQuestion); got != 1 {
		t.Fatalf("question messages = %d, want 1", got)
	}
}

func TestAskFromToolRejectsMissingScope(t *testing.T) {
	f := newQuestionFixture()
	scope := questionToolScope()
	scope.AgentRunID = 0
	_, err := f.service.AskFromTool(context.Background(), scope, questionToolInput())
	if err == nil {
		t.Fatal("tool scope without the owning agent run accepted")
	}
	var apiErr *platform.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %T %v, want typed platform error", err, err)
	}
}
