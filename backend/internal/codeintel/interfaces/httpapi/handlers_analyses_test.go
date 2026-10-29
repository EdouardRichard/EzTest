package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/application"
	"github.com/EdouardRichard/EzTest/backend/internal/codeintel/domain"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
)

// requestBody serves one request carrying body (nil sends no body at all). It is
// the write-request counterpart of the shared serve helper: POST/PATCH bodies
// must go through the same production strict decoder the handlers use. The
// anchor handler tests share this helper (same package).
func requestBody(t *testing.T, handler http.Handler, method, target, token string, body io.Reader) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, target, body)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

// serveJSON marshals body into a JSON request. A nil body sends no body, which
// the strict decoder rejects (the endpoint requires a JSON object).
func serveJSON(t *testing.T, handler http.Handler, method, target, token string, body any) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		return requestBody(t, handler, method, target, token, nil)
	}
	encoded, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal request body: %v", err)
	}
	return requestBody(t, handler, method, target, token, strings.NewReader(string(encoded)))
}

// serveRawBody sends an arbitrary raw request body (malformed JSON, unknown
// fields, wrong field types).
func serveRawBody(t *testing.T, handler http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	return requestBody(t, handler, method, target, token, strings.NewReader(body))
}

// candidateEvidenceWire is one Candidate evidence entry. The line range and the
// excerpt hash are optional: anchor-sourced evidence carries only file (spec
// §7.3), so their absence must be observable.
type candidateEvidenceWire struct {
	File        string  `json:"file"`
	StartLine   *int    `json:"startLine"`
	EndLine     *int    `json:"endLine"`
	ExcerptHash *string `json:"excerptHash"`
}

// candidateWire is the Candidate DTO shape of frontend types.ts (R6): candidate
// and subject ids are decimal strings; selectionUpdatedBy stays a number.
type candidateWire struct {
	ID                 string                  `json:"id"`
	SubjectKind        string                  `json:"subjectKind"`
	SubjectID          string                  `json:"subjectId"`
	Reason             string                  `json:"reason"`
	Confidence         string                  `json:"confidence"`
	Source             string                  `json:"source"`
	Evidence           []candidateEvidenceWire `json:"evidence"`
	Selected           bool                    `json:"selected"`
	SelectionUpdatedBy *int64                  `json:"selectionUpdatedBy"`
	SelectionUpdatedAt *string                 `json:"selectionUpdatedAt"`
}

// unmappedWire is the UnmappedChange DTO shape.
type unmappedWire struct {
	Path       string `json:"path"`
	Status     string `json:"status"`
	Reason     string `json:"reason"`
	Suggestion string `json:"suggestion"`
}

// factVersionWire is one FactVersionRef DTO entry.
type factVersionWire struct {
	FactKey string `json:"factKey"`
	Version int    `json:"version"`
}

// analysisStatsWire is the AnalysisStats DTO shape (types.ts AnalysisStats). The
// pointer field on analysisWire distinguishes an object from a null.
type analysisStatsWire struct {
	ChangedFiles       int  `json:"changedFiles"`
	AnalyzedFiles      int  `json:"analyzedFiles"`
	CacheHits          int  `json:"cacheHits"`
	Facts              int  `json:"facts"`
	Edges              int  `json:"edges"`
	Contracts          int  `json:"contracts"`
	EvidenceChecks     int  `json:"evidenceChecks"`
	EvidenceFailures   int  `json:"evidenceFailures"`
	Unmapped           int  `json:"unmapped"`
	InvalidFacts       int  `json:"invalidFacts"`
	DuplicateFacts     int  `json:"duplicateFacts"`
	UnmatchedContracts int  `json:"unmatchedContracts"`
	Truncated          bool `json:"truncated"`
	UnmappedTruncated  bool `json:"unmappedTruncated"`
	Skipped            int  `json:"skipped"`
}

// analysisWire is the Analysis DTO shape of frontend types.ts (R6/R8): every
// codeintel id is a decimal string decoded into a Go string, so a numeric id
// fails the decode; repoId/createdBy stay numbers. R8 requires the four array
// fields to be [] rather than null, and stats to be an object.
type analysisWire struct {
	ID              string              `json:"id"`
	VersionID       string              `json:"versionId"`
	RepoID          int64               `json:"repoId"`
	BaseCommit      string              `json:"baseCommit"`
	HeadCommit      string              `json:"headCommit"`
	Status          string              `json:"status"`
	Degraded        bool                `json:"degraded"`
	AnalysisVersion int                 `json:"analysisVersion"`
	Model           string              `json:"model"`
	PromptVersion   string              `json:"promptVersion"`
	ChangeSetID     *string             `json:"changeSetId"`
	ChangeSummary   string              `json:"changeSummary"`
	Stats           *analysisStatsWire  `json:"stats"`
	FactVersions    []factVersionWire   `json:"factVersions"`
	Candidates      []candidateWire     `json:"candidates"`
	Unmapped        []unmappedWire      `json:"unmapped"`
	Errors          []analysisErrorWire `json:"errors"`
	CreatedBy       int64               `json:"createdBy"`
	CreatedAt       string              `json:"createdAt"`
	StartedAt       *string             `json:"startedAt"`
	FinishedAt      *string             `json:"finishedAt"`
}

