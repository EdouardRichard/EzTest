package versionapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	auditapp "github.com/EdouardRichard/EzTest/backend/internal/audit/application"
	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

type handlerClock struct{ at time.Time }

func (c handlerClock) Now() time.Time { return c.at }

type handlerStageRepo struct {
	state    version.StageState
	found    bool
	applyErr error
	applied  []version.StageState
}

func (r *handlerStageRepo) GetStage(_ context.Context, versionID int64) (version.StageState, error) {
	if !r.found {
		return version.StageState{}, version.ErrNotFound
	}
	state := r.state
	state.VersionID = versionID
	return state, nil
}

func (r *handlerStageRepo) ApplyStage(_ context.Context, expected version.Stage, next version.StageState) error {
	if r.applyErr != nil {
		return r.applyErr
	}
	if r.state.Stage != expected {
		return version.ErrStageConflict
	}
	r.state = next
	r.applied = append(r.applied, next)
	return nil
}

var _ version.StageRepository = (*handlerStageRepo)(nil)

type handlerStageJobs struct{ jobs []versionapp.StageJob }

func (j *handlerStageJobs) EnqueueStageJob(_ context.Context, job versionapp.StageJob) error {
	j.jobs = append(j.jobs, job)
	return nil
}

var _ versionapp.StageJobEnqueuer = (*handlerStageJobs)(nil)

type handlerStageAudit struct{ events []auditapp.Event }

func (a *handlerStageAudit) Record(_ context.Context, event auditapp.Event) error {
	a.events = append(a.events, event)
	return nil
}

func newStageTestServer(t *testing.T, initial version.Stage) (http.Handler, *handlerStageRepo, *handlerStageJobs, *handlerStageAudit) {
	t.Helper()
	repo := &handlerStageRepo{
		state: version.StageState{Stage: initial, StageEnteredAt: testTime},
		found: true,
	}
	jobs := &handlerStageJobs{}
	audit := &handlerStageAudit{}
	machine := versionapp.NewStageMachine(repo, jobs, audit, nil, handlerClock{at: testTime})
	handler := versionapi.NewStageHandler(machine)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/versions/{id}/stage", handler.Get)
	mux.HandleFunc("POST /api/versions/{id}/stage/confirm-requirements", handler.ConfirmRequirements)
	mux.HandleFunc("POST /api/versions/{id}/stage/confirm-cases", handler.ConfirmCases)
	mux.HandleFunc("POST /api/versions/{id}/stage/reject", handler.Reject)
	return mux, repo, jobs, audit
}

func stageDeniedAudit(t *testing.T, audit *handlerStageAudit) auditapp.Event {
	t.Helper()
	for index := len(audit.events) - 1; index >= 0; index-- {
		if audit.events[index].Action == "version.stage_gate_denied" {
			return audit.events[index]
		}
	}
	t.Fatalf("no version.stage_gate_denied audit entry (events = %#v)", audit.events)
	return auditapp.Event{}
}

func nonUserActor(role string) *platform.Actor {
	return &platform.Actor{ID: 77, Role: role}
}

