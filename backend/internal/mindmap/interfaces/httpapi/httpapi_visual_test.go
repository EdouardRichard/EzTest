package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/application"
	"github.com/EdouardRichard/EzTest/backend/internal/mindmap/domain"
	mindmaphttp "github.com/EdouardRichard/EzTest/backend/internal/mindmap/interfaces/httpapi"
)

// visualFake implements mindmaphttp.VisualAssertionService and records the call.
type visualFake struct {
	updateCalls   int
	updateCmd     application.UpdateVisualAssertionCommand
	updateActor   int64
	updateVersion int64
	updateID      int64
	approveCalls  int
	evidenceID    int64
	approveActor  int64
	result        domain.VisualAssertion
}

func (f *visualFake) Update(_ context.Context, versionID, assertionID, actor int64, cmd application.UpdateVisualAssertionCommand) (domain.VisualAssertion, error) {
	f.updateCalls++
	f.updateVersion, f.updateID, f.updateActor = versionID, assertionID, actor
	f.updateCmd = cmd
	return f.result, nil
}

func (f *visualFake) ApproveBaseline(_ context.Context, versionID, assertionID int64, actor, evidenceID int64) (domain.VisualAssertion, error) {
	f.approveCalls++
	f.updateVersion, f.updateID, f.approveActor, f.evidenceID = versionID, assertionID, actor, evidenceID
	return f.result, nil
}

// visualResult is the stored visual projection the fake returns.
func visualResult() domain.VisualAssertion {
	approved := int64(501)
	approver := int64(1)
	at := time.Date(2026, 10, 29, 9, 0, 0, 0, time.UTC)
	return domain.VisualAssertion{
		AssertionID: 77, Selector: "#chart",
		Region:    &domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3},
		Threshold: 0.02, BaselineEvidenceID: &approved,
		ViewportWidth: 1440, ViewportHeight: 900, DeviceScaleFactor: 2,
		ApprovedBy: &approver, ApprovedAt: &at, Revision: 2,
	}
}

func TestUpdateVisualAssertionPersistsParams(t *testing.T) {
	fake := &visualFake{result: visualResult()}
	handler := newServer(t, mindmaphttp.Deps{Visual: fake})

	body := `{"nodeRevision":3,"selector":"#chart","region":{"x":0.1,"y":0.2,"width":0.5,"height":0.3},` +
		`"threshold":0.02,"viewport":{"width":1440,"height":900},"deviceScaleFactor":2}`
	rec := call(t, handler, "PUT", "/api/versions/7/mindmap/assertions/77/visual", tokenUser, body)
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body)
	}
	if fake.updateCalls != 1 || fake.updateVersion != 7 || fake.updateID != 77 || fake.updateActor != regularUser.ID {
		t.Fatalf("update call = %d version=%d assertion=%d actor=%d", fake.updateCalls, fake.updateVersion, fake.updateID, fake.updateActor)
	}
	cmd := fake.updateCmd
	if cmd.NodeRevision != 3 || cmd.Selector != "#chart" || cmd.Threshold != 0.02 ||
		cmd.ViewportWidth != 1440 || cmd.ViewportHeight != 900 || cmd.DeviceScaleFactor != 2 {
		t.Fatalf("command = %+v", cmd)
	}
	if cmd.Region == nil || *cmd.Region != (domain.VisualRegion{X: 0.1, Y: 0.2, Width: 0.5, Height: 0.3}) {
		t.Fatalf("region = %+v", cmd.Region)
	}
	data := decodeData(t, rec)
	if data["selector"] != "#chart" || data["threshold"] != 0.02 ||
		data["baselineEvidenceId"] != "501" || data["viewportWidth"] != float64(1440) {
		t.Fatalf("response data = %+v", data)
	}
	region, ok := data["region"].(map[string]any)
	if !ok || region["width"] != 0.5 || region["height"] != 0.3 {
		t.Fatalf("response region = %+v", data["region"])
	}
}

func TestApproveVisualBaselineRequiresAdmin(t *testing.T) {
	fake := &visualFake{result: visualResult()}
	handler := newServer(t, mindmaphttp.Deps{Visual: fake})

	rec := call(t, handler, "POST", "/api/versions/7/mindmap/assertions/77/visual/baseline", tokenUser, `{"evidenceId":"501"}`)
	if rec.Code != 403 {
		t.Fatalf("status = %d body=%s, want 403", rec.Code, rec.Body)
	}
	if reason := reasonOf(t, rec); reason != "forbidden" {
		t.Fatalf("reason = %q, want forbidden", reason)
	}
	if fake.approveCalls != 0 {
		t.Fatalf("a non-admin approval reached the service")
	}
}

func TestApproveVisualBaselinePersistsEvidence(t *testing.T) {
	fake := &visualFake{result: visualResult()}
	handler := newServer(t, mindmaphttp.Deps{Visual: fake})

	rec := call(t, handler, "POST", "/api/versions/7/mindmap/assertions/77/visual/baseline", tokenAdmin, `{"evidenceId":"501"}`)
	if rec.Code != 200 {
		t.Fatalf("status = %d body=%s, want 200", rec.Code, rec.Body)
	}
	if fake.approveCalls != 1 || fake.evidenceID != 501 || fake.approveActor != adminUser.ID ||
		fake.updateVersion != 7 || fake.updateID != 77 {
		t.Fatalf("approve call = %d evidence=%d actor=%d version=%d assertion=%d",
			fake.approveCalls, fake.evidenceID, fake.approveActor, fake.updateVersion, fake.updateID)
	}
	data := decodeData(t, rec)
	if data["baselineEvidenceId"] != "501" || data["approvedBy"] != float64(1) {
		t.Fatalf("response data = %+v", data)
	}
}

func TestApproveVisualBaselineRejectsInvalidEvidenceID(t *testing.T) {
	fake := &visualFake{result: visualResult()}
	handler := newServer(t, mindmaphttp.Deps{Visual: fake})

	for _, body := range []string{`{"evidenceId":""}`, `{"evidenceId":"0"}`, `{"evidenceId":"abc"}`} {
		rec := call(t, handler, "POST", "/api/versions/7/mindmap/assertions/77/visual/baseline", tokenAdmin, body)
		if rec.Code != 400 && rec.Code != 422 {
			t.Fatalf("body=%s status = %d, want 400 or 422 (body=%s)", body, rec.Code, rec.Body)
		}
	}
	if fake.approveCalls != 0 {
		t.Fatalf("invalid evidence ids reached the service")
	}
}
