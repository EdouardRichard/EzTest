package httpapi

import (
	"errors"
	"net/http"
	"testing"

	"github.com/EdouardRichard/EzTest/backend/internal/platform"
	versionapp "github.com/EdouardRichard/EzTest/backend/internal/version/application"
	"github.com/EdouardRichard/EzTest/backend/internal/version/domain"
)

const bigRequirementID = int64(9007199254740995)

// seedRequirement stores one requirement fixture on the fake service.
func seedRequirement(deps *testDeps, requirement versionapp.Requirement) *versionapp.Requirement {
	if requirement.VersionID == 0 {
		requirement.VersionID = versionDetailID
	}
	if requirement.Priority == "" {
		requirement.Priority = domain.PriorityShould
	}
	if requirement.Status == "" {
		requirement.Status = domain.RequirementDraft
	}
	if requirement.Source == "" {
		requirement.Source = domain.SourceUser
	}
	if requirement.LastUpdateSource == "" {
		requirement.LastUpdateSource = domain.SourceUser
	}
	return deps.requirements.seed(requirement)
}

// TestRequirementRoutesCRUDAndCounts covers create/list/detail/patch, the
// confirm and reopen actions and the requirement error reasons.
func TestRequirementRoutesCRUDAndCounts(t *testing.T) {
	t.Run("create returns 201 with string ids and the user source", func(t *testing.T) {
		handler, deps := newTestServer(t)
		deps.requirements.nextID = bigRequirementID

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/requirements", testUserToken,
			`{"title":"Login","description":"users log in","acceptanceCriteria":"works","priority":"must"}`)
		requireStatus(t, recorder, http.StatusCreated)
		data := decodeDataMap(t, recorder)
		requireJSONString(t, data, "id", "9007199254740995")
		requireJSONString(t, data, "versionId", "100")
		requireStringField(t, data, "title", "Login")
		requireStringField(t, data, "description", "users log in")
		requireStringField(t, data, "acceptanceCriteria", "works")
		requireStringField(t, data, "priority", "must")
		requireStringField(t, data, "status", "draft")
		requireStringField(t, data, "source", "user")
		requireStringField(t, data, "lastUpdateSource", "user")
		requireJSONNumber(t, data, "revision", 1)
		requireJSONNumber(t, data, "createdBy", testUserID)
		requireJSONNumber(t, data, "openQuestionCount", 0)
		requireJSONNumber(t, data, "relationCount", 0)
		requireJSONNumber(t, data, "attachmentCount", 0)
		requireNullField(t, data, "confirmedAt")
		requireNullField(t, data, "confirmedBy")

		if deps.requirements.lastActor != testUserID {
			t.Fatalf("actor=%d want %d", deps.requirements.lastActor, testUserID)
		}
		command := deps.requirements.lastCreate
		if command.Title != "Login" || command.Description != "users log in" ||
			command.AcceptanceCriteria != "works" || command.Priority != "must" {
			t.Fatalf("create command=%+v", command)
		}
		if command.Source != domain.SourceUser {
			t.Fatalf("create source=%q want user", command.Source)
		}
	})

	t.Run("list exposes the aggregate counters without the attachment array", func(t *testing.T) {
		handler, deps := newTestServer(t)
		requirement := seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.requirements.listResult = []versionapp.Requirement{*requirement}
		deps.requirementStats.open[200] = 2
		deps.requirementStats.attachments[200] = 1
		deps.relations.seed(versionapp.Relation{
			ID: 400, VersionID: versionDetailID, FromRequirementID: 200, ToRequirementID: 201,
			RelationType: domain.RelationDependsOn, Source: domain.SourceUser, CreatedBy: testUserID,
		})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements?status=draft&limit=20", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		items, _, _ := decodePage(t, recorder)
		if len(items) != 1 {
			t.Fatalf("items=%d want 1", len(items))
		}
		requireJSONString(t, items[0], "id", "200")
		requireJSONString(t, items[0], "versionId", "100")
		requireJSONNumber(t, items[0], "openQuestionCount", 2)
		requireJSONNumber(t, items[0], "relationCount", 1)
		requireJSONNumber(t, items[0], "attachmentCount", 1)
		requireMissingField(t, items[0], "attachments")
		if deps.requirements.lastListQuery != "draft" {
			t.Fatalf("status filter=%q want draft", deps.requirements.lastListQuery)
		}
	})

	t.Run("detail includes attachment metadata only", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.requirementStats.open[200] = 1
		deps.requirementStats.attachments[200] = 1
		deps.attachments.seed(versionapp.Attachment{
			ID: 300, VersionID: versionDetailID, RequirementID: 200,
			Kind: domain.AttachmentText, Filename: "note.txt", MIMEType: "text/plain",
			SizeBytes: 5, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			StoragePath: "100/300_note.txt", CreatedBy: testUserID,
		}, []byte("hello"))

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireJSONNumber(t, data, "openQuestionCount", 1)
		requireJSONNumber(t, data, "attachmentCount", 1)
		attachments := requireArrayField(t, data, "attachments")
		if len(attachments) != 1 {
			t.Fatalf("attachments=%d want 1", len(attachments))
		}
		attachment := attachments[0].(map[string]any)
		requireJSONString(t, attachment, "id", "300")
		requireStringField(t, attachment, "kind", "text")
		requireStringField(t, attachment, "filename", "note.txt")
		requireStringField(t, attachment, "mimeType", "text/plain")
		requireJSONNumber(t, attachment, "sizeBytes", 5)
		requireStringField(t, attachment, "sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		requireMissingField(t, attachment, "storagePath")
	})

	t.Run("detail exposes an empty attachments array when none exist", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})

		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		attachments := requireArrayField(t, decodeDataMap(t, recorder), "attachments")
		if len(attachments) != 0 {
			t.Fatalf("attachments=%v want []", attachments)
		}
	})

	t.Run("patch applies optional fields under the revision lock", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{
			ID: 200, Title: "Old", Description: "kept", Priority: domain.PriorityShould, Revision: 2,
		})

		recorder := doRequest(handler, http.MethodPatch, "/api/versions/100/requirements/200", testUserToken, `{"revision":1,"title":"Stale"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "revision_conflict")

		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/requirements/200", testUserToken,
			`{"revision":2,"title":"New","priority":"could"}`)
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "title", "New")
		requireStringField(t, data, "description", "kept")
		requireStringField(t, data, "priority", "could")
		requireJSONNumber(t, data, "revision", 3)
		call := deps.requirements.lastUpdate
		if call.Revision != 2 || call.Title == nil || *call.Title != "New" || call.Description != nil ||
			call.Priority == nil || *call.Priority != "could" || call.Source != domain.SourceUser {
			t.Fatalf("update command=%+v", call)
		}
		if deps.requirements.lastActor != testUserID {
			t.Fatalf("update actor=%d want %d", deps.requirements.lastActor, testUserID)
		}

		deps.requirements.requirements[200].Status = domain.RequirementClarified
		recorder = doRequest(handler, http.MethodPatch, "/api/versions/100/requirements/200", testUserToken,
			`{"revision":3,"title":"Too late"}`)
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "requirement_state_conflict")
	})

	t.Run("confirm maps open_questions_remaining and reopen clears the confirmation", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.requirements.confirmErr = domain.NewStateConflict("open_questions_remaining",
			"requirement 200 still has 2 open question(s)")

		recorder := doRequest(handler, http.MethodPost, "/api/versions/100/requirements/200/confirm", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "open_questions_remaining")

		deps.requirements.confirmErr = nil
		recorder = doRequest(handler, http.MethodPost, "/api/versions/100/requirements/200/confirm", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data := decodeDataMap(t, recorder)
		requireStringField(t, data, "status", "clarified")
		requireJSONNumber(t, data, "confirmedBy", testUserID)
		if data["confirmedAt"] == nil {
			t.Fatal("confirmedAt is null after confirm")
		}

		recorder = doRequest(handler, http.MethodPost, "/api/versions/100/requirements/200/reopen", testUserToken, "")
		requireStatus(t, recorder, http.StatusOK)
		data = decodeDataMap(t, recorder)
		requireStringField(t, data, "status", "draft")
		requireNullField(t, data, "confirmedAt")
		requireNullField(t, data, "confirmedBy")
		if deps.requirements.lastActor != testUserID {
			t.Fatalf("action actor=%d want %d", deps.requirements.lastActor, testUserID)
		}
	})

	t.Run("delete requires a draft requirement", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login", Status: domain.RequirementClarified})

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "requirement_state_conflict")

		deps.requirements.requirements[200].Status = domain.RequirementDraft
		recorder = doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireStatus(t, recorder, http.StatusNoContent)
		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements/200", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")
	})

	t.Run("not found and malformed path ids", func(t *testing.T) {
		handler, _ := newTestServer(t)
		recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements/999", testUserToken, "")
		requireError(t, recorder, http.StatusNotFound, platform.CodeNotFound, "not_found")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/not-a-number/requirements", testUserToken, "")
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")

		recorder = doRequest(handler, http.MethodGet, "/api/versions/100/requirements/not-a-number", testUserToken, "")
		requireError(t, recorder, http.StatusUnprocessableEntity, platform.CodeInvalidArgument, "invalid_id")
	})
}

// TestRequirementListLimitContract pins the shared limit contract of spec §5.6
// C6 at the requirements boundary: 100 is accepted, and a non-integer or an
// out-of-range limit keeps the existing 400 invalid_limit mapping but carries
// the distinct message and details{field,min,max}.
func TestRequirementListLimitContract(t *testing.T) {
	handler, deps := newTestServer(t)

	recorder := doRequest(handler, http.MethodGet, "/api/versions/100/requirements?limit=100", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	if got := deps.requirements.lastListPage.Limit; got != platform.MaxLimit {
		t.Fatalf("list limit=%d want %d", got, platform.MaxLimit)
	}

	for _, testCase := range []struct {
		name    string
		limit   string
		message string
	}{
		{"non-integer", "ten", "limit must be an integer"},
		{"too large", "200", "limit must be between 1 and 100"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			recorder := doRequest(handler, http.MethodGet,
				"/api/versions/100/requirements?limit="+testCase.limit, testUserToken, "")
			apiError := requireError(t, recorder, http.StatusBadRequest,
				platform.CodeInvalidArgument, "invalid_limit")
			if apiError.Message != testCase.message {
				t.Fatalf("message=%q want %q", apiError.Message, testCase.message)
			}
			if got := apiError.Details["field"]; got != "limit" {
				t.Fatalf("details.field=%v want limit", got)
			}
			if got := apiError.Details["min"]; got != float64(1) {
				t.Fatalf("details.min=%v want 1", got)
			}
			if got := apiError.Details["max"]; got != float64(platform.MaxLimit) {
				t.Fatalf("details.max=%v want %d", got, platform.MaxLimit)
			}
		})
	}
}

// TestRequirementConfirmGate409 pins INV-3 at the HTTP boundary: a requirement
// with open questions is a 409 open_questions_remaining, and the same call
// succeeds once the questions are closed.
func TestRequirementConfirmGate409(t *testing.T) {
	handler, deps := newTestServer(t)
	seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
	deps.requirements.confirmErr = domain.NewStateConflict("open_questions_remaining",
		"requirement 200 still has open questions")

	recorder := doRequest(handler, http.MethodPost, "/api/versions/100/requirements/200/confirm", testUserToken, "")
	requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "open_questions_remaining")

	deps.requirements.confirmErr = nil
	recorder = doRequest(handler, http.MethodPost, "/api/versions/100/requirements/200/confirm", testUserToken, "")
	requireStatus(t, recorder, http.StatusOK)
	requireStringField(t, decodeDataMap(t, recorder), "status", "clarified")
}

// TestRequirementDeleteCleansUpStoredFilesInOrder pins the cascade contract of
// lead ruling 6: capture the storage paths before the delete, delete the row,
// then remove the files after the committed delete. Cleanup failures are
// log-only; capture failures abort before any delete.
func TestRequirementDeleteCleansUpStoredFilesInOrder(t *testing.T) {
	t.Run("successful delete captures, deletes and removes in order", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.attachments.storagePaths = []string{"100/300_note.txt", "100/301_spec.md"}

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireStatus(t, recorder, http.StatusNoContent)

		order := recordOrder(deps)
		want := []string{"StoragePathsForRequirement", "RequirementDelete", "RemoveStoredFiles"}
		if len(order) != len(want) {
			t.Fatalf("call order=%v want %v", order, want)
		}
		for i := range want {
			if order[i] != want[i] {
				t.Fatalf("call order=%v want %v", order, want)
			}
		}
		if len(deps.attachments.removedPaths) != 2 ||
			deps.attachments.removedPaths[0] != "100/300_note.txt" ||
			deps.attachments.removedPaths[1] != "100/301_spec.md" {
			t.Fatalf("removed paths=%v", deps.attachments.removedPaths)
		}
		if deps.requirements.lastActor != testUserID {
			t.Fatalf("delete actor=%d want %d", deps.requirements.lastActor, testUserID)
		}
	})

	t.Run("failed requirement delete does not remove files", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login", Status: domain.RequirementClarified})
		deps.attachments.storagePaths = []string{"100/300_note.txt"}

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireError(t, recorder, http.StatusConflict, platform.CodeConflict, "requirement_state_conflict")

		order := recordOrder(deps)
		want := []string{"StoragePathsForRequirement", "RequirementDelete"}
		if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
			t.Fatalf("call order=%v want %v", order, want)
		}
		if len(deps.attachments.removedPaths) != 0 {
			t.Fatalf("files were removed after a failed delete: %v", deps.attachments.removedPaths)
		}
	})

	t.Run("file cleanup failure is log-only", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.attachments.storagePaths = []string{"100/300_note.txt"}
		deps.attachments.removeErr = errBoom

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireStatus(t, recorder, http.StatusNoContent)
		if len(deps.attachments.removedPaths) != 1 {
			t.Fatalf("removed paths=%v want the captured path", deps.attachments.removedPaths)
		}
	})

	t.Run("path capture failure aborts before the delete", func(t *testing.T) {
		handler, deps := newTestServer(t)
		seedRequirement(deps, versionapp.Requirement{ID: 200, Title: "Login"})
		deps.attachments.pathsErr = errors.New("attachment metadata unavailable")

		recorder := doRequest(handler, http.MethodDelete, "/api/versions/100/requirements/200", testUserToken, "")
		requireError(t, recorder, http.StatusInternalServerError, platform.CodeInternal, "")

		order := recordOrder(deps)
		if len(order) != 1 || order[0] != "StoragePathsForRequirement" {
			t.Fatalf("call order=%v want only the path capture", order)
		}
	})
}