// TestStageConfirmRequiresUser pins the hard gate authorization rule: only a
// user token may confirm requirements/cases or reject a gate; assistant and
// service tokens get a 403 and an audit entry, and the stage never moves.
func TestStageConfirmRequiresUser(t *testing.T) {
	endpoints := []struct {
		name string
		path string
		body string
		gate string
	}{
		{"confirm requirements", "/api/versions/101/stage/confirm-requirements", `{"requirement_ids":[]}`, "requirement"},
		{"confirm cases", "/api/versions/101/stage/confirm-cases", `{"case_ids":[]}`, "case"},
		{"reject", "/api/versions/101/stage/reject", `{"reason":"missing acceptance criteria"}`, "reject"},
	}
	for _, role := range []string{"assistant", "service"} {
		for _, endpoint := range endpoints {
			t.Run(role+"/"+endpoint.name, func(t *testing.T) {
				mux, repo, jobs, audit := newStageTestServer(t, version.StageRequirementReview)
				recorder := call(t, mux, http.MethodPost, endpoint.path, endpoint.body, nonUserActor(role))
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("status = %d, want 403, body = %s", recorder.Code, recorder.Body.String())
				}
				if code := errorBody(t, recorder)["code"]; code != "forbidden" {
					t.Fatalf("error code = %#v, want forbidden", code)
				}
				if len(jobs.jobs) != 0 {
					t.Fatalf("non-user token enqueued jobs: %#v", jobs.jobs)
				}
				if len(repo.applied) != 0 || repo.state.Stage != version.StageRequirementReview {
					t.Fatalf("non-user token moved the stage: %#v", repo.state)
				}
				event := stageDeniedAudit(t, audit)
				if event.ActorID == nil || *event.ActorID != 77 {
					t.Fatalf("denied audit actor = %#v", event.ActorID)
				}
				if event.Metadata["gate"] != endpoint.gate {
					t.Fatalf("denied audit gate = %#v, want %q", event.Metadata["gate"], endpoint.gate)
				}
				if event.Metadata["role"] != role {
					t.Fatalf("denied audit role = %#v, want %q", event.Metadata["role"], role)
				}
				if event.TargetKind != "version" || event.TargetID != "101" {
					t.Fatalf("denied audit target = %#v/%#v", event.TargetKind, event.TargetID)
				}
			})
		}
	}

	// A regular user token is accepted and drives the gate.
	mux, repo, jobs, audit := newStageTestServer(t, version.StageRequirementReview)
	recorder := call(t, mux, http.MethodPost, "/api/versions/101/stage/confirm-requirements", `{"requirement_ids":["701"]}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("user status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.state.Stage != version.StageDesigning {
		t.Fatalf("stage = %q, want designing", repo.state.Stage)
	}
	if len(jobs.jobs) != 1 || jobs.jobs[0].Kind != versionapp.StageJobDesignCases {
		t.Fatalf("jobs = %#v", jobs.jobs)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "version.stage_decision" {
		t.Fatalf("audit = %#v", audit.events)
	}
}

func TestStageGetReturnsStageProjection(t *testing.T) {
	mux, repo, _, _ := newStageTestServer(t, version.StageBlocked)
	repo.state.Blocker = "executor is offline"

	recorder := call(t, mux, http.MethodGet, "/api/versions/101/stage", "", actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	data := dataBody(t, recorder)
	if data["version_id"] != "101" || data["stage"] != "blocked" {
		t.Fatalf("data = %#v", data)
	}
	if data["stage_entered_at"] != testTime.UTC().Format(time.RFC3339) {
		t.Fatalf("stage_entered_at = %#v", data["stage_entered_at"])
	}
	if data["blocker"] != "executor is offline" {
		t.Fatalf("blocker = %#v", data["blocker"])
	}

	invalid := call(t, mux, http.MethodGet, "/api/versions/not-an-id/stage", "", actor())
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d", invalid.Code)
	}

	missingMux, missingRepo, _, _ := newStageTestServer(t, version.StageCreated)
	missingRepo.found = false
	missing := call(t, missingMux, http.MethodGet, "/api/versions/999/stage", "", actor())
	if missing.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestStageConfirmCasesAcceptsPartialSelection(t *testing.T) {
	mux, repo, jobs, _ := newStageTestServer(t, version.StageCaseReview)

	recorder := call(t, mux, http.MethodPost, "/api/versions/101/stage/confirm-cases",
		`{"case_ids":["501","502"]}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.state.Stage != version.StageExecuting {
		t.Fatalf("stage = %q, want executing", repo.state.Stage)
	}
	if len(jobs.jobs) != 1 || jobs.jobs[0].Kind != versionapp.StageJobExecuteBatch {
		t.Fatalf("jobs = %#v", jobs.jobs)
	}
	if got := jobs.jobs[0].CaseIDs; len(got) != 2 || got[0] != 501 || got[1] != 502 {
		t.Fatalf("case ids = %#v", got)
	}
	if data := dataBody(t, recorder); data["stage"] != "executing" {
		t.Fatalf("data = %#v", data)
	}

	// An empty case_ids list means all confirmed cases.
	mux, repo, jobs, _ = newStageTestServer(t, version.StageCaseReview)
	recorder = call(t, mux, http.MethodPost, "/api/versions/101/stage/confirm-cases", `{"case_ids":[]}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("all-cases status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if jobs.jobs[0].CaseIDs != nil {
		t.Fatalf("case ids = %#v, want nil for all", jobs.jobs[0].CaseIDs)
	}
	if repo.state.Stage != version.StageExecuting {
		t.Fatalf("stage = %q", repo.state.Stage)
	}
}

func TestStageRejectValidatesAndMovesBack(t *testing.T) {
	mux, repo, _, audit := newStageTestServer(t, version.StageCaseReview)
	recorder := call(t, mux, http.MethodPost, "/api/versions/101/stage/reject", `{"reason":"coverage gap"}`, actor())
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if repo.state.Stage != version.StageDesigning {
		t.Fatalf("stage = %q, want designing", repo.state.Stage)
	}
	if len(audit.events) != 1 || audit.events[0].Action != "version.stage_decision" {
		t.Fatalf("audit = %#v", audit.events)
	}
	if audit.events[0].Metadata["decision"] != "rejected" || audit.events[0].Metadata["reason"] != "coverage gap" {
		t.Fatalf("audit metadata = %#v", audit.events[0].Metadata)
	}

	cases := []struct {
		name  string
		body  string
		gate  version.Stage
		want  int
		field string
	}{
		{"missing reason", `{}`, version.StageCaseReview, 400, "reason"},
		{"blank reason", `{"reason":"   "}`, version.StageCaseReview, 400, "reason"},
		{"malformed body", `{"reason":`, version.StageCaseReview, 400, "body"},
		{"invalid target", `{"reason":"x","target_stage":"executing"}`, version.StageCaseReview, 400, "target_stage"},
		{"target skips a gate", `{"reason":"x","target_stage":"clarifying"}`, version.StageCaseReview, 409, ""},
		{"not a gate", `{"reason":"x"}`, version.StageExecuting, 409, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repo, jobs, _ := newStageTestServer(t, tc.gate)
			recorder := call(t, mux, http.MethodPost, "/api/versions/101/stage/reject", tc.body, actor())
			if recorder.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.field != "" {
				if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
					t.Fatalf("field = %#v, want %q", field, tc.field)
				}
			}
			if len(repo.applied) != 0 || len(jobs.jobs) != 0 {
				t.Fatalf("invalid reject moved the stage: %#v jobs=%#v", repo.applied, jobs.jobs)
			}
		})
	}
}

func TestStageConfirmValidatesIDsAndBody(t *testing.T) {
	cases := []struct {
		name  string
		path  string
		body  string
		field string
	}{
		{"requirements malformed body", "/api/versions/101/stage/confirm-requirements", `{`, "body"},
		{"requirements invalid id", "/api/versions/101/stage/confirm-requirements", `{"requirement_ids":["nope"]}`, "requirement_ids[0]"},
		{"requirements zero id", "/api/versions/101/stage/confirm-requirements", `{"requirement_ids":["0"]}`, "requirement_ids[0]"},
		{"cases invalid id", "/api/versions/101/stage/confirm-cases", `{"case_ids":["nope"]}`, "case_ids[0]"},
		{"invalid version id", "/api/versions/abc/stage/confirm-cases", `{"case_ids":[]}`, "id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, repo, jobs, _ := newStageTestServer(t, version.StageCaseReview)
			recorder := call(t, mux, http.MethodPost, tc.path, tc.body, actor())
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			if field := errorBody(t, recorder)["details"].(map[string]any)["field"]; field != tc.field {
				t.Fatalf("field = %#v, want %q", field, tc.field)
			}
			if len(repo.applied) != 0 || len(jobs.jobs) != 0 {
				t.Fatalf("invalid input moved the stage: %#v", repo.applied)
			}
		})
	}

	// Missing actor is a 401, never a 500.
	mux, _, _, _ := newStageTestServer(t, version.StageRequirementReview)
	if recorder := call(t, mux, http.MethodPost, "/api/versions/101/stage/confirm-requirements", `{}`, nil); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("missing actor status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
