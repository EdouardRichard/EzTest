package versionapi_test

import (
	"net/http"
	"testing"

	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// TestP4ReopenRoutesAreRegistered exercises the local mux registration used by
// the server assembly: RegisterP4 must mount both P4 reopen routes with the
// contract path parameter names, and both handlers must reach application
// services only.
func TestP4ReopenRoutesAreRegistered(t *testing.T) {
	versionStub := &stubService{}
	versionReopen := &stubVersionReopenService{}
	requirementStub := &stubRequirementService{}
	requirementReopen := &stubRequirementReopener{}
	closures := &stubRequirementClosureReader{closures: map[int64]versionapp.RequirementClosureView{}}

	closedAt := testTime
	completed := sampleView()
	completed.Status = "completed"
	completed.ClosedAt = &closedAt
	completed.CloseReason = "delivered"
	running := sampleView()
	running.Status = "running"
	versionStub.getQueue = []versionapp.VersionView{completed, running}

	closed := sampleRequirementDetail()
	closed.Status = "closed"
	closed.ClosedAt = &closedAt
	draft := sampleRequirementDetail()
	draft.Status = "draft"
	draft.ClosedAt = nil
	requirementStub.getQueue = []versionapp.RequirementDetail{closed, draft}
	closures.closures[9001] = versionapp.RequirementClosureView{
		ID: 3001, CloseRound: 1, ClosedAt: testTime, ReopenCount: 1,
	}

	versionHandler := versionapi.New(versionStub, versionapi.WithVersionReopener(versionReopen))
	requirementHandler := versionapi.NewRequirementHandler(requirementStub,
		versionapi.WithRequirementReopener(requirementReopen),
		versionapi.WithRequirementClosures(closures),
	)
	mux := http.NewServeMux()
	versionapi.RegisterP4(mux, versionHandler, requirementHandler)

	versionRecorder := call(t, mux, http.MethodPost, "/api/versions/101/reopen", `{"reason":"manual"}`, actor())
	if versionRecorder.Code != http.StatusOK {
		t.Fatalf("version reopen status = %d, body = %s", versionRecorder.Code, versionRecorder.Body.String())
	}
	if len(versionReopen.calls) != 1 || versionReopen.calls[0].versionID != 101 {
		t.Fatalf("version reopen calls = %#v", versionReopen.calls)
	}
	if data := dataBody(t, versionRecorder); data["status"] != "running" {
		t.Fatalf("version reopen data = %#v", data)
	}

	requirementRecorder := call(t, mux, http.MethodPost, "/api/versions/101/requirements/9001/reopen", `{"reason":"manual"}`, actor())
	if requirementRecorder.Code != http.StatusOK {
		t.Fatalf("requirement reopen status = %d, body = %s", requirementRecorder.Code, requirementRecorder.Body.String())
	}
	if len(requirementReopen.calls) != 1 || requirementReopen.calls[0].requirementID != 9001 {
		t.Fatalf("requirement reopen calls = %#v", requirementReopen.calls)
	}
	requirementData := dataBody(t, requirementRecorder)
	if requirementData["status"] != "draft" || requirementData["closure"] == nil {
		t.Fatalf("requirement reopen data = %#v", requirementData)
	}

	if recorder := call(t, mux, http.MethodGet, "/api/versions/101/reopen", "", actor()); recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET version reopen status = %d", recorder.Code)
	}
}