// startAnalysisWire is the new-or-reused body of POST /analyses (spec §7.3).
type startAnalysisWire struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	AnalysisVersion int    `json:"analysisVersion"`
	Reused          bool   `json:"reused"`
}

// analysisStartCall records one AnalysisService.Start invocation.
type analysisStartCall struct {
	VersionID  int64
	ActorID    int64
	HeadCommit string
	BaseCommit string
	Force      bool
}

// analysisGetCall records one AnalysisService.Get invocation.
type analysisGetCall struct {
	VersionID  int64
	AnalysisID int64
}

// analysisPatchCall records one AnalysisService.PatchCandidate invocation.
type analysisPatchCall struct {
	VersionID   int64
	AnalysisID  int64
	CandidateID int64
	ActorID     int64
	Selected    bool
}

// fakeAnalysisService is the hand-written application.AnalysisService consumer
// double. Each method records its arguments and delegates to the configured
// function.
type fakeAnalysisService struct {
	startFn          func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error)
	getFn            func(context.Context, int64, int64) (domain.CodeAnalysis, error)
	latestFn         func(context.Context, int64) (domain.CodeAnalysis, error)
	listCandidatesFn func(context.Context, int64) ([]domain.ImpactCandidate, error)
	patchFn          func(context.Context, int64, int64, int64, int64, bool) (domain.ImpactCandidate, error)

	startCalls          []analysisStartCall
	getCalls            []analysisGetCall
	latestCalls         []int64
	listCandidatesCalls []int64
	patchCalls          []analysisPatchCall
}

func (f *fakeAnalysisService) Start(ctx context.Context, versionID, actorID int64, in application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
	f.startCalls = append(f.startCalls, analysisStartCall{
		VersionID: versionID, ActorID: actorID,
		HeadCommit: in.HeadCommit, BaseCommit: in.BaseCommit, Force: in.Force,
	})
	if f.startFn == nil {
		return domain.CodeAnalysis{}, false, nil
	}
	return f.startFn(ctx, versionID, actorID, in)
}

func (f *fakeAnalysisService) Get(ctx context.Context, versionID, analysisID int64) (domain.CodeAnalysis, error) {
	f.getCalls = append(f.getCalls, analysisGetCall{VersionID: versionID, AnalysisID: analysisID})
	if f.getFn == nil {
		return domain.CodeAnalysis{}, nil
	}
	return f.getFn(ctx, versionID, analysisID)
}

func (f *fakeAnalysisService) GetLatest(ctx context.Context, versionID int64) (domain.CodeAnalysis, error) {
	f.latestCalls = append(f.latestCalls, versionID)
	if f.latestFn == nil {
		return domain.CodeAnalysis{}, nil
	}
	return f.latestFn(ctx, versionID)
}

func (f *fakeAnalysisService) ListCandidates(ctx context.Context, analysisID int64) ([]domain.ImpactCandidate, error) {
	f.listCandidatesCalls = append(f.listCandidatesCalls, analysisID)
	if f.listCandidatesFn == nil {
		return nil, nil
	}
	return f.listCandidatesFn(ctx, analysisID)
}

func (f *fakeAnalysisService) PatchCandidate(ctx context.Context, versionID, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate, error) {
	f.patchCalls = append(f.patchCalls, analysisPatchCall{VersionID: versionID, AnalysisID: analysisID, CandidateID: candidateID, ActorID: actorID, Selected: selected})
	if f.patchFn == nil {
		return domain.ImpactCandidate{}, nil
	}
	return f.patchFn(ctx, versionID, analysisID, candidateID, actorID, selected)
}

// analysisStatsFixture is a persisted analysis stats document (design spec §7.3
// plus the file-level diagnostics). R28 puts the joined changeSummary inside the
// same JSONB document.
const analysisStatsFixture = `{
	"changedFiles": 2,
	"analyzedFiles": 1,
	"cacheHits": 1,
	"skipped": 1,
	"failedFiles": 0,
	"facts": 4,
	"edges": 2,
	"contracts": 1,
	"evidenceChecks": 6,
	"evidenceFailures": 1,
	"invalidFacts": 0,
	"duplicateFacts": 0,
	"unmatchedContracts": 1,
	"truncated": false,
	"conflicts": 0,
	"unmapped": 1,
	"candidates": 1,
	"changeSummary": "退款接口新增 reason 参数，订单页调用点同步调整"
}`

// testAnalysis builds one done analysis fixture with every aggregate collection
// populated.
func testAnalysis() domain.CodeAnalysis {
	changeSetID := int64(7001)
	return domain.CodeAnalysis{
		ID:              8801,
		VersionID:       42,
		RepoID:          3,
		BaseCommit:      commitA,
		HeadCommit:      commitB,
		ChangeSetID:     &changeSetID,
		Status:          domain.RunDone,
		Degraded:        true,
		Model:           "deepseek-chat",
		PromptVersion:   "p4.1",
		AnalysisVersion: 2,
		FactVersions:    []domain.FactVersionRef{{FactKey: "api:POST /api/refund", Version: 3}},
		Stats:           []byte(analysisStatsFixture),
		Errors: []domain.AnalysisError{{
			Code:    "parse_error",
			Path:    "src/api/refund.ts",
			Message: "codeintel: could not parse the model response",
		}},
		Unmapped: []domain.UnmappedChange{{
			Path:       "src/utils/format.ts",
			Status:     "M",
			Reason:     domain.UnmappedNoFact,
			Suggestion: "人工确认影响",
		}},
		CreatedBy:  testUserID,
		CreatedAt:  startedAt,
		StartedAt:  &startedAt,
		FinishedAt: &finishedAt,
	}
}

