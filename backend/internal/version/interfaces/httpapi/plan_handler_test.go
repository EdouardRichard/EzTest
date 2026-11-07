package versionapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	version "github.com/EdouardRichard/EzTest/backend/internal/version/domain"
	versionapi "github.com/EdouardRichard/EzTest/backend/internal/version/interfaces/httpapi"
)

// fakePlanUpdateService records the PATCH arguments and returns a canned plan
// item or typed failure. It stands in for *versionapp.PlanService.
type fakePlanUpdateService struct {
	item      version.PlanItem
	err       error
	calls     int
	versionID int64
	itemID    int64
	patch     versionapp.PlanPatch
	revision  int
}

func (f *fakePlanUpdateService) Update(
	_ context.Context, versionID, itemID int64, patch versionapp.PlanPatch, revision int,
) (version.PlanItem, error) {
	f.calls++
	f.versionID = versionID
	f.itemID = itemID
	f.patch = patch
	f.revision = revision
	if f.err != nil {
		return version.PlanItem{}, f.err
	}
	return f.item, nil
}

var _ versionapi.PlanService = (*fakePlanUpdateService)(nil)

func newPlanUpdateTestServer(t *testing.T, svc versionapi.PlanService) http.Handler {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /api/versions/{id}/plan-items/{itemId}", versionapi.NewPlanHandler(svc).Update)
	return mux
}

func planUpdateCall(t *testing.T, svc versionapi.PlanService, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return call(t, newPlanUpdateTestServer(t, svc), http.MethodPatch, path, body, actor())
}

func storedPlanItem() version.PlanItem {
	workItemID := int64(88)
	return version.PlanItem{
		ID:         9,
		VersionID:  7,
		Phase:      "clarifying",
		Title:      "Clarify requirements",
		Status:     "done",
		Decision:   "approved",
		Order:      3,
		DependsOn:  []int64{4, 5},
		WorkItemID: &workItemID,
		Revision:   3,
	}
}

// TestPlanPatchHandlerUpdates pins the success contract: the handler parses the
// path IDs and the revision-guarded patch, delegates to PlanService.Update and
// renders the stored plan item (including the new revision) in a {data} object.
func TestPlanPatchHandlerUpdates(t *testing.T) {
	svc := &fakePlanUpdateService{item: storedPlanItem()}
	recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9",
		`{"title":"Clarify requirements","status":"done","decision":"approved","order":3,"revision":2}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.calls != 1 || svc.versionID != 7 || svc.itemID != 9 || svc.revision != 2 {
		t.Fatalf("service call = %+v", svc)
	}
	if svc.patch.Title != "Clarify requirements" || svc.patch.Status != "done" ||
		svc.patch.Decision != "approved" || svc.patch.Order == nil || *svc.patch.Order != 3 {
		t.Fatalf("patch = %+v", svc.patch)
	}

	data := dataBody(t, recorder)
	want := map[string]any{
		"id":           "9",
		"version_id":   "7",
		"phase":        "clarifying",
		"title":        "Clarify requirements",
		"status":       "done",
		"decision":     "approved",
		"order":        float64(3),
		"revision":     float64(3),
		"work_item_id": "88",
	}
	for key, value := range want {
		if data[key] != value {
			t.Fatalf("data[%q] = %#v, want %#v (body %s)", key, data[key], value, recorder.Body.String())
		}
	}
	dependsOn, ok := data["depends_on"].([]any)
	if !ok || len(dependsOn) != 2 || dependsOn[0] != "4" || dependsOn[1] != "5" {
		t.Fatalf("depends_on = %#v, want [\"4\",\"5\"]", data["depends_on"])
	}
}

// TestPlanPatchHandlerRequiresActor pins the package-standard actor guard: like
// every sibling mutating handler, the plan write rejects an unauthenticated
// request as 401 before parsing the path/body and before calling the service.
func TestPlanPatchHandlerRequiresActor(t *testing.T) {
	svc := &fakePlanUpdateService{item: storedPlanItem()}
	recorder := call(t, newPlanUpdateTestServer(t, svc), http.MethodPatch,
		"/api/versions/7/plan-items/9", `{"status":"done","revision":1}`, nil)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body = %s", recorder.Code, recorder.Body.String())
	}
	if code := errorBody(t, recorder)["code"]; code != "unauthenticated" {
		t.Fatalf("code = %#v, want unauthenticated (body %s)", code, recorder.Body.String())
	}
	if svc.calls != 0 {
		t.Fatalf("unauthenticated request reached the service: %+v", svc)
	}
}

// TestPlanPatchHandlerOmittedFieldsKeepStoredValues pins the partial-write
// semantics: absent optional fields reach the service as zero values (keep the
// stored value) and an absent order stays a nil pointer.
func TestPlanPatchHandlerOmittedFieldsKeepStoredValues(t *testing.T) {
	svc := &fakePlanUpdateService{item: storedPlanItem()}
	recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", `{"revision":4}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.patch.Title != "" || svc.patch.Status != "" || svc.patch.Decision != "" || svc.patch.Order != nil {
		t.Fatalf("patch = %+v, want zero values and a nil order", svc.patch)
	}
}

