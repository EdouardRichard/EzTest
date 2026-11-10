package server

// Task 37 W0 wiring tests: the contract paths the plan names must be mounted
// (never 404), stage gate/transition rejections must become the typed 409
// envelopes, API-8 messages must default to the newest page with a working
// `before` cursor, and the attachment image routes must verify version ownership
// before reading (H7).

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	executionhttp "github.com/EdouardRichard/EzTest/backend/internal/execution/interfaces/httpapi"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	resourceapp "github.com/EdouardRichard/EzTest/backend/internal/resource/application"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// stubStageService is the narrow v0.22 stage surface used by the route tests.
type stubStageService struct {
	view       versionapp.StageView
	requestErr error
}

func (s stubStageService) Stage(_ context.Context, versionID int64) (versionapp.StageView, error) {
	if s.view.VersionID == 0 {
		return versionapp.StageView{VersionID: versionID, Stage: version.StageClarifying}, nil
	}
	return s.view, nil
}

func (s stubStageService) Request(context.Context, int64, version.Stage) error { return s.requestErr }
func (s stubStageService) Advance(context.Context, int64) error                { return s.requestErr }
func (s stubStageService) Block(context.Context, int64, versionapp.Blocker) error {
	return s.requestErr
}
func (s stubStageService) Unblock(context.Context, int64) error { return s.requestErr }
func (s stubStageService) Pause(context.Context, int64, string) error {
	return s.requestErr
}
func (s stubStageService) Resume(context.Context, int64) error { return s.requestErr }
func (s stubStageService) Reject(ctx context.Context, versionID int64, _ string, _ *version.Stage) error {
	return s.requestErr
}

// actorAuth injects a user actor so the handlers reach the stage service.
func actorAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := platform.WithActor(r.Context(), platform.Actor{ID: 42, Role: "admin"})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newW0Mux assembles the W0 route table behind denyAuth. It mirrors the
// production registration calls (registerW0Routes/registerStageRoutes/
// registerP6Routes/execution Register) without a database.
func newW0Mux(t *testing.T) *http.ServeMux {
	t.Helper()
	deps := Deps{
		Stage:    stubStageService{},
		Resource: resourceapp.New(nil, nil, nil, nil, nil, nil),
	}
	stageHandler := versionapi.NewStageHandler(newStageHTTPAdapter(deps))
	stageHandler.SetStageWorkflow(deps.Stage)
	mux := http.NewServeMux()
	registerStageRoutes(mux, denyAuth, stageHandler)
	registerW0Routes(mux, denyAuth, deps)
	registerP6Routes(mux, denyAuth, denyAuth, &P6Handlers{
		VersionRead: versionapi.NewVersionReadHandler(nil, nil),
	})
	executionhttp.New(executionhttp.Deps{}).Register(mux, denyAuth, denyAuth)
	registerConversationRoutes(mux, denyAuth, versionapi.NewConversationHandler(nil))
	return mux
}

// TestV022W0RoutesMounted is the Task 37 route table assertion: every W0
// contract path exists (a mounted route answers 401 behind denyAuth, never 404).
func TestV022W0RoutesMounted(t *testing.T) {
	mux := newW0Mux(t)
	cases := []struct{ method, path string }{
		// Plan / facts.
		{"GET", "/api/versions/7/plan-items"},
		{"PATCH", "/api/versions/7/plan-items/1"},
		{"GET", "/api/versions/7/facts"},
		{"POST", "/api/versions/7/facts/1/arbitrate"},
		// Handoff.
		{"GET", "/api/versions/7/handoff"},
		// Questions.
		{"GET", "/api/versions/7/questions?status=pending"},
		{"POST", "/api/versions/7/conversations/8/questions"},
		{"POST", "/api/versions/7/questions/1/answer"},
		{"POST", "/api/versions/7/questions/1/skip"},
		{"POST", "/api/versions/7/questions/1/reopen"},
		// Extraction.
		{"GET", "/api/versions/7/requirements/extractions"},
		{"POST", "/api/versions/7/requirements/extractions"},
		{"POST", "/api/versions/7/requirements/extractions/9/pause"},
		{"POST", "/api/versions/7/requirements/extractions/9/resume"},
		{"POST", "/api/versions/7/requirements/extractions/9/retry"},
		// Attachment images (version-scoped).
		{"GET", "/api/versions/7/attachments/9/images"},
		{"POST", "/api/versions/7/attachments/9/reanalyze"},
		// Binding / repositories / accounts.
		{"GET", "/api/versions/7/binding"},
		{"PUT", "/api/versions/7/binding"},
		{"PATCH", "/api/versions/7/binding"},
		{"GET", "/api/suts/9/repositories"},
		{"POST", "/api/suts/9/repositories"},
		{"GET", "/api/versions/7/accounts"},
		{"POST", "/api/versions/7/accounts"},
		// Stage.
		{"GET", "/api/versions/7/stage"},
		{"POST", "/api/versions/7/stage/request"},
		{"POST", "/api/versions/7/stage/advance"},
		{"POST", "/api/versions/7/stage/block"},
		{"POST", "/api/versions/7/stage/unblock"},
		{"POST", "/api/versions/7/stage/pause"},
		{"POST", "/api/versions/7/stage/resume"},
		{"POST", "/api/versions/7/stage/reject"},
	}
	for _, tc := range cases {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(tc.method, tc.path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s = %d, want 401 (mounted behind auth, never 404)", tc.method, tc.path, recorder.Code)
		}
	}
}