// testCandidate builds one persisted candidate fixture with a human selection
// and one anchor-style (file-only) evidence entry.
func testCandidate() domain.ImpactCandidate {
	updatedBy := int64(7)
	updatedAt := finishedAt
	return domain.ImpactCandidate{
		ID:          9901,
		AnalysisID:  8801,
		SubjectKind: domain.CandidateRequirement,
		SubjectID:   9001,
		Reason:      "changed api POST /api/refund",
		Confidence:  domain.ConfidenceHigh,
		Source:      domain.CandidateDirect,
		Evidence: []domain.Evidence{
			{File: "src/api/refund.ts", StartLine: 80, EndLine: 96, ExcerptHash: "sha256:abc"},
			{File: "src/router.ts"},
		},
		Selected:           true,
		SelectionUpdatedBy: &updatedBy,
		SelectionUpdatedAt: &updatedAt,
	}
}

// TestAnalysisStartReuseForceAndConflicts pins POST /analyses: 202 for a new run,
// 200 for a reused one, the {headCommit,force} body, and the §7.1 reason mapping
// (409 in progress, 422 unbound/invalid head, 429 queue full, 422 bad id).
func TestAnalysisStartReuseForceAndConflicts(t *testing.T) {
	queued := testAnalysis()
	queued.Status = domain.RunQueued
	queued.AnalysisVersion = 1
	queued.StartedAt = nil
	queued.FinishedAt = nil
	done := testAnalysis()

	t.Run("a new analysis is 202 with reused=false", func(t *testing.T) {
		analyses := &fakeAnalysisService{startFn: func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
			return queued, false, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken,
			map[string]any{"headCommit": commitB, "baseCommit": commitA, "force": false})
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data startAnalysisWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ID != "8801" || body.Data.Status != domain.RunQueued ||
			body.Data.AnalysisVersion != 1 || body.Data.Reused {
			t.Fatalf("accepted body = %+v, want id 8801 queued v1 reused=false", body.Data)
		}
		want := analysisStartCall{VersionID: 42, ActorID: testUserID, HeadCommit: commitB, BaseCommit: commitA}
		if got := analyses.startCalls; len(got) != 1 || got[0] != want {
			t.Fatalf("Start call = %+v, want %+v", got, want)
		}
	})

	t.Run("baseCommit is optional and forwarded as the empty default", func(t *testing.T) {
		analyses := &fakeAnalysisService{startFn: func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
			return queued, false, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken,
			map[string]any{"baseCommit": commitA})
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
		}
		if got := analyses.startCalls; len(got) != 1 || got[0].BaseCommit != commitA || got[0].HeadCommit != "" {
			t.Fatalf("Start call = %+v, want baseCommit %s and the default head", got, commitA)
		}
	})

	t.Run("reused analysis is 200 with reused=true", func(t *testing.T) {
		analyses := &fakeAnalysisService{startFn: func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
			return done, true, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken, map[string]any{})
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 for a reused analysis (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data startAnalysisWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if !body.Data.Reused || body.Data.Status != domain.RunDone || body.Data.ID != "8801" {
			t.Fatalf("reused body = %+v, want done/8801/reused=true", body.Data)
		}
		if got := analyses.startCalls; len(got) != 1 || got[0].HeadCommit != "" || got[0].Force {
			t.Fatalf("Start call = %+v, want the empty-object defaults", got)
		}
	})

	t.Run("force is forwarded to the application service", func(t *testing.T) {
		analyses := &fakeAnalysisService{startFn: func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
			return queued, false, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken,
			map[string]any{"force": true})
		if recorder.Code != http.StatusAccepted {
			t.Fatalf("status = %d, want 202 (body %s)", recorder.Code, recorder.Body.String())
		}
		if got := analyses.startCalls; len(got) != 1 || !got[0].Force {
			t.Fatalf("Start call = %+v, want force=true", got)
		}
	})

	reasonCases := []struct {
		name      string
		err       error
		status    int
		code      string
		reason    string
		field     string
		retryable bool
	}{
		{
			name:   "active analysis maps to 409 analysis_in_progress",
			err:    domain.NewConflict("analysis_in_progress", "codeintel: version already has an active analysis"),
			status: http.StatusConflict,
			code:   platform.CodeConflict,
			reason: "analysis_in_progress",
		},
		{
			name:   "unbound version maps to 422 code_repository_not_bound",
			err:    domain.NewInvalid("versionId", "code_repository_not_bound", "codeintel: version has no code repository binding"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "code_repository_not_bound",
			field:  "versionId",
		},
		{
			name:   "invalid head maps to 422 invalid_head",
			err:    domain.NewInvalid("headCommit", "invalid_head", "codeintel: headCommit must be a lowercase 40-hex commit id"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_head",
			field:  "headCommit",
		},
		{
			name:   "invalid base maps to 422 invalid_base_commit",
			err:    domain.NewInvalid("baseCommit", "invalid_base_commit", "codeintel: baseCommit must be a 40-hex ancestor of HEAD"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_base_commit",
			field:  "baseCommit",
		},
		{
			name: "a full queue maps to retryable 429 analysis_queue_full",
			err: fmt.Errorf("%w: %w",
				domain.NewConflict("analysis_queue_full", "codeintel: the analysis queue is full"),
				application.ErrQueueFull),
			status:    http.StatusTooManyRequests,
			code:      platform.CodeRateLimited,
			reason:    "analysis_queue_full",
			retryable: true,
		},
	}
	for _, testCase := range reasonCases {
		t.Run(testCase.name, func(t *testing.T) {
			analyses := &fakeAnalysisService{startFn: func(context.Context, int64, int64, application.StartAnalysisInput) (domain.CodeAnalysis, bool, error) {
				return domain.CodeAnalysis{}, false, testCase.err
			}}
			handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
			recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken, map[string]any{})
			body := assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
			if body.Error.Retryable != testCase.retryable {
				t.Fatalf("retryable = %v, want %v", body.Error.Retryable, testCase.retryable)
			}
			if testCase.field != "" {
				if got, _ := body.Error.Details["field"].(string); got != testCase.field {
					t.Fatalf("details.field = %q, want %q", got, testCase.field)
				}
			}
		})
	}

	t.Run("a malformed version id is 422 invalid_id before the service", func(t *testing.T) {
		analyses := &fakeAnalysisService{}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPost, "/api/versions/not-a-number/code/analyses", userToken, map[string]any{})
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(analyses.startCalls) != 0 {
			t.Fatalf("Start calls = %+v, want none for a malformed id", analyses.startCalls)
		}
	})

	bodyCases := []struct {
		name    string
		target  string
		token   string
		body    string
		status  int
		code    string
		reason  string
		details string
	}{
		{
			name:   "unknown field is rejected",
			target: "/api/versions/42/code/analyses",
			body:   `{"headCommit":"` + commitB + `","unexpected":1}`,
			status: http.StatusBadRequest,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_json",
		},
		{
			name:   "trailing data is rejected",
			target: "/api/versions/42/code/analyses",
			body:   `{} {}`,
			status: http.StatusBadRequest,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_json",
		},
		{
			name:   "malformed JSON is rejected",
			target: "/api/versions/42/code/analyses",
			body:   `{"headCommit":`,
			status: http.StatusBadRequest,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_json",
		},
		{
			name:   "a non-boolean force is rejected",
			target: "/api/versions/42/code/analyses",
			body:   `{"force":"yes"}`,
			status: http.StatusBadRequest,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_json",
		},
		{
			name:   "a non-string baseCommit is rejected",
			target: "/api/versions/42/code/analyses",
			body:   `{"baseCommit":123}`,
			status: http.StatusBadRequest,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_json",
		},
	}
	for _, testCase := range bodyCases {
		t.Run(testCase.name, func(t *testing.T) {
			analyses := &fakeAnalysisService{}
			handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
			recorder := serveRawBody(t, handler, http.MethodPost, testCase.target, userToken, testCase.body)
			assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
			if len(analyses.startCalls) != 0 {
				t.Fatalf("Start calls = %+v, want none for an invalid body", analyses.startCalls)
			}
		})
	}

	t.Run("a missing body is rejected", func(t *testing.T) {
		analyses := &fakeAnalysisService{}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodPost, "/api/versions/42/code/analyses", userToken)
		assertErrorResponse(t, recorder, http.StatusBadRequest, platform.CodeInvalidArgument, "invalid_json")
	})
}

