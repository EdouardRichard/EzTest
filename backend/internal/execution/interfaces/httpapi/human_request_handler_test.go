package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	executionapp "github.com/EdouardRichard/EzTest/backend/internal/execution/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// v0.22 W2-2 handler contract: POST /api/versions/{id}/human-requests creates the
// version-level approval/blocked_recovery/conflict request and the existing
// POST /api/human-requests/{id}/resolve carries the decision/reason.

// handlerHumanStage is the narrow version-stage port fake used by the handler
// fixture; it records Unblock calls.
type handlerHumanStage struct {
	mu      sync.Mutex
	calls   []int64
	blocked map[int64]bool
}

func newHandlerHumanStage(blocked ...int64) *handlerHumanStage {
	port := &handlerHumanStage{blocked: map[int64]bool{}}
	for _, versionID := range blocked {
		port.blocked[versionID] = true
	}
	return port
}

func (s *handlerHumanStage) Unblock(_ context.Context, versionID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, versionID)
	s.blocked[versionID] = false
	return nil
}

func (s *handlerHumanStage) unblockedIDs() []int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int64(nil), s.calls...)
}

// humanHandlerSettings answers the question timeout; every other key is absent.
type humanHandlerSettings struct{ timeoutSeconds int64 }

func (s humanHandlerSettings) String(context.Context, string) (string, error) {
	return "", errors.New("setting not found")
}

func (s humanHandlerSettings) Int(context.Context, string) (int64, error) {
	if s.timeoutSeconds > 0 {
		return s.timeoutSeconds, nil
	}
	return 0, errors.New("setting not found")
}

func (s humanHandlerSettings) Bool(context.Context, string) (bool, error) { return false, nil }

type handlerHumanFixture struct {
	handler *Handler
	repo    *handlerQuestionRepo
	events  *handlerEvents
	stage   *handlerHumanStage
	service *executionapp.HumanService
}

func newHandlerHumanFixture() *handlerHumanFixture {
	repo := newHandlerQuestionRepo()
	events := &handlerEvents{}
	stage := newHandlerHumanStage(100)
	service := executionapp.NewHumanService(executionapp.HumanServiceDeps{
		Humans:   repo,
		Settings: humanHandlerSettings{timeoutSeconds: 86400},
		IDs:      &handlerIDs{},
		Clock:    &handlerClock{now: questionHandlerNow},
		Events:   events,
		Stages:   stage,
	})
	return &handlerHumanFixture{
		handler: New(Deps{Humans: repo, HumanService: service}),
		repo:    repo,
		events:  events,
		stage:   stage,
		service: service,
	}
}

func TestHumanRequestHandlerCreateAndResolveApproval(t *testing.T) {
	f := newHandlerHumanFixture()
	body := `{"kind":"approval","reason":"发布？","conversation_id":"2001","message_id":"3001"}`
	create := questionActorRequest(http.MethodPost, "/api/versions/100/human-requests", body)
	setQuestionPathValues(create, map[string]string{"id": "100"})
	createRecorder := httptest.NewRecorder()
	f.handler.CreateHumanRequest(createRecorder, create)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s, want 201", createRecorder.Code, createRecorder.Body.String())
	}

	var created HumanRequestView
	if err := json.Unmarshal(decodeQuestionData(t, createRecorder), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Kind != "approval" || created.Status != "pending" || created.CaseRunID != nil {
		t.Fatalf("created view = %+v, want a pending version-level approval", created)
	}
	if created.ConversationID == nil || *created.ConversationID != "2001" {
		t.Fatalf("conversation_id = %v, want 2001", created.ConversationID)
	}
	if created.MessageID == nil || *created.MessageID != "3001" {
		t.Fatalf("message_id = %v, want 3001", created.MessageID)
	}
	if created.Payload.Reason != "发布？" {
		t.Fatalf("payload.reason = %q", created.Payload.Reason)
	}
	if got := f.events.count("approval.requested"); got != 1 {
		t.Fatalf("approval.requested events = %d, want 1", got)
	}

	resolve := questionActorRequest(http.MethodPost,
		"/api/human-requests/"+created.ID+"/resolve", `{"response":{"decision":"approved","reason":"ok"}}`)
	setQuestionPathValues(resolve, map[string]string{"humanRequestId": created.ID})
	resolveRecorder := httptest.NewRecorder()
	f.handler.ResolveHumanRequest(resolveRecorder, resolve)
	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("resolve = %d body=%s, want 200", resolveRecorder.Code, resolveRecorder.Body.String())
	}
	var resolved HumanRequestView
	if err := json.Unmarshal(decodeQuestionData(t, resolveRecorder), &resolved); err != nil {
		t.Fatalf("decode resolve: %v", err)
	}
	if resolved.Status != "answered" {
		t.Fatalf("resolved status = %s, want answered", resolved.Status)
	}
	if resolved.Response == nil || resolved.Response.Decision != "approved" || resolved.Response.Reason != "ok" {
		t.Fatalf("resolved response = %+v, want decision/reason", resolved.Response)
	}
	if got := f.events.count("approval.decided"); got != 1 {
		t.Fatalf("approval.decided events = %d, want 1", got)
	}
}

