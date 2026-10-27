package httpapi

import (
	"context"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// anchorWire is the Anchor DTO shape of frontend types.ts (R6): anchor/version/
// subject ids are decimal strings, createdBy stays a number, and the location
// fields are optional (null when absent).
type anchorWire struct {
	ID            string  `json:"id"`
	VersionID     string  `json:"versionId"`
	SubjectKind   string  `json:"subjectKind"`
	SubjectID     string  `json:"subjectId"`
	FilePath      string  `json:"filePath"`
	StartLine     *int    `json:"startLine"`
	EndLine       *int    `json:"endLine"`
	CommitSHA     *string `json:"commitSha"`
	Confidence    string  `json:"confidence"`
	CreatedByKind string  `json:"createdByKind"`
	CreatedBy     int64   `json:"createdBy"`
	CreatedAt     string  `json:"createdAt"`
}

// anchorCreateCall records one AnchorService.Create invocation.
type anchorCreateCall struct {
	VersionID int64
	ActorID   int64
	Params    domain.CreateAnchorParams
}

// anchorListCall records one AnchorService.List invocation.
type anchorListCall struct {
	VersionID int64
	Filter    domain.AnchorFilter
}

// anchorDeleteCall records one AnchorService.Delete invocation.
type anchorDeleteCall struct {
	VersionID int64
	AnchorID  int64
}

// fakeAnchorService is the hand-written application.AnchorService consumer
// double. Each method records its arguments and delegates to the configured
// function.
type fakeAnchorService struct {
	createFn func(context.Context, int64, int64, domain.CreateAnchorParams) (domain.CodeAnchor, error)
	listFn   func(context.Context, int64, domain.AnchorFilter) ([]domain.CodeAnchor, error)
	deleteFn func(context.Context, int64, int64) error

	createCalls []anchorCreateCall
	listCalls   []anchorListCall
	deleteCalls []anchorDeleteCall
}

func (f *fakeAnchorService) Create(ctx context.Context, versionID, actorID int64, params domain.CreateAnchorParams) (domain.CodeAnchor, error) {
	f.createCalls = append(f.createCalls, anchorCreateCall{VersionID: versionID, ActorID: actorID, Params: params})
	if f.createFn == nil {
		return domain.CodeAnchor{}, nil
	}
	return f.createFn(ctx, versionID, actorID, params)
}

func (f *fakeAnchorService) List(ctx context.Context, versionID int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error) {
	f.listCalls = append(f.listCalls, anchorListCall{VersionID: versionID, Filter: filter})
	if f.listFn == nil {
		return nil, nil
	}
	return f.listFn(ctx, versionID, filter)
}

func (f *fakeAnchorService) Delete(ctx context.Context, versionID, anchorID int64) error {
	f.deleteCalls = append(f.deleteCalls, anchorDeleteCall{VersionID: versionID, AnchorID: anchorID})
	if f.deleteFn == nil {
		return nil
	}
	return f.deleteFn(ctx, versionID, anchorID)
}

// testAnchor builds one persisted requirement anchor fixture without a line
// range or commit pin.
func testAnchor(id, subjectID int64) domain.CodeAnchor {
	return domain.CodeAnchor{
		ID:            id,
		VersionID:     42,
		RepoID:        3,
		SubjectKind:   domain.SubjectRequirement,
		SubjectID:     subjectID,
		FilePath:      "src/api/refund.ts",
		Confidence:    domain.ConfidenceHigh,
		CreatedByKind: "user",
		CreatedBy:     testUserID,
		CreatedAt:     startedAt,
	}
}

// TestAnchorsCRUDAndReason covers the §7.3 anchor endpoints: create 201, list
// with the subject filter, delete 204, and the §7.1 anchor reasons (409
// duplicate, 422 unsupported subject kind / ownership, 404 missing).
func TestAnchorsCRUDAndReason(t *testing.T) {
	startLine, endLine := 10, 20

	t.Run("create returns 201 with the anchor DTO", func(t *testing.T) {
		anchor := testAnchor(6001, 9001)
		anchor.StartLine = &startLine
		anchor.EndLine = &endLine
		anchors := &fakeAnchorService{createFn: func(_ context.Context, versionID, actorID int64, params domain.CreateAnchorParams) (domain.CodeAnchor, error) {
			if versionID != 42 || actorID != testUserID {
				t.Fatalf("Create(%d, %d), want (42, %d)", versionID, actorID, testUserID)
			}
			if params.SubjectKind != domain.SubjectRequirement || params.SubjectID != 9001 ||
				params.FilePath != "src/api/refund.ts" {
				t.Fatalf("Create params = %+v", params)
			}
			if params.StartLine == nil || *params.StartLine != startLine || params.EndLine == nil || *params.EndLine != endLine {
				t.Fatalf("Create line range = %v/%v, want %d/%d", params.StartLine, params.EndLine, startLine, endLine)
			}
			return anchor, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/anchors", userToken, map[string]any{
			"subjectKind": "requirement",
			"subjectId":   "9001",
			"filePath":    "src/api/refund.ts",
			"startLine":   startLine,
			"endLine":     endLine,
		})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data anchorWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ID != "6001" || body.Data.VersionID != "42" || body.Data.SubjectID != "9001" {
			t.Fatalf("anchor ids = %+v, want decimal strings", body.Data)
		}
		if body.Data.SubjectKind != domain.SubjectRequirement || body.Data.FilePath != "src/api/refund.ts" {
			t.Fatalf("anchor = %+v", body.Data)
		}
		if body.Data.StartLine == nil || *body.Data.StartLine != startLine ||
			body.Data.EndLine == nil || *body.Data.EndLine != endLine {
			t.Fatalf("line range = %v/%v, want %d/%d", body.Data.StartLine, body.Data.EndLine, startLine, endLine)
		}
		if body.Data.CommitSHA != nil {
			t.Fatalf("commitSha = %v, want null for a user anchor", *body.Data.CommitSHA)
		}
		if body.Data.Confidence != domain.ConfidenceHigh || body.Data.CreatedByKind != "user" || body.Data.CreatedBy != testUserID {
			t.Fatalf("anchor metadata = %+v", body.Data)
		}
		if body.Data.CreatedAt == "" {
			t.Fatal("createdAt is empty, want an ISO timestamp")
		}
		if got := anchors.createCalls; len(got) != 1 || got[0].Params.SubjectID != 9001 {
			t.Fatalf("Create calls = %+v, want one", got)
		}
	})

	t.Run("an anchor without a line range keeps startLine/endLine null", func(t *testing.T) {
		anchors := &fakeAnchorService{createFn: func(_ context.Context, _ int64, _ int64, params domain.CreateAnchorParams) (domain.CodeAnchor, error) {
			if params.StartLine != nil || params.EndLine != nil {
				t.Fatalf("Create line range = %v/%v, want none", params.StartLine, params.EndLine)
			}
			return testAnchor(6002, 9001), nil
		}}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/anchors", userToken, map[string]any{
			"subjectKind": "requirement",
			"subjectId":   "9001",
			"filePath":    "src/api/refund.ts",
		})
		if recorder.Code != http.StatusCreated {
			t.Fatalf("status = %d, want 201 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data anchorWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.StartLine != nil || body.Data.EndLine != nil {
			t.Fatalf("line range = %v/%v, want null", body.Data.StartLine, body.Data.EndLine)
		}
	})

	reasonCases := []struct {
		name   string
		err    error
		status int
		code   string
		reason string
	}{
		{
			name:   "duplicate point maps to 409 anchor_duplicate",
			err:    domain.NewConflict("anchor_duplicate", "codeintel: an anchor with the same subject, file and line range already exists"),
			status: http.StatusConflict,
			code:   platform.CodeConflict,
			reason: "anchor_duplicate",
		},
		{
			name:   "a case subject kind maps to 422 unsupported_subject_kind",
			err:    domain.NewInvalid("subjectKind", "unsupported_subject_kind", "codeintel: subjectKind must be one of version, requirement or app_model"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "unsupported_subject_kind",
		},
		{
			name:   "a requirement outside the version maps to 422 subject_not_in_version",
			err:    domain.NewInvalid("subjectId", "subject_not_in_version", "codeintel: requirement does not belong to version"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "subject_not_in_version",
		},
		{
			name:   "an app model outside the system maps to 422 app_model_not_found",
			err:    domain.NewInvalid("subjectId", "app_model_not_found", "codeintel: app model does not belong to the version system"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "app_model_not_found",
		},
		{
			name:   "a half line range maps to 422 invalid_line_range",
			err:    domain.NewInvalid("startLine", "invalid_line_range", "codeintel: startLine and endLine must be provided together"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_line_range",
		},
		{
			name:   "an escaping path maps to 422 path_escape",
			err:    domain.NewInvalid("filePath", "path_escape", "codeintel: filePath escapes the repository root"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "path_escape",
		},
	}
	for _, testCase := range reasonCases {
		t.Run(testCase.name, func(t *testing.T) {
			anchors := &fakeAnchorService{createFn: func(context.Context, int64, int64, domain.CreateAnchorParams) (domain.CodeAnchor, error) {
				return domain.CodeAnchor{}, testCase.err
			}}
			handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
			recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/anchors", userToken, map[string]any{
				"subjectKind": "requirement",
				"subjectId":   "9001",
				"filePath":    "src/api/refund.ts",
			})
			assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
		})
	}

	t.Run("delete returns 204 with an empty body", func(t *testing.T) {
		anchors := &fakeAnchorService{}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodDelete, "/api/versions/42/code/anchors/6001", userToken)
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want 204 (body %s)", recorder.Code, recorder.Body.String())
		}
		if recorder.Body.Len() != 0 {
			t.Fatalf("body = %q, want empty for 204", recorder.Body.String())
		}
		want := anchorDeleteCall{VersionID: 42, AnchorID: 6001}
		if got := anchors.deleteCalls; len(got) != 1 || got[0] != want {
			t.Fatalf("Delete calls = %+v, want %+v", got, want)
		}
	})

	t.Run("deleting a missing anchor is 404 anchor_not_found", func(t *testing.T) {
		anchors := &fakeAnchorService{deleteFn: func(context.Context, int64, int64) error {
			return domain.NewNotFound("anchor_not_found", "codeintel: anchor not found")
		}}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodDelete, "/api/versions/42/code/anchors/4040", userToken)
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "anchor_not_found")
	})

	t.Run("a malformed anchor id is 422 invalid_id", func(t *testing.T) {
		anchors := &fakeAnchorService{}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodDelete, "/api/versions/42/code/anchors/not-a-number", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(anchors.deleteCalls) != 0 {
			t.Fatalf("Delete calls = %+v, want none", anchors.deleteCalls)
		}
	})
}