// TestAnalysisLatestAndPatchCandidate covers GET latest, GET detail and PATCH
// candidate: the aggregated AnalysisDTO, the 404 analysis_not_found reads, the
// 200 selection update with its actor, and the 409/404/400/422 failures.
func TestAnalysisLatestAndPatchCandidate(t *testing.T) {
	analysis := testAnalysis()
	candidates := []domain.ImpactCandidate{testCandidate()}

	t.Run("latest aggregates candidates, unmapped and the fact-version snapshot", func(t *testing.T) {
		analyses := &fakeAnalysisService{
			latestFn: func(_ context.Context, versionID int64) (domain.CodeAnalysis, error) {
				if versionID != 42 {
					t.Fatalf("GetLatest(%d), want 42", versionID)
				}
				return analysis, nil
			},
			listCandidatesFn: func(_ context.Context, analysisID int64) ([]domain.ImpactCandidate, error) {
				if analysisID != 8801 {
					t.Fatalf("ListCandidates(%d), want 8801", analysisID)
				}
				return candidates, nil
			},
		}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/latest", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data analysisWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ID != "8801" || body.Data.VersionID != "42" || body.Data.RepoID != 3 {
			t.Fatalf("identity = %+v, want ids 8801/42 and repoId 3", body.Data)
		}
		if body.Data.BaseCommit != commitA || body.Data.HeadCommit != commitB {
			t.Fatalf("commit pair = %q..%q", body.Data.BaseCommit, body.Data.HeadCommit)
		}
		if body.Data.Status != domain.RunDone || !body.Data.Degraded || body.Data.AnalysisVersion != 2 {
			t.Fatalf("run state = %+v", body.Data)
		}
		if body.Data.Model != "deepseek-chat" || body.Data.PromptVersion != "p4.1" {
			t.Fatalf("model = %q promptVersion = %q", body.Data.Model, body.Data.PromptVersion)
		}
		if body.Data.ChangeSetID == nil || *body.Data.ChangeSetID != "7001" {
			t.Fatalf("changeSetId = %v, want \"7001\"", body.Data.ChangeSetID)
		}
		if body.Data.ChangeSummary != "退款接口新增 reason 参数，订单页调用点同步调整" {
			t.Fatalf("changeSummary = %q, want the stats changeSummary (R28)", body.Data.ChangeSummary)
		}
		if body.Data.Stats == nil {
			t.Fatal("stats = null, want an object")
		}
		stats := *body.Data.Stats
		if stats.ChangedFiles != 2 || stats.AnalyzedFiles != 1 || stats.CacheHits != 1 || stats.Skipped != 1 {
			t.Fatalf("stats file counters = %+v", stats)
		}
		if stats.Facts != 4 || stats.Edges != 2 || stats.Contracts != 1 || stats.UnmatchedContracts != 1 {
			t.Fatalf("stats fact counters = %+v", stats)
		}
		if stats.EvidenceChecks != 6 || stats.EvidenceFailures != 1 || stats.Unmapped != 1 || stats.Truncated {
			t.Fatalf("stats evidence counters = %+v", stats)
		}
		if len(body.Data.FactVersions) != 1 || body.Data.FactVersions[0].FactKey != "api:POST /api/refund" || body.Data.FactVersions[0].Version != 3 {
			t.Fatalf("factVersions = %+v", body.Data.FactVersions)
		}
		if len(body.Data.Candidates) != 1 {
			t.Fatalf("candidates = %+v, want one", body.Data.Candidates)
		}
		candidate := body.Data.Candidates[0]
		if candidate.ID != "9901" || candidate.SubjectID != "9001" {
			t.Fatalf("candidate ids = %+v, want decimal strings", candidate)
		}
		if candidate.SubjectKind != domain.CandidateRequirement || candidate.Confidence != domain.ConfidenceHigh ||
			candidate.Source != domain.CandidateDirect || !candidate.Selected {
			t.Fatalf("candidate = %+v", candidate)
		}
		if candidate.SelectionUpdatedBy == nil || *candidate.SelectionUpdatedBy != 7 {
			t.Fatalf("selectionUpdatedBy = %v, want the numeric 7", candidate.SelectionUpdatedBy)
		}
		if candidate.SelectionUpdatedAt == nil {
			t.Fatal("selectionUpdatedAt = null, want an ISO timestamp")
		}
		if len(candidate.Evidence) != 2 {
			t.Fatalf("evidence = %+v, want two entries", candidate.Evidence)
		}
		aiEvidence := candidate.Evidence[0]
		if aiEvidence.File != "src/api/refund.ts" || aiEvidence.StartLine == nil || *aiEvidence.StartLine != 80 ||
			aiEvidence.EndLine == nil || *aiEvidence.EndLine != 96 || aiEvidence.ExcerptHash == nil || *aiEvidence.ExcerptHash != "sha256:abc" {
			t.Fatalf("AI evidence = %+v", aiEvidence)
		}
		anchorEvidence := candidate.Evidence[1]
		if anchorEvidence.File != "src/router.ts" || anchorEvidence.StartLine != nil ||
			anchorEvidence.EndLine != nil || anchorEvidence.ExcerptHash != nil {
			t.Fatalf("anchor evidence = %+v, want only file (spec §7.3)", anchorEvidence)
		}
		if len(body.Data.Unmapped) != 1 || body.Data.Unmapped[0].Reason != domain.UnmappedNoFact ||
			body.Data.Unmapped[0].Path != "src/utils/format.ts" || body.Data.Unmapped[0].Suggestion != "人工确认影响" {
			t.Fatalf("unmapped = %+v", body.Data.Unmapped)
		}
		if len(body.Data.Errors) != 1 || body.Data.Errors[0].Code != "parse_error" ||
			body.Data.Errors[0].Path != "src/api/refund.ts" || body.Data.Errors[0].Message == "" {
			t.Fatalf("errors = %+v", body.Data.Errors)
		}
		if body.Data.CreatedBy != testUserID || body.Data.CreatedAt == "" {
			t.Fatalf("createdBy/createdAt = %d/%q", body.Data.CreatedBy, body.Data.CreatedAt)
		}
		if body.Data.StartedAt == nil || body.Data.FinishedAt == nil {
			t.Fatalf("timestamps = %v/%v, want both set", body.Data.StartedAt, body.Data.FinishedAt)
		}
	})

	t.Run("latest without an analysis is 404 analysis_not_found", func(t *testing.T) {
		analyses := &fakeAnalysisService{latestFn: func(context.Context, int64) (domain.CodeAnalysis, error) {
			return domain.CodeAnalysis{}, domain.NewNotFound("analysis_not_found", "codeintel: version has no analysis")
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/latest", userToken)
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "analysis_not_found")
		if len(analyses.listCandidatesCalls) != 0 {
			t.Fatalf("ListCandidates calls = %+v, want none", analyses.listCandidatesCalls)
		}
	})

	t.Run("detail reads the version-scoped analysis id", func(t *testing.T) {
		analyses := &fakeAnalysisService{
			getFn: func(_ context.Context, versionID, analysisID int64) (domain.CodeAnalysis, error) {
				if versionID != 42 || analysisID != 8801 {
					t.Fatalf("Get(%d, %d), want (42, 8801)", versionID, analysisID)
				}
				return analysis, nil
			},
			listCandidatesFn: func(context.Context, int64) ([]domain.ImpactCandidate, error) {
				return candidates, nil
			},
		}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/8801", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data analysisWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ID != "8801" || len(body.Data.Candidates) != 1 {
			t.Fatalf("detail = %+v", body.Data)
		}
	})

	t.Run("detail of a missing analysis is 404", func(t *testing.T) {
		analyses := &fakeAnalysisService{getFn: func(context.Context, int64, int64) (domain.CodeAnalysis, error) {
			return domain.CodeAnalysis{}, domain.NewNotFound("analysis_not_found", "codeintel: analysis not found")
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/4040", userToken)
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "analysis_not_found")
	})

	t.Run("a malformed analysis id is 422 invalid_id", func(t *testing.T) {
		analyses := &fakeAnalysisService{}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/not-a-number", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(analyses.getCalls) != 0 {
			t.Fatalf("Get calls = %+v, want none", analyses.getCalls)
		}
	})

	t.Run("PATCH persists the selection for the authenticated actor", func(t *testing.T) {
		updated := testCandidate()
		updated.Selected = false
		analyses := &fakeAnalysisService{patchFn: func(_ context.Context, versionID, analysisID, candidateID, actorID int64, selected bool) (domain.ImpactCandidate, error) {
			if versionID != 42 || analysisID != 8801 || candidateID != 9901 || actorID != testUserID || selected {
				t.Fatalf("PatchCandidate(%d, %d, %d, %d, %v), want (42, 8801, 9901, %d, false)",
					versionID, analysisID, candidateID, actorID, selected, testUserID)
			}
			return updated, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPatch,
			"/api/versions/42/code/analyses/8801/candidates/9901", userToken, map[string]any{"selected": false})
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data candidateWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ID != "9901" || body.Data.SubjectID != "9001" || body.Data.Selected {
			t.Fatalf("patched candidate = %+v", body.Data)
		}
		if body.Data.SelectionUpdatedBy == nil || *body.Data.SelectionUpdatedBy != 7 {
			t.Fatalf("selectionUpdatedBy = %v, want the numeric 7", body.Data.SelectionUpdatedBy)
		}
		if len(analyses.patchCalls) != 1 {
			t.Fatalf("PatchCandidate calls = %+v, want one", analyses.patchCalls)
		}
	})

	t.Run("PATCH on an unfinished analysis is 409 analysis_not_done", func(t *testing.T) {
		analyses := &fakeAnalysisService{patchFn: func(context.Context, int64, int64, int64, int64, bool) (domain.ImpactCandidate, error) {
			return domain.ImpactCandidate{}, domain.NewConflict("analysis_not_done", "codeintel: analysis is not done")
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPatch,
			"/api/versions/42/code/analyses/8801/candidates/9901", userToken, map[string]any{"selected": true})
		assertErrorResponse(t, recorder, http.StatusConflict, platform.CodeConflict, "analysis_not_done")
	})

	t.Run("PATCH of an unknown candidate is 404 candidate_not_found", func(t *testing.T) {
		analyses := &fakeAnalysisService{patchFn: func(context.Context, int64, int64, int64, int64, bool) (domain.ImpactCandidate, error) {
			return domain.ImpactCandidate{}, domain.NewNotFound("candidate_not_found", "codeintel: candidate not found")
		}}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPatch,
			"/api/versions/42/code/analyses/8801/candidates/4040", userToken, map[string]any{"selected": true})
		assertErrorResponse(t, recorder, http.StatusNotFound, platform.CodeNotFound, "candidate_not_found")
	})

	t.Run("PATCH requires a boolean selected", func(t *testing.T) {
		for _, testCase := range []struct {
			name       string
			body       string
			wantStatus int
			wantCode   string
			wantReason string
		}{
			{name: "missing selected", body: `{}`, wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument, wantReason: "invalid_request"},
			{name: "non-boolean selected", body: `{"selected":"yes"}`, wantStatus: http.StatusBadRequest, wantCode: platform.CodeInvalidArgument, wantReason: "invalid_json"},
		} {
			t.Run(testCase.name, func(t *testing.T) {
				analyses := &fakeAnalysisService{}
				handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
				recorder := serveRawBody(t, handler, http.MethodPatch,
					"/api/versions/42/code/analyses/8801/candidates/9901", userToken, testCase.body)
				body := assertErrorResponse(t, recorder, testCase.wantStatus, testCase.wantCode, testCase.wantReason)
				if testCase.name == "missing selected" {
					if got, _ := body.Error.Details["field"].(string); got != "selected" {
						t.Fatalf("details.field = %q, want selected", got)
					}
				}
				if len(analyses.patchCalls) != 0 {
					t.Fatalf("PatchCandidate calls = %+v, want none", analyses.patchCalls)
				}
			})
		}
	})

	t.Run("a malformed candidate id is 422 invalid_id", func(t *testing.T) {
		analyses := &fakeAnalysisService{}
		handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
		recorder := serveJSON(t, handler, http.MethodPatch,
			"/api/versions/42/code/analyses/8801/candidates/nope", userToken, map[string]any{"selected": true})
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(analyses.patchCalls) != 0 {
			t.Fatalf("PatchCandidate calls = %+v, want none", analyses.patchCalls)
		}
	})
}

