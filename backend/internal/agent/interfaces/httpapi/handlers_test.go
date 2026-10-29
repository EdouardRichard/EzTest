package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/agent/domain"
	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// fakeProfileService is a programmable ProfileService.
type fakeProfileService struct {
	listFn   func(ctx context.Context) ([]domain.AgentProfile, error)
	getFn    func(ctx context.Context, id int64) (domain.AgentProfile, error)
	updateFn func(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error)

	updateCalls int
	lastID      int64
	lastParams  domain.UpdateParams
	lastActor   *int64
}

// List implements ProfileService.
func (f *fakeProfileService) List(ctx context.Context) ([]domain.AgentProfile, error) {
	if f.listFn != nil {
		return f.listFn(ctx)
	}
	return []domain.AgentProfile{}, nil
}

// Get implements ProfileService.
func (f *fakeProfileService) Get(ctx context.Context, id int64) (domain.AgentProfile, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return domain.AgentProfile{}, domain.ErrNotFound
}

// Update implements ProfileService.
func (f *fakeProfileService) Update(ctx context.Context, id int64, params domain.UpdateParams, actor *int64) (domain.AgentProfile, error) {
	f.updateCalls++
	f.lastID = id
	f.lastParams = params
	f.lastActor = actor
	if f.updateFn != nil {
		return f.updateFn(ctx, id, params, actor)
	}
	return domain.AgentProfile{}, nil
}

// fakeSessionService is a programmable SessionService.
type fakeSessionService struct {
	listFn        func(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error)
	createFn      func(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error)
	getFn         func(ctx context.Context, id int64) (domain.SessionDetail, error)
	updateTitleFn func(ctx context.Context, id int64, title string) (domain.Session, error)
	deleteFn      func(ctx context.Context, id int64, actor *int64) error
	messageFn     func(ctx context.Context, sessionID, messageID int64) (domain.Message, error)

	createCalls      int
	createdProfileID int64
	createdTitle     string
	createdActor     *int64
	updateTitleCalls int
	updatedTitleID   int64
	updatedTitle     string
	deleteCalls      int
	deletedID        int64
	deletedActor     *int64
	messageCalls     int
	messageSessionID int64
	messageIDArg     int64

	lastCursor    int64
	lastLimit     int
	lastProfileID *int64
	listCalls     int
}

// List implements SessionService.
func (f *fakeSessionService) List(ctx context.Context, cursor int64, limit int, profileID *int64) ([]domain.SessionSummary, bool, error) {
	f.listCalls++
	f.lastCursor = cursor
	f.lastLimit = limit
	f.lastProfileID = profileID
	if f.listFn != nil {
		return f.listFn(ctx, cursor, limit, profileID)
	}
	return []domain.SessionSummary{}, false, nil
}

// Create implements SessionService.
func (f *fakeSessionService) Create(ctx context.Context, profileID int64, title string, actor *int64) (domain.Session, error) {
	f.createCalls++
	f.createdProfileID = profileID
	f.createdTitle = title
	f.createdActor = actor
	if f.createFn != nil {
		return f.createFn(ctx, profileID, title, actor)
	}
	return domain.Session{}, nil
}

// Get implements SessionService.
func (f *fakeSessionService) Get(ctx context.Context, id int64) (domain.SessionDetail, error) {
	if f.getFn != nil {
		return f.getFn(ctx, id)
	}
	return domain.SessionDetail{}, domain.ErrNotFound
}

// UpdateTitle implements SessionService.
func (f *fakeSessionService) UpdateTitle(ctx context.Context, id int64, title string) (domain.Session, error) {
	f.updateTitleCalls++
	f.updatedTitleID = id
	f.updatedTitle = title
	if f.updateTitleFn != nil {
		return f.updateTitleFn(ctx, id, title)
	}
	return domain.Session{}, nil
}

// Delete implements SessionService.
func (f *fakeSessionService) Delete(ctx context.Context, id int64, actor *int64) error {
	f.deleteCalls++
	f.deletedID = id
	f.deletedActor = actor
	if f.deleteFn != nil {
		return f.deleteFn(ctx, id, actor)
	}
	return nil
}

// Message implements SessionService.
func (f *fakeSessionService) Message(ctx context.Context, sessionID, messageID int64) (domain.Message, error) {
	f.messageCalls++
	f.messageSessionID = sessionID
	f.messageIDArg = messageID
	if f.messageFn != nil {
		return f.messageFn(ctx, sessionID, messageID)
	}
	return domain.Message{}, domain.ErrNotFound
}

// fakeRunControl is a programmable RunControl.
type fakeRunControl struct {
	startFn func(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error)
	abortFn func(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error)

	startCalls   int
	startSession int64
	startMessage string
	startActor   *int64
	abortCalls   int
	abortSession int64
	abortActor   *int64
}

