package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	eventingapp "github.com/EdouardRichard/EzTest/backend/internal/eventing/application"
	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	execution "github.com/EdouardRichard/EzTest/backend/internal/execution/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	platformid "github.com/EdouardRichard/EzTest/backend/internal/platform/id"
)

// Task 23 handler contract: POST conversations/{cid}/questions, POST
// questions/{qid}/answer|skip|reopen, GET versions/{id}/questions?status=.

var questionHandlerNow = time.Date(2026, 11, 5, 6, 0, 0, 0, time.UTC)

type handlerQuestionRepo struct {
	mu     sync.Mutex
	byID   map[int64]execution.HumanRequest
	nextID int64
}

func newHandlerQuestionRepo() *handlerQuestionRepo {
	return &handlerQuestionRepo{byID: map[int64]execution.HumanRequest{}}
}

func (r *handlerQuestionRepo) Create(_ context.Context, request execution.HumanRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if request.ID == 0 {
		r.nextID++
		request.ID = 9000 + r.nextID
	}
	if _, exists := r.byID[request.ID]; exists {
		return execution.ErrDuplicate
	}
	r.byID[request.ID] = request
	return nil
}

func (r *handlerQuestionRepo) Get(_ context.Context, id int64) (execution.HumanRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.byID[id]
	if !ok {
		return execution.HumanRequest{}, execution.ErrNotFound
	}
	return request, nil
}

func (r *handlerQuestionRepo) GetOpenByCaseRun(context.Context, int64) (*execution.HumanRequest, error) {
	return nil, nil
}

func (r *handlerQuestionRepo) ListByVersion(context.Context, int64, string, int, *int64) ([]execution.HumanRequest, bool, error) {
	return nil, false, nil
}

func (r *handlerQuestionRepo) ListOpen(context.Context) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (r *handlerQuestionRepo) CountByCaseRun(context.Context, int64) (int, error) { return 0, nil }

func (r *handlerQuestionRepo) Transition(_ context.Context, id int64, from, to execution.HumanRequestStatus, response map[string]any, actorID int64, at time.Time) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.byID[id]
	if !ok {
		return false, execution.ErrNotFound
	}
	if request.Status != from {
		return false, nil
	}
	request.Status = to
	request.Response = response
	switch to {
	case execution.HumanAnswered, execution.HumanCancelled:
		request.ResolvedBy = &actorID
		request.ResolvedAt = &at
	case execution.HumanPending:
		request.ResolvedBy = nil
		request.ResolvedAt = nil
	}
	request.UpdatedAt = at
	r.byID[id] = request
	return true, nil
}

func (r *handlerQuestionRepo) Expire(context.Context, time.Time) ([]execution.HumanRequest, error) {
	return nil, nil
}

func (r *handlerQuestionRepo) ListQuestions(_ context.Context, versionID int64, status string, limit int, beforeID *int64) ([]execution.HumanRequest, bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]execution.HumanRequest, 0)
	for _, request := range r.byID {
		if request.VersionID != versionID || request.Kind != execution.HumanClarificationQuestion {
			continue
		}
		if status != "" && string(request.Status) != status {
			continue
		}
		if beforeID != nil && request.ID >= *beforeID {
			continue
		}
		items = append(items, request)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID > items[j].ID })
	hasMore := limit > 0 && len(items) > limit
	if hasMore {
		items = items[:limit]
	}
	return items, hasMore, nil
}

func (r *handlerQuestionRepo) AttachMessage(_ context.Context, id, messageID int64) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	request, ok := r.byID[id]
	if !ok {
		return execution.ErrNotFound
	}
	request.MessageID = &messageID
	r.byID[id] = request
	return nil
}

func (r *handlerQuestionRepo) ExpireQuestions(_ context.Context, now time.Time) ([]execution.HumanRequest, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	expired := make([]execution.HumanRequest, 0)
	for id, request := range r.byID {
		if request.Kind != execution.HumanClarificationQuestion || request.Status != execution.HumanPending || request.ExpiresAt.After(now) {
			continue
		}
		request.Status = execution.HumanExpired
		request.UpdatedAt = now
		r.byID[id] = request
		expired = append(expired, request)
	}
	sort.Slice(expired, func(i, j int) bool { return expired[i].ID < expired[j].ID })
	return expired, nil
}

type handlerQuestionMessages struct {
	mu     sync.Mutex
	nextID int64
	rows   []executionapp.QuestionMessage
}