// TestAnalysisDTOIDTypes pins the R6/R8 projection: string analysis/version/
// changeSet/candidate/subject ids, numeric repoId/createdBy, anchor evidence
// carrying only file, stats as an object, the changeSummary from the stats
// document, and the four aggregate arrays as [] (never null).
func TestAnalysisDTOIDTypes(t *testing.T) {
	analysis := testAnalysis()
	analysis.ChangeSetID = nil
	analysis.FactVersions = nil
	analysis.Unmapped = nil
	analysis.Errors = nil
	analysis.Stats = []byte("{}")
	analysis.StartedAt = nil
	analysis.FinishedAt = nil

	anchorEvidenceCandidate := testCandidate()
	anchorEvidenceCandidate.ID = 9901
	anchorEvidenceCandidate.SelectionUpdatedBy = nil
	anchorEvidenceCandidate.SelectionUpdatedAt = nil
	anchorEvidenceCandidate.Evidence = []domain.Evidence{{File: "src/router.ts"}}
	noEvidenceCandidate := testCandidate()
	noEvidenceCandidate.ID = 9902
	noEvidenceCandidate.SubjectKind = domain.CandidateAppModel
	noEvidenceCandidate.SubjectID = 9002
	noEvidenceCandidate.Evidence = nil

	analyses := &fakeAnalysisService{
		latestFn: func(context.Context, int64) (domain.CodeAnalysis, error) {
			return analysis, nil
		},
		listCandidatesFn: func(context.Context, int64) ([]domain.ImpactCandidate, error) {
			return []domain.ImpactCandidate{anchorEvidenceCandidate, noEvidenceCandidate}, nil
		},
	}
	handler := newTestHandler(t, HandlerDeps{Analyses: analyses})
	recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/analyses/latest", userToken)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Data analysisWire `json:"data"`
	}
	decodeBody(t, recorder, &body)

	if body.Data.ID != "8801" || body.Data.VersionID != "42" || body.Data.RepoID != 3 {
		t.Fatalf("identity = %+v", body.Data)
	}
	if body.Data.ChangeSetID != nil {
		t.Fatalf("changeSetId = %v, want null for an analysis without a change set", *body.Data.ChangeSetID)
	}
	if body.Data.Stats == nil {
		t.Fatal("stats = null, want an object (R8)")
	}
	if body.Data.FactVersions == nil || body.Data.Candidates == nil || body.Data.Unmapped == nil || body.Data.Errors == nil {
		t.Fatalf("aggregate arrays = factVersions:%v candidates:%v unmapped:%v errors:%v, want [] not null (R8)",
			body.Data.FactVersions, body.Data.Candidates, body.Data.Unmapped, body.Data.Errors)
	}
	if body.Data.StartedAt != nil || body.Data.FinishedAt != nil {
		t.Fatalf("timestamps = %v/%v, want null for a never-started fake", body.Data.StartedAt, body.Data.FinishedAt)
	}
	if len(body.Data.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want two", body.Data.Candidates)
	}
	anchorCandidate := body.Data.Candidates[0]
	if anchorCandidate.ID != "9901" || anchorCandidate.SubjectID != "9001" {
		t.Fatalf("candidate ids = %+v, want decimal strings", anchorCandidate)
	}
	if len(anchorCandidate.Evidence) != 1 || anchorCandidate.Evidence[0].File != "src/router.ts" ||
		anchorCandidate.Evidence[0].StartLine != nil || anchorCandidate.Evidence[0].ExcerptHash != nil {
		t.Fatalf("anchor evidence = %+v, want only file", anchorCandidate.Evidence)
	}
	if anchorCandidate.SelectionUpdatedBy != nil || anchorCandidate.SelectionUpdatedAt != nil {
		t.Fatalf("selection update = %v/%v, want null", anchorCandidate.SelectionUpdatedBy, anchorCandidate.SelectionUpdatedAt)
	}
	appModelCandidate := body.Data.Candidates[1]
	if appModelCandidate.ID != "9902" || appModelCandidate.SubjectID != "9002" ||
		appModelCandidate.SubjectKind != domain.CandidateAppModel {
		t.Fatalf("app model candidate = %+v", appModelCandidate)
	}
	if appModelCandidate.Evidence == nil || len(appModelCandidate.Evidence) != 0 {
		t.Fatalf("evidence = %v, want an empty array not null", appModelCandidate.Evidence)
	}
}