// Start implements RunControl.
func (f *fakeRunControl) Start(ctx context.Context, sessionID int64, message string, actor *int64) (domain.StartResult, error) {
	f.startCalls++
	f.startSession = sessionID
	f.startMessage = message
	f.startActor = actor
	if f.startFn != nil {
		return f.startFn(ctx, sessionID, message, actor)
	}
	return domain.StartResult{}, nil
}

// Abort implements RunControl.
func (f *fakeRunControl) Abort(ctx context.Context, sessionID int64, actor *int64) (domain.AbortResult, error) {
	f.abortCalls++
	f.abortSession = sessionID
	f.abortActor = actor
	if f.abortFn != nil {
		return f.abortFn(ctx, sessionID, actor)
	}
	return domain.AbortResult{Status: "idle"}, nil
}

// fakeToolCatalog is a static ToolCatalog.
type fakeToolCatalog struct {
	infos []domain.ToolInfo
	calls int
}

// Infos implements ToolCatalog.
func (f *fakeToolCatalog) Infos() []domain.ToolInfo {
	f.calls++
	return f.infos
}

// apiErrorEnvelope mirrors the shared platform error envelope.
type apiErrorEnvelope struct {
	Error struct {
		Code      string         `json:"code"`
		Message   string         `json:"message"`
		RequestID string         `json:"request_id"`
		Retryable bool           `json:"retryable"`
		Details   map[string]any `json:"details"`
	} `json:"error"`
}

// serve issues one request through the registered routes with no guard.
func serve(t *testing.T, h *Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	return serveWithContext(t, h, context.Background(), method, target, body)
}

// serveWithContext issues one request through the registered routes with ctx.
func serveWithContext(t *testing.T, h *Handler, ctx context.Context, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	RegisterRoutes(mux, h, nil)
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, target, reader)
	if ctx != nil {
		request = request.WithContext(ctx)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

// decodeError decodes the shared error envelope.
func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) apiErrorEnvelope {
	t.Helper()
	var envelope apiErrorEnvelope
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode error envelope from %q: %v", recorder.Body.String(), err)
	}
	return envelope
}

// assertStatus checks the HTTP status and returns the decoded error envelope.
func assertStatus(t *testing.T, recorder *httptest.ResponseRecorder, want int) apiErrorEnvelope {
	t.Helper()
	if recorder.Code != want {
		t.Fatalf("status=%d want %d (body=%s)", recorder.Code, want, recorder.Body.String())
	}
	return decodeError(t, recorder)
}

// testProfile returns a valid profile payload source.
func testProfile() domain.AgentProfile {
	profile := domain.BuiltinProfile()
	profile.ID = 1
	profile.CreatedAt = time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	profile.UpdatedAt = time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC)
	return profile
}

// testRun returns a stored run with every optional API field populated.
func testRun(id int64, status domain.RunStatus) domain.Run {
	ended := time.Date(2026, 10, 27, 12, 10, 0, 0, time.UTC)
	run := domain.Run{
		ID:              id,
		SessionID:       4,
		Status:          status,
		TerminalReason:  domain.ReasonCompleted,
		AbortCode:       "user_abort",
		ErrorCode:       "model_error",
		Turns:           2,
		InputTokens:     1200,
		OutputTokens:    300,
		ReasoningTokens: 800,
		LastToolName:    "file.read",
		LastToolPending: false,
		StartedAt:       time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		EndedAt:         &ended,
	}
	if status == domain.RunRunning {
		run.TerminalReason = ""
		run.EndedAt = nil
	}
	return run
}

// testSession returns a stored session.
func testSession() domain.Session {
	return domain.Session{
		ID:             4,
		ProfileID:      1,
		ProfileKey:     "runtime-diagnostic",
		Title:          "检查运行时健康",
		CreatedBy:      ptrInt64(1),
		CreatedAt:      time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC),
		LastActivityAt: time.Date(2026, 10, 27, 12, 30, 0, 0, time.UTC),
	}
}

// ptrInt64 returns a pointer to v.
func ptrInt64(v int64) *int64 { return &v }

// newTestHandler builds the handler with all fakes.
func newTestHandler(profiles *fakeProfileService, sessions *fakeSessionService, runs *fakeRunControl, tools *fakeToolCatalog) *Handler {
	if profiles == nil {
		profiles = &fakeProfileService{}
	}
	if sessions == nil {
		sessions = &fakeSessionService{}
	}
	if runs == nil {
		runs = &fakeRunControl{}
	}
	if tools == nil {
		tools = &fakeToolCatalog{}
	}
	return NewHandler(profiles, sessions, runs, tools)
}