// TestPlanPatchHandlerRevisionConflict pins the 409 mapping: a typed service
// conflict becomes a non-retryable conflict envelope carrying the expected and
// stored revisions, never a generic 500.
func TestPlanPatchHandlerRevisionConflict(t *testing.T) {
	svc := &fakePlanUpdateService{err: &versionapp.ConflictError{
		VersionID: 7, ItemID: 9, Expected: 2, Actual: 5,
	}}
	recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", `{"status":"done","revision":2}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", recorder.Code, recorder.Body.String())
	}
	errorData := errorBody(t, recorder)
	if errorData["code"] != "conflict" {
		t.Fatalf("code = %#v, want conflict (body %s)", errorData["code"], recorder.Body.String())
	}
	if errorData["retryable"] != false {
		t.Fatalf("retryable = %#v, want false", errorData["retryable"])
	}
	if message, _ := errorData["message"].(string); !strings.Contains(message, "revision conflict") {
		t.Fatalf("message = %#v, want the revision conflict wording", errorData["message"])
	}
	details, _ := errorData["details"].(map[string]any)
	if details["item_id"] != "9" || details["expected_revision"] != float64(2) ||
		details["actual_revision"] != float64(5) {
		t.Fatalf("details = %#v", details)
	}
}

// TestPlanPatchHandlerConflictReportsUnknownActualRevision pins that the 409
// details always carry actual_revision: when the store only reports the generic
// conflict (Actual == 0 means unknown) the field is 0, never omitted.
func TestPlanPatchHandlerConflictReportsUnknownActualRevision(t *testing.T) {
	svc := &fakePlanUpdateService{err: &versionapp.ConflictError{
		VersionID: 7, ItemID: 9, Expected: 2,
	}}
	recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", `{"status":"done","revision":2}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409, body = %s", recorder.Code, recorder.Body.String())
	}
	details, _ := errorBody(t, recorder)["details"].(map[string]any)
	actual, present := details["actual_revision"]
	if !present {
		t.Fatalf("details = %#v, want actual_revision present", details)
	}
	if actual != float64(0) {
		t.Fatalf("actual_revision = %#v, want 0 when the store did not report it", actual)
	}
}

// TestPlanPatchHandlerRejectsMissingRevision pins that a revision is mandatory
// for every plan write: a missing/non-positive/non-integer revision is a 400
// before the service is called.
func TestPlanPatchHandlerRejectsMissingRevision(t *testing.T) {
	tests := []struct {
		name  string
		body  string
		field string
	}{
		{"missing revision", `{"status":"done"}`, "revision"},
		{"empty object", `{}`, "revision"},
		{"zero revision", `{"revision":0}`, "revision"},
		{"negative revision", `{"revision":-1}`, "revision"},
		{"string revision", `{"revision":"2"}`, "body"},
		{"malformed json", `{"revision":1`, "body"},
		{"empty body", `  `, "body"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			svc := &fakePlanUpdateService{item: storedPlanItem()}
			recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", test.body)
			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
			}
			errorData := errorBody(t, recorder)
			if errorData["code"] != "invalid_argument" {
				t.Fatalf("code = %#v, want invalid_argument", errorData["code"])
			}
			details, _ := errorData["details"].(map[string]any)
			if details["field"] != test.field {
				t.Fatalf("details.field = %#v, want %q (body %s)", details["field"], test.field, recorder.Body.String())
			}
			if svc.calls != 0 {
				t.Fatalf("invalid request reached the service: %+v", svc)
			}
		})
	}
}

// TestPlanPatchHandlerIgnoresUnknownJSONFields keeps decoding consistent with
// the sibling version handlers: unknown body fields are ignored, not rejected.
func TestPlanPatchHandlerIgnoresUnknownJSONFields(t *testing.T) {
	svc := &fakePlanUpdateService{item: storedPlanItem()}
	recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9",
		`{"revision":1,"unexpected_field":"ignored"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body = %s", recorder.Code, recorder.Body.String())
	}
	if svc.calls != 1 {
		t.Fatalf("service calls = %d, want 1", svc.calls)
	}
}

// TestPlanPatchHandlerMapsServiceErrors pins the remaining error mapping:
// platform not-found is 404, platform validation is 400 and invalid path IDs are
// rejected before the service call.
func TestPlanPatchHandlerMapsServiceErrors(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		svc := &fakePlanUpdateService{err: platform.NotFound("plan item")}
		recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", `{"revision":1}`)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404, body = %s", recorder.Code, recorder.Body.String())
		}
		if code := errorBody(t, recorder)["code"]; code != "not_found" {
			t.Fatalf("code = %#v, want not_found", code)
		}
	})
	t.Run("service validation", func(t *testing.T) {
		svc := &fakePlanUpdateService{err: platform.Invalid("order", "must not be negative")}
		recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/9", `{"order":-1,"revision":1}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
		}
		details, _ := errorBody(t, recorder)["details"].(map[string]any)
		if details["field"] != "order" {
			t.Fatalf("details = %#v", details)
		}
	})
	t.Run("invalid version path id", func(t *testing.T) {
		svc := &fakePlanUpdateService{item: storedPlanItem()}
		recorder := planUpdateCall(t, svc, "/api/versions/not-a-number/plan-items/9", `{"revision":1}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
		}
		details, _ := errorBody(t, recorder)["details"].(map[string]any)
		if details["field"] != "id" {
			t.Fatalf("details = %#v", details)
		}
		if svc.calls != 0 {
			t.Fatalf("invalid path reached the service")
		}
	})
	t.Run("invalid item path id", func(t *testing.T) {
		svc := &fakePlanUpdateService{item: storedPlanItem()}
		recorder := planUpdateCall(t, svc, "/api/versions/7/plan-items/not-a-number", `{"revision":1}`)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400, body = %s", recorder.Code, recorder.Body.String())
		}
		details, _ := errorBody(t, recorder)["details"].(map[string]any)
		if details["field"] != "itemId" {
			t.Fatalf("details = %#v", details)
		}
		if svc.calls != 0 {
			t.Fatalf("invalid path reached the service")
		}
	})
}