func (m *handlerQuestionMessages) AppendQuestionMessage(_ context.Context, message executionapp.QuestionMessage) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	id := 7000 + m.nextID
	m.rows = append(m.rows, message)
	return id, true, nil
}

func (m *handlerQuestionMessages) countKind(kind string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, row := range m.rows {
		if row.Kind == kind {
			count++
		}
	}
	return count
}

type handlerClock struct{ now time.Time }

func (c *handlerClock) Now() time.Time { return c.now }

type handlerIDs struct {
	mu   sync.Mutex
	next int64
}

func (g *handlerIDs) Next() int64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.next++
	return g.next
}

type handlerEvents struct {
	mu     sync.Mutex
	events []string
}

func (p *handlerEvents) Publish(_ context.Context, event eventingapp.Event) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.events = append(p.events, event.Type)
	return nil
}

func (p *handlerEvents) count(eventType string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	count := 0
	for _, item := range p.events {
		if item == eventType {
			count++
		}
	}
	return count
}

type handlerQuestionFixture struct {
	handler  *Handler
	repo     *handlerQuestionRepo
	messages *handlerQuestionMessages
	events   *handlerEvents
	service  *executionapp.QuestionService
}

func newHandlerQuestionFixture() *handlerQuestionFixture {
	repo := newHandlerQuestionRepo()
	messages := &handlerQuestionMessages{}
	events := &handlerEvents{}
	service := executionapp.NewQuestionService(executionapp.QuestionServiceDeps{
		Humans:   repo,
		Messages: messages,
		IDs:      &handlerIDs{},
		Clock:    &handlerClock{now: questionHandlerNow},
		Events:   events,
	})
	return &handlerQuestionFixture{
		handler:  New(Deps{QuestionService: service}),
		repo:     repo,
		messages: messages,
		events:   events,
		service:  service,
	}
}

func (f *handlerQuestionFixture) seed(t *testing.T, count int) execution.HumanRequest {
	t.Helper()
	questions := make([]execution.HumanQuestion, 0, count)
	for index := 0; index < count; index++ {
		questions = append(questions, execution.HumanQuestion{
			ID:            "q" + string(rune('1'+index)),
			Text:          "问题",
			Options:       []execution.HumanQuestionOption{{ID: "a", Label: "A"}, {ID: "b", Label: "B"}},
			AllowFreeText: true,
			Required:      index == 0,
		})
	}
	request, err := f.service.Create(context.Background(), executionapp.QuestionCreateInput{
		VersionID: 100, ConversationID: 2001, AgentRunID: 9901, Questions: questions,
	})
	if err != nil {
		t.Fatalf("seed Create: %v", err)
	}
	return request
}

func questionActorRequest(method, target, body string) *http.Request {
	var request *http.Request
	if body == "" {
		request = httptest.NewRequest(method, target, nil)
	} else {
		request = httptest.NewRequest(method, target, strings.NewReader(body))
	}
	return request.WithContext(platform.WithActor(request.Context(), platform.Actor{ID: 7, Role: "admin"}))
}

func setQuestionPathValues(request *http.Request, values map[string]string) {
	for name, value := range values {
		request.SetPathValue(name, value)
	}
}

func decodeQuestionData(t *testing.T, recorder *httptest.ResponseRecorder) json.RawMessage {
	t.Helper()
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode body %s: %v", recorder.Body.String(), err)
	}
	return envelope.Data
}