func TestSendMessageReturns202AndMapsErrors(t *testing.T) {
	t.Run("accepted", func(t *testing.T) {
		runs := &fakeRunControl{startFn: func(context.Context, int64, string, *int64) (domain.StartResult, error) {
			return domain.StartResult{RunID: 9, UserMessageID: 77}, nil
		}}
		handler := newTestHandler(nil, nil, runs, nil)

		recorder := serve(t, handler, http.MethodPost, "/api/agents/sessions/1/messages", `{"message":"检查运行时健康"}`)
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status=%d want 202 (body=%s)", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				RunID  int64  `json:"run_id"`
				Status string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if response.Data.RunID != 9 || response.Data.Status != "accepted" {
			t.Fatalf("data=%+v want run_id=9 status=accepted", response.Data)
		}
		if runs.startCalls != 1 || runs.startSession != 1 || runs.startMessage != "检查运行时健康" {
			t.Fatalf("Start calls=%d session=%d message=%q want 1/1/message", runs.startCalls, runs.startSession, runs.startMessage)
		}
	})

	t.Run("error mapping", func(t *testing.T) {
		cases := []struct {
			name      string
			err       error
			status    int
			code      string
			reason    string
			field     string
			retryable bool
		}{
			{name: "run active", err: domain.ErrRunActive, status: http.StatusConflict, code: platform.CodeConflict, reason: "run_active"},
			{name: "capacity", err: domain.ErrCapacity, status: http.StatusServiceUnavailable, code: platform.CodeUnavailable, reason: "capacity", retryable: true},
			{name: "llm not configured", err: domain.ErrLLMNotConfigured, status: http.StatusServiceUnavailable, code: platform.CodeUnavailable, reason: "llm_not_configured"},
			{name: "tools unsupported", err: domain.ErrToolsUnsupported, status: http.StatusServiceUnavailable, code: platform.CodeUnavailable, reason: "tools_require_chat_completions"},
			{name: "profile disabled", err: domain.ErrProfileDisabled, status: http.StatusConflict, code: platform.CodeConflict, reason: "profile_disabled"},
			{name: "empty message", err: domain.ErrEmptyMessage, status: http.StatusUnprocessableEntity, code: platform.CodeInvalidArgument, field: "message"},
			{name: "not found", err: domain.ErrNotFound, status: http.StatusNotFound, code: platform.CodeNotFound},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				runs := &fakeRunControl{startFn: func(context.Context, int64, string, *int64) (domain.StartResult, error) {
					return domain.StartResult{}, tc.err
				}}
				handler := newTestHandler(nil, nil, runs, nil)

				recorder := serve(t, handler, http.MethodPost, "/api/agents/sessions/1/messages", `{"message":"hi"}`)
				envelope := assertStatus(t, recorder, tc.status)
				if envelope.Error.Code != tc.code {
					t.Fatalf("code=%q want %q", envelope.Error.Code, tc.code)
				}
				if tc.reason != "" {
					if got := envelope.Error.Details["reason"]; got != tc.reason {
						t.Fatalf("details.reason=%v want %q", got, tc.reason)
					}
				}
				if tc.field != "" {
					if got := envelope.Error.Details["field"]; got != tc.field {
						t.Fatalf("details.field=%v want %q", got, tc.field)
					}
				}
				if envelope.Error.Retryable != tc.retryable {
					t.Fatalf("retryable=%v want %v", envelope.Error.Retryable, tc.retryable)
				}
			})
		}
	})

	t.Run("malformed body is a 400", func(t *testing.T) {
		handler := newTestHandler(nil, nil, nil, nil)
		recorder := serve(t, handler, http.MethodPost, "/api/agents/sessions/1/messages", `{"message":`)
		envelope := assertStatus(t, recorder, http.StatusBadRequest)
		if envelope.Error.Code != platform.CodeInvalidArgument {
			t.Fatalf("code=%q want invalid_argument", envelope.Error.Code)
		}
	})
}