func TestHumanRequestHandlerBlockedRecoveryUnblocks(t *testing.T) {
	f := newHandlerHumanFixture()
	create := questionActorRequest(http.MethodPost, "/api/versions/100/human-requests",
		`{"kind":"blocked_recovery","reason":"执行器离线"}`)
	setQuestionPathValues(create, map[string]string{"id": "100"})
	createRecorder := httptest.NewRecorder()
	f.handler.CreateHumanRequest(createRecorder, create)
	if createRecorder.Code != http.StatusCreated {
		t.Fatalf("create = %d body=%s", createRecorder.Code, createRecorder.Body.String())
	}
	var created HumanRequestView
	if err := json.Unmarshal(decodeQuestionData(t, createRecorder), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	resolve := questionActorRequest(http.MethodPost,
		"/api/human-requests/"+created.ID+"/resolve", `{"response":{"decision":"approved"}}`)
	setQuestionPathValues(resolve, map[string]string{"humanRequestId": created.ID})
	resolveRecorder := httptest.NewRecorder()
	f.handler.ResolveHumanRequest(resolveRecorder, resolve)
	if resolveRecorder.Code != http.StatusOK {
		t.Fatalf("resolve = %d body=%s", resolveRecorder.Code, resolveRecorder.Body.String())
	}
	if got := f.stage.unblockedIDs(); len(got) != 1 || got[0] != 100 {
		t.Fatalf("Unblock calls = %v, want [100]", got)
	}
}

func TestHumanRequestHandlerValidation(t *testing.T) {
	f := newHandlerHumanFixture()

	// Unauthenticated create is rejected before any body work.
	noActor := httptest.NewRequest(http.MethodPost, "/api/versions/100/human-requests",
		strings.NewReader(`{"kind":"approval","reason":"x"}`))
	setQuestionPathValues(noActor, map[string]string{"id": "100"})
	noActorRecorder := httptest.NewRecorder()
	f.handler.CreateHumanRequest(noActorRecorder, noActor)
	if noActorRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated create = %d, want 401", noActorRecorder.Code)
	}

	create := func(body string) *httptest.ResponseRecorder {
		request := questionActorRequest(http.MethodPost, "/api/versions/100/human-requests", body)
		setQuestionPathValues(request, map[string]string{"id": "100"})
		recorder := httptest.NewRecorder()
		f.handler.CreateHumanRequest(recorder, request)
		return recorder
	}
	invalidBodies := map[string]string{
		"login takeover": `{"kind":"login_takeover","reason":"x"}`,
		"missing reason": `{"kind":"approval"}`,
		"unknown field":  `{"kind":"approval","reason":"x","bogus":true}`,
	}
	for name, body := range invalidBodies {
		t.Run(name, func(t *testing.T) {
			if got := create(body); got.Code != http.StatusBadRequest {
				t.Fatalf("create(%s) = %d body=%s, want 400", name, got.Code, got.Body.String())
			}
		})
	}
	if rows, _, err := f.repo.ListByVersion(context.Background(), 100, "", 10, nil); err != nil || len(rows) != 0 {
		t.Fatalf("invalid creates persisted rows = %#v / %v", rows, err)
	}

	// Resolve rejects a malformed decision and a missing decision.
	valid := create(`{"kind":"approval","reason":"发布？"}`)
	if valid.Code != http.StatusCreated {
		t.Fatalf("valid create = %d body=%s", valid.Code, valid.Body.String())
	}
	var created HumanRequestView
	if err := json.Unmarshal(decodeQuestionData(t, valid), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	for name, body := range map[string]string{
		"missing decision": `{"response":{"note":"x"}}`,
		"invalid decision": `{"response":{"decision":"maybe"}}`,
		"unknown response": `{"response":{"decision":"approved","bogus":true}}`,
	} {
		t.Run(name, func(t *testing.T) {
			resolve := questionActorRequest(http.MethodPost, "/api/human-requests/"+created.ID+"/resolve", body)
			setQuestionPathValues(resolve, map[string]string{"humanRequestId": created.ID})
			recorder := httptest.NewRecorder()
			f.handler.ResolveHumanRequest(recorder, resolve)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("resolve(%s) = %d body=%s, want 400", name, recorder.Code, recorder.Body.String())
			}
		})
	}
	if got := f.events.count("approval.decided"); got != 0 {
		t.Fatalf("invalid resolves published %d approval.decided events", got)
	}
}

// TestHumanRequestCreateRouteIsRegistered pins routes.go: the new create route is
// mounted on the execution handler.
func TestHumanRequestCreateRouteIsRegistered(t *testing.T) {
	f := newHandlerHumanFixture()
	mux := http.NewServeMux()
	authenticated := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r.WithContext(platform.WithActor(r.Context(), platform.Actor{ID: 7, Role: "admin"})))
		})
	}
	passthrough := func(next http.Handler) http.Handler { return next }
	f.handler.Register(mux, authenticated, passthrough)

	request := httptest.NewRequest(http.MethodPost, "/api/versions/100/human-requests",
		strings.NewReader(`{"kind":"conflict","reason":"冲突"}`))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("routed create = %d body=%s, want 201", recorder.Code, recorder.Body.String())
	}
}