func TestQuestionHandlerListAndAnswer(t *testing.T) {
	f := newHandlerQuestionFixture()
	question := f.seed(t, 2)

	// GET /api/versions/{id}/questions?status=pending
	listRequest := questionActorRequest(http.MethodGet, "/api/versions/100/questions?status=pending", "")
	setQuestionPathValues(listRequest, map[string]string{"id": "100"})
	listRecorder := httptest.NewRecorder()
	f.handler.ListQuestions(listRecorder, listRequest)
	if listRecorder.Code != http.StatusOK {
		t.Fatalf("list status = %d body=%s", listRecorder.Code, listRecorder.Body.String())
	}
	var listBody struct {
		Data    []QuestionView `json:"data"`
		HasMore bool           `json:"has_more"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &listBody); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listBody.Data) != 1 || listBody.Data[0].Status != "pending" {
		t.Fatalf("list = %+v", listBody.Data)
	}
	if len(listBody.Data[0].Questions) != 2 || listBody.Data[0].BatchID == "" || listBody.Data[0].ExpiresAt == "" {
		t.Fatalf("question view = %+v", listBody.Data[0])
	}

	// POST /api/versions/{id}/questions/{qid}/answer
	answerRequest := questionActorRequest(http.MethodPost, "/api/versions/100/questions/"+questionID(question.ID)+"/answer",
		`{"answers":[{"question_id":"q1","selected":["a"]},{"question_id":"q2","text":"需要财务确认"}]}`)
	setQuestionPathValues(answerRequest, map[string]string{"id": "100", "qid": questionID(question.ID)})
	answerRecorder := httptest.NewRecorder()
	f.handler.AnswerQuestion(answerRecorder, answerRequest)
	if answerRecorder.Code != http.StatusOK {
		t.Fatalf("answer status = %d body=%s", answerRecorder.Code, answerRecorder.Body.String())
	}
	var answered QuestionView
	if err := json.Unmarshal(decodeQuestionData(t, answerRecorder), &answered); err != nil {
		t.Fatalf("decode answer: %v", err)
	}
	if answered.Status != "answered" || len(answered.Answers) != 2 || answered.AnsweredBy == nil || *answered.AnsweredBy != "7" {
		t.Fatalf("answered view = %+v", answered)
	}
	if answered.AnsweredAt == nil || *answered.AnsweredAt == "" {
		t.Fatalf("answered_at = %v", answered.AnsweredAt)
	}
	if got := f.messages.countKind("answer"); got != 1 {
		t.Fatalf("answer messages = %d, want 1", got)
	}
	if f.events.count("question.answered") != 1 {
		t.Fatalf("question.answered events = %d", f.events.count("question.answered"))
	}

	// Answered questions are filtered by status and hidden from the pending list.
	pendingRequest := questionActorRequest(http.MethodGet, "/api/versions/100/questions?status=pending", "")
	setQuestionPathValues(pendingRequest, map[string]string{"id": "100"})
	pendingRecorder := httptest.NewRecorder()
	f.handler.ListQuestions(pendingRecorder, pendingRequest)
	var pendingBody struct {
		Data []QuestionView `json:"data"`
	}
	if err := json.Unmarshal(pendingRecorder.Body.Bytes(), &pendingBody); err != nil {
		t.Fatalf("decode pending: %v", err)
	}
	if len(pendingBody.Data) != 0 {
		t.Fatalf("pending list = %+v, want empty", pendingBody.Data)
	}

	invalidRequest := questionActorRequest(http.MethodGet, "/api/versions/100/questions?status=bogus", "")
	setQuestionPathValues(invalidRequest, map[string]string{"id": "100"})
	invalidRecorder := httptest.NewRecorder()
	f.handler.ListQuestions(invalidRecorder, invalidRequest)
	if invalidRecorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want 400", invalidRecorder.Code)
	}

	// Version scoping: another version cannot list or answer this question.
	otherList := questionActorRequest(http.MethodGet, "/api/versions/101/questions", "")
	setQuestionPathValues(otherList, map[string]string{"id": "101"})
	otherListRecorder := httptest.NewRecorder()
	f.handler.ListQuestions(otherListRecorder, otherList)
	var otherBody struct {
		Data []QuestionView `json:"data"`
	}
	if err := json.Unmarshal(otherListRecorder.Body.Bytes(), &otherBody); err != nil {
		t.Fatalf("decode other list: %v", err)
	}
	if len(otherBody.Data) != 0 {
		t.Fatalf("other version list = %+v", otherBody.Data)
	}
	otherAnswer := questionActorRequest(http.MethodPost, "/api/versions/101/questions/"+questionID(question.ID)+"/answer",
		`{"answers":[{"question_id":"q1","selected":["a"]}]}`)
	setQuestionPathValues(otherAnswer, map[string]string{"id": "101", "qid": questionID(question.ID)})
	otherAnswerRecorder := httptest.NewRecorder()
	f.handler.AnswerQuestion(otherAnswerRecorder, otherAnswer)
	if otherAnswerRecorder.Code != http.StatusNotFound {
		t.Fatalf("cross-version answer = %d, want 404", otherAnswerRecorder.Code)
	}
}

func TestQuestionHandlerSkipAndReopen(t *testing.T) {
	f := newHandlerQuestionFixture()
	question := f.seed(t, 1)

	skipRequest := questionActorRequest(http.MethodPost, "/api/versions/100/questions/"+questionID(question.ID)+"/skip", `{"reason":"暂不确认"}`)
	setQuestionPathValues(skipRequest, map[string]string{"id": "100", "qid": questionID(question.ID)})
	skipRecorder := httptest.NewRecorder()
	f.handler.SkipQuestion(skipRecorder, skipRequest)
	if skipRecorder.Code != http.StatusOK {
		t.Fatalf("skip status = %d body=%s", skipRecorder.Code, skipRecorder.Body.String())
	}
	var skipped QuestionView
	if err := json.Unmarshal(decodeQuestionData(t, skipRecorder), &skipped); err != nil {
		t.Fatalf("decode skip: %v", err)
	}
	if skipped.Status != "cancelled" || !skipped.Skipped {
		t.Fatalf("skipped view = %+v", skipped)
	}

	// Reopen is owner-scoped: the owning Host run only.
	foreign := questionActorRequest(http.MethodPost, "/api/versions/100/questions/"+questionID(question.ID)+"/reopen", "")
	foreign.Header.Set("X-Agent-Run-Id", "9999")
	setQuestionPathValues(foreign, map[string]string{"id": "100", "qid": questionID(question.ID)})
	foreignRecorder := httptest.NewRecorder()
	f.handler.ReopenQuestion(foreignRecorder, foreign)
	if foreignRecorder.Code != http.StatusForbidden {
		t.Fatalf("foreign reopen = %d, want 403", foreignRecorder.Code)
	}

	reopen := questionActorRequest(http.MethodPost, "/api/versions/100/questions/"+questionID(question.ID)+"/reopen", "")
	reopen.Header.Set("X-Agent-Run-Id", "9901")
	setQuestionPathValues(reopen, map[string]string{"id": "100", "qid": questionID(question.ID)})
	reopenRecorder := httptest.NewRecorder()
	f.handler.ReopenQuestion(reopenRecorder, reopen)
	if reopenRecorder.Code != http.StatusOK {
		t.Fatalf("reopen status = %d body=%s", reopenRecorder.Code, reopenRecorder.Body.String())
	}
	var reopened QuestionView
	if err := json.Unmarshal(decodeQuestionData(t, reopenRecorder), &reopened); err != nil {
		t.Fatalf("decode reopen: %v", err)
	}
	if reopened.Status != "pending" {
		t.Fatalf("reopened status = %s, want pending", reopened.Status)
	}
}

func TestQuestionHandlerCreateValidationAndAuth(t *testing.T) {
	f := newHandlerQuestionFixture()
	validBody := `{"questions":[{"id":"q1","text":"退款按 T+1 还是 T+3？","options":[{"id":"a","label":"T+1"}],"allow_free_text":true,"required":true}],"expires_at":"2026-11-06T06:00:00Z"}`

	create := func(body, runID string) *httptest.ResponseRecorder {
		request := questionActorRequest(http.MethodPost, "/api/versions/100/conversations/2001/questions", body)
		if runID != "" {
			request.Header.Set("X-Agent-Run-Id", runID)
		}
		setQuestionPathValues(request, map[string]string{"id": "100", "cid": "2001"})
		recorder := httptest.NewRecorder()
		f.handler.CreateQuestion(recorder, request)
		return recorder
	}

	if got := create(validBody, ""); got.Code != http.StatusBadRequest {
		t.Fatalf("missing host run = %d, want 400", got.Code)
	}
	if got := create(`{"questions":[]}`, "9901"); got.Code != http.StatusBadRequest {
		t.Fatalf("empty questions = %d, want 400", got.Code)
	}
	created := create(validBody, "9901")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", created.Code, created.Body.String())
	}
	var createdView QuestionView
	if err := json.Unmarshal(decodeQuestionData(t, created), &createdView); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if createdView.Status != "pending" || len(createdView.Questions) != 1 || createdView.ConversationID == nil {
		t.Fatalf("created view = %+v", createdView)
	}
	if got := f.messages.countKind("question"); got != 1 {
		t.Fatalf("question messages = %d, want 1", got)
	}

	// Users only answer/skip: an unauthenticated actor cannot answer.
	noActor := httptest.NewRequest(http.MethodPost, "/api/versions/100/questions/"+createdView.ID+"/answer",
		strings.NewReader(`{"answers":[{"question_id":"q1","selected":["a"]}]}`))
	setQuestionPathValues(noActor, map[string]string{"id": "100", "qid": createdView.ID})
	noActorRecorder := httptest.NewRecorder()
	f.handler.AnswerQuestion(noActorRecorder, noActor)
	if noActorRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated answer = %d, want 401", noActorRecorder.Code)
	}
	if got := f.messages.countKind("answer"); got != 0 {
		t.Fatalf("unauthenticated answer wrote %d messages", got)
	}
}

// questionID renders a human_request id for the test paths.
func questionID(id int64) string {
	return platformid.Format(id)
}