func TestListSessionsReturnsItemsCursorAndHasMore(t *testing.T) {
	t.Run("full page carries the last id as an opaque cursor", func(t *testing.T) {
		summaries := []domain.SessionSummary{
			{Session: testSession(), ActiveRun: ptrRun(testRun(9, domain.RunRunning))},
			{Session: testSession()},
		}
		summaries[1].Session.ID = 3
		summaries[1].Session.ProfileKey = "runtime-diagnostic"
		sessions := &fakeSessionService{listFn: func(context.Context, int64, int, *int64) ([]domain.SessionSummary, bool, error) {
			return summaries, true, nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet,
			"/api/agents/sessions?limit=2&cursor="+platform.EncodeCursor(10), "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.lastCursor != 10 || sessions.lastLimit != 2 || sessions.lastProfileID != nil {
			t.Fatalf("List cursor=%d limit=%d profileID=%v want 10/2/nil",
				sessions.lastCursor, sessions.lastLimit, sessions.lastProfileID)
		}
		var response struct {
			Data struct {
				Items []struct {
					ID         int64  `json:"id"`
					ProfileKey string `json:"profile_key"`
					Running    bool   `json:"running"`
					ActiveRun  *struct {
						ID     int64  `json:"id"`
						Status string `json:"status"`
					} `json:"active_run"`
					LastRun *struct {
						ID     int64  `json:"id"`
						Status string `json:"status"`
					} `json:"last_run"`
				} `json:"items"`
				HasMore    bool    `json:"has_more"`
				NextCursor *string `json:"next_cursor"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(response.Data.Items) != 2 {
			t.Fatalf("items=%d want 2", len(response.Data.Items))
		}
		first := response.Data.Items[0]
		if first.ID != 4 || !first.Running || first.ActiveRun == nil || first.ActiveRun.ID != 9 {
			t.Fatalf("first item=%+v want id=4 running with active_run 9", first)
		}
		if !response.Data.HasMore {
			t.Fatal("has_more=false want true")
		}
		if response.Data.NextCursor == nil || *response.Data.NextCursor != platform.EncodeCursor(3) {
			t.Fatalf("next_cursor=%v want %q", response.Data.NextCursor, platform.EncodeCursor(3))
		}
	})

	t.Run("last page has a null cursor", func(t *testing.T) {
		sessions := &fakeSessionService{listFn: func(context.Context, int64, int, *int64) ([]domain.SessionSummary, bool, error) {
			return nil, false, nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet, "/api/agents/sessions", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if strings.Contains(recorder.Body.String(), `"items":null`) {
			t.Fatalf("body=%s want an empty items array, not null", recorder.Body.String())
		}
		var response struct {
			Data struct {
				Items      []json.RawMessage `json:"items"`
				HasMore    bool              `json:"has_more"`
				NextCursor *string           `json:"next_cursor"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(response.Data.Items) != 0 || response.Data.HasMore || response.Data.NextCursor != nil {
			t.Fatalf("data=%+v want an empty page with a null cursor", response.Data)
		}
	})

	t.Run("profile filter and limits are decoded", func(t *testing.T) {
		sessions := &fakeSessionService{}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet, "/api/agents/sessions?limit=5&profile_id=2", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.lastLimit != 5 || sessions.lastProfileID == nil || *sessions.lastProfileID != 2 {
			t.Fatalf("List limit=%d profileID=%v want 5/2", sessions.lastLimit, sessions.lastProfileID)
		}
	})

	t.Run("invalid limit and cursor are 400", func(t *testing.T) {
		handler := newTestHandler(nil, nil, nil, nil)
		for _, target := range []string{
			"/api/agents/sessions?limit=0",
			"/api/agents/sessions?limit=101",
			"/api/agents/sessions?cursor=!!!",
		} {
			recorder := serve(t, handler, http.MethodGet, target, "")
			envelope := assertStatus(t, recorder, http.StatusBadRequest)
			if envelope.Error.Code != platform.CodeInvalidArgument {
				t.Fatalf("%s code=%q want invalid_argument", target, envelope.Error.Code)
			}
		}
	})
}

func TestProfilePatchRejectsUnknownFieldsAndBuiltinKey(t *testing.T) {
	t.Run("key is rejected", func(t *testing.T) {
		handler := newTestHandler(nil, nil, nil, nil)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"key":"evil"}`)
		envelope := assertStatus(t, recorder, http.StatusBadRequest)
		if envelope.Error.Code != platform.CodeInvalidArgument {
			t.Fatalf("code=%q want invalid_argument", envelope.Error.Code)
		}
	})

	t.Run("builtin is rejected", func(t *testing.T) {
		handler := newTestHandler(nil, nil, nil, nil)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"builtin":false}`)
		assertStatus(t, recorder, http.StatusBadRequest)
	})

	t.Run("unknown field is rejected", func(t *testing.T) {
		handler := newTestHandler(nil, nil, nil, nil)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"nope":1}`)
		assertStatus(t, recorder, http.StatusBadRequest)
	})
}

func TestProfilePatchMapsAllowedToolsAndValidationErrors(t *testing.T) {
	tools := &fakeToolCatalog{infos: []domain.ToolInfo{{Name: "test.echo"}, {Name: "file.read"}}}
	profiles := &fakeProfileService{updateFn: func(context.Context, int64, domain.UpdateParams, *int64) (domain.AgentProfile, error) {
		return testProfile(), nil
	}}

	t.Run("unknown allowed tool is 422 with the field", func(t *testing.T) {
		handler := newTestHandler(profiles, nil, nil, tools)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1",
			`{"allowed_tools":["test.echo","file.read","missing.tool"]}`)
		envelope := assertStatus(t, recorder, http.StatusUnprocessableEntity)
		if envelope.Error.Code != platform.CodeInvalidArgument {
			t.Fatalf("code=%q want invalid_argument", envelope.Error.Code)
		}
		if got := envelope.Error.Details["field"]; got != "allowed_tools" {
			t.Fatalf("details.field=%v want allowed_tools", got)
		}
		if profiles.updateCalls != 0 {
			t.Fatalf("service Update called %d times, want 0", profiles.updateCalls)
		}
	})

	t.Run("registered allowed tools pass through", func(t *testing.T) {
		before := profiles.updateCalls
		handler := newTestHandler(profiles, nil, nil, tools)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1",
			`{"allowed_tools":["test.echo","file.read"]}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if profiles.updateCalls != before+1 {
			t.Fatalf("service Update calls=%d want %d", profiles.updateCalls, before+1)
		}
		if profiles.lastParams.AllowedTools == nil || len(*profiles.lastParams.AllowedTools) != 2 {
			t.Fatalf("params.AllowedTools=%v want the requested two tools", profiles.lastParams.AllowedTools)
		}
	})

	t.Run("domain validation error is 422 with the failing field", func(t *testing.T) {
		invalid := &fakeProfileService{updateFn: func(context.Context, int64, domain.UpdateParams, *int64) (domain.AgentProfile, error) {
			return domain.AgentProfile{}, fmt.Errorf("agent: max_turns 200 must be between 0 and 100")
		}}
		handler := newTestHandler(invalid, nil, nil, tools)
		recorder := serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"max_turns":200}`)
		envelope := assertStatus(t, recorder, http.StatusUnprocessableEntity)
		if got := envelope.Error.Details["field"]; got != "max_turns" {
			t.Fatalf("details.field=%v want max_turns", got)
		}
	})

	t.Run("missing profile and internal errors", func(t *testing.T) {
		missing := &fakeProfileService{updateFn: func(context.Context, int64, domain.UpdateParams, *int64) (domain.AgentProfile, error) {
			return domain.AgentProfile{}, domain.ErrNotFound
		}}
		handler := newTestHandler(missing, nil, nil, tools)
		assertStatus(t, serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"model":"x"}`), http.StatusNotFound)

		broken := &fakeProfileService{updateFn: func(context.Context, int64, domain.UpdateParams, *int64) (domain.AgentProfile, error) {
			return domain.AgentProfile{}, errors.New("database is down")
		}}
		handler = newTestHandler(broken, nil, nil, tools)
		assertStatus(t, serve(t, handler, http.MethodPatch, "/api/agents/profiles/1", `{"model":"x"}`), http.StatusInternalServerError)
	})
}