// TestV022W0StageRejectUsesAuditedBackEdge pins the contract: the legacy reject
// route now answers 200 (not a permanent 409) by calling the audited
// StageService back edge.
func TestV022W0StageRejectUsesAuditedBackEdge(t *testing.T) {
	stage := &adapterStage{stage: version.StageRequirementReview}
	deps := Deps{Stage: stage}
	handler := versionapi.NewStageHandler(newStageHTTPAdapter(deps))
	handler.SetStageWorkflow(stage)
	mux := http.NewServeMux()
	mux.Handle("POST /api/versions/{id}/stage/reject", actorAuth(http.HandlerFunc(handler.Reject)))
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
		"/api/versions/7/stage/reject", strings.NewReader(`{"reason":"missing acceptance criteria"}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("stage reject = %d body=%s, want 200", recorder.Code, recorder.Body.String())
	}
	if stage.rejects != 1 {
		t.Fatalf("stage-service reject calls = %d, want 1", stage.rejects)
	}
}

// TestNormalizeDepsTypedNil pins the degraded-assembly guard: an interface
// holding a typed nil pointer must be cleared so the handler's nil check runs
// (503) instead of a method call on a nil receiver.
func TestNormalizeDepsTypedNil(t *testing.T) {
	var stage StageService = (*stubStageService)(nil)
	var extraction versionapi.ExtractionService = (*versionapp.ExtractionService)(nil)
	deps := normalizeDeps(Deps{Stage: stage, Extraction: extraction})
	if deps.Stage != nil {
		t.Fatalf("typed-nil Stage was not cleared")
	}
	if deps.Extraction != nil {
		t.Fatalf("typed-nil Extraction was not cleared")
	}
	if normalizeDeps(Deps{}).Stage != nil {
		t.Fatalf("nil Stage changed")
	}
}

// TestV022W0StageTypedErrors pins the Task 25 error mapping at the HTTP edge:
// *StageGateError -> 409 stage_gate_failed and *StageTransitionError -> 409
// invalid_stage_transition, with the actionable details intact.
func TestV022W0StageTypedErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode string
	}{
		{
			name: "gate",
			err: &versionapp.StageGateError{
				Gate: "executor", Reason: "executor_offline",
				Action: "start the executor", Next: "executing",
			},
			wantCode: "stage_gate_failed",
		},
		{
			name:     "transition",
			err:      &versionapp.StageTransitionError{From: version.StageCreated, To: version.StageCompleted, Action: "walk the workflow"},
			wantCode: "invalid_stage_transition",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			deps := Deps{Stage: stubStageService{requestErr: tc.err}}
			handler := versionapi.NewStageHandler(newStageHTTPAdapter(deps))
			handler.SetStageWorkflow(deps.Stage)
			mux := http.NewServeMux()
			mux.Handle("POST /api/versions/{id}/stage/request", actorAuth(http.HandlerFunc(handler.Request)))

			recorder := httptest.NewRecorder()
			mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost,
				"/api/versions/7/stage/request", strings.NewReader(`{"to":"designing"}`)))
			if recorder.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409: %s", recorder.Code, recorder.Body.String())
			}
			var envelope struct {
				Error struct {
					Code    string         `json:"code"`
					Details map[string]any `json:"details"`
				} `json:"error"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if envelope.Error.Code != tc.wantCode {
				t.Fatalf("code = %q, want %q", envelope.Error.Code, tc.wantCode)
			}
			if len(envelope.Error.Details) == 0 {
				t.Fatalf("details are empty: %s", recorder.Body.String())
			}
		})
	}
}

// stubConversationService serves a fixed ascending message history.
type stubConversationService struct {
	messages []versionapp.MessageView
}

func (s *stubConversationService) CreateWithModel(context.Context, int64, string, string, *string, int64) (versionapp.ConversationView, error) {
	return versionapp.ConversationView{}, nil
}

func (s *stubConversationService) List(context.Context, int64, string, int, *int64) ([]versionapp.ConversationView, bool, error) {
	return nil, false, nil
}

func (s *stubConversationService) Get(context.Context, int64, int64) (versionapp.ConversationView, error) {
	return versionapp.ConversationView{}, nil
}

func (s *stubConversationService) SetModelOverride(context.Context, int64, int64, *string) (versionapp.ConversationView, error) {
	return versionapp.ConversationView{}, nil
}

func (s *stubConversationService) Messages(_ context.Context, _, _ int64, after int64, limit int) ([]versionapp.MessageView, bool, error) {
	filtered := make([]versionapp.MessageView, 0, len(s.messages))
	for _, message := range s.messages {
		if message.ID > after {
			filtered = append(filtered, message)
		}
	}
	hasMore := len(filtered) > limit
	if hasMore {
		filtered = filtered[:limit]
	}
	return filtered, hasMore, nil
}