// TestVersionChangesPreviewAndReasonMapping covers GET /changes (R7 ChangeList
// inside data, base from the version baseline, nullable changeSetId) and its
// §7.1 reasons.
func TestVersionChangesPreviewAndReasonMapping(t *testing.T) {
	changeSetID := int64(7001)
	preview := domain.ChangeSetData{
		ChangeSetID: &changeSetID,
		Stats:       []byte(`{"changedFiles":2,"additions":5,"deletions":1,"truncated":false}`),
		Files: []domain.ChangedFile{
			{Path: "src/api/refund.ts", Status: "M", Additions: 5, HeadBlobHash: "git-sha1:" + commitA},
			{Path: "src/router.ts", Status: "M", HeadBlobHash: "git-sha1:" + commitB},
		},
	}

	t.Run("an explicit head returns the ChangeList in the data field", func(t *testing.T) {
		query := &fakeQueryService{versionChangesFn: func(_ context.Context, versionID int64, head string) (domain.ChangeSetData, string, string, error) {
			if versionID != 42 || head != commitB {
				t.Fatalf("VersionChanges(%d, %q), want (42, %q)", versionID, head, commitB)
			}
			return preview, commitA, commitB, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes?head="+commitB, userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data changeListWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ChangeSetID == nil || *body.Data.ChangeSetID != "7001" {
			t.Fatalf("changeSetId = %v, want the decimal string \"7001\"", body.Data.ChangeSetID)
		}
		if body.Data.BaseCommit != commitA || body.Data.HeadCommit != commitB {
			t.Fatalf("commit pair = %q..%q, want the version baseline and the resolved head", body.Data.BaseCommit, body.Data.HeadCommit)
		}
		if body.Data.Stats.Files != 2 || body.Data.Stats.Additions != 5 || body.Data.Stats.Deletions != 1 {
			t.Fatalf("stats = %+v", body.Data.Stats)
		}
		if len(body.Data.Files) != 2 || body.Data.Files[0].Path != "src/api/refund.ts" {
			t.Fatalf("files = %+v", body.Data.Files)
		}
		if body.Data.HasMore || body.Data.NextCursor != nil {
			t.Fatalf("page = hasMore %v cursor %v, want the whole preview", body.Data.HasMore, body.Data.NextCursor)
		}
		want := versionChangesCall{VersionID: 42, Head: commitB}
		if got := query.versionChangesCalls; len(got) != 1 || got[0] != want {
			t.Fatalf("VersionChanges calls = %+v, want %+v", got, want)
		}
	})

	t.Run("head is optional and changeSetId stays null for an unpersisted preview", func(t *testing.T) {
		unpersisted := preview
		unpersisted.ChangeSetID = nil
		query := &fakeQueryService{versionChangesFn: func(_ context.Context, versionID int64, head string) (domain.ChangeSetData, string, string, error) {
			if versionID != 42 || head != "" {
				t.Fatalf("VersionChanges(%d, %q), want the empty head default", versionID, head)
			}
			return unpersisted, commitA, commitB, nil
		}}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", recorder.Code, recorder.Body.String())
		}
		var body struct {
			Data changeListWire `json:"data"`
		}
		decodeBody(t, recorder, &body)
		if body.Data.ChangeSetID != nil {
			t.Fatalf("changeSetId = %v, want null for a preview that never persisted a change set", *body.Data.ChangeSetID)
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
			name:   "unbound version",
			err:    domain.NewInvalid("versionId", "code_repository_not_bound", "codeintel: version has no code repository binding"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "code_repository_not_bound",
		},
		{
			name:   "invalid head",
			err:    domain.NewInvalid("headCommit", "invalid_head", "codeintel: headCommit must be a lowercase 40-hex commit id"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "invalid_head",
		},
		{
			name:   "missing baseline",
			err:    domain.NewInvalid("versionId", "baseline_commit_missing", "codeintel: version has no code baseline commit"),
			status: http.StatusUnprocessableEntity,
			code:   platform.CodeInvalidArgument,
			reason: "baseline_commit_missing",
		},
	}
	for _, testCase := range reasonCases {
		t.Run(testCase.name, func(t *testing.T) {
			query := &fakeQueryService{versionChangesFn: func(context.Context, int64, string) (domain.ChangeSetData, string, string, error) {
				return domain.ChangeSetData{}, "", "", testCase.err
			}}
			handler := newTestHandler(t, HandlerDeps{Query: query})
			recorder := serve(t, handler, http.MethodGet, "/api/versions/42/code/changes", userToken)
			assertErrorResponse(t, recorder, testCase.status, testCase.code, testCase.reason)
		})
	}

	t.Run("a malformed version id is 422 invalid_id", func(t *testing.T) {
		query := &fakeQueryService{}
		handler := newTestHandler(t, HandlerDeps{Query: query})
		recorder := serve(t, handler, http.MethodGet, "/api/versions/nope/code/changes", userToken)
		assertErrorResponse(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
		if len(query.versionChangesCalls) != 0 {
			t.Fatalf("VersionChanges calls = %+v, want none", query.versionChangesCalls)
		}
	})
}

// TestAnalysisRoutesRequireAuthentication proves every version-level code route
// is behind the login middleware (no admin required).
func TestAnalysisRoutesRequireAuthentication(t *testing.T) {
	handler := newTestHandler(t, HandlerDeps{Query: &fakeQueryService{}, Analyses: &fakeAnalysisService{}})
	targets := []struct {
		method string
		target string
	}{
		{method: http.MethodGet, target: "/api/versions/42/code/changes"},
		{method: http.MethodPost, target: "/api/versions/42/code/analyses"},
		{method: http.MethodGet, target: "/api/versions/42/code/analyses/latest"},
		{method: http.MethodGet, target: "/api/versions/42/code/analyses/8801"},
		{method: http.MethodPatch, target: "/api/versions/42/code/analyses/8801/candidates/9901"},
	}
	for _, target := range targets {
		recorder := serve(t, handler, target.method, target.target, "")
		assertErrorResponse(t, recorder, http.StatusUnauthorized, platform.CodeUnauthenticated, "")
	}
}