func TestProfilePatchUpdatesProfile(t *testing.T) {
	profiles := &fakeProfileService{updateFn: func(_ context.Context, id int64, params domain.UpdateParams, _ *int64) (domain.AgentProfile, error) {
		updated := testProfile()
		updated.Model = "deepseek-chat"
		return updated, nil
	}}
	handler := newTestHandler(profiles, nil, nil, nil)
	actorCtx := auditapp.WithActorID(context.Background(), 7)

	recorder := serveWithContext(t, handler, actorCtx, http.MethodPatch, "/api/agents/profiles/1",
		`{"model":"deepseek-chat","enabled":false}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	if profiles.lastID != 1 {
		t.Fatalf("service id=%d want 1", profiles.lastID)
	}
	if profiles.lastParams.Model == nil || *profiles.lastParams.Model != "deepseek-chat" {
		t.Fatalf("params.Model=%v want deepseek-chat", profiles.lastParams.Model)
	}
	if profiles.lastParams.Enabled == nil || *profiles.lastParams.Enabled {
		t.Fatalf("params.Enabled=%v want false", profiles.lastParams.Enabled)
	}
	if profiles.lastActor == nil || *profiles.lastActor != 7 {
		t.Fatalf("actor=%v want 7 from the request context", profiles.lastActor)
	}
	var response struct {
		Data struct {
			ID        int64  `json:"id"`
			Key       string `json:"key"`
			Model     string `json:"model"`
			Builtin   bool   `json:"builtin"`
			ToolCount int    `json:"-"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Data.ID != 1 || response.Data.Key != "runtime-diagnostic" || response.Data.Model != "deepseek-chat" {
		t.Fatalf("profile payload=%+v want the service result", response.Data)
	}
}

func TestProfilesListAndGet(t *testing.T) {
	profile := testProfile()
	profiles := &fakeProfileService{
		listFn: func(context.Context) ([]domain.AgentProfile, error) {
			return []domain.AgentProfile{profile}, nil
		},
		getFn: func(_ context.Context, id int64) (domain.AgentProfile, error) {
			if id == 1 {
				return profile, nil
			}
			return domain.AgentProfile{}, domain.ErrNotFound
		},
	}
	handler := newTestHandler(profiles, nil, nil, nil)

	recorder := serve(t, handler, http.MethodGet, "/api/agents/profiles", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var list struct {
		Data []struct {
			ID          int64    `json:"id"`
			Key         string   `json:"key"`
			AllowedTool []string `json:"allowed_tools"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].ID != 1 || list.Data[0].Key != "runtime-diagnostic" {
		t.Fatalf("profiles=%+v want the configured profile", list.Data)
	}
	if len(list.Data[0].AllowedTool) != 5 {
		t.Fatalf("allowed_tools=%v want the five builtin tools", list.Data[0].AllowedTool)
	}

	recorder = serve(t, handler, http.MethodGet, "/api/agents/profiles/1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	assertStatus(t, serve(t, handler, http.MethodGet, "/api/agents/profiles/99", ""), http.StatusNotFound)
}

func TestCreateSessionUsesDefaultProfileAndMapsErrors(t *testing.T) {
	t.Run("created", func(t *testing.T) {
		sessions := &fakeSessionService{createFn: func(context.Context, int64, string, *int64) (domain.Session, error) {
			return testSession(), nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)
		actorCtx := auditapp.WithActorID(context.Background(), 3)

		recorder := serveWithContext(t, handler, actorCtx, http.MethodPost, "/api/agents/sessions",
			`{"profile_id":0,"title":"检查运行时健康"}`)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status=%d want 201 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.createdProfileID != 0 || sessions.createdTitle != "检查运行时健康" {
			t.Fatalf("Create profile=%d title=%q want 0/检查运行时健康", sessions.createdProfileID, sessions.createdTitle)
		}
		if sessions.createdActor == nil || *sessions.createdActor != 3 {
			t.Fatalf("Create actor=%v want 3 from the request context", sessions.createdActor)
		}
		if !strings.Contains(recorder.Body.String(), `"profile_key":"runtime-diagnostic"`) {
			t.Fatalf("body=%s want the session payload", recorder.Body.String())
		}
	})

	t.Run("disabled profile", func(t *testing.T) {
		sessions := &fakeSessionService{createFn: func(context.Context, int64, string, *int64) (domain.Session, error) {
			return domain.Session{}, domain.ErrProfileDisabled
		}}
		handler := newTestHandler(nil, sessions, nil, nil)
		envelope := assertStatus(t, serve(t, handler, http.MethodPost, "/api/agents/sessions", `{}`),
			http.StatusConflict)
		if got := envelope.Error.Details["reason"]; got != "profile_disabled" {
			t.Fatalf("details.reason=%v want profile_disabled", got)
		}
	})

	t.Run("missing profile", func(t *testing.T) {
		sessions := &fakeSessionService{createFn: func(context.Context, int64, string, *int64) (domain.Session, error) {
			return domain.Session{}, domain.ErrNotFound
		}}
		handler := newTestHandler(nil, sessions, nil, nil)
		assertStatus(t, serve(t, handler, http.MethodPost, "/api/agents/sessions", `{"profile_id":99}`), http.StatusNotFound)
	})

	t.Run("empty body uses the builtin defaults", func(t *testing.T) {
		sessions := &fakeSessionService{createFn: func(context.Context, int64, string, *int64) (domain.Session, error) {
			return testSession(), nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodPost, "/api/agents/sessions", "")
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status=%d want 201 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.createCalls != 1 || sessions.createdProfileID != 0 || sessions.createdTitle != "" {
			t.Fatalf("Create calls=%d profile=%d title=%q want 1/0/empty",
				sessions.createCalls, sessions.createdProfileID, sessions.createdTitle)
		}
	})
}

func TestGetSessionUpdateTitleAndDelete(t *testing.T) {
	t.Run("get returns the session detail", func(t *testing.T) {
		detail := domain.SessionDetail{
			Session:   testSession(),
			Running:   true,
			ActiveRun: ptrRun(testRun(9, domain.RunRunning)),
			LastRun:   ptrRun(testRun(8, domain.RunDone)),
		}
		sessions := &fakeSessionService{getFn: func(context.Context, int64) (domain.SessionDetail, error) {
			return detail, nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet, "/api/agents/sessions/4", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		body := recorder.Body.String()
		for _, want := range []string{`"running":true`, `"active_run":{"id":9`, `"last_run":{"id":8`, `"profile_key":"runtime-diagnostic"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("body=%s missing %s", body, want)
			}
		}
	})

	t.Run("missing session is 404", func(t *testing.T) {
		sessions := &fakeSessionService{getFn: func(context.Context, int64) (domain.SessionDetail, error) {
			return domain.SessionDetail{}, domain.ErrNotFound
		}}
		handler := newTestHandler(nil, sessions, nil, nil)
		assertStatus(t, serve(t, handler, http.MethodGet, "/api/agents/sessions/404", ""), http.StatusNotFound)
	})

	t.Run("title patch is validated and forwarded", func(t *testing.T) {
		sessions := &fakeSessionService{updateTitleFn: func(_ context.Context, id int64, title string) (domain.Session, error) {
			session := testSession()
			session.Title = title
			return session, nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodPatch, "/api/agents/sessions/4", `{"title":"新标题"}`)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.updateTitleCalls != 1 || sessions.updatedTitleID != 4 || sessions.updatedTitle != "新标题" {
			t.Fatalf("UpdateTitle calls=%d id=%d title=%q want 1/4/新标题",
				sessions.updateTitleCalls, sessions.updatedTitleID, sessions.updatedTitle)
		}

		overlong := `{"title":"` + strings.Repeat("题", 121) + `"}`
		before := sessions.updateTitleCalls
		envelope := assertStatus(t, serve(t, handler, http.MethodPatch, "/api/agents/sessions/4", overlong),
			http.StatusUnprocessableEntity)
		if got := envelope.Error.Details["field"]; got != "title" {
			t.Fatalf("details.field=%v want title", got)
		}
		if sessions.updateTitleCalls != before {
			t.Fatalf("UpdateTitle called %d times for an invalid title", sessions.updateTitleCalls-before)
		}

		assertStatus(t, serve(t, handler, http.MethodPatch, "/api/agents/sessions/4", `{}`),
			http.StatusUnprocessableEntity)
	})

	t.Run("delete aborts via the service and maps run_active", func(t *testing.T) {
		sessions := &fakeSessionService{}
		handler := newTestHandler(nil, sessions, nil, nil)
		actorCtx := auditapp.WithActorID(context.Background(), 9)

		recorder := serveWithContext(t, handler, actorCtx, http.MethodDelete, "/api/agents/sessions/4", "")
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status=%d want 204 (body=%s)", recorder.Code, recorder.Body.String())
		}
		if sessions.deleteCalls != 1 || sessions.deletedID != 4 {
			t.Fatalf("Delete calls=%d id=%d want 1/4", sessions.deleteCalls, sessions.deletedID)
		}
		if sessions.deletedActor == nil || *sessions.deletedActor != 9 {
			t.Fatalf("Delete actor=%v want 9 from the request context", sessions.deletedActor)
		}

		active := &fakeSessionService{deleteFn: func(context.Context, int64, *int64) error {
			return domain.ErrRunActive
		}}
		handler = newTestHandler(nil, active, nil, nil)
		envelope := assertStatus(t, serve(t, handler, http.MethodDelete, "/api/agents/sessions/4", ""),
			http.StatusConflict)
		if got := envelope.Error.Details["reason"]; got != "run_active" {
			t.Fatalf("details.reason=%v want run_active", got)
		}
	})
}

func TestGetMessageRequiresMessageInSession(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		sessions := &fakeSessionService{messageFn: func(context.Context, int64, int64) (domain.Message, error) {
			return domain.Message{}, domain.ErrNotFound
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet, "/api/agents/sessions/1/messages/42", "")
		assertStatus(t, recorder, http.StatusNotFound)
		if sessions.messageCalls != 1 || sessions.messageSessionID != 1 || sessions.messageIDArg != 42 {
			t.Fatalf("Message calls=%d session=%d message=%d want 1/1/42",
				sessions.messageCalls, sessions.messageSessionID, sessions.messageIDArg)
		}
	})

	t.Run("found", func(t *testing.T) {
		message := domain.Message{
			ID:        42,
			SessionID: 1,
			RunID:     ptrInt64(9),
			Role:      "assistant",
			Parts:     []domain.Part{{Type: "text", Text: "诊断完成"}},
			Reasoning: "先读取 README",
			ToolCalls: []domain.ToolCall{{ID: "call_1", Name: "file.read", Arguments: `{"path":"README.md"}`}},
			Usage:     &domain.Usage{PromptTokens: 10, CompletionTokens: 2, ReasoningTokens: 3, TotalTokens: 15},
			CreatedAt: time.Date(2026, 10, 27, 12, 1, 0, 0, time.UTC),
		}
		sessions := &fakeSessionService{messageFn: func(context.Context, int64, int64) (domain.Message, error) {
			return message, nil
		}}
		handler := newTestHandler(nil, sessions, nil, nil)

		recorder := serve(t, handler, http.MethodGet, "/api/agents/sessions/1/messages/42", "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
		}
		var response struct {
			Data struct {
				ID        int64  `json:"id"`
				SessionID int64  `json:"session_id"`
				RunID     *int64 `json:"run_id"`
				Role      string `json:"role"`
				Content   []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				Reasoning string `json:"reasoning"`
				ToolCalls []struct {
					ID string `json:"id"`
				} `json:"tool_calls"`
				CreateAt time.Time `json:"created_at"`
				Usage    *struct {
					TotalTokens int `json:"total_tokens"`
				} `json:"usage"`
			} `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		data := response.Data
		if data.ID != 42 || data.SessionID != 1 || data.Role != "assistant" {
			t.Fatalf("message=%+v want id=42 session=1 assistant", data)
		}
		if data.RunID == nil || *data.RunID != 9 {
			t.Fatalf("run_id=%v want 9", data.RunID)
		}
		if len(data.Content) != 1 || data.Content[0].Text != "诊断完成" {
			t.Fatalf("content=%+v want one text part", data.Content)
		}
		if data.Reasoning != "先读取 README" {
			t.Fatalf("reasoning=%q want the stored reasoning", data.Reasoning)
		}
		if len(data.ToolCalls) != 1 || data.ToolCalls[0].ID != "call_1" {
			t.Fatalf("tool_calls=%+v want call_1", data.ToolCalls)
		}
		if data.Usage == nil || data.Usage.TotalTokens != 15 {
			t.Fatalf("usage=%+v want total_tokens 15", data.Usage)
		}
	})
}

func TestStopReturnsIdleOrStopping(t *testing.T) {
	cases := []string{"stopping", "idle"}
	for _, status := range cases {
		t.Run(status, func(t *testing.T) {
			runs := &fakeRunControl{abortFn: func(_ context.Context, sessionID int64, _ *int64) (domain.AbortResult, error) {
				return domain.AbortResult{Status: status}, nil
			}}
			handler := newTestHandler(nil, nil, runs, nil)

			recorder := serve(t, handler, http.MethodPost, "/api/agents/sessions/4/stop", "")
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
			}
			var response struct {
				Data struct {
					Status string `json:"status"`
				} `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("decode response: %v", err)
			}
			if response.Data.Status != status {
				t.Fatalf("status=%q want %q", response.Data.Status, status)
			}
			if runs.abortCalls != 1 || runs.abortSession != 4 {
				t.Fatalf("Abort calls=%d session=%d want 1/4", runs.abortCalls, runs.abortSession)
			}
		})
	}

	t.Run("not found", func(t *testing.T) {
		runs := &fakeRunControl{abortFn: func(context.Context, int64, *int64) (domain.AbortResult, error) {
			return domain.AbortResult{}, domain.ErrNotFound
		}}
		handler := newTestHandler(nil, nil, runs, nil)
		assertStatus(t, serve(t, handler, http.MethodPost, "/api/agents/sessions/404/stop", ""),
			http.StatusNotFound)
	})
}