func (s *stubConversationService) PostUserMessage(context.Context, int64, int64, []version.MessagePart, string, int64) (versionapp.MessageView, error) {
	return versionapp.MessageView{}, nil
}

type messagePageEnvelope struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
	NextCursor string `json:"next_cursor"`
	HasMore    bool   `json:"has_more"`
}

// TestV022W0MessagesLatestPagePagination pins API-8/H4 at the HTTP edge: no
// cursor returns the newest page, `before` walks older pages and the envelope
// carries has_more/next_cursor.
func TestV022W0MessagesLatestPagePagination(t *testing.T) {
	history := make([]versionapp.MessageView, 0, 250)
	for id := int64(1); id <= 250; id++ {
		history = append(history, versionapp.MessageView{ID: id, ConversationID: 8, Role: "user"})
	}
	stub := &stubConversationService{messages: history}
	mux := http.NewServeMux()
	registerConversationRoutes(mux, actorAuth, versionapi.NewConversationHandler(stub))

	get := func(path string) messagePageEnvelope {
		t.Helper()
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, recorder.Code, recorder.Body.String())
		}
		var envelope messagePageEnvelope
		if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode %s: %v", path, err)
		}
		return envelope
	}

	latest := get("/api/versions/7/conversations/8/messages?limit=100")
	if len(latest.Data) != 100 || latest.Data[0].ID != "151" || latest.Data[99].ID != "250" {
		t.Fatalf("latest page = %d rows, first %q last %q, want 151..250",
			len(latest.Data), latest.Data[0].ID, latest.Data[len(latest.Data)-1].ID)
	}
	if !latest.HasMore || latest.NextCursor != "151" {
		t.Fatalf("latest envelope = has_more %v next %q, want true/151", latest.HasMore, latest.NextCursor)
	}

	older := get("/api/versions/7/conversations/8/messages?before=151&limit=100")
	if len(older.Data) != 100 || older.Data[0].ID != "51" || older.Data[99].ID != "150" {
		t.Fatalf("older page = %d rows, first %q last %q, want 51..150",
			len(older.Data), older.Data[0].ID, older.Data[len(older.Data)-1].ID)
	}
	if !older.HasMore || older.NextCursor != "51" {
		t.Fatalf("older envelope = has_more %v next %q, want true/51", older.HasMore, older.NextCursor)
	}

	oldest := get("/api/versions/7/conversations/8/messages?before=51&limit=100")
	if len(oldest.Data) != 50 || oldest.Data[0].ID != "1" || oldest.HasMore || oldest.NextCursor != "" {
		t.Fatalf("oldest page = %+v, want 1..50 without more", oldest)
	}
}

// stubAttachmentService implements the image read plus the H7 ownership check.
type stubAttachmentService struct {
	getErr    error
	listCalls int
}

func (s *stubAttachmentService) Get(_ context.Context, versionID, attachmentID int64) (versionapp.AttachmentView, error) {
	if s.getErr != nil {
		return versionapp.AttachmentView{}, s.getErr
	}
	return versionapp.AttachmentView{ID: attachmentID, Name: "shot.png"}, nil
}

func (s *stubAttachmentService) ListImages(context.Context, int64) ([]versionapp.AttachmentImageView, error) {
	s.listCalls++
	return []versionapp.AttachmentImageView{{ID: 11, AttachmentID: 9, Seq: 1, Status: "done"}}, nil
}

func (s *stubAttachmentService) Reanalyze(context.Context, int64) ([]versionapp.AttachmentImageView, error) {
	s.listCalls++
	return nil, nil
}

// ListVersionAttachments completes the attachment route surface for the C-05
// version list; this wiring test only exercises the H7 image scope.
func (s *stubAttachmentService) ListVersionAttachments(context.Context, int64) ([]versionapp.VersionAttachmentView, error) {
	return nil, nil
}

// TestV022W0AttachmentImagesRequireVersionScope pins H7: the version-scoped
// image route rejects an attachment that does not belong to the addressed
// version before touching the image read.
func TestV022W0AttachmentImagesRequireVersionScope(t *testing.T) {
	stub := &stubAttachmentService{getErr: platform.NotFound("attachment")}
	handler := versionapi.NewAttachmentHandler(stub)
	handler.SetVersionResolver(stub)
	mux := http.NewServeMux()
	mux.Handle("GET /api/versions/{id}/attachments/{attachmentId}/images",
		actorAuth(http.HandlerFunc(handler.ListImagesForVersion)))

	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions/7/attachments/9/images", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("cross-version attachment = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}
	if stub.listCalls != 0 {
		t.Fatalf("image read ran for a foreign attachment (%d calls)", stub.listCalls)
	}

	stub.getErr = nil
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/versions/7/attachments/9/images", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("owned attachment = %d, want 200: %s", recorder.Code, recorder.Body.String())
	}
}