// TestAnchorListFiltersAndArray covers GET /anchors: the subject filter is
// forwarded with the string subjectId parsed to an int64, ids stay decimal
// strings and an empty result is [] rather than null.
func TestAnchorListFiltersAndArray(t *testing.T) {
	t.Run("lists the anchors of one subject", func(t *testing.T) {
		anchors := &fakeAnchorService{listFn: func(_ context.Context, versionID int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error) {
			if versionID != 42 {
				t.Fatalf("List(%d), want 42", versionID)
			}
			if filter.SubjectKind != domain.SubjectRequirement || filter.SubjectID == nil || *filter.SubjectID != 9001 {
				t.Fatalf("List filter = %+v, want requirement/9001", filter)
			}
			return []domain.CodeAnchor{testAnchor(6001, 9001), testAnchor(6002, 9001)}, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodGet,
			"/api/versions/42/code/anchors?subjectKind=requirement&subjectId=9001", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data []anchorWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if len(body.Data) != 2 {
			t.Fatalf("anchors = %+v, want two", body.Data)
		}
		if body.Data[0].ID != "6001" || body.Data[0].VersionID != "42" || body.Data[0].SubjectID != "9001" {
			t.Fatalf("anchor ids = %+v, want decimal strings", body.Data[0])
		}
		if body.Data[0].CommitSHA != nil || body.Data[0].StartLine != nil {
			t.Fatalf("anchor location = %+v, want nulls", body.Data[0])
		}
	})

	t.Run("an unfiltered empty list is [] not null", func(t *testing.T) {
		anchors := &fakeAnchorService{listFn: func(_ context.Context, _ int64, filter domain.AnchorFilter) ([]domain.CodeAnchor, error) {
			if filter.SubjectKind != "" || filter.SubjectID != nil {
				t.Fatalf("List filter = %+v, want the zero filter", filter)
			}
			return nil, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/anchors", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data []anchorWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data == nil {
			t.Fatal("data = null, want an empty array")
		}
		if len(body.Data) != 0 {
			t.Fatalf("anchors = %+v, want none", body.Data)
		}
	})

	t.Run("a malformed subjectId is 422 invalid_id before the service", func(t *testing.T) {
		anchors := &fakeAnchorService{}
		handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/anchors?subjectId=abc", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(anchors.listCalls) != 0 {
			t.Fatalf("List calls = %+v, want none", anchors.listCalls)
		}
	})
}

// TestAnchorCreateBodyValidation pins the handler-owned shape checks: the body
// is strict JSON and the required fields are present before the service owns
// ownership/duplicate rules.
func TestAnchorCreateBodyValidation(t *testing.T) {
	testCases := []struct {
		name       string
		target     string
		body       string
		wantStatus int
		wantCode   string
		wantReason string
		wantField  string
	}{
		{
			name:       "missing subjectKind",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectId":"9001","filePath":"src/api/refund.ts"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_request",
			wantField:  "subjectKind",
		},
		{
			name:       "missing subjectId",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":"requirement","filePath":"src/api/refund.ts"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_request",
			wantField:  "subjectId",
		},
		{
			name:       "malformed subjectId",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":"requirement","subjectId":"abc","filePath":"src/api/refund.ts"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_id",
			wantField:  "subjectId",
		},
		{
			name:       "missing filePath",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":"requirement","subjectId":"9001"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_request",
			wantField:  "filePath",
		},
		{
			name:       "unknown field",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":"requirement","subjectId":"9001","filePath":"src/api/refund.ts","extra":1}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_json",
			wantField:  "body",
		},
		{
			name:       "malformed JSON",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_json",
			wantField:  "body",
		},
		{
			name:       "a numeric subjectId is rejected",
			target:     "/api/versions/42/code/anchors",
			body:       `{"subjectKind":"requirement","subjectId":9001,"filePath":"src/api/refund.ts"}`,
			wantStatus: http.StatusBadRequest,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_json",
			wantField:  "body",
		},
		{
			name:       "a malformed version id is 422 invalid_id",
			target:     "/api/versions/nope/code/anchors",
			body:       `{"subjectKind":"requirement","subjectId":"9001","filePath":"src/api/refund.ts"}`,
			wantStatus: http.StatusUnprocessableEntity,
			wantCode:   platform.CodeInvalidArgument,
			wantReason: "invalid_id",
			wantField:  "id",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			anchors := &fakeAnchorService{}
			handler := newTestHandler(t, HandlerDeps{Anchors: anchors})
			recorder := serveRawBody(t, handler, http.MethodPost, testCase.target, userToken, testCase.body)
			body := assertErrorResponse(t, recorder, testCase.wantStatus, testCase.wantCode, testCase.wantReason)
			if got, _ := body.Error.Details["field"].(string); got != testCase.wantField {
				t.Fatalf("details.field = %q, want %q (body %s)", got, testCase.wantField, recorder.Body.String())
			}
			if len(anchors.createCalls) != 0 {
				t.Fatalf("Create calls = %+v, want none for an invalid request", anchors.createCalls)
			}
		})
	}
}

// TestAnchorRoutesRequireAuthentication proves the anchor CRUD routes are behind
// the login middleware (every logged-in user may manage anchors; no admin).
func TestAnchorRoutesRequireAuthentication(t *testing.T) {
	handler := newTestHandler(t, HandlerDeps{Anchors: &fakeAnchorService{}})
	targets := []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/api/versions/42/code/anchors"},
		{method: http.MethodPost, target: "/api/versions/42/code/anchors"},
		{method: http.MethodDelete, target: "/api/versions/42/code/anchors/6001"},
	}
	for _, target := range targets {
		recorder := serve(t, handler, target.method, target.target, "")
		assertErrorResponse(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
	}
}