func TestToolsList(t *testing.T) {
	tools := &fakeToolCatalog{infos: []domain.ToolInfo{
		{Name: "file.read", Description: "read a file", Schema: json.RawMessage(`{"type":"object"}`)},
		{Name: "test.echo", Description: "echo text", Schema: json.RawMessage(`{"type":"object"}`)},
	}}
	handler := newTestHandler(nil, nil, nil, tools)

	recorder := serve(t, handler, http.MethodGet, "/api/agents/tools", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d want 200 (body=%s)", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data []struct {
			Name        string          `json:"name"`
			Description string          `json:"description"`
			Schema      json.RawMessage `json:"schema"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(response.Data) != 2 {
		t.Fatalf("tools=%d want 2", len(response.Data))
	}
	if response.Data[0].Name != "file.read" || response.Data[0].Description != "read a file" {
		t.Fatalf("first tool=%+v want file.read", response.Data[0])
	}
	if string(response.Data[0].Schema) != `{"type":"object"}` {
		t.Fatalf("schema=%s want the registry schema", response.Data[0].Schema)
	}
}

func TestRegisterRoutesWrapsEveryRouteInGuard(t *testing.T) {
	routes := []struct {
		method string
		target string
	}{
		{http.MethodGet, "/api/agents/profiles"},
		{http.MethodGet, "/api/agents/profiles/1"},
		{http.MethodPatch, "/api/agents/profiles/1"},
		{http.MethodGet, "/api/agents/tools"},
		{http.MethodGet, "/api/agents/sessions"},
		{http.MethodPost, "/api/agents/sessions"},
		{http.MethodGet, "/api/agents/sessions/1"},
		{http.MethodPatch, "/api/agents/sessions/1"},
		{http.MethodDelete, "/api/agents/sessions/1"},
		{http.MethodPost, "/api/agents/sessions/1/messages"},
		{http.MethodPost, "/api/agents/sessions/1/stop"},
		{http.MethodGet, "/api/agents/sessions/1/messages/2"},
	}
	guardCalls := 0
	guard := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			guardCalls++
			platform.WriteError(w, nil, http.StatusForbidden, platform.CodeForbidden, "admin role required")
		})
	}
	mux := http.NewServeMux()
	RegisterRoutes(mux, newTestHandler(nil, nil, nil, nil), guard)

	for _, route := range routes {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(route.method, route.target, strings.NewReader(`{}`))
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Fatalf("%s %s status=%d want the guard's 403 (body=%s)",
				route.method, route.target, recorder.Code, recorder.Body.String())
		}
	}
	if guardCalls != len(routes) {
		t.Fatalf("guard calls=%d want %d (every route must be wrapped)", guardCalls, len(routes))
	}
}

// ptrRun returns a pointer to run.
func ptrRun(run domain.Run) *domain.Run { return &run }
